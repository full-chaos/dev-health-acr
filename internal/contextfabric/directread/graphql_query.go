package directread

// GraphQLRunner is graphql_query's pipeline (CHAOS-7036 design D.8, form
// iii; slice S1b, CHAOS-7075): one validated free-form GraphQL query, run
// for one principal through GWC's MCP listener (CHAOS-7085), with the SAME
// edge rules run_operation applies (graphql_policy.go derives them).
//
//	V1  size, parse, exactly one operation, type query            typed refusal
//	V2  syntax walk: no fragment, no directive, aliases on root fields only,
//	    no nested argument, root field allowlist (no introspection), depth,
//	    field, alias, root and complexity caps                    typed refusal
//	V3  validation against the vendored ops SDL                   query_invalid
//	M   per root field: arguments -> the candidate operation's variable tree;
//	    candidates strictest first (R2); the first one whose literals,
//	    selection (output allowlist), variables (7b) and constraints (7e')
//	    admit the root wins                                       typed refusal
//	7c  the subject gate for the root's ids; 7d restricted caller: forced
//	    repository scope (grant ∩ requested), empty = no_granted_scope
//	7e  acr-set values (orgId from the principal) and cost clamps
//	B   acr REBUILDS the query text from the validated tree: one aliased
//	    root field per selection, arguments as typed variables. The client
//	    text is never forwarded. Restricted caller: the row id leaf is added
//	    to the selection when its row list is selected.
//	V4  the rebuilt query and its variables validate against the SDL with
//	    every rule                                                query_invalid
//	8   the internal call to the MCP listener
//	9a  GraphQL errors = upstream_error; restricted caller: every row id in
//	    the grant, else the WHOLE answer is refused (row_outside_grant)
//	9b  output filter: only the SELECTED allowed paths leave acr; anything
//	    else is removed with an ERROR line
//	9a' max_bytes on the serialized data
//	10  D.7 status and the "context fabric graphql query" event
//
// Nothing is dispatched before every root passed M, 7c, 7d and V4: a
// refused query costs zero upstream requests. No model is called anywhere
// on this path (design A1.7).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"
	"github.com/vektah/gqlparser/v2/validator/rules"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// GraphQLRepeatedRootKeyReason is the refusal reason of a query that uses
// one root response key twice.
const GraphQLRepeatedRootKeyReason = "repeated_root_key: select each root once; put all fields in one selection"

// graphql_query refusal codes, in addition to run_operation's.
const (
	RefusalQueryInvalid            RefusalCode = "query_invalid"
	RefusalOperationTypeNotAllowed RefusalCode = "operation_type_not_allowed"
	RefusalRootFieldNotAllowed     RefusalCode = "root_field_not_allowed"
	RefusalFieldNotAllowed         RefusalCode = "field_not_allowed"
	RefusalFragmentNotAllowed      RefusalCode = "fragment_not_allowed"
	RefusalDirectiveNotAllowed     RefusalCode = "directive_not_allowed"
	RefusalQueryLimitExceeded      RefusalCode = "query_limit_exceeded"
	// RefusalReadBudgetExceeded: the MCP listener refused the query on its
	// ClickHouse bytes-read or time ceiling (MCP_READ_BUDGET_EXCEEDED).
	RefusalReadBudgetExceeded RefusalCode = "read_budget_exceeded"
)

// GraphQLRefusalCodes is the closed set of refusal codes graphql_query can
// answer: run_operation's codes (unknown_operation excepted: there is no
// operation name; team_inactive excepted: graphql_query names no team id) plus the query codes.
func GraphQLRefusalCodes() []RefusalCode {
	out := []RefusalCode{}
	for _, code := range OperationRefusalCodes() {
		if code != RefusalUnknownOperation && code != RefusalTeamInactive {
			out = append(out, code)
		}
	}
	return append(out,
		RefusalQueryInvalid, RefusalOperationTypeNotAllowed, RefusalRootFieldNotAllowed,
		RefusalFieldNotAllowed, RefusalFragmentNotAllowed, RefusalDirectiveNotAllowed,
		RefusalQueryLimitExceeded, RefusalReadBudgetExceeded,
	)
}

// Fixed vocabulary of graphql_query.
const (
	GraphQLListenerMCP        = "mcp"
	GraphQLQueryLogMessage    = "context fabric graphql query"
	GraphQLRowsForeignLog     = "context fabric graphql query refused a row outside the grant"
	GraphQLPathsRemovedLog    = "context fabric graphql query removed unselected response paths"
	GraphQLListenerRefusedLog = "context fabric graphql query was refused by the MCP listener"
	graphqlRebuiltOperation   = "AcrGraphQLQuery"
	graphqlUnknownRootField   = "unknown"
	graphqlTelemetryRootsMax  = 8
)

// GraphQLRequest is graphql_query's input.
type GraphQLRequest struct {
	Query     string          `json:"query"`
	Variables json.RawMessage `json:"variables,omitempty"`
	MaxBytes  int             `json:"max_bytes,omitempty"`
}

// GraphQLSource names where the data came from.
type GraphQLSource struct {
	Path         string `json:"path"`
	Service      string `json:"service"`
	Listener     string `json:"listener"`
	SchemaDigest string `json:"schema_digest"`
	// QueryDigest is the sha256 of the query text acr sent (the rebuilt
	// text, never the client's).
	QueryDigest string `json:"query_digest,omitempty"`
}

// GraphQLRootResult is the per-root disclosure of a served answer.
type GraphQLRootResult struct {
	Key                string             `json:"key"`
	Field              string             `json:"field"`
	Operation          string             `json:"operation"`
	Completeness       Completeness       `json:"completeness"`
	CompletenessReason CompletenessReason `json:"completeness_reason,omitempty"`
	EffectiveScope     *EffectiveScope    `json:"effective_scope"`
	// AddedPaths are row id paths acr added to the selection so the rows
	// could be checked against the caller's grant.
	AddedPaths []string `json:"added_paths"`
}

// GraphQLRefusal is run_operation's typed refusal plus the closed
// read-budget reason (set only with read_budget_exceeded).
type GraphQLRefusal struct {
	OperationRefusal
	ReadBudget ReadBudgetReason `json:"read_budget,omitempty"`
}

// UpstreamAcrDeadline is graphql_query's error class for a call acr's own
// deadline cut (a transport timeout), distinct from the listener's
// time_ceiling read-budget refusal.
const UpstreamAcrDeadline UpstreamErrorClass = "acr_deadline"

// graphql_query error classes for a typed MCP listener refusal (ops PR
// #3425): the identity carrier, or the query, was refused by the listener.
const (
	UpstreamCarrierRefused  UpstreamErrorClass = "carrier_refused"
	UpstreamListenerRefused UpstreamErrorClass = "listener_refused"
)

// GraphQLUpstreamErrorClasses is the closed set of graphql_query upstream
// error classes: run_operation's, with timeout replaced by acr_deadline.
func GraphQLUpstreamErrorClasses() []UpstreamErrorClass {
	out := []UpstreamErrorClass{}
	for _, c := range UpstreamErrorClassVocabulary() {
		if c != UpstreamTimeout {
			out = append(out, c)
		}
	}
	return append(out, UpstreamAcrDeadline, UpstreamCarrierRefused, UpstreamListenerRefused)
}

// GraphQLResponse is graphql_query's answer (design D.7 vocabulary).
type GraphQLResponse struct {
	Call               CallStatus          `json:"call"`
	Completeness       Completeness        `json:"completeness"`
	CompletenessReason CompletenessReason  `json:"completeness_reason,omitempty"`
	Result             ResultState         `json:"result,omitempty"`
	Refusal            *GraphQLRefusal     `json:"refusal,omitempty"`
	Source             GraphQLSource       `json:"source"`
	RootFields         []GraphQLRootResult `json:"root_fields"`
	Data               json.RawMessage     `json:"data,omitempty"`
	Errors             []OperationError    `json:"errors"`
	Page               OperationPage       `json:"page"`
	Consistency        string              `json:"consistency"`
	UntrustedContent   UntrustedContent    `json:"untrusted_content"`
}

