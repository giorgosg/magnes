# Running bitmagnet with authentication

The operator's page: what to set, what it does, and what you have to do outside bitmagnet
for it to hold. For how the code is put together and why each control is shaped the way it
is, see [architecture/auth.md](architecture/auth.md).

Upstream `main` has **no authentication at all** — GraphQL, Torznab and the web UI are all
open, and CORS allows `*`, so any web page in any browser can query a reachable instance
directly. That is why bitmagnet is normally run on a trusted network only. This lineage
adds authentication; a fresh installation requires credentials for catalogue searches and
Torznab until an administrator grants the `anon` role access.

## First login and anonymous access

`auth.anonymous_access` defaults to `true`, but a new installation gives the `anon` role
**no permissions**. Searching, browsing and Torznab require credentials until an
administrator grants that role the desired object actions through `putRole`. Set the flag
to `false` to deny anonymous access regardless of those grants.

The setting is a **deny-override over a role**, not a permission set of its own:

- `true` means anonymous callers get exactly what the `anon` role holds in the database.
  An administrator changes that through `putRole`, in the running process, with no restart
  and no configuration edit.
- `false` means anonymous callers get nothing, whatever the role holds. The role's stored
  permissions are dropped when the authorization policy is compiled, so they are inert
  rather than deleted, and switching the setting back restores them.

Two consequences worth stating plainly:

- **The `anon` role's grants take effect only while `auth.anonymous_access` allows it.** An
  administrator who grants the anon role permissions while the setting is `false` has
  configured something that does nothing yet. `listRoles` reports the grant, because it is
  genuinely configured; it is simply not honoured.
- **Granting the `anon` role the `auth` object hands the instance away.** An anonymous
  caller that can call `putRole` can give the anon role a wildcard, and because the grant
  is stored it survives restarts. Set `auth.anonymous_access: false` as an emergency deny
  switch, then remove the grant before turning it back on. Nothing in the server stops an
  administrator making the grant; the role editor should explain the consequence.

If you choose to grant anonymous catalogue access, keep these actions out of that grant:

| Withheld from anonymous                                             | Why                                                                                                                                                                                                                                                 |
| ------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `torrent:delete`, `torrent:mutate`, `queue:mutate`, `import:mutate` | `allowed_origins` defaults to `*` and `Content-Type: application/json` is an allowed header, so any page the operator visited could issue these cross-origin. For a delete the side effect is the damage, so an unreadable response bought nothing. |
| `auth:query`, `auth:mutate`                                         | An anonymous caller that can administer auth can grant the anon role a wildcard. An administrator may still grant it, with the consequence above.                                                                                                   |
| `pprof:query`                                                       | `/debug/pprof` dumps process memory structure and every stack, and `/debug/pprof/profile` runs a CPU profile for a caller-chosen `?seconds=`, as often as asked.                                                                                    |
| `metrics:query`                                                     | The scrape discloses index size, queue depth and crawl rate.                                                                                                                                                                                        |

New object actions are denied to anonymous callers until an administrator grants them.

An anonymous GraphQL refusal carries `AUTHENTICATION_REQUIRED`, so a UI can show a
login prompt; an authenticated Identity lacking an action gets `UNAUTHORIZED`. Torznab
returns XML error 100 with “Authentication required” when no key was supplied, or
“Incorrect user credentials” when a supplied key was rejected.

On the first start with the empty-role default, bitmagnet clears the `anon` role's stored
grants and records `auth.anon_role_empty_default` in `key_values`. This applies to upgrades
as well as new installations: anonymous searches and Torznab access stop until an
administrator grants them again. The earlier automatic seed and an administrator's grants
are identical rows, so the upgrade cannot preserve one while removing the other. Later
restarts leave any new grants alone. The startup log reports the empty role and how to open
the desired read actions.

Two object actions are outside all of this. `version:query` and `health:query` are granted
to anonymous callers regardless of the setting, because the web UI shell reads them before
anyone can log in — a closed instance still has to be able to show a login screen.

