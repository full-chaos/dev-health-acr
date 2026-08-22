package contextfabric

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4098 tests.
//
// The defect is a VALIDATION failure, so the consumer under test is the
// real InvestigationResult.Validate() -- never a hand-rolled restatement of
// its rules. Every test below either runs the engine end to end (which
// calls Validate itself, on the real object) or calls Validate directly on
// the composed value. A test that asserted only "Status == no_match" would
// pass against a fix that still produced an unservable result.

// clarificationDraftEngine builds an engine whose synthesis step returns
// draftStatus, over a resolution that has COMMITTED a subject -- so
// SubjectResolution.ClarificationPrompt is empty, exactly as it is on every
// decisive path.
//
// provenCommitBases: the commit basis is deliberately PROVEN so CHAOS-4085's
// affirmation gate never fires. This ticket's defect must be reproduced with
// that gate exempted, which is the evidence it is not the gate's doing.
func clarificationDraftEngine(t *testing.T, telemetry EngineTelemetry, draftStatus InvestigationStatus) (*Engine, InvestigationRequest) {
	t.Helper()
	interpretation := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "release_readiness_and_drivers",
		TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graphReaderStub{
			resolution: SubjectResolution{
				Candidates: []SubjectCandidate{affirmationCandidate(ResolutionCommitted)},
				Committed:  []SubjectRef{affirmationSubject},
			},
			context: emptyAffirmationGraph(),
			bases:   provenCommitBases(affirmationSubject),
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return emptyAffirmationFacts(), nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			draft := affirmationResult()
			draft.SubjectResolution = SubjectResolution{}
			draft.Status = draftStatus
			draft.Versions = VersionSet{
				Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
				InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
			}
			return draft, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(100, 0).UTC() },
		NewResultID:    func() string { return "result_12345678" },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.Question = "which work item is blocked?"
	return engine, request
}

// ---------------------------------------------------------------------------
// The case-60 regression pin
// ---------------------------------------------------------------------------

// TestChaos4098_ClarificationDraftOnDecisivePathIsServedNotRejected is the
// regression test for the observed defect: the v9 rerun's case 60,
// member=expected_kind, arm=inferred_tier, hinted call. The synthesis model
// returned status clarification_required over a committed resolution, and
// Investigate returned ErrInvalidResult ("clarification result requires a
// prompt") instead of an answer.
//
// Reverting applySynthesisStatusOverride's call in engine.go fails this
// test with exactly that message.
func TestChaos4098_ClarificationDraftOnDecisivePathIsServedNotRejected(t *testing.T) {
	telemetry := &recordingTelemetry{}
	engine, request := clarificationDraftEngine(t, telemetry, InvestigationClarificationRequired)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("a clarification-status draft must be served, not rejected: %v", err)
	}
	// The real consumer, run explicitly: Investigate already validated this
	// object, but naming Validate here is what makes the test fail loudly
	// if the engine's own call site is ever moved or removed.
	if err := result.Validate(); err != nil {
		t.Fatalf("the served result must be a valid InvestigationResult: %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("status = %q, want no_match", result.Status)
	}
	if !hasLimitation(result.Limitations, synthesisClarificationUnavailableLimitation) {
		t.Fatalf("the served answer must disclose the override, got %#v", result.Limitations)
	}
	if !result.Coverage.Partial {
		t.Fatal("an answer whose synthesis declined to conclude does not cover what it set out to")
	}
}

// TestChaos4098_TheOverrideIsWhatMakesTheResultValid proves the causal
// claim directly rather than by inference: the SAME composed object fails
// the real validator before the override and passes after it. Without this,
// a fix that happened to change something else could still pass the
// end-to-end test above.
func TestChaos4098_TheOverrideIsWhatMakesTheResultValid(t *testing.T) {
	result := decisiveClarificationResult()

	beforeErr := result.Validate()
	if beforeErr == nil {
		t.Fatal("the pre-override object must be invalid -- if this passes, the contract rule this ticket exists for is gone and the override is untested")
	}
	if !strings.Contains(beforeErr.Error(), "clarification result requires a prompt") {
		t.Fatalf("pre-override validation failed for an unexpected reason: %v", beforeErr)
	}

	if outcome := applySynthesisStatusOverride(&result); outcome == nil {
		t.Fatal("the override must fire on this shape")
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the post-override object must validate: %v", err)
	}
}

