package app_test

import (
	"bytes"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/app/appfx"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
	"go.uber.org/fx"
	"gorm.io/gorm"
)

var errDatabaseOpened = errors.New("the database was opened")

// `check` runs through the real application graph, with every database handle replaced
// by one that records being opened and fails. Migrations run only when the database is
// first opened, so this also shows that none are applied.
func TestCheckRunsWithoutADatabase(t *testing.T) {
	t.Parallel()

	var opened atomic.Bool

	var (
		cliApp *cli.App
		db     lazy.Lazy[*gorm.DB]
	)

	app := fx.New(
		appfx.New(),
		fx.NopLogger,
		fx.Decorate(
			func() lazy.Lazy[*sql.DB] {
				return lazy.New(func() (*sql.DB, error) {
					opened.Store(true)
					return nil, errDatabaseOpened
				})
			},
			func() lazy.Lazy[*pgxpool.Pool] {
				return lazy.New(func() (*pgxpool.Pool, error) {
					opened.Store(true)
					return nil, errDatabaseOpened
				})
			},
		),
		fx.Populate(&cliApp, &db),
	)
	require.NoError(t, app.Err())

	var out bytes.Buffer

	cliApp.Writer = &out
	cliApp.After = nil

	require.NoError(t, cliApp.RunContext(t.Context(),
		[]string{"bitmagnet", "check", "../exchange/testdata/exchange-v1.jsonl"}))
	assert.Contains(t, out.String(), "records: 4\n")
	assert.False(t, opened.Load(), "check opened the database")

	// The replacement is the one the rest of the graph sees, so the check above is real.
	_, err := db.Get()
	require.ErrorIs(t, err, errDatabaseOpened)
}

func TestExportIsRegistered(t *testing.T) {
	t.Parallel()

	var cliApp *cli.App

	app := fx.New(appfx.New(), fx.NopLogger, fx.Populate(&cliApp))
	require.NoError(t, app.Err())

	command := cliApp.Command("export")
	require.NotNil(t, command, "the export command is not registered")
	assert.Equal(t, "<file|->", command.ArgsUsage)
}
