# Conventions

The patterns to copy when adding code. The rules for changing it live under
[docs/rules/](../rules/); this page explains the shapes behind them.

## A subsystem

```
internal/<feature>/
    config.go               # config struct + NewDefaultConfig, registered via configfx
    factory.go              # the fx.Provide constructors
    <feature>.go            # the implementation
    <feature>fx/module.go   # fx.Module("<feature>", ...) — the public entry point
```

Plus **one line** in [`internal/app/appfx/module.go`](../../internal/app/appfx/module.go).

Three worked examples, each a different shape:

- `internal/tmdb` — an external API client.
- `internal/torznab` — an HTTP surface, including a nested `httpserver/` package that
  contributes an `httpserver.Option`.
- `internal/worker` + `internal/dhtcrawler` — a long-running worker.
- `internal/auth/authfx` — a module made of several sub-packages, using value groups so
  other modules contribute to it.

The consequence worth carrying into a review: **fork features cherry-pick far more
cleanly than their raw diff sizes suggest**, because a feature is usually a new directory
plus a registration line, not edits threaded through existing code.

## Idioms you will meet constantly

- `lazy.Lazy[T]` — defer construction to `OnStart`, so CLI commands do not open databases.
- fx value groups — the wiring is by tag, not by reference. Grep for the tag string, not
  for a caller.
- `internal/concurrency` — `BatchingChannel` (accumulate then emit a slice),
  `BufferedConcurrentChannel` (buffer + semaphore + goroutine per item), keyed limiters.
- `internal/model` null and enum types — `NullUint`, `NullString`, `NullContentType`, and
  the generated `*_enum.go` files. Every signature in the tree uses them.
- `internal/slice` and `internal/maps` — `Map`, `FlatMap`, and ordered map helpers, used
  in place of hand-rolled loops.
- Errors are wrapped with sentinel chains (`%w: %w: %w`) so a caller can ask both "which
  subsystem" and "which failure" — see `auth/api_key/errors.go` and how
  `identity/authenticator_api_key.go` interrogates them.

## A GraphQL change

Edit `graphql/schema/*.graphqls`, run `task gen-gql`, and implement the resolver in
`internal/gql/resolvers/`. External clients regenerate from the schema in their own
projects — see [hacking.md](../hacking.md#changing-the-graphql-api).

## A new surface is open by default

Upstream `main` has no authentication and allows `*` CORS, so treat any new HTTP, GraphQL
or Torznab surface as reachable by anyone unless the deployment supplies its own boundary.
See [auth.md](auth.md) and the operator page [docs/auth.md](../auth.md).

## The rules

Generated files, the pinned Prettier, migration numbers, the red-test bar and the linter
each have one page under [docs/rules/](../rules/), linked from the root
[AGENTS.md](../../AGENTS.md#read-when).
