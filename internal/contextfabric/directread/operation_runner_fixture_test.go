package directread_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Fixture: two organizations. Org A holds repositories A (acme/a) and B
// (acme/b) and team T (owns A and B). Org B holds repository D. A
// restricted principal of org A is granted acme/a only.
const (
	opOrgA  = "11111111-1111-4111-8111-111111111111"
	opOrgB  = "22222222-2222-4222-8222-222222222222"
	opRepoA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	opRepoB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	opRepoC = "cccccccc-cccc-4ccc-8ccc-cccccccccccc" // in no graph
	opRepoD = "dddddddd-dddd-4ddd-8ddd-dddddddddddd" // org B only
	opTeamT = "team:t1"
)

var opNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func opRepo(id string) string { return "repository:" + id }

func opUnrestricted(org string) storage.Principal {
	return storage.Principal{OrgID: org, Subject: "user-" + org[:4], CredentialID: "cred-" + org[:4]}
}

func opRestrictedA() storage.Principal {
	return storage.Principal{OrgID: opOrgA, Subject: "user-r", CredentialID: "cred-r", RepositoryScopes: []string{"acme/a"}}
}

// opGraph is a two-organization graph authority. The per-node decision is
// the real shared predicate (graphrank.AuthorizeStoredSubjectNodes).
type opGraph struct {
	nodes map[string]map[string]map[string]interface{} // org -> SubjectKey -> attributes
	reach map[string]map[string][]string
	calls int
	mu    sync.Mutex
}

func newOpGraph() *opGraph {
	repo := func(slug string) map[string]interface{} {
		return map[string]interface{}{"authorization_repositories": []string{slug}}
	}
	key := func(kind contextfabric.SubjectKind, id string) string {
		return graphrank.SubjectKey(contextfabric.SubjectRef{Kind: kind, CanonicalID: id})
	}
	return &opGraph{
		nodes: map[string]map[string]map[string]interface{}{
			opOrgA: {
				key(contractsv1.ContextFabricSubjectRepository, opRepo(opRepoA)): repo("acme/a"),
				key(contractsv1.ContextFabricSubjectRepository, opRepo(opRepoB)): repo("acme/b"),
				key(contractsv1.ContextFabricSubjectTeam, opTeamT):               {"authorization_repositories": []string{"acme/a", "acme/b"}},
			},
			opOrgB: {
				key(contractsv1.ContextFabricSubjectRepository, opRepo(opRepoD)): repo("other/d"),
			},
		},
		reach: map[string]map[string][]string{
			opOrgA: {key(contractsv1.ContextFabricSubjectTeam, opTeamT): {"acme/a", "acme/b"}},
		},
	}
}

func (g *opGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "op", Epoch: 1}, nil
}

func (g *opGraph) AuthorizeStoredSubjects(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	nodes := map[string][]graphrank.CandidateNode{}
	for _, subject := range subjects {
		if attributes, ok := g.nodes[principal.OrgID][graphrank.SubjectKey(subject)]; ok {
			nodes[graphrank.SubjectKey(subject)] = []graphrank.CandidateNode{{Attributes: attributes}}
		}
	}
	return graphrank.AuthorizeStoredSubjectNodes(principal, subjects, nodes), nil
}

func (g *opGraph) OwnershipReachedRepositories(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	out := make([][]string, len(subjects))
	for i, subject := range subjects {
		out[i] = g.reach[principal.OrgID][graphrank.SubjectKey(subject)]
	}
	return out, nil
}

func (g *opGraph) gateCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// opGrants is the GrantedRepositories port: it offers every repository of
// the caller's organization as a candidate; the gate must cut it to the
// grant.
type opGrants struct {
	refs map[string][]contextfabric.SubjectRef
}

func (g opGrants) GrantedRepositories(_ context.Context, principal storage.Principal) ([]contextfabric.SubjectRef, error) {
	return g.refs[principal.OrgID], nil
}

