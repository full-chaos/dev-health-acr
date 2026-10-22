package contextfabric

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// Every rejection reason the package declares is a member of the canonical
// table, and the table holds nothing the package does not declare. A reason
// declared but missing from the table is logged as "unclassified" even though
// a rule fired, which is the gap this pins; a table entry with no constant is
// a reason nothing can emit.
func TestEveryDeclaredRejectionReasonIsInTheCanonicalTable(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthesis_rejection_reason.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[SynthesisRejectionReason]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			if !strings.HasPrefix(name.Name, "RejectionReason") || i >= len(spec.Values) {
				continue
			}
			lit, ok := spec.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("%s: %v", name.Name, err)
			}
			declared[SynthesisRejectionReason(value)] = name.Name
		}
		return true
	})
	if len(declared) < 20 {
		t.Fatalf("found %d declared rejection reasons; the scan no longer sees the const block", len(declared))
	}
	for reason, name := range declared {
		if !ValidSynthesisRejectionReason(reason) {
			t.Errorf("%s = %q is declared but missing from canonicalSynthesisRejectionReasons, so a rejection carrying it is logged as unclassified", name, reason)
		}
	}
	for reason := range canonicalSynthesisRejectionReasons {
		if _, ok := declared[reason]; !ok {
			t.Errorf("canonicalSynthesisRejectionReasons holds %q, which no RejectionReason constant declares", reason)
		}
	}
}
