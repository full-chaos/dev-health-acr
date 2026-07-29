# Full-stack Context Fabric acceptance (CHAOS-3065)

Status: **Implementation contract** — authoritative for `scripts/e2e/fullstack-opencode.sh`,
`testdata/fullstack/v1/`, and `tests/fullstack/`.

TRD: *Automated full-stack acceptance testing for Context Fabric*.

## 1. What this gate proves

```text
versioned fixture corpus
  -> Dev Health PostgreSQL/ClickHouse + agent_context_runtime entitlement
  -> acr-api context assembly and evidence expansion
  -> host-local acr-mcp STDIO tools
  -> real headless OpenCode tool invocation
  -> strict JSON, evidence-backed final output
  -> Context Packet Explorer agreement (live web)
```

It is **not** a model benchmark. The required CI path uses a deterministic scripted
OpenAI-compatible model so acceptance never depends on probabilistic generation.

## 2. Relationship to existing suites

| Suite | Covers | Still valid |
| --- | --- | --- |
| `scripts/e2e/compose.sh` | isolated Compose lifecycle, migrations, credentials, ACL, direct MCP STDIO | yes |
| `scripts/e2e/svs.sh` (CHAOS-2914) | API/MCP/browser cross-surface agreement, fail-closed negatives | yes |
| `scripts/clients/test-real-clients.sh` (CHAOS-3010) | client install, registration, lifecycle, safety | yes |
| **`scripts/e2e/fullstack-opencode.sh` (this)** | **a real OpenCode session driving the live stack, verified against a versioned oracle** | new |

None of the existing suites may be relabelled as this gate.

## 3. Layout

```text
scripts/e2e/fullstack-opencode.sh        # orchestrator; sources compose.sh
testdata/fullstack/v1/
  fixture-manifest.json                  # identities, timestamps, row-count probes, corpus hashes
  tasks.json                             # full-stack task definitions
  seed/clickhouse/*.sql                  # deterministic projection of the evaluation corpus
  schema/context_fabric_agent_result.v1.schema.json
  expected/task-*.oracle.json
tests/fullstack/
  modeloracle/                           # deterministic OpenAI-compatible scripted model
  assertrun/                             # Go assertion + JUnit + artifact tool
  sidecarmd/                             # reader for the sidecar's markdown renderings (shared by both)
  opencode/opencode.json.template
  opencode/task-prompt.md
.tmp/fullstack/<run-id>/                 # sanitized artifacts (gitignored)
```

`context_fabric_agent_result.v1` is a **harness-owned** schema under `testdata/`, not a
product wire contract. It never enters `contracts/` and is not subject to the
contract-first rule, because ACR does not produce or consume it — the agent does.

## 4. Fixture design

### 4.1 Canonical source

`testdata/evaluation/v1` (CHAOS-2918) stays the source of truth for scenario semantics.
`testdata/fullstack/v1/seed/clickhouse` is its **projection** into the current Dev Health
ClickHouse schema. The manifest records the SHA-256 of every consumed corpus file, so a
corpus edit that is not reflected in the projection fails preflight.

Fixed identities (no generated UUIDs, no `now()`):

| Entity | Value |
| --- | --- |
| repository slug | `example-org/widget-service` |
| repository UUID | `00000000-3065-4000-8000-000000000001` |
| default ref | `main` |
| repository `last_synced` | `2026-01-14T12:00:00.000Z` |

### 4.2 Projection targets

The assembler's source catalog (`internal/contextpacket/source_queries.go`,
`dev-health-source-catalog.v1`) determines the required tables. All are `ReplacingMergeTree`
and are read with `FINAL`.

