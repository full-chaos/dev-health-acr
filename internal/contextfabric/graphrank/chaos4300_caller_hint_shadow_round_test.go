package graphrank

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4300: resolveSubjects' caller-hint short circuit (the AnyCallerSourced
// branch) computes its commit via FinalizeExactResolutionWithBasis and
// returns immediately -- runShadowEvidenceRoundForResolution is called
// exactly once elsewhere in this file, strictly DOWNSTREAM of that return, so
// a commit made through this short circuit structurally never reached it
// before this ticket: CHAOS-3896 Slice C's live evidence-census consumption
// and CHAOS-4081's ConfirmedHandle probe were both unreachable for it. These
// are the RED pins for the fix: an observability-only shadow round now runs
// at the short circuit itself, and it can never touch what the short circuit
// already decided.

// evidenceRoundEvents returns every captured "evidence_round"-stage event.
func (r *recordingTracer) evidenceRoundEvents() []ResolutionTraceEvent {
	var out []ResolutionTraceEvent
	for _, event := range r.events {
		if event.Stage == "evidence_round" {
			out = append(out, event)
		}
	}
	return out
}

func callerHintRequest(subject contextfabric.SubjectRef, source string) contextfabric.InvestigationRequest {
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: source},
	}
	return request
}

// TestChaos4300_CallerHintShortCircuitRunsShadowEvidenceRound is the primary
// RED pin: before this ticket, a caller-hint short circuit commit NEVER
// produced an "evidence_round" trace event, regardless of whether
// deps.CensusFunc was configured. It must now, tagged
// ShadowCallerHintShortCircuit=true so it is distinguishable from the
// pre-existing stalled-resolution call site's own events.
func TestChaos4300_CallerHintShortCircuitRunsShadowEvidenceRound(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_explicit", Label: "Explicit"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, "*"),
	}}
	request := callerHintRequest(subject, "workbench")
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		t.Fatalf("CensusFunc must never be invoked for a caller hint with no anchor/handle discriminator (ReasonNoDiscriminators short-circuits before any read)")
		return CensusOutcome{}, nil
	}

	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("hint must still commit, got %v", resolution.Committed)
	}

	events := tracer.evidenceRoundEvents()
	if len(events) != 1 {
		t.Fatalf("evidence_round events = %d, want exactly 1 from the caller-hint short circuit's own observability call, got %+v", len(events), events)
	}
	event := events[0]
	if !event.ShadowCallerHintShortCircuit {
		t.Fatalf("event = %+v, want ShadowCallerHintShortCircuit=true", event)
	}
	if event.ShadowReason != string(ReasonNoDiscriminators) {
		t.Fatalf("event.ShadowReason = %q, want %q -- a bare caller hint with no anchor/handle has no discriminator to census", event.ShadowReason, ReasonNoDiscriminators)
	}
}

// TestChaos4300_CallerHintShortCircuitSkipsShadowRoundWithoutCensusFunc
// proves the zero-cost-when-unconfigured convention: no CensusFunc means no
// evidence_round event at all, byte-identical to every caller before this
// ticket.
func TestChaos4300_CallerHintShortCircuitSkipsShadowRoundWithoutCensusFunc(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_explicit", Label: "Explicit"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, "*"),
	}}
	request := callerHintRequest(subject, "workbench")
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	// deps.CensusFunc left nil -- the deployment-not-opted-in default.

	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("hint must still commit, got %v", resolution.Committed)
	}
	if events := tracer.evidenceRoundEvents(); len(events) != 0 {
		t.Fatalf("evidence_round events = %+v, want none -- no CensusFunc configured must cost this path nothing", events)
	}
}

