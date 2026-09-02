package devhealthsource

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Property test for the candidate pipeline this change set introduced:
// per-item quarantine -> orphaned-dependent sweep -> batch-identity dedup.
//
// WHY THIS EXISTS RATHER THAN MORE FIXTURES. Those three passes produced FIVE
// real defects across three adversarial rounds and the lane's own review:
// orphan stub entities, an all-quarantined tail that stalled the cursor,
// duplicate relationship ids, entity-subject uniqueness missed by a
// per-instance sweep, and a pass-ORDERING interaction that lost the valid stub
// of a shared target. Every one was found by constructing the single input
// that broke it, and every fix was verified by a fixture aimed at that same
// input. That is the shape that keeps producing defect number six: a
// hand-built primitive re-derived one case at a time.
//
// So the passes are pinned here by their INVARIANTS over GENERATED inputs
// instead. The assertions run after the WHOLE pipeline and are indifferent to
// pass order, which is exactly the property the fifth defect violated -- an
// ordering bug cannot hide behind an assertion that only inspects one pass.
//
// A failure prints its seed; re-running with that seed reproduces the case.
const pipelineInvariantCases = 400

type generatedCase struct {
	candidates []candidate
	// dependents maps a stub entity's canonical id to the relationship id it
	// exists to serve, so the test can check the sweep without reading the
	// production code's own bookkeeping.
	dependents map[string]string
}

func generateCandidates(rng *rand.Rand) generatedCase {
	observed := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	valid := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	beyondGo := time.Date(2299, 12, 31, 0, 0, 0, 0, time.UTC) // past Go's epoch-nanosecond range
	scope := contractsv1.ContextFabricAuthorizationScope{RepositorySlugs: []string{"acme/svc"}}

	types := []contractsv1.ContextFabricRelationshipType{
		contractsv1.ContextFabricRelationshipBlocks,
		contractsv1.ContextFabricRelationshipRelatesTo,
		contractsv1.ContextFabricRelationshipDuplicates,
		"EXTERNAL_ISSUE_KEY", // outside the vocabulary
		"NOT_A_REAL_TYPE",    // outside the vocabulary
	}

	// Resolution is decided PER TARGET, not per row, because that is what the
	// producer does: whether a target resolves is a property of the target
	// work item, read through the same LEFT JOIN for every row naming it. An
	// earlier generator chose per row, which let one batch both ASSERT a
	// ref-form edge and emit the healing tombstone that retracts it -- a
	// combination the producer cannot emit, and one the contract deliberately
	// rejects rather than silently resolving (see
	// validateProjectionRelationshipTombstoneCollision's own comment: a loud
	// wedge is not the worst outcome available). Generating impossible inputs
	// there would have argued for a guard the contract authors explicitly
	// refused.
	resolved := map[string]bool{}
	for i := 0; i < 3; i++ {
		resolved[fmt.Sprintf("T-%d", i)] = rng.Intn(2) == 0
	}

	out := generatedCase{dependents: map[string]string{}}
	n := 3 + rng.Intn(8)
	for i := 0; i < n; i++ {
		at := observed.Add(time.Duration(rng.Intn(5)) * time.Second)
		// Deliberately a SMALL id space so pairs, spellings and targets
		// collide often -- a fixture whose identifiers are all distinct hides
		// every aliasing defect, which is how three of the five known defects
		// were missed.
		target := fmt.Sprintf("T-%d", rng.Intn(3))
		source := fmt.Sprintf("S-%d", rng.Intn(3))
		relType := types[rng.Intn(len(types))]
		sortKey := source + ":" + target

		label := "item " + target
		if rng.Intn(6) == 0 {
			label += " " // untrimmed: the contract refuses it
		}
		// The ref-form producer builds the stub entity and its edge from the
		// SAME refSubject, observedAt, window, evidence and authorization
		// (tables.go's ref-form branch), so they cannot fail validation
		// independently. TestRefStubAndItsEdgeShareAValidationFate pins that
		// coupling, so if a future field breaks it this assumption fails
		// loudly instead of silently going stale.
		observedForPair := at
		if rng.Intn(7) == 0 {
			observedForPair = beyondGo // unrepresentable instant, for BOTH
		}
		validFrom := valid

		refID := "work_item_ref:" + target
		refRelID := "relationship.v2:gen:ref:" + source + ":" + target + ":" + string(relType)
		resolvedRelID := "relationship.v2:gen:res:" + source + ":" + target + ":" + string(relType)

		refSubject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItemRef, CanonicalID: refID, Label: label}
		fromSubject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + source, Label: source}

		newEdge := func(id string, to contractsv1.ContextFabricSubjectRef) *contractsv1.ContextFabricRelationshipProjection {
			return &contractsv1.ContextFabricRelationshipProjection{
				RelationshipID: id, Type: relType, From: fromSubject, To: to,
				Derivation:      contractsv1.ContextFabricDerivationCanonicalStructured,
				EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
				Authorization:   scope, EvidenceRefIDs: []string{"acr:v1:gen:" + source + ":" + target},
				ObservedAt: observedForPair, ValidFrom: &validFrom, SourceVersion: "v1",
			}
		}

		if !resolved[target] {
			// Ref-form: a stub entity plus the edge it exists to serve.
			stub := contractsv1.ContextFabricEntityProjection{
				Subject: refSubject, Authorization: scope,
				EvidenceRefIDs: []string{"acr:v1:gen:" + source + ":" + target},
				ObservedAt:     observedForPair, ValidFrom: &validFrom, SourceVersion: "v1",
			}
			out.candidates = append(out.candidates,
				candidate{observedAt: at, sortKey: sortKey, entity: &stub, supports: refRelID},
				candidate{observedAt: at, sortKey: sortKey, relationship: newEdge(refRelID, refSubject)})
			out.dependents[refID] = refRelID
			continue
		}

		// Resolved: the real edge, plus the healing tombstones that retire the
		// ref-form ids this row WOULD have minted. The asserted id and the
		// tombstoned id are different by construction, exactly as in tables.go.
		out.candidates = append(out.candidates, candidate{observedAt: at, sortKey: sortKey,
			relationship: newEdge(resolvedRelID, contractsv1.ContextFabricSubjectRef{
				Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + target, Label: label})})
		effective := at
		if rng.Intn(8) == 0 {
			effective = beyondGo
		}
		out.candidates = append(out.candidates, candidate{observedAt: at, sortKey: sortKey,
			tombstone: &contractsv1.ContextFabricProjectionTombstone{
				Kind: "relationship", CanonicalID: refRelID,
				Reason: "healing the ref-form edge", EffectiveAt: effective, SourceVersion: "v1",
			}})
	}
	sortCandidates(out.candidates)
	return out
}

