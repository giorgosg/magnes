package importer_test

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/database/catalogue"
	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/stretchr/testify/require"
)

func exportAll(t *testing.T, db *dbtest.DB) []byte {
	t.Helper()

	var out bytes.Buffer
	require.NoError(t, catalogue.ExportExchange(t.Context(), db.Pool, &out, catalogue.ExchangeOptions{
		IncludeTags: true,
	}))

	return out.Bytes()
}

func readRecords(t *testing.T, file []byte) []exchange.Record {
	t.Helper()

	var records []exchange.Record

	reader := exchange.NewReader(bytes.NewReader(file))

	for {
		line, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		require.NoError(t, err)

		if line.Record != nil {
			records = append(records, *line.Record)
		}
	}

	require.Empty(t, reader.Report())

	return records
}

// Exporting, importing into an empty catalogue and exporting again gives the same file,
// except that every source gains the origin prefix, and the classification, which arrives
// as a hint for the receiver's own classifier, is not yet anyone's classification.
func TestExchangeImportRoundTripsAnExport(t *testing.T) {
	t.Parallel()

	sender, receiver := dbtest.New(t), dbtest.New(t)
	single, multi, over, bare := protocol.ID{1}, protocol.ID{2}, protocol.ID{3}, protocol.ID{4}
	published := time.Date(2023, 5, 30, 0, 0, 0, 0, time.UTC)
	scraped := time.Date(2026, 10, 17, 3, 12, 44, 0, time.UTC)

	seedTorrent(t, sender, single, "Example.Release.2021.1080p.mkv", 4831838208, false, "single")
	seedSource(t, sender, single, "dht", ptr(41), ptr(7), &scraped)
	seedSource(t, sender, single, "Torrents CSV", nil, nil, nil)
	exec(
		t,
		sender,
		`UPDATE torrents_torrent_sources SET published_at = $2 WHERE info_hash = $1`,
		single[:],
		published,
	)
	exec(
		t,
		sender,
		`INSERT INTO torrent_tags (info_hash,name,created_at,updated_at) VALUES ($1,'keep',now(),now())`,
		single[:],
	)
	exec(t, sender, `INSERT INTO content (type,source,id,title,created_at,updated_at)
VALUES ('movie','tmdb','123456','Catalogue Title',now(),now())`)
	exec(t, sender, `INSERT INTO torrent_contents (info_hash,content_type,content_source,content_id,inferred_title,
inferred_year,languages,video_resolution,video_source,release_group,created_at,updated_at)
VALUES ($1,'movie','tmdb','123456','Example Release',2021,'["en"]','V1080p','WEBDL','GRP',now(),now())`, single[:])

	seedTorrent(t, sender, multi, "Multi", 30, true, "multi")
	exec(t, sender, `UPDATE torrents SET files_count = 2 WHERE info_hash = $1`, multi[:])
	exec(t, sender, `INSERT INTO torrent_files (info_hash,index,path,size,created_at,updated_at)
VALUES ($1,0,'a/one.mkv',20,now(),now()), ($1,1,'a/two.srt',10,now(),now())`, multi[:])
	seedSource(t, sender, multi, "dht", nil, nil, nil)

	seedTorrent(t, sender, over, "Over", 100, false, "over_threshold")
	exec(t, sender, `UPDATE torrents SET files_count = 5 WHERE info_hash = $1`, over[:])
	exec(t, sender, `INSERT INTO torrent_files (info_hash,index,path,size,created_at,updated_at)
VALUES ($1,0,'1',20,now(),now()), ($1,1,'2',20,now(),now())`, over[:])
	seedSource(t, sender, over, "alice:rarbg", ptr(3), nil, nil)

	seedTorrent(t, sender, bare, "Bare", 0, false, "no_info")

	first := exportAll(t, sender)

	report, err := newExchangeImporter(receiver).Import(t.Context(), bytes.NewReader(first), alice())
	require.NoError(t, err)
	require.Equal(t, 4, report.New)
	require.Empty(t, report.Dropped)

	want := readRecords(t, first)
	for i := range want {
		for j := range want[i].Sources.Val {
			want[i].Sources.Val[j].Name = "alice:" + want[i].Sources.Val[j].Name
		}

		want[i].Magnes.Classification = model.Maybe[exchange.Classification]{}
	}

	require.Len(t, want, 4)
	require.Equal(t, want, readRecords(t, exportAll(t, receiver)))

	require.Equal(t, &hintRow{
		ContentType: ptr("movie"), ContentSource: ptr("tmdb"), ContentID: ptr("123456"),
		Title: ptr("Example Release"), ReleaseYear: ptr(2021), Languages: ptr(`["en"]`),
		Resolution: ptr("V1080p"), Source: ptr("WEBDL"), ReleaseGroup: ptr("GRP"),
	}, hintOf(t, receiver, single), "the classification arrives as the hint")
}
