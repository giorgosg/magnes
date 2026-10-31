# Architecture

How bitmagnet is put together. Read this page first; it is the map, and each page
below is one territory on it. **Snapshot:** written against `trunk` at `9dc8862`
(2026-08-22).

## Which page, and when

| Read                             | When                                                                               |
| -------------------------------- | ---------------------------------------------------------------------------------- |
| [runtime.md](runtime.md)         | Working out what starts what, how `fx` assembles the binary, or why shutdown hangs |
| [ingest.md](ingest.md)           | Touching the DHT crawler, the metainfo protocol, or anything that writes torrents  |
| [processing.md](processing.md)   | Touching the queue, the processor, or the classifier                               |
| [data.md](data.md)               | Writing a query, adding a migration, or explaining a slow search                   |
| [interfaces.md](interfaces.md)   | Adding or changing anything externally reachable: GraphQL, Torznab, HTTP           |
| [auth.md](auth.md)               | Resolving an identity, adding a permission, or reviewing an auth change            |
| [moderation.md](moderation.md)   | Working on exact harmful-content decisions and private review evidence             |
| [conventions.md](conventions.md) | Adding a subsystem, a GraphQL field, or a new externally reachable surface         |

The rules for _changing_ code live in [AGENTS.md](../../AGENTS.md) and [docs/rules/](../rules/). These pages describe
what is there; that one describes what you must do about it.

## The shape in one paragraph

A single Go binary (`main.go`) backed by PostgreSQL. It crawls the BitTorrent DHT for
infohashes, fetches each torrent's metainfo from peers, writes it to the database, and
enqueues a classification job. A Postgres-backed job queue drives a processor that runs a
rule engine over each torrent to work out what it _is_ — film, episode, music — and
writes a searchable `torrent_contents` row. Two APIs read that back out: a
GraphQL API and a Torznab endpoint for \*arr clients. Release builds embed the Elm UI from `webui/` and serve it on the API origin. Everything is wired with [`uber-go/fx`](https://github.com/uber-go/fx) and
run as a set of registered workers.

```
        DHT network                                       PostgreSQL
             │                                                 ▲
             ▼                                                 │
   ┌──────────────────┐   infohashes    ┌───────────────┐      │
   │   dhtcrawler     │────────────────▶│    triage     │──────┤ "seen this?"
   │  (sample, ping,  │                 └───────┬───────┘      │
   │   find_node)     │                         │ new          │
   └──────────────────┘                         ▼              │
                                     ┌────────────────────┐    │
             peers ◀─────────────────│ get_peers / scrape │    │
                                     └──────────┬─────────┘    │
                                                ▼              │
                                     ┌────────────────────┐    │
                                     │  metainfo request  │────┤ torrents,
                                     │  (BEP 9 over TCP)  │    │ torrent_files
                                     └──────────┬─────────┘    │
                                                ▼              │
                                     ┌────────────────────┐    │
                                     │  queue_jobs row    │────┘
                                     └──────────┬─────────┘
                                                ▼
   ┌──────────────────┐            ┌────────────────────────┐
   │  queue/server    │───────────▶│ processor → classifier │──▶ torrent_contents
   │ LISTEN + poll    │            │  (CEL rules, TMDB)     │
   └──────────────────┘            └────────────────────────┘
                                                │
                                                ▼
                              GraphQL  ·  Torznab
```

## Where the code lives

| Path                    | Role                                                                           |
| ----------------------- | ------------------------------------------------------------------------------ |
| `main.go`               | Four lines: build the fx app and run it                                        |
| `internal/app/`         | fx assembly (`appfx`), CLI commands (`cmd/`), urfave/cli glue                  |
| `internal/dhtcrawler/`  | The ingest pipeline — see [ingest.md](ingest.md)                               |
| `internal/protocol/`    | DHT protocol, k-table, metainfo/BEP-9 client                                   |
| `internal/queue/`       | Postgres job queue: manager, server, handler                                   |
| `internal/processor/`   | Turns a torrent into a `torrent_contents` row                                  |
| `internal/enhancement/` | Bounded LLM metadata enhancement workflow — see [processing.md](processing.md) |
| `internal/classifier/`  | CEL + `.ryml` rule engine that decides what a torrent is                       |
| `internal/database/`    | GORM setup, generated dao/query, search criteria, full-text search             |
| `internal/model/`       | GORM models and the enum/null types the whole tree passes around               |
| `internal/gql/`         | gqlgen GraphQL server (`gql.gen.go` is ~24k generated lines)                   |
| `internal/torznab/`     | Torznab/Newznab indexer API — the \*arr surface                                |
| `internal/httpserver/`  | gin engine, middleware, option registration                                    |
| `internal/auth/`        | Identity, RBAC, API keys, JWT, users — see [auth.md](auth.md)                  |
| `internal/importer/`    | Exchange-file import, behind the CLI `import` and `POST /import`               |
| `internal/exchange/`    | Exchange-file reader and writer — see [exchange-file.md](../exchange-file.md)  |
| `internal/moderation/`  | Exact decisions, keyword quarantine and catalogue visibility                   |
| `internal/health/`      | Health checks reported over GraphQL                                            |
| `internal/worker/`      | The registry every long-running thing registers with                           |
| `internal/concurrency/` | Batching channels, buffered concurrent channels, keyed limiters                |
| `internal/lazy/`        | `lazy.Lazy[T]`, how fx factories defer expensive construction                  |
| `pkg/json_schema/`      | JSON schema types, used by `internal/config/param`                             |
| `migrations/`           | Goose SQL migrations, `00001`–`00030`                                          |
| `graphql/`              | Schema and operations — the _source_ for `internal/gql`                        |

The `static` HTTP component serves the bundled Elm UI by default in release builds,
or an external UI from a configured directory. Backend-only Go builds omit the bundle.
