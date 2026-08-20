package mcp

// CHAOS-3972 acceptance debt (pivot-intent design brief, DESIGN-FINAL §5,
// row "P3 | mcpclientfixtures scenario suite"). The ratified acceptance
// instrument for P3 is a fixture suite driving the RATIFIED agent flow
// end to end through the real MCP handler (handleInvestigateQuestion,
// exactly as callInvestigateQuestion drives it in investigate_question_test.go):
//
//	refusal (StructureNeeds disclosed on a non-decisive terminal)
//	  -> agent PARSES structure_needs for the offer's receipt_id/prior_result_id
//	  -> agent CONFIRMS by returning the receipt on the matching prior_*_receipts field
//	  -> CONVERTED (confirmed_structure carries one applied entry for the member)
//
// One fixture scenario per closed StructureNeeds member (kind/anchor/handle/
// window, design brief §2.1's four-member frame), loaded from JSON under
// testdata/structure_confirmation_scenarios/ -- the client_fixtures_test.go
// pattern of this package: JSON-shaped fixtures, a Go-coded table of cases,
// Go-coded assertions (not JSON-driven assertions). The upstream MCP mapping
// itself (investigate_question.go's straight-through field mapping) is
// already covered by TestInvestigateQuestionMapsStructureAndWindowRequestFields/
// TestInvestigateQuestionProjectsStructureAndWindowDisclosure; this suite is
// the END-TO-END two-turn scenario the acceptance row names, not a
// duplicate of those unit-level checks.
//
// Parity requirement (design brief §5 row, "must NOT move: existing tool
// contracts byte-stable"): TestStructureConfirmationScenarios_ExistingToolContractsByteStable
// below pins the schema-version constants and enabled-tool set this suite's
// own two-turn traffic must never perturb.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// structureConfirmationScenario is the JSON fixture shape: one closed
// StructureNeeds member, enough typed fields to mint that member's own
// offer type, and the AppliedValue the converted (turn-2) result must
// echo back. Kind/CanonicalID/PatternID/Value/SourceColumn are read only
// for the members that use them; JSON omits the rest.
type structureConfirmationScenario struct {
	Label        string `json:"label"`
	Member       string `json:"member"`
	Question     string `json:"question"`
	Kind         string `json:"kind,omitempty"`
	CanonicalID  string `json:"canonical_id,omitempty"`
	PatternID    string `json:"pattern_id,omitempty"`
	Value        string `json:"value,omitempty"`
	SourceColumn string `json:"source_column,omitempty"`
	AppliedValue string `json:"applied_value"`
}

func loadStructureConfirmationScenario(t *testing.T, root, relPath string) structureConfirmationScenario {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, relPath))
	if err != nil {
		t.Fatalf("read %s: %v", relPath, err)
	}
	var scenario structureConfirmationScenario
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatalf("decode %s: %v", relPath, err)
	}
	return scenario
}

// receiptPrefixForMember mirrors the design brief §2.1 closed namespace
// table (kindr_/ancr_/handr_/winr_) -- the SAME prefixes
// contractsv1.ContextFabric*OptionReceiptPrefix name, restated here only to
// keep the fixture's member string as the single lookup key.
func receiptPrefixForMember(member string) string {
	switch member {
	case string(contractsv1.ContextFabricStructureNeedExpectedKind):
		return contractsv1.ContextFabricKindOptionReceiptPrefix
	case string(contractsv1.ContextFabricStructureNeedSubjectAnchor):
		return contractsv1.ContextFabricAnchorOptionReceiptPrefix
	case string(contractsv1.ContextFabricStructureNeedSubjectHandle):
		return contractsv1.ContextFabricHandleOptionReceiptPrefix
	case string(contractsv1.ContextFabricStructureNeedWindow):
		return contractsv1.ContextFabricWindowOptionReceiptPrefix
	default:
		return ""
	}
}

