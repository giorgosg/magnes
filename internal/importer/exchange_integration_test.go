package importer_test

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/moderation"
	"github.com/bitmagnet-io/bitmagnet/internal/processor"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

const exchangeHeader = `{"format":{"name":"torrent-exchange","version":{"major":1,"minor":0}}}`

// exchangeFile is a header followed by the given records, one per line. A record literal
// may span lines for readability; JSON allows no raw newline inside a string, so removing
// them leaves the record unchanged.
func exchangeFile(records ...string) io.Reader {
	lines := make([]string, len(records))
	for i, record := range records {
		lines[i] = strings.ReplaceAll(record, "\n", "")
	}

	return strings.NewReader(exchangeHeader + "\n" + strings.Join(lines, "\n") + "\n")
}

func newExchangeImporter(db *dbtest.DB) *importer.ExchangeImporter {
	return importer.NewExchangeImporter(db.Gorm, nil, importer.ExchangeConfig{FilesThreshold: 100})
}

func importExchange(
	t *testing.T, db *dbtest.DB, options importer.ExchangeOptions, records ...string,
) importer.ExchangeReport {
	t.Helper()

	report, err := newExchangeImporter(db).Import(t.Context(), exchangeFile(records...), options)
	require.NoError(t, err)
	require.Empty(t, report.Malformed, "a crafted record did not decode")

	return report
}

func alice() importer.ExchangeOptions { return importer.ExchangeOptions{Origin: "alice"} }

func exec(t *testing.T, db *dbtest.DB, sql string, args ...any) {
	t.Helper()

	_, err := db.Pool.Exec(t.Context(), sql, args...)
	require.NoError(t, err)
}

func hashHex(b byte) string {
	return protocol.ID{b}.String()
}

func seedTorrent(
	t *testing.T,
	db *dbtest.DB,
	hash protocol.ID,
	name string,
	size int64,
	private bool,
	filesStatus string,
) {
	t.Helper()

	exec(t, db, `INSERT INTO torrents (info_hash,name,size,private,files_status,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,now(),now())`, hash[:], name, size, private, filesStatus)
}

func seedSource(
	t *testing.T, db *dbtest.DB, hash protocol.ID, source string, seeders, leechers *int, scrapedAt *time.Time,
) {
	t.Helper()

	exec(t, db, `INSERT INTO torrent_sources (key,name,created_at,updated_at) VALUES ($1,$1,now(),now())
ON CONFLICT DO NOTHING`, source)
	exec(t, db, `INSERT INTO torrents_torrent_sources (source,info_hash,seeders,leechers,scraped_at,created_at,
updated_at) VALUES ($1,$2,$3,$4,$5,now(),now())`, source, hash[:], seeders, leechers, scrapedAt)
}

func ptr[T any](v T) *T { return &v }

// sourceRow is one stored torrents_torrent_sources row.
type sourceRow struct {
	Seeders, Leechers      *int
	PublishedAt, ScrapedAt *time.Time
}

func sourceRows(t *testing.T, db *dbtest.DB, hash protocol.ID) map[string]sourceRow {
	t.Helper()

	rows, err := db.Pool.Query(t.Context(), `SELECT source, seeders, leechers, published_at, scraped_at
FROM torrents_torrent_sources WHERE info_hash = $1`, hash[:])
	require.NoError(t, err)

	defer rows.Close()

	result := make(map[string]sourceRow)

	for rows.Next() {
		var (
			name string
			row  sourceRow
		)

		require.NoError(t, rows.Scan(&name, &row.Seeders, &row.Leechers, &row.PublishedAt, &row.ScrapedAt))

		if row.PublishedAt != nil {
			row.PublishedAt = ptr(row.PublishedAt.UTC())
		}

		if row.ScrapedAt != nil {
			row.ScrapedAt = ptr(row.ScrapedAt.UTC())
		}

		result[name] = row
	}

	require.NoError(t, rows.Err())

	return result
}

func count(t *testing.T, db *dbtest.DB, query string, args ...any) int {
	t.Helper()

	var n int
	require.NoError(t, db.Pool.QueryRow(t.Context(), query, args...).Scan(&n))

	return n
}