// GraphQLQueryRecorder receives one record per Run.
type GraphQLQueryRecorder interface {
	RecordGraphQLQuery(ctx context.Context, principal storage.Principal, read GraphQLQueryRead)
}

// GraphQLQueryRead is the telemetry record of one Run: closed vocabularies,
// SDL field names, counts and digests. Never the query text, a variable
// value, a subject id, a row value or a credential.
type GraphQLQueryRead struct {
	CallerClass   CallerClass
	ScopeClass    ScopeClass
	Decision      string
	RefusalCode   RefusalCode
	ErrorClass    UpstreamErrorClass
	RootFields    []string
	Operations    []string
	RootCount     int
	AliasCount    int
	Depth         int
	FieldCount    int
	Complexity    int
	ForcedByGrant bool
	RowsChecked   int
	RowsForeign   int
	PathsRemoved  int
	Completeness  Completeness
	Result        ResultState
	Bytes         int
	Latency       time.Duration
	SchemaDigest  string
	QueryDigest   string
	ReadBudget    ReadBudgetReason
	// UpstreamStatus, GraphQLCode and Variable carry an upstream non-2xx
	// answer as run_operation does: the HTTP status and closed tokens only.
	UpstreamStatus int
	GraphQLCode    UpstreamGraphQLCode
	Variable       string
	ErrorMessage   string
}

// GraphQLRunnerConfig wires the runner.
type GraphQLRunnerConfig struct {
	Policy *GraphQLPolicy
	Gate   SubjectAuthorizer
	// Client calls GWC's MCP listener (NewHTTPGraphQLClient).
	Client QueryClient
	// Grants is optional (see GrantedRepositories).
	Grants GrantedRepositories
	// Recorder defaults to a SlogGraphQLRecorder over Logger.
	Recorder GraphQLQueryRecorder
	Logger   *slog.Logger
	Now      func() time.Time
}

// GraphQLRunner runs validated free-form queries.
type GraphQLRunner struct {
	policy   *GraphQLPolicy
	edge     *OperationRunner
	client   QueryClient
	recorder GraphQLQueryRecorder
	logger   *slog.Logger
	now      func() time.Time

	rowsForeignTotal  atomic.Int64
	pathsRemovedTotal atomic.Int64
}

// ErrGraphQLRunnerNotConfigured is returned for a runner without its parts.
var ErrGraphQLRunnerNotConfigured = errors.New("graphql runner is not configured")

