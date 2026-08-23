package hosted_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestTwoTurnPairInvalidIsInstrumentFailure pins CHAOS-4138's own eligibility
// test exhaustively: every PairInvalid reason runTwoTurnInferredTierArm can
// actually produce, plus the PRODUCT-bar negative controls (a row that never
// even sets PairInvalid) the ticket explicitly calls out as never-retry.
func TestTwoTurnPairInvalidIsInstrumentFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		res  twoTurnCaseResult
		want bool
	}{
		{
			name: "baseline leg Investigate() error -- the exact shape case 21/57 hit",
			res:  twoTurnCaseResult{PairInvalid: true, ArmInvalidReason: "baseline investigate error: synthesis_rejected"},
			want: true,
		},
		{
			name: "hinted leg Investigate() error, non-window",
			res:  twoTurnCaseResult{PairInvalid: true, ArmInvalidReason: "investigate error: dependency_unavailable"},
			want: true,
		},
		{
			name: "pairing-precondition failure (Reused/VersionSet/window-bounds) -- ArmInvalidReason stays empty",
			res:  twoTurnCaseResult{PairInvalid: true, ArmInvalidReason: ""},
			want: false,
		},
		{
			name: "missing window-oracle-entry structural gap",
			res:  twoTurnCaseResult{PairInvalid: true, ArmInvalidReason: "no confirmed-window precondition available for this case (no window oracle entry)"},
			want: false,
		},
		{
			name: "window-precondition SETUP call error -- a third kind of call, not the baseline or hinted leg",
			res:  twoTurnCaseResult{PairInvalid: true, ArmInvalidReason: "window precondition setup failed: synthesis_rejected"},
			want: false,
		},
		{
			name: "window-precondition SETUP offer-miss -- an engine-refusal finding, not an Investigate() error at all",
			res:  twoTurnCaseResult{PairInvalid: true, ArmInvalidReason: "window precondition setup turn did not offer the case's own window back as a receipt-bound offer (an engine-refusal finding, not this harness's own defect)"},
			want: false,
		},
		{
			name: "reason PREFIX must not falsely match a differently-shaped 'investigate error' string from another arm",
			res:  twoTurnCaseResult{PairInvalid: true, ArmInvalidReason: "investigate error (inconclusive, not counted as a trip): synthesis_rejected"},
			want: false,
		},
		{
			name: "PRODUCT bar: wrong_commit on a completed pair -- PairInvalid was never set",
			res:  twoTurnCaseResult{PairInvalid: false, WrongCommit: true, ArmInvalidReason: ""},
			want: false,
		},
		{
			name: "PRODUCT bar: false_no_match on a completed pair -- PairInvalid was never set",
			res:  twoTurnCaseResult{PairInvalid: false, FalseNoMatch: true, ArmInvalidReason: ""},
			want: false,
		},
		{
			name: "PRODUCT bar: inferred_tier classification reached unjustified on a completed pair -- PairInvalid was never set",
			res:  twoTurnCaseResult{PairInvalid: false, InferredClassification: "unjustified", ArmInvalidReason: ""},
			want: false,
		},
		{
			name: "PRODUCT bar with a reason string that WOULD match if PairInvalid were checked second, not first",
			res:  twoTurnCaseResult{PairInvalid: false, WrongCommit: true, ArmInvalidReason: "baseline investigate error: synthesis_rejected"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := twoTurnPairInvalidIsInstrumentFailure(tc.res); got != tc.want {
				t.Errorf("twoTurnPairInvalidIsInstrumentFailure(%+v) = %v, want %v", tc.res, got, tc.want)
			}
		})
	}
}

// chaos4138FakeInvestigator is a request-ID-keyed contextfabric.Investigator
// stub. Unlike fakeInvestigator (generative_trial_structure_needs_test.go's
// own single canned pair), runTwoTurnInferredTierArm/its retry wrapper make
// up to four DISTINCT Investigate() calls per attempt (two window-
// precondition setups, the baseline leg, the hinted leg) -- the retry
// wrapper's own bounded-retry contract can only be proven by controlling
// each call's outcome independently by role AND attempt. Keyed on
// req.RequestID's own suffix (twoTurnRequest's construction, mirrored here)
// rather than call order, so a test reads as "what does THIS call return"
// rather than "the Nth call returns X".
type chaos4138FakeInvestigator struct {
	windowSetup func(requestID string) (contractsv1.ContextFabricInvestigationResult, error)
	baseline    func(requestID string) (contractsv1.ContextFabricInvestigationResult, error)
	hinted      func(requestID string) (contractsv1.ContextFabricInvestigationResult, error)
	calls       []string
}

