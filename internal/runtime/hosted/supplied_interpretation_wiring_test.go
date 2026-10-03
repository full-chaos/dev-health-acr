package hosted

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestTheCompositionConstructorServesASuppliedInterpretationWithNoModel
// drives the constructor the hosted composition calls, with no model runtime
// at all, and a supplied interpretation made under the contract this binary
// runs. The interpreter it returns must serve the request: a constructor that
// dropped the supplied runtime would refuse it.
func TestTheCompositionConstructorServesASuppliedInterpretationWithNoModel(t *testing.T) {
	t.Parallel()
	interpreter, err := newContextFabricQuestionInterpreter(nil, nil, nil, nil, 0, nil)
	if err != nil {
		t.Fatalf("newContextFabricQuestionInterpreter() error = %v", err)
	}
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12345678",
		Question: "What is the status of Ask Dev?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "mcp"},
		SuppliedInterpretation: &contextfabric.SuppliedInterpretation{
			Output:             json.RawMessage(`{"shape":"open","requested_judgment":"status","subject_terms":["Ask Dev"],"time_context":{"axis":"current"},"fact_requirements":[{"kind":"status"}],"clarification_needed":false}`),
			ModelOutputVersion: genkitruntime.DefaultSchemaVersion,
			PromptVersion:      genkitruntime.DefaultInterpretationPromptVersion,
			SystemSHA256:       genkitruntime.InterpretationSystemPromptSHA256(),
		},
	}

	question, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Interpret() error = %v, want the supplied interpretation served", err)
	}
	if question.Shape != contextfabric.ShapeOpen || len(question.SubjectTerms) != 1 || question.SubjectTerms[0] != "Ask Dev" {
		t.Fatalf("interpreted question = %#v, want the supplied one", question)
	}
	want := contextfabric.InterpretationStamp{
		Ran: true, InterpretationVersion: genkitruntime.DefaultSchemaVersion,
		ModelIdentity: "client-supplied/undeclared", Source: contextfabric.InterpretationSourceClient,
	}
	if outcome.Interpretation != want {
		t.Fatalf("interpretation stamp = %#v, want %#v", outcome.Interpretation, want)
	}
}
