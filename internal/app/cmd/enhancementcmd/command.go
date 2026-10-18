// Package enhancementcmd exposes bounded metadata experiments and operator controls.
package enhancementcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/database/dao"
	"github.com/bitmagnet-io/bitmagnet/internal/database/postgres"
	"github.com/bitmagnet-io/bitmagnet/internal/enhancement"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/processor"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/bitmagnet-io/bitmagnet/internal/tmdb"
	"github.com/jackc/pgx/v5"
	"github.com/urfave/cli/v2"
	"go.uber.org/fx"
)

type Params struct {
	fx.In
	Lifecycle fx.Lifecycle
	Config    enhancement.Config
	Postgres  postgres.Config
	Catalogue lazy.Lazy[tmdb.Client]
	Dao       lazy.Lazy[*dao.Query]
	Processor lazy.Lazy[processor.Processor]
	Tracer    pgx.QueryTracer `optional:"true"`
}
type Result struct {
	fx.Out
	Command *cli.Command `group:"commands"`
}
type Report struct {
	Accounting        string                   `json:"accounting"`
	Model             string                   `json:"model"`
	RecipeFingerprint string                   `json:"recipeFingerprint"`
	Recipe            json.RawMessage          `json:"recipe"`
	Evaluations       []enhancement.Evaluation `json:"evaluations"`
}

const (
	evaluateCommand = "evaluate"
	processCommand  = "process"
	resumeCommand   = "resume"
)

func New(p Params) Result {
	action := lifecycleAction(p)

	result := Result{
		Command: &cli.Command{
			Name:  "enhancement",
			Usage: "Inspect LLM metadata enhancement without applying results",
			Subcommands: []*cli.Command{
				{
					Name: evaluateCommand,
					Description: "Runs explicit experiments again regardless of saved fingerprints. No migrations, " +
						"workers, metadata, recipe, evaluation or shared budget writes. Spending is " +
						"process-local; concurrent commands multiply the allowance. Save returned JSON for " +
						"comparisons.",
					Usage: "Read visible torrents and return JSON; performs no database writes",
					Flags: []cli.Flag{
						&cli.StringSliceFlag{
							Name:  "hash",
							Usage: "Explicit visible torrent infohash; repeatable (maximum 100)",
						},
						&cli.IntFlag{
							Name:  "sample",
							Usage: "Deterministic visible sample ordered by infohash (maximum 100)",
						},
						&cli.StringFlag{
							Name: "base-url",
						},
						&cli.StringFlag{
							Name: "thinking", Value: p.Config.Thinking,
						},
						&cli.StringFlag{
							Name:  "reasoning-effort",
							Value: p.Config.ReasoningEffort,
							Usage: "low, high or max; empty leaves the provider default",
						},
						&cli.StringFlag{
							Name:  "reasoning-parameter",
							Value: p.Config.ReasoningParameter,
							Usage: "thinking (DeepSeek API) or reasoning (OpenRouter)",
						},
						&cli.StringSliceFlag{
							Name:  "provider-order",
							Value: cli.NewStringSlice(p.Config.ProviderOrder...),
							Usage: "OpenRouter provider slug to pin, without fallbacks; repeatable",
						},
						&cli.StringFlag{
							Name:  "output-token-parameter",
							Value: p.Config.OutputTokenParameter,
						},
						&cli.StringFlag{Name: "model"},
						&cli.StringFlag{
							Name:  "api-key-env",
							Value: "BITMAGNET_ENHANCEMENT_API_KEY",
							Usage: "Environment variable containing the provider secret",
						},
						&cli.StringFlag{
							Name:  "prompt-file",
							Usage: "Additional operator instructions",
						},
						&cli.Float64Flag{Name: "temperature", Value: p.Config.Temperature},
						&cli.IntFlag{
							Name:  "concurrency",
							Value: p.Config.Concurrency,
						},
						&cli.IntFlag{Name: "max-requests", Value: p.Config.MaxRequests},
						&cli.IntFlag{Name: "max-tool-calls", Value: p.Config.MaxToolCalls},
						&cli.IntFlag{
							Name:  "request-output-tokens",
							Value: p.Config.RequestOutputTokens,
						},
						&cli.IntFlag{
							Name:  "max-output-tokens",
							Value: p.Config.MaxOutputTokens,
						},
						&cli.DurationFlag{Name: "timeout", Value: p.Config.Timeout},
						&cli.IntFlag{
							Name:  "run-max-requests",
							Value: p.Config.MaxRunRequests,
						},
						&cli.IntFlag{
							Name:  "run-max-output-tokens",
							Value: p.Config.MaxRunOutputTokens,
						},
						&cli.DurationFlag{Name: "run-timeout", Value: p.Config.RunTimeout},
						&cli.Float64Flag{
							Name:  "run-max-cost",
							Value: p.Config.MaxRunCost,
							Usage: "Positive whole-command spending ceiling; requires conservative token prices",
						},
						&cli.Float64Flag{
							Name:  "input-cost-per-million",
							Value: p.Config.InputCostPerMillion,
						},
						&cli.Float64Flag{
							Name:  "output-cost-per-million",
							Value: p.Config.OutputCostPerMillion,
						},
					},
					Action: action,
				},
			},
		},
	}

	evaluate := result.Command.Subcommands[0]
	for _, mode := range []string{"preview", "status", processCommand, resumeCommand} {
		command := *evaluate
		command.Name = mode

		command.Flags = append([]cli.Flag(nil), evaluate.Flags...)

		switch mode {
		case "preview":
			command.Description = "Shows bounded evidence and eligibility without provider calls. ordinary_required " +
				"needs ordinary classification; unchanged_completed suppresses the same input/recipe; " +
				"exhausted_failure needs explicit retry or resume. Admission still rechecks " +
				"freshness, moderation and spending."
			command.Usage = "Preview bounded visible evidence and evaluation state; no writes or provider calls"
		case "status":
			command.Description = "Shows latest completed outcome separately from last attempt and retained accepted " +
				"output, persistent pause and latest seven UTC spending totals. Failed or stale " +
				"attempts do not erase valid accepted output. Daily charges include conservative " +
				"unknown usage; per-run token/latency history is not retained."
			command.Usage = "Inspect bounded latest completion, retained output, pause and daily spending; no writes"
		case resumeCommand:
			command.Flags = nil
			command.Description = "Correct provider configuration first. Clears the durable pause and readmits " +
				"provider/malformed failures. Does not enable automatic processing, start workers, " +
				"reset spending, or retry unchanged completed limited outcomes."
			command.Usage = "Explicitly clear persistent pause and readmit exhausted failures; starts no workers"
		case processCommand:
			command.Description = "Requires explicit hashes, positive configured daily_budget and run-max-cost, " +
				"endpoint, model and token prices. Shares claim capacity, pause and daily ledger with " +
				"automatic workers. Completed accepted/unresolved/limited input+recipe suppress " +
				"repeats unless force/rematch is explicit. Force does not grant authority to replace " +
				"importer values or existing works; rematch does. Failures retain valid accepted " +
				"output. First-request budget denial defers; paid interruption becomes limited. A " +
				"model answer can still be rejected as stale before application."
			command.Usage = "Persist explicit hashes with shared concurrency/daily budget, even when automatic is off"
			command.Flags = append(
				command.Flags,
				&cli.BoolFlag{
					Name:  "force",
					Usage: "Retry unchanged input/recipe; does not grant rematch authority",
				},
				&cli.BoolFlag{
					Name:  "rematch",
					Usage: "Explicitly permit replacing importer hints or existing work identity",
				},
			)
		}

		result.Command.Subcommands = append(result.Command.Subcommands, &command)
	}

	result.Command.Usage = "Bounded metadata evaluation, processing and operator inspection"

	return result
}

