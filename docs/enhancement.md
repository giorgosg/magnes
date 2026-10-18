# Metadata enhancement

`bitmagnet enhancement evaluate` reads selected visible torrents, evaluates their
metadata through an explicitly configured OpenAI-compatible Chat Completions endpoint,
and prints one JSON report. It does not apply metadata or save evaluation state.
Automatic enhancement ships disabled. Evaluation starts no workers, even when automatic
processing is configured for other invocations.

Configure the existing `postgres` and `tmdb` sections as usual. The database must
already be migrated. This command opens a separate PostgreSQL connection with
`default_transaction_read_only=on`, loads evidence in a read-only repeatable-read
transaction, rolls it back, and then evaluates outside the transaction. It never
resolves the application's migration-decorated database or starts workers. Pending
quarantined and banned hashes are excluded from explicit selections and samples.

For example, with the secret supplied in the named environment variable:

```bash
bitmagnet enhancement evaluate \
  --hash 0123456789abcdef0123456789abcdef01234567 \
  --base-url https://your-provider.example/v1 \
  --model your-model \
  --api-key-env LLM_API_KEY \
  --run-max-cost 0.25 \
  --input-cost-per-million 1 \
  --output-cost-per-million 2 > evaluation.json
```

The prices above illustrate configuration, not a provider price quote. Supply
conservative prices for the chosen model, including billed reasoning output. The
positive run spending ceiling and both positive token prices are required. The
currency unit is the unit used for those prices. Cost is a token-price estimate;
it does not include other provider fees. There is no provider/model fallback.
The model must support Chat Completions function tools. `output_token_parameter`
(default `max_completion_tokens`) selects the explicit request cap field; set
`--output-token-parameter max_tokens` for providers that require that spelling.
Both spellings must bound all billed output, including reasoning, for the chosen
provider/model. There is no automatic parameter retry or fallback.
Provider incompatibility is reported as a failure. Compatible reasoning replay fields
remain in memory only for the bounded conversation and are not returned in reports.
`--thinking disabled` or `--thinking enabled` sets reasoning explicitly; the default
omits it. `--reasoning-effort low`, `high` or `max` requests an effort and cannot be
combined with `--thinking disabled`. `--reasoning-parameter` says how both are sent:
`thinking` (the default, DeepSeek's API) sends `thinking.type` and `reasoning_effort`;
`reasoning` (OpenRouter) sends its normalized `reasoning` object, `enabled` or `effort`.
OpenRouter ignores DeepSeek's `thinking` field, so through OpenRouter use `reasoning`.
OpenRouter's `reasoning_details` are replayed on later turns like `reasoning_content`.

On OpenRouter, repeat `--provider-order` (setting `provider_order`) to pin providers by
slug, for example `deepseek`. Requests then forbid fallbacks and require every sent
parameter to be supported. Without it OpenRouter chooses among all hosts of the model,
and hosts of the same model ID can answer quite differently. Effort, the reasoning
parameter and the provider order are part of the recipe.

For DeepSeek through OpenRouter:

```bash
bitmagnet enhancement evaluate --sample 10 --base-url https://openrouter.ai/api/v1 \
  --model deepseek/deepseek-v4.1-flash --api-key-env OPENROUTER_API_KEY \
  --output-token-parameter max_tokens --reasoning-parameter reasoning --thinking disabled \
  --provider-order deepseek --run-max-cost 0.05 \
  --input-cost-per-million 0.3 --output-cost-per-million 1.2
```

Repeat `--hash` for explicit selections, or use `--sample 10` for a deterministic
sample ordered by infohash. Choose one selection mode, with at most 100 torrents.
An explicit experiment always runs again: no saved fingerprint or previous answer
suppresses it. `--model`, `--base-url`, `--temperature`, and `--prompt-file` override
configuration for comparison. A prompt file replaces the configured operator
instructions for that run; like them, it adds to the built-in instructions and cannot
relax local output validation. Save reports yourself when comparing experiments.
Application shutdown, including SIGINT handled by fx, cancels this command's
in-flight HTTP work and waits for its bounded result reporting; canceled work
cannot become an accepted late result.

All settings also live under the `enhancement` configuration section, using the
usual snake-case configuration names. Defaults are:

| Setting                   | Default                  |
| ------------------------- | ------------------------ |
| `concurrency`             | 2 evaluations            |
| `max_requests`            | 4 per evaluation         |
| `max_tool_calls`          | 6 per evaluation         |
| `timeout`                 | 2 minutes per evaluation |
| `request_output_tokens`   | 2,000 per request        |
| `max_output_tokens`       | 8,000 per evaluation     |
| `max_run_requests`        | 40 per command           |
| `max_run_output_tokens`   | 80,000 per command       |
| `run_timeout`             | 10 minutes per command   |
| `max_run_cost`            | No default; required     |
| `input_cost_per_million`  | No default; required     |
| `output_cost_per_million` | No default; required     |

CLI run-limit flags use `--run-max-requests`, `--run-max-output-tokens`,
`--run-timeout`, and `--run-max-cost`; evaluation flags use the corresponding
hyphenated setting names. `base_url`, `api_key`, `model`, `temperature`, `thinking`,
`reasoning_effort`, `reasoning_parameter`, `provider_order`, `prompt` and `tags`
configure the provider, reasoning, routing, additional instructions and tag whitelist.
By default the whitelist permits `llm-platform-windows`, `llm-platform-linux`,
`llm-platform-macos`, `llm-platform-android`, `llm-platform-ios`,
`llm-edition-deluxe`, and `llm-edition-collectors`. Tags must use lowercase
kebab-case, the `llm-` prefix and at most 30 characters.

The model's rules live in `internal/enhancement/instructions.txt` and are part of the
recipe revision. They require a fresh `search_works` call before any work ID, abstain on
competing regional versions and unresolved year conflicts, treat a one-year
title/catalogue difference as a soft mismatch, and accept release languages only from
explicit audio or subtitle markers. `prompt` is empty by default; when set, it is sent
after these rules and the tag list as additional operator instructions. The rules, tag
list and any operator instructions are the same for every request, so providers can
cache them as one prefix.

Spending, request and output reservations are shared by concurrent evaluations in
this command only. They do **not** enter a database-backed shared budget ledger.
Running several commands multiplies these allowances. Each request reserves its
maximum output and a conservative input allowance before contacting the provider.
Successful responses require explicit nonnegative `prompt_tokens` and
`completion_tokens` counters before reconciling reservations. Missing/null counters
are provider failures, rather than zero-token completions. Unknown usage
retains the full reservation and sets `usage.estimated`; its cost is an upper-bound
estimate of both input and output tokens and their cost, including reservations
from a later failed turn after earlier billed calls. `completion_tokens` includes reasoning, so reasoning is reported separately
and is not added twice. Transient transport/429/5xx failures use backoff and at most
three attempts total; malformed output (including invalid tool names/arguments) gets at most one retry
within that ceiling. Tool batches are validated before any catalogue call.
Every retry consumes request, token, spending and time limits.

Both read-only experiments and persistent evaluation use the durable ordinary snapshot
when present, including inferred title/year, without reading prior enhancement as ordinary
classification. Legacy rows without a snapshot use existing classification. Both paths
select the same representative files; read-only loading remains bounded and performs no
state writes.

Evidence contains the name, size/count, ordinary classification, supplied hints and
at most twenty representative stored paths/sizes, favoring large relevant files and
directory diversity. Missing/truncated lists are explicitly marked. Names and paths
are bounded; content bytes and NFO text are never available. Each request, including
tool definitions and accumulated search summaries, is limited to 32,000 UTF-8 bytes.
A token is in practice never shorter than a byte, so this also bounds input tokens without assuming the
runtime model's tokenizer; measured on DeepSeek it is about 9,500 tokens. A conversation
that would exceed it ends `limited` without sending the request.

The only model-facing tool is `search_works(title, type, year?)`, backed by TMDB's
existing movie/TV search client. It returns at most ten short summaries and only
movie or series IDs. A TV episode/season filename year is not automatically used as
a premiere filter. Search summaries omit original language, and work language never
becomes a release-track language automatically. The model may refine searches within
the limits. A selected identity must have been retrieved in this evaluation; mixed
works cannot attach one identity. Unknown fields, invalid enums/ranges, invented IDs,
confidence/justification fields and non-whitelisted tags fail local validation.

The report contains model, accounting scope, each infohash, latency, usage, optional
cost and one outcome:

- `accepted`: validated, nonempty metadata for inspection.
- `unresolved`: a completed empty metadata answer.
- `limited`: request, token, tool, spending, input, cancellation or deadline limit.
- `malformed`: invalid structured output after its permitted retry.
- `provider_failure`: transport, authentication, catalogue or usage failure.

Accepted metadata is still a model inference for manual evaluation, not proof of
correctness. This command supplies the engine for later quality measurements; no
live model-quality claim follows from deterministic tests passing.

The transport follows the official [Chat Completions API](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
, the [DeepSeek compatible parameter reference](https://api-docs.deepseek.com/api/create-chat-completion/),
and TMDB's [search workflow](https://developer.themoviedb.org/docs/search-and-query-for-details).

## Durable application for internal callers

`enhancement.Application.Enhance` is the explicit single-torrent service entry point.
It requires a real completed ordinary classification for current evidence, runs the
bounded evaluator, fetches selected TMDB work details outside the application transaction,
and rechecks freshness, authority and moderation before writing. The application
transaction follows the original run deadline and cancellation through lock waits,
regardless of the completion caller context, and checks again before committing. It can also be invoked by the separately registered automatic worker. The bounded `enhancement process` command uses this application through shared scheduler
admission and accounting. `enhancement evaluate` remains read-only with this storage present.

Ordinary enhancement may correct unsourced rule results. Importer hints and existing
catalogue identity are protected; a type-only hint permits adding a compatible absent
catalogue identity. This includes a work attached by an earlier enhancement: force
alone cannot replace or drop it, and a later fields-only result retains it. The retained
identity lives with accepted state so reconstruction also works after search-row deletion.
Newly supplied hint type/source/ID takes precedence over incompatible retained
identity. A compatible type-only hint still permits retaining or filling a missing ID.
An explicit rematch request may replace supplied values or remove a work match.
Accepted authority remembers the hint identity fields it superseded, so an unchanged
hint does not undo that rematch; changing those fields establishes new supplied authority.
Stored hint title/year now participate in ordinary classification. Accepted inferred
title/year remains searchable and displayable without a catalogue match. Catalogue
values take precedence in display, Release Year facets and Torznab. Original torrent
names remain available and are retained in magnet names.

Accepted results replay during ordinary reconstruction only while their evidence is
valid, including ordinary inferred title/year. Changing those ordinary values invalidates
prior output and unchanged-input suppression; replayed enhancement values never become
their own ordinary evidence. Catalogue identity remains protected when changed ordinary
evidence invalidates release-level output; explicit ordinary rematch can establish new
identity, including an absent match, independently of whether ordinary evidence changes.
Valid release fields still replay, but cannot restore the prior identity proposal. When
changed evidence invalidates release fields, reconstruction rebuilds the search vector
from the retained catalogue work. Failed/malformed later attempts retain valid accepted
output. Internal explicit callers may retry; automatic processing suppresses exhausted
provider/malformed attempts for unchanged evidence and recipe until explicit resume.
Completed unresolved and limited results suppress unchanged input/recipe; explicit force,
rematch or a changed recipe/evidence can reconsider them. These records are backed up
with the database; catalogue exports omit evaluation/recipe internals. A search-vector
rebuild does not count as evaluation. Existing torrents acquire ordinary completion
records when genuinely classified, never from a fabricated migration backfill.

Recipes store sanitized canonical JSON excluding API credentials, prices and operational
concurrency/run bounds. They include the requested endpoint/model/generation settings,
prompt, controlled tags, implementation revision and ordinary workflow recipe. Hosted
model aliases may change behind the same requested recipe. The reserved generated tag
set is replaced atomically while user/importer tags outside it survive. No year tags
are generated. Automatic processing remains disabled by default; migrating the database enables no calls.

## Automatic worker and shared spending

The registered `enhancement` worker only runs inference when `enhancement.automatic`
is explicitly true. It requires a positive `daily_budget`, `input_cost_per_million`
and `output_cost_per_million`, together with the endpoint, key and model. Prices and
budget use the same operator-chosen currency. No model or price fallback is selected.
`worker run --all` includes this worker; `worker run --keys=enhancement` selects it alone.
Keep automatic processing disabled until manually reviewed evaluation evidence supports
its use. Deterministic tests do not measure model quality.

```yaml
enhancement:
  automatic: false
  daily_budget: 0
  concurrency: 2
  poll_interval: 5s
  reconsider_interval: 24h
```

Ordinary classification transactionally marks the torrent ready after successful
persistence. Moderation early stops never hand off work. A migration marks existing
classified rows eligible without inventing ordinary completion records; selected
legacy rows go through real ordinary classification before inference. Populated
metadata and existing catalogue matches remain eligible, with the existing authority
checks unchanged.

Admission alternates slots between discovery within seven days and older discovery.
Each group orders the maximum reported seeder count across sources descending,
unknown last, then oldest instance discovery and infohash. Empty groups lend slots.
Source writes refresh the maximum, including deletion of a source. A bounded boundary
refresh moves expired recent rows to the older group in pages of 64 per poll. Admission
waits for boundary catch-up to finish, so an incomplete older group cannot admit lower
ranked work first. A large simultaneous crossing can therefore pause admission. Queue notifications
cannot select enhancement work. The shared claim count enforces concurrency across
processes; configure the same concurrency and budget on all instances sharing a database.

Selection uses an indexed readiness hint on the torrent, without a job per catalogue
row. Checked and hidden rows leave the ranking index. Selection examines at most 64
ranked candidates per group; a hidden prefix is discarded page by page. Up to 64 rows whose last check is older
than `reconsider_interval` reenter per poll, so changed recipes and evidence are
reconsidered gradually. Persistent evaluations suppress unchanged accepted, unresolved
and limited work even after queue cleanup. Exhausted provider and malformed attempts
are also suppressed automatically for unchanged evidence/recipe. Reprocessing makes
new ordinary evidence ready immediately. Low ranked work has no exhaustive coverage
promise.

Every model request, including retries, first reserves its configured worst-case price
for 32,000 input tokens, the request bound, and the requested remaining billed output
limit. Reservation and budget comparison serialize across processes. Known billed
usage reconciles the reservation exactly once; completion output includes reasoning,
without double counting reasoning already included by the provider. Missing usage, timeouts and crashes retain
the full charge. Configured prices must cover the actual provider's billed token rates.
A provider exceeding its advertised token ceiling can exceed a reservation; further
requests stop when local limits detect it. No client can prevent an upstream provider
from violating its requested cap.

Daily allowances use UTC dates. An in-flight reservation remains charged to the day
on which it was admitted; settlement after midnight adjusts that original day. A new
day gets its own allowance. Unknown reservations are never refunded on restart.
`enhancement_daily_spending` holds totals and `enhancement_reservations` holds only
live request tokens. Terminal responses retire their tokens even when usage is unknown,
without refunding the daily charge. Crash-orphaned tokens expire five seconds after
claim expiry and are reclaimed in indexed pages of 64 during admission. Known late
usage can reconcile exactly once within that settlement grace; expired tokens cannot
refund charges. Accepted evaluation state is independent of both.

Active claims expire after three evaluation timeouts, allowing ordinary preparation
and the bounded inference run. An interrupted claim with no request becomes ready
again. Once any request was reserved, recovery records `limited` for that generation
and input/recipe, retaining accepted output and charges. A late worker cannot apply
metadata or release a successor's claim. Its deadline is bounded by the stored lease
expiry, and expired claims remain for conservative recovery. Completion preserves any
ordinary handoff recorded after admission, including one concurrent with result persistence. Model requests retain the evaluator's
configured request/tool/token/deadline limits, three total transient attempts and one
malformed retry; the scheduler adds no queue retry layer. A budget stop before the
first request defers work. After any paid request, a budget stop completes the attempt
as limited. If final persistence fails, conservative recovery records that paid attempt
as limited before releasing its claim, preserving the effective caps.

HTTP 400/401/403/404/422 model failures durably pause further automatic admission.
Catalogue authentication and configuration failures also pause admission, whether
encountered during search or detail retrieval; missing catalogue works do not pause it.
Error classification retains only a safe failure kind, without provider response bodies
or credentials.
Restarting the process does not clear the pause. Correct the endpoint/key/model first;
run `bitmagnet enhancement resume`, which clears the pause
and explicitly readmits failed/malformed attempts. It does not enable automation or
start workers. To stop calls now, disable the worker and stop its running process;
shutdown cancels in-flight contexts and waits for handlers. `enhancement evaluate`
never starts this worker or uses its ledger, even when automatic configuration is enabled.

## Operator commands and recovery

All commands print one JSON document to stdout; diagnostic logs go to stderr. Keep
reports private when names, stored paths or accepted output identify sensitive material.
No command accepts results submitted by a remote model or exposes an HTTP surface.

```bash
# No provider calls or database writes; inspect at most 100 visible torrents.
bitmagnet enhancement preview --sample 10
bitmagnet enhancement status --hash 0123456789abcdef0123456789abcdef01234567

# Explicit writes and paid calls: endpoint/key/model/prices, daily_budget and
# positive max_run_cost must already be configured under enhancement.
bitmagnet enhancement process --hash 0123456789abcdef0123456789abcdef01234567
bitmagnet enhancement process --hash 0123456789abcdef0123456789abcdef01234567 --force
bitmagnet enhancement process --hash 0123456789abcdef0123456789abcdef01234567 --rematch

# After correcting authentication or provider configuration:
bitmagnet enhancement resume
```

`preview` and `status` read the same bounded explicit hashes or deterministic sample.
They show evidence, latest completed and attempted evaluations, retained accepted
output, automatic configuration, persistent pause and the latest seven UTC spending
totals. These are snapshots, not reservations or a promise of admission. An
`ordinary_required` item needs real ordinary classification before enhancement;
`eligible` has no unchanged completion suppressing this recipe; `unchanged_completed`
needs force, rematch, changed evidence or a changed recipe to run again.
`exhausted_failure` needs explicit resume, force or rematch (or changed evidence/recipe).
A failed attempt can coexist with a prior accepted completion and retained output.
The ledger stores charged currency totals, including pessimistic unknown usage;
it is not a per-torrent token or latency history. Current process/evaluate reports
contain per-attempt requests, tools, tokens, estimated/known usage, cost and latency.

`process` requires explicit hashes (repeatable, at most 100); it never applies a
sample implicitly. It runs selected torrents sequentially within one command deadline
and shared process-local request/output/spending caps. Its requests additionally reserve
against the same UTC daily ledger and claim capacity as automatic workers on every
process. Persistent processing uses configured shared concurrency; command-line
`--concurrency` overrides are available only for read-only experiments.
`automatic: false` permits these explicit requests but starts no worker.
Positive daily budget, token prices and model configuration are still required.
Moderation, current ordinary evidence, importer authority, deadline and late-write
checks remain enforced. Older legacy evidence may first undergo ordinary processing.
Capacity exhaustion returns `not_admitted`; retry once active owners finish or expire.
A persistent pause also prevents manual calls until explicit resume. Budget denial
before any request is `deferred`; it is not an unresolved identification.

`--force` retries unchanged evaluations, including `limited` results, without granting
permission to replace importer-supplied fields or an existing work. `--rematch` grants
that additional authority and can replace or remove an attached work. Ordinary
reconstruction reuses valid retained output; reindexing does not re-evaluate it.
Failure, cancellation, or stale output cannot overwrite a newer result. `not_completed`
reports preparation/application failure separately from a successful model answer.
Read the returned error and inspect state rather than assuming a paid answer was applied.

An accepted or unresolved evaluation is completed. `limited` means the configured
investigation could not finish; unchanged limited results suppress automatic repeats
so restarts cannot silently buy fresh caps. A provider or malformed failure retains
previous accepted output and records the failed attempt separately. Authentication
and configuration failures pause admission durably; restarting does not resume it.
Run `resume` after fixing configuration: it clears the pause and readmits exhausted
provider/malformed attempts, but does not reset daily spending or automatically retry
unchanged completed limited results. Paid interrupted claims recover conservatively
as limited; unknown charges remain charged.

Before automatic rollout, compare saved read-only reports on a reviewed corpus with
clear releases, translations, remakes, ambiguous names, episodes, season packs and
mixed works. Verify work IDs independently; existing automatic matches can be wrong.
Record recipe and configuration identifiers, expected IDs/abstention, field errors,
tool calls, retries/failures, latency, billed reasoning/output and cost per 1,000 with
the sample mix and uncertainty. Review every wrong attachment. Valid JSON and passing
fake-provider tests do not establish identification quality. The recipe in an evaluation
report is sanitized and excludes credentials. Provider model aliases can change.

Start with a small explicit selection and a conservative run allowance. Inspect both
results and charged daily spend before expanding it. Deliberately record prompt/model
changes, which change the recipe and allow gradual reconsideration of old evidence.
Approve measured quality separately before setting `automatic: true` with a positive
daily budget. To pause new automatic work, set `automatic: false` and stop/restart the
worker process; stopping cancels in-flight calls and waits for bounded cleanup.
Keep automation disabled during recovery, correct configuration, inspect retained state
and charges, then resume explicitly. Never delete the ledger or fingerprints to recover
from a provider failure. No model, price, acceptance threshold or rollout approval is
implied by the examples in this guide.
