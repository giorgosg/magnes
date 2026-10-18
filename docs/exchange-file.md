# Exchange file version 1.0

An exchange file is a versioned, self-describing stream of torrent records. Other
instances and other programs can read it, whatever version produced it. It is for sharing
an index. The catalogue export is a different thing: it restores only into the same
version of the software, and it is for backup and migration.

This page is the contract. The reader and writer are `internal/exchange`, and the
[frozen fixture](../internal/exchange/testdata/exchange-v1.jsonl) is the compatibility
input for every reader and writer of version 1. Today the format, its reader and writer,
the `check` command and the full `export` exist. Incremental export and import are
specified below and are not built yet.

## Stream

- The file is UTF-8 NDJSON: one JSON object per line, with no array wrapper. Compression
  is left to the pipe, for example `zstd`, which keeps chunking and resumption working.
- Every line is either a **header** or a **record**. A header is the only line with a
  top-level `format` key.
- A header applies to every record after it, until the next header. Concatenating files is
  therefore always valid, and each chunk keeps its own provenance.
- A record before the first header is an error.

## Header

```json
{
  "format": {
    "name": "torrent-exchange",
    "version": { "major": 1, "minor": 0 }
  },
  "producer": { "program": "magnes", "version": "0.14.0" },
  "exportedAt": "2026-10-18T12:00:00Z",
  "since": "2026-10-11T00:00:00Z"
}
```

- `format.name` is program-independent. A reader refuses any other name.
- `format.version` is an integer major and minor, separate from the producer's version.
  A reader refuses an unknown **major** version. It accepts an unknown **minor** version
  and ignores what it does not recognise.
- Version changes:
  - A **minor** bump adds something an older reader can ignore.
  - A **major** bump removes, renames or redefines a field, or makes an optional field
    required.
  - There is no patch level.
- `producer` identifies the writing program. It does not affect how a file is read.
- `since` is present only on an incremental export. It means: "torrents whose exchanged
  data changed at or after this time; deletions are not conveyed".

## Record

```json
{
  "infoHash": "c9e15763f722f23e98a29decdfae341b98d53056",
  "name": "Example.Release.2021.1080p.WEB-DL.x264-GRP",
  "size": 4831838208,
  "private": false,
  "publishedAt": "2026-09-02T17:14:09Z",
  "files": [
    {
      "index": 0,
      "path": "Example.Release.2021.1080p.WEB-DL.x264-GRP.mkv",
      "size": 4831830016
    },
    {
      "index": 1,
      "path": "Example.Release.2021.1080p.WEB-DL.x264-GRP.nfo",
      "size": 8192
    }
  ],
  "sources": [
    {
      "source": "dht",
      "seeders": 41,
      "leechers": 7,
      "publishedAt": "2026-09-02T17:14:09Z",
      "scrapedAt": "2026-10-17T03:12:44Z"
    },
    { "source": "alice:rarbg", "publishedAt": "2023-05-30T00:00:00Z" }
  ],
  "magnes": {
    "classification": {
      "contentType": "movie",
      "work": { "source": "tmdb", "id": "123456" },
      "inferredTitle": "Example Release",
      "inferredYear": 2021,
      "languages": ["en"],
      "video": { "resolution": "1080p", "source": "web-dl", "codec": "x264" },
      "releaseGroup": "GRP"
    },
    "tags": ["4k-candidate"]
  }
}
```

Records are shown pretty-printed here. In a file, each one is a single line.

### Neutral core

Any program can write and read these fields.

