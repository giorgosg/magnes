package dhtcrawler

import (
	"testing"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/bits-and-blooms/bloom/v3"
	"github.com/stretchr/testify/require"
)

// A torrent deleted between triage and the scrape write must cost only its own
// source row: the foreign key on torrents_torrent_sources would otherwise reject
// the whole batch.
func TestPersistSourceBatchSkipsDeletedTorrent(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()
	kept := []protocol.ID{{101}, {103}}
	deleted := protocol.ID{102}

	for _, hash := range append([]protocol.ID{deleted}, kept...) {
		require.NoError(t, db.Query.Torrent.WithContext(ctx).Create(&model.Torrent{
			InfoHash: hash, Name: "Synthetic release", FilesStatus: model.FilesStatusSingle,
		}))
	}

	require.NoError(t, db.Query.TorrentsTorrentSource.WithContext(ctx).Create(&model.TorrentsTorrentSource{
		InfoHash: kept[0], Source: dhtSource, Seeders: model.NewNullUint(9), Leechers: model.NewNullUint(9),
	}))

	_, err := db.Query.Torrent.WithContext(ctx).Where(db.Query.Torrent.InfoHash.Eq(deleted)).Delete()
	require.NoError(t, err)

	filter := *bloom.NewWithEstimates(100, 0.01)
	filter.Add([]byte("peer"))

	scrapes := make([]infoHashWithScrape, 0, 3)
	for _, hash := range []protocol.ID{kept[0], deleted, kept[1]} {
		scrapes = append(scrapes, infoHashWithScrape{
			nodeHasPeersForHash: nodeHasPeersForHash{infoHash: hash}, bfsd: filter, bfpe: filter,
		})
	}

	newPersistTestCrawler(db.Query, 0).persistSourceBatch(ctx, scrapes)

	sources, err := db.Query.TorrentsTorrentSource.WithContext(ctx).Find()
	require.NoError(t, err)

	persisted := make([]protocol.ID, 0, len(sources))
	for _, source := range sources {
		persisted = append(persisted, source.InfoHash)
		require.Equal(t, dhtSource, source.Source)
		require.Equal(t, model.NewNullUint(1), source.Seeders)
	}

	require.ElementsMatch(t, kept, persisted, "a deleted torrent must not reject the rest of the batch")
}

// The same delete, but still in flight when the scrape write starts: filtering
// alone would see the row, and the foreign key check would then wait for the
// delete and fail the batch when it commits.
func TestPersistSourceBatchWaitsForInFlightTorrentDelete(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()
	kept := protocol.ID{111}
	deleted := protocol.ID{112}

	for _, hash := range []protocol.ID{kept, deleted} {
		require.NoError(t, db.Query.Torrent.WithContext(ctx).Create(&model.Torrent{
			InfoHash: hash, Name: "Synthetic release", FilesStatus: model.FilesStatusSingle,
		}))
	}

	deleter := db.Gorm.WithContext(ctx).Begin()
	require.NoError(t, deleter.Error)
	t.Cleanup(func() { deleter.Rollback() })
	require.NoError(t, deleter.Exec("DELETE FROM torrents WHERE info_hash = ?", deleted).Error)

	filter := *bloom.NewWithEstimates(100, 0.01)
	done := make(chan struct{})

	go func() {
		defer close(done)

		newPersistTestCrawler(db.Query, 0).persistSourceBatch(ctx, []infoHashWithScrape{
			{nodeHasPeersForHash: nodeHasPeersForHash{infoHash: kept}, bfsd: filter, bfpe: filter},
			{nodeHasPeersForHash: nodeHasPeersForHash{infoHash: deleted}, bfsd: filter, bfpe: filter},
		})
	}()

	require.Eventually(t, func() bool {
		var waiting int64
		require.NoError(t, db.Gorm.Raw(
			"SELECT count(*) FROM pg_stat_activity WHERE datname = ? AND wait_event_type = 'Lock'", db.Name,
		).Scan(&waiting).Error)

		return waiting > 0
	}, 10*time.Second, 10*time.Millisecond, "the scrape write should block on the deleting transaction")

	require.NoError(t, deleter.Commit().Error)
	<-done

	sources, err := db.Query.TorrentsTorrentSource.WithContext(ctx).Find()
	require.NoError(t, err)
	require.Len(t, sources, 1, "the in-flight delete must cost only its own row")
	require.Equal(t, kept, sources[0].InfoHash)
}
