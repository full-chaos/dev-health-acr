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

// walkForIdentityRefs reports every reference to a FUNCTION named by a bare
// in-package identifier or by a package-qualified selector, and deliberately
// does NOT report a struct field that happens to share the name.
//
// The two shapes it must tell apart:
//
//	QuestionHash(q) / hash := QuestionHash    the function  -> reported
//	contextfabric.QuestionHash(q)             the function  -> reported
//	event.QuestionHash                        a field read  -> not reported
//	StructureSelectionEvent{QuestionHash: h}  a field name  -> not reported
func walkForIdentityRefs(root ast.Node, imports map[string]bool, report func(ast.Node, string)) {
	var walk func(n ast.Node)
	walk = func(n ast.Node) {
		if n == nil {
			return
		}
		switch node := n.(type) {
		case *ast.SelectorExpr:
			// Only a PACKAGE qualifier names a function here. Anything else is
			// a field read on a value. Either way, do not descend: the Sel is
			// not an independent identifier reference.
			if pkg, ok := node.X.(*ast.Ident); ok && imports[pkg.Name] {
				report(node, node.Sel.Name)
				return
			}
			walk(node.X)
			return
		case *ast.KeyValueExpr:
			// The KEY of a composite literal is a field name, never a
			// reference to a function. Descend into the VALUE only.
			walk(node.Value)
			return
		case *ast.Ident:
			report(node, node.Name)
			return
		}
		ast.Inspect(n, func(child ast.Node) bool {
			if child == nil || child == n {
				return true
			}
			switch child.(type) {
			case *ast.SelectorExpr, *ast.KeyValueExpr, *ast.Ident:
				walk(child)
				return false
			}
			return true
		})
	}
	walk(root)
}

// questionHashCallSites finds every reference to QuestionHash, CanonicalizeQuestion
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
		// The file's own import names, so a package-qualified reference
		// (`contextfabric.QuestionHash`) can be told apart from a STRUCT FIELD
		// read (`event.QuestionHash`). Both are SelectorExprs; only the first
		// touches the function. Without this the walk reported every selection
		// event's QuestionHash FIELD as a consumer -- over-reporting into a
		// different thing entirely, which is noise a reader learns to ignore.
		imports := map[string]bool{}
		for _, spec := range file.Imports {
			if spec.Name != nil {
				imports[spec.Name.Name] = true
				continue
			}
			path := strings.Trim(spec.Path.Value, `"`)
			if i := strings.LastIndex(path, "/"); i >= 0 {
				path = path[i+1:]
			}
			imports[path] = true
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := qualifiedFuncName(fn)
			// An EXPLICIT walk, not ast.Inspect's blanket descent.
			//
			// ast.Inspect visits the Sel of every SelectorExpr and the Key of
			// every KeyValueExpr as bare identifiers in their own right, so a
			// field read (`entry.QuestionHash`) and a composite-literal key
			// (`QuestionHash: h`) both surface as an Ident named QuestionHash
			// and were reported as consumers of the FUNCTION. They are not:
			// they are uses of a same-named struct field.
			//
			// The walk below decides at each node and refuses to descend into
			// the parts that would produce those false positives, so what it
			// reports is references to the function itself: bare in-package
			// identifiers (a call or a function VALUE) and package-qualified
			// selectors.
			walkForIdentityRefs(fn.Body, imports, func(node ast.Node, referenced string) {
				if referenced != "QuestionHash" && referenced != "CanonicalizeQuestion" && referenced != "IdentitylessQuestionHash" {
					return
				}
				pos := fset.Position(node.Pos())
				rel, rerr := filepath.Rel(root, pos.Filename)
				if rerr != nil {
					rel = pos.Filename
				}
				sites = append(sites, questionHashSite{file: rel, line: pos.Line, enclosing: name})
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return sites
}

// TestQuestionHashConsumers_WalkTellsFunctionsFromFields is the NEGATIVE
// CONTROL for the walk, and every row is one the walk got WRONG at some point
// while being extended.
//
// The check above reports success by finding nothing unregistered, which is
// indistinguishable from a walk that finds nothing at all. These rows pin both
// directions: the shapes it must catch, and the two same-named decoys it must
// not — a struct field read and a composite-literal key. Reporting those was
// not merely noisy; it was the walk claiming a consumer where there was none,
// which teaches a reader to ignore its output.
func TestQuestionHashConsumers_WalkTellsFunctionsFromFields(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		src     string
		wantHit bool
	}{
		{name: "direct call", wantHit: true,
			src: `func consume(q string) string { return QuestionHash(q) }`},
		{name: "FUNCTION VALUE (codex r2: invisible to a call-only walk)", wantHit: true,
			src: `func consume(q string) string { hash := QuestionHash; return hash(q) }`},
		{name: "package-qualified call", wantHit: true,
			src: `func consume(q string) string { return contextfabric.QuestionHash(q) }`},
		{name: "package-qualified function value", wantHit: true,
			src: `func consume(q string) string { h := contextfabric.QuestionHash; return h(q) }`},
		{name: "the guard predicate alone", wantHit: true,
			src: `func consume(h string) bool { return IdentitylessQuestionHash(h) }`},
		{name: "DECOY: a struct field read", wantHit: false,
			src: `type ev struct{ QuestionHash string }` + "\n" + `func consume(e ev) string { return e.QuestionHash }`},
		{name: "DECOY: a composite-literal field key", wantHit: false,
			src: `type ev struct{ QuestionHash string }` + "\n" + `func consume(h string) ev { return ev{QuestionHash: h} }`},
		{name: "DECOY: an indexed field read", wantHit: false,
			src: `type ev struct{ QuestionHash string }` + "\n" + `func consume(all []ev) string { return all[0].QuestionHash }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			header := "package sample\n\nimport \"github.com/full-chaos/dev-health-acr/internal/contextfabric\"\n\nvar _ = contextfabric.InvestigationRequest{}\n\nfunc QuestionHash(q string) string { return q }\n\nfunc IdentitylessQuestionHash(h string) bool { return h == \"\" }\n\n"
			if err := os.WriteFile(filepath.Join(dir, "s.go"), []byte(header+tc.src+"\n"), 0o600); err != nil {
				t.Fatalf("writing the fixture: %v", err)
			}
			var hits []string
			for _, site := range questionHashCallSites(t, dir) {
				if site.enclosing == "consume" {
					hits = append(hits, site.String())
				}
			}
			if tc.wantHit && len(hits) == 0 {
				t.Errorf("the walk MISSED %s: a consumer using this shape would need no registry entry, which is how the rule gets bypassed", tc.name)
			}
			if !tc.wantHit && len(hits) > 0 {
				t.Errorf("the walk REPORTED %s (%v): it is a same-named struct field, not the function, and claiming a consumer where there is none teaches readers to ignore this check", tc.name, hits)
			}
		})
	}
}
