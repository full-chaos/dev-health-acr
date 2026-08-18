package graphrank

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// fakeCensus is a scriptable CensusFunc test double: keyed by kind, each
// call for that kind pops the next queued CensusOutcome/error.
type fakeCensus struct {
	byKind map[CensusKind][]struct {
		outcome CensusOutcome
		err     error
	}
	calls int
}

func (f *fakeCensus) fn(_ context.Context, _ string, kind CensusKind, _ string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (CensusOutcome, error) {
	f.calls++
	queue := f.byKind[kind]
	if len(queue) == 0 {
		return CensusOutcome{}, nil
	}
	next := queue[0]
	f.byKind[kind] = queue[1:]
	return next.outcome, next.err
}

func withCensus(kind CensusKind, outcome CensusOutcome, err error) *fakeCensus {
	f := &fakeCensus{byKind: map[CensusKind][]struct {
		outcome CensusOutcome
		err     error
	}{}}
	f.byKind[kind] = append(f.byKind[kind], struct {
		outcome CensusOutcome
		err     error
	}{outcome, err})
	return f
}

func baseInput() ShadowEvidenceRoundInput {
	return ShadowEvidenceRoundInput{
		RequestID: "req-1", OrgID: "org-1", CurrentAxis: true, UnscopedVisibility: true,
		AliasLookupComplete: true,
	}
}

func TestRunShadowEvidenceRound_HistoricalAxisSkip(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.CurrentAxis = false
	tracer := &captureResolutionTracer{}
	att := RunShadowEvidenceRound(context.Background(), input, tracer)
	if att.Outcome != ShadowWouldClarify || att.Reason != ReasonHistoricalAxisSkip {
		t.Fatalf("att = %#v", att)
	}
	assertSingleEvidenceRoundEvent(t, tracer)
}

func TestRunShadowEvidenceRound_ScopedVisibility_NoSourceReads(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.UnscopedVisibility = false
	input.Question = "PR 532 failed"
	f := &fakeCensus{byKind: map[CensusKind][]struct {
		outcome CensusOutcome
		err     error
	}{}}
	input.CensusFunc = f.fn
	tracer := &captureResolutionTracer{}
	att := RunShadowEvidenceRound(context.Background(), input, tracer)
	if att.Outcome != ShadowWouldClarify || att.Reason != ReasonScopedVisibility {
		t.Fatalf("att = %#v", att)
	}
	if f.calls != 0 {
		t.Fatalf("CensusFunc called %d times, want 0 -- scoped_visibility must issue NO source reads (brief §1.3(5))", f.calls)
	}
}

func TestRunShadowEvidenceRound_MultiHandleRefuses(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "PR 532 and PR 533 both failed"
	tracer := &captureResolutionTracer{}
	att := RunShadowEvidenceRound(context.Background(), input, tracer)
	if att.Outcome != ShadowWouldClarify || att.Reason != ReasonMultiHandle {
		t.Fatalf("att = %#v", att)
	}
}

func TestRunShadowEvidenceRound_NoDiscriminatorsRefuses(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "how healthy is the team?"
	tracer := &captureResolutionTracer{}
	att := RunShadowEvidenceRound(context.Background(), input, tracer)
	if att.Outcome != ShadowWouldClarify || att.Reason != ReasonNoDiscriminators {
		t.Fatalf("att = %#v", att)
	}
}

func TestRunShadowEvidenceRound_WouldCommit_PreconditionUnproven(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "why did PR 532 fail?"
	readAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	f := withCensus(contextfabric.SubjectPullRequest, CensusOutcome{Count: 1, CensusReadAt: readAt, SatisfierNaturalKey: "repo-1:532"}, nil)
	input.CensusFunc = f.fn
	tracer := &captureResolutionTracer{}
	att := RunShadowEvidenceRound(context.Background(), input, tracer)
	if att.Outcome != ShadowWouldCommit {
		t.Fatalf("att.Outcome = %v, want would_commit: %#v", att.Outcome, att)
	}
	if !att.PreconditionUnproven {
		t.Fatalf("att.PreconditionUnproven = false, want true (CHAOS-3898 blocks the bridge -- brief §1.4)")
	}
	if att.Protocol != "aggregate_first" {
		t.Fatalf("att.Protocol = %q, want aggregate_first", att.Protocol)
	}
	if att.DIdentity == "" {
		t.Fatalf("att.DIdentity is empty, want a SHA-256")
	}
}

func TestRunShadowEvidenceRound_WouldNoMatch(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "why did PR 532 fail?"
	f := withCensus(contextfabric.SubjectPullRequest, CensusOutcome{Count: 0, CensusReadAt: time.Now().UTC()}, nil)
	input.CensusFunc = f.fn
	tracer := &captureResolutionTracer{}
	att := RunShadowEvidenceRound(context.Background(), input, tracer)
	if att.Outcome != ShadowWouldNoMatch {
		t.Fatalf("att.Outcome = %v, want would_no_match: %#v", att.Outcome, att)
	}
}

func TestRunShadowEvidenceRound_ClosureMismatchDemotes(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "why did PR 532 fail?"
	f := withCensus(contextfabric.SubjectPullRequest, CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), ClosureMismatch: true}, nil)
	input.CensusFunc = f.fn
	tracer := &captureResolutionTracer{}
	att := RunShadowEvidenceRound(context.Background(), input, tracer)
	if att.Outcome != ShadowWouldClarify || att.Reason != ReasonCensusClosureMismatch {
		t.Fatalf("att = %#v, want would_clarify/census_closure_mismatch", att)
	}
}