// NewGraphQLRunner validates the wiring. Policy, Gate and Client are
// required.
func NewGraphQLRunner(cfg GraphQLRunnerConfig) (*GraphQLRunner, error) {
	if cfg.Policy == nil || storage.IsNil(cfg.Gate) || storage.IsNil(cfg.Client) {
		return nil, ErrGraphQLRunnerNotConfigured
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	// The edge (gate, forced scope, per-org limiter) is run_operation's own
	// code, reused: its recorder is silent, graphql_query writes its own line.
	edge, err := NewOperationRunner(OperationRunnerConfig{
		Catalogue: cfg.Policy.catalogue, Gate: cfg.Gate, Client: cfg.Client, Grants: cfg.Grants,
		Recorder: silentOperationRecorder{}, Logger: logger, Now: cfg.Now,
	})
	if err != nil {
		return nil, err
	}
	r := &GraphQLRunner{policy: cfg.Policy, edge: edge, client: cfg.Client, logger: logger, now: edge.now}
	if storage.IsNil(cfg.Recorder) {
		r.recorder = NewSlogGraphQLRecorder(logger)
	} else {
		r.recorder = cfg.Recorder
	}
	return r, nil
}

type silentOperationRecorder struct{}

func (silentOperationRecorder) RecordOperationRead(context.Context, storage.Principal, OperationRead) {
}

// Counters returns the process totals of foreign rows refused and response
// values removed. Both must stay 0.
func (r *GraphQLRunner) Counters() OperationRunnerCounters {
	return OperationRunnerCounters{RowsForeign: r.rowsForeignTotal.Load(), PathsRemoved: r.pathsRemovedTotal.Load()}
}

// Policy returns the runner's root-field policy.
func (r *GraphQLRunner) Policy() *GraphQLPolicy { return r.policy }

// gqlRun is the per-request state.
type gqlRun struct {
	r         *GraphQLRunner
	principal storage.Principal
	class     CallerClass
	resp      GraphQLResponse
	read      GraphQLQueryRead
}

func (x *gqlRun) refuse(code RefusalCode, reason, path string) GraphQLResponse {
	x.resp.Call = CallRefused
	x.resp.Refusal = &GraphQLRefusal{OperationRefusal: OperationRefusal{Code: code, Reason: reason, Path: safeRefusalPath(path)}}
	x.resp.Data = nil
	x.resp.RootFields = []GraphQLRootResult{}
	x.read.Decision = string(CallRefused)
	x.read.RefusalCode = code
	return x.resp
}

func (x *gqlRun) upstreamEntries(call CallStatus, class UpstreamErrorClass, entries []OperationError) GraphQLResponse {
	resp := x.upstream(call, class)
	if len(entries) > 0 {
		x.resp.Errors = entries
		x.resp.UntrustedContent.Fields = []string{"data", "errors"}
		x.read.ErrorMessage = logErrorMessage(entries)
		resp = x.resp
	}
	return resp
}

func (x *gqlRun) upstream(call CallStatus, class UpstreamErrorClass) GraphQLResponse {
	x.resp.Call = call
	x.resp.Errors = []OperationError{{Class: class}}
	x.resp.Data = nil
	x.resp.RootFields = []GraphQLRootResult{}
	x.read.Decision = string(call)
	x.read.ErrorClass = class
	return x.resp
}

// Run executes one graphql_query request. Refusals, upstream failures and
// served answers are a GraphQLResponse; the error return is for a runner
// that cannot decide (no configuration, no principal org, an unavailable
// gate decision). Nothing is served on an error.
func (r *GraphQLRunner) Run(ctx context.Context, principal storage.Principal, req GraphQLRequest) (GraphQLResponse, error) {
	if r == nil || r.policy == nil || r.edge == nil || r.client == nil {
		return GraphQLResponse{}, ErrGraphQLRunnerNotConfigured
	}
	// The listener refuses an empty or padded org header (401 invalid_org):
	// acr never sends one; such a principal ends here, before any work.
	if strings.TrimSpace(principal.OrgID) == "" || principal.OrgID != strings.TrimSpace(principal.OrgID) {
		return GraphQLResponse{}, ErrOperationPrincipalInvalid
	}
	start := r.now()
	digest := r.policy.catalogue.StampedSchemaDigest()
	x := &gqlRun{r: r, principal: principal, class: CallerClassFor(ClassifyPrincipal(principal))}
	x.read = GraphQLQueryRead{CallerClass: x.class, ScopeClass: ScopeNotReached, Completeness: CompletenessUnknown, SchemaDigest: digest}
	x.resp = GraphQLResponse{
		Completeness:     CompletenessUnknown,
		Source:           GraphQLSource{Path: OperationSourcePath, Service: OperationSourceService, Listener: GraphQLListenerMCP, SchemaDigest: digest},
		RootFields:       []GraphQLRootResult{},
		Errors:           []OperationError{},
		Consistency:      OperationConsistency,
		UntrustedContent: UntrustedContent{Untrusted: true, Fields: []string{"data"}},
	}
	resp, err := x.execute(ctx, req)
	x.read.Latency = r.now().Sub(start)
	if err != nil {
		if errors.Is(err, ErrOperationAuthorizationUnavailable) {
			x.read.Decision = OperationDecisionAuthorizationUnavailable
		}
		r.recorder.RecordGraphQLQuery(ctx, principal, x.read)
		return GraphQLResponse{}, err
	}
	x.read.Completeness = resp.Completeness
	x.read.Result = resp.Result
	r.recorder.RecordGraphQLQuery(ctx, principal, x.read)
	return resp, nil
}

// ---------------------------------------------------------------- V1-V3

// selNode is one selected field of the validated query.
type selNode struct {
	name     string
	list     bool // the field's SDL type is a list (at any level)
	listDeep int  // list nesting depth of the SDL type
	children []*selNode
}

func (n *selNode) child(name string) *selNode {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

// rootSel is one root selection of the query.
type rootSel struct {
	key    string // response key (alias or field name)
	field  string
	policy *GraphQLRootPolicy
	args   map[string]*ast.Value
	node   *selNode
}

// syntax is what the V2 walk measured.
type syntax struct {
	fields, aliases, depth int
}

func (x *gqlRun) execute(ctx context.Context, req GraphQLRequest) (GraphQLResponse, error) {
	r := x.r
	limits := r.policy.limits
	maxBytes := req.MaxBytes
	switch {
	case maxBytes < 0:
		return x.refuse(RefusalInvalidRequest, "max_bytes must not be negative", ""), nil
	case maxBytes == 0:
		maxBytes = DefaultOperationMaxBytes
	case maxBytes > MaxOperationMaxBytes:
		maxBytes = MaxOperationMaxBytes
	}
	x.resp.Page.MaxBytes = maxBytes

	// V1.
	if strings.TrimSpace(req.Query) == "" {
		return x.refuse(RefusalQueryInvalid, "query is empty", ""), nil
	}
	if len(req.Query) > limits.MaxQueryBytes {
		return x.refuse(RefusalQueryLimitExceeded, fmt.Sprintf("query text is longer than %d bytes", limits.MaxQueryBytes), ""), nil
	}
	doc, perr := parser.ParseQuery(&ast.Source{Name: "graphql_query", Input: req.Query})
	if perr != nil {
		return x.refuse(RefusalQueryInvalid, "query does not parse", ""), nil
	}
	if len(doc.Fragments) > 0 {
		return x.refuse(RefusalFragmentNotAllowed, "fragments are not served; select fields directly", ""), nil
	}
	if len(doc.Operations) != 1 {
		return x.refuse(RefusalQueryInvalid, "query must hold exactly one operation", ""), nil
	}
	opDef := doc.Operations[0]
	if opDef.Operation != ast.Query {
		return x.refuse(RefusalOperationTypeNotAllowed, "only query operations are served; the MCP surface is read-only", ""), nil
	}
	if len(opDef.Directives) > 0 {
		return x.refuse(RefusalDirectiveNotAllowed, "directives are not served", ""), nil
	}
	for _, v := range opDef.VariableDefinitions {
		if len(v.Directives) > 0 {
			return x.refuse(RefusalDirectiveNotAllowed, "directives are not served", ""), nil
		}
	}

	// V2: the syntax walk and the root allowlist, before any schema work.
	var syn syntax
	roots := make([]rootSel, 0, len(opDef.SelectionSet))
	rootKeys := map[string]bool{}
	for _, sel := range opDef.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			return x.refuse(RefusalFragmentNotAllowed, "fragments are not served; select fields directly", ""), nil
		}
		x.read.RootFields = append(x.read.RootFields, x.rootFieldName(f.Name))
		if strings.HasPrefix(f.Name, "__") {
			return x.refuse(RefusalRootFieldNotAllowed, "introspection is not served; data_catalog section schema describes the allowed schema", ""), nil
		}
		policy, ok := r.policy.Root(f.Name)
		if !ok {
			return x.refuse(RefusalRootFieldNotAllowed, "root field is not on the allowlist; data_catalog section schema lists the allowed root fields", ""), nil
		}
		// A root response key that appears twice is refused (r3 P2, lead
		// ruling): select each root once and put all its fields in one
		// selection. Root-level merging is a follow-up.
		if rootKeys[f.Alias] {
			// No path: the key is a client-chosen alias, never echoed (pr2 r3).
			return x.refuse(RefusalQueryInvalid, GraphQLRepeatedRootKeyReason, ""), nil
		}
		rootKeys[f.Alias] = true
		if len(f.Directives) > 0 {
			return x.refuse(RefusalDirectiveNotAllowed, "directives are not served", ""), nil
		}
		if f.Alias != f.Name {
			syn.aliases++
		}
		syn.fields++
		syn.depth = max(syn.depth, 1)
		if refusal := walkSyntax(f.SelectionSet, 2, &syn); refusal != nil {
			return x.refuse(refusal.Code, refusal.Reason, ""), nil
		}
		args := map[string]*ast.Value{}
		for _, a := range f.Arguments {
			args[a.Name] = a.Value
		}
		roots = append(roots, rootSel{key: f.Alias, field: f.Name, policy: policy, args: args})
	}
	x.read.RootCount, x.read.AliasCount, x.read.Depth, x.read.FieldCount = len(roots), syn.aliases, syn.depth, syn.fields
	complexity, compute := 0, 0
	for _, root := range roots {
		class := root.policy.CostClass()
		complexity += GraphQLCostWeight(class)
		if class == CostCompute {
			compute++
		}
	}
	x.read.Complexity = complexity
	switch {
	case len(roots) == 0:
		return x.refuse(RefusalQueryInvalid, "query selects no root field", ""), nil
	case len(roots) > limits.MaxRootFields:
		return x.refuse(RefusalQueryLimitExceeded, fmt.Sprintf("query selects more than %d root fields", limits.MaxRootFields), ""), nil
	case syn.aliases > limits.MaxAliases:
		return x.refuse(RefusalQueryLimitExceeded, fmt.Sprintf("query uses more than %d aliases", limits.MaxAliases), ""), nil
	case syn.depth > limits.MaxDepth:
		return x.refuse(RefusalQueryLimitExceeded, fmt.Sprintf("selection is deeper than %d", limits.MaxDepth), ""), nil
	case syn.fields > limits.MaxFields:
		return x.refuse(RefusalQueryLimitExceeded, fmt.Sprintf("query selects more than %d fields", limits.MaxFields), ""), nil
	case complexity > limits.MaxComplexity:
		return x.refuse(RefusalQueryLimitExceeded, fmt.Sprintf("query complexity %d is over the limit %d", complexity, limits.MaxComplexity), ""), nil
	case compute > limits.MaxComputeRoots:
		return x.refuse(RefusalQueryLimitExceeded, fmt.Sprintf("query selects more than %d compute root fields", limits.MaxComputeRoots), ""), nil
	}

	// V3: the SDL. Required arguments and literal value types are checked
	// on the rebuilt query (V4): acr sets orgId and forced values the client
	// never writes.
	if errs := validator.Validate(r.policy.schema, doc, clientValidationRules()...); len(errs) > 0 {
		return x.refuse(RefusalQueryInvalid, "query does not validate against the served schema", ""), nil
	}
	// Literal kinds (r2 P2): ValuesOfCorrectType is not in the client rules
	// (acr sets orgId and forced fields), so each literal is checked here
	// against the type the SDL gives its position.
	if !literalKindsMatch(opDef) {
		return x.refuse(RefusalQueryInvalid, "a literal does not match the type of its argument", ""), nil
	}
	for i := range roots {
		roots[i].node = buildSelTree(roots[i].field, opDef.SelectionSet[i].(*ast.Field))
	}

	// Variables: typed by the definitions; a name the query does not define
	// is refused.
	vars, parseRefusal := decodeVariables(req.Variables)
	if parseRefusal != nil {
		return x.refuse(parseRefusal.Code, parseRefusal.Reason, ""), nil
	}
	defined := map[string]*ast.VariableDefinition{}
	for _, v := range opDef.VariableDefinitions {
		defined[v.Variable] = v
	}
	for name := range vars {
		if defined[name] == nil {
			return x.refuse(RefusalVariableNotAllowed, "variables names a variable the query does not define", ""), nil
		}
	}
	// Values are not coerced here: an input object may lack orgId, which acr
	// sets. The allowlist walk (7b) checks every leaf's shape and V4 checks
	// the rebuilt variables against the SDL with every rule.
	for name, def := range defined {
		if _, ok := vars[name]; !ok && def.DefaultValue != nil {
			value, err := astValueJSON(def.DefaultValue, nil)
			if err != nil {
				return x.refuse(RefusalQueryInvalid, "a variable default is not a constant", ""), nil
			}
			vars[name] = value
		}
	}

	// M + 7c/7d/7e per root.
	planned := make([]plannedRoot, 0, len(roots))
	for i := range roots {
		plan, resp, err := x.planRoot(ctx, i, roots[i], vars)
		if err != nil || resp != nil {
			if resp != nil {
				return *resp, nil
			}
			return GraphQLResponse{}, err
		}
		planned = append(planned, plan)
	}
	x.read.Operations = make([]string, len(planned))
	for i, p := range planned {
		x.read.Operations[i] = p.cand.op.Name
	}
	x.aggregateScope(planned)

	// The query acr sends, row ids added, stays under the field cap.
	sent := 0
	for _, p := range planned {
		sent += countNodes(p.sel.node)
	}
	if sent > limits.MaxFields {
		return x.refuse(RefusalQueryLimitExceeded, fmt.Sprintf("query with the row ids acr adds selects more than %d fields", limits.MaxFields), ""), nil
	}

	// B + V4.
	text, outVars := rebuildQuery(planned)
	sum := sha256.Sum256([]byte(text))
	x.resp.Source.QueryDigest = hex.EncodeToString(sum[:])
	x.read.QueryDigest = x.resp.Source.QueryDigest
	rebuilt, perr := parser.ParseQuery(&ast.Source{Name: "rebuilt", Input: text})
	if perr != nil {
		return x.refuse(RefusalPolicyStale, "the rebuilt query does not parse", ""), nil
	}
	if errs := validator.Validate(r.policy.schema, rebuilt); len(errs) > 0 {
		return x.refuse(RefusalQueryInvalid, "the query with acr-set values does not validate against the served schema (a required argument or field may be missing)", ""), nil
	}
	if _, err := validator.VariableValues(r.policy.schema, rebuilt.Operations[0], plainJSON(outVars)); err != nil {
		return x.refuse(RefusalQueryInvalid, "an argument value does not match the served schema", ""), nil
	}

	// Per-org concurrency (compute class) and the deadline.
	deadline := 0
	for _, p := range planned {
		deadline = max(deadline, p.cand.op.DeadlineSeconds)
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(deadline)*time.Second)
	defer cancel()
	acquired := map[string]bool{}
	for _, p := range planned {
		// One slot per distinct operation: two roots of one capped operation
		// share the call, and a second acquire would wait on the first.
		if p.cand.op.MaxInFlightPerOrg > 0 && !acquired[p.cand.op.Name] {
			acquired[p.cand.op.Name] = true
			release, ok := r.edge.limiter.acquire(callCtx, x.principal.OrgID+"\x00"+p.cand.op.Name, p.cand.op.MaxInFlightPerOrg)
			if !ok {
				return x.upstream(CallUpstreamTimeout, UpstreamConcurrency), nil
			}
			defer release()
		}
	}

	// 8.
	result, err := r.client.Execute(callCtx, QueryCall{OrgID: x.principal.OrgID, Document: text, Variables: outVars})
	if err != nil {
		return x.mapCallError(err, maxBytes), nil
	}
	if reason := ReadBudgetOf(result.Body); reason != "" {
		return x.readBudget(reason), nil
	}
	if class, reason := listenerRefusalOf(result.Body); class != "" {
		return x.listenerRefused(class, reason, result.StatusCode), nil
	}
	data, class9, entries, ok := parseGraphQLAnswer(result.Body, result.StatusCode)
	if !ok {
		return x.upstreamEntries(CallUpstreamError, class9, entries), nil
	}
	return x.answer(ctx, planned, data, maxBytes)
}

