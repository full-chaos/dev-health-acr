package graphrank

import (
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func candidateFor(kind contextfabric.SubjectKind, canonicalID string) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		ReceiptID:    "receipt-" + canonicalID,
		Subject:      contextfabric.SubjectRef{Kind: kind, CanonicalID: canonicalID, Label: canonicalID},
		State:        contextfabric.ResolutionProposed,
		MatchReasons: []string{"Exact canonical subject hint matched the organization graph."},
		Confidence:   0.9,
	}
}

// TestSurvivorsFirstOrder_EliminatesOnCountZero pins design brief §1.5's
// "the census proved ZERO satisfiers exist -- pool \ satisfiers = every
// pooled candidate of that kind."
func TestSurvivorsFirstOrder_EliminatesOnCountZero(t *testing.T) {
	t.Parallel()
	survivor := candidateFor(contextfabric.SubjectWorkItem, "widget-1")
	eliminated := candidateFor(contextfabric.SubjectPullRequest, "pull_request:repo-1:1")
	candidates := []contextfabric.SubjectCandidate{eliminated, survivor}
	attestation := Attestation{Kinds: []KindAttestation{
		{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 0},
	}}
	got := SurvivorsFirstOrder(candidates, attestation)
	if len(got) != 2 || got[0].Subject.CanonicalID != survivor.Subject.CanonicalID || got[1].Subject.CanonicalID != eliminated.Subject.CanonicalID {
		t.Fatalf("SurvivorsFirstOrder = %#v, want [survivor, eliminated]", got)
	}
}

// TestSurvivorsFirstOrder_EliminatesNonMatchingSingleWitness pins the
// Count==1 decisive-path comparison.
func TestSurvivorsFirstOrder_EliminatesNonMatchingSingleWitness(t *testing.T) {
	t.Parallel()
	survivor := candidateFor(contextfabric.SubjectPullRequest, "pull_request:repo-1:1")
	eliminated := candidateFor(contextfabric.SubjectPullRequest, "pull_request:repo-1:2")
	candidates := []contextfabric.SubjectCandidate{eliminated, survivor}
	attestation := Attestation{Kinds: []KindAttestation{
		{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 1, SatisfierCanonicalID: "pull_request:repo-1:1"},
	}}
	got := SurvivorsFirstOrder(candidates, attestation)
	if len(got) != 2 || got[0].Subject.CanonicalID != survivor.Subject.CanonicalID || got[1].Subject.CanonicalID != eliminated.Subject.CanonicalID {
		t.Fatalf("SurvivorsFirstOrder = %#v, want [survivor, eliminated]", got)
	}
}

// TestSurvivorsFirstOrder_EliminatesOutsideSatisfierSet pins the
// 2<=Count<=CensusBudget non-decisive enrichment path.
func TestSurvivorsFirstOrder_EliminatesOutsideSatisfierSet(t *testing.T) {
	t.Parallel()
	survivor1 := candidateFor(contextfabric.SubjectPullRequest, "pull_request:repo-1:1")
	survivor2 := candidateFor(contextfabric.SubjectPullRequest, "pull_request:repo-1:2")
	eliminated := candidateFor(contextfabric.SubjectPullRequest, "pull_request:repo-1:3")
	candidates := []contextfabric.SubjectCandidate{eliminated, survivor1, survivor2}
	attestation := Attestation{Kinds: []KindAttestation{
		{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 2, SatisfierCanonicalIDs: []string{"pull_request:repo-1:1", "pull_request:repo-1:2"}},
	}}
	got := SurvivorsFirstOrder(candidates, attestation)
	if len(got) != 3 {
		t.Fatalf("SurvivorsFirstOrder returned %d candidates, want 3", len(got))
	}
	if got[2].Subject.CanonicalID != eliminated.Subject.CanonicalID {
		t.Fatalf("last candidate = %q, want the eliminated one (%q)", got[2].Subject.CanonicalID, eliminated.Subject.CanonicalID)
	}
	// Survivors keep their ORIGINAL relative order (stable sort): survivor1
	// appeared before survivor2 in the input.
	if got[0].Subject.CanonicalID != survivor1.Subject.CanonicalID || got[1].Subject.CanonicalID != survivor2.Subject.CanonicalID {
		t.Fatalf("survivor order = [%q, %q], want original relative order preserved", got[0].Subject.CanonicalID, got[1].Subject.CanonicalID)
	}
}