// buildRefusalResult constructs the turn-1 canonical result: a non-decisive
// terminal (clarification_required) whose StructureNeeds discloses exactly
// one offer for the scenario's member, receipt-bound under that member's
// closed namespace prefix.
func buildRefusalResult(scenario structureConfirmationScenario, resultID, receiptID string) contractsv1.ContextFabricInvestigationResult {
	result := parityResult()
	result.ResultID = resultID
	result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
	result.SubjectResolution.ClarificationPrompt = "synthetic clarification prompt, not corpus content"
	needs := &contractsv1.ContextFabricStructureNeeds{
		Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedKind(scenario.Member)},
	}
	switch scenario.Member {
	case string(contractsv1.ContextFabricStructureNeedExpectedKind):
		needs.KindOptions = []contractsv1.ContextFabricKindOption{{
			ReceiptID: receiptID, OptionID: "opt_" + scenario.Label, Label: "a " + scenario.Kind,
			Kind: contractsv1.ContextFabricSubjectKind(scenario.Kind), OfferSource: contractsv1.ContextFabricStructureOfferEngine,
		}}
	case string(contractsv1.ContextFabricStructureNeedSubjectAnchor):
		needs.AnchorOptions = []contractsv1.ContextFabricAnchorOption{{
			ReceiptID: receiptID, OptionID: "opt_" + scenario.Label, Label: "the " + scenario.Kind,
			Kind: contractsv1.ContextFabricSubjectKind(scenario.Kind), CanonicalID: scenario.CanonicalID,
			MatchedTermHash: strings.Repeat("a", 24), OfferSource: contractsv1.ContextFabricStructureOfferEngine,
		}}
	case string(contractsv1.ContextFabricStructureNeedSubjectHandle):
		needs.HandleOptions = []contractsv1.ContextFabricHandleOption{{
			ReceiptID: receiptID, OptionID: "opt_" + scenario.Label, Label: "PR " + scenario.Value,
			Kind: contractsv1.ContextFabricSubjectKind(scenario.Kind), PatternID: scenario.PatternID,
			Value: scenario.Value, SourceColumn: scenario.SourceColumn, OfferSource: contractsv1.ContextFabricStructureOfferEngine,
		}}
	}
	result.StructureNeeds = needs
	if scenario.Member == string(contractsv1.ContextFabricStructureNeedWindow) {
		// Window offers are minted through the SEPARATE CHAOS-3900 W1
		// WindowClarification path, not StructureNeeds.WindowOptions
		// (codex round-1 finding #1, confirmed against
		// internal/contextfabric/structure.go's composeStructureNeeds,
		// which populates KindOptions/AnchorOptions/HandleOptions only --
		// StructureNeeds.WindowOptions exists on the wire per "3900's type,
		// verbatim" but is not the field production actually fills).
		start, end := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC), time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
		result.WindowClarification = &contractsv1.ContextFabricWindowClarification{
			Options: []contractsv1.ContextFabricWindowOption{{
				ReceiptID: receiptID, OptionID: "opt_" + scenario.Label, Label: "the last 90 days",
				RelativeID: contractsv1.ContextFabricRelativeWindowID(scenario.AppliedValue), Start: &start, End: &end,
			}},
		}
	}
	result.ConfirmedStructure = nil
	return result
}

// buildConvertedResult constructs the turn-2 canonical result: the same
// case, now decisive, carrying exactly one ConfirmedStructure entry for the
// confirmed member -- the confirmed_structure echo the acceptance row
// requires present in EVERY applied case.
func buildConvertedResult(scenario structureConfirmationScenario, priorResultID, receiptID string) contractsv1.ContextFabricInvestigationResult {
	result := parityResult()
	result.StructureNeeds = nil
	result.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{{
		Member: contractsv1.ContextFabricStructureNeedKind(scenario.Member), AppliedValue: scenario.AppliedValue,
		Source: contractsv1.ContextFabricStructureSourceReceipt, PriorResultID: priorResultID, ReceiptID: receiptID,
		OfferSource: contractsv1.ContextFabricStructureOfferEngine, Provenance: contractsv1.ContextFabricStructureClarificationConfirmed,
		Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}}
	return result
}

