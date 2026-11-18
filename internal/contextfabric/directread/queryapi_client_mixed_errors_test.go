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

// CHAOS-13999: each entry of a GraphQL errors list is read on its own, so an entry whose extensions have an
// unexpected type (a numeric code, an object reason) never hides a canonical MCP_REFUSED entry in the same
// list, before or after it, on both clients (the run-operation route and the MCP listener route).
func TestTypedRefusalSurvivesUnrelatedMalformedEntries(t *testing.T) {
	const (
		carrier = `{"extensions":{"code":"MCP_REFUSED","reason":"no_carrier"}}`
		query   = `{"extensions":{"code":"MCP_REFUSED","reason":"not_a_query"}}`
		numCode = `{"extensions":{"code":7}}`
		objRsn  = `{"extensions":{"reason":{"x":1}}}`
		plain   = `{"message":"x"}`
		noExt   = `{"extensions":"not an object"}`
	)
	body := func(entries ...string) string {
		out := `{"errors":[`
		for i, e := range entries {
			if i > 0 {
				out += ","
			}
			out += e
		}
		return out + `]}`
	}
	for _, path := range []string{directread.RunOperationPath, "/query"} {
		for _, tc := range []struct {
			name       string
			body       string
			wantClass  directread.ListenerRefusalClass
			wantReason string
		}{
			{"numeric code before a carrier refusal", body(numCode, carrier), directread.ListenerRefusalCarrier, "no_carrier"},
			{"numeric code after a carrier refusal", body(carrier, numCode), directread.ListenerRefusalCarrier, "no_carrier"},
			{"object reason before a carrier refusal", body(objRsn, carrier), directread.ListenerRefusalCarrier, "no_carrier"},
			{"object reason after a carrier refusal", body(carrier, objRsn), directread.ListenerRefusalCarrier, "no_carrier"},
			{"numeric code before a query refusal", body(numCode, query), directread.ListenerRefusalQuery, "not_a_query"},
			{"numeric code after a query refusal", body(query, numCode), directread.ListenerRefusalQuery, "not_a_query"},
			{"object reason before a query refusal", body(objRsn, query), directread.ListenerRefusalQuery, "not_a_query"},
			{"object reason after a query refusal", body(query, objRsn), directread.ListenerRefusalQuery, "not_a_query"},
			{"a non-object extensions entry before a carrier refusal", body(noExt, carrier), directread.ListenerRefusalCarrier, "no_carrier"},
			{"an entry without extensions around a carrier refusal", body(plain, carrier, plain), directread.ListenerRefusalCarrier, "no_carrier"},
			{"a typed entry whose reason is an object is an unknown query refusal", body(`{"extensions":{"code":"MCP_REFUSED","reason":{"x":1}}}`), directread.ListenerRefusalQuery, "unknown"},
			{"the first typed entry wins", body(carrier, query), directread.ListenerRefusalCarrier, "no_carrier"},
			{"only malformed entries: no typed refusal", body(numCode, objRsn, noExt), "", ""},
			{"an errors value that is not an array", `{"errors":{"extensions":{"code":"MCP_REFUSED","reason":"no_carrier"}}}`, "", ""},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, tc.body)
				}))
				t.Cleanup(server.Close)
				client, err := directread.NewHTTPQueryClientWithPath(server.URL, 5*time.Second, path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = client.Execute(context.Background(), directread.QueryCall{OrgID: opOrgA, Document: "query X { x }"})
				var qe *directread.QueryError
				if !errors.As(err, &qe) {
					t.Fatalf("no QueryError: %v", err)
				}
				if qe.ListenerRefusal != tc.wantClass || qe.ListenerReason != tc.wantReason {
					t.Fatalf("ListenerRefusal=%q reason=%q, want %q %q", qe.ListenerRefusal, qe.ListenerReason, tc.wantClass, tc.wantReason)
				}
			})
		}
	}
}
