package directread_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// gqlHarness is a GraphQLRunner wired to the real gate, the real HTTP
// client and the fake MCP listener.
type gqlHarness struct {
	runner   *directread.GraphQLRunner
	listener *fakeMCPListener
	graph    *opGraph
	logs     *bytes.Buffer
	policy   *directread.GraphQLPolicy
}

type gqlHarnessOptions struct {
	limits   *directread.GraphQLLimits
	fake     func(cfg *fakeMCPConfig)
	noGrants bool
	// ownCatalogue builds the policy over a catalogue of its own, so a test
	// that attaches a registry watch to it cannot change the shared default.
	ownCatalogue bool
	recorder     directread.GraphQLQueryRecorder
	// clientTimeout is the MCP listener client's deadline (default 5 s).
	clientTimeout time.Duration
}

func newGQLHarness(t *testing.T, opts gqlHarnessOptions) *gqlHarness {
	t.Helper()
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatalf("DefaultGraphQLPolicy: %v", err)
	}
	if opts.ownCatalogue {
		cat, catErr := directread.LoadCatalogue(directread.EmbeddedCatalogueJSON())
		if catErr != nil {
			t.Fatalf("LoadCatalogue: %v", catErr)
		}
		policy, err = directread.NewGraphQLPolicy(cat, directread.EmbeddedOpsSchema(), directread.DefaultGraphQLLimits())
		if err != nil {
			t.Fatalf("NewGraphQLPolicy: %v", err)
		}
	}
	if opts.limits != nil {
		policy, err = directread.NewGraphQLPolicy(policy.Catalogue(), directread.EmbeddedOpsSchema(), *opts.limits)
		if err != nil {
			t.Fatalf("NewGraphQLPolicy: %v", err)
		}
	}
	cfg := defaultFakeMCPConfig(t)
	if opts.fake != nil {
		opts.fake(&cfg)
	}
	listener := newFakeMCPListener(t, policy, cfg)
	timeout := opts.clientTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	client, err := directread.NewHTTPGraphQLClient(listener.server.URL, timeout)
	if err != nil {
		t.Fatalf("NewHTTPGraphQLClient: %v", err)
	}
	graph := newOpGraph()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(&lockedWriter{w: logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rc := directread.GraphQLRunnerConfig{
		Policy:   policy,
		Gate:     directread.NewSubjectGate(graph, directread.NewSlogRecorder(logger)),
		Client:   client,
		Logger:   logger,
		Recorder: opts.recorder,
		Now:      func() time.Time { return opNow },
	}
	if !opts.noGrants {
		rc.Grants = opDefaultGrants()
	}
	runner, err := directread.NewGraphQLRunner(rc)
	if err != nil {
		t.Fatalf("NewGraphQLRunner: %v", err)
	}
	return &gqlHarness{runner: runner, listener: listener, graph: graph, logs: logs, policy: policy}
}

func (h *gqlHarness) run(t *testing.T, principal storage.Principal, query string, vars map[string]any) directread.GraphQLResponse {
	t.Helper()
	var raw json.RawMessage
	if vars != nil {
		b, err := json.Marshal(vars)
		if err != nil {
			t.Fatalf("marshal variables: %v", err)
		}
		raw = b
	}
	resp, err := h.runner.Run(context.Background(), principal, directread.GraphQLRequest{Query: query, Variables: raw})
	if err != nil {
		t.Fatalf("Run: unexpected error %v", err)
	}
	return resp
}

// wantRefused asserts a refusal with code and ZERO upstream requests.
func (h *gqlHarness) wantRefused(t *testing.T, resp directread.GraphQLResponse, code directread.RefusalCode) {
	t.Helper()
	if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != code {
		got := ""
		if resp.Refusal != nil {
			got = string(resp.Refusal.Code) + " (" + resp.Refusal.Reason + ")"
		}
		t.Fatalf("want refused %s, got call=%s refusal=%s", code, resp.Call, got)
	}
	if n := len(h.listener.requests()); n != 0 {
		t.Fatalf("refused query dispatched %d upstream request(s)", n)
	}
	if resp.Data != nil {
		t.Fatalf("refused answer carries data: %s", resp.Data)
	}
}

// wantServed asserts a served answer, one request, and a listener that
// refused nothing acr sent.
func (h *gqlHarness) wantServed(t *testing.T, resp directread.GraphQLResponse) {
	t.Helper()
	if resp.Call != directread.CallServed {
		got := ""
		if resp.Refusal != nil {
			got = string(resp.Refusal.Code) + " (" + resp.Refusal.Reason + ")"
		}
		t.Fatalf("want served, got call=%s refusal=%s errors=%v listener refusals=%v", resp.Call, got, resp.Errors, h.listener.refusals())
	}
	if n := len(h.listener.requests()); n != 1 {
		t.Fatalf("served query sent %d upstream requests, want 1", n)
	}
	if refused := h.listener.refusals(); len(refused) != 0 {
		t.Fatalf("the MCP listener refused what acr sent: %v", refused)
	}
}

// gqlQuery is a generated client query and its variables.
type gqlQuery struct {
	text string
	vars map[string]any
}

// gqlQueryFor builds a client query for one candidate operation of a root
// field FROM ITS POLICY: the registered document's arguments (orgId and
// forced paths left to acr), the given document variables as GraphQL
// variables with the document's own types, and a selection of every
// allowed output path (or only the given paths).
func gqlQueryFor(t *testing.T, op *directread.OperationPolicy, vars map[string]any, selection []string, alias string) gqlQuery {
	t.Helper()
	doc, err := parser.ParseQuery(&ast.Source{Input: op.DocumentText})
	if err != nil {
		t.Fatalf("%s: parse registered document: %v", op.Name, err)
	}
	def := doc.Operations[0]
	root := def.SelectionSet[0].(*ast.Field)
	types := map[string]string{}
	for _, v := range def.VariableDefinitions {
		types[v.Variable] = v.Type.String()
	}
	var defs, args []string
	outVars := map[string]any{}
	for _, a := range root.Arguments {
		if a.Value.Kind != ast.Variable {
			args = append(args, a.Name+": "+a.Value.String())
			continue
		}
		name := a.Value.Raw
		value, ok := vars[name]
		if !ok {
			continue
		}
		defs = append(defs, "$"+name+": "+types[name])
		args = append(args, a.Name+": $"+name)
		outVars[name] = value
	}
	if selection == nil {
		for _, out := range op.Outputs {
			selection = append(selection, out.Path)
		}
	}
	head := "query Client"
	if len(defs) > 0 {
		head += "(" + strings.Join(defs, ", ") + ")"
	}
	field := root.Name
	if alias != "" {
		field = alias + ": " + root.Name
	}
	if len(args) > 0 {
		field += "(" + strings.Join(args, ", ") + ")"
	}
	return gqlQuery{text: head + " { " + field + " " + selectionText(t, root.Name, selection) + " }", vars: outVars}
}

// selectionText prints a selection set from generalized output paths
// rooted at root ("hotspots.rows[*].repoId" -> { rows { repoId } }).
func selectionText(t *testing.T, root string, paths []string) string {
	t.Helper()
	type node struct {
		order    []string
		children map[string]*node
	}
	top := &node{children: map[string]*node{}}
	for _, p := range paths {
		segs := strings.Split(strings.ReplaceAll(p, "[*]", ""), ".")
		if segs[0] != root {
			t.Fatalf("path %q is not under root %q", p, root)
		}
		cur := top
		for _, s := range segs[1:] {
			next, ok := cur.children[s]
			if !ok {
				next = &node{children: map[string]*node{}}
				cur.children[s] = next
				cur.order = append(cur.order, s)
			}
			cur = next
		}
	}
	var print func(n *node) string
	print = func(n *node) string {
		parts := make([]string, 0, len(n.order))
		for _, name := range n.order {
			child := n.children[name]
			if len(child.order) == 0 {
				parts = append(parts, name)
			} else {
				parts = append(parts, name+" "+print(child))
			}
		}
		return "{ " + strings.Join(parts, " ") + " }"
	}
	return print(top)
}

// gqlRootOps maps each allowed root field to its candidate operations.
func gqlRootOps(t *testing.T, policy *directread.GraphQLPolicy) map[string][]*directread.OperationPolicy {
	t.Helper()
	out := map[string][]*directread.OperationPolicy{}
	for _, root := range policy.Roots() {
		for _, name := range root.Operations() {
			op, refusal := policy.Catalogue().Lookup(name)
			if refusal != nil {
				t.Fatalf("root %s: candidate %s not served: %v", root.Field, name, refusal)
			}
			out[root.Field] = append(out[root.Field], op)
		}
	}
	return out
}

// gqlDecode decodes a served answer's data.
func gqlDecode(t *testing.T, data json.RawMessage) map[string]any {
	t.Helper()
	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode data %s: %v", data, err)
	}
	return out
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
