package contextfabric

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestServeWorkItemTupleCensusExactRemovesOnlyWorkItemCensus(t *testing.T) {
	result := workItemTuplePayloadFixture(t)
	result.ClaimedFacts[2].Value.Integer = int64Pointer(37)
	result.Coverage = Coverage{
		Sources: []SourceObservation{},
		Partial: true,
		Details: []CoverageDetail{
			storedWorkItemCensusDetail(SubjectWorkItem, 2000, 1),
			storedWorkItemCensusDetail(SubjectRepository, 2000, 2),
		},
		DegradedReasons: []string{
			storedWorkItemCensusReason(SubjectWorkItem, 2000, 1),
			storedWorkItemCensusReason(SubjectRepository, 2000, 2),
		},
	}
	original := result
	principal := storage.Principal{RepositoryScopes: []string{"org/repo"}}
	census := validStoredWorkItemCensus(t, principal, WorkItemMembershipCensusExact, 3, 1, []string{"org/repo"})

	got := proofServeWorkItemTupleCensus(t, result, census)
	if got.Coverage.Partial != result.Coverage.Partial {
		t.Fatalf("exact census changed partial marker: got %v, want %v", got.Coverage.Partial, result.Coverage.Partial)
	}
	if got.Coverage.Details == nil || len(got.Coverage.Details) != 1 || got.Coverage.Details[0].Kind != SubjectRepository {
		t.Fatalf("exact census details = %#v, want only the unrelated repository detail", got.Coverage.Details)
	}
	if got.Coverage.DegradedReasons == nil || len(got.Coverage.DegradedReasons) != 1 || !strings.HasPrefix(got.Coverage.DegradedReasons[0], "kind_census_truncated:repository:") {
		t.Fatalf("exact census reasons = %#v, want only the unrelated repository reason", got.Coverage.DegradedReasons)
	}
	if got.ClaimedFacts[2].Value.Integer == nil || *got.ClaimedFacts[2].Value.Integer != 37 {
		t.Fatalf("exact census changed persisted cardinality claim: got %#v, want 37", got.ClaimedFacts[2].Value.Integer)
	}
	if len(result.Coverage.Details) != len(original.Coverage.Details) || len(result.Coverage.DegradedReasons) != len(original.Coverage.DegradedReasons) {
		t.Fatal("exact census mutated the input result")
	}
}

func TestServeWorkItemTupleCensusFloorReconstructsMissingAndNormalizesWorkItemDetail(t *testing.T) {
	result := workItemTuplePayloadFixture(t)
	result.Coverage = Coverage{
		Sources:         []SourceObservation{},
		Details:         []CoverageDetail{storedWorkItemCensusDetail(SubjectRepository, 42, 2)},
		DegradedReasons: []string{storedWorkItemCensusReason(SubjectRepository, 42, 2)},
	}
	principal := storage.Principal{RepositoryScopes: []string{"org/repo"}}
	census := validStoredWorkItemCensus(t, principal, WorkItemMembershipCensusFloor, WorkItemMembershipCensusLimit, 1, []string{"org/repo"})

	got := proofServeWorkItemTupleCensus(t, result, census)
	workItemDetails := workItemCensusDetails(got.Coverage.Details)
	if len(workItemDetails) != 1 {
		t.Fatalf("floor census work-item details = %#v, want one reconstructed detail", workItemDetails)
	}
	detail := workItemDetails[0]
	if detail.Declared == nil || *detail.Declared != WorkItemMembershipCensusLimit || detail.Served == nil || *detail.Served != len(result.Cohort.Members) {
		t.Fatalf("floor census detail declared/served = %#v/%#v, want %d/%d", detail.Declared, detail.Served, WorkItemMembershipCensusLimit, len(result.Cohort.Members))
	}
	wantReason := storedWorkItemCensusReason(SubjectWorkItem, WorkItemMembershipCensusLimit, len(result.Cohort.Members))
	if !containsExactString(got.Coverage.DegradedReasons, wantReason) {
		t.Fatalf("floor census reasons = %#v, want %q", got.Coverage.DegradedReasons, wantReason)
	}
	if !containsExactString(got.Coverage.DegradedReasons, storedWorkItemCensusReason(SubjectRepository, 42, 2)) {
		t.Fatalf("floor census dropped unrelated repository reason: %#v", got.Coverage.DegradedReasons)
	}
	if !got.Coverage.Partial {
		t.Fatal("floor census did not mark the served result partial")
	}
}

