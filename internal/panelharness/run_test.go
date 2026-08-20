package panelharness

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// stubSelector always returns a fixed set of selections, or a fixed error.
type stubSelector struct {
	selections map[string]string
	err        error
}

func (s stubSelector) SelectReceipts(context.Context, string, contractsv1.ContextFabricStructureNeeds) (map[string]string, error) {
	return s.selections, s.err
}

// newTwoTurnServer builds an httptest server simulating one panelist's
// hosted API: turn 1 always returns a clarification_required result naming
// receiptID as the sole expected_kind offer; turn 2 returns a decisive
// result whose ConfirmedStructure applies (or vetoes, per applyDisposition)
// whichever receipt turn 2's own request carried.
func newTwoTurnServer(t *testing.T, receiptID, appliedValue string, disposition contractsv1.ContextFabricStructureDisposition) *httptest.Server {
	t.Helper()
	turn := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request contractsv1.ContextFabricInvestigationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		turn++
		w.Header().Set("Content-Type", "application/json")
		if turn == 1 {
			_ = json.NewEncoder(w).Encode(contractsv1.ContextFabricInvestigationResult{
				SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
				ResultID:      "result_turn1", RequestID: request.RequestID,
				Status: contractsv1.ContextFabricInvestigationClarificationRequired,
				StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
					Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
					KindOptions: []contractsv1.ContextFabricKindOption{
						{ReceiptID: receiptID, OptionID: "opt1", Label: "Pull request", Kind: "pull_request"},
					},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(contractsv1.ContextFabricInvestigationResult{
			SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
			ResultID:      "result_turn2", RequestID: request.RequestID,
			Status: contractsv1.ContextFabricInvestigationComplete,
			ConfirmedStructure: []contractsv1.ContextFabricConfirmedStructureEntry{
				{
					Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: appliedValue,
					Source: contractsv1.ContextFabricStructureSourceReceipt, ReceiptID: receiptID,
					Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: disposition,
				},
			},
		})
	}))
}

