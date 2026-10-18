package exchange_test

import (
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A newer minor version adds fields an older reader does not know. They are read as
// absent, and reported once per path with a count rather than once per record.
func TestUnknownFieldsAreReportedPerPathWithCounts(t *testing.T) {
	t.Parallel()

	input := `{"format":{"name":"torrent-exchange","version":{"major":1,"minor":3},"flavour":"x"},` +
		`"compression":"zstd"}` + "\n" +
		`{"infoHash":"` + hashA + `","name":"one","infoHashV2":"ab","tags":["hd"],` +
		`"sources":[{"source":"dht","completed":4},{"source":"rarbg","completed":1}]}` + "\n" +
		`{"infoHash":"` + hashA + `","name":"two","Name":"case variant",` +
		`"sources":[{"source":"dht","completed":2,"history":[{"seeders":1}]}]}` + "\n"

	lines, malformed, err, reader := readAll(t, input)
	require.NoError(t, err)
	require.Empty(t, malformed)
	require.Len(t, lines, 3)

	assert.Equal(t, "two", lines[2].Record.Name, "keys match exactly, not case-insensitively")
	assert.Equal(t, []exchange.ReportEntry{
		{Scope: exchange.ScopeHeader, Reason: exchange.ReasonUnknownField, Path: "compression", Count: 1},
		{Scope: exchange.ScopeHeader, Reason: exchange.ReasonUnknownField, Path: "format.flavour", Count: 1},
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonUnknownField, Path: "Name", Count: 1},
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonUnknownField, Path: "infoHashV2", Count: 1},
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonUnknownField, Path: "sources[].completed", Count: 3},
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonUnknownField, Path: "sources[].history", Count: 1},
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonUnknownField, Path: "tags", Count: 1},
	}, reader.Report())
}

func TestNullIsReadAsAbsentAndReported(t *testing.T) {
	t.Parallel()

	input := header10 + "\n" +
		`{"infoHash":"` + hashA + `","name":"one","size":null,"private":false,` +
		`"sources":[null,{"source":"dht","seeders":null,"leechers":0}]}` + "\n"

	lines, malformed, err, reader := readAll(t, input)
	require.NoError(t, err)
	require.Empty(t, malformed)
	require.Len(t, lines, 2)

	rec := lines[1].Record
	assert.False(t, rec.Size.Valid, "null size is absent")
	assert.True(t, rec.Private.Valid, "false is not absent")
	assert.False(t, rec.Private.Val)
	assert.False(t, rec.PublishedAt.Valid, "a missing field is absent")
	require.True(t, rec.Sources.Valid)
	require.Len(t, rec.Sources.Val, 1, "a null array element is skipped")
	assert.False(t, rec.Sources.Val[0].Seeders.Valid)
	assert.True(t, rec.Sources.Val[0].Leechers.Valid, "zero is not absent")
	assert.Equal(t, uint(0), rec.Sources.Val[0].Leechers.Val)

	assert.Equal(t, []exchange.ReportEntry{
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonNull, Path: "size", Count: 1},
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonNull, Path: "sources[]", Count: 1},
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonNull, Path: "sources[].seeders", Count: 1},
	}, reader.Report())
}

// A malformed line is skipped whole, so what it would have dropped is not reported.
func TestMalformedLineContributesNothingToTheReport(t *testing.T) {
	t.Parallel()

	input := header10 + "\n" +
		`{"infoHash":"` + hashA + `","name":"one","extra":1,"size":"big"}` + "\n"

	_, malformed, err, reader := readAll(t, input)
	require.NoError(t, err)
	require.Len(t, malformed, 1)
	assert.Empty(t, reader.Report())
}

func TestEmptyArraysAndAbsentArraysDiffer(t *testing.T) {
	t.Parallel()

	input := header10 + "\n" +
		`{"infoHash":"` + hashA + `","name":"one","files":[],"sources":[]}` + "\n" +
		`{"infoHash":"` + hashA + `","name":"two"}` + "\n"

	lines, _, err, _ := readAll(t, input)
	require.NoError(t, err)
	require.Len(t, lines, 3)

	assert.True(t, lines[1].Record.Files.Valid)
	assert.Empty(t, lines[1].Record.Files.Val)
	assert.True(t, lines[1].Record.Sources.Valid)
	assert.False(t, lines[2].Record.Files.Valid)
	assert.False(t, lines[2].Record.Sources.Valid)
}
