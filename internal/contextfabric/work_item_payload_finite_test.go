package contextfabric

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestClassifyWorkItemTupleFiniteReadDomain(t *testing.T) {
	t.Parallel()
	result := workItemTuplePayloadFixture(t)
	state := workItemTupleSemanticStateFixture()
	cases := []struct {
		name  string
		read  SemanticStateReadStatus
		state *PersistedSemanticState
		want  WorkItemTupleDisposition
	}{
		{name: "available validated reading", read: SemanticStateReadAvailable, state: state, want: WorkItemTupleEligible},
		{name: "absent", read: SemanticStateReadAbsent, state: nil, want: WorkItemTupleDeclined},
		{name: "malformed", read: SemanticStateReadMalformed, state: nil, want: WorkItemTupleDeclined},
		{name: "unsupported version", read: SemanticStateReadUnsupportedVersion, state: nil, want: WorkItemTupleDeclined},
		{name: "oversized", read: SemanticStateReadOversized, state: nil, want: WorkItemTupleDeclined},
		{name: "future read status", read: SemanticStateReadStatus("future"), state: nil, want: WorkItemTupleDeclined},
		{name: "available without decoded state", read: SemanticStateReadAvailable, state: nil, want: WorkItemTupleNotApplicable},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyWorkItemTuple(result, tc.state, tc.read)
			if got.SemanticRead != tc.read {
				t.Fatalf("SemanticRead = %q, want %q", got.SemanticRead, tc.read)
			}
			if got.Disposition != tc.want {
				t.Fatalf("Disposition = %q, want %q", got.Disposition, tc.want)
			}
		})
	}
}

func TestClassifyWorkItemTupleRejectsWrongPayloadMarker(t *testing.T) {
	t.Parallel()
	result := workItemTuplePayloadFixture(t)
	cases := []struct {
		name   string
		family QuestionFamily
		member SubjectKind
	}{
		{name: "wrong family", family: QuestionFamilyGroupedCohortStatus, member: SubjectWorkItem},
		{name: "wrong member kind", family: QuestionFamilyScopedCohortStatus, member: SubjectRepository},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mutated := result
			mutated.AnswerPlan = &AnswerPlan{Family: tc.family, MemberKind: tc.member}
			got := ClassifyWorkItemTuple(mutated, nil, SemanticStateReadAbsent)
			if got.Disposition != WorkItemTupleNotApplicable {
				t.Fatalf("Disposition = %q, want %q for a wrong payload marker", got.Disposition, WorkItemTupleNotApplicable)
			}
		})
	}
}

