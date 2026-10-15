package contextfabric

import (
	"context"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const k7TableField = "compounding_risk_drivers"

// k7Input is a synthesis input about one team whose health fact carries the declared score compounding_risk and, when
// withTable, its drivers table.
func k7Input(withTable bool) (SynthesisInput, SubjectRef) {
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "Team A"}
	input := validSynthesisInputFixture()
	input.Graph.Resolution.Committed = []SubjectRef{team}
	risk, severity := 0.993773746751932, "high"
	fields := map[string]FactValue{"compounding_risk": {Number: &risk}, "severity": {String: &severity}}
	if withTable {
		fields[k7TableField] = FactValue{}
	}
	input.Facts.Facts = []CanonicalFact{{
		Kind: FactHealth, Subject: team, Fields: fields,
		EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: SourceAvailable,
		Source: "devhealthfacts.health", SourceVersion: "v1",
	}}
	return input, team
}

func k7Draft(input SynthesisInput, team SubjectRef, field string) SynthesisDraft {
	draft := validSynthesisDraftFixture(input)
	draft.Drivers[0].AffectedSubjects = []SubjectRef{team}
	value := ScalarValue{}
	for name, fieldValue := range input.Facts.Facts[0].Fields {
		if name == field {
			if fieldValue.Number != nil {
				value = ScalarValue{Number: fieldValue.Number}
			} else {
				value = ScalarValue{String: fieldValue.String}
			}
		}
	}
	draft.ClaimedFacts = []ClaimedFact{{ClaimID: "claim_risk_1", Kind: FactHealth, Subject: team, Field: field, Value: value}}
	return draft
}

// recordingModelRuntime keeps the synthesis input the model was given.
type recordingModelRuntime struct {
	fakeModelRuntime
	seen []SynthesisInput
}

func (r *recordingModelRuntime) SynthesizeAnswer(ctx context.Context, principal storage.Principal, input SynthesisInput) (SynthesisDraft, ModelExecutionReceipt, error) {
	r.seen = append(r.seen, input)
	return r.fakeModelRuntime.SynthesizeAnswer(ctx, principal, input)
}

func claimedFields(result InvestigationResult) []string {
	fields := []string{}
	for _, claim := range result.ClaimedFacts {
		fields = append(fields, claim.Field)
	}
	return fields
}

func scoreWithheldDetails(result InvestigationResult) []CoverageDetail {
	details := []CoverageDetail{}
	for _, detail := range result.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailFactScoreWithheld {
			details = append(details, detail)
		}
	}
	return details
}

// Decision K7: never a bare score. A model-written answer never carries a declared score whose drivers table is not
// served: the model is not shown the score, and the answer says the score was withheld. On main the claim was admitted.
func TestAModelWrittenAnswerNeverClaimsABareScore(t *testing.T) {
	input, team := k7Input(false)
	runtime := &recordingModelRuntime{fakeModelRuntime: fakeModelRuntime{draft: k7Draft(input, team, "severity"), receipt: validModelReceiptFixture(ModelOperationSynthesize)}}
	synthesizer := RuntimeAnswerSynthesizer{Runtime: runtime, FactCapabilities: k7Capabilities()}

	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(runtime.seen) != 1 {
		t.Fatalf("model calls = %d, want 1", len(runtime.seen))
	}
	shown := runtime.seen[0].Facts.Facts[0].Fields
	if _, bare := shown["compounding_risk"]; bare {
		t.Fatalf("the model was shown the bare score: %v", shown)
	}
	if _, plain := shown["severity"]; !plain {
		t.Fatalf("the plain scalar was withheld too: %v", shown)
	}
	if _, kept := input.Facts.Facts[0].Fields["compounding_risk"]; !kept {
		t.Fatal("the caller's input was changed")
	}
	details := scoreWithheldDetails(result)
	if len(details) != 1 || details[0].FactKind != FactHealth || details[0].Degrading || details[0].Label != "Health scores withheld: their drivers were not served" {
		t.Fatalf("fact_score_withheld details %+v, want one for health, not degrading, with the approved label", details)
	}
}

