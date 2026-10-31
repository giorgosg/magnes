# Working on Magnes

Magnes is a fork of bitmagnet — a self-hosted BitTorrent DHT crawler, classifier and
search engine in Go with a GraphQL API — that bundles its own Elm UI from `webui/`.

This file is the entry point for every agent and human. `CLAUDE.md` and other harness
files only point here: add an instruction to this file or to a page it links, never to a
harness file.

## Every task

- Start with `git status --short` and `git branch --show-current`, and leave unrelated and
  untracked work (`.scratch/`, `*.local.md`) as you found it.
- Read `CLAUDE.local.md` or `AGENTS.local.md` at the root if present: it holds this
  checkout's environment and any standing authority. `*.local.md` is gitignored at any
  depth; keep every such file, and the local-only paths in the table below, out of
  tracked files and links.
- Commit, push and merge when the user asks, or under standing authority recorded in a
  `*.local.md` file. Absent both, stop before merging and say so.
- Cut topic branches from `trunk`. `main` is a pristine mirror of `upstream/main`.
- Generated files change only through their source and generator: `*.gen.go`,
  `internal/gql/enums/enums.go`, `*.pb.go`, `mocks/`, `schemas/classifier-0.1.json`.
  Editing the output passes locally and is reverted by the next `task gen`.
- A behaviour change ships with a test seen **red** against the unfixed code.
- A PR updates every doc page it makes wrong, in the same PR.

## Commands

Tasks run through go-task ([`Taskfile.yml`](Taskfile.yml)); the toolchain comes from
`nix develop`. These are the commands whose behaviour the Taskfile does not tell you:

```bash
task gen                                      # every generator; narrow targets in docs/rules/generated-code.md
git diff --exit-code                          # after any generator: CI's clean-tree check
./node_modules/.bin/prettier --write <files>  # pinned 3.3.3; Markdown is linted too
nix develop --command task lint               # golangci-lint, elm-format + elm-review, Prettier
# Without the DSN every database test skips silently and the run still looks green:
TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/postgres' go test ./...
```

Before handing off, run the checks covering the touched area and report every check that
skipped or could not run, and why.

## Read when

| Read                                                                                       | When                                                                                           |
| ------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------- |
| [docs/architecture/](docs/architecture/README.md)                                          | Starting cold, or entering a subsystem you have not touched: what it does, where a change goes |
| [docs/architecture/conventions.md](docs/architecture/conventions.md)                       | Adding a subsystem, a GraphQL field, or any new HTTP, GraphQL or Torznab surface               |
| [docs/rules/generated-code.md](docs/rules/generated-code.md)                               | Changing a generated file's source: which generator, which task                                |
| [docs/rules/linting.md](docs/rules/linting.md)                                             | Lint fails, or you are choosing which Prettier or golangci-lint to run                         |
| [docs/rules/migrations.md](docs/rules/migrations.md)                                       | Adding, renumbering or cherry-picking a migration                                              |
| [docs/rules/testing.md](docs/rules/testing.md)                                             | Writing a test, or deciding whether a green run proved anything                                |
| [docs/rules/documentation.md](docs/rules/documentation.md)                                 | Opening a PR: which pages it must update                                                       |
| [docs/rules/branches.md](docs/rules/branches.md)                                           | Cutting a branch, preparing an upstream PR, or taking a commit from a fork                     |
| [docs/hacking.md](docs/hacking.md)                                                         | Toolchain setup, building the bundled UI, CI behaviour, testing at realistic scale             |
| [docs/porting.md](docs/porting.md)                                                         | Measuring, reviewing or cherry-picking from a fork or an upstream PR                           |
| [docs/forks/](docs/forks/README.md)                                                        | Choosing which downstream repo a change should come from                                       |
| [docs/upstream-status.md](docs/upstream-status.md)                                         | Sending a change upstream, or weighing whether the `next` rewrite makes it moot                |
| [docs/integration-status.md](docs/integration-status.md)                                   | Checking whether a candidate is already merged, queued or rejected                             |
| [docs/auth.md](docs/auth.md)                                                               | Configuring or deploying authentication, or running behind a reverse proxy                     |
| [docs/moderation.md](docs/moderation.md)                                                   | Moderator permissions, private evidence and decisions                                          |
| [docs/moderation-ban-file.md](docs/moderation-ban-file.md)                                 | The frozen ban-file format, its provenance and membership privacy limit                        |
| [docs/exchange-file.md](docs/exchange-file.md)                                             | Exchange file v1: shareable torrent records, `check`, `export` (full and `--since`), `import`  |
| [docs/enhancement.md](docs/enhancement.md)                                                 | LLM metadata enhancement and `bitmagnet enhancement evaluate`                                  |
| [docs/adr/](docs/adr/)                                                                     | Reversing a past decision, or making one whose reason the code will not show                   |
| [CONTEXT.md](CONTEXT.md), [GLOSSARY.md](GLOSSARY.md), [webui/CONTEXT.md](webui/CONTEXT.md) | Naming a domain concept: principals and permissions, catalogue terms, UI identity language     |
| [webui/docs/](webui/docs/README.md), [elm-development.md](webui/docs/elm-development.md)   | Elm UI work, its generated client, browser tests; the second before designing any Elm feature  |
| `docs/agents/`                                                                             | Filing an issue, applying a triage label, or looking for domain docs — **local only**          |
| `docs/issues/`                                                                             | Review findings: what is confirmed, fixed or rejected, and why — **local only**                |
| `docs/ideas/`                                                                              | Weighing what to work on next rather than how — **local only**                                 |

Pages carrying fork, upstream or divergence counts are dated **snapshots**; re-measure
with [docs/porting.md](docs/porting.md) before acting on a number.
