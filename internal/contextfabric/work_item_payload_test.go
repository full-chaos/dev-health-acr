package contextfabric

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestClassifyWorkItemTuplePreservesEverySemanticReadState(t *testing.T) {
	t.Parallel()
	state := workItemTupleSemanticStateFixture()
	result := workItemTuplePayloadFixture(t)
	for _, read := range []SemanticStateReadStatus{
		SemanticStateReadAvailable,
		SemanticStateReadAbsent,
		SemanticStateReadMalformed,
		SemanticStateReadUnsupportedVersion,
		SemanticStateReadOversized,
	} {
		read := read
		t.Run(string(read), func(t *testing.T) {
			t.Parallel()
			got := ClassifyWorkItemTuple(result, state, read)
			if got.SemanticRead != read {
				t.Fatalf("SemanticRead = %q, want the source read status %q", got.SemanticRead, read)
			}
			want := WorkItemTupleDeclined
			if read == SemanticStateReadAvailable {
				want = WorkItemTupleEligible
			}
			if got.Disposition != want {
				t.Fatalf("Disposition = %q, want %q", got.Disposition, want)
			}
		})
	}
}

func TestClassifyWorkItemTupleAvailableReadingIsTheAuthority(t *testing.T) {
	t.Parallel()
	result := workItemTuplePayloadFixture(t)
	cases := []struct {
		name   string
		mutate func(*PersistedSemanticState)
	}{
		{name: "nil state", mutate: func(state *PersistedSemanticState) { *state = PersistedSemanticState{} }},
		{name: "wrong family", mutate: func(state *PersistedSemanticState) { state.Family = QuestionFamilyGroupedCohortStatus }},
		{name: "wrong expression kind", mutate: func(state *PersistedSemanticState) {
			state.Frame.SubjectExpression.Kind = SubjectExpressionNamed
		}},
		{name: "missing scoped expression", mutate: func(state *PersistedSemanticState) { state.Frame.SubjectExpression.Scoped = nil }},
		{name: "wrong member kind", mutate: func(state *PersistedSemanticState) {
			state.Frame.SubjectExpression.Scoped.MemberKind = SubjectRepository
		}},
		{name: "qualified members", mutate: func(state *PersistedSemanticState) {
			state.Frame.SubjectExpression.Scoped.MemberQualifier = MemberQualifierStatus
		}},
		{name: "wrong anchor kind", mutate: func(state *PersistedSemanticState) { state.ScopeAnchor.Kind = SubjectTeam }},
		{name: "second expression variant", mutate: func(state *PersistedSemanticState) {
			state.Frame.SubjectExpression.Named = &NamedSubjectExpression{Terms: []string{"anchor"}}
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := workItemTupleSemanticStateFixture()
			tc.mutate(state)
			got := ClassifyWorkItemTuple(result, state, SemanticStateReadAvailable)
			if got.Disposition != WorkItemTupleNotApplicable {
				t.Fatalf("Disposition = %q, want %q for malformed tuple state", got.Disposition, WorkItemTupleNotApplicable)
			}
		})
	}
}

func TestClassifyWorkItemTupleUnreadableStateUsesPayloadOnlyAsDecline(t *testing.T) {
	t.Parallel()
	result := workItemTuplePayloadFixture(t)
	state := workItemTupleSemanticStateFixture()
	classifyWithoutPanic := func(result InvestigationResult, state *PersistedSemanticState, read SemanticStateReadStatus) (classification WorkItemTupleClassification, panicked bool) {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		return ClassifyWorkItemTuple(result, state, read), false
	}

	withoutMarker := result
	withoutMarker.AnswerPlan = nil
	for _, read := range []SemanticStateReadStatus{SemanticStateReadAbsent, SemanticStateReadMalformed, SemanticStateReadUnsupportedVersion, SemanticStateReadOversized, "unreported"} {
		got, panicked := classifyWithoutPanic(withoutMarker, state, read)
		if panicked {
			t.Errorf("without payload marker, read %q panicked; want ordinary not-applicable classification", read)
			continue
		}
		if got.Disposition != WorkItemTupleNotApplicable {
			t.Errorf("without payload marker, read %q disposition = %q, want %q", read, got.Disposition, WorkItemTupleNotApplicable)
		}
	}

	for _, read := range []SemanticStateReadStatus{SemanticStateReadAbsent, SemanticStateReadMalformed, SemanticStateReadUnsupportedVersion, SemanticStateReadOversized} {
		got, panicked := classifyWithoutPanic(result, nil, read)
		if panicked {
			t.Errorf("payload marker with read %q panicked; want ordinary declined classification", read)
			continue
		}
		if got.Disposition != WorkItemTupleDeclined {
			t.Errorf("payload marker with read %q disposition = %q, want %q", read, got.Disposition, WorkItemTupleDeclined)
		}
	}
	// An available status with no decoded state is inconsistent and must not
	// fall back to the plan marker.
	got, panicked := classifyWithoutPanic(result, nil, SemanticStateReadAvailable)
	if panicked {
		t.Fatal("available status with nil state panicked; want ordinary not-applicable classification")
	}
	if got.Disposition != WorkItemTupleNotApplicable {
		t.Fatalf("available status with nil state disposition = %q, want %q", got.Disposition, WorkItemTupleNotApplicable)
	}
}

