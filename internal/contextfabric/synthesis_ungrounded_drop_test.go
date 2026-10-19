package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// partlyUngroundedDraft is one draft with a grounded claim and driver beside a
// claim that contradicts the stored fact, a driver that rests only on it, and a
// finding that rests only on it.
func partlyUngroundedDraft() (SynthesisInput, SynthesisDraft) {
	input, team := k7Input(false)
	draft := k7Draft(input, team, "severity")
	draft.ClaimedFacts[0].ClaimID = "claim_good_1"
	wrong := "low"
	draft.ClaimedFacts = append(draft.ClaimedFacts, ClaimedFact{ClaimID: "claim_bad_1", Kind: FactHealth, Subject: team, Field: "severity", Value: ScalarValue{String: &wrong}})
	good, bad := draft.Drivers[0], draft.Drivers[0]
	good.DriverID, good.Category, good.ClaimedFactIDs = "driver_good_01", "health", []string{"claim_good_1"}
	bad.DriverID, bad.Category, bad.ClaimedFactIDs = "driver_bad_001", "health", []string{"claim_bad_1"}
	draft.Drivers = []DriverJudgment{good, bad}
	draft.ReadinessGaps = []Finding{{FindingID: "finding_bad_01", Kind: "health", Summary: "s", Subjects: []SubjectRef{team}, EvidenceRefIDs: []string{"evidence_release_1234"}, ClaimedFactIDs: []string{"claim_bad_1"}}}
	return input, draft
}

func TestAPartlyUngroundedSynthesisIsServedWithoutTheUngroundedItems(t *testing.T) {
	input, draft := partlyUngroundedDraft()
	runtime := &recordingModelRuntime{fakeModelRuntime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)}}
	var logs bytes.Buffer
	synthesizer := RuntimeAnswerSynthesizer{Runtime: runtime, Telemetry: NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&logs, nil)))}

	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if err != nil {
		t.Fatalf("Synthesize() error = %v, want the grounded remainder served", err)
	}
	if got := servedClaimIDs(result); !slices.Equal(got, []string{"claim_good_1"}) {
		t.Fatalf("claims on the wire %v, want only the grounded one", got)
	}
	if len(result.Drivers) != 1 || result.Drivers[0].DriverID != "driver_good_01" || len(result.ReadinessGaps) != 0 {
		t.Fatalf("drivers %+v findings %+v, want only the grounded driver", result.Drivers, result.ReadinessGaps)
	}
	if result.Status != InvestigationPartial || !result.Coverage.Partial {
		t.Fatalf("status %q partial %v, want a partial answer", result.Status, result.Coverage.Partial)
	}
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricUngroundedItemsDroppedLimitation) {
		t.Fatalf("limitations %v, want the dropped-items line", result.Limitations)
	}
	if strings.Contains(strings.ToLower(result.DirectJudgment+result.DeterministicAnswer), "is complete") {
		t.Fatalf("prose %q / %q says complete beside a partial status", result.DirectJudgment, result.DeterministicAnswer)
	}
	for _, want := range []string{"dropped_claim_value_contradicts_canonical=1", "dropped_driver_claim_ungrounded=1", "dropped_finding_claim_ungrounded=1"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("served-answer drop line lacks %s: %s", want, logs.String())
		}
	}
}

func TestADuplicatedClaimIDIsNeverHiddenByDroppingOneOfThem(t *testing.T) {
	input, draft := partlyUngroundedDraft()
	twin := draft.ClaimedFacts[0]
	twin.Value = draft.ClaimedFacts[1].Value
	draft.ClaimedFacts = []ClaimedFact{twin, draft.ClaimedFacts[0]}
	runtime := &recordingModelRuntime{fakeModelRuntime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)}}

	_, err := RuntimeAnswerSynthesizer{Runtime: runtime}.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if err == nil {
		t.Fatal("Synthesize() served a draft whose duplicated claim id was hidden by the drop")
	}
}