// The shared CLI currently runs commands with a Background context. Bind only this
// command to fx shutdown so SIGINT/termination cancels its reads and provider calls,
// and wait for its bounded result reporting before application shutdown completes.
func lifecycleAction(p Params) cli.ActionFunc {
	lifetime, cancelLifetime := context.WithCancel(context.Background())

	var (
		mu     sync.Mutex
		active chan struct{}
	)

	p.Lifecycle.Append(fx.Hook{OnStop: func(ctx context.Context) error {
		cancelLifetime()
		mu.Lock()
		done := active
		mu.Unlock()

		if done == nil {
			return nil
		}

		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})

	return func(c *cli.Context) error {
		mu.Lock()
		if lifetime.Err() != nil {
			mu.Unlock()
			return nil
		}

		done := make(chan struct{})
		active = done
		mu.Unlock()

		defer close(done)

		ctx, cancel := context.WithCancel(c.Context)

		stop := context.AfterFunc(lifetime, cancel)
		defer stop()
		defer cancel()

		//nolint:contextcheck // Caller context comes from c.Context and gains fx cancellation.
		err := run(ctx, c, p)
		// Shutdown can interrupt evidence loading before a report exists. The global
		// CLI treats returned errors as panics; an intentional shutdown is not a failure.
		if lifetime.Err() != nil {
			return nil
		}

		return err
	}
}

func run(parent context.Context, c *cli.Context, p Params) error {
	cfg := p.Config
	if c.IsSet("base-url") {
		cfg.BaseURL = c.String("base-url")
	}

	if c.IsSet("model") {
		cfg.Model = c.String("model")
	}

	if key := os.Getenv(c.String("api-key-env")); key != "" {
		cfg.APIKey = key
	}

	if c.IsSet("prompt-file") {
		b, err := os.ReadFile(c.String("prompt-file"))
		if err != nil {
			return errors.New("cannot read prompt file")
		}

		if len(b) > 4000 {
			return errors.New("prompt file exceeds 4000 bytes")
		}

		cfg.Prompt = string(b)
	}

	cfg.Thinking = c.String("thinking")
	cfg.ReasoningEffort = c.String("reasoning-effort")
	cfg.ReasoningParameter = c.String("reasoning-parameter")
	cfg.ProviderOrder = c.StringSlice("provider-order")
	cfg.OutputTokenParameter = c.String("output-token-parameter")
	cfg.Temperature = c.Float64("temperature")
	cfg.Concurrency = c.Int("concurrency")
	cfg.MaxRequests = c.Int("max-requests")
	cfg.MaxToolCalls = c.Int("max-tool-calls")
	cfg.RequestOutputTokens = c.Int("request-output-tokens")
	cfg.MaxOutputTokens = c.Int("max-output-tokens")
	cfg.Timeout = c.Duration("timeout")
	cfg.MaxRunRequests = c.Int("run-max-requests")
	cfg.MaxRunOutputTokens = c.Int("run-max-output-tokens")
	cfg.RunTimeout = c.Duration("run-timeout")
	cfg.MaxRunCost = c.Float64("run-max-cost")
	cfg.InputCostPerMillion = c.Float64("input-cost-per-million")

	cfg.OutputCostPerMillion = c.Float64("output-cost-per-million")
	if c.Command.Name == resumeCommand {
		return resume(parent, c, p, cfg)
	}

	if c.Command.Name == evaluateCommand || c.Command.Name == processCommand {
		if cfg.MaxRunCost <= 0 {
			return errors.New(
				"evaluation requires positive run-max-cost and conservative input/output token prices",
			)
		}

		if err := cfg.Validate(); err != nil {
			return err
		}
	}

	hashes := make([]protocol.ID, 0)

	for _, h := range c.StringSlice("hash") {
		hash, parseErr := protocol.ParseID(h)
		if parseErr != nil {
			return errors.New("invalid torrent infohash")
		}

		hashes = append(hashes, hash)
	}

	sample := c.Int("sample")
	if len(hashes) > 100 || sample > 100 || sample < 0 || (len(hashes) == 0 && sample == 0) ||
		(len(hashes) > 0 && sample > 0) {
		return errors.New("select either 1-100 hashes or --sample 1-100")
	}

	if c.Command.Name == processCommand {
		if cfg.Concurrency != p.Config.Concurrency {
			return errors.New(
				"persistent processing uses configured shared concurrency; " +
					"concurrency overrides are for read-only experiments",
			)
		}

		required := cfg

		required.Automatic = true
		if err := required.Validate(); err != nil {
			return err
		}

		if sample != 0 {
			return errors.New("persistent processing requires explicit --hash selection")
		}

		return process(parent, c, p, cfg, hashes)
	}

	ctx, cancel := context.WithTimeout(parent, cfg.RunTimeout)
	defer cancel()

	connCfg, err := pgx.ParseConfig(p.Postgres.CreateDSN())
	if err != nil {
		return errors.New("invalid PostgreSQL configuration")
	}
	// These settings apply in the startup packet, before any application query.
	connCfg.RuntimeParams["default_transaction_read_only"] = "on"
	connCfg.Tracer = p.Tracer

	conn, err := pgx.ConnectConfig(ctx, connCfg)
	if err != nil {
		return errors.New("cannot open read-only catalogue connection")
	}

	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer closeCancel()

		_ = conn.Close(closeCtx)
	}()

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return errors.New("cannot begin read-only catalogue transaction")
	}

	evidence, loadErr := enhancement.LoadEvidence(ctx, tx, hashes, sample)
	rollbackErr := tx.Rollback(ctx)

	if loadErr != nil {
		return fmt.Errorf("cannot load visible catalogue evidence: %w", loadErr)
	}

	if rollbackErr != nil {
		return errors.New("cannot close read-only catalogue transaction")
	}

	if c.Command.Name != evaluateCommand {
		return inspect(ctx, c, conn, cfg, evidence)
	}

	engine, err := enhancement.NewEvaluator(cfg, p.Catalogue, http.DefaultClient)
	if err != nil {
		return err
	}

	report := Report{
		Accounting:  "process-local only; no shared database budget ledger is used and no evaluation state is written",
		Model:       cfg.Model,
		Evaluations: make([]enhancement.Evaluation, len(evidence)),
	}
	report.RecipeFingerprint, _, _ = enhancement.Recipe(cfg, "read-only-experiment")
	_, canonical, _ := enhancement.Recipe(cfg, "read-only-experiment")
	report.Recipe = json.RawMessage(canonical)
	jobs := make(chan int)

	var wg sync.WaitGroup
	for range min(cfg.Concurrency, len(evidence)) {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for i := range jobs {
				report.Evaluations[i] = engine.Evaluate(ctx, evidence[i])
			}
		}()
	}

	for i := range evidence {
		jobs <- i
	}

	close(jobs)
	wg.Wait()

	return json.NewEncoder(c.App.Writer).Encode(report)
}
