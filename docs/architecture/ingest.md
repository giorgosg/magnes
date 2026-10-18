# Ingest — the DHT crawler

Everything in `internal/dhtcrawler` plus the protocol packages it drives. This is the
path that turns "a stranger on the internet mentioned a hash" into rows in `torrents`.

**It has little test coverage.** AGENTS.md names it explicitly. A change here gets a
focused regression test or a standalone reproduction.

## The pipeline

`crawler.start()` ([`crawler.go`](../../internal/dhtcrawler/crawler.go)) launches
seventeen goroutines that pass work between typed channels from `internal/concurrency`.
Two channel kinds do all the work:

- **`BatchingChannel[T]`** — accumulates until a count or a timeout, then emits `[]T`.
  Used where the consumer wants a batch: node discovery, triage, both persist stages.
- **`BufferedConcurrentChannel[T]`** — a buffered channel plus a semaphore; `Run` spawns
  up to `concurrency` goroutines. Used where the consumer does network I/O.

Every buffer and concurrency figure is `Config.ScalingFactor` (default 10) multiplied by
a constant in [`factory.go`](../../internal/dhtcrawler/factory.go). That is the one knob
for the crawler's resource usage.

```
 kTable ──▶ getNodesForFindNode ────▶ nodesForFindNode ──▶ find_node ──┐
 kTable ──▶ getNodesForSample… ─────▶ nodesForSampleInfoHashes ────────┤
 DHT server + responses ────────────▶ discoveredNodes (batching) ──────┘
                                             │
                    sample_infohashes yields infohashes
                                             ▼
                                  ignoreHashes (in-memory bloom)
                                             │ not seen before
                                             ▼
                                  infoHashTriage (batching, 20s)
                        ┌────────────────────┼────────────────────┐
                   need metainfo         need scrape          discard
                        ▼                    ▼
                    getPeers ──▶ requestMetaInfo        scrape (BEP 33)
                                      ▼                      ▼
                             persistTorrents (batch)   persistSources (batch)
```

## Discovery and validation

The process-local stable `ignoreHashes` Bloom filter suppresses repeated discoveries
under a mutex. It is a bounded load optimization and forgets hashes over time; it is
never moderation authority. Triage checks discovered identifiers against the exact
moderation ledger, discarding bans and permitting local allows and pending records.
Pending discovery can acquire missing private evidence.

Triage then joins `torrents` to `torrents_torrent_sources` to choose metainfo retrieval,
rescraping or discard. `banning.Checker` on fetched metainfo is structural validation
(name length, size and UTF-8), independent of harmful-content decisions. Rejection
cancels the peer race without creating a persistent block or a suppression map.

Migration `00026` discards `blocked_torrents` and its unshared large object. The
legacy blocking subsystem is removed. Deleted torrents may be rediscovered if peers
still offer them; discovery and recovery are not guaranteed. The identifier format is
frozen in [ADR 0006](../adr/0006-version-moderation-identifiers.md).

## Fetching metainfo

`internal/protocol/metainfo/metainforequester` implements BEP 9 over a raw TCP
connection: BitTorrent handshake, extension handshake, request every 16 KiB piece, read
them back, bencode-parse the result. `maxMetadataSize` is 10 MiB and bounds both the
declared metadata size and any single message.

For each hash, the crawler requests metadata from up to five peers at once and returns
the first permitted result. A shared request limit of `10 * ScalingFactor` bounds all
peer connections across hashes. Failed peers are tried until one succeeds or all fail;
a structurally invalid result rejects the request. The winning result or crawler shutdown cancels the
other requests, including a TCP connection stalled after its handshake begins.

Everything this package reads is attacker-controlled. It is the only place in the tree
where a remote party's bytes become array indices, and it runs on a crawler goroutine with
**no `recover()` anywhere in the path** — `BufferedConcurrentChannel.Run` does not install
one. A panic here takes the process down. See
the issue notes below.

## Persistence

[`persist.go`](../../internal/dhtcrawler/persist.go) is where the pipeline meets the
database, and it does more than its name suggests:

- `persistTorrentBatch` de-duplicates the batch, builds `Torrent`, `TorrentFile`,
  `TorrentsTorrentSource` and optionally `TorrentPieces` rows, and creates
  `queue_jobs` rows in batches of `classifyBatchSize` (100) — all in **one transaction**.
  On success it forwards each hash to the scrape channel. The write itself is
  `writeTorrentBatch`; the forwarding is separate because the shutdown drain below calls
  the write on its own, when there is no scrape stage left to forward to.