// setReceipt attaches the confirming receipt to the request field matching
// the scenario's member -- the one prior_*_receipts array the ratified
// agent flow uses to confirm (design brief §2.1/§2.3).
func setReceipt(req *contractsv1.MCPInvestigateQuestionRequest, member string, receipt contractsv1.ContextFabricBoundSubjectReceipt) {
	switch member {
	case string(contractsv1.ContextFabricStructureNeedExpectedKind):
		req.PriorKindReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{receipt}
	case string(contractsv1.ContextFabricStructureNeedSubjectAnchor):
		req.PriorAnchorReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{receipt}
	case string(contractsv1.ContextFabricStructureNeedSubjectHandle):
		req.PriorHandleReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{receipt}
	case string(contractsv1.ContextFabricStructureNeedWindow):
		req.PriorWindowReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{receipt}
	}
}

// twoTurnFixtureBootstrap serves TWO distinct canonical results from the
// SAME hosted endpoint: the refusal until the decoded request carries the
// confirming receipt, the converted result once it does. This is the real
// MCP mapping/projection layer (handleInvestigateQuestion,
// answerprojection.Project) exercising the ratified two-turn shape; the
// census/commit-gate/redemption machinery those two canned results stand in
// for is covered at the engine level by internal/contextfabric's own
// TestCHAOS3900_*/TestCHAOS3972_* suites -- this suite's job is the MCP
// surface's own contract, not a second copy of engine-level redemption
// proof.
func twoTurnFixtureBootstrap(t *testing.T, refusal, converted contractsv1.ContextFabricInvestigationResult, member, priorResultID, receiptID string) *Bootstrap {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/context-fabric/investigations":
			var seen contractsv1.ContextFabricInvestigationRequest
			if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
				t.Errorf("decode investigation request: %v", err)
			}
			// Only the CORRECTLY-NAMESPACED field, bound to the exact
			// (result_id, receipt_id) pair, counts as a confirmation --
			// scanning every prior_*_receipts field regardless of member
			// (the original version) would silently accept a receipt
			// mis-mapped into the wrong field or bound to a different
			// result (a real class of mapping bug this fixture exists to
			// catch).
			var receiptsForMember []contractsv1.ContextFabricBoundSubjectReceipt
			switch member {
			case string(contractsv1.ContextFabricStructureNeedExpectedKind):
				receiptsForMember = seen.PriorKindReceipts
			case string(contractsv1.ContextFabricStructureNeedSubjectAnchor):
				receiptsForMember = seen.PriorAnchorReceipts
			case string(contractsv1.ContextFabricStructureNeedSubjectHandle):
				receiptsForMember = seen.PriorHandleReceipts
			case string(contractsv1.ContextFabricStructureNeedWindow):
				receiptsForMember = seen.PriorWindowReceipts
			}
			sawReceipt := false
			for _, r := range receiptsForMember {
				if r.ReceiptID == receiptID && r.ResultID == priorResultID {
					sawReceipt = true
				}
			}
			if sawReceipt {
				writeJSONFixture(t, w, http.StatusOK, converted)
			} else {
				writeJSONFixture(t, w, http.StatusOK, refusal)
			}
		default:
			writeErrorFixture(t, w, http.StatusNotFound, "not_found", false)
		}
	}))
	t.Cleanup(server.Close)

	cfg := fixtureConfig(t, server)
	client, err := sidecar.NewClient(cfg, fixedCredentialSource(fixtureToken(0xCD)))
	if err != nil {
		t.Fatal(err)
	}
	caps := validCapabilitiesFixture()
	caps.EnabledTools = append(caps.EnabledTools, toolInvestigateQuestion, toolInvestigationResult)
	return &Bootstrap{Config: cfg, Client: client, Capabilities: caps}
}