// decisiveClarificationResult is the composed shape the engine holds when a
// synthesis draft asks for clarification on a decisive path: a committed
// subject, no clarification prompt, and the server-composed prose rendered
// for the clarification status.
func decisiveClarificationResult() InvestigationResult {
	result := affirmationResult()
	result.SchemaVersion = InvestigationResultSchemaV1
	result.ResultID = "result_12345678"
	result.RequestID = "request_12345678"
	result.GeneratedAt = time.Unix(100, 0).UTC()
	result.Question = "which work item is blocked?"
	result.Interpretation = InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "release_readiness_and_drivers",
		TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{},
		SubjectTerms: []string{}, ComparisonTerms: []string{},
	}
	result.Versions = VersionSet{
		ServiceVersion: "acr-test", ContractVersion: InvestigationResultSchemaV1,
		Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
		InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
		CanonicalServiceVersion: "ops-v1", ModelIdentity: "unwired",
	}
	result.Status = InvestigationClarificationRequired
	result.DirectJudgment = composeDirectJudgmentFrom(result.Status, result.Drivers, result.SubjectResolution)
	result.DeterministicAnswer = composeDeterministicAnswerFrom(result.Status, result.Drivers, result.ClaimedFacts, result.SubjectResolution)
	return result
}

// ---------------------------------------------------------------------------
// Invariants
// ---------------------------------------------------------------------------

// TestChaos4098_ARealClarificationTerminalIsUntouched is the false-positive
// pin. The engine's OWN clarification terminals (unresolved.go, window.go)
// carry the prompt that makes them actionable, and this override must never
// rewrite one of them into a no_match -- that would turn a question the
// caller could answer into an answer they cannot act on.
func TestChaos4098_ARealClarificationTerminalIsUntouched(t *testing.T) {
	result := decisiveClarificationResult()
	result.SubjectResolution.ClarificationPrompt = "Which of these subjects did you mean?"

	before := result
	if outcome := applySynthesisStatusOverride(&result); outcome != nil {
		t.Fatalf("a genuine clarification terminal must not be overridden, got %+v", outcome)
	}
	if result.Status != before.Status || result.DirectJudgment != before.DirectJudgment ||
		result.DeterministicAnswer != before.DeterministicAnswer || len(result.Limitations) != len(before.Limitations) {
		t.Fatal("a genuine clarification terminal must be left byte-identical")
	}
}

// TestChaos4098_AWhitespaceOnlyPromptIsAbsent pins the trimming rule: a
// padded prompt is NOT a prompt, and the override still fires.
//
// The object is not asserted valid afterwards, deliberately. A padded
// prompt independently violates the contract's own trimming rule
// ("clarification prompt violates v1 bounds"), so this shape can never be
// served whatever the status is -- which is precisely why treating it as
// PRESENT would be the wrong reading: the override would decline, the
// status would stay clarification_required, and the result would fail on
// the missing-prompt rule as well. What this test pins is that the
// override's own notion of "absent" matches the validator's, not that a
// malformed prompt can be rescued.
func TestChaos4098_AWhitespaceOnlyPromptIsAbsent(t *testing.T) {
	result := decisiveClarificationResult()
	result.SubjectResolution.ClarificationPrompt = "   "
	if outcome := applySynthesisStatusOverride(&result); outcome == nil {
		t.Fatal("a whitespace-only prompt is not a prompt; the override must fire")
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("status = %q, want no_match", result.Status)
	}
	// Same object with the padding removed IS servable -- proving the
	// override did its whole job and only the independent trimming rule
	// stands between this shape and a valid result.
	result.SubjectResolution.ClarificationPrompt = ""
	if err := result.Validate(); err != nil {
		t.Fatalf("post-override validation: %v", err)
	}
}

// TestChaos4098_OverrideRecomposesResolutionDependentServerText is the
// honesty pin. DirectJudgment and DeterministicAnswer both open with
// statusSentence(status, resolution); leaving them rendered for the OLD
// status would ship a result whose primary answer field says clarification
// is required while its status says no_match.
func TestChaos4098_OverrideRecomposesResolutionDependentServerText(t *testing.T) {
	result := decisiveClarificationResult()
	if !strings.Contains(result.DirectJudgment, "Clarification is required") {
		t.Fatalf("fixture precondition: pre-override prose must carry the clarification sentence, got %q", result.DirectJudgment)
	}

	applySynthesisStatusOverride(&result)

	for name, got := range map[string]string{"direct_judgment": result.DirectJudgment, "deterministic_answer": result.DeterministicAnswer} {
		if strings.Contains(got, "Clarification is required") {
			t.Fatalf("%s still asserts clarification on a no_match result: %q", name, got)
		}
	}
	// Equal to what the SHARED renderers produce, not merely "different":
	// the point of recomposing rather than patching is that an overridden
	// result is indistinguishable from one composed at no_match to begin
	// with.
	wantJudgment := composeDirectJudgmentFrom(InvestigationNoMatch, result.Drivers, result.SubjectResolution)
	wantAnswer := composeDeterministicAnswerFrom(InvestigationNoMatch, result.Drivers, result.ClaimedFacts, result.SubjectResolution)
	if result.DirectJudgment != wantJudgment {
		t.Fatalf("direct_judgment = %q, want the shared renderer's output %q", result.DirectJudgment, wantJudgment)
	}
	if result.DeterministicAnswer != wantAnswer {
		t.Fatalf("deterministic_answer = %q, want the shared renderer's output %q", result.DeterministicAnswer, wantAnswer)
	}
}

