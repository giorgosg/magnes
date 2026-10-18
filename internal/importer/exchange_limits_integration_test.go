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
// the import stops, and the batch that crossed it is not written.
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
	assert.Equal(t, 2, report.New, "the batches before the one that crossed the limit are written")
	assert.Equal(t, 2, count(t, db, `SELECT count(*) FROM torrents`))
}

// One record can carry as many distinct invalid tags as its line holds. The import stops
// at the limit while it plans the batch, so it neither holds nor reports more than the
// limit, and the batch that crossed it is not written.
func TestExchangeImportStopsAtTheLimitWithinOneRecord(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	options := alice()
	options.Limits = exchange.Limits{MaxReported: 3}

	tags := make([]string, 50)
	for i := range tags {
		tags[i] = `"Bad ` + strings.Repeat("x", i) + `"`
	}

	report, err := newExchangeImporter(db).Import(t.Context(), exchangeFile(
		`{"infoHash":"`+hashHex(1)+`","name":"one","magnes":{"tags":[`+strings.Join(tags, ",")+`]}}`,
	), options)
	require.ErrorIs(t, err, exchange.ErrTooMuchReported)
	assert.LessOrEqual(t, len(report.Headers)+len(report.Malformed)+len(report.Dropped), 3)
	assert.Zero(t, report.New)
	assert.Zero(t, count(t, db, `SELECT count(*) FROM torrents`))
}

// Malformed lines read after the import's own drops count against the same limit, so the
// report never holds more than it, however the two are interleaved.
func TestExchangeImportCountsItsOwnDropsAgainstLaterMalformedLines(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	options := alice()
	options.Limits = exchange.Limits{MaxReported: 4}

	report, err := importer.NewExchangeImporter(db.Gorm, nil, importer.ExchangeConfig{BatchSize: 1}).Import(
		t.Context(), exchangeFile(
			`{"infoHash":"`+hashHex(1)+`","name":"one","magnes":{"tags":["Bad 1","Bad 2"]}}`,
			"not json", "not json", "not json",
		), options)
	require.ErrorIs(t, err, exchange.ErrTooMuchReported)
	assert.LessOrEqual(t, len(report.Headers)+len(report.Malformed)+len(report.Dropped), 4)
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