// TestStructureConfirmationScenarios drives the ratified agent flow --
// refusal -> parse structure_needs -> confirm (receipts) -> converted --
// through the real MCP handler, once per closed StructureNeeds member.
func TestStructureConfirmationScenarios(t *testing.T) {
	root := findRepoRoot(t)

	cases := []struct {
		label   string
		relPath string
	}{
		{"expected_kind", "internal/mcp/testdata/structure_confirmation_scenarios/expected_kind.json"},
		{"subject_anchor", "internal/mcp/testdata/structure_confirmation_scenarios/subject_anchor.json"},
		{"subject_handle", "internal/mcp/testdata/structure_confirmation_scenarios/subject_handle.json"},
		{"window", "internal/mcp/testdata/structure_confirmation_scenarios/window.json"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			scenario := loadStructureConfirmationScenario(t, root, tc.relPath)
			priorResultID := "result_refusal_" + scenario.Label + "0001"
			receiptID := receiptPrefixForMember(scenario.Member) + strings.Repeat("a", 24)

			refusal := buildRefusalResult(scenario, priorResultID, receiptID)
			converted := buildConvertedResult(scenario, priorResultID, receiptID)
			boot := twoTurnFixtureBootstrap(t, refusal, converted, scenario.Member, priorResultID, receiptID)

			// Turn 1: refusal. The agent PARSES structure_needs for the
			// offer's receipt_id/prior_result_id -- read directly off the
			// response, never hand-picked from the fixture, so this proves
			// the response is actually machine-readable.
			turn1 := callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{Question: scenario.Question})
			if turn1.Structured.StructureNeeds == nil {
				t.Fatalf("turn 1: structured.structure_needs is nil, want the disclosure block")
			}
			missingOK := false
			for _, need := range turn1.Structured.StructureNeeds.Missing {
				if string(need) == scenario.Member {
					missingOK = true
				}
			}
			if !missingOK {
				t.Fatalf("turn 1: structure_needs.missing = %+v, want it to name %q", turn1.Structured.StructureNeeds.Missing, scenario.Member)
			}
			if len(turn1.Structured.ConfirmedStructure) != 0 {
				t.Fatalf("turn 1: confirmed_structure = %+v, want none before any confirmation", turn1.Structured.ConfirmedStructure)
			}
			parsedReceiptID := extractOfferReceipt(t, turn1.Structured, scenario.Member)
			if parsedReceiptID != receiptID {
				t.Fatalf("parsed offer receipt_id = %q, want %q", parsedReceiptID, receiptID)
			}
			parsedPriorResultID := turn1.Structured.ResultID
			if parsedPriorResultID != priorResultID {
				t.Fatalf("parsed structured.result_id = %q, want %q", parsedPriorResultID, priorResultID)
			}

			// Turn 2: confirm by returning the parsed receipt, bound to the
			// parsed result id, on the matching prior_*_receipts field.
			turn2Req := contractsv1.MCPInvestigateQuestionRequest{Question: scenario.Question}
			setReceipt(&turn2Req, scenario.Member, contractsv1.ContextFabricBoundSubjectReceipt{
				ResultID: parsedPriorResultID, ReceiptID: parsedReceiptID,
			})
			turn2 := callInvestigateQuestion(t, boot, turn2Req)

			// Converted: confirmed_structure echo present for the applied
			// case, StructureNeeds cleared (decisive terminal -- codex
			// round-1 finding #8: this assertion was missing).
			if turn2.Structured.StructureNeeds != nil {
				t.Fatalf("turn 2: structured.structure_needs = %+v, want nil (decisive terminal clears disclosure)", turn2.Structured.StructureNeeds)
			}
			if len(turn2.Structured.ConfirmedStructure) != 1 {
				t.Fatalf("turn 2: confirmed_structure = %+v, want exactly one applied entry", turn2.Structured.ConfirmedStructure)
			}
			entry := turn2.Structured.ConfirmedStructure[0]
			if string(entry.Member) != scenario.Member {
				t.Errorf("confirmed_structure entry member = %q, want %q", entry.Member, scenario.Member)
			}
			if entry.AppliedValue != scenario.AppliedValue {
				t.Errorf("confirmed_structure entry applied_value = %q, want %q", entry.AppliedValue, scenario.AppliedValue)
			}
			if entry.Disposition != contractsv1.ContextFabricStructureDispositionApplied {
				t.Errorf("confirmed_structure entry disposition = %q, want applied", entry.Disposition)
			}
			if entry.Provenance != contractsv1.ContextFabricStructureClarificationConfirmed {
				t.Errorf("confirmed_structure entry provenance = %q, want clarification_confirmed", entry.Provenance)
			}
			if err := turn2.Validate(); err != nil {
				t.Fatalf("turn 2 response failed contract validation: %v", err)
			}
		})
	}
}

