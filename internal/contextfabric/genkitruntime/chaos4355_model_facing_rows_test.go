package genkitruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestDefaultSynthesisPromptVersionBumpedForModelFacingFactsChange is codex
// R1's P2 finding: modelFacingFacts changes the actual bytes
// synthesisInputFromDomain sends the model (measured in
// TestBuildSynthesisPromptExcludesRowsShapedCanonicalFields below), so
// DefaultSynthesisPromptVersion -- which participates directly in
// contextfabric.ReuseKey.SynthesisPromptVersion -- must move off its
// pre-fix value, or an org with answer reuse enabled could keep serving a
// row generated when the model could still see Rows-shaped fields as
// though it were equivalent to one generated under the new, filtered
// payload. This is the literal-constant half of the regression guard;
// TestCHAOS4355_SynthesisPromptVersionBumpInvalidatesPreFixStoredAnswers
// (internal/contextfabric package) is the reuse-key MECHANISM half.
//
// codex R2 P2: asserting only "!= v12" stays green for ANY other value,
// including a typo or an accidental double-bump that would ALSO defeat
// the sibling mechanism test's hardcoded "v13" expectation without this
// test ever catching it. Assert the exact current value instead.
func TestDefaultSynthesisPromptVersionBumpedForModelFacingFactsChange(t *testing.T) {
	t.Parallel()
	const wantVersion = "context-fabric-synthesis.v13"
	if DefaultSynthesisPromptVersion != wantVersion {
		t.Fatalf("DefaultSynthesisPromptVersion = %q, want %q (moved off the pre-CHAOS-4355-follow-up v12 value now that modelFacingFacts changes the prompt payload)", DefaultSynthesisPromptVersion, wantVersion)
	}
}

// --- CHAOS-4355 follow-up: canonical facts must not present a Rows-shaped
// field to the model in the first place (fix (a)), and Runtime.SynthesizeAnswer
// -- the actual production ValidateAgainst call site, and the live source of
// the kiac pilot rev 19 3/3 422s -- must tolerate a model that authors Rows
// anyway rather than reject the whole answer (fix (b)). ---

// synthesisInputWithRowsShapedFact is validSynthesisInput() with a SECOND
// Rows-shaped field ("team_rows") added to the same canonical fact,
// mirroring the CHAOS-4364 flow/landscape producers' scalar-rollup +
// breakdown-table shape.
func synthesisInputWithRowsShapedFact() contextfabric.SynthesisInput {
	input := validSynthesisInput()
	input.Facts.Facts[0].Fields["team_rows"] = contextfabric.RowsFactValue([]contextfabric.FactValueRow{
		{Fields: map[string]contextfabric.FactValue{
			"team_id":       contextfabric.StringFactValue("team_a"),
			"commits_count": contextfabric.IntegerFactValue(12),
		}},
	})
	return input
}

// TestModelFacingFactsDropsRowsShapedFieldsButKeepsScalars is
// modelFacingFacts' own direct unit test (fix (a)'s core): a Rows-shaped
// field must be excluded entirely (not merely emptied), while every scalar
// field on the same fact, and the fact's own identity, survive untouched.
func TestModelFacingFactsDropsRowsShapedFieldsButKeepsScalars(t *testing.T) {
	t.Parallel()
	input := synthesisInputWithRowsShapedFact()
	got := modelFacingFacts(input.Facts.Facts)
	if len(got) != 1 {
		t.Fatalf("modelFacingFacts() = %v, want exactly 1 fact", got)
	}
	if _, present := got[0].Fields["team_rows"]; present {
		t.Fatalf("modelFacingFacts()[0].Fields has team_rows, want it excluded entirely")
	}
	scalar, present := got[0].Fields["release_ready"]
	if !present || scalar.Boolean == nil || *scalar.Boolean != false {
		t.Fatalf("modelFacingFacts()[0].Fields[release_ready] = %+v, want the scalar field untouched", scalar)
	}
	if got[0].Kind != input.Facts.Facts[0].Kind || got[0].Subject != input.Facts.Facts[0].Subject {
		t.Fatalf("modelFacingFacts() changed fact identity: got kind=%v subject=%v, want kind=%v subject=%v", got[0].Kind, got[0].Subject, input.Facts.Facts[0].Kind, input.Facts.Facts[0].Subject)
	}
	// The original input must be untouched -- ValidateAgainst and
	// attachCanonicalRows both need the ORIGINAL canonical facts,
	// including Rows, after the model call returns.
	if _, present := input.Facts.Facts[0].Fields["team_rows"]; !present {
		t.Fatalf("modelFacingFacts() mutated the input's own Fields map -- team_rows must survive on the original")
	}
}