// TestChaos4300_CallerHintShortCircuitSkipsShadowRoundUnderOffersOnly proves
// the offers-only pass -- whose own resolution is discarded unconditionally
// by the engine (chaos4234_offers_only.go) -- never pays for a shadow round
// nobody keeps, mirroring the stalled-resolution call site's own
// `!offersOnly` guard.
func TestChaos4300_CallerHintShortCircuitSkipsShadowRoundUnderOffersOnly(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_explicit", Label: "Explicit"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, "*"),
	}}
	request := callerHintRequest(subject, "workbench")
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		t.Fatalf("CensusFunc must never be invoked under the offers-only pass")
		return CensusOutcome{}, nil
	}

	resolution, _, err := ResolveSubjects(contextfabric.WithOffersOnlyResolution(context.Background()), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("hint must still commit on the offers-only pass too, got %v", resolution.Committed)
	}
	if events := tracer.evidenceRoundEvents(); len(events) != 0 {
		t.Fatalf("evidence_round events = %+v, want none under offers-only", events)
	}
}

// TestChaos4300_CallerHintShortCircuitShadowRoundNeverAltersResolution is the
// structural non-decisiveness guarantee: the returned resolution and
// commit-basis set are byte-identical whether or not deps.CensusFunc is
// configured -- mirrors TestResolveSubjects_ShadowEvidenceRoundNeverChangesResolutionDecision's
// own before/after shape for the pre-existing stalled-resolution call site.
func TestChaos4300_CallerHintShortCircuitShadowRoundNeverAltersResolution(t *testing.T) {
	t.Parallel()
	// codex R2 (Low, confirmed): a bare hint with no anchor/handle
	// discriminator hits ReasonNoDiscriminators before ever calling
	// CensusFunc (proven separately by
	// TestChaos4300_CallerHintShortCircuitRunsShadowEvidenceRound's own
	// t.Fatalf-on-call guard), so scripting a would-commit CensusFunc
	// outcome against that scenario never actually exercises the
	// mechanism this test claims to guard. The pull_request/repository
	// pairing (same as TestChaos4300_CallerHintShortCircuitConfirmedHandleProbeFires)
	// gives the round a REAL anchor-driven discriminator, so the scripted
	// would_commit outcome below is genuinely computed, not vacuous.
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:acme/widgets:532", Label: "PR 532"}
	confirmedAnchor := &contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme/widgets"}
	newBackend := func() *fakeGraphBackend {
		return &fakeGraphBackend{exactHints: map[string]CandidateNode{
			SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, "*"),
		}}
	}
	request := callerHintRequest(subject, "workbench")

	without := newBackend().deps()
	withoutResolution, _, withoutBases, withoutDigests, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), without, nil, confirmedAnchor, nil)
	if err != nil {
		t.Fatalf("without CensusFunc: ResolveSubjectsWithCommitBasis error = %v", err)
	}

	readAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	with := newBackend().deps()
	censusCalls := 0
	// A decisive would_commit outcome from the shadow round would be the
	// LOUDEST possible non-decisiveness violation if it ever leaked into the
	// real resolution -- scripted deliberately, not left at the zero value,
	// so this test cannot pass vacuously.
	baseCensus := withCensus(subject.Kind, CensusOutcome{Count: 1, CensusReadAt: readAt, SatisfierCanonicalID: subject.CanonicalID}, nil).fn
	with.CensusFunc = func(ctx context.Context, orgID string, kind CensusKind, value string, handleApplies bool, anchorKind contextfabric.SubjectKind, anchorID string, anchorApplies bool) (CensusOutcome, error) {
		censusCalls++
		return baseCensus(ctx, orgID, kind, value, handleApplies, anchorKind, anchorID, anchorApplies)
	}
	withResolution, _, withBases, withDigests, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), with, nil, confirmedAnchor, nil)
	if err != nil {
		t.Fatalf("with CensusFunc: ResolveSubjectsWithCommitBasis error = %v", err)
	}
	if censusCalls == 0 {
		t.Fatalf("CensusFunc was never called -- this test proves nothing about non-decisiveness without a genuinely computed would_commit outcome")
	}

	if !reflect.DeepEqual(withoutResolution, withResolution) {
		t.Fatalf("resolution changed by the shadow round:\nwithout = %+v\nwith    = %+v", withoutResolution, withResolution)
	}
	if !reflect.DeepEqual(withoutBases, withBases) {
		t.Fatalf("commit basis set changed by the shadow round:\nwithout = %+v\nwith    = %+v", withoutBases, withBases)
	}
	// codex R1 (Low, confirmed): CommitDecisionDigestSet is a SEPARATE
	// CHAOS-4087 return value from CommitBasisSet -- asserting bases alone
	// would let a future edit that starts threading the shadow round's own
	// attestation into a digest (but not a basis) pass this test silently.
	if !reflect.DeepEqual(withoutDigests, withDigests) {
		t.Fatalf("commit decision digest set changed by the shadow round:\nwithout = %+v\nwith    = %+v", withoutDigests, withDigests)
	}
}