// failingReader fails the test if anything reads from it.
type failingReader struct{ t *testing.T }

func (r failingReader) Read([]byte) (int, error) {
	r.t.Error("the import read its input before refusing the origin label")
	return 0, io.EOF
}

func TestExchangeImportRefusesAnInvalidOriginBeforeReading(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	for _, origin := range []string{"", "Alice", "alice:bob", "alice_bob", "-alice", "alice-", "al ice"} {
		_, err := newExchangeImporter(db).Import(t.Context(), failingReader{t}, importer.ExchangeOptions{
			Origin: origin,
		})
		require.ErrorIs(t, err, importer.ErrInvalidOrigin, "origin %q", origin)
	}

	require.Zero(t, count(t, db, `SELECT count(*) FROM torrents`))
	require.Zero(t, count(t, db, `SELECT count(*) FROM torrent_sources WHERE key NOT IN ('dht','rarbg')`))
}

// Every incoming source is stored under the origin's prefix, so the local dht row keeps
// meaning "this instance's crawler saw it", and chains accumulate.
func TestExchangeImportPrefixesEverySourceWithTheOrigin(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	hash := protocol.ID{1}
	scraped := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	seedTorrent(t, db, hash, "Known", 10, false, "no_info")
	seedSource(t, db, hash, "dht", ptr(9), ptr(3), &scraped)

	report := importExchange(t, db, alice(), `{"infoHash":"`+hashHex(1)+`","name":"Known","sources":[
{"source":"dht","seeders":40,"leechers":2,"scrapedAt":"2026-10-17T00:00:00Z"},
{"source":"bob:dht","seeders":5}]}`, `{"infoHash":"`+hashHex(2)+`","name":"New","sources":[{"source":"dht"}]}`)

	require.Equal(t, map[string]sourceRow{
		"dht": {Seeders: ptr(9), Leechers: ptr(3), ScrapedAt: &scraped},
		"alice:dht": {
			Seeders:   ptr(40),
			Leechers:  ptr(2),
			ScrapedAt: ptr(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)),
		},
		"alice:bob:dht": {Seeders: ptr(5)},
	}, sourceRows(t, db, hash))
	require.Equal(t, map[string]sourceRow{"alice:dht": {}}, sourceRows(t, db, protocol.ID{2}))
	require.Zero(t, count(t, db, `SELECT count(*) FROM torrents_torrent_sources
WHERE source NOT LIKE 'alice:%' AND source <> 'dht'`), "an import never writes an unprefixed source")

	require.Equal(t, 1, report.New)
	require.Equal(t, 1, report.Updated)
}

