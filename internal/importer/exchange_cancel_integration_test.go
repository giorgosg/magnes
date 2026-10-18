package importer_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/stretchr/testify/require"
)

type importResult struct {
	report importer.ExchangeReport
	err    error
}

// startImport runs an import of src in the background, one record per batch.
func startImport(ctx context.Context, db *dbtest.DB, src io.Reader) <-chan importResult {
	done := make(chan importResult, 1)

	go func() {
		report, err := importer.NewExchangeImporter(db.Gorm, nil, importer.ExchangeConfig{
			FilesThreshold: 100, BatchSize: 1,
		}).Import(ctx, src, alice())
		done <- importResult{report, err}
	}()

	return done
}

func awaitImport(t *testing.T, done <-chan importResult) importResult {
	t.Helper()

	select {
	case result := <-done:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("the import did not return after it was cancelled")
		return importResult{}
	}
}

// An import waiting on input that never comes must still stop when cancelled: a client
// that stalls mid-upload, or a pipe whose writer is stuck, cannot hold a shutdown.
func TestExchangeImportReturnsWhenCancelledWhileWaitingForInput(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx, cancel := context.WithCancel(t.Context())

	defer cancel()

	src, input := io.Pipe()

	t.Cleanup(func() { _ = input.Close() })

	done := startImport(ctx, db, src)

	go func() {
		_, _ = io.WriteString(input, exchangeHeader+"\n"+`{"infoHash":"`+hashHex(1)+`","name":"First"}`+"\n")
	}()

	require.Eventually(t, func() bool {
		return count(t, db, `SELECT count(*) FROM torrents`) == 1
	}, 5*time.Second, 10*time.Millisecond, "the first batch should commit")

	cancel()

	result := awaitImport(t, done)
	require.ErrorIs(t, result.err, context.Canceled)
	require.Equal(t, 1, result.report.New, "the report covers the batches that committed")
}

// An import whose batch is waiting on a lock, as it would behind a crawler write to the
// same torrent, must stop when cancelled, and the waiting batch must not commit.
func TestExchangeImportReturnsWhenCancelledWhileItsBatchWaits(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx, cancel := context.WithCancel(t.Context())

	defer cancel()

	contended := protocol.ID{2}
	seedTorrent(t, db, contended, "Contended", 10, false, "no_info")

	blocker, err := db.Pool.Begin(t.Context())
	require.NoError(t, err)

	t.Cleanup(func() { _ = blocker.Rollback(context.Background()) })

	_, err = blocker.Exec(t.Context(), `SELECT 1 FROM torrents WHERE info_hash = $1 FOR UPDATE`, contended[:])
	require.NoError(t, err)

	done := startImport(ctx, db, exchangeFile(
		`{"infoHash":"`+hashHex(1)+`","name":"First"}`,
		`{"infoHash":"`+hashHex(2)+`","name":"Contended","sources":[{"source":"dht","seeders":1}]}`,
	))

	require.Eventually(t, func() bool {
		return count(
			t,
			db,
			`SELECT count(*) FROM pg_stat_activity WHERE datname = $1 AND wait_event_type = 'Lock'`,
			db.Name,
		) > 0
	}, 5*time.Second, 10*time.Millisecond, "the second batch should wait on the locked torrent")

	cancel()

	result := awaitImport(t, done)
	require.ErrorIs(t, result.err, context.Canceled)
	require.Equal(t, 1, result.report.New)
	require.Equal(t, 0, result.report.Updated)

	require.NoError(t, blocker.Rollback(t.Context()))

	// The abandoned backend finishes its statement once the lock is free, then finds its
	// client gone and rolls back.
	require.Eventually(t, func() bool {
		return count(t, db, `SELECT count(*) FROM pg_stat_activity WHERE datname = $1 AND state <> 'idle'
 AND pid <> pg_backend_pid()`, db.Name) == 0
	}, 5*time.Second, 10*time.Millisecond)
	require.Zero(
		t,
		count(t, db, `SELECT count(*) FROM torrents_torrent_sources WHERE info_hash = $1`, contended[:]),
		"the cancelled batch must not commit",
	)
	require.Equal(t, 1, count(t, db, `SELECT count(*) FROM torrents WHERE info_hash = $1`, protocol.ID{1}.Bytes()))
}
