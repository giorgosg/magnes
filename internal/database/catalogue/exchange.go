package catalogue

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/jackc/pgx/v5"
)

// Beginner starts the export's snapshot transaction. *pgx.Conn and *pgxpool.Pool both
// satisfy it.
type Beginner interface {
	BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error)
}

// DefaultExchangePageSize is how many torrents one keyset page reads.
const DefaultExchangePageSize = 1000

// ExchangeOptions controls an exchange-file export.
type ExchangeOptions struct {
	// Producer identifies this program in the header.
	Producer exchange.Producer
	// IncludeTags writes each torrent's local tags as magnes.tags.
	IncludeTags bool
	// PageSize bounds how many torrents are held in memory at once. Zero means
	// DefaultExchangePageSize.
	PageSize int
}

// ExportExchange writes the visible catalogue as an exchange file: one header, then one
// record per torrent in infohash order. Pending and banned torrents are never written.
// It reads one read-only repeatable-read snapshot in keyset pages, so memory stays flat
// however large the catalogue is. The caller must discard partial output on error.
func ExportExchange(ctx context.Context, db Beginner, output io.Writer, options ExchangeOptions) error {
	pageSize := options.PageSize
	if pageSize <= 0 {
		pageSize = DefaultExchangePageSize
	}

	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("start exchange export: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The first statement takes the snapshot, so exportedAt is never later than it.
	var exportedAt time.Time
	if err = tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&exportedAt); err != nil {
		return fmt.Errorf("start exchange export: %w", err)
	}

	buffered := bufio.NewWriter(output)
	writer := exchange.NewWriter(buffered)

	if err = writer.WriteHeader(exchange.Header{
		Producer:   model.MaybeValid(options.Producer),
		ExportedAt: model.MaybeValid(exportedAt),
	}); err != nil {
		return err
	}

	reader := exchangeReader{tx: tx, options: options}

	for after := []byte{}; ; {
		records, err := reader.readPage(ctx, after, pageSize)
		if err != nil {
			return err
		}

		for _, record := range records {
			if err = writer.WriteRecord(record); err != nil {
				return fmt.Errorf("export torrent %w", err)
			}
		}

		if len(records) < pageSize {
			break
		}

		after = records[len(records)-1].InfoHash.Bytes()
	}

	if err = buffered.Flush(); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// exchangeReader reads keyset pages of visible torrents, and everything their records are
// built from, inside the export's snapshot.
type exchangeReader struct {
	tx      pgx.Tx
	options ExchangeOptions
}

// exchangePage is one page's records by infohash, for the readers that fill them in.
type exchangePage struct {
	tx      pgx.Tx
	records map[protocol.ID]*exchange.Record
	hashes  [][]byte
}

func (r exchangeReader) readPage(ctx context.Context, after []byte, limit int) ([]exchange.Record, error) {
	records, listed, err := r.readTorrents(ctx, after, limit)
	if err != nil || len(records) == 0 {
		return records, err
	}

	page := exchangePage{
		tx:      r.tx,
		records: make(map[protocol.ID]*exchange.Record, len(records)),
		hashes:  make([][]byte, len(records)),
	}

	for i := range records {
		page.records[records[i].InfoHash] = &records[i]
		page.hashes[i] = records[i].InfoHash.Bytes()
	}

	if err = page.readFiles(ctx, listed); err != nil {
		return nil, err
	}

	if err = page.readSources(ctx); err != nil {
		return nil, err
	}

	if err = page.readClassifications(ctx); err != nil {
		return nil, err
	}

	if r.options.IncludeTags {
		if err = page.readTags(ctx); err != nil {
			return nil, err
		}
	}

	return records, nil
}

// readTorrents reads the page's torrents, and lists those whose files are read from
// torrent_files.
func (r exchangeReader) readTorrents(
	ctx context.Context, after []byte, limit int,
) (records []exchange.Record, listed [][]byte, err error) {
	rows, err := r.tx.Query(ctx, `SELECT info_hash, name, size, private, files_status::text FROM torrents
 WHERE info_hash > $1 AND `+VisibleInfoHash("torrents.info_hash")+`
 ORDER BY info_hash LIMIT $2`, after, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("read torrents: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			hash        []byte
			record      exchange.Record
			size        int64
			private     bool
			filesStatus string
		)

		if err = rows.Scan(&hash, &record.Name, &size, &private, &filesStatus); err != nil {
			return nil, nil, fmt.Errorf("read torrents: %w", err)
		}

		if record.InfoHash, err = protocol.NewIDFromByteSlice(hash); err != nil {
			return nil, nil, fmt.Errorf("read torrents: %w", err)
		}

		record.Size = model.MaybeValid(uint64(size)) //nolint:gosec // sizes are never negative
		record.Private = model.MaybeValid(private)

		var isListed bool

		if isListed, err = applyFilesStatus(&record, model.FilesStatus(filesStatus)); err != nil {
			return nil, nil, fmt.Errorf("export torrent %s: %w", record.InfoHash, err)
		}

		if isListed {
			listed = append(listed, hash)
		}

		records = append(records, record)
	}

	if err = rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read torrents: %w", err)
	}

	return records, listed, nil
}

