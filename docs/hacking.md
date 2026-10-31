# Hacking on bitmagnet

Orientation for a first change. [rules/](rules/) has the rules — the generated
files, the pinned Prettier, the migration numbers, the red test — and this page has the
detail behind them. [architecture/](architecture/README.md) covers _what_ the pieces are;
this covers _how to work on them_.

## Toolchain

`flake.nix` provides a dev shell — Go, go-task, Node 22, golangci-lint and protoc.
Prettier is deliberately not in it;
the root package's pinned copy, installed by `task install-tools`, is the one to run. `direnv allow` or `nix develop` gets
you a working environment. Without Nix you need Go, go-task, Node 22, PostgreSQL, and —
only if you touch protobufs — `protoc`.

Two things the shell does **not** supply: PostgreSQL, which most targets want and
`docker-compose.yml` at the root brings up, and a `goose` binary, which only
`task create-migration` needs.

Everything is driven by [`Taskfile.yml`](../Taskfile.yml) (go-task, not `make`). One
target worth knowing:

```bash
task serve-website   # the Astro/Starlight site under website/
```

## Building and testing the UI

The default `task`, or `task build`, installs `webui/package-lock.json` dependencies with
`npm ci`, builds optimized Elm JavaScript, and compiles Go with the `webui` tag. The
executable embeds the HTML, JavaScript, CSS and favicon; no asset directory is needed at
runtime. Docker builds the UI on the build host before cross-compiling Go, and GoReleaser
builds it once before compiling its platform binaries.

The GoReleaser configuration uses version 2 and the current `archives.ids` and
`format_overrides.formats` fields. Validate it with `goreleaser check` before changing
release packaging; the release workflow installs GoReleaser's latest version.

`task build-backend` and plain `go build ./...` omit the bundle. This keeps generators,
backend development and database tests independent of Elm. Building Go directly with
`-tags webui` requires `task build-ui` first; a missing bundle fails compilation.
Generated bundles and runtime configuration stay ignored.

```bash
task install-tools       # root Prettier and UI tools
task build               # one executable, including the optimized UI
task test-ui             # Elm tests
task lint-ui             # Elm formatting and static review
task test-bundled-ui     # build the UI and test the bundled HTTP routes/fx wiring
cd webui
BITMAGNET_URL=http://localhost:3333 npm run dev  # HTTPS development proxy
```

`task test` runs the Go, bundled HTTP and Elm tests. `task lint` includes elm-format and
elm-review alongside Go lint and root Prettier 3.3.3. UI source and browser tests live in
`webui/`; its [documentation](../webui/docs/README.md) and
[browser test guide](../webui/e2e/README.md) explain the existing tests.

CI also drives the built executable in Chromium: `/` redirects to `/ui/`, the optimized
Elm app reads the same-origin API, and navigation/reloading a login deep link works. To
run that smoke test after `task build`:

```bash
cd webui
npx playwright install chromium
BUNDLED_UI_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/ui_smoke' npm run test:e2e:bundled
```

Create a disposable database for this command: the executable runs its migrations. The
smoke test starts only the HTTP worker and stops it afterwards; the database remains for
the caller to drop. It does not exercise sign-in over HTTP. Credentialed browser tests
use the HTTPS proxy and an isolated fixture clone instead.

When installing Chromium with `--with-deps` inside a sanitized Nix shell, retain the
host `PATH` (`--keep PATH`): Playwright calls the runner's `sudo` and `apt-get` to install
system libraries. The UI CI job keeps it for that installation step.

The bundled mount defaults to `/ui/` and can be disabled with
`http_server.static.bundled: false`. `http_server.static.dir` overrides it with an
external UI. Cross-origin browser deployments must set
`http_server.cors.allowed_origins` explicitly.

## Magnes public documentation

The new public site is Astro/Starlight under `website/`. Run `task serve-website` for
local development or `task build-website` for type checking, static generation, search
indexing, and local-link validation under the `/magnes/` GitHub Pages prefix. The site
publishes `schemas/classifier-0.1.json` through an Astro endpoint, so
`task gen-classifier-schema` remains the single generator and no second copy is edited. It uses its
own locked npm dependencies; application builds do not install the website toolchain.
See [the website README](../website/README.md) for publishing and editorial scope.

