# Documentation lands with the change

A merged PR updates `docs/` wherever the change makes a page wrong or incomplete, in the
same PR. A ledger that stops mid-history, or an architecture page describing code that
moved, is worse than no page at all, because it is read as current.

| If the change…                                        | Update                                                                                    |
| ----------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| is merged at all                                      | [integration-status.md](../integration-status.md) — one row, with what was seen red       |
| alters a default, a config key, or a deployment       | the operator page for it, e.g. [auth.md](../auth.md)                                      |
| moves a subsystem boundary, or adds or removes one    | the matching page under [architecture/](../architecture/README.md)                        |
| settles a decision that constrains later work         | a new record in [adr/](../adr/)                                                           |
| changes the toolchain, a generator, or the test setup | [hacking.md](../hacking.md), and the matching page in this folder if it is a trap         |
| adds a doc page                                       | its row in the root [AGENTS.md](../../AGENTS.md) _Read when_ table                        |
| rejects a candidate, or kills one with a measurement  | [integration-status.md](../integration-status.md) — the rejected section, with the number |
| resolves or reprioritises a review finding            | `docs/issues/` if the checkout has it — local, and stays untracked                        |

A pure refactor with no visible effect usually touches nothing. Answer the question
anyway: say in the PR body which pages you checked and why none needed changing. A
measurement that rejects an idea is worth recording precisely because the next reader
would otherwise repeat it.