// TestChaos4098_OverrideNeverFiresOnAnyOtherStatus pins the narrow trigger.
// A status the engine CAN serve is never rewritten, however weak it is.
func TestChaos4098_OverrideNeverFiresOnAnyOtherStatus(t *testing.T) {
	for _, status := range []InvestigationStatus{
		InvestigationComplete, InvestigationPartial, InvestigationDegraded, InvestigationNoMatch,
	} {
		result := decisiveClarificationResult()
		result.Status = status
		result.DirectJudgment = composeDirectJudgmentFrom(status, result.Drivers, result.SubjectResolution)
		before := result.DirectJudgment
		if outcome := applySynthesisStatusOverride(&result); outcome != nil {
			t.Fatalf("status %q must not be overridden, got %+v", status, outcome)
		}
		if result.Status != status || result.DirectJudgment != before || result.Coverage.Partial {
			t.Fatalf("status %q was mutated by an override that must not have fired", status)
		}
	}
}

// TestChaos4098_OverrideNeverTouchesTheResolution pins that this is a
// STATUS decision, not a subject one. Committing, retracting and
// clarification-prompt minting all belong to other layers.
func TestChaos4098_OverrideNeverTouchesTheResolution(t *testing.T) {
	result := decisiveClarificationResult()
	before := result.SubjectResolution

	applySynthesisStatusOverride(&result)

	after := result.SubjectResolution
	if len(after.Committed) != len(before.Committed) || len(after.Candidates) != len(before.Candidates) ||
		after.ClarificationPrompt != before.ClarificationPrompt {
		t.Fatal("the override must read the resolution and never write it")
	}
	if after.Candidates[0].State != ResolutionCommitted {
		t.Fatalf("candidate state = %q, want it untouched at committed", after.Candidates[0].State)
	}
	if len(after.Committed) == 0 {
		t.Fatal("the override must not retract a commit -- that is CHAOS-4085's decision, not this one")
	}
}

// TestChaos4098_OverrideIsIdempotent pins that a second application is a
// no-op: no duplicate disclosure, no double-counted displacement.
func TestChaos4098_OverrideIsIdempotent(t *testing.T) {
	result := decisiveClarificationResult()
	applySynthesisStatusOverride(&result)
	first := result

	if outcome := applySynthesisStatusOverride(&result); outcome != nil {
		t.Fatalf("a second application must be a no-op, got %+v", outcome)
	}
	if len(result.Limitations) != len(first.Limitations) || result.LimitationsDisplaced != first.LimitationsDisplaced {
		t.Fatal("a second application changed the disclosure or the displacement count")
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("post-idempotency validation: %v", err)
	}
}

// TestChaos4098_DisclosureIsServiceAuthoredAndLeakFree holds the same line
// commitRetractionLimitation holds: never displaced to make room for
// another disclosure, and naming nothing an operator-facing field should
// carry instead.
func TestChaos4098_DisclosureIsServiceAuthoredAndLeakFree(t *testing.T) {
	if !isServiceAuthoredLimitation(synthesisClarificationUnavailableLimitation) {
		t.Fatal("the disclosure must be service-authored, or a full limitation list can displace it")
	}
	for _, forbidden := range []string{
		string(InvestigationClarificationRequired), string(InvestigationNoMatch),
		"model", "synthesis", "status", affirmationSubject.CanonicalID, affirmationSubject.Label,
	} {
		if strings.Contains(strings.ToLower(synthesisClarificationUnavailableLimitation), strings.ToLower(forbidden)) {
			t.Fatalf("the disclosure names %q -- answer-facing prose carries no vocabulary, identity, or mechanism", forbidden)
		}
	}
	if length := len(synthesisClarificationUnavailableLimitation); length < 1 || length > contractsv1.ContextFabricLimitationMaxLength {
		t.Fatalf("the disclosure is %d characters, outside the contract bound", length)
	}
}

// ---------------------------------------------------------------------------
// Telemetry -- struct level, then SINK level
// ---------------------------------------------------------------------------

