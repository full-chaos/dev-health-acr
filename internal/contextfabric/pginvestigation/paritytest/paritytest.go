// Package paritytest is the single, shared table of
// contextfabric.InvestigationResultStore behavior scenarios. Both
// pginvestigation.Store and memoryinvestigation.Store run this exact same
// table (see pginvestigation's *_integration_test.go and
// memoryinvestigation's store_test.go) so their observable behavior cannot
// silently drift apart -- a change to one implementation that breaks parity
// fails here regardless of which package the change was made in.
//
// This lives under pginvestigation/ (rather than a third top-level
// package) so it stays within the CHAOS-3755 change boundary
// (pginvestigation/** and memoryinvestigation/**).
package paritytest

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// SaveStep is one Save call exercised as part of a Case, in order.
type SaveStep struct {
	Principal storage.Principal
	Result    contextfabric.InvestigationResult
	WantErr   bool
}

// GetStep is the Get call a Case verifies after its Save steps.
type GetStep struct {
	Principal    storage.Principal
	ResultID     string
	WantNotFound bool
	Want         *contextfabric.InvestigationResult
}

// Case is one save/get parity scenario.
type Case struct {
	Name string
	Save []SaveStep
	Get  GetStep
}

var (
	orgA = storage.Principal{OrgID: "org-parity-a"}
	orgB = storage.Principal{OrgID: "org-parity-b"}

	generatedAt = time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)
)

func result(resultID, question string) contextfabric.InvestigationResult {
	return contextfabric.InvestigationResult{
		SchemaVersion:       contextfabric.InvestigationResultSchemaV1,
		ResultID:            resultID,
		RequestID:           "request-" + resultID,
		GeneratedAt:         generatedAt,
		Status:              contextfabric.InvestigationComplete,
		Question:            question,
		DirectJudgment:      "judgment for " + question,
		DeterministicAnswer: "deterministic answer for " + question,
		StrongestPressures:  []string{},
		Drivers:             []contextfabric.DriverJudgment{},
		RemainingWork:       []contextfabric.Finding{},
		ReadinessGaps:       []contextfabric.Finding{},
		Paths:               []contextfabric.RelationshipPath{},
		Conflicts:           []contextfabric.Finding{},
		Limitations:         []string{},
		EvidenceRefIDs:      []string{},
		Warnings:            []string{},
	}
}

// Cases is the shared parity table: save+get roundtrip, cross-org
// not-found, unknown-id not-found, idempotent identical replay, and
// rejected divergent replay.
func Cases() []Case {
	roundTrip := result("result-id-roundtrip", "what shipped this week?")
	crossOrg := result("result-id-cross-org", "who owns this service?")
	identicalA := result("result-id-idempotent", "is the rollout healthy?")
	identicalB := identicalA
	original := result("result-id-immutable", "why did the deploy fail?")
	divergent := original
	divergent.DirectJudgment = "a different judgment"
	divergent.Question = "why did the deploy fail? (mutated)"

	return []Case{
		{
			Name: "save and get round trip",
			Save: []SaveStep{{Principal: orgA, Result: roundTrip}},
			Get:  GetStep{Principal: orgA, ResultID: roundTrip.ResultID, Want: &roundTrip},
		},
		{
			Name: "get scoped to a different org returns not found",
			Save: []SaveStep{{Principal: orgA, Result: crossOrg}},
			Get:  GetStep{Principal: orgB, ResultID: crossOrg.ResultID, WantNotFound: true},
		},
		{
			Name: "get unknown result id returns not found",
			Get:  GetStep{Principal: orgA, ResultID: "unknown-result-id-00000000", WantNotFound: true},
		},
		{
			Name: "save same result id twice with identical payload is idempotent",
			Save: []SaveStep{
				{Principal: orgA, Result: identicalA},
				{Principal: orgA, Result: identicalB},
			},
			Get: GetStep{Principal: orgA, ResultID: identicalA.ResultID, Want: &identicalA},
		},
		{
			Name: "save same result id twice with different payload errors and leaves the original intact",
			Save: []SaveStep{
				{Principal: orgA, Result: original},
				{Principal: orgA, Result: divergent, WantErr: true},
			},
			Get: GetStep{Principal: orgA, ResultID: original.ResultID, Want: &original},
		},
	}
}

// RunSuite runs Cases() against a fresh store per case. newStore must
// return a clean, empty store (or an equivalent independent scope) each
// call. isNotFound classifies a Get error as the implementation's
// not-found sentinel; the two implementations intentionally define their
// own local sentinel (pginvestigation.ErrNotFound,
// memoryinvestigation.ErrNotFound) rather than sharing one, so callers
// supply their own predicate.
func RunSuite(t *testing.T, newStore func(t *testing.T) contextfabric.InvestigationResultStore, isNotFound func(error) bool) {
	t.Helper()
	for _, testCase := range Cases() {
		t.Run(testCase.Name, func(t *testing.T) {
			store := newStore(t)
			ctx := context.Background()

			for index, step := range testCase.Save {
				err := store.Save(ctx, step.Principal, step.Result)
				if step.WantErr {
					if err == nil {
						t.Fatalf("save[%d] %q: want error, got nil", index, step.Result.ResultID)
					}
					continue
				}
				if err != nil {
					t.Fatalf("save[%d] %q: unexpected error: %v", index, step.Result.ResultID, err)
				}
			}

			got, err := store.Get(ctx, testCase.Get.Principal, testCase.Get.ResultID)
			if testCase.Get.WantNotFound {
				if !isNotFound(err) {
					t.Fatalf("get %q: want not-found error, got %v", testCase.Get.ResultID, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("get %q: unexpected error: %v", testCase.Get.ResultID, err)
			}
			if testCase.Get.Want == nil {
				return
			}
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal got result: %v", err)
			}
			wantJSON, err := json.Marshal(*testCase.Get.Want)
			if err != nil {
				t.Fatalf("marshal want result: %v", err)
			}
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Fatalf("get %q: result mismatch\n got: %s\nwant: %s", testCase.Get.ResultID, gotJSON, wantJSON)
			}
		})
	}
}