| Field                            | Type                           | Meaning                                                                                                   |
| -------------------------------- | ------------------------------ | --------------------------------------------------------------------------------------------------------- |
| `infoHash`                       | 40 lowercase hex, **required** | v1 infohash, or the 20-byte truncated v2 hash the DHT uses                                                |
| `infoHashV2`                     | —                              | **Reserved.** Full 32-byte v2 hash; unpopulated in 1.0                                                    |
| `name`                           | string, **required**           | The torrent's name                                                                                        |
| `size`                           | integer bytes                  | Total size                                                                                                |
| `private`                        | boolean                        | Private-torrent flag                                                                                      |
| `publishedAt`                    | RFC 3339 UTC                   | Earliest publication the producer knows of                                                                |
| `files`                          | array                          | Absent means unknown. Present means complete, unless `filesComplete` is `false`                           |
| `filesComplete`                  | boolean                        | `false` means the list is partial (the producer's threshold). Absent means `true` when `files` is present |
| `files[].index`                  | integer                        | Position in the torrent's file list                                                                       |
| `files[].path`                   | string                         | `/`-separated path within the torrent. There is no `extension` field, because it is derived from the path |
| `files[].size`                   | integer bytes                  | File size                                                                                                 |
| `sources[]`                      | array                          | One entry per source that observed the torrent                                                            |
| `sources[].source`               | string, **required**           | Lowercase kebab-case segments joined by `:`, such as `dht` or `alice:bob:dht`                             |
| `sources[].seeders` / `leechers` | integer                        | Peer counts as last observed by that source                                                               |
| `sources[].scrapedAt`            | RFC 3339 UTC                   | When those counts were observed                                                                           |
| `sources[].publishedAt`          | RFC 3339 UTC                   | When that source first published the torrent                                                              |
| `sources[].completed`            | —                              | **Reserved.** Snatch count; unpopulated in 1.0                                                            |
| `tags`                           | —                              | **Reserved.** Generic labels the producer chose to publish; unpopulated in 1.0                            |

A reserved field is neither written nor read in 1.0. Filling one is a minor version bump,
so until then a reader treats it like any other unknown field.

A single-file torrent has one file entry: index `0`, its name as the path, and its size.

### Vendor section `magnes`

Other programs ignore this section, and converters may drop it. Nothing in it is required
to understand the torrent. It holds two things:

- `classification` is the producer's current classification. Every field is optional,
  and an absent field means unknown. A receiving instance treats it as a hint, and its own
  classifier still decides.
- `tags` holds the producer's local tags. They are exported only with `--include-tags`.

| Field              | Type            | Meaning                                                                                                      |
| ------------------ | --------------- | ------------------------------------------------------------------------------------------------------------ |
| `contentType`      | enum            | See below                                                                                                    |
| `work`             | object          | Work identity, always confirmed by a catalogue provider. `source` and `id` are both required when present    |
| `work.source`      | string          | Provider key: `tmdb`, `imdb`, `tvdb`                                                                         |
| `work.id`          | string          | The provider's identifier                                                                                    |
| `inferredTitle`    | string          | Title inferred from torrent evidence, not the work's catalogue title                                         |
| `inferredYear`     | integer         | Year inferred from torrent evidence                                                                          |
| `languages`        | array of string | ISO 639-1 two-letter codes                                                                                   |
| `episodes`         | array           | `{season, episodes: [int]}` or `{season, wholeSeason: true}`, exactly one of the two. Season `0` is specials |
| `video.resolution` | enum            | See below                                                                                                    |
| `video.source`     | enum            |                                                                                                              |
| `video.codec`      | enum            |                                                                                                              |
| `video.3d`         | enum            |                                                                                                              |
| `video.modifier`   | enum            |                                                                                                              |
| `releaseGroup`     | string          |                                                                                                              |

The format has its own enum vocabulary, in lowercase kebab-case:

| Field              | Values                                                                                |
| ------------------ | ------------------------------------------------------------------------------------- |
| `contentType`      | `movie`, `tv-show`, `music`, `ebook`, `comic`, `audiobook`, `game`, `software`, `xxx` |
| `video.resolution` | `360p`, `480p`, `540p`, `576p`, `720p`, `1080p`, `1440p`, `2160p`, `4320p`            |
| `video.source`     | `cam`, `telesync`, `telecine`, `workprint`, `dvd`, `tv`, `web-dl`, `webrip`, `bluray` |
| `video.codec`      | `av1`, `h264`, `x264`, `x265`, `xvid`, `divx`, `mpeg2`, `mpeg4`                       |
| `video.3d`         | `3d`, `3d-sbs`, `3d-ou`                                                               |
| `video.modifier`   | `regional`, `screener`, `rawhd`, `brdisk`, `remux`                                    |

- An unknown enum value is treated as an unknown field. It is dropped, the rest of the
  record is kept, and the value is reported. A language code the reader does not support
  is handled the same way. Magnes supports a subset of ISO 639-1, so a valid code
  outside that subset is also dropped and reported.
- Adding a value is a minor bump. Removing or renaming one is a major bump.
- A writer fails on an internal value that has no spelling here, rather than leak it.
- Never included: the work's title, year or other extended work metadata, evaluation
  history, recipe internals and raw hint rows.

## Conventions

- Field names are camelCase, and keys match exactly, including case.
- Timestamps are RFC 3339 in UTC. Sizes and counts are non-negative integers.
- **An absent field means unknown.** `null` is not used. A reader treats `null` as absent,
  and reports it.
- Every unknown field is ignored. A reader reports one line per unknown field path, with
  a count, rather than one per record.

## Reading

A reader distinguishes a line it skips from an error that stops the stream.

- **Fatal**, and nothing after it is read: a record before the first header, a header
  with another `format.name`, a header with an unknown major version, and a header whose
  `format` cannot be read, because the records after it could not be interpreted. A
  header is recognised by its top-level `format` key, so a line that is not valid JSON
  is a malformed line, never a header.
- **Malformed line**, which is skipped and reported by line number: a line that is not a
  JSON object, a record missing `infoHash` or `name`, an `infoHash` that is not 40
  lowercase hex digits, a neutral-core field holding the wrong JSON type, and a file
  entry missing `index`, `path` or `size`. A file entry is never dropped on its own,
  because that would silently turn a complete list into a partial one.
- **Dropped and reported**, with the rest of the line kept:
  - unknown fields, including reserved ones;
  - `null`s, read as absent, with `null` array elements skipped;
  - unknown enum values and language codes;
  - source entries whose name breaks the label rule;
  - a header's `producer`, `exportedAt` or `since` holding the wrong type, since none of
    them affects how the file is read;
  - a `magnes` section, its `classification` or its `tags` holding a wrong JSON type
    anywhere inside, since nothing in the vendor section is needed to understand the
    torrent. The whole part is dropped, so nothing else inside it is reported;
  - a `work` missing `source` or `id`;
  - an `episodes` entry that is not exactly one of a non-empty `episodes` list or
    `wholeSeason: true`, or that has a negative season or episode. An empty `episodes`
    list and `wholeSeason: false` are each the same as leaving the field out.
- Blank lines are ignored.

Reports name array-index-free paths, such as `sources[].completed`.

## Check

`bitmagnet check <file|->` reads a file, or standard input for `-`, without importing it.
It needs no database and no configuration. It reports:

- the format version and producer of every header;
- the record count;
- malformed lines, with their line numbers;
- unknown field paths and `null`s, with counts;
- unknown values, invalid source names and invalid entries, with counts;
- incoming `magnes.tags`, with counts.

It exits `1` on a fatal error, after printing what it read before it, and `2` when it is
not given exactly one input. Malformed lines and dropped fields do not change the exit
code.

## Export

`bitmagnet export [--include-tags] <file|->` writes the visible catalogue to a file, or to
standard output for `-`. A file is written beside its target and renamed into place only
once the export has succeeded, so a failed export leaves any previous file intact. It exits
`1` on a failure and `2` when it is not given exactly one output.

- The export is a CLI command. It reads from a single snapshot, in pages of torrents, so
  its memory use does not grow with the catalogue.
- It writes one header, with `producer.program` `magnes`, the build's version, and the
  snapshot's time as `exportedAt`.
- Pending and banned torrents are never written. Bans travel only as the
  [ban file](moderation-ban-file.md).
- Source names are normalised to the label rule on the way out. A stored `Torrents CSV` is
  written as `torrents-csv`.
- Origin prefixes are kept as stored.
- Two stored names that normalise to one name become one entry. The earliest
  `publishedAt` wins, and the peer counts come from the most recently updated row that
  has one.
- The record's `publishedAt` is the earliest across its sources.
- `scrapedAt` is not written, because no stored value records when peer counts were
  observed. A source row's `updated_at` also moves on writes that leave the counts alone.
- The classification is the torrent's current one. A classification that knows nothing,
  an empty `inferredTitle`, and empty `languages` and `episodes` are unknown, so they are
  absent.
- `magnes.tags` is written only with `--include-tags`.
- An internal value with no spelling in the format, or a source name that cannot be
  normalised, stops the export with an error that names the value and the torrent.
- Full export ships first. `--since` follows:
  - The cursor is the newest `updated_at` across every table a record is built from,
    because `torrents.updated_at` alone misses seeder changes.
  - It re-reads an overlap window, because timestamps are taken before commit.

## Import

- An import requires an **origin label**: lowercase kebab-case, such as `alice`. See
  [ADR 0010](adr/0010-prefix-imported-sources-with-their-origin.md).
- `/import` accepts only exchange files. The legacy flat format is dropped.
- Interfaces:
  - CLI `import --origin <label> [--overwrite-hints] <file|->`, which reads a file or
    standard input.
  - HTTP `POST /import?origin=<label>[&overwriteHints=true]`. `origin` is required. The
    endpoint accepts `application/x-ndjson` or `application/json`, and keeps its
    permission check.
  - Both interfaces share one import code path.
- Every record passes ingestion moderation.
- Merge rules for an infohash the receiver already has:

| Data                      | Rule                                                                                                                                                                                                                                                  |
| ------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `name`, `size`, `private` | Local wins. They are only filled in when the torrent is new.                                                                                                                                                                                          |
| `files`                   | Taken only when local files are unknown or partial **and** the incoming list is complete                                                                                                                                                              |
| `sources[]`               | Stored as `<origin>:<source>`, so `dht` becomes `alice:dht` and `bob:dht` becomes `alice:bob:dht`. Never writes an unprefixed source. Within one stored source, the newer `scrapedAt` wins, and a known value is never overwritten by an unknown one. |
| `magnes.classification`   | Becomes the torrent's **hint**. The receiver's classifier still decides. An existing hint wins unless the import passes an explicit overwrite flag.                                                                                                   |
| `magnes.tags`             | Unioned into local tags, on by default. A local tag is never removed.                                                                                                                                                                                 |

- Source entries whose names break the label rule are dropped and reported.
- A database-aware dry run, reporting new, would-update and banned counts, can follow
  `check`. The merge rules above define what it would report.
