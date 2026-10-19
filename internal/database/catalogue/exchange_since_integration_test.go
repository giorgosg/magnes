package catalogue_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/database/catalogue"
	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/moderation"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// The cursor every --since test exports from, and a time well before it.
var (
	since   = time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	longAgo = since.Add(-30 * 24 * time.Hour)
)

// seedSettled writes a torrent whose every row was last written at the given time: a
// multi-file list, a scraped dht source, a classification and a tag.
func seedSettled(t *testing.T, db *dbtest.DB, hash protocol.ID, name string, at time.Time) {
	t.Helper()

	exec(t, db, `INSERT INTO torrents (info_hash,name,size,private,files_status,files_count,created_at,updated_at)
VALUES ($1,$2,10,false,'multi',1,$3,$3)`, hash[:], name, at)
	exec(t, db, `INSERT INTO torrent_files (info_hash,index,path,size,created_at,updated_at)
VALUES ($1,0,$2,10,$3,$3)`, hash[:], name+"/a.bin", at)
	exec(t, db, `INSERT INTO torrent_sources (key,name,created_at,updated_at) VALUES ('dht','dht',now(),now())
ON CONFLICT DO NOTHING`)
	exec(t, db, `INSERT INTO torrents_torrent_sources (source,info_hash,seeders,leechers,scraped_at,created_at,
updated_at) VALUES ('dht',$1,3,1,$2,$2,$2)`, hash[:], at)
	exec(t, db, `INSERT INTO torrent_contents (info_hash,content_type,created_at,updated_at)
VALUES ($1,'movie',$2,$2)`, hash[:], at)
	exec(t, db, `INSERT INTO torrent_tags (info_hash,name,created_at,updated_at) VALUES ($1,'settled',$2,$2)`,
		hash[:], at)
}

// Each kind of change made at or after the cursor puts its torrent in the export, whole;
// a torrent nothing touched stays out. torrents.updated_at alone would miss the scrape
// and the reclassification, because neither writes the torrents row.
func TestExchangeExportSinceWritesEveryTorrentChangedSince(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	after := since.Add(time.Hour)
	unchanged, scraped, reclassified, metainfo, added := protocol.ID{1}, protocol.ID{2}, protocol.ID{3},
		protocol.ID{4}, protocol.ID{5}

	for hash, name := range map[protocol.ID]string{
		unchanged: "Unchanged", scraped: "Scraped", reclassified: "Reclassified", metainfo: "Metainfo",
	} {
		seedSettled(t, db, hash, name, longAgo)
	}

	// The crawler's scrape writes only the source row: counts, scraped_at, updated_at.
	exec(t, db, `UPDATE torrents_torrent_sources SET seeders = 40, scraped_at = $2, updated_at = $2
WHERE info_hash = $1`, scraped[:], after)
	// The classifier deletes the torrent's other torrent_contents rows and writes its own.
	exec(t, db, `DELETE FROM torrent_contents WHERE info_hash = $1`, reclassified[:])
	exec(t, db, `INSERT INTO torrent_contents (info_hash,content_type,created_at,updated_at)
VALUES ($1,'tv_show',$2,$2)`, reclassified[:], after)
	// A metainfo write, or an import filling an unknown file list, moves the torrents row.
	exec(t, db, `UPDATE torrents SET name = 'Metainfo renamed', updated_at = $2 WHERE info_hash = $1`,
		metainfo[:], after)
	seedSettled(t, db, added, "Added", after)

	out := exportExchange(t, db, catalogue.ExchangeOptions{Since: model.MaybeValid(since)})

	require.Equal(t, []protocol.ID{scraped, reclassified, metainfo, added}, out.order)
	require.Equal(t, model.MaybeValid[uint](40), out.records[scraped].Sources.Val[0].Seeders)
	require.Equal(t, model.MaybeValid(model.ContentTypeTvShow),
		out.records[reclassified].Magnes.Classification.Val.ContentType)
	require.Equal(t, "Metainfo renamed", out.records[metainfo].Name)

	// A changed torrent is written in full, as a full export would write it.
	full := exportExchange(t, db, catalogue.ExchangeOptions{})
	for _, hash := range out.order {
		require.Equal(t, full.records[hash], out.records[hash])
	}
}

