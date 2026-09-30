package directread_test

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

func gqlCertify(t *testing.T, line []byte, want map[string]any) {
	t.Helper()
	parsed, err := certify.Parse(line)
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.GraphQLQuery, Want: want}); err != nil {
		t.Fatalf("certify: %v\n%s", err, line)
	}
}

// The "context fabric graphql query" line, certified from the bytes the
// production SlogGraphQLRecorder wrote for a served restricted query, a
// refused root and a refused foreign id. The query text, variable values
// and subject ids never reach the log.
func TestGraphQLQueryLineCertifiesAndLeaksNothing(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
	secret := "secretVariableValue"
	query := `query Secret($since: DateTime!) { hotspots(input: {sinceUtc: $since, untilUtc: "2026-09-28T00:00:00Z", repoIds: ["` + opRepo(opRepoA) + `"]}) { rows { filePath } } }`
	vars, _ := json.Marshal(map[string]any{"since": "2026-09-21T00:00:00Z"})
	resp, err := h.runner.Run(ctx, opRestrictedA(), directread.GraphQLRequest{Query: query, Variables: vars})
	if err != nil || resp.Call != directread.CallServed {
		t.Fatalf("served query: %v %+v", err, resp.Refusal)
	}
	line := opLineOf(t, h.logs.String(), directread.GraphQLQueryLogMessage)
	gqlCertify(t, line, map[string]any{
		"org_id": opOrgA, "caller_class": "restricted", "scope_class": "forced_grant", "decision": "served",
		"root_fields": []any{"hotspots"}, "operations": []any{"hotspots"}, "root_count": 1, "alias_count": 0,
		"depth": 3, "field_count": 3, "complexity": 2, "forced_by_grant": true, "rows_checked": 2, "rows_foreign": 0,
		"paths_removed": 0, "completeness": "unknown", "result": "data", "query_digest": resp.Source.QueryDigest,
		"schema_digest": h.policy.Catalogue().SchemaDigest(), "request_id": "req_0123456789abcdef0123456789abcdef",
	})

	h.logs.Reset()
	if _, err := h.runner.Run(ctx, opRestrictedA(), directread.GraphQLRequest{Query: `{ evil_` + secret + ` { x } }`}); err != nil {
		t.Fatal(err)
	}
	line = opLineOf(t, h.logs.String(), directread.GraphQLQueryLogMessage)
	gqlCertify(t, line, map[string]any{"org_id": opOrgA, "decision": "refused", "refusal_code": "root_field_not_allowed", "root_fields": []any{"unknown"}, "operations": []any{}, "scope_class": "not_reached"})

	h.logs.Reset()
	foreign := `{ hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z", repoIds: ["` + opRepo(opRepoB) + `"]}) { rows { filePath } } }`
	if _, err := h.runner.Run(ctx, opRestrictedA(), directread.GraphQLRequest{Query: foreign}); err != nil {
		t.Fatal(err)
	}
	line = opLineOf(t, h.logs.String(), directread.GraphQLQueryLogMessage)
	gqlCertify(t, line, map[string]any{"org_id": opOrgA, "decision": "refused", "refusal_code": "denied_or_not_found", "root_fields": []any{"hotspots"}})

	for _, leak := range []string{secret, "Secret", "2026-09-21", opRepoA, opRepoB, "filePath"} {
		if strings.Contains(h.logs.String(), leak) {
			t.Fatalf("%q reached the log:\n%s", leak, h.logs.String())
		}
	}
}

// The literal vocabularies of eventspec.GraphQLQuery equal the producer's.
func TestGraphQLQueryEventVocabulariesMatchProducer(t *testing.T) {
	var codes []string
	for _, c := range directread.GraphQLRefusalCodes() {
		codes = append(codes, string(c))
	}
	cat, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	// The operations a graphql_query root can map to: the candidates of the
	// derived root policy (design-excluded roots never appear).
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	_ = cat
	var ops []string
	for _, root := range policy.Roots() {
		ops = append(ops, root.Operations()...)
	}
	var classes, budgets []string
	for _, c := range directread.GraphQLUpstreamErrorClasses() {
		classes = append(classes, string(c))
	}
	for _, b := range directread.ReadBudgetReasonVocabulary() {
		budgets = append(budgets, string(b))
	}
	want := map[string][]string{"refusal_code": codes, "operations": ops, "error_class": classes, "read_budget": budgets}
	seen := 0
	for _, field := range eventspec.GraphQLQuery.Fields {
		expected, ok := want[field.Key]
		if !ok {
			continue
		}
		seen++
		got := append([]string{}, field.ClosedVocabulary...)
		exp := append([]string{}, expected...)
		sort.Strings(got)
		sort.Strings(exp)
		if !slices.Equal(got, exp) {
			t.Errorf("eventspec %s vocabulary %v, producer %v", field.Key, got, exp)
		}
	}
	if seen != len(want) {
		t.Fatalf("checked %d of %d vocabularies", seen, len(want))
	}
	if directread.GraphQLQueryLogMessage != eventspec.GraphQLQuery.Msg {
		t.Fatalf("message %q vs spec %q", directread.GraphQLQueryLogMessage, eventspec.GraphQLQuery.Msg)
	}
	// The shared vocabularies are run_operation's own lists.
	for _, key := range []string{"caller_class", "scope_class", "decision", "completeness", "result"} {
		var a, b []string
		for _, f := range eventspec.GraphQLQuery.Fields {
			if f.Key == key {
				a = f.ClosedVocabulary
			}
		}
		for _, f := range eventspec.OperationRead.Fields {
			if f.Key == key {
				b = f.ClosedVocabulary
			}
		}
		if len(a) == 0 || !slices.Equal(a, b) {
			t.Errorf("%s vocabulary %v differs from run_operation's %v", key, a, b)
		}
	}
}
