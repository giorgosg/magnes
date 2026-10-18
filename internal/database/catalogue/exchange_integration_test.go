package catalogue_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/database/catalogue"
	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// exported is the output of an exchange-file export, decoded with exchange.Reader.
type exported struct {
	raw     string
	headers []exchange.Header
	records map[protocol.ID]exchange.Record
	order   []protocol.ID
}

// exportExchange runs the export and decodes its output, failing on anything the reader
// would skip, drop or report: what the export writes must read back unchanged.
func exportExchange(t *testing.T, db *dbtest.DB, options catalogue.ExchangeOptions) exported {
	t.Helper()

	var output bytes.Buffer
	require.NoError(t, catalogue.ExportExchange(t.Context(), db.Pool, &output, options))

	result := exported{raw: output.String(), records: make(map[protocol.ID]exchange.Record)}
	reader := exchange.NewReader(bytes.NewReader(output.Bytes()))

	for {
		line, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		require.NoError(t, err)

		if line.Header != nil {
			require.Empty(t, result.order, "a header after the first record")
			result.headers = append(result.headers, *line.Header)

			continue
		}

		_, duplicate := result.records[line.Record.InfoHash]
		require.False(t, duplicate, "record %s written twice", line.Record.InfoHash)
		result.records[line.Record.InfoHash] = *line.Record
		result.order = append(result.order, line.Record.InfoHash)
	}

	require.Empty(t, reader.Report(), "the export wrote something a reader drops")

	return result
}

func exec(t *testing.T, db *dbtest.DB, sql string, args ...any) {
	t.Helper()

	_, err := db.Pool.Exec(t.Context(), sql, args...)
	require.NoError(t, err)
}

func TestExchangeExportWritesOneHeaderAndOnlyVisibleTorrents(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	hashes := dbtest.ModerationCatalogue(t, db)

	out := exportExchange(t, db, catalogue.ExchangeOptions{
		Producer: exchange.Producer{
			Program: model.MaybeValid("magnes"),
			Version: model.MaybeValid("v1.2.3"),
		},
	})

	require.Len(t, out.headers, 1)
	require.Equal(t, exchange.CurrentVersion, out.headers[0].Version)
	require.Equal(t, model.MaybeValid(exchange.Producer{
		Program: model.MaybeValid("magnes"),
		Version: model.MaybeValid("v1.2.3"),
	}), out.headers[0].Producer)
	require.True(t, out.headers[0].ExportedAt.Valid)
	require.False(t, out.headers[0].Since.Valid, "a full export has no since")

	// Visible and Allowed travel; the quarantined (pending) and banned torrents never do.
	require.Equal(t, []protocol.ID{hashes[0], hashes[2]}, out.order)
	require.Equal(t, "Visible", out.records[hashes[0]].Name)
	require.Equal(t, "Allowed", out.records[hashes[2]].Name)
	require.NotContains(t, out.raw, "Pending")
	require.NotContains(t, out.raw, "Banned")
}

func insertTorrent(t *testing.T, db *dbtest.DB, hash protocol.ID, name string, size int64, filesStatus string) {
	t.Helper()

	exec(t, db, `INSERT INTO torrents (info_hash,name,size,private,files_status,created_at,updated_at)
VALUES ($1,$2,$3,false,$4,now(),now())`, hash[:], name, size, filesStatus)
}

func insertFile(t *testing.T, db *dbtest.DB, hash protocol.ID, index int, path string, size int64) {
	t.Helper()

	exec(t, db, `INSERT INTO torrent_files (info_hash,index,path,size,created_at,updated_at)
VALUES ($1,$2,$3,$4,now(),now())`, hash[:], index, path, size)
}

