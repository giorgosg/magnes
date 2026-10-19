package httpserver_test

import (
	"bytes"
	"net/http"
	"os"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/app/cmd/exchangecmd"
	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
	"go.uber.org/fx/fxtest"
)

// importedTables are the tables an import writes, each as the columns that do not record
// when a row was written. A queue job's id and timing are left out for the same reason.
var importedTables = map[string]string{
	"torrents":                 `to_jsonb(t) - 'created_at' - 'updated_at' - 'enhancement_checked_at'`,
	"torrent_files":            `to_jsonb(t) - 'created_at' - 'updated_at'`,
	"torrent_sources":          `to_jsonb(t) - 'created_at' - 'updated_at'`,
	"torrents_torrent_sources": `to_jsonb(t) - 'created_at' - 'updated_at'`,
	"torrent_hints":            `to_jsonb(t) - 'created_at' - 'updated_at'`,
	"torrent_tags":             `to_jsonb(t) - 'created_at' - 'updated_at'`,
	"queue_jobs": `jsonb_build_object('queue', queue, 'status', status, 'payload', payload,
'priority', priority, 'fingerprint', fingerprint)`,
}

// dumpImported returns every imported row of every table, in a stable order.
func dumpImported(t *testing.T, db *dbtest.DB) map[string]string {
	t.Helper()

	dump := make(map[string]string, len(importedTables))

	for table, row := range importedTables {
		var rows string
		require.NoError(t, db.Pool.QueryRow(t.Context(),
			`SELECT coalesce(jsonb_agg(r ORDER BY r::text), '[]')::text FROM (SELECT `+row+
				` AS r FROM `+table+` t) rows`).Scan(&rows))

		dump[table] = rows
	}

	return dump
}

// importByCLI runs the real import command on the fixture.
func importByCLI(t *testing.T, db *dbtest.DB) {
	t.Helper()

	lifecycle := fxtest.NewLifecycle(t)
	command := exchangecmd.NewImport(exchangecmd.ImportParams{
		Importer: lazy.New(func() (*importer.ExchangeImporter, error) {
			return importer.NewExchangeImporter(
				db.Gorm,
				nil,
				importer.ExchangeConfig{FilesThreshold: 100},
			), nil
		}),
		Lifecycle: lifecycle,
	}).Command

	lifecycle.RequireStart()

	var out bytes.Buffer

	app := &cli.App{Name: "bitmagnet", Commands: []*cli.Command{command}, Writer: &out, ErrWriter: &out}
	require.NoError(t, app.Run([]string{"bitmagnet", "import", "--origin", "alice", fixturePath}), out.String())
}

// An HTTP import of the frozen fixture writes exactly the rows the CLI import does,
// because both run the one import code path.
func TestImportOverHTTPWritesWhatTheCLIImportWrites(t *testing.T) {
	t.Parallel()

	byCLI, byHTTP := dbtest.New(t), dbtest.New(t)

	importByCLI(t, byCLI)

	fixture, err := os.Open(fixturePath)
	require.NoError(t, err)

	defer fixture.Close()

	rec := post(t, newDBEngine(t, byHTTP), "/import?origin=alice", "application/x-ndjson", fixture)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	want := dumpImported(t, byCLI)
	require.Contains(t, want["torrents"], "Example Discography", "the CLI import wrote nothing")
	require.Equal(t, want, dumpImported(t, byHTTP))
}