// TestSurvivorsFirstOrder_NonCensusedKindNeverEliminated is the literal
// acceptance pin: "non-censused kinds never eliminated."
func TestSurvivorsFirstOrder_NonCensusedKindNeverEliminated(t *testing.T) {
	t.Parallel()
	// SubjectProject is not in the closed census-kind registry.
	candidate := candidateFor(contextfabric.SubjectProject, "project.v2:github:p-1")
	attestation := Attestation{Kinds: []KindAttestation{
		// Even a Count==0 attestation for a DIFFERENT kind must not spill
		// over onto a non-censused one.
		{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 0},
	}}
	got := SurvivorsFirstOrder([]contextfabric.SubjectCandidate{candidate}, attestation)
	if len(got) != 1 || got[0].Subject.CanonicalID != candidate.Subject.CanonicalID {
		t.Fatalf("SurvivorsFirstOrder = %#v, want the non-censused candidate untouched", got)
	}
}

// TestSurvivorsFirstOrder_ClosureMismatchGoesNeutral pins chris's ruling
// for BOTH closure disciplines: the existing decisive-path ClosureMismatch
// and the new SatisfierSetClosureMismatch each demote their kind to
// neutral, never eliminate.
func TestSurvivorsFirstOrder_ClosureMismatchGoesNeutral(t *testing.T) {
	t.Parallel()
	t.Run("decisive path", func(t *testing.T) {
		t.Parallel()
		candidate := candidateFor(contextfabric.SubjectPullRequest, "pull_request:repo-1:9")
		attestation := Attestation{Kinds: []KindAttestation{
			{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 1, ClosureMismatch: true, SatisfierCanonicalID: "pull_request:repo-1:1"},
		}}
		got := SurvivorsFirstOrder([]contextfabric.SubjectCandidate{candidate}, attestation)
		if len(got) != 1 || got[0].Subject.CanonicalID != candidate.Subject.CanonicalID {
			t.Fatalf("SurvivorsFirstOrder = %#v, want the candidate untouched (ClosureMismatch -> neutral)", got)
		}
	})
	t.Run("satisfier-set enrichment path", func(t *testing.T) {
		t.Parallel()
		candidate := candidateFor(contextfabric.SubjectPullRequest, "pull_request:repo-1:9")
		attestation := Attestation{Kinds: []KindAttestation{
			{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 2, SatisfierSetClosureMismatch: true},
		}}
		got := SurvivorsFirstOrder([]contextfabric.SubjectCandidate{candidate}, attestation)
		if len(got) != 1 || got[0].Subject.CanonicalID != candidate.Subject.CanonicalID {
			t.Fatalf("SurvivorsFirstOrder = %#v, want the candidate untouched (SatisfierSetClosureMismatch -> neutral)", got)
		}
	})
}

// TestSurvivorsFirstOrder_BudgetExhaustedPreservesOrder is the literal
// acceptance pin: "budget-exhausted discards preserve ordering."
func TestSurvivorsFirstOrder_BudgetExhaustedPreservesOrder(t *testing.T) {
	t.Parallel()
	first := candidateFor(contextfabric.SubjectPullRequest, "pull_request:repo-1:1")
	second := candidateFor(contextfabric.SubjectPullRequest, "pull_request:repo-1:2")
	candidates := []contextfabric.SubjectCandidate{first, second}
	// Even a Kinds entry that WOULD eliminate `second` outright must be
	// ignored once the round's own outcome is budget_exhausted.
	attestation := Attestation{
		Reason: ReasonBudgetExhausted,
		Kinds:  []KindAttestation{{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 1, SatisfierCanonicalID: first.Subject.CanonicalID}},
	}
	got := SurvivorsFirstOrder(candidates, attestation)
	if !reflect.DeepEqual(got, candidates) {
		t.Fatalf("SurvivorsFirstOrder = %#v, want candidates unchanged verbatim on budget_exhausted", got)
	}
}

