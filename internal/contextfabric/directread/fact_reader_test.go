package directread

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// inRequest is a context carrying one request id (req_ + 32 hex, the only
// shape observability accepts), derived from name, as the API middleware
// sets one for every request.
func inRequest(name string) context.Context {
	sum := sha256.Sum256([]byte(name))
	ctx := observability.WithRequestID(context.Background(), "req_"+hex.EncodeToString(sum[:16]))
	if _, ok := observability.RequestIDFromContext(ctx); !ok {
		panic("test request id rejected: " + name)
	}
	return ctx
}

type recordingSource struct {
	requests []contextfabric.CanonicalFactRequest
}

func (s *recordingSource) ReadFacts(_ context.Context, _ storage.Principal, request contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	s.requests = append(s.requests, request)
	return contextfabric.CanonicalFactBundle{}, nil
}

// Rule 1: the registry sees ONLY the subjects the gate admitted for this
// caller, whatever the tool put in the request.
func TestFactReaderReadsOnlyGateAdmittedSubjects(t *testing.T) {
	principal := restrictedToA()
	ctx := inRequest("req_admitted_subjects")
	authorized, _ := NewSubjectGate(graphOfOrgA(), nil).Authorize(ctx, principal, []contextfabric.SubjectRef{repoA, repoB, teamT})
	source := &recordingSource{}
	forged := contextfabric.CanonicalFactRequest{
		Subjects:     []contextfabric.SubjectRef{repoB, guessed},
		Cohort:       &contextfabric.Cohort{},
		Requirements: []contextfabric.FactRequirement{{Kind: "health", Subjects: []contextfabric.SubjectRef{repoA}}},
	}
	if _, err := NewFactReader(source).Read(ctx, principal, authorized, forged); err != nil {
		t.Fatal(err)
	}
	if len(source.requests) != 1 {
		t.Fatalf("%d registry reads", len(source.requests))
	}
	got := source.requests[0]
	if !slices.Equal(got.Subjects, []contextfabric.SubjectRef{repoA, teamT}) || got.Cohort != nil || got.Scope != nil {
		t.Fatalf("registry request subjects %v cohort %v scope %v", got.Subjects, got.Cohort, got.Scope)
	}
}

// Every guard of Read, one case each, and zero registry reads on a refusal.
func TestFactReaderRefusesUngatedReads(t *testing.T) {
	principal := restrictedToA()
	ctx := inRequest("req_ungated_reads")
	authorized, _ := NewSubjectGate(graphOfOrgA(), nil).Authorize(ctx, principal, []contextfabric.SubjectRef{repoA})
	denied, _ := NewSubjectGate(graphOfOrgA(), nil).Authorize(ctx, principal, []contextfabric.SubjectRef{repoB})
	unbound, _ := NewSubjectGate(graphOfOrgA(), nil).Authorize(context.Background(), principal, []contextfabric.SubjectRef{repoA})
	other := func(mutate func(*storage.Principal)) storage.Principal {
		p := restrictedToA()
		p.RepositoryScopes = slices.Clone(p.RepositoryScopes)
		mutate(&p)
		return p
	}
	cases := map[string]struct {
		ctx       context.Context
		principal storage.Principal
		subjects  AuthorizedSubjects
		request   contextfabric.CanonicalFactRequest
	}{
		"zero value":             {ctx, principal, AuthorizedSubjects{}, contextfabric.CanonicalFactRequest{}},
		"denied decision":        {ctx, principal, denied, contextfabric.CanonicalFactRequest{}},
		"other organization":     {ctx, other(func(p *storage.Principal) { p.OrgID = orgB }), authorized, contextfabric.CanonicalFactRequest{}},
		"other subject":          {ctx, other(func(p *storage.Principal) { p.Subject = "user-2" }), authorized, contextfabric.CanonicalFactRequest{}},
		"other credential":       {ctx, other(func(p *storage.Principal) { p.CredentialID = "cred-2" }), authorized, contextfabric.CanonicalFactRequest{}},
		"widened grant":          {ctx, other(func(p *storage.Principal) { p.RepositoryScopes = append(p.RepositoryScopes, "acme/b") }), authorized, contextfabric.CanonicalFactRequest{}},
		"requirement smuggling":  {ctx, principal, authorized, contextfabric.CanonicalFactRequest{Requirements: []contextfabric.FactRequirement{{Kind: "health", Subjects: []contextfabric.SubjectRef{repoB}}}}},
		"other request":          {inRequest("req_another_request"), principal, authorized, contextfabric.CanonicalFactRequest{}},
		"read outside request":   {context.Background(), principal, authorized, contextfabric.CanonicalFactRequest{}},
		"issued outside request": {ctx, principal, unbound, contextfabric.CanonicalFactRequest{}},
	}
	for name, tc := range cases {
		source := &recordingSource{}
		_, err := NewFactReader(source).Read(tc.ctx, tc.principal, tc.subjects, tc.request)
		if !errors.Is(err, ErrUngatedRead) || len(source.requests) != 0 {
			t.Errorf("%s: err %v, %d registry reads", name, err, len(source.requests))
		}
	}
	if _, err := NewFactReader(nil).Read(ctx, principal, authorized, contextfabric.CanonicalFactRequest{}); err == nil {
		t.Fatal("reader without a source served")
	}
	// None of the refusals above spent the decision: it still reads once.
	source := &recordingSource{}
	if _, err := NewFactReader(source).Read(ctx, principal, authorized, contextfabric.CanonicalFactRequest{}); err != nil || len(source.requests) != 1 {
		t.Fatalf("good read after refusals: err %v, %d registry reads", err, len(source.requests))
	}
}

