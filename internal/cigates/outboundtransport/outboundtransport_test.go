// Package outboundtransport is the CI guard that every outbound HTTP client of production code dials with a transport
// of its own and never through a proxy taken from the process environment (HTTP_PROXY, HTTPS_PROXY, NO_PROXY).
//
// A client with no Transport uses http.DefaultTransport, whose Proxy is http.ProxyFromEnvironment: an ambient proxy
// variable then receives every request the client sends, credential headers included (a plain-http target is sent to
// the proxy in full). Which client carries a credential cannot be decided statically, so the rule covers every client.
//
// The walk is by TYPE (go/packages + go/types) over the module, once for every GOOS of the release matrix
// (releasebuild.Matrix: the sidecar ships for darwin and windows too); an import alias or a dot import cannot hide a
// site. A SITE is
//
//	client-no-transport  a composite literal of net/http.Client whose Transport is absent or nil, new(net/http.Client),
//	                     or a var declared with the non-pointer type net/http.Client and no value, or a named result of
//	                     that type (a bare return yields the zero client)
//	transport-nil        an assignment of nil to the Transport field of a net/http.Client
//	transport-unproven   a Transport (in a literal, by assignment, or as one target of a multi-value assignment) whose
//	                     value is not PROVEN non-nil. Proven are: a call to an approved constructor, an approved wrapper
//	                     over a nil or proven transport, Clone of a proven *http.Transport, a composite literal whose
//	                     RoundTripper fields are all proven, a conversion of a func literal, and a local defined once from a
//	                     proven value and never reassigned or address-taken. No control flow is reasoned about.
//	client-by-value      a struct field (embedded or named), a defined type or alias, or an array, slice, map or channel
//	                     element whose type is the non-pointer net/http.Client: its zero value has no Transport and its
//	                     (promoted) Do uses http.DefaultTransport, and no literal of net/http.Client is ever written
//	transport-address    &c.Transport (resolved through the type checker: promoted fields and named pointer types included):
//	                     a write through the pointer is invisible to every other rule; &t.Proxy is a proxy site
//	client-copy          a copy of a supplied net/http.Client (`*client`): the copy keeps a nil Transport unless the
//	                     code gives it one, so every copy needs a row naming where its transport comes from
//	client-parameter     a parameter of type *net/http.Client or net/http.Client: the constructor sweep. Every function that
//	                     takes a supplied client is listed with where that client gets its transport, so a new one fails
//	value-overwrite      an assignment to `*p` for a pointer to net/http.Client or net/http.Transport (or such a range target):
//	                     the whole value is replaced, which no field rule sees
//	client-instantiate   a generic function or type instantiated with the non-pointer net/http.Client (zero[http.Client]())
//	reverseproxy-no-transport  a net/http/httputil.ReverseProxy with a nil Transport (it dials through http.DefaultTransport and
//	                     so the environment proxy): a composite literal whose Transport is absent or nil, a zero value (a
//	                     var, named result, struct field, defined type, or array, slice, map or channel element of the
//	                     non-pointer type), new(httputil.ReverseProxy), and any use of httputil.NewSingleHostReverseProxy.
//	                     A literal Transport not proven non-nil, and an assignment to or address of a ReverseProxy's
//	                     Transport, are the transport-unproven, transport-nil and transport-address kinds
//	default-transport    any use of net/http.DefaultTransport
//	default-transport-assign  an assignment to net/http.DefaultTransport: FORBIDDEN, no allow row admits it (the transport
//	                     helpers fall back to net/http's defaults when it is replaced, so repository code must never replace it)
//	default-client       any use of net/http.DefaultClient or the package functions Get, Head, Post, PostForm
//	proxy                any use of net/http.ProxyFromEnvironment, net/http.ProxyURL or httpproxy.FromEnvironment, and a
//	                     Proxy field of a net/http.Transport set (in a literal or by assignment) to anything but nil
//
// Every site needs a row in allow.tsv (file, symbol, kind, count, reason): the transport helpers themselves and the
// documented customer-machine clients that honour a proxy on purpose. An unlisted site, a count that differs and a
// row that matches nothing FAIL. Production code takes its transport from outboundhttp.SharedTransport or
// outboundhttp.Transport.
//
// NOT covered, stated: a client built inside a dependency outside the module (an SDK's own default client, such as the
// LLM clients of dev-health-go); a production file outside every release build (pinned by name, the test fails when
// that set changes).
package outboundtransport

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/full-chaos/dev-health-acr/internal/releasebuild"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