func TestValidateWorkItemTuplePayloadRejectsForeignReferencesAcrossEveryField(t *testing.T) {
	t.Parallel()
	foreignSubject := mutationWorkItemSubject(t, "repo-2", "work-2", "Foreign work item")
	foreignEvidence := mutationWorkItemEvidence(t, "repo-2", "work-2")
	anchorEvidence := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, "project-1")

	cases := []struct {
		name   string
		mutate func(*InvestigationResult)
	}{
		{name: "candidate anchor evidence", mutate: func(result *InvestigationResult) {
			result.SubjectResolution.Candidates[0].EvidenceRefIDs = []string{anchorEvidence}
		}},
		{name: "candidate foreign member evidence", mutate: func(result *InvestigationResult) {
			result.SubjectResolution.Candidates[0].EvidenceRefIDs = []string{foreignEvidence}
		}},
		{name: "candidate foreign evidence label", mutate: func(result *InvestigationResult) {
			result.EvidenceRefLabels = map[string]string{foreignEvidence: "foreign"}
		}},
		{name: "result evidence", mutate: func(result *InvestigationResult) {
			result.EvidenceRefIDs = []string{foreignEvidence}
		}},
		{name: "member evidence", mutate: func(result *InvestigationResult) {
			result.Cohort.Members[0].EvidenceRefIDs = []string{foreignEvidence}
		}},
		{name: "status claim subject", mutate: func(result *InvestigationResult) {
			result.ClaimedFacts[0].Subject = foreignSubject
		}},
		{name: "work claim subject", mutate: func(result *InvestigationResult) {
			result.ClaimedFacts[1].Subject = foreignSubject
		}},
		{name: "cardinality claim subject", mutate: func(result *InvestigationResult) {
			result.ClaimedFacts[2].Subject = SubjectRef{Kind: SubjectOrganization, CanonicalID: "org-2", Label: "org-2"}
		}},
		{name: "remaining-work subject", mutate: func(result *InvestigationResult) {
			result.ReadinessGaps, result.Conflicts, result.Drivers = nil, nil, nil
			result.RemainingWork[0].Subjects = []SubjectRef{foreignSubject}
		}},
		{name: "remaining-work evidence", mutate: func(result *InvestigationResult) {
			result.ReadinessGaps, result.Conflicts, result.Drivers = nil, nil, nil
			result.RemainingWork[0].EvidenceRefIDs = []string{foreignEvidence}
		}},
		{name: "remaining-work claim", mutate: func(result *InvestigationResult) {
			result.ReadinessGaps, result.Conflicts, result.Drivers = nil, nil, nil
			result.RemainingWork[0].ClaimedFactIDs = []string{"claim-cardinality"}
		}},
		{name: "readiness-gap subject", mutate: func(result *InvestigationResult) {
			result.RemainingWork, result.Conflicts, result.Drivers = nil, nil, nil
			result.ReadinessGaps = []Finding{mutationWorkItemFinding(*result)}
			result.ReadinessGaps[0].Subjects = []SubjectRef{foreignSubject}
		}},
		{name: "readiness-gap evidence", mutate: func(result *InvestigationResult) {
			result.RemainingWork, result.Conflicts, result.Drivers = nil, nil, nil
			result.ReadinessGaps = []Finding{mutationWorkItemFinding(*result)}
			result.ReadinessGaps[0].EvidenceRefIDs = []string{foreignEvidence}
		}},
		{name: "readiness-gap claim", mutate: func(result *InvestigationResult) {
			result.RemainingWork, result.Conflicts, result.Drivers = nil, nil, nil
			result.ReadinessGaps = []Finding{mutationWorkItemFinding(*result)}
			result.ReadinessGaps[0].ClaimedFactIDs = []string{"claim-cardinality"}
		}},
		{name: "conflict subject", mutate: func(result *InvestigationResult) {
			result.RemainingWork, result.ReadinessGaps, result.Drivers = nil, nil, nil
			result.Conflicts = []Finding{mutationWorkItemFinding(*result)}
			result.Conflicts[0].Subjects = []SubjectRef{foreignSubject}
		}},
		{name: "conflict evidence", mutate: func(result *InvestigationResult) {
			result.RemainingWork, result.ReadinessGaps, result.Drivers = nil, nil, nil
			result.Conflicts = []Finding{mutationWorkItemFinding(*result)}
			result.Conflicts[0].EvidenceRefIDs = []string{foreignEvidence}
		}},
		{name: "conflict claim", mutate: func(result *InvestigationResult) {
			result.RemainingWork, result.ReadinessGaps, result.Drivers = nil, nil, nil
			result.Conflicts = []Finding{mutationWorkItemFinding(*result)}
			result.Conflicts[0].ClaimedFactIDs = []string{"claim-cardinality"}
		}},
		{name: "driver subject", mutate: func(result *InvestigationResult) {
			result.RemainingWork, result.ReadinessGaps, result.Conflicts = nil, nil, nil
			result.Drivers = []DriverJudgment{mutationWorkItemDriver(*result)}
			result.Drivers[0].AffectedSubjects = []SubjectRef{foreignSubject}
		}},
		{name: "driver evidence", mutate: func(result *InvestigationResult) {
			result.RemainingWork, result.ReadinessGaps, result.Conflicts = nil, nil, nil
			result.Drivers = []DriverJudgment{mutationWorkItemDriver(*result)}
			result.Drivers[0].EvidenceRefIDs = []string{foreignEvidence}
		}},
		{name: "driver claim", mutate: func(result *InvestigationResult) {
			result.RemainingWork, result.ReadinessGaps, result.Conflicts = nil, nil, nil
			result.Drivers = []DriverJudgment{mutationWorkItemDriver(*result)}
			result.Drivers[0].ClaimedFactIDs = []string{"claim-cardinality"}
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := workItemTuplePayloadFixture(t)
			tc.mutate(&result)
			if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err == nil {
				t.Fatal("ValidateWorkItemTuplePayload() error = nil, want foreign reference rejected")
			}
		})
	}
}

func TestValidateWorkItemTuplePayloadAllowsEmptyPrincipalWhenCardinalityIsAbsent(t *testing.T) {
	t.Parallel()
	result := workItemTuplePayloadFixture(t)
	result.ClaimedFacts = result.ClaimedFacts[:2]
	if err := ValidateWorkItemTuplePayload(result, storage.Principal{}); err != nil {
		t.Fatalf("ValidateWorkItemTuplePayload() error = %v, want no-cardinality payload accepted", err)
	}
}

