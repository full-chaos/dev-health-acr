package contextfabric

import (
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWalkListCutKeepsTheTuplePayloadValid(t *testing.T) {
	result := workItemTuplePayloadFixture(t)
	members := []CohortMember{result.Cohort.Members[0]}
	refs := []string{result.Cohort.Members[0].EvidenceRefIDs[0]}
	for i := 2; i < 21; i++ {
		id, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", fmt.Sprintf("work-%d", i)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, fmt.Sprintf("repo-1:work-%d", i))
		members = append(members, CohortMember{Subject: SubjectRef{Kind: SubjectWorkItem, CanonicalID: id}, EvidenceRefIDs: []string{ref}})
		refs = append(refs, ref)
		result.EvidenceRefLabels[ref] = "member"
	}
	result.Cohort.Members = members
	result.Cohort.Population = 2000
	result.Cohort.Complete = true
	result.EvidenceRefIDs = refs
	result.SubjectResolution.Candidates[0].EvidenceRefIDs = refs
	if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err != nil {
		t.Fatalf("precondition: uncut payload invalid: %v", err)
	}
	cut, ok := cutWalkListMembers(result)
	if !ok {
		t.Fatal("expected a cut")
	}
	if err := ValidateWorkItemTuplePayload(cut, storage.Principal{OrgID: "org-1"}); err != nil {
		t.Fatalf("payload after the walk-list cut is invalid: %v", err)
	}
}

func TestAByteFitCutOfAWalkListServesAValidPayloadInsteadOfFailingValidation(t *testing.T) {
	shape := budgetTrimShape{members: 60, claims: 2, maxItems: 120, findings: 1, evidenceMembers: 40}
	full, err, _ := budgetTrimInvestigate(t, shape)
	if err != nil {
		t.Fatalf("uncut probe: %v", err)
	}
	measured, err := contractsv1.MeasureContextFabricResponse(full)
	if err != nil {
		t.Fatal(err)
	}
	shape.maxBytes = measured.Bytes - measured.Bytes/5
	cutResult, err, _ := budgetTrimInvestigate(t, shape)
	if err != nil {
		t.Fatalf("the byte fit cut failed the investigation: %v", err)
	}
	if len(cutResult.Cohort.Members) >= len(full.Cohort.Members) {
		t.Fatalf("members %d of %d: the byte fit did not cut, the row does not exercise the lever", len(cutResult.Cohort.Members), len(full.Cohort.Members))
	}
	if err := ValidateWorkItemTuplePayload(cutResult, storage.Principal{OrgID: "org-1"}); err != nil {
		t.Fatalf("served payload invalid: %v", err)
	}
}