Then start the process and **read the log for the bootstrap invitation code** — or ask
for it afterwards with `bitmagnet auth initial-invitation`, below. An
`auth_initial_invitation` worker creates an admin invitation when no enabled admin user
exists and logs its code; that is how the first administrator registers, through the web
UI's registration form. It is idempotent — a restart finds the unclaimed invitation rather
than issuing another — and safe to run as multiple replicas.

**The code is logged once, when it is created.** Later boots report only its last four
characters, enough to tell that an invitation is still outstanding and which one, without
every log file, aggregator and support bundle holding a live path to the first
administrator account.

### Recovering the code

If you missed that log line, or you are adopting an instance somebody else started, read
the outstanding code back off the machine:

```bash
bitmagnet auth initial-invitation
```

Paste what it prints into the **Invitation** field of the registration form; registering
with it creates the first administrator. It reads and never writes — running it twice
prints the same code, and an instance deliberately left without an administrator
invitation still has none afterwards. It reports the other states in words rather than by
printing nothing: an administrator already exists, or nothing is outstanding because the
workers have not run.

The command writes the code to stdout on its own and everything else to stderr. **The
application's logger also writes to stdout**, though, so a migration or startup line can
land in front of the code on the pipe. Raising the log level suppresses the routine ones:

```bash
LOG_LEVEL=error bitmagnet auth initial-invitation
```

That reduces the noise rather than eliminating it — a warning or error still goes to
stdout. For a script, take the last line rather than the whole stream:

```bash
code=$(LOG_LEVEL=error bitmagnet auth initial-invitation 2>/dev/null | tail -n 1)
```

There is deliberately no registration **link**. bitmagnet does not know its own external
scheme, host, port, or the path a UI is mounted under behind a reverse proxy, so any URL
it built would be wrong exactly when you could least afford it.

**The code is never exposed over the API**, authenticated or not: reaching it requires
access to the machine, which is the same boundary the log line had. Serving it over
GraphQL would hand the instance to whoever asked first.

Deleting the unclaimed invitation and restarting still works, and mints a fresh one:

```sql
DELETE FROM invitations
WHERE role_name = 'admin' AND created_by IS NULL AND claimed_by IS NULL;
```

## Configuration

| Key                              | Default              |                                                                                                                 |
| -------------------------------- | -------------------- | --------------------------------------------------------------------------------------------------------------- |
| `auth.anonymous_access`          | `true`               | deny-override on the `anon` role: `false` denies anonymous callers whatever the role holds; `true` defers to it |
| `auth.jwt_secret`                | _(none)_             | random per process when unset, so tokens do not survive a restart                                               |
| `auth.jwt_duration`              | `24h`                |                                                                                                                 |
| `auth.browser_cookie_name`       | `__Secure-bitmagnet` | must retain the `__Secure-` prefix                                                                              |
| `auth.rbac_cache_ttl`            | `1m`                 | how long a revoked permission or role change stays in force                                                     |
| `auth.invitation_required`       | `true`               |                                                                                                                 |
| `auth.email_required`            | `false`              |                                                                                                                 |
| `auth.email_verification`        | `false`              | inert — see Known gaps                                                                                          |
| `auth.password_min_entropy`      | `70`                 |                                                                                                                 |
| `auth.password_hashing_cost`     | bcrypt default       | applies to registration _and_ password changes                                                                  |
| `auth.login_requests_per_minute` | `30`                 | per bucket, not per process                                                                                     |
| `auth.login_request_burst`       | `5`                  | per bucket, not per process                                                                                     |
| `graphql.introspection`          | `false`              | `__schema` and `__type` queries; off unless asked for                                                           |
| `graphql.playground`             | `false`              | GraphiQL on `GET /graphql`; off means the route 404s                                                            |

**Set `auth.jwt_secret` if you do not want every restart to log everyone out.** Unset, it
is generated per process.

**The two `graphql.*` keys gate developer surface, not access.** Schema introspection and
the GraphiQL page carry no authorization of their own, so on an instance reachable
anonymously they hand out a full map of the API. Nothing shipped needs either at runtime —
clients can generate against the schema files — so both default to off, and a developer turns them on deliberately.

## Browser login

Same-origin browser clients can call `self.loginBrowser` to receive the session credential
as an `HttpOnly`, `Secure`, `SameSite=Strict` cookie on `/graphql`. The mutation returns no
credential or Identity snapshot; query `self.identity` after it succeeds. `self.logoutBrowser`
expires the same cookie and succeeds even when the browser has no usable credential.

