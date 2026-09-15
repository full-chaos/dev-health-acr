package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// completenessAuthorityTestEngine builds an Engine directly (not through
// mustReuseTestEngine, which hardcodes EngineOptions) so these tests can
// set Telemetry and ServerCompletenessAuthorityEnabled explicitly.
func completenessAuthorityTestEngine(t *testing.T, deps EngineDependencies, enabled bool, symmetricEnabled bool) *Engine {
	t.Helper()
	if deps.Graph == nil {
		deps.Graph = bindingOnlyGraphReader{t: t}
	}
	if deps.Interpreter == nil {
		deps.Interpreter = interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			t.Fatal("interpreter should not be reached")
			return InterpretedQuestion{}, nil
		})
	}
	if deps.Facts == nil {
		deps.Facts = failingFactReader{t: t}
	}
	if deps.Synthesizer == nil {
		deps.Synthesizer = synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("synthesizer should not be reached")
			return InvestigationResult{}, nil
		})
	}
	if deps.Results == nil {
		deps.Results = &resultStoreStub{}
	}
	engine, err := NewEngine(deps, EngineOptions{
		ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID:                                 func() string { return "result_fresh_00001" },
		ServerCompletenessAuthorityEnabled:          enabled,
		ServerCompletenessAuthoritySymmetricEnabled: symmetricEnabled,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// degradedOutcomeCandidate returns a stored InvestigationResult whose model
// status is `complete` but whose own outcome rows say `degraded` -- the
// shape the reuse and by-id serving paths must not serve unmeasured.
func degradedOutcomeCandidate() (SubjectRef, InvestigationResult) {
	project, candidate := reusableCandidate()
	candidate.Completeness.Outcomes = []RequirementOutcomeRow{{
		Stage:         contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement:   "evidence/subject/team",
		Obligation:    "evidence",
		Outcome:       contractsv1.ContextFabricRequirementUnavailable,
		Impact:        contractsv1.ContextFabricAnswerImpactDimension,
		CauseCoverage: contractsv1.ContextFabricCoverageDetailFactUnconfigured,
		CauseObserved: true,
	}}
	candidate.Completeness = ComputeAnswerCompleteness(candidate)
	if candidate.Completeness.State != contractsv1.ContextFabricAnswerCompletenessDegraded {
		panic("test setup: expected the fixture's own outcome rows to derive degraded")
	}
	return project, candidate
}

// outcomeAuthorityCandidate is degradedOutcomeCandidate's general form:
// a stored, reuse-eligible InvestigationResult with the given model status,
// whose own outcome rows derive serverState. The every-direction pins
// below need every ordered pair among complete/partial/degraded, not only
// the one degradedOutcomeCandidate fixes on.
func outcomeAuthorityCandidate(modelStatus InvestigationStatus, serverState contractsv1.ContextFabricAnswerCompletenessState) (SubjectRef, InvestigationResult) {
	project, candidate := reusableCandidate()
	candidate.Status = modelStatus
	switch serverState {
	case contractsv1.ContextFabricAnswerCompletenessComplete:
		candidate.Completeness.Outcomes = []RequirementOutcomeRow{{
			Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: "evidence/subject/team",
			Obligation: "evidence", Outcome: contractsv1.ContextFabricRequirementSatisfied,
		}}
	case contractsv1.ContextFabricAnswerCompletenessPartial:
		candidate.Completeness.Outcomes = []RequirementOutcomeRow{{
			Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: "ranking/subject/team",
			Obligation: "ranking", Outcome: contractsv1.ContextFabricRequirementNarrowed,
		}}
	case contractsv1.ContextFabricAnswerCompletenessDegraded:
		candidate.Completeness.Outcomes = []RequirementOutcomeRow{{
			Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: "evidence/subject/team",
			Obligation: "evidence", Outcome: contractsv1.ContextFabricRequirementUnavailable,
			Impact: contractsv1.ContextFabricAnswerImpactDimension, CauseCoverage: contractsv1.ContextFabricCoverageDetailFactUnconfigured, CauseObserved: true,
		}}
	default:
		panic("outcomeAuthorityCandidate: unsupported serverState " + string(serverState))
	}
	candidate.Completeness = ComputeAnswerCompleteness(candidate)
	if candidate.Completeness.State != serverState {
		panic("test setup: expected the fixture's own outcome rows to derive " + string(serverState) + ", got " + string(candidate.Completeness.State))
	}
	return project, candidate
}

// TestCompletenessAuthority_EveryDirectionEveryFlagState exercises every
// (model, server) disagreement pair against every combination of the two
// gated flags, through Engine.Investigate (the reuse-hit path) rather than
// the bare function: DERIVATION (Direction, Disagreed, WouldFlip) is
// unconditionally symmetric and identical across every flag combination;
// SERVICE is gated exactly by the two flags' documented scope -- the
// complete-side pair by `enabled`, the lateral partial/degraded pair by
// `symmetricEnabled` -- and no pair is ever promoted to complete, under any
// combination of the two flags.
func TestCompletenessAuthority_EveryDirectionEveryFlagState(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name          string
		modelStatus   InvestigationStatus
		serverState   contractsv1.ContextFabricAnswerCompletenessState
		wantDirection CompletenessAuthorityDirection
		// gate names which flag (if any) must be true for the correction to
		// be SERVED: "enabled", "symmetric", or "never".
		gate string
	}{
		{"complete_to_partial", InvestigationComplete, contractsv1.ContextFabricAnswerCompletenessPartial, CompletenessAuthorityDirectionCompleteToPartial, "enabled"},
		{"complete_to_degraded", InvestigationComplete, contractsv1.ContextFabricAnswerCompletenessDegraded, CompletenessAuthorityDirectionCompleteToDegraded, "enabled"},
		{"partial_to_degraded", InvestigationPartial, contractsv1.ContextFabricAnswerCompletenessDegraded, CompletenessAuthorityDirectionPartialToDegraded, "symmetric"},
		{"degraded_to_partial", InvestigationDegraded, contractsv1.ContextFabricAnswerCompletenessPartial, CompletenessAuthorityDirectionDegradedToPartial, "symmetric"},
		{"partial_to_complete_never_served", InvestigationPartial, contractsv1.ContextFabricAnswerCompletenessComplete, CompletenessAuthorityDirectionPartialToComplete, "never"},
		{"degraded_to_complete_never_served", InvestigationDegraded, contractsv1.ContextFabricAnswerCompletenessComplete, CompletenessAuthorityDirectionDegradedToComplete, "never"},
	} {
		testCase := testCase
		for _, flagCase := range []struct {
			name             string
			enabled          bool
			symmetricEnabled bool
		}{
			{"both_off", false, false},
			{"asymmetric_only", true, false},
			{"symmetric_only", false, true},
			{"both_on", true, true},
		} {
			flagCase := flagCase
			t.Run(testCase.name+"/"+flagCase.name, func(t *testing.T) {
				t.Parallel()
				project, candidate := outcomeAuthorityCandidate(testCase.modelStatus, testCase.serverState)
				telemetry := &recordingTelemetry{}
				engine := completenessAuthorityTestEngine(t, EngineDependencies{
					Graph:     graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
					Telemetry: telemetry,
					ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
						return candidate, true, nil
					}),
				}, flagCase.enabled, flagCase.symmetricEnabled)

				result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
				if err != nil {
					t.Fatalf("Investigate() error = %v", err)
				}
				if len(telemetry.completenessAuthorities) != 1 {
					t.Fatalf("completenessAuthorities recorded = %d, want 1", len(telemetry.completenessAuthorities))
				}
				observation := telemetry.completenessAuthorities[0]
				if observation.Direction != testCase.wantDirection {
					t.Fatalf("Direction = %q, want %q -- the measurement must be identical regardless of either flag", observation.Direction, testCase.wantDirection)
				}
				if !observation.Disagreed || !observation.WouldFlip {
					t.Fatalf("Disagreed=%v WouldFlip=%v, want both true (model=%q server=%q disagree by construction)", observation.Disagreed, observation.WouldFlip, testCase.modelStatus, testCase.serverState)
				}

				servedShouldFlip := (testCase.gate == "enabled" && flagCase.enabled) || (testCase.gate == "symmetric" && flagCase.symmetricEnabled)
				wantServed := testCase.modelStatus
				if servedShouldFlip {
					mapped, ok := answerCompletenessStateToStatus(testCase.serverState)
					if !ok {
						t.Fatalf("test setup: serverState %q does not map to a status", testCase.serverState)
					}
					wantServed = mapped
				}
				if result.Status != wantServed {
					t.Fatalf("result.Status = %q, want %q (enabled=%v symmetricEnabled=%v, gate=%q)", result.Status, wantServed, flagCase.enabled, flagCase.symmetricEnabled, testCase.gate)
				}
				if result.Completeness.TerminalStatus != result.Status {
					t.Fatalf("result.Completeness.TerminalStatus = %q, must equal result.Status %q", result.Completeness.TerminalStatus, result.Status)
				}
			})
		}
	}
}

