package importer_test

import (
	"strings"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The import reports values of its own, such as invalid tags, and a file can make as many
// of those as it has records. They count towards the same limit as the reader's: past it,
// the import stops after the batch that crossed it.
func TestExchangeImportStopsAtTooManyDistinctDropsOfItsOwn(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	options := alice()
	options.Limits = exchange.Limits{MaxReported: 3}

	report, err := importer.NewExchangeImporter(db.Gorm, nil, importer.ExchangeConfig{BatchSize: 1}).Import(
		t.Context(), exchangeFile(
			`{"infoHash":"`+hashHex(1)+`","name":"one","magnes":{"tags":["Bad 1"]}}`,
			`{"infoHash":"`+hashHex(2)+`","name":"two","magnes":{"tags":["Bad 2"]}}`,
			`{"infoHash":"`+hashHex(3)+`","name":"three","magnes":{"tags":["Bad 3"]}}`,
			`{"infoHash":"`+hashHex(4)+`","name":"four","magnes":{"tags":["Bad 4"]}}`,
		), options)
	require.ErrorIs(t, err, exchange.ErrTooMuchReported)
	assert.Contains(t, err.Error(), "more than 3 headers, malformed lines and distinct dropped entries")
	assert.Equal(t, 3, report.New, "the batches up to the one that crossed the limit are written")
	assert.Equal(t, 3, count(t, db, `SELECT count(*) FROM torrents`))
}

// The values the import reports are clipped like the reader's.
func TestExchangeImportClipsTheValuesItReports(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	options := alice()
	options.Limits = exchange.Limits{MaxReportedBytes: 4}

	report, err := newExchangeImporter(db).Import(t.Context(), exchangeFile(
		`{"infoHash":"`+hashHex(1)+`","name":"one","magnes":{"tags":["`+strings.Repeat("X", 100)+`"]}}`,
	), options)
	require.NoError(t, err)
	require.Len(t, report.Dropped, 1)
	assert.Equal(t, importer.ReasonInvalidTag, report.Dropped[0].Reason)
	assert.Equal(t, "XXXX…", report.Dropped[0].Value)
}

// The reader's limits apply to an import: a line that is too long stops it.
func TestExchangeImportAppliesTheReadersLimits(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	options := alice()
	options.Limits = exchange.Limits{MaxLineBytes: 100}

	report, err := newExchangeImporter(db).Import(t.Context(), exchangeFile(
		`{"infoHash":"`+hashHex(1)+`","name":"one"}`,
		`{"infoHash":"`+hashHex(2)+`","name":"`+strings.Repeat("x", 100)+`"}`,
	), options)
	require.ErrorIs(t, err, exchange.ErrLineTooLong)
	assert.Equal(t, 1, report.New)
}
