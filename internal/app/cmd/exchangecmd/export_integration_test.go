package exchangecmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/app/cmd/exchangecmd"
	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

// runExport drives the real export command against a test database.
func runExport(t *testing.T, db *dbtest.DB, args ...string) (stdout, stderr string, exitCode int, err error) {
	t.Helper()

	var out, errOut bytes.Buffer

	exitCode = -1
	command := exchangecmd.NewExport(exchangecmd.ExportParams{
		Pool: lazy.New(func() (*pgxpool.Pool, error) { return db.Pool, nil }),
	}).Command
	app := &cli.App{
		Name:      "bitmagnet",
		Commands:  []*cli.Command{command},
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

	err = app.Run(append([]string{"bitmagnet", "export"}, args...))

	return out.String(), errOut.String(), exitCode, err
}

func seedTagged(t *testing.T, db *dbtest.DB, hash protocol.ID) {
	t.Helper()

	_, err := db.Pool.Exec(t.Context(), `INSERT INTO torrents (info_hash,name,size,private,created_at,updated_at)
VALUES ($1,'Tagged',10,false,now(),now())`, hash[:])
	require.NoError(t, err)
	_, err = db.Pool.Exec(t.Context(), `INSERT INTO torrent_tags (info_hash,name,created_at,updated_at)
VALUES ($1,'keep',now(),now())`, hash[:])
	require.NoError(t, err)
}

func TestExportWritesAFile(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	seedTagged(t, db, protocol.ID{1})

	path := filepath.Join(t.TempDir(), "catalogue.jsonl")

	stdout, stderr, exitCode, err := runExport(t, db, "--include-tags", path)
	require.NoError(t, err)
	assert.Equal(t, -1, exitCode)
	assert.Empty(t, stdout, "a file export writes nothing to stdout")
	assert.Empty(t, stderr)

	file, err := os.Open(path)
	require.NoError(t, err)

	defer func() { _ = file.Close() }()

	result, err := exchange.Check(file)
	require.NoError(t, err)
	require.Len(t, result.Headers, 1)
	assert.Equal(t, "magnes", result.Headers[0].Header.Producer.Val.Program.Val)
	assert.Equal(t, 1, result.Records)
	assert.Equal(t, []exchange.TagCount{{Tag: "keep", Count: 1}}, result.Tags)
	assert.Empty(t, result.Dropped)
}

func TestExportWritesStdoutWithoutTagsByDefault(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	seedTagged(t, db, protocol.ID{1})

	stdout, _, exitCode, err := runExport(t, db, "-")
	require.NoError(t, err)
	assert.Equal(t, -1, exitCode)

	result, err := exchange.Check(strings.NewReader(stdout))
	require.NoError(t, err)
	assert.Equal(t, 1, result.Records)
	assert.Empty(t, result.Tags)
	assert.NotContains(t, stdout, `"tags"`)
}

// A failed export never leaves a partial file that looks complete: the previous file
// survives untouched, and nothing else is left beside it.
func TestAFailedExportKeepsThePreviousFile(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	hash := protocol.ID{1}
	seedTagged(t, db, hash)
	_, err := db.Pool.Exec(t.Context(), `INSERT INTO torrent_contents (info_hash,content_type,created_at,updated_at)
VALUES ($1,'hologram',now(),now())`, hash[:])
	require.NoError(t, err)

	dir := t.TempDir()
	path := filepath.Join(dir, "catalogue.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("previous export\n"), 0o600))

	_, stderr, exitCode, err := runExport(t, db, path)
	require.Error(t, err)
	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderr, `"hologram"`)

	previous, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "previous export\n", string(previous))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a temporary file was left behind")
}

func TestExportNeedsExactlyOneOutput(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	_, stderr, exitCode, err := runExport(t, db)
	require.Error(t, err)
	assert.Equal(t, 2, exitCode)
	assert.Contains(t, stderr, "<file|->")
}