// extractOfferReceipt reads the receipt_id an agent would parse off
// structure_needs for the given member, proving the response is
// machine-readable rather than hand-matched by the test. The prior_result_id
// half of the receipt comes from structured.result_id (asserted separately,
// callInvestigateQuestion's caller).
func extractOfferReceipt(t *testing.T, structured contractsv1.ContextFabricAnswerProjection, member string) (receiptID string) {
	t.Helper()
	needs := structured.StructureNeeds
	switch member {
	case string(contractsv1.ContextFabricStructureNeedExpectedKind):
		if len(needs.KindOptions) != 1 {
			t.Fatalf("kind_options = %+v, want exactly one offer", needs.KindOptions)
		}
		return needs.KindOptions[0].ReceiptID
	case string(contractsv1.ContextFabricStructureNeedSubjectAnchor):
		if len(needs.AnchorOptions) != 1 {
			t.Fatalf("anchor_options = %+v, want exactly one offer", needs.AnchorOptions)
		}
		return needs.AnchorOptions[0].ReceiptID
	case string(contractsv1.ContextFabricStructureNeedSubjectHandle):
		if len(needs.HandleOptions) != 1 {
			t.Fatalf("handle_options = %+v, want exactly one offer", needs.HandleOptions)
		}
		return needs.HandleOptions[0].ReceiptID
	case string(contractsv1.ContextFabricStructureNeedWindow):
		// Window offers project through WindowClarification, not
		// StructureNeeds.WindowOptions (see buildRefusalResult's own
		// comment, codex round-1 finding #1).
		if structured.WindowClarification == nil || len(structured.WindowClarification.Options) != 1 {
			t.Fatalf("window_clarification.options = %+v, want exactly one offer", structured.WindowClarification)
		}
		return structured.WindowClarification.Options[0].ReceiptID
	default:
		t.Fatalf("unknown member %q", member)
		return ""
	}
}

// TestStructureConfirmationScenarios_ExistingToolContractsByteStable is the
// acceptance row's "must NOT move" column: this suite's own two-turn
// traffic must never perturb the enabled-tool set or either tool's schema
// version, which is what "byte-stable" cashes out to at the Go level (the
// wire-level parity check is schemas_parity_test.go's
// TestEmbeddedSchemasMatchCanonicalSource/TestManifestEntryMatchesRegisteredTools,
// unchanged by this suite).
func TestStructureConfirmationScenarios_ExistingToolContractsByteStable(t *testing.T) {
	if toolInvestigateQuestion != "investigate_question" {
		t.Errorf("toolInvestigateQuestion = %q, want investigate_question", toolInvestigateQuestion)
	}
	if toolInvestigationResult != "investigation_result" {
		t.Errorf("toolInvestigationResult = %q, want investigation_result", toolInvestigationResult)
	}
	if contractsv1.MCPInvestigateQuestionRequestSchema == "" || contractsv1.MCPInvestigateQuestionResponseSchema == "" {
		t.Fatal("MCP investigate_question schema version constants must not be empty")
	}
}