func TestValidateWorkItemTuplePayloadAcceptsCompletePartialAndDegraded(t *testing.T) {
	t.Parallel()
	for _, status := range []InvestigationStatus{InvestigationComplete, InvestigationPartial, InvestigationDegraded} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			result := workItemTuplePayloadFixture(t)
			result.Status = status
			if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err != nil {
				t.Fatalf("ValidateWorkItemTuplePayload() error = %v", err)
			}
		})
	}
}

func TestValidateWorkItemTuplePayloadRejectsCandidateEvidenceAndLabels(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*InvestigationResult)
	}{
		{name: "candidate evidence reference", mutate: func(result *InvestigationResult) {
			result.SubjectResolution.Candidates[0].EvidenceRefIDs = []string{"evidence-anchor"}
		}},
		{name: "candidate evidence label", mutate: func(result *InvestigationResult) {
			result.EvidenceRefLabels["evidence-anchor"] = "anchor"
		}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := workItemTuplePayloadFixture(t)
			tc.mutate(&result)
			if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err == nil {
				t.Fatal("ValidateWorkItemTuplePayload() error = nil, want candidate evidence rejected")
			}
		})
	}
}

func TestValidateWorkItemTuplePayloadAllowsCandidateMemberEvidence(t *testing.T) {
	t.Parallel()
	result := workItemTuplePayloadFixture(t)
	memberEvidence := result.Cohort.Members[0].EvidenceRefIDs[0]
	result.SubjectResolution.Candidates[0].EvidenceRefIDs = []string{memberEvidence}
	if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err != nil {
		t.Fatalf("ValidateWorkItemTuplePayload() error = %v, want candidate member evidence accepted", err)
	}
}

func TestValidateWorkItemTuplePayloadAllowsCanonicalCandidateWithoutEvidence(t *testing.T) {
	t.Parallel()
	result := workItemTuplePayloadFixture(t)
	result.SubjectResolution.Candidates[0].EvidenceRefIDs = nil
	candidate := result.SubjectResolution.Candidates[0]
	if err := candidate.Validate(); err != nil {
		t.Fatalf("canonical candidate validation rejected empty evidence refs: %v", err)
	}
	if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err != nil {
		t.Fatalf("ValidateWorkItemTuplePayload() error = %v, want restricted candidate accepted", err)
	}
}

func TestValidateWorkItemTuplePayloadDerivesMemberEvidenceFromCanonicalIdentity(t *testing.T) {
	t.Parallel()
	result := workItemTuplePayloadFixture(t)
	member := result.Cohort.Members[0]
	want := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, "repo-1:work-1")
	if got := member.EvidenceRefIDs[0]; got != want {
		t.Fatalf("member EvidenceRefIDs[0] = %q, want canonical producer ref %q", got, want)
	}
	if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err != nil {
		t.Fatalf("ValidateWorkItemTuplePayload() error = %v for canonical member evidence", err)
	}
}

func TestCanonicalWorkItemEvidenceRefDecodesEveryIdentitySegment(t *testing.T) {
	t.Parallel()
	canonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{"repo:one", "work%two"}, nil)
	if err != nil {
		t.Fatalf("identity.Derive() error = %v", err)
	}
	if omitted {
		t.Fatal("identity.Derive() unexpectedly omitted encoded work-item fixture")
	}
	got, ok := canonicalWorkItemEvidenceRef(SubjectRef{Kind: SubjectWorkItem, CanonicalID: canonicalID})
	if !ok {
		t.Fatalf("canonicalWorkItemEvidenceRef(%q) was not recognized", canonicalID)
	}
	want := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, "repo:one:work%two")
	if got != want {
		t.Fatalf("canonicalWorkItemEvidenceRef(%q) = %q, want existing producer ref %q", canonicalID, got, want)
	}
}

