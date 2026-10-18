package importer

import (
	"database/sql"
	"testing"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/stretchr/testify/require"
)

func TestPersistItemsSkipsExistingQueueJob(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	importer := activeImport{
		importer: importer{
			dao: db.Query,
		},
		ctx:             t.Context(),
		importedSources: make(map[string]struct{}),
	}
	item := Item{
		Source:   "test-source",
		InfoHash: protocol.ID{1},
		Name:     "A torrent",
	}

	require.NoError(t, importer.persistItems(item))
	require.NoError(t, importer.persistItems(item))
}

// A legacy import item carries no peer counts, so re-importing a known torrent
// under its existing source must leave what that source row already knows.
func TestPersistItemsKeepsKnownSourceValues(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	infoHash := protocol.ID{2}
	publishedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	require.NoError(t, db.Query.Torrent.WithContext(t.Context()).Create(&model.Torrent{
		InfoHash:    infoHash,
		Name:        "A torrent",
		FilesStatus: model.FilesStatusNoInfo,
		Sources: []model.TorrentsTorrentSource{{
			Source:      "dht",
			InfoHash:    infoHash,
			ImportID:    model.NewNullString("first-import"),
			Seeders:     model.NewNullUint(9),
			Leechers:    model.NewNullUint(3),
			PublishedAt: sql.NullTime{Time: publishedAt, Valid: true},
		}},
	}))

	importer := activeImport{
		importer: importer{
			dao: db.Query,
		},
		info:            Info{ID: "second-import"},
		ctx:             t.Context(),
		importedSources: make(map[string]struct{}),
	}
	require.NoError(t, importer.persistItems(Item{
		Source:   "dht",
		InfoHash: infoHash,
		Name:     "A torrent",
	}))

	source, err := db.Query.TorrentsTorrentSource.WithContext(t.Context()).
		Where(
			db.Query.TorrentsTorrentSource.InfoHash.Eq(infoHash),
			db.Query.TorrentsTorrentSource.Source.Eq("dht"),
		).
		First()
	require.NoError(t, err)

	require.Equal(t, model.NewNullUint(9), source.Seeders)
	require.Equal(t, model.NewNullUint(3), source.Leechers)
	require.True(t, source.PublishedAt.Valid)
	require.True(t, publishedAt.Equal(source.PublishedAt.Time))
	require.Equal(t, model.NewNullString("second-import"), source.ImportID)

	// enhancement_source_rank derives the torrent's rank from its source rows.
	var enhancementSeeders sql.NullInt64
	require.NoError(t, db.Gorm.Raw(
		"SELECT enhancement_seeders FROM torrents WHERE info_hash = ?", infoHash,
	).Scan(&enhancementSeeders).Error)
	require.Equal(t, sql.NullInt64{Int64: 9, Valid: true}, enhancementSeeders)
}