func repoRef(id string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: opRepo(id)}
}

func opDefaultGrants() opGrants {
	repo := repoRef
	return opGrants{refs: map[string][]contextfabric.SubjectRef{
		opOrgA: {repo(opRepoA), repo(opRepoB)},
		opOrgB: {repo(opRepoD)},
	}}
}

// opRecorded is one request the fake query service saw.
type opRecorded struct {
	Header http.Header
	Raw    []byte
	Body   map[string]any
}

func (r opRecorded) variables() map[string]any {
	vars, _ := r.Body["variables"].(map[string]any)
	return vars
}

// opUpstream is a fake ops query service that records every request.
type opUpstream struct {
	server  *httptest.Server
	mu      sync.Mutex
	records []opRecorded
	respond func(rec opRecorded) (int, string)
}

func newOpUpstream(t *testing.T, respond func(rec opRecorded) (int, string)) *opUpstream {
	t.Helper()
	u := &opUpstream{respond: respond}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec := opRecorded{Header: r.Header.Clone(), Raw: raw}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		_ = dec.Decode(&rec.Body)
		if r.URL.Path != "/query" || r.Method != http.MethodPost {
			rec.Header.Set("X-Test-Wrong-Route", r.Method+" "+r.URL.Path)
		}
		u.mu.Lock()
		u.records = append(u.records, rec)
		u.mu.Unlock()
		status, body := u.respond(rec)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *opUpstream) requests() []opRecorded {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]opRecorded(nil), u.records...)
}

func (u *opUpstream) reset() {
	u.mu.Lock()
	u.records = nil
	u.mu.Unlock()
}

// opHarness is a runner wired to the real gate, the real HTTP client and a
// fake query service.
type opHarness struct {
	runner   *directread.OperationRunner
	upstream *opUpstream
	graph    *opGraph
	logs     *bytes.Buffer
	cat      *directread.Catalogue
}

type opHarnessOptions struct {
	noGrants bool
	grants   directread.GrantedRepositories
	timeout  time.Duration
}