// applyFilesStatus decides what the record says about its file list, and reports
// whether the list is read from torrent_files. A single-file torrent has one entry built
// from the torrent itself; a multi-file list and a partial one are read afterwards, into
// the empty list started here.
func applyFilesStatus(record *exchange.Record, status model.FilesStatus) (listed bool, err error) {
	switch status {
	case model.FilesStatusNoInfo:
		return false, nil
	case model.FilesStatusSingle:
		record.Files = model.MaybeValid([]exchange.File{{Index: 0, Path: record.Name, Size: record.Size.Val}})
		return false, nil
	case model.FilesStatusMulti:
		record.Files = model.MaybeValid([]exchange.File{})
		return true, nil
	case model.FilesStatusOverThreshold:
		record.Files = model.MaybeValid([]exchange.File{})
		record.FilesComplete = model.MaybeValid(false)

		return true, nil
	default:
		return false, fmt.Errorf("%w: files_status %q", exchange.ErrUnmappedValue, string(status))
	}
}

// readFiles reads the stored file lists of the listed torrents, in index order.
func (p exchangePage) readFiles(ctx context.Context, listed [][]byte) error {
	if len(listed) == 0 {
		return nil
	}

	rows, err := p.tx.Query(ctx, `SELECT info_hash, index, path, size FROM torrent_files
 WHERE info_hash = ANY($1) ORDER BY info_hash, index`, listed)
	if err != nil {
		return fmt.Errorf("read files: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			hash      []byte
			fileIndex int64
			path      string
			size      int64
		)

		if err = rows.Scan(&hash, &fileIndex, &path, &size); err != nil {
			return fmt.Errorf("read files: %w", err)
		}

		record := p.records[protocol.ID(hash)]
		record.Files.Val = append(record.Files.Val, exchange.File{
			Index: uint(fileIndex), //nolint:gosec // file indexes are never negative
			Path:  path,
			Size:  uint64(size), //nolint:gosec // sizes are never negative
		})
	}

	if err = rows.Err(); err != nil {
		return fmt.Errorf("read files: %w", err)
	}

	return nil
}