func TestExchangeExportWritesFilesByFilesStatus(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	noInfo, single, multi, partial := protocol.ID{1}, protocol.ID{2}, protocol.ID{3}, protocol.ID{4}

	insertTorrent(t, db, noInfo, "No info", 10, "no_info")
	// A stray row does not make an unknown list known.
	insertFile(t, db, noInfo, 0, "stray.bin", 10)
	insertTorrent(t, db, single, "Single.mkv", 4096, "single")
	insertTorrent(t, db, multi, "Multi", 300, "multi")
	insertFile(t, db, multi, 2, "Multi/c.txt", 100)
	insertFile(t, db, multi, 0, "Multi/a.txt", 120)
	insertFile(t, db, multi, 1, "Multi/b.txt", 80)
	insertTorrent(t, db, partial, "Partial", 9000, "over_threshold")
	insertFile(t, db, partial, 0, "Partial/first.bin", 1000)
	insertFile(t, db, partial, 3, "Partial/fourth.bin", 2000)

	out := exportExchange(t, db, catalogue.ExchangeOptions{})

	require.False(t, out.records[noInfo].Files.Valid, "no_info has no files")
	require.False(t, out.records[noInfo].FilesComplete.Valid)

	require.Equal(t, model.MaybeValid([]exchange.File{{Index: 0, Path: "Single.mkv", Size: 4096}}),
		out.records[single].Files)
	require.False(t, out.records[single].FilesComplete.Valid)

	require.Equal(t, model.MaybeValid([]exchange.File{
		{Index: 0, Path: "Multi/a.txt", Size: 120},
		{Index: 1, Path: "Multi/b.txt", Size: 80},
		{Index: 2, Path: "Multi/c.txt", Size: 100},
	}), out.records[multi].Files)
	require.False(t, out.records[multi].FilesComplete.Valid)

	require.Equal(t, model.MaybeValid([]exchange.File{
		{Index: 0, Path: "Partial/first.bin", Size: 1000},
		{Index: 3, Path: "Partial/fourth.bin", Size: 2000},
	}), out.records[partial].Files)
	require.Equal(t, model.MaybeValid(false), out.records[partial].FilesComplete)
}

func insertSource(
	t *testing.T, db *dbtest.DB, hash protocol.ID, source string, seeders, leechers *int, publishedAt *time.Time,
) {
	t.Helper()

	exec(t, db, `INSERT INTO torrent_sources (key,name,created_at,updated_at) VALUES ($1,$1,now(),now())
ON CONFLICT DO NOTHING`, source)
	exec(t, db, `INSERT INTO torrents_torrent_sources (source,info_hash,seeders,leechers,published_at,created_at,
updated_at) VALUES ($1,$2,$3,$4,$5,now(),now())`, source, hash[:], seeders, leechers, publishedAt)
}

func ptr[T any](v T) *T { return &v }

func TestExchangeExportWritesSources(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	hash, unsourced := protocol.ID{1}, protocol.ID{2}
	early := time.Date(2023, 5, 30, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 9, 2, 17, 14, 9, 0, time.UTC)

	insertTorrent(t, db, hash, "Sourced", 10, "no_info")
	insertSource(t, db, hash, "dht", ptr(41), ptr(7), &late)
	insertSource(t, db, hash, "alice:rarbg", nil, nil, &early)
	// A known zero is a count; only an unknown one is left out.
	insertSource(t, db, hash, "Torrents CSV", ptr(0), nil, nil)
	insertTorrent(t, db, unsourced, "Unsourced", 10, "no_info")

	out := exportExchange(t, db, catalogue.ExchangeOptions{})

	require.Equal(t, model.MaybeValid([]exchange.Source{
		{Name: "alice:rarbg", PublishedAt: model.MaybeValid(early)},
		{
			Name: "dht", Seeders: model.MaybeValid[uint](41), Leechers: model.MaybeValid[uint](7),
			PublishedAt: model.MaybeValid(late),
		},
		{Name: "torrents-csv", Seeders: model.MaybeValid[uint](0)},
	}), out.records[hash].Sources)
	require.Equal(t, model.MaybeValid(early), out.records[hash].PublishedAt,
		"the record's publishedAt is the earliest across its sources")

	require.False(t, out.records[unsourced].Sources.Valid)
	require.False(t, out.records[unsourced].PublishedAt.Valid)

	require.NotContains(t, out.raw, `"leechers":0`, "an unknown peer count is never written as 0")
	require.NotContains(t, out.raw, "Torrents CSV")
}