// The header says what the file covers: since is the cursor the export was asked for.
func TestExchangeExportSinceWritesSinceInTheHeader(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	out := exportExchange(t, db, catalogue.ExchangeOptions{Since: model.MaybeValid(since)})

	require.Len(t, out.headers, 1)
	require.Equal(t, model.MaybeValid(since), out.headers[0].Since)
	require.True(t, out.headers[0].ExportedAt.Valid)
}

// updated_at is taken from the writer's clock before its transaction commits, so a row
// can commit after an export whose snapshot already passed its timestamp. The overlap
// re-reads that window: a row stamped just before the cursor is caught, inclusively at
// the window's start, while a row older than the window is not. The header still says
// the cursor that was asked for.
func TestExchangeExportSinceReReadsTheOverlap(t *testing.T) {
	t.Parallel()

	const overlap = time.Minute

	db := dbtest.New(t)
	atCursor, lateCommit, atWindowStart, beforeWindow := protocol.ID{1}, protocol.ID{2}, protocol.ID{3},
		protocol.ID{4}

	seedSettled(t, db, atCursor, "At the cursor", since)
	seedSettled(t, db, lateCommit, "Committed late", since.Add(-30*time.Second))
	seedSettled(t, db, atWindowStart, "At the window's start", since.Add(-overlap))
	seedSettled(t, db, beforeWindow, "Before the window", since.Add(-overlap-time.Microsecond))

	out := exportExchange(t, db, catalogue.ExchangeOptions{Since: model.MaybeValid(since), Overlap: overlap})
	require.Equal(t, []protocol.ID{atCursor, lateCommit, atWindowStart}, out.order)
	require.Equal(t, model.MaybeValid(since), out.headers[0].Since)

	exact := exportExchange(t, db, catalogue.ExchangeOptions{Since: model.MaybeValid(since)})
	require.Equal(t, []protocol.ID{atCursor}, exact.order, "with no overlap, since is inclusive")
}

// decide writes a moderation decision for a torrent, as the moderation service would,
// at an explicit time.
func decide(t *testing.T, db *dbtest.DB, hash protocol.ID, status string, at time.Time) {
	t.Helper()

	origin := "automatic"
	if status == "allowed" {
		origin = "moderator"
	}

	exec(t, db, `INSERT INTO moderation_decisions (identifier,status,origin,created_at,updated_at)
VALUES ($1,$2,$3,$4,$4)`, moderation.IdentifierForInfoHash(hash).String(), status, origin, at)
}

// A torrent held in quarantine was left out of every earlier export. Allowing it changes
// none of its rows, only its decision, and it must still reach the next incremental
// export: otherwise the receiver never hears of it.
func TestExchangeExportSinceWritesATorrentAllowedSince(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	allowed, allowedLongAgo, stillPending := protocol.ID{1}, protocol.ID{2}, protocol.ID{3}

	seedSettled(t, db, allowed, "Allowed", longAgo)
	decide(t, db, allowed, "allowed", since.Add(time.Hour))
	seedSettled(t, db, allowedLongAgo, "Allowed long ago", longAgo)
	decide(t, db, allowedLongAgo, "allowed", longAgo)
	seedSettled(t, db, stillPending, "Pending", longAgo)
	decide(t, db, stillPending, "pending", since.Add(time.Hour))

	out := exportExchange(t, db, catalogue.ExchangeOptions{Since: model.MaybeValid(since)})
	require.Equal(t, []protocol.ID{allowed}, out.order)
}

