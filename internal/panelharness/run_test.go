package panelharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
			result := minimalValidResult("result_turn1", request.RequestID)
			result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
			result.SubjectResolution.ClarificationPrompt = "Which kind of work item is this about?"
			result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
				Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
				KindOptions: []contractsv1.ContextFabricKindOption{
					{ReceiptID: receiptID, OptionID: "opt1", Label: "Pull request", Kind: "pull_request", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
				},
			}
			_ = json.NewEncoder(w).Encode(result)
			return
		}
		result := minimalValidResult("result_turn2", request.RequestID)
		result.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
			{
				Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: appliedValue,
				Source: contractsv1.ContextFabricStructureSourceReceipt, PriorResultID: "result_turn1", ReceiptID: receiptID,
				Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: disposition,
			},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
}

func newTestPanelist(t *testing.T, identity string, server *httptest.Server, selections map[string]string, selectErr error) Panelist {
	t.Helper()
	// Each identity gets its OWN shape-valid token (derived deterministically
	// from identity, so repeated calls with the same identity are stable):
	// distinct tokens matter once duplicate-credential rejection exists,
	// and every existing test here uses a distinct CanonicalModelIdentity
	// per panelist already.
	var discriminator byte
	for i := 0; i < len(identity); i++ {
		discriminator += identity[i]
	}
	client, err := NewClient(server.URL, testBearerToken(discriminator), 5*time.Second)
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
	if !member.Panelists[0].Accepted {
		t.Error("Accepted = false, want true: the redeemed receipt was turn 1's only (therefore rank-0/top-ranked) offer")
	}
}