func TestPipelineInvariantsOverGeneratedCandidates(t *testing.T) {
	t.Parallel()
	for i := 0; i < pipelineInvariantCases; i++ {
		seed := int64(i)
		rng := rand.New(rand.NewSource(seed))
		gen := generateCandidates(rng)

		var observations []quarantineObservation
		kept := partitionProjectableCandidates(gen.candidates, func(o quarantineObservation) {
			observations = append(observations, o)
		})

		fail := func(format string, args ...any) {
			t.Fatalf("SEED=%d: "+format, append([]any{seed}, args...)...)
		}

		// (d) every dropped item is observed exactly once, with a reason from
		// the CLOSED vocabulary. Counting rather than inspecting is what makes
		// a silent drop impossible to miss.
		if want := len(gen.candidates) - len(kept); len(observations) != want {
			fail("observations = %d, want %d (one per dropped item, never silent)", len(observations), want)
		}
		closed := map[string]bool{
			quarantineUnknownRelationshipType: true, quarantineUntrimmedLabel: true,
			quarantineInvertedWindow: true, quarantineOversizeScalar: true,
			quarantineUnrepresentableInstant: true, quarantineContractBoundViolation: true,
			quarantineOrphanedDependent: true, quarantineDuplicateWithinBatch: true,
		}
		for _, o := range observations {
			if !closed[o.Reason] {
				fail("quarantine reason %q is outside the closed vocabulary", o.Reason)
			}
			if o.Kind == "" {
				fail("observation with reason %q carries no item kind", o.Reason)
			}
		}

		keptRelIDs := map[string]struct{}{}
		keptEntityIDs := map[string]struct{}{}
		for _, c := range kept {
			switch {
			case c.relationship != nil:
				keptRelIDs[c.relationship.RelationshipID] = struct{}{}
			case c.entity != nil:
				keptEntityIDs[c.entity.Subject.CanonicalID] = struct{}{}
			}
		}

		// (b) nothing survives that exists only to support something dropped.
		for _, c := range kept {
			if c.supports == "" {
				continue
			}
			if _, ok := keptRelIDs[c.supports]; !ok {
				fail("a candidate supporting dropped relationship %q survived", c.supports)
			}
		}

		// (a) every surviving relationship pointing at a ref-stub subject has
		// that subject projected as a REAL entity carrying its validity
		// window -- not left to the backend's implicit stub, which asserts no
		// window and is admitted at every requested time.
		for _, c := range kept {
			if c.relationship == nil || c.relationship.To.Kind != contractsv1.ContextFabricSubjectWorkItemRef {
				continue
			}
			if _, ok := keptEntityIDs[c.relationship.To.CanonicalID]; !ok {
				fail("relationship %q survived but its ref-stub endpoint %q was not projected",
					c.relationship.RelationshipID, c.relationship.To.CanonicalID)
			}
		}

		if !carriesPayload(kept) {
			continue // nothing publishable; buildBatch is not reached
		}

		// (c) all four batch-level uniqueness rules hold, checked by the
		// contract's OWN validator rather than by restating them here.
		batch, err := buildBatch("org-1", SourceName, ClickHouseSourceVersion, "", gen.candidates, kept, false, false, time.Unix(1, 0).UTC())
		if err != nil {
			fail("buildBatch rejected a fully-swept candidate set: %v", err)
		}
		if err := batch.Validate(); err != nil {
			fail("built batch fails contract validation: %v", err)
		}
		// The cursor comes from every candidate CONSUMED, never the survivors.
		last := gen.candidates[len(gen.candidates)-1]
		wantCursor, encErr := encodeCursor(cursorState{Since: last.observedAt, After: last.sortKey})
		if encErr != nil {
			fail("encode cursor: %v", encErr)
		}
		if batch.NextCursor != wantCursor {
			fail("NextCursor was derived from the survivors, not from every consumed candidate")
		}
	}
}

