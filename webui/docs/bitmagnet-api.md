# bitmagnet's GraphQL API

Transcribed from `../bitmagnet/graphql/schema/*.graphqls` in the fork at `trunk`
`77fdb9de7`, the source of truth for every shape below. Re-read that schema when the fork
is upgraded. This page exists because bitmagnet publishes no API reference of its own.

Read it before writing a query. For how to _express_ a query in Elm, see the `elm` skill;
this page is only about what bitmagnet offers.

Statements marked **[verified]** were checked on 2026-08-24 by read-only query against a
live instance running that commit and holding ~2.9M torrents. Everything else is read off
the same snapshot of the schema. Set
`BITMAGNET_URL` to point the tooling at an instance; the address itself is local
configuration and is not recorded here.

The Elm bindings are generated and committed under `src/Magnes/Api`, so the real
signatures are on disk — read those rather than inferring shapes from the GraphQL below.

## CORS is open, but the allowed headers are now a fixed set

[verified] `Access-Control-Allow-Origin: *` on both the POST and the preflight, so a
browser can query bitmagnet directly with nothing in the path — which is what makes the
serverless UI possible.

Allowed _headers_ are no longer reflected back from the request. Since PR #45 they are
the ones the server actually reads: `Content-Type`, `Authorization` and `X-Api-Key`.
`X-Import-Id` was among them until the legacy import format, its only reader, was removed.
`Authorization` is among them, so bearer tokens work cross-origin; any other header Magnes
invents fails preflight until an operator adds it to `http_server.cors.allowed_headers`.

Serving Magnes from bitmagnet's own origin (`http_server.static`) removes CORS from the
picture entirely. See `docs/serving-and-testing.md`.

## Root

```graphql
type Query {
  version: String!
  workers: WorkersQuery!
  health: HealthQuery!
  queue: QueueQuery!
  torrent: TorrentQuery!
  torrentContent: TorrentContentQuery!
}

type TorrentContentQuery {
  search(input: TorrentContentSearchQueryInput!): TorrentContentSearchResult!
}

type TorrentQuery {
  files(input: TorrentFilesQueryInput!): TorrentFilesQueryResult!
  listSources: TorrentListSourcesResult!
  suggestTags(input: SuggestTagsQueryInput): TorrentSuggestTagsResult!
  metrics(input: TorrentMetricsQueryInput!): TorrentMetricsQueryResult!
}
```

Queries are **nested one level** — `torrentContent` then `search` — so an Elm query is two
selection sets, not one. Note `input` is non-null: it is a required record argument, _not_
elm-graphql's optionals-updater. The optional-ness lives in the input object's fields.

## Search

```graphql
input TorrentContentSearchQueryInput {
  queryString: String
  limit: Int
  page: Int
  offset: Int
  totalCount: Boolean
  hasNextPage: Boolean
  infoHashes: [Hash20!]
  facets: TorrentContentFacetsInput
  orderBy: [TorrentContentOrderByInput!]
  cached: Boolean
  aggregationBudget: Float
}

type TorrentContentSearchResult {
  totalCount: Int!
  totalCountIsEstimate: Boolean!
  hasNextPage: Boolean
  items: [TorrentContent!]!
  aggregations: TorrentContentAggregations!
}
```

### Pagination is offset-based, not cursor-based

`limit` + `offset` (or `page`). There are no cursors anywhere in this schema. Model the
load-more state on an offset.

**`hasNextPage` and `totalCount` are opt-in request flags, and omitting them fails
silently and plausibly.** [verified] They do not come back null — they come back with
values that look real:

| Request                                           | `totalCount` | `totalCountIsEstimate` | `hasNextPage` |
| ------------------------------------------------- | ------------ | ---------------------- | ------------- |
| `{limit: 2}`                                      | `0`          | `false`                | `false`       |
| `{limit: 2, totalCount: true, hasNextPage: true}` | `2870414`    | `true`                 | `true`        |

So forgetting `hasNextPage: true` doesn't scroll forever — it makes the list report
`hasNextPage: false` and **stop dead after the first batch**, which reads as "that's all
the results" rather than as a bug. Forgetting `totalCount: true` displays "0 results"
above a list of results. Set both flags on every search.

**`totalCountIsEstimate` varies per query — read it, don't assume it.** [verified] The
whole-index count came back `2870414` with `totalCountIsEstimate: true`; a narrower query
(`"linux"`) came back `2715` with `false`. So the UI needs both renderings — "~2.9M" and
"2,715" — chosen from the flag, rather than hedging every figure.

