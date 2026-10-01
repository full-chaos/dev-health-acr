package directread_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

// The exact header set on the wire. Go's server adds nothing to the
// request header map; Content-Length is set by the transport from the body.
func TestQueryClientSendsExactlyTheInternalIdentityHeaders(t *testing.T) {
	u := newOpUpstream(t, func(opRecorded) (int, string) { return 200, `{"data":{"x":1}}` })
	client, err := directread.NewHTTPQueryClient(u.server.URL+"/", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
	// A context that carries anything a caller might hope to forward is
	// irrelevant: the client builds headers from QueryCall only.
	res, err := client.Execute(ctx, directread.QueryCall{OrgID: opOrgA, Document: "query X { x }", Variables: map[string]any{"orgId": opOrgA, "n": json.Number("3")}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if string(res.Body) != `{"data":{"x":1}}` {
		t.Fatalf("body = %s", res.Body)
	}
	reqs := u.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	got := make([]string, 0, len(reqs[0].Header))
	for name := range reqs[0].Header {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"Accept", "Content-Length", "Content-Type", "User-Agent", "X-Dh-Internal-Impersonation-Active", "X-Dh-Internal-Org-Id", "X-Dh-Internal-Role", "X-Dh-Internal-Superuser", "X-Request-Id"}
	if !slices.Equal(got, want) {
		t.Fatalf("header set = %v, want %v", got, want)
	}
	h := reqs[0].Header
	for name, value := range map[string]string{
		"X-DH-Internal-Org-Id": opOrgA, "X-DH-Internal-Role": "viewer", "X-DH-Internal-Superuser": "false",
		"X-DH-Internal-Impersonation-Active": "false", "X-Request-Id": "req_0123456789abcdef0123456789abcdef",
		"Content-Type": "application/json",
	} {
		if v := h.Values(name); len(v) != 1 || v[0] != value {
			t.Errorf("%s = %v, want [%s]", name, v, value)
		}
	}
	if h.Get("Authorization") != "" {
		t.Fatal("Authorization header sent")
	}
	if wrong := h.Get("X-Test-Wrong-Route"); wrong != "" {
		t.Fatalf("request went to %s, want POST /query", wrong)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(reqs[0].Raw, &body); err != nil || len(body) != 2 || string(body["query"]) != `"query X { x }"` || string(body["variables"]) != `{"n":3,"orgId":"`+opOrgA+`"}` {
		t.Fatalf("body shape = %s", reqs[0].Raw)
	}
	// No request id in context: no X-Request-Id header at all.
	u.reset()
	if _, err := client.Execute(context.Background(), directread.QueryCall{OrgID: opOrgA, Document: "query X { x }"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := u.requests()[0].Header["X-Request-Id"]; ok {
		t.Fatal("X-Request-Id sent without a request id")
	}
	if string(u.requests()[0].Raw) != `{"query":"query X { x }","variables":{}}` {
		t.Fatalf("nil variables body = %s", u.requests()[0].Raw)
	}
}

func TestQueryClientStatusMappingAndNoUpstreamText(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		delay  time.Duration
		want   directread.QueryErrorClass
	}{
		{"not_found", 404, "SECRET-UPSTREAM no route", 0, directread.QueryErrorNotFound},
		{"bad_gateway", 502, "SECRET-UPSTREAM panic", 0, directread.QueryErrorHTTPStatus},
		{"unauthorized", 401, "SECRET-UPSTREAM", 0, directread.QueryErrorHTTPStatus},
		{"redirect", 307, "SECRET-UPSTREAM", 0, directread.QueryErrorHTTPStatus},
		{"timeout", 200, `{"data":{}}`, 400 * time.Millisecond, directread.QueryErrorTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newOpUpstream(t, func(opRecorded) (int, string) {
				time.Sleep(tc.delay)
				return tc.status, tc.body
			})
			client, err := directread.NewHTTPQueryClient(u.server.URL, 150*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Execute(context.Background(), directread.QueryCall{OrgID: opOrgA, Document: "query X { x }"})
			if got := directread.QueryErrorClassOf(err); got != tc.want {
				t.Fatalf("class = %q (%v), want %s", got, err, tc.want)
			}
			host := strings.TrimPrefix(u.server.URL, "http://")
			if strings.Contains(err.Error(), "SECRET-UPSTREAM") || strings.Contains(err.Error(), host) || strings.Contains(err.Error(), "127.0.0.1") {
				t.Fatalf("error text leaks upstream detail: %q", err.Error())
			}
			if n := len(u.requests()); n != 1 {
				t.Fatalf("%d requests; a failure must not be retried", n)
			}
		})
	}
	// Transport failure: nothing listens.
	client, _ := directread.NewHTTPQueryClient("http://127.0.0.1:1", time.Second)
	_, err := client.Execute(context.Background(), directread.QueryCall{OrgID: opOrgA, Document: "query X { x }"})
	if directread.QueryErrorClassOf(err) != directread.QueryErrorTransport || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("transport: %v", err)
	}
	// Canceled context.
	u := newOpUpstream(t, func(opRecorded) (int, string) { return 200, `{}` })
	client, _ = directread.NewHTTPQueryClient(u.server.URL, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Execute(ctx, directread.QueryCall{OrgID: opOrgA, Document: "query X { x }"})
	if directread.QueryErrorClassOf(err) != directread.QueryErrorCanceled {
		t.Fatalf("canceled: %v", err)
	}
}

func TestQueryClientRefusesBeforeSending(t *testing.T) {
	u := newOpUpstream(t, func(opRecorded) (int, string) { return 200, `{"data":{}}` })
	client, _ := directread.NewHTTPQueryClient(u.server.URL, time.Second)
	doc := "query X { x }"
	// Exactly at the limit is sent; one byte over is refused.
	base, _ := directread.EncodeQueryBody(directread.QueryCall{Document: doc, Variables: map[string]any{"s": ""}})
	pad := strings.Repeat("a", directread.MaxQueryRequestBytes-len(base))
	atLimit := directread.QueryCall{OrgID: opOrgA, Document: doc, Variables: map[string]any{"s": pad}}
	if body, _ := directread.EncodeQueryBody(atLimit); len(body) != directread.MaxQueryRequestBytes {
		t.Fatalf("fixture body is %d bytes, want %d", len(body), directread.MaxQueryRequestBytes)
	}
	if _, err := client.Execute(context.Background(), atLimit); err != nil {
		t.Fatalf("a body at the limit was refused: %v", err)
	}
	over := directread.QueryCall{OrgID: opOrgA, Document: doc, Variables: map[string]any{"s": pad + "a"}}
	if _, err := client.Execute(context.Background(), over); directread.QueryErrorClassOf(err) != directread.QueryErrorRequestInvalid {
		t.Fatalf("over the limit: %v", err)
	}
	if _, err := client.Execute(context.Background(), directread.QueryCall{Document: doc}); directread.QueryErrorClassOf(err) != directread.QueryErrorRequestInvalid {
		t.Fatalf("no org: %v", err)
	}
	if n := len(u.requests()); n != 1 {
		t.Fatalf("%d requests; only the at-limit body may be sent", n)
	}
	// The runner maps the client's refusal to invalid_request.
	cat, _ := directread.DefaultCatalogue()
	runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{
		Catalogue: cat,
		Gate:      directread.NewSubjectGate(newOpGraph(), nil),
		Client:    refusingClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "securityAlerts"})
	if err != nil || resp.Refusal == nil || resp.Refusal.Code != directread.RefusalInvalidRequest {
		t.Fatalf("runner mapping: %+v %v", resp.Refusal, err)
	}
}

type refusingClient struct{}

func (refusingClient) Execute(context.Context, directread.QueryCall) (directread.QueryResult, error) {
	return directread.QueryResult{}, &directread.QueryError{Class: directread.QueryErrorRequestInvalid}
}

func TestQueryClientResponseBoundAndConfig(t *testing.T) {
	big := `{"data":{"x":"` + strings.Repeat("a", directread.MaxQueryResponseBytes) + `"}}`
	u := newOpUpstream(t, func(opRecorded) (int, string) { return 200, big })
	client, _ := directread.NewHTTPQueryClient(u.server.URL, 5*time.Second)
	_, err := client.Execute(context.Background(), directread.QueryCall{OrgID: opOrgA, Document: "query X { x }"})
	if directread.QueryErrorClassOf(err) != directread.QueryErrorResponseTooLarge {
		t.Fatalf("oversized answer: %v", err)
	}
	for _, bad := range []string{"", "ftp://h/x", "http://", "http://u:p@h", "http://h/?q=1", "http://h/#f", "::"} {
		if _, err := directread.NewHTTPQueryClient(bad, time.Second); !errors.Is(err, directread.ErrQueryClientConfig) || strings.Contains(err.Error(), "u:p") {
			t.Errorf("NewHTTPQueryClient(%q) = %v", bad, err)
		}
	}
	if _, err := directread.NewHTTPQueryClient("http://h", 0); !errors.Is(err, directread.ErrQueryClientConfig) {
		t.Error("zero timeout accepted")
	}
	c, err := directread.NewHTTPQueryClientWithHTTP("http://h/base", time.Second, &http.Client{})
	if err != nil || c == nil {
		t.Fatalf("with http: %v", err)
	}
	if _, err := url.Parse("http://h/base/query"); err != nil {
		t.Fatal(err)
	}
}

// Only the listener (graphql) client reads a 404 body: the run_operation
// client never does, so its 404 carries no listener reason.
func TestQueryClientNotFoundBodyIsReadOnlyByTheListenerClient(t *testing.T) {
	const refused = `{"errors":[{"message":"m","extensions":{"code":"MCP_REFUSED","reason":"root_field_not_enabled"}}]}`
	u := newOpUpstream(t, func(opRecorded) (int, string) { return 404, refused })
	call := directread.QueryCall{OrgID: opOrgA, Document: "query X { x }"}

	op, err := directread.NewHTTPQueryClient(u.server.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = op.Execute(context.Background(), call)
	var qe *directread.QueryError
	if !errors.As(err, &qe) || qe.Class != directread.QueryErrorNotFound || qe.ListenerReason != "" {
		t.Fatalf("run_operation client 404: %+v", err)
	}

	gq, err := directread.NewHTTPGraphQLClient(u.server.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = gq.Execute(context.Background(), call)
	if !errors.As(err, &qe) || qe.Class != directread.QueryErrorNotFound || qe.ListenerReason != directread.ListenerNotFoundRootNotEnabled {
		t.Fatalf("graphql client 404: %+v", err)
	}
}