const (
	modulePath   = "github.com/full-chaos/dev-health-acr"
	fixturesBase = modulePath + "/internal/cigates/outboundtransport/testdata/planted"
	allowFile    = "allow.tsv"
)

// pinnedOutOfScope: the production .go files no release build contains; none holds an HTTP client. They build only
// under a tag: race, or the compiled lifecycle fixtures of the acr-mcp and sidecar tests.
var pinnedOutOfScope = []string{
	"cmd/acr-mcp/lifecycle_compiled_fixture.go",
	"internal/contracts/v1/answer_bound_race.go",
	"internal/sidecar/credential_keyring_compiled_lifecycle_fixture.go",
	"internal/sidecar/credential_lifecycle_lock_compiled_fixture_unix.go",
	"internal/sidecar/credential_lifecycle_lock_compiled_fixture_unsupported.go",
}

type site struct{ file, symbol, kind string }

type allowRow struct {
	count  int
	reason string
}

func TestEveryOutboundClientHasATransportWithoutTheEnvironmentProxy(t *testing.T) {
	root := repopath.Root(t)
	found, problems, loaded := scan(t, root, []string{"./..."})
	if len(found) == 0 {
		t.Fatal("the walk found no site at all: the transport helpers alone are sites, so the walk did not run")
	}
	problems = append(problems, compare(found, readAllow(t, allowFile))...)
	problems = append(problems, scopeProblems(outsideBuild(t, root, loaded), pinnedOutOfScope)...)
	for _, problem := range problems {
		t.Error(problem)
	}
	if len(problems) > 0 {
		t.Log("fix: give the client Transport: outboundhttp.SharedTransport() (or outboundhttp.Transport() to change a field of it); a documented, deliberate proxy gets an allow.tsv row with its reason")
	}
}

// scan loads the packages matching patterns (non-test files) for every release target and returns every site with
// the largest count seen in one build, the load problems, and the files loaded. The targets load concurrently.
func scan(t *testing.T, root string, patterns []string) (map[site]int, []string, map[string]bool) {
	t.Helper()
	targets := releaseTargets()
	results := make([]targetScan, len(targets))
	var wait sync.WaitGroup
	for i, target := range targets {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[i] = scanTarget(root, patterns, target)
		}()
	}
	wait.Wait()
	found := map[site]int{}
	var problems []string
	loaded := map[string]bool{}
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("packages.Load: %v", result.err)
		}
		problems = append(problems, result.problems...)
		for name := range result.loaded {
			loaded[name] = true
		}
		for key, count := range result.counts {
			if count > found[key] {
				found[key] = count
			}
		}
	}
	sort.Strings(problems)
	return found, uniqueStrings(problems), loaded
}

type targetScan struct {
	counts   map[site]int
	problems []string
	loaded   map[string]bool
	err      error
}

func scanTarget(root string, patterns []string, target [2]string) targetScan {
	cfg := &packages.Config{
		Dir:  root,
		Env:  append(os.Environ(), "GOOS="+target[0], "GOARCH="+target[1]),
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return targetScan{err: err}
	}
	result := targetScan{counts: map[site]int{}, loaded: map[string]bool{}}
	for _, pkg := range pkgs {
		for _, loadErr := range pkg.Errors {
			result.problems = append(result.problems, fmt.Sprintf("LOAD error in %s (%s/%s): %v", pkg.PkgPath, target[0], target[1], loadErr))
		}
		for _, name := range pkg.CompiledGoFiles {
			result.loaded[name] = true
		}
		if pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel, relErr := filepath.Rel(root, pkg.Fset.Position(file.Pos()).Filename)
			if relErr != nil {
				return targetScan{err: relErr}
			}
			rel = filepath.ToSlash(rel)
			for _, declaration := range file.Decls {
				symbol := declSymbol(declaration)
				ast.Inspect(declaration, func(node ast.Node) bool {
					for _, kind := range kindsOf(pkg.TypesInfo, declaration, node) {
						result.counts[site{rel, symbol, kind}]++
					}
					return true
				})
			}
		}
	}
	return result
}