// TestCompletenessAuthority_ReuseHitIsMeasured pins that a reuse hit is
// measured too: the reuse path returns from Investigate before ever
// reaching the decisive path's own telemetry point, so the measurement
// must live at finalizeServed -- the one point both paths share -- or a
// served cache hit goes unmeasured.
func TestCompletenessAuthority_ReuseHitIsMeasured(t *testing.T) {
	t.Parallel()

	project, candidate := degradedOutcomeCandidate()
	telemetry := &recordingTelemetry{}
	engine := completenessAuthorityTestEngine(t, EngineDependencies{
		Graph:     graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Telemetry: telemetry,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	}, false, false)

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("premise: this must be a reuse hit")
	}
	if len(telemetry.completenessAuthorities) != 1 {
		t.Fatalf("completenessAuthorities recorded = %d, want 1 -- a reuse hit must be measured, not silently skipped", len(telemetry.completenessAuthorities))
	}
	observation := telemetry.completenessAuthorities[0]
	if !observation.Derived || observation.ServerState != contractsv1.ContextFabricAnswerCompletenessDegraded {
		t.Fatalf("observation = %+v, want a derived degraded state from the reused candidate's own outcome rows", observation)
	}
	if !observation.Disagreed {
		t.Fatalf("observation.Disagreed = false, want true: the reused document says complete, the outcome rows say degraded")
	}
}

