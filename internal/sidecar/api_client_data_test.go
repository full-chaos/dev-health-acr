package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-7072 (S1a): the three direct data client methods return the hosted
// JSON UNCHANGED, send only the caller's own bearer, and map failures onto
// sanitized typed errors.

const (
	dataCatalogBody = `{"contract_version":"acr-data.v1","sections":["operations"],"operations":{"caller_class":"unrestricted","available":true,"operations":[],"not_served":[],"refused_shapes":[]},"versions":{"contract":"acr-data.v1"},"caller":{"scopes":["context:read"],"grant_class":"unrestricted"},"consistency":"best_effort","untrusted_content":{"untrusted":true,"notice":"n","fields":[]},"future_field":{"kept":[1,2,3]}}`
	dataFindBody    = `{"status":"complete","subjects":[{"kind":"repository","canonical_id":"repository:a","label":"acme/a","match":""}],"population":{"returned":1,"total_known":1,"truncated":false},"page":{"returned":1,"complete":true},"consistency":"best_effort","request":{"mode":"list","kind":"repository","limit":25},"untrusted_content":{"untrusted":true,"notice":"n","fields":["subjects[].label"]},"future_field":"kept"}`
	dataOpBody      = `{"call":"refused","completeness":"unknown","operation":"investmentBreakdown","refusal":{"code":"basis_dependent_shape","reason":"team and repository investment comes from read_facts (kind investment)"},"source":{"path":"graphql","service":"dho query-api","schema_digest":"sha256:0"},"errors":[],"page":{"returned_bytes":0,"max_bytes":32768},"consistency":"best_effort","untrusted_content":{"untrusted":true,"fields":["data"]},"request":{"operation":"investmentBreakdown","max_bytes":32768},"future_field":9007199254740993}`
)

func newDataClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeRaw(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func TestDataCatalogReturnsTheHostedJSONVerbatimWithTheCallersBearer(t *testing.T) {
	var gotAuth, gotMethod, gotPath, gotQuery string
	client := newDataClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotMethod, gotPath, gotQuery = r.Header.Get("Authorization"), r.Method, r.URL.Path, r.URL.RawQuery
		writeRaw(w, http.StatusOK, dataCatalogBody+"\n")
	})
	raw, err := client.DataCatalog(context.Background(), []string{"operations", "limits"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+testBearerCanary || gotMethod != http.MethodGet || gotPath != "/api/v1/context-fabric/data/catalog" || gotQuery != "sections=operations%2Climits" {
		t.Fatalf("request: %q %s %s ?%s", gotAuth, gotMethod, gotPath, gotQuery)
	}
	// Byte for byte, including a member this client has never heard of: a
	// client that decoded into a struct would have dropped future_field.
	if string(raw) != dataCatalogBody {
		t.Fatalf("the catalogue JSON was changed:\n got %s\nwant %s", raw, dataCatalogBody)
	}
}

func TestFindSubjectsPostsTheRequestAndReturnsTheHostedJSONVerbatim(t *testing.T) {
	var sent map[string]any
	client := newDataClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/context-fabric/data/subjects" || r.Header.Get("Authorization") != "Bearer "+testBearerCanary {
			t.Errorf("request %s %s auth %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.Unmarshal(mustReadAll(t, r), &sent)
		writeRaw(w, http.StatusOK, dataFindBody)
	})
	raw, err := client.FindSubjects(context.Background(), contractsv1.MCPFindSubjectsRequest{Query: "acme/a", Kinds: []string{"repository"}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != dataFindBody {
		t.Fatalf("the answer JSON was changed: %s", raw)
	}
	if sent["query"] != "acme/a" || sent["limit"] != float64(5) || len(sent) != 3 {
		t.Fatalf("body sent: %v", sent)
	}
}

func TestRunOperationReturnsARefusalAsAnAnswerAndKeepsBigNumbersExact(t *testing.T) {
	client := newDataClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/context-fabric/data/operations" {
			t.Errorf("path %s", r.URL.Path)
		}
		var body map[string]any
		_ = json.Unmarshal(mustReadAll(t, r), &body)
		if body["operation"] != "investmentBreakdown" {
			t.Errorf("body %v", body)
		}
		writeRaw(w, http.StatusOK, dataOpBody)
	})
	raw, err := client.RunOperation(context.Background(), contractsv1.MCPRunOperationRequest{Operation: "investmentBreakdown", Variables: map[string]any{"batch": map[string]any{}}})
	if err != nil {
		t.Fatalf("a policy refusal is an answer, not an error: %v", err)
	}
	if string(raw) != dataOpBody || !strings.Contains(string(raw), "9007199254740993") {
		t.Fatalf("the answer was changed: %s", raw)
	}
}