func (f *chaos4138FakeInvestigator) Investigate(_ context.Context, _ storage.Principal, req contractsv1.ContextFabricInvestigationRequest) (contractsv1.ContextFabricInvestigationResult, error) {
	f.calls = append(f.calls, req.RequestID)
	switch {
	case strings.Contains(req.RequestID, "windowsetup"):
		return f.windowSetup(req.RequestID)
	case strings.HasSuffix(req.RequestID, "baseline"):
		return f.baseline(req.RequestID)
	default:
		return f.hinted(req.RequestID)
	}
}

// chaos4138WindowSetupResult builds the smallest window-precondition setup
// response mintWindowPrecondition's own selectOracleOffer call needs to
// succeed: one WindowClarification.Option whose RelativeID matches the
// case's own window band, carrying a receipt ID. Question/labels are
// synthetic placeholder text, never real corpus content (this repo's own
// PII-withholding discipline for hand-built test fixtures, the same
// convention minimalValidStalledResult already documents).
//
// CHAOS-4121: StructureNeeds.WindowOptions is set to the SAME slice value
// as WindowClarification.Options -- mirroring window.go:1315's real
// production assignment exactly (never a copy) -- because selectOracleOffer
// now reads StructureNeeds.WindowOptions for the window member, and
// twoTurnAssertWindowSurfacesAgree Fatal's this fixture's own caller if the
// two fields ever disagree.
func chaos4138WindowSetupResult(resultID, windowBand string) contractsv1.ContextFabricInvestigationResult {
	options := []contractsv1.ContextFabricWindowOption{
		{ReceiptID: "winr_" + resultID, OptionID: "opt1", Label: "synthetic test label, not corpus content", RelativeID: contractsv1.ContextFabricRelativeWindowID(windowBand)},
	}
	return contractsv1.ContextFabricInvestigationResult{
		ResultID: resultID,
		Status:   contractsv1.ContextFabricInvestigationClarificationRequired,
		WindowClarification: &contractsv1.ContextFabricWindowClarification{
			Options: options,
		},
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing:       []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
			WindowOptions: options,
		},
	}
}

// chaos4138ClarificationResult builds a minimal, non-decisive, non-erroring
// leg response: no subject committed, so WrongCommit/FalseNoMatch cannot
// fire. Every test below passes a fresh, empty *twoTurnTraceCapture (never
// nil -- runTwoTurnInferredTierArm's own !isWindow branch calls
// trace.finalDecisionEvent() unconditionally, a pre-existing precondition
// this ticket does not change); with zero events captured,
// finalDecisionEvent's own doc comment ("zero value, ok=false if none")
// means hintedDecision/baselineDecision.Outcome stays "", so the decisive
// Outcome=="committed" branch never runs either. That keeps a "the retry
// succeeded" fixture down to exactly what this function reads: Status and
// an empty commit set.
func chaos4138ClarificationResult(resultID string) contractsv1.ContextFabricInvestigationResult {
	return contractsv1.ContextFabricInvestigationResult{
		ResultID: resultID,
		Status:   contractsv1.ContextFabricInvestigationClarificationRequired,
	}
}

func chaos4138Entry() twoTurnOracleEntry {
	return twoTurnOracleEntry{Member: string(contractsv1.ContextFabricStructureNeedExpectedKind), NegativeKind: string(contractsv1.ContextFabricSubjectRepository)}
}

func chaos4138Case() trialCase {
	return trialCase{Question: "synthetic test question, not corpus content", ExpectKind: "repository", ExpectID: "repository:r1"}
}