// Two stored names that normalise to one are one source in the file: the newest row with
// a known count supplies the counts, and the earliest publication wins.
func TestExchangeExportMergesSourcesThatNormaliseAlike(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	fallback, newer := protocol.ID{1}, protocol.ID{2}
	early := time.Date(2023, 5, 30, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	insertTorrent(t, db, fallback, "Fallback", 10, "no_info")
	insertSource(t, db, fallback, "Torrents CSV", ptr(3), ptr(1), &late)
	insertSource(t, db, fallback, "torrents-csv", nil, nil, &early)
	exec(t, db, `UPDATE torrents_torrent_sources SET updated_at = now() - interval '1 day'
WHERE info_hash = $1 AND source = 'Torrents CSV'`, fallback[:])

	insertTorrent(t, db, newer, "Newer", 10, "no_info")
	insertSource(t, db, newer, "Torrents CSV", ptr(3), ptr(1), &early)
	insertSource(t, db, newer, "torrents-csv", ptr(9), nil, &late)
	exec(t, db, `UPDATE torrents_torrent_sources SET updated_at = now() - interval '1 day'
WHERE info_hash = $1 AND source = 'Torrents CSV'`, newer[:])

	out := exportExchange(t, db, catalogue.ExchangeOptions{})

	require.Equal(t, model.MaybeValid([]exchange.Source{{
		Name: "torrents-csv", Seeders: model.MaybeValid[uint](3), Leechers: model.MaybeValid[uint](1),
		PublishedAt: model.MaybeValid(early),
	}}), out.records[fallback].Sources)
	require.Equal(t, model.MaybeValid([]exchange.Source{{
		Name: "torrents-csv", Seeders: model.MaybeValid[uint](9), PublishedAt: model.MaybeValid(early),
	}}), out.records[newer].Sources, "counts travel as a pair from the newest row that has one")
}

func TestExchangeExportWritesTheCurrentClassification(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	movie, show, unclassified, unknown := protocol.ID{1}, protocol.ID{2}, protocol.ID{3}, protocol.ID{4}

	insertTorrent(t, db, movie, "Example.Release.2021.1080p.WEB-DL.x264-GRP", 10, "no_info")
	// The work's own catalogue title and year never travel.
	exec(t, db, `INSERT INTO content (type,source,id,title,release_year,created_at,updated_at)
VALUES ('movie','tmdb','123456','Catalogue Title',1999,now(),now())`)
	exec(t, db, `INSERT INTO torrent_contents (info_hash,content_type,content_source,content_id,inferred_title,
inferred_year,languages,video_resolution,video_source,video_codec,video_3d,video_modifier,release_group,
created_at,updated_at)
VALUES ($1,'movie','tmdb','123456','Example Release',2021,'["en","fr"]',$2,$3,$4,$5,$6,'GRP',now(),now())`,
		movie[:], model.VideoResolutionV1080p, model.VideoSourceWEBDL, model.VideoCodecX264, model.Video3DV3D,
		model.VideoModifierREMUX)

	insertTorrent(t, db, show, "Show.S01E02-E03.S02", 10, "no_info")
	// An empty title, an empty language list and no work are all unknown.
	exec(t, db, `INSERT INTO torrent_contents (info_hash,content_type,languages,episodes,created_at,updated_at)
VALUES ($1,'tv_show','[]','{"1":{"2":{},"3":{}},"2":{}}',now(),now())`, show[:])

	insertTorrent(t, db, unclassified, "Unclassified", 10, "no_info")

	// A row that knows nothing is no classification at all.
	insertTorrent(t, db, unknown, "Unknown", 10, "no_info")
	exec(t, db, `INSERT INTO torrent_contents (info_hash,languages,created_at,updated_at)
VALUES ($1,'[]',now(),now())`, unknown[:])

	out := exportExchange(t, db, catalogue.ExchangeOptions{})

	require.Equal(t, model.MaybeValid(exchange.Classification{
		ContentType:   model.MaybeValid(model.ContentTypeMovie),
		Work:          model.MaybeValid(exchange.Work{Source: "tmdb", ID: "123456"}),
		InferredTitle: model.MaybeValid("Example Release"),
		InferredYear:  model.MaybeValid(model.Year(2021)),
		Languages:     model.MaybeValid([]model.Language{"en", "fr"}),
		Video: exchange.Video{
			Resolution: model.MaybeValid(model.VideoResolutionV1080p),
			Source:     model.MaybeValid(model.VideoSourceWEBDL),
			Codec:      model.MaybeValid(model.VideoCodecX264),
			ThreeD:     model.MaybeValid(model.Video3DV3D),
			Modifier:   model.MaybeValid(model.VideoModifierREMUX),
		},
		ReleaseGroup: model.MaybeValid("GRP"),
	}), out.records[movie].Magnes.Classification)

	require.Equal(t, model.MaybeValid(exchange.Classification{
		ContentType: model.MaybeValid(model.ContentTypeTvShow),
		Episodes: model.MaybeValid([]exchange.SeasonEpisodes{
			{Season: 1, Episodes: []int{2, 3}},
			{Season: 2, WholeSeason: true},
		}),
	}), out.records[show].Magnes.Classification)

	require.False(t, out.records[unclassified].Magnes.Classification.Valid)
	require.False(t, out.records[unknown].Magnes.Classification.Valid)
	require.NotContains(t, out.raw, `"magnes":{}`)
	require.NotContains(t, out.raw, `"classification":{}`)
	require.NotContains(t, out.raw, "Catalogue Title")
	require.NotContains(t, out.raw, "1999")
}

func TestExchangeExportWritesTagsOnlyWhenAskedTo(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	tagged, untagged := protocol.ID{1}, protocol.ID{2}

	insertTorrent(t, db, tagged, "Tagged", 10, "no_info")
	exec(t, db, `INSERT INTO torrent_tags (info_hash,name,created_at,updated_at)
VALUES ($1,'zeta',now(),now()), ($1,'4k-candidate',now(),now())`, tagged[:])
	insertTorrent(t, db, untagged, "Untagged", 10, "no_info")

	without := exportExchange(t, db, catalogue.ExchangeOptions{})
	require.NotContains(t, without.raw, `"tags"`, "tags are opt-in")
	require.NotContains(t, without.raw, "4k-candidate")
	require.NotContains(t, without.raw, `"magnes"`)

	with := exportExchange(t, db, catalogue.ExchangeOptions{IncludeTags: true})
	require.Equal(t, model.MaybeValid([]string{"4k-candidate", "zeta"}), with.records[tagged].Magnes.Tags)
	require.False(t, with.records[untagged].Magnes.Tags.Valid)
	require.NotContains(t, with.raw, `"tags":[]`)
}

// An internal value the format has no spelling for stops the export, naming the value,
// rather than leaking it or quietly dropping it.
func TestExchangeExportFailsOnAnUnmappedValue(t *testing.T) {
	t.Parallel()

	classified := func(columns, values string) func(*testing.T, *dbtest.DB, protocol.ID) {
		return func(t *testing.T, db *dbtest.DB, hash protocol.ID) {
			t.Helper()
			exec(t, db, `INSERT INTO torrent_contents (info_hash,`+columns+`,created_at,updated_at)
VALUES ($1,`+values+`,now(),now())`, hash[:])
		}
	}

	for name, tc := range map[string]struct {
		seed func(*testing.T, *dbtest.DB, protocol.ID)
		want string
	}{
		"content type":     {classified("content_type", "'hologram'"), `contentType "hologram"`},
		"video resolution": {classified("video_resolution", "'V999p'"), `video.resolution "V999p"`},
		"language":         {classified("languages", `'["en","klingon"]'`), `language "klingon"`},
		"source name": {
			func(t *testing.T, db *dbtest.DB, hash protocol.ID) {
				t.Helper()
				insertSource(t, db, hash, "???", nil, nil, nil)
			},
			`"???"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)
			hash := protocol.ID{7}

			insertTorrent(t, db, hash, "Unmapped", 10, "no_info")
			tc.seed(t, db, hash)

			var output bytes.Buffer

			err := catalogue.ExportExchange(t.Context(), db.Pool, &output, catalogue.ExchangeOptions{})
			require.ErrorContains(t, err, tc.want)
			require.ErrorContains(t, err, hash.String(), "the error names the torrent")
		})
	}
}

// rowCounter records the largest result any one query returned on a connection.
type rowCounter struct {
	queries int
	maxRows int64
}

func (c *rowCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.queries++
	return ctx
}

func (c *rowCounter) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	c.maxRows = max(c.maxRows, data.CommandTag.RowsAffected())
}

// Memory stays flat: the export reads the catalogue in pages, so in this test no query
// returns more than a page of rows, however many torrents there are. Each torrent here
// has one source and no files, which bounds every per-page query by the page size; with
// files, a page holds up to the page size times the file-saving threshold.
func TestExchangeExportPagesThroughTheCatalogue(t *testing.T) {
	t.Parallel()

	const (
		torrents = 2500
		pageSize = 100
	)

	db := dbtest.New(t)
	exec(t, db, `INSERT INTO torrents (info_hash,name,size,private,created_at,updated_at)
SELECT decode(lpad(to_hex(i), 40, '0'), 'hex'), 'Torrent ' || i, i, false, now(), now()
FROM generate_series(1, $1) AS i`, torrents)
	exec(t, db, `INSERT INTO torrent_sources (key,name,created_at,updated_at) VALUES ('dht','dht',now(),now())
ON CONFLICT DO NOTHING`)
	exec(t, db, `INSERT INTO torrents_torrent_sources (source,info_hash,seeders,created_at,updated_at)
SELECT 'dht', info_hash, 1, now(), now() FROM torrents`)

	config, err := pgx.ParseConfig(db.DSN)
	require.NoError(t, err)

	counter := &rowCounter{}
	config.Tracer = counter
	conn, err := pgx.ConnectConfig(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	var output bytes.Buffer
	require.NoError(t, catalogue.ExportExchange(t.Context(), conn, &output, catalogue.ExchangeOptions{
		PageSize: pageSize,
	}))

	result, err := exchange.Check(&output)
	require.NoError(t, err)
	require.Equal(t, torrents, result.Records)
	require.LessOrEqual(t, counter.maxRows, int64(pageSize), "a query returned more than one page")
	require.GreaterOrEqual(t, counter.queries, torrents/pageSize, "the export did not page")
}

// The classifier and enhancement both replace a torrent's row rather than add one, but
// the schema allows several. The most recently written one is current.
func TestExchangeExportPicksTheNewestClassificationRow(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	hash := protocol.ID{1}

	insertTorrent(t, db, hash, "Twice", 10, "no_info")
	exec(t, db, `INSERT INTO torrent_contents (info_hash,content_type,created_at,updated_at)
VALUES ($1,'movie',now(),now() - interval '1 day'), ($1,'music',now(),now()),
($1,'ebook',now(),now() - interval '2 days')`,
		hash[:])

	out := exportExchange(t, db, catalogue.ExchangeOptions{})

	require.Len(t, out.order, 1)
	require.Equal(t, model.MaybeValid(model.ContentTypeMusic),
		out.records[hash].Magnes.Classification.Val.ContentType)
}
