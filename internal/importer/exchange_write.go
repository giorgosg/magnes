package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/database/dao"
	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/moderation"
	"github.com/bitmagnet-io/bitmagnet/internal/processor"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// recordPlan is one record translated into the rows it may write. Which of them are
// written is decided in SQL, against what the catalogue holds when the batch commits.
type recordPlan struct {
	hash    protocol.ID
	name    string
	size    int64
	private bool
	// filesStatus and filesCount describe the incoming file list as this instance would
	// store it, and files holds the rows it would store, already cut to the threshold.
	filesStatus model.FilesStatus
	filesCount  *int32
	files       []exchange.File
	// completeFiles is set when the incoming list is complete, the only case in which it
	// may fill an existing torrent's unknown or partial list.
	completeFiles bool
	sources       []sourcePlan
	hint          *hintPlan
	tags          []string
	evidence      moderation.Evidence
}

// hintPlan is the torrent_hints row a classification becomes. Absent values are nil.
type hintPlan struct {
	contentType                     string
	contentSource, contentID, title *string
	releaseYear                     *int32
	languages, episodes             *string
	resolution, source, codec       *string
	threeD, modifier, releaseGroup  *string
}

// workSources are the catalogue providers a work identity can name. torrent_hints
// references metadata_sources, which holds exactly these.
var workSources = map[string]bool{model.SourceTmdb: true, model.SourceImdb: true, model.SourceTvdb: true}

type sourcePlan struct {
	name                   string
	seeders, leechers      *int32
	publishedAt, scrapedAt *time.Time
}

// plan translates one record, or reports why it cannot be stored.
func (i *ExchangeImporter) plan(record exchange.Record, options ExchangeOptions, d drops) (recordPlan, bool) {
	p := recordPlan{hash: record.InfoHash, name: record.Name, private: record.Private.Val}

	if record.Size.Valid {
		if record.Size.Val > math.MaxInt64 {
			d.add(ReasonOutOfRange, "size", "")
			return p, false
		}

		p.size = int64(record.Size.Val)
	}

	for _, source := range record.Sources.Val {
		s := sourcePlan{name: exchange.PrefixSourceName(options.Origin, source.Name)}

		var ok bool
		if s.seeders, ok = peerCount(source.Seeders); !ok {
			d.add(ReasonOutOfRange, "sources[].seeders", "")
			return p, false
		}

		if s.leechers, ok = peerCount(source.Leechers); !ok {
			d.add(ReasonOutOfRange, "sources[].leechers", "")
			return p, false
		}

		s.publishedAt = source.PublishedAt.Addr()
		s.scrapedAt = source.ScrapedAt.Addr()
		p.sources = append(p.sources, s)
	}

	p.filesStatus = model.FilesStatusNoInfo
	p.evidence = moderation.Evidence{InfoHash: &p.hash, Name: p.name}

	if record.Files.Valid && len(record.Files.Val) > 0 {
		if path, ok := i.planFiles(&p, record); !ok {
			d.add(ReasonOutOfRange, path, "")
			return p, false
		}
	}

	if record.Magnes.Classification.Valid {
		p.hint = planHint(record.Magnes.Classification.Val, d)
	}

	for _, tag := range record.Magnes.Tags.Val {
		if model.ValidateTagName(tag) != nil {
			d.add(ReasonInvalidTag, "magnes.tags[]", tag)
		} else if !slices.Contains(p.tags, tag) {
			p.tags = append(p.tags, tag)
		}
	}

	return p, true
}