// kindsOf is the site kinds one node of declaration is.
func kindsOf(info *types.Info, declaration ast.Decl, node ast.Node) []string {
	switch n := node.(type) {
	case *ast.CompositeLit:
		var kinds []string
		if tv, ok := info.Types[n]; ok {
			switch {
			case isNamed(tv.Type, "Client"):
				value, ok := fieldValue(n, "Transport", 0)
				if !ok || isNil(info, value) {
					kinds = append(kinds, "client-no-transport")
				} else if !provenTransport(info, declaration, value, 0) {
					kinds = append(kinds, "transport-unproven")
				}
			case isNamed(tv.Type, "Transport"):
				if value, ok := fieldValue(n, "Proxy", -1); ok && !isNil(info, value) {
					kinds = append(kinds, "proxy")
				}
			case isReverseProxy(tv.Type):
				if value, ok := fieldValue(n, "Transport", -1); !ok || isNil(info, value) {
					kinds = append(kinds, "reverseproxy-no-transport")
				} else if !provenTransport(info, declaration, value, 0) {
					kinds = append(kinds, "transport-unproven")
				}
			}
		}
		for _, element := range n.Elts {
			if keyed, ok := element.(*ast.KeyValueExpr); ok {
				element = keyed.Value
			}
			if isClientCopy(info, element) {
				kinds = append(kinds, "client-copy")
			}
		}
		return kinds
	case *ast.CallExpr:
		var kinds []string
		if ident, ok := n.Fun.(*ast.Ident); ok && len(n.Args) == 1 {
			if builtin, ok := info.Uses[ident].(*types.Builtin); ok && builtin.Name() == "new" {
				if argument := info.Types[n.Args[0]].Type; isNamed(argument, "Client") {
					kinds = append(kinds, "client-no-transport")
				} else if isReverseProxy(argument) {
					kinds = append(kinds, "reverseproxy-no-transport")
				}
			}
		}
		for _, argument := range n.Args {
			if isClientCopy(info, argument) {
				kinds = append(kinds, "client-copy")
			}
		}
		return kinds
	case *ast.ReturnStmt:
		var kinds []string
		for _, result := range n.Results {
			if isClientCopy(info, result) {
				kinds = append(kinds, "client-copy")
			}
		}
		for _, result := range n.Results {
			if call, ok := ast.Unparen(result).(*ast.CallExpr); ok && isReverseProxyRef(info.Types[result].Type) && !isBuiltinCall(info, call) {
				kinds = append(kinds, "reverseproxy-result")
			}
		}
		return kinds
	case *ast.SendStmt:
		if isClientCopy(info, n.Value) {
			return []string{"client-copy"}
		}
	case *ast.UnaryExpr:
		if n.Op.String() != "&" {
			return nil
		}
		switch writtenField(info, n.X) {
		case "Transport":
			return []string{"transport-address"}
		case "Proxy":
			return []string{"proxy"}
		}
	case *ast.RangeStmt:
		var kinds []string
		for _, target := range []ast.Expr{n.Key, n.Value} {
			if target == nil {
				continue
			}
			switch writtenField(info, target) {
			case "Transport":
				kinds = append(kinds, "transport-unproven")
			case "Proxy":
				kinds = append(kinds, "proxy")
			}
			if isValueOverwrite(info, target) {
				kinds = append(kinds, "value-overwrite")
			}
		}
		return kinds
	case *ast.FuncType:
		var kinds []string
		if n.Params != nil {
			for _, field := range n.Params.List {
				if t := info.Types[field.Type].Type; isNamed(t, "Client") || isNamed(derefPointer(t), "Client") {
					names := len(field.Names)
					if names == 0 {
						names = 1
					}
					for i := 0; i < names; i++ {
						kinds = append(kinds, "client-parameter")
					}
				}
				if isReverseProxyRef(info.Types[field.Type].Type) {
					names := len(field.Names)
					if names == 0 {
						names = 1
					}
					for i := 0; i < names; i++ {
						kinds = append(kinds, "reverseproxy-parameter")
					}
				}
			}
		}
		if n.Results != nil {
			for _, field := range n.Results.List {
				if isNamed(derefPointer(info.Types[field.Type].Type), "Client") {
					for range field.Names {
						kinds = append(kinds, "client-no-transport")
					}
				}
				if isReverseProxyRef(info.Types[field.Type].Type) {
					for range field.Names {
						kinds = append(kinds, "reverseproxy-no-transport")
					}
				}
			}
		}
		return kinds
	case *ast.StructType:
		var kinds []string
		for _, field := range n.Fields.List {
			if isNamed(info.Types[field.Type].Type, "Client") {
				kinds = append(kinds, "client-by-value")
			}
			if isReverseProxyRef(info.Types[field.Type].Type) {
				kinds = append(kinds, "reverseproxy-no-transport")
			}
		}
		return kinds
	case *ast.TypeSpec:
		if isNamed(info.Types[n.Type].Type, "Client") {
			return []string{"client-by-value"}
		}
		if isReverseProxyRef(info.Types[n.Type].Type) {
			return []string{"reverseproxy-no-transport"}
		}
	case *ast.ArrayType:
		if isNamed(info.Types[n.Elt].Type, "Client") {
			return []string{"client-by-value"}
		}
		if isReverseProxyRef(info.Types[n.Elt].Type) {
			return []string{"reverseproxy-no-transport"}
		}
	case *ast.MapType:
		if isNamed(info.Types[n.Key].Type, "Client") || isNamed(info.Types[n.Value].Type, "Client") {
			return []string{"client-by-value"}
		}
		if isReverseProxyRef(info.Types[n.Key].Type) || isReverseProxyRef(info.Types[n.Value].Type) {
			return []string{"reverseproxy-no-transport"}
		}
	case *ast.ChanType:
		if isNamed(info.Types[n.Value].Type, "Client") {
			return []string{"client-by-value"}
		}
		if isReverseProxyRef(info.Types[n.Value].Type) {
			return []string{"reverseproxy-no-transport"}
		}
	case *ast.ValueSpec:
		var kinds []string
		if n.Type != nil && len(n.Values) == 0 && isNamed(info.Types[n.Type].Type, "Client") {
			kinds = append(kinds, "client-no-transport")
		}
		if n.Type != nil && len(n.Values) == 0 && isReverseProxyRef(info.Types[n.Type].Type) {
			kinds = append(kinds, "reverseproxy-no-transport")
		}
		for _, value := range n.Values {
			if isClientCopy(info, value) {
				kinds = append(kinds, "client-copy")
			}
		}
		return kinds
	case *ast.AssignStmt:
		var kinds []string
		tuple := len(n.Lhs) != len(n.Rhs)
		for i, lhs := range n.Lhs {
			if isDefaultTransportVar(info, lhs) {
				kinds = append(kinds, "default-transport-assign")
			}
			if isValueOverwrite(info, lhs) {
				kinds = append(kinds, "value-overwrite")
			}
			switch writtenField(info, lhs) {
			case "Transport":
				switch {
				case tuple:
					kinds = append(kinds, "transport-unproven")
				case isNil(info, n.Rhs[i]):
					kinds = append(kinds, "transport-nil")
				case !provenTransport(info, declaration, n.Rhs[i], 0):
					kinds = append(kinds, "transport-unproven")
				}
			case "Proxy":
				if tuple || !isNil(info, n.Rhs[i]) {
					kinds = append(kinds, "proxy")
				}
			}
		}
		for _, rhs := range n.Rhs {
			if isClientCopy(info, rhs) {
				kinds = append(kinds, "client-copy")
			}
		}
		return kinds
	case *ast.Ident:
		if instance, ok := info.Instances[n]; ok && instance.TypeArgs != nil {
			for i := 0; i < instance.TypeArgs.Len(); i++ {
				if isNamed(instance.TypeArgs.At(i), "Client") {
					return []string{"client-instantiate"}
				}
			}
		}
		obj := info.Uses[n]
		if obj == nil || obj.Pkg() == nil || obj.Pkg().Scope().Lookup(obj.Name()) != obj {
			return nil
		}
		switch obj.Pkg().Path() {
		case "net/http/httputil":
			if obj.Name() == "NewSingleHostReverseProxy" {
				return []string{"reverseproxy-no-transport"}
			}
		case "net/http":
			switch obj.Name() {
			case "DefaultTransport":
				return []string{"default-transport"}
			case "DefaultClient", "Get", "Head", "Post", "PostForm":
				return []string{"default-client"}
			case "ProxyFromEnvironment", "ProxyURL":
				return []string{"proxy"}
			}
		case "golang.org/x/net/http/httpproxy":
			if obj.Name() == "FromEnvironment" {
				return []string{"proxy"}
			}
		}
	}
	return nil
}