func TestServeWorkItemTupleCensusUnmeasuredDisclosesExistingLimitation(t *testing.T) {
	result := workItemTuplePayloadFixture(t)
	result.Limitations = []string{"The provider returned a stale status."}
	result.Coverage = Coverage{
		Sources:         []SourceObservation{},
		Partial:         true,
		Details:         []CoverageDetail{storedWorkItemCensusDetail(SubjectWorkItem, 2000, 1)},
		DegradedReasons: []string{storedWorkItemCensusReason(SubjectWorkItem, 2000, 1)},
	}
	principal := storage.Principal{RepositoryScopes: []string{"org/repo"}}
	census := validStoredWorkItemCensus(t, principal, WorkItemMembershipCensusUnmeasured, 0, 0, []string{"org/repo"})

	got := proofServeWorkItemTupleCensus(t, result, census)
	if len(workItemCensusDetails(got.Coverage.Details)) != 0 || containsPrefix(got.Coverage.DegradedReasons, "kind_census_truncated:work_item:") {
		t.Fatalf("unmeasured census served a work-item census: details=%#v reasons=%#v", got.Coverage.Details, got.Coverage.DegradedReasons)
	}
	if !containsExactString(got.Limitations, WorkItemMembershipLimitation()) {
		t.Fatalf("unmeasured census limitations = %#v, want existing membership limitation", got.Limitations)
	}
	if len(got.Cohort.Members) != len(result.Cohort.Members) {
		t.Fatalf("unmeasured census changed retained cohort cardinality: got %d, want %d", len(got.Cohort.Members), len(result.Cohort.Members))
	}
}

func TestServeStoredWorkItemTupleDigestMismatchIsNotFound(t *testing.T) {
	result := workItemTuplePayloadFixture(t)
	state := workItemTupleSemanticStateFixture()
	other := storage.Principal{RepositoryScopes: []string{"other/repo"}}
	census := validStoredWorkItemCensus(t, other, WorkItemMembershipCensusExact, 1, 1, []string{"other/repo"})
	state.WorkItemCensus = census

	got := proofServeStoredWorkItemTuple(t, result, state, SemanticStateReadAvailable, storage.Principal{RepositoryScopes: []string{"org/repo"}})
	if got.Disposition != WorkItemTupleByIDNotFound {
		t.Fatalf("digest mismatch disposition = %q, want not_found", got.Disposition)
	}
	if got.CensusRead != WorkItemTupleCensusReadAvailable {
		t.Fatalf("digest mismatch census read = %q, want available", got.CensusRead)
	}
}

