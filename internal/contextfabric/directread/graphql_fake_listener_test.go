package directread_test

// fakeMCPListener is an httptest stand-in for GWC's MCP listener
// (CHAOS-7085, not built yet). It ENFORCES the listener contract the ticket
// states, so a test proves acr never sends what the listener would refuse:
//
//   - POST /graphql, Content-Type application/json only (no GET, no APQ
//     extensions, no websocket or multipart);
//   - no Authorization header; the four internal identity headers exactly
//     once; superuser and impersonation "false" (a "true" claim is refused
//     and recorded loudly); no privileged role;
//   - the query parses and validates against the ops SDL (every rule) and
//     its variables coerce;
//   - exactly one operation, type query; no __schema / __type anywhere;
//   - root fields on the configured allowlist; depth, alias, root and
//     complexity caps.
//
// Defaults are GWC's CHAOS-7085 values (relayed 2026-09-30): POST /query,
// depth 10, aliases 15, complexity 150 in gqlgen's default metric (one per
// selected field), the 14-root allowlist below plus __typename at the root,
// 403 elevated_claim on a superuser, impersonation or admin/owner/operator
// claim, and every orgId / org_id argument (inline, inside input objects or
// in variables) equal to the header org. Each is CONFIG a test can tighten.
//
// A request that passes is answered with typed data synthesized from the
// selection (lists hold two items); tests override the row ids or the whole
// answer to plant a leak.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// gwcMCPRoots is GWC's listener root allowlist (CHAOS-7085, 14 roots).
var gwcMCPRoots = []string{
	"compoundingRisk", "hotspots", "securityAlerts", "catalog", "complexityTimeseries", "cognitiveLoad",
	"analytics", "securityOverview", "workGraphEdges", "workGraphFlow", "workGraphArtifacts",
	"throughputForecast", "capacityForecasts", "capacityForecast",
}

// gwcMCPLimits are GWC's listener caps. MaxComplexity is gqlgen's default
// metric (selected fields); MaxRootFields and MaxFields are not listener
// caps and are left unbounded here.
var gwcMCPLimits = directread.GraphQLLimits{MaxQueryBytes: 1 << 20, MaxDepth: 10, MaxAliases: 15, MaxRootFields: 1000, MaxFields: 1 << 20, MaxComplexity: 150}

type fakeMCPConfig struct {
	Limits directread.GraphQLLimits
	Roots  map[string]bool
	// RowID returns the id written at a leaf named repoId or scopeId.
	RowID func() string
	// Plant, when set, edits the synthesized data before it is sent.
	Plant func(data map[string]any)
	// Status, when set, replaces the whole answer.
	Status func() (int, string)
	// ReadBudget, when set, answers every admitted query with the typed
	// read-budget refusal at this HTTP status (200 or 4xx), with
	// extensions.reason ReadBudgetReason (bytes_ceiling or time_ceiling).
	ReadBudget       int
	ReadBudgetReason string
	// Delay, when set, holds every answer this long (acr deadline tests).
	Delay time.Duration
}

type fakeMCPRecord struct {
	Header    http.Header
	Query     string
	Variables map[string]any
}

type fakeMCPListener struct {
	server  *httptest.Server
	cfg     fakeMCPConfig
	schema  *ast.Schema
	mu      sync.Mutex
	records []fakeMCPRecord
	refused []string
	loud    []string
}

func defaultFakeMCPConfig(t *testing.T) fakeMCPConfig {
	t.Helper()
	roots := map[string]bool{"__typename": true}
	for _, r := range gwcMCPRoots {
		roots[r] = true
	}
	return fakeMCPConfig{Limits: gwcMCPLimits, Roots: roots, RowID: func() string { return opRepoA }}
}

func newFakeMCPListener(t *testing.T, policy *directread.GraphQLPolicy, cfg fakeMCPConfig) *fakeMCPListener {
	t.Helper()
	l := &fakeMCPListener{cfg: cfg, schema: policy.Schema()}
	l.server = httptest.NewServer(http.HandlerFunc(l.serve))
	t.Cleanup(l.server.Close)
	return l
}

func (l *fakeMCPListener) refuse(w http.ResponseWriter, reason string) {
	l.refuseStatus(w, http.StatusBadRequest, reason)
}

