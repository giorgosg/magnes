package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/moderation"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"gorm.io/gorm"
)

// ErrInvalidOrigin refuses an origin label that is not one lowercase kebab-case segment.
var ErrInvalidOrigin = errors.New("invalid origin label")

// DefaultExchangeBatchSize is how many records one transaction writes. Each batch holds
// the moderation locks of its infohashes until it commits, and the crawler waits on
// those locks, so a batch is kept small.
const DefaultExchangeBatchSize = 100

// ExchangeConfig is how this instance stores what it imports.
type ExchangeConfig struct {
	// FilesThreshold is the crawler's SaveFilesThreshold: no more file rows than this are
	// stored for one torrent, and a longer list is stored as partial.
	FilesThreshold uint
	// BatchSize is how many records one transaction writes. Zero means
	// DefaultExchangeBatchSize.
	BatchSize int
}

// ExchangeOptions are the choices one import makes.
type ExchangeOptions struct {
	// Origin labels where the file came from. Every incoming source is stored as
	// <origin>:<source>.
	Origin string
	// OverwriteHints replaces an existing hint with the incoming classification. Without
	// it, an existing hint wins.
	OverwriteHints bool
	// Limits bound what the import holds in memory. Zero is no limit.
	Limits exchange.Limits
}

// ExchangeImporter reads exchange files into the catalogue under the merge rules in
// docs/exchange-file.md. It is the one import code path: the CLI import command and
// POST /import both call it.
type ExchangeImporter struct {
	db            *gorm.DB
	keywordPolicy *moderation.KeywordPolicy
	config        ExchangeConfig
}

func NewExchangeImporter(
	db *gorm.DB, keywordPolicy *moderation.KeywordPolicy, config ExchangeConfig,
) *ExchangeImporter {
	if config.BatchSize <= 0 {
		config.BatchSize = DefaultExchangeBatchSize
	}

	return &ExchangeImporter{db: db, keywordPolicy: keywordPolicy, config: config}
}

// ExchangeReport counts what an import did. The torrent, hint and tag counts cover only
// batches that committed: a batch interrupted by cancellation or an error is rolled back
// whole. Records, Malformed and Dropped describe everything read, written or not.
type ExchangeReport struct {
	Headers []exchange.CheckedHeader
	// Records is how many records were handed over to be written.
	Records int
	// New, Updated and Unchanged count written torrents by what the import did to them.
	New, Updated, Unchanged int
	// Banned counts records refused by moderation.
	Banned int
	// Skipped counts records that could not be written for another reason: a value too
	// large for its column, or a torrent deleted between the batch's insert and its lock.
	Skipped int
	// HintsWritten counts hints created or replaced. HintsKept counts incoming
	// classifications that lost to an existing hint.
	HintsWritten, HintsKept int
	// TagsAdded counts tags added to torrents that did not have them.
	TagsAdded int
	// Malformed lists skipped lines.
	Malformed []exchange.MalformedLineError
	// Dropped is what the reader dropped, and what the import itself dropped: invalid
	// tags, classifications with no content type, unknown work sources and values out of
	// range.
	Dropped []exchange.ReportEntry
}

// Reasons the import itself drops something. The reader's reasons are in package
// exchange.
const (
	// ReasonInvalidTag is a magnes tag that is not a valid local tag name.
	ReasonInvalidTag exchange.Reason = "invalid-tag"
	// ReasonNoContentType is a classification with no contentType. It becomes no hint,
	// because a hint needs a content type.
	ReasonNoContentType exchange.Reason = "no-content-type"
	// ReasonUnknownWorkSource is a work identity from a provider this instance does not
	// know. The hint is written without the identity.
	ReasonUnknownWorkSource exchange.Reason = "unknown-work-source"
	// ReasonOutOfRange is a value too large for its column. The record is skipped.
	ReasonOutOfRange exchange.Reason = "out-of-range"
)

// readBatch is what the reading goroutine hands over: records to write in one
// transaction, and what was read alongside them.
type readBatch struct {
	records   []exchange.Record
	headers   []exchange.CheckedHeader
	malformed []exchange.MalformedLineError
	// dropped is the reader's whole report so far, so it covers every record handed over.
	dropped []exchange.ReportEntry
	// last is set on the final batch, with err set when the stream ended on a fatal error.
	last bool
	err  error
}

