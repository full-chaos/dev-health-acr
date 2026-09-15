package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func proofServeWorkItemTupleCensus(t *testing.T, candidate InvestigationResult, census *WorkItemTupleCensus) (got InvestigationResult) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("ServeWorkItemTupleCensus panicked instead of returning a served result: %v", recovered)
		}
	}()
	return ServeWorkItemTupleCensus(candidate, census)
}

func proofServeStoredWorkItemTuple(t *testing.T, candidate InvestigationResult, state *PersistedSemanticState, read SemanticStateReadStatus, principal storage.Principal) (got WorkItemTupleByIDDecision) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("ServeStoredWorkItemTuple panicked instead of returning a serving decision: %v", recovered)
		}
	}()
	got = ServeStoredWorkItemTuple(candidate, state, read, principal)
	if got.Err != nil {
		t.Fatalf("unexpected stored coverage error: %v", got.Err)
	}
	return got
}

func TestProofFloorServingNormalizesExistingDetailAndOrdering(t *testing.T) {
	principal := storage.Principal{RepositoryScopes: []string{"org/repo"}}
	census := validStoredWorkItemCensus(t, principal, WorkItemMembershipCensusFloor, WorkItemMembershipCensusLimit, 1, []string{"org/repo"})

	t.Run("preserves identity and inserts before nondegrading detail", func(t *testing.T) {
		result := workItemTuplePayloadFixture(t)
		nondegrading := storedWorkItemCensusDetail(SubjectRepository, 42, 2)
		nondegrading.Degrading = false
		nondegrading.Raw = "a"
		existing := storedWorkItemCensusDetail(SubjectWorkItem, 11, 0)
		existing.DetailID = "cov-09"
		result.Coverage.Details = []CoverageDetail{nondegrading, existing}

		got := proofServeWorkItemTupleCensus(t, result, census)
		if len(got.Coverage.Details) != 2 || got.Coverage.Details[0].Kind != SubjectWorkItem {
			t.Fatalf("details = %#v, want work-item detail first", got.Coverage.Details)
		}
		detail := got.Coverage.Details[0]
		if detail.DetailID != "cov-09" {
			t.Fatalf("detail id = %q, want preserved cov-09", detail.DetailID)
		}
		if detail.Declared == nil || *detail.Declared != WorkItemMembershipCensusLimit || detail.Served == nil || *detail.Served != len(result.Cohort.Members) {
			t.Fatalf("declared/served = %v/%v, want %d/%d", detail.Declared, detail.Served, WorkItemMembershipCensusLimit, len(result.Cohort.Members))
		}
		wantRaw := storedWorkItemCensusReason(SubjectWorkItem, WorkItemMembershipCensusLimit, len(result.Cohort.Members))
		if detail.Raw != wantRaw || detail.Label != contractsv1.ComposeCoverageDetailLabel(detail) {
			t.Fatalf("detail raw/label = %q/%q, want raw %q and composed label", detail.Raw, detail.Label, wantRaw)
		}
	})

	t.Run("sorts degrading detail by raw value", func(t *testing.T) {
		result := workItemTuplePayloadFixture(t)
		later := storedWorkItemCensusDetail(SubjectRepository, 42, 2)
		later.Raw = "z"
		result.Coverage.Details = []CoverageDetail{later}

		got := proofServeWorkItemTupleCensus(t, result, census)
		if len(got.Coverage.Details) != 2 || got.Coverage.Details[0].Kind != SubjectWorkItem {
			t.Fatalf("details = %#v, want work-item detail ordered before raw z", got.Coverage.Details)
		}
	})

	t.Run("allocates a fresh detail id", func(t *testing.T) {
		result := workItemTuplePayloadFixture(t)
		result.Coverage.Details = []CoverageDetail{storedWorkItemCensusDetail(SubjectRepository, 42, 2)}

		got := proofServeWorkItemTupleCensus(t, result, census)
		if len(got.Coverage.Details) != 2 || got.Coverage.Details[1].DetailID != "cov-02" {
			t.Fatalf("details = %#v, want generated cov-02 id", got.Coverage.Details)
		}
	})
}

