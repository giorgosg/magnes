# Branches and imported commits

- **`main` is a pristine mirror of `upstream/main`.** Leave it untouched so
  `git merge --ff-only upstream/main` always works.
- **`trunk` is this fork's integration branch.** Fork features, fixes, docs and accepted
  patches from forks and upstream PRs all land here through reviewed PRs.
- **Cut every topic branch from `trunk`.** When a change is headed for an upstream PR,
  rebase it onto `main` at that point so the upstream PR carries only that change;
  [upstream-status.md](../upstream-status.md) covers whether sending it is worthwhile.
- **Take outside commits with `git cherry-pick -x`**, which records provenance.

When to commit, push and merge is in the root [AGENTS.md](../../AGENTS.md#every-task).

## Imported commits are candidates

A fork's commit message describes intent, not behaviour, and `perf:` has been observed on
changes that alter semantics. Read the whole substantive diff, verify the claimed
behaviour, and give it a test seen red ([testing.md](testing.md)).
[porting.md](../porting.md) has the commands that make that diff readable, and
[integration-status.md](../integration-status.md) is where the outcome is recorded.
