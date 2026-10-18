---
title: HTTP and API endpoints
description: The paths served by a Magnes backend and how clients authenticate.
---

These paths belong to your running Magnes instance. The GitHub Pages documentation
site does not proxy them or connect to your catalogue.

| Path             | Purpose                       | Access                                                                                       |
| ---------------- | ----------------------------- | -------------------------------------------------------------------------------------------- |
| `/`              | Redirect to the included UI   | Present when the bundled UI is active                                                        |
| `/ui/`           | Bundled Elm interface         | The shell loads before sign-in; its data operations have separate Permissions                |
| `/graphql`       | GraphQL queries and mutations | User browser cookie or supported bearer credential; each Object action is authorised         |
| `/status`        | HTTP health check             | Available without signing in                                                                 |
| `/torznab`       | Servarr-compatible indexer    | API key and `torznab:torznab:query` Permission, or an explicitly permitted anonymous request |
| `/import`        | Exchange-file import          | `POST`, `application/x-ndjson`, `origin`, and `http:import:mutate` Permission                |
| `/metrics`       | Prometheus scrape             | `http:metrics:query` Permission                                                              |
| `/debug/pprof/*` | Go profiling                  | `http:pprof:query` Permission                                                                |

The static mount can be changed through `http_server.static.path`. An explicit HTTP
options list controls which components are served. A backend-only build does not
include the UI; an external static directory overrides the bundle.

## GraphQL clients

The committed [GraphQL schema](https://github.com/giorgosg/magnes/tree/trunk/graphql/schema)
is the API contract. Introspection and the interactive playground are disabled by
default. Generate your client against a schema matching the backend revision instead
of assuming a reachable production instance publishes introspection.

For a machine client, create an API key and pass it as `Authorization: Bearer KEY`.
For browser sessions, use the UI's same-origin HTTPS sign-in flow. Cookie-authenticated
mutations require the matching HTTPS Origin; machine bearer calls have a separate
credential contract. API keys cannot perform every User-only action, including managing
other API keys or changing a User's password.

## Failure responses

GraphQL authentication and authorisation failures appear in GraphQL errors; inspect
those even when the HTTP response is successful. Torznab uses XML errors, including
error 100 for required or rejected credentials. Import, metrics, and profiling return
HTTP 401 when the Identity does not have the required Permission. An authorised import
with a missing or incorrect content type returns 415.

See [authentication](../authentication/), [Servarr](../../guides/servarr/), and
[importing](../../guides/import/) for complete client workflows.
