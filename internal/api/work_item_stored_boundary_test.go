package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func storedServingCredential(t *testing.T, app *App, grants []string) string {
	t.Helper()
	return issueScopedCredential(t, app.runtime.Credentials, nil, app.now(), []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, grants)
}

// This helper observes the real SDK's error result too, so denial cannot stop
// the proof before checking its content-free protocol response.
func callStoredServingMCP(t *testing.T, app *App, token, resultID string) (*mcpsdk.CallToolResult, []byte) {
	t.Helper()
	ctx := context.Background()
	server := httptest.NewTLSServer(app.Handler())
	defer server.Close()
	configureSidecarEnvironment(t, server, token)
	boot, err := acrmcp.NewBootstrap(ctx, "1.2.5")
	if err != nil {
		t.Fatal(err)
	}
	mcpServer := acrmcp.NewServer(boot, "test-version")
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "stored-boundary", Version: "0.0.1"}, nil)
	st, ct := mcpsdk.NewInMemoryTransports()
	ss, err := mcpServer.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	arguments, _ := json.Marshal(contractsv1.MCPInvestigationResultRequest{ResultID: resultID})
	called, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "investigation_result", Arguments: json.RawMessage(arguments)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(called)
	if err != nil {
		t.Fatal(err)
	}
	return called, encoded
}

func storedServingFault(t *testing.T, stored *contextfabric.StoredInvestigationResult, mode string) {
	t.Helper()
	switch mode {
	case "absent":
		stored.SemanticState.WorkItemCensus = nil
	case "malformed":
		stored.SemanticState.WorkItemCensus.AuthorizationDigest = "broken"
	case "unsupported":
		stored.SemanticState.WorkItemCensus.Version = "work-item-census.v999"
	case "unreadable":
		stored.SemanticState = nil
		stored.SemanticStateRead = contextfabric.SemanticStateReadMalformed
	case "reading_absent":
		stored.SemanticState = nil
		stored.SemanticStateRead = contextfabric.SemanticStateReadAbsent
	}
}

func TestStoredWorkItemAuthorizationBeforeFallback(t *testing.T) {
	savedPrincipal := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{hostedTestRepository}}
	for _, mode := range []string{"available", "absent", "malformed", "unsupported", "unreadable", "reading_absent"} {
		for _, grant := range []struct {
			name   string
			values []string
		}{{"unchanged", []string{hostedTestRepository}}, {"owner", []string{"example-org/*"}}, {"revoked", []string{"other/repo"}}, {"universal", []string{"*"}}} {
			t.Run(mode+"/"+grant.name, func(t *testing.T) {
				stored := storedWorkItemTupleRouteFixture(t, savedPrincipal)
				storedServingFault(t, &stored, mode)
				store := &storedWorkItemTupleRouteStore{stored: stored}
				before := portableStoredCarrierBytes(t, store.stored)
				app, _ := newParityHostedApp(t, nil, store)
				token := storedServingCredential(t, app, grant.values)
				var logs bytes.Buffer
				app.logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
				want := http.StatusNotFound
				if mode == "available" && grant.name == "unchanged" || mode != "available" && grant.name == "universal" {
					want = http.StatusOK
				}
				for _, view := range []string{"", "view=projection"} {
					req := investigationResultRequest(t, token, stored.Result.ResultID)
					req.URL.RawQuery = view
					rec := httptest.NewRecorder()
					app.Handler().ServeHTTP(rec, req)
					if rec.Code != want {
						t.Errorf("view=%s status=%d want=%d body=%s", view, rec.Code, want, rec.Body.String())
					}
					if want == http.StatusNotFound {
						assertStoredServingContentAbsent(t, rec.Body.Bytes(), stored.Result)
					}
				}
				called, raw := callStoredServingMCP(t, app, token, stored.Result.ResultID)
				if called.IsError != (want != http.StatusOK) {
					t.Errorf("MCP error=%t wantHTTP=%d body=%s", called.IsError, want, raw)
				}
				if want == http.StatusNotFound {
					assertStoredServingContentAbsent(t, raw, stored.Result)
				}
				if !strings.Contains(logs.String(), contextfabric.WorkItemStoredServingLogMessage) {
					t.Error("missing configured Info serving decision")
				}
				if mode != "available" && grant.name != "universal" && !strings.Contains(logs.String(), "authorization_unverifiable") {
					t.Error("missing unverifiable basis")
				}
				if want == http.StatusNotFound {
					denied := 0
					for _, event := range app.runtime.Audit.(*memory.AuditStore).Events() {
						if event.Action == "context_fabric_investigation_result_denied" {
							denied++
						}
					}
					if denied != 3 {
						t.Errorf("audited denials=%d want3 for canonical/projected/MCP", denied)
					}
				}
				assertPortableStoredCarrierUnchanged(t, "all serving surfaces", before, store.stored)
			})
		}
	}
}

func assertStoredServingContentAbsent(t *testing.T, body []byte, result contextfabric.InvestigationResult) {
	t.Helper()
	values := append([]string{}, result.EvidenceRefIDs...)
	for _, label := range result.EvidenceRefLabels {
		values = append(values, label)
	}
	for _, value := range values {
		if value != "" && bytes.Contains(body, []byte(value)) {
			t.Errorf("denial exposed stored evidence %q", value)
		}
	}
	if bytes.Contains(body, []byte("coverage_details")) {
		t.Error("denial exposed coverage counts")
	}
}
