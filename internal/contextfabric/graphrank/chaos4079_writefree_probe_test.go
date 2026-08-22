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

// CHAOS-4079 pins the WRITE-FREE observation mode of the shadow
// kind-insensitivity probe.
//
// The defect: an explicit kind hint DISJOINT from every pooled candidate
// kind -- exactly what the CHAOS-3742 trial's inferred-tier arm injects on
// purpose -- makes narrowPooledKindsByExplicitKinds return nil, so
// PreNarrowingExplicitKinds stays empty and the probe's own gate in
// RunShadowEvidenceRound never opens. The probe was unreachable for the one
// case it was built to measure.
//
// The fix that was REJECTED on adversarial review (codex xhigh, 2026-08-22):
// populate PreNarrowingExplicitKinds for that case so the existing branch
// runs. That branch issues a SECOND live census read and overwrites
// base.Outcome to would_clarify when the two reads disagree -- and that
// Outcome is consumed for a REAL commit by CHAOS-3896 Slice C
// (attestedSatisfier/mergeCensusAttestedSatisfier). Under census-read drift
// it refuses a commit that would otherwise land.
//
// The fix that shipped: DERIVE the verdict from census results the round
// already collected. Zero additional CensusFunc calls, so the drift hazard
// is structurally absent rather than merely improbable, and the only
// Attestation state touched is the three observability fields. These tests
// pin all three halves -- derivation correctness, write-freedom, and
// commit-behavior identity under a drifting census.

// zeroOverlapCensusInput is a decisive would_commit round carrying an
// explicit kind hint that narrowed NOTHING.
func zeroOverlapCensusInput(census CensusFunc) ShadowEvidenceRoundInput {
	input := baseInput()
	input.Question = "why did PR 532 fail?"
	input.PooledKinds = []CensusKind{contextfabric.SubjectPullRequest}
	input.CensusFunc = census
	input.ObservedExplicitKindHint = true
	return input
}

// TestObservedKindInsensitivityProbeIsWriteFree is the STRUCTURAL guarantee:
// turning the observation on changes the returned Attestation in the three
// KindInsensitivity* fields and in NOTHING else.
//
// Deliberately reflect-driven rather than a hand-written field list: the
// whole risk this ticket exists to contain is a future edit widening what
// the observation path writes, and a hand-written list would silently keep
// passing while a newly added decision-bearing field escaped the guarantee.
func TestObservedKindInsensitivityProbeIsWriteFree(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	outcome := CensusOutcome{Count: 1, CensusReadAt: readAt, SatisfierNaturalKey: "repo-1:532", SatisfierCanonicalID: "pull_request:repo-1:532"}

	off := zeroOverlapCensusInput(withCensus(contextfabric.SubjectPullRequest, outcome, nil).fn)
	off.ObservedExplicitKindHint = false
	before := RunShadowEvidenceRound(context.Background(), off, nil)

	after := RunShadowEvidenceRound(context.Background(),
		zeroOverlapCensusInput(withCensus(contextfabric.SubjectPullRequest, outcome, nil).fn), nil)

	if before.Outcome != ShadowWouldCommit {
		t.Fatalf("precondition: before.Outcome = %v, want would_commit -- the scenario must be decisive or the probe never evaluates", before.Outcome)
	}
	if before.KindInsensitivityEvaluated {
		t.Fatalf("precondition: before.KindInsensitivityEvaluated = true, want false -- this is the gap CHAOS-4079 closes")
	}

	// The complete, closed set of fields the observation path may write.
	observability := map[string]bool{
		"KindInsensitivityEvaluated": true,
		"KindInsensitivityOutcome":   true,
		"KindInsensitivityMode":      true,
	}
	bv, av := reflect.ValueOf(before), reflect.ValueOf(after)
	for i := 0; i < bv.NumField(); i++ {
		name := bv.Type().Field(i).Name
		same := reflect.DeepEqual(bv.Field(i).Interface(), av.Field(i).Interface())
		if observability[name] {
			continue
		}
		if !same {
			t.Errorf("Attestation.%s differs with the observation enabled (%#v -> %#v) -- the observation path must write ONLY the KindInsensitivity* fields; if this field was added to that path deliberately, it is decision-bearing until proven otherwise and needs its own consumer audit",
				name, bv.Field(i).Interface(), av.Field(i).Interface())
		}
	}

	// And the observation itself actually happened (a write-free no-op would
	// pass the loop above vacuously).
	if !after.KindInsensitivityEvaluated || after.KindInsensitivityOutcome != kindInsensitivityCommitSound ||
		after.KindInsensitivityMode != explicitKindNarrowingNoOverlap {
		t.Fatalf("after = evaluated:%v outcome:%q mode:%q, want true/commit_sound/observed_no_overlap",
			after.KindInsensitivityEvaluated, after.KindInsensitivityOutcome, after.KindInsensitivityMode)
	}
}