// Within one stored, prefixed source, the newer observation wins, a known count is never
// replaced by an unknown one, and the earliest publication wins.
func TestExchangeImportMergesPeerCountsByScrapedAt(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	stamp := func(d int) string { return day(d).Format(time.RFC3339) }

	cases := []struct {
		name     string
		stored   sourceRow
		incoming string
		want     sourceRow
	}{
		{
			name:     "a newer scrape replaces the counts and their date",
			stored:   sourceRow{Seeders: ptr(9), Leechers: ptr(3), ScrapedAt: ptr(day(10))},
			incoming: `"seeders":40,"leechers":2,"scrapedAt":"` + stamp(17) + `"`,
			want:     sourceRow{Seeders: ptr(40), Leechers: ptr(2), ScrapedAt: ptr(day(17))},
		},
		{
			name:     "an older scrape does not",
			stored:   sourceRow{Seeders: ptr(9), Leechers: ptr(3), ScrapedAt: ptr(day(10))},
			incoming: `"seeders":40,"leechers":2,"scrapedAt":"` + stamp(5) + `"`,
			want:     sourceRow{Seeders: ptr(9), Leechers: ptr(3), ScrapedAt: ptr(day(10))},
		},
		{
			name:     "an equally old scrape does not",
			stored:   sourceRow{Seeders: ptr(9), Leechers: ptr(3), ScrapedAt: ptr(day(10))},
			incoming: `"seeders":40,"leechers":2,"scrapedAt":"` + stamp(10) + `"`,
			want:     sourceRow{Seeders: ptr(9), Leechers: ptr(3), ScrapedAt: ptr(day(10))},
		},
		{
			name:     "a newer scrape never replaces a known count with an unknown one",
			stored:   sourceRow{Seeders: ptr(9), Leechers: ptr(3), ScrapedAt: ptr(day(10))},
			incoming: `"seeders":40,"scrapedAt":"` + stamp(17) + `"`,
			want:     sourceRow{Seeders: ptr(40), Leechers: ptr(3), ScrapedAt: ptr(day(17))},
		},
		{
			name:     "a dated scrape replaces undated counts",
			stored:   sourceRow{Seeders: ptr(9), Leechers: ptr(3)},
			incoming: `"seeders":40,"leechers":2,"scrapedAt":"` + stamp(5) + `"`,
			want:     sourceRow{Seeders: ptr(40), Leechers: ptr(2), ScrapedAt: ptr(day(5))},
		},
		{
			name:     "undated counts only fill unknown ones",
			stored:   sourceRow{Seeders: ptr(9), ScrapedAt: ptr(day(10))},
			incoming: `"seeders":40,"leechers":2`,
			want:     sourceRow{Seeders: ptr(9), Leechers: ptr(2), ScrapedAt: ptr(day(10))},
		},
		{
			name:     "an older scrape fills unknown counts without redating them",
			stored:   sourceRow{Leechers: ptr(3), ScrapedAt: ptr(day(10))},
			incoming: `"seeders":40,"leechers":2,"scrapedAt":"` + stamp(5) + `"`,
			want:     sourceRow{Seeders: ptr(40), Leechers: ptr(3), ScrapedAt: ptr(day(10))},
		},
		{
			name:     "a date with no counts is not stored",
			stored:   sourceRow{Seeders: ptr(9)},
			incoming: `"scrapedAt":"` + stamp(17) + `"`,
			want:     sourceRow{Seeders: ptr(9)},
		},
		{
			name:     "the earliest publication wins",
			stored:   sourceRow{PublishedAt: ptr(day(10))},
			incoming: `"publishedAt":"` + stamp(5) + `"`,
			want:     sourceRow{PublishedAt: ptr(day(5))},
		},
		{
			name:     "a later publication does not",
			stored:   sourceRow{PublishedAt: ptr(day(10))},
			incoming: `"publishedAt":"` + stamp(17) + `"`,
			want:     sourceRow{PublishedAt: ptr(day(10))},
		},
		{
			name:     "an unknown publication does not",
			stored:   sourceRow{PublishedAt: ptr(day(10))},
			incoming: `"seeders":1`,
			want:     sourceRow{Seeders: ptr(1), PublishedAt: ptr(day(10))},
		},
	}

	records := make([]string, 0, len(cases))

	for i, c := range cases {
		hash := protocol.ID{byte(i + 1)}
		seedTorrent(t, db, hash, "Known", 10, false, "no_info")
		seedSource(t, db, hash, "alice:dht", c.stored.Seeders, c.stored.Leechers, c.stored.ScrapedAt)
		exec(t, db, `UPDATE torrents_torrent_sources SET published_at = $2 WHERE info_hash = $1`,
			hash[:], c.stored.PublishedAt)

		records = append(records,
			`{"infoHash":"`+hash.String()+`","name":"Known","sources":[{"source":"dht",`+c.incoming+`}]}`)
	}

	importExchange(t, db, alice(), records...)

	for i, c := range cases {
		require.Equal(
			t,
			map[string]sourceRow{"alice:dht": c.want},
			sourceRows(t, db, protocol.ID{byte(i + 1)}),
			c.name,
		)
	}
}

// One record can list the same source twice; each entry merges by the same rule, in order.
func TestExchangeImportMergesARepeatedSourceWithinARecord(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	report := importExchange(t, db, alice(), `{"infoHash":"`+hashHex(1)+`","name":"Twice","sources":[
{"source":"dht","seeders":1,"scrapedAt":"2026-10-17T00:00:00Z"},
{"source":"dht","seeders":2,"leechers":5,"scrapedAt":"2026-10-01T00:00:00Z"}]}`)

	require.Equal(t, map[string]sourceRow{"alice:dht": {
		Seeders: ptr(1), Leechers: ptr(5), ScrapedAt: ptr(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)),
	}}, sourceRows(t, db, protocol.ID{1}))
	require.Equal(t, 1, report.New)
}