func TestServeStoredWorkItemTupleUnavailableCensusUsesStoredFallback(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state *PersistedSemanticState
		read  SemanticStateReadStatus
		want  WorkItemTupleCensusReadStatus
	}{
		{name: "absent", state: workItemTupleSemanticStateFixture(), read: SemanticStateReadAvailable, want: WorkItemTupleCensusReadAbsent},
		{name: "malformed", state: workItemTupleSemanticStateFixture(), read: SemanticStateReadAvailable, want: WorkItemTupleCensusReadMalformed},
		{name: "unsupported", state: workItemTupleSemanticStateFixture(), read: SemanticStateReadAvailable, want: WorkItemTupleCensusReadUnsupportedVersion},
		{name: "unreadable semantic state", state: nil, read: SemanticStateReadMalformed, want: WorkItemTupleCensusReadAbsent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := workItemTuplePayloadFixture(t)
			result.Coverage = Coverage{
				Sources:         []SourceObservation{},
				Details:         []CoverageDetail{storedWorkItemCensusDetail(SubjectWorkItem, 2000, 1)},
				DegradedReasons: []string{storedWorkItemCensusReason(SubjectWorkItem, 2000, 1)},
			}
			if tc.name == "malformed" {
				tc.state.WorkItemCensus = &WorkItemTupleCensus{raw: []byte(`{"version":"work-item-census.v1","state":"floor"}`)}
			}
			if tc.name == "unsupported" {
				tc.state.WorkItemCensus = &WorkItemTupleCensus{raw: []byte(`{"version":"work-item-census.v2"}`)}
			}
			got := proofServeStoredWorkItemTuple(t, result, tc.state, tc.read, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}})
			if got.Disposition != WorkItemTupleByIDStored {
				t.Fatalf("disposition = %q, want stored", got.Disposition)
			}
			if got.CensusRead != tc.want {
				t.Fatalf("census read = %q, want %q", got.CensusRead, tc.want)
			}
			if len(workItemCensusDetails(got.Result.Coverage.Details)) != 0 || containsPrefix(got.Result.Coverage.DegradedReasons, "kind_census_truncated:work_item:") {
				t.Fatalf("fallback served a census detail: details=%#v reasons=%#v", got.Result.Coverage.Details, got.Result.Coverage.DegradedReasons)
			}
			if !containsExactString(got.Result.Limitations, WorkItemMembershipLimitation()) {
				t.Fatalf("fallback limitations = %#v, want membership limitation", got.Result.Limitations)
			}
		})
	}
}

func TestServeStoredWorkItemTupleDoesNotHandleOtherResult(t *testing.T) {
	result := InvestigationResult{ResultID: "ordinary-result"}
	got := proofServeStoredWorkItemTuple(t, result, nil, SemanticStateReadAbsent, storage.Principal{})
	if got.Disposition != WorkItemTupleByIDNotApplicable {
		t.Fatalf("ordinary result disposition = %q, want not_applicable", got.Disposition)
	}
	if !reflect.DeepEqual(got.Result, result) {
		t.Fatalf("ordinary result changed: got %#v, want %#v", got.Result, result)
	}
}

func validStoredWorkItemCensus(t *testing.T, principal storage.Principal, state WorkItemMembershipCensusState, value, retained int, requested []string) *WorkItemTupleCensus {
	t.Helper()
	digest, err := WorkItemAuthorizationDigest(principal, requested)
	if err != nil {
		t.Fatalf("WorkItemAuthorizationDigest() error = %v", err)
	}
	return &WorkItemTupleCensus{
		Version: WorkItemTupleCensusVersion, State: state, Value: value, Retained: retained,
		RequestedRepositoryScope: append([]string(nil), requested...), AuthorizationDigest: digest,
	}
}

func storedWorkItemCensusDetail(kind SubjectKind, declared, served int) CoverageDetail {
	detail := CoverageDetail{
		DetailID: "cov-01", Source: "context-fabric:graph", Code: contractsv1.ContextFabricCoverageDetailKindCensusTruncated,
		Degrading: true, Kind: kind, Declared: intPointer(declared), Served: intPointer(served),
		Raw: storedWorkItemCensusReason(kind, declared, served),
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	return detail
}

func storedWorkItemCensusReason(kind SubjectKind, declared, served int) string {
	return fmt.Sprintf("kind_census_truncated:%s:%d:%d", kind, declared, served)
}

func workItemCensusDetails(details []CoverageDetail) []CoverageDetail {
	filtered := make([]CoverageDetail, 0, len(details))
	for _, detail := range details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated && detail.Kind == SubjectWorkItem {
			filtered = append(filtered, detail)
		}
	}
	return filtered
}

func containsExactString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func intPointer(value int) *int { return &value }

func int64Pointer(value int64) *int64 { return &value }
