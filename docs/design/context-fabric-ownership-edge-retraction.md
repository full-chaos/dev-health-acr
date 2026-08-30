# Context Fabric: retracting a suppressed `OWNED_BY_TEAM` edge (CHAOS-4565)

Short design note, the companion to
[`context-fabric-work-item-ref-healing.md`](context-fabric-work-item-ref-healing.md):
both are cases where this producer has to REMOVE something it previously
projected, and both do it with a `ProjectionTombstone` rather than by
omission. Read that note first if you have not — the idempotent-unconditional
tombstone shape is the same, and this note assumes it.

Owning code: `internal/contextfabric/devhealthsource/teams_projects_edges.go`
(`projectTeamsStatement`, `queryProjectTeams`, `projectTeamRelationshipID`).

## 0. The defect

`queryProjectTeams` refuses to assert an ownership row in two cases:

| path | decided | since |
| --- | --- | --- |
| **ambiguous key** — the `project_key` names more than one project | in SQL, upstream: `readers.ProjectIdentityJoinSQL` emits no key scope row for `key_resolution_count > 1` | v7 |
| **conflicting identity** — the row's `project_id` resolves project A while its `project_key` resolves B | in the scan, `edge_suppressed = 1` | v8 (CHAOS-4542) |

In both cases the producer emitted only a progress candidate. But
**incremental graph application does not delete an absent relationship** —
`internal/contextfabric/AGENTS.md` states the rule the other way round:
*full-snapshot deletion semantics require an explicit complete-enumeration
proof*, and an incremental page is not one. So an `OWNED_BY_TEAM` edge
projected BEFORE its row became suppressed stayed live indefinitely, and only
a full rebuild cleared it.

**Suppression is a decision not to assert. It is not a retraction — and the
graph cannot tell the difference.**

## 1. Why a tombstone, and not a closed validity window

The ticket offered three shapes. A closed `valid_to = now` is the one worth
arguing about, because this producer already models ownership as an interval
and `ownershipValidity` already states both ends explicitly.

It is rejected because it asserts the wrong KIND of fact. Closing the window
says *"this ownership ended at T"* — a temporal claim about the world, which
the source never made and which nothing here can substantiate. What actually
happened is epistemic: *"we can no longer tell which project this ownership
names."* The ownership may well still be true; we have lost the ability to say
so. Writing a temporal fact to express an epistemic loss puts a statement into
the graph that a reader would be right to believe and wrong to act on. It
would also leave the edge present for any read path that does not filter on
`valid_to`, which is the acceptance criterion inverted.

Making suppression a rebuild trigger was rejected on amplification: one
ambiguous key would rebuild an organization's whole graph.

So: a `ProjectionTombstone{Kind: "relationship"}`. No contract widening — the
tombstone path, `applyTombstone`'s single `MATCH … DELETE` with the staleness
guard folded into the `WHERE`, and the batch's `Tombstones` slice all already
exist.

## 2. The mechanism

```mermaid
flowchart TD
    subgraph src["ClickHouse — the producer reads BOTH"]
        TPO[("team_project_ownership")]
        PRJ[("projects")]
    end

    TPO --> A["arm A — scope arm<br/>o.project_ref = p.scope<br/>retraction_only = 0"]
    TPO --> B["arm B — key arm<br/>o.project_key = p.scope<br/>WHERE p.scope_kind = 'key'<br/>retraction_only = 0"]
    TPO --> C["arm C — RETRACTION arm<br/>o.project_key = p.project_key<br/>WHERE p.key_resolution_count > 1<br/>retraction_only = 1"]
    PRJ --> A
    PRJ --> B
    PRJ --> C

    A --> W["window layer, per ownership identity<br/>unassertable = retraction_only OR min(project_id) != max(project_id)<br/>row_watermark = greatest(o.updated_at, max(project_updated_at))"]
    B --> W
    C --> W

    W --> G["GROUP BY resolved project_id, provider, team_id, source_name"]
    G --> Q{"countIf(unassertable = 0) = 0 ?"}
    Q -- "no: some row still asserts" --> E["OWNED_BY_TEAM relationship"]
    Q -- "yes: nothing can assert" --> T["relationship tombstone<br/>projectTeamRelationshipID(...)"]

    E --> BATCH["ProjectionBatch"]
    T --> BATCH
    BATCH --> FG["falkorgraph: relationships first, tombstones last"]
```

Three properties carry the design, and each is load-bearing.

### 2.1 One group decides once

`edge_suppressed` is a GROUP property and the relationship id IS the group key
(`provider`, resolved `project_id`, `team_id`, `source_name`). A group with one
asserting row is not suppressed, so **a batch can never assert and retract the
same edge**. That is not a coincidence to be defended by a comment: falkorgraph
applies tombstones AFTER relationships, so a batch holding both for one id
would write the edge and immediately delete it.

Generalising the old `identity_conflict` flag to `unassertable` is what lets
ONE mechanism cover BOTH suppression paths. Fixing only the conflicting-identity
path would have left the older ambiguity hole open — live since v7, and
invisible only because every intervening source-version bump happened to
rebuild.

### 2.2 Arm C exists because the ambiguity path is otherwise invisible