type torrentRow struct {
	Name        string
	Size        int64
	Private     bool
	FilesStatus string
	FilesCount  *int
}

func torrentRowOf(t *testing.T, db *dbtest.DB, hash protocol.ID) torrentRow {
	t.Helper()

	var row torrentRow

	require.NoError(t, db.Pool.QueryRow(t.Context(), `SELECT name, size, private, files_status::text, files_count
FROM torrents WHERE info_hash = $1`, hash[:]).
		Scan(&row.Name, &row.Size, &row.Private, &row.FilesStatus, &row.FilesCount))

	return row
}

func filePaths(t *testing.T, db *dbtest.DB, hash protocol.ID) []string {
	t.Helper()

	rows, err := db.Pool.Query(t.Context(), `SELECT index || ':' || path || ':' || size FROM torrent_files
WHERE info_hash = $1 ORDER BY index`, hash[:])
	require.NoError(t, err)

	defer rows.Close()

	var paths []string

	for rows.Next() {
		var path string
		require.NoError(t, rows.Scan(&path))
		paths = append(paths, path)
	}

	require.NoError(t, rows.Err())

	return paths
}

// Name, size and private are local: an import writes them only for a new torrent.
func TestExchangeImportNeverChangesLocalNameSizeOrPrivate(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	known := protocol.ID{1}
	seedTorrent(t, db, known, "Local name", 10, true, "no_info")

	report := importExchange(t, db, alice(),
		`{"infoHash":"`+hashHex(1)+`","name":"Their name","size":99,"private":false}`,
		`{"infoHash":"`+hashHex(2)+`","name":"New name","size":42,"private":true}`)

	require.Equal(t, torrentRow{Name: "Local name", Size: 10, Private: true, FilesStatus: "no_info"},
		torrentRowOf(t, db, known))
	require.Equal(t, torrentRow{Name: "New name", Size: 42, Private: true, FilesStatus: "no_info"},
		torrentRowOf(t, db, protocol.ID{2}))
	require.Equal(t, 1, report.New)
	require.Equal(t, 1, report.Unchanged)
}

// A new torrent stores the incoming list as this instance would store a crawled one: the
// local threshold applies, padding entries are not content, a single-file torrent has no
// file rows, and a partial list is stored marked partial.
func TestExchangeImportStoresANewTorrentsFiles(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	report, err := importer.NewExchangeImporter(db.Gorm, nil, importer.ExchangeConfig{FilesThreshold: 2}).Import(
		t.Context(), exchangeFile(
			`{"infoHash":"`+hashHex(1)+`","name":"Unknown"}`,
			`{"infoHash":"`+hashHex(2)+`","name":"Single.mkv",
"files":[{"index":0,"path":"Single.mkv","size":7}]}`,
			`{"infoHash":"`+hashHex(3)+`","name":"Multi","files":[{"index":1,"path":"b.srt","size":2},
{"index":0,"path":"a.mkv","size":5},{"index":2,"path":".pad/1024","size":1024}]}`,
			`{"infoHash":"`+hashHex(4)+`","name":"Over","files":[{"index":0,"path":"a","size":1},
{"index":1,"path":"b","size":1},{"index":2,"path":"c","size":1}]}`,
			`{"infoHash":"`+hashHex(5)+`","name":"Partial",
"filesComplete":false,"files":[{"index":0,"path":"a","size":1}]}`,
			`{"infoHash":"`+hashHex(6)+`","name":"Empty","files":[]}`,
		), alice())
	require.NoError(t, err)
	require.Equal(t, 6, report.New)

	require.Equal(t, torrentRow{Name: "Unknown", FilesStatus: "no_info"}, torrentRowOf(t, db, protocol.ID{1}))
	require.Equal(
		t,
		torrentRow{Name: "Single.mkv", Size: 7, FilesStatus: "single"},
		torrentRowOf(t, db, protocol.ID{2}),
		"a size left out is the sum of a complete list",
	)
	require.Empty(t, filePaths(t, db, protocol.ID{2}))
	require.Equal(t, torrentRow{Name: "Multi", Size: 1031, FilesStatus: "multi", FilesCount: ptr(2)},
		torrentRowOf(t, db, protocol.ID{3}))
	require.Equal(t, []string{"0:a.mkv:5", "1:b.srt:2"}, filePaths(t, db, protocol.ID{3}))
	require.Equal(t, torrentRow{Name: "Over", Size: 3, FilesStatus: "over_threshold", FilesCount: ptr(3)},
		torrentRowOf(t, db, protocol.ID{4}))
	require.Equal(t, []string{"0:a:1", "1:b:1"}, filePaths(t, db, protocol.ID{4}))
	require.Equal(
		t,
		torrentRow{Name: "Partial", FilesStatus: "over_threshold"},
		torrentRowOf(t, db, protocol.ID{5}),
		"a partial list says nothing about the total count or size",
	)
	require.Equal(t, []string{"0:a:1"}, filePaths(t, db, protocol.ID{5}))
	require.Equal(t, torrentRow{Name: "Empty", FilesStatus: "no_info"}, torrentRowOf(t, db, protocol.ID{6}),
		"an empty list is no list")
}

