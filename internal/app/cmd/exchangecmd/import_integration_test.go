package exchangecmd_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/app/cmd/exchangecmd"
	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
	"go.uber.org/fx/fxtest"
)

const importHeader = `{"format":{"name":"torrent-exchange","version":{"major":1,"minor":0}}}`

type importRun struct {
	stdout, stderr string
	exitCode       int
	err            error
}

// runImport drives the real import command against a test database. The importer is
// built only if the command asks for it, which is how a usage error is shown to come
// before the database is opened.
func runImport(t *testing.T, db *dbtest.DB, lifecycle *fxtest.Lifecycle, stdin io.Reader, args ...string) importRun {
	t.Helper()

	var out, errOut bytes.Buffer

	run := importRun{exitCode: -1}
	command := exchangecmd.NewImport(exchangecmd.ImportParams{
		Importer: lazy.New(func() (*importer.ExchangeImporter, error) {
			if db == nil {
				t.Error("the command opened the database")
				return nil, io.EOF
			}

			return importer.NewExchangeImporter(
				db.Gorm,
				nil,
				importer.ExchangeConfig{FilesThreshold: 100, BatchSize: 1},
			), nil
		}),
		Lifecycle: lifecycle,
	}).Command
	// fx stops only hooks that started, so start once the command has registered its own.
	lifecycle.RequireStart()

	app := &cli.App{
		Name:      "bitmagnet",
		Commands:  []*cli.Command{command},
		Reader:    stdin,
		Writer:    &out,
		ErrWriter: &errOut,
		ExitErrHandler: func(_ *cli.Context, err error) {
			var coder cli.ExitCoder
			if err != nil && asExitCoder(err, &coder) {
				run.exitCode = coder.ExitCode()
				_, _ = errOut.WriteString(err.Error())
			}
		},
	}

	run.err = app.Run(append([]string{"bitmagnet", "import"}, args...))
	run.stdout, run.stderr = out.String(), errOut.String()

	return run
}

func TestImportRefusesBadUsageBeforeOpeningAnything(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing.jsonl")

	for _, args := range [][]string{
		{missing},
		{"--origin", "Alice", missing},
		{"--origin", "alice:bob", missing},
		{"--origin", "alice"},
		{"--origin", "alice", missing, missing},
	} {
		run := runImport(t, nil, fxtest.NewLifecycle(t), strings.NewReader(""), args...)
		assert.Equal(t, 2, run.exitCode, "args %q", args)
	}
}

func TestImportWritesAFileAndReportsWhatItDid(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	path := filepath.Join(t.TempDir(), "in.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(importHeader+"\n"+
		`{"infoHash":"`+protocol.ID{1}.String()+`","name":"New","sources":[{"source":"dht","seeders":3}],`+
		`"magnes":{"tags":["keep","Bad Tag"],"classification":{"inferredTitle":"x"}},"surprise":1}`+"\n"+
		"not json\n"), 0o600))

	run := runImport(t, db, fxtest.NewLifecycle(t), strings.NewReader(""), "--origin", "alice", path)
	require.NoError(t, run.err)
	assert.Equal(t, -1, run.exitCode)
	assert.Empty(t, run.stderr)

	for _, line := range []string{
		"records: 1\n",
		"malformed lines: 1\n  line 3: ",
		"torrents: new 1, updated 0, unchanged 0, banned 0, skipped 0\n",
		"hints: written 0, kept 0\n",
		"tags added: 1\n",
		"unknown fields:\n  record surprise: 1\n",
		"invalid tags:\n  record magnes.tags[] \"Bad Tag\": 1\n",
		"classifications without a content type:\n  record magnes.classification: 1\n",
	} {
		assert.Contains(t, run.stdout, line)
	}

	var source string
	require.NoError(t, db.Pool.QueryRow(t.Context(), `SELECT source FROM torrents_torrent_sources`).Scan(&source))
	assert.Equal(t, "alice:dht", source)
}

func TestImportReadsStandardInputAndOverwritesHintsOnRequest(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	_, err := db.Pool.Exec(t.Context(), `INSERT INTO torrents (info_hash,name,size,private,created_at,updated_at)
VALUES ($1,'Local',1,false,now(),now())`, protocol.ID{1}.Bytes())
	require.NoError(t, err)
	_, err = db.Pool.Exec(t.Context(), `INSERT INTO torrent_hints (info_hash,content_type,created_at,updated_at)
VALUES ($1,'movie',now(),now())`, protocol.ID{1}.Bytes())
	require.NoError(t, err)

	record := `{"infoHash":"` + protocol.ID{1}.String() +
		`","name":"x","magnes":{"classification":{"contentType":"ebook"}}}`
	run := runImport(t, db, fxtest.NewLifecycle(t), strings.NewReader(importHeader+"\n"+record+"\n"),
		"--origin", "alice", "--overwrite-hints", "-")
	require.NoError(t, run.err)
	assert.Contains(t, run.stdout, "hints: written 1, kept 0\n")

	var contentType string
	require.NoError(t, db.Pool.QueryRow(t.Context(), `SELECT content_type FROM torrent_hints`).Scan(&contentType))
	assert.Equal(t, "ebook", contentType)
}

// Ctrl-C and SIGTERM reach the command as the application stopping. An import waiting
// mid-file must then stop promptly, say so, and still print what it committed.
func TestImportStopsWhenTheApplicationStops(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	lifecycle := fxtest.NewLifecycle(t)
	stdin, input := io.Pipe()

	t.Cleanup(func() { _ = input.Close() })

	go func() {
		_, _ = io.WriteString(
			input,
			importHeader+"\n"+`{"infoHash":"`+protocol.ID{1}.String()+`","name":"First"}`+"\n",
		)
	}()

	done := make(chan importRun, 1)

	go func() { done <- runImport(t, db, lifecycle, stdin, "--origin", "alice", "-") }()

	require.Eventually(t, func() bool {
		var n int
		require.NoError(t, db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM torrents`).Scan(&n))

		return n == 1
	}, 5*time.Second, 10*time.Millisecond, "the first batch should commit")

	stopped := make(chan error, 1)

	go func() { stopped <- lifecycle.Stop(t.Context()) }()

	select {
	case run := <-done:
		assert.Equal(t, 1, run.exitCode)
		assert.Contains(t, run.stderr, "import interrupted")
		assert.Contains(t, run.stdout, "torrents: new 1,")
	case <-time.After(5 * time.Second):
		t.Fatal("the import did not stop when the application stopped")
	}

	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the application's stop waited forever on the import")
	}
}
