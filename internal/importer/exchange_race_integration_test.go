package importer_test

import (
	"context"
	"testing"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// holdRow starts a transaction that writes to a row and keeps it open, as a crawler batch
// does while it writes the same torrent. The import must wait for it, then judge its own
// write against what that transaction committed.
func holdRow(t *testing.T, db *dbtest.DB, sql string, args ...any) pgx.Tx {
	t.Helper()

	tx, err := db.Pool.Begin(t.Context())
	require.NoError(t, err)

	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	_, err = tx.Exec(t.Context(), sql, args...)
	require.NoError(t, err)

	return tx
}

// importWhileHeld starts the import, waits until it is blocked on the held row, commits
// the holder, and returns the import's result.
func importWhileHeld(t *testing.T, db *dbtest.DB, holder pgx.Tx, records ...string) importResult {
	t.Helper()

	done := startImport(t.Context(), db, exchangeFile(records...))

	waitForLockWaits(t, db, 1, "the import should wait for the concurrent write")

	require.NoError(t, holder.Commit(t.Context()))

	select {
	case result := <-done:
		require.NoError(t, result.err)
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("the import did not finish after the concurrent write committed")
		return importResult{}
	}
}

// A concurrent write that completes a torrent's file list first wins: the import read the
// list as unknown, but it is no longer unknown when its write lands.
func TestExchangeImportDoesNotOverwriteAFileListCompletedConcurrently(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	hash := protocol.ID{1}
	seedTorrent(t, db, hash, "Racing", 10, false, "no_info")

	holder := holdRow(
		t,
		db,
		`UPDATE torrents SET files_status = 'multi', files_count = 1 WHERE info_hash = $1`,
		hash[:],
	)
	_, err := holder.Exec(t.Context(), `INSERT INTO torrent_files (info_hash,index,path,size,created_at,updated_at)
VALUES ($1,0,'crawled',10,now(),now())`, hash[:])
	require.NoError(t, err)

	result := importWhileHeld(t, db, holder, `{"infoHash":"`+hashHex(1)+`","name":"x",
"files":[{"index":0,"path":"a","size":4},{"index":1,"path":"b","size":6}]}`)

	require.Equal(t, torrentRow{Name: "Racing", Size: 10, FilesStatus: "multi", FilesCount: ptr(1)},
		torrentRowOf(t, db, hash))
	require.Equal(t, []string{"0:crawled:10"}, filePaths(t, db, hash))
	require.Equal(t, 1, result.report.Unchanged)
}

// A concurrent write of newer counts to the same stored source wins over the import's
// older ones, and the import's known counts still fill what that write left unknown.
func TestExchangeImportMergesWithCountsWrittenConcurrently(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	hash := protocol.ID{1}
	older := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)

	seedTorrent(t, db, hash, "Racing", 10, false, "no_info")
	seedSource(t, db, hash, "alice:dht", nil, nil, nil)

	holder := holdRow(t, db, `UPDATE torrents_torrent_sources SET seeders = 50, scraped_at = $2
WHERE info_hash = $1 AND source = 'alice:dht'`, hash[:], newer)

	importWhileHeld(t, db, holder, `{"infoHash":"`+hashHex(1)+`","name":"x",
"sources":[{"source":"dht","seeders":40,"leechers":2,"scrapedAt":"`+older.Format(time.RFC3339)+`"}]}`)

	require.Equal(t, map[string]sourceRow{"alice:dht": {Seeders: ptr(50), Leechers: ptr(2), ScrapedAt: &newer}},
		sourceRows(t, db, hash))
}

// A file may carry one infohash more than once, as when exports are concatenated. Each
// record merges in order, as if the file were read one record at a time.
func TestExchangeImportMergesARepeatedRecordInOrder(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	hash := protocol.ID{1}

	report := importExchange(t, db, alice(),
		`{"infoHash":"`+hashHex(1)+`","name":"First","sources":[{"source":"dht","seeders":1,
"scrapedAt":"2026-10-01T00:00:00Z"}],"magnes":{"tags":["one"]}}`,
		`{"infoHash":"`+hashHex(1)+`","name":"Second","sources":[{"source":"dht","seeders":2,
"scrapedAt":"2026-10-17T00:00:00Z"}],"magnes":{"tags":["two"]}}`,
	)

	require.Equal(t, "First", torrentRowOf(t, db, hash).Name)
	require.Equal(t, map[string]sourceRow{"alice:dht": {
		Seeders: ptr(2), ScrapedAt: ptr(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)),
	}}, sourceRows(t, db, hash))
	require.Equal(t, []string{"one", "two"}, tagsOf(t, db, hash))
	require.Equal(t, 2, report.Records)
	require.Equal(t, 1, report.New)
	require.Equal(t, 1, report.Updated)
}