func TestValidateWorkItemTuplePayloadRejectsEveryOutsideShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*InvestigationResult)
	}{
		{name: "missing candidate", mutate: func(result *InvestigationResult) { result.SubjectResolution.Candidates = nil }},
		{name: "two candidates", mutate: func(result *InvestigationResult) {
			result.SubjectResolution.Candidates = append(result.SubjectResolution.Candidates, result.SubjectResolution.Candidates[0])
		}},
		{name: "missing committed anchor", mutate: func(result *InvestigationResult) { result.SubjectResolution.Committed = nil }},
		{name: "candidate is proposed", mutate: func(result *InvestigationResult) { result.SubjectResolution.Candidates[0].State = ResolutionProposed }},
		{name: "candidate is a team", mutate: func(result *InvestigationResult) { result.SubjectResolution.Candidates[0].Subject.Kind = SubjectTeam }},
		{name: "committed anchor is a team", mutate: func(result *InvestigationResult) { result.SubjectResolution.Committed[0].Kind = SubjectTeam }},
		{name: "candidate and committed disagree", mutate: func(result *InvestigationResult) { result.SubjectResolution.Committed[0].CanonicalID = "project-other" }},
		{name: "clarification prompt", mutate: func(result *InvestigationResult) { result.SubjectResolution.ClarificationPrompt = "choose" }},
		{name: "wrong plan family", mutate: func(result *InvestigationResult) { result.AnswerPlan.Family = QuestionFamilyGroupedCohortStatus }},
		{name: "wrong plan member kind", mutate: func(result *InvestigationResult) { result.AnswerPlan.MemberKind = SubjectRepository }},
		{name: "wrong cohort kind", mutate: func(result *InvestigationResult) { result.Cohort.Kind = SubjectProject }},
		{name: "cohort exclusion", mutate: func(result *InvestigationResult) {
			result.Cohort.Exclusions = []contractsv1.ContextFabricCohortExclusion{{Subject: SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work-2", Label: "work-2"}, Reason: "hidden"}}
		}},
		{name: "cohort group", mutate: func(result *InvestigationResult) {
			result.Cohort.Groups = []contractsv1.ContextFabricCohortGroup{{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team-1", Label: "team-1"}}}
		}},
		{name: "duplicate member", mutate: func(result *InvestigationResult) {
			result.Cohort.Members = append(result.Cohort.Members, result.Cohort.Members[0])
		}},
		{name: "member is a project", mutate: func(result *InvestigationResult) { result.Cohort.Members[0].Subject.Kind = SubjectProject }},
		{name: "member is anchor", mutate: func(result *InvestigationResult) { result.Cohort.Members[0].Subject.CanonicalID = "project-1" }},
		{name: "member has legacy identity", mutate: func(result *InvestigationResult) { result.Cohort.Members[0].Subject.CanonicalID = "work-legacy" }},
		{name: "member has foreign evidence", mutate: func(result *InvestigationResult) {
			result.Cohort.Members[0].EvidenceRefIDs = []string{"evidence-foreign"}
		}},
		{name: "relationship path", mutate: func(result *InvestigationResult) { result.Paths = []RelationshipPath{{PathID: "path-1"}} }},
		{name: "unsupported claim kind", mutate: func(result *InvestigationResult) { result.ClaimedFacts[0].Kind = FactReadiness }},
		{name: "status claim on anchor", mutate: func(result *InvestigationResult) {
			result.ClaimedFacts[0].Subject = result.SubjectResolution.Committed[0]
		}},
		{name: "member claim carries table data", mutate: func(result *InvestigationResult) {
			result.ClaimedFacts[0].Rows = []contractsv1.ContextFabricClaimedFactRow{{Fields: map[string]contractsv1.ContextFabricScalarValue{"status": {String: stringPointer("open")}}}}
		}},
		{name: "cardinality claim on another organization", mutate: func(result *InvestigationResult) { result.ClaimedFacts[2].Subject.CanonicalID = "org-2" }},
		{name: "cardinality claim has wrong field", mutate: func(result *InvestigationResult) { result.ClaimedFacts[2].Field = "project_count" }},
		{name: "cardinality claim has non-integer value", mutate: func(result *InvestigationResult) {
			result.ClaimedFacts[2].Value = ScalarValue{String: stringPointer("1")}
		}},
		{name: "foreign result evidence", mutate: func(result *InvestigationResult) {
			result.EvidenceRefIDs = append(result.EvidenceRefIDs, "evidence-foreign")
		}},
		{name: "finding has no member subject", mutate: func(result *InvestigationResult) { result.RemainingWork[0].Subjects = nil }},
		{name: "finding names foreign subject", mutate: func(result *InvestigationResult) {
			result.RemainingWork[0].Subjects[0] = SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work-foreign", Label: "foreign"}
		}},
		{name: "finding has foreign evidence", mutate: func(result *InvestigationResult) {
			result.RemainingWork[0].EvidenceRefIDs = []string{"evidence-foreign"}
		}},
		{name: "finding cites cardinality claim", mutate: func(result *InvestigationResult) {
			result.RemainingWork[0].ClaimedFactIDs = []string{"claim-cardinality"}
		}},
		{name: "driver has no member subject", mutate: func(result *InvestigationResult) {
			result.Drivers = []DriverJudgment{{EvidenceRefIDs: []string{"evidence-member"}}}
		}},
		{name: "driver names foreign subject", mutate: func(result *InvestigationResult) {
			result.Drivers = []DriverJudgment{{AffectedSubjects: []SubjectRef{{Kind: SubjectProject, CanonicalID: "project-other", Label: "other"}}, EvidenceRefIDs: []string{"evidence-member"}}}
		}},
		{name: "driver has foreign evidence", mutate: func(result *InvestigationResult) {
			result.Drivers = []DriverJudgment{{AffectedSubjects: []SubjectRef{result.Cohort.Members[0].Subject}, EvidenceRefIDs: []string{"evidence-foreign"}}}
		}},
		{name: "foreign evidence label", mutate: func(result *InvestigationResult) {
			result.EvidenceRefLabels = map[string]string{"evidence-foreign": "foreign"}
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := workItemTuplePayloadFixture(t)
			tc.mutate(&result)
			if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err == nil {
				t.Fatal("ValidateWorkItemTuplePayload() error = nil, want the outside shape rejected")
			}
		})
	}
}

func TestValidateWorkItemTuplePayloadRequiresTheCurrentOrganizationForCardinality(t *testing.T) {
	t.Parallel()
	result := workItemTuplePayloadFixture(t)
	if err := ValidateWorkItemTuplePayload(result, storage.Principal{}); err == nil {
		t.Fatal("ValidateWorkItemTuplePayload() error = nil, want an empty current organization to reject a cardinality claim")
	}
	if WorkItemTuplePayloadAllowed(result, storage.Principal{OrgID: "org-2"}) {
		t.Fatal("WorkItemTuplePayloadAllowed() = true for a cardinality claim belonging to another organization")
	}
}

func workItemTupleSemanticStateFixture() *PersistedSemanticState {
	return &PersistedSemanticState{
		Family:       QuestionFamilyScopedCohortStatus,
		FramePresent: true,
		Frame: &QuestionFrame{SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"project"}, MemberKind: SubjectWorkItem},
		}},
		ScopeAnchor: SemanticScopeAnchor{Kind: SubjectProject, Term: "project"},
	}
}

