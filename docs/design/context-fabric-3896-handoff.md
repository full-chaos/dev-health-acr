# Context Fabric: CHAOS-3896 hand-off (CHAOS-3898 S3)

Short design note for the final CHAOS-3898 slice: design brief v4.1 §6's
S3 row -- "uniform bridge fns + injectivity pins + `anchor_collision`.
S2a+S2+S3 unblocks 3896 Slices B and C." This closes the precondition
CHAOS-3896's own design brief v6 §1.4 states verbatim: "for every census
kind, (source natural key) <-> (graph canonical id) is injective...
delivered and pinned by 3898."

## What existed before this slice

CHAOS-3899 (merged, PR #144) shipped 3896 Slice A: the census registry
(`chaos3899_census_registry.go`), the aggregate-first protocol
(`chaos3899_census.go`), and the shadow evidence round
(`chaos3899_evidence_round.go`). Slice A performs **no graph read at all**
-- every would-commit outcome is marked `PreconditionUnproven`, by design,
because naming a graph satisfier from a census result needs a bridge from
the census's own `SatisfierNaturalKey` (a raw source-column composite) to
a graph canonical id, and that bridge did not exist yet. This slice adds
it, plus the project-anchor soundness gap design brief v4.1 §1.4 names.

## 1. The bridge: `BridgeSatisfierToCanonicalID`

Added as a new field on `censusKindRegistryEntry`
(`bridgeCanonicalID func(satisfierNaturalKey string) (string, bool, error)`)
alongside the existing `handlePredicate`/`anchorColumns` per-kind facets --
the SAME registry pattern, not a second parallel kind switch, matching
design brief §5 item 1's own framing ("Derivation registry (identity
columns; derive; bridge fns; anchor extractors...)").

Each kind's `identityColumn` is a ClickHouse `concat(org_id, ':', ...)`
expression -- `RunCensus`'s row statement returns it as ONE string
(`CensusResult.SatisfierNaturalKey`). The bridge must invert that
concatenation to recover the segments `identity.Derive` needs. The naive
`strings.Split(s, ":")` is wrong: `work_items.work_item_id` can itself
contain a colon (`"linear:CHAOS-3896"`, the same shape
`ticketKeyAlias`/`workItemTicketKeyPredicate` already handle elsewhere in
this package), and a naive split would fragment it. `splitCensusNaturalKey`
uses `strings.SplitN(s, ":", wantSegments+1)` instead -- only the first
N colons are separators, so the LAST segment keeps any embedded colon
intact. `wantSegments` comes from `identity.Lookup(kind).Columns`'s own
length, not a hardcoded number, so a future Registration column change is
picked up automatically.

Four kinds bridge through `identity.Derive` (ci_pipeline_run, work_item,
pull_request_review -- the three changed census kinds -- using the exact
segment order `identity.Registry` already declares). `pull_request` is
grandfathered onto the pre-CHAOS-3898 scheme (design brief §1.2: "injective,
colon-free by type") and bridges via plain string concatenation, never
calling `identity.Derive` and never omitting.

**Injectivity pin**: `TestBridgeSatisfierToCanonicalID_MatchesIdentityDerive`
proves, for every changed kind including an embedded-colon work_item_id and
review_id, that bridging a census natural key equals `identity.Derive`ing
the same segments directly -- the bridge inherits `identity.Derive`'s own
already-proven injectivity rather than re-deriving a parallel id that could
diverge. `TestBridgeSatisfierToCanonicalID_Injective` spot-checks distinct
tuples never collide. Both mutation-verified (temporarily reverting
`SplitN` to `Split` fails exactly the embedded-colon cases).

## 2. `anchor_collision`

Design brief v4.1 §1.4 ("project: provider-qualified identity"): `projects.id`
alone does not name a provider (`projects` is unique by `(org, provider,
id)`), and `work_items.project_id` -- the work_item census kind's own
anchor FK column -- carries no provider. `BuildCensusDiscriminator`'s
project-anchor predicate compares that raw column against
`canonicalIDValue(SubjectProject, anchorCanonicalID)`, which drops the
provider (it decodes `project.v2:<provider>:<id>` and returns only the
LAST segment). If the same raw id is shared by two providers' projects in
one org, that predicate silently matches BOTH providers' work items -- a
false-positive satisfier the aggregate-first protocol has no way to detect
on its own (it only sees a predicate, not that the predicate under-qualifies).

`AnchorCollision(ctx, client, orgID, anchorKind, anchorCanonicalID)` is the
live, BIND-TIME check: for a `SubjectProject` anchor, it queries
`key_resolution_count` (`count()` on `projects FINAL WHERE org_id=... AND
id=...`) -- the exact ambiguity `queryWorkItemProjects`/`queryProjectTeams`
already guard at PROJECTION time via their own `ambiguityLedger`, run here
per-request instead of discovered only after a batch run. Every other
anchor kind (`SubjectRepository`) returns `false` without issuing a query --
this defect is project-specific.

`graphrank.ReasonAnchorCollision` (`"anchor_collision"`) is the new
DegradationReason a future 3896 Slice B/C's `BindAnchor`-adjacent call site
returns when `AnchorCollision` reports true -- distinct from
`ReasonAnchorNotUnique` (claimant-COUNT ambiguity at the graph
alias-lookup layer, a different failure class from a provider-collided
SOURCE id). It is not yet in 3896 brief v6's own §4 vocabulary table;
3898 exposes the surface, 3896 consumes it.

## 3. What this slice does not do

- **Does not wire either primitive into `RunShadowEvidenceRound`.**
  CHAOS-3899's shadow round is shipped, stable production-adjacent code;
  consuming these two primitives inside its control flow (the keyed graph
  existence read for `ShadowWouldCommit`, and gating a project anchor on
  `AnchorCollision` before `BuildCensusDiscriminator`) is 3896 Slice B/C's
  own scope, per team-lead's commission ("uniform bridge fns + injectivity
  pins + the anchor_collision surface... exposed where 3896's Slice B/C
  will consume it").
- **Does not touch `graph_missing_satisfier`.** That DegradationReason
  already exists (CHAOS-3899); producing it for real requires the keyed
  `nodeByKindID` read Slice C performs using this slice's bridge -- the
  read itself, and its own projection-lag fixture, stay Slice C's to build
  (CHAOS-3899's own PR description already flagged this as deliberately
  deferred, not a gap papered over here).
