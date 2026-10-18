package exchange_test

import (
	"os"
	"strings"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckSummarisesTheFixture(t *testing.T) {
	t.Parallel()

	file, err := os.Open(fixturePath)
	require.NoError(t, err)

	t.Cleanup(func() { _ = file.Close() })

	result, err := exchange.Check(file)
	require.NoError(t, err)

	require.Len(t, result.Headers, 2)
	assert.Equal(t, 1, result.Headers[0].Line)
	assert.Equal(t, 4, result.Headers[1].Line)
	assert.Equal(t, "torrents-csv-converter", result.Headers[1].Header.Producer.Val.Program.Val)
	assert.Equal(t, 4, result.Records)
	assert.Empty(t, result.Malformed)
	assert.Empty(t, result.Dropped)
	assert.Equal(t, []exchange.TagCount{{Tag: "4k-candidate", Count: 1}, {Tag: "keep", Count: 1}}, result.Tags)
}

func TestCheckReportsDropsMalformedLinesAndTags(t *testing.T) {
	t.Parallel()

	input := header10 + "\n" +
		`{"infoHash":"` + hashA + `","name":"a","size":null,"magnes":{"tags":["keep","hd"]},` +
		`"sources":[{"source":"Bad Name"}]}` + "\n" +
		"{oops\n" +
		`{"infoHash":"` + hashA + `","name":"b","magnes":{"tags":["keep"]},"future":1}` + "\n"

	result, err := exchange.Check(strings.NewReader(input))
	require.NoError(t, err)

	assert.Equal(t, 2, result.Records)
	require.Len(t, result.Malformed, 1)
	assert.Equal(t, 3, result.Malformed[0].Line)
	assert.Equal(t, []exchange.TagCount{{Tag: "keep", Count: 2}, {Tag: "hd", Count: 1}}, result.Tags,
		"most frequent first, then by name")
	assert.Equal(t, []exchange.ReportEntry{
		{
			Scope: exchange.ScopeRecord, Reason: exchange.ReasonInvalidSourceName, Path: "sources[].source",
			Value: "Bad Name", Count: 1,
		},
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonNull, Path: "size", Count: 1},
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonUnknownField, Path: "future", Count: 1},
	}, result.Dropped)
}

func TestCheckStopsAtAFatalErrorAndKeepsWhatItRead(t *testing.T) {
	t.Parallel()

	input := header10 + "\n" +
		`{"infoHash":"` + hashA + `","name":"a"}` + "\n" +
		`{"format":{"name":"torrent-exchange","version":{"major":2,"minor":0}}}` + "\n" +
		`{"infoHash":"` + hashA + `","name":"b"}` + "\n"

	result, err := exchange.Check(strings.NewReader(input))
	require.ErrorIs(t, err, exchange.ErrUnsupportedVersion)

	var fatal *exchange.FatalError
	require.ErrorAs(t, err, &fatal)
	assert.Equal(t, 3, fatal.Line)
	assert.Len(t, result.Headers, 1)
	assert.Equal(t, 1, result.Records)
}