func workItemTuplePayloadFixture(t *testing.T) InvestigationResult {
	t.Helper()
	anchor := SubjectRef{Kind: SubjectProject, CanonicalID: "project-1", Label: "Project"}
	memberID, omitted, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "work-1"}, nil)
	if err != nil {
		t.Fatalf("identity.Derive() error = %v", err)
	}
	if omitted {
		t.Fatal("identity.Derive() unexpectedly omitted canonical work-item fixture")
	}
	member := SubjectRef{Kind: SubjectWorkItem, CanonicalID: memberID, Label: "Work item"}
	memberEvidence := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, "repo-1:work-1")
	statusValue := "open"
	titleValue := "Implement the thing"
	count := int64(1)
	return InvestigationResult{
		Status: InvestigationComplete,
		AnswerPlan: &AnswerPlan{
			Family:     QuestionFamilyScopedCohortStatus,
			MemberKind: SubjectWorkItem,
		},
		SubjectResolution: SubjectResolution{
			Candidates: []SubjectCandidate{{
				ReceiptID: "receipt-1", Subject: anchor, State: ResolutionCommitted,
				// The anchor as resolution recorded it: a match for the stored
				// reading's anchor term (count_population_scope.go).
				MatchedTerms: []string{"project"}, MatchReasons: []string{"exact"},
			}},
			Committed: []SubjectRef{anchor},
		},
		Cohort: &Cohort{
			Kind:    SubjectWorkItem,
			Members: []CohortMember{{Subject: member, EvidenceRefIDs: []string{memberEvidence}}},
		},
		EvidenceRefIDs: []string{memberEvidence},
		ClaimedFacts: []ClaimedFact{
			{ClaimID: "claim-status", Kind: FactStatus, Subject: member, Field: "status", Value: ScalarValue{String: &statusValue}},
			{ClaimID: "claim-work", Kind: FactWork, Subject: member, Field: "title", Value: ScalarValue{String: &titleValue}},
			{ClaimID: "claim-cardinality", Kind: contractsv1.ContextFabricFactCardinality, Subject: SubjectRef{Kind: SubjectOrganization, CanonicalID: "org-1", Label: "org-1"}, Field: "work_item_count", Value: ScalarValue{Integer: &count}},
		},
		RemainingWork: []Finding{{
			FindingID: "finding-1", Kind: "remaining_work", Summary: "work remains", Subjects: []SubjectRef{member},
			EvidenceRefIDs: []string{memberEvidence}, ClaimedFactIDs: []string{"claim-status"},
		}},
		EvidenceRefLabels: map[string]string{
			memberEvidence: "member",
		},
	}
}

func stringPointer(value string) *string { return &value }