// Importing the same file again changes nothing and queues nothing new.
func TestExchangeImportIsIdempotent(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	full := `{"infoHash":"` + hashHex(1) + `","name":"x",
"files":[{"index":0,"path":"a","size":1},{"index":1,"path":"b","size":1}],
"sources":[{"source":"dht","seeders":3,"leechers":1,
"publishedAt":"2026-01-01T00:00:00Z","scrapedAt":"2026-10-17T00:00:00Z"}],
"magnes":{"tags":["t"],"classification":{"contentType":"movie"}}}`
	partial := `{"infoHash":"` + hashHex(2) + `","name":"y",
"filesComplete":false,"files":[{"index":0,"path":"a","size":1}]}`
	records := []string{full, partial}

	importExchange(t, db, alice(), records...)
	queued := len(queuedForClassification(t, db))

	report := importExchange(t, db, alice(), records...)
	require.Equal(t, 2, report.Unchanged)
	require.Zero(t, report.New+report.Updated+report.TagsAdded+report.HintsWritten)
	require.Len(t, queuedForClassification(t, db), queued)
}

// A torrent being deleted while the import writes it must cost nothing but that torrent's
// old row: the import waits for the delete, then writes the record as a new torrent, and the
// rest of the batch is written. A foreign key failure would roll the whole batch back.
func TestExchangeImportWaitsForAnInFlightDelete(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	deleted := protocol.ID{2}
	seedTorrent(t, db, deleted, "Deleted", 10, false, "no_info")

	holder := holdRow(t, db, `DELETE FROM torrents WHERE info_hash = $1`, deleted[:])

	done := make(chan importResult, 1)

	go func() {
		report, err := newExchangeImporter(db).Import(t.Context(), exchangeFile(
			`{"infoHash":"`+hashHex(1)+`","name":"Other","sources":[{"source":"dht"}]}`,
			`{"infoHash":"`+hashHex(2)+`","name":"Again","sources":[{"source":"dht","seeders":1}],
"magnes":{"tags":["t"]}}`,
		), alice())
		done <- importResult{report, err}
	}()

	waitForLockWaits(t, db, 1, "the import should wait for the delete")

	require.NoError(t, holder.Commit(t.Context()))

	var result importResult
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the import did not finish after the delete committed")
	}

	require.NoError(t, result.err)
	require.Equal(t, 2, result.report.New)
	require.Equal(t, "Again", torrentRowOf(t, db, deleted).Name)
	require.Equal(t, map[string]sourceRow{"alice:dht": {Seeders: ptr(1)}}, sourceRows(t, db, deleted))
	require.Equal(t, []string{"t"}, tagsOf(t, db, deleted))
}

// waitForLockWaits waits until n backends of the test database are waiting on a lock.
func waitForLockWaits(t *testing.T, db *dbtest.DB, n int, why string) {
	t.Helper()

	require.Eventually(t, func() bool {
		return count(t, db, lockWaits, db.Name) >= n
	}, 5*time.Second, 10*time.Millisecond, why)
}

// lockWaits counts the backends of one database that are waiting on a lock.
const lockWaits = `SELECT count(*) FROM pg_stat_activity WHERE datname = $1 AND wait_event_type = 'Lock'`

// The enhancement scheduler claims a torrent and then updates it: the claim's foreign key
// takes KEY SHARE on the torrent, and the update takes NO KEY UPDATE. The import's source
// write fires enhancement_source_rank, which takes FOR UPDATE on the same torrent. If the
// import already held NO KEY UPDATE, the scheduler's update would wait on the import while
// the import waits on the claim's KEY SHARE: a deadlock that aborts one of them. Holding
// only KEY SHARE, as the crawler does, lets the scheduler finish first.
func TestExchangeImportDoesNotDeadlockWithTheEnhancementScheduler(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	hash := protocol.ID{1}
	seedTorrent(t, db, hash, "Contended", 10, false, "no_info")

	// Pause the import after it has locked the torrent and before it writes the source,
	// on the torrent_sources row it is about to create.
	pause := holdRow(t, db, `INSERT INTO torrent_sources (key,name,created_at,updated_at)
VALUES ('alice:dht','alice:dht',now(),now())`)

	done := startImport(t.Context(), db, exchangeFile(
		`{"infoHash":"`+hashHex(1)+`","name":"x","sources":[{"source":"dht","seeders":1}]}`))

	waitForLockWaits(t, db, 1, "the import should wait on the paused source key")

	scheduler := holdRow(t, db, `INSERT INTO enhancement_claims (info_hash,token,expires_at)
VALUES ($1,'claim',now() + interval '1 hour')`, hash[:])

	updated := make(chan error, 1)

	go func() {
		_, err := scheduler.Exec(
			t.Context(),
			`UPDATE torrents SET enhancement_ready=false,enhancement_checked_at=now() WHERE info_hash=$1`,
			hash[:],
		)
		updated <- err
	}()

	select {
	case err := <-updated:
		require.NoError(t, err)
	case <-time.After(500 * time.Millisecond):
		// Waiting on the import: release it, and let the deadlock, if any, be detected.
		require.NoError(t, pause.Rollback(t.Context()))
		require.NoError(t, <-updated, "the scheduler's update must not deadlock with the import")
	}

	_ = pause.Rollback(t.Context())

	require.NoError(t, scheduler.Commit(t.Context()))

	select {
	case result := <-done:
		require.NoError(t, result.err)
		require.Equal(t, 1, result.report.Updated)
	case <-time.After(5 * time.Second):
		t.Fatal("the import did not finish")
	}
}