func TestRunShadowEvidenceRound_CensusErrorPoisons(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "why did PR 532 fail?"
	f := withCensus(contextfabric.SubjectPullRequest, CensusOutcome{}, context.DeadlineExceeded)
	input.CensusFunc = f.fn
	tracer := &captureResolutionTracer{}
	att := RunShadowEvidenceRound(context.Background(), input, tracer)
	if att.Outcome != ShadowWouldClarify || att.Reason != ReasonCensusError {
		t.Fatalf("att = %#v, want would_clarify/census_error", att)
	}
}

// TestRunShadowEvidenceRound_NonCensusedSurvivorBlocksNoMatch pins brief
// §3(2): a pooled hypothesis of a kind OUTSIDE the census registry (e.g.
// repository) means no_match can never be this round's outcome, even
// though the one censused kind came back empty.
func TestRunShadowEvidenceRound_NonCensusedSurvivorBlocksNoMatch(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "why did PR 532 fail?"
	input.PooledKinds = []CensusKind{contextfabric.SubjectRepository}
	f := withCensus(contextfabric.SubjectPullRequest, CensusOutcome{Count: 0, CensusReadAt: time.Now().UTC()}, nil)
	input.CensusFunc = f.fn
	tracer := &captureResolutionTracer{}
	att := RunShadowEvidenceRound(context.Background(), input, tracer)
	if att.Outcome == ShadowWouldNoMatch {
		t.Fatalf("att.Outcome = would_no_match, want NOT no_match -- a non-censused-kind survivor must block it (§3(2))")
	}
	if !att.NonCensusedSurvivor {
		t.Fatalf("att.NonCensusedSurvivor = false, want true")
	}
}

// TestRunShadowEvidenceRound_AnchorOnlyCensusesEveryApplicableKind pins the
// anchor-only path (no handle): every pooled census kind sharing the
// anchor's FK column gets censused.
func TestRunShadowEvidenceRound_AnchorOnlyCensusesEveryApplicableKind(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "how is this repo's CI doing?"
	input.PooledKinds = []CensusKind{contextfabric.SubjectPullRequest, contractsv1.ContextFabricSubjectCIRun}
	input.AliasClaimants = map[string][]IdentityMatch{
		"dev-health-acr": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchAlias}},
	}
	f := &fakeCensus{byKind: map[CensusKind][]struct {
		outcome CensusOutcome
		err     error
	}{
		contextfabric.SubjectPullRequest:      {{outcome: CensusOutcome{Count: 0, CensusReadAt: time.Now().UTC()}}},
		contractsv1.ContextFabricSubjectCIRun: {{outcome: CensusOutcome{Count: 0, CensusReadAt: time.Now().UTC()}}},
	}}
	input.CensusFunc = f.fn
	tracer := &captureResolutionTracer{}
	att := RunShadowEvidenceRound(context.Background(), input, tracer)
	if len(att.Kinds) != 2 {
		t.Fatalf("att.Kinds = %#v, want both pooled census kinds censused via the shared repository anchor", att.Kinds)
	}
	if att.Outcome != ShadowWouldNoMatch {
		t.Fatalf("att.Outcome = %v, want would_no_match (both kinds empty, no non-censused survivor)", att.Outcome)
	}
}

