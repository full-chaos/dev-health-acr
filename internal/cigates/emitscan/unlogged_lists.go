package emitscan

import "go/ast"

// markUnloggedLists records, for one top-level function body, the []any literals and append calls that build a list
// no log call can read: a local list whose every use is the declaration, an assign-back append, or len/cap. Any other
// use of the name (an argument, a spread, an index, a range, a return, a closure) leaves the list checked, and one
// name is one list across every scope of the body, so a shadowing inner list keeps the outer one checked.
func (s *fileScan) markUnloggedLists(fnType *ast.FuncType, body *ast.BlockStmt) {
	if body == nil {
		return
	}
	candidates := map[string][]ast.Node{}
	allowed := map[*ast.Ident]bool{}
	noteList := func(lhs ast.Expr, rhs ast.Expr) {
		id, ok := lhs.(*ast.Ident)
		lit, isLit := rhs.(*ast.CompositeLit)
		if ok && isLit && isAnySlice(lit.Type) {
			candidates[id.Name] = append(candidates[id.Name], lit)
			allowed[id] = true
		}
	}
	inspectCharged(s.pkg.budget, body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) != 1 || len(n.Rhs) != 1 {
				return true
			}
			noteList(n.Lhs[0], n.Rhs[0])
			lhs, ok := n.Lhs[0].(*ast.Ident)
			call, isCall := n.Rhs[0].(*ast.CallExpr)
			if !ok || !isCall || n.Tok.String() != "=" || !isBuiltinAppend(call) {
				return true
			}
			if first, same := call.Args[0].(*ast.Ident); same && first.Name == lhs.Name && call.Ellipsis == 0 {
				candidates[lhs.Name] = append(candidates[lhs.Name], call)
				allowed[lhs], allowed[first] = true, true
			}
		case *ast.ValueSpec:
			if len(n.Names) == 1 && len(n.Values) == 1 {
				noteList(n.Names[0], n.Values[0])
			}
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && (id.Name == "len" || id.Name == "cap") && len(n.Args) == 1 {
				if arg, isIdent := n.Args[0].(*ast.Ident); isIdent {
					allowed[arg] = true
				}
			}
		}
		return true
	})
	live := map[string]bool{}
	for _, root := range []ast.Node{fnType, body} {
		inspectCharged(s.pkg.budget, root, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && !allowed[id] {
				live[id.Name] = true
			}
			return true
		})
	}
	if s.pkg.budget.over {
		return
	}
	for name, nodes := range candidates {
		if live[name] {
			continue
		}
		if s.unlogged == nil {
			s.unlogged = map[ast.Node]bool{}
		}
		for _, node := range nodes {
			s.unlogged[node] = true
		}
	}
}

// isBuiltinAppend reports a call written append(x, ...) with at least one argument. The scan reads the name only, as
// the append site in call does; a shadowing function named append is checked there against the type information.
func isBuiltinAppend(call *ast.CallExpr) bool {
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "append" && len(call.Args) >= 1
}
