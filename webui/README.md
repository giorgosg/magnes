# Magnes

The default web UI for this bitmagnet fork, written in [Elm](https://elm-lang.org).

Backend and UI development happen together. From the repository root, `task build`
builds both into one executable; Docker images and releases include the same UI at `/ui/`.
Use `task build-backend` when only the backend is needed.

## Status

Runs. Search, sort, facet filters, infinite scroll, row expansion down to a file tree and
`/torrent/<hash>` are built.

Identity and permissions are in, against
[the bitmagnet fork](https://github.com/giorgosg/magnes)'s browser-cookie contract. That
covers registration through an Invitation, sign-in, a User overview with sign-out and a
password change, API-key management, and administration of Users, Invitations and Roles.
Changing a password signs the User out everywhere, this browser included, which the form says
before it is sent and the login form says again afterwards; the new password is offered to the
browser's password manager on the way. If the answer to a change is lost, Magnes keeps the new
password until it knows, and the login form tries it first. The header menu offers only the
pages the current Identity may reach. A credentialed end-to-end suite drives the signed-in
half in a real browser, against a disposable bitmagnet it starts itself, so no real password
is involved.

`/status` shows bitmagnet's health checks, and its workers to an Identity that may see them.
A dot in the header gives the same answer at a glance, polled every 30 seconds while the tab
is showing, and links to it. Under the report it lists the operational pages the Identity
may open. `/stats/torrents` draws how fast the index is growing: torrents new and updated
per time bucket, a pair of lines for each source, over the last quarter hour to the last
week, in buckets of minutes, hours or days. The timeframe, the bucket length, the sources
and how often to look again by itself (off until asked for, and paused while the tab is
hidden) are all in the URL. `/stats/queue` draws how bitmagnet's queue has behaved: jobs
created, processed and failed per time bucket, a line for each event of each queue, and a bar
for each queue of its jobs by status. It has the same controls, everything the queue holds
among the timeframes and opening on it by the hour, and chips that pick queues and events out
of the answer without asking again, all in the URL too. `/queue/jobs` is bitmagnet's queue a
page at a time: filtered by queue and status with each facet's counts, ordered, with every
choice in the URL, and each job opening to show its payload and error in full.

`/admin/queue`, offered in the header menu and on the status page to an Identity holding
`queue::mutate` (and `queue::query`, to count with), purges jobs and queues a batch that
reprocesses torrents.
A purge is chosen by queue and status, with bitmagnet's count of the jobs that match; a
reprocessing batch by which torrents, how they are classified, whether matches are looked up
and how big its jobs are, each option labelled with what the fork's source does with it.
Neither is sent until a question that says what it will do has been answered, including that
reprocessing can delete a torrent the classifier now judges unwanted, and the counts are asked
for again afterwards. An answer that does not come back leaves the act unknown, and the page
says so rather than inviting it twice. The page points to `/queue/jobs` to watch a batch run.

See [docs/plan.md](docs/plan.md) for the completed first milestone and
[docs/accounts-plan.md](docs/accounts-plan.md) for the current phase; the working notes
are in [docs/](docs/README.md).

## Running it

```
cd webui   # from the repository root
npm ci
BITMAGNET_URL=http://your-bitmagnet:3333 npm run dev
```

The development server uses HTTPS and proxies same-origin `/graphql` requests to
`BITMAGNET_URL`, allowing bitmagnet's Secure, HttpOnly browser cookie to behave as it does
in production. It creates a self-signed localhost certificate on first use; accept it once
in the development browser. The API address and static mount path remain runtime values,
so one compiled bundle works against any instance.

Default builds serve Magnes on the API origin at `/ui/`, with `/` redirecting there.
The bundled HTML base and runtime mount configuration follow `http_server.static.path`
automatically, and the API URL is `/graphql`. Set `http_server.static.bundled: false` to
turn it off. For an external deployment, `http_server.static.dir` can serve a separately
built `public/`; set that HTML base and `window.MAGNES_BASE_PATH` to its mount path.

**That mount has to be reached over HTTPS.** bitmagnet cannot serve TLS itself, and its
browser credential is a cookie marked `Secure`, which a browser on an insecure origin
discards without reporting anything — so over plain HTTP registration works, login appears
to work, and the person stays anonymous. Put a TLS-terminating proxy in front of it, and
pass the browser's `Host` through with its port. See
[docs/serving-and-testing.md](docs/serving-and-testing.md).

## Approach

Bitmagnet's entire API is GraphQL, so the client is generated rather than
hand-written: [`dillonkearns/elm-graphql`](https://github.com/dillonkearns/elm-graphql)
introspects the live schema and emits type-safe Elm for every query, object,
input and enum. A schema change in the fork becomes a compile error here, not a
runtime surprise.

The generated modules are committed under `src/Magnes/Api`, so a checkout builds without
reaching an instance. Regenerate against one when bitmagnet is upgraded:

```
BITMAGNET_URL=http://your-bitmagnet:3333 npm run codegen
```

That reads the schema by introspection, which bitmagnet keeps off by default. The client
can also be generated offline from the fork's committed SDL, with no instance at all; see
[Regenerating the client](docs/serving-and-testing.md#regenerating-the-client).

## Design decisions

### Bitmagnet serves Magnes and enforces access

The current deployment is the static Elm bundle served by bitmagnet's
`http_server.static` mount. Runtime GraphQL traffic stays on that origin, and the fork
enforces its permission model on every top-level field. Magnes reads those permissions to
present only reachable controls; bitmagnet remains the protection boundary.

A separate Magnes server is deferred. It would be justified by state bitmagnet does not
hold, such as saved searches, or by an independently hosted origin—not by access control
the fork already provides. Raise that decision before adding a server. See
[docs/accounts-plan.md](docs/accounts-plan.md).

### Anonymous access is a permission set

`auth.anonymous_access` decides what an unauthenticated identity may reach. When enabled,
that identity gets what bitmagnet's `anon` Role holds. A new installation seeds the Role
with the read object actions, so search works and nothing can be changed. When disabled,
the identity gets nothing beyond version and health, and search waits for login. The UI derives its navigation and controls from `self.identity.permissions`,
while the server enforces the same permissions regardless of what the UI draws.

### Search state lives in the URL

Search terms, filters and sort order are query parameters, not just fields in
the Elm model. A search is therefore a link — shareable, bookmarkable, and the
back button walks the queries you actually ran instead of doing nothing.

The cost is that every filter change round-trips through `onUrlChange`. Rapid
changes use `replaceUrl`, so a back press doesn't have to unwind one history
entry per keystroke.

### Results load on scroll

No page numbers. Results append as you reach the bottom.

Browsing a DHT index is scanning, not lookup; page 47 of 900 is not a place
anyone means to go. The trade-off is that scroll depth is not addressable — a
shared link restores the query, not your position five hundred results down.
That seems like the right thing to give up.

Because scanning means long scroll sessions, the list is virtualized from the
start rather than as a later optimization: only visible rows exist in the DOM.
Retrofitting that is awkward, since the code that decides _what to render_ and
the code that decides _when to fetch more_ both want to own the scroll handler.

### Rows expand in place

A result row is one dense line with the name truncated. Expanding it reveals
metadata; expanding further reveals the torrent's files — where there are any. On
a real index most torrents have no file information at all, so the second level
is offered conditionally rather than always drawn and then found empty. Most
torrents are also unclassified, so a row has to look right with nothing but a
title, a size and a date.

The files are a directory tree, not a list of paths: the last line of an expanded
row is the torrent's own folder, and opening it opens every folder beneath at
once. Folders then close one at a time. bitmagnet returns flat paths, so the tree
is reconstructed client-side.

Detail is therefore progressive and doesn't interrupt scanning — no round trip
to a detail page and back just to see a file list. Each state has a known
height, so virtualization stays exact without measuring anything. File lists are
fetched on demand and capped, since a single torrent can contain thousands of
files.

### A torrent also has its own address

`/torrent/<hash>` is a real route, and every row links to it — so middle-click
and "open in new tab" work, and a torrent can be shared without sharing the
search that found it. It renders the same expanded row the list does, standalone,
fetching that one torrent when opened cold.

A plain left-click on that link does _not_ follow it: it expands the row where it
is. Browsing an index means opening many rows in passing, and a page load for
each one is the wrong default — so the ordinary click is the cheap thing and the
address is what the deliberate gestures reach.

Expansion itself is _not_ in the URL. Scanning means expanding several rows in
passing, and if each one pushed a history entry the back button would fill with
them — the same problem `replaceUrl` solves for filter churn. So expansion is
local state, and the route is an address you can navigate to deliberately. The
two are independent, and multiple rows can be open at once.

### Real paths, not fragments

Routes are ordinary paths (`/search?q=…`, `/torrent/<hash>`), which requires whatever
serves the app to fall back to `index.html` on unmatched paths, so a deep link survives a
refresh. Everything that serves Magnes already does: `dev.js` in development, and
bitmagnet's own static mount in deployment. So this costs nothing, and it avoids the `#/`
in every URL.

## The name

"Magnet" comes from Magnesia, a region in Thessaly where lodestone was found —
and from Magnes, the shepherd of the legend whose iron-nailed sandals stuck to
the rock.

## License

[MIT](LICENSE), matching bitmagnet's own licence — a UI for a project should not be
harder to use than the project.