An ambiguous key is filtered out inside the identity expansion, so a key-only
ownership row produces **no result row at all** and the scan has nothing to
retract. Arm C resolves the key across the AMBIGUOUS key partition — one row
per project sharing that key — flagged `retraction_only = 1` so `unassertable`
is forced true and it can never assert.

Those projects are exactly the candidates the edge could have been projected to
while the key was still unambiguous, so tombstoning every one of them retracts
the real edge and no-ops on the rest. That is the same unconditional shape as
the ref-form healing, for the same reason: this producer is backend-neutral and
cannot ask the graph what is there, so it never knows whether a retraction is
"necessary" — and `applyTombstone`'s `DELETE` matching zero rows is what makes
the never-projected case and the re-run case the same no-op.

`retraction_only` is IN the window partition key. Without it, arm C's extra
resolved projects would land inside arm A/B's own `min()`/`max()` comparison and
read as an identity conflict, so an ownership row with a perfectly good
`project_id` would be suppressed the moment its UNUSED `project_key` became
ambiguous — turning a retraction feature into an edge-deletion bug.

### 2.3 The cursor had to grow a `projects` side, or arm C is dead code

The keyset watermark was `max(o.updated_at)` over `team_project_ownership`
alone. But ambiguity usually arrives because a NEW project starts sharing an
existing key — **that writes `projects`, not `team_project_ownership`**. The
ownership row's own `updated_at` never moves, the group stays behind the
checkpoint, and no incremental tick ever re-reads it. The retraction would be
emitted by code that is never reached. The same applies to a conflicting
identity that arrives because some OTHER project's `project_key` changed.

So the watermark is `greatest(the ownership row's own updated_at, the newest
updated_at among every project this ownership row's identity values can
reach)`. The window partition is the ownership row's own identity and the arms
are unioned BEFORE it, so "every project this row can reach" is not a second
query — it is exactly the rows already present.

`projectIdentityWithWatermarkSQL` carries `projects.updated_at` alongside
`readers.ProjectIdentityCatalogSQL`'s expansion. The library expansion is
wrapped in a `SELECT *` subquery before being joined, because its SQL ends
`) AS p` and this producer's own alias must also be `p`
(`ProjectIdentityMatchSQL` hard-codes `= p.scope`); enclosing it keeps the two
`p`s in separate scopes rather than relying on shadowing resolution, which this
file has already lost a live round to on ClickHouse 24.8.

## 3. The one thing that would make this decoration

`applyTombstone` matches on `relationship_id`. A tombstone whose canonical id
differs from the projected edge's **by one byte** deletes nothing, returns no
error, and is counted as applied — so every log, receipt and counter in the
pipeline reports a successful retraction while the stale edge is still there.

`projectTeamRelationshipID` is therefore the single definition, used by the
assertion and the retraction, and the test that guards it does not spell the id
out as a literal: it runs the SAME group twice, once asserting and once
suppressed, and requires the two ids to be equal. Both spellings would have to
be wrong in the same way to pass.

## 4. Telemetry

Counted on the existing `ambiguityLedger`, logged beside the existing
suppression line, under the closed vocabulary `ambiguous_key` /
`conflicting_identity`:

```
devhealthsource tombstoned project ownership edges
  org_id=<hashed> source=dev_health_teams_projects
  ownership_edge_tombstones_ambiguous_key=N
  ownership_edge_tombstones_conflicting_identity=M
```

Two naming decisions are deliberate:

- **A separate line from `logConflictingIdentities`**, which says an ownership
  was NOT ASSERTED. This one says an ownership that HAD been asserted was TAKEN
  BACK. Folding them together would restore the exact ambiguity this change
  removes.
- **"tombstones", not "retracted edges".** Retractions are emitted
  unconditionally (§2.2), so the number is an UPPER BOUND on what was removed.
  A count that claims more than it can support reads as coverage.

Counts and the closed reason vocabulary only — never a project id, key, team id
or row key, the same budget as its siblings.

## 5. Source version v8 → v9, and what the rebuild is for

The mechanism only reaches an edge whose group crosses the checkpoint. An edge
that went stale BEFORE this deploy went stale without moving its group's
watermark — which is precisely why nothing retracted it. **Those already-live
stale edges are unreachable to the mechanism that removes them.**

The version bump is what clears them, once per organization, exactly as v7 → v8
paid it. Note what it does NOT mean: the rebuild is not how retraction works, it
is how the pre-existing backlog is drained. CHAOS-4565's acceptance — *no
rebuild is required, and no checkpoint is reset* — is about the steady state
after this deploy, and that path is rebuild-free.

## 6. What this slice does not do

- It does not tell you whether a tombstone actually removed an edge. The
  producer cannot see the graph; the receipt's `TombstonesApplied` counts
  tombstones sent, not rows deleted. Closing that would need a backend-side
  signal from `applyTombstone`, which no other tombstone in this pipeline has
  either.
- It does not attribute an omission to a specific ownership row for the
  ambiguity path — CHAOS-4566 carries that, and its absence is why the catalog
  ambiguity count is a fact about the catalog rather than a claim about edges.
- It does not change `BELONGS_TO_PROJECT`, `queryWorkItemTeams`, or the team
  authorization scope. Only the project↔team ownership producer suppresses, so
  only it needs to retract.