// writtenField names the net/http field expression selects, resolved through the type checker's selection (so a field
// promoted through an embedded *http.Client or *http.Transport, a named pointer type and parentheses all resolve):
// "Transport" for net/http.Client.Transport, "Proxy" for net/http.Transport.Proxy, "" otherwise.
func writtenField(info *types.Info, expression ast.Expr) string {
	selector, ok := ast.Unparen(expression).(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	selection, ok := info.Selections[selector]
	if !ok || selection.Kind() != types.FieldVal {
		return ""
	}
	field, ok := selection.Obj().(*types.Var)
	if !ok || !field.IsField() || field.Pkg() == nil {
		return ""
	}
	if field.Pkg().Path() == "net/http/httputil" {
		if field.Name() == "Transport" && field == netHTTPField(field.Pkg(), "ReverseProxy", "Transport") {
			return "Transport"
		}
		return ""
	}
	if field.Pkg().Path() != "net/http" {
		return ""
	}
	switch {
	case field.Name() == "Transport" && field == netHTTPField(field.Pkg(), "Client", "Transport"):
		return "Transport"
	case field.Name() == "Proxy" && field == netHTTPField(field.Pkg(), "Transport", "Proxy"):
		return "Proxy"
	}
	return ""
}

// netHTTPField is the field object name of the struct type typeName of package net/http.
func netHTTPField(pkg *types.Package, typeName, name string) *types.Var {
	obj := pkg.Scope().Lookup(typeName)
	if obj == nil {
		return nil
	}
	structType, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	for i := 0; i < structType.NumFields(); i++ {
		if structType.Field(i).Name() == name {
			return structType.Field(i)
		}
	}
	return nil
}

// isClientCopy: expression is `*p` for a pointer to net/http.Client used as a value (a copy that keeps a nil Transport
// unless the code gives it one). A dereference that is only the base of a field selector, or the target of an
// assignment, is not a copy.
func isClientCopy(info *types.Info, expression ast.Expr) bool {
	star, ok := ast.Unparen(expression).(*ast.StarExpr)
	if !ok {
		return false
	}
	tv, ok := info.Types[star]
	return ok && tv.IsValue() && isNamed(tv.Type, "Client")
}

// isValueOverwrite: the assignment target is `*p` for a pointer to net/http.Client or net/http.Transport: the whole
// value is replaced, which no field rule sees.
func isValueOverwrite(info *types.Info, expression ast.Expr) bool {
	star, ok := ast.Unparen(expression).(*ast.StarExpr)
	if !ok {
		return false
	}
	tv, ok := info.Types[star]
	return ok && (isNamed(tv.Type, "Client") || isNamed(tv.Type, "Transport"))
}

// approvedTransportConstructors are the functions whose result is a non-nil RoundTripper that never uses an
// environment proxy, whatever their arguments: the transport helpers.
var approvedTransportConstructors = map[string]bool{
	modulePath + "/internal/outboundhttp.Transport":             true,
	modulePath + "/internal/outboundhttp.SharedTransport":       true,
	modulePath + "/internal/outboundhttp.PublicTransport":       true,
	modulePath + "/internal/outboundhttp.SharedPublicTransport": true,
}

const maxProofDepth = 8

// provenTransport reports whether expression, the value of a Transport, cannot be nil at run time. Anything it cannot
// prove (a parameter, a field read, a package variable, a call to another function) is unproven: the site then needs
// an allow row naming why it is safe, or a helper.
func provenTransport(info *types.Info, declaration ast.Decl, expression ast.Expr, depth int) bool {
	if depth > maxProofDepth || expression == nil {
		return false
	}
	switch e := ast.Unparen(expression).(type) {
	case *ast.UnaryExpr:
		if lit, ok := ast.Unparen(e.X).(*ast.CompositeLit); ok && e.Op.String() == "&" {
			return provenLiteral(info, declaration, lit, depth)
		}
	case *ast.CompositeLit:
		return provenLiteral(info, declaration, e, depth)
	case *ast.CallExpr:
		if tv, ok := info.Types[e.Fun]; ok && tv.IsType() {
			if len(e.Args) != 1 {
				return false
			}
			if _, ok := ast.Unparen(e.Args[0]).(*ast.FuncLit); ok {
				return true
			}
			return provenTransport(info, declaration, e.Args[0], depth+1)
		}
		fn := calledFunc(info, e)
		if fn == nil || fn.Pkg() == nil {
			return false
		}
		signature, _ := fn.Type().(*types.Signature)
		if signature != nil && signature.Recv() == nil {
			return approvedTransportConstructors[fn.Pkg().Path()+"."+fn.Name()]
		}
		if signature != nil && fn.Name() == "Clone" && isNamed(derefPointer(signature.Recv().Type()), "Transport") {
			if selector, ok := ast.Unparen(e.Fun).(*ast.SelectorExpr); ok {
				return provenTransport(info, declaration, selector.X, depth+1)
			}
		}
	case *ast.Ident:
		return provenVariable(info, declaration, e, depth)
	}
	return false
}

// provenLiteral: a net/http.Transport literal is a non-nil transport (its Proxy is checked as its own site); any other
// literal is proven when every field it holds of type net/http.RoundTripper or *net/http.Transport is proven, an
// omitted one included (a wrapper over a nil base).
func provenLiteral(info *types.Info, declaration ast.Decl, lit *ast.CompositeLit, depth int) bool {
	tv, ok := info.Types[lit]
	if !ok {
		return false
	}
	if isNamed(tv.Type, "Transport") {
		return true
	}
	structType, ok := derefPointer(tv.Type).Underlying().(*types.Struct)
	if !ok {
		return true
	}
	for i := 0; i < structType.NumFields(); i++ {
		field := structType.Field(i)
		if !isNamed(field.Type(), "RoundTripper") && !isNamed(derefPointer(field.Type()), "Transport") {
			continue
		}
		value, present := fieldValue(lit, field.Name(), i)
		if !present || !provenTransport(info, declaration, value, depth+1) {
			return false
		}
	}
	return true
}

// provenVariable: a local variable of declaration defined exactly once, by `v := <proven>` or `var v = <proven>`, and
// never assigned again, never address-taken and never a range variable anywhere in the function (closures included).
// No control flow is reasoned about: parameters, receivers, package variables, a variable with a nil fallback and any
// other shape are unproven, and their site needs an allow row or a helper call.
func provenVariable(info *types.Info, declaration ast.Decl, ident *ast.Ident, depth int) bool {
	variable, ok := info.Uses[ident].(*types.Var)
	if !ok {
		return false
	}
	fn, ok := declaration.(*ast.FuncDecl)
	if !ok || fn.Body == nil {
		return false
	}
	refersTo := func(expression ast.Expr) bool {
		id, ok := ast.Unparen(expression).(*ast.Ident)
		return ok && (info.Defs[id] == variable || info.Uses[id] == variable)
	}
	var definition ast.Expr
	definitions, spoiled := 0, false
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.ValueSpec:
			for i, name := range n.Names {
				if info.Defs[name] != variable {
					continue
				}
				definitions++
				if len(n.Values) == len(n.Names) {
					definition = n.Values[i]
				} else {
					spoiled = true
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				if !refersTo(lhs) {
					continue
				}
				id, _ := ast.Unparen(lhs).(*ast.Ident)
				if n.Tok.String() == ":=" && id != nil && info.Defs[id] == variable && len(n.Lhs) == len(n.Rhs) {
					definitions++
					definition = n.Rhs[i]
				} else {
					spoiled = true
				}
			}
		case *ast.UnaryExpr:
			if n.Op.String() == "&" && refersTo(n.X) {
				spoiled = true
			}
		case *ast.RangeStmt:
			if (n.Key != nil && refersTo(n.Key)) || (n.Value != nil && refersTo(n.Value)) {
				spoiled = true
			}
		}
		return true
	})
	return !spoiled && definitions == 1 && provenTransport(info, declaration, definition, depth+1)
}

