# CHAOS-3860 P6 panel run manifest

Status: **Implementation contract** — authoritative for `internal/panelharness`,
`cmd/acr-panel-harness`, and `testdata/panelharness/v1/`.

Ruling: sol-max architectural channel, CHAOS-3860, 2026-08-20 (adopted
recommendation (b), amended as (d) staged as (b) — see the Linear issue's own
comment history for the full ruling text).

## 1. What this harness is, and is not

`internal/panelharness` is the CHAOS-3860 P6 activation driver: it runs a
multi-model panel through a bounded, N-turn select-and-continue confirmation
loop (pivot-intent design brief §2.4/§3.1, generalized past its original
two-turn shape by CHAOS-4146(a) to as many rounds as
`RunConfig.MaxClarificationTurns` allows) against a REAL org's REAL data, speaking
the hosted ACR contract directly over HTTP
(`POST /api/v1/context-fabric/investigations`) with a REAL, per-panelist
bearer credential — the "3860 guard": no synthetic principal, no shared
credential, no in-process bypass of authentication.

It is **not**:

- A second capture pipeline. Every structure-offer receipt a panelist
  redeems flows through the EXISTING P4 capture path
  (`internal/contextfabric/structure_capture.go`,
  `internal/contextfabric/pgstructureselection`) exactly like any other
  `agent_receipt` confirmation. This package adds no engine code and no
  contract field.
- A writer of `consensus_evidence`. This package has no Postgres access at
  all — HTTP only — so it cannot construct a
  `contextfabric.ConsensusEvidence` value even by mistake. Consensus is
  computed here, client-side, from the harness's own observations, and
  reported ONLY in the manifest this document describes.
- The P5 annotator. Materializing verified consensus onto captured rows —
  the `consensus_evidence` column's actual write path — is separately
  ratified, later work, owned by an internal, ops-scoped component that
  reads these manifests. This package produces the annotator's INPUT; it
  does not implement the annotator.

## 2. The manifest: a harness-owned schema, not a product contract

`testdata/panelharness/v1/schema/panel_run_manifest.v{1,2}.schema.json` are
this package's own versioned artifact — ONE JSON Schema file per
`ManifestSchemaVersion` value, so a manifest a prior binary wrote keeps
validating against its OWN version's file after a later bump, rather than
one shared file being silently overwritten to require a shape older
artifacts never had. This mirrors
`testdata/fullstack/v1/schema/context_fabric_agent_result.v1.schema.json`'s
own precedent (never enters `contracts/`, not subject to the CONTRACT-FIRST
rule in `AGENTS.md`, because ACR itself does not produce or consume it as a
wire contract — the harness produces it, and a future P5 annotator consumes
it). `internal/panelharness.PanelRunManifest` (Go) and the CURRENT version's
JSON Schema are kept in lockstep by hand; there is no code generator between
them (each schema file is small and changes rarely enough that generation
would be more machinery than the problem needs).

One manifest file is written per (org, question) panel activation, via
`PanelRunManifest.WriteFile` (temp-file-then-rename, matching every other
durable-artifact publish convention in this repo). Manifests are immutable by
convention: nothing in this repo ever opens one for in-place editing.

### 2.1 Row resolution (for the future P5 annotator)

The ruling names the future annotator's lookup key into
`acr.context_fabric_structure_selections` as
`(org_id, prior_result_id, member, selected_receipt_id)` — a bare result id
is insufficient, because a StructureSelectionEvent's own `prior_result_id`
identifies the TURN-1 result an offer came FROM, not the turn-2 result it
landed in. `PanelistSelection` carries every field that lookup needs
(`prior_result_id`, `receipt_id`, plus `confirmed_result_id` for the turn-2
result_id an operator would use to independently re-verify a redemption
against the hosted API's own read path).

### 2.2 The required invariant

Every `PanelMemberRun` carries `complete` and `distinct_identities` booleans,
computed by `internal/panelharness.BuildMemberRun` — the ONLY place in this
package this invariant is evaluated, so no caller can re-derive a
slightly-different version of it:

- **complete**: true only when EVERY configured panelist produced a landed
  (applied, not vetoed/superseded/unresolved) selection for this member.
  A panelist that errored, timed out, or had its confirmation vetoed
  contributes NO entry at all — never a placeholder — so a missing panelist
  is indistinguishable from an absent one, by design.
- **distinct_identities**: true only when every panelist's
  `canonical_model_identity` is unique. Two "panelists" reporting the same
  underlying model can never carry multi-model authority, no matter how
  their votes split.
- **agreement_bits[i]** reports whether `panelists[i].applied_value` equals
  the majority value (ties broken on the lexicographically smaller value,
  deterministic and reproducible from `value_counts` alone — never on
  panelist arrival order, which a retry could reshuffle).

None of this computes or asserts a PROMOTION decision. The actual authority
threshold (when does multi-model consensus outrank single-model support) is
P5/curation's own, separately-ratified rule (design brief §3.2's promotion
rule) — this harness reports the raw histogram and the three booleans above;
it does not guess the threshold.

