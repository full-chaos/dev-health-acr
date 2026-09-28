package directread

import (
	"context"
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

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

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
	authorized, _ := NewSubjectGate(graphOfOrgA(), nil).Authorize(context.Background(), principal, []contextfabric.SubjectRef{repoA, repoB, teamT})
	source := &recordingSource{}
	forged := contextfabric.CanonicalFactRequest{
		Subjects:     []contextfabric.SubjectRef{repoB, guessed},
		Cohort:       &contextfabric.Cohort{},
		Requirements: []contextfabric.FactRequirement{{Kind: "health", Subjects: []contextfabric.SubjectRef{repoA}}},
	}
	if _, err := NewFactReader(source).Read(context.Background(), principal, authorized, forged); err != nil {
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
	authorized, _ := NewSubjectGate(graphOfOrgA(), nil).Authorize(context.Background(), principal, []contextfabric.SubjectRef{repoA})
	denied, _ := NewSubjectGate(graphOfOrgA(), nil).Authorize(context.Background(), principal, []contextfabric.SubjectRef{repoB})
	other := func(mutate func(*storage.Principal)) storage.Principal {
		p := restrictedToA()
		p.RepositoryScopes = slices.Clone(p.RepositoryScopes)
		mutate(&p)
		return p
	}
	cases := map[string]struct {
		principal storage.Principal
		subjects  AuthorizedSubjects
		request   contextfabric.CanonicalFactRequest
	}{
		"zero value":            {principal, AuthorizedSubjects{}, contextfabric.CanonicalFactRequest{}},
		"denied decision":       {principal, denied, contextfabric.CanonicalFactRequest{}},
		"other organization":    {other(func(p *storage.Principal) { p.OrgID = orgB }), authorized, contextfabric.CanonicalFactRequest{}},
		"other subject":         {other(func(p *storage.Principal) { p.Subject = "user-2" }), authorized, contextfabric.CanonicalFactRequest{}},
		"other credential":      {other(func(p *storage.Principal) { p.CredentialID = "cred-2" }), authorized, contextfabric.CanonicalFactRequest{}},
		"widened grant":         {other(func(p *storage.Principal) { p.RepositoryScopes = append(p.RepositoryScopes, "acme/b") }), authorized, contextfabric.CanonicalFactRequest{}},
		"requirement smuggling": {principal, authorized, contextfabric.CanonicalFactRequest{Requirements: []contextfabric.FactRequirement{{Kind: "health", Subjects: []contextfabric.SubjectRef{repoB}}}}},
	}
	for name, tc := range cases {
		source := &recordingSource{}
		_, err := NewFactReader(source).Read(context.Background(), tc.principal, tc.subjects, tc.request)
		if !errors.Is(err, ErrUngatedRead) || len(source.requests) != 0 {
			t.Errorf("%s: err %v, %d registry reads", name, err, len(source.requests))
		}
	}
	if _, err := NewFactReader(nil).Read(context.Background(), principal, authorized, contextfabric.CanonicalFactRequest{}); err == nil {
		t.Fatal("reader without a source served")
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
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == registryReadMethod {
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

// Rule 2: the guard must catch a planted violation in a tool-shaped package.
func TestRegistryReadGuardCatchesAPlantedCall(t *testing.T) {
	root := t.TempDir()
	for rel, body := range map[string]string{
		"internal/api/data_facts_routes.go":      "package api\nfunc f(r interface{ ReadFacts() }) { r.ReadFacts() }\n",
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
	if files != 1 || !slices.Equal(violations, []string{"internal/api/data_facts_routes.go"}) {
		t.Fatalf("planted scan: %d files, violations %v", files, violations)
	}
}
