package exchangecmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/urfave/cli/v2"
	"go.uber.org/fx"
)

const importUsage = "usage: import --origin <label> [--overwrite-hints] <file|->"

type ImportParams struct {
	fx.In
	// Importer opens the database, applying migrations, only once the arguments are valid.
	Importer  lazy.Lazy[*importer.ExchangeImporter]
	Lifecycle fx.Lifecycle
}

// NewImport builds `import --origin <label> [--overwrite-hints] <file|->`.
func NewImport(p ImportParams) Result {
	bound := bindToShutdown(p.Lifecycle)

	return Result{Command: &cli.Command{
		Name:  "import",
		Usage: "Import an exchange file into the catalogue",
		Description: "Reads an exchange file, or standard input for -, into the catalogue under the merge " +
			"rules in docs/exchange-file.md. Every incoming source is stored as <origin>:<source>. Name, " +
			"size and private stay local; files fill only an unknown or partial list; newer peer counts win; " +
			"tags are added; the classification becomes the hint, and an existing hint wins unless " +
			"--overwrite-hints is given. Every record passes ingestion moderation. Writes are committed " +
			"in batches, so an interrupted import keeps what it reported, and importing the file again is safe.",
		ArgsUsage: fileArgs,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "origin",
				Usage: "label for where the file came from, one lowercase kebab-case segment such as alice",
			},
			&cli.BoolFlag{
				Name:  "overwrite-hints",
				Usage: "replace existing hints with the incoming classifications",
			},
		},
		Action: bound.action(p.importFile),
	}}
}

func (p ImportParams) importFile(ctx context.Context, c *cli.Context) error {
	if c.NArg() != 1 {
		return cli.Exit(importUsage, exitUsage)
	}

	origin := c.String("origin")
	if !exchange.ValidOriginLabel(origin) {
		return cli.Exit(
			fmt.Sprintf("import: --origin must be one lowercase kebab-case label, such as alice; got %q",
				origin),
			exitUsage,
		)
	}

	src, closeSrc, err := open(c)
	if err != nil {
		return cli.Exit(err.Error(), exitFatal)
	}

	defer closeSrc()

	imp, err := p.Importer.Get()
	if err != nil {
		return cli.Exit("import failed: "+err.Error(), exitFatal)
	}

	report, importErr := imp.Import(ctx, src, importer.ExchangeOptions{
		Origin:         origin,
		OverwriteHints: c.Bool("overwrite-hints"),
	})

	writeErr := writeImportReport(c.App.Writer, report)

	switch {
	case importErr == nil && writeErr != nil:
		return cli.Exit("import: the report could not be written: "+writeErr.Error(), exitFatal)
	case importErr == nil:
		return nil
	case ctx.Err() != nil:
		return cli.Exit("import interrupted: the batches counted above were committed and the rest were not; "+
			"importing the file again is safe", exitFatal)
	default:
		return cli.Exit("import failed: "+importErr.Error(), exitFatal)
	}
}

// importSections are the reader's drop reasons followed by the import's own.
var importSections = append(slices.Clip(readerSections),
	dropSection{"invalid tags", importer.ReasonInvalidTag},
	dropSection{"classifications without a content type", importer.ReasonNoContentType},
	dropSection{"unknown work sources", importer.ReasonUnknownWorkSource},
	dropSection{"values out of range", importer.ReasonOutOfRange},
)

func writeImportReport(w io.Writer, report importer.ExchangeReport) error {
	var out strings.Builder

	writeStream(&out, report.Headers, report.Records, report.Malformed)

	fmt.Fprintf(&out, "torrents: new %d, updated %d, unchanged %d, banned %d, skipped %d\n",
		report.New, report.Updated, report.Unchanged, report.Banned, report.Skipped)
	fmt.Fprintf(&out, "hints: written %d, kept %d\n", report.HintsWritten, report.HintsKept)
	fmt.Fprintf(&out, "tags added: %d\n", report.TagsAdded)

	writeSections(&out, importSections, report.Dropped)

	_, err := io.WriteString(w, out.String())

	return err
}

// shutdownBound ties a command's context to the application's stop. The CLI runs commands
// with a background context, and Ctrl-C or SIGTERM reach them only as fx stopping, so the
// stop hook cancels the running action and waits for it to finish.
type shutdownBound struct {
	lifetime context.Context
	cancel   context.CancelFunc

	mu     sync.Mutex
	active chan struct{}
}

func bindToShutdown(lifecycle fx.Lifecycle) *shutdownBound {
	lifetime, cancel := context.WithCancel(context.Background())
	b := &shutdownBound{lifetime: lifetime, cancel: cancel}

	lifecycle.Append(fx.Hook{OnStop: b.stop})

	return b
}

func (b *shutdownBound) stop(ctx context.Context) error {
	b.cancel()

	b.mu.Lock()
	done := b.active
	b.mu.Unlock()

	if done == nil {
		return nil
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *shutdownBound) action(run func(context.Context, *cli.Context) error) cli.ActionFunc {
	return func(c *cli.Context) error {
		b.mu.Lock()

		if b.lifetime.Err() != nil {
			b.mu.Unlock()
			return cli.Exit("stopped before the command started", exitFatal)
		}

		done := make(chan struct{})
		b.active = done
		b.mu.Unlock()

		defer close(done)

		ctx, cancel := context.WithCancel(c.Context)
		defer cancel()

		stopCancelling := context.AfterFunc(b.lifetime, cancel)
		defer stopCancelling()

		err := run(ctx, c)
		if err == nil {
			return nil
		}

		// A failed command exits here, before releasing the stop hook. Once shutdown has
		// begun, fx exits 0 as soon as its stop completes, which would race an exit code
		// handled after this returns. A failure that only looks unrelated, such as the pool
		// closing, can arrive just before the stop hook cancels the context. Only an
		// ExitCoder sets an exit code, so any other error becomes one.
		var coder cli.ExitCoder
		if errors.As(err, &coder) {
			// HandleExitCoder checks the type directly, so a wrapped ExitCoder is unwrapped.
			err = coder
		} else {
			err = cli.Exit(err.Error(), exitFatal)
		}

		if c.App.ExitErrHandler != nil {
			c.App.ExitErrHandler(c, err)
		} else {
			cli.HandleExitCoder(err)
		}

		return nil
	}
}