// Import reads src and writes it in batches, each in its own transaction. The origin is
// checked before anything is read.
//
// Cancelling ctx stops the import promptly: the batch being written is rolled back, and
// the report covers the batches already committed. Reading happens on a goroutine of its
// own, so an import waiting on its input still returns. That goroutine ends at its next
// hand-over, or when its pending read returns, which for a request body is when the
// request ends.
//
// The returned report is complete even when the error is not nil.
func (i *ExchangeImporter) Import(
	ctx context.Context, src io.Reader, options ExchangeOptions,
) (ExchangeReport, error) {
	if !exchange.ValidOriginLabel(options.Origin) {
		return ExchangeReport{}, fmt.Errorf("%w: %q", ErrInvalidOrigin, options.Origin)
	}

	batches := make(chan readBatch)
	done := make(chan struct{})

	defer close(done)

	go readBatches(exchange.NewLimitedReader(src, options.Limits), i.config.BatchSize, batches, done)

	var (
		report  ExchangeReport
		dropped []exchange.ReportEntry
		ownDrop = newDrops(options.Limits)
	)

	finish := func(err error) (ExchangeReport, error) {
		report.Dropped = mergeDrops(dropped, ownDrop)
		return report, err
	}

	for {
		if err := ctx.Err(); err != nil {
			return finish(err)
		}

		var batch readBatch

		select {
		case <-ctx.Done():
			return finish(ctx.Err())
		case batch = <-batches:
		}

		report.Headers = append(report.Headers, batch.headers...)
		report.Malformed = append(report.Malformed, batch.malformed...)
		report.Records += len(batch.records)
		dropped = batch.dropped

		if len(batch.records) > 0 {
			written, err := i.writeBatch(ctx, batch.records, options, ownDrop)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return finish(ctxErr)
				}

				return finish(err)
			}

			report.add(written)

			if err := ownDrop.checkLimit(len(report.Headers) + len(report.Malformed) + len(dropped)); err != nil {
				return finish(err)
			}
		}

		if batch.last {
			return finish(batch.err)
		}
	}
}

// readBatches cuts the stream into batches. A batch never holds one infohash twice, so a
// repeated record is written after the earlier one, as if the file were read one record
// at a time. It stops at the end of the stream, on a fatal error, or when done closes.
func readBatches(reader *exchange.Reader, size int, out chan<- readBatch, done <-chan struct{}) {
	var batch readBatch

	seen := make(map[protocol.ID]struct{}, size)

	send := func(last bool, err error) bool {
		batch.dropped, batch.last, batch.err = reader.Report(), last, err

		select {
		case out <- batch:
			batch = readBatch{}

			clear(seen)

			return true
		case <-done:
			return false
		}
	}

	for {
		line, err := reader.Next()
		if errors.Is(err, io.EOF) {
			send(true, nil)
			return
		}

		var malformed *exchange.MalformedLineError
		if errors.As(err, &malformed) {
			batch.malformed = append(batch.malformed, *malformed)
			continue
		}

		if err != nil {
			send(true, err)
			return
		}

		if line.Header != nil {
			batch.headers = append(
				batch.headers,
				exchange.CheckedHeader{Line: line.Number, Header: *line.Header},
			)

			continue
		}

		if _, repeated := seen[line.Record.InfoHash]; repeated && !send(false, nil) {
			return
		}

		batch.records = append(batch.records, *line.Record)
		seen[line.Record.InfoHash] = struct{}{}

		if len(batch.records) >= size && !send(false, nil) {
			return
		}
	}
}

// batchResult is what one committed batch did.
type batchResult struct {
	new, updated, unchanged, banned, skipped int
	hintsWritten, hintsKept, tagsAdded       int
}

func (r *ExchangeReport) add(b batchResult) {
	r.New += b.new
	r.Updated += b.updated
	r.Unchanged += b.unchanged
	r.Banned += b.banned
	r.Skipped += b.skipped
	r.HintsWritten += b.hintsWritten
	r.HintsKept += b.hintsKept
	r.TagsAdded += b.tagsAdded
}

// dropKey is one line of the import's own drop report.
type dropKey struct {
	reason exchange.Reason
	path   string
	value  string
}

// drops is the import's own drop report, kept within the import's limits.
type drops struct {
	counts map[dropKey]int
	limits exchange.Limits
}

func newDrops(limits exchange.Limits) drops {
	return drops{counts: make(map[dropKey]int), limits: limits}
}

func (d drops) add(reason exchange.Reason, path, value string) {
	d.counts[dropKey{reason: reason, path: path, value: d.limits.Clip(value)}]++
}

// checkLimit stops an import whose report, with the reader's reported entries, would hold
// more than the limit allows. The reader enforces the limit on its own entries.
func (d drops) checkLimit(reported int) error {
	if d.limits.MaxReported > 0 && reported+len(d.counts) > d.limits.MaxReported {
		return fmt.Errorf("%w: more than %d headers, malformed lines and distinct dropped entries",
			exchange.ErrTooMuchReported, d.limits.MaxReported)
	}

	return nil
}

// mergeDrops appends the import's own drops to the reader's, in the same stable order.
func mergeDrops(read []exchange.ReportEntry, own drops) []exchange.ReportEntry {
	entries := append([]exchange.ReportEntry(nil), read...)
	for key, n := range own.counts {
		entries = append(entries, exchange.ReportEntry{
			Scope:  exchange.ScopeRecord,
			Reason: key.reason,
			Path:   key.path,
			Value:  key.value,
			Count:  n,
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}

		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}

		if a.Path != b.Path {
			return a.Path < b.Path
		}

		return a.Value < b.Value
	})

	return entries
}