func TestValidateWorkItemTuplePayloadAdmitsOnlyMemberFactsAndOrganizationCardinality(t *testing.T) {
	t.Parallel()
	for _, kind := range contractsv1.ContextFabricFactKindVocabulary() {
		kind := kind
		if kind == contractsv1.ContextFabricFactStatus || kind == contractsv1.ContextFabricFactWork {
			continue
		}
		t.Run(string(kind), func(t *testing.T) {
			result := workItemTuplePayloadFixture(t)
			result.ClaimedFacts[0].Kind = kind
			if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err == nil {
				t.Fatalf("ValidateWorkItemTuplePayload() accepted non-member fact kind %q", kind)
			}
		})
	}

	result := workItemTuplePayloadFixture(t)
	result.ClaimedFacts = result.ClaimedFacts[:2]
	if err := ValidateWorkItemTuplePayload(result, storage.Principal{}); err != nil {
		t.Fatalf("ValidateWorkItemTuplePayload() rejected the two admitted member fact kinds: %v", err)
	}
}

func TestValidateWorkItemTuplePayloadRejectsEveryClaimTableField(t *testing.T) {
	t.Parallel()
	row := contractsv1.ContextFabricClaimedFactRow{Fields: map[string]contractsv1.ContextFabricScalarValue{"status": {String: stringPointer("open")}}}
	cases := []struct {
		name   string
		mutate func(*ClaimedFact)
	}{
		{name: "rows", mutate: func(claim *ClaimedFact) { claim.Rows = []contractsv1.ContextFabricClaimedFactRow{row} }},
		{name: "table declaration", mutate: func(claim *ClaimedFact) { claim.Table = &contractsv1.ContextFabricClaimedFactTable{} }},
		{name: "time series rows", mutate: func(claim *ClaimedFact) { claim.TimeSeriesRows = []contractsv1.ContextFabricClaimedFactRow{row} }},
		{name: "time series table declaration", mutate: func(claim *ClaimedFact) { claim.TimeSeriesTable = &contractsv1.ContextFabricClaimedFactTable{} }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := workItemTuplePayloadFixture(t)
			result.ClaimedFacts = result.ClaimedFacts[:1]
			tc.mutate(&result.ClaimedFacts[0])
			if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err == nil {
				t.Fatal("ValidateWorkItemTuplePayload() accepted a member claim carrying table data")
			}
		})
	}
}

func TestValidateWorkItemTuplePayloadRejectsClaimIdentityBoundaryViolations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*InvestigationResult)
	}{
		{name: "empty claim id", mutate: func(result *InvestigationResult) { result.ClaimedFacts[0].ClaimID = "" }},
		{name: "duplicate claim id", mutate: func(result *InvestigationResult) { result.ClaimedFacts[1].ClaimID = result.ClaimedFacts[0].ClaimID }},
		{name: "duplicate cardinality claim", mutate: func(result *InvestigationResult) {
			result.ClaimedFacts = append(result.ClaimedFacts, result.ClaimedFacts[2])
			result.ClaimedFacts[3].ClaimID = "claim-cardinality-copy"
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := workItemTuplePayloadFixture(t)
			tc.mutate(&result)
			if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err == nil {
				t.Fatal("ValidateWorkItemTuplePayload() accepted an invalid claim identity")
			}
		})
	}
}

func mutationWorkItemSubject(t *testing.T, repositoryID, workItemID, label string) SubjectRef {
	t.Helper()
	canonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{repositoryID, workItemID}, nil)
	if err != nil {
		t.Fatalf("identity.Derive() error = %v", err)
	}
	if omitted {
		t.Fatalf("identity.Derive() omitted %q/%q", repositoryID, workItemID)
	}
	return SubjectRef{Kind: SubjectWorkItem, CanonicalID: canonicalID, Label: label}
}

func mutationWorkItemEvidence(t *testing.T, repositoryID, workItemID string) string {
	t.Helper()
	return contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, repositoryID+":"+workItemID)
}

func mutationWorkItemFinding(result InvestigationResult) Finding {
	member := result.Cohort.Members[0].Subject
	ref := result.Cohort.Members[0].EvidenceRefIDs[0]
	return Finding{
		FindingID: "finding-extra", Kind: "extra", Summary: "extra finding",
		Subjects: []SubjectRef{member}, EvidenceRefIDs: []string{ref}, ClaimedFactIDs: []string{"claim-status"},
	}
}

func mutationWorkItemDriver(result InvestigationResult) DriverJudgment {
	member := result.Cohort.Members[0].Subject
	ref := result.Cohort.Members[0].EvidenceRefIDs[0]
	return DriverJudgment{
		AffectedSubjects: []SubjectRef{member}, EvidenceRefIDs: []string{ref}, ClaimedFactIDs: []string{"claim-status"},
	}
}