// TestPipelineInvariantsReachTheirAssertions guards the property test itself:
// a generator that stopped producing quarantinable or colliding inputs would
// leave every assertion above trivially satisfied and the suite would stay
// green while proving nothing. Same family as the vacuous-assertion trap that
// already bit this lane once.
func TestPipelineInvariantsReachTheirAssertions(t *testing.T) {
	t.Parallel()
	var dropped, duplicates, orphans, stubs, published int
	for i := 0; i < pipelineInvariantCases; i++ {
		gen := generateCandidates(rand.New(rand.NewSource(int64(i))))
		var obs []quarantineObservation
		kept := partitionProjectableCandidates(gen.candidates, func(o quarantineObservation) { obs = append(obs, o) })
		dropped += len(obs)
		for _, o := range obs {
			switch o.Reason {
			case quarantineDuplicateWithinBatch:
				duplicates++
			case quarantineOrphanedDependent:
				orphans++
			}
		}
		for _, c := range kept {
			if c.entity != nil && strings.HasPrefix(c.entity.Subject.CanonicalID, "work_item_ref:") {
				stubs++
			}
		}
		if carriesPayload(kept) {
			published++
		}
	}
	for name, count := range map[string]int{
		"dropped items": dropped, "duplicate_within_batch": duplicates,
		"orphaned_dependent": orphans, "surviving ref stubs": stubs, "publishable sets": published,
	} {
		if count == 0 {
			t.Fatalf("the generator produced ZERO %s across %d cases: the invariant assertions never exercised that path and are proving nothing", name, pipelineInvariantCases)
		}
	}
	t.Logf("reach: dropped=%d duplicates=%d orphans=%d stubs=%d publishable=%d over %d cases",
		dropped, duplicates, orphans, stubs, published, pipelineInvariantCases)
}

// TestRefStubAndItsEdgeShareAValidationFate pins the coupling the property
// test's generator relies on, and that invariant (a) holds by.
//
// The ref-form producer builds the stub entity and its edge from the SAME
// refSubject, observedAt, validity window, evidence refs and authorization,
// so every bound the entity can breach the edge breaches too: the edge
// validates that same subject as its To endpoint. A surviving edge therefore
// implies a valid stub, which is why a ref-stub endpoint can never be missing
// for a reason other than being dropped by a pipeline pass.
//
// That is currently true by SHARED FIELDS, not by construction -- add a
// property to the stub alone and it stops holding silently. This test is what
// makes that stop being silent.
func TestRefStubAndItsEdgeShareAValidationFate(t *testing.T) {
	t.Parallel()
	observed := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	valid := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	beyondGo := time.Date(2299, 12, 31, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.ContextFabricAuthorizationScope{RepositorySlugs: []string{"acme/svc"}}

	build := func(label string, at time.Time) (contractsv1.ContextFabricEntityProjection, contractsv1.ContextFabricRelationshipProjection) {
		refSubject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItemRef, CanonicalID: "work_item_ref:T-1", Label: label}
		validFrom := valid
		stub := contractsv1.ContextFabricEntityProjection{
			Subject: refSubject, Authorization: scope, EvidenceRefIDs: []string{"acr:v1:gen:S-1:T-1"},
			ObservedAt: at, ValidFrom: &validFrom, SourceVersion: "v1",
		}
		edge := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: "relationship.v2:gen:S-1:T-1", Type: contractsv1.ContextFabricRelationshipBlocks,
			From:            contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:S-1", Label: "S-1"},
			To:              refSubject,
			Derivation:      contractsv1.ContextFabricDerivationCanonicalStructured,
			EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
			Authorization:   scope, EvidenceRefIDs: []string{"acr:v1:gen:S-1:T-1"},
			ObservedAt: at, ValidFrom: &validFrom, SourceVersion: "v1",
		}
		return stub, edge
	}

	cases := []struct {
		name  string
		label string
		at    time.Time
	}{
		{"well formed", "T-1", observed},
		{"untrimmed label", "T-1 ", observed},
		{"unrepresentable instant", "T-1", beyondGo},
		{"oversize label", strings.Repeat("t", 513), observed},
	}
	checked := 0
	for _, tc := range cases {
		stub, edge := build(tc.label, tc.at)
		stubOK, edgeOK := stub.Validate() == nil, edge.Validate() == nil
		if stubOK != edgeOK {
			t.Fatalf("%s: stub valid=%v but edge valid=%v -- the pair no longer shares a validation fate, so an edge can outlive its endpoint and the backend's implicit stub (no validity window, admitted at every time) takes its place",
				tc.name, stubOK, edgeOK)
		}
		checked++
	}
	if checked != len(cases) {
		t.Fatalf("only %d of %d cases reached the assertion", checked, len(cases))
	}
}
