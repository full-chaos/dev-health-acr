package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Every rule of the validator has a closed token: no plain error escapes the
// validator file, every call names a declared rule, and every declared rule
// is produced by at least one call.
func TestWorkItemTupleEveryValidatorRuleHasAToken(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "work_item_payload.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, rule := range WorkItemTupleRules() {
		if declared[string(rule)] {
			t.Fatalf("rule token %q declared twice", rule)
		}
		declared[string(rule)] = true
	}
	constNames := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Values) != 1 || spec.Type == nil {
			return true
		}
		if id, ok := spec.Type.(*ast.Ident); ok && id.Name == "WorkItemTupleRule" {
			if lit, ok := spec.Values[0].(*ast.BasicLit); ok {
				value, _ := strconv.Unquote(lit.Value)
				constNames[spec.Names[0].Name] = value
			}
		}
		return true
	})
	if len(constNames) != len(declared) {
		t.Fatalf("%d rule constants but %d listed in WorkItemTupleRules", len(constNames), len(declared))
	}
	used := map[string]int{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if x, ok := fun.X.(*ast.Ident); ok && x.Name == "fmt" && fun.Sel.Name == "Errorf" {
				t.Errorf("fmt.Errorf at %v: a validator rejection must name its rule", call.Pos())
			}
		case *ast.Ident:
			if fun.Name == "workItemRuleErrorf" {
				// The rule is a declared constant, or ruleForKind choosing between two.
				candidates := []ast.Expr{call.Args[0]}
				if pick, ok := call.Args[0].(*ast.CallExpr); ok {
					if f, ok := pick.Fun.(*ast.Ident); !ok || f.Name != "ruleForKind" || len(pick.Args) != 3 {
						t.Errorf("rule argument at %v is not a declared constant", call.Pos())
						return true
					}
					candidates = pick.Args[1:]
				}
				for _, candidate := range candidates {
					id, ok := candidate.(*ast.Ident)
					if !ok {
						t.Errorf("rule argument at %v is not a declared constant", call.Pos())
						continue
					}
					value, ok := constNames[id.Name]
					if !ok {
						t.Errorf("rule %s at %v is not declared", id.Name, call.Pos())
					}
					used[value]++
				}
			}
		}
		return true
	})
	// Every error a validator function returns is nil, a rule error, or the
	// error of another validator function of this file: a plain errors.New or
	// an untyped helper cannot slip through as "unclassified".
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !(strings.HasPrefix(fn.Name.Name, "validateWorkItem") || fn.Name.Name == "ValidateWorkItemTuplePayload") {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.ReturnStmt:
				for _, result := range node.Results {
					switch expr := result.(type) {
					case *ast.CallExpr:
						if id, ok := expr.Fun.(*ast.Ident); ok && (id.Name == "workItemRuleErrorf" || id.Name == "workItemSubjectKey" || id.Name == "make" || strings.HasPrefix(id.Name, "validateWorkItem")) {
							continue
						}
						if id, ok := expr.Fun.(*ast.Ident); ok && id.Name == "string" {
							continue
						}
						if _, ok := expr.Fun.(*ast.SelectorExpr); ok {
							continue // value-producing calls such as SubjectRef{}, maps
						}
						t.Errorf("%s returns the result of an untyped call at %v", fn.Name.Name, expr.Pos())
					}
				}
			case *ast.AssignStmt:
				for i, lhs := range node.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok || id.Name != "err" || len(node.Rhs) == 0 {
						continue
					}
					rhs := node.Rhs[0]
					if len(node.Rhs) == len(node.Lhs) {
						rhs = node.Rhs[i]
					}
					call, ok := rhs.(*ast.CallExpr)
					fun, isIdent := ast.Expr(nil), false
					if ok {
						fun = call.Fun
						_, isIdent = fun.(*ast.Ident)
					}
					if !ok || !isIdent || !strings.HasPrefix(fun.(*ast.Ident).Name, "validateWorkItem") {
						t.Errorf("%s assigns err from something other than a validator function at %v", fn.Name.Name, node.Pos())
					}
				}
			}
			return true
		})
	}
	for value := range declared {
		if used[value] == 0 {
			t.Errorf("rule token %q is declared but no validator rule produces it", value)
		}
	}
}