func TestAModelWrittenAnswerKeepsAScoreWhoseDriversWereServed(t *testing.T) {
	input, team := k7Input(true)
	runtime := &recordingModelRuntime{fakeModelRuntime: fakeModelRuntime{draft: k7Draft(input, team, "compounding_risk"), receipt: validModelReceiptFixture(ModelOperationSynthesize)}}
	synthesizer := RuntimeAnswerSynthesizer{Runtime: runtime, FactCapabilities: k7Capabilities()}

	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if _, shown := runtime.seen[0].Facts.Facts[0].Fields["compounding_risk"]; !shown {
		t.Fatal("the score was withheld although its drivers table was served")
	}
	if got := strings.Join(claimedFields(result), ","); got != "compounding_risk" {
		t.Fatalf("claimed fields %q, want the score kept", got)
	}
	if details := scoreWithheldDetails(result); len(details) != 0 {
		t.Fatalf("fact_score_withheld %+v for an answer that withheld nothing", details)
	}
}

func TestASynthesizerWithoutDeclarationsWithholdsNothing(t *testing.T) {
	input, team := k7Input(false)
	runtime := &recordingModelRuntime{fakeModelRuntime: fakeModelRuntime{draft: k7Draft(input, team, "compounding_risk"), receipt: validModelReceiptFixture(ModelOperationSynthesize)}}

	result, err := RuntimeAnswerSynthesizer{Runtime: runtime}.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if err != nil || len(result.ClaimedFacts) != 1 || len(scoreWithheldDetails(result)) != 0 {
		t.Fatalf("err %v claims %d details %d, want the unchanged answer", err, len(result.ClaimedFacts), len(scoreWithheldDetails(result)))
	}
}

// A draft written elsewhere (a client's own) can still claim a score it was never shown; the claim is removed and the
// answer says so.
func TestAForgedClaimOfAWithheldScoreIsStrippedAndDisclosed(t *testing.T) {
	input, team := k7Input(false)
	forged := k7Draft(input, team, "compounding_risk")
	forged.Drivers[0].ClaimedFactIDs = []string{"claim_risk_1"}
	synthesizer := RuntimeAnswerSynthesizer{FactCapabilities: k7Capabilities()}
	given := synthesizer.withholdBareScores(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	draft, err := synthesizer.vetSynthesisDraft(context.Background(), storage.Principal{OrgID: "org_1"}, given, forged)

	if err != nil {
		t.Fatalf("vetSynthesisDraft() error = %v", err)
	}
	if len(draft.ClaimedFacts) != 0 || len(draft.Drivers[0].ClaimedFactIDs) != 0 {
		t.Fatalf("the forged claim survived: claims %v driver citations %v", draft.ClaimedFacts, draft.Drivers[0].ClaimedFactIDs)
	}
	result, err := synthesizer.composeSynthesisResult(context.Background(), storage.Principal{OrgID: "org_1"}, given, draft, validModelReceiptFixture(ModelOperationSynthesize), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if details := scoreWithheldDetails(result); len(details) != 1 {
		t.Fatalf("fact_score_withheld details %+v, want exactly one for health", details)
	}
}

func TestAClientSynthesisPayloadHasABareScoreOnlyWithItsDrivers(t *testing.T) {
	for _, withTable := range []bool{false, true} {
		input, _ := k7Input(withTable)
		synthesizer := RuntimeAnswerSynthesizer{
			FactCapabilities: k7Capabilities(),
			ClientSynthesis: &ClientSynthesisAssembly{
				PromptVersion: "p", ModelOutputVersion: "m", SystemSHA256: strings.Repeat("a", 64), Rules: []string{"rule one"}, MaxBytes: clientTestMaxBytes, Encode: clientTestEncode,
			},
		}

		result, bundle, err := synthesizer.ComposeForClientSynthesis(context.Background(), storage.Principal{OrgID: "org_1"}, input)

		if err != nil || bundle == nil {
			t.Fatalf("withTable=%v: err %v bundle nil %v", withTable, err, bundle == nil)
		}
		if got := strings.Contains(string(bundle.Input), "compounding_risk\""); got != withTable {
			t.Fatalf("withTable=%v: payload carries the score = %v", withTable, got)
		}
		if got := len(scoreWithheldDetails(result)) == 1; got == withTable {
			t.Fatalf("withTable=%v: fact_score_withheld disclosed = %v", withTable, got)
		}
	}
}