// TestRun_AcceptedReflectsOfferRankNotProvenance is a regression test for a
// real bug caught during review: Accepted must report whether the redeemed
// receipt was the rank-0 (top-ranked) offer turn 1 presented, NOT whether
// its confirmation provenance was clarification_confirmed -- every
// successfully redeemed structure receipt on this flow carries that same
// provenance regardless of rank, so a provenance-based check is always
// true and never actually distinguishes "confirmed the engine's leading
// proposal" from "overrode it with a lower-ranked alternative." This test
// offers TWO kind options and has the panelist choose the SECOND
// (rank 1) one, asserting Accepted is false.
func TestRun_AcceptedReflectsOfferRankNotProvenance(t *testing.T) {
	turn := 0
	const topRanked, chosen = "kindr_top_ranked_00000000", "kindr_second_choice_0000"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request contractsv1.ContextFabricInvestigationRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		turn++
		w.Header().Set("Content-Type", "application/json")
		if turn == 1 {
			result := minimalValidResult("result_turn1", request.RequestID)
			result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
			result.SubjectResolution.ClarificationPrompt = "Which kind of work item is this about?"
			result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
				Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
				KindOptions: []contractsv1.ContextFabricKindOption{
					{ReceiptID: topRanked, OptionID: "opt1", Label: "Pull request", Kind: "pull_request", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
					{ReceiptID: chosen, OptionID: "opt2", Label: "Work item", Kind: "work_item", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
				},
			}
			_ = json.NewEncoder(w).Encode(result)
			return
		}
		result := minimalValidResult("result_turn2", request.RequestID)
		result.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
			{
				Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: "work_item",
				Source: contractsv1.ContextFabricStructureSourceReceipt, PriorResultID: "result_turn1", ReceiptID: chosen,
				Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
			},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	panelist := newTestPanelist(t, "anthropic/sol-max", server, map[string]string{"expected_kind": chosen}, nil)
	manifest, err := Run(context.Background(), RunConfig{
		OrgID: "org-test", Question: "Was Ask Dev ready to ship?",
		Panelists: []Panelist{panelist}, BaseRequest: validRequest(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(manifest.Members) != 1 || len(manifest.Members[0].Panelists) != 1 {
		t.Fatalf("Members = %+v, want one member with one panelist", manifest.Members)
	}
	if manifest.Members[0].Panelists[0].Accepted {
		t.Error("Accepted = true, want false: the panelist confirmed the SECOND (rank 1) offer, overriding the engine's own top-ranked proposal -- provenance alone (clarification_confirmed either way) must not report this as accepted")
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

// failIfCalledSelector proves SelectReceipts is never invoked -- a stronger
// assertion than stubSelector's own fixed-response shape, which cannot tell
// "never called" from "called and returned an empty map" the way this can.
type failIfCalledSelector struct {
	t *testing.T
}

func (s failIfCalledSelector) SelectReceipts(context.Context, string, contractsv1.ContextFabricStructureNeeds) (map[string]string, error) {
	s.t.Helper()
	s.t.Fatal("SelectReceipts called, want it never invoked: this package's select-and-continue flow has no offers to project for a window-only StructureNeeds")
	return nil, nil
}

// TestRun_WindowOnlyStructureNeedsContributesNoSelection is a regression test
// for CHAOS-4118 (codex xhigh review round 1, confirmed real): windowConfirmationRequiredResult
// (contextfabric/window.go) now composes a window-only StructureNeeds
// (Missing=["window"], WindowOptions only, zero Kind/Anchor/HandleOptions)
// on every turn-1 window-gated response. Window rides its own, separately
// designed WindowSelectionEvent path and is deliberately excluded from
// projectOffers (selector.go's own doc comment) -- before this ticket's
// panelharness guard fix, runPanelist's own len(Missing)==0 check would no
// longer short-circuit here, and it would instead call SelectReceipts with
// zero projected offers: a real, pointless file-exchange round trip against
// an external responder for a member this flow can never resolve. Pins that
// SelectReceipts is never even invoked for this case.
func TestRun_WindowOnlyStructureNeedsContributesNoSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request contractsv1.ContextFabricInvestigationRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")
		result := minimalValidResult("result_window_gated", request.RequestID)
		result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
		result.SubjectResolution.ClarificationPrompt = "Confirm the evidence window for this answer."
		windowEnd := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
		windowStart := windowEnd.Add(-90 * 24 * time.Hour)
		result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
			WindowOptions: []contractsv1.ContextFabricWindowOption{
				{ReceiptID: "winr_receipt00000000000001", OptionID: "opt_win1", Label: "the last 90 days", RelativeID: "trailing_90d", Start: &windowStart, End: &windowEnd},
			},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, testBearerToken('w'), 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	panelist := Panelist{CanonicalModelIdentity: "anthropic/sol-max", Client: client, Selector: failIfCalledSelector{t: t}}

	manifest, err := Run(context.Background(), RunConfig{
		OrgID: "org-test", Question: "Was Ask Dev ready to ship?",
		Panelists: []Panelist{panelist}, BaseRequest: validRequest(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(manifest.Members) != 0 {
		t.Errorf("Members = %v, want none: a window-only StructureNeeds has nothing this flow can confirm", manifest.Members)
	}
}

func TestRun_DecisiveTurn1ContributesNoSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request contractsv1.ContextFabricInvestigationRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(minimalValidResult("result_decisive", request.RequestID)) // Status complete, no StructureNeeds at all
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

// TestFindConfirmedEntry_RequiresPriorResultIDToMatchNotJustReceiptID is a
// regression test for the (PriorResultID, ReceiptID) tuple-matching rule:
// an entry that matches member and receipt but names a DIFFERENT prior
// result ID must NOT be treated as confirming this panelist's own turn-1
// offer -- a receipt ID can be reused/coincide across unrelated prior
// results, so matching on ReceiptID alone would let another result's
// confirmation be misattributed here.
func TestFindConfirmedEntry_RequiresPriorResultIDToMatchNotJustReceiptID(t *testing.T) {
	const member = "expected_kind"
	const receiptID = "kindr_receipt00000000000001"
	confirmed := []contractsv1.ContextFabricConfirmedStructureEntry{
		{
			Member: member, PriorResultID: "result_from_a_different_turn", ReceiptID: receiptID,
			Source: contractsv1.ContextFabricStructureSourceReceipt, AppliedValue: "pull_request",
		},
	}

	if _, ok := findConfirmedEntry(confirmed, member, "result_turn1", receiptID); ok {
		t.Error("expected no match when PriorResultID differs, even though Member and ReceiptID both match")
	}
	if entry, ok := findConfirmedEntry(confirmed, member, "result_from_a_different_turn", receiptID); !ok || entry.AppliedValue != "pull_request" {
		t.Errorf("expected a match on the correct PriorResultID tuple, got ok=%v entry=%+v", ok, entry)
	}
}

// autoSelectTopRanked is a Selector that, for every member StructureNeeds
// currently offers, always picks that member's rank-0 (first-listed) offer
// -- unlike stubSelector's fixed map, it reads the needs argument, so it
// works correctly across a multi-turn chain where each turn offers
// different members and different receipt ids.
type autoSelectTopRanked struct{}

func (autoSelectTopRanked) SelectReceipts(_ context.Context, _ string, needs contractsv1.ContextFabricStructureNeeds) (map[string]string, error) {
	selections := make(map[string]string)
	if len(needs.KindOptions) > 0 {
		selections[string(contractsv1.ContextFabricStructureNeedExpectedKind)] = needs.KindOptions[0].ReceiptID
	}
	if len(needs.AnchorOptions) > 0 {
		selections[string(contractsv1.ContextFabricStructureNeedSubjectAnchor)] = needs.AnchorOptions[0].ReceiptID
	}
	if len(needs.HandleOptions) > 0 {
		selections[string(contractsv1.ContextFabricStructureNeedSubjectHandle)] = needs.HandleOptions[0].ReceiptID
	}
	return selections, nil
}

// TestRun_ThreeTurnFlowAccumulatesReceiptsAndAppliesBothMembers is
// CHAOS-4146(a)'s own central regression test: the hosted engine derives
// confirmed structure fresh from what EACH request itself carries (no
// server-side session state across calls -- see run.go's own runPanelist
// doc comment), so turn 3's request must carry BOTH the turn-1-originated
// kind receipt (resent) AND the turn-2-originated anchor receipt, or the
// engine would see the kind confirmation silently dropped. Turn 1 offers
// expected_kind; turn 2 confirms it AND offers subject_anchor; turn 3
// confirms subject_anchor and is decisive.
func TestRun_ThreeTurnFlowAccumulatesReceiptsAndAppliesBothMembers(t *testing.T) {
	const kindReceipt, anchorReceipt = "kindr_receipt00000000000001", "ancr_receipt00000000000001"
	var requests []contractsv1.ContextFabricInvestigationRequest
	turn := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request contractsv1.ContextFabricInvestigationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requests = append(requests, request)
		turn++
		w.Header().Set("Content-Type", "application/json")
		switch turn {
		case 1:
			result := minimalValidResult("result_turn1", request.RequestID)
			result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
			result.SubjectResolution.ClarificationPrompt = "Which kind of work item is this about?"
			result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
				Missing:     []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
				KindOptions: []contractsv1.ContextFabricKindOption{{ReceiptID: kindReceipt, OptionID: "opt1", Label: "Pull request", Kind: "pull_request", OfferSource: contractsv1.ContextFabricStructureOfferEngine}},
			}
			_ = json.NewEncoder(w).Encode(result)
		case 2:
			result := minimalValidResult("result_turn2", request.RequestID)
			result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
			result.SubjectResolution.ClarificationPrompt = "Which subject anchor?"
			result.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
				{
					Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: "pull_request",
					Source: contractsv1.ContextFabricStructureSourceReceipt, PriorResultID: "result_turn1", ReceiptID: kindReceipt,
					Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
				},
			}
			result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
				Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor},
				AnchorOptions: []contractsv1.ContextFabricAnchorOption{{
					ReceiptID: anchorReceipt, OptionID: "opt2", Label: "acr repo",
					Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository_acr",
					MatchedTermHash: "aa11bb22cc33dd44ee55ff66", OfferSource: contractsv1.ContextFabricStructureOfferEngine,
				}},
			}
			_ = json.NewEncoder(w).Encode(result)
		default:
			result := minimalValidResult("result_turn3", request.RequestID)
			// ContextFabricConfirmedStructureEntry's own doc comment: the
			// response carries "one entry PER carried member, INCLUDING
			// vetoed ones" -- turn 3's request resends BOTH the kind and
			// anchor receipts, so a realistic response echoes a disposition
			// for BOTH, not only the newly-offered anchor member.
			result.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
				{
					Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: "pull_request",
					Source: contractsv1.ContextFabricStructureSourceReceipt, PriorResultID: "result_turn1", ReceiptID: kindReceipt,
					Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
				},
				{
					Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedValue: "anchor-42",
					Source: contractsv1.ContextFabricStructureSourceReceipt, PriorResultID: "result_turn2", ReceiptID: anchorReceipt,
					Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
				},
			}
			_ = json.NewEncoder(w).Encode(result)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, testBearerToken('n'), 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	panelist := Panelist{CanonicalModelIdentity: "anthropic/sol-max", Client: client, Selector: autoSelectTopRanked{}}
	manifest, err := Run(context.Background(), RunConfig{
		OrgID: "org-test", Question: "Was Ask Dev ready to ship?",
		Panelists: []Panelist{panelist}, BaseRequest: validRequest(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(requests) != 3 {
		t.Fatalf("server received %d requests, want exactly 3", len(requests))
	}
	if len(requests[1].PriorKindReceipts) != 1 || requests[1].PriorKindReceipts[0].ReceiptID != kindReceipt || requests[1].PriorKindReceipts[0].ResultID != "result_turn1" {
		t.Errorf("turn 2 request PriorKindReceipts = %+v, want the turn-1-originated kind receipt", requests[1].PriorKindReceipts)
	}
	if len(requests[2].PriorKindReceipts) != 1 || requests[2].PriorKindReceipts[0].ReceiptID != kindReceipt || requests[2].PriorKindReceipts[0].ResultID != "result_turn1" {
		t.Errorf("turn 3 request PriorKindReceipts = %+v, want the turn-1-originated kind receipt RESENT", requests[2].PriorKindReceipts)
	}
	if len(requests[2].PriorAnchorReceipts) != 1 || requests[2].PriorAnchorReceipts[0].ReceiptID != anchorReceipt || requests[2].PriorAnchorReceipts[0].ResultID != "result_turn2" {
		t.Errorf("turn 3 request PriorAnchorReceipts = %+v, want the turn-2-originated anchor receipt", requests[2].PriorAnchorReceipts)
	}

	byMember := make(map[string]PanelMemberRun, len(manifest.Members))
	for _, m := range manifest.Members {
		byMember[m.Member] = m
	}
	if len(manifest.Members) != 2 {
		t.Fatalf("Members = %+v, want exactly two (expected_kind, subject_anchor)", manifest.Members)
	}
	kind, anchor := byMember["expected_kind"], byMember["subject_anchor"]
	// ConfirmedResultID = result_turn3, not result_turn2: turn 3's own
	// request resent the kind receipt (see the accumulation assertion
	// above), and its response re-echoed an Applied disposition for it --
	// the LATEST turn to have actually observed the confirmation, matching
	// PanelistSelection.ConfirmedResultID's own doc comment.
	if len(kind.Panelists) != 1 || kind.Panelists[0].AppliedValue != "pull_request" || kind.Panelists[0].ConfirmedResultID != "result_turn3" {
		t.Errorf("expected_kind = %+v, want AppliedValue pull_request landed at result_turn3", kind)
	}
	if len(anchor.Panelists) != 1 || anchor.Panelists[0].AppliedValue != "anchor-42" || anchor.Panelists[0].ConfirmedResultID != "result_turn3" {
		t.Errorf("subject_anchor = %+v, want AppliedValue anchor-42 landed at result_turn3", anchor)
	}

	if len(manifest.ClarificationLogs) != 1 {
		t.Fatalf("ClarificationLogs = %+v, want exactly one panelist entry", manifest.ClarificationLogs)
	}
	turns := manifest.ClarificationLogs[0].Turns
	wantOutcomes := []ClarificationTurnOutcome{ClarificationTurnContinued, ClarificationTurnContinued, ClarificationTurnDecisive}
	if len(turns) != 3 {
		t.Fatalf("Turns = %+v, want exactly 3 entries", turns)
	}
	for i, want := range wantOutcomes {
		if turns[i].Turn != i+1 || turns[i].Outcome != want {
			t.Errorf("Turns[%d] = %+v, want Turn=%d Outcome=%s", i, turns[i], i+1, want)
		}
	}
	if len(turns[0].OfferKinds) != 1 || turns[0].OfferKinds[0] != "expected_kind" {
		t.Errorf("Turns[0].OfferKinds = %v, want [expected_kind]", turns[0].OfferKinds)
	}
	if len(turns[1].OfferKinds) != 1 || turns[1].OfferKinds[0] != "subject_anchor" {
		t.Errorf("Turns[1].OfferKinds = %v, want [subject_anchor]", turns[1].OfferKinds)
	}
	if len(turns[2].OfferKinds) != 0 {
		t.Errorf("Turns[2].OfferKinds = %v, want none (decisive offers nothing)", turns[2].OfferKinds)
	}
}

// TestRun_LaterTurnVetoDropsAnEarlierTurnsAlreadyAppliedConfirmation is a
// regression test for codex xhigh review round 1, HIGH: a member confirmed
// Applied on an EARLIER turn is resent (as an accumulated receipt) on every
// later turn, and ContextFabricConfirmedStructureEntry's own doc comment
// requires the response to carry a fresh disposition for EVERY carried
// member each time -- so a later turn can legitimately flip that member to
// vetoed_stale/vetoed_conflict (e.g. a graph epoch flip, or a conflict with
// another confirmation in the same request). Before this fix, runPanelist
// only reconciled the NEWEST turn's own selections against the response,
// never re-checking already-accumulated ones -- a since-invalidated
// confirmation would stay in the manifest forever. Turn 1 offers
// expected_kind; turn 2 applies it and offers subject_anchor; turn 3
// VETOES the (resent) kind confirmation while applying anchor. The final
// manifest must carry ONLY subject_anchor.
func TestRun_LaterTurnVetoDropsAnEarlierTurnsAlreadyAppliedConfirmation(t *testing.T) {
	const kindReceipt, anchorReceipt = "kindr_receipt00000000000002", "ancr_receipt00000000000002"
	turn := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request contractsv1.ContextFabricInvestigationRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		turn++
		w.Header().Set("Content-Type", "application/json")
		switch turn {
		case 1:
			result := minimalValidResult("result_turn1", request.RequestID)
			result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
			result.SubjectResolution.ClarificationPrompt = "Which kind of work item is this about?"
			result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
				Missing:     []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
				KindOptions: []contractsv1.ContextFabricKindOption{{ReceiptID: kindReceipt, OptionID: "opt1", Label: "Pull request", Kind: "pull_request", OfferSource: contractsv1.ContextFabricStructureOfferEngine}},
			}
			_ = json.NewEncoder(w).Encode(result)
		case 2:
			result := minimalValidResult("result_turn2", request.RequestID)
			result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
			result.SubjectResolution.ClarificationPrompt = "Which subject anchor?"
			result.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
				{
					Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: "pull_request",
					Source: contractsv1.ContextFabricStructureSourceReceipt, PriorResultID: "result_turn1", ReceiptID: kindReceipt,
					Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
				},
			}
			result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
				Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor},
				AnchorOptions: []contractsv1.ContextFabricAnchorOption{{
					ReceiptID: anchorReceipt, OptionID: "opt2", Label: "acr repo",
					Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository_acr",
					MatchedTermHash: "aa11bb22cc33dd44ee55ff66", OfferSource: contractsv1.ContextFabricStructureOfferEngine,
				}},
			}
			_ = json.NewEncoder(w).Encode(result)
		default:
			result := minimalValidResult("result_turn3", request.RequestID)
			result.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
				{
					// The turn-1-originated kind confirmation, previously
					// Applied on turn 2, is now VETOED on turn 3.
					Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: "pull_request",
					Source: contractsv1.ContextFabricStructureSourceReceipt, PriorResultID: "result_turn1", ReceiptID: kindReceipt,
					Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionVetoedStale,
				},
				{
					Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedValue: "anchor-42",
					Source: contractsv1.ContextFabricStructureSourceReceipt, PriorResultID: "result_turn2", ReceiptID: anchorReceipt,
					Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
				},
			}
			_ = json.NewEncoder(w).Encode(result)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, testBearerToken('v'), 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	panelist := Panelist{CanonicalModelIdentity: "anthropic/sol-max", Client: client, Selector: autoSelectTopRanked{}}
	manifest, err := Run(context.Background(), RunConfig{
		OrgID: "org-test", Question: "Was Ask Dev ready to ship?",
		Panelists: []Panelist{panelist}, BaseRequest: validRequest(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(manifest.Members) != 1 || manifest.Members[0].Member != "subject_anchor" {
		t.Fatalf("Members = %+v, want ONLY subject_anchor: the kind confirmation was vetoed on a later turn and must not survive in the manifest", manifest.Members)
	}
}

// TestRun_BlankSelectorValueIsTreatedAsRefusalNotAChoice is a regression
// test for codex xhigh review round 1, MEDIUM: Selector's own doc comment
// states a member mapped to an empty string means "no offer worth
// confirming" -- a map containing ONLY such blank entries must be treated
// exactly like an empty map (refused_not_confident), never as a genuine
// continuation carrying an empty receipt id onto the wire.
func TestRun_BlankSelectorValueIsTreatedAsRefusalNotAChoice(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request contractsv1.ContextFabricInvestigationRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		result := minimalValidResult("result_turn1", request.RequestID)
		result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
		result.SubjectResolution.ClarificationPrompt = "Which kind of work item is this about?"
		result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
			Missing:     []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
			KindOptions: []contractsv1.ContextFabricKindOption{{ReceiptID: "kindr_receipt00000000000003", OptionID: "opt1", Label: "Pull request", Kind: "pull_request", OfferSource: contractsv1.ContextFabricStructureOfferEngine}},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	panelist := newTestPanelist(t, "anthropic/sol-max", server, map[string]string{"expected_kind": ""}, nil)
	manifest, err := Run(context.Background(), RunConfig{
		OrgID: "org-test", Question: "Was Ask Dev ready to ship?",
		Panelists: []Panelist{panelist}, BaseRequest: validRequest(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if requestCount != 1 {
		t.Fatalf("server received %d requests, want exactly 1: a blank-valued selection must be treated as refusal, never a second turn", requestCount)
	}
	if len(manifest.Members) != 0 {
		t.Errorf("Members = %+v, want none", manifest.Members)
	}
	if len(manifest.ClarificationLogs) != 1 || len(manifest.ClarificationLogs[0].Turns) != 1 {
		t.Fatalf("ClarificationLogs = %+v, want exactly one turn", manifest.ClarificationLogs)
	}
	if manifest.ClarificationLogs[0].Turns[0].Outcome != ClarificationTurnRefusedNotConfident {
		t.Errorf("Turns[0].Outcome = %q, want refused_not_confident", manifest.ClarificationLogs[0].Turns[0].Outcome)
	}
}

// TestRun_TurnBudgetExhaustionStopsWithoutFurtherCalls is a regression test
// for CHAOS-4146(a)'s own turn-exhaustion terminal state: a panelist that
// would keep continuing forever (an engine that never actually resolves)
// must stop at MaxClarificationTurns, record turn_exhausted, and never send
// a request beyond the bound -- it must never spend a selection round the
// loop cannot act on.
func TestRun_TurnBudgetExhaustionStopsWithoutFurtherCalls(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request contractsv1.ContextFabricInvestigationRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		// Always re-offers the SAME member with a fresh receipt id, an
		// engine that never actually terminates -- exactly what the turn
		// bound exists to guard against.
		result := minimalValidResult(fmt.Sprintf("result_turn%d", requestCount), request.RequestID)
		result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
		result.SubjectResolution.ClarificationPrompt = "Which kind of work item is this about?"
		result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
			KindOptions: []contractsv1.ContextFabricKindOption{
				{ReceiptID: fmt.Sprintf("kindr_receipt%020d", requestCount), OptionID: "opt1", Label: "Pull request", Kind: "pull_request", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
			},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, testBearerToken('x'), 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	panelist := Panelist{CanonicalModelIdentity: "anthropic/sol-max", Client: client, Selector: autoSelectTopRanked{}}
	manifest, err := Run(context.Background(), RunConfig{
		OrgID: "org-test", Question: "Was Ask Dev ready to ship?",
		Panelists: []Panelist{panelist}, BaseRequest: validRequest(),
		MaxClarificationTurns: 2,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if requestCount != 2 {
		t.Fatalf("server received %d requests, want exactly 2 (the configured MaxClarificationTurns bound)", requestCount)
	}
	if len(manifest.Members) != 0 {
		t.Errorf("Members = %+v, want none: the loop was exhausted before any selection was ever confirmed by a later turn", manifest.Members)
	}
	if len(manifest.ClarificationLogs) != 1 {
		t.Fatalf("ClarificationLogs = %+v, want exactly one panelist entry", manifest.ClarificationLogs)
	}
	turns := manifest.ClarificationLogs[0].Turns
	if len(turns) != 2 {
		t.Fatalf("Turns = %+v, want exactly 2 entries", turns)
	}
	if turns[0].Turn != 1 || turns[0].Outcome != ClarificationTurnContinued {
		t.Errorf("Turns[0] = %+v, want Turn=1 Outcome=continued", turns[0])
	}
	if turns[1].Turn != 2 || turns[1].Outcome != ClarificationTurnExhausted {
		t.Errorf("Turns[1] = %+v, want Turn=2 Outcome=turn_exhausted", turns[1])
	}
}

func TestRun_RequiresAtLeastOnePanelist(t *testing.T) {
	_, err := Run(context.Background(), RunConfig{OrgID: "org-test", Question: "q"})
	if err == nil {
		t.Fatal("expected an error for zero configured panelists")
	}
}

// TestRun_RejectsPanelistsSharingTheSameBearerCredential is a regression
// test for codex round-1 finding HIGH-2: nothing previously stopped two
// panelist configs from using the identical bearer token (only their
// CanonicalModelIdentity strings had to differ), letting one authenticated
// principal be silently counted as multiple "distinct" panelists.
func TestRun_RejectsPanelistsSharingTheSameBearerCredential(t *testing.T) {
	server := newTwoTurnServer(t, "kindr_receipt00000000000001", "pull_request", contractsv1.ContextFabricStructureDispositionApplied)
	defer server.Close()

	sharedToken := testBearerToken(99)
	clientA, err := NewClient(server.URL, sharedToken, 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient A: %v", err)
	}
	clientB, err := NewClient(server.URL, sharedToken, 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient B: %v", err)
	}
	panelistA := Panelist{CanonicalModelIdentity: "anthropic/sol-max", Client: clientA, Selector: stubSelector{}}
	panelistB := Panelist{CanonicalModelIdentity: "anthropic/luna", Client: clientB, Selector: stubSelector{}}

	_, err = Run(context.Background(), RunConfig{
		OrgID: "org-test", Question: "Was Ask Dev ready to ship?",
		Panelists: []Panelist{panelistA, panelistB}, BaseRequest: validRequest(),
	})
	if err == nil {
		t.Fatal("expected an error when two panelists share the same bearer credential")
	}
}

// TestRun_ErrorsWhenEveryPanelistFails is a regression test for codex
// round-1 finding MEDIUM-12: a run where every panelist errors (bad
// credentials, unreachable API, timeouts) must not silently return a
// successful, zero-member manifest -- that outcome is indistinguishable
// from every panelist genuinely finding the question decisive on turn 1.
func TestRun_ErrorsWhenEveryPanelistFails(t *testing.T) {
	failingA := newTestPanelist(t, "anthropic/sol-max", httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})), nil, nil)
	failingB := newTestPanelist(t, "anthropic/luna", httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})), nil, nil)

	_, err := Run(context.Background(), RunConfig{
		OrgID: "org-test", Question: "Was Ask Dev ready to ship?",
		Panelists: []Panelist{failingA, failingB}, BaseRequest: validRequest(),
	})
	if err == nil {
		t.Fatal("expected an error when every configured panelist fails")
	}
}
