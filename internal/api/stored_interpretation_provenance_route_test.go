package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
)

// TestAStoredResultReadByIDNamesItsInterpreter reads four stored rows through
// the real result-by-id route, in the canonical view and in the projection
// view. The real engine wrote each row behind the real investigation route:
// one turn the service interpreted, one the caller interpreted, and one the
// engine ended before its interpret step. The fourth row is the service's
// turn as a build without the provenance fields stored it.
//
// A row that records an interpretation version and no source is served with
// the service as its interpreter and no model identity. A row whose
// interpretation version is the unwired placeholder is served without a
// source. A row that names its source is served unchanged.
func TestAStoredResultReadByIDNamesItsInterpreter(t *testing.T) {
	supplied, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	contract := supplied.Contract()
	interpreted, err := genkitruntime.ParseInterpretationOutput([]byte(suppliedRouteOutput), contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	receipt := contextfabric.ModelExecutionReceipt{
		Operation: contextfabric.ModelOperationInterpret, Provider: "test-provider", Model: "test-model", ModelVersion: "model-v1",
		PromptVersion: contract.PromptVersion, SchemaVersion: contract.ModelOutputVersion, EvaluatorVersion: "eval-v1",
		StartedAt: at, CompletedAt: at, Attempts: 1, InputDigest: strings.Repeat("a", 64), Outcome: "success",
	}
	const (
		serverIdentity = "test-provider/test-model"
		clientIdentity = "client-supplied/claude-test"
		unwired        = "unwired"
		legacyID       = "result_stored_before_provenance"
	)
	server, client := contractsv1.ContextFabricInterpretationSourceServer, contractsv1.ContextFabricInterpretationSourceClient

	interprets := 0
	var synthesized []contextfabric.InterpretedQuestion
	store := memoryinvestigation.NewStore()
	engine := newSuppliedRouteEngine(t, contextfabric.RuntimeQuestionInterpreter{
		Runtime:  countingScriptedRuntime{scriptedInterpretRuntime: scriptedInterpretRuntime{interpreted: interpreted, receipt: receipt}, interprets: &interprets},
		Supplied: supplied,
	}, contextfabric.StoredSubjectAdmitted, &synthesized, store)
	logs := &bytes.Buffer{}
	app, token := newParityHostedAppWithLogs(t, engine, store, limits.ResourceBudget{MaxItems: 500, MaxTokens: 500_000, MaxBytes: 8 << 20}, logs)
	hosted := httptest.NewTLSServer(app.Handler())
	t.Cleanup(hosted.Close)

	ask := func(requestID string, edit func(*contractsv1.ContextFabricInvestigationRequest)) contractsv1.ContextFabricInvestigationResult {
		t.Helper()
		body := investigationRequestBody()
		body.RequestID = requestID
		edit(&body)
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-ACR-Client-Version", "1.2.5")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body=%s, want 200", requestID, recorder.Code, recorder.Body.String())
		}
		var result contractsv1.ContextFabricInvestigationResult
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	byService := ask("request_provenance_service", func(*contractsv1.ContextFabricInvestigationRequest) {})
	byCaller := ask("request_provenance_caller", func(r *contractsv1.ContextFabricInvestigationRequest) {
		r.SuppliedInterpretation = &contractsv1.ContextFabricSuppliedInterpretation{
			Output: json.RawMessage(suppliedRouteOutput), ModelOutputVersion: contract.ModelOutputVersion, PromptVersion: contract.PromptVersion, SystemSHA256: contract.SystemSHA256,
			ClientModel: "claude-test",
		}
	})
	beforeInterpretation := ask("request_provenance_no_interpretation", func(r *contractsv1.ContextFabricInvestigationRequest) {
		r.PriorWindowReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "winr_confirm0001"}}
	})
	if interprets != 1 {
		t.Fatalf("model interpret calls = %d, want 1: only the service's turn reaches the model", interprets)
	}

	principal := seedPrincipal(callerOrgID)
	written, err := store.Get(context.Background(), principal, byService.ResultID)
	if err != nil {
		t.Fatalf("read the service's stored row: %v", err)
	}
	legacy := written.Result
	legacy.ResultID = legacyID
	legacy.Versions.InterpretationSource = ""
	legacy.Versions.InterpretationModelIdentity = ""
	saveSurfaceRow(t, store, legacy, contextfabric.SemanticStateOf(written.SemanticState))

	backfillLines := func(offset int) int {
		count := 0
		for _, raw := range strings.Split(strings.TrimSpace(logs.String()[offset:]), "\n") {
			var line map[string]any
			if json.Unmarshal([]byte(raw), &line) == nil && line["msg"] == storedInterpretationProvenanceBackfilledLogMessage {
				if line["level"] != "INFO" || line["interpretation_source"] != string(server) {
					t.Fatalf("backfill line = %v, want an Info line that names server", line)
				}
				count++
			}
		}
		return count
	}

	for _, row := range []struct {
		name          string
		resultID      string
		storedVersion string
		storedSource  contractsv1.ContextFabricInterpretationSource
		wantSource    contractsv1.ContextFabricInterpretationSource
		wantIdentity  string
		backfilled    bool
	}{
		{"the service interpreted and the row names no source", legacyID, contract.ModelOutputVersion, "", server, "", true},
		{"the turn ended before interpretation", beforeInterpretation.ResultID, unwired, "", "", "", false},
		{"the service interpreted and the row names it", byService.ResultID, contract.ModelOutputVersion, server, server, serverIdentity, false},
		{"the caller interpreted and the row names it", byCaller.ResultID, contract.ModelOutputVersion, client, client, clientIdentity, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			stored, err := store.Get(context.Background(), principal, row.resultID)
			if err != nil {
				t.Fatalf("read the stored row: %v", err)
			}
			if got := stored.Result.Versions; got.InterpretationVersion != row.storedVersion || got.InterpretationSource != row.storedSource {
				t.Fatalf("stored row: interpretation_version = %q interpretation_source = %q, want %q and %q", got.InterpretationVersion, got.InterpretationSource, row.storedVersion, row.storedSource)
			}

			offset := logs.Len()
			canonical := getRealAPIResult(t, hosted, token, row.resultID)
			projection := getRealAPIProjection(t, hosted, token, row.resultID, 3, 1, 10)
			for view, got := range map[string]contractsv1.ContextFabricVersionSet{"canonical": canonical.Versions, "projection": projection.Versions} {
				if got.InterpretationSource != row.wantSource || got.InterpretationModelIdentity != row.wantIdentity || got.InterpretationVersion != row.storedVersion {
					t.Fatalf("%s view: interpretation_source = %q interpretation_model_identity = %q interpretation_version = %q, want %q, %q and %q",
						view, got.InterpretationSource, got.InterpretationModelIdentity, got.InterpretationVersion, row.wantSource, row.wantIdentity, row.storedVersion)
				}
			}
			if err := canonical.Validate(); err != nil {
				t.Fatalf("served result fails its own contract: %v", err)
			}
			wantLines := 0
			if row.backfilled {
				wantLines = 2
			}
			if got := backfillLines(offset); got != wantLines {
				t.Fatalf("backfill lines = %d, want %d: one for each read that named a source", got, wantLines)
			}
		})
	}
}
