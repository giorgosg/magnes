package exchange_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	header10 = `{"format":{"name":"torrent-exchange","version":{"major":1,"minor":0}}}`
	hashA    = "c9e15763f722f23e98a29decdfae341b98d53056"
)

// readAll drains a stream, collecting lines and skipped-line errors, and stopping at
// the end or at a fatal error.
func readAll(t *testing.T, input string) ([]exchange.Line, []*exchange.MalformedLineError, *exchange.Reader, error) {
	t.Helper()

	reader := exchange.NewReader(strings.NewReader(input))

	var (
		lines     []exchange.Line
		malformed []*exchange.MalformedLineError
	)

	for {
		line, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return lines, malformed, reader, nil
		}

		var skipped *exchange.MalformedLineError
		if errors.As(err, &skipped) {
			malformed = append(malformed, skipped)
			continue
		}

		if err != nil {
			return lines, malformed, reader, err
		}

		lines = append(lines, line)
	}
}

func TestStreamHeaderAppliesToFollowingRecords(t *testing.T) {
	t.Parallel()

	input := header10 + "\n" +
		`{"infoHash":"` + hashA + `","name":"one"}` + "\n" +
		`{"format":{"name":"torrent-exchange","version":{"major":1,"minor":0}},` +
		`"producer":{"program":"other","version":"2"}}` + "\n" +
		`{"infoHash":"` + hashA + `","name":"two"}` + "\n"

	lines, malformed, _, err := readAll(t, input)
	require.NoError(t, err)
	require.Empty(t, malformed)
	require.Len(t, lines, 4)

	assert.NotNil(t, lines[0].Header)
	assert.Equal(t, 1, lines[0].Number)
	require.NotNil(t, lines[1].Record)
	assert.Equal(t, "one", lines[1].Record.Name)
	assert.Equal(t, hashA, lines[1].Record.InfoHash.String())
	require.NotNil(t, lines[2].Header)
	assert.Equal(t, "other", lines[2].Header.Producer.Val.Program.Val)
	require.NotNil(t, lines[3].Record)
	assert.Equal(t, "two", lines[3].Record.Name)
	assert.Equal(t, 4, lines[3].Number)
}

func TestStreamRecordBeforeHeaderIsFatal(t *testing.T) {
	t.Parallel()

	input := `{"infoHash":"` + hashA + `","name":"one"}` + "\n" + header10 + "\n"

	lines, _, reader, err := readAll(t, input)
	require.ErrorIs(t, err, exchange.ErrRecordBeforeHeader)
	assert.Empty(t, lines)

	var fatal *exchange.FatalError
	require.ErrorAs(t, err, &fatal)
	assert.Equal(t, 1, fatal.Line)

	_, again := reader.Next()
	require.ErrorIs(t, again, exchange.ErrRecordBeforeHeader, "a fatal error is sticky")
}

// A malformed line names the offending path once, as check prints it.
func TestMalformedLineNamesThePathOnce(t *testing.T) {
	t.Parallel()

	input := header10 + "\n" +
		`{"infoHash":"` + hashA + `","name":"a","files":[{"path":"x","size":1}]}` + "\n" +
		`{"infoHash":"` + hashA + `","name":"b","files":[5]}` + "\n" +
		`{"infoHash":"` + hashA + `","name":"c","files":{}}` + "\n"

	_, malformed, _, err := readAll(t, input)
	require.NoError(t, err)
	require.Len(t, malformed, 3)

	assert.Equal(t, "files[].index: required", malformed[0].Err.Error())
	assert.Equal(t, "files[]: not a JSON object", malformed[1].Err.Error())
	assert.Equal(t, "files: json: cannot unmarshal object into Go value of type []json.RawMessage",
		malformed[2].Err.Error())
}

func TestStreamMalformedLinesAreSkippedWithTheirNumbers(t *testing.T) {
	t.Parallel()

	input := header10 + "\n" +
		"not json\n" +
		"\n" +
		`{"infoHash":"NOT-HEX","name":"bad hash"}` + "\n" +
		`{"name":"no hash"}` + "\n" +
		`{"infoHash":"` + strings.ToUpper(hashA) + `","name":"uppercase hash"}` + "\n" +
		`{"infoHash":"` + hashA + `","name":"fine","size":"big"}` + "\n" +
		`[1,2]` + "\n" +
		`{"infoHash":"` + hashA + `","name":"fine"}` + "\n"

	lines, malformed, _, err := readAll(t, input)
	require.NoError(t, err)

	numbers := make([]int, 0, len(malformed))
	for _, m := range malformed {
		numbers = append(numbers, m.Line)
	}

	assert.Equal(t, []int{2, 4, 5, 6, 7, 8}, numbers, "blank line 3 is ignored")
	require.Len(t, lines, 2)
	assert.Equal(t, 9, lines[1].Number)
}
