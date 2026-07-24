# testdata/fullstack/v1

Fixture corpus and oracle set for the CHAOS-3065 full-stack OpenCode acceptance gate.
Authoritative spec: `docs/fullstack-acceptance.md` (sections 4-5). Do not edit this
directory's *meaning* without updating that doc, and vice versa.

## Provenance

* `seed/clickhouse/001_widget_service.sql` is a deterministic, fixed-UUID / fixed-timestamp
  projection of `testdata/evaluation/v1` (CHAOS-2918) into the Dev Health ClickHouse schema
  (`ops/src/dev_health_ops/migrations/clickhouse`, migrations `000` through `068` as of this
  writing). It seeds the six source-catalog tables needed to reproduce the corpus's four
  evidence records, plus a second, out-of-scope repository (`example-org/other-service`) used
  only to prove repository-scope denial and evidence isolation, plus one bystander row in each
  of 10 further `dev-health-source-catalog.v1` tables so task-001's packet has as few
  `UnavailableSource` entries as the current code allows (see "Packet status" below and
  `fixture-manifest.json`'s `background_density_rows`). Those 10 tables' rows are background
  density only, never `required_evidence`.
* `fixture-manifest.json` records the fixture version, the SHA-256 of every consumed
  `testdata/evaluation/v1` file (independently re-verified against the files on disk, not just
  copied from that corpus's own `manifest.json` -- no mismatch was found), the SHA-256 of the
  seed SQL file, fixed identities, expected per-repo row counts, and scope probes.
* `tasks.json` defines the five full-stack tasks (`docs/fullstack-acceptance.md` section 5).
  Tasks 001-003 keep their `goal` text verbatim from `testdata/evaluation/v1/tasks.json`; their
  `scope` fields were re-derived from docs/fullstack-acceptance.md section 5, not copied
  wholesale from the evaluation corpus (see the "commit evidence is unreachable" note below).
  Tasks 004-005 are new (no evaluation-corpus equivalent).
* `expected/task-*.oracle.json` is authored **from the fixture and the real
  `internal/contextpacket` code, before any model or ACR output was observed** (CHAOS-2918 /
  CHAOS-3065 requirement). Where the design doc's or evaluation corpus's stated expectations
  disagree with what the real assembler code will actually produce, the oracle follows the
  code and calls out the disagreement in its own `*_reasoning` field -- see "Packet status"
  below for the big one.
* `schema/context_fabric_agent_result.v1.schema.json` is a harness-owned JSON Schema (draft
  2020-12) for the agent's final strict-JSON message. It is not a product contract.

## Regeneration

1. Re-run the evaluation corpus's own generator if `testdata/evaluation/v1` changes
   (`internal/evalfixture`, CHAOS-2918). Recompute its manifest.
2. Update `seed/clickhouse/001_widget_service.sql` to match any changed corpus content,
   keeping every UUID/timestamp fixed and literal.
3. Recompute `sha256` for every changed file and update `fixture-manifest.json`
   (`shasum -a 256 <file>`).
4. Re-derive `expected/task-*.oracle.json` from the updated fixture and the current
   `internal/contextpacket/source_queries.go` / `scope_resolution.go` / `rules.go` /
   `ranking.go` / `packet_state.go` -- by static reading, not by running the stack and copying
   its output.
5. Validate every JSON file parses (`jq -e . <file>`) and that the JSON Schema itself is a
   valid schema document.

## Manifest authoring gotchas

Three lessons learned the hard way while wiring this manifest into the real orchestrator/verifier
-- keep all three in mind before editing `fixture-manifest.json` again:

* **`file_hotspot_daily` and `file_complexity_snapshots` reject `FINAL`.** Both are plain
  `MergeTree` (`PARTITION BY toYYYYMM(...)`), not `ReplacingMergeTree` -- this is exactly why
  `file_hotspots.v1`/`file_complexity.v1` in `source_queries.go` use `GROUP BY` instead of
  `FINAL`. Any probe or count query against those two tables must be a plain
  `SELECT count() FROM <t> WHERE repo_id = {repo_id:UUID}` (no `FINAL`). Every other seeded
  table is `ReplacingMergeTree` and should use `FINAL`. See `expected_row_counts_note` for the
  full per-table breakdown.
* **Keep prose out of typed collections.** `fixture-manifest.json`'s verifier decodes
  `expected_row_counts` as `map[string]map[string]int` (repo slug -> table -> count). A
  reasoning/notes string dropped in as a sibling key inside that map (or a `"totals"` entry
  that isn't a real repo slug, or a boolean value sitting among the int counts) breaks decoding
  for the *entire* manifest, not just that field. Every `*_note`/`*_reasoning` field in this
  manifest and in `expected/task-*.oracle.json` lives at its own sibling key, one level up from
  any homogeneous map/array the tooling decodes into a concrete type -- never injected as an
  extra entry inside one. When adding a new note, check whether the field it's next to is a
  fixed-shape object (safe) or a dynamically-keyed dictionary (needs its own sibling key).
* **`work_item_dependencies` has no `repo_id` column at all** (see its DDL in
  `011_work_item_extras.sql`: `source_work_item_id`, `target_work_item_id`, `relationship_type`,
  `relationship_type_raw`, `last_synced`, plus `org_id` from migration `024`). It cannot be
  counted per-repository the way every other table in `expected_row_counts` is -- there is
  nothing to join or filter on. The verifier handles this by summing the per-repository
  expectations for such a table and checking a single org-scoped count instead (e.g. widget-service's
  `1` + other-service's `0` become one check for `1` total, org-scoped, not repo-scoped). The
  manifest's `expected_row_counts` entries for `work_item_dependencies` do not need to change to
  reflect this -- they're still meaningful as the intended per-repo breakdown for a human reader
  -- but don't be surprised that the actual probe run against it is a single count, and don't add
  a `repo_id`-based query for it anywhere.

## The `__ORG_ID__` substitution contract

Wherever an `org_id` column value appears in the seed SQL, it is the literal quoted token
`'__ORG_ID__'`. The orchestrator (`scripts/e2e/fullstack-opencode.sh`, sourcing
`scripts/e2e/compose.sh`'s org provisioning) performs exactly one textual substitution of
`__ORG_ID__` with the real organization UUID minted by `dev-hops admin orgs create` (see
`provision_ops_control_plane` in `scripts/e2e/compose.sh`) before executing the seed file.
There is no other substitution mechanism -- do not template this any other way. Preflight must
confirm the substitution happened exactly once per row (see `fixture-manifest.json`'s
`org_id_substitution` and `scope_probes`).

Six tables (`git_commits`, `git_commit_stats`, `ci_pipeline_runs`, `git_pull_requests`,
`git_pull_request_reviews`, `deployments`) also get an `org_id` column, added by the Python
migration `027_add_org_id_to_sorting_keys.py` -- a `.py` migration, not `.sql`, so a
column-existence check that only greps `*.sql` files will miss it (this bit both a prior audit
of this fixture and, briefly, this file itself -- see git history if curious). That migration
also rebuilds each table's ORDER BY with `org_id` prepended first (e.g. `git_commits` becomes
`ORDER BY (org_id, repo_id, hash)`), making `org_id` part of the `ReplacingMergeTree` dedup
identity, not merely an inert column -- leaving it unset would be a real correctness bug (rows
would dedup against any other org's rows sharing the same natural key under the shared
`'default'` bucket), not just cosmetic. All six INSERTs therefore set `org_id` explicitly to
`'__ORG_ID__'`, exactly like every other table that has the column. Separately, and still worth
knowing: `source_queries.go`'s catalog queries never filter any of these six tables by their
own `org_id` directly regardless -- every query scopes org through
`INNER JOIN repos ... WHERE repo.org_id = {org_id:String}` instead -- so the explicit value
doesn't change any ACR query result; it exists purely for correct dedup identity, and matters
if anything other than these catalog queries ever reads these tables directly. See
`fixture-manifest.json`'s `org_id_substitution.org_id_on_git_ci_deployment_tables` for the full
list and reasoning.

## The `as_of` pin

Every `context_for_task` call the orchestrator makes on behalf of this fixture must set
`scope.as_of = 2026-01-14T12:00:00.000Z` and leave `scope.time_window_days` unset. See
`fixture-manifest.json`'s `as_of_pin` for the full rationale: neither field has a server-side
default anywhere in `internal/api`/`internal/mcp`/`internal/contextpacket`, so omitting both
disables every `source_queries.go` time filter entirely -- this fixture's fixed Jan-2026
timestamps have only "worked" so far because there was no filtering to fail, not because they
fall inside a window, and today (2026-07-23) is already more than six months past them. Pinning
`as_of` removes that fragility and also pins `packet.generated_at`/`freshness.as_of` to an exact,
assertable value.

## Oracle-before-observation rule

Every `expected/task-*.oracle.json` file was written by reading the fixture (this
directory) and the real `internal/contextpacket` source, and only then -- reasoning
about what the code will do, not by booting the stack and recording its output. This
matters because it is the only way the acceptance gate can catch a real regression
instead of re-certifying whatever the code currently happens to do.

## Evidence-ref-id matching (read this before writing assertion code)

`internal/contextpacket/source_queries.go`'s SQL emits a human-readable locator as the
`evidence_ref_id` column, e.g. `acr:v1:commit:a1b2c3d4...`. **That is not the value ACR
actually returns to clients.** `internal/contextpacket/clickhouse.go`'s `ContextForTask`
immediately overwrites it:

```go
handle, encodeErr := s.codec.Encode(p.OrgID, scope.RepoID, evidence[index].SourceVersion, evidence[index].EvidenceRefID)
evidence[index].EvidenceRefID = handle
```

`EvidenceIDCodec.Encode` (`internal/contextpacket/evidence_id.go`) produces an opaque,
per-request token of the form `ev1_<kid>_<code>_<base64(repository_tag)>.<base64(hmac)>`,
where the HMAC is keyed by a deployment-specific signing key and covers `(org_id, repo_id,
query_id, locator)`. Because `org_id` is only minted at provisioning time (it is the
`__ORG_ID__` substitution target), **no oracle authored ahead of a run can ever contain the
literal wire-format `evidence_ref_id` string** -- this is by design (evidence refs are
intentionally unguessable and non-portable across orgs/repos), not a fixture gap.

Every `required_evidence` / `forbidden_evidence` entry in the oracle files therefore carries
both:

* `derived_locator` -- the pre-encode locator, useful for readability and for tracing back to
  `source_queries.go`, but **never directly comparable to a live `evidence_ref_id`**;
* `query_id`, `entity_type`, `entity_id` -- stable, plaintext fields that travel unchanged
  through `codec.Encode` (only `EvidenceRefID` is replaced; `Source.EntityType`/`EntityID`
  are not touched) and so **are** safe for the assertion tool to match against a live
  `ContextPacketItem.RelatedEntities` / `EvidenceRef.Source` on the running system.

`tests/fullstack/assertrun` (a separate deliverable) must match required/forbidden evidence
by `(query_id, entity_type, entity_id)`, never by string-comparing `derived_locator` against
an observed `evidence_ref_id`.

## Claim-id convention

`context_fabric_agent_result.v1` findings need a `claim_id`. No product or test-harness
convention existed for this before this fixture set, so this oracle set defines one:
`claim_id = "finding:" + <derived_locator with the "acr:v1:" prefix stripped>`, e.g.
`finding:commit:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2`. This is purely a harness
convention for `tests/fullstack/modeloracle` and `tests/fullstack/assertrun` to agree on; it
is not derived from any ACR code.

## Packet status

**`PacketComplete` is currently unreachable for every task, for every organization on Dev
Health, not just this fixture -- for TWO independent, confirmed product bugs, plus one
structural limitation.** This is the single most important finding from authoring these
oracles, and it overrides both `testdata/evaluation/v1/tasks.json`'s `expected_status` field
and `docs/fullstack-acceptance.md` section 5's "Expected packet" column for tasks 001-003. The
team lead independently verified both bugs live (see Consequences 2 and 3 below) -- neither is
being fixed inside CHAOS-3065, so every oracle here asserts `partial`/`degraded` as ground
truth, with an exact `expected_unavailable_sources` tripwire per task (`expected_unavailable_sources_exact:
true`, see each oracle's own file) so the day either bug *is* fixed, the affected oracle(s) fail
loudly instead of silently staying green forever.

**Two independent bugs, two independent fixes, landing separately** (both only ever affect
task-001 -- see Consequence 1 for why 002/003/005 are structurally unaffected by either):
* **CHAOS-3068** (incidents.v1 queries a dropped table, Consequence 2): fixed by repointing
  `internal/contextpacket/source_queries.go`'s `incidents.v1` at a real incidents source
  (`operational_incidents`).
* **CHAOS-3069** (Float32 confidence-scan bug, Consequence 3): fixed in one place, either
  scan-side (`source_executor.go`'s `scanEvidenceRow`) or with `toFloat64(confidence)` in the
  four affected queries.

**What to change in task-001's oracle, per landing order** -- exactly one of these three, never
more than what actually landed:
* **If CHAOS-3068 lands first (3069 still open):** `expected_unavailable_sources` drops to
  exactly the 4 CHAOS-3069 entries
  (`work_graph.v1`, `ai_workflow_artifacts.v1`, `ai_review_outcomes.v1`,
  `deployment_incident_provenance.v1`, all `source_unavailable`). `expected_packet_status`
  **stays** `partial`.
* **If CHAOS-3069 lands first (3068 still open):** `expected_unavailable_sources` drops to
  exactly `[{"source":"incidents.v1","reason":"source_unavailable"}]`.
  `expected_packet_status` **stays** `partial`.
* **Once both have landed:** `expected_unavailable_sources` becomes `[]`, and **only then**
  does `expected_packet_status` flip from `partial` to `complete`. No other change to this
  oracle or the fixture should be needed in any of the three cases. Tasks 002/003/005's oracles
  do **not** change for either fix, landing in any order.

`dev-health-source-catalog.v1` (`source_queries.go`) has **17** source queries, scoped
`commit` / `branch` / `repo`. `source_catalog.go`'s `ExecuteCatalog`:

* skips every `repo`-scoped query outright (with an `UnavailableSource`) whenever the request
  has a branch;
* skips every `commit`-scoped query outright (with an `UnavailableSource`) whenever the
  request has no commit sha;
* and, for every query that *does* run and returns zero rows, `appendMissingCatalogSources`
  adds a `no_evidence` `UnavailableSource` too.

**Consequence 1 -- task-002 (and any branch-scoped task) can never reach `complete`, for any
fixture.** Its scope is branch-only (`branch=main`, no commit, required by its own "branch
resolution" purpose), so all 10 `repo`-scoped sources are unconditionally skipped
(`repo_fallback_branch_not_supported`) and both `commit`-scoped sources are unconditionally
skipped (`commit_scope_not_requested`) -- 12 guaranteed `UnavailableSource` entries with zero
dependence on what is seeded. The *only* request shape under which every source is even
eligible to run is "branch empty AND commit_sha set" (both the repo-scope skip and the
commit-scope skip are avoided simultaneously) -- exactly task-001's shape, and the only one.
This fixture now seeds one bystander row in each of the 10 `repo`-scoped/`file_complexity.v1`
sources specifically so task-001 can reach zero `UnavailableSource` (see
`background_density_rows` in `fixture-manifest.json`); task-002 structurally cannot, no matter
how much is seeded.

**Consequence 2 -- even task-001 cannot reach `complete` today, because of a real product bug
(filed as CHAOS-3068, not fixed inside CHAOS-3065).**
`incidents.v1` still does `FROM incidents AS i ...`, but
`ops/src/dev_health_ops/migrations/clickhouse/068_drop_legacy_incidents.sql` (`DROP TABLE IF
EXISTS incidents;`, CHAOS-3062) removes that table in favor of `operational_incidents` (a
different column shape -- notably no `repo_id` at all -- `066_operational_canonical.sql`), with
no compatibility view. After a full `dev-hops migrate clickhouse` run, the `incidents` table
does not exist, so `incidents.v1` always fails with `UnavailableSource{Reason:"source_unavailable"}`
-- for every task, for every organization, forever, regardless of seeding (there is nothing to
insert). Until `source_queries.go` is repointed at `operational_incidents` (a real design change,
not a rename, since there's no `repo_id` to join on), this alone is enough to keep
`PacketComplete` unreachable for task-001.

**Consequence 3 -- a second, independent product bug, CHAOS-3069, affects the other 4 of
task-001's 5 `expected_unavailable_sources`.** `internal/contextpacket/source_executor.go`'s
`scanEvidenceRow` declares `var confidence float64` and scans every catalog row's `confidence`
column into it -- but four sources project a genuinely native `Float32`-typed `confidence`
column straight through with no cast: `work_graph.v1` (`work_graph_edges.confidence Float32`),
`ai_workflow_artifacts.v1` (`ai_workflow_artifact_edges.confidence Float32`),
`ai_review_outcomes.v1` (`work_graph_pr_review_outcome_edges.confidence Float32`), and
`deployment_incident_provenance.v1` (`work_graph_deployment_incident_edges.confidence Float32`).
Every other catalog query's `confidence` is either a `Float64` literal (`1.0`) or a real
`Float64` column (e.g. `deployments.v1`'s `release_ref_confidence`), so this is exactly and only
these four. `clickhouse-go/v2`'s `Float32` column `ScanRow` only accepts `*float32`/`**float32`/
`sql.Scanner`, so scanning into `*float64` fails with a `ColumnConverterError`, and the
row-level error surfaces as `UnavailableSource{Reason:"source_unavailable"}` -- indistinguishable,
from the packet (or the acr-api logs, which record the failure with no error text) alone, from a
genuine zero-row result. This was confirmed live (acr-api logged exactly these four plus
`incidents.v1` as failed evidence queries for task-001) and independently by reading the
`clickhouse-go/v2` module source; the fixture's rows for these four tables are correctly seeded
and were verified matching every WHERE-clause predicate both live (`fixture-verification.json`)
and by this author's own from-scratch ClickHouse replay -- **the seed is not the problem here.**
This bug fires for any organization's data, not just this fixture, and -- like CHAOS-3068 --
only manifests when the affected query actually executes; since all four are `EvidenceScopeRepo`,
`ExecuteCatalog` skips them outright (`repo_fallback_branch_not_supported`) whenever a request
sets `branch`, masking this bug for every branch-scoped task (002/003/005). It only actually
fires for task-001 (branch empty, commit_sha set -- the one scope shape that unlocks
`EvidenceScopeRepo` queries in the first place, per Consequence 1).

`packet_state.go`'s `setStatus()`:

```text
items non-empty:  Unavailable>0 || Truncated  -> Partial   (else Complete)
items empty:      candidateCount>0            -> Partial + "context_filtered_or_truncated"
                  else Unavailable>0          -> Degraded
                  else                        -> Empty + "no_evidence_found"
```

task-003's own branch genuinely has zero matching rows in the 5 branch-scoped sources no
matter what else is seeded elsewhere, so its `Unavailable` is never empty either --
`PacketEmpty` and the literal `no_evidence_found` warning are unreachable for it; the real
code produces `Degraded` instead (see its oracle's `status_reasoning`).

This whole section is flagged as a finding for `docs/fullstack-acceptance.md`'s owner and
`internal/contextpacket/source_queries.go`/`packet_state.go`'s owner to reconcile.

## Cross-task evidence bleed

All five tasks share one repository (`example-org/widget-service`) and effectively one
branch (`main`), because that is what the underlying evaluation corpus models. Several
source queries are scoped by branch, not by task, so e.g. task-001's exact-commit request
(which leaves branch empty) will also surface PR #1042 and its review, and task-002's
branch-only request will also surface the `checkout-e2e-run-4821` CI run. This is expected
and each oracle notes it explicitly (`known_additional_evidence_note`) -- it is not something
`forbidden_evidence` should ever flag. Only `example-org/other-service` content is genuinely
forbidden.