// TestChaos4098_OverrideReachesTheEngineTelemetry proves the branch is
// wired to a sink at all, with the exact values rather than a bare count.
func TestChaos4098_OverrideReachesTheEngineTelemetry(t *testing.T) {
	telemetry := &recordingTelemetry{}
	engine, request := clarificationDraftEngine(t, telemetry, InvestigationClarificationRequired)

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	if len(telemetry.synthesisStatusOverrides) != 1 {
		t.Fatalf("expected one override event, got %d", len(telemetry.synthesisStatusOverrides))
	}
	got := telemetry.synthesisStatusOverrides[0]
	want := SynthesisStatusOverrideOutcome{
		From: InvestigationClarificationRequired, To: InvestigationNoMatch,
		Reason: SynthesisStatusOverrideClarificationUnavailable, CommittedCount: 1,
	}
	if got != want {
		t.Fatalf("override event = %+v, want %+v", got, want)
	}
}

// TestChaos4098_NoOverrideEmitsNoTelemetry is the counterpart: an ordinary
// answer must not produce a spurious event, or the rate signal is useless.
func TestChaos4098_NoOverrideEmitsNoTelemetry(t *testing.T) {
	telemetry := &recordingTelemetry{}
	engine, request := clarificationDraftEngine(t, telemetry, InvestigationPartial)

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	if len(telemetry.synthesisStatusOverrides) != 0 {
		t.Fatalf("an unoverridden answer emitted %d events", len(telemetry.synthesisStatusOverrides))
	}
}

// TestChaos4098_ProductionTelemetryEmitsAnOverride is the SINK-level pin --
// see chaos4085_telemetry_sink_test.go's own header for why a struct-level
// assertion is not enough. This branch's signal must survive as real bytes
// an operator receives.
//
// The compile-time proof is the load-bearing half here too, but in the
// opposite direction from CHAOS-4085's: RecordSynthesisStatusOverride is
// declared on EngineTelemetry itself, so a sink that drops it cannot build.
// The assertion states that dependency rather than leaving it implicit.
func TestChaos4098_ProductionTelemetryEmitsAnOverride(t *testing.T) {
	var _ EngineTelemetry = SlogEngineTelemetry{}

	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordSynthesisStatusOverride(
			context.Background(),
			storage.Principal{OrgID: "org_sink_test"},
			SynthesisStatusOverrideOutcome{
				From: InvestigationClarificationRequired, To: InvestigationNoMatch,
				Reason: SynthesisStatusOverrideClarificationUnavailable, CommittedCount: 1,
			},
		)
	})
	if len(records) != 1 {
		t.Fatalf("an override must produce exactly one log record, got %d", len(records))
	}
	record := records[0]
	for key, want := range map[string]any{
		"org_id":      "org_sink_test",
		"from_status": string(InvestigationClarificationRequired),
		"to_status":   string(InvestigationNoMatch),
		"reason":      string(SynthesisStatusOverrideClarificationUnavailable),
	} {
		if got, ok := record[key]; !ok || got != want {
			t.Fatalf("record[%q] = %v (present=%v), want %v -- an operator greps for this key", key, got, ok, want)
		}
	}
	if got, ok := record["committed_count"].(float64); !ok || got != 1 {
		t.Fatalf("record[\"committed_count\"] = %v, want 1", record["committed_count"])
	}
}

// TestChaos4098_OverrideTelemetryLeaksNoIdentityAndStaysAtWarn mirrors
// CHAOS-4085's allow-list oracle: a new field fails by default and has to
// be argued for.
func TestChaos4098_OverrideTelemetryLeaksNoIdentityAndStaysAtWarn(t *testing.T) {
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordSynthesisStatusOverride(
			context.Background(),
			storage.Principal{OrgID: "org_sink_test"},
			SynthesisStatusOverrideOutcome{
				From: InvestigationClarificationRequired, To: InvestigationNoMatch,
				Reason: SynthesisStatusOverrideClarificationUnavailable, CommittedCount: 0,
			},
		)
	})
	if len(records) != 1 {
		t.Fatalf("expected one record, got %d", len(records))
	}
	record := records[0]
	permitted := map[string]struct{}{
		"time": {}, "level": {}, "msg": {},
		"org_id": {}, "from_status": {}, "to_status": {}, "reason": {},
		"committed_count": {}, "request_id": {},
	}
	for key := range record {
		if _, ok := permitted[key]; !ok {
			t.Fatalf("override telemetry emits unpermitted key %q -- if this field is genuinely safe, add it to the allow-list deliberately", key)
		}
	}
	if got := record["level"]; got != "WARN" {
		t.Fatalf("level = %v, want WARN -- see RecordSynthesisStatusOverride's own doc comment for why", got)
	}
}