// TestRunShadowEvidenceRound_WouldCommitBlockedByNonCensusedSurvivor is an
// adversarial review regression pin: would_commit must be gated on
// !NonCensusedSurvivor exactly like would_no_match is (brief §1.3(4)'s
// censusComplete is a per-kind AND over every hypothesized kind, and a
// non-censused survivor structurally cannot be part of that AND) -- a
// single censused kind returning count==1 must not commit while a pooled
// hypothesis of a kind outside the census registry is still in play.
func TestRunShadowEvidenceRound_WouldCommitBlockedByNonCensusedSurvivor(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "why did PR 532 fail?"
	input.PooledKinds = []CensusKind{contextfabric.SubjectRepository}
	f := withCensus(contextfabric.SubjectPullRequest, CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "repo-1:532"}, nil)
	input.CensusFunc = f.fn
	att := RunShadowEvidenceRound(context.Background(), input, nil)
	if att.Outcome == ShadowWouldCommit {
		t.Fatalf("att.Outcome = would_commit, want NOT would_commit -- a non-censused-kind survivor must block it too (§1.3(4)), not just would_no_match")
	}
}

// TestRunShadowEvidenceRound_NonVacuity proves the round actually executed
// -- one evidence_round event, and one evidence_probe per kind censused.
func TestRunShadowEvidenceRound_NonVacuity(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "why did PR 532 fail?"
	f := withCensus(contextfabric.SubjectPullRequest, CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "repo-1:532"}, nil)
	input.CensusFunc = f.fn
	tracer := &captureResolutionTracer{}
	RunShadowEvidenceRound(context.Background(), input, tracer)
	if len(tracer.eventsForStage("evidence_round")) != 1 {
		t.Fatalf("evidence_round events = %d, want 1", len(tracer.eventsForStage("evidence_round")))
	}
	probes := tracer.eventsForStage("evidence_probe")
	if len(probes) != 1 {
		t.Fatalf("evidence_probe events = %d, want 1", len(probes))
	}
	if probes[0].CensusKind != contextfabric.SubjectPullRequest || !probes[0].CensusComplete || probes[0].CensusProtocol != "aggregate_first" {
		t.Fatalf("probe = %#v", probes[0])
	}
	if probes[0].CensusReadAtUnix == 0 {
		t.Fatalf("probe.CensusReadAtUnix = 0, want non-zero (brief §1.3(3))")
	}
}

// TestRunShadowEvidenceRound_NilTracerStillComputes proves the Attestation
// is fully computed even with no tracer wired (nil is a valid, common
// call shape for a direct unit test or a caller that only wants the value).
func TestRunShadowEvidenceRound_NilTracerStillComputes(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "why did PR 532 fail?"
	f := withCensus(contextfabric.SubjectPullRequest, CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "repo-1:532"}, nil)
	input.CensusFunc = f.fn
	att := RunShadowEvidenceRound(context.Background(), input, nil)
	if att.Outcome != ShadowWouldCommit {
		t.Fatalf("att = %#v", att)
	}
}

func assertSingleEvidenceRoundEvent(t *testing.T, tracer *captureResolutionTracer) {
	t.Helper()
	if got := len(tracer.eventsForStage("evidence_round")); got != 1 {
		t.Fatalf("evidence_round events = %d, want 1", got)
	}
}