// readSources writes one entry per stored source, under its normalised name, and dates
// the record by the earliest publication any source knows of. An unknown peer count
// stays absent rather than becoming 0, and scraped_at travels as the scrapedAt of the
// counts it dates. Rows arrive most recently scraped first, then most recently updated,
// which mergeSource relies on.
func (p exchangePage) readSources(ctx context.Context) error {
	rows, err := p.tx.Query(ctx, `SELECT info_hash, source, seeders, leechers, published_at, scraped_at
 FROM torrents_torrent_sources WHERE info_hash = ANY($1)
 ORDER BY info_hash, scraped_at DESC NULLS LAST, updated_at DESC, source`, p.hashes)
	if err != nil {
		return fmt.Errorf("read sources: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			hash                   []byte
			stored                 string
			seeders, leechers      *int64
			publishedAt, scrapedAt *time.Time
		)

		if err = rows.Scan(&hash, &stored, &seeders, &leechers, &publishedAt, &scrapedAt); err != nil {
			return fmt.Errorf("read sources: %w", err)
		}

		record := p.records[protocol.ID(hash)]

		name, err := exchange.NormalizeSourceName(stored)
		if err != nil {
			return fmt.Errorf("export torrent %s: %w", record.InfoHash, err)
		}

		source := exchange.Source{Name: name, Seeders: peerCount(seeders), Leechers: peerCount(leechers)}

		if scrapedAt != nil {
			source.ScrapedAt = model.MaybeValid(scrapedAt.UTC())
		}

		if publishedAt != nil {
			source.PublishedAt = model.MaybeValid(publishedAt.UTC())
			record.PublishedAt = earliest(record.PublishedAt, source.PublishedAt)
		}

		record.Sources.Valid = true
		record.Sources.Val = mergeSource(record.Sources.Val, source)
	}

	if err = rows.Err(); err != nil {
		return fmt.Errorf("read sources: %w", err)
	}

	for _, record := range p.records {
		slices.SortFunc(record.Sources.Val, func(a, b exchange.Source) int {
			return strings.Compare(a.Name, b.Name)
		})
	}

	return nil
}

// mergeSource adds a source row to a record's entries. Two stored names can normalise to
// one, such as "Torrents CSV" and "torrents-csv", and a file has one entry per source.
// The merge follows the import's rules: the earliest publication wins, a known count is
// never replaced by an unknown one, and otherwise the newer observation wins. Seeders,
// leechers and the scrapedAt dating them were observed together, so they travel as one
// from the first row that has a count.
func mergeSource(entries []exchange.Source, source exchange.Source) []exchange.Source {
	i := slices.IndexFunc(entries, func(e exchange.Source) bool { return e.Name == source.Name })
	if i < 0 {
		return append(entries, source)
	}

	merged := &entries[i]

	if !merged.Seeders.Valid && !merged.Leechers.Valid {
		merged.Seeders, merged.Leechers = source.Seeders, source.Leechers
		merged.ScrapedAt = source.ScrapedAt
	}

	merged.PublishedAt = earliest(merged.PublishedAt, source.PublishedAt)

	return entries
}

// earliest returns the earlier of two times, where an unknown time never wins.
func earliest(a, b model.Maybe[time.Time]) model.Maybe[time.Time] {
	if !a.Valid || (b.Valid && b.Val.Before(a.Val)) {
		return b
	}

	return a
}

func peerCount(v *int64) model.Maybe[uint] {
	if v == nil {
		return model.Maybe[uint]{}
	}

	return model.MaybeValid(uint(*v)) //nolint:gosec // peer counts are never negative
}

// readClassifications writes each torrent's current classification. The classifier and
// enhancement both delete a torrent's other torrent_contents rows when they write one
// (internal/processor/persist.go, internal/enhancement/application.go), so a torrent has
// at most one row in practice. The schema allows several, though, so the most recently
// written row is taken as current, with the row id breaking ties.
//
// Only release attributes and the work identity travel. The work's own title, year and
// other catalogue metadata stay behind: the receiver re-fetches them by identity.
func (p exchangePage) readClassifications(ctx context.Context) error {
	rows, err := p.tx.Query(ctx, `SELECT DISTINCT ON (info_hash) info_hash, content_type, content_source,
 content_id, inferred_title, inferred_year, languages, episodes, video_resolution, video_source,
 video_codec, video_3d, video_modifier, release_group
 FROM torrent_contents WHERE info_hash = ANY($1) ORDER BY info_hash, updated_at DESC, id`, p.hashes)
	if err != nil {
		return fmt.Errorf("read classifications: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			hash                                   []byte
			contentType, workSource, workID        *string
			inferredTitle                          string
			inferredYear                           *int32
			languages, episodes                    []byte
			resolution, videoSource, codec, threeD *string
			modifier, releaseGroup                 *string
		)

		if err = rows.Scan(
			&hash,
			&contentType,
			&workSource,
			&workID,
			&inferredTitle,
			&inferredYear,
			&languages,
			&episodes,
			&resolution,
			&videoSource,
			&codec,
			&threeD,
			&modifier,
			&releaseGroup,
		); err != nil {
			return fmt.Errorf("read classifications: %w", err)
		}

		record := p.records[protocol.ID(hash)]

		c := exchange.Classification{
			ContentType:   known[model.ContentType](contentType),
			InferredTitle: known[string](&inferredTitle),
			ReleaseGroup:  known[string](releaseGroup),
			Video: exchange.Video{
				Resolution: known[model.VideoResolution](resolution),
				Source:     known[model.VideoSource](videoSource),
				Codec:      known[model.VideoCodec](codec),
				ThreeD:     known[model.Video3D](threeD),
				Modifier:   known[model.VideoModifier](modifier),
			},
		}

		if workSource != nil && workID != nil {
			c.Work = model.MaybeValid(exchange.Work{Source: *workSource, ID: *workID})
		}

		if inferredYear != nil && *inferredYear > 0 {
			c.InferredYear = model.MaybeValid(model.Year(*inferredYear)) //nolint:gosec // checked positive
		}

		if c.Languages, err = decodeLanguages(languages); err != nil {
			return fmt.Errorf("export torrent %s: languages: %w", record.InfoHash, err)
		}

		if c.Episodes, err = decodeEpisodes(episodes); err != nil {
			return fmt.Errorf("export torrent %s: episodes: %w", record.InfoHash, err)
		}

		if !reflect.ValueOf(c).IsZero() {
			record.Magnes.Classification = model.MaybeValid(c)
		}
	}

	if err = rows.Err(); err != nil {
		return fmt.Errorf("read classifications: %w", err)
	}

	return nil
}

