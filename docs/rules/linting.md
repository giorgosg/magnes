# Linting and formatting

CI runs `task lint` inside `nix develop`, and `task lint` is three tasks:
`lint-golangci`, `lint-ui` (elm-format and elm-review) and `lint-prettier`. Passing one
of them proves a third of what CI checks.

## Prettier is pinned, and it lints Markdown

`.prettierignore` does not exclude `*.md`, so a docs change is linted like code.
Formatting differs between 3.x releases, so a stray version passes locally and fails CI.

Run the repo's own copy: exact version `3.3.3` in the root `package.json`, installed by
`task install-tools`. `task lint-prettier` calls it by path for this reason, and it is
deliberately absent from the Nix shell so nothing else wins on `PATH`. `npx prettier@3`
resolves to a later 3.x and is the specific mistake to avoid.

```bash
./node_modules/.bin/prettier --write AGENTS.md 'docs/**/*.md'
```

## golangci-lint comes from the Nix shell

The linter has to be built with a Go at least as new as the toolchain compiling the code,
so `flake.nix` pins both together and CI takes its linter from `nix develop` rather than
installing one. A mismatch gives no tidy error: too old and it refuses the module, too
new for the compiler and it panics mid-run. Version strings do not settle it — a
`go install`ed binary reports the same version as the official release while being built
with a different Go.

`task lint-golangci` must end in `0 issues.` A clean tree reports zero, so every finding
is yours.
