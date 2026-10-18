---
title: Import torrent metadata
description: Load an exchange file through the authenticated import endpoint.
---

`POST /import` reads an exchange file: newline-delimited JSON, a header line followed by
one torrent record per line. The format is specified in
[the exchange file contract](https://github.com/giorgosg/magnes/blob/trunk/docs/exchange-file.md).
It does not accept a JSON array, a multipart upload, a raw `.torrent` file, or the flat
one-object-per-line format earlier versions read. Convert your source catalogue to an
exchange file before sending it, and check it locally first with
`bitmagnet check torrents.jsonl`.

Use an API key whose owning User and key Permissions include `http:import:mutate`.
The HTTP component is named `import`; the default HTTP options include it.
Configure your own TMDB key or disable TMDB before queueing a large classification load.

## Prepare the file

The first line is a header; every record after it needs at least a 40-character lowercase
hexadecimal infohash and a name. This illustrative file has one record; replace the
synthetic infohash and size with the real metadata from your source:

```text
{"format":{"name":"torrent-exchange","version":{"major":1,"minor":0}}}
{"infoHash":"0123456789abcdef0123456789abcdef01234567","name":"Public-domain collection","size":1048576,"sources":[{"source":"my-catalogue"}]}
```

Records can also carry file lists, per-source peer counts and publication dates, and a
classification that the receiving instance takes as a hint. A line may be at most 1 MiB.

## Send the import

Save the file as `torrents.jsonl`, with a final newline. Send it with
`Content-Type: application/x-ndjson` and an `origin` label naming where it came from: one
lowercase kebab-case word, such as `alice`. Every source in the file is stored under that
label, so `my-catalogue` becomes `alice:my-catalogue`. Add `&overwriteHints=true` to let
the file's classifications replace hints the instance already has.

The following example reads the API key from a private file and passes it to curl
through stdin, avoiding a literal key in command history or curl's arguments:

```sh
{
  printf 'header = "Authorization: Bearer '
  tr -d '\r\n' < /secure/path/import-api-key
  printf '"\n'
} | curl --config - --fail-with-body --no-buffer \
  -H 'Content-Type: application/x-ndjson' \
  --data-binary @torrents.jsonl \
  'https://magnes.example.com/import?origin=alice'
```

Replace the URL, key-file path and origin. Store the key file with access limited to its
owner.

## Check the outcome

The response is the import's report as JSON: the headers read, the record count,
malformed lines with their line numbers, new, updated, unchanged, banned and skipped
torrents, hints written and kept, tags added, and every dropped field or value with its
count. A status other than `200` carries an `error`; the counts then cover what was
committed before the import stopped. The import commits every 100 records, and importing
the same file again changes nothing the first import wrote, so a stopped import is resumed
by sending the file again.

Imported torrents enter the classification queue. They become searchable after
processing. Watch Queue jobs, then verify a few known names and source filters in the UI.
Split large inputs into manageable files and keep the converted source so you can
diagnose failed records without rerunning an entire import.