// North Star check 18: authorization is re-checked live every turn. A gate
// decision cannot be replayed: not after its request, not after
// AuthorizationTTL, not a second time (copies included), and so never after
// the graph withdrew the access it proved.
func TestFactReaderRefusesAStaleOrReplayedDecision(t *testing.T) {
	principal := restrictedToA()
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

	// The reviewer's scenario: project Q reaches granted acme/a when the gate
	// decides; then Q's ownership moves so it reaches only acme/b.
	graph := graphOfOrgA()
	gate := NewSubjectGate(graph, nil)
	gate.now = func() time.Time { return t0 }
	first := inRequest("req_turn_one")
	proof, decision := gate.Authorize(first, principal, []contextfabric.SubjectRef{projectQ})
	if decision.Decision != DecisionAdmitted || proof.Len() != 1 {
		t.Fatalf("turn one: decision %s, %d admitted", decision.Decision, proof.Len())
	}
	graph.reach[graphrank.SubjectKey(projectQ)] = []string{"acme/b"}
	second := inRequest("req_turn_two")
	if fresh, decision := gate.Authorize(second, principal, []contextfabric.SubjectRef{projectQ}); fresh.Len() != 0 || decision.Decision == DecisionAdmitted {
		t.Fatalf("turn two fresh decision: %s, %d admitted, want refused", decision.Decision, fresh.Len())
	}
	source := &recordingSource{}
	reader := NewFactReader(source)
	reader.now = func() time.Time { return t0.Add(time.Second) }
	if _, err := reader.Read(second, principal, proof, contextfabric.CanonicalFactRequest{}); !errors.Is(err, ErrUngatedRead) || len(source.requests) != 0 {
		t.Fatalf("turn-one proof replayed in turn two: err %v, %d registry reads", err, len(source.requests))
	}

	// Expiry: valid until AuthorizationTTL after issue, refused from then on.
	for _, tc := range []struct {
		name    string
		at      time.Time
		wantErr error
	}{
		{"at the TTL", t0.Add(AuthorizationTTL), ErrAuthorizationExpired},
		{"after the TTL", t0.Add(AuthorizationTTL + time.Minute), ErrAuthorizationExpired},
		{"just inside the TTL", t0.Add(AuthorizationTTL - time.Nanosecond), nil},
	} {
		ctx := inRequest("req_expiry_" + strings.ReplaceAll(tc.name, " ", "_"))
		proof, _ := gate.Authorize(ctx, principal, []contextfabric.SubjectRef{repoA})
		source := &recordingSource{}
		reader := NewFactReader(source)
		reader.now = func() time.Time { return tc.at }
		_, err := reader.Read(ctx, principal, proof, contextfabric.CanonicalFactRequest{})
		wantReads := 0
		if tc.wantErr == nil {
			wantReads = 1
		}
		if !errors.Is(err, tc.wantErr) || len(source.requests) != wantReads {
			t.Errorf("%s: err %v, %d registry reads, want %v and %d", tc.name, err, len(source.requests), tc.wantErr, wantReads)
		}
	}

	// Single use: the second read of the same decision, or of a copy, is
	// refused, even inside the same request and TTL.
	ctx := inRequest("req_single_use")
	proof, _ = gate.Authorize(ctx, principal, []contextfabric.SubjectRef{repoA})
	copied := proof
	source = &recordingSource{}
	reader = NewFactReader(source)
	reader.now = func() time.Time { return t0 }
	if _, err := reader.Read(ctx, principal, proof, contextfabric.CanonicalFactRequest{}); err != nil {
		t.Fatalf("first read: %v", err)
	}
	for name, value := range map[string]AuthorizedSubjects{"same value": proof, "copy": copied} {
		if _, err := reader.Read(ctx, principal, value, contextfabric.CanonicalFactRequest{}); !errors.Is(err, ErrAuthorizationSpent) {
			t.Errorf("%s: second read err %v, want ErrAuthorizationSpent", name, err)
		}
	}
	if len(source.requests) != 1 {
		t.Fatalf("%d registry reads for one decision, want 1", len(source.requests))
	}
}