// TestSurvivorsFirstOrder_NeverChangesMembership is a property-style pin
// on the function's own stated invariant: regardless of verdicts, the
// output is always the same SET (by canonical id) and the same LENGTH as
// the input.
func TestSurvivorsFirstOrder_NeverChangesMembership(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		candidateFor(contextfabric.SubjectPullRequest, "pull_request:repo-1:1"),
		candidateFor(contextfabric.SubjectPullRequest, "pull_request:repo-1:2"),
		candidateFor(contextfabric.SubjectWorkItem, "widget-1"),
		candidateFor(contextfabric.SubjectProject, "project.v2:github:p-1"),
	}
	attestation := Attestation{Kinds: []KindAttestation{
		{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 0},
		{Kind: contextfabric.SubjectWorkItem, Complete: true, Count: 1, SatisfierCanonicalID: "widget-1"},
	}}
	got := SurvivorsFirstOrder(candidates, attestation)
	if len(got) != len(candidates) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(candidates))
	}
	wantIDs := map[string]bool{}
	for _, c := range candidates {
		wantIDs[c.Subject.CanonicalID] = true
	}
	for _, c := range got {
		if !wantIDs[c.Subject.CanonicalID] {
			t.Fatalf("SurvivorsFirstOrder introduced a candidate not in the input: %q", c.Subject.CanonicalID)
		}
		delete(wantIDs, c.Subject.CanonicalID)
	}
	if len(wantIDs) != 0 {
		t.Fatalf("SurvivorsFirstOrder dropped candidates: %v", wantIDs)
	}
	// Also: the input slice itself must not be mutated (SurvivorsFirstOrder
	// returns a copy).
	if candidates[0].Subject.CanonicalID != "pull_request:repo-1:1" {
		t.Fatalf("input slice was mutated: %#v", candidates)
	}
}

// TestReorderingWasReachable pins the harness-measurement helper: true iff
// at least one kind reached a trustworthy 2<=Count<=CensusBudget
// enrichment result.
func TestReorderingWasReachable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		att  Attestation
		want bool
	}{
		{"count==1 only", Attestation{Kinds: []KindAttestation{{Complete: true, Count: 1, SatisfierCanonicalID: "x"}}}, false},
		{"count==0 only", Attestation{Kinds: []KindAttestation{{Complete: true, Count: 0}}}, false},
		{"over budget", Attestation{Kinds: []KindAttestation{{Complete: true, Count: CensusSatisfierSetBudget + 1}}}, false},
		{"set closure mismatch", Attestation{Kinds: []KindAttestation{{Complete: true, Count: 2, SatisfierSetClosureMismatch: true}}}, false},
		{"reachable", Attestation{Kinds: []KindAttestation{{Complete: true, Count: 2, SatisfierCanonicalIDs: []string{"a", "b"}}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ReorderingWasReachable(tc.att); got != tc.want {
				t.Fatalf("ReorderingWasReachable(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestResolutionTraceEventNeverCarriesSurvivorData is chris's rider #2:
// "SatisfierCanonicalIDs stays in-process ONLY -- never in
// ResolutionTraceEvent or any telemetry... Add a pin test that the trace
// event does NOT grow the field." A structural, reflection-based pin
// rather than a behavioral one: it fails the moment a future edit adds a
// "Satisfier*"-named field to ResolutionTraceEvent, regardless of whether
// any test exercises that field's value.
func TestResolutionTraceEventNeverCarriesSurvivorData(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(ResolutionTraceEvent{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if len(name) >= 9 && name[:9] == "Satisfier" {
			t.Fatalf("ResolutionTraceEvent gained a field named %q -- census satisfier/canonical-id data must stay in-process only, never traced (chris's ruling)", name)
		}
	}
}