// planHint maps a classification onto the hint it becomes: the work identity onto
// content_source and content_id, the inferred title and year onto title and release_year,
// and the release attributes directly. A classification with no content type becomes no
// hint, because the column is required. A work from a provider this instance does not know
// is left out, since the hint could not reference it.
func planHint(c exchange.Classification, d drops) *hintPlan {
	if !c.ContentType.Valid {
		d.add(ReasonNoContentType, "magnes.classification", "")
		return nil
	}

	h := &hintPlan{contentType: string(c.ContentType.Val)}

	if c.Work.Valid {
		if workSources[c.Work.Val.Source] {
			h.contentSource, h.contentID = &c.Work.Val.Source, &c.Work.Val.ID
		} else {
			d.add(ReasonUnknownWorkSource, "magnes.classification.work.source", c.Work.Val.Source)
		}
	}

	if c.InferredTitle.Valid && c.InferredTitle.Val != "" {
		h.title = &c.InferredTitle.Val
	}

	if c.InferredYear.Valid && c.InferredYear.Val > 0 {
		year := int32(c.InferredYear.Val)
		h.releaseYear = &year
	}

	if len(c.Languages.Val) > 0 {
		languages := make(model.Languages, len(c.Languages.Val))
		for _, language := range c.Languages.Val {
			languages[language] = struct{}{}
		}

		h.languages = jsonText(languages)
	}

	if len(c.Episodes.Val) > 0 {
		episodes := make(model.Episodes)

		for _, entry := range c.Episodes.Val {
			if entry.WholeSeason {
				episodes = episodes.AddSeason(entry.Season)
				continue
			}

			for _, episode := range entry.Episodes {
				episodes = episodes.AddEpisode(entry.Season, episode)
			}
		}

		h.episodes = jsonText(episodes)
	}

	h.resolution = enumValue(c.Video.Resolution)
	h.source = enumValue(c.Video.Source)
	h.codec = enumValue(c.Video.Codec)
	h.threeD = enumValue(c.Video.ThreeD)
	h.modifier = enumValue(c.Video.Modifier)

	if c.ReleaseGroup.Valid && c.ReleaseGroup.Val != "" {
		h.releaseGroup = &c.ReleaseGroup.Val
	}

	return h
}

// jsonText encodes a JSON column as gorm's json serializer does, so the hint reads back
// like one the legacy importer wrote. Both types encode without error.
func jsonText(v any) *string {
	encoded, _ := json.Marshal(v) //nolint:errchkjson // maps of ints and strings always encode
	text := string(encoded)

	return &text
}

func enumValue[T ~string](v model.Maybe[T]) *string {
	if !v.Valid {
		return nil
	}

	text := string(v.Val)

	return &text
}

// planFiles decides how a non-empty incoming list would be stored. An empty list says
// nothing, so it is left unknown. It reports the field that is out of range, if any.
func (i *ExchangeImporter) planFiles(p *recordPlan, record exchange.Record) (string, bool) {
	incoming := record.Files.Val
	complete := !record.FilesComplete.Valid || record.FilesComplete.Val

	var total uint64

	files := make([]exchange.File, 0, len(incoming))
	paths := make([]string, 0, len(incoming))

	for _, f := range incoming {
		if f.Index > math.MaxInt32 {
			return "files[].index", false
		}

		if f.Size > math.MaxInt64 || total+f.Size > math.MaxInt64 {
			return "files[].size", false
		}

		total += f.Size
		paths = append(paths, f.Path)

		// Padding entries align the next file to a piece boundary and are not content;
		// the crawler does not store them either.
		if !strings.HasPrefix(f.Path, ".pad/") {
			files = append(files, f)
		}
	}

	slices.SortFunc(files, func(a, b exchange.File) int { return int(a.Index) - int(b.Index) })

	p.evidence.Files = paths
	p.completeFiles = complete

	if complete && !record.Size.Valid {
		p.size = int64(total)
	}

	switch {
	case complete && len(incoming) == 1 && len(files) == 1 && files[0].Index == 0 && files[0].Path == record.Name:
		// A single-file torrent is exported as one entry named after the torrent, and
		// stored, as the crawler stores it, with no file rows.
		p.filesStatus = model.FilesStatusSingle
	case !complete:
		p.filesStatus = model.FilesStatusOverThreshold
		p.files = files[:min(len(files), int(i.config.FilesThreshold))]
	case len(files) > int(i.config.FilesThreshold):
		p.filesStatus = model.FilesStatusOverThreshold
		p.filesCount = ptrInt32(len(files))
		p.files = files[:i.config.FilesThreshold]
	default:
		p.filesStatus = model.FilesStatusMulti
		p.filesCount = ptrInt32(len(files))
		p.files = files
	}

	return "", true
}