// A complete incoming list fills a local list that is unknown or partial, and never
// replaces a complete one; a partial incoming list fills nothing.
func TestExchangeImportFillsOnlyUnknownOrPartialLocalFiles(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	noInfo, partial, complete, single, partialIn := protocol.ID{1}, protocol.ID{2}, protocol.ID{3}, protocol.ID{4},
		protocol.ID{5}

	seedTorrent(t, db, noInfo, "NoInfo", 10, false, "no_info")
	seedTorrent(t, db, partial, "Partial", 10, false, "over_threshold")
	exec(t, db, `INSERT INTO torrent_files (info_hash,index,path,size,created_at,updated_at)
VALUES ($1,0,'local-a',4,now(),now())`, partial[:])
	seedTorrent(t, db, complete, "Complete", 10, false, "multi")
	exec(t, db, `UPDATE torrents SET files_count = 1 WHERE info_hash = $1`, complete[:])
	exec(t, db, `INSERT INTO torrent_files (info_hash,index,path,size,created_at,updated_at)
VALUES ($1,0,'local',10,now(),now())`, complete[:])
	seedTorrent(t, db, single, "Single", 10, false, "single")
	seedTorrent(t, db, partialIn, "PartialIn", 10, false, "no_info")

	list := `"files":[{"index":0,"path":"a","size":4},{"index":1,"path":"b","size":6}]`
	report := importExchange(t, db, alice(),
		`{"infoHash":"`+hashHex(1)+`","name":"x",`+list+`}`,
		`{"infoHash":"`+hashHex(2)+`","name":"x",`+list+`}`,
		`{"infoHash":"`+hashHex(3)+`","name":"x",`+list+`}`,
		`{"infoHash":"`+hashHex(4)+`","name":"x",`+list+`}`,
		`{"infoHash":"`+hashHex(5)+`","name":"x","filesComplete":false,`+list+`}`,
	)

	require.Equal(t, torrentRow{Name: "NoInfo", Size: 10, FilesStatus: "multi", FilesCount: ptr(2)},
		torrentRowOf(t, db, noInfo))
	require.Equal(t, []string{"0:a:4", "1:b:6"}, filePaths(t, db, noInfo))
	require.Equal(t, torrentRow{Name: "Partial", Size: 10, FilesStatus: "multi", FilesCount: ptr(2)},
		torrentRowOf(t, db, partial))
	require.Equal(t, []string{"0:local-a:4", "1:b:6"}, filePaths(t, db, partial),
		"a stored entry is kept; the list is completed around it")
	require.Equal(t, torrentRow{Name: "Complete", Size: 10, FilesStatus: "multi", FilesCount: ptr(1)},
		torrentRowOf(t, db, complete))
	require.Equal(t, []string{"0:local:10"}, filePaths(t, db, complete))
	require.Equal(t, torrentRow{Name: "Single", Size: 10, FilesStatus: "single"}, torrentRowOf(t, db, single))
	require.Empty(t, filePaths(t, db, single))
	require.Equal(
		t,
		torrentRow{Name: "PartialIn", Size: 10, FilesStatus: "no_info"},
		torrentRowOf(t, db, partialIn),
	)
	require.Empty(t, filePaths(t, db, partialIn))

	require.Equal(t, 2, report.Updated)
	require.Equal(t, 3, report.Unchanged)
}

