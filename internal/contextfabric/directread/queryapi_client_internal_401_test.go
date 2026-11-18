package directread_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// The ops internal listener refuses a bad identity carrier on the run-operation route with a bare text
// 401. That status on that exact route is the carrier refusal; no other route, status or body is.
func TestQueryClientBareInternal401OnRunOperationIsCarrierRefused(t *testing.T) {
	const typed = `{"errors":[{"message":"m","extensions":{"code":"MCP_REFUSED","reason":"no_carrier"}}]}`
	for _, tc := range []struct {
		name       string
		base       string
		path       string
		status     int
		body       string
		wantClass  directread.ListenerRefusalClass
		wantReason string
	}{
		{"bare 401 on run-operation", "", directread.RunOperationPath, 401, "unauthorized\n", directread.ListenerRefusalCarrier, directread.ListenerReasonInternal401},
		{"empty 401 on run-operation", "", directread.RunOperationPath, 401, "", directread.ListenerRefusalCarrier, directread.ListenerReasonInternal401},
		{"typed 401 keeps its own reason", "", directread.RunOperationPath, 401, typed, directread.ListenerRefusalCarrier, "no_carrier"},
		{"bare 401 on the /query client", "", "/query", 401, "unauthorized\n", "", ""},
		{"bare 401 on another path", "", "/query/other", 401, "unauthorized\n", "", ""},
		{"bare 403 (elevated claim) stays http_status", "", directread.RunOperationPath, 403, "forbidden\n", "", ""},
		{"bare 404", "", directread.RunOperationPath, 404, "not found\n", "", ""},
		{"bare 422", "", directread.RunOperationPath, 422, "unprocessable\n", "", ""},
		{"split base /query plus /run-operation", "/query", "/run-operation", 401, "unauthorized\n", directread.ListenerRefusalCarrier, directread.ListenerReasonInternal401},
		{"prefixed base /base plus run-operation path", "/base", directread.RunOperationPath, 401, "unauthorized\n", "", ""},
		{"child route of run-operation", "", directread.RunOperationPath + "/extra", 401, "unauthorized\n", "", ""},
		{"401 with a read-budget code", "", directread.RunOperationPath, 401, `{"errors":[{"extensions":{"code":"MCP_READ_BUDGET_EXCEEDED","reason":"rows_ceiling"}}]}`, "", ""},
		{"401 with only unrelated errors", "", directread.RunOperationPath, 401, `{"errors":[{"message":"x","extensions":{"code":"OTHER"}}]}`, directread.ListenerRefusalCarrier, directread.ListenerReasonInternal401},
		{"bare 500", "", directread.RunOperationPath, 500, "boom\n", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)
			client, err := directread.NewHTTPQueryClientWithPath(server.URL+tc.base, 5*time.Second, tc.path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Execute(context.Background(), directread.QueryCall{OrgID: opOrgA, Document: "query X { x }"})
			var qe *directread.QueryError
			if !errors.As(err, &qe) {
				t.Fatalf("no QueryError: %v", err)
			}
			if qe.ListenerRefusal != tc.wantClass || (tc.wantClass != "" && qe.ListenerReason != tc.wantReason) {
				t.Fatalf("ListenerRefusal=%q reason=%q, want %q %q", qe.ListenerRefusal, qe.ListenerReason, tc.wantClass, tc.wantReason)
			}
			if tc.status != 404 && qe.Class != directread.QueryErrorHTTPStatus {
				t.Fatalf("class = %q, want http_status kept on the error", qe.Class)
			}
		})
	}
}