func ptrInt32(n int) *int32 {
	v := int32(min(n, math.MaxInt32))
	return &v
}

func peerCount(v model.Maybe[uint]) (*int32, bool) {
	if !v.Valid {
		return nil, true
	}

	if v.Val > math.MaxInt32 {
		return nil, false
	}

	n := int32(v.Val)

	return &n, true
}

// writeBatch writes one batch in one transaction. Moderation runs first, as it does for
// the crawler: keyword matches are recorded, and the batch is written inside the ledger's
// transaction, which holds the moderation lock of every infohash in it until commit. The
// crawler takes the same locks, so the two never write one torrent at the same time; the
// merge rules are still enforced in SQL, so they hold against any other writer too.
func (i *ExchangeImporter) writeBatch(
	ctx context.Context, records []exchange.Record, options ExchangeOptions, d drops,
) (batchResult, error) {
	var result batchResult

	plans := make([]recordPlan, 0, len(records))
	evidence := make([]moderation.Evidence, 0, len(records))

	for _, record := range records {
		p, ok := i.plan(record, options, d)
		if !ok {
			result.skipped++
			continue
		}

		plans = append(plans, p)
		evidence = append(evidence, p.evidence)
	}

	if len(plans) == 0 {
		return result, nil
	}

	ledger := moderation.NewService(i.db)
	for _, observed := range evidence {
		if _, err := i.keywordPolicy.Apply(ctx, ledger, observed); err != nil {
			return batchResult{}, err
		}
	}

	err := ledger.Ingest(ctx, evidence, func(tx *gorm.DB, permitted map[protocol.ID]bool) error {
		eligible := make([]recordPlan, 0, len(plans))

		written := result

		for _, p := range plans {
			if permitted[p.hash] {
				eligible = append(eligible, p)
			} else {
				written.banned++
			}
		}

		if len(eligible) == 0 {
			result = written
			return nil
		}

		w := batchWriter{ctx: ctx, tx: tx, conn: tx.Statement.ConnPool}
		if err := w.write(eligible, options, &written); err != nil {
			return err
		}

		result = written

		return nil
	})
	if err != nil {
		return batchResult{}, err
	}

	return result, nil
}

// batchWriter runs one batch's statements inside the ledger's transaction. They go to the
// transaction's connection directly, so arrays travel as PostgreSQL arrays and every row
// of a statement is written by one unnest.
type batchWriter struct {
	ctx  context.Context
	tx   *gorm.DB
	conn gorm.ConnPool
}

// hashSet is a set of infohashes returned by a statement.
type hashSet map[protocol.ID]struct{}

func (s hashSet) has(hash protocol.ID) bool {
	_, ok := s[hash]
	return ok
}

// returning runs a statement that returns info_hash rows, and collects them. n is the
// number of rows, which can exceed the set's size.
func (w batchWriter) returning(query string, args ...any) (set hashSet, n int, err error) {
	rows, err := w.conn.QueryContext(w.ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}

	defer func() { _ = rows.Close() }()

	set = make(hashSet)

	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, 0, err
		}

		hash, err := protocol.NewIDFromByteSlice(raw)
		if err != nil {
			return nil, 0, err
		}

		set[hash] = struct{}{}
		n++
	}

	return set, n, rows.Err()
}