// hintRow is one stored torrent_hints row, with JSON columns as text.
type hintRow struct {
	ContentType, ContentSource, ContentID, Title *string
	ReleaseYear                                  *int
	Languages, Episodes                          *string
	Resolution, Source, Codec, ThreeD, Modifier  *string
	ReleaseGroup                                 *string
}

func hintOf(t *testing.T, db *dbtest.DB, hash protocol.ID) *hintRow {
	t.Helper()

	var row hintRow

	err := db.Pool.QueryRow(t.Context(), `SELECT content_type, content_source, content_id, title, release_year,
 languages::text, episodes::text, video_resolution, video_source, video_codec, video_3d, video_modifier, release_group
FROM torrent_hints WHERE info_hash = $1`, hash[:]).Scan(&row.ContentType, &row.ContentSource, &row.ContentID,
		&row.Title, &row.ReleaseYear, &row.Languages, &row.Episodes, &row.Resolution, &row.Source, &row.Codec,
		&row.ThreeD, &row.Modifier, &row.ReleaseGroup)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}

	require.NoError(t, err)

	return &row
}

func seedHint(t *testing.T, db *dbtest.DB, hash protocol.ID, contentType, title string) {
	t.Helper()

	exec(t, db, `INSERT INTO torrent_hints (info_hash,content_type,title,created_at,updated_at)
VALUES ($1,$2,$3,now(),now())`, hash[:], contentType, title)
}

const fullClassification = `"magnes":{"classification":{"contentType":"tv-show","work":{"source":"tmdb","id":"1399"},
"inferredTitle":"Example Show","inferredYear":2011,"languages":["en","fr"],
"episodes":[{"season":1,"episodes":[1,2]},{"season":2,"wholeSeason":true}],
"video":{"resolution":"1080p","source":"web-dl","codec":"x264","3d":"3d-sbs","modifier":"remux"},
"releaseGroup":"GRP"}}`

// The classification becomes the hint: the work identity, inferred title and year, and the
// release attributes, each in its own column.
func TestExchangeImportWritesTheClassificationAsTheHint(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	report := importExchange(t, db, alice(), `{"infoHash":"`+hashHex(1)+`","name":"Show",`+fullClassification+`}`)

	require.Equal(t, &hintRow{
		ContentType: ptr("tv_show"), ContentSource: ptr("tmdb"), ContentID: ptr("1399"),
		Title: ptr("Example Show"), ReleaseYear: ptr(2011), Languages: ptr(`["en", "fr"]`),
		Episodes: ptr(`{"1": {"1": {}, "2": {}}, "2": {}}`), Resolution: ptr("V1080p"), Source: ptr("WEBDL"),
		Codec: ptr("x264"), ThreeD: ptr("V3DSBS"), Modifier: ptr("REMUX"), ReleaseGroup: ptr("GRP"),
	}, hintOf(t, db, protocol.ID{1}))
	require.Equal(t, 1, report.HintsWritten)
}

// An existing hint wins unless the import asks to overwrite it.
func TestExchangeImportKeepsAnExistingHintUnlessOverwriting(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	kept, replaced := protocol.ID{1}, protocol.ID{2}

	for _, hash := range []protocol.ID{kept, replaced} {
		seedTorrent(t, db, hash, "Local", 10, false, "no_info")
		seedHint(t, db, hash, "movie", "Local hint")
	}

	record := func(hash protocol.ID) string {
		return `{"infoHash":"` + hash.String() + `","name":"x","magnes":{"classification":{"contentType":"ebook",
"inferredTitle":"Their hint"}}}`
	}

	report := importExchange(t, db, alice(), record(kept))
	require.Equal(t, &hintRow{ContentType: ptr("movie"), Title: ptr("Local hint")}, hintOf(t, db, kept))
	require.Equal(t, 0, report.HintsWritten)
	require.Equal(t, 1, report.HintsKept)
	require.Equal(t, 1, report.Unchanged)

	report = importExchange(
		t,
		db,
		importer.ExchangeOptions{Origin: "alice", OverwriteHints: true},
		record(replaced),
	)
	require.Equal(t, &hintRow{ContentType: ptr("ebook"), Title: ptr("Their hint")}, hintOf(t, db, replaced),
		"an overwrite replaces the whole hint")
	require.Equal(t, 1, report.HintsWritten)
	require.Equal(t, 1, report.Updated)

	report = importExchange(
		t,
		db,
		importer.ExchangeOptions{Origin: "alice", OverwriteHints: true},
		record(replaced),
	)
	require.Equal(t, 0, report.HintsWritten, "rewriting an identical hint changes nothing")
	require.Equal(t, 1, report.HintsKept)
	require.Equal(t, 1, report.Unchanged)
}