// TestObservedKindInsensitivityProbeIssuesNoExtraCensusRead is the
// mechanical proof of the derive-not-re-read construction, and the reason
// the drift hazard cannot recur: the observation adds no census call at all.
func TestObservedKindInsensitivityProbeIssuesNoExtraCensusRead(t *testing.T) {
	t.Parallel()
	outcome := CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "repo-1:532"}

	off := withCensus(contextfabric.SubjectPullRequest, outcome, nil)
	offInput := zeroOverlapCensusInput(off.fn)
	offInput.ObservedExplicitKindHint = false
	RunShadowEvidenceRound(context.Background(), offInput, nil)

	on := withCensus(contextfabric.SubjectPullRequest, outcome, nil)
	RunShadowEvidenceRound(context.Background(), zeroOverlapCensusInput(on.fn), nil)

	if on.calls != off.calls {
		t.Fatalf("CensusFunc calls = %d with the observation on, %d with it off -- they must be EQUAL; a second read is the exact hazard that sank the naive fix", on.calls, off.calls)
	}
	if on.calls != 1 {
		t.Fatalf("CensusFunc calls = %d, want exactly 1 (one pooled kind, one census)", on.calls)
	}
}

// TestKindInsensitivityOutcomeFromRound pins the derivation itself, including
// the one dimension in which it is NOT a restatement of the round's own
// outcome: SatisfierSetClosureMismatch is checked here (mirroring
// kindInsensitivityProof) but not by the decisive switch, so a would_commit
// round whose census could not prove its satisfier SET closed derives
// kind_sensitive_outcome.
func TestKindInsensitivityOutcomeFromRound(t *testing.T) {
	t.Parallel()
	complete := func(count int) KindAttestation {
		return KindAttestation{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: count}
	}
	for _, tc := range []struct {
		name  string
		kinds []KindAttestation
		want  kindInsensitivityOutcome
	}{
		{name: "single satisfier", kinds: []KindAttestation{complete(1)}, want: kindInsensitivityCommitSound},
		{name: "no satisfier", kinds: []KindAttestation{complete(0)}, want: kindInsensitivityNoMatchSound},
		{name: "sum across kinds is one", kinds: []KindAttestation{complete(1), complete(0)}, want: kindInsensitivityCommitSound},
		{name: "sum across kinds exceeds one", kinds: []KindAttestation{complete(1), complete(1)}, want: kindInsensitivitySensitive},
		{name: "multi satisfier", kinds: []KindAttestation{complete(2)}, want: kindInsensitivitySensitive},
		{name: "no census at all", kinds: nil, want: kindInsensitivitySensitive},
		{
			name:  "incomplete census",
			kinds: []KindAttestation{{Kind: contextfabric.SubjectPullRequest, Complete: false}},
			want:  kindInsensitivitySensitive,
		},
		{
			name:  "closure mismatch",
			kinds: []KindAttestation{{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 1, ClosureMismatch: true}},
			want:  kindInsensitivitySensitive,
		},
		{
			// The non-vacuous case: decisive would_commit, yet unsound.
			name:  "satisfier set closure mismatch",
			kinds: []KindAttestation{{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 1, SatisfierSetClosureMismatch: true}},
			want:  kindInsensitivitySensitive,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := kindInsensitivityOutcomeFromRound(tc.kinds); got != tc.want {
				t.Errorf("kindInsensitivityOutcomeFromRound = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestObservedKindInsensitivityProbeReportsSensitiveOnUnclosedSatisfierSet
// proves the observation is not a constant restatement of the outcome: the
// SAME decisive would_commit round derives kind_sensitive_outcome when its
// census could not prove the satisfier set closed -- and, critically, the
// Outcome itself is STILL would_commit, because the observation never writes.
func TestObservedKindInsensitivityProbeReportsSensitiveOnUnclosedSatisfierSet(t *testing.T) {
	t.Parallel()
	f := withCensus(contextfabric.SubjectPullRequest, CensusOutcome{
		Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "repo-1:532",
		SatisfierSetClosureMismatch: true,
	}, nil)
	att := RunShadowEvidenceRound(context.Background(), zeroOverlapCensusInput(f.fn), nil)

	if att.Outcome != ShadowWouldCommit {
		t.Fatalf("att.Outcome = %v, want would_commit -- an UNSOUND observation must not demote the outcome the way the genuine-narrowing branch does", att.Outcome)
	}
	if att.Reason != "" {
		t.Fatalf("att.Reason = %q, want empty -- the observation must never stamp a degradation reason", att.Reason)
	}
	if !att.PreconditionUnproven {
		t.Fatalf("att.PreconditionUnproven = false, want true -- would_commit's own invariant, which the observation must leave alone")
	}
	if att.KindInsensitivityOutcome != kindInsensitivitySensitive {
		t.Fatalf("att.KindInsensitivityOutcome = %q, want kind_sensitive_outcome", att.KindInsensitivityOutcome)
	}
}

// TestGenuineNarrowingProbeStillOverwritesOutcome pins that CHAOS-4079 left
// the DECISION-BEARING branch alone: a real narrowing whose re-census
// disagrees still demotes to would_clarify/kind_sensitive_outcome, and now
// also records mode="narrowed".
func TestGenuineNarrowingProbeStillOverwritesOutcome(t *testing.T) {
	t.Parallel()
	f := &fakeCensus{byKind: map[CensusKind][]struct {
		outcome CensusOutcome
		err     error
	}{}}
	// First (narrowed) census: exactly one satisfier -> would_commit.
	// Second (pre-narrowing re-census): two -> the proof disagrees.
	f.byKind[contextfabric.SubjectPullRequest] = []struct {
		outcome CensusOutcome
		err     error
	}{
		{outcome: CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC()}},
		{outcome: CensusOutcome{Count: 2, CensusReadAt: time.Now().UTC()}},
	}
	input := baseInput()
	input.Question = "why did PR 532 fail?"
	input.PooledKinds = []CensusKind{contextfabric.SubjectPullRequest}
	input.PreNarrowingExplicitKinds = []CensusKind{contextfabric.SubjectPullRequest}
	input.CensusFunc = f.fn

	att := RunShadowEvidenceRound(context.Background(), input, nil)
	if att.Outcome != ShadowWouldClarify || att.Reason != ReasonKindSensitiveOutcome {
		t.Fatalf("att = outcome:%v reason:%q, want would_clarify/kind_sensitive_outcome -- the genuine-narrowing branch's overwrite semantics are unchanged by CHAOS-4079", att.Outcome, att.Reason)
	}
	if att.KindInsensitivityMode != explicitKindNarrowingApplied {
		t.Fatalf("att.KindInsensitivityMode = %q, want %q", att.KindInsensitivityMode, explicitKindNarrowingApplied)
	}
	if att.PreconditionUnproven {
		t.Fatal("att.PreconditionUnproven = true, want false -- the demotion left would_commit")
	}
}

// TestClassifyExplicitKindNarrowing pins the four-way split, including that
// the ratified §2.0/§2.3 nil semantics are UNCHANGED: only a genuine
// narrowing returns a non-nil set.
func TestClassifyExplicitKindNarrowing(t *testing.T) {
	t.Parallel()
	pooled := []CensusKind{contextfabric.SubjectPullRequest, contractsv1.ContextFabricSubjectCIRun}
	for _, tc := range []struct {
		name     string
		explicit []contractsv1.ContextFabricSubjectKind
		wantSet  []CensusKind
		wantMode explicitKindNarrowingMode
	}{
		{name: "no hint", explicit: nil, wantMode: explicitKindNarrowingNone},
		{
			name:     "disjoint hint",
			explicit: []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam},
			wantMode: explicitKindNarrowingNoOverlap,
		},
		{
			name:     "hint admits the whole pool",
			explicit: []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectCIRun},
			wantMode: explicitKindNarrowingSubsumed,
		},
		{
			name:     "genuine narrowing",
			explicit: []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectCIRun},
			wantSet:  []CensusKind{contractsv1.ContextFabricSubjectCIRun},
			wantMode: explicitKindNarrowingApplied,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, mode := classifyExplicitKindNarrowing(pooled, tc.explicit)
			if mode != tc.wantMode {
				t.Errorf("mode = %q, want %q", mode, tc.wantMode)
			}
			if !reflect.DeepEqual(got, tc.wantSet) {
				t.Errorf("narrowed = %#v, want %#v", got, tc.wantSet)
			}
			// The wrapper must keep reporting exactly the old nil contract.
			if legacy := narrowPooledKindsByExplicitKinds(pooled, tc.explicit); !reflect.DeepEqual(legacy, tc.wantSet) {
				t.Errorf("narrowPooledKindsByExplicitKinds = %#v, want %#v -- the ratified nil semantics must not drift", legacy, tc.wantSet)
			}
		})
	}
}

