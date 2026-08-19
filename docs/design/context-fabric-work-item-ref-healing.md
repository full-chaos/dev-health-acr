# Context Fabric: work_item_ref stub + relationship.v2 healing (CHAOS-3898 §1.5, S2b)

Short design note. Covers the two closures design brief §1.5 (P5) requires,
the decisions made implementing them, and what this slice deliberately does
not do. See `.remember/chaos3898-design-brief.md` v4.1 §1.5 for the full
verified-defect writeup this implements; this note is the "what we actually
built and why" companion, not a restatement of the defect analysis.

## 0. The two closures

1. **Relationship identity versioned by canonical endpoint identities.**
   `relationship.v2:<family>:<hex(sha256(enc(from)+":"+enc(to)+":"+enc(type)))[:32]>`
   replaces the pre-CHAOS-3898 `relationship:work_item_dependency:<repo>:<src>:<tgt>:<type>`
   shape for `work_item_dependency`/`work_item_hierarchy` edges. An edge
   between different endpoints is now structurally a different relationship
   id, closing the collision class where a healed (resolved) edge and a
   stranded ref-form (unresolved) edge could collide on `relationship_id`'s
   unique constraint or silently overwrite one another.
2. **A non-authoritative `work_item_ref` stub subject kind** for a
   `work_item_dependency` target that does not resolve to a real
   `work_items` row at projection time (a cross-system reference such as
   `ghpr:owner/repo#N` — see `queryWorkItemDependencies`'s own doc comment).
   Heals deterministically: once the same raw target id later resolves, the
   producer derives what the ref-form ids WOULD have been (from the same row
   data) and tombstones both, in the same batch as the resolved edge.

`work_item_hierarchy` does not need either of the above beyond the id
scheme: its parent join is `INNER`, so an unresolved parent can never
produce a candidate row at all — there is no ref-stub case there.

## 1. The relationship id: digest, not embedded pair

The obvious literal reading of the design brief's
`relationship.v2:<family>:<enc(from)>:<enc(to)>:<enc(type)>` formula embeds
both full endpoint canonical ids verbatim. Two `identity.Derive`-produced
endpoint ids can each independently reach `identity.MaxNaturalKeyBytes`
(256 bytes) on their own, so a worst-case pair overflows any fixed
`RelationshipID` contract bound (`8–256` UTF-8 code points, unchanged by
this slice).

Two options were on the table:

- **Widen the contract bound** (considered, then rejected on chris's
  ruling): raising `RelationshipID`'s max length to accommodate the
  worst-case pair keeps a real whole-row-omit failure class alive for an
  already-rare edge case (an oversize id still forces a choice between
  refusing the row or truncating it — never done here). The exact
  `600`-code-point number floated during design was itself flagged as the
  precise code-point-vs-bytes slip H6 exists to prevent — a signal the
  widening path is trap-prone, not just unnecessary.
- **Digest the endpoint pair + type** (what shipped): a SHA-256 digest of
  `enc(from) + ":" + enc(to) + ":" + enc(type)`, truncated to the first 32
  hex characters (16 bytes), gives a FIXED-length id regardless of
  endpoint length. `identity.DeriveRelationship` takes no ledger and
  returns no `omitted` flag — the whole-row-omit failure class is
  structurally unconstructible, not merely rare. This mirrors the
  established house pattern: `falkorgraph`'s own `graphKey` is
  `prefix + sha256(org)` for the identical reason (variable-length
  identity embedded in a bounded key). Collision risk reduces to SHA-256
  collision (negligible at this cardinality), provided the digest INPUT is
  injective per `(from, to, type)` — which it is, because each component is
  passed through the registry's own `EncodeSegment`/`JoinSegments` closure
  before hashing, the same codec `identity.Derive`'s own id uses, so an
  unescaped `:` inside an endpoint id can never manufacture a false
  top-level-separator collision (`identity/relationship_test.go`'s
  `TestDeriveRelationshipRespectsUnescapedColonBoundary` pins this
  directly: `("A:B", "C", "x")` and `("A", "B:C", "x")` do not collide).

`RelationshipFamily` (`work_item_dependency` / `work_item_hierarchy`) is a
plain string PREFIX, deliberately outside the hashed input — two producers
deriving from coincidentally identical endpoint/type inputs must still land
on different ids (`TestDeriveRelationshipDistinguishesFamily`).

## 2. The work_item_ref stub

`identity.DeriveWorkItemRef(raw string) -> "work_item_ref:<enc(raw)>"` — a
single segment, deliberately WITHOUT the `.v2:` marker every
`identity.Derive`-produced id carries: it names no resolved row at all, so
there is no natural key to derive FROM, only a raw string to encode. It is
NOT registered in `identity.Registry` (`Lookup`/`Derive`/`Segments` never
recognize it) — it is a distinct `SubjectKind`
(`contractsv1.ContextFabricSubjectWorkItemRef`), added as the one closed
Go switch (`validContextFabricSubjectKind`) plus the 13 duplicated JSON
Schema enum sites the contract-first rule requires (see the S2b PR
description for the exact file list — the same footprint shape CHAOS-3802's
`BELONGS_TO_PROJECT`/`OWNED_BY_TEAM` addition established as precedent).

The stub is:

- **Repo-scoped to the SOURCE row's own authorization**, not the (unknown)
  target's — it is reachable only through the edge that revealed it, so it
  can never be visible more broadly than that row already is.
- **Never fact-eligible** — no `devhealthfacts` capability allowlist
  includes it (every allowlist there is explicit, and none were touched).
- **Never censused** — the census registry's anchor-kind map was not
  extended to include it.
- **Deliberately NOT embedded for vector retrieval today** — a bare raw
  label carries little useful search-text signal, and `embedKindSkipped`'s
  membership is a COMPOSITION decision (CHAOS-3833 spec §4 Layer B) that
  rides `embedTextTemplateVersion` like any template change, forcing a
  rebuild for every vector-enabled organization. Left as a disclosed,
  deliberate omission rather than risking an improperly-triggered template
  bump; a future slice can add it if the corpus shows it matters.

## 3. Tombstone healing: one genuinely new mechanism, one that already existed

`falkorgraph.Adapter.applyTombstone` already handled `Kind: "relationship"`
(or `"edge"`) — an unconditional `DELETE r` by `relationship_id` — before
this slice. That was NOT a gap; research during S2b planning confirmed it,
correcting an earlier assumption. `queryWorkItemDependencies` uses it
unmodified: whenever a row's target resolves, it derives what the ref-form
relationship id WOULD have been (from the same row data) and tombstones it,
unconditionally, idempotent whether or not it was ever actually minted.

The genuinely new mechanism is the **conditional** stub-node cleanup. The
node tombstone (`Kind: "work_item_ref"`, `CanonicalID` = the stub's own id)
depends ONLY on the raw target id — never source or row — so it must be
deleted only when NO edge still references it: a DIFFERENT, still-unresolved
row may legitimately hold its own edge to the same raw target. `applyTombstone`
gained a new case:

```cypher
MATCH (n:Subject {org_id:$org, kind:$kind, canonical_id:$id})
WHERE (n.observed_at_ns IS NULL OR n.observed_at_ns <= $effectiveNs)
  AND NOT (n)--()
DELETE n
```

Plain `DELETE`, not `DETACH DELETE` (every other subject-tombstone kind
uses `DETACH DELETE` unconditionally) — deliberate defense in depth.
FalkorDB (matching Neo4j Cypher semantics) refuses to `DELETE` a node that
still has relationships, so a bug in the `NOT (n)--()` guard fails loudly
here instead of silently detaching and deleting a node another edge still
points at.

Two devhealthsource rows resolving the SAME previously-unresolved target in
one page (the CHAOS-3779 two-relationship-type shape: `blocks` AND
`relates_to` between the same pair, or two different sources depending on
the same target) would otherwise derive the IDENTICAL node tombstone twice,
which `ContextFabricProjectionBatch.Validate()` rejects as a duplicate. A
local `seenNodeTombstones` set — plain, fresh on every
`queryWorkItemDependencies` call, since the function itself is invoked once
per tick/page and is never a persistent closure — dedupes exactly that; the
edge tombstone needs no such dedup, already unique per `(source, target,
type)`.

Verified live (real FalkorDB via testcontainers,
`chaos3898_work_item_ref_tombstone_live_test.go`): a stub referenced by two
edges survives healing ONE of them, and is deleted only once BOTH are
healed. Confirmed as a real regression test by temporarily reverting the
orphan-check guard to unconditional `DETACH DELETE` and observing the test
fail exactly where expected.

## 4. What this slice does not do

- **cf_work_item_ref_stubs** (§5b: created/healed/tombstone-applied
  counters + live-stub gauge) is not wired. The two other §5b signals this
  ticket owns (`flip_during_investigation`, `cf_binding_epoch_delta`) ARE
  wired (`EngineTelemetry.RecordBindingEpochDelta`) — a Class-A-adjacent
  signal with a natural home on the existing `EngineTelemetry` interface.
  `cf_work_item_ref_stubs` would need either a new devhealthsource-level
  telemetry sink (none exists there today beyond a plain logger) or
  extending `falkorgraph.GraphTelemetry` (a real interface-wiring cost
  across every implementer and test fake) — flagged as follow-up work
  rather than rushed.
- **Golden fixture examples** exercising `work_item_ref`/`relationship.v2`
  content are not added to `contracts/examples/v1/*.json`. The JSON Schema
  enum widening itself is exercised by `make contract-test` (schema
  validity) and this slice's own Go-level tests (actual production
  behavior); illustrative example CONTENT is a separate, lower-priority
  follow-up.