| Corpus evidence | ClickHouse table | Key columns |
| --- | --- | --- |
| `ev-commit-checkout-001` | `git_commits` | `hash=a1b2…a1b2`, `committer_when=2026-01-13T18:42:00Z` |
| `ev-commit-checkout-001` | `git_commit_stats` | `commit_hash`, `file_path` (expandable evidence #2) |
| `ev-ci-checkout-001` | `ci_pipeline_runs` | `run_id=checkout-e2e-run-4821`, `branch=main`, `commit_hash=a1b2…` |
| `ev-commit-auth-002` | `git_commits` | `hash=b2c3…b2c3`, `committer_when=2026-01-12T09:05:00Z` |
| `ev-pr-auth-002` | `git_pull_requests` | `number=1042`, `head_branch=main` |
| `ev-pr-auth-002` | `git_pull_request_reviews` | `review_id`, `state=changes_requested` |
| repository freshness | `repos` | `org_id` bound at seed time, `ref=main`, `provider=synthetic` |

`repos.org_id` is the only value not fixed at authoring time: it is the isolated
organization UUID minted by `dev-hops admin orgs create`. The seeder substitutes it and the
manifest asserts the substitution happened exactly once per row.

ACR evidence-ref IDs are derived by the catalog, not by the corpus. The oracle stores the
**derived** form, e.g. `acr:v1:commit:a1b2…`, `acr:v1:ci:checkout-e2e-run-4821`,
`acr:v1:pull-request:1042`, and the corpus `evidence_id` alongside it for traceability.

### 4.3 Preflight

Before any client runs, the seeder must verify and record in `fixture-verification.json`:

* every migration completed (`acr.schema_migrations` and the Ops ClickHouse migration table);
* per-table row counts equal the manifest's expected counts;
* the repository resolves to exactly one row for the authenticated org (`repos FINAL` must
  return exactly 1 — 2 rows makes `ResolveEvidenceScope` fail with an ambiguity error);
* corpus file hashes match `testdata/evaluation/v1/manifest.json`;
* the target is not dirty — a pre-existing seeded row aborts the run.

## 5. Scenarios

| Task | Scope | Expected packet | Purpose |
| --- | --- | --- | --- |
| `task-001-checkout-flake-exact-commit` | `commit_sha=a1b2…` | `complete` / `exact_commit` | ≥2 expandable evidence refs |
| `task-002-auth-refactor-branch` | `branch=main` | `complete` / `branch_filtered` | branch resolution, repository-wide labeling |
| `task-003-unindexed-branch-empty` | `branch=release/1.4-unindexed` | `partial` / `branch_filtered` | visible branch gaps, no branch-specific fabrication |
| `task-004-foreign-repo-denied` | `repo=example-org/other-service` | HTTP 403 `repo_forbidden` | credential repository scope |
| `task-005-unavailable-evidence` | forged/expired evidence ref | typed `not_found`, no URL fetch | evidence boundary |

Tasks 001 and 003 are the **PR smoke** set. All five run nightly.

Every task pins `scope.as_of` to the fixture's `as_of_pin` (`2026-01-14T12:00:00.000Z`) and
leaves `time_window_days` unset. Neither field has a server-side default, so without the pin
the fixture's January 2026 timestamps pass only because time filtering is silently inactive —
which would break the moment a default appeared, or as wall-clock drift accumulated.

### 5.1 Packet-status expectations

Task-001 expects `complete` and an exact empty unavailable-source set. `incidents.v1` reads
canonical `operational_incidents` through active service-to-repository mappings for the resolved
repository; it does not query the retired `incidents` table or disclose unmapped/foreign rows.
The shared catalog projection promotes confidence to `Float64`, preserving native Float64 values
while safely widening native Float32 sources.

**Scope-aware source behavior.** Branch-filtered sources apply the requested branch.
Repository-scoped sources still execute and label returned items `repository-wide`. The commit
and commit-file sources use an exact commit when one is supplied and otherwise perform a
repository-wide read. A branch request therefore does not turn an otherwise available source
into an unavailable source merely because that source cannot apply the branch filter.

Each oracle asserts the **exact** unavailable-source set:

| Task | Unavailable sources | Why |
| --- | --- | --- |
| task-001 | 0 | commit-pinned with no branch; all seeded catalog sources are available |
| task-002 | 0 | all branch-filtered and repository-wide source families have seeded evidence |
| task-003 | 5 | only the branch-filtered source families have no rows for the unindexed branch |

Exact-set matching is what makes this useful: tasks 001 and 002 fail when any source becomes
unavailable, while task-003 pins the deliberate five-source branch gap and still proves that
repository-wide evidence remains available without becoming a branch-specific finding.

The ten background-density rows that make the exact-set assertion meaningful are explicitly
not semantic. The four evaluation-corpus evidence records remain the only required evidence.

## 6. OpenCode client

Each run builds a throwaway client root and proves the developer's own configuration is
untouched. The proof has two halves, because "nothing changed" is satisfied vacuously by a
client that never ran:

* **Negative** — a metadata fingerprint (name, mtime, size) of the configuration roots
  `~/.config/opencode` and `~/.opencode`, plus the *entry names* under
  `~/.local/share/opencode`, taken before and after. File contents are deliberately *not*
  hashed: a real installation's data directory routinely holds tens of gigabytes across
  hundreds of thousands of session files, and hashing it takes longer than the acceptance run
  itself. The session-data root contributes names only, not timestamps, because the operator
  may legitimately be using OpenCode for their own work while the suite runs; what must not
  happen is this suite creating a store of its own, and that appears as a new entry.
* **Positive** — the run asserts the client actually wrote its state *into* the throwaway
  root, so an OpenCode process that silently failed to start cannot pass the isolation check.

Two implementation notes worth keeping:

* Even `opencode --version` initialises the data directory, so the pinned-version check also
  runs inside the throwaway root. Otherwise the baseline is polluted before it is taken.
* The `stat` flavour must be probed with GNU's `-c` before BSD's `-f`. On GNU coreutils `-f`
  means *filesystem status*, so `stat -f <fmt>` succeeds while printing free-block counts —
  a BSD-first probe silently produces a digest that changes every second on Linux runners.

The client environment itself: `HOME` and `XDG_{CONFIG,DATA,CACHE,STATE}_HOME` all point
inside `.tmp/fullstack/<run-id>/`, and the client starts from a cleared environment (`env -i`)
with `--pure`.
* Its recorded OpenCode version is read only through that same throwaway environment; the
  harness never probes the operator's OpenCode installation after recording the host baseline.
* Exactly one provider (the scripted local model) and exactly one MCP server
  (`acr-mcp serve`, the locally built or released binary) are registered.
* No writeback, no automatic pre-plan, no external plugins.
* The pinned OpenCode version and headless event format are recorded in `run.json`;
  a version mismatch is a hard failure, not a warning.

Two properties of OpenCode 1.18.4 were established empirically and shape this design:

**The client does not pass its environment to a local MCP child.** A sidecar started by
OpenCode sees none of the parent's `ACR_*` variables, so the credential cannot simply be
inherited. Writing the token into `opencode.json` was rejected — it would put a bearer
credential on disk in a config file. The suite instead uses `ACR_API_TOKEN_FILE`, the
sidecar's documented credential source: only the *path* travels through JSON, and the driver
asserts the file is mode `0600` before the client starts. This is also the canonical form the
supported client packages already use.

**The built-in `openai` provider drives the Responses API, not chat/completions.** Overriding
its `baseURL` therefore does not work with a chat/completions test model. The client uses the
`@ai-sdk/openai-compatible` provider package instead, pinned to an exact version and
pre-warmed into the npm cache by CI, so the graded run never depends on a live registry. The
TRD requirement is that the gate run without customer data, external provider credentials or
a hosted LLM; a cached, version-pinned provider adapter satisfies that.

**`--model` takes config *keys*, and a wrong one fails opaquely.** The flag's
`<provider>/<model>` pair addresses the keys of the `provider` object in `opencode.json`, not
the human-readable `name` fields. Naming a provider the config does not define does not
produce a "no such model" message: OpenCode fails inside its own server with
`UnknownError: Unexpected server error` and issues *zero* model requests, which is easy to
misread as a network or provider-install fault. The driver therefore never spells the id out —
it reads `.model` back from the config it just rendered and asserts that the pair resolves
inside `.provider`, so the two can never drift. The client also runs with `--print-logs`, so
the server-side log that names the real cause is captured in the run artifacts; without it
the only evidence lives in the throwaway `HOME` that teardown removes.

**A dead MCP server is a warning, not an error.** If the sidecar fails to start, OpenCode logs
`server unavailable key=acr status=failed` at WARN and completes the session with the tools
simply absent. Nothing in the client's own exit status distinguishes that from a healthy run.
Two independent guards cover it: the scripted model refuses to answer and returns a
`fullstack_model_failure.v1` envelope naming the missing tool (which the driver surfaces as a
first-class failure), and the assertion layer requires both tool calls in the session event
stream. Either one alone would catch it; the pair means a silent degradation cannot be read as
a pass.

### 6.1 Deterministic model

`tests/fullstack/modeloracle` serves only the OpenAI-compatible subset the pinned OpenCode
version needs, and returns a fixed turn sequence:

1. tool call → `context_for_task` with the task goal and scope;
2. tool call → `source_evidence` for an ID taken from the previous **live** tool result;
3. final message → strict JSON matching `context_fabric_agent_result.v1`.

Step 2's argument is extracted from the real MCP response, so a broken ACR read path still
fails the run. The model service never fabricates packet or evidence content.

**What the agent actually receives is markdown, not JSON.** The sidecar returns each tool
result twice — the JSON contract as MCP `StructuredContent`, and a bounded rendering as text
content — and OpenCode 1.18.4 forwards only the text. So a real agent driving `acr-mcp`
through OpenCode never sees `context_packet.v1`; it reasons over the rendering. Three
consequences, all load-bearing:

* The scripted model reads the rendering, because that is the surface under test. It still
  accepts the JSON shape, for clients that do forward structured content.
* Evidence reference IDs are base64url tokens full of underscores, and the renderer escapes
  markdown-active characters — `ev2_kid_code` arrives as `ev2\_kid\_code`. The reader
  unescapes before using one, since passing the escaped form back to `source_evidence` would
  be a reference the service never issued.
* The rendered packet lists an item's evidence IDs but *not* the entity behind them, so the
  model cannot know which reference supports which claim until it expands. It therefore
  expands every returned reference (capped at 20) rather than the planned minimum, and
  `min_expandable_evidence` becomes a floor the run must clear rather than a target.

The reader only ever parses sidecar-authored structural lines. All hosted content is rendered
inside quoted `UNTRUSTED DATA` blocks, and every quoted line is skipped, so an evidence
excerpt containing a plausible `- Source: entity_id=…` line cannot inject a sighting the
packet never returned. There is a test for exactly that.

The same fact governs the *assertion* side, and it is easy to forget there. OpenCode records
the text content — the rendering — as `part.state.output` in its event stream, so the
assertion tool grades a session's `source_evidence` round trips by reading markdown too. An
earlier revision JSON-decoded that field; every test passed and every live run failed with
`invalid character '#' looking for beginning of value`, because the test fixtures put JSON
where the client puts a rendering. The reader therefore lives in one shared package,
`tests/fullstack/sidecarmd`, used by both the scripted model and the assertion tool, and its
tests generate their input with the production renderer (`sidecar.RenderEvidenceMarkdown`,
`RenderContextPacketMarkdown`) rather than with hand-written strings, so the fixture cannot
drift from what the sidecar actually emits.

Two things preserve rigor on the markdown path, where the full JSON document is not available:

* An expansion counts only if the rendering the client received *names the reference the
  client asked for*. The `# Evidence <id>` heading is authored by the sidecar from the
  document it resolved, so it cannot be produced without a real resolution — this is what
  keeps "the client got some text back" from passing as proof of an expansion.
* Schema validation and the `content_hash` pin move to the driver's independent direct-HTTP
  capture of the same reference, and `client_and_direct_http_evidence_agree` ties what the
  client saw (entity and availability) to that capture. The skipped client-side schema check
  is recorded as an explicit `SKIPPED` with its reason, never as a silent pass.

An optional pinned local model (Ollama/llama.cpp, temperature 0) may run in a separate
non-blocking profile using rubric checks. It never gates.

## 7. Assertion layers

`tests/fullstack/assertrun` fails the suite unless every layer passes, and every failure
names the layer plus normalized expected/actual values.

1. **Infrastructure** — service readiness, migrations, fixture verification.
2. **ACR API** — capabilities list the expected read tools/versions; packet validates against
   `contracts/jsonschema/v1`; status, `scope_resolution`, categories, rule IDs and evidence
   IDs satisfy the oracle; no cross-org or unrelated fixture evidence appears.
3. **MCP** — OpenCode started the real `acr-mcp serve` process; `tools/list` is exactly
   `["context_for_task","source_evidence"]`; both calls observed in the OpenCode event
   stream; `record_episode` absent.
4. **Evidence** — expanded evidence belongs to the same org/repo; entity type/ID, provenance,
   availability and content hash match the oracle; no evidence URL was fetched.
5. **Agent result** — validates against the result schema; every `observed` claim cites at
   least one evidence ref that was actually returned; no invented refs; required findings and
   checks present; forbidden claims absent; empty/degraded stays explicit.
6. **Web** (when enabled) — Context Packet Explorer describes the same context as the API path:
   same repository, resolved scope, status and expanded-evidence identity. It deliberately does
   *not* compare `context_packet_id`: that ID is `sha256(org_id, request_id, …)` and
   `request_id` is minted server-side per HTTP call, so two independent requests can never
   agree on it. An earlier revision compared it anyway, which made L6 structurally incapable of
   passing. Screenshot, DOM, picker state and the raw catalog response are captured on failure.

Model output is compared on normalized structured fields, never by full-string equality.

The web layer depends on a licensing entitlement, not only on data. `agent_context_runtime` is
the sole *explicit-purchase* feature: `decide_feature()` closes it for every organization
unless an `OrgFeatureOverride` exists, before any tier-eligibility fallback, so a fresh
community-tier org is closed by design rather than by misconfiguration. Two independent
consumers depend on it — the BFF's repository-catalog call, and the sidecar's `/capabilities`
entitlements, which the web client hard-requires before it will request a packet — so without
it the picker is empty and permanently disabled (a 30-second `selectOption` timeout that says
nothing about why), and past that the client fails 426.

The shared driver already grants it: `provision_ops_control_plane` issues `bundles assign-org`
for this feature when it creates the organization. `assert_acr_entitlement` therefore only
verifies, reading back the override row that `decide_feature()` itself consults. The check
exists because nothing previously confirmed the grant was in effect, so a regression in the
driver would have surfaced as that opaque timeout rather than as a named failure.

### 7.1 Device-login approval lifecycle

When the web profile is enabled, the same isolated stack also proves the self-service
`acr-mcp login` path. The CLI runs from a fresh `HOME` and all XDG roots with token and
keyring selectors cleared and `ACR_API_TOKEN_KEYRING_DISABLED=true`; the driver requires this
exact value because any other nonempty setting fails closed before a keyring seam is touched.
Only its private `0600` fallback credential file is permitted.
The browser signs in to the seeded admin account, previews one code, and approves the default
organization-wide repository scope. Before refresh or logout, the exact credential written by
that login drives the real stdio MCP server from a non-Git directory: `context_for_task` and
`source_evidence` must succeed for the seeded repository when it is supplied explicitly. The
same credential must pass authorization for a same-organization future repository that is
absent from the analytics catalog, while a separately seeded foreign-OrgID canary must return
no evidence. The lifecycle then exercises `doctor --live`, scope-preserving refresh, doctor,
logout, and the expected post-logout failure. The consumed code is replayed through the
browser and must return a conflict.

The browser retains connected screenshots for the pending, review, and success states at
375px, 768px, and 1280px. Its network receipt fails on a bearer header, a query string, a
mixed or owner-wildcard repository scope, any console/request failure, or a device-route 5xx;
the singleton `*` is the expected default approval.
The driver redacts device codes, credentials, and DSNs before failure artifacts are retained.

## 8. Commands

```bash
make fullstack-opencode-e2e                                   # Compose, smoke, deterministic model
make fullstack-opencode-e2e E2E_SCENARIO=full                 # all tasks + web agreement
make fullstack-opencode-e2e E2E_SCENARIO=self-test            # prove the assertions reject bad agents
make fullstack-opencode-e2e E2E_MODEL=ollama E2E_SCENARIO=smoke   # optional, non-gating
```

To prove an unmerged web revision in the canonical GitHub Actions gate, dispatch the ACR
workflow from the reviewed ACR branch and pass the reviewed sibling ref explicitly:

```bash
gh workflow run fullstack-acceptance.yml --repo full-chaos/dev-health-acr \
  --ref feat/chaos-3096-acr-mcp-login \
  -f scenario=full \
  -f web_ref=feat/chaos-3096-device-approval
```

Leaving `web_ref` empty preserves the sibling repository's default-branch checkout. The run
manifest records the resolved Dev Health commit SHA in `web_ref` so the uploaded evidence names
the revision the browser agreement actually exercised.

`make fullstack-contract` runs the offline contract checks alone; they are also part of
`make verify`, so a change that weakens an invariant fails an ordinary PR without needing
Docker, a network, or OpenCode.

For a hermetic local proof, `OPENCODE_RUNTIME_FIXTURE` may name an absolute, readable
declared runtime fixture containing `config/opencode/{package.json,package-lock.json,node_modules}`
and `tree-hashes.sha256`. The driver requires normalized manifest entries that exactly cover every
regular runtime file, verifies source bytes, copies into a private stage, then verifies staged bytes
before publishing only that runtime into its fresh client root. It rejects special nodes and all
symlinks; evidence records only the fixture manifest hash, never its host path or package payload.

### 8.1 The self-test

A suite that only ever sees a well-behaved scripted model proves nothing about its own
ability to catch a misbehaving one. The `self-test` scenario replays suitable tasks with a
deliberate fault injected into the deterministic model and requires the assertion layers to
**fail**:

Each fault names the exact check that must reject it, and the run fails if the rejection comes
from anywhere else:

| Fault | What it simulates | Check that must catch it |
| --- | --- | --- |
| `invent-evidence` | cites an `evidence_ref_id` no tool response returned | L5 `no_invented_evidence_ids` |
| `inflate-status` | reports `complete` for task-003's partial packet | L5 `agent_result_packet_status_matches_live_packet` |
| `fabricate-findings` | reports branch-specific findings where task-003 permits none | L5 `findings_must_be_empty` |
| `skip-evidence` | never calls `source_evidence` | L3 `source_evidence_meets_expansion_floor` |
| `wrong-scope` | reports a scope resolution the packet did not resolve to | L5 `agent_result_scope_resolution_matches_live_packet` |
| `unsupported-claim` | asserts an `observed` finding with no citation | L5 `observed_finding_has_citation[…]` |
| `downgrade-claim-kind` | returns a required claim under a weaker kind, with no citation | L5 `required_finding_claim_kind_matches[…]` |

The names are matched whole, not as substrings. Two of them were previously written as
`packet_status` and `source_evidence`, neither of which is a check that fires for those
faults — both were only ever satisfied as substrings of the real check names above, which
credited each fault to a check that had not caught it.

`downgrade-claim-kind` is the one fault the harness schema does **not** also catch, and that is
the point of it. Returning a required claim under a weaker kind makes the schema's
`observed` ⇒ `minItems: 1` conditional stop applying, so an empty `evidence_ref_ids` becomes
schema-valid. Before the oracle's declared `claim_kind` was enforced, and before a required
finding with no resolvable citation became a *recorded* failure rather than an absent check,
this combination passed every gate in the suite — a run could report green having never proven
an entity-backed observed claim. The fault exists so that cannot silently return.

A fault that slips through is a hole in the gate, and is reported as a failure of the run.

Two properties make the difference between a real self-test and a decorative one, and both
were added after a run where neither held:

**The rejection must come from the check the fault targets.** A faulted run that fails for
some unrelated reason is indistinguishable from a real catch if you only look at the exit
status, and it leaves the targeted check silently unproven. `assert_rejected_for` therefore
requires the named check to appear among the failing ones. This is also why
`inflate-status` and `fabricate-findings` are replayed against task-003 rather than task-001:
task-003 is partial and forbids branch-specific findings, while task-001 is complete and permits
evidence-backed findings. Using task-001 would make either targeted mutation a no-op.

**Each faulted session must leave its own event stream.** The faulted run's artifacts are
filed under `<task>-<fault>`, and the run fails outright if
`opencode-events-<task>-<fault>.jsonl` is missing or empty, rather than grading whatever else
is on disk. That guard exists because it caught a real defect: `run_opencode_task` built its
output path with `local task_id="$1" events="…${task_id}…"`, and `local` expands all of its
arguments before assigning any of them, so `${task_id}` resolved to the *caller's* variable of
the same name. Every honest task passed its own id and matched by coincidence; the self-test
passes `<task>-<fault>`, so each faulted session overwrote the honest task's event stream and
the assertions graded that instead. Only `skip-evidence` reads the event stream, so it was the
one fault whose result was meaningless — the other three read the agent result and were
unaffected. Five helpers had the same construction; all are now split across lines, and
`test-fullstack-opencode.sh` fails on the pattern.

The target creates and tears down only what it owns. Failure preserves artifacts but never
containers, volumes, credentials, or modified host configuration unless `E2E_DEBUG=1`.

## 9. Artifacts

`.tmp/fullstack/<run-id>/` and the CI upload contain `run.json`, `junit.xml`,
`service-readiness.json`, `fixture-verification.json`, `capabilities.json`,
`context-packet.json`, `expanded-evidence/*.json`, `opencode-events.jsonl`,
`agent-result.json`, `assertion-report.json`, `logs/*.log`, and `playwright/`.

Redaction rules follow `redact_log` in `scripts/e2e/compose.sh`: no ACR (`fcacr_*`), Ops
(`svc_acr_*`), model-provider or session tokens; DSNs and `Authorization` headers replaced.
Request IDs, packet IDs, fixture IDs, image digests and repository SHAs are retained.

## 10. CI profiles

| Profile | Tasks | Model | Web | Trigger |
| --- | --- | --- | --- | --- |
| PR smoke | 001, 003 | scripted | only when ACR/web contract paths change | pull request |
| Nightly / full | 001–005 | scripted (+ optional local smoke) | yes | schedule |
| Release gate | 001–005 | scripted | yes | release; uses the built image and released `acr-mcp` archive |

## 11. Acceptance criteria map

Where each of CHAOS-3065's acceptance criteria is enforced, so a reviewer can check the claim
rather than take it on trust.

| Criterion | Enforced by |
| --- | --- |
| One command starts the Dev Health services, ACR API, and a real OpenCode + ACR MCP workflow | `make fullstack-opencode-e2e` → `scripts/e2e/fullstack-opencode.sh`, which sources the shared driver's `prepare_stack` |
| Temporary bare-bones OpenCode config; the operator's own config is untouched | `render_client_sandbox`, `host_config_digest` / `assert_host_config_untouched`, `assert_throwaway_home_was_used`; pinned by `test-fullstack-opencode.sh` |
| OpenCode starts the actual `acr-mcp serve` and calls both read tools | `opencode.json.template` registers `["<acr-mcp>", "serve"]`; assertion layer **L3** requires both invocations in the event stream |
| Packet and evidence validate against current contracts | **L2** validates the packet; **L4** validates the driver's direct-HTTP evidence capture in full, unconditionally. The client's *own* copy is schema-checked only when a client forwards JSON — never, with OpenCode 1.18.4 — and is otherwise tied to the validated capture by `client_and_direct_http_evidence_agree` on entity and availability. The skip is recorded explicitly; see §6.1 |
| Complete, branch/commit-scoped and partial branch-gap behaviour match the oracle | tasks 001 / 002 / 003 and their `expected_packet_status` + `expected_scope_resolution`; §5.1 pins exact unavailable-source sets |
| Final response validates against `context_fabric_agent_result.v1` | **L5**, against `testdata/fullstack/v1/schema/` |
| Every observed claim cites returned evidence; invented IDs, unsupported claims, wrong scope, hidden degradation all fail | **L5** checks, each proven end to end by one of the `self-test` scenario's six injected faults (§8.1) |
| `record_episode` unavailable by default | `capture_capabilities`, `capture_mcp_tools`, and **L2**/**L3** |
| Runs without customer data, external provider credentials, or a hosted LLM | synthetic corpus only; scripted local model on loopback; provider package pinned and pre-warmed |
| Failure output identifies the failing layer and retains sanitized artifacts | `assertion-report.json` / `junit.xml` are layer-tagged; `fullstack_cleanup` retains artifacts and redacts through `redact_log` |
| Existing component, client-lifecycle and mock-web suites remain intact and are not mislabelled | §2; `test-compose.sh` still passes unchanged; `test-fullstack-opencode.sh` asserts the distinction is documented |
| Kind produces the same normalized result | not delivered — see §13 |

## 12. Known limits of the proof

Recorded so a reader knows what this gate does *not* establish, rather than inferring more
from a green run than it earns.

**The scripted model still receives claim identity from the plan.** A finding's `claim_id` and
`claim_kind` come from the task oracle by way of the model plan; its citations, its packet
status, its scope resolution and — since the review — its wording all come from live tool
results. So the gate proves *"the agent could support this claim with evidence the live path
returned, and refused when it could not"*, and it does not prove *"the agent chose this claim
unprompted"*. That second property needs a real model and a rubric, which is the optional
non-gating profile in §6.1, not this deterministic one.

**The denied tasks' boundaries are checked over HTTP, not through the client.** Tasks 004
(foreign repo) and 005 (unavailable evidence) both assert the hosted error status directly
through `expect_task_http_error`, and `isDeniedTask` makes both skip L2–L5 identically. So a
regression confined to `acr-mcp`'s handling of those responses would not fail this gate. The
assertion report marks the client-side layers *skipped* rather than passed for both, so the
gap is visible in the output rather than implied by silence.

**L3's expansion requirement is oracle-conditional, not structural.** A session that never
calls `source_evidence` is only caught when its oracle sets `min_expandable_evidence > 0` or
`findings_must_be_empty`. Every shipped oracle does — 001 and 002 set the floor, 003 requires
emptiness, 004 and 005 are denied and skip L3 — so the gap is closed today by the fixture set
rather than by the code. An oracle that sets neither would let a session that expanded nothing
pass silently, which is worth knowing before writing task 006.

**A probe that reads past the component under test cannot speak for it.** The web check spent
three sessions blocked on an "empty repository picker" that was really the ops `api` service
reading the wrong ClickHouse database: the driver never exported `CLICKHOUSE_DB`, so every ops
service resolved `${CLICKHOUSE_DB:-default}` to the literal `default` while all seeding went to
the isolated per-project database. The application saw a valid, empty schema and answered `200
{"repositories": []}`. `assert_repository_catalog_visible` passed throughout, because it
queried the seeded database *directly* — proving the rows existed somewhere, and saying nothing
about whether the application could see them. It now asserts the api service's own
`CLICKHOUSE_URI` names the database this suite seeds before it checks for rows. The general
lesson generalizes past this bug: a fixture probe that bypasses the component under test can
only ever produce false confidence.

**The `content_hash` pin has never fired.** No shipped oracle sets `content_hash` on any
required evidence, so `direct_http_expanded_evidence_content_hash` is unit-tested but has
never run against live data. A pin that has never fired is not yet evidence of anything.

**Some faults trip more than their targeted check, and that is expected.** `skip-evidence`
also fails `required_findings_present`, because with nothing expanded the model's evidence
selectors match nothing and its own refusal to assert uncitable claims drops both findings into
assumptions — the targeted check fires on the cause, this one on the effect.
`fabricate-findings` also fails `agent_result_schema` and `observed_finding_has_citation`,
because the branch-specific finding it synthesizes has nothing to cite and so violates the
same `observed` ⇒ `minItems: 1` rule that `unsupported-claim` targets by name. Neither is
noise: `assert_rejected_for` requires the *targeted* check among the failures, so a fault that
stopped tripping its own check would still fail the run.

**Isolation is proven by metadata, not by a sandbox.** The operator's OpenCode roots are
fingerprinted by name, mtime and size — deeply enough to catch this suite creating a store of
its own, not deeply enough to catch a nested write inside a directory that already existed. A
real boundary would need a container or a user namespace; that is a bigger change than this
gate warranted.

## 13. Deferred

TRD Phase D (Kind parity) is explicitly optional and is **not** delivered here; the Compose
gate must be stable first. When added, Kind must reuse this fixture manifest, oracle, runner
and assertion tool, keep OpenCode and `acr-mcp` host-local, and produce an identical
normalized result.
