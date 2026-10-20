package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const entitlementValidFields = `"schema_version":"acr_entitlement.v1","org_id":"org_1","agent_context_runtime":true`

var rejectionCases = []struct {
	reason string
	status int
	body   string
	closed bool
}{
	{reason: "unknown_field", status: http.StatusOK, body: `{` + entitlementValidFields + `,"future_grant":true}`},
	{reason: "schema", status: http.StatusOK, body: `{"schema_version":"acr_entitlement.v2","org_id":"org_1","agent_context_runtime":true}`},
	{reason: "decode", status: http.StatusOK, body: `{` + entitlementValidFields},
	{reason: "status", status: http.StatusInternalServerError, body: `{}`},
	{reason: "transport", closed: true},
}

func rejectingEntitlementClient(t *testing.T, status int, body string, closed bool) *entitlements.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("svc_acr_standin_token_0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := entitlements.New(entitlements.Config{
		BaseURL: baseURL, TokenFile: tokenFile, Timeout: time.Second, MaxResponseBytes: 4096,
		PositiveCacheTTL: time.Second, NegativeCacheTTL: time.Second, CacheCapacity: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if closed {
		server.Close()
	}
	return client
}

func logReasons(t *testing.T, logs *bytes.Buffer, msg string) []string {
	t.Helper()
	var reasons []string
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil || entry["msg"] != msg {
			continue
		}
		reason, _ := entry["reason"].(string)
		reasons = append(reasons, reason)
	}
	return reasons
}

func TestServiceModelGateLogsTheRejectionReasonOfAnEntitlementRead(t *testing.T) {
	for _, tc := range rejectionCases {
		t.Run(tc.reason, func(t *testing.T) {
			rig := gatedClientRouteRig(t, &serviceModelGrants{}, ownModelConfigs{})
			rejecting := rejectingEntitlementClient(t, tc.status, tc.body, tc.closed)
			rig.app.runtime.Entitlements = EntitlementFunc(func(ctx context.Context, orgID, entitlement string) (bool, error) {
				if entitlement == agentContextRuntimeEntitlement {
					return true, nil
				}
				return rejecting.HasEntitlement(ctx, orgID, entitlement)
			})

			recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil)

			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d body=%s, want 503", recorder.Code, recorder.Body.String())
			}
			for _, msg := range []string{
				"context fabric service model entitlement unavailable; deployment model withheld",
				"context fabric service model decision unavailable",
			} {
				if got := logReasons(t, rig.logs, msg); len(got) != 1 || got[0] != tc.reason {
					t.Fatalf("%q reason attrs = %v, want [%s]", msg, got, tc.reason)
				}
			}
		})
	}
}

func TestServiceModelListingLogsTheRejectionReasonOfAnEntitlementRead(t *testing.T) {
	for _, tc := range rejectionCases {
		t.Run(tc.reason, func(t *testing.T) {
			logs := &bytes.Buffer{}
			app := &App{logger: slog.New(slog.NewJSONHandler(logs, nil)), runtime: &RuntimeDependencies{
				Entitlements:    rejectingEntitlementClient(t, tc.status, tc.body, tc.closed),
				OrgModelConfigs: ownModelConfigs{},
			}}

			listed := app.serviceModelListable(context.Background(), storage.Principal{OrgID: "org_1"})

			if !listed {
				t.Fatal("serviceModelListable() = false, want true when the entitlement is unreadable")
			}
			var got []string
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var entry map[string]any
				if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == "context fabric service model listing decision" {
					value, _ := entry["entitlement_reason"].(string)
					got = append(got, value)
				}
			}
			if len(got) != 1 || got[0] != tc.reason {
				t.Fatalf("entitlement_reason attrs = %v, want [%s]", got, tc.reason)
			}
		})
	}
}

func TestEntitlementLookupErrorLogsTheRejectionReason(t *testing.T) {
	for _, tc := range rejectionCases {
		t.Run(tc.reason, func(t *testing.T) {
			logs := &bytes.Buffer{}
			app := &App{logger: slog.New(slog.NewJSONHandler(logs, nil))}
			_, err := rejectingEntitlementClient(t, tc.status, tc.body, tc.closed).HasEntitlement(context.Background(), "org_1", agentContextRuntimeEntitlement)
			recorder := httptest.NewRecorder()

			app.writeEntitlementLookupError(recorder, httptest.NewRequest(http.MethodGet, "/", nil), err)

			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", recorder.Code)
			}
			if got := logReasons(t, logs, "entitlement lookup failed"); len(got) != 1 || got[0] != tc.reason {
				t.Fatalf("reason attrs = %v, want [%s]", got, tc.reason)
			}
		})
	}
}