func calledFunc(info *types.Info, call *ast.CallExpr) *types.Func {
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		fn, _ := info.Uses[fun].(*types.Func)
		return fn
	case *ast.SelectorExpr:
		fn, _ := info.Uses[fun.Sel].(*types.Func)
		return fn
	}
	return nil
}

// isDefaultTransportVar: expression names the package variable net/http.DefaultTransport.
func isDefaultTransportVar(info *types.Info, expression ast.Expr) bool {
	var ident *ast.Ident
	switch e := ast.Unparen(expression).(type) {
	case *ast.Ident:
		ident = e
	case *ast.SelectorExpr:
		ident = e.Sel
	default:
		return false
	}
	obj, ok := info.Uses[ident].(*types.Var)
	return ok && obj.Pkg() != nil && obj.Pkg().Path() == "net/http" && obj.Name() == "DefaultTransport"
}

// forbiddenKinds can never be allowed: an allow row for one is itself a problem.
var forbiddenKinds = map[string]bool{"default-transport-assign": true}

// isNamed: the type is exactly net/http.<name> (not a pointer to it).
func isNamed(t types.Type, name string) bool {
	return isNamedIn(t, "net/http", name)
}

func isReverseProxy(t types.Type) bool { return isNamedIn(t, "net/http/httputil", "ReverseProxy") }