// TestChaos4300_CallerHintShortCircuitConfirmedHandleProbeFires closes the
// ticket's second named gap: CHAOS-4081's ConfirmedHandle probe
// (HandleInsensitivityEvaluated/Outcome) must now be reachable through the
// caller-hint short circuit, not just the pre-existing stalled-resolution
// path -- observable on the traced evidence_round event, exactly as it
// already is for the other call site.
func TestChaos4300_CallerHintShortCircuitConfirmedHandleProbeFires(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	// pull_request/repository pairing mirrors confirmedHandleScenario
	// (chaos4081_confirmed_handle_probe_test.go): pull_request has a real
	// anchor FK to repository, so the round's OWN pooled kind gets censused
	// via ConfirmedAnchor and the function runs past both early
	// no-discriminator returns to reach the final ConfirmedHandle probe
	// check -- a bare caller hint alone (no anchor reach for its own kind)
	// stops at ReasonNoDiscriminators before ever consulting the probe, as
	// TestChaos4300_CallerHintShortCircuitRunsShadowEvidenceRound already
	// pins.
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:acme/widgets:532", Label: "PR 532"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, "*"),
	}}
	request := callerHintRequest(subject, "workbench")
	request.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{
		{Kind: contractsv1.ContextFabricSubjectWorkItem, PatternID: "work_item_ticket_key", Value: "CHAOS-1"},
	}
	confirmedAnchor := &contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme/widgets"}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	deps.HandleGrammarChecker = func(contractsv1.ContextFabricSubjectKind, string, string) (string, bool) {
		return "work_items.work_item_id", true
	}
	deps.CensusFunc = multiKindCensus(map[CensusKind]CensusOutcome{
		contextfabric.SubjectPullRequest:         {Count: 1, CensusReadAt: readAt, SatisfierCanonicalID: "pull_request:acme/widgets:532"},
		contractsv1.ContextFabricSubjectWorkItem: {Count: 1, CensusReadAt: readAt, SatisfierCanonicalID: "work_item:linear:CHAOS-1"},
	}).fn

	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, confirmedAnchor)
	if err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("hint must still commit, got %v", resolution.Committed)
	}
	if resolution.Committed[0].Kind != subject.Kind || resolution.Committed[0].CanonicalID != subject.CanonicalID {
		t.Fatalf("committed subject = %+v, want the caller-hint subject unchanged by the confirmed-handle probe", resolution.Committed[0])
	}

	events := tracer.evidenceRoundEvents()
	if len(events) != 1 {
		t.Fatalf("evidence_round events = %d, want exactly 1, got %+v", len(events), events)
	}
	event := events[0]
	if !event.ShadowCallerHintShortCircuit {
		t.Fatalf("event = %+v, want ShadowCallerHintShortCircuit=true", event)
	}
	if !event.ShadowHandleInsensitivityEvaluated {
		t.Fatalf("event = %+v, want ShadowHandleInsensitivityEvaluated=true -- CHAOS-4081's ConfirmedHandle probe must now reach the caller-hint short circuit", event)
	}
	if event.ShadowHandleInsensitivityOutcome != string(kindInsensitivityCommitSound) {
		t.Fatalf("event.ShadowHandleInsensitivityOutcome = %q, want %q", event.ShadowHandleInsensitivityOutcome, kindInsensitivityCommitSound)
	}
}

