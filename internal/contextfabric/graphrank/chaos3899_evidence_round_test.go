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

// TestRunShadowEvidenceRound_ExplicitKindNarrowing_SensitiveDemotes is the
// CHAOS-3972 P3 hard-precondition pin (design brief §2.0, CHAOS-3927/P1.D's
// own wiring requirement): a would_commit reached under an EXPLICIT
// (non-receipt) kind narrowing is untrustworthy when the SAME census,
// re-run over the pre-narrowing kind set, finds a SECOND satisfier the
// narrowing hid -- exactly the hazard the kind-insensitivity rule exists
// to catch. The round must demote to would_clarify/kind_sensitive_outcome,
// never commit.
func TestRunShadowEvidenceRound_ExplicitKindNarrowing_SensitiveDemotes(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "how is this repo's CI doing?"
	// PooledKinds is the NARROWED set this round actually censuses (an
	// explicit expected_kinds=[ci_pipeline_run] having already dropped
	// pull_request from the hypothesis set) -- PreNarrowingExplicitKinds
	// carries the set BEFORE that narrowing, per
	// runShadowEvidenceRoundForResolution's own contract.
	input.PooledKinds = []CensusKind{contractsv1.ContextFabricSubjectCIRun}
	input.PreNarrowingExplicitKinds = []CensusKind{contextfabric.SubjectPullRequest, contractsv1.ContextFabricSubjectCIRun}
	input.AliasClaimants = map[string][]IdentityMatch{
		"dev-health-acr": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchAlias}},
	}
	// CIRun is censused TWICE: once by the round itself (over the narrowed
	// PooledKinds), once again by kindInsensitivityProof (over the
	// pre-narrowing set) -- both return count=1. PullRequest is censused
	// ONLY by the insensitivity proof, and ALSO returns count=1: the
	// all-kinds total is 2, so the narrowed round's own would-commit
	// verdict is unsound.
	f := &fakeCensus{byKind: map[CensusKind][]struct {
		outcome CensusOutcome
		err     error
	}{
		contractsv1.ContextFabricSubjectCIRun: {
			{outcome: CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "run-1"}},
			{outcome: CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "run-1"}},
		},
		contextfabric.SubjectPullRequest: {
			{outcome: CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "pr-1"}},
		},
	}}
	input.CensusFunc = f.fn
	att := RunShadowEvidenceRound(context.Background(), input, nil)
	if att.Outcome != ShadowWouldClarify || att.Reason != ReasonKindSensitiveOutcome {
		t.Fatalf("att = %#v, want would_clarify/kind_sensitive_outcome -- the narrowed commit must be demoted, not trusted", att)
	}
	if att.PreconditionUnproven {
		t.Fatalf("att.PreconditionUnproven = true, want false once demoted off would_commit (invariant: true only when Outcome==would_commit)")
	}
	if f.calls != 3 {
		t.Fatalf("census calls = %d, want 3 (1 narrowed round + 2 insensitivity proof)", f.calls)
	}
	if !att.KindInsensitivityEvaluated || att.KindInsensitivityOutcome != kindInsensitivitySensitive {
		t.Fatalf("att.KindInsensitivityEvaluated/Outcome = %v/%q, want true/kind_sensitive_outcome (CHAOS-4039)", att.KindInsensitivityEvaluated, att.KindInsensitivityOutcome)
	}
}

// TestRunShadowEvidenceRound_ExplicitKindNarrowing_SoundCommits is the
// converse of the sensitive-demotion pin: when the all-kinds census, re-run
// over the pre-narrowing set, agrees there is exactly one satisfier
// overall, the narrowed round's would_commit stands.
func TestRunShadowEvidenceRound_ExplicitKindNarrowing_SoundCommits(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "how is this repo's CI doing?"
	input.PooledKinds = []CensusKind{contractsv1.ContextFabricSubjectCIRun}
	input.PreNarrowingExplicitKinds = []CensusKind{contextfabric.SubjectPullRequest, contractsv1.ContextFabricSubjectCIRun}
	input.AliasClaimants = map[string][]IdentityMatch{
		"dev-health-acr": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchAlias}},
	}
	f := &fakeCensus{byKind: map[CensusKind][]struct {
		outcome CensusOutcome
		err     error
	}{
		contractsv1.ContextFabricSubjectCIRun: {
			{outcome: CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "run-1"}},
			{outcome: CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "run-1"}},
		},
		contextfabric.SubjectPullRequest: {
			{outcome: CensusOutcome{Count: 0, CensusReadAt: time.Now().UTC()}},
		},
	}}
	input.CensusFunc = f.fn
	att := RunShadowEvidenceRound(context.Background(), input, nil)
	if att.Outcome != ShadowWouldCommit {
		t.Fatalf("att = %#v, want would_commit -- the all-kinds proof agrees, the narrowed commit is sound", att)
	}
	if !att.KindInsensitivityEvaluated || att.KindInsensitivityOutcome != kindInsensitivityCommitSound {
		t.Fatalf("att.KindInsensitivityEvaluated/Outcome = %v/%q, want true/commit_sound (CHAOS-4039)", att.KindInsensitivityEvaluated, att.KindInsensitivityOutcome)
	}
}