func (w batchWriter) write(plans []recordPlan, options ExchangeOptions, result *batchResult) error {
	slices.SortFunc(
		plans,
		func(a, b recordPlan) int { return bytes.Compare(a.hash[:], b.hash[:]) },
	)

	created, err := w.insertOrLockTorrents(plans)
	if err != nil {
		return err
	}

	changed := make(hashSet)

	filled, err := w.fillFiles(plans, created)
	if err != nil {
		return err
	}

	filesAdded, err := w.insertFiles(plans, created, filled)
	if err != nil {
		return err
	}

	sourcesChanged, err := w.writeSources(plans)
	if err != nil {
		return err
	}

	hintsWritten, err := w.writeHints(plans, options.OverwriteHints)
	if err != nil {
		return err
	}

	for _, p := range plans {
		if p.hint == nil {
			continue
		}

		if hintsWritten.has(p.hash) {
			result.hintsWritten++
		} else {
			result.hintsKept++
		}
	}

	tagged, tagsAdded, err := w.addTags(plans)
	if err != nil {
		return err
	}

	result.tagsAdded += tagsAdded

	for _, set := range []hashSet{filled, filesAdded, sourcesChanged, hintsWritten, tagged} {
		for hash := range set {
			changed[hash] = struct{}{}
		}
	}

	queued := make([]protocol.ID, 0, len(plans))

	for _, p := range plans {
		switch {
		case created.has(p.hash):
			result.new++
		case changed.has(p.hash):
			result.updated++
		default:
			result.unchanged++
			continue
		}

		queued = append(queued, p.hash)
	}

	return w.queueClassification(queued)
}

// insertOrLockTorrents creates the torrents the catalogue does not have, returns them, and
// locks the ones it has until commit. Name, size and private are written only here: for a
// torrent that exists, local wins.
//
// The update never happens, but ON CONFLICT DO UPDATE locks every conflicting row even
// when its WHERE is false, so once this statement returns every torrent in the batch
// exists and cannot be deleted under the rest of the batch, whose foreign keys would
// otherwise roll the whole batch back. A delete in flight is waited for, and its torrent is
// then created afresh. The lock is FOR NO KEY UPDATE, the one the source-rank trigger
// takes anyway, and rows are locked in infohash order.
func (w batchWriter) insertOrLockTorrents(plans []recordPlan) (hashSet, error) {
	var (
		hashes   = make([][]byte, len(plans))
		names    = make([]string, len(plans))
		sizes    = make([]int64, len(plans))
		private  = make([]bool, len(plans))
		statuses = make([]string, len(plans))
		counts   = make([]*int32, len(plans))
	)

	for j, p := range plans {
		hashes[j], names[j], sizes[j], private[j] = p.hash.Bytes(), p.name, p.size, p.private
		statuses[j], counts[j] = string(p.filesStatus), p.filesCount
	}

	created, _, err := w.returning(`INSERT INTO torrents AS t
 (info_hash, name, size, private, files_status, files_count, created_at, updated_at)
SELECT u.info_hash, u.name, u.size, u.private, u.files_status::"FilesStatus", u.files_count, now(), now()
FROM unnest($1::bytea[], $2::text[], $3::bigint[], $4::boolean[], $5::text[], $6::integer[])
 AS u(info_hash, name, size, private, files_status, files_count)
ORDER BY u.info_hash
ON CONFLICT (info_hash) DO UPDATE SET name = t.name WHERE false
RETURNING info_hash`, hashes, names, sizes, private, statuses, counts)
	if err != nil {
		return nil, fmt.Errorf("insert torrents: %w", err)
	}

	return created, nil
}

// fillFiles takes a complete incoming list for a torrent that existed before this batch,
// when its own list is unknown or partial, and returns the torrents it took one for. The
// condition is part of the update, so a crawler write that completed the list first
// wins.
func (w batchWriter) fillFiles(plans []recordPlan, created hashSet) (hashSet, error) {
	var (
		hashes   [][]byte
		statuses []string
		counts   []*int32
	)

	for _, p := range plans {
		if p.completeFiles && !created.has(p.hash) {
			hashes = append(hashes, p.hash.Bytes())
			statuses = append(statuses, string(p.filesStatus))
			counts = append(counts, p.filesCount)
		}
	}

	if len(hashes) == 0 {
		return hashSet{}, nil
	}

	filled, _, err := w.returning(`UPDATE torrents AS t
SET files_status = u.files_status::"FilesStatus", files_count = u.files_count, updated_at = now()
FROM unnest($1::bytea[], $2::text[], $3::integer[]) AS u(info_hash, files_status, files_count)
WHERE t.info_hash = u.info_hash
 AND t.files_status IN ('no_info', 'over_threshold')
 AND (t.files_status::text, t.files_count) IS DISTINCT FROM (u.files_status, u.files_count)
RETURNING t.info_hash`, hashes, statuses, counts)
	if err != nil {
		return nil, fmt.Errorf("fill files: %w", err)
	}

	return filled, nil
}