// TestChaos4300_StalledResolutionCallSiteTagsCallerHintShortCircuitFalse
// pins the OTHER half of the tag: the pre-existing stalled-resolution call
// site (resolveSubjects' own CHAOS-3899 gate, unrelated to this ticket) must
// keep emitting ShadowCallerHintShortCircuit=false, so a reader filtering on
// the new field never silently loses the population this field did not
// change.
func TestChaos4300_StalledResolutionCallSiteTagsCallerHintShortCircuitFalse(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	// Same stalled-resolution shape as
	// TestResolveSubjects_ShadowEvidenceRoundNeverChangesResolution
	// (chaos3899_resolve_wiring_test.go): a low-confidence, single-candidate,
	// truncated search commits nothing, so the pre-existing CHAOS-3899 gate
	// (`len(resolution.Committed) == 0 && searchTruncated`) fires.
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{"PR 532": {candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.6, "*")}},
		searchTruncated: true,
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	deps.CensusFunc = withCensus(contextfabric.SubjectPullRequest, CensusOutcome{Count: 0, CensusReadAt: readAt}, nil).fn

	_, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}

	events := tracer.evidenceRoundEvents()
	if len(events) != 1 {
		t.Fatalf("evidence_round events = %d, want exactly 1 from the stalled-resolution call site, got %+v", len(events), events)
	}
	if events[0].ShadowCallerHintShortCircuit {
		t.Fatalf("event = %+v, want ShadowCallerHintShortCircuit=false for the pre-existing stalled-resolution call site", events[0])
	}
}

// TestChaos4300_CallerHintShortCircuitPanicRecoveryTagsCallerHintShortCircuit
// is the RED pin for codex R1's Medium finding: runShadowEvidenceRoundForResolution's
// own top-level recover() (resolve.go) builds its recovery evidence_round
// event directly, bypassing RunShadowEvidenceRound's emit() closure that
// every NORMAL-path event goes through -- so it must independently carry
// ShadowCallerHintShortCircuit, or a panic on this new call site's own
// CensusFunc invocation reads, indistinguishably, as a probe_error from the
// pre-existing stalled-resolution path.
func TestChaos4300_CallerHintShortCircuitPanicRecoveryTagsCallerHintShortCircuit(t *testing.T) {
	t.Parallel()
	// Same pull_request/repository pairing as
	// TestChaos4300_CallerHintShortCircuitConfirmedHandleProbeFires: a real
	// anchor-driven discriminator, so the round's own main census loop --
	// OUTSIDE confirmedHandleInsensitivityProbe's own isolated recover(),
	// see that function's doc comment -- actually calls CensusFunc and can
	// panic there, escaping to this function's own top-level recover().
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:acme/widgets:532", Label: "PR 532"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, "*"),
	}}
	request := callerHintRequest(subject, "workbench")
	confirmedAnchor := &contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme/widgets"}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		panic("simulated CensusFunc panic on the caller-hint short circuit's own shadow round")
	}

	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, confirmedAnchor)
	if err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("a panic in the observability-only shadow round must never affect the already-decided commit, got %v", resolution.Committed)
	}

	events := tracer.evidenceRoundEvents()
	if len(events) != 1 {
		t.Fatalf("evidence_round events = %d, want exactly 1 (the recovery event), got %+v", len(events), events)
	}
	event := events[0]
	if event.ShadowReason != string(ReasonProbeError) {
		t.Fatalf("event.ShadowReason = %q, want %q", event.ShadowReason, ReasonProbeError)
	}
	if !event.ShadowCallerHintShortCircuit {
		t.Fatalf("event = %+v, want ShadowCallerHintShortCircuit=true -- the panic-recovery event must carry the same provenance a normal-path event on this call site would have", event)
	}
}