// TestCompletenessAuthority_ReuseHitIsCorrectedWhenEnabled proves the flip
// reaches the reuse path too, once wired through finalizeServed.
func TestCompletenessAuthority_ReuseHitIsCorrectedWhenEnabled(t *testing.T) {
	t.Parallel()

	project, candidate := degradedOutcomeCandidate()
	engine := completenessAuthorityTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	}, true, false)

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("premise: this must be a reuse hit")
	}
	if result.Status != InvestigationDegraded {
		t.Fatalf("result.Status = %q, want degraded -- the flip must reach a served reuse hit when enabled", result.Status)
	}
	if result.Completeness.TerminalStatus != result.Status {
		t.Fatalf("result.Completeness.TerminalStatus = %q, must equal result.Status %q", result.Completeness.TerminalStatus, result.Status)
	}
	// The flip changes Status, which the terminal reason is derived FROM
	// -- the served document must stay internally consistent as a whole,
	// not merely agree on the one field the flip wrote directly.
	if want := contractsv1.ContextFabricTerminalReasonUndisclosed; result.Completeness.TerminalReason != want {
		t.Fatalf("result.Completeness.TerminalReason = %q, want %q (no Coverage/Limitations/Warnings disclosure on this fixture)", result.Completeness.TerminalReason, want)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("a flipped result must still validate as a whole document: %v", err)
	}
}

// TestCompletenessAuthority_NonAnswerTerminalsAreMeasured pins that a
// clarification/refusal/no_match terminal is measured too, reporting
// `not_an_answer`: the measurement lives at finalizeServed, which every
// terminal exit calls, never only at the decisive path's own telemetry
// point.
//
// Reuses the interpreted-time-bound exit's exact fixture shape from
// TestEveryBudgetAssertStageIsReachedByARealTerminal (budget_assertion_test.go)
// -- an interpreter handing back an unanswerable time bound refuses BEFORE
// resolution, facts or synthesis, landing on InvestigationNoMatch through
// finalizeServed(BudgetAssertInterpretedTimeBound, ...).
func TestCompletenessAuthority_NonAnswerTerminalsAreMeasured(t *testing.T) {
	t.Parallel()

	telemetry := &recordingTelemetry{}
	ancient := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC).Add(-3000 * 24 * time.Hour)
	end := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	unanswerable := bootstrapInterpretation()
	unanswerable.TimeContext = TimeContext{Axis: TemporalRange, Start: &ancient, End: &end}
	interpreter := &countingInterpreter{interpretation: unanswerable}
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	engine := buildWindowGateEngineWithBudget(t, interpreter, graph, newMapResultStore(), 0, telemetry)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want no_match (sanity check: the intended exit was not taken, so this test proves nothing)", result.Status)
	}
	if len(telemetry.completenessAuthorities) != 1 {
		t.Fatalf("completenessAuthorities recorded = %d, want 1 -- a non-answer terminal must still be measured (reporting not_an_answer), not skipped", len(telemetry.completenessAuthorities))
	}
	if got := telemetry.completenessAuthorities[0].Basis; got != CompletenessAuthorityBasisNotAnAnswer {
		t.Fatalf("Basis = %q, want not_an_answer", got)
	}
}