// isReverseProxyRef: the type is httputil.ReverseProxy or a pointer to it.
func isReverseProxyRef(t types.Type) bool { return isReverseProxy(derefPointer(t)) }

func isNamedIn(t types.Type, path, name string) bool {
	if t == nil {
		return false
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == path && obj.Name() == name
}

func derefPointer(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	if pointer, ok := types.Unalias(t).(*types.Pointer); ok {
		return pointer.Elem()
	}
	return t
}

// fieldValue is the value the literal gives the field: by key, or (an unkeyed literal) by position when position >= 0.
func fieldValue(lit *ast.CompositeLit, field string, position int) (ast.Expr, bool) {
	for i, element := range lit.Elts {
		keyed, ok := element.(*ast.KeyValueExpr)
		if !ok {
			if i == position {
				return element, true
			}
			continue
		}
		if key, ok := keyed.Key.(*ast.Ident); ok && key.Name == field {
			return keyed.Value, true
		}
	}
	return nil, false
}

func isNil(info *types.Info, expression ast.Expr) bool {
	tv, ok := info.Types[expression]
	return ok && tv.IsNil()
}

func declSymbol(declaration ast.Decl) string {
	switch d := declaration.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil && len(d.Recv.List) == 1 {
			return receiverName(d.Recv.List[0].Type) + "." + d.Name.Name
		}
		return d.Name.Name
	case *ast.GenDecl:
		if len(d.Specs) > 0 {
			switch spec := d.Specs[0].(type) {
			case *ast.ValueSpec:
				if len(spec.Names) > 0 {
					return "var " + spec.Names[0].Name
				}
			case *ast.TypeSpec:
				return "type " + spec.Name.Name
			}
		}
	}
	return "package-level"
}