// TestRunShadowEvidenceRound_ExplicitKindNarrowing_RegistryMissPoisons pins
// the kindInsensitivityProof registry-miss rule (design brief §2.0
// implementation pin (b)) reaching all the way through RunShadowEvidenceRound:
// a pre-narrowing kind outside the closed census registry makes
// insensitivity unprovable, demoting even an otherwise-sound narrowed
// commit.
func TestRunShadowEvidenceRound_ExplicitKindNarrowing_RegistryMissPoisons(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "how is this repo's CI doing?"
	input.PooledKinds = []CensusKind{contractsv1.ContextFabricSubjectCIRun}
	// contextfabric.SubjectDocument is not a census-registered kind.
	input.PreNarrowingExplicitKinds = []CensusKind{contextfabric.SubjectDocument, contractsv1.ContextFabricSubjectCIRun}
	input.AliasClaimants = map[string][]IdentityMatch{
		"dev-health-acr": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchAlias}},
	}
	f := withCensus(contractsv1.ContextFabricSubjectCIRun, CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "run-1"}, nil)
	input.CensusFunc = f.fn
	att := RunShadowEvidenceRound(context.Background(), input, nil)
	if att.Outcome != ShadowWouldClarify || att.Reason != ReasonKindSensitiveOutcome {
		t.Fatalf("att = %#v, want would_clarify/kind_sensitive_outcome -- a non-censused pre-narrowing kind must poison the proof", att)
	}
	if !att.KindInsensitivityEvaluated || att.KindInsensitivityOutcome != kindInsensitivitySensitive {
		t.Fatalf("att.KindInsensitivityEvaluated/Outcome = %v/%q, want true/kind_sensitive_outcome (CHAOS-4039)", att.KindInsensitivityEvaluated, att.KindInsensitivityOutcome)
	}
}

// TestRunShadowEvidenceRound_ReceiptConfirmedKindNeverProofGated proves the
// OTHER half of the design: PreNarrowingExplicitKinds unset (the receipt-
// confirmed case -- ConfirmedExpectedKind already narrowed candidatesBySubject
// upstream, so PooledKinds arrives pre-narrowed with nothing to prove
// insensitive against) reaches would_commit with NO extra census call,
// exactly the pre-CHAOS-3972 behavior.
func TestRunShadowEvidenceRound_ReceiptConfirmedKindNeverProofGated(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "how is this repo's CI doing?"
	input.PooledKinds = []CensusKind{contractsv1.ContextFabricSubjectCIRun}
	input.AliasClaimants = map[string][]IdentityMatch{
		"dev-health-acr": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchAlias}},
	}
	f := withCensus(contractsv1.ContextFabricSubjectCIRun, CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "run-1"}, nil)
	input.CensusFunc = f.fn
	att := RunShadowEvidenceRound(context.Background(), input, nil)
	if att.Outcome != ShadowWouldCommit {
		t.Fatalf("att = %#v, want would_commit", att)
	}
	if f.calls != 1 {
		t.Fatalf("census calls = %d, want exactly 1 -- no insensitivity proof runs absent PreNarrowingExplicitKinds", f.calls)
	}
	if att.KindInsensitivityEvaluated {
		t.Fatalf("att.KindInsensitivityEvaluated = true, want false -- the proof must never be consulted absent PreNarrowingExplicitKinds (CHAOS-4039)")
	}
}

