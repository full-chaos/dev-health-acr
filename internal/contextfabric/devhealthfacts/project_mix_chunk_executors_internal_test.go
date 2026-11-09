package devhealthfacts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// projectMixChunkedStatements are the statements that carry the chunk key range
// ({chunk_lo:String}, {chunk_hi:String}): the server refuses one sent without
// both bindings.
var projectMixChunkedStatements = map[string]bool{
	"projectRollupRepoBugfixStatement":   true,
	"projectRollupEvidenceArmStatement":  true,
	"projectNativePlacementStatement":    true,
	"projectNativeBugfixValuesStatement": true,
}

// projectMixChunkedStatementSites are the uses of a chunked statement outside
// queryProjectMixChunks, each with how it treats the chunk bindings.
var projectMixChunkedStatementSites = map[string]string{
	"investment_mix_single_pass_internal_test.go|TestPhasedProjectMixStatementsReadOneWideColumnGroupEach":   "reads the text only",
	"investment_uncategorized_internal_test.go|TestInvestmentMixStatementsKeepEvidenceGatedUnitsOutOfTheMix": "reads the text only",
	"team_attribution_scope_sites_internal_test.go|TestInvestmentVoteStatementsReadOnlyPrimaryRows":          "reads the text only",
	"investment_issue_size_internal_test.go|TestInvestmentStatementsWeighByTheSizedSource":                   "reads the text only",
	"investment_issue_size_internal_test.go|TestProjectFoldsCarryTheOrgColumnNotAnAliasOfIt":                 "reads the text only",
	"scope_literal_readers_export_test.go|ScopeLiteralCasesForTest":                                          "executes it with one chunk's bindings over every pin, and refuses a case whose key range and bindings disagree",
}

// Every execution of a chunked statement binds its chunk's key range: in this
// package's sources (tests included) a chunked statement is built only as an
// argument of queryProjectMixChunks, which binds each chunk's pins, chunk_lo and
// chunk_hi, or at a named site above. A new executor fails here until it is
// named with how it binds the range.
func TestEveryExecutorOfAChunkedProjectMixStatementBindsTheChunkRange(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := map[string]bool{}
	inChunkLoop := 0
	for _, file := range files {
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			passed := map[*ast.CallExpr]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "queryProjectMixChunks" {
					for _, arg := range call.Args {
						if inner, ok := arg.(*ast.CallExpr); ok {
							passed[inner] = true
						}
					}
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || !projectMixChunkedStatements[id.Name] {
					return true
				}
				if passed[call] {
					inChunkLoop++
					return true
				}
				key := file + "|" + fn.Name.Name
				seen[key] = true
				if _, named := projectMixChunkedStatementSites[key]; !named {
					t.Errorf("%s: %s builds %s outside queryProjectMixChunks and is not a named site", fset.Position(call.Pos()), fn.Name.Name, id.Name)
				}
				return true
			})
		}
	}
	if inChunkLoop != len(projectMixChunkedStatements) {
		t.Errorf("%d chunked statements are run through queryProjectMixChunks, want %d (one per phase)", inChunkLoop, len(projectMixChunkedStatements))
	}
	for key := range projectMixChunkedStatementSites {
		if !seen[key] {
			t.Errorf("named site %s uses no chunked statement", key)
		}
	}
	if !strings.Contains(projectMixChunkRangeFilter, "{chunk_lo:String}") || !strings.Contains(projectMixChunkRangeFilter, "{chunk_hi:String}") {
		t.Fatal("the chunk key range names no chunk_lo/chunk_hi parameter")
	}
	names := map[string]bool{}
	for _, b := range (projectMixChunk{lo: "a", hi: "b", pins: projectMixScope{unitIDs: []string{"a"}, versionsMs: []int64{1}}}).bindings() {
		names[b.Name] = true
	}
	if !names["chunk_lo"] || !names["chunk_hi"] || !names["pin_json"] {
		t.Errorf("a chunk binds %v, want pin_json, chunk_lo and chunk_hi", names)
	}
}