func TestProofUnmeasuredAndUnavailableFallbackPreserveExistingLimitation(t *testing.T) {
	result := workItemTuplePayloadFixture(t)
	result.Limitations = []string{"provider returned a stale status"}
	result.Coverage = Coverage{
		Sources:         []SourceObservation{},
		Details:         []CoverageDetail{storedWorkItemCensusDetail(SubjectWorkItem, 2000, 1)},
		DegradedReasons: []string{storedWorkItemCensusReason(SubjectWorkItem, 2000, 1)},
	}
	principal := storage.Principal{RepositoryScopes: []string{"org/repo"}}
	unmeasured := validStoredWorkItemCensus(t, principal, WorkItemMembershipCensusUnmeasured, 0, 0, []string{"org/repo"})

	for _, testCase := range []struct {
		name string
		got  InvestigationResult
	}{
		{name: "unmeasured", got: proofServeWorkItemTupleCensus(t, result, unmeasured)},
		{name: "unavailable", got: proofServeStoredWorkItemTuple(t, result, workItemTupleSemanticStateFixture(), SemanticStateReadAvailable, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}).Result},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if !containsExactString(testCase.got.Limitations, "provider returned a stale status") || !containsExactString(testCase.got.Limitations, WorkItemMembershipLimitation()) {
				t.Fatalf("limitations = %#v, want existing and membership disclosures", testCase.got.Limitations)
			}
			if len(workItemCensusDetails(testCase.got.Coverage.Details)) != 0 || containsPrefix(testCase.got.Coverage.DegradedReasons, "kind_census_truncated:work_item:") {
				t.Fatalf("fallback retained work-item census: details=%#v reasons=%#v", testCase.got.Coverage.Details, testCase.got.Coverage.DegradedReasons)
			}
		})
	}

}

func TestProofStoredAvailableCensusUsesFinalServingTransformation(t *testing.T) {
	result := workItemTuplePayloadFixture(t)
	result.Coverage.Sources = []SourceObservation{}
	result.Coverage.Details = []CoverageDetail{storedWorkItemCensusDetail(SubjectRepository, 42, 2)}
	result.Coverage.DegradedReasons = []string{storedWorkItemCensusReason(SubjectRepository, 42, 2)}
	state := workItemTupleSemanticStateFixture()
	principal := storage.Principal{RepositoryScopes: []string{"org/repo"}}
	state.WorkItemCensus = validStoredWorkItemCensus(t, principal, WorkItemMembershipCensusFloor, WorkItemMembershipCensusLimit, 1, []string{"org/repo"})

	got := proofServeStoredWorkItemTuple(t, result, state, SemanticStateReadAvailable, principal)
	if got.Disposition != WorkItemTupleByIDServed || got.CensusRead != WorkItemTupleCensusReadAvailable {
		t.Fatalf("decision = %q/%q, want served/available", got.Disposition, got.CensusRead)
	}
	workItemDetails := workItemCensusDetails(got.Result.Coverage.Details)
	if len(workItemDetails) != 1 || workItemDetails[0].Declared == nil || *workItemDetails[0].Declared != WorkItemMembershipCensusLimit || workItemDetails[0].Served == nil || *workItemDetails[0].Served != len(result.Cohort.Members) {
		t.Fatalf("served details = %#v, want reconstructed floor detail", workItemDetails)
	}

	exactResult := workItemTuplePayloadFixture(t)
	exactResult.Coverage.Details = []CoverageDetail{storedWorkItemCensusDetail(SubjectWorkItem, 2000, 1)}
	exactResult.Coverage.DegradedReasons = []string{storedWorkItemCensusReason(SubjectWorkItem, 2000, 1)}
	exactCensus := validStoredWorkItemCensus(t, principal, WorkItemMembershipCensusExact, 1, 1, []string{"org/repo"})
	exactGot := proofServeWorkItemTupleCensus(t, exactResult, exactCensus)
	if containsExactString(exactGot.Limitations, WorkItemMembershipLimitation()) || len(workItemCensusDetails(exactGot.Coverage.Details)) != 0 {
		t.Fatalf("exact serving changed into an unmeasured fallback: limitations=%#v details=%#v", exactGot.Limitations, exactGot.Coverage.Details)
	}
}
