package graphrank

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestResolveSubjects_ShadowEvidenceRoundNeverChangesResolution is
// CHAOS-3899's own zero-behavior-change proof (design brief v5 §6 Slice A:
// "decisions suppressed... production outcomes byte-identical"). It runs
// the IDENTICAL stalled-resolution scenario twice -- once with
// deps.CensusFunc nil (today's production wiring), once with a CensusFunc
// that would answer would_commit (Count==1) if the shadow round's output
// were EVER allowed to influence a real decision -- and asserts the
// returned contextfabric.SubjectResolution is byte-identical either way.
func TestResolveSubjects_ShadowEvidenceRoundNeverChangesResolution(t *testing.T) {
	t.Parallel()
	// A low-confidence, single-candidate, TRUNCATED search: aliasIdentityComplete
	// is false (no AliasLookup wired), so the commit switch's `case
	// searchTruncated:` fires before LoneFloor is ever consulted --
	// "stalled" per brief §0's own definition (search_truncated=true, pool
	// == cap, nothing committed).
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.6, "*")
	buildBackend := func() *fakeGraphBackend {
		return &fakeGraphBackend{
			searchResults:   map[string][]CandidateNode{"PR 532": {target}},
			searchTruncated: true,
		}
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"

	runOnce := func(censusWired bool) (contextfabric.SubjectResolution, *captureResolutionTracer) {
		backend := buildBackend()
		deps := backend.deps()
		tracer := &captureResolutionTracer{}
		deps.ResolutionTracer = tracer
		if censusWired {
			deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
				return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "repo-1:532"}, nil
			}
		}
		resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps)
		if err != nil {
			t.Fatalf("ResolveSubjects() error = %v", err)
		}
		return resolution, tracer
	}

	withoutShadow, tracerWithout := runOnce(false)
	withShadow, tracerWith := runOnce(true)

	if len(withoutShadow.Committed) != 0 {
		t.Fatalf("baseline resolution.Committed = %#v, want nothing committed (this scenario must actually be stalled for the proof to mean anything)", withoutShadow.Committed)
	}
	if !reflect.DeepEqual(withoutShadow, withShadow) {
		t.Fatalf("resolution differs when deps.CensusFunc is wired:\n  without shadow: %#v\n  with shadow:    %#v", withoutShadow, withShadow)
	}

	if got := len(tracerWithout.eventsForStage("evidence_round")); got != 0 {
		t.Fatalf("evidence_round events with CensusFunc nil = %d, want 0 (the round must not run at all)", got)
	}
	if got := len(tracerWith.eventsForStage("evidence_round")); got != 1 {
		t.Fatalf("evidence_round events with CensusFunc wired = %d, want 1 (the round DID run -- non-vacuity)", got)
	}
	decision := tracerWith.eventsForStage("evidence_round")[0]
	if decision.ShadowOutcome != string(ShadowWouldCommit) {
		t.Fatalf("shadow outcome = %q, want %q (proving the round genuinely reached a would-commit verdict it was still forbidden from acting on)", decision.ShadowOutcome, ShadowWouldCommit)
	}
}

// TestResolveSubjects_ShadowEvidenceRoundUsesInterpretedAxisNotRequestAxis
// is an adversarial review regression pin: the shadow round must read
// interpreted.TimeContext.Axis (the ENGINE's own authoritative axis --
// engine.go clamps request.TimeContext.Axis into interpreted.TimeContext
// separately) rather than the raw request.TimeContext.Axis. A historical
// question submitted with a current-axis REQUEST context must still be
// refused via historical_axis_skip (D7), never silently run a
// current-state census against a historical question.
func TestResolveSubjects_ShadowEvidenceRoundUsesInterpretedAxisNotRequestAxis(t *testing.T) {
	t.Parallel()
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.6, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"PR 532": {target}}, searchTruncated: true}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		t.Fatalf("CensusFunc called -- historical_axis_skip must refuse before any source read")
		return CensusOutcome{}, nil
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"
	request.TimeContext.Axis = contextfabric.TemporalCurrent // the RAW request context claims current...
	interpreted := testInterpreted("PR 532")
	interpreted.TimeContext.Axis = contextfabric.TemporalValidTime // ...but the INTERPRETED axis (what the engine actually treats as authoritative) is historical.

	if _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, interpreted, deps); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	events := tracer.eventsForStage("evidence_round")
	if len(events) != 1 || events[0].ShadowReason != string(ReasonHistoricalAxisSkip) {
		t.Fatalf("evidence_round events = %#v, want exactly 1 with Reason=historical_axis_skip", events)
	}
}

// TestResolveSubjects_ShadowEvidenceRoundPanicIsolation is an adversarial
// review regression pin: a panic inside a caller-supplied CensusFunc must
// never escape into a real ResolveSubjects call -- the shadow round is
// purely observational, and its own hardening must be structural, not
// merely "true while nothing panics".
func TestResolveSubjects_ShadowEvidenceRoundPanicIsolation(t *testing.T) {
	t.Parallel()
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.6, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"PR 532": {target}}, searchTruncated: true}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		panic("simulated CensusFunc panic")
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"

	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v, want the panic to be isolated, not propagated as an error either", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want nothing committed", resolution.Committed)
	}
	events := tracer.eventsForStage("evidence_round")
	if len(events) != 1 || events[0].ShadowReason != string(ReasonProbeError) {
		t.Fatalf("evidence_round events = %#v, want exactly 1 recovered probe_error event", events)
	}
}

// TestResolveSubjects_ShadowEvidenceRoundSkipsNonStalledResolutions pins the
// cost discipline (brief's own pricing note: "Committed resolutions pay
// nothing"): a resolution that commits outright must never trigger the
// shadow round at all, even with deps.CensusFunc wired.
func TestResolveSubjects_ShadowEvidenceRoundSkipsNonStalledResolutions(t *testing.T) {
	t.Parallel()
	exact := candidateNode(contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", 0.4, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"Ask Dev": {exact}}}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	censusCalls := 0
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		censusCalls++
		return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC()}, nil
	}
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), deps)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("resolution.Committed = %#v, want exactly 1 (this scenario must actually commit for the proof to mean anything)", resolution.Committed)
	}
	if censusCalls != 0 {
		t.Fatalf("CensusFunc called %d times for a COMMITTED resolution, want 0", censusCalls)
	}
	if got := len(tracer.eventsForStage("evidence_round")); got != 0 {
		t.Fatalf("evidence_round events for a committed resolution = %d, want 0", got)
	}
}