// insertFiles writes the file rows of new torrents and of torrents whose list was just
// filled. A stored row with the same index or path is kept, so a partial local list is
// completed around what it already holds.
func (w batchWriter) insertFiles(plans []recordPlan, created, filled hashSet) (hashSet, error) {
	var (
		hashes  [][]byte
		indexes []int32
		paths   []string
		sizes   []int64
	)

	for _, p := range plans {
		if !created.has(p.hash) && !filled.has(p.hash) {
			continue
		}

		for _, f := range p.files {
			hashes = append(hashes, p.hash.Bytes())
			indexes = append(indexes, int32(f.Index)) //nolint:gosec // checked in planFiles
			paths = append(paths, f.Path)
			sizes = append(sizes, int64(f.Size)) //nolint:gosec // checked in planFiles
		}
	}

	if len(hashes) == 0 {
		return hashSet{}, nil
	}

	added, _, err := w.returning(`INSERT INTO torrent_files (info_hash, index, path, size, created_at, updated_at)
SELECT u.info_hash, u.index, u.path, u.size, now(), now()
FROM unnest($1::bytea[], $2::integer[], $3::text[], $4::bigint[]) AS u(info_hash, index, path, size)
ORDER BY u.info_hash, u.index
ON CONFLICT DO NOTHING
RETURNING info_hash`, hashes, indexes, paths, sizes)
	if err != nil {
		return nil, fmt.Errorf("insert files: %w", err)
	}

	return added, nil
}

// writeHints writes each classification as its torrent's hint, and returns the torrents
// whose hint it created or changed. An existing hint wins, in the insert itself, unless
// overwrite is set; an overwrite replaces the whole hint, so it never mixes two
// classifications.
func (w batchWriter) writeHints(plans []recordPlan, overwrite bool) (hashSet, error) {
	var (
		hashes                             [][]byte
		contentTypes                       []string
		contentSources, contentIDs, titles []*string
		years                              []*int32
		languages, episodes                []*string
		resolutions, sources, codecs       []*string
		threeDs, modifiers, groups         []*string
	)

	for _, p := range plans {
		h := p.hint
		if h == nil {
			continue
		}

		hashes = append(hashes, p.hash.Bytes())
		contentTypes = append(contentTypes, h.contentType)
		contentSources = append(contentSources, h.contentSource)
		contentIDs = append(contentIDs, h.contentID)
		titles = append(titles, h.title)
		years = append(years, h.releaseYear)
		languages = append(languages, h.languages)
		episodes = append(episodes, h.episodes)
		resolutions = append(resolutions, h.resolution)
		sources = append(sources, h.source)
		codecs = append(codecs, h.codec)
		threeDs = append(threeDs, h.threeD)
		modifiers = append(modifiers, h.modifier)
		groups = append(groups, h.releaseGroup)
	}

	if len(hashes) == 0 {
		return hashSet{}, nil
	}

	onConflict := `DO NOTHING`
	if overwrite {
		onConflict = `DO UPDATE SET (` + hintColumns + `, updated_at) = (` + excludedHintColumns + `, now())
WHERE (` + storedHintColumns + `) IS DISTINCT FROM (` + excludedHintColumns + `)`
	}

	written, _, err := w.returning(
		`INSERT INTO torrent_hints AS h (info_hash, `+hintColumns+`, created_at, updated_at)
SELECT u.info_hash, u.content_type, u.content_source, u.content_id, u.title, u.release_year,
 u.languages::jsonb, u.episodes::jsonb, u.video_resolution, u.video_source, u.video_codec, u.video_3d,
 u.video_modifier, u.release_group, now(), now()
FROM unnest($1::bytea[], $2::text[], $3::text[], $4::text[], $5::text[], $6::integer[], $7::text[], $8::text[],
 $9::text[], $10::text[], $11::text[], $12::text[], $13::text[], $14::text[])
 AS u(info_hash, content_type, content_source, content_id, title, release_year, languages, episodes,
 video_resolution, video_source, video_codec, video_3d, video_modifier, release_group)
ORDER BY u.info_hash
ON CONFLICT (info_hash) `+onConflict+`
RETURNING info_hash`,
		hashes,
		contentTypes,
		contentSources,
		contentIDs,
		titles,
		years,
		languages,
		episodes,
		resolutions,
		sources,
		codecs,
		threeDs,
		modifiers,
		groups,
	)
	if err != nil {
		return nil, fmt.Errorf("write hints: %w", err)
	}

	return written, nil
}