// Rejections from the real validator carry the token of the rule that fired.
func TestWorkItemTupleRejectionNamesTheRule(t *testing.T) {
	principal := storage.Principal{OrgID: "org-1"}
	for _, tc := range []struct {
		name   string
		mutate func(*InvestigationResult)
		want   WorkItemTupleRule
	}{
		{"foreign result evidence", func(r *InvestigationResult) {
			r.EvidenceRefIDs = append(append([]string(nil), r.EvidenceRefIDs...), "foreign")
		}, WorkItemRuleResultEvidenceOutsideMembers},
		{"foreign label", func(r *InvestigationResult) { r.EvidenceRefLabels = map[string]string{"foreign": "x"} }, WorkItemRuleEvidenceLabelOutsideMembers},
		{"no committed anchor", func(r *InvestigationResult) { r.SubjectResolution.Committed = nil }, WorkItemRuleAnchorCardinality},
		{"driver without subject", func(r *InvestigationResult) { r.Drivers = []DriverJudgment{{}} }, WorkItemRuleDriverNoMemberSubject},
		{"finding without subject", func(r *InvestigationResult) { r.RemainingWork = []Finding{{}} }, WorkItemRuleFindingNoMemberSubject},
		{"relationship path", func(r *InvestigationResult) { r.Paths = []RelationshipPath{{}} }, WorkItemRuleRelationshipPaths},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := workItemTuplePayloadFixture(t)
			tc.mutate(&result)
			err := ValidateWorkItemTuplePayload(result, principal)
			if err == nil {
				t.Fatal("not rejected")
			}
			wrapped := joinRejected(err)
			if got := workItemTupleRuleOf(wrapped); got != string(tc.want) {
				t.Fatalf("rule = %q, want %q", got, tc.want)
			}
		})
	}
	if got := workItemTupleRuleOf(nil); got != WorkItemTupleRuleNone {
		t.Errorf("nil save error = %q", got)
	}
	if got := workItemTupleRuleOf(joinRejected(errors.New("plain"))); got != WorkItemTupleRuleUnclassified {
		t.Errorf("untyped rejection = %q", got)
	}
}

func joinRejected(err error) error {
	return errors.Join(errWorkItemTuplePayloadRejected, err)
}

// The shipped persistence line carries the token, and only the token.
func TestPersistenceLineCarriesTheRejectReason(t *testing.T) {
	var buf bytes.Buffer
	sink := SlogEngineTelemetry{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	bad := workItemTuplePayloadFixture(t)
	bad.ResultID = "result-reason-line"
	bad.EvidenceRefIDs = append(append([]string(nil), bad.EvidenceRefIDs...), "foreign-secret-ref")
	engine := mustReuseTestEngine(t, EngineDependencies{Results: &resultStoreStub{}, Telemetry: sink})
	err := engine.saveResult(context.Background(), storage.Principal{OrgID: "org-1"}, BudgetAssertReuse, bad, nil, nil, "", 0, "", semanticStateCapture{Write: SemanticStateWrite{State: workItemTupleSemanticStateFixture()}})
	if err == nil {
		t.Fatal("not rejected")
	}
	var line map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var l map[string]any
		if json.Unmarshal([]byte(raw), &l) == nil && l["msg"] == "context fabric semantic state persistence" {
			line = l
		}
	}
	if line == nil {
		t.Fatalf("no persistence line in %s", buf.String())
	}
	if line["decision"] != "payload_rejected" || line["reject_reason"] != string(WorkItemRuleResultEvidenceOutsideMembers) {
		t.Errorf("line = %v", line)
	}
	if strings.Contains(buf.String(), "foreign-secret-ref") {
		t.Error("caller-derived text reached the record")
	}
}