// clientValidationRules is every SDL rule but the two that the rebuilt
// query is checked with instead (acr sets orgId and forced input fields).
func clientValidationRules() []validator.Rule {
	return []validator.Rule{
		rules.FieldsOnCorrectTypeRule, rules.FragmentsOnCompositeTypesRule, rules.KnownArgumentNamesRule,
		rules.KnownDirectivesRule, rules.KnownFragmentNamesRule, rules.KnownRootTypeRule, rules.KnownTypeNamesRule,
		rules.LoneAnonymousOperationRule, rules.NoFragmentCyclesRule, rules.NoUndefinedVariablesRule,
		rules.NoUnusedFragmentsRule, rules.NoUnusedVariablesRule, rules.OverlappingFieldsCanBeMergedRule,
		rules.PossibleFragmentSpreadsRule, rules.ScalarLeafsRule, rules.SingleFieldSubscriptionsRule,
		rules.UniqueArgumentNamesRule, rules.UniqueDirectivesPerLocationRule, rules.UniqueFragmentNamesRule,
		rules.UniqueInputFieldNamesRule, rules.UniqueOperationNamesRule, rules.UniqueVariableNamesRule,
		rules.VariablesAreInputTypesRule, rules.VariablesInAllowedPositionRule,
	}
}

// rootFieldName is the telemetry name of a root field: an SDL Query field
// name, else "unknown". A client string never reaches the line.
func (x *gqlRun) rootFieldName(name string) string {
	if len(x.read.RootFields) >= graphqlTelemetryRootsMax {
		return graphqlUnknownRootField
	}
	if def := x.r.policy.schema.Query.Fields.ForName(name); def != nil && !strings.HasPrefix(name, "__") {
		return name
	}
	switch name {
	case "__schema", "__type", "__typename":
		return name
	}
	return graphqlUnknownRootField
}

// literalKindsMatch walks every argument value (and variable default) of
// the validated operation and checks each literal against the SDL type the
// validator attached to its position: an enum position takes an enum
// literal, a String/ID/custom scalar position a string, Int an integer,
// Float an integer or a float, Boolean a boolean. null and variables are
// left to the rebuilt-query check.
func literalKindsMatch(op *ast.OperationDefinition) bool {
	var value func(v *ast.Value) bool
	value = func(v *ast.Value) bool {
		if v == nil || v.Kind == ast.Variable || v.Kind == ast.NullValue {
			return true
		}
		if v.ExpectedType != nil && v.ExpectedType.Elem != nil && v.Kind != ast.ListValue {
			// A single value in a list position is coerced to a list: check
			// it against the element type.
			elem := *v
			elem.ExpectedType = v.ExpectedType.Elem
			return value(&elem)
		}
		switch v.Kind {
		case ast.ListValue, ast.ObjectValue:
			for _, c := range v.Children {
				if !value(c.Value) {
					return false
				}
			}
			return true
		}
		def := v.Definition
		if def == nil {
			return false // a literal the validator could not type is not sent
		}
		switch def.Kind {
		case ast.Enum:
			return v.Kind == ast.EnumValue
		case ast.Scalar:
			switch def.Name {
			case "Int":
				return v.Kind == ast.IntValue
			case "Float":
				return v.Kind == ast.IntValue || v.Kind == ast.FloatValue
			case "Boolean":
				return v.Kind == ast.BooleanValue
			default: // String, ID, custom scalars (Date, DateTime, JSON)
				return v.Kind == ast.StringValue || v.Kind == ast.BlockValue
			}
		default:
			return false
		}
	}
	for _, d := range op.VariableDefinitions {
		if !value(d.DefaultValue) {
			return false
		}
	}
	var walk func(set ast.SelectionSet) bool
	walk = func(set ast.SelectionSet) bool {
		for _, sel := range set {
			f, ok := sel.(*ast.Field)
			if !ok {
				continue
			}
			for _, a := range f.Arguments {
				if !value(a.Value) {
					return false
				}
			}
			if !walk(f.SelectionSet) {
				return false
			}
		}
		return true
	}
	return walk(op.SelectionSet)
}