// known reads a nullable column as a field: NULL and the empty string are both unknown.
// Enum values are carried as stored, so the writer refuses one it cannot spell.
func known[T ~string](v *string) model.Maybe[T] {
	if v == nil || *v == "" {
		return model.Maybe[T]{}
	}

	return model.MaybeValid(T(*v))
}

// decodeLanguages reads the stored JSON array. An empty list is unknown: the classifier
// writes one whenever it found no language.
func decodeLanguages(raw []byte) (model.Maybe[[]model.Language], error) {
	var codes []string
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &codes); err != nil {
			return model.Maybe[[]model.Language]{}, err
		}
	}

	if len(codes) == 0 {
		return model.Maybe[[]model.Language]{}, nil
	}

	languages := make([]model.Language, len(codes))
	for i, code := range codes {
		languages[i] = model.Language(code)
	}

	return model.MaybeValid(languages), nil
}

// decodeEpisodes reads the stored season map: a season with no episodes is the whole
// season. An empty map is unknown.
func decodeEpisodes(raw []byte) (model.Maybe[[]exchange.SeasonEpisodes], error) {
	var episodes model.Episodes
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &episodes); err != nil {
			return model.Maybe[[]exchange.SeasonEpisodes]{}, err
		}
	}

	if len(episodes) == 0 {
		return model.Maybe[[]exchange.SeasonEpisodes]{}, nil
	}

	seasons := episodes.SeasonEntries()
	entries := make([]exchange.SeasonEpisodes, len(seasons))

	for i, season := range seasons {
		entries[i] = exchange.SeasonEpisodes{
			Season:      season.Season,
			Episodes:    season.Episodes,
			WholeSeason: len(season.Episodes) == 0,
		}
	}

	return model.MaybeValid(entries), nil
}

// readTags writes each torrent's local tags. They record the operator's own curation,
// so they travel only when the export asks for them.
func (p exchangePage) readTags(ctx context.Context) error {
	rows, err := p.tx.Query(ctx, `SELECT info_hash, name FROM torrent_tags
 WHERE info_hash = ANY($1) ORDER BY info_hash, name`, p.hashes)
	if err != nil {
		return fmt.Errorf("read tags: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			hash []byte
			name string
		)

		if err = rows.Scan(&hash, &name); err != nil {
			return fmt.Errorf("read tags: %w", err)
		}

		tags := &p.records[protocol.ID(hash)].Magnes.Tags
		tags.Valid = true
		tags.Val = append(tags.Val, name)
	}

	if err = rows.Err(); err != nil {
		return fmt.Errorf("read tags: %w", err)
	}

	return nil
}