func receiverName(expression ast.Expr) string {
	switch e := expression.(type) {
	case *ast.StarExpr:
		return receiverName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return receiverName(e.X)
	}
	return "?"
}

// compare is the verdict of the found sites against the allow rows, as sorted text.
func compare(found map[site]int, rows map[site]allowRow) []string {
	var problems []string
	for key, count := range found {
		row, ok := rows[key]
		switch {
		case forbiddenKinds[key.kind]:
			problems = append(problems, fmt.Sprintf("FORBIDDEN site (no allow row can admit it): %s %s %s (x%d)", key.file, key.symbol, key.kind, count))
		case !ok:
			problems = append(problems, fmt.Sprintf("UNLISTED site: %s %s %s (x%d)", key.file, key.symbol, key.kind, count))
		case row.count != count:
			problems = append(problems, fmt.Sprintf("COUNT differs: %s %s %s: allow.tsv %d, code %d", key.file, key.symbol, key.kind, row.count, count))
		case strings.TrimSpace(row.reason) == "":
			problems = append(problems, fmt.Sprintf("NO REASON: %s %s %s", key.file, key.symbol, key.kind))
		}
	}
	for key := range rows {
		if _, ok := found[key]; !ok {
			problems = append(problems, fmt.Sprintf("STALE row: %s %s %s", key.file, key.symbol, key.kind))
		}
	}
	sort.Strings(problems)
	return problems
}