const (
	hintColumns = `content_type, content_source, content_id, title, release_year, languages, episodes,
 video_resolution, video_source, video_codec, video_3d, video_modifier, release_group`
	storedHintColumns = `h.content_type, h.content_source, h.content_id, h.title, h.release_year, h.languages,
 h.episodes, h.video_resolution, h.video_source, h.video_codec, h.video_3d, h.video_modifier, h.release_group`
	excludedHintColumns = `EXCLUDED.content_type, EXCLUDED.content_source, EXCLUDED.content_id, EXCLUDED.title,
 EXCLUDED.release_year, EXCLUDED.languages, EXCLUDED.episodes, EXCLUDED.video_resolution, EXCLUDED.video_source,
 EXCLUDED.video_codec, EXCLUDED.video_3d, EXCLUDED.video_modifier, EXCLUDED.release_group`
)

// addTags adds incoming tags to the local ones, never removing one, and returns the
// torrents that gained a tag and how many tags were added.
func (w batchWriter) addTags(plans []recordPlan) (hashSet, int, error) {
	var (
		hashes [][]byte
		names  []string
	)

	for _, p := range plans {
		for _, tag := range p.tags {
			hashes = append(hashes, p.hash.Bytes())
			names = append(names, tag)
		}
	}

	if len(hashes) == 0 {
		return hashSet{}, 0, nil
	}

	tagged, added, err := w.returning(`INSERT INTO torrent_tags (info_hash, name, created_at, updated_at)
SELECT u.info_hash, u.name, now(), now() FROM unnest($1::bytea[], $2::text[]) AS u(info_hash, name)
ORDER BY u.info_hash, u.name
ON CONFLICT (info_hash, name) DO NOTHING
RETURNING info_hash`, hashes, names)
	if err != nil {
		return nil, 0, fmt.Errorf("add tags: %w", err)
	}

	return tagged, added, nil
}

// writeSources stores every incoming source under its prefixed name, creating the
// torrent_sources rows it needs, and returns the torrents whose source rows changed.
//
// A statement cannot update one row twice, so a source a record lists more than once is
// written in rounds: the n-th entry of each source goes in the n-th statement, and each
// merges with what the one before it wrote, as if the entries arrived one at a time.
func (w batchWriter) writeSources(plans []recordPlan) (hashSet, error) {
	var (
		keys   []string
		rounds []sourceRound
	)

	for _, p := range plans {
		seen := make(map[string]int, len(p.sources))

		for _, s := range p.sources {
			n := seen[s.name]
			seen[s.name]++

			if n == len(rounds) {
				rounds = append(rounds, sourceRound{})
			}

			rounds[n].add(p.hash, s)
			keys = append(keys, s.name)
		}
	}

	changed := make(hashSet)

	if len(keys) == 0 {
		return changed, nil
	}

	slices.Sort(keys)

	if _, err := w.conn.ExecContext(w.ctx, `INSERT INTO torrent_sources (key, name, created_at, updated_at)
SELECT k, k, now(), now() FROM unnest($1::text[]) AS k ORDER BY k
ON CONFLICT (key) DO NOTHING`, slices.Compact(keys)); err != nil {
		return nil, fmt.Errorf("insert torrent sources: %w", err)
	}

	for _, round := range rounds {
		set, _, err := w.returning(upsertSources, round.hashes, round.names, round.seeders, round.leechers,
			round.published, round.scraped)
		if err != nil {
			return nil, fmt.Errorf("write sources: %w", err)
		}

		for hash := range set {
			changed[hash] = struct{}{}
		}
	}

	return changed, nil
}

