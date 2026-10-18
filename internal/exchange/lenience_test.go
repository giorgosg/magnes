package exchange_test

import (
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wholeSeason: false is not the whole-season form, so an entry with episodes and an
// explicit false is the episodes form.
func TestEpisodesWithWholeSeasonFalseAreTheEpisodesForm(t *testing.T) {
	t.Parallel()

	rec, report := readClassification(t, `{"episodes":[{"season":1,"episodes":[3],"wholeSeason":false}]}`)

	assert.Equal(t, model.MaybeValid([]exchange.SeasonEpisodes{{Season: 1, Episodes: []int{3}}}),
		rec.Magnes.Classification.Val.Episodes)
	assert.Empty(t, report)
}

// Nothing in the vendor section is required to understand the torrent, so a mistyped
// value there drops that part of the section, never the record.
func TestMistypedVendorSectionKeepsTheRecord(t *testing.T) {
	t.Parallel()

	input := header10 + "\n" +
		`{"infoHash":"` + hashA + `","name":"a","magnes":{"classification":{"contentType":5},"tags":["keep"]}}` +
		"\n" +
		`{"infoHash":"` + hashA + `","name":"b","magnes":{"classification":{"contentType":"movie"},"tags":"keep"}}` +
		"\n" +
		`{"infoHash":"` + hashA + `","name":"c","magnes":[]}` + "\n"

	lines, malformed, reader, err := readAll(t, input)
	require.NoError(t, err)
	require.Empty(t, malformed)
	require.Len(t, lines, 4)

	a, b, c := lines[1].Record.Magnes, lines[2].Record.Magnes, lines[3].Record.Magnes
	assert.False(t, a.Classification.Valid)
	assert.Equal(t, model.MaybeValid([]string{"keep"}), a.Tags)
	assert.True(t, b.Classification.Valid)
	assert.False(t, b.Tags.Valid)
	assert.Equal(t, exchange.Magnes{}, c)

	assert.Equal(t, []exchange.ReportEntry{
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonInvalidEntry, Path: "magnes", Count: 1},
		{
			Scope: exchange.ScopeRecord, Reason: exchange.ReasonInvalidEntry,
			Path: "magnes.classification", Count: 1,
		},
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonInvalidEntry, Path: "magnes.tags", Count: 1},
	}, reader.Report())
}

// The producer does not affect how a file is read, so a mistyped producer or timestamp
// is dropped and reported rather than stopping the stream.
func TestMistypedHeaderMetadataIsDroppedNotFatal(t *testing.T) {
	t.Parallel()

	input := `{"format":{"name":"torrent-exchange","version":{"major":1,"minor":0}},` +
		`"producer":{"program":"magnes","version":14},"exportedAt":"yesterday","since":5}` + "\n" +
		`{"infoHash":"` + hashA + `","name":"one"}` + "\n"

	lines, malformed, reader, err := readAll(t, input)
	require.NoError(t, err)
	require.Empty(t, malformed)
	require.Len(t, lines, 2)

	header := lines[0].Header
	assert.False(t, header.Producer.Valid)
	assert.False(t, header.ExportedAt.Valid)
	assert.False(t, header.Since.Valid)
	assert.Equal(t, []exchange.ReportEntry{
		{Scope: exchange.ScopeHeader, Reason: exchange.ReasonInvalidEntry, Path: "exportedAt", Count: 1},
		{Scope: exchange.ScopeHeader, Reason: exchange.ReasonInvalidEntry, Path: "producer", Count: 1},
		{Scope: exchange.ScopeHeader, Reason: exchange.ReasonInvalidEntry, Path: "since", Count: 1},
	}, reader.Report())
}
