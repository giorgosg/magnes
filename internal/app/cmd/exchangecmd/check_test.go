package exchangecmd_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/app/cmd/exchangecmd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

const fixture = "../../../exchange/testdata/exchange-v1.jsonl"

// run drives the real command through a CLI app whose exit handler records the exit
// code instead of calling os.Exit.
func run(t *testing.T, stdin io.Reader, args ...string) (stdout, stderr string, exitCode int, err error) {
	t.Helper()

	var out, errOut bytes.Buffer

	exitCode = -1
	app := &cli.App{
		Name:      "bitmagnet",
		Commands:  []*cli.Command{exchangecmd.NewCheck().Command},
		Reader:    stdin,
		Writer:    &out,
		ErrWriter: &errOut,
		ExitErrHandler: func(_ *cli.Context, err error) {
			var coder cli.ExitCoder
			if err != nil && asExitCoder(err, &coder) {
				exitCode = coder.ExitCode()
				_, _ = errOut.WriteString(err.Error())
			}
		},
	}

	err = app.Run(append([]string{"bitmagnet", "check"}, args...))

	return out.String(), errOut.String(), exitCode, err
}

func asExitCoder(err error, target *cli.ExitCoder) bool {
	coder, ok := err.(cli.ExitCoder) //nolint:errorlint // cli.Exit errors are returned unwrapped
	if ok {
		*target = coder
	}

	return ok
}

func TestCheckReportsTheFixture(t *testing.T) {
	t.Parallel()

	stdout, stderr, exitCode, err := run(t, nil, fixture)
	require.NoError(t, err)
	assert.Equal(t, -1, exitCode)
	assert.Empty(t, stderr)
	assert.Equal(t, `headers: 2
  line 1: torrent-exchange 1.0, producer magnes 0.14.0, exported 2026-10-18T12:00:00Z
  line 4: torrent-exchange 1.0, producer torrents-csv-converter 1.2.0, exported 2026-10-18T13:30:00Z, `+
		`since 2026-10-11T00:00:00Z
records: 4
malformed lines: 0
unknown fields: none
nulls: none
unknown values: none
invalid source names: none
invalid entries: none
tags:
  4k-candidate: 1
  keep: 1
`, stdout)
}

func TestCheckReadsStdinAndListsWhatItWouldDrop(t *testing.T) {
	t.Parallel()

	hash := "c9e15763f722f23e98a29decdfae341b98d53056"
	input := `{"format":{"name":"torrent-exchange","version":{"major":1,"minor":2}},"compression":"zstd"}` + "\n" +
		`{"infoHash":"` + hash + `","name":"a","size":null,"sources":[{"source":"Bad Name","completed":1}],` +
		`"magnes":{"classification":{"video":{"codec":"hevc"},"episodes":[{"season":1}]}}}` + "\n" +
		"{oops\n" +
		`{"infoHash":"` + hash + `","name":"b","sources":[{"source":"dht","completed":2}]}` + "\n"

	stdout, _, exitCode, err := run(t, strings.NewReader(input), "-")
	require.NoError(t, err)
	assert.Equal(t, -1, exitCode, "malformed lines and drops are not fatal")
	assert.Equal(t, `headers: 1
  line 1: torrent-exchange 1.2, producer unknown
records: 2
malformed lines: 1
  line 3: invalid character 'o' looking for beginning of object key string
unknown fields:
  header compression: 1
  record sources[].completed: 2
nulls:
  record size: 1
unknown values:
  record magnes.classification.video.codec "hevc": 1
invalid source names:
  record sources[].source "Bad Name": 1
invalid entries:
  record magnes.classification.episodes[]: 1
tags: none
`, stdout)
}

func TestCheckExitsNonZeroOnAFatalError(t *testing.T) {
	t.Parallel()

	hash := "c9e15763f722f23e98a29decdfae341b98d53056"

	for name, tc := range map[string]struct {
		input string
		want  string
	}{
		"unknown major version": {
			`{"format":{"name":"torrent-exchange","version":{"major":2,"minor":0}}}` + "\n",
			"line 1: unsupported major version: 2.0",
		},
		"wrong format name": {
			`{"format":{"name":"something-else","version":{"major":1,"minor":0}}}` + "\n",
			`line 1: unsupported format name: "something-else"`,
		},
		"record before the first header": {
			`{"infoHash":"` + hash + `","name":"a"}` + "\n",
			"line 1: record before the first header",
		},
	} {
		path := filepath.Join(t.TempDir(), "input.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(tc.input), 0o600))

		stdout, stderr, exitCode, err := run(t, nil, path)
		require.Error(t, err, name)
		assert.Equal(t, 1, exitCode, name)
		assert.Contains(t, stderr, tc.want, name)
		assert.Contains(t, stdout, "records: 0", "%s: the summary of what was read is still printed", name)
	}
}

func TestCheckNeedsExactlyOneInput(t *testing.T) {
	t.Parallel()

	_, stderr, exitCode, err := run(t, nil)
	require.Error(t, err)
	assert.Equal(t, 2, exitCode)
	assert.Contains(t, stderr, "<file|->")

	_, _, exitCode, err = run(t, nil, filepath.Join(t.TempDir(), "missing.jsonl"))
	require.Error(t, err)
	assert.Equal(t, 1, exitCode)
}
