# Data — models, queries, search, migrations

PostgreSQL only. There is no other supported store and no abstraction pretending
otherwise. `internal/database` is layered, and knowing which layer you are in decides
whether your change is safe.

## The layers

```
  internal/database/dao/     generated GORM Gen DAOs        ── `task gen-gorm`
  internal/database/query/   generic query builder: criteria, facets, options, hydrators
  internal/database/search/  bitmagnet's concrete criteria, facets, orderings
  internal/database/fts/     Postgres tsvector/tsquery construction and parsing
  internal/database/exclause/ CTE, UNION, INTERSECT, EXCEPT clauses GORM lacks
  internal/model/            the structs; `*.gen.go` are generated, the rest hand-written
```

`internal/model` is the layer everything else depends on. Note the split: `*.gen.go`
files are produced from the schema, and the hand-written neighbours (`null.go`,
`episodes.go`, `language.go`, `date.go`, the `*_enum.go` files) carry the custom
`Scan`/`Value`/GraphQL marshalling. `null.go` is 550 lines of nullable scalar types used
in every signature in the tree.

## Tables

| Table                                                                                  | Holds                                                                                                |
| -------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| `torrents`                                                                             | infohash, name, size, private, files_status, files_count                                             |
| `torrent_files`                                                                        | one row per stored file, capped by `SaveFilesThreshold`                                              |
| `torrent_pieces`                                                                       | piece hashes, only when `SavePieces` is on                                                           |
| `torrent_sources`                                                                      | the catalogue of sources: `dht`, legacy importer names, `<origin>:<source>` from imports             |
| `torrents_torrent_sources`                                                             | per-source seeders, leechers, scraped_at (when counts were observed), published_at, legacy import_id |
| `torrent_hints`                                                                        | externally supplied classification hints from importers                                              |
| `torrent_tags`                                                                         | free-form tags                                                                                       |
| `torrent_contents`                                                                     | the search table: effective classification, inferred title/year fallbacks and `tsv` full-text vector |
| `content`                                                                              | metadata records (TMDB etc.), with their own `tsv`                                                   |
| `content_collections`                                                                  | genres, collections, and the join to `content`                                                       |
| `queue_jobs`                                                                           | the job queue — see [processing.md](processing.md)                                                   |
| `moderation_decisions`, `moderation_evidence`, `moderation_history`                    | Exact decisions and independent private review metadata — see [moderation.md](moderation.md)         |
| `bloom_filters`                                                                        | retired legacy filter storage; `blocked_torrents` discarded by migration `00026`                     |
| `users`, `roles`, `role_permissions`, `api_keys`, `api_key_permissions`, `invitations` | auth — see [auth.md](auth.md)                                                                        |

`torrent_contents.id` is _derived from the content identity_, not a surrogate key
(`InferID`). Re-classifying a torrent into a different match therefore produces a
different row, and the processor deletes the old one.

`classification_evaluations` is keyed by `(info_hash, stage)` and references the torrent
with cascading deletion, independently of replaceable search rows. Ordinary completion
stores its pre-enhancement snapshot; enhancement completion, attempt generation/outcome
and retained accepted output are separate state. Accepted output separately retains effective
catalogue authority, including identities attached by enhancement and explicit detachments;
this authority never enters the ordinary snapshot or evidence. It also retains the supplied
hint identity fields present when that authority was established, distinguishing a new
importer decision from an unchanged hint previously overridden by explicit rematch. Immutable sanitized recipes live under
`classifier-recipe/<sha256>` in `key_values`. Neither internal table is included in
ordinary catalogue exports. See [ADR 0009](../adr/0009-keep-classification-evaluations-independent-of-search-rows.md).

## Search

A search is assembled as `query.Option`s — criteria, facets, orderings, hydrators — and
run through the generic query in `query/query.go`. Three properties are worth knowing:

- **Facets** (`query/facets.go`, `search/facet_*.go`) run as additional aggregate queries
  in the same request. Language and content-type values use one grouped count when its
  planned cost fits the combined per-value aggregation budget; otherwise they use the
  estimate-capable per-value path. Other facets still count per value. All facet counts
  share an eight-slot limit so a search cannot take the whole database pool.
- **Full text** goes through `fts.AppQueryToTsquery`, which lexes the user's search string
  into a Postgres `tsquery` (`&`, `|`, `<->`, `!`, `:*`) and binds it as a **parameter**
  — `tsv @@ ?::tsquery`. Ranking uses `ts_rank_cd`.
- **Counting is budgeted.** `dao.BudgetedCount` asks Postgres to `EXPLAIN` the query
  first and, if the estimated cost exceeds a budget, returns the planner's row estimate
  rather than an exact count. This is what stops a broad search from sequentially scanning
  a 33-million-row table just to render "about N results". The plan and the exact count
  are separate parameterized queries, so each value stays bound through both operations.

