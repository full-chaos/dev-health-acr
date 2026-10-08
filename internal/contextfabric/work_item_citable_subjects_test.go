package contextfabric

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestTupleValidatorAcceptsADriverNamingTheCommittedAnchor(t *testing.T) {
	result := workItemTuplePayloadFixture(t)
	anchor := result.SubjectResolution.Committed[0]
	result.Drivers = []DriverJudgment{{DriverID: "driver_status01", Standing: DriverPrincipal, Category: "status", Title: "t", Summary: "s", AffectedSubjects: []SubjectRef{anchor}, EvidenceRefIDs: []string{}, ClaimedFactIDs: []string{}, Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.9, Current: true}}
	if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err != nil {
		t.Fatalf("driver naming the committed anchor refused: %v", err)
	}
}

func TestADriverNamingTheCommittedAnchorIsServedNotFailed(t *testing.T) {
	result, err, _ := budgetTrimInvestigate(t, budgetTrimShape{members: 30, claims: 3, maxItems: 30, findings: 1, anchorDriver: true})
	if err != nil {
		t.Fatalf("investigation failed: %v", err)
	}
	if !hasDriverID(result, "driver_anchor01") {
		t.Fatalf("the driver naming the anchor was not served: %+v", result.Drivers)
	}
}

func TestSynthesisAndTupleValidatorShareOneCitableSubjectSet(t *testing.T) {
	result := workItemTuplePayloadFixture(t)
	anchor := result.SubjectResolution.Committed[0]
	member := result.Cohort.Members[0].Subject
	admitted := synthesisSubjects(SynthesisInput{Graph: GraphContext{Resolution: result.SubjectResolution, Cohort: result.Cohort}})
	for _, subject := range []SubjectRef{anchor, member} {
		if _, ok := admitted[subjectKeyForModel(subject)]; !ok {
			t.Fatalf("synthesis does not admit %v", subject)
		}
		probe := result
		probe.Drivers = []DriverJudgment{{DriverID: "driver_status01", Standing: DriverPrincipal, Category: "status", Title: "t", Summary: "s", AffectedSubjects: []SubjectRef{subject}, EvidenceRefIDs: []string{}, ClaimedFactIDs: []string{}, Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.9, Current: true}}
		if err := ValidateWorkItemTuplePayload(probe, storage.Principal{OrgID: "org-1"}); err != nil {
			t.Fatalf("validator refuses %v that synthesis admits: %v", subject, err)
		}
	}
}

func TestAModelDraftBreakingATupleRuleIsServedDegradedNotFailed(t *testing.T) {
	result, err, _ := budgetTrimInvestigate(t, budgetTrimShape{members: 30, claims: 3, maxItems: 30, findings: 1, foreignDriver: true, degradable: true})
	if err != nil {
		t.Fatalf("investigation failed instead of degrading: %v", err)
	}
	if result.Status != InvestigationDegraded || hasDriverID(result, "driver_foreign01") {
		t.Fatalf("status %q drivers %+v: want degraded without the refused driver", result.Status, result.Drivers)
	}
	if !IsSynthesisModelFailureAnswer(result) {
		t.Fatalf("degraded answer does not carry the closed model-failure warning: %v", result.Warnings)
	}
	if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err != nil {
		t.Fatalf("degraded answer is itself invalid: %v", err)
	}
}

func TestAServerCausedTupleRuleStaysAHardFailure(t *testing.T) {
	for _, rule := range []WorkItemTupleRule{WorkItemRuleCardinalityClaimNotAnchor, WorkItemRuleCardinalityClaimValue, WorkItemRuleAnchorDisagree, WorkItemRuleMemberIsAnchor, WorkItemRuleCandidateEvidenceOutsideMembers, WorkItemRuleRelationshipPaths} {
		if WorkItemTupleRuleModelCaused(rule) {
			t.Fatalf("%s is server-caused and must not degrade", rule)
		}
	}
	for _, rule := range []WorkItemTupleRule{WorkItemRuleDriverSubjectOutsideMembers, WorkItemRuleFindingEvidenceOutsideMembers, WorkItemRuleStatusClaimOutsideMembers, WorkItemRuleResultEvidenceOutsideMembers} {
		if !WorkItemTupleRuleModelCaused(rule) {
			t.Fatalf("%s is model-caused and must degrade", rule)
		}
	}
}

func hasDriverID(result InvestigationResult, id string) bool {
	for _, driver := range result.Drivers {
		if driver.DriverID == id {
			return true
		}
	}
	return false
}