- Crawler and importer ingestion apply the shared harmful-keyword policy from the merged
  classifier source before catalogue writes. Initial matches retain complete observed
  metadata before the file cap, including a known single-file filename. This check also
  runs when queue backpressure skips classification work. Imports without metainfo retain
  a name without inventing paths; unmatched observations acquire no private evidence.
- The moderation service holds per-identifier transaction locks through catalogue
  writes and decision commits, including identifiers without an existing decision.
  A racing ban cannot pass a stale membership check. Banned observations enrich private
  evidence without catalogue insertion or scrape/classification forwarding. Pending
  records retain an independent copy of all observed paths before the ordinary file cap,
  and keep catalogue metadata hidden for a later local allow. Scraped source-count writes and imports use the same boundary.
- Files whose display path starts with `.pad/` are skipped (BEP 47 padding), while
  `FilesCount` is still derived from the full metainfo file list.
- `SaveFilesThreshold` (default 100) caps stored file rows; exceeding it sets
  `FilesStatus = over_threshold`, which triage reads back.
- `MaxQueueDepth` (default 50 000) is a backpressure valve: when `queue_jobs` has that
  many pending/retry rows, torrents are still persisted but **no classification job is
  created**. A `reprocess` run recovers them later. The depth is refreshed by a `COUNT`
  every 30 s.

`createTorrentSourceModel` derives seeders and leechers from the BEP 33 scrape bloom
filters' `ApproximatedSize()` — they are estimates, not counts.

`writeEligibleSourceBatch` writes those counts as an upsert on `torrents_torrent_sources`.
A torrent can be deleted between triage and that write, and its foreign key would reject
the whole batch, so the write first selects the batch's surviving `torrents` rows
`FOR KEY SHARE` and inserts only those. The lock is the one the foreign key check takes
anyway; taking it first means a delete still in flight is waited for and then skipped,
rather than failing the insert. GORM silently drops a `Where` on `Create` with
`OnConflict`, which is why this is not a `WHERE EXISTS` guard on the insert.

## Starting and stopping

`OnStart` gives the crawler a context of its own, deliberately not derived from the start
hook's — fx cancels that one as soon as startup finishes. `start` tracks all seventeen
stages in a `WaitGroup`, and `OnStop` cancels and then **waits**, bounded by the context
fx gives it.

That wait is what makes the shutdown lose nothing. Both persist stages feed from batching
channels that flush on a count of 1000 or after a minute, so returning from `OnStop`
immediately — which is what it used to do — discarded up to a minute of crawled torrents
on every restart. Once every stage has returned, `start` closes those two channels, which
flushes whatever is queued or buffered, and writes the last batches under a fresh context
with its own `persistDrainTimeout` (10s, inside fx's 15s `StopTimeout`), because the
crawler's own context is cancelled by then.

Two consequences worth knowing before changing anything here:

- **Every stage must observe the context.** The stop hook waits for all of them, so one
  that ignores cancellation hangs the process instead of losing a batch. Two of them did:
  `getNodesForFindNode` and `getNodesForSampleInfoHashes` only checked the context while
  handing a node on, and an empty routing table skips that path entirely.
- **`BufferedConcurrentChannel.Run` does not wait for the workers it spawns.** A stage
  returns while its in-flight handler goroutines are still finishing, so an item handed
  over after the drain is still lost. That tail is bounded by one in-flight request per
  concurrency slot, against a batch of up to a thousand before.

## The routing table

`internal/protocol/dht/ktable` is a btree-backed Kademlia routing table fed by
`BatchCommand` (`PutNode`, `DropNode`, `PutHash`, `DropAddr`) from every stage above. The
crawler asks it for nodes to ping, to `find_node`, and to `sample_infohashes`;
`soughtNodeID` is a random target rotated every 10 seconds so the crawl walks the whole
keyspace rather than settling near the node's own ID.

`internal/protocol/dht/server` answers inbound DHT requests, and feeds nodes it learns
about into the same `discoveredNodes` channel — which is why that channel is provided
separately in `discovered_nodes.go`, to break the circular dependency.

---

_Known defects and improvement ideas referenced above are kept as untracked `docs/issues/*.local.md` notes, which a given checkout may or may not have._
