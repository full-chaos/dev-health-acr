package falkorgraph

// CHAOS-3785 codex round-3 finding R3-2: the OWNED/REFERENCED split between
// ownedSubjectMergeCypher and referencedSubjectStubMergeCypher (projection.go)
// is a naming convention, not a compiler-enforced mechanism -- nothing stops
// a future writer from picking the wrong one for a subject it doesn't own,
// silently reintroducing the round-1/round-2 authorization-and-metadata
// clobber class. This file pins every REAL call site of both by (enclosing
// function, line), parsed from projection.go's actual AST rather than
// counted by a loose grep a decoy could satisfy (a call inside a comment, a
// same-named symbol elsewhere) -- see findMergeCypherCallSites' doc comment.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"testing"
)

// mergeCypherCallSite pins one real call expression invoking funcName to
// the method it appears inside and the source line it appears on.
type mergeCypherCallSite struct {
	enclosingFunc string
	line          int
}

// findMergeCypherCallSites parses projection.go and returns every call
// expression whose callee is the bare identifier funcName, sorted by
// (enclosingFunc, line) for a deterministic comparison. It walks the real
// syntax tree (go/ast), not the source text -- a substring match against
// the raw file would count the function's own definition, its doc comment,
// or a decoy identifier that merely CONTAINS funcName as a call, none of
// which are real invocations.
func findMergeCypherCallSites(t *testing.T, funcName string) []mergeCypherCallSite {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "projection.go", nil, 0)
	if err != nil {
		t.Fatalf("parse projection.go: %v", err)
	}
	var sites []mergeCypherCallSite
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != funcName {
				return true
			}
			sites = append(sites, mergeCypherCallSite{enclosingFunc: fn.Name.Name, line: fset.Position(call.Pos()).Line})
			return true
		})
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].enclosingFunc != sites[j].enclosingFunc {
			return sites[i].enclosingFunc < sites[j].enclosingFunc
		}
		return sites[i].line < sites[j].line
	})
	return sites
}

func enclosingFuncs(sites []mergeCypherCallSite) []string {
	names := make([]string, len(sites))
	for i, site := range sites {
		names[i] = site.enclosingFunc
	}
	return names
}

// TestOwnedSubjectMergeCypherCallSitesArePinned pins ownedSubjectMergeCypher
// to exactly the three methods authoritative for the exact node they write:
// projectEntity (its own entity subject), and projectContent/projectEpisode
// (the document:ID/episode:ID node each one itself synthesizes -- never the
// subject the content/episode merely attaches to). A new call site anywhere
// else means a writer is claiming OWNERSHIP of a subject node -- that write
// will authoritatively overwrite the node's canonical data, including on
// match -- and that decision must be deliberate, confirmed by updating this
// list, not incidental.
func TestOwnedSubjectMergeCypherCallSitesArePinned(t *testing.T) {
	got := enclosingFuncs(findMergeCypherCallSites(t, "ownedSubjectMergeCypher"))
	want := []string{"projectContent", "projectEntity", "projectEpisode"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ownedSubjectMergeCypher call sites (by enclosing method) = %v, want exactly %v -- if you added a new one, confirm it is genuinely authoritative for that node (see projection.go's OWNED/REFERENCED doc comment) before updating this list", got, want)
	}
}

// TestReferencedSubjectStubMergeCypherCallSitesArePinned pins
// referencedSubjectStubMergeCypher to exactly the four call sites that
// point at a subject some OTHER producer owns: projectRelationship's From
// and To (two calls, hence "projectRelationship" appearing twice), and
// projectContent/projectEpisode's own attachment subject (content.Subject /
// episode.Subject -- distinct from the document:ID/episode:ID node those
// same methods themselves own via ownedSubjectMergeCypher above). A new
// call site anywhere else means a writer is claiming it does NOT own a
// subject -- its write must never authoritatively overwrite that node -- and
// that decision must be deliberate too.
func TestReferencedSubjectStubMergeCypherCallSitesArePinned(t *testing.T) {
	got := enclosingFuncs(findMergeCypherCallSites(t, "referencedSubjectStubMergeCypher"))
	want := []string{"projectContent", "projectEpisode", "projectRelationship", "projectRelationship"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("referencedSubjectStubMergeCypher call sites (by enclosing method) = %v, want exactly %v -- if you added a new one, confirm the writer genuinely does not own that subject (see projection.go's OWNED/REFERENCED doc comment) before updating this list", got, want)
	}
}