// sourceRound is the columns of one source upsert.
type sourceRound struct {
	hashes    [][]byte
	names     []string
	seeders   []*int32
	leechers  []*int32
	published []*time.Time
	scraped   []*time.Time
}

func (r *sourceRound) add(hash protocol.ID, s sourcePlan) {
	r.hashes = append(r.hashes, hash.Bytes())
	r.names = append(r.names, s.name)
	r.seeders = append(r.seeders, s.seeders)
	r.leechers = append(r.leechers, s.leechers)
	r.published = append(r.published, s.publishedAt)
	r.scraped = append(r.scraped, s.scrapedAt)
}

// The merge rule for one stored source, evaluated against the row as it is when the
// statement runs, so no read-then-write can race a concurrent writer.
//
//   - Incoming counts dated later than the stored ones, or dated when the stored ones are
//     not, replace them, and their scrapedAt is stored with them.
//   - Otherwise incoming counts only fill counts that are unknown.
//   - Either way a known count is never replaced by an unknown one.
//   - The earliest publication wins; LEAST ignores an unknown one.
//
// A scrapedAt that dates no count is dropped on the way in, so it can neither be stored
// nor make an incoming row look newer.
const (
	sourceIsNewer = `(EXCLUDED.scraped_at IS NOT NULL AND (s.scraped_at IS NULL OR EXCLUDED.scraped_at > s.scraped_at))`

	mergedSeeders = `CASE WHEN ` + sourceIsNewer + ` THEN COALESCE(EXCLUDED.seeders, s.seeders)
 ELSE COALESCE(s.seeders, EXCLUDED.seeders) END`
	mergedLeechers = `CASE WHEN ` + sourceIsNewer + ` THEN COALESCE(EXCLUDED.leechers, s.leechers)
 ELSE COALESCE(s.leechers, EXCLUDED.leechers) END`
	mergedScrapedAt   = `CASE WHEN ` + sourceIsNewer + ` THEN EXCLUDED.scraped_at ELSE s.scraped_at END`
	mergedPublishedAt = `LEAST(s.published_at, EXCLUDED.published_at)`

	upsertSources = `INSERT INTO torrents_torrent_sources AS s
 (source, info_hash, seeders, leechers, published_at, scraped_at, created_at, updated_at)
SELECT u.source, u.info_hash, u.seeders, u.leechers, u.published_at,
 CASE WHEN u.seeders IS NULL AND u.leechers IS NULL THEN NULL ELSE u.scraped_at END, now(), now()
FROM unnest($1::bytea[], $2::text[], $3::integer[], $4::integer[], $5::timestamptz[], $6::timestamptz[])
 AS u(info_hash, source, seeders, leechers, published_at, scraped_at)
ORDER BY u.info_hash, u.source
ON CONFLICT (source, info_hash) DO UPDATE SET
 seeders = ` + mergedSeeders + `,
 leechers = ` + mergedLeechers + `,
 scraped_at = ` + mergedScrapedAt + `,
 published_at = ` + mergedPublishedAt + `,
 updated_at = now()
WHERE (s.seeders, s.leechers, s.scraped_at, s.published_at) IS DISTINCT FROM
 (` + mergedSeeders + `, ` + mergedLeechers + `, ` + mergedScrapedAt + `, ` + mergedPublishedAt + `)
RETURNING info_hash`
)

// queueClassification queues new and changed torrents for classification, as the legacy
// importer does. An identical pending job already does the same work, so a fingerprint
// collision is not an error.
func (w batchWriter) queueClassification(hashes []protocol.ID) error {
	if len(hashes) == 0 {
		return nil
	}

	job, err := processor.NewQueueJob(processor.MessageParams{InfoHashes: hashes}, model.QueueJobPriority(20))
	if err != nil {
		return err
	}

	return dao.Use(w.tx).QueueJob.WithContext(w.ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&job)
}
