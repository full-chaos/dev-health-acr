package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// decisions returns every captured "decision"-stage event, in capture order
// -- the CHAOS-4096 plural counterpart to recordingTracer.decision(), which
// only ever returns the first one. Both a multi-subject commit
// (ResolveFromMergedCandidatesWithGateAndBasis's pre_committed_exact_hint
// loop) and the caller-hint short circuit (resolve.go's AnyCallerSourced
// branch) can now emit more than one.
func (r *recordingTracer) decisions() []ResolutionTraceEvent {
	var out []ResolutionTraceEvent
	for _, event := range r.events {
		if event.Stage == "decision" {
			out = append(out, event)
		}
	}
	return out
}

// TestChaos4096_MultiSubjectCommitEmitsOneDecisionEventPerSubject is the RED
// pin for gap 1 (CHAOS-4096): resolution.go's decision-stage switch only had
// a `case len(resolution.Committed) == 1`, so a resolution that commits MORE
// than one subject -- the pre_committed_exact_hint loop above it can append
// more than one candidate arriving already State==Committed/Confidence==1 --
// matched no case at all and emitted NOTHING. CommitBasis is recorded
// correctly either way (bases.Record fires once per candidate in that same
// loop); this is purely an emission-cardinality gap.
func TestChaos4096_MultiSubjectCommitEmitsOneDecisionEventPerSubject(t *testing.T) {
	// codex R1 (Low, confirmed): distinct mechanisms per candidate, so this
	// test actually proves the per-subject WinningMechanism lookup
	// (resolution.go, keyed by SubjectKey) rather than passing vacuously
	// because both subjects happened to share the same mechanism.
	first := corroborationCandidate("multi_first", 1, contextfabric.MatchExact)
	first.State = contextfabric.ResolutionCommitted
	second := corroborationCandidate("multi_second", 1, contextfabric.MatchAlias)
	second.State = contextfabric.ResolutionCommitted
	wantMechanism := map[string]string{
		SubjectKey(first.Subject):  string(contextfabric.MatchExact),
		SubjectKey(second.Subject): string(contextfabric.MatchAlias),
	}

	tracer := &recordingTracer{}
	bySubject := map[string]contextfabric.SubjectCandidate{
		SubjectKey(first.Subject):  first,
		SubjectKey(second.Subject): second,
	}
	resolution, bases, _ := ResolveFromMergedCandidatesWithGateAndBasis(
		bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
		nil, 0, false, 10, 20, true,
		DefaultCommitGatePolicy(), nil, nil, false, tracer, "req-multi", "", false, false, nil)

	if len(resolution.Committed) != 2 {
		t.Fatalf("both pre-committed hints must still commit, got %v", resolution.Committed)
	}

	events := tracer.decisions()
	if len(events) != 2 {
		t.Fatalf("decision events = %d, want one PER committed subject (2), got %+v", len(events), events)
	}
	seen := map[string]ResolutionTraceEvent{}
	for _, e := range events {
		seen[SubjectKey(e.Subject)] = e
	}
	for _, subject := range resolution.Committed {
		event, ok := seen[SubjectKey(subject)]
		if !ok {
			t.Fatalf("no decision event for committed subject %v; events = %+v", subject, events)
		}
		if event.Outcome != "committed" {
			t.Fatalf("event for %v: Outcome = %q, want %q", subject, event.Outcome, "committed")
		}
		if event.CommitGate != "pre_committed_exact_hint" {
			t.Fatalf("event for %v: CommitGate = %q, want %q", subject, event.CommitGate, "pre_committed_exact_hint")
		}
		if event.CommitBasis != string(bases.For(subject)) {
			t.Fatalf("event for %v: CommitBasis = %q, want it to match the recorded basis %q", subject, event.CommitBasis, bases.For(subject))
		}
		if want := wantMechanism[SubjectKey(subject)]; event.WinningMechanism != want {
			t.Fatalf("event for %v: WinningMechanism = %q, want %q -- the per-subject lookup must not cross-attribute mechanisms between committed subjects", subject, event.WinningMechanism, want)
		}
	}
}

