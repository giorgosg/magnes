package dhtcrawler

import (
	"math/rand/v2"
	"strings"
	"testing"

	torrentmetainfo "github.com/anacrolix/torrent/metainfo"
	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/metainfo"
	"github.com/stretchr/testify/require"
)

// incompressible returns n bytes that PostgreSQL cannot compress, the worst case for an
// index entry.
func incompressible(seed uint64, n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

	r := rand.New(rand.NewPCG(seed, seed)) //nolint:gosec // a fixed sequence, not a secret
	b := make([]byte, n)

	for i := range b {
		b[i] = alphabet[r.IntN(len(alphabet))]
	}

	return string(b)
}

func multiFile(firstPath string) metainfo.Info {
	return metainfo.Info{Name: "multi", Files: []torrentmetainfo.FileInfo{
		{Path: []string{firstPath}, Length: 1024},
		{Path: []string{"b"}, Length: 1024},
	}}
}

func withMetaInfo(hash byte, info metainfo.Info) infoHashWithMetaInfo {
	return infoHashWithMetaInfo{
		nodeHasPeersForHash: nodeHasPeersForHash{infoHash: protocol.ID{hash}},
		metaInfo:            info,
	}
}

// Metainfo comes from untrusted peers. A name or stored file path too long for its btree
// index used to fail the whole transaction (SQLSTATE 54000), and with it every other
// torrent in the batch. Only the torrent that cannot be stored is skipped now.
func TestPersistTorrentsSkipsATorrentWhoseStringsAreTooLongToIndex(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()
	c := newPersistTestCrawler(db.Query, 0)

	written, ok := c.writeTorrentBatch(ctx, []infoHashWithMetaInfo{
		withMetaInfo(1, metainfo.Info{Name: "normal torrent", Length: 2048}),
		withMetaInfo(2, metainfo.Info{Name: incompressible(1, model.MaxTorrentNameBytes+1), Length: 2048}),
		withMetaInfo(3, metainfo.Info{Name: strings.Repeat("x", 1<<20), Length: 2048}),
		withMetaInfo(4, multiFile(incompressible(2, model.MaxTorrentFilePathBytes+1))),
	})
	require.True(t, ok, "one unstorable torrent must not fail the batch")
	require.Contains(t, written, protocol.ID{1})
	require.Len(t, written, 1, "a skipped torrent is neither counted nor forwarded to scrape")

	stored, err := db.Query.Torrent.WithContext(ctx).Find()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Equal(t, protocol.ID{1}, stored[0].InfoHash)

	files, err := db.Query.TorrentFile.WithContext(ctx).Count()
	require.NoError(t, err)
	require.Zero(t, files)
}

// Each limit is exactly what its index accepts from a value PostgreSQL cannot compress.
func TestPersistTorrentsStoresStringsUpToWhatTheirIndexesAccept(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ctx := t.Context()
	c := newPersistTestCrawler(db.Query, 0)

	name := incompressible(1, model.MaxTorrentNameBytes)
	path := incompressible(2, model.MaxTorrentFilePathBytes)

	written, ok := c.writeTorrentBatch(ctx, []infoHashWithMetaInfo{
		withMetaInfo(1, metainfo.Info{Name: name, Length: 2048}),
		withMetaInfo(2, multiFile(path)),
	})
	require.True(t, ok)
	require.Len(t, written, 2)

	torrent, err := db.Query.Torrent.WithContext(ctx).Where(db.Query.Torrent.InfoHash.Eq(protocol.ID{1})).First()
	require.NoError(t, err)
	require.Equal(t, name, torrent.Name)

	file, err := db.Query.TorrentFile.WithContext(ctx).
		Where(db.Query.TorrentFile.InfoHash.Eq(protocol.ID{2}), db.Query.TorrentFile.Index.Eq(0)).First()
	require.NoError(t, err)
	require.Equal(t, path, file.Path)
}

// Only a path that becomes a torrent_files row is limited: a padding entry and a file
// past the threshold are not stored, so a long path there skips nothing.
func TestCreateTorrentModelLimitsOnlyThePathsItStores(t *testing.T) {
	t.Parallel()

	long := incompressible(3, model.MaxTorrentFilePathBytes+1)

	torrent, err := createTorrentModel(protocol.ID{}, metainfo.Info{
		Name: "over",
		Files: []torrentmetainfo.FileInfo{
			{Path: []string{"a"}, Length: 1024},
			{Path: []string{".pad", long}, Length: 1},
			{Path: []string{long}, Length: 1024},
		},
	}, false, 1)
	require.NoError(t, err)
	require.Equal(t, model.FilesStatusOverThreshold, torrent.FilesStatus)
	require.Len(t, torrent.Files, 1)

	_, err = createTorrentModel(protocol.ID{}, multiFile(long), false, 100)
	require.Error(t, err)
}
