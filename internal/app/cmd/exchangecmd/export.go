package exchangecmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

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

// exportUsage is the export command's synopsis.
const exportUsage = "export [--include-tags] [--since <time> [--overlap <duration>]] " + fileArgs

// NewExport builds `export [--include-tags] [--since <time> [--overlap <duration>]] <file|->`.
func NewExport(p ExportParams) Result {
	return Result{Command: &cli.Command{
		Name:  "export",
		Usage: "Export the visible catalogue as an exchange file",
		Description: "Writes every visible torrent as an exchange file, to a file or to standard output " +
			"for -. Pending and banned torrents are never written. A file is replaced only once the " +
			"export has succeeded. Tags are written only with --include-tags. For a same-version backup, " +
			"use the catalogue export (task export-data) instead.\n\n" +
			"With --since, the export is incremental: it writes only torrents whose exchanged data changed " +
			"at or after that RFC 3339 time, such as a previous export's exportedAt. It re-reads --overlap " +
			"before it, because a change is timestamped before it commits. Removed torrents, removed tags " +
			"and new bans are never conveyed; a receiver that mirrors deletions needs a full export.",
		ArgsUsage: fileArgs,
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "include-tags",
				Usage: "write each torrent's local tags as magnes.tags",
			},
			&cli.StringFlag{
				Name:  "since",
				Usage: "write only torrents changed at or after this RFC 3339 time",
			},
			&cli.StringFlag{
				Name:  "overlap",
				Value: defaultOverlap.String(),
				Usage: "with --since, also re-read changes timestamped this long before it",
			},
		},
		Action: p.export,
	}}
}

func (p ExportParams) export(ctx *cli.Context) error {
	if ctx.NArg() != 1 {
		return cli.Exit("usage: "+exportUsage, exitUsage)
	}

	since, overlap, err := parseSince(ctx)
	if err != nil {
		return cli.Exit(err.Error()+"\nusage: "+exportUsage, exitUsage)
	}

	pool, err := p.Pool.Get()
	if err != nil {
		return cli.Exit("export failed: "+err.Error(), exitFatal)
	}

	options := catalogue.ExchangeOptions{
		Producer:    producer(),
		IncludeTags: ctx.Bool("include-tags"),
		Since:       since,
		Overlap:     overlap,
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

// defaultOverlap is how far before --since an incremental export re-reads by default.
// updated_at is stamped from the writer's clock before its transaction commits, so a
// change can become visible after an export whose snapshot already passed its time.
// Re-sending a record is harmless, because an import is idempotent.
const defaultOverlap = time.Hour

// parseSince reads --since and --overlap. --since must carry its offset, as exportedAt
// does, so the cursor never depends on the local time zone.
func parseSince(ctx *cli.Context) (model.Maybe[time.Time], time.Duration, error) {
	if !ctx.IsSet("since") {
		if ctx.IsSet("overlap") {
			return model.Maybe[time.Time]{}, 0, errors.New("--overlap needs --since")
		}

		return model.Maybe[time.Time]{}, 0, nil
	}

	since, err := time.Parse(time.RFC3339Nano, ctx.String("since"))
	if err != nil {
		return model.Maybe[time.Time]{}, 0, fmt.Errorf("--since is not an RFC 3339 time: %w", err)
	}

	overlap, err := time.ParseDuration(ctx.String("overlap"))
	if err != nil {
		return model.Maybe[time.Time]{}, 0, fmt.Errorf("--overlap: %w", err)
	}

	if overlap < 0 {
		return model.Maybe[time.Time]{}, 0, errors.New("--overlap cannot be negative")
	}

	return model.MaybeValid(since.UTC()), overlap, nil
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