// Changes scattered across a catalogue many pages long all arrive, in infohash order, and
// nothing else does.
func TestExchangeExportSincePagesThroughSparseChanges(t *testing.T) {
	t.Parallel()

	const torrents = 600

	db := dbtest.New(t)
	exec(t, db, `INSERT INTO torrents (info_hash,name,size,private,created_at,updated_at)
SELECT decode(lpad(to_hex(i), 40, '0'), 'hex'), 'Torrent ' || i, i, false, $2, $2
FROM generate_series(1, $1) AS i`, torrents, longAgo)
	exec(t, db, `INSERT INTO torrent_sources (key,name,created_at,updated_at) VALUES ('dht','dht',now(),now())
ON CONFLICT DO NOTHING`)
	exec(t, db, `INSERT INTO torrents_torrent_sources (source,info_hash,seeders,created_at,updated_at)
SELECT 'dht', info_hash, 1, $1, $1 FROM torrents`, longAgo)
	// Every 7th torrent is scraped since, every 11th renamed, every 13th reclassified.
	exec(t, db, `UPDATE torrents_torrent_sources SET seeders = 2, updated_at = $1
WHERE get_byte(info_hash, 19) % 7 = 0`, since)
	exec(t, db, `UPDATE torrents SET updated_at = $1 WHERE get_byte(info_hash, 19) % 11 = 0`, since)
	exec(t, db, `INSERT INTO torrent_contents (info_hash,content_type,created_at,updated_at)
SELECT info_hash, 'movie', $1, $1 FROM torrents WHERE get_byte(info_hash, 19) % 13 = 0`, since)

	var want []protocol.ID

	for i := 1; i <= torrents; i++ {
		if b := byte(i); b%7 == 0 || b%11 == 0 || b%13 == 0 {
			hash := protocol.ID{18: byte(i >> 8), 19: b}
			want = append(want, hash)
		}
	}

	out := exportExchange(t, db, catalogue.ExchangeOptions{Since: model.MaybeValid(since), PageSize: 10})
	require.Equal(t, want, out.order)
}

// A table rewrite is not MVCC-safe: a snapshot taken before it commits sees the rewritten
// table as empty. An export whose snapshot predates a migration it then waits on would
// exit 0 having silently written nothing, and in a chain of incremental exports that
// window would never be sent. The export must wait for the rewrite before its snapshot.
func TestExchangeExportWaitsForATableRewriteBeforeItsSnapshot(t *testing.T) {
	t.Parallel()

	for name, options := range map[string]catalogue.ExchangeOptions{
		"full":        {},
		"incremental": {Since: model.MaybeValid(since)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)
			hash := protocol.ID{1}
			seedSettled(t, db, hash, "Rewritten", since)

			migration, err := db.Pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = migration.Rollback(context.Background()) }()

			// A volatile default forces PostgreSQL to rewrite the table.
			_, err = migration.Exec(t.Context(),
				`ALTER TABLE torrents ADD COLUMN rewrite_probe double precision DEFAULT random()`)
			require.NoError(t, err)

			config, err := pgx.ParseConfig(db.DSN)
			require.NoError(t, err)
			conn, err := pgx.ConnectConfig(t.Context(), config)
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close(context.Background()) })

			exportPID := conn.PgConn().PID()

			var output bytes.Buffer

			done := make(chan error, 1)

			go func() { done <- catalogue.ExportExchange(t.Context(), conn, &output, options) }()

			// Commit the rewrite only once the export is blocked on it.
			require.Eventually(t, func() bool {
				var waiting bool

				_ = db.Pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
 WHERE pid = $1 AND wait_event_type = 'Lock')`, exportPID).Scan(&waiting)

				return waiting
			}, 10*time.Second, 10*time.Millisecond, "the export never waited on the rewrite")
			require.NoError(t, migration.Commit(t.Context()))
			require.NoError(t, <-done)

			result, err := exchange.Check(&output)
			require.NoError(t, err)
			require.Equal(t, 1, result.Records, "the export read the rewritten table as empty")
		})
	}
}

// Tags are insert-only, so a new tag carries its own time. It is a change to the file only
// when the file carries tags: without --include-tags the record would be identical to the
// one already sent.
func TestExchangeExportSinceCountsANewTagOnlyWithTags(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	tagged := protocol.ID{1}

	seedSettled(t, db, tagged, "Tagged", longAgo)
	exec(t, db, `INSERT INTO torrent_tags (info_hash,name,created_at,updated_at) VALUES ($1,'fresh',$2,$2)`,
		tagged[:], since.Add(time.Hour))

	with := exportExchange(t, db, catalogue.ExchangeOptions{Since: model.MaybeValid(since), IncludeTags: true})
	require.Equal(t, []protocol.ID{tagged}, with.order)
	require.Equal(t, model.MaybeValid([]string{"fresh", "settled"}), with.records[tagged].Magnes.Tags)

	without := exportExchange(t, db, catalogue.ExchangeOptions{Since: model.MaybeValid(since)})
	require.Empty(t, without.order)
}