// walkSyntax checks one selection set below a root field.
func walkSyntax(set ast.SelectionSet, depth int, syn *syntax) *Refusal {
	for _, sel := range set {
		f, ok := sel.(*ast.Field)
		if !ok {
			return &Refusal{Code: RefusalFragmentNotAllowed, Reason: "fragments are not served; select fields directly"}
		}
		syn.fields++
		syn.depth = max(syn.depth, depth)
		if len(f.Directives) > 0 {
			return &Refusal{Code: RefusalDirectiveNotAllowed, Reason: "directives are not served"}
		}
		if f.Alias != f.Name {
			syn.aliases++
			return &Refusal{Code: RefusalFieldNotAllowed, Reason: "an alias is allowed only on a root field"}
		}
		if len(f.Arguments) > 0 {
			return &Refusal{Code: RefusalVariableNotAllowed, Reason: "arguments are allowed only on root fields"}
		}
		if refusal := walkSyntax(f.SelectionSet, depth+1, syn); refusal != nil {
			return refusal
		}
	}
	return nil
}

func buildSelTree(name string, f *ast.Field) *selNode {
	n := &selNode{name: name}
	if f.Definition != nil && f.Definition.Type != nil {
		for t := f.Definition.Type; t.Elem != nil; t = t.Elem {
			n.list = true
			n.listDeep++
		}
	}
	for _, sel := range f.SelectionSet {
		child := sel.(*ast.Field)
		n.merge(buildSelTree(child.Name, child))
	}
	return n
}

// merge adds a child selection, merging a repeated field's sub-selections
// recursively (GraphQL field merging: `values { value } values { count }`
// selects both value and count; OverlappingFieldsCanBeMerged held that the
// two are mergeable).
func (n *selNode) merge(child *selNode) {
	existing := n.child(child.name)
	if existing == nil {
		n.children = append(n.children, child)
		return
	}
	for _, grandchild := range child.children {
		existing.merge(grandchild)
	}
}

// leafPaths lists the generalized leaf paths ("a.b[*].c") of a selection.
func countNodes(n *selNode) int {
	count := 1
	for _, c := range n.children {
		count += countNodes(c)
	}
	return count
}

func leafPaths(n *selNode, prefix string, out *[]string) {
	path := n.name
	if prefix != "" {
		path = prefix + "." + n.name
	}
	path += strings.Repeat("[*]", n.listDeep)
	if len(n.children) == 0 {
		*out = append(*out, path)
		return
	}
	for _, c := range n.children {
		leafPaths(c, path, out)
	}
}

// plainJSON converts json.Number to float64/int64 values for the gqlparser
// variable validator (which accepts json.Number only at the top level).
func plainJSON(v map[string]any) map[string]any {
	var conv func(any) any
	conv = func(value any) any {
		switch t := value.(type) {
		case json.Number:
			if i, err := t.Int64(); err == nil {
				return i
			}
			f, _ := t.Float64()
			return f
		case map[string]any:
			out := make(map[string]any, len(t))
			for k, c := range t {
				out[k] = conv(c)
			}
			return out
		case []any:
			out := make([]any, len(t))
			for i, c := range t {
				out[i] = conv(c)
			}
			return out
		default:
			return value
		}
	}
	out := make(map[string]any, len(v))
	for k, c := range v {
		out[k] = conv(c)
	}
	return out
}

// ---------------------------------------------------------------- M, 7c-7e

// plannedRoot is one root after mapping, gate and forced scope.
type plannedRoot struct {
	index      int
	sel        rootSel
	cand       *rootCandidate
	scope      CallerScope
	tree       map[string]any
	effective  *EffectiveScope
	scopeClass ScopeClass
	forced     bool
	selected   []string // allowed leaf paths the client selected
	added      []string // row id paths acr added
}

// candidateOutcome is how far one candidate got.
type candidateOutcome struct {
	refusal *Refusal
	path    string
	stage   int // 1 fixed literal, 2 argument binding, 3 selection, 4 variables, 5 constraints, 6 caller class
	// terminal: the candidate admits the shape but refuses the caller
	// class; the search ends with its refusal.
	terminal bool
}

func (x *gqlRun) planRoot(ctx context.Context, index int, root rootSel, vars map[string]any) (plannedRoot, *GraphQLResponse, error) {
	var leaves []string
	leafPaths(root.node, "", &leaves)
	sort.Strings(leaves)

	var best *candidateOutcome
	for _, cand := range root.policy.candidates {
		tree, subjects, scope, outcome := x.tryCandidate(cand, root, leaves, vars)
		if outcome == nil {
			return x.finishRoot(ctx, index, root, cand, scope, tree, subjects, leaves)
		}
		if outcome.terminal {
			resp := x.refuse(outcome.refusal.Code, outcome.refusal.Reason, outcome.path)
			return plannedRoot{}, &resp, nil
		}
		if best == nil || outcome.stage > best.stage {
			best = outcome
		}
	}
	resp := x.refuse(best.refusal.Code, best.refusal.Reason, best.path)
	return plannedRoot{}, &resp, nil
}