// A hint needs a content type, so a classification without one becomes no hint; and a
// work identity from a provider this instance does not know is dropped from the hint.
// Both are reported.
func TestExchangeImportReportsClassificationsItCannotStore(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	report := importExchange(t, db, alice(),
		`{"infoHash":"`+hashHex(1)+`","name":"x","magnes":{"classification":{"inferredTitle":"Untyped"}}}`,
		`{"infoHash":"`+hashHex(2)+`","name":"x","magnes":{"classification":{"contentType":"movie",
"work":{"source":"letterboxd","id":"abc"},"inferredTitle":"Typed"}}}`)

	require.Nil(t, hintOf(t, db, protocol.ID{1}))
	require.Equal(t, &hintRow{ContentType: ptr("movie"), Title: ptr("Typed")}, hintOf(t, db, protocol.ID{2}))
	require.Equal(t, 1, report.HintsWritten)
	require.Contains(t, report.Dropped, exchange.ReportEntry{
		Scope:  exchange.ScopeRecord,
		Reason: importer.ReasonNoContentType,
		Path:   "magnes.classification",
		Count:  1,
	})
	require.Contains(t, report.Dropped, exchange.ReportEntry{
		Scope:  exchange.ScopeRecord,
		Reason: importer.ReasonUnknownWorkSource,
		Path:   "magnes.classification.work.source",
		Value:  "letterboxd",
		Count:  1,
	})
}

func tagsOf(t *testing.T, db *dbtest.DB, hash protocol.ID) []string {
	t.Helper()

	rows, err := db.Pool.Query(
		t.Context(),
		`SELECT name FROM torrent_tags WHERE info_hash = $1 ORDER BY name`,
		hash[:],
	)
	require.NoError(t, err)

	defer rows.Close()

	var tags []string

	for rows.Next() {
		var tag string
		require.NoError(t, rows.Scan(&tag))
		tags = append(tags, tag)
	}

	require.NoError(t, rows.Err())

	return tags
}

// Incoming tags are added to the local ones and never remove one. Tags that are not
// valid local tag names are dropped and reported.
func TestExchangeImportAddsTagsWithoutRemovingAny(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	hash := protocol.ID{1}
	seedTorrent(t, db, hash, "Tagged", 10, false, "no_info")
	exec(t, db, `INSERT INTO torrent_tags (info_hash,name,created_at,updated_at) VALUES ($1,'local',now(),now()),
($1,'shared',now(),now())`, hash[:])

	report := importExchange(t, db, alice(), `{"infoHash":"`+hashHex(1)+`","name":"x",
"magnes":{"tags":["shared","4k-candidate","Not Valid","4k-candidate","`+strings.Repeat("a", 31)+`"]}}`)

	require.Equal(t, []string{"4k-candidate", "local", "shared"}, tagsOf(t, db, hash))
	require.Equal(t, 1, report.TagsAdded)
	require.Equal(t, 1, report.Updated)
	require.Contains(t, report.Dropped, exchange.ReportEntry{
		Scope:  exchange.ScopeRecord,
		Reason: importer.ReasonInvalidTag,
		Path:   "magnes.tags[]",
		Value:  "Not Valid",
		Count:  1,
	})
	require.Contains(t, report.Dropped, exchange.ReportEntry{
		Scope: exchange.ScopeRecord, Reason: importer.ReasonInvalidTag, Path: "magnes.tags[]",
		Value: strings.Repeat("a", 31), Count: 1,
	})

	report = importExchange(t, db, alice(), `{"infoHash":"`+hashHex(1)+`","name":"x","magnes":{"tags":["shared"]}}`)
	require.Equal(t, 0, report.TagsAdded)
	require.Equal(t, 1, report.Unchanged)
}

