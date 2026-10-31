package importer_test

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/stretchr/testify/require"
)

// incompressible returns n bytes of lowercase letters and digits that PostgreSQL cannot
// compress, the worst case for an index entry. They follow the source label rule too.
func incompressible(seed uint64, n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

	r := rand.New(rand.NewPCG(seed, seed)) //nolint:gosec // a fixed sequence, not a secret
	b := make([]byte, n)

	for i := range b {
		b[i] = alphabet[r.IntN(len(alphabet))]
	}

	return string(b)
}

func outOfRange(path string) exchange.ReportEntry {
	return exchange.ReportEntry{
		Scope: exchange.ScopeRecord, Reason: importer.ReasonOutOfRange, Path: path, Count: 1,
	}
}

// A name too long for the btree index on torrents.name used to fail the statement, and so
// the batch and the whole import. It is a value too large to store, so only its record is
// skipped.
func TestExchangeImportSkipsARecordWhoseNameIsTooLongToIndex(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	report := importExchange(t, db, alice(),
		`{"infoHash":"`+hashHex(1)+`","name":"Fine"}`,
		`{"infoHash":"`+hashHex(2)+`","name":"`+strings.Repeat("x", 1<<20)+`"}`)

	require.Equal(t, 1, report.New)
	require.Equal(t, 1, report.Skipped)
	require.Contains(t, report.Dropped, outOfRange("name"))
	require.Equal(t, 0, count(t, db, `SELECT count(*) FROM torrents WHERE info_hash = $1`, protocol.ID{2}.Bytes()))
}

// Each limit is exactly what its index accepts from a value PostgreSQL cannot compress:
// one byte more is refused by the index, so the record is skipped instead. A source name
// is measured as stored, with its origin prefix.
func TestExchangeImportStoresStringsUpToWhatTheirIndexesAccept(t *testing.T) {
	t.Parallel()

	const (
		nameLimit   = 2692 // torrents_name_idx
		pathLimit   = 2668 // torrent_files_pkey, after the infohash
		sourceLimit = 2671 // torrents_torrent_sources_pkey, before the infohash
	)

	prefix := len("alice:")
	name := func(n int) string { return incompressible(1, n) }
	path := func(n int) string { return incompressible(2, n) }
	source := func(n int) string { return incompressible(3, n-prefix) }

	db := dbtest.New(t)

	multi := func(hash byte, filePath string) string {
		return `{"infoHash":"` + hashHex(hash) + `","name":"Multi","files":[{"index":0,"path":"` + filePath +
			`","size":1},{"index":1,"path":"b","size":1}]}`
	}

	sourced := func(hash byte, sourceName string) string {
		return `{"infoHash":"` + hashHex(hash) + `","name":"Sourced","sources":[{"source":"` + sourceName +
			`"}]}`
	}

	report := importExchange(t, db, alice(),
		`{"infoHash":"`+hashHex(1)+`","name":"`+name(nameLimit)+`"}`,
		`{"infoHash":"`+hashHex(2)+`","name":"`+name(nameLimit+1)+`"}`,
		multi(3, path(pathLimit)),
		multi(4, path(pathLimit+1)),
		sourced(5, source(sourceLimit)),
		sourced(6, source(sourceLimit+1)),
	)

	require.Equal(t, 3, report.New)
	require.Equal(t, 3, report.Skipped)
	require.Contains(t, report.Dropped, outOfRange("name"))
	require.Contains(t, report.Dropped, outOfRange("files[].path"))
	require.Contains(t, report.Dropped, outOfRange("sources[].source"))

	require.Equal(t, name(nameLimit), torrentRowOf(t, db, protocol.ID{1}).Name)
	require.Equal(t, []string{"0:" + path(pathLimit) + ":1", "1:b:1"}, filePaths(t, db, protocol.ID{3}))
	require.Contains(t, sourceRows(t, db, protocol.ID{5}), "alice:"+source(sourceLimit))

	for _, skipped := range []byte{2, 4, 6} {
		require.Equal(t, 0, count(t, db, `SELECT count(*) FROM torrents WHERE info_hash = $1`,
			protocol.ID{skipped}.Bytes()))
	}
}

// Only a path that becomes a torrent_files row is limited. A padding entry, a file past
// the threshold and a single-file torrent's one entry are not stored as rows, so a long
// path there skips nothing.
func TestExchangeImportLimitsOnlyThePathsItStores(t *testing.T) {
	t.Parallel()

	long := incompressible(4, 2692)
	db := dbtest.New(t)

	report, err := importer.NewExchangeImporter(db.Gorm, nil, importer.ExchangeConfig{FilesThreshold: 1}).Import(
		t.Context(), exchangeFile(
			`{"infoHash":"`+hashHex(1)+`","name":"Over","files":[{"index":0,"path":"a","size":1},
{"index":1,"path":".pad/`+long+`","size":1},{"index":2,"path":"`+long+`","size":1}]}`,
			`{"infoHash":"`+hashHex(
				2,
			)+`","name":"`+long+`","files":[{"index":0,"path":"`+long+`","size":1}]}`,
		), alice())
	require.NoError(t, err)

	require.Equal(t, 2, report.New)
	require.Zero(t, report.Skipped)
	require.Equal(t, []string{"0:a:1"}, filePaths(t, db, protocol.ID{1}))
	require.Equal(t, "single", torrentRowOf(t, db, protocol.ID{2}).FilesStatus)
}