func (x *gqlRun) tryCandidate(cand *rootCandidate, root rootSel, leaves []string, vars map[string]any) (map[string]any, []subjectUse, CallerScope, *candidateOutcome) {
	op := cand.op
	scope := op.Scope(x.class)
	// The caller-class rule is checked LAST (r2 P2): a candidate that
	// admits the shape but refuses the class is terminal, so a stricter
	// candidate's refusal never falls through to a less strict sibling.
	// Arguments: a literal argument is fixed (stage 1); every other argument
	// must bind a document variable (stage 2). A candidate that passes the
	// literals got further than one that does not.
	docVars := map[string]any{}
	names := sortedArgNames(root.args)
	for _, name := range names {
		lit, fixed := cand.literalJSON[name]
		if !fixed {
			continue
		}
		got, err := astValueJSON(root.args[name], vars)
		if err != nil || !reflect.DeepEqual(normalizeNumbers(got), normalizeNumbers(lit)) {
			return nil, nil, scope, &candidateOutcome{refusal: &Refusal{Code: RefusalVariableNotAllowed, Reason: "argument is fixed by the operation policy"}, path: name, stage: 1}
		}
	}
	for _, name := range names {
		if _, fixed := cand.literalJSON[name]; fixed {
			continue
		}
		variable, bound := cand.bindings[name]
		if !bound {
			return nil, nil, scope, &candidateOutcome{refusal: &Refusal{Code: RefusalVariableNotAllowed, Reason: "argument is not on the root field's allowlist"}, path: name, stage: 2}
		}
		got, err := astValueJSON(root.args[name], vars)
		if errors.Is(err, errAbsentVariable) {
			continue // an absent variable is an absent argument
		}
		if err != nil {
			return nil, nil, scope, &candidateOutcome{refusal: &Refusal{Code: RefusalInvalidRequest, Reason: "argument value cannot be read"}, path: name, stage: 2}
		}
		docVars[variable] = got
	}
	// Selection: every leaf on the operation's output allowlist.
	for _, leaf := range leaves {
		if op.OutputAllowed(leaf) {
			continue
		}
		reason := "field is not on the root field's output allowlist"
		if slicesContainsFunc(pathSegments(leaf), IsPersonNamed) {
			reason = "person-named fields are not served (design D.5)"
		} else if isWithheld(op, leaf) {
			reason = "field is withheld by the operation policy (free text evidence)"
		}
		return nil, nil, scope, &candidateOutcome{refusal: &Refusal{Code: RefusalFieldNotAllowed, Reason: reason}, path: leaf, stage: 3}
	}
	// 7b: the variable allowlist of the operation.
	tree, subjects, refusals := checkVariables(op, docVars)
	if len(refusals) > 0 {
		deepest := refusals[0]
		path := ""
		if _, known := op.Variable(deepest.path); known {
			path = argumentPath(cand, deepest.path)
		}
		return nil, nil, scope, &candidateOutcome{refusal: &Refusal{Code: deepest.code, Reason: deepest.reason}, path: path, stage: 4}
	}
	// 7e': constraints, the caller class's first.
	constraints := op.Constraints
	if x.class == CallerRestricted && scope.Served {
		constraints = append(append([]Constraint{}, scope.Constraints...), op.Constraints...)
	}
	if refusal := checkConstraints(tree, constraints, x.r.now()); refusal != nil {
		return nil, nil, scope, &candidateOutcome{refusal: refusal, stage: 5}
	}
	if !scope.Served {
		refusal := scope.Refusal
		if refusal == nil {
			refusal = &Refusal{Code: RefusalOperationNotServedForCaller, Reason: "root field is not served to this caller class"}
		}
		return nil, nil, scope, &candidateOutcome{refusal: refusal, stage: 6, terminal: true}
	}
	return tree, subjects, scope, nil
}

func (x *gqlRun) finishRoot(ctx context.Context, index int, root rootSel, cand *rootCandidate, scope CallerScope, tree map[string]any, subjects []subjectUse, leaves []string) (plannedRoot, *GraphQLResponse, error) {
	op := cand.op
	// 7c/7d with run_operation's own code; its refusal becomes ours.
	edgeRun := &run{r: x.r.edge, principal: x.principal}
	effective, refusalResp, err := edgeRun.gateAndScope(ctx, op, scope, x.class, tree, subjects)
	if err != nil {
		return plannedRoot{}, nil, err
	}
	if refusalResp != nil {
		resp := x.refuse(refusalResp.Refusal.Code, refusalResp.Refusal.Reason, "")
		return plannedRoot{}, &resp, nil
	}
	// 7e.
	if err := applyAcrValues(op, tree, x.principal.OrgID); err != nil {
		resp := x.refuse(RefusalPolicyStale, "the policy cannot place an acr-set value", "")
		return plannedRoot{}, &resp, nil
	}
	if err := applyCostClamps(op, tree); err != nil {
		resp := x.refuse(RefusalPolicyStale, "the policy cannot place a clamp", "")
		return plannedRoot{}, &resp, nil
	}
	plan := plannedRoot{
		index: index, sel: root, cand: cand, scope: scope, tree: tree, effective: effective,
		scopeClass: edgeRun.read.ScopeClass, forced: edgeRun.read.ForcedByGrant, selected: leaves,
	}
	// B': the row id leaf of every selected row list (restricted caller).
	if x.class == CallerRestricted {
		for _, rowPath := range scope.RowIDPaths {
			if addRowIDPath(root.node, rowPath) {
				plan.added = append(plan.added, rowPath)
			}
		}
	}
	return plan, nil, nil
}

// addRowIDPath adds a row id path to the selection when the row list (the
// path up to its first [*]) is selected. It reports whether it added any
// field.
func addRowIDPath(root *selNode, rowPath string) bool {
	segs := parsePath(rowPath)
	if len(segs) == 0 || segs[0].name != root.name {
		return false
	}
	// Find the first list segment; it must be selected for rows to exist.
	cur := root
	firstList := -1
	for i, s := range segs {
		if s.array {
			firstList = i
			break
		}
	}
	if firstList < 0 {
		return false
	}
	for i := 1; i <= firstList; i++ {
		next := cur.child(segs[i].name)
		if next == nil {
			return false // the row list is not selected: no rows come back
		}
		cur = next
	}
	if firstList == 0 {
		cur = root
	}
	added := false
	for i := firstList + 1; i < len(segs); i++ {
		next := cur.child(segs[i].name)
		if next == nil {
			next = &selNode{name: segs[i].name}
			if segs[i].array {
				next.list, next.listDeep = true, 1
			}
			cur.children = append(cur.children, next)
			added = true
		}
		cur = next
	}
	return added
}

func (x *gqlRun) aggregateScope(planned []plannedRoot) {
	rank := map[ScopeClass]int{ScopeNotReached: 0, ScopeOrgWide: 1, ScopeClient: 2, ScopeForcedGrant: 3}
	for _, p := range planned {
		if rank[p.scopeClass] > rank[x.read.ScopeClass] {
			x.read.ScopeClass = p.scopeClass
		}
		x.read.ForcedByGrant = x.read.ForcedByGrant || p.forced
	}
}

// argumentPath maps a variable path ("filter.repoIds") to the argument
// path the client wrote ("filter.repoIds" under the bound argument).
func argumentPath(cand *rootCandidate, varPath string) string {
	top, rest, _ := strings.Cut(varPath, ".")
	for arg, variable := range cand.bindings {
		if variable == top {
			if rest == "" {
				return arg
			}
			return arg + "." + rest
		}
	}
	return ""
}

func isWithheld(op *OperationPolicy, path string) bool {
	for _, w := range op.WithheldOutputs {
		if w.Path == path || strings.HasPrefix(path, w.Path+".") {
			return true
		}
	}
	return false
}

func slicesContainsFunc(s []string, fn func(string) bool) bool {
	for _, v := range s {
		if fn(v) {
			return true
		}
	}
	return false
}