**Offset pagination over a live crawler will double-count.** bitmagnet ingests
continuously from the DHT, so under a recency ordering new rows shift everything down
while the user scrolls: offset 50 after an insert re-returns a row already shown.
**Dedupe by `TorrentContent.id` when appending a batch.** (Reasoned from the schema and
how bitmagnet works, not observed — but it's inherent to offset paging over a moving
table, not a rare race.)

`totalCountIsEstimate: Boolean!` — don't render an estimate as an exact figure.
`aggregationBudget: Float` caps facet computation, and every `*Agg` carries its own
`isEstimate: Boolean!`.

### Fetching one torrent

There is **no get-torrent-by-hash query**. For the `/torrent/<hash>` route, call `search`
with `infoHashes: [hash]` and take the single item.

### Ordering

```graphql
enum TorrentContentOrderByField {
  relevance | published_at | updated_at | size | files_count
  seeders | leechers | name | info_hash
}
```

`orderBy` is a _list_ of `{ field, descending }`, so sorts compose.

### Facets

`contentType`, `torrentSource`, `torrentTag`, `torrentFileType`, `language`, `genre`,
`releaseYear`, `videoResolution`, `videoSource`. Each takes `aggregate: Boolean` (return
counts) and `filter: [...]`; the multi-valued ones also take `logic: FacetLogic` (`and`/`or`).

These are the filter UI and map directly onto URL query parameters. `aggregate` and
`filter` are independent — you can filter without paying for aggregation.

Each aggregation bucket is `{ value, label, count, isEstimate }`. `contentType` includes a
`value: null` / `label: "Unknown"` bucket, and [verified] it is usually the biggest one —
497 of 673 on a `"ubuntu"` search.

**`torrentFileType` aggregation counts are wrong — don't display them.** [verified] On the
same `"ubuntu"` search (`totalCount: 673`) every file-type bucket returned ~1918, and the
values drifted between two identical calls. They are not scoped to the query. The
_filter_ side works correctly (`filter: [video]` returns sensible rows), so file type is
usable as a filter with no count beside it.

**All nine facets have since been checked** [verified] by comparing each aggregation's
bucket sum against the query's own `totalCount`. `torrentFileType` is the only broken one
— 18,832 across its buckets on a query totalling 4,703. The other eight are query-scoped
and sane, but most have nearly no data on a DHT index: on that same 4,703-row search,
`genre` and `torrentTag` returned no buckets at all, and `language`, `videoResolution` and
`videoSource` returned buckets totalling 4, 2 and 6 rows.

**`contentType` can be filtered to null.** [verified] `ContentTypeFacetInput.filter` is
`[ContentType]` with nullable elements, and `filter: [null]` returns exactly the
unclassified rows — 4,210 of 4,703. Since unclassified is the largest bucket on any real
index, this is the most useful value in the facet.

## Row model

Search returns **`TorrentContent`**, not `Torrent`. The torrent is nested inside it.

```graphql
type TorrentContent {
  id: ID!
  infoHash: Hash20!
  torrent: Torrent!
  contentType: ContentType
  content: Content
  title: String!
  languages: [LanguageInfo!]
  episodes: Episodes
  videoResolution: VideoResolution
  videoSource: VideoSource
  videoCodec: VideoCodec
  releaseGroup: String
  seeders: Int
  leechers: Int
  publishedAt: DateTime!
  createdAt: DateTime!
  updatedAt: DateTime!
}
```

**Key rows on `id`, not `infoHash`.** [verified] `id` is a composite string:

```
0827c2eb860c40d2789e4bff01b153f24078552c:?:?:?
<infoHash>:<contentSource>:<contentType>:<contentId>     -- ? where unset
```

So the same torrent matched to two content entries yields two rows sharing an `infoHash`.
A 100-row sample showed no such collision (unclassified torrents dominate, see below), but
`id` is the actual row identity and costs nothing to use.

`title` is the parsed, cleaned display title; `torrent.name` is the raw torrent name. Show
`title`.

**Most torrents are unclassified.** [verified] In a 100-row sample of a `"linux"` search,
`contentType` was null for 88. Content metadata (`content`, `episodes`, `videoResolution`,
genre/language facets) is a bonus on a minority of rows, not the backbone of the UI —
design the row to look right with nothing but title, size, and dates.

```graphql
type Torrent {
  infoHash: Hash20!
  name: String!
  size: Int!
  hasFilesInfo: Boolean!
  singleFile: Boolean
  filesStatus: FilesStatus!
  filesCount: Int
  fileType: FileType
  files: [TorrentFile!]
  sources: [TorrentSourceInfo!]!
  seeders: Int
  leechers: Int
  tagNames: [String!]!
  magnetUri: String!
  createdAt: DateTime!
  updatedAt: DateTime!
}
```

