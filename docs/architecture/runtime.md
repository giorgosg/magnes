# Runtime

What starts what, and in what order. Read this before debugging a startup failure, a
shutdown hang, or "why is my new service never constructed".

## The binary does almost nothing

`main.go` builds an fx app and runs it. `internal/app/app.go` composes three things:
`appfx.New()` (every module), the logger, and one `fx.Invoke` whose only job is to force
construction of the `*cli.App` and the lifecycle hooks. fx does the rest.

```go
fx.New(appfx.New(), loggingfx.WithLogger(), fx.Invoke(...)).Run()
```

## fx assembly

[`internal/app/appfx/module.go`](../../internal/app/appfx/module.go) is the single list of
modules. Every subsystem contributes exactly one line there. As of this snapshot:

```
authfx  classifierfx  configfx  databasefx  dhtcrawlerfx  dhtfx
gqlfx   healthfx    httpserverfx  importerfx  loggingfx  metainfofx   metricsfx
moderationfx  processorfx  queuefx  telemetryfx  tmdbfx  torznabfx  validationfx  versionfx  workerfx
```

Three fx idioms carry most of the weight, and you will not read the code successfully
without them:

- **Value groups.** `group:"workers"`, `group:"http_server_options"`,
  `group:"config_specs"`, `group:"auth_object_actions"`, `group:"commands"`,
  `group:"prometheus_collectors"`. A subsystem adds itself to a group; something else
  collects the whole group. This is why grep for a caller often finds nothing — the wiring
  is by group tag, not by reference.
- **`lazy.Lazy[T]`** (`internal/lazy`). Constructors take `lazy.Lazy[*dao.Query]` rather
  than `*dao.Query` so that a command like `bitmagnet config show` does not open a
  database connection just by existing. `.Get()` is what actually constructs, and it is
  called inside `OnStart`, not in the factory.
- **Named dependencies.** `` `name:"dht_discovered_nodes"` ``,
  `` `name:"metainfo_banning_checker"` ``. Used where two values share a type.

`fx.Decorate(migrations.NewDecorator)` in the same file is what makes migrations run
before anything touches the database.

## Workers

Anything long-running is a `worker.Worker` — a key plus an `fx.Hook` — contributed to the
`workers` group and collected by `internal/worker`'s registry. Registered workers include
`dht_crawler`, `dht_server`, `http_server`, `queue_server`, and the metrics/telemetry
collectors. The `enhancement` worker is also registered but inert unless explicitly
configured for automatic operation with a positive daily budget. Its shutdown cancels
its contexts and waits for in-flight handlers.

The registry is **not** driven by fx's own lifecycle. Nothing starts until a CLI command
calls `Registry.Enable(...)` then `Registry.Start(ctx)`:

```bash
bitmagnet worker run --all
bitmagnet worker run --keys=http_server --keys=queue_server
bitmagnet worker list
```

`internal/app/cmd/workercmd` is that command. `Start` returns `ErrNoWorkersEnabled` if the
selection was empty, and `After:` calls `Registry.Stop`, which walks the started workers
calling their `OnStop`.

The registry starts workers in map order. Authentication decorates `http_server` so the
one-time anonymous-role reset runs before its listener opens, even if that is the only
worker selected; the standalone auth reset worker may start before or after it.

The consequence worth carrying: **a worker's `OnStart` is where its real construction
happens**, because that is the first point at which `lazy.Lazy` dependencies are resolved
and errors can be returned. A factory that does work eagerly breaks the CLI commands.

## Shutdown

`Registry.Stop` calls each `OnStop` in map-iteration order, logging and continuing past
errors. Individual workers vary in how completely they stop:

- `httpserver` calls `http.Server.Shutdown(ctx)` and waits.
- `queue/server` cancels the context its handlers select on.
- `dhtcrawler` closes a `stopped` channel and returns **immediately**, without waiting for
  its seventeen pipeline goroutines to drain
  ([`factory.go`](../../internal/dhtcrawler/factory.go), `OnStop`). In-flight database
  writes race the process exit.

This area has a history: a cherry-pick that compiled, vetted and passed the whole suite
once carried a shutdown deadlock. Treat any change to a stop path as security-grade.

## CLI commands

`internal/app/cmd/`, each contributing a `*cli.Command` to the `commands` group:

| Command       | Does                                                                                  |
| ------------- | ------------------------------------------------------------------------------------- |
| `auth`        | `initial-invitation` — print the outstanding bootstrap code                           |
| `worker`      | `run` / `list` — the normal way to run the application                                |
| `process`     | Process specific infohashes through the classifier now                                |
| `reprocess`   | Re-run classification in bulk over the existing catalogue                             |
| `classifier`  | Inspect and test the classifier configuration and its JSON schema                     |
| `enhancement` | `evaluate`, `preview`, `status`, `process`, `resume` — bounded enhancement operations |
| `config`      | Show and validate resolved configuration                                              |
| `check`       | Read an [exchange file](../exchange-file.md) without importing it; needs no database  |
| `export`      | Write the visible catalogue as an [exchange file](../exchange-file.md)                |
| `import`      | Read an [exchange file](../exchange-file.md) into the catalogue under the merge rules |

`internal/dev` is a **separate binary** with its own `commands` group, and nothing in it
reaches the shipped surface above: `migrate` and `gorm` (the generators `task` calls), and
`fixture serve`, which stands up a real authenticated instance over a clone of the
btm-testdb seed template for an external harness to drive. The engine that last one serves
is built by `internal/dev/fixtureserver`, shared with the GraphQL auth integration tests so
the two cannot drift. See [hacking.md](../hacking.md).

## Configuration

`internal/config` resolves a tree of typed structs. A subsystem contributes a `Spec`
(key + default struct) to `config_specs`; resolvers layered by priority fill it from
defaults, a config file, and environment variables; `go-playground/validator` tags on the
struct are enforced at resolution. `godotenv/autoload` in `main.go` means a `.env` file is
read before any of this.

A validation tag is therefore load-bearing, not decorative — see the comments on
`authconfig.Config`, where a missing `gt=0` was a division by zero at startup.

**`internal/config/param` is not that path.** It builds `Param*` values describing a
setting — description, type, default, whether it is dynamic — and nothing collects them:
there is no `init`, no registry, and no resolver that reads one. A `Param*` declaration
therefore configures nothing, and the `Spec` above is the only way a setting reaches the
running server. This matters when reading a subsystem's `config.go`, because the two can
sit side by side: `rbac` carried a `ParamAnonymousAccess` and an `AnonymousAccess` type
next to the live `authconfig.Config.AnonymousAccess`, two identically named settings of
which one did nothing. The dead pair is gone; whether the rest of the machinery should be
wired up or removed is still open.