`docs/` remains contributor and architecture documentation. The legacy Jekyll site,
its build tasks and deployment workflow have been removed. Public Magnes guides belong
in `website/`.
The Magnes Pages workflow checks documentation pull requests and automatically publishes
changes to the website, shared schemas, or Markdown documentation merged into `trunk`.
Repository Pages uses GitHub Actions as its source.

## CI cost controls

The checks workflows cancel an older run when a newer commit arrives on the same ref.
GitHub pull requests are checked as synthetic merge commits, so the push produced by the
GitHub merge button skips the duplicate checks; a direct push still runs them.

Container builds are deliberately separate. They run when a Docker build input changes
and once a week to catch image or registry drift, rather than spending roughly ten runner
minutes on every source-only pull request. CodeQL scans Go on pull requests
and weekly.

## Adding a subsystem

The codebase is uniformly wired with `uber-go/fx`, and following the existing shape is
much easier than fighting it. [architecture/conventions.md](architecture/conventions.md#a-subsystem) gives the four files
and the one registration line; the parts that are easy to get wrong:

- **Config is picked up automatically.** The struct in `config.go` is registered with
  `configfx` by the module; defaults belong in its constructor, not in a separate
  defaults file.
- **An HTTP surface is its own package.** Add `<feature>/httpserver/` that registers a
  handler, following `internal/torznab/httpserver/`.
- **Copy the closest existing module rather than the smallest.** `internal/tmdb` for an
  external API client with rate limiting and a lazy requester; `internal/torznab` for an
  HTTP-facing subsystem; `internal/worker` for something long-running.

`internal/prowlarr/`, `internal/rssfeed/`, `internal/omdb/` and `internal/tpdb/` in the
[o51r15 fork](forks/o51r15.md) are textbook applications of this shape — worth reading
before writing your own, whatever you think of the features.

## Changing the GraphQL API

After editing the schema, running `task gen-gql` and implementing the resolver,
regenerate any external client that consumes the changed fields.
The schema is the contract, and nothing outside this repo learns that it moved.

## Testing at realistic scale

A dev database with a few thousand torrents behaves nothing like a real one. Query plans
in particular flip over completely: on a real instance `torrent_contents` is tens of
millions of rows with ~15 GB of indexes, and roughly half of those indexes are never
scanned at all. A change that looks like a win on synthetic data can be measuring an
entirely different plan.

If you can point at a populated instance, `EXPLAIN` — not `EXPLAIN ANALYZE` — is free and
tells you far more than a synthetic dataset. `task export-data` produces a portable
data-only column-INSERT dump of the ordinary visible catalogue for seeding a smaller one.
Pending/banned torrents and metadata backed only by them are excluded. Auth records,
internal key-value state, moderation evidence/history/decisions and ban lists are omitted.

The export uses PostgreSQL connection environment variables (`PGHOST`, `PGPORT`, `PGUSER`,
`PGPASSWORD`, `PGDATABASE`), defaulting the database to `bitmagnet`. It streams from one
read-only repeatable-read snapshot without modifying the source database. `export.sql` is
replaced only after a successful export; a failed export leaves the previous file intact.
Restore it into a migrated database with `psql -v ON_ERROR_STOP=1 -d bitmagnet -f export.sql`.
Explicit columns omit generated columns; `ON CONFLICT DO NOTHING` permits repeated restores.
This SQL dump is separate from the exchange file that `POST /import` reads. A full
administrative `pg_dump` still includes private data and is a database backup, not a
catalogue export or shareable ban list.

`bitmagnet export [--include-tags] [--since <time> [--overlap <duration>]] <file|->` writes
the same visible torrents as an [exchange file](exchange-file.md) instead. Use the
catalogue export to copy a catalogue into another database at the **same** version, such as
a test instance. Use the exchange file to share an index with another instance or program,
whatever its version. It carries torrents, files, sources and the current classification,
but not the work's catalogue metadata, which a receiver re-fetches. Unlike
`task export-data`, it reads the instance's own configuration rather than the `PG*`
variables. It is read-only and applies no migrations, and it pages through one snapshot,
so it is safe on a large catalogue. With `--since <time>` it writes only what changed
since then, for keeping a receiver current; it never conveys deletions. See
[Incremental export](exchange-file.md#incremental-export).

Be careful with instances you do not own. bitmagnet is frequently self-hosted on slow
hardware, where a single `count(*)` on `torrents` is genuinely disruptive.

### Integration tests with content in them

`internal/database/dbtest` has two provisioning paths. `dbtest.New(t)` hands each test an
empty, fully migrated database — which is what the auth tests want — and needs only
`TEST_POSTGRES_DSN`, which CI sets. `dbtest.NewSeeded(t)` instead clones a seed template
built by the local **btm-testdb** project, and the clone arrives with the fixture corpus —
roughly 100k torrent contents — in about a second, at file-copy cost regardless of corpus
size.

The seeded path is local-only by design and skips unless `TEST_POSTGRES_TEMPLATE_DSN`
names the btm-testdb instance (its `bin/testdb url` prints the value), so CI and bare
checkouts never touch it. The template's name carries the migration version it was built
at: a template stale against the migrations in the tree is refused rather than used, and
an instance with no template on it skips rather than fails. Treat the clone's content as
read-only — one immutable template is what keeps the clone cost flat.

**Adding a migration obsoletes the template**, and the seeded tests then fail rather than
skip — deliberately, since a clone of the older schema would be missing the columns the new
code writes to. Migrate btm-testdb's corpus database and run `bin/testdb seed` to build the
template at the new version.

Postgres refuses to clone a template while anything is connected to it. btm-testdb seals
its templates against connections once built, so when `dbtest` reports that error it
usually means a `bin/testdb seed` rebuild is running right now; wait for it and retry.

### Driving a real instance from outside: `dev fixture serve`

A browser suite cannot test anything past a sign-in against a test binary. `internal/dev`
therefore has a command that serves a real bitmagnet, over a clone of the same seed
template, and prints what a harness needs to drive it:

```bash
TEST_POSTGRES_TEMPLATE_DSN=$(cd ../btm-testdb && bin/testdb url) \
  go run ./internal/dev fixture serve --anonymous-access=false
```

It writes **one line of JSON to stdout** and nothing else — address, GraphQL endpoint,
bootstrap invitation code, database name, and the three settings a harness branches on
(`anonymousAccess`, `invitationRequired`, `seededDashboardData`) — so the harness parses it
rather than scraping logs. Everything else goes to stderr to keep that line alone on
stdout: gin is put in release mode and pointed there, and the dev binary's logger writes
there too. The default address is `127.0.0.1:0`, so
parallel runs do not collide and the assigned port comes back in the announcement.

The invitation is minted fresh for each run, which is the point: the harness registers its
own throwaway administrator instead of a password living somewhere. `--invitation-required`,
`--anonymous-access`, `--jwt-duration`, `--login-requests-per-minute` and
`--login-request-burst` vary the workflows; setting the last two to `1` makes the second
login attempt throttle, which is otherwise not reachable inside a test's patience.

`--anonymous-access=true` grants the fixture's `anon` role the catalogue read surface;
production starts with no anonymous grants. Pass `false` to exercise the closed path.

`--seed-dashboard-data` **defaults to on**, and gives the status, statistics and queue
pages data in the recent window each of them opens on. Two halves, because the seed
template is a months-old snapshot:

- Queue jobs covering every `QueueJobStatus` across two queue names, paired inside hourly
  buckets so the facet counts and the chart's columns are both above one. The template
  carries no `queue_jobs` at all, so without this the jobs table, status facet and totals
  chart have nothing to render.
- A bounded number of `torrents_torrent_sources` rows moved into the last hour, because
  `torrent.metrics` buckets on `updated_at` and the statistics page opens on the last hour
  — so the chart was empty on the only view it shows by default. This half **updates
  existing rows rather than inserting synthetic torrents**, so it changes nothing about what
  the search pages serve, and does nothing at all on an empty database.

Pass `--seed-dashboard-data=false` for a clone that matches the template exactly. The
announcement reports which way it went, so a spec that needs the data can skip rather than
fail obscurely.

What the served instance answers for those pages: `health` reports one real check against
the clone; `workers` lists the keys production registers, with the ones this stack actually
runs marked started and the crawler, queue server and invitation worker listed and stopped;
`queue.jobs`, `queue.metrics`, `torrent.metrics` and `torrent.listSources` all read the
clone. `--seed-moderation-data` defaults to off. The credentialed UI harness turns it on
to add harmless synthetic review names, complete/missing paths, a digest-only ban and
more than one search page. It wires the real classifier/processor with external APIs and
local content matching disabled; direct `torrent.reprocess` can then verify classification
and local allow precedence without crawlers or external data. The queue _server_ remains
unwired, so a job enqueued through `queue.purgeJobs` or `enqueueReprocessTorrentsBatch` stays in the
table to be asserted on rather than being picked up and run.

The moderation browser spec runs with the queue-changing project after readers finish,
because allow adds normal classification jobs.

The clone is dropped when the command exits, including on `SIGINT`/`SIGTERM`. That drop
runs from an fx `OnStop` hook rather than a `defer`, because `fx.App.Run` returns on the
signal and the process would otherwise exit while the cleanup was still running — which
leaves a stray database on the fixture instance. If you ever do find one, they are named
`bitmagnet_test_*` and are safe to drop.

The engine it serves is assembled by `internal/dev/fixtureserver`, which the GraphQL auth
integration tests use too. That sharing is deliberate: a second copy of that wiring would
pass its own tests while drifting from the one under test.

## Where the interesting problems are

The fork survey in [forks/](forks/README.md) is a map of what the community found worth
fixing. The recurring themes, arrived at independently by several people:

- **DHT crawl throughput** — rate limiting, bootstrap node health, bloom filter
  behaviour, concurrency caps on in-flight UDP
- **Queue contention** — `queue_jobs` bloat, garbage collection, polling vs LISTEN/NOTIFY
- **Over-indexing** — the default index set is expensive and largely unused for
  self-hosted workloads
- **Search and classification quality** — full-text lexemes, torznab category mapping,
  fuzzy matching cost
- **Authentication** — there is none; see [auth.md](auth.md)

[integration-status.md](integration-status.md) records which of these already reached
`trunk` and which were tried and rejected. Read it before starting: several plausible
patches are there with the reason they were turned down.

To send work upstream instead, see [upstream-status.md](upstream-status.md).

### Ban-file import regression checks

The moderation sharing tests use harmless synthetic metadata and isolated `dbtest.New`
instances. The two-instance flow exercises the real offline classifier and importer through
quarantine, allow/classification, ban, export and upload, then verifies ordinary exclusion,
local allow precedence, future ingestion suppression and private evidence after deletion.
`TestBanImportReverseLookupUsesIndexAtFixtureScale` uses `dbtest.NewSeeded` and therefore
needs a separate sealed fixture template at migration 00027. Build a new template clone;
do not migrate the prior sealed template or the source fixture database in place. The
00027 index build is a one-time full-index operation; file imports perform bounded indexed
lookups. See [the operator import contract](moderation-ban-file.md) and
[upgrade cost](architecture/data.md).

Automatic enhancement tests exercise PostgreSQL admission locks, leases and per-request
spending with fake providers. `TestSchedulerSelectionUsesRankIndexAtFixtureScale` uses
a seed template at the tree's high-water mark and checks both group query plans. It
landed with `btm_seed_29`; since migration `00030` the seeded tests need `btm_seed_30`, a
nullable-column migration of a clone of `btm_seed_29`. Rebuild from a disposable clone of
the prior template; preserve source and sealed fixtures.
Scheduling columns on `torrents` are intentionally excluded by `internal/database/gen`
so generated ingestion upserts cannot overwrite SQL-maintained readiness/ranking.

Operator CLI tests in `internal/app/cmd/enhancementcmd` run the real app wiring with a
write-denying PostgreSQL connection for evaluate/preview/status, including automatic
configuration enabled. Explicit processing tests cover shared request accounting,
unchanged suppression, force, retained completion after authentication failure, durable
pause/resume and daily denial. `internal/enhancement` tests cover cross-mode capacity
and recovery; the processor series scenario follows admitted processing through work
attachment, reprocessing and explicit rematch. Paid model evaluations are separate
operator activities, excluded from normal CI. See [the operator guide](enhancement.md)
for corpus review, cost reporting and rollout/recovery requirements.