### 2.3 Clarification log (CHAOS-4146(a))

`ClarificationLogs` carries one `PanelistClarificationLog` per panelist that
attempted at least one `Investigate` call: that panelist's own turn-by-turn
`ClarificationTurnEvent` history (turn index, closed-vocabulary outcome,
offer kinds seen). It is independent of `Members` — a single turn's
`StructureNeeds` can offer more than one member at once, so the log groups
by panelist-and-turn, not by member. Capture-only, matching `AgreementBits`:
no consensus/disagreement label is derived from it in this package (see §1).

Turn-level receipt accumulation: the hosted engine derives confirmed
structure fresh from what EACH request itself carries (no server-side
session state across calls), so every turn after the first resends the FULL
set of previously-applied receipts, not just the newest turn's — a request
that dropped an earlier turn's receipt would silently un-confirm it.

### 2.4 Batch corpus provenance (CHAOS-4146(b)/(c), schema v2)

`cmd/acr-panel-harness -corpus` (§5) drives the panel over a JSON array
corpus file, one case per array element, instead of a single `-question`.
Each case's manifest carries:

- **`case_index`** — the case's 0-based position in the corpus array
  (matching the two-turn trial harness's own oracle-annex indexing
  convention), denormalized onto both `PanelRunManifest` and every
  `PanelMemberRun` row. Index only — the corpus's own `question` field text
  is read locally to drive the `Investigate` call and is NEVER carried onto
  the manifest, a report, a Linear ticket, or any other artifact (§3
  extends identically to batch runs).
- **`run_tag`** — groups every manifest one batch invocation writes,
  mirroring `scripts/trial`'s own RUN_TAG discipline (UTC timestamp + PID).
- **`corpus_path`** / **`corpus_sha256`** — which corpus file drove the run,
  and a content hash proving it was not silently swapped mid-batch. Path and
  hash only; never the corpus's own content.

All four fields are absent (zero value / `nil`) on a manifest from a single
ad-hoc `-question` run.

## 3. Privacy discipline

`question_hash` is `contextfabric.QuestionHash(question)` — the SAME
canonicalizing SHA-256 hash the P4 capture schema already uses. The raw
question TEXT is never carried by a manifest, matching the standing rule
(design brief §6): question text stays out of telemetry, capture sinks, and
prior stores; only the product's own org-scoped result storage
(`engine.go`'s own `result.Question` persistence) is the legitimate
exception, and manifests are not that. This applies identically whether the
question came from `-question` or from one array element of a `-corpus`
file (§2.4): the corpus driver reads the case's `question` field locally,
solely to place it on the wire request, and never writes it to any output
this binary produces.

## 4. Frozen corpus stays eval-only

This harness runs against a real org's own real data. It is never pointed at
the frozen evaluation corpus (`.remember/acr-3778-corpus-frozen-annotated.json`)
— that corpus stays eval-only forever, per the holdout discipline CHAOS-3860's
own GUARDS section states as a design requirement, not an option. The batch
corpus driver (§2.4, §5) is validated against `.remember/acr-3778-corpus-ext65.json`
(CHAOS-4146(d)'s own ext65 measurement corpus) — a SEPARATE, already-in-use
trial corpus, never the frozen holdout.

## 5. Batch corpus driver (CHAOS-4146(b))

`cmd/acr-panel-harness -corpus <path>` replaces `-question <text>` (mutually
exclusive) and drives the panel across a contiguous slice of the corpus
array:

- `-case-start <int>` (default `0`) / `-case-count <int>` (default: every
  remaining case) select the slice this invocation processes — the
  driver's own shard-selection surface, mirroring `scripts/trial`'s
  contiguous shard-range pattern. Unlike that harness, panelharness has no
  local Postgres dependency to isolate per shard (every call is a real,
  stateless HTTP round trip to the hosted API), so no per-shard
  database/container machinery is needed: an operator wanting parallel
  shards simply launches several invocations with disjoint
  `-case-start`/`-case-count` ranges and a shared `-run-tag`.
- `-run-tag <string>` (default: computed as `panelbatch-<UTC timestamp>-<pid>`,
  matching `scripts/trial`'s own RUN_TAG discipline) groups every manifest
  the invocation writes.
- `-output-dir <dir>` (replaces `-output` in corpus mode) receives one
  manifest file per case, named `<run-tag>-case<index>.json`.

Each case's corpus `question` field is read locally and used only to build
that case's `Investigate` request; it is never written to stdout, a log
line, or any manifest field (§2.4, §3).