func (l *fakeMCPListener) refuseStatus(w http.ResponseWriter, status int, reason string) {
	l.mu.Lock()
	l.refused = append(l.refused, reason)
	l.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"errors":[{"message":%q}]}`, reason)
}

func (l *fakeMCPListener) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	if r.Method != http.MethodPost || r.URL.Path != directread.GraphQLListenerPath {
		l.refuse(w, "route "+r.Method+" "+r.URL.Path)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		l.refuse(w, "content type")
		return
	}
	if _, ok := r.Header["Authorization"]; ok {
		l.refuse(w, "authorization header present")
		return
	}
	for _, h := range []string{directread.HeaderInternalOrgID, directread.HeaderInternalRole, directread.HeaderInternalSuperuser, directread.HeaderInternalImpersonationActive} {
		if len(r.Header.Values(h)) != 1 {
			l.refuse(w, "header "+h+" not exactly once")
			return
		}
	}
	if r.Header.Get(directread.HeaderInternalOrgID) == "" {
		l.refuse(w, "empty org")
		return
	}
	elevated := r.Header.Get(directread.HeaderInternalSuperuser) != "false" || r.Header.Get(directread.HeaderInternalImpersonationActive) != "false"
	switch strings.ToLower(r.Header.Get(directread.HeaderInternalRole)) {
	case "admin", "owner", "operator":
		elevated = true
	}
	if elevated {
		l.mu.Lock()
		l.loud = append(l.loud, "elevated_claim")
		l.mu.Unlock()
		l.refuseStatus(w, http.StatusForbidden, "elevated_claim")
		return
	}
	var body struct {
		Query      string          `json:"query"`
		Variables  map[string]any  `json:"variables"`
		Extensions json.RawMessage `json:"extensions"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		l.refuse(w, "body")
		return
	}
	if len(body.Extensions) > 0 {
		l.refuse(w, "persisted query extensions")
		return
	}
	l.mu.Lock()
	l.records = append(l.records, fakeMCPRecord{Header: r.Header.Clone(), Query: body.Query, Variables: body.Variables})
	l.mu.Unlock()
	doc, errs := gqlparser.LoadQuery(l.schema, body.Query)
	if len(errs) > 0 {
		l.refuse(w, "query does not validate: "+errs.Error())
		return
	}
	if len(doc.Operations) != 1 || doc.Operations[0].Operation != ast.Query {
		l.refuse(w, "not exactly one query operation")
		return
	}
	op := doc.Operations[0]
	if _, err := validator.VariableValues(l.schema, op, body.Variables); err != nil {
		l.refuse(w, "variables do not coerce: "+err.Error())
		return
	}
	org := r.Header.Get(directread.HeaderInternalOrgID)
	if reason := orgArgumentsMismatch(op, body.Variables, org); reason != "" {
		l.refuse(w, reason)
		return
	}
	depth, aliases, complexity := 0, 0, 0
	var walk func(set ast.SelectionSet, d int) string
	walk = func(set ast.SelectionSet, d int) string {
		for _, sel := range set {
			f, ok := sel.(*ast.Field)
			if !ok {
				return "fragment"
			}
			if f.Name == "__schema" || f.Name == "__type" {
				return "introspection"
			}
			if f.Alias != f.Name {
				aliases++
			}
			complexity++
			depth = max(depth, d)
			if reason := walk(f.SelectionSet, d+1); reason != "" {
				return reason
			}
		}
		return ""
	}
	if reason := walk(op.SelectionSet, 1); reason != "" {
		l.refuse(w, reason)
		return
	}
	for _, sel := range op.SelectionSet {
		f := sel.(*ast.Field)
		if !l.cfg.Roots[f.Name] {
			l.refuse(w, "root field "+f.Name+" not on the listener allowlist")
			return
		}
	}
	lim := l.cfg.Limits
	switch {
	case len(op.SelectionSet) > lim.MaxRootFields:
		l.refuse(w, "root fields over the cap")
		return
	case depth > lim.MaxDepth:
		l.refuse(w, "depth over the cap")
		return
	case aliases > lim.MaxAliases:
		l.refuse(w, "aliases over the cap")
		return
	case complexity > lim.MaxComplexity:
		l.refuse(w, "complexity over the cap")
		return
	}
	if l.cfg.Delay > 0 {
		time.Sleep(l.cfg.Delay)
	}
	if l.cfg.ReadBudget != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(l.cfg.ReadBudget)
		_, _ = io.WriteString(w, `{"errors":[{"message":"read budget exceeded: 5368709120 bytes","extensions":{"code":"`+directread.MCPReadBudgetExceededCode+`","reason":"`+l.cfg.ReadBudgetReason+`"}}],"data":null}`)
		return
	}
	if l.cfg.Status != nil {
		status, answer := l.cfg.Status()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
		return
	}
	data := map[string]any{}
	for _, sel := range op.SelectionSet {
		f := sel.(*ast.Field)
		data[f.Alias] = l.synthesize(f)
	}
	if l.cfg.Plant != nil {
		l.cfg.Plant(data)
	}
	out, _ := json.Marshal(map[string]any{"data": data})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (l *fakeMCPListener) synthesize(f *ast.Field) any {
	return l.value(f, f.Definition.Type)
}