// Writers that take no moderation lock, such as the classifier deleting torrents or the
// enhancement scheduler's maintenance, lock torrents in their own order, so a deadlock with
// an import can still happen. PostgreSQL then aborts one transaction; when that is the
// import's batch, the batch is retried rather than failing a long import.
func TestExchangeImportRetriesABatchAbortedByADeadlock(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	hash := protocol.ID{1}
	seedTorrent(t, db, hash, "Contended", 10, false, "no_info")

	// The import locks the torrent, then waits on this uncommitted source key.
	other := holdRow(t, db, `INSERT INTO torrent_sources (key,name,created_at,updated_at)
VALUES ('alice:dht','alice:dht',now(),now())`)

	done := startImport(t.Context(), db, exchangeFile(
		`{"infoHash":"`+hashHex(1)+`","name":"x","sources":[{"source":"dht","seeders":7}]}`))

	waitForLockWaits(t, db, 1, "the import should wait on the uncommitted source key")

	// Now the other transaction waits on the import's lock on the torrent: a cycle. The
	// import has waited longer, so its backend detects the deadlock and aborts itself.
	locked := make(chan error, 1)

	go func() {
		_, err := other.Exec(t.Context(), `SELECT 1 FROM torrents WHERE info_hash = $1 FOR UPDATE`, hash[:])
		locked <- err
	}()

	select {
	case err := <-locked:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the other transaction never got its lock")
	}

	require.NoError(t, other.Rollback(t.Context()))

	select {
	case result := <-done:
		require.NoError(t, result.err, "a batch aborted by a deadlock is retried")
		require.Equal(t, 1, result.report.Updated)
	case <-time.After(5 * time.Second):
		t.Fatal("the import did not finish")
	}

	require.Equal(t, map[string]sourceRow{"alice:dht": {Seeders: ptr(7)}}, sourceRows(t, db, hash))
}

// A delete that commits after the batch's insert but before its lock removes a torrent
// the batch meant to update. The batch skips and counts it rather than failing a foreign
// key and rolling back the rest. The lock is taken in infohash order, so holding the
// first torrent keeps the import between the two statements while the second is deleted.
func TestExchangeImportSkipsATorrentDeletedBeforeItIsLocked(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	held, deleted := protocol.ID{1}, protocol.ID{2}
	seedTorrent(t, db, held, "Held", 10, false, "no_info")
	seedTorrent(t, db, deleted, "Deleted", 10, false, "no_info")

	holder := holdRow(t, db, `SELECT 1 FROM torrents WHERE info_hash = $1 FOR UPDATE`, held[:])

	done := startImport(t.Context(), db, exchangeFile(
		`{"infoHash":"`+hashHex(1)+`","name":"x","sources":[{"source":"dht","seeders":1}]}`,
		`{"infoHash":"`+hashHex(2)+`","name":"x","sources":[{"source":"dht","seeders":2}]}`,
	), 100)

	waitForLockWaits(t, db, 1, "the import should wait to lock the held torrent")
	exec(t, db, `DELETE FROM torrents WHERE info_hash = $1`, deleted[:])
	require.NoError(t, holder.Commit(t.Context()))

	select {
	case result := <-done:
		require.NoError(t, result.err)
		require.Equal(t, 1, result.report.Updated)
		require.Equal(t, 1, result.report.Skipped)
	case <-time.After(5 * time.Second):
		t.Fatal("the import did not finish")
	}

	require.Zero(t, count(t, db, `SELECT count(*) FROM torrents WHERE info_hash = $1`, deleted[:]))
	require.Equal(t, map[string]sourceRow{"alice:dht": {Seeders: ptr(1)}}, sourceRows(t, db, held))
}
