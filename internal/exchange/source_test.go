package exchange_test

import (
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidSourceName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"dht", "rarbg", "torrents-csv", "alice:dht", "alice:bob:dht", "a1", "x-2-y"} {
		assert.True(t, exchange.ValidSourceName(name), name)
	}

	for _, name := range []string{
		"", "DHT", "Torrents CSV", "torrents_csv", "-dht", "dht-", "a--b", ":dht", "dht:", "alice::dht",
		"alice: dht", "café",
	} {
		assert.False(t, exchange.ValidSourceName(name), name)
	}
}

func TestNormalizeSourceName(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"dht":               "dht",
		"Torrents CSV":      "torrents-csv",
		"torrents_csv":      "torrents-csv",
		"  RARBG  ":         "rarbg",
		"Alice : Bob__DHT":  "alice:bob-dht",
		"a--b":              "a-b",
		"alice:bob:dht":     "alice:bob:dht",
		"Café del Mar.2024": "caf-del-mar-2024",
	} {
		got, err := exchange.NormalizeSourceName(input)
		require.NoError(t, err, input)
		assert.Equal(t, want, got, input)
		assert.True(t, exchange.ValidSourceName(got), input)
	}

	for _, input := range []string{"", "   ", "!!!", "alice::dht", "alice:", "ééé"} {
		_, err := exchange.NormalizeSourceName(input)
		require.ErrorIs(t, err, exchange.ErrInvalidSourceName, input)
	}
}

func TestInvalidSourceNameDropsOnlyThatEntry(t *testing.T) {
	t.Parallel()

	input := header10 + "\n" +
		`{"infoHash":"` + hashA + `","name":"one","sources":[` +
		`{"source":"Torrents CSV","seeders":3},{"source":"alice:dht","seeders":5},{"seeders":1}]}` + "\n"

	lines, malformed, reader, err := readAll(t, input)
	require.NoError(t, err)
	require.Empty(t, malformed)
	require.Len(t, lines, 2)

	sources := lines[1].Record.Sources
	require.True(t, sources.Valid)
	require.Len(t, sources.Val, 1)
	assert.Equal(t, "alice:dht", sources.Val[0].Name)

	assert.Equal(t, []exchange.ReportEntry{
		{
			Scope:  exchange.ScopeRecord,
			Reason: exchange.ReasonInvalidSourceName,
			Path:   "sources[].source",
			Count:  1,
		},
		{
			Scope:  exchange.ScopeRecord,
			Reason: exchange.ReasonInvalidSourceName,
			Path:   "sources[].source",
			Value:  "Torrents CSV",
			Count:  1,
		},
	}, reader.Report())
}