// Every record passes ingestion moderation: an exact ban is never written, and the
// keyword policy sees the incoming file paths as well as the name.
func TestExchangeImportPassesIngestionModeration(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ledger := moderation.NewService(db.Gorm)
	banned, byName, byFile, clean := protocol.ID{1}, protocol.ID{2}, protocol.ID{3}, protocol.ID{4}
	require.NoError(t, ledger.Ban(t.Context(), moderation.IdentifierForInfoHash(banned), "operator"))

	policy, err := moderation.NewKeywordPolicy([]string{"forbidden"}, moderation.Config{AutomaticBan: true})
	require.NoError(t, err)

	report, err := importer.NewExchangeImporter(db.Gorm, policy, importer.ExchangeConfig{FilesThreshold: 100}).
		Import(
			t.Context(), exchangeFile(
				`{"infoHash":"`+hashHex(1)+`","name":"Banned","sources":[{"source":"dht"}],
"magnes":{"tags":["x"],"classification":{"contentType":"movie"}}}`,
				`{"infoHash":"`+hashHex(2)+`","name":"Forbidden release"}`,
				`{"infoHash":"`+hashHex(3)+`","name":"Innocent",
"files":[{"index":0,"path":"forbidden.mkv","size":1},{"index":1,"path":"b","size":1}]}`,
				`{"infoHash":"`+hashHex(4)+`","name":"Clean"}`,
			), alice())
	require.NoError(t, err)

	require.Equal(t, 3, report.Banned)
	require.Equal(t, 1, report.New)
	require.Equal(t, 0, report.HintsKept, "a banned record's classification is neither written nor kept")
	require.Equal(t, 1, count(t, db, `SELECT count(*) FROM torrents`))
	require.Equal(t, 1, count(t, db, `SELECT count(*) FROM torrents WHERE info_hash = $1`, clean[:]))
	require.Zero(t, count(t, db, `SELECT count(*) FROM torrents_torrent_sources`))
	require.Zero(t, count(t, db, `SELECT count(*) FROM torrent_hints`))
	require.Zero(t, count(t, db, `SELECT count(*) FROM torrent_tags`))

	for _, hash := range []protocol.ID{byName, byFile} {
		record, err := ledger.Get(t.Context(), moderation.IdentifierForInfoHash(hash))
		require.NoError(t, err)
		require.Equal(t, moderation.Banned, record.Status)
	}
}

// queuedForClassification lists the infohashes of every pending classification job.
func queuedForClassification(t *testing.T, db *dbtest.DB) []protocol.ID {
	t.Helper()

	jobs, err := db.Query.QueueJob.WithContext(t.Context()).
		Where(db.Query.QueueJob.Queue.Eq(processor.MessageName)).
		Find()
	require.NoError(t, err)

	hashes := make([]protocol.ID, 0, len(jobs))

	for _, job := range jobs {
		var message processor.MessageParams
		require.NoError(t, json.Unmarshal([]byte(job.Payload), &message))
		hashes = append(hashes, message.InfoHashes...)
	}

	return hashes
}

// New and changed torrents are queued for classification; a torrent the import left as it
// was is not.
func TestExchangeImportQueuesNewAndChangedTorrentsForClassification(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	changed, unchanged := protocol.ID{1}, protocol.ID{2}
	seedTorrent(t, db, changed, "Changed", 10, false, "no_info")
	seedTorrent(t, db, unchanged, "Unchanged", 10, false, "no_info")

	importExchange(t, db, alice(),
		`{"infoHash":"`+hashHex(1)+`","name":"x","sources":[{"source":"dht","seeders":1}]}`,
		`{"infoHash":"`+hashHex(2)+`","name":"x"}`,
		`{"infoHash":"`+hashHex(3)+`","name":"New"}`,
	)

	require.ElementsMatch(t, []protocol.ID{changed, {3}}, queuedForClassification(t, db))
}