func newTestPanelist(t *testing.T, identity string, server *httptest.Server, selections map[string]string, selectErr error) Panelist {
	t.Helper()
	client, err := NewClient(server.URL, "test-token-"+identity, 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return Panelist{CanonicalModelIdentity: identity, Client: client, Selector: stubSelector{selections: selections, err: selectErr}}
}

func TestRun_TwoTurnFlowProducesAppliedSelectionInManifest(t *testing.T) {
	server := newTwoTurnServer(t, "kindr_receipt00000000000001", "pull_request", contractsv1.ContextFabricStructureDispositionApplied)
	defer server.Close()

	panelist := newTestPanelist(t, "anthropic/sol-max", server, map[string]string{"expected_kind": "kindr_receipt00000000000001"}, nil)
	manifest, err := Run(context.Background(), RunConfig{
		OrgID: "org-test", Question: "Was Ask Dev ready to ship?",
		Panelists:   []Panelist{panelist},
		BaseRequest: validRequest(),
		Now:         func() time.Time { return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if manifest.SchemaVersion != ManifestSchemaVersion {
		t.Errorf("SchemaVersion = %q, want %q", manifest.SchemaVersion, ManifestSchemaVersion)
	}
	if manifest.OrgID != "org-test" {
		t.Errorf("OrgID = %q, want org-test", manifest.OrgID)
	}
	if len(manifest.Members) != 1 {
		t.Fatalf("Members = %v, want exactly one member", manifest.Members)
	}
	member := manifest.Members[0]
	if member.Member != "expected_kind" {
		t.Errorf("Member = %q, want expected_kind", member.Member)
	}
	if !member.Complete {
		t.Error("Complete = false, want true (the one configured panelist produced a selection)")
	}
	if len(member.Panelists) != 1 || member.Panelists[0].AppliedValue != "pull_request" {
		t.Errorf("Panelists = %+v, want one entry with AppliedValue pull_request", member.Panelists)
	}
	if member.Panelists[0].ConfirmedResultID != "result_turn2" {
		t.Errorf("ConfirmedResultID = %q, want result_turn2", member.Panelists[0].ConfirmedResultID)
	}
}

func TestRun_VetoedTurn2ConfirmationContributesNoSelection(t *testing.T) {
	server := newTwoTurnServer(t, "kindr_receipt00000000000001", "pull_request", contractsv1.ContextFabricStructureDispositionVetoedConflict)
	defer server.Close()

	panelist := newTestPanelist(t, "anthropic/sol-max", server, map[string]string{"expected_kind": "kindr_receipt00000000000001"}, nil)
	manifest, err := Run(context.Background(), RunConfig{
		OrgID: "org-test", Question: "Was Ask Dev ready to ship?",
		Panelists: []Panelist{panelist}, BaseRequest: validRequest(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(manifest.Members) != 0 {
		t.Errorf("Members = %v, want none: a vetoed confirmation must never appear as a landed selection", manifest.Members)
	}
}

func TestRun_PanelistErrorDoesNotFailWholeRun(t *testing.T) {
	// Two INDEPENDENT servers, one per panelist: newTwoTurnServer's turn
	// counter is per-server state keyed on request ARRIVAL ORDER, not on
	// which panelist sent it -- sharing one server between two panelists
	// running concurrently would let the failing panelist's own turn-1
	// call consume the "turn 1" response meant for the succeeding
	// panelist (or vice versa), a test-harness race having nothing to do
	// with the behavior under test.
	goodServer := newTwoTurnServer(t, "kindr_receipt00000000000001", "pull_request", contractsv1.ContextFabricStructureDispositionApplied)
	defer goodServer.Close()
	failingServer := newTwoTurnServer(t, "kindr_receipt00000000000002", "pull_request", contractsv1.ContextFabricStructureDispositionApplied)
	defer failingServer.Close()

	good := newTestPanelist(t, "anthropic/sol-max", goodServer, map[string]string{"expected_kind": "kindr_receipt00000000000001"}, nil)
	failing := newTestPanelist(t, "anthropic/luna", failingServer, nil, errors.New("selector unavailable"))

	manifest, err := Run(context.Background(), RunConfig{
		OrgID: "org-test", Question: "Was Ask Dev ready to ship?",
		Panelists: []Panelist{good, failing}, BaseRequest: validRequest(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(manifest.Members) != 1 {
		t.Fatalf("Members = %v, want one member (from the succeeding panelist)", manifest.Members)
	}
	if manifest.Members[0].Complete {
		t.Error("Complete = true, want false: only one of two configured panelists actually produced a selection")
	}
	if len(manifest.Members[0].Panelists) != 1 {
		t.Errorf("Panelists = %v, want exactly one entry (the failing panelist contributes none, never a placeholder)", manifest.Members[0].Panelists)
	}
}

func TestRun_DecisiveTurn1ContributesNoSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request contractsv1.ContextFabricInvestigationRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(contractsv1.ContextFabricInvestigationResult{
			SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
			ResultID:      "result_decisive", RequestID: request.RequestID,
			Status: contractsv1.ContextFabricInvestigationComplete, // no StructureNeeds at all
		})
	}))
	defer server.Close()

	panelist := newTestPanelist(t, "anthropic/sol-max", server, map[string]string{"expected_kind": "kindr_should_never_be_used"}, nil)
	manifest, err := Run(context.Background(), RunConfig{
		OrgID: "org-test", Question: "Was Ask Dev ready to ship?",
		Panelists: []Panelist{panelist}, BaseRequest: validRequest(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(manifest.Members) != 0 {
		t.Errorf("Members = %v, want none: a decisive turn-1 result has nothing to confirm", manifest.Members)
	}
}

func TestRun_RequiresAtLeastOnePanelist(t *testing.T) {
	_, err := Run(context.Background(), RunConfig{OrgID: "org-test", Question: "q"})
	if err == nil {
		t.Fatal("expected an error for zero configured panelists")
	}
}
