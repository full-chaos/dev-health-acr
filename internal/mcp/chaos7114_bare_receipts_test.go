package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const chaos7114Parent = "result_7e95c3885a29d789b01ff58bbaf0750c"

var chaos7114Fields = []struct {
	field  string
	prefix string
	seen   func(*contractsv1.ContextFabricInvestigationRequest) []contractsv1.ContextFabricBoundSubjectReceipt
}{
	{"prior_subject_receipts", "subr_", func(r *contractsv1.ContextFabricInvestigationRequest) []contractsv1.ContextFabricBoundSubjectReceipt {
		return r.PriorSubjectReceipts
	}},
	{"prior_kind_receipts", "kindr_", func(r *contractsv1.ContextFabricInvestigationRequest) []contractsv1.ContextFabricBoundSubjectReceipt {
		return r.PriorKindReceipts
	}},
	{"prior_anchor_receipts", "ancr_", func(r *contractsv1.ContextFabricInvestigationRequest) []contractsv1.ContextFabricBoundSubjectReceipt {
		return r.PriorAnchorReceipts
	}},
	{"prior_handle_receipts", "handr_", func(r *contractsv1.ContextFabricInvestigationRequest) []contractsv1.ContextFabricBoundSubjectReceipt {
		return r.PriorHandleReceipts
	}},
	{"prior_window_receipts", "winr_", func(r *contractsv1.ContextFabricInvestigationRequest) []contractsv1.ContextFabricBoundSubjectReceipt {
		return r.PriorWindowReceipts
	}},
	{"prior_candidate_receipts", "candr_", func(r *contractsv1.ContextFabricInvestigationRequest) []contractsv1.ContextFabricBoundSubjectReceipt {
		return r.PriorCandidateReceipts
	}},
}

func chaos7114Call(t *testing.T, args map[string]any) (*mcpsdk.CallToolResult, contractsv1.ContextFabricInvestigationRequest) {
	t.Helper()
	var seen contractsv1.ContextFabricInvestigationRequest
	boot := answerFixtureBootstrap(t, parityResult(), &seen)
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := invokeInvestigateQuestion(context.Background(), boot, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: raw}})
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	return result, seen
}

// A real client (Claude for Mac, CHAOS-7114) sent bare receipt-id strings.
// With parent_result_id present they are accepted, and the result_id of each
// receipt is the parent_result_id.
func TestChaos7114BareReceiptIDsAcceptedWithParent(t *testing.T) {
	for _, tc := range chaos7114Fields {
		t.Run(tc.field, func(t *testing.T) {
			id := tc.prefix + strings.Repeat("a", 24)
			result, seen := chaos7114Call(t, map[string]any{"question": "Use the trailing 90 days", "parent_result_id": chaos7114Parent, tc.field: []any{id}})
			if result.IsError {
				t.Fatalf("bare receipt refused: %s", toolResultText(result))
			}
			want := []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: chaos7114Parent, ReceiptID: id}}
			if got := tc.seen(&seen); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s forwarded = %+v, want %+v", tc.field, got, want)
			}
		})
	}
}

func TestChaos7114MixedBareAndObjectForm(t *testing.T) {
	bare := "winr_" + strings.Repeat("b", 24)
	obj := "winr_" + strings.Repeat("c", 24)
	result, seen := chaos7114Call(t, map[string]any{
		"question": "q", "parent_result_id": chaos7114Parent,
		"prior_window_receipts": []any{bare, map[string]any{"result_id": "result_other_00000001", "receipt_id": obj}},
	})
	if result.IsError {
		t.Fatalf("mixed form refused: %s", toolResultText(result))
	}
	want := []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: chaos7114Parent, ReceiptID: bare}, {ResultID: "result_other_00000001", ReceiptID: obj}}
	if !reflect.DeepEqual(seen.PriorWindowReceipts, want) {
		t.Fatalf("forwarded = %+v, want %+v", seen.PriorWindowReceipts, want)
	}
}

func TestChaos7114BareReceiptWithoutParentIsRefusedClearly(t *testing.T) {
	for _, tc := range chaos7114Fields {
		t.Run(tc.field, func(t *testing.T) {
			result, seen := chaos7114Call(t, map[string]any{"question": "q", tc.field: []any{tc.prefix + strings.Repeat("a", 24)}})
			if !result.IsError {
				t.Fatal("bare receipt without parent_result_id was accepted")
			}
			text := toolResultText(result)
			if !strings.Contains(text, "parent_result_id") || !strings.Contains(text, tc.field) {
				t.Fatalf("message does not name the field and parent_result_id: %q", text)
			}
			if got := tc.seen(&seen); len(got) != 0 {
				t.Fatalf("refused call still reached the hosted API: %+v", got)
			}
		})
	}
}

func TestChaos7114ObjectFormUnchanged(t *testing.T) {
	id := "winr_" + strings.Repeat("d", 24)
	result, seen := chaos7114Call(t, map[string]any{
		"question": "q", "prior_window_receipts": []any{map[string]any{"result_id": "result_prior_00000001", "receipt_id": id}},
	})
	if result.IsError {
		t.Fatalf("object form refused: %s", toolResultText(result))
	}
	want := []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: "result_prior_00000001", ReceiptID: id}}
	if !reflect.DeepEqual(seen.PriorWindowReceipts, want) {
		t.Fatalf("forwarded = %+v, want %+v", seen.PriorWindowReceipts, want)
	}
}

// A bare string still has to carry its namespace prefix; a wrong prefix is
// refused exactly as the object form's is.
func TestChaos7114BareReceiptWrongPrefixRefused(t *testing.T) {
	result, _ := chaos7114Call(t, map[string]any{"question": "q", "parent_result_id": chaos7114Parent, "prior_window_receipts": []any{"kindr_" + strings.Repeat("a", 24)}})
	if !result.IsError {
		t.Fatal("wrong-prefix bare receipt was accepted")
	}
}

// The published tool schema (what clients read) must accept the bare form,
// and must still reject a non-string non-object entry.
func TestChaos7114PublishedSchemaAcceptsBareForm(t *testing.T) {
	ok := `{"question":"q","parent_result_id":"` + chaos7114Parent + `","prior_window_receipts":["winr_aaaaaaaaaaaa"],"prior_kind_receipts":[{"result_id":"result_prior_00000001","receipt_id":"kindr_aaaaaaaaaaaa"}]}`
	if err := contractcheck.ValidateSerialized("", "mcp_investigate_question_request.v1.schema.json", []byte(ok)); err != nil {
		t.Fatalf("schema refuses bare form: %v", err)
	}
	bad := `{"question":"q","parent_result_id":"` + chaos7114Parent + `","prior_window_receipts":[7]}`
	if err := contractcheck.ValidateSerialized("", "mcp_investigate_question_request.v1.schema.json", []byte(bad)); err == nil {
		t.Fatal("schema accepts a numeric receipt entry")
	}
}
