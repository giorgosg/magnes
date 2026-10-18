# Prefix imported sources with their origin

Accepted design, 2026-10-18. Implemented by the CLI `import` command. An exchange file lists the sources that
observed each torrent, such as `dht` or `rarbg`. Every import is given an origin label, and
each incoming source is stored as `<origin>:<source>`. Importing Alice's file writes
`alice:dht` and `alice:rarbg`. An import never writes an unprefixed source, so a plain
`dht` row always means this instance's own crawler observed the torrent. Exports keep
stored names unchanged, so prefixes accumulate across hops (`alice:bob:dht`), and
everything that arrived through one origin can be found by a prefix match. Origin labels
and source segments are lowercase kebab-case joined by `:`.

The reason is provenance. Removing torrents that were only ever imported, and never
confirmed by the local crawler, is a real cleanup operation. It works by checking for the
absence of a locally observed source. If incoming `dht` observations were merged into the
local `dht` row, a friend's crawl would become indistinguishable from this instance's own
crawl, and that cleanup could never be done again.

## Considered options

- **Keep incoming source names as sent.** This gives the simplest peer-count merge, but it
  permanently loses provenance, as described above.
- **Collapse each import into a single source row named by its origin.** This preserves
  provenance, but it discards the sender's per-source breakdown.
- **Record provenance in a separate table.** This is exact, but it adds a row per imported
  torrent and duplicates what source rows already express.

## Consequences

Source rows grow by one per incoming source per torrent. The same swarm may be recorded
several times, for example as `dht`, `alice:dht` and `alice:bob:dht`. That does not
inflate popularity, which takes the maximum seeder count across sources rather than the
sum. Peer counts merge within each stored source: the newer observation wins, and a known
count is never replaced by an unknown one. Re-exporting publishes the origin labels the
operator chose.
