package directread

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

func TestServedRelationshipsVocabularyRefusesAnOffSetValueBuiltAtRunTime(t *testing.T) {
	build := func(axis, tier string) RelationshipsResponse {
		response := RelationshipsResponse{Effective: EffectiveRelationshipsRead{Axis: axis}}
		response.Edges = []ServedEdge{{Provenance: RelationshipSource{LinkTier: tier}}}
		return response
	}
	for _, axis := range RelationshipsAxisVocabulary() {
		for _, tier := range append(LinkTierVocabulary(), "") {
			if err := validateServedRelationshipsVocabulary(build(axis, tier)); err != nil {
				t.Errorf("axis %q tier %q refused: %v", axis, tier, err)
			}
		}
	}
	offAxis := fmt.Sprintf("%s_%d", "cur", len("rent"))
	offTier := fmt.Sprintf("%s-%s", "nat", "ive")
	if err := validateServedRelationshipsVocabulary(build(offAxis, "native")); err == nil {
		t.Error("an off-set axis was served")
	}
	if err := validateServedRelationshipsVocabulary(build("current", offTier)); err == nil {
		t.Error("an off-set link tier was served")
	}
}

func TestReadRelationshipsRunsTheServedVocabularyCheckOnItsReturnPath(t *testing.T) {
	src, err := os.ReadFile("read_relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "read_relationships.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Read" || fn.Recv == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if _, isDefer := n.(*ast.DeferStmt); isDefer {
				ast.Inspect(n, func(m ast.Node) bool {
					if id, ok := m.(*ast.Ident); ok && id.Name == "validateServedRelationshipsVocabulary" {
						found = true
					}
					return true
				})
			}
			return true
		})
	}
	if !found {
		t.Fatal("RelationshipsReader.Read has no deferred served-vocabulary check; the measurement found no call site")
	}
}
