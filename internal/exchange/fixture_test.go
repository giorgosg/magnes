package exchange_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fixturePath = "testdata/exchange-v1.jsonl"

func decodeStrict(t *testing.T, data []byte) []exchange.Line {
	t.Helper()

	reader := exchange.NewReader(bytes.NewReader(data))

	var lines []exchange.Line

	for {
		line, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		require.NoError(t, err)

		lines = append(lines, line)
	}

	assert.Empty(t, reader.Report(), "the fixture drops nothing")

	return lines
}

func encode(t *testing.T, lines []exchange.Line) []byte {
	t.Helper()

	var out bytes.Buffer

	writer := exchange.NewWriter(&out)

	for _, line := range lines {
		if line.Header != nil {
			require.NoError(t, writer.WriteHeader(*line.Header))
		} else {
			require.NoError(t, writer.WriteRecord(*line.Record))
		}
	}

	return out.Bytes()
}

// The frozen fixture is the compatibility input for every later reader and writer.
// Changing it is a format change.
func TestFixtureRoundTripIsLossless(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile(fixturePath)
	require.NoError(t, err)

	lines := decodeStrict(t, fixture)
	require.Len(t, lines, 6)

	// What the fixture is required to cover, so it cannot be quietly weakened.
	require.NotNil(t, lines[0].Header)
	require.NotNil(t, lines[3].Header)
	assert.Equal(t, "magnes", lines[0].Header.Producer.Val.Program.Val)
	assert.Equal(t, "torrents-csv-converter", lines[3].Header.Producer.Val.Program.Val)
	assert.True(t, lines[3].Header.Since.Valid)

	single, multi, partial, absent := lines[1].Record, lines[2].Record, lines[4].Record, lines[5].Record
	assert.Len(t, single.Files.Val, 1, "single-file torrent")
	assert.Len(t, multi.Files.Val, 3, "multi-file torrent")
	assert.Equal(t, model.MaybeValid(false), partial.FilesComplete, "partial file list")
	assert.False(t, absent.Files.Valid, "absent files")
	assert.Equal(t, "alice:rarbg", single.Sources.Val[1].Name, "prefixed source")
	assert.Equal(t, "alice:bob:dht", multi.Sources.Val[0].Name, "chained prefix")
	assert.Equal(t, model.MaybeValid(uint(0)), single.Sources.Val[0].Leechers, "a zero count is known")
	assert.Contains(t, multi.Magnes.Classification.Val.Episodes.Val,
		exchange.SeasonEpisodes{Season: 1, WholeSeason: true}, "wholeSeason episodes")
	assert.Contains(t, multi.Magnes.Classification.Val.Episodes.Val,
		exchange.SeasonEpisodes{Season: 0, Episodes: []int{1, 2}}, "season 0 specials")
	assert.Equal(t, model.MaybeValid([]model.Language{}), partial.Magnes.Classification.Val.Languages,
		"an empty list is not absent")

	encoded := encode(t, lines)
	assert.Equal(t, string(fixture), string(encoded), "the writer's output is the fixture, byte for byte")
	assert.Equal(t, lines, decodeStrict(t, encoded))
}

func TestWriterRefusesWhatAReaderWouldDrop(t *testing.T) {
	t.Parallel()

	valid := func() exchange.Record {
		return exchange.Record{InfoHash: [20]byte{1}, Name: "x"}
	}

	withClassification := func(c exchange.Classification) exchange.Record {
		rec := valid()
		rec.Magnes.Classification = model.MaybeValid(c)

		return rec
	}

	for name, tc := range map[string]struct {
		record exchange.Record
		err    error
	}{
		"unmapped content type": {
			withClassification(exchange.Classification{ContentType: model.MaybeValid(model.ContentType("podcast"))}),
			exchange.ErrUnmappedValue,
		},
		"unmapped codec": {
			withClassification(exchange.Classification{
				Video: exchange.Video{Codec: model.MaybeValid(model.VideoCodec("HEVC"))},
			}),
			exchange.ErrUnmappedValue,
		},
		"unknown language": {
			withClassification(exchange.Classification{Languages: model.MaybeValid([]model.Language{"xx"})}),
			exchange.ErrUnmappedValue,
		},
		"episodes entry with both forms": {
			withClassification(exchange.Classification{Episodes: model.MaybeValid([]exchange.SeasonEpisodes{
				{Season: 1, Episodes: []int{1}, WholeSeason: true},
			})}),
			exchange.ErrInvalidEpisodes,
		},
		"episodes entry with neither form": {
			withClassification(exchange.Classification{Episodes: model.MaybeValid([]exchange.SeasonEpisodes{
				{Season: 1},
			})}),
			exchange.ErrInvalidEpisodes,
		},
		"invalid source name": {
			func() exchange.Record {
				rec := valid()
				rec.Sources = model.MaybeValid([]exchange.Source{{Name: "Torrents CSV"}})

				return rec
			}(),
			exchange.ErrInvalidSourceName,
		},
	} {
		var out bytes.Buffer

		writer := exchange.NewWriter(&out)
		require.NoError(t, writer.WriteHeader(exchange.Header{}), name)

		before := out.Len()

		require.ErrorIs(t, writer.WriteRecord(tc.record), tc.err, name)
		assert.Equal(t, before, out.Len(), "%s: nothing is written", name)
	}
}

func TestWriterRefusesRecordBeforeHeader(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := exchange.NewWriter(&out).WriteRecord(exchange.Record{Name: "x"})
	require.ErrorIs(t, err, exchange.ErrRecordBeforeHeader)
	assert.Zero(t, out.Len())
}

func TestWriterDeclaresTheCurrentVersion(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	require.NoError(t, exchange.NewWriter(&out).WriteHeader(exchange.Header{Version: exchange.Version{Major: 9}}))
	assert.JSONEq(t, `{"format":{"name":"torrent-exchange","version":{"major":1,"minor":0}}}`, out.String())
}