// TestRunTwoTurnInferredTierArmWithPairRetry_RecoversFromBaselineInstrumentFailure
// mutation-verifies the RETRY-FIRES direction: a first-attempt baseline-leg
// Investigate() error (ErrSynthesisRejected, the exact case-21/57 shape)
// is an instrument failure, so the wrapper retries once, the retry's own
// baseline/hinted calls both succeed, and the returned row shows the
// recovery plus the first attempt's own error preserved.
func TestRunTwoTurnInferredTierArmWithPairRetry_RecoversFromBaselineInstrumentFailure(t *testing.T) {
	t.Parallel()
	fake := &chaos4138FakeInvestigator{
		windowSetup: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			return chaos4138WindowSetupResult(requestID, "trailing_90d"), nil
		},
		baseline: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			if strings.Contains(requestID, "retry") {
				return chaos4138ClarificationResult(requestID), nil
			}
			return contractsv1.ContextFabricInvestigationResult{}, &contextfabric.StageError{Stage: contextfabric.StageSynthesis, Err: contextfabric.ErrSynthesisRejected}
		},
		hinted: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			return chaos4138ClarificationResult(requestID), nil
		},
	}
	res := runTwoTurnInferredTierArmWithPairRetry(t, context.Background(), fake, storage.Principal{}, 21, chaos4138Case(), chaos4138Entry(), 30*time.Second, &twoTurnTraceCapture{}, "trailing_90d")

	if !res.PairRetried {
		t.Error("PairRetried = false, want true -- the first attempt's baseline leg errored with ErrSynthesisRejected, an instrument failure")
	}
	if res.PairInvalid {
		t.Errorf("PairInvalid = true, want false -- the retry's own baseline and hinted calls both succeeded")
	}
	if res.ArmInvalidReason != "" {
		t.Errorf("ArmInvalidReason = %q, want empty -- this row is the RETRY's own outcome, which succeeded", res.ArmInvalidReason)
	}
	if res.ArmInvalidStage != "" {
		t.Errorf("ArmInvalidStage = %q, want empty -- the retry succeeded, so this row's own stage field must be empty", res.ArmInvalidStage)
	}
	if res.ArmInvalidErrorType != "" {
		t.Errorf("ArmInvalidErrorType = %q, want empty -- the retry succeeded", res.ArmInvalidErrorType)
	}
	if want := "baseline investigate error: synthesis_rejected"; res.PairRetryFirstArmInvalidReason != want {
		t.Errorf("PairRetryFirstArmInvalidReason = %q, want %q -- the first attempt's own error must survive on the row", res.PairRetryFirstArmInvalidReason, want)
	}
	if want := string(contextfabric.StageSynthesis); res.PairRetryFirstArmInvalidStage != want {
		t.Errorf("PairRetryFirstArmInvalidStage = %q, want %q -- the first attempt's own stage must survive on the row", res.PairRetryFirstArmInvalidStage, want)
	}
	if want := "*errors.errorString"; res.PairRetryFirstArmInvalidErrorType != want {
		t.Errorf("PairRetryFirstArmInvalidErrorType = %q, want %q -- the first attempt's own error type must survive on the row", res.PairRetryFirstArmInvalidErrorType, want)
	}

	// Every RequestID the fake actually saw must be unique -- the whole
	// point of threading requestIDPrefix through is that a retry's four
	// calls never collide with the first attempt's own four.
	seen := map[string]bool{}
	for _, id := range fake.calls {
		if seen[id] {
			t.Fatalf("duplicate RequestID %q across attempts -- retry must use a distinct prefix", id)
		}
		seen[id] = true
	}
	// First attempt: 2 window setups + baseline (errors, returns before the
	// hinted call). Retry: 2 window setups + baseline + hinted (all
	// succeed). 7 calls total, never a third attempt.
	if len(fake.calls) != 7 {
		t.Errorf("investigator called %d times, want exactly 7 (2 window setups + baseline on attempt 1; 2 window setups + baseline + hinted on the retry) -- bounded to exactly one retry", len(fake.calls))
	}
}