func (l *fakeMCPListener) value(f *ast.Field, typ *ast.Type) any {
	if typ.Elem != nil {
		return []any{l.value(f, typ.Elem), l.value(f, typ.Elem)}
	}
	if f.Name == "__typename" {
		return f.ObjectDefinition.Name
	}
	def := l.schema.Types[typ.NamedType]
	switch def.Kind {
	case ast.Object, ast.Interface:
		obj := map[string]any{}
		for _, sel := range f.SelectionSet {
			child := sel.(*ast.Field)
			obj[child.Alias] = l.value(child, child.Definition.Type)
		}
		return obj
	case ast.Enum:
		return def.EnumValues[0].Name
	}
	switch f.Name {
	case "repoId", "scopeId":
		return l.cfg.RowID()
	}
	switch typ.NamedType {
	case "Int":
		return 1
	case "Float":
		return 0.5
	case "Boolean":
		return false
	case "JSON":
		return map[string]any{"high": 1}
	case "Date":
		return "2026-09-01"
	case "DateTime":
		return "2026-09-01T00:00:00Z"
	default:
		return "x"
	}
}

func (l *fakeMCPListener) requests() []fakeMCPRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]fakeMCPRecord(nil), l.records...)
}

func (l *fakeMCPListener) refusals() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.refused...)
}

func (l *fakeMCPListener) reset() {
	l.mu.Lock()
	l.records, l.refused, l.loud = nil, nil, nil
	l.mu.Unlock()
}

// orgArgumentsMismatch checks every orgId / org_id argument of the query,
// inline, inside input objects, and inside variable values, against the
// header org. An empty result means every one matched.
func orgArgumentsMismatch(op *ast.OperationDefinition, vars map[string]any, org string) string {
	var checkJSON func(v any) string
	checkJSON = func(v any) string {
		switch t := v.(type) {
		case map[string]any:
			for k, c := range t {
				if k == "orgId" || k == "org_id" {
					if s, _ := c.(string); s != org {
						return "org argument differs from the header org"
					}
				}
				if reason := checkJSON(c); reason != "" {
					return reason
				}
			}
		case []any:
			for _, c := range t {
				if reason := checkJSON(c); reason != "" {
					return reason
				}
			}
		}
		return ""
	}
	var checkValue func(name string, v *ast.Value) string
	checkValue = func(name string, v *ast.Value) string {
		if v == nil {
			return ""
		}
		if v.Kind == ast.Variable {
			value := vars[v.Raw]
			if name == "orgId" || name == "org_id" {
				if s, _ := value.(string); s != org {
					return "org variable differs from the header org"
				}
			}
			return checkJSON(value)
		}
		if name == "orgId" || name == "org_id" {
			if v.Raw != org {
				return "org literal differs from the header org"
			}
		}
		for _, c := range v.Children {
			if reason := checkValue(c.Name, c.Value); reason != "" {
				return reason
			}
		}
		return ""
	}
	var walk func(set ast.SelectionSet) string
	walk = func(set ast.SelectionSet) string {
		for _, sel := range set {
			f, ok := sel.(*ast.Field)
			if !ok {
				continue
			}
			for _, a := range f.Arguments {
				if reason := checkValue(a.Name, a.Value); reason != "" {
					return reason
				}
			}
			if reason := walk(f.SelectionSet); reason != "" {
				return reason
			}
		}
		return ""
	}
	return walk(op.SelectionSet)
}