// driftingCensus returns a DIFFERENT count on every call for the same kind:
// the codex repro's own shape for a census read with no snapshot isolation
// against a concurrent write.
type driftingCensus struct {
	counts []int
	calls  int
}

func (d *driftingCensus) fn(_ context.Context, _ string, _ CensusKind, _ string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (CensusOutcome, error) {
	i := d.calls
	d.calls++
	if i >= len(d.counts) {
		i = len(d.counts) - 1
	}
	return CensusOutcome{Count: d.counts[i], CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: "pull_request:repo-1:532"}, nil
}

// TestResolveSubjects_ZeroOverlapHintDoesNotChangeCommitUnderCensusDrift is
// THE regression this ticket is built around, driven end to end through
// ResolveSubjects (so CHAOS-3896 Slice C's real consumer is in the loop).
//
// The census drifts: first read finds one satisfier, any second read would
// find two. The naive fix re-reads, sees the drift, demotes the round to
// would_clarify, and attestedSatisfier then refuses a commit that would
// otherwise land. This test pins that the commit STILL LANDS, that the
// census was read exactly once, and that the probe nevertheless reported.
func TestResolveSubjects_ZeroOverlapHintDoesNotChangeCommitUnderCensusDrift(t *testing.T) {
	t.Parallel()
	commitUnder := func(t *testing.T, expectedKinds []contractsv1.ContextFabricSubjectKind) (contextfabric.SubjectResolution, *captureResolutionTracer, int) {
		t.Helper()
		target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.50, "*")
		backend := &fakeGraphBackend{
			searchResults:   map[string][]CandidateNode{"PR 532": {target}},
			searchTruncated: true,
			exactHints: map[string]CandidateNode{
				SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:532"}): target,
			},
		}
		deps := backend.deps()
		tracer := &captureResolutionTracer{}
		deps.ResolutionTracer = tracer
		drift := &driftingCensus{counts: []int{1, 2}}
		deps.CensusFunc = drift.fn

		request := testRequest()
		request.Question = "why did PR 532 fail?"
		request.ExpectedKinds = expectedKinds

		resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps, nil, nil)
		if err != nil {
			t.Fatalf("ResolveSubjects error = %v", err)
		}
		return resolution, tracer, drift.calls
	}

	// Control: no hint at all. This is the behavior that must be preserved.
	control, _, controlCalls := commitUnder(t, nil)
	if len(control.Committed) != 1 || control.Committed[0].CanonicalID != "pull_request:repo-1:532" {
		t.Fatalf("control resolution.Committed = %#v, want the census-attested satisfier", control.Committed)
	}

	// The wrong-kind hint: disjoint from the pooled pull_request candidate.
	hinted, tracer, hintedCalls := commitUnder(t, []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam})
	if len(hinted.Committed) != 1 || hinted.Committed[0].CanonicalID != "pull_request:repo-1:532" {
		t.Fatalf("hinted resolution.Committed = %#v, want the SAME commit the control produced -- a wrong-kind hint must not change the commit decision, and an observability probe must not either", hinted.Committed)
	}
	if hintedCalls != controlCalls {
		t.Fatalf("CensusFunc calls = %d with the hint, %d without -- they must be EQUAL; an extra read under a drifting census is precisely how the rejected fix broke commits", hintedCalls, controlCalls)
	}

	// ...and the probe now actually reports, which it could not before.
	rounds := tracer.eventsForStage("evidence_round")
	if len(rounds) != 1 {
		t.Fatalf("evidence_round events = %d, want 1", len(rounds))
	}
	if !rounds[0].ShadowKindInsensitivityEvaluated ||
		rounds[0].ShadowKindInsensitivityOutcome != string(kindInsensitivityCommitSound) ||
		rounds[0].ShadowKindInsensitivityMode != string(explicitKindNarrowingNoOverlap) {
		t.Fatalf("evidence_round probe = evaluated:%v outcome:%q mode:%q, want true/commit_sound/observed_no_overlap -- this is the observability CHAOS-4079 exists to deliver",
			rounds[0].ShadowKindInsensitivityEvaluated, rounds[0].ShadowKindInsensitivityOutcome, rounds[0].ShadowKindInsensitivityMode)
	}
}

// TestResolveSubjects_NoExplicitHintLeavesProbeUnevaluated pins the blast
// radius: the overwhelming common case (no explicit kind hint at all) is
// untouched -- no observation, no mode, byte-identical trace to before.
func TestResolveSubjects_NoExplicitHintLeavesProbeUnevaluated(t *testing.T) {
	t.Parallel()
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.50, "*")
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{"PR 532": {target}},
		searchTruncated: true,
		exactHints: map[string]CandidateNode{
			SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:532"}): target,
		},
	}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: "pull_request:repo-1:532"}, nil
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"

	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps, nil, nil); err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}
	for _, e := range tracer.eventsForStage("evidence_round") {
		if e.ShadowKindInsensitivityEvaluated || e.ShadowKindInsensitivityMode != "" {
			t.Fatalf("evidence_round probe = evaluated:%v mode:%q, want false/\"\" when no explicit kind hint was supplied at all", e.ShadowKindInsensitivityEvaluated, e.ShadowKindInsensitivityMode)
		}
	}
}