// The registry method a direct tool must never call itself.
const registryReadMethod = "ReadFacts"

// scanRegistryReads parses every non-test Go file under root, except the
// directories allowed to hold the registry (internal/contextfabric and its
// subpackages: the registry itself, the engine, and the providers, whose
// provider-level ReadFacts is the registry's own fan-out), and returns each
// call of a method named ReadFacts. It returns the number of files parsed so
// a scan that saw nothing fails loudly.
func scanRegistryReads(t *testing.T, root string, allowed func(rel string) bool) (violations []string, files int) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if name := entry.Name(); rel != "." && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			if allowed(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		files++
		// Every selector that names the method counts, not only a direct
		// call: a method value (read := r.ReadFacts; read()) and a method
		// expression ((*Registry).ReadFacts) reach the registry just as well.
		ast.Inspect(parsed, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok && selector.Sel.Name == registryReadMethod {
				violations = append(violations, rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return violations, files
}

func contextFabricTree(rel string) bool {
	return rel == "internal/contextfabric" || strings.HasPrefix(rel, "internal/contextfabric/")
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

// Construction rule (CHAOS-7071): no production code outside
// internal/contextfabric calls ReadFacts. A direct tool lives in
// internal/api or internal/mcp; it reaches facts only through
// FactReader.Read, which needs a gate-issued AuthorizedSubjects.
func TestNoDirectRegistryReadOutsideContextFabric(t *testing.T) {
	root := repositoryRoot(t)
	violations, files := scanRegistryReads(t, root, contextFabricTree)
	// Rule 4: a scan that parsed (almost) nothing is not a pass.
	if files < 200 {
		t.Fatalf("scan parsed only %d production files under %s; the guard did not measure", files, root)
	}
	for _, required := range []string{"internal/api", "internal/mcp", "cmd"} {
		if _, err := os.Stat(filepath.Join(root, required)); err != nil {
			t.Fatalf("expected production tree %s missing: %v", required, err)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("ReadFacts called outside internal/contextfabric (use directread.FactReader): %v", violations)
	}
}

// Rule 2: the guard must catch a planted violation in a tool-shaped package,
// in each shape that reaches the registry: a direct call, a method value and
// a method expression. The interface declaration of the method is not a
// read and is not flagged.
func TestRegistryReadGuardCatchesAPlantedCall(t *testing.T) {
	root := t.TempDir()
	for rel, body := range map[string]string{
		"internal/api/data_facts_routes.go":      "package api\nfunc f(r interface{ ReadFacts() }) { r.ReadFacts() }\n",
		"internal/api/data_subjects_routes.go":   "package api\nfunc v(r interface{ ReadFacts() }) { read := r.ReadFacts; read() }\n",
		"internal/mcp/data_tool.go":              "package mcp\ntype reg struct{}\nfunc (reg) ReadFacts() {}\nfunc e() { read := reg.ReadFacts; read(reg{}) }\n",
		"internal/api/declares_only.go":          "package api\ntype source interface{ ReadFacts() }\n",
		"internal/contextfabric/engine.go":       "package contextfabric\nfunc g(r interface{ ReadFacts() }) { r.ReadFacts() }\n",
		"internal/api/data_facts_routes_test.go": "package api\nfunc h(r interface{ ReadFacts() }) { r.ReadFacts() }\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	violations, files := scanRegistryReads(t, root, contextFabricTree)
	want := []string{"internal/api/data_facts_routes.go", "internal/api/data_subjects_routes.go", "internal/mcp/data_tool.go"}
	if files != 4 || !slices.Equal(violations, want) {
		t.Fatalf("planted scan: %d files, violations %v, want 4 files and %v", files, violations, want)
	}
}