Logging out also **revokes the session**, rather than only dropping the browser's copy of
it. That ends every session for that account, on every device and both transports — see
[Ending a session](#ending-a-session).

The cookie name defaults to `__Secure-bitmagnet` and is configurable through
`auth.browser_cookie_name`, but the `__Secure-` prefix is required. Its expiry follows
`auth.jwt_duration`. Because browsers reject `Secure` cookies over plain HTTP, browser login
requires HTTPS in development as well as production. Bearer login remains available through
`self.login` for clients that manage their own credential.

The top-level `self` query and mutation are a recovery boundary, not Role Permissions.
Identity discovery, registration, both login forms, and browser logout remain reachable even
when the current Role has no Permissions. Sensitive fields below that boundary — listing,
creating, or deleting API keys — still require an authenticated User session and reject API
key identities as well as Anonymous callers.

## Ending a session

A JWT is stateless: it cannot be recalled, and until it expires it is accepted on its own
merits. Every token therefore carries the **epoch** of its user's sessions, and
authentication refuses a token whose epoch is behind the one on the user row. Two things
bump it:

| Operation             | Effect                                                                |
| --------------------- | --------------------------------------------------------------------- |
| `self.logoutBrowser`  | ends every session for the calling account, then expires the cookie   |
| `self.updatePassword` | ends every session for the account, including the one that changed it |

Both revoke **all** of that account's sessions, not just the credential in hand. bitmagnet
keeps no per-session record — see
[ADR 0003](adr/0003-revoke-sessions-with-a-per-user-token-epoch.md) — so "sign out of this
device only" is not available. A revoked token behaves exactly like any other dead
credential: the request falls through to Anonymous rather than being refused outright, so
the client can still reach `self.login` to replace it.

`self.updatePassword(input: {currentPassword, newPassword})` is the rotation surface. It
requires an interactive User session — an API key is refused, as it is for key management —
checks the current password, and holds the new password to `auth.password_min_entropy`. The
caller has to log in again afterwards; that is deliberate, since the operation exists for
the case where a credential has leaked.

Revoking does not touch API keys. They are separate credentials with their own lifecycle:
delete the key.

An explicit `Authorization` header always takes precedence over the browser cookie. A bad
explicit bearer credential falls back to the Anonymous identity; it does not borrow the
cookie's authority. An expired, malformed, disabled, or deleted-User cookie also falls back
to Anonymous, and the response expires that cookie so the next request can recover cleanly.
Database and authorization-service failures remain errors and do not clear the cookie.

Cookie-backed GraphQL mutations require an `Origin` of `https://<request-host>` before any
resolver runs. Cookie-backed queries remain available without `Origin`; they cannot change
server state. Explicit bearer requests do not depend on `Origin`, and anonymous requests —
including `loginBrowser` before a browser credential exists — remain governed by CORS. The
GraphQL endpoint accepts JSON POST only; multipart uploads and WebSocket upgrades are not
supported.

## GraphQL authentication error codes

Authentication and registration failures expose stable machine-readable values at
`errors[].extensions.code`. Clients must branch on the code, not on the human-readable
message or its wrapping. Application errors retain their GraphQL `path` and `locations`.

| Code                                    | Meaning                                                        |
| --------------------------------------- | -------------------------------------------------------------- |
| `INVALID_CREDENTIALS`                   | The username/password pair is unusable                         |
| `USER_DISABLED`                         | The credentials are correct, but the User is disabled          |
| `LOGIN_THROTTLED`                       | The login bucket has no capacity; retry later                  |
| `USER_ALREADY_EXISTS`                   | Registration would duplicate a User                            |
| `USERNAME_INVALID`                      | The username does not meet server validation                   |
| `INVITATION_REQUIRED`                   | Registration requires an Invitation code                       |
| `INVITATION_INVALID`                    | The Invitation code does not exist                             |
| `INVITATION_EXPIRED`                    | The Invitation has expired                                     |
| `INVITATION_CLAIMED`                    | The Invitation has already been claimed                        |
| `EMAIL_REQUIRED`                        | `auth.email_required` is on and no email was supplied          |
| `EMAIL_INVALID`                         | The email does not match the server's address pattern          |
| `PASSWORD_INSUFFICIENT_ENTROPY`         | The password is below `auth.password_min_entropy`              |
| `PASSWORD_INCORRECT`                    | The current password given to `self.updatePassword` is wrong   |
| `ROLE_NOT_FOUND`                        | The named Role does not exist                                  |
| `PERMISSION_INVALID`                    | A requested API-key Object action is not a registered one      |
| `AUTHENTICATION_REQUIRED`               | An anonymous caller needs a credential for this Object action  |
| `UNAUTHORIZED`                          | The Identity lacks the refused GraphQL Object action           |
| `AUTHENTICATION_INFRASTRUCTURE_FAILURE` | A credential could not be resolved because a dependency failed |
| `USER_SESSION_REQUIRED`                 | The field requires an interactive User session                 |
| `API_KEY_MANAGEMENT_FORBIDDEN`          | An API-key Identity attempted to manage API keys               |
| `INTERNAL_SERVER_ERROR`                 | An unclassified server-side failure occurred                   |

`AUTHENTICATION_REQUIRED` and `UNAUTHORIZED` include `namespace`, `object`, and `action` in their extensions. The
two session-specific codes are field-level account constraints, not Role-Permission
refusals, so they do not invent an Object action. `ROLE_NOT_FOUND` and
`PERMISSION_INVALID` are input validation: both name something the caller supplied that
the server does not recognise. Previously one reached the client as a foreign-key
violation presented as `INTERNAL_SERVER_ERROR`, and the other was accepted silently as a
key that grants nothing. Authentication infrastructure and
unclassified internal failures use fixed public messages; their underlying error details
are not returned to the client. GraphQL parsing and validation errors remain gqlgen's
ordinary protocol errors rather than being mislabeled as application failures.

## Magnes integration environment

Exercise Magnes against the same boundary a browser will use in production. The reference
harness is `internal/gql/resolvers/auth_integration_test.go`; it mounts the production Gin
authentication middleware and gqlgen server over an isolated, fully migrated PostgreSQL
database. Run that matrix, including the unchanged Torznab boundary, with:

```bash
TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/postgres' \
  go test ./internal/gql/resolvers ./internal/torznab/httpserver -count=1
```

For a real Magnes browser run, make these three properties explicit:

1. Set `auth.jwt_secret` to one fixed value shared by every bitmagnet replica and every
   restart in the environment. A generated-per-process secret invalidates the browser
   cookie whenever the backend restarts. A deterministic test-only value is acceptable in
   an isolated integration environment; do not reuse it in production.
2. Leave `http_server.trusted_proxies` empty when the browser connects directly. When TLS
   terminates at a reverse proxy, set it to that proxy's actual CIDR — and only that CIDR —
   so forwarded client addresses feed the login throttle without becoming caller-controlled.
3. Serve Magnes and `/graphql` from the exact same browser-visible HTTPS origin, including
   port. The `__Secure-` cookie is rejected over plain HTTP, and cookie-authorized mutations
   require an `Origin` of `https://<request-host>`. A TLS-terminating proxy may use HTTP to
   bitmagnet internally, but it must preserve the browser-facing `Host` when forwarding
   `/graphql`.

Set `auth.anonymous_access` to `false` for the authenticated Magnes scenario, and also run
the matrix once with its default `true` value to preserve the open-installation contract.
Registration uses the bootstrap Invitation from the bitmagnet log — or from
`bitmagnet auth initial-invitation` — and remains separate
from login; after registration, submit the username and password to `loginBrowser`, then
query `self.identity`. Browser code must never read or store the cookie value.

**Every bound in that table is enforced, and they are load-bearing.**
`login_requests_per_minute: 0` reaches `rate.Every(time.Minute / 0)` and takes the process
down from a config file alone; `password_min_entropy: 0` accepts any password at all; a
`jwt_duration` of zero issues tokens that have already expired. `configresolver` validates
every resolved config struct, so a bad value fails at startup rather than in production.

One key outside the `auth.` tree matters as much as any of them:

| Key                           | Default   |                                                        |
| ----------------------------- | --------- | ------------------------------------------------------ |
| `http_server.trusted_proxies` | _(empty)_ | whose `X-Forwarded-For` to believe; empty means nobody |

## If you run bitmagnet behind a reverse proxy

Two things need doing, and neither is optional once authentication is on.

**1. Set `http_server.trusted_proxies` to your proxy's CIDR.** The login throttle is keyed
partly by client address, taken from gin's `ClientIP()`. Gin's own default is to trust
_every_ proxy, which would make that address a header the attacker writes — rotating
`X-Forwarded-For` buys a fresh budget per request. bitmagnet therefore defaults to
trusting nobody and using the peer that actually opened the connection. Behind a proxy and
with this unset, every request is attributed to the proxy and shares one bucket; the
per-source budget is wide enough to absorb that, but the per-account protection is what is
really doing the work until you set it.

**2. Redact the `apikey` query parameter in your proxy's logs.** The Torznab protocol
carries its credential in the URL, and API keys do not expire by default — so read access
to a log recording full URLs is equivalent to application access. bitmagnet redacts
`apikey`, `token`, `password`, `secret` and `api_key` from **its own** logging, in both
the request logger and the panic recovery path. Nothing else in the request path does. A
reverse proxy, ingress controller, CDN or WAF will record the full URL unless you tell it
not to.

## Torznab and \*arr clients

The credential is accepted as the `apikey` query parameter (what \*arr clients actually
send) or an `X-Api-Key` header. A refusal is a Torznab XML error, code 100 "Incorrect user
credentials", not a bare status code, because \*arr clients parse the error code.

**There is no network-based bypass**: being on the LAN is not a credential for machine
access. And Torznab takes machine credentials only — a browser session presented in the
`apikey` slot is refused, and the endpoint ignores whatever the global bearer middleware
resolved.

Issue an API key through the GraphQL `self.createAPIKey` mutation or your external UI. Keys can be scoped to individual
object actions, so a key handed to Prowlarr can be allowed Torznab and nothing else.

**Keys issued before this version keep working, and there is nothing to run.** How a key's
secret is hashed changed — bcrypt was costing ~57 ms of CPU per request that presented a
key, before any authorization check, which made a guessable key id a way for anyone who
could reach the port to burn a core with no credential at all. Existing keys are re-hashed
automatically the first time the right secret is presented, so an \*arr client polling
Torznab migrates its own key within seconds, and so does a key whose owner is disabled or
whose expiry has passed.

**One thing is worth five minutes if this instance has been running a while.** A key that
is never used again is never re-hashed, and until it is, _that key's id still carries the
old problem_ — not a cost to its owner, but the original hole, open to anyone who can
reach the port. Nothing in the UI or the API reports which keys those are, so ask the
database:

```sql
select id, name, user_id, created_at from api_keys where length(hash) = 60;
```

Every row it returns is a key that has not been used since the upgrade. Use each one once,
or delete it; either closes it. An empty result means there is nothing left to do. The
reasoning is in
[docs/architecture/auth.md](architecture/auth.md#why-an-api-keys-hash-is-fast).

## Endpoints that are not GraphQL

`/import`, `/metrics` and `/debug/pprof/*` are guarded by object actions in the `http`
namespace. **None of the three is anonymous, whatever `auth.anonymous_access` is set to**
— `/import` because it writes, and the other two because profiling and the scrape are the
operator's instruments rather than the catalogue.

That is a change for anyone whose **Prometheus scrape was unauthenticated**. Mint an API
key scoped to `http:metrics:query` and give it to Prometheus as a bearer token:

```yaml
scrape_configs:
  - job_name: bitmagnet
    authorization:
      credentials: <api key>
    static_configs:
      - targets: ["bitmagnet:3333"]
```

The same applies to any profiling you do by hand, which now needs a credential carrying
`http:pprof:query`.

`/import` additionally requires `Content-Type: application/x-ndjson` or
`application/json` and answers 415 otherwise, before reading the body. This is defence
against the browser rather than against the operator: without it, a cross-origin `POST`
of `text/plain` is a CORS simple request and lands with no preflight, whatever
`allowed_origins` says. Neither accepted type is a simple-request type. The endpoint reads
[exchange files](exchange-file.md#http-import) only; the legacy flat format is gone.

`/status` is deliberately public, matching the `health::query` grant in the GraphQL
baseline: orchestrators poll it, and it reports liveness only.

## Known gaps

- **`auth.email_verification` does nothing.** The parameter is carried over from
  `upstream/next` for fidelity, but its value is never read and no verification code is
  ever issued — the `users.email_verify_code` column is written nowhere. It is inert on
  `next` too. It defaults to `false` here, diverging from `next`, because a parameter that
  defaults to on while doing nothing tells an operator their addresses are verified when
  they are not. The default follows the behaviour, and flips back when the behaviour
  arrives.

## UI deployment and CORS

Release builds bundle the Magnes Elm UI at `/ui/` and redirect `/` there. It uses
same-origin `/graphql` and the browser cookie contract. Put a TLS-terminating reverse
proxy in front of the HTTP server and preserve the browser's Host (including its port):
the Secure cookie and browser-authenticated mutations require HTTPS.

`http_server.static.path` changes the mount; the bundled HTML base and runtime
configuration follow it automatically. `http_server.static.bundled: false` disables the
bundle. Plain Go/backend-only builds contain no UI. `http_server.static.dir` overrides
the bundle with an external UI and preserves that UI's own configuration; this mode has
no root redirect. `/webui` is a supported mount, but `/` remains refused because Gin's
static wildcard conflicts with API routes. An explicit `http_server.options` list must
include `static` to serve either UI; the removed Angular `webui` option is not valid.

Use `self.loginBrowser` for same-origin browser sessions. The HTTP-only cookie contract
is described above; Magnes leaves credentials in the HTTP-only cookie.

`http_server.cors.allowed_origins` now defaults to `[]`, withholding permission for
cross-origin browser reads. Same-origin requests and non-browser API clients need no
CORS permission. To deploy a UI on another origin, list its exact origin explicitly;
`["*"]` restores the previous public cross-origin behavior. This controls browser access,
while authentication and role permissions still govern API access.

The default allowed headers are `Content-Type`, `Authorization` and `X-Api-Key`.
`X-Import-Id` left the list with the legacy import format, which was the only thing that
read it. If a cross-origin client needs another header, add it to
`http_server.cors.allowed_headers`.

## Moderation and catalogue reads

Catalogue query permission does not expose pending/banned torrent metadata. Ordinary
GraphQL and Torznab reads, including files, direct hash requests, suggestions, facets and
source listings, enforce the exact moderation ledger irrespective of the caller's role.
Private moderation search, direct evidence and history require
`graphql:moderation:query`; flag, allow, ban, reopen and purge require
`graphql:moderation:mutate`. Catalogue/tag Permissions do not imply moderation access.
Administrators can grant these Object actions to a custom Role or scoped API key; the
fixture's anonymous catalogue grant excludes them. Evidence/history selected from mutation
results require the query action as well. See [moderator operations](moderation.md). See [architecture/moderation.md](architecture/moderation.md).

The explicit ban-file download uses `http:moderation:export`, separately from query and
mutate. It has no baseline grant. `GET /graphql/moderation/export` accepts a permissioned
browser cookie or bearer credential; the cookie remains scoped to `/graphql` and its
resolver accepts this exact additional read-only path. HTTP surfaces outside the exact moderation export/import paths do not borrow
browser authority. Forward this path through the same HTTPS proxy as `/graphql` and include
`moderation` when configuring an explicit HTTP option list. See
[moderator sharing](moderation.md) and the [file contract](moderation-ban-file.md).

Ban-file upload requires the separate `http:moderation:import` Object action, without a
baseline grant. `POST /graphql/moderation/import` accepts a JSON file up to 8 MiB and
10,000 entries, with source labels up to 256 UTF-8 bytes. Query, mutate and export do not imply import. The browser moderation area
also requires its existing query Permission. Cookie-backed uploads enforce the same HTTPS
Origin/host check as GraphQL mutations before accepting a body; bearer clients use explicit
credentials and selected Object actions. Forward the exact upload path through the same
HTTPS proxy and permit its bounded body size. See [the import contract](moderation-ban-file.md).
