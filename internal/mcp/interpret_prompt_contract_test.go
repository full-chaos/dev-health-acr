package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestInterpretPromptMetaIsTheContractTheGateAccepts joins the read side to
// the write side with no model in between: the _meta block a real prompts/get
// returns, sent back unchanged as the contract of a supplied interpretation,
// is accepted by the real gate; each of its three values changed by one
// character is refused, naming that field.
func TestInterpretPromptMetaIsTheContractTheGateAccepts(t *testing.T) {
	client, closeFn := connectedClient(t, investigateBootstrap(t))
	defer closeFn()
	const question = "how is the payments team doing"
	prompt, err := getInterpretPrompt(t, client, question)
	if err != nil {
		t.Fatal(err)
	}
	meta := func(key string) string {
		value, _ := prompt.Meta[key].(string)
		if value == "" {
			t.Fatalf("_meta has no %q: %v", key, prompt.Meta)
		}
		return value
	}
	served := contractsv1.ContextFabricInterpretationContract{
		ModelOutputVersion: meta("model_output_version"), PromptVersion: meta("prompt_version"), SystemSHA256: meta("system_sha256"),
	}

	gate, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if gate.Contract() != served {
		t.Fatalf("prompts/get _meta = %#v, the gate's contract = %#v: a client that copies _meta would be refused", served, gate.Contract())
	}
	request := func(contract contractsv1.ContextFabricInterpretationContract) contextfabric.InvestigationRequest {
		return contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12345678", Question: question,
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
				MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
			},
			Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "mcp"},
			SuppliedInterpretation: &contextfabric.SuppliedInterpretation{
				Output:             json.RawMessage(`{"shape":"open","requested_judgment":"status","subject_terms":["payments"],"time_context":{"axis":"current"},"fact_requirements":[{"kind":"status"}],"clarification_needed":false}`),
				ModelOutputVersion: contract.ModelOutputVersion, PromptVersion: contract.PromptVersion, SystemSHA256: contract.SystemSHA256,
			},
		}
	}
	principal := storage.Principal{OrgID: "org_1"}
	if _, _, err := gate.InterpretSuppliedQuestion(context.Background(), principal, request(served)); err != nil {
		t.Fatalf("an interpretation declared under the served _meta was refused: %v", err)
	}

	flip := func(value string) string {
		last := value[len(value)-1]
		if last == '0' {
			return value[:len(value)-1] + "1"
		}
		return value[:len(value)-1] + "0"
	}
	for field, changed := range map[string]contractsv1.ContextFabricInterpretationContract{
		"model_output_version": {ModelOutputVersion: flip(served.ModelOutputVersion), PromptVersion: served.PromptVersion, SystemSHA256: served.SystemSHA256},
		"prompt_version":       {ModelOutputVersion: served.ModelOutputVersion, PromptVersion: flip(served.PromptVersion), SystemSHA256: served.SystemSHA256},
		"system_sha256":        {ModelOutputVersion: served.ModelOutputVersion, PromptVersion: served.PromptVersion, SystemSHA256: flip(served.SystemSHA256)},
	} {
		_, _, err := gate.InterpretSuppliedQuestion(context.Background(), principal, request(changed))
		var mismatch *contextfabric.SuppliedInterpretationContractMismatch
		if !errors.As(err, &mismatch) || len(mismatch.Refusal.Mismatch) != 1 || mismatch.Refusal.Mismatch[0] != field {
			t.Fatalf("%s changed by one character: error = %v, want a refusal naming exactly that field", field, err)
		}
		if mismatch.Refusal.Current != served {
			t.Fatalf("%s: refusal current = %#v, want the served _meta %#v", field, mismatch.Refusal.Current, served)
		}
	}
}