// TestRunShadowEvidenceRound_ExplicitKindNarrowing_HandleAppendedKindJoinsTheProof
// is the codex xhigh review round-2 regression pin (CHAOS-3972): a bound
// handle can name a census kind that was NEVER in the pre-narrowing
// candidate pool at all (design brief: a handle is decisive alone,
// independent of pooling) -- the main census loop appends that kind via
// appendUniqueCensusKind, and the insensitivity proof MUST mirror the same
// append onto its own pre-narrowing kind set, or it certifies soundness
// over an incomplete hypothesis space.
//
// Scenario (exactly codex's own repro): pre-narrowing pool =
// [pull_request, pull_request_review]; an explicit expected_kinds=[pull_request]
// narrows PooledKinds to [pull_request] alone; the question ALSO binds a
// ci_pipeline_run handle -- a kind absent from both the narrowed AND the
// pre-narrowing pool. The round's own narrowed census (pull_request=0,
// appended ci_pipeline_run=1 via the handle) reaches would_commit. The
// TRUE all-kinds union also includes pull_request_review=1 (reachable only
// via the shared repository anchor) -- two real satisfiers, not one -- so
// the round's own commit is UNSOUND and must be demoted.
func TestRunShadowEvidenceRound_ExplicitKindNarrowing_HandleAppendedKindJoinsTheProof(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.Question = "run 18234567"
	input.PooledKinds = []CensusKind{contractsv1.ContextFabricSubjectPullRequest}
	input.PreNarrowingExplicitKinds = []CensusKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectPullRequestReview}
	input.AliasClaimants = map[string][]IdentityMatch{
		"dev-health-acr": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchAlias}},
	}
	f := &fakeCensus{byKind: map[CensusKind][]struct {
		outcome CensusOutcome
		err     error
	}{
		// pull_request: censused twice (round's own narrowed census, then
		// the proof's all-kinds re-census) -- zero satisfiers both times.
		contractsv1.ContextFabricSubjectPullRequest: {
			{outcome: CensusOutcome{Count: 0, CensusReadAt: time.Now().UTC()}},
			{outcome: CensusOutcome{Count: 0, CensusReadAt: time.Now().UTC()}},
		},
		// ci_pipeline_run: censused twice (the round's own handle-appended
		// census, then the proof's own matching append) -- one satisfier
		// both times, the SAME satisfier the round's own would_commit
		// rests on entirely.
		contractsv1.ContextFabricSubjectCIRun: {
			{outcome: CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "run-18234567"}},
			{outcome: CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "run-18234567"}},
		},
		// pull_request_review: censused ONLY by the proof (never by the
		// round's own narrowed census) -- one satisfier the narrowed round
		// never saw at all.
		contractsv1.ContextFabricSubjectPullRequestReview: {
			{outcome: CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "review-1"}},
		},
	}}
	input.CensusFunc = f.fn
	att := RunShadowEvidenceRound(context.Background(), input, nil)
	if att.Outcome != ShadowWouldClarify || att.Reason != ReasonKindSensitiveOutcome {
		t.Fatalf("att = %#v, want would_clarify/kind_sensitive_outcome -- the narrowed round's commit rests on a handle-appended kind the proof must also consider, and the true all-kinds union has TWO satisfiers (ci_pipeline_run + pull_request_review), not one", att)
	}
	if f.calls != 5 {
		t.Fatalf("census calls = %d, want 5 (2 pull_request + 2 ci_pipeline_run + 1 pull_request_review)", f.calls)
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

// TestRunShadowEvidenceRound_ConfirmedAnchorOverridesBindAnchor is CHAOS-4042's
// (sol-max ruling) own proof that a redeemed anchor receipt actually becomes
// the census discriminator, closing the finding-#3 gap the ruling named:
// "confirmed anchor receipts today are reverified and echoed but never
// become the census discriminator". input.AliasClaimants is set up so
// BindAnchor would derive a DIFFERENT unique claimant (a decoy) than
// input.ConfirmedAnchor names -- proving the confirmed value TAKES
// PRIORITY over BindAnchor's own re-derivation (chaos3899_evidence_round.go's
// own doc comment on ConfirmedAnchor), not merely that it is consulted
// alongside it.
func TestRunShadowEvidenceRound_ConfirmedAnchorOverridesBindAnchor(t *testing.T) {
	t.Parallel()
	input := baseInput()
	input.PooledKinds = []CensusKind{contextfabric.SubjectPullRequest}
	input.AliasClaimants = map[string][]IdentityMatch{
		"decoy-repo": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:decoy-1"}, Mechanism: contextfabric.MatchAlias}},
	}
	input.ConfirmedAnchor = &AnchorBinding{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:confirmed-1"}

	var gotAnchorKind contextfabric.SubjectKind
	var gotAnchorCanonicalID string
	var gotAnchorBound bool
	f := withCensus(contextfabric.SubjectPullRequest, CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierNaturalKey: "pr-1"}, nil)
	input.CensusFunc = func(ctx context.Context, orgID string, kind CensusKind, handleValue string, handleBound bool, anchorKind contextfabric.SubjectKind, anchorCanonicalID string, anchorBound bool) (CensusOutcome, error) {
		gotAnchorKind, gotAnchorCanonicalID, gotAnchorBound = anchorKind, anchorCanonicalID, anchorBound
		return f.fn(ctx, orgID, kind, handleValue, handleBound, anchorKind, anchorCanonicalID, anchorBound)
	}

	tracer := &captureResolutionTracer{}
	att := RunShadowEvidenceRound(context.Background(), input, tracer)

	if !gotAnchorBound || gotAnchorKind != contextfabric.SubjectRepository || gotAnchorCanonicalID != "repository:confirmed-1" {
		t.Fatalf("census anchor discriminator = (kind=%q, canonical_id=%q, bound=%v), want the CONFIRMED anchor (repository:confirmed-1), not BindAnchor's own decoy derivation", gotAnchorKind, gotAnchorCanonicalID, gotAnchorBound)
	}
	if !att.AnchorReceiptConfirmed {
		t.Errorf("att.AnchorReceiptConfirmed = false, want true")
	}
	if att.AnchorUniqueClaimant {
		t.Errorf("att.AnchorUniqueClaimant = true, want false -- a confirmed receipt is a DIFFERENT proof than BindAnchor's own uniqueness scan, never both true for the same round")
	}
	assertSingleEvidenceRoundEvent(t, tracer)
}

func assertSingleEvidenceRoundEvent(t *testing.T, tracer *captureResolutionTracer) {
	t.Helper()
	if got := len(tracer.eventsForStage("evidence_round")); got != 1 {
		t.Fatalf("evidence_round events = %d, want 1", got)
	}
}