// TestBuildSynthesisPromptExcludesRowsShapedCanonicalFields is the
// RED-FIRST proof for fix (a) at the actual prompt-serialization boundary
// (the same synthesisInputFromDomain BuildSynthesisPrompt and the real
// Synthesize call both use): before modelFacingFacts existed, the model's
// own prompt handed it the "team_rows" table verbatim in canonical_facts --
// exactly the shape a model would then be tempted to echo back into
// ClaimedFacts.Rows, which ValidateAgainst unconditionally rejects. It also
// measures the prompt-bytes-changed number the PR must report.
func TestBuildSynthesisPromptExcludesRowsShapedCanonicalFields(t *testing.T) {
	t.Parallel()
	input := synthesisInputWithRowsShapedFact()

	after, err := BuildSynthesisPrompt(input, 512<<10)
	if err != nil {
		t.Fatalf("BuildSynthesisPrompt() error = %v", err)
	}
	if strings.Contains(after, "team_rows") || strings.Contains(after, "team_a") {
		t.Fatalf("BuildSynthesisPrompt() output still contains the Rows-shaped field/content: %s", after)
	}
	if !strings.Contains(after, "release_ready") {
		t.Fatalf("BuildSynthesisPrompt() output lost the scalar field release_ready, want it preserved")
	}

	// Measure the prompt-bytes-changed number (cf-standing-rules: any PR
	// that shrinks/grows the model-facing shape reports before/after
	// bytes) by building the SAME payload the pre-fix code would have sent
	// -- input.Facts.Facts unstripped -- through the identical encoder.
	before := synthesisInput{
		Question: input.Request.Question, Interpretation: input.Interpretation,
		Resolution: input.Graph.Resolution, Cohort: input.Graph.Cohort,
		Paths: input.Graph.Paths, DriverCandidates: input.Graph.DriverCandidates,
		Facts: input.Facts.Facts, Coverage: contextfabric.MergeCoverage("", input.Graph.Coverage, input.Facts.Coverage),
	}
	beforeBytes, err := json.Marshal(before)
	if err != nil {
		t.Fatalf("json.Marshal(before) error = %v", err)
	}
	t.Logf("synthesis prompt bytes: before=%d after=%d delta=%d", len(beforeBytes), len(after), len(beforeBytes)-len(after))
	if len(after) >= len(beforeBytes) {
		t.Fatalf("BuildSynthesisPrompt() after-bytes = %d, want fewer than before-bytes = %d (the Rows-shaped field must shrink the prompt)", len(after), len(beforeBytes))
	}
}

// rowsAuthoredSynthesisOutput is validSynthesisOutput() with its driver
// switched to the "readiness" category (which requires a claimed fact) and
// ONE claim restating validSynthesisInput()'s own release_ready=false
// canonical fact correctly -- except the model ALSO attaches a fabricated
// Rows array, the exact shape TestRuntimeRejectsSynthesisThatInventsEvidence's
// sibling tests prove ValidateAgainst rejects unconditionally
// (model_runtime_test.go's TestSynthesisDraftValidateAgainstRejectsClaimedFactSettingRows).
func rowsAuthoredSynthesisOutput() synthesisOutput {
	output := validSynthesisOutput()
	output.Drivers[0].Category = "readiness"
	output.Drivers[0].ClaimedFactIDs = []string{"claim_readiness_1"}
	fabricated := "fabricated"
	notReady := false
	output.ClaimedFacts = []contextfabric.ClaimedFact{{
		ClaimID: "claim_readiness_1", Kind: contextfabric.FactReadiness,
		Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"},
		Field:   "release_ready", Value: contextfabric.ScalarValue{Boolean: &notReady},
		Rows: []contextfabric.ClaimedFactRow{{Fields: map[string]contextfabric.ScalarValue{"anything": {String: &fabricated}}}},
	}}
	return output
}

// TestRuntimeSynthesizeAnswerToleratesModelAuthoredRowsInsteadOfRejecting is
// the RED-FIRST proof at the ACTUAL production rejection site
// (Runtime.SynthesizeAnswer's own draft.ValidateAgainst(input) call --
// TestRuntimeClassifiesSynthesisBoundViolationWithoutFallback's sibling
// proves this IS where CHAOS-3784-class rejections classify from): before
// the tolerance existed, this exact draft made SynthesizeAnswer return
// ErrSynthesisRejected wrapping a *ModelBoundViolation naming the
// Rows-authorship bound -- the kiac pilot rev 19 live 3/3 422. After the
// fix, the call must succeed with the fabricated Rows stripped before
// ValidateAgainst ever ran.
func TestRuntimeSynthesizeAnswerToleratesModelAuthoredRowsInsteadOfRejecting(t *testing.T) {
	t.Parallel()
	telemetry := &fakeRowsStrippedTelemetry{}
	runtime := mustRuntime(t, &generatorStub{synthesis: rowsAuthoredSynthesisOutput()}, Config{Telemetry: telemetry})
	draft, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want the model-authored Rows tolerated (stripped), not the whole answer rejected", err)
	}
	if receipt.Outcome != "success" {
		t.Fatalf("receipt.Outcome = %q, want success", receipt.Outcome)
	}
	if len(draft.ClaimedFacts) != 1 || draft.ClaimedFacts[0].Rows != nil {
		t.Fatalf("draft.ClaimedFacts = %+v, want exactly 1 claim with Rows stripped to nil", draft.ClaimedFacts)
	}
	if len(telemetry.claims) != 1 || telemetry.claims[0] != 1 {
		t.Fatalf("telemetry.claims = %v, want exactly one record of 1 (one claim stripped)", telemetry.claims)
	}
}

// fakeRowsStrippedTelemetry implements just enough of
// contextfabric.EngineTelemetry for Config.Telemetry -- genkitruntime never
// needs the full interface's other methods for this call path, but Go
// requires every method to satisfy the interface, so this embeds a nil
// contextfabric.EngineTelemetry and overrides only RecordModelRowsStripped;
// any OTHER method call would nil-panic, which is itself a useful
// assertion that Runtime.SynthesizeAnswer never calls anything else on it.
type fakeRowsStrippedTelemetry struct {
	contextfabric.EngineTelemetry
	claims []int
}

func (f *fakeRowsStrippedTelemetry) RecordModelRowsStripped(_ context.Context, _ storage.Principal, claims int) {
	f.claims = append(f.claims, claims)
}