The existence check and limited CTE item strategy also pass GORM subqueries with bound
parameters. Search no longer renders user values into SQL text. Grouping criteria extract immutable
conditions from cloned GORM statements, so parallel items/counts/facets do not consume
scopes on a shared statement. The historical
`budgeted_count(text, double precision)` function in `migrations/00010_budgeted_count.sql`
is unused by the application; it remains in existing databases for compatibility with
older instances during an upgrade. `search/injection_integration_test.go` checks search
strings, tag criteria and tag facet filters with both counting modes, including with
`standard_conforming_strings` off.

## Catalogue visibility

Concrete catalogue search factories apply the mandatory pending/banned exclusion from
`internal/database/catalogue` before item, count and facet queries. They force no-cache
mode, so caller cache options cannot resurrect pre-quarantine metadata. Dynamic facet
values and source listings also use the policy. Content metadata backed solely by hidden torrents is excluded; unassociated metadata
retains its previous visibility. The classifier receives a separate trusted metadata lookup interface
for content not yet present in the ordinary catalogue. See [moderation.md](moderation.md).

Catalogue SQL exports apply the same policy and retain only visible relationships.
Metadata source definitions include dependencies used only by visible torrent hints;
unassociated definitions are preserved, while sources used solely by hidden rows are excluded.
They are distinct from full administrative database backups.
The exchange-file `export` command reads through the same `VisibleInfoHash` rule.

## Migrations

Goose SQL in [`migrations/`](../../migrations), embedded and run through
`internal/database/migrations` — an fx decorator, so they apply before anything queries.
Current high-water mark is `00030_source_scraped_at.sql`; `ls migrations/` rather than trusting this
sentence.

Forks number theirs independently and the numbers collide;
[rules/migrations.md](../rules/migrations.md) has the renumbering rule and the commands.

## Testing against a real database

`internal/database/dbtest` creates and drops an isolated database per test, by either of
two paths:

```go
db := dbtest.New(t)       // empty, fully migrated
db := dbtest.NewSeeded(t) // cloned from the fixture template, corpus included
```

Both hand back the same `*dbtest.DB` — `db.Gorm`, `db.Query`, `db.Pool`, `db.DSN`,
`db.Name` — and differ only in how the database was provisioned. `New` migrates from
empty, which is what the auth tests want. `NewSeeded` clones a seed template built by the
local btm-testdb project, so the database arrives populated for the cost of a file copy;
the template's name carries the migration version it was built at, and one that does not
match the migrations in this tree is refused rather than handed back.

Each path **skips silently** without its own variable — `TEST_POSTGRES_DSN`, which CI
sets, and `TEST_POSTGRES_TEMPLATE_DSN`, which only a checkout with fixtures has — so a
bare `go test ./...` proves considerably less than it appears to. See
[docs/hacking.md](../hacking.md).

---

_Known defects and improvement ideas referenced above are kept as untracked `docs/issues/*.local.md` notes, which a given checkout may or may not have._

Migration `00027` creates the frozen v1 moderation digest expression index on `torrents`.
It enables exact reverse lookup for private evidence during additive file import, without
scanning the catalogue per imported identifier. Index creation hashes every existing
infohash once, consumes additional index space and holds the normal CREATE INDEX write
lock. Schedule the upgrade with catalogue writers stopped and budget time/disk space for
that one-time build; subsequent imports use indexed point lookups. The expression uses an
immutable bytea context literal, checked against ADR 0006's independent vectors. It changes
no table/model fields and requires no DAO generation.

Migration `00028` adds the independent evaluation table and inferred title/year columns
on `torrent_contents`. It backfills no evaluation records and creates no catalogue works. A partial index
on non-null inferred years supports the fallback facet values without a full release
scan on each request. Building that initially empty index still reads the existing
`torrent_contents` table once during migration; budget upgrade time with catalogue writers stopped.
Release Year facets use catalogue years first and inferred years second; full-text
vectors and display titles follow the same preference. Torznab uses the effective title
and year while magnet names retain the original release name.

Migration `00029` adds SQL-only readiness/ranking hints to `torrents`, three partial
indexes and source/insert triggers. It scans existing torrent/source/search rows to
initialize ranking and legacy eligibility, without fabricating ordinary evaluations
or populating a queue. This is an upgrade cost: stop catalogue writers and budget
space/time for three index builds and torrent updates. Source changes subsequently
recompute only that infohash's maximum. GORM generation excludes the operational
columns, preventing generic upserts from resetting them.

`enhancement_claims` contains active leases only. `enhancement_daily_spending` stores
UTC-day totals; `enhancement_reservations` stores expiring live per-request reservation tokens
so only known usage can refund a charge once. Unknown charges survive crashes.
Authentication pause and the next admission group use existing `key_values` entries.
Attempt fingerprints on `classification_evaluations` suppress exhausted failures
without replacing completed/accepted state. These operational tables are excluded
from catalogue exports. They are not a full evaluation history.

Migration `00030` adds a nullable `scraped_at` to `torrents_torrent_sources`: when the row's
peer counts were observed. It is a catalogue-only change with no backfill, so existing counts
stay undated until they are scraped again, and the upgrade costs no table rewrite.