// TestRunTwoTurnInferredTierArmWithPairRetry_BoundedToExactlyOneRetry
// mutation-verifies the BOUNDED-not-a-loop direction: when the retry's own
// baseline leg ALSO errors, the wrapper must report that failure honestly
// (never paper over a call that fails twice) and must NOT attempt a third
// call.
func TestRunTwoTurnInferredTierArmWithPairRetry_BoundedToExactlyOneRetry(t *testing.T) {
	t.Parallel()
	firstErr := &contextfabric.StageError{Stage: contextfabric.StageSynthesis, Err: contextfabric.ErrSynthesisRejected}
	retryErr := &contextfabric.StageError{Stage: contextfabric.StageGraph, Err: contextfabric.ErrUnavailable}
	fake := &chaos4138FakeInvestigator{
		windowSetup: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			return chaos4138WindowSetupResult(requestID, "trailing_90d"), nil
		},
		baseline: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			if strings.Contains(requestID, "retry") {
				return contractsv1.ContextFabricInvestigationResult{}, retryErr
			}
			return contractsv1.ContextFabricInvestigationResult{}, firstErr
		},
		hinted: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			t.Fatal("hinted leg must never be called -- both attempts' baseline legs errored, which returns before the hinted call")
			return contractsv1.ContextFabricInvestigationResult{}, nil
		},
	}
	res := runTwoTurnInferredTierArmWithPairRetry(t, context.Background(), fake, storage.Principal{}, 99, chaos4138Case(), chaos4138Entry(), 30*time.Second, &twoTurnTraceCapture{}, "trailing_90d")

	if !res.PairRetried {
		t.Error("PairRetried = false, want true -- the retry DID run, it just also failed")
	}
	if !res.PairInvalid {
		t.Error("PairInvalid = true, want... true -- a call that fails twice is a real, reportable instrument failure, never silently absorbed")
	}
	if want := "baseline investigate error: dependency_unavailable"; res.ArmInvalidReason != want {
		t.Errorf("ArmInvalidReason = %q, want %q -- this row is the RETRY's own (second) failure", res.ArmInvalidReason, want)
	}
	if want := string(contextfabric.StageGraph); res.ArmInvalidStage != want {
		t.Errorf("ArmInvalidStage = %q, want %q -- this row's own stage must be the RETRY's own (second) failure's stage", res.ArmInvalidStage, want)
	}
	if want := "*errors.errorString"; res.ArmInvalidErrorType != want {
		t.Errorf("ArmInvalidErrorType = %q, want %q", res.ArmInvalidErrorType, want)
	}
	if want := "baseline investigate error: synthesis_rejected"; res.PairRetryFirstArmInvalidReason != want {
		t.Errorf("PairRetryFirstArmInvalidReason = %q, want %q -- the FIRST attempt's own error must still be preserved", res.PairRetryFirstArmInvalidReason, want)
	}
	if want := string(contextfabric.StageSynthesis); res.PairRetryFirstArmInvalidStage != want {
		t.Errorf("PairRetryFirstArmInvalidStage = %q, want %q -- the FIRST attempt's own stage must still be preserved, DISTINCT from the retry's own %q", res.PairRetryFirstArmInvalidStage, want, res.ArmInvalidStage)
	}
	// 2 window setups + baseline (errors) per attempt, 2 attempts, never a
	// third: exactly 6 calls, the hinted leg never reached by either
	// attempt.
	if len(fake.calls) != 6 {
		t.Errorf("investigator called %d times, want exactly 6 (2 window setups + baseline, twice) -- bounded to exactly one retry, never a loop", len(fake.calls))
	}
}

// TestRunTwoTurnInferredTierArmWithPairRetry_NeverRetriesWhenFirstAttemptSucceeds
// negative control: a first attempt with no PairInvalid at all (every call
// succeeds) must return that single row unchanged, with zero retry calls --
// proves the wrapper does not retry a row that never needed one.
func TestRunTwoTurnInferredTierArmWithPairRetry_NeverRetriesWhenFirstAttemptSucceeds(t *testing.T) {
	t.Parallel()
	fake := &chaos4138FakeInvestigator{
		windowSetup: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			return chaos4138WindowSetupResult(requestID, "trailing_90d"), nil
		},
		baseline: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			return chaos4138ClarificationResult(requestID), nil
		},
		hinted: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			return chaos4138ClarificationResult(requestID), nil
		},
	}
	res := runTwoTurnInferredTierArmWithPairRetry(t, context.Background(), fake, storage.Principal{}, 1, chaos4138Case(), chaos4138Entry(), 30*time.Second, &twoTurnTraceCapture{}, "trailing_90d")

	if res.PairRetried {
		t.Error("PairRetried = true, want false -- the first attempt never failed at all")
	}
	if res.PairInvalid {
		t.Error("PairInvalid = true, want false")
	}
	if res.PairRetryFirstArmInvalidReason != "" {
		t.Errorf("PairRetryFirstArmInvalidReason = %q, want empty -- no retry ran", res.PairRetryFirstArmInvalidReason)
	}
	if len(fake.calls) != 4 {
		t.Errorf("investigator called %d times, want exactly 4 (2 window setups + baseline + hinted) -- a clean first attempt must never trigger a retry", len(fake.calls))
	}
	for _, id := range fake.calls {
		if strings.Contains(id, "retry") {
			t.Errorf("call %q used the retry request-ID prefix, want none -- no retry should have run", id)
		}
	}
}