func TestAFullyGroundedSynthesisCarriesNoDroppedItemsLine(t *testing.T) {
	input, draft := partlyUngroundedDraft()
	draft.ClaimedFacts = draft.ClaimedFacts[:1]
	draft.Drivers = draft.Drivers[:1]
	draft.ReadinessGaps = nil
	runtime := &recordingModelRuntime{fakeModelRuntime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)}}

	result, err := RuntimeAnswerSynthesizer{Runtime: runtime}.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if err != nil || slices.Contains(result.Limitations, contractsv1.ContextFabricUngroundedItemsDroppedLimitation) || result.Status != InvestigationComplete {
		t.Fatalf("err %v status %q limitations %v, want an unchanged complete answer", err, result.Status, result.Limitations)
	}
}

func TestADraftWithNothingGroundedStillRejects(t *testing.T) {
	input, draft := partlyUngroundedDraft()
	draft.ClaimedFacts = draft.ClaimedFacts[1:]
	draft.Drivers = draft.Drivers[1:]
	runtime := &recordingModelRuntime{fakeModelRuntime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)}}

	_, err := RuntimeAnswerSynthesizer{Runtime: runtime}.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if err == nil {
		t.Fatal("Synthesize() served an answer with nothing grounded left")
	}
}

func TestAnOutOfScopeClaimIsNotDroppedAsUngrounded(t *testing.T) {
	input, draft := partlyUngroundedDraft()
	draft.ClaimedFacts[1].Subject = SubjectRef{Kind: SubjectTeam, CanonicalID: "team_other", Label: "Other"}
	runtime := &recordingModelRuntime{fakeModelRuntime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)}}

	_, err := RuntimeAnswerSynthesizer{Runtime: runtime}.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if SynthesisRejectionReasonOf(err) != RejectionReasonClaimSubjectOutOfScope {
		t.Fatalf("error %v, want the subject-scope rejection kept", err)
	}
}

func servedClaimIDs(result InvestigationResult) []string {
	ids := []string{}
	for _, claim := range result.ClaimedFacts {
		ids = append(ids, claim.ClaimID)
	}
	return ids
}

func TestEngineServesTheGroundedRemainderOfAPartlyUngroundedSynthesis(t *testing.T) {
	t.Parallel()
	project := acceptanceProject()
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bootstrapFactBundle(project), nil
	})
	draft := bootstrapDraft(project)
	draft.ClaimedFacts = append(draft.ClaimedFacts, ClaimedFact{ClaimID: "claim_readiness_wrong", Kind: FactReadiness, Subject: project, Field: "release_ready", Value: boolScalar(true)})
	wrong := draft.Drivers[0]
	wrong.DriverID, wrong.ClaimedFactIDs = "driver_wrong_0001", []string{"claim_readiness_wrong"}
	draft.Drivers = append(draft.Drivers, wrong)
	engine := buildAcceptanceEngine(t, graph, facts, bootstrapInterpretation(), draft, newMapResultStore())

	request := validInvestigationRequestWithConfirmedWindow()
	request.Question = "What is the actual status of Ask Dev and what is driving it?"
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)

	if err != nil {
		t.Fatalf("Investigate() error = %v, want the grounded remainder served", err)
	}
	if got := servedClaimIDs(result); !slices.Equal(got, []string{"claim_readiness_bootstrap"}) {
		t.Fatalf("claims on the wire %v, want only the grounded one", got)
	}
	if len(result.Drivers) != 1 || result.Drivers[0].DriverID != "driver_bootstrap01" {
		t.Fatalf("drivers %+v, want only the grounded driver", result.Drivers)
	}
	if result.Status != InvestigationPartial || !slices.Contains(result.Limitations, contractsv1.ContextFabricUngroundedItemsDroppedLimitation) {
		t.Fatalf("status %q limitations %v, want partial with the dropped-items line", result.Status, result.Limitations)
	}
}