func readAllow(t *testing.T, name string) map[site]allowRow {
	t.Helper()
	handle, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	rows := map[site]allowRow{}
	scanner := bufio.NewScanner(handle)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "\t")
		if len(fields) != 5 {
			t.Fatalf("%s:%d: want 5 tab-separated fields (file, symbol, kind, count, reason), got %d", name, line, len(fields))
		}
		count, err := strconv.Atoi(fields[3])
		if err != nil || count < 1 {
			t.Fatalf("%s:%d: count %q is not a positive integer", name, line, fields[3])
		}
		key := site{fields[0], fields[1], fields[2]}
		if _, dup := rows[key]; dup {
			t.Fatalf("%s:%d: duplicate row %v", name, line, key)
		}
		rows[key] = allowRow{count: count, reason: fields[4]}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

// outsideBuild is every production .go file of the module that no loaded build contains (another GOOS, a build tag).
func outsideBuild(t *testing.T, root string, loaded map[string]bool) []string {
	t.Helper()
	var outside []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "testdata":
				return filepath.SkipDir
			}
			if path != root {
				if _, statErr := os.Stat(filepath.Join(path, "go.mod")); statErr == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && !loaded[path] {
			rel, _ := filepath.Rel(root, path)
			outside = append(outside, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(outside)
	return outside
}

func scopeProblems(outside, pinned []string) []string {
	if strings.Join(outside, " ") != strings.Join(pinned, " ") {
		return []string{fmt.Sprintf("SCOPE LIMIT changed: production files outside every release build are %v, pinned %v (add the file with its reason, or move it into a build)", outside, pinned)}
	}
	return nil
}

// releaseTargets is one GOOS/GOARCH pair per GOOS releasebuild.Matrix ships (its first architecture). A file built for
// one architecture only would not be loaded and so lands in the out-of-scope list, which is pinned.
func releaseTargets() [][2]string {
	seen := map[string]bool{}
	var targets [][2]string
	for _, target := range releasebuild.Matrix() {
		if !seen[target.GOOS] {
			seen[target.GOOS] = true
			targets = append(targets, [2]string{target.GOOS, target.GOARCH})
		}
	}
	return targets
}

func uniqueStrings(values []string) []string {
	var out []string
	for i, value := range values {
		if i == 0 || value != values[i-1] {
			out = append(out, value)
		}
	}
	return out
}

func isBuiltinCall(info *types.Info, call *ast.CallExpr) bool {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}
	_, builtin := info.Uses[ident].(*types.Builtin)
	return builtin
}