func sortedArgNames(args map[string]*ast.Value) []string {
	out := make([]string, 0, len(args))
	for k := range args {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func normalizeNumbers(v any) any {
	switch t := v.(type) {
	case json.Number:
		return t.String()
	case []any:
		out := make([]any, len(t))
		for i, c := range t {
			out[i] = normalizeNumbers(c)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, c := range t {
			out[k] = normalizeNumbers(c)
		}
		return out
	default:
		return v
	}
}

// ---------------------------------------------------------------- B

// rebuildQuery prints the query acr sends: one aliased root field per
// selection, every bound argument as a typed variable r<i>_<var>, every
// literal argument as the document's own constant, and the selection tree.
// Identifiers come from the parser (GraphQL names), so printing them is
// safe; no client value is printed into the text.
func rebuildQuery(planned []plannedRoot) (string, map[string]any) {
	var defs []string
	var body strings.Builder
	vars := map[string]any{}
	for _, p := range planned {
		body.WriteString("  ")
		body.WriteString(p.sel.key)
		body.WriteString(": ")
		body.WriteString(p.sel.field)
		var args []string
		names := make([]string, 0, len(p.cand.bindings)+len(p.cand.literals))
		for arg := range p.cand.bindings {
			names = append(names, arg)
		}
		for arg := range p.cand.literals {
			names = append(names, arg)
		}
		sort.Strings(names)
		for _, arg := range names {
			if lit, ok := p.cand.literals[arg]; ok {
				args = append(args, arg+": "+lit.String())
				continue
			}
			variable := p.cand.bindings[arg]
			value, present := p.tree[variable]
			if !present {
				continue
			}
			name := fmt.Sprintf("r%d_%s", p.index, variable)
			defs = append(defs, "$"+name+": "+p.cand.varTypes[variable])
			vars[name] = value
			args = append(args, arg+": $"+name)
		}
		if len(args) > 0 {
			body.WriteString("(" + strings.Join(args, ", ") + ")")
		}
		printSelection(&body, p.sel.node.children, 2)
		body.WriteString("\n")
	}
	head := "query " + graphqlRebuiltOperation
	if len(defs) > 0 {
		head += "(" + strings.Join(defs, ", ") + ")"
	}
	return head + " {\n" + body.String() + "}", vars
}

func printSelection(b *strings.Builder, children []*selNode, indent int) {
	if len(children) == 0 {
		return
	}
	b.WriteString(" {\n")
	for _, c := range children {
		b.WriteString(strings.Repeat("  ", indent))
		b.WriteString(c.name)
		printSelection(b, c.children, indent+1)
		b.WriteString("\n")
	}
	b.WriteString(strings.Repeat("  ", indent-1))
	b.WriteString("}")
}

// ---------------------------------------------------------------- 9, 10

func (x *gqlRun) answer(ctx context.Context, planned []plannedRoot, data []byte, maxBytes int) (GraphQLResponse, error) {
	r := x.r
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil || root == nil {
		return x.upstream(CallUpstreamError, UpstreamDecode), nil
	}
	out := map[string]any{}
	results := make([]GraphQLRootResult, 0, len(planned))
	removedPaths := map[string]bool{}
	var joined *CompletenessVerdict
	known := map[string]bool{}
	for _, p := range planned {
		known[p.sel.key] = true
	}
	for key := range root {
		if !known[key] {
			x.read.PathsRemoved++
			removedPaths["(root key)"] = true
		}
	}
	for _, p := range planned {
		op := p.cand.op
		wrapped, err := json.Marshal(map[string]any{p.sel.field: root[p.sel.key]})
		if err != nil {
			return x.upstream(CallUpstreamError, UpstreamDecode), nil
		}
		if x.class == CallerRestricted {
			checked, err := checkRows(wrapped, p.scope.RowIDPaths, edgeGrantSet(p.effective))
			if err != nil {
				return x.upstream(CallUpstreamError, UpstreamDecode), nil
			}
			x.read.RowsChecked += checked.checked
			x.read.RowsForeign += checked.foreign
			if checked.foreign > 0 {
				r.rowsForeignTotal.Add(int64(checked.foreign))
				r.logger.ErrorContext(ctx, GraphQLRowsForeignLog,
					"org_id", contextfabric.SanitizeLogAttr(x.principal.OrgID),
					"operation", contextfabric.SanitizeLogAttr(op.Name),
					"rows_checked", checked.checked,
					"rows_foreign", checked.foreign,
				)
				return x.refuse(RefusalRowOutsideGrant, "the upstream answer holds a row outside the caller's grant; the whole answer is refused", ""), nil
			}
		}
		// 9b: only what the client selected (plus the added row ids).
		narrowed := op.narrowedTo(append(append([]string{}, p.selected...), p.added...))
		filtered, err := FilterResponse(narrowed, wrapped)
		if err != nil {
			return x.upstream(CallUpstreamError, UpstreamDecode), nil
		}
		if filtered.RemovedValues > 0 {
			x.read.PathsRemoved += filtered.RemovedValues
			for _, path := range filtered.RemovedPaths {
				removedPaths[path] = true
			}
		}
		var kept map[string]any
		fdec := json.NewDecoder(bytes.NewReader(filtered.Data))
		fdec.UseNumber()
		if err := fdec.Decode(&kept); err != nil {
			return x.upstream(CallUpstreamError, UpstreamDecode), nil
		}
		out[p.sel.key] = kept[p.sel.field]
		verdict := op.Verdict(filtered.Data, p.tree)
		rootCompleteness := verdict.State
		joined = joinVerdicts(joined, verdict)
		added := p.added
		if added == nil {
			added = []string{}
		}
		results = append(results, GraphQLRootResult{
			Key: p.sel.key, Field: p.sel.field, Operation: op.Name, Completeness: rootCompleteness, CompletenessReason: verdict.Reason,
			EffectiveScope: p.effective, AddedPaths: added,
		})
	}
	if x.read.PathsRemoved > 0 {
		r.pathsRemovedTotal.Add(int64(x.read.PathsRemoved))
		paths := make([]string, 0, len(removedPaths))
		for p := range removedPaths {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		if len(paths) > 16 {
			paths = paths[:16]
		}
		r.logger.ErrorContext(ctx, GraphQLPathsRemovedLog,
			"org_id", contextfabric.SanitizeLogAttr(x.principal.OrgID),
			"removed_values", x.read.PathsRemoved,
			"removed_path_count", len(removedPaths),
			"removed_paths", contextfabric.SanitizeLogStrings(paths),
		)
	}
	final, err := json.Marshal(out)
	if err != nil {
		return x.upstream(CallUpstreamError, UpstreamDecode), nil
	}
	measured := len(final)
	x.read.Bytes = measured
	if measured > maxBytes {
		resp := x.refuse(RefusalResponseBudget, "the serialized data exceeds max_bytes; select fewer fields or narrow the window, the scope or the limit argument", "")
		resp.Refusal.MeasuredBytes = measured
		resp.Refusal.MaxBytes = maxBytes
		x.resp = resp
		return resp, nil
	}
	completeness := CompletenessUnknown
	if joined != nil {
		completeness = joined.State
		x.resp.CompletenessReason = joined.Reason
	}
	x.resp.Call = CallServed
	x.resp.Completeness = completeness
	x.resp.Result = ResultStateFor(dataIsEmpty(final), completeness)
	x.resp.Data = final
	x.resp.RootFields = results
	x.resp.Page.ReturnedBytes = measured
	x.read.Decision = string(CallServed)
	return x.resp, nil
}

func edgeGrantSet(effective *EffectiveScope) map[string]bool {
	grant := map[string]bool{}
	if effective == nil {
		return grant
	}
	for _, id := range effective.RepoIDs {
		grant[strings.ToLower(strings.TrimPrefix(id, "repository:"))] = true
	}
	return grant
}

// mapCallError is run_operation's D.7 mapping of an internal-call failure.
func (x *gqlRun) readBudget(reason ReadBudgetReason) GraphQLResponse {
	resp := x.refuse(RefusalReadBudgetExceeded, "the query service refused the query on its read budget (bytes read or time); select fewer fields or narrow the window, the scope or the limit argument", "")
	resp.Refusal.ReadBudget = reason
	x.resp = resp
	x.read.ReadBudget = reason
	return resp
}

// listenerRefused maps a typed MCP listener refusal (any HTTP status). acr
// refuses before the wire whatever the listener refuses, so it is a defect
// or a drift: loud, never passed through.
func (x *gqlRun) listenerRefused(refusal ListenerRefusalClass, reason string, status int) GraphQLResponse {
	class := UpstreamListenerRefused
	if refusal == ListenerRefusalCarrier {
		class = UpstreamCarrierRefused
	}
	// listener_reason is the closed listener vocabulary (or "unknown");
	// acr_precheck_gap marks a reason acr validates itself before the
	// wire: the listener refusing it means acr's own check failed.
	x.r.logger.Error(GraphQLListenerRefusedLog,
		"org_id", contextfabric.SanitizeLogAttr(x.principal.OrgID),
		"error_class", contextfabric.SanitizeLogAttr(string(class)),
		"listener_reason", contextfabric.SanitizeLogAttr(reason),
		"acr_precheck_gap", ListenerRefusalIsPrecheckGap(reason),
		"status", status,
	)
	return x.upstream(CallUpstreamError, class)
}

// GraphQLRootNotEnabledLog is the line for a listener 404: the roots acr sent
// and the listener's closed reason (root_field_not_enabled = the routing rows
// do not enable the root yet; retrying does not help until they do).
const GraphQLRootNotEnabledLog = "context fabric graphql query root is not enabled on the MCP listener"

func (x *gqlRun) logNotFound(qe *QueryError) {
	reason := ListenerNotFoundUnknown
	if qe != nil && qe.ListenerReason != "" {
		reason = qe.ListenerReason
	}
	roots := append([]string{}, x.read.RootFields...)
	x.r.logger.Warn(GraphQLRootNotEnabledLog,
		"org_id", contextfabric.SanitizeLogAttr(x.principal.OrgID),
		"listener_reason", contextfabric.SanitizeLogAttr(reason),
		"root_fields", contextfabric.SanitizeLogStrings(roots),
		"retryable", false,
	)
}

func (x *gqlRun) mapCallError(err error, maxBytes int) GraphQLResponse {
	var qe *QueryError
	if errors.As(err, &qe) && qe.ReadBudget != "" {
		return x.readBudget(qe.ReadBudget)
	}
	if qe != nil && qe.ListenerRefusal != "" {
		return x.listenerRefused(qe.ListenerRefusal, qe.ListenerReason, qe.StatusCode)
	}
	switch QueryErrorClassOf(err) {
	case QueryErrorNotFound:
		x.logNotFound(qe)
		return x.upstream(CallOperationUnavailable, UpstreamNotFound)
	case QueryErrorTimeout:
		// acr's own deadline cut the call before the listener answered
		// (ACR_DATA_QUERY_TIMEOUT or the operation deadline), distinct from
		// the listener's time_ceiling refusal.
		return x.upstream(CallUpstreamTimeout, UpstreamAcrDeadline)
	case QueryErrorCanceled:
		return x.upstream(CallUpstreamError, UpstreamCanceled)
	case QueryErrorRequestInvalid:
		return x.refuse(RefusalInvalidRequest, "the request does not fit the query service limits", "")
	case QueryErrorResponseTooLarge:
		resp := x.refuse(RefusalResponseBudget, "the upstream answer exceeds the read limit; select fewer fields or narrow the window", "")
		resp.Refusal.MeasuredBytes = MaxQueryResponseBytes + 1
		resp.Refusal.MaxBytes = maxBytes
		x.resp = resp
		return resp
	case QueryErrorHTTPStatus:
		return x.httpStatusError(err)
	default:
		return x.upstream(CallUpstreamError, UpstreamTransport)
	}
}

// httpStatusError carries the upstream HTTP status and the closed GraphQL
// code into errors[] and the log line. A GraphQL validation or parse
// rejection is the caller's request: a refusal, not an upstream error.
func (x *gqlRun) httpStatusError(err error) GraphQLResponse {
	entry, callerFault, ok := upstreamHTTPEntry(err)
	if !ok {
		return x.upstream(CallUpstreamError, UpstreamHTTPStatus)
	}
	x.read.UpstreamStatus, x.read.GraphQLCode, x.read.Variable = entry.Status, entry.GraphQLCode, entry.Variable
	if callerFault {
		x.refuse(RefusalInvalidRequest, RefusalReasonUpstreamRejectedRequest, "")
		x.resp.Errors = []OperationError{entry}
		x.read.ErrorClass = UpstreamHTTPStatus
		return x.resp
	}
	x.upstream(CallUpstreamError, UpstreamHTTPStatus)
	x.resp.Errors = []OperationError{entry}
	return x.resp
}

// ---------------------------------------------------------------- telemetry

// GraphQLQueryLogArgs renders one Run: closed vocabularies, SDL field
// names, counts and digests. Never the query text, a variable value, a
// subject id, a row value or a credential. The caller appends request_id.
func GraphQLQueryLogArgs(principal storage.Principal, read GraphQLQueryRead) []any {
	roots := read.RootFields
	if roots == nil {
		roots = []string{}
	}
	ops := read.Operations
	if ops == nil {
		ops = []string{}
	}
	args := []any{
		"org_id", contextfabric.SanitizeLogAttr(principal.OrgID),
		"caller_class", contextfabric.SanitizeLogAttr(string(read.CallerClass)),
		"scope_class", contextfabric.SanitizeLogAttr(string(read.ScopeClass)),
		"decision", contextfabric.SanitizeLogAttr(read.Decision),
		"root_fields", contextfabric.SanitizeLogStrings(append([]string{}, roots...)),
		"operations", contextfabric.SanitizeLogStrings(append([]string{}, ops...)),
		"root_count", read.RootCount,
		"alias_count", read.AliasCount,
		"depth", read.Depth,
		"field_count", read.FieldCount,
		"complexity", read.Complexity,
		"forced_by_grant", read.ForcedByGrant,
		"rows_checked", read.RowsChecked,
		"rows_foreign", read.RowsForeign,
		"paths_removed", read.PathsRemoved,
		"completeness", contextfabric.SanitizeLogAttr(string(read.Completeness)),
		"bytes", read.Bytes,
		"latency_ms", read.Latency.Milliseconds(),
		"schema_digest", contextfabric.SanitizeLogAttr(read.SchemaDigest),
	}
	if read.QueryDigest != "" {
		args = append(args, "query_digest", contextfabric.SanitizeLogAttr(read.QueryDigest))
	}
	if read.Result != "" {
		args = append(args, "result", contextfabric.SanitizeLogAttr(string(read.Result)))
	}
	if read.RefusalCode != "" {
		args = append(args, "refusal_code", contextfabric.SanitizeLogAttr(string(read.RefusalCode)))
	}
	if read.ErrorClass != "" {
		args = append(args, "error_class", contextfabric.SanitizeLogAttr(string(read.ErrorClass)))
	}
	if read.ReadBudget != "" {
		args = append(args, "read_budget", contextfabric.SanitizeLogAttr(string(read.ReadBudget)))
	}
	if read.UpstreamStatus != 0 {
		args = append(args, "upstream_status", read.UpstreamStatus)
	}
	if read.GraphQLCode != "" {
		args = append(args, "graphql_code", contextfabric.SanitizeLogAttr(string(read.GraphQLCode)))
	}
	if read.Variable != "" {
		args = append(args, "variable", contextfabric.SanitizeLogAttr(read.Variable))
	}
	if read.ErrorMessage != "" {
		args = append(args, "error_message", contextfabric.SanitizeLogAttr(read.ErrorMessage))
	}
	return args
}

// SlogGraphQLRecorder is the production GraphQLQueryRecorder.
type SlogGraphQLRecorder struct {
	logger *slog.Logger
}

// NewSlogGraphQLRecorder returns a recorder over logger (nil = default).
func NewSlogGraphQLRecorder(logger *slog.Logger) *SlogGraphQLRecorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogGraphQLRecorder{logger: logger}
}

// RecordGraphQLQuery writes the "context fabric graphql query" line.
func (s *SlogGraphQLRecorder) RecordGraphQLQuery(ctx context.Context, principal storage.Principal, read GraphQLQueryRead) {
	if s == nil || s.logger == nil {
		return
	}
	args := GraphQLQueryLogArgs(principal, read)
	if requestID, ok := requestIDOf(ctx); ok {
		args = append(args, "request_id", contextfabric.SanitizeLogAttr(requestID))
	}
	s.logger.InfoContext(ctx, GraphQLQueryLogMessage, args...)
}
