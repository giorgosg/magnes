package exchangecmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bitmagnet-io/bitmagnet/internal/database/catalogue"
	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/version"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/urfave/cli/v2"
	"go.uber.org/fx"
)

// producerProgram names this program in the headers it writes.
const producerProgram = "magnes"

type ExportParams struct {
	fx.In
	// Pool reads the catalogue directly. It never opens gorm, so the export applies no
	// migrations: it only reads.
	Pool lazy.Lazy[*pgxpool.Pool]
}

// NewExport builds `export [--include-tags] <file|->`.
func NewExport(p ExportParams) Result {
	return Result{Command: &cli.Command{
		Name:  "export",
		Usage: "Export the visible catalogue as an exchange file",
		Description: "Writes every visible torrent as an exchange file, to a file or to standard output " +
			"for -. Pending and banned torrents are never written. A file is replaced only once the " +
			"export has succeeded. Tags are written only with --include-tags. For a same-version backup, " +
			"use the catalogue export (task export-data) instead.",
		ArgsUsage: "<file|->",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "include-tags",
				Usage: "write each torrent's local tags as magnes.tags",
			},
		},
		Action: p.export,
	}}
}

func (p ExportParams) export(ctx *cli.Context) error {
	if ctx.NArg() != 1 {
		return cli.Exit("usage: export [--include-tags] <file|->", exitUsage)
	}

	pool, err := p.Pool.Get()
	if err != nil {
		return cli.Exit("export failed: "+err.Error(), exitFatal)
	}

	options := catalogue.ExchangeOptions{
		Producer:    producer(),
		IncludeTags: ctx.Bool("include-tags"),
	}

	exportTo := func(w io.Writer) error {
		return catalogue.ExportExchange(ctx.Context, pool, w, options)
	}

	if name := ctx.Args().First(); name == "-" {
		err = exportTo(ctx.App.Writer)
	} else {
		err = writeFileAtomically(name, exportTo)
	}

	if err != nil {
		return cli.Exit("export failed: "+err.Error(), exitFatal)
	}

	return nil
}

func producer() exchange.Producer {
	p := exchange.Producer{Program: model.MaybeValid(producerProgram)}
	if version.GitTag != "" {
		p.Version = model.MaybeValid(version.GitTag)
	}

	return p
}

// writeFileAtomically writes to a temporary file beside name and renames it into place
// only once fill has succeeded, so a failed export leaves any previous file intact and
// never leaves a partial file under the requested name.
func writeFileAtomically(name string, fill func(io.Writer) error) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".*.partial")
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	buffered := bufio.NewWriter(tmp)
	if err = fill(buffered); err != nil {
		return err
	}

	if err = buffered.Flush(); err != nil {
		return err
	}

	if err = tmp.Sync(); err != nil {
		return err
	}

	if err = tmp.Close(); err != nil {
		return err
	}

	if err = os.Rename(tmp.Name(), name); err != nil {
		return fmt.Errorf("replace %s: %w", name, err)
	}

	return nil
}
