package orgpurge_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The write fence reads the purge marker after it takes the lock, which is
// only sound in READ COMMITTED. This is the list of every transaction that is
// opened with an explicit isolation level; every other transaction in the
// repository must pass nil options (the database default, READ COMMITTED,
// which the integration test reads back from the pool).
var explicitIsolation = map[string]string{
	"internal/storage/postgres/oauth_purge.go":                "LevelReadCommitted",
	"internal/contextfabric/pgprojection/org_purge_marker.go": "LevelRepeatableRead", // deletes purge markers only; writes no fenced table
}

func TestNoWriterOfAFencedTableRunsOutsideReadCommitted(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	seen := map[string]bool{}
	transactions := 0
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, banned := range []string{"default_transaction_isolation", "SET TRANSACTION", "ISOLATION LEVEL", "SET SESSION CHARACTERISTICS"} {
				if strings.Contains(string(source), banned) {
					t.Errorf("%s sets a transaction isolation level in SQL (%q); name it in this test", relative, banned)
				}
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch selector.Sel.Name {
				case "Begin":
					if len(call.Args) == 0 {
						transactions++
					}
				case "BeginTx":
					if len(call.Args) != 2 {
						return true
					}
					transactions++
					if ident, ok := call.Args[1].(*ast.Ident); ok && ident.Name == "nil" {
						return true
					}
					level, allowed := explicitIsolation[relative]
					if !allowed {
						t.Errorf("%s opens a transaction with explicit options; a writer of a fenced table must run in READ COMMITTED: name the file and its level in this test", relative)
						return true
					}
					if !strings.Contains(string(source), "sql."+level) || strings.Count(string(source), "sql.Level") != 1 {
						t.Errorf("%s must use exactly sql.%s", relative, level)
					}
					seen[relative] = true
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if transactions < 10 {
		t.Fatalf("found only %d transaction starts; the scan did not read the repository", transactions)
	}
	for file := range explicitIsolation {
		if !seen[file] {
			t.Errorf("%s no longer opens a transaction with explicit options; remove it from this test", file)
		}
	}
	marker, err := os.ReadFile(filepath.Join(root, "internal/contextfabric/pgprojection/org_purge_marker.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, write := range []string{"INSERT INTO", "UPDATE acr."} {
		if strings.Contains(string(marker), write) {
			t.Errorf("the repeatable-read marker transaction's file now contains %q; it may delete purge markers only", write)
		}
	}
	if strings.Count(string(marker), "DELETE FROM") != 1 || !strings.Contains(string(marker), "DELETE FROM acr.context_fabric_org_purge_markers") {
		t.Error("the repeatable-read marker transaction may delete from the purge marker table only")
	}
}
