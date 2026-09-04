package contextfabric

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// EVERY CONSUMER OF A QUESTION HASH IS REGISTERED, WITH ITS DISPOSITION.
//
// WHY THIS EXISTS RATHER THAN A SENTENCE IN A DOC COMMENT. The same defect --
// a question with no identity ("?", "!!", "..." all canonicalize to "" and
// share one hash) treated as a real question -- has now been found at THREE
// separate seams, on three separate occasions: answer reuse, the carry
// containment, and the structure priors path. Each was fixed where it was
// found, and each time the next seam was left to be discovered by the next
// review.
//
// "Every future consumer must call IdentitylessQuestionHash" is a true and
// useful sentence, and a sentence is exactly what failed twice. A disclosure
// nothing verifies is indistinguishable from an omission. So the rule is
// ENFORCED here: every call to QuestionHash or CanonicalizeQuestion in
// non-test code across the whole module must have its enclosing function
// registered below with a stated disposition. A new consumer fails this test
// on the day it is written, and the author must decide -- in review -- which
// of the four dispositions it has.
//
// This cannot check that a guard is CORRECT; it checks that the question was
// ASKED. That is the property that was missing.
func TestQuestionHashConsumers_EveryCallSiteHasADeclaredDisposition(t *testing.T) {
	t.Parallel()

	type disposition struct {
		kind string // guards | guarded-upstream | not-serving | identity-safe
		why  string
	}

	// THE REGISTRY. Keyed by the enclosing function's receiver-qualified name.
	registry := map[string]disposition{
		// --- the hash and its predicate ---
		"QuestionHash":             {"identity-safe", "the hash function itself; it is what every other site keys on"},
		"IdentitylessQuestionHash": {"identity-safe", "the predicate itself"},

		// --- ASKS THE QUESTION AND FAILS CLOSED ---
		"Engine.carryOriginSameQuestionVerdict": {"guards",
			"refuses a carry when EITHER the request's or the origin's question has no identity (chaos4360_carry.go)"},
		"Engine.recordStructureConfirmationOutcome": {"guards",
			"the sole emitter of structure-selection events; drops any built under the identityless hash before the sink sees it (structure.go)"},
		"Engine.fetchPriorEntries": {"guards",
			"refuses the structure-priors lookup BEFORE issuing it, so a store holding identityless rows cannot serve them (priors_consult.go)"},
		"Engine.captureClarificationSelection": {"guards",
			"refuses to capture a clarification selection under the identityless hash; curation turns these rows into priors (engine.go)"},
		"Engine.tryReuse": {"guards",
			"refuses the reuse lookup; the original instance of this class, fixed at its own earlier review (answer_reuse.go)"},
		"Store.reuseColumnsFor": {"guards",
			"refuses to write reuse columns, the save-side twin of tryReuse (pginvestigation/store.go)"},

		// --- PROTECTED BY A GUARD THAT ALREADY RAN ---
		"Engine.buildStructureSelectionEvent": {"guarded-upstream",
			"builds the event but never emits it; recordStructureConfirmationOutcome is the sole emitter and drops identityless events there (structure.go)"},
		"Engine.Investigate": {"guarded-upstream",
			"computes the hash once and hands it to fetchPriorEntries, which guards; Investigate itself performs no keyed lookup with it"},

		// --- NOT AN IDENTITY KEY AT ALL ---
		"Runtime.interpretQuestionWithSample": {"identity-safe",
			"uses the hash as a deterministic DECODING SEED for model sampling, never to retrieve or inherit anything: two questions sharing a seed sample identically, which carries no data between them"},

		// --- NOT A LIVE SERVING PATH ---
		"Run": {"not-serving",
			"panel-harness manifest field, written for offline replay provenance; nothing is retrieved or inherited by it (internal/panelharness)"},
	}

	calls := questionHashCallSites(t, moduleRoot(t))

	// SALTED POSITIVE: the walk must find the sites we know exist, or an
	// empty result reads as "no unregistered consumers" when it means the
	// walk is broken.
	if len(calls) < 5 {
		t.Fatalf("found only %d question-hash call site(s) across the module -- the walk is broken, so 'every site is registered' would be vacuous", len(calls))
	}

	var unregistered []string
	seen := map[string]bool{}
	for _, site := range calls {
		seen[site.enclosing] = true
		if _, ok := registry[site.enclosing]; !ok {
			unregistered = append(unregistered, site.String())
		}
	}
	sort.Strings(unregistered)
	for _, site := range unregistered {
		t.Errorf("%s touches question identity (QuestionHash / CanonicalizeQuestion / IdentitylessQuestionHash) and is not registered.\n"+
			"Every consumer of a question hash must declare how it handles a question with NO IDENTITY "+
			"(\"?\", \"!!\" and \"...\" all canonicalize to \"\" and share one hash).\n"+
			"Add it to the registry in this test with one of: guards (it calls IdentitylessQuestionHash and fails closed) / "+
			"guarded-upstream (name the guard that already ran) / not-serving (offline or harness only) / identity-safe.\n"+
			"This has been a live defect at three separate seams; the registry is what stops the fourth being found by a review instead of by a test.", site)
	}

	// A registry entry whose site is GONE is stale, and a stale registry
	// silently shrinks what this test covers.
	for name := range registry {
		if !seen[name] {
			t.Errorf("registry entry %q no longer matches any call site: remove it, or the registry is documenting a consumer that does not exist", name)
		}
	}
}

type questionHashSite struct {
	file      string
	line      int
	enclosing string
}

func (s questionHashSite) String() string {
	return s.enclosing + " (" + s.file + ":" + itoa(s.line) + ")"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// moduleRoot walks up from the test's working directory to the go.mod, so the
// sweep covers the WHOLE module rather than this package. The defect's history
// is that it appears at a seam nobody was looking at, so a package-scoped
// check would reproduce the failure it exists to prevent.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolving the working directory: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("no go.mod found above the working directory, so the module-wide sweep cannot run")
	return ""
}

// questionHashCallSites finds every call to QuestionHash, CanonicalizeQuestion
// or IdentitylessQuestionHash in non-test Go files under root, qualified or
// not. The predicate is included deliberately -- see the callee check below.
func questionHashCallSites(t *testing.T, root string) []questionHashSite {
	t.Helper()
	var sites []questionHashSite
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Vendored and hidden trees are not this module's own code.
			if name := info.Name(); name == "vendor" || strings.HasPrefix(name, ".") && name != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // a file this walk cannot parse is not a silent pass; see the count guard
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := qualifiedFuncName(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var callee string
				switch target := call.Fun.(type) {
				case *ast.Ident:
					callee = target.Name
				case *ast.SelectorExpr:
					callee = target.Sel.Name
				}
				// IdentitylessQuestionHash counts as a call site too, and
				// leaving it out was an error this test caught in its own
				// registry: a function that calls only the PREDICATE is a
				// question-identity consumer -- in fact it is the most
				// important kind, since it is a guard -- and a population that
				// excluded guards would drop a site the moment it was fixed.
				if callee != "QuestionHash" && callee != "CanonicalizeQuestion" && callee != "IdentitylessQuestionHash" {
					return true
				}
				pos := fset.Position(call.Pos())
				rel, rerr := filepath.Rel(root, pos.Filename)
				if rerr != nil {
					rel = pos.Filename
				}
				sites = append(sites, questionHashSite{file: rel, line: pos.Line, enclosing: name})
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return sites
}