func TestRunOperationInsufficientScopeIsATypedSanitizedError(t *testing.T) {
	client := newDataClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONFixture(t, w, http.StatusForbidden, contractsv1.ErrorEnvelope{
			SchemaVersion: contractsv1.ErrorSchema, RequestID: "req_server",
			Error: contractsv1.ErrorDetail{Code: "insufficient_scope", Message: "Credential is missing the required scope", HTTPStatus: http.StatusForbidden},
		})
	})
	_, err := client.RunOperation(context.Background(), contractsv1.MCPRunOperationRequest{Operation: "hotspots"})
	if !errors.Is(err, ErrInsufficientScope) {
		t.Fatalf("want ErrInsufficientScope, got %v", err)
	}
	if strings.Contains(err.Error(), testBearerCanary) {
		t.Fatal("the bearer reached an error string")
	}
}

func TestDataMethodsNeverEchoANonEnvelopeUpstreamBody(t *testing.T) {
	const secret = "RAW-UPSTREAM-SECRET-do-not-echo"
	client := newDataClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeRaw(w, http.StatusBadGateway, `{"leak":"`+secret+`"}`)
	})
	for name, call := range map[string]func() error{
		"catalog": func() error { _, err := client.DataCatalog(context.Background(), nil); return err },
		"find": func() error {
			_, err := client.FindSubjects(context.Background(), contractsv1.MCPFindSubjectsRequest{Kind: "team"})
			return err
		},
		"run": func() error {
			_, err := client.RunOperation(context.Background(), contractsv1.MCPRunOperationRequest{Operation: "hotspots"})
			return err
		},
	} {
		err := call()
		if err == nil {
			t.Fatalf("%s: a 502 must be an error", name)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("%s: the upstream body reached the error: %v", name, err)
		}
	}
}

func TestDataMethodsRejectAnAnswerThatIsNotTheContract(t *testing.T) {
	cases := map[string]string{
		"not an object":            `[1]`,
		"missing member":           `{"call":"served"}`,
		"null member":              strings.Replace(dataOpBody, `"errors":[]`, `"errors":null`, 1),
		"trailing content":         dataOpBody + `{}`,
		"catalogue wrong contract": strings.Replace(dataCatalogBody, "acr-data.v1", "acr-data.v9", 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			client := newDataClient(t, func(w http.ResponseWriter, r *http.Request) { writeRaw(w, http.StatusOK, body) })
			var err error
			if name == "catalogue wrong contract" {
				_, err = client.DataCatalog(context.Background(), nil)
			} else {
				_, err = client.RunOperation(context.Background(), contractsv1.MCPRunOperationRequest{Operation: "hotspots"})
			}
			if err == nil || (!errors.Is(err, ErrInvalidResponse) && !errors.Is(err, ErrMalformedResponse)) {
				t.Fatalf("want an invalid-response error, got %v", err)
			}
		})
	}
}

func TestDataMethodsRefuseAnInvalidRequestBeforeAnyCall(t *testing.T) {
	calls := 0
	client := newDataClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; writeRaw(w, http.StatusOK, dataOpBody) })
	if _, err := client.FindSubjects(context.Background(), contractsv1.MCPFindSubjectsRequest{}); err == nil {
		t.Fatal("neither kind nor query must be refused")
	}
	if _, err := client.RunOperation(context.Background(), contractsv1.MCPRunOperationRequest{Operation: "not an operation"}); err == nil {
		t.Fatal("a malformed operation name must be refused")
	}
	if calls != 0 {
		t.Fatalf("%d hosted calls for invalid requests", calls)
	}
}

func TestDataResponseOverTheClientLimitIsATypedError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeRaw(w, http.StatusOK, `{"call":"served","pad":"`+strings.Repeat("x", 16384)+`"}`)
	}))
	defer server.Close()
	cfg := newFixtureConfig(t, server)
	cfg.MaxResponseBytes = 8192
	client, err := NewClient(cfg, fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.RunOperation(context.Background(), contractsv1.MCPRunOperationRequest{Operation: "hotspots"})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("want ErrResponseTooLarge, got %v", err)
	}
}
