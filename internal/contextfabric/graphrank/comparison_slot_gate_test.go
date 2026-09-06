package graphrank

// ONE OPERAND SLOT, IN ISOLATION -- the unit that answers "did the gate refuse,
// or did the wiring never reach it".
//
// WHY THIS FILE EXISTS, stated because the reason is a debugging lesson worth
// keeping. The end-to-end battery reported `committed = []` for a comparison
// whose two operands were each retrieved with confidence 1 and a MatchExact
// mechanism. That single fact is consistent with at least three different
// defects -- retrieval never reaching the slots, the per-slot gate declining a
// lone exact match, or publication holding a pair that had in fact resolved --
// and an end-to-end fixture cannot tell them apart, because it can only see
// what came out of the far end.
//
// This file drives ONE slot with a hand-built dependency stub and a RECORDING
// TRACER, so the gate states which branch it took in its own words rather than
// leaving it to be inferred from the outcome.

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// recordingTracer captures every ResolutionTraceEvent the resolver emits.
type recordingTracer struct{ events []ResolutionTraceEvent }

func (r *recordingTracer) Trace(event ResolutionTraceEvent) { r.events = append(r.events, event) }

// decisions returns the decision-stage events, which are the ones that name
// the commit gate that fired (or that nothing did).
func (r *recordingTracer) decisions() []ResolutionTraceEvent {
	var out []ResolutionTraceEvent
	for _, event := range r.events {
		if event.Stage == "decision" {
			out = append(out, event)
		}
	}
	return out
}

// exactMatchNode is a node whose label EQUALS the term, which is what makes it
// an exact-label match, and whose authorization attribute admits every
// principal so authorization can never be the reason a slot came back empty.
func exactMatchNode(kind contextfabric.SubjectKind, canonicalID, label string) CandidateNode {
	return CandidateNode{
		UUID: canonicalID,
		Name: label,
		Attributes: map[string]interface{}{
			"subject_kind":               string(kind),
			"canonical_id":               canonicalID,
			"label":                      label,
			"authorization_repositories": "*",
		},
		Mechanism: contextfabric.MatchLexical,
	}
}

// slotGateDeps is the smallest ResolveDeps that can carry one slot's
// retrieval: a Search keyed on the term, and the two callbacks the merge path
// requires. Everything else is left nil, which is the point -- the census,
// the evidence round and the question pass are NOT wired here, because a
// comparison slot must not reach any of them.
func slotGateDeps(byTerm map[string][]CandidateNode, tracer ResolutionTracer) ResolveDeps {
	return ResolveDeps{
		Search: func(_ context.Context, term string, _ int) ([]CandidateNode, bool, bool, error) {
			return byTerm[term], false, false, nil
		},
		Traverse: func(context.Context, string, CandidateNode, bool) (contextfabric.SubjectCandidate, ObservationTraversal) {
			return contextfabric.SubjectCandidate{}, ObservationNoParent
		},
		IsInternal:       func(contextfabric.SubjectRef) bool { return false },
		ResolutionTracer: tracer,
	}
}

func slotGateRequest() contextfabric.InvestigationRequest {
	return contextfabric.InvestigationRequest{
		RequestID: "request_slot_gate",
		Question:  "compare alpha and beta",
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
}

// TestOneOperandSlotCommitsItsLoneExactMatch is the isolating arm.
//
// One operand, one term, one node whose label equals that term. Nothing else
// competes and nothing else is wired. If this slot does not commit, the defect
// is in the per-slot gate invocation and NOT in the dispatch, the engine, or
// publication -- and the recorded decision event says which branch the gate
// took, so the next question does not have to be guessed either.
func TestOneOperandSlotCommitsItsLoneExactMatch(t *testing.T) {
	t.Parallel()

	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_alpha", Label: "alpha"}
	tracer := &recordingTracer{}
	deps := slotGateDeps(map[string][]CandidateNode{
		"alpha": {exactMatchNode(contextfabric.SubjectTeam, "team_alpha", "alpha")},
	}, tracer)

	slot := contextfabric.ComparisonOperandSlot{
		Position: 0, Variant: contextfabric.ComparisonOperandNamed,
		Kind: contextfabric.SubjectTeam, Terms: []string{"alpha"},
	}

	run, err := resolveOneOperandSlot(context.Background(), storage.Principal{OrgID: "org-1"}, slotGateRequest(), deps, slot)
	if err != nil {
		t.Fatalf("resolveOneOperandSlot() error = %v", err)
	}

	// FIXTURE CONTROL FIRST. If the candidate is not in the slot's pool, the
	// arm is measuring retrieval, not the gate.
	if len(run.candidates) != 1 {
		t.Fatalf("slot candidates = %d, want 1 -- retrieval did not reach this slot, so nothing below measures the gate", len(run.candidates))
	}
	candidate := run.candidates[0]
	if candidate.Confidence != 1 || !HasMechanism(candidate.MatchMechanisms, contextfabric.MatchExact) {
		t.Fatalf("slot candidate = conf %.2f mechanisms %v, want confidence 1 with an exact mechanism -- the gate's exact tier requires both, so a fixture that produces neither cannot exercise it",
			candidate.Confidence, candidate.MatchMechanisms)
	}

	// THE PROPERTY.
	if len(run.committed) != 1 {
		t.Errorf("slot committed = %v (%d), want exactly the lone exact match %s.\ndecision events = %#v",
			run.committed, len(run.committed), SubjectKey(subject), tracer.decisions())
	}
	if !run.resolved() {
		t.Errorf("slot state = %q, want %q", run.state(), operandSlotResolved)
	}
}

// TestOneOperandSlotSeesOnlyItsOwnTerms is the isolation property at the unit,
// and it is the one an end-to-end fixture proves only circumstantially.
//
// The stub answers a DIFFERENT term with a different subject. A slot that
// searched anything other than its own terms would pick it up.
func TestOneOperandSlotSeesOnlyItsOwnTerms(t *testing.T) {
	t.Parallel()

	tracer := &recordingTracer{}
	deps := slotGateDeps(map[string][]CandidateNode{
		"alpha": {exactMatchNode(contextfabric.SubjectTeam, "team_alpha", "alpha")},
		"beta":  {exactMatchNode(contextfabric.SubjectTeam, "team_beta", "beta")},
	}, tracer)

	slot := contextfabric.ComparisonOperandSlot{
		Position: 0, Variant: contextfabric.ComparisonOperandNamed,
		Kind: contextfabric.SubjectTeam, Terms: []string{"alpha"},
	}
	run, err := resolveOneOperandSlot(context.Background(), storage.Principal{OrgID: "org-1"}, slotGateRequest(), deps, slot)
	if err != nil {
		t.Fatalf("resolveOneOperandSlot() error = %v", err)
	}

	for _, candidate := range run.candidates {
		if candidate.Subject.CanonicalID == "team_beta" {
			t.Errorf("the slot for term %q retrieved %s, which only the OTHER operand's term reaches -- slot term isolation is the whole mechanism and it is not holding",
				slot.Terms[0], SubjectKey(candidate.Subject))
		}
	}
	if len(run.candidates) == 0 {
		t.Fatal("the slot retrieved nothing at all, so this arm cannot distinguish isolation from a dead fixture")
	}
}
