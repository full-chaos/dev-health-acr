package contextfabric

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestWorkItemTupleCensusValidation(t *testing.T) {
	t.Parallel()
	digest := strings.Repeat("a", 64)
	tests := []struct {
		name   string
		census *WorkItemTupleCensus
		want   WorkItemTupleCensusReadStatus
	}{
		{name: "absent", want: WorkItemTupleCensusReadAbsent},
		{name: "exact zero", census: testWorkItemTupleCensus(WorkItemMembershipCensusExact, 0, 0, digest), want: WorkItemTupleCensusReadAvailable},
		{name: "exact at census limit", census: testWorkItemTupleCensus(WorkItemMembershipCensusExact, WorkItemMembershipCensusLimit, WorkItemMembershipServeLimit, digest), want: WorkItemTupleCensusReadAvailable},
		{name: "floor", census: testWorkItemTupleCensus(WorkItemMembershipCensusFloor, WorkItemMembershipCensusLimit, WorkItemMembershipServeLimit, digest), want: WorkItemTupleCensusReadAvailable},
		{name: "unmeasured", census: testWorkItemTupleCensus(WorkItemMembershipCensusUnmeasured, 0, 0, digest), want: WorkItemTupleCensusReadAvailable},
		{name: "exact over census limit", census: testWorkItemTupleCensus(WorkItemMembershipCensusExact, WorkItemMembershipCensusLimit+1, 0, digest), want: WorkItemTupleCensusReadMalformed},
		{name: "exact retained exceeds value", census: testWorkItemTupleCensus(WorkItemMembershipCensusExact, 2, 3, digest), want: WorkItemTupleCensusReadMalformed},
		{name: "floor has noncanonical value", census: testWorkItemTupleCensus(WorkItemMembershipCensusFloor, WorkItemMembershipCensusLimit-1, 0, digest), want: WorkItemTupleCensusReadMalformed},
		{name: "unmeasured has a claim", census: testWorkItemTupleCensus(WorkItemMembershipCensusUnmeasured, 1, 0, digest), want: WorkItemTupleCensusReadMalformed},
		{name: "bad digest", census: testWorkItemTupleCensus(WorkItemMembershipCensusExact, 1, 1, "not-a-sha256"), want: WorkItemTupleCensusReadMalformed},
		{name: "future version", census: &WorkItemTupleCensus{Version: "work-item-census.v2", State: WorkItemMembershipCensusExact, Value: 1, Retained: 1, RequestedRepositoryScope: []string{}, AuthorizationDigest: digest}, want: WorkItemTupleCensusReadUnsupportedVersion},
	}
	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidateWorkItemTupleCensus(testCase.census); got != testCase.want {
				t.Fatalf("ValidateWorkItemTupleCensus() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestWorkItemTupleCensusNestedWireReadStatuses(t *testing.T) {
	t.Parallel()
	valid := `{"version":"work-item-census.v1","state":"exact","value":1,"retained":1,"requested_repository_scope":["acme/api"],"authorization_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	tests := []struct {
		name string
		raw  string
		want WorkItemTupleCensusReadStatus
	}{
		{name: "valid", raw: valid, want: WorkItemTupleCensusReadAvailable},
		{name: "missing required state", raw: `{"version":"work-item-census.v1","value":1,"retained":1,"requested_repository_scope":[],"authorization_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, want: WorkItemTupleCensusReadMalformed},
		{name: "unrecognized state", raw: `{"version":"work-item-census.v1","state":"unavailable","value":0,"retained":0,"requested_repository_scope":[],"authorization_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, want: WorkItemTupleCensusReadMalformed},
		{name: "unknown field", raw: `{"version":"work-item-census.v1","state":"exact","value":1,"retained":1,"requested_repository_scope":[],"authorization_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","extra":true}`, want: WorkItemTupleCensusReadMalformed},
		{name: "future version stays unsupported", raw: `{"version":"work-item-census.v2"}`, want: WorkItemTupleCensusReadUnsupportedVersion},
	}
	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var census WorkItemTupleCensus
			if err := json.Unmarshal([]byte(testCase.raw), &census); err != nil {
				t.Fatalf("decode nested census: %v", err)
			}
			if got := ValidateWorkItemTupleCensus(&census); got != testCase.want {
				t.Fatalf("ValidateWorkItemTupleCensus() = %q, want %q", got, testCase.want)
			}
		})
	}
	var absent *WorkItemTupleCensus
	if err := json.Unmarshal([]byte("null"), &absent); err != nil {
		t.Fatalf("decode absent census: %v", err)
	}
	if got := ValidateWorkItemTupleCensus(absent); got != WorkItemTupleCensusReadAbsent {
		t.Fatalf("ValidateWorkItemTupleCensus(absent) = %q, want %q", got, WorkItemTupleCensusReadAbsent)
	}
}

func TestDecodeSemanticStateKeepsReadingWhenCensusIsUnavailable(t *testing.T) {
	t.Parallel()
	state := semanticFixture(t)
	encoded, err := EncodeSemanticState(state)
	if err != nil {
		t.Fatalf("EncodeSemanticState() error = %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	for name, census := range map[string]any{
		"malformed": map[string]any{
			"version":                    WorkItemTupleCensusVersion,
			"state":                      "exact",
			"value":                      WorkItemMembershipCensusLimit + 1,
			"retained":                   0,
			"requested_repository_scope": []string{},
			"authorization_digest":       strings.Repeat("a", 64),
		},
		"unsupported": map[string]any{"version": "work-item-census.v2"},
	} {
		name, census := name, census
		t.Run(name, func(t *testing.T) {
			document["work_item_census"] = census
			raw, marshalErr := json.Marshal(document)
			if marshalErr != nil {
				t.Fatalf("marshal mutated snapshot: %v", marshalErr)
			}
			decoded, status := DecodeSemanticState(raw)
			if status != SemanticStateReadAvailable || decoded == nil {
				t.Fatalf("DecodeSemanticState() = %v/%q, want an available semantic reading", decoded, status)
			}
			censusStatus := ValidateWorkItemTupleCensus(decoded.WorkItemCensus)
			if name == "malformed" && censusStatus != WorkItemTupleCensusReadMalformed {
				t.Fatalf("malformed census status = %q, want %q", censusStatus, WorkItemTupleCensusReadMalformed)
			}
			if name == "unsupported" && censusStatus != WorkItemTupleCensusReadUnsupportedVersion {
				t.Fatalf("unsupported census status = %q, want %q", censusStatus, WorkItemTupleCensusReadUnsupportedVersion)
			}
		})
	}
}

func TestEncodeSemanticStateRestrictsWorkItemCensusToTuple(t *testing.T) {
	t.Parallel()
	digest := strings.Repeat("a", 64)
	nonTuple := semanticFixture(t)
	nonTuple.WorkItemCensus = testWorkItemTupleCensus(WorkItemMembershipCensusExact, 1, 1, digest)
	if _, err := EncodeSemanticState(nonTuple); !errors.Is(err, ErrSemanticStateRejected) {
		t.Fatalf("EncodeSemanticState(non-tuple census) error = %v, want ErrSemanticStateRejected", err)
	}

	tuple := validWorkItemTupleSemanticState(t)
	tuple.WorkItemCensus = testWorkItemTupleCensus(WorkItemMembershipCensusExact, 1, 1, digest)
	if _, err := EncodeSemanticState(tuple); err != nil {
		t.Fatalf("EncodeSemanticState(tuple census) error = %v", err)
	}
}

func TestSemanticStateCopiesWorkItemCensusScope(t *testing.T) {
	t.Parallel()
	input := testWorkItemTupleCensus(WorkItemMembershipCensusExact, 1, 1, strings.Repeat("a", 64))
	state := BuildSemanticState(SemanticStateInput{WorkItemCensus: input})
	input.RequestedRepositoryScope[0] = "changed/input"
	if state.WorkItemCensus.RequestedRepositoryScope[0] == "changed/input" {
		t.Fatal("BuildSemanticState() aliased requested repository scope")
	}
	clone := cloneSemanticState(state)
	if clone == nil || clone.WorkItemCensus == nil {
		t.Fatal("cloneSemanticState() lost the work-item census")
	}
	clone.WorkItemCensus.RequestedRepositoryScope[0] = "changed/repository"
	if state.WorkItemCensus.RequestedRepositoryScope[0] == "changed/repository" {
		t.Fatal("cloneSemanticState() aliased requested repository scope")
	}
}

func testWorkItemTupleCensus(state WorkItemMembershipCensusState, value, retained int, digest string) *WorkItemTupleCensus {
	return &WorkItemTupleCensus{
		Version:                  WorkItemTupleCensusVersion,
		State:                    state,
		Value:                    value,
		Retained:                 retained,
		RequestedRepositoryScope: []string{"acme/api"},
		AuthorizationDigest:      digest,
	}
}

func validWorkItemTupleSemanticState(t testing.TB) *PersistedSemanticState {
	t.Helper()
	validation := ValidateFrame(QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{
				AnchorTerms: []string{"Project Alpha"},
				MemberKind:  SubjectWorkItem,
			},
		},
		Temporal: TemporalIntentCurrent,
	}, []AnswerObligation{ObligationState}, ShapeDiscoveredCohort)
	if validation.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("fixture frame invalid: %v", validation.Failure)
	}
	frame := validation.Frame
	state := BuildSemanticState(SemanticStateInput{
		Outcome: QuestionFamilyOutcome{
			Family:             QuestionFamilyScopedCohortStatus,
			Source:             QuestionFamilySourceModel,
			Frame:              &frame,
			Gate:               DecideFrameGate(validation, true),
			WinningSampleIndex: 0,
			WinningSample: FamilySample{
				ModelFamily:     QuestionFamilyScopedCohortStatus,
				ScopeAnchorKind: SubjectProject,
				ScopeAnchorTerm: "Project Alpha",
			},
		},
		EmittedShape:  ShapeDiscoveredCohort,
		FamilyVersion: QuestionFamilyTableVersion,
	})
	if _, err := EncodeSemanticState(state); err != nil {
		t.Fatalf("fixture semantic state invalid: %v", err)
	}
	return state
}