func newOpHarness(t *testing.T, respond func(rec opRecorded) (int, string), opts opHarnessOptions) *opHarness {
	t.Helper()
	cat, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatalf("DefaultCatalogue: %v", err)
	}
	upstream := newOpUpstream(t, respond)
	timeout := opts.timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	client, err := directread.NewHTTPQueryClient(upstream.server.URL, timeout)
	if err != nil {
		t.Fatalf("NewHTTPQueryClient: %v", err)
	}
	graph := newOpGraph()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(&lockedWriter{w: logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := directread.OperationRunnerConfig{
		Catalogue: cat,
		Gate:      directread.NewSubjectGate(graph, directread.NewSlogRecorder(logger)),
		Client:    client,
		Logger:    logger,
		Now:       func() time.Time { return opNow },
	}
	switch {
	case opts.grants != nil:
		cfg.Grants = opts.grants
	case !opts.noGrants:
		cfg.Grants = opDefaultGrants()
	}
	runner, err := directread.NewOperationRunner(cfg)
	if err != nil {
		t.Fatalf("NewOperationRunner: %v", err)
	}
	return &opHarness{runner: runner, upstream: upstream, graph: graph, logs: logs, cat: cat}
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (h *opHarness) run(t *testing.T, principal storage.Principal, operation string, vars any) directread.OperationResponse {
	t.Helper()
	raw, err := json.Marshal(vars)
	if err != nil {
		t.Fatalf("marshal variables: %v", err)
	}
	resp, err := h.runner.Run(context.Background(), principal, directread.OperationRequest{Operation: operation, Variables: raw})
	if err != nil {
		t.Fatalf("Run(%s): unexpected error %v", operation, err)
	}
	return resp
}

// ------------------------------------------------------ tree builders

// opBuild returns a variables tree holding leaf at a generalized path; a
// [*] segment becomes a one-element list.
func opBuild(path string, leaf any) map[string]any {
	root := map[string]any{}
	opPlace(root, strings.Split(path, "."), leaf)
	return root
}

func opPlace(cur map[string]any, segs []string, leaf any) {
	seg := segs[0]
	name, array := strings.CutSuffix(seg, "[*]")
	if len(segs) == 1 {
		if array {
			cur[name] = []any{leaf}
		} else {
			cur[name] = leaf
		}
		return
	}
	child := map[string]any{}
	if array {
		if existing, ok := cur[name].([]any); ok && len(existing) == 1 {
			if m, ok := existing[0].(map[string]any); ok {
				child = m
			}
		}
		cur[name] = []any{child}
	} else {
		if existing, ok := cur[name].(map[string]any); ok {
			child = existing
		}
		cur[name] = child
	}
	opPlace(child, segs[1:], leaf)
}

// opMerge places leaf at path inside an existing tree.
func opMerge(root map[string]any, path string, leaf any) map[string]any {
	opPlace(root, strings.Split(path, "."), leaf)
	return root
}

// opLookup reads a path with no [*] from a decoded tree.
func opLookup(root any, path string) (any, bool) {
	cur := root
	for _, seg := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// opRootField is the operation's root response field.
func opRootField(op *directread.OperationPolicy) string {
	root, _, _ := strings.Cut(op.Outputs[0].Path, ".")
	return strings.TrimSuffix(root, "[*]")
}

// opNullAnswer is a valid empty answer for any operation.
func opNullAnswer(op *directread.OperationPolicy) string {
	return `{"data":{"` + opRootField(op) + `":null}}`
}

// opRowsAnswer builds an answer whose row id path carries the given ids,
// one row per id: "hotspots.rows[*].repoId" -> {"hotspots":{"rows":[{"repoId":id}]}}.
func opRowsAnswer(rowIDPath string, ids []any) string {
	before, after, _ := strings.Cut(rowIDPath, "[*]")
	after = strings.TrimPrefix(after, ".")
	rows := make([]any, 0, len(ids))
	for _, id := range ids {
		row := map[string]any{}
		if id != opMissing {
			opPlace(row, strings.Split(after, "."), id)
		}
		rows = append(rows, row)
	}
	data := opBuild(before, rows)
	raw, _ := json.Marshal(map[string]any{"data": data})
	return string(raw)
}

// opMissing marks a row without an id field.
const opMissing = "\x00missing"

func opDate(t time.Time, typ string) string {
	if strings.Contains(typ, "DateTime") {
		return t.Format(time.RFC3339)
	}
	return t.Format("2006-01-02")
}

// opMinimalVariables builds a request every operation serves to an
// unrestricted caller of org A, derived from the policy: every required
// path gets a value and every closed window gets a 7-day span.
func opMinimalVariables(t *testing.T, op *directread.OperationPolicy) map[string]any {
	t.Helper()
	vars := map[string]any{}
	for _, con := range op.Constraints {
		switch con.Kind {
		case directread.ConstraintRequired:
			rule, _ := op.Variable(con.Path)
			var value any = "x"
			if rule.Subject != nil && rule.Subject.Kind == directread.SubjectKindTeam {
				value = opTeamT
			} else if rule.Kind == "object" {
				value = map[string]any{}
			} else if len(rule.AllowedValues) > 0 {
				value = rule.AllowedValues[0]
			} else if rule.Min != nil {
				value = *rule.Min
			}
			opMerge(vars, con.Path, value)
		case directread.ConstraintWindowMaxDays:
			if con.AllowOpenStart || strings.Contains(con.Path, "[*]") {
				continue
			}
			rule, _ := op.Variable(con.Path)
			opMerge(vars, con.Path, opDate(opNow.AddDate(0, 0, -7), rule.Type))
			opMerge(vars, con.Other, opDate(opNow, rule.Type))
		}
	}
	return vars
}