// TestFinalizeServed_BudgetMeasuresThePostFlipDocument pins the ONE
// observable reason the flip must run BEFORE the budget assertion inside
// finalizeServed: the flip changes result.Status, which changes the
// terminal reason ComputeAnswerCompleteness derives from it, which is
// bytes in the served document. A budget check that ran against the
// pre-flip document could certify a fit the actually-served, flipped
// document does not have.
//
// The two byte counts are MEASURED, not assumed: if this fixture's flip
// ever stops changing the serialized size, the premise check below fails
// loudly instead of the rest of the test silently proving nothing.
func TestFinalizeServed_BudgetMeasuresThePostFlipDocument(t *testing.T) {
	t.Parallel()

	project, candidate := degradedOutcomeCandidate()
	preFlipMeasurement, err := contractsv1.MeasureContextFabricResponse(candidate)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse(pre-flip) error = %v", err)
	}
	flipped := ApplyServerCompletenessAuthority(candidate, true, false, DeriveCompletenessAuthority(candidate))
	if flipped.Status != InvestigationDegraded {
		t.Fatalf("setup: expected the flip to degrade, got %q", flipped.Status)
	}
	postFlipMeasurement, err := contractsv1.MeasureContextFabricResponse(flipped)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse(post-flip) error = %v", err)
	}
	if postFlipMeasurement.Bytes <= preFlipMeasurement.Bytes {
		t.Fatalf("premise: the flip must add bytes on this fixture to exercise the ordering invariant -- pre=%d post=%d (the fixture no longer distinguishes the two documents)", preFlipMeasurement.Bytes, postFlipMeasurement.Bytes)
	}

	// A ceiling that FITS the pre-flip document and does NOT fit the
	// post-flip one: correct code (flip before the budget check) must
	// refuse; code that measured the pre-flip document would wrongly
	// certify a fit and go on to serve the larger, unmeasured document.
	//
	// Set at the SERVICE (engine) level, not on the request: the wire
	// Options.MaxSerializedBytes bound is [8192, 1MiB] (validate_request.go)
	// and this fixture's documents are far smaller than that floor.
	ceiling := preFlipMeasurement.Bytes

	engine, err := NewEngine(EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			t.Fatal("interpreter should not be reached")
			return InterpretedQuestion{}, nil
		}),
		Facts: failingFactReader{t: t},
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("synthesizer should not be reached")
			return InvestigationResult{}, nil
		}),
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	}, EngineOptions{
		ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID:                        func() string { return "result_fresh_00001" },
		ServerCompletenessAuthorityEnabled: true,
		MaxSerializedBytes:                 ceiling,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	_, err = engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err == nil {
		t.Fatalf("Investigate() error = nil, want a budget refusal: the flip pushes the served document (%d bytes) over the ceiling (%d bytes) that only the pre-flip document (%d bytes) fit under", postFlipMeasurement.Bytes, ceiling, preFlipMeasurement.Bytes)
	}
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Investigate() error = %v (%T), want an AnswerBudgetRefusal", err, err)
	}
	if refusal.Overrun != contractsv1.ContextFabricBudgetOverrunBytes {
		t.Fatalf("refusal.Overrun = %q, want the bytes axis", refusal.Overrun)
	}
	// Not an exact byte match against the locally-computed postFlipMeasurement:
	// the actually served document also carries Reused=true (this is a
	// reuse hit) and a re-stamped plan/coverage, which this fixture's
	// standalone flip does not reproduce byte-for-byte. What this proves is
	// the invariant under test: the refusal's own measurement is STRICTLY
	// ABOVE the ceiling (never <= the pre-flip size that "fits" under it) --
	// a refusal computed from the pre-flip document could not exceed a
	// ceiling the pre-flip document itself fits under.
	if refusal.MeasuredBytes <= ceiling {
		t.Fatalf("refusal.MeasuredBytes = %d, want > ceiling %d (the pre-flip size) -- a refusal at or under the ceiling would mean the budget measured the pre-flip document, not the served one", refusal.MeasuredBytes, ceiling)
	}
}

// TestCompletenessAuthority_NilTelemetryDoesNotPanic recovers any panic
// itself and reports it as a named failure, so a regression that makes
// finalizeServed call a nil telemetry sink dies as ONE clean test failure
// instead of a process crash that takes the rest of the package's run
// down with it. An engine composed with no telemetry sink at all is a
// legitimate, documented configuration (EngineDependencies.Telemetry is
// optional), and this is the reuse path -- the same route
// TestCompletenessAuthority_ReuseHitIsMeasured already exercises, but that
// test has no telemetry double AND no recover, so a panic there would
// already take out the rest of the suite rather than reporting cleanly.
func TestCompletenessAuthority_NilTelemetryDoesNotPanic(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("finalizeServed panicked with a nil telemetry sink: %v", r)
		}
	}()

	project, candidate := degradedOutcomeCandidate()
	engine := completenessAuthorityTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
		// Telemetry deliberately left nil.
	}, true, false)

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationDegraded {
		t.Fatalf("result.Status = %q, want degraded -- the flip must still work with no telemetry sink configured", result.Status)
	}
}