// TestChaos4096_CallerHintShortCircuitEmitsDecisionEvents is the RED pin for
// gap 2 (CHAOS-4096): resolve.go's AnyCallerSourced short circuit computes
// its resolution via FinalizeExactResolutionWithBasis and returns
// immediately -- it never reaches ResolveFromMergedCandidatesWithGateAndBasis
// (the only place that traces a "decision" event), so this whole commit path
// is invisible to the trace regardless of how many subjects it commits.
//
// Mirrors TestChaos4085_ExactHintShortCircuitRecordsBasisPerClass's mixed
// caller-explicit + receipt-derived shape so the SAME per-class basis
// distinction is asserted on the trace, not just on the returned
// CommitBasisSet.
func TestChaos4096_CallerHintShortCircuitEmitsDecisionEvents(t *testing.T) {
	t.Parallel()
	explicit := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_explicit", Label: "Explicit"}
	fromReceipt := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_receipt", Label: "Receipt"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(explicit):    candidateNode(explicit.Kind, explicit.CanonicalID, explicit.Label, 0.2, "*"),
		SubjectKey(fromReceipt): candidateNode(fromReceipt.Kind, fromReceipt.CanonicalID, fromReceipt.Label, 0.2, "*"),
	}}
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		{Kind: explicit.Kind, ID: explicit.CanonicalID, Label: explicit.Label, Source: "workbench"},
		{Kind: fromReceipt.Kind, ID: fromReceipt.CanonicalID, Label: fromReceipt.Label, Source: "prior_subject_receipt"},
	}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer

	resolution, _, bases, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis error = %v", err)
	}
	if len(resolution.Committed) != 2 {
		t.Fatalf("both hints must still commit, got %v", resolution.Committed)
	}

	events := tracer.decisions()
	if len(events) != 2 {
		t.Fatalf("decision events = %d, want one PER committed subject (2) from the caller-hint short circuit, got %+v", len(events), events)
	}
	seen := map[string]ResolutionTraceEvent{}
	for _, e := range events {
		seen[SubjectKey(e.Subject)] = e
	}
	for _, subject := range []contextfabric.SubjectRef{explicit, fromReceipt} {
		event, ok := seen[SubjectKey(subject)]
		if !ok {
			t.Fatalf("no decision event for committed subject %v; events = %+v", subject, events)
		}
		if event.Outcome != "committed" || event.CommitGate != "caller_hint_short_circuit" {
			t.Fatalf("event for %v = %+v, want Outcome=committed CommitGate=caller_hint_short_circuit", subject, event)
		}
		if event.CommitBasis != string(bases.For(subject)) {
			t.Fatalf("event for %v: CommitBasis = %q, want it to match the recorded basis %q", subject, event.CommitBasis, bases.For(subject))
		}
	}
	if got := seen[SubjectKey(explicit)].CommitBasis; got != string(contextfabric.CommitBasisCallerCanonicalID) {
		t.Fatalf("caller-explicit subject: CommitBasis = %q, want %q", got, contextfabric.CommitBasisCallerCanonicalID)
	}
	if got := seen[SubjectKey(fromReceipt)].CommitBasis; got != string(contextfabric.CommitBasisStatistical) {
		t.Fatalf("receipt-derived rider: CommitBasis = %q, want %q (never exempted)", got, contextfabric.CommitBasisStatistical)
	}
}

// TestChaos4096_CallerHintShortCircuitDecisionEventFields is the RED pin for
// codex R1 findings 1+2 on the caller-hint short circuit's own decision
// event (resolve.go's AnyCallerSourced branch, added by this ticket):
// unlike ResolveFromMergedCandidatesWithGateAndBasis's own decision event,
// this short circuit runs no search and consults no ranked population, so
// its SearchCandidateLimit/PopulationBasis must say exactly that ("none")
// rather than silently reading as "search truncated at 0" or "unset"; and
// it bypasses offersOnlyDecisionTracer entirely (it traces straight to
// deps.ResolutionTracer, resolve.go's own comment on this call site), so it
// must tag OfferedUnderWindowGate itself or an offers-only pass through
// this path reads as an indistinguishable-from-real commit.
func TestChaos4096_CallerHintShortCircuitDecisionEventFields(t *testing.T) {
	t.Parallel()
	explicit := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_explicit", Label: "Explicit"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(explicit): candidateNode(explicit.Kind, explicit.CanonicalID, explicit.Label, 0.2, "*"),
	}}
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		{Kind: explicit.Kind, ID: explicit.CanonicalID, Label: explicit.Label, Source: "workbench"},
	}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer

	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(contextfabric.WithOffersOnlyResolution(context.Background()), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("hint must still commit, got %v", resolution.Committed)
	}

	events := tracer.decisions()
	if len(events) != 1 {
		t.Fatalf("decision events = %d, want 1, got %+v", len(events), events)
	}
	event := events[0]
	if !event.OfferedUnderWindowGate {
		t.Fatalf("event = %+v, want OfferedUnderWindowGate=true under offers-only mode (this path bypasses offersOnlyDecisionTracer)", event)
	}
	if event.SearchCandidateLimit != request.Options.MaxSubjectCandidates {
		t.Fatalf("event.SearchCandidateLimit = %d, want %d (Options.MaxSubjectCandidates)", event.SearchCandidateLimit, request.Options.MaxSubjectCandidates)
	}
	if event.PopulationBasis != "none" {
		t.Fatalf(`event.PopulationBasis = %q, want "none" -- this short circuit never ranks or truncates a searched population`, event.PopulationBasis)
	}
}
