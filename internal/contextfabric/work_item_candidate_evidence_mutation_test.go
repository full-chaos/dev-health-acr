package contextfabric

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWorkItemGuardCandidateAndEvidenceClosure(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	payload := workItemTuplePayloadFixture(t)
	anchor := payload.SubjectResolution.Candidates[0]
	foreignKind := anchor
	foreignKind.Subject.Kind = SubjectTeam
	foreignID := anchor
	foreignID.Subject.CanonicalID = "foreign-project"
	input := payload.SubjectResolution
	input.Candidates = []SubjectCandidate{foreignKind, foreignID, anchor}
	before := copySubjectResolutionForRetry(input)
	got := restrictWorkItemTupleCandidate(input)
	if len(got.Candidates) != 1 || got.Candidates[0].Subject != anchor.Subject || len(got.Candidates[0].EvidenceRefIDs) != 0 || !reflect.DeepEqual(got.Committed, input.Committed) {
		t.Errorf("candidate closure=%+v", got)
	}
	if !reflect.DeepEqual(input, before) {
		t.Error("candidate closure changed input")
	}
	member := payload.Cohort.Members[0].Subject
	ref, ok := canonicalWorkItemEvidenceRef(member)
	if !ok {
		t.Fatal("producer identity missing")
	}
	for _, name := range []string{"retained", "nil_cohort", "invalid_identity"} {
		t.Run(name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			result := workItemTuplePayloadFixture(t)
			result.SubjectResolution.Candidates[0].EvidenceRefIDs = []string{ref, "foreign-ref"}
			result.EvidenceRefLabels = map[string]string{ref: "retained label", "foreign-ref": "foreign label"}
			if name == "nil_cohort" {
				result.Cohort = nil
			}
			if name == "invalid_identity" {
				result.Cohort.Members[0].Subject.CanonicalID = "invalid"
				// This helper also closes malformed input before validation.
				// Failed canonical derivation returns an empty ref; it must
				// not authorize that empty key as candidate evidence/labels.
				result.SubjectResolution.Candidates[0].EvidenceRefIDs = append(result.SubjectResolution.Candidates[0].EvidenceRefIDs, "")
				result.EvidenceRefLabels[""] = "invalid identity label"
			}
			got := restrictWorkItemTupleEvidence(result)
			count := 0
			if name == "retained" {
				count = 1
			}
			if len(got.SubjectResolution.Candidates[0].EvidenceRefIDs) != count || len(got.EvidenceRefLabels) != count {
				t.Errorf("closure=%+v labels=%v", got.SubjectResolution, got.EvidenceRefLabels)
			}
			if count == 1 && (got.SubjectResolution.Candidates[0].EvidenceRefIDs[0] != ref || got.EvidenceRefLabels[ref] != "retained label") {
				t.Error("retained identity evidence lost")
			}
		})
	}
}

func TestWorkItemGuardNilAndCardinalityHelpers(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	if got := workItemTupleSubjects(nil); got == nil || len(got) != 0 {
		t.Errorf("nil cohort subjects=%+v", got)
	}
	if finalWorkItemTupleCensus(nil, nil) != nil {
		t.Error("absent census manufactured")
	}
	applyWorkItemTitles(nil, nil)
	for _, c := range []*WorkItemTupleCensus{nil, {State: WorkItemMembershipCensusUnmeasured}, {State: WorkItemMembershipCensusExact, Value: 0}, {State: WorkItemMembershipCensusExact, Value: 7, Retained: 2}, {State: WorkItemMembershipCensusFloor, Value: 2000, Retained: 2}} {
		got := workItemTupleCardinality(c)
		measured := c != nil && c.State != WorkItemMembershipCensusUnmeasured
		if got.Resolved != measured {
			t.Errorf("census=%+v count=%+v", c, got)
		}
		if measured && (got.Served != c.Value || got.Declared != c.Value || got.PopulationIncomplete != (c.State == WorkItemMembershipCensusFloor)) {
			t.Errorf("population count=%+v census=%+v", got, c)
		}
	}
}

func TestWorkItemGuardAnchorAffirmationRemainsTupleScoped(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, tuple := range []bool{false, true} {
		result := affirmationResult()
		var census *WorkItemTupleCensus
		if tuple {
			census = &WorkItemTupleCensus{State: WorkItemMembershipCensusExact, Value: 1, Retained: 1}
		}
		outcomes := applyCommitAffirmationForWorkItemTuple(&result, census, statisticalInputs(emptyAffirmationGraph(), emptyAffirmationFacts(), result))
		if tuple && (len(outcomes) != 0 || len(result.SubjectResolution.Committed) != 1) {
			t.Error("tuple required anchor status")
		}
		if !tuple && (len(outcomes) != 1 || len(result.SubjectResolution.Committed) != 0) {
			t.Error("ordinary statistical commit lost affirmation")
		}
	}
}

func TestWorkItemGuardPopulationNarrowingCells(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, c := range []*WorkItemTupleCensus{nil, {Value: 0, Retained: 0}, {Value: 1, Retained: 1}, {Value: 7, Retained: 2}} {
		plan := &AnswerPlan{Budget: AnswerPlanBudget{MaxMembers: 5}}
		telemetry := &recordingTelemetry{}
		engine := &Engine{telemetry: telemetry}
		engine.workItemTupleNarrowing(context.Background(), storage.Principal{OrgID: "org-1"}, plan, 0, c)
		want := 0
		if c != nil && c.Value > c.Retained {
			want = 1
		}
		if len(plan.Narrowing) != want || len(telemetry.planNarrowings) != want {
			t.Errorf("census=%+v steps=%+v events=%+v", c, plan.Narrowing, telemetry.planNarrowings)
		}
	}
}
