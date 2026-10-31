# bitmagnet — working notes

Reference for hacking on [bitmagnet](https://github.com/bitmagnet-io/bitmagnet) and for
mining its ecosystem. The rules that apply to every change are linked from
[AGENTS.md](../AGENTS.md); these pages carry the reasoning, the measurements, and the
longer examples behind them.

Upstream development is slow-moving, and a fair amount of real work has accumulated in
community forks and unmerged PRs instead. Much of what follows maps that out: where the
code is, what someone has already fixed, and what is worth picking up.

These are _working notes_. New Magnes user documentation lives in
[`website/`](../website/README.md), replacing the legacy upstream site.

## Which page, and when

The _Read when_ table in the root [AGENTS.md](../AGENTS.md#read-when) is the one index of
these pages, kept there so an agent sees every trigger without opening this file. A new
page adds its row there. The rules that apply to every change live under
[rules/](rules/).

## Keeping these pages true

These pages are part of the change that affects them, not follow-up work.
[rules/documentation.md](rules/documentation.md) says which page a given change writes
back to.

## Snapshots expire

The fork survey, the upstream survey, and every divergence count on these pages are
**snapshots**, taken 2026-08-18 against upstream `main` at `e31b30d` (2026-05-21). The
figures were patch-equivalence based and accurate that day; the active forks move.
Re-measure with the commands in [porting.md](porting.md) before acting on a number.

Pages carrying snapshots say so at the top, with their date.
