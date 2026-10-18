package exchange_test

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drain reads a limited stream to its end or its fatal error, returning what it listed.
func drain(t *testing.T, src io.Reader, limits exchange.Limits) (
	[]exchange.Line, []*exchange.MalformedLineError, *exchange.Reader, error,
) {
	t.Helper()

	reader := exchange.NewLimitedReader(src, limits)

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

// endless is a line that never ends: reading it whole would never return.
type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}

	return len(p), nil
}

// A line longer than the limit stops the stream with an error naming the line, without
// reading the rest of it, so one enormous line cannot exhaust memory.
func TestLimitedReaderStopsAtALineThatIsTooLong(t *testing.T) {
	t.Parallel()

	record := `{"infoHash":"` + hashA + `","name":"longer than the header line"}`
	limits := exchange.Limits{MaxLineBytes: len(record)}

	lines, _, _, err := drain(t, strings.NewReader(header10+"\n"+record+"\n"+record+"x\n"+record+"\n"), limits)
	require.ErrorIs(t, err, exchange.ErrLineTooLong)
	assert.Contains(t, err.Error(), fmt.Sprintf("line 3: line too long: longer than %d bytes", len(record)))
	assert.Len(t, lines, 2, "the lines before the long one are read")

	limits.MaxLineBytes = len(header10)
	_, _, _, err = drain(t, io.MultiReader(strings.NewReader(header10+"\n"), endless{}), limits)
	require.ErrorIs(t, err, exchange.ErrLineTooLong)
}

// Distinct dropped entries are what an unknown field or value adds to the report, and a
// stream can make as many as it has lines. More than the limit stops the stream.
func TestLimitedReaderStopsAtTooManyDistinctDroppedEntries(t *testing.T) {
	t.Parallel()

	var input strings.Builder

	input.WriteString(header10 + "\n")

	for i := range 4 {
		fmt.Fprintf(&input, `{"infoHash":"%s","name":"x","unknown%d":1}`+"\n", hashA, i)
	}

	// The header is the first thing reported, so three distinct fields reach the limit.
	lines, _, reader, err := drain(t, strings.NewReader(input.String()), exchange.Limits{MaxReported: 4})
	require.ErrorIs(t, err, exchange.ErrTooMuchReported)
	assert.Contains(t, err.Error(), "line 5: too much to report: more than 4 headers, malformed lines "+
		"and distinct dropped entries")
	assert.Len(t, lines, 4, "the header and the three records before the limit are read")
	assert.Len(t, reader.Report(), 3)

	// The same field over and over is one entry, however often it repeats.
	repeated := header10 + "\n" + strings.Repeat(`{"infoHash":"`+hashA+`","name":"x","unknown":1}`+"\n", 10)
	_, _, _, err = drain(t, strings.NewReader(repeated), exchange.Limits{MaxReported: 2})
	require.NoError(t, err)
}

// Malformed lines and headers are listed one by one, so they count towards the limit too.
func TestLimitedReaderCountsMalformedLinesAndHeaders(t *testing.T) {
	t.Parallel()

	_, malformed, _, err := drain(t,
		strings.NewReader(header10+"\n"+strings.Repeat("not json\n", 5)), exchange.Limits{MaxReported: 3})
	require.ErrorIs(t, err, exchange.ErrTooMuchReported)
	assert.Contains(t, err.Error(), "line 4: ")
	assert.Len(t, malformed, 2)

	_, _, _, err = drain(t, strings.NewReader(strings.Repeat(header10+"\n", 5)), exchange.Limits{MaxReported: 3})
	require.ErrorIs(t, err, exchange.ErrTooMuchReported)
	assert.Contains(t, err.Error(), "line 4: ")
}

// Each string the report keeps is clipped, so a few entries cannot each hold a line's
// worth of memory.
func TestLimitedReaderClipsReportedStrings(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", 20)
	input := `{"format":{"name":"torrent-exchange","version":{"major":1,"minor":0}},` +
		`"producer":{"program":"` + long + `","version":"` + long + `"}}` + "\n" +
		`{"infoHash":"` + hashA + `","name":"x","` + long + `":1,` +
		`"magnes":{"classification":{"contentType":"` + long + `"}}}` + "\n" +
		`{"infoHash":"` + long + `","name":"x"}` + "\n"

	lines, malformed, reader, err := drain(t, strings.NewReader(input), exchange.Limits{MaxReportedBytes: 9})
	require.NoError(t, err)

	clipped := "éééé…"

	require.NotNil(t, lines[0].Header)
	assert.Equal(t, clipped, lines[0].Header.Producer.Val.Program.Val)
	assert.Equal(t, clipped, lines[0].Header.Producer.Val.Version.Val)

	require.Len(t, malformed, 1)
	assert.LessOrEqual(t, len(malformed[0].Err.Error()), 9+len("…"))

	paths, values := []string{}, []string{}
	for _, entry := range reader.Report() {
		paths = append(paths, entry.Path)
		values = append(values, entry.Value)
	}

	assert.Contains(t, paths, clipped, "an unknown field's name is clipped")
	assert.Contains(t, values, clipped, "an unknown value is clipped")
}

// Without limits, nothing is clipped or refused: the CLI reads the operator's own files.
func TestUnlimitedReaderKeepsEverything(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 100_000)
	input := header10 + "\n" + `{"infoHash":"` + hashA + `","name":"` + long + `","` + long + `":1}` + "\n"

	lines, _, reader, err := drain(t, strings.NewReader(input), exchange.Limits{})
	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, long, lines[1].Record.Name)
	require.Len(t, reader.Report(), 1)
	assert.Equal(t, long, reader.Report()[0].Path)
}
