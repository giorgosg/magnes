# bitmagnet — working notes

Reference for hacking on [bitmagnet](https://github.com/bitmagnet-io/bitmagnet) and for
mining its ecosystem. The rules that apply to every change live one level up in
[AGENTS.md](../AGENTS.md); these pages carry the reasoning, the measurements, and the
longer examples behind them.

Upstream development is slow-moving, and a fair amount of real work has accumulated in
community forks and unmerged PRs instead. Much of what follows maps that out: where the
code is, what someone has already fixed, and what is worth picking up.

These are _working notes_. New Magnes user documentation lives in
[`website/`](../website/README.md), replacing the legacy upstream site.

## Which page, and when

| Read                                             | When                                                                                  |
| ------------------------------------------------ | ------------------------------------------------------------------------------------- |
| [hacking.md](hacking.md)                         | Making a change: toolchain, feature shape, GraphQL, migrations, testing at real scale |
| [architecture/](architecture/README.md)          | Getting oriented: what each subsystem is, how it connects, where a change goes        |
| [porting.md](porting.md)                         | Measuring, reviewing, or cherry-picking from a fork or an upstream PR                 |
| [forks/](forks/README.md)                        | Choosing which downstream repo a change should come from                              |
| [upstream-status.md](upstream-status.md)         | Sending a change upstream, or weighing whether the `next` rewrite makes it moot       |
| [integration-status.md](integration-status.md)   | Checking whether a candidate is already merged, queued, or rejected                   |
| [auth.md](auth.md)                               | Configuring or deploying authentication, or running behind a reverse proxy            |
| [exchange-file.md](exchange-file.md)             | Exchange file v1: shareable torrent records, version rules, `check` and `export`      |
| [moderation-ban-file.md](moderation-ban-file.md) | Frozen shareable ban-file format, provenance and membership privacy limit             |
| [moderation.md](moderation.md)                   | Granting moderator Permissions and reviewing private evidence and decisions           |
| [adr/](adr/)                                     | A decision constrains future changes and the reason will not be obvious from the code |
| `agents/`                                        | Filing an issue, applying a triage label, or looking for domain docs — **local only** |
| `issues/`                                        | Review findings: what is confirmed, fixed, or rejected, and why — **local only**      |
| `ideas/`                                         | Weighing what to work on next rather than how to do it — **local only**               |

## Keeping these pages true

These pages are part of the change that affects them, not follow-up work. AGENTS.md
carries the rule and the table of which page a given change writes back to; the short
version is that a merged PR adds its row to [integration-status.md](integration-status.md),
and anything that moves a default, a boundary, or a decision updates the page that
describes it in the same PR.

## Snapshots expire

The fork survey, the upstream survey, and every divergence count on these pages are
**snapshots**, taken 2026-08-18 against upstream `main` at `e31b30d` (2026-05-21). The
figures were patch-equivalence based and accurate that day; the active forks move.
Re-measure with the commands in [porting.md](porting.md) before acting on a number.

Pages carrying snapshots say so at the top, with their date.
