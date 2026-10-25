package emitscan

import (
	"go/ast"
	"go/types"
)

type listDecl struct {
	id   *ast.Ident
	node ast.Node
}

type listAppend struct {
	lhs, first, fun *ast.Ident
	call            *ast.CallExpr
}

type listMeasure struct{ arg, fun *ast.Ident }

// markUnloggedLists records, for one top-level function body, the []any literals and append calls that build a list
// no log call can read: a variable defined in the body by a []any literal whose every use is its definition, an
// assign-back append, or len/cap, with append, len and cap the predeclared functions. Variables are told apart by
// type information, never by name, so a package variable, a receiver, a parameter or a shadowing variable is not
// this list. Without type information nothing is marked and every list stays checked.
func (s *fileScan) markUnloggedLists(body *ast.BlockStmt) {
	if body == nil {
		return
	}
	var decls []listDecl
	var appends []listAppend
	var measures []listMeasure
	noteList := func(lhs ast.Expr, rhs ast.Expr) {
		id, ok := lhs.(*ast.Ident)
		lit, isLit := rhs.(*ast.CompositeLit)
		if ok && isLit && isAnySlice(lit.Type) {
			decls = append(decls, listDecl{id, lit})
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
			if !ok || !isCall || !isBuiltinAppend(call) || call.Ellipsis != 0 {
				return true
			}
			if first, same := call.Args[0].(*ast.Ident); same {
				appends = append(appends, listAppend{lhs, first, call.Fun.(*ast.Ident), call})
			}
		case *ast.ValueSpec:
			if len(n.Names) == 1 && len(n.Values) == 1 {
				noteList(n.Names[0], n.Values[0])
			}
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && (id.Name == "len" || id.Name == "cap") && len(n.Args) == 1 {
				if arg, isIdent := n.Args[0].(*ast.Ident); isIdent {
					measures = append(measures, listMeasure{arg, id})
				}
			}
		}
		return true
	})
	if len(decls) == 0 || s.pkg.budget.over {
		return
	}
	typed := s.pkg.typedInfo()
	if typed.err != nil {
		return
	}
	lists := map[types.Object]bool{}
	for _, decl := range decls {
		if v, ok := typed.info.Defs[decl.id].(*types.Var); ok {
			lists[v] = true
		}
	}
	allowed := map[*ast.Ident]bool{}
	for _, decl := range decls {
		allowed[decl.id] = true
	}
	var unlogged []ast.Node
	for _, decl := range decls {
		unlogged = append(unlogged, decl.node)
	}
	for _, app := range appends {
		if typed.info.Uses[app.lhs] != typed.info.Uses[app.first] || !isPredeclared(typed.info, app.fun) {
			continue
		}
		allowed[app.lhs], allowed[app.first] = true, true
		unlogged = append(unlogged, app.call)
	}
	for _, m := range measures {
		if isPredeclared(typed.info, m.fun) {
			allowed[m.arg] = true
		}
	}
	live := map[types.Object]bool{}
	inspectCharged(s.pkg.budget, body, func(node ast.Node) bool {
		if id, ok := node.(*ast.Ident); ok && !allowed[id] {
			if obj := objectOf(typed.info, id); obj != nil && lists[obj] {
				live[obj] = true
			}
		}
		return true
	})
	if s.pkg.budget.over {
		return
	}
	if s.unlogged == nil {
		s.unlogged = map[ast.Node]bool{}
	}
	owner := func(node ast.Node) types.Object {
		switch n := node.(type) {
		case *ast.CompositeLit:
			for _, decl := range decls {
				if decl.node == n {
					return typed.info.Defs[decl.id]
				}
			}
		case *ast.CallExpr:
			for _, app := range appends {
				if app.call == n {
					return typed.info.Uses[app.lhs]
				}
			}
		}
		return nil
	}
	for _, node := range unlogged {
		if obj := owner(node); obj != nil && lists[obj] && !live[obj] {
			s.unlogged[node] = true
		}
	}
}

// isBuiltinAppend reports a call written append(x, ...) with at least one argument.
func isBuiltinAppend(call *ast.CallExpr) bool {
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "append" && len(call.Args) >= 1
}

// isPredeclared reports that an identifier is the predeclared function it is spelled as: a function or variable of the
// same name could log its argument.
func isPredeclared(info *types.Info, id *ast.Ident) bool {
	_, ok := info.Uses[id].(*types.Builtin)
	return ok
}
