package contextfabric

import (
	"context"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"testing"
)

func TestWorkItemTupleSaveRejectsForeignPayloadBeforePersistence(t *testing.T) {
	principal := storage.Principal{OrgID: "org-1"}
	stored := StoredInvestigationResult{Result: workItemTuplePayloadFixture(t), SemanticState: workItemTupleSemanticStateFixture()}
	stored.Result.ResultID = "result-save-tuple"
	for _, rejected := range []bool{false, true} {
		candidate := stored.Result
		if rejected {
			candidate.EvidenceRefIDs = append(append([]string(nil), candidate.EvidenceRefIDs...), "foreign")
		}
		store := &resultStoreStub{}
		telemetry := &recordingTelemetry{}
		engine := mustReuseTestEngine(t, EngineDependencies{Results: store, Telemetry: telemetry})
		err := engine.saveResult(context.Background(), principal, BudgetAssertReuse, candidate, nil, nil, "", 0, "", semanticStateCapture{Write: SemanticStateWrite{State: stored.SemanticState}})
		if (err != nil) != rejected {
			t.Fatalf("reject=%t err=%v", rejected, err)
		}
		if (store.saved.ResultID == "") != rejected {
			t.Fatalf("reject=%t persisted=%s", rejected, store.saved.ResultID)
		}
		if len(telemetry.semanticStatePersistences) != 1 {
			t.Fatal("missing save decision")
		}
		want := SemanticStatePersisted
		if rejected {
			want = SemanticStatePersistenceDecision("payload_rejected")
		}
		if telemetry.semanticStatePersistences[0].Decision != want {
			t.Fatal("save rejection reason not recorded")
		}
	}
}