// TestRunTwoTurnInferredTierArmWithPairRetry_NeverRetriesPairingPreconditionFailure
// negative control (the ticket's own explicit exclusion): a window-
// precondition SETUP call error is NOT the baseline or hinted leg -- it
// must not retry, matching twoTurnPairInvalidIsInstrumentFailure's own
// unit-tested exclusion for that exact reason shape.
func TestRunTwoTurnInferredTierArmWithPairRetry_NeverRetriesWindowPreconditionSetupFailure(t *testing.T) {
	t.Parallel()
	fake := &chaos4138FakeInvestigator{
		windowSetup: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			return contractsv1.ContextFabricInvestigationResult{}, contextfabric.ErrSynthesisRejected
		},
		baseline: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			t.Fatal("baseline leg must never be called -- the window-precondition setup already failed and returns before it")
			return contractsv1.ContextFabricInvestigationResult{}, nil
		},
		hinted: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			t.Fatal("hinted leg must never be called")
			return contractsv1.ContextFabricInvestigationResult{}, nil
		},
	}
	res := runTwoTurnInferredTierArmWithPairRetry(t, context.Background(), fake, storage.Principal{}, 5, chaos4138Case(), chaos4138Entry(), 30*time.Second, &twoTurnTraceCapture{}, "trailing_90d")

	if !res.PairInvalid {
		t.Error("PairInvalid = false, want true -- the window-precondition setup call errored")
	}
	if res.PairRetried {
		t.Error("PairRetried = true, want false -- a window-precondition SETUP failure is not a baseline/hinted-leg Investigate() error, and must not retry")
	}
	if len(fake.calls) != 1 {
		t.Errorf("investigator called %d times, want exactly 1 (the failing window setup) -- no retry, no second window setup", len(fake.calls))
	}
}

// TestRunTwoTurnInferredTierArmWithPairRetry_RetriesOnHintedLegInstrumentFailure
// mutation-verifies the SAME retry-fires direction as
// TestRunTwoTurnInferredTierArmWithPairRetry_RecoversFromBaselineInstrumentFailure,
// but for the OTHER eligible reason prefix: the hinted leg's own
// Investigate() error (twoTurnPairInvalidIsInstrumentFailure's second
// accepted prefix, "investigate error: ", distinct from "baseline
// investigate error: "). Proves the wrapper's eligibility check is not
// baseline-leg-only by construction.
func TestRunTwoTurnInferredTierArmWithPairRetry_RetriesOnHintedLegInstrumentFailure(t *testing.T) {
	t.Parallel()
	fake := &chaos4138FakeInvestigator{
		windowSetup: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			return chaos4138WindowSetupResult(requestID, "trailing_90d"), nil
		},
		baseline: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			return chaos4138ClarificationResult(requestID), nil
		},
		hinted: func(requestID string) (contractsv1.ContextFabricInvestigationResult, error) {
			if strings.Contains(requestID, "retry") {
				return chaos4138ClarificationResult(requestID), nil
			}
			return contractsv1.ContextFabricInvestigationResult{}, &contextfabric.StageError{Stage: contextfabric.StageSynthesis, Err: contextfabric.ErrSynthesisRejected}
		},
	}
	res := runTwoTurnInferredTierArmWithPairRetry(t, context.Background(), fake, storage.Principal{}, 42, chaos4138Case(), chaos4138Entry(), 30*time.Second, &twoTurnTraceCapture{}, "trailing_90d")

	if !res.PairRetried {
		t.Error("PairRetried = false, want true -- the first attempt's hinted leg errored with ErrSynthesisRejected, an instrument failure")
	}
	if res.PairInvalid {
		t.Error("PairInvalid = true, want false -- the retry's own baseline and hinted calls both succeeded")
	}
	if want := "investigate error: synthesis_rejected"; res.PairRetryFirstArmInvalidReason != want {
		t.Errorf("PairRetryFirstArmInvalidReason = %q, want %q -- the first attempt's own HINTED-leg error must survive on the row", res.PairRetryFirstArmInvalidReason, want)
	}
	// First attempt: 2 window setups + baseline (succeeds) + hinted
	// (errors) = 4 calls. Retry: 2 window setups + baseline + hinted (all
	// succeed) = 4 calls. 8 total, never a third attempt.
	if len(fake.calls) != 8 {
		t.Errorf("investigator called %d times, want exactly 8 (4 calls per attempt, 2 attempts) -- bounded to exactly one retry", len(fake.calls))
	}
	seen := map[string]bool{}
	for _, id := range fake.calls {
		if seen[id] {
			t.Fatalf("duplicate RequestID %q across attempts -- retry must use a distinct prefix", id)
		}
		seen[id] = true
	}
}
