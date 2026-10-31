# Testing

## A behaviour change ships with a test seen red

An observable behaviour change ships with a test that was watched failing against the
unfixed code. Reproduce first, apply the smallest fix, then run the focused test and the
suite around it. A test written after the fix and never seen red proves nothing. When a
port breaks an existing test, fix the port and keep the test as strict as it was.
Refactors and docs-only changes need no new test.

This bar was earned: cherry-picks in this repo have compiled, vetted and passed the whole
suite while carrying a shutdown deadlock and a cache that answered the wrong question.
`dhtcrawler`, `importer` and `processor` have little coverage, so a change there gets a
focused regression test or a standalone reproduction.

## A green run can be an empty one

PostgreSQL tests use `dbtest.New(t)`, which creates and drops an isolated, fully migrated
database per test. They **skip** when `TEST_POSTGRES_DSN` is unset, so a bare
`go test ./...` stays offline and proves less than it appears to. CI supplies a
PostgreSQL 16 service.

```bash
TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/postgres' go test ./...
```

Tests that need a populated index use `dbtest.NewSeeded(t)`, which clones the local
btm-testdb fixture template and **skips** unless `TEST_POSTGRES_TEMPLATE_DSN` names that
instance. A checkout without fixtures still runs everything else.

Report every check that skipped or could not run, and why.

- Both `dbtest` paths in detail: [architecture/data.md](../architecture/data.md#testing-against-a-real-database)
- Fixture-scale tests and driving a real instance: [hacking.md](../hacking.md#testing-at-realistic-scale)