`size` is bytes as `Int!`, which **routinely exceeds the 32-bit range the GraphQL spec
defines for `Int`**. [verified] In a 100-row sample, 27 exceeded 2,147,483,647; the largest
was ~31.9 GB. Elm's `Int` is a JS number and handles this fine to 2^53, but any JS on the
proxy side must not assume int32, and no client should trust the declared type here.

`magnetUri` is the actual action target for a row.

## File lists

`Torrent.files` exists inline, but selecting it per row in a search is expensive. Use the
separate paginated query when a row is expanded:

```graphql
input TorrentFilesQueryInput {
  limit: Int
  page: Int
  offset: Int
  totalCount: Boolean
  hasNextPage: Boolean
  infoHashes: [Hash20!]
  orderBy: [TorrentFilesOrderByInput!]
  cached: Boolean
}

type TorrentFilesQueryResult {
  totalCount: Int!
  hasNextPage: Boolean
  items: [TorrentFile!]!
}
```

It is paginated, so the cap on expanded file lists is a `limit`, not something to enforce
client-side.

**`filesStatus` is a real UI state, not a detail:**

```graphql
enum FilesStatus { no_info | single | multi | over_threshold }
```

- `no_info` — bitmagnet has no file list. Nothing to expand to.
- `single` — one file; the file list adds nothing over the row itself.
- `over_threshold` — bitmagnet _declined_ to index the files, too many.
- `multi` — the only case with a file list worth fetching.

**This is the common case, not the edge case.** [verified] A 100-row sample:

| `no_info` | `multi` | `single` | `over_threshold` |
| --------- | ------- | -------- | ---------------- |
| 81        | 13      | 5        | 1                |

So the second expansion level is **unavailable for roughly four rows in five**. Decide from
`filesStatus` whether to render the affordance at all — don't offer an expander that
resolves to an empty panel. `no_info` also means `filesCount` is null, so a row can't even
show a file count.

**Never infer the affordance from `filesCount`.** [verified] `over_threshold` rows report a
real count while having no indexed files — 189, 222 and 489 were all observed — and
`torrent.files` for them returns `totalCount: 0, items: []`. Only `filesStatus == multi`
means there is something to fetch.

## Mutations — all destructive or account-scoped

```graphql
type TorrentMutation {
  delete(infoHashes: [Hash20!]!): Void
  putTags(infoHashes: [Hash20!]!, tagNames: [String!]!): Void
  setTags(infoHashes: [Hash20!]!, tagNames: [String!]!): Void
  deleteTags(infoHashes: [Hash20!], tagNames: [String!]): Void
  reprocess(input: TorrentReprocessInput!): Void
}
```

`delete` permanently removes torrents from the index — an anonymous caller who can reach
`Mutation` can empty it. That used to be the argument for putting a Magnes proxy in the
path. It no longer is: the fork enforces an object action on every top-level field, so
this is the server's job and Magnes must not reimplement it.

What Magnes owes the user is an accurate view of what they may do, taken from
`self.identity.permissions` rather than guessed. Enforcement is per **top-level field**,
so the permission to check for a destructive control is `torrent::delete`, and for the
dashboard `torrent::query` plus `queue::query`. Drawing a button the server will refuse is
the failure mode to avoid; hiding one the server would have allowed is the other.

Use the fork's vocabulary from `../bitmagnet/CONTEXT.md` — Identity, User, Object action,
Permission, Anonymous access. Do **not** write "guest", "session", "scope" or "account".
See `docs/auth-api.md`.

## Custom scalars

```graphql
scalar Hash20
scalar Date
scalar DateTime
scalar Duration
scalar Void
scalar Year
```

All six arrive as `String`-backed opaque wrappers unless mapped in
`Magnes/Api/ScalarCodecs.elm`. Worth mapping: `DateTime`/`Date` to `Time.Posix`, `Year` to
`Int`. `Void` is the mutation return type and can stay opaque.

## Enums worth knowing

`ContentType`: movie, tv_show, music, ebook, comic, audiobook, game, software, xxx.
`FileType`: archive, audio, data, document, image, software, subtitles, video.
`VideoResolution` values are prefixed (`V1080p`) since GraphQL enums can't start with a
digit — needs a display mapping, don't show the raw constructor.
`Language` is a 60-odd value ISO-639-1 enum.
