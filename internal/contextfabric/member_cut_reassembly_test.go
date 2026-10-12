package contextfabric

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The seam the member cut stands on: assembling the pass-1 raw draft over the
// SAME cohort, with no model call, produces the document pass 1 produced. If
// this holds, a cut cohort's document is the one assembly over that cohort and
// not a patched one.
func TestReassemblingThePassOneDraftOverTheSameCohortIsTheSameDocument(t *testing.T) {
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := budgetStageEngine(t, budgetStageCohort(6), 2, budgetStageOptions(0, 0), &calls, telemetry)
	type pass struct {
		params  synthesisAssemblyParams
		result  InvestigationResult
		pending assemblyTelemetry
	}
	var first *pass
	engine.assemblyObserver = func(params synthesisAssemblyParams, result InvestigationResult, pending assemblyTelemetry) {
		if first == nil {
			first = &pass{params: params, result: copyResultDeep(result), pending: pending}
		}
	}
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if first == nil || first.pending.Draft == nil {
		t.Fatal("pass one kept no draft")
	}
	if calls != 1 {
		t.Fatalf("synthesizer calls = %d, want 1", calls)
	}
	reassembled := first.params
	reassembled.Draft = first.pending.Draft
	draftBefore := copyResultDeep(*first.pending.Draft)
	engine.assemblyObserver = nil
	again, _, _, _, err := engine.synthesizeAndAssemble(context.Background(), storage.Principal{OrgID: "org_1"}, reassembled)
	if err != nil {
		t.Fatalf("re-assembly error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("re-assembly called the model: calls = %d", calls)
	}
	// Assembling does not write through to the draft: a second candidate
	// cohort is assembled from the same draft the first one was.
	if !reflect.DeepEqual(draftBefore, *first.pending.Draft) {
		t.Fatal("assembly wrote to the kept draft")
	}
	twice, _, _, _, err := engine.synthesizeAndAssemble(context.Background(), storage.Principal{OrgID: "org_1"}, reassembled)
	if err != nil || !reflect.DeepEqual(again, twice) {
		t.Fatalf("a second assembly of the same draft differs (err %v)", err)
	}
	if !reflect.DeepEqual(first.result, again) {
		t.Fatalf("re-assembly over the same cohort differs from pass one:\nfirst: %+v\nagain: %+v", first.result, again)
	}
}

// The copy shares nothing with its source and keeps nil apart from empty.
func TestCopyResultDeepSharesNothingAndKeepsNilApartFromEmpty(t *testing.T) {
	value := int64(7)
	source := InvestigationResult{
		Limitations:  make([]string, 1, 8),
		Warnings:     []string{},
		ClaimedFacts: []ClaimedFact{{ClaimID: "claim_a", Value: ScalarValue{Integer: &value}}},
		Cohort:       &Cohort{Members: []CohortMember{{Rank: 1, InclusionReasons: []string{"a"}}}},
	}
	source.Limitations[0] = "kept"
	copied := copyResultDeep(source)
	if copied.Warnings == nil || len(copied.Warnings) != 0 || copied.Drivers != nil {
		t.Fatalf("empty/nil not preserved: warnings %#v drivers %#v", copied.Warnings, copied.Drivers)
	}
	copied.Limitations[0] = "changed"
	copied.Limitations = append(copied.Limitations[:1], "appended")
	*copied.ClaimedFacts[0].Value.Integer = 9
	copied.Cohort.Members[0].InclusionReasons[0] = "changed"
	if source.Limitations[0] != "kept" || source.Limitations[:2][1] != "" || value != 7 || source.Cohort.Members[0].InclusionReasons[0] != "a" {
		t.Fatalf("the copy wrote through to its source: %+v value=%d", source.Limitations[:2], value)
	}
}
