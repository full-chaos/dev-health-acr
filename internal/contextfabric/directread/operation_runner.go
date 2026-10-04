package directread

// OperationRunner is run_operation's pipeline (CHAOS-7036 design E.1 steps
// 7a to 10, D.2 to D.7): one allowlisted ops GraphQL operation, run for one
// principal through the internal query service, with edge-enforced scope.
//
//	7a  Catalogue.LookupFor(operation, caller class)         typed refusal
//	7b  variable path check (operation_edge.go)                typed refusal
//	7e' cross-path constraints (operation and caller scope)    typed refusal
//	7c  every subject id in the variables through the subject gate, once per
//	    request; any id not admitted = ONE answer, denied_or_not_found
//	7d  restricted caller: the forced variable is set to grant ∩ requested
//	    (bare ops ids); empty = no_granted_scope BEFORE dispatch
//	7e  acr-set values (orgId from the principal only, forced values) and
//	    cost clamps; per-org concurrency for the compute class
//	8   the internal call (QueryClient)
//	9a  GraphQL errors = upstream_error with a safe class; restricted caller:
//	    every row id must be in the grant, else the WHOLE answer is refused
//	    (row_outside_grant), with an ERROR line and zero data
//	9b  output allowlist (FilterResponse); removed paths = ERROR line
//	9a' max_bytes on the serialized data; an operation with one primary list
//	    (a row-limit variable and one top-level list) is cut to the largest
//	    whole-row page that fits, with the cut stated in page; any other
//	    answer over it, or a list whose first row does not fit, is
//	    response_budget, never a half row
//	10  D.7 status and the "context fabric operation read" event
//
// Constraints run before the gate (7e' above): they depend on the request
// shape only, so a refused shape costs no graph read and a refused shape
// never reaches the gate with its ids. The answer is the same either way;
// only which of two refusals is reported first can differ.
//
// No step uses a cursor, an earlier answer or a cached proof as permission:
// the gate is called for this request, with this principal, every time.
// No model is called anywhere on this path (design A1.7).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Response budget of run_operation (design D.6: max_bytes applies to the
// serialized data).
const (
	DefaultOperationMaxBytes = 32768
	MaxOperationMaxBytes     = 262144
)

// Fixed response vocabulary.
const (
	OperationSourcePath        = "graphql"
	OperationSourceService     = "dho query-api"
	OperationConsistency       = "best_effort"
	OperationReadLogMessage    = "context fabric operation read"
	OperationRowsForeignLog    = "context fabric operation row outside grant"
	OperationPathsRemovedLog   = "context fabric operation output path removed"
	operationUnknownName       = "unknown"
	operationRefusalPathMaxLen = 128
)

// RefusalDeniedOrNotFound is run_operation's public answer for a subject id
// the gate did not admit (denied, absent or malformed alike). It is the
// gate's public answer (gatevocab.PublicDeniedOrNotFound), not a member of
// RefusalCodeVocabulary: the policy artifact never assigns it.
const RefusalDeniedOrNotFound RefusalCode = PublicDeniedOrNotFound

// OperationRefusalCodes is every code an OperationResponse refusal can
// carry: the policy's closed vocabulary plus denied_or_not_found.
func OperationRefusalCodes() []RefusalCode {
	vocab := RefusalCodeVocabulary()
	return append(vocab[:], RefusalDeniedOrNotFound)
}

// UpstreamErrorClass is the closed vocabulary of the safe error class an
// upstream_error or upstream_timeout answer carries.
type UpstreamErrorClass string

const (
	UpstreamGraphQLErrors UpstreamErrorClass = "graphql_errors"
	UpstreamDecode        UpstreamErrorClass = "decode"
	UpstreamHTTPStatus    UpstreamErrorClass = "http_status"
	UpstreamTransport     UpstreamErrorClass = "transport"
	UpstreamCanceled      UpstreamErrorClass = "canceled"
	UpstreamTimeout       UpstreamErrorClass = "timeout"
	UpstreamNotFound      UpstreamErrorClass = "not_found"
	UpstreamConcurrency   UpstreamErrorClass = "concurrency_wait"
)

// UpstreamErrorClassVocabulary is the closed set of upstream error classes.
func UpstreamErrorClassVocabulary() [8]UpstreamErrorClass {
	return [8]UpstreamErrorClass{UpstreamGraphQLErrors, UpstreamDecode, UpstreamHTTPStatus, UpstreamTransport, UpstreamCanceled, UpstreamTimeout, UpstreamNotFound, UpstreamConcurrency}
}

// ScopeClass says how the request's repository scope was decided.
type ScopeClass string

const (
	// ScopeOrgWide: no repository scope was sent (unrestricted caller).
	ScopeOrgWide ScopeClass = "org_wide"
	// ScopeClient: the unrestricted caller named its own repositories.
	ScopeClient ScopeClass = "client_scope"
	// ScopeForcedGrant: a restricted caller; acr set the scope.
	ScopeForcedGrant ScopeClass = "forced_grant"
	// ScopeNotReached: the call ended before the scope was decided.
	ScopeNotReached ScopeClass = "not_reached"
)

// ScopeClassVocabulary is the closed set of scope classes.
func ScopeClassVocabulary() [4]ScopeClass {
	return [4]ScopeClass{ScopeOrgWide, ScopeClient, ScopeForcedGrant, ScopeNotReached}
}

// OperationDecisionAuthorizationUnavailable is the telemetry decision of a
// run the subject gate could not decide (Run returns
// ErrOperationAuthorizationUnavailable; no response is served).
const OperationDecisionAuthorizationUnavailable = "authorization_unavailable"

// Errors Run returns instead of a response. The route maps them to an
// internal or unavailable answer; they are never a caller refusal.
var (
	ErrOperationRunnerNotConfigured      = errors.New("operation runner is not configured")
	ErrOperationPrincipalInvalid         = errors.New("operation runner: principal has no organization")
	ErrOperationAuthorizationUnavailable = errors.New("operation runner: subject gate decision unavailable")
)

// OperationRequest is run_operation's input.
type OperationRequest struct {
	Operation string          `json:"operation"`
	Variables json.RawMessage `json:"variables,omitempty"`
	MaxBytes  int             `json:"max_bytes,omitempty"`
}

// OperationRefusal is a typed, terminal refusal. Reason never carries a
// client value; Path is set only for an allowlisted (policy) path.
type OperationRefusal struct {
	Code          RefusalCode `json:"code"`
	Reason        string      `json:"reason"`
	Path          string      `json:"path,omitempty"`
	MeasuredBytes int         `json:"measured_bytes,omitempty"`
	MaxBytes      int         `json:"max_bytes,omitempty"`
}

// OperationError is one safe upstream error entry.
type OperationError struct {
	Class UpstreamErrorClass `json:"class"`
	// Status is the upstream HTTP status, set with class http_status.
	Status int `json:"status,omitempty"`
	// GraphQLCode is the closed token of the upstream GraphQL error code.
	GraphQLCode UpstreamGraphQLCode `json:"graphql_code,omitempty"`
	// Variable is the GraphQL variable name the upstream rejected.
	Variable string `json:"variable,omitempty"`
}

// OperationSource names where the data came from.
type OperationSource struct {
	Path           string `json:"path"`
	Service        string `json:"service"`
	SchemaDigest   string `json:"schema_digest"`
	DocumentDigest string `json:"document_digest,omitempty"`
}

// EffectiveScope echoes the repository scope acr sent.
type EffectiveScope struct {
	RepoIDs       []string `json:"repo_ids"`
	ForcedByGrant bool     `json:"forced_by_grant"`
}

// OperationPage is the size disclosure.
type OperationPage struct {
	ReturnedBytes int `json:"returned_bytes"`
	MaxBytes      int `json:"max_bytes"`
	// RowsReturned, RowsRead and Cut are set only when the answer was cut
	// to the largest whole-row page that fits MaxBytes.
	RowsReturned int    `json:"rows_returned,omitempty"`
	RowsRead     int    `json:"rows_read,omitempty"`
	Cut          string `json:"cut,omitempty"`
}

// UntrustedContent marks fields that carry upstream text.
type UntrustedContent struct {
	Untrusted bool     `json:"untrusted"`
	Fields    []string `json:"fields"`
}

// OperationResponse is run_operation's answer (design D.2, D.7).
type OperationResponse struct {
	Call             CallStatus        `json:"call"`
	Completeness     Completeness      `json:"completeness"`
	Result           ResultState       `json:"result,omitempty"`
	Operation        string            `json:"operation"`
	Refusal          *OperationRefusal `json:"refusal,omitempty"`
	Source           OperationSource   `json:"source"`
	EffectiveScope   *EffectiveScope   `json:"effective_scope,omitempty"`
	Data             json.RawMessage   `json:"data,omitempty"`
	Errors           []OperationError  `json:"errors"`
	Page             OperationPage     `json:"page"`
	Consistency      string            `json:"consistency"`
	UntrustedContent UntrustedContent  `json:"untrusted_content"`
}

// SubjectAuthorizer is the gate the runner calls once per request.
// *SubjectGate implements it.
type SubjectAuthorizer interface {
	Authorize(ctx context.Context, principal storage.Principal, requested []contextfabric.SubjectRef) (AuthorizedSubjects, Authorization)
}

// GrantedRepositories lists the repository subjects ("repository:<uuid>") a
// restricted principal's grant reaches, so a request that names no
// repository can be scoped to the whole grant. The list is only a
// candidate set: every entry still passes the subject gate. Without this
// port a restricted request that names no repository is refused with
// scope_required.
type GrantedRepositories interface {
	GrantedRepositories(ctx context.Context, principal storage.Principal) ([]contextfabric.SubjectRef, error)
}

// OperationRecorder receives one record per Run.
type OperationRecorder interface {
	RecordOperationRead(ctx context.Context, principal storage.Principal, read OperationRead)
}

// OperationRead is the telemetry record of one Run: closed vocabularies and
// counts only. It never holds a subject id, a row value or a credential.
type OperationRead struct {
	// Operation is a catalogue name (served or not served) or "unknown".
	Operation         string
	CallerClass       CallerClass
	ScopeClass        ScopeClass
	Decision          string
	RefusalCode       RefusalCode
	ErrorClass        UpstreamErrorClass
	UpstreamStatus    int
	GraphQLCode       UpstreamGraphQLCode
	Variable          string
	ForcedByGrant     bool
	VariablesRejected int
	RowsChecked       int
	RowsForeign       int
	PathsRemoved      int
	Completeness      Completeness
	Result            ResultState
	Bytes             int
	Latency           time.Duration
	SchemaDigest      string
	DocumentDigest    string
	// QueryPath is the configured ops query path the client posts to (path
	// only, never the URL); empty when the client does not report one.
	QueryPath string
}

// QueryPathReporter is implemented by a QueryClient that can name the ops
// query path it posts to.
type QueryPathReporter interface {
	QueryPath() string
}

// OperationRunnerConfig wires the runner.
type OperationRunnerConfig struct {
	Catalogue *Catalogue
	Gate      SubjectAuthorizer
	Client    QueryClient
	// Grants is optional (see GrantedRepositories).
	Grants GrantedRepositories
	// Recorder defaults to a SlogOperationRecorder over Logger.
	Recorder OperationRecorder
	// Logger receives the ERROR lines; nil uses slog.Default().
	Logger *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// OperationRunner runs allowlisted operations.
type OperationRunner struct {
	catalogue *Catalogue
	gate      SubjectAuthorizer
	client    QueryClient
	grants    GrantedRepositories
	recorder  OperationRecorder
	logger    *slog.Logger
	now       func() time.Time
	limiter   *orgLimiter

	rowsForeignTotal  atomic.Int64
	pathsRemovedTotal atomic.Int64
}

// NewOperationRunner validates the wiring. Catalogue, Gate and Client are
// required.
func NewOperationRunner(cfg OperationRunnerConfig) (*OperationRunner, error) {
	if cfg.Catalogue == nil || storage.IsNil(cfg.Gate) || storage.IsNil(cfg.Client) {
		return nil, ErrOperationRunnerNotConfigured
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	r := &OperationRunner{
		catalogue: cfg.Catalogue,
		gate:      cfg.Gate,
		client:    cfg.Client,
		logger:    logger,
		now:       cfg.Now,
		limiter:   &orgLimiter{slots: map[string]chan struct{}{}},
	}
	if !storage.IsNil(cfg.Grants) {
		r.grants = cfg.Grants
	}
	if storage.IsNil(cfg.Recorder) {
		r.recorder = NewSlogOperationRecorder(logger)
	} else {
		r.recorder = cfg.Recorder
	}
	if r.now == nil {
		r.now = time.Now
	}
	return r, nil
}

// OperationRunnerCounters are the runner's process counters.
type OperationRunnerCounters struct {
	RowsForeign  int64
	PathsRemoved int64
}

// Counters returns the process totals of foreign rows refused and output
// values removed. Both must stay 0; any other value is an ERROR condition.
func (r *OperationRunner) Counters() OperationRunnerCounters {
	return OperationRunnerCounters{RowsForeign: r.rowsForeignTotal.Load(), PathsRemoved: r.pathsRemovedTotal.Load()}
}

// run is the per-request state.
type run struct {
	r         *OperationRunner
	principal storage.Principal
	resp      OperationResponse
	read      OperationRead
	start     time.Time
}

func (x *run) refuse(code RefusalCode, reason, path string) OperationResponse {
	x.resp.Call = CallRefused
	x.resp.Refusal = &OperationRefusal{Code: code, Reason: reason, Path: safeRefusalPath(path)}
	x.resp.Data = nil
	x.read.Decision = string(CallRefused)
	x.read.RefusalCode = code
	return x.resp
}

func (x *run) upstream(call CallStatus, class UpstreamErrorClass) OperationResponse {
	x.resp.Call = call
	x.resp.Errors = []OperationError{{Class: class}}
	x.resp.Data = nil
	x.read.Decision = string(call)
	x.read.ErrorClass = class
	return x.resp
}

// safeRefusalPath keeps a path only when it is short and uses path
// characters; anything else is a client string and is not echoed.
func safeRefusalPath(path string) string {
	if path == "" || len(path) > operationRefusalPathMaxLen {
		return ""
	}
	for _, r := range path {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.[*]", r)) {
			return ""
		}
	}
	return path
}

// Run executes one run_operation request. A refusal, an upstream failure
// and a served answer are all an OperationResponse; the error return is for
// a runner that cannot decide (no configuration, no principal org, an
// unavailable gate decision), which the route maps to an internal or
// unavailable answer. Nothing is served on an error.
func (r *OperationRunner) Run(ctx context.Context, principal storage.Principal, req OperationRequest) (OperationResponse, error) {
	if r == nil || r.catalogue == nil || r.gate == nil || r.client == nil {
		return OperationResponse{}, ErrOperationRunnerNotConfigured
	}
	if strings.TrimSpace(principal.OrgID) == "" {
		return OperationResponse{}, ErrOperationPrincipalInvalid
	}
	x := &run{r: r, principal: principal, start: r.now()}
	class := CallerClassFor(ClassifyPrincipal(principal))
	stampedDigest := r.catalogue.StampedSchemaDigest()
	x.read = OperationRead{
		Operation:    r.telemetryOperationName(req.Operation),
		CallerClass:  class,
		ScopeClass:   ScopeNotReached,
		Completeness: CompletenessUnknown,
		SchemaDigest: stampedDigest,
	}
	if reporter, ok := r.client.(QueryPathReporter); ok {
		x.read.QueryPath = reporter.QueryPath()
	}
	x.resp = OperationResponse{
		Completeness:     CompletenessUnknown,
		Operation:        x.read.Operation,
		Source:           OperationSource{Path: OperationSourcePath, Service: OperationSourceService, SchemaDigest: stampedDigest},
		Errors:           []OperationError{},
		Consistency:      OperationConsistency,
		UntrustedContent: UntrustedContent{Untrusted: true, Fields: []string{"data"}},
	}
	resp, err := x.execute(ctx, class, req)
	x.read.Latency = r.now().Sub(x.start)
	if err != nil {
		if errors.Is(err, ErrOperationAuthorizationUnavailable) {
			x.read.Decision = OperationDecisionAuthorizationUnavailable
		}
		r.recorder.RecordOperationRead(ctx, principal, x.read)
		return OperationResponse{}, err
	}
	x.read.Completeness = resp.Completeness
	x.read.Result = resp.Result
	r.recorder.RecordOperationRead(ctx, principal, x.read)
	return resp, nil
}

// telemetryOperationName logs a name only when the catalogue knows it.
func (r *OperationRunner) telemetryOperationName(name string) string {
	if _, refusal := r.catalogue.Lookup(name); refusal == nil {
		return name
	}
	for _, ns := range r.catalogue.NotServed() {
		if ns.Name == name {
			return name
		}
	}
	return operationUnknownName
}

func (x *run) execute(ctx context.Context, class CallerClass, req OperationRequest) (OperationResponse, error) {
	r := x.r
	// Response budget.
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

	// 7a: the operation allowlist and the caller-class rule.
	op, scope, refusal := r.catalogue.LookupFor(req.Operation, class)
	if refusal != nil {
		return x.refuse(refusal.Code, refusal.Reason, ""), nil
	}
	x.resp.Source.DocumentDigest = op.Digest
	x.read.DocumentDigest = op.Digest

	// 7b: the variable path allowlist.
	vars, parseRefusal := decodeVariables(req.Variables)
	if parseRefusal != nil {
		return x.refuse(parseRefusal.Code, parseRefusal.Reason, ""), nil
	}
	tree, subjects, refusals := checkVariables(op, vars)
	x.read.VariablesRejected = len(refusals)
	if len(refusals) > 0 {
		deepest := refusals[0]
		path := ""
		if _, known := op.Variable(deepest.path); known {
			path = deepest.path
		}
		return x.refuse(deepest.code, deepest.reason, path), nil
	}

	// 7e': cross-path constraints of the operation and of the caller class.
	now := r.now()
	// The caller-class constraints go first: a shape the class is not
	// served answers operation_not_served_for_caller, not a shape detail.
	constraints := op.Constraints
	if class == CallerRestricted {
		constraints = append(append([]Constraint{}, scope.Constraints...), op.Constraints...)
	}
	if refusal := checkConstraints(tree, constraints, now); refusal != nil {
		return x.refuse(refusal.Code, refusal.Reason, ""), nil
	}

	// 7c + 7d: the subject gate and the forced scope.
	effective, refusalResp, err := x.gateAndScope(ctx, op, scope, class, tree, subjects)
	if err != nil || refusalResp != nil {
		if refusalResp != nil {
			return *refusalResp, nil
		}
		return OperationResponse{}, err
	}
	x.resp.EffectiveScope = effective

	// 7e: acr-set values and cost clamps.
	if err := applyAcrValues(op, tree, x.principal.OrgID); err != nil {
		return x.refuse(RefusalPolicyStale, "the policy cannot place an acr-set value", ""), nil
	}
	if err := applyCostClamps(op, tree); err != nil {
		return x.refuse(RefusalPolicyStale, "the policy cannot place a clamp", ""), nil
	}

	// Per-org concurrency (the compute cost class).
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(op.DeadlineSeconds)*time.Second)
	defer cancel()
	if op.MaxInFlightPerOrg > 0 {
		release, ok := r.limiter.acquire(callCtx, x.principal.OrgID+"\x00"+op.Name, op.MaxInFlightPerOrg)
		if !ok {
			return x.upstream(CallUpstreamTimeout, UpstreamConcurrency), nil
		}
		defer release()
	}

	// 8: the internal call.
	result, err := r.client.Execute(callCtx, QueryCall{OrgID: x.principal.OrgID, Document: op.DocumentText, Variables: tree})
	if err != nil {
		return x.mapCallError(err, maxBytes), nil
	}

	// 9a: the GraphQL answer.
	data, class9, ok := parseGraphQLAnswer(result.Body)
	if !ok {
		return x.upstream(CallUpstreamError, class9), nil
	}
	if class == CallerRestricted {
		checked, err := checkRows(data, scope.RowIDPaths, x.grantSet(effective))
		if err != nil {
			return x.upstream(CallUpstreamError, UpstreamDecode), nil
		}
		x.read.RowsChecked, x.read.RowsForeign = checked.checked, checked.foreign
		if checked.foreign > 0 {
			r.rowsForeignTotal.Add(int64(checked.foreign))
			r.logger.ErrorContext(ctx, OperationRowsForeignLog,
				"org_id", contextfabric.SanitizeLogAttr(x.principal.OrgID),
				"operation", contextfabric.SanitizeLogAttr(op.Name),
				"rows_checked", checked.checked,
				"rows_foreign", checked.foreign,
			)
			return x.refuse(RefusalRowOutsideGrant, "the upstream answer holds a row outside the caller's grant; the whole answer is refused", ""), nil
		}
	}

	// 9b: the output allowlist.
	filtered, err := FilterResponse(op.documentOutputsOnly(), data)
	if err != nil {
		return x.upstream(CallUpstreamError, UpstreamDecode), nil
	}
	if filtered.RemovedValues > 0 {
		x.read.PathsRemoved = filtered.RemovedValues
		r.pathsRemovedTotal.Add(int64(filtered.RemovedValues))
		removed := filtered.RemovedPaths
		if len(removed) > 16 {
			removed = removed[:16]
		}
		r.logger.ErrorContext(ctx, OperationPathsRemovedLog,
			"org_id", contextfabric.SanitizeLogAttr(x.principal.OrgID),
			"operation", contextfabric.SanitizeLogAttr(op.Name),
			"removed_values", filtered.RemovedValues,
			"removed_path_count", len(filtered.RemovedPaths),
			"removed_paths", contextfabric.SanitizeLogStrings(append([]string{}, removed...)),
		)
	}

	// 9a': the response budget, on the serialized data.
	measured := len(filtered.Data)
	x.read.Bytes = measured
	var cut *pageCut
	if measured > maxBytes {
		if listPath, has := op.PrimaryList(); has {
			if fit, ok := fitListPage(filtered.Data, listPath, maxBytes); ok {
				cut = &fit
				filtered.Data = fit.data
				measured = len(fit.data)
				x.read.Bytes = measured
			}
		}
	}
	if measured > maxBytes {
		resp := x.refuse(RefusalResponseBudget, "the serialized data exceeds max_bytes; narrow the window, the scope or the limit variable", "")
		resp.Refusal.MeasuredBytes = measured
		resp.Refusal.MaxBytes = maxBytes
		x.resp = resp
		return resp, nil
	}

	// 10: D.7 status.
	completeness := op.Completeness(filtered.Data)
	if cut != nil {
		if completeness == CompletenessDeclaredComplete {
			completeness = CompletenessUnknown
		}
		x.resp.Page.RowsReturned = cut.rowsReturned
		x.resp.Page.RowsRead = cut.rowsRead
		x.resp.Page.Cut = cut.statement(maxBytes, op)
	}
	x.resp.Call = CallServed
	x.resp.Completeness = completeness
	x.resp.Result = ResultStateFor(dataIsEmpty(filtered.Data), completeness)
	x.resp.Data = filtered.Data
	x.resp.Page.ReturnedBytes = measured
	x.read.Decision = string(CallServed)
	return x.resp, nil
}

func (x *run) grantSet(effective *EffectiveScope) map[string]bool {
	grant := map[string]bool{}
	if effective == nil {
		return grant
	}
	for _, id := range effective.RepoIDs {
		bare := strings.TrimPrefix(id, "repository:")
		grant[strings.ToLower(bare)] = true
	}
	return grant
}

// decodeVariables reads the client's variables: absent or null is {}; any
// other non-object is invalid_request. Numbers stay json.Number.
func decodeVariables(raw json.RawMessage) (map[string]any, *Refusal) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return map[string]any{}, nil
	}
	if len(trimmed) > MaxQueryRequestBytes {
		return nil, &Refusal{Code: RefusalInvalidRequest, Reason: "variables exceed the 16 KiB request limit"}
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var vars map[string]any
	if err := dec.Decode(&vars); err != nil || vars == nil {
		return nil, &Refusal{Code: RefusalInvalidRequest, Reason: "variables must be a JSON object"}
	}
	if dec.More() {
		return nil, &Refusal{Code: RefusalInvalidRequest, Reason: "variables must be one JSON object"}
	}
	return vars, nil
}

// gateAndScope is steps 7c and 7d.
func (x *run) gateAndScope(ctx context.Context, op *OperationPolicy, scope CallerScope, class CallerClass, tree map[string]any, subjects []subjectUse) (*EffectiveScope, *OperationResponse, error) {
	r := x.r
	restricted := class == CallerRestricted

	// The forced path's client value decides where the candidate list comes
	// from: the client's own ids, or (absent / null) the whole grant.
	var fromGrant []contextfabric.SubjectRef
	if restricted {
		forced := lookupOne(tree, scope.ForcedVariablePath)
		if !isNullish(forced) {
			if items, ok := forced.value.([]any); ok && len(items) == 0 {
				resp := x.refuse(RefusalNoGrantedScope, "the requested repositories and the caller's grant do not intersect", "")
				return nil, &resp, nil
			}
		} else {
			if r.grants == nil {
				resp := x.refuse(RefusalScopeRequired, "a repository-restricted caller must name the repositories (find_subjects gives their ids)", "")
				return nil, &resp, nil
			}
			granted, err := r.grants.GrantedRepositories(ctx, x.principal)
			if errors.Is(err, ErrGrantedRepositoriesIncomplete) {
				// A grant too large to list whole is never served as a
				// partial scope: the caller names its repositories.
				resp := x.refuse(RefusalScopeRequired, "the caller's grant is too large to use whole; name the repositories (find_subjects gives their ids)", "")
				return nil, &resp, nil
			}
			if err != nil {
				return nil, nil, ErrOperationAuthorizationUnavailable
			}
			for _, ref := range granted {
				if ref.Kind == contextfabric.SubjectRepository {
					fromGrant = append(fromGrant, contextfabric.SubjectRef{Kind: ref.Kind, CanonicalID: ref.CanonicalID})
				}
			}
			if len(fromGrant) == 0 {
				resp := x.refuse(RefusalNoGrantedScope, "the caller's grant reaches no repository", "")
				return nil, &resp, nil
			}
		}
	}

	// 7c: one gate call for every id of this request.
	requested := make([]contextfabric.SubjectRef, 0, len(subjects)+len(fromGrant))
	for _, use := range subjects {
		requested = append(requested, contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(use.kind), CanonicalID: use.id})
	}
	requested = append(requested, fromGrant...)
	admitted := map[string]bool{}
	if len(requested) > 0 {
		proof, decision := r.gate.Authorize(ctx, x.principal, requested)
		if decision.Decision == DecisionUnavailable {
			return nil, nil, ErrOperationAuthorizationUnavailable
		}
		if proof.IssuedTo(x.principal) {
			for _, ref := range proof.Subjects() {
				admitted[string(ref.Kind)+"\x00"+ref.CanonicalID] = true
			}
		}
	}
	// Every client id must be admitted and convertible; one that is not is
	// ONE public answer, with no id echoed and no distinction.
	for _, use := range subjects {
		if !admitted[use.kind+"\x00"+strings.TrimSpace(use.id)] {
			resp := x.refuse(RefusalDeniedOrNotFound, "a named subject is denied or not found", "")
			return nil, &resp, nil
		}
		rule, _ := op.Variable(use.path)
		if rule.Subject == nil {
			resp := x.refuse(RefusalDeniedOrNotFound, "a named subject is denied or not found", "")
			return nil, &resp, nil
		}
		if _, err := rule.Subject.ToOps(strings.TrimSpace(use.id)); err != nil {
			resp := x.refuse(RefusalDeniedOrNotFound, "a named subject is denied or not found", "")
			return nil, &resp, nil
		}
	}
	if err := convertSubjectValues(op, tree); err != nil {
		resp := x.refuse(RefusalDeniedOrNotFound, "a named subject is denied or not found", "")
		return nil, &resp, nil
	}

	effective := &EffectiveScope{RepoIDs: []string{}}
	if !restricted {
		for _, use := range subjects {
			if use.kind == SubjectKindRepository {
				effective.RepoIDs = append(effective.RepoIDs, strings.TrimSpace(use.id))
			}
		}
		effective.RepoIDs = sortedUnique(effective.RepoIDs)
		x.read.ScopeClass = ScopeOrgWide
		if len(effective.RepoIDs) > 0 {
			x.read.ScopeClass = ScopeClient
		}
		return effective, nil, nil
	}

	// 7d: grant ∩ requested, as bare ops ids, at the forced path.
	forcedRule, ok := op.Variable(scope.ForcedVariablePath)
	if !ok || forcedRule.Subject == nil {
		resp := x.refuse(RefusalPolicyStale, "the forced scope path is not a subject variable", "")
		return nil, &resp, nil
	}
	var candidates []string
	if len(fromGrant) > 0 {
		for _, ref := range fromGrant {
			candidates = append(candidates, ref.CanonicalID)
		}
	} else {
		for _, use := range subjects {
			if use.path == scope.ForcedVariablePath {
				candidates = append(candidates, strings.TrimSpace(use.id))
			}
		}
	}
	bareIDs := []string{}
	acrIDs := []string{}
	for _, id := range sortedUnique(candidates) {
		if !admitted[forcedRule.Subject.Kind+"\x00"+id] {
			continue
		}
		bare, err := forcedRule.Subject.ToOps(id)
		if err != nil {
			continue
		}
		bareIDs = append(bareIDs, strings.ToLower(bare))
		acrIDs = append(acrIDs, forcedRule.Subject.AcrPrefix+strings.ToLower(bare))
	}
	bareIDs, acrIDs = sortedUnique(bareIDs), sortedUnique(acrIDs)
	if len(bareIDs) == 0 {
		// Never an empty filter: hotspots and compoundingRisk read an empty
		// list as ALL repositories.
		resp := x.refuse(RefusalNoGrantedScope, "the requested repositories and the caller's grant do not intersect", "")
		return nil, &resp, nil
	}
	list := make([]any, len(bareIDs))
	for i, id := range bareIDs {
		list[i] = id
	}
	if err := setPath(tree, scope.ForcedVariablePath, list); err != nil {
		resp := x.refuse(RefusalPolicyStale, "the forced scope path cannot be set", "")
		return nil, &resp, nil
	}
	effective.RepoIDs = acrIDs
	effective.ForcedByGrant = true
	x.read.ScopeClass = ScopeForcedGrant
	x.read.ForcedByGrant = true
	return effective, nil, nil
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// convertSubjectValues replaces every acr subject id in the allowlisted tree
// with its ops form.
func convertSubjectValues(op *OperationPolicy, tree map[string]any) error {
	for _, rule := range op.Variables {
		if rule.Subject == nil || strings.Contains(rule.Path, "[*]") {
			continue
		}
		f := lookupOne(tree, rule.Path)
		if isNullish(f) {
			continue
		}
		conv := *rule.Subject
		switch v := f.value.(type) {
		case string:
			bare, err := conv.ToOps(strings.TrimSpace(v))
			if err != nil {
				return err
			}
			if err := setPath(tree, rule.Path, bare); err != nil {
				return err
			}
		case []any:
			out := make([]any, len(v))
			for i, item := range v {
				s, ok := item.(string)
				if !ok {
					return errors.New("subject id is not a string")
				}
				bare, err := conv.ToOps(strings.TrimSpace(s))
				if err != nil {
					return err
				}
				out[i] = bare
			}
			if err := setPath(tree, rule.Path, out); err != nil {
				return err
			}
		default:
			return errors.New("subject value has an unexpected shape")
		}
	}
	return nil
}

// mapCallError is the D.7 mapping of an internal-call failure.
// OperationNotFoundLog is the line for an upstream 404 on run_operation: the
// operation and the upstream's closed reason (root_field_not_enabled = the
// routing rows do not enable the root yet; unknown = no typed reason).
const OperationNotFoundLog = "context fabric run_operation upstream answered not found"

func (x *run) logNotFound(err error) {
	reason := ListenerNotFoundUnknown
	var qe *QueryError
	if errors.As(err, &qe) && qe.ListenerReason != "" {
		reason = qe.ListenerReason
	}
	x.r.logger.Warn(OperationNotFoundLog,
		"org_id", contextfabric.SanitizeLogAttr(x.principal.OrgID),
		"operation", contextfabric.SanitizeLogAttr(x.read.Operation),
		"listener_reason", contextfabric.SanitizeLogAttr(reason),
		"retryable", false,
	)
}

func (x *run) mapCallError(err error, maxBytes int) OperationResponse {
	switch QueryErrorClassOf(err) {
	case QueryErrorNotFound:
		x.logNotFound(err)
		return x.upstream(CallOperationUnavailable, UpstreamNotFound)
	case QueryErrorTimeout:
		return x.upstream(CallUpstreamTimeout, UpstreamTimeout)
	case QueryErrorCanceled:
		return x.upstream(CallUpstreamError, UpstreamCanceled)
	case QueryErrorRequestInvalid:
		return x.refuse(RefusalInvalidRequest, "the request does not fit the query service limits", "")
	case QueryErrorResponseTooLarge:
		resp := x.refuse(RefusalResponseBudget, "the upstream answer exceeds the read limit; narrow the window, the scope or the limit variable", "")
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

// RefusalReasonUpstreamRejectedRequest is the fixed reason of a refusal
// that the upstream's GraphQL validation or parse step caused.
const RefusalReasonUpstreamRejectedRequest = "the query service rejected the request variables or document; see errors for the variable"

// httpStatusError carries the upstream HTTP status and the closed GraphQL
// code into errors[] and the read log. A GraphQL validation or parse
// rejection is the caller's request: it is a refusal, not an upstream error.
func (x *run) httpStatusError(err error) OperationResponse {
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
	x.resp.Call = CallUpstreamError
	x.resp.Errors = []OperationError{entry}
	x.resp.Data = nil
	x.read.Decision = string(CallUpstreamError)
	x.read.ErrorClass = UpstreamHTTPStatus
	return x.resp
}

// upstreamHTTPEntry builds the errors[] entry of an upstream non-2xx answer
// and says whether it is a GraphQL validation or parse rejection (HTTP 422)
// of the caller's own request.
func upstreamHTTPEntry(err error) (entry OperationError, callerFault, ok bool) {
	var qe *QueryError
	if !errors.As(err, &qe) {
		return OperationError{}, false, false
	}
	entry = OperationError{Class: UpstreamHTTPStatus, GraphQLCode: qe.GraphQLCode, Variable: qe.Variable}
	if qe.StatusCode >= 100 && qe.StatusCode <= 599 {
		entry.Status = qe.StatusCode
	}
	return entry, qe.GraphQLCode.callerFault() && entry.Status == http.StatusUnprocessableEntity, true
}

// parseGraphQLAnswer reads {"data": ..., "errors": [...]}. Any GraphQL error
// fails the call: a partial answer with errors is not served. The error
// text is never read into the response.
func parseGraphQLAnswer(body []byte) (json.RawMessage, UpstreamErrorClass, bool) {
	var answer struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, UpstreamDecode, false
	}
	if len(answer.Errors) > 0 {
		return nil, UpstreamGraphQLErrors, false
	}
	data := bytes.TrimSpace(answer.Data)
	if len(data) == 0 || data[0] != '{' {
		return nil, UpstreamDecode, false
	}
	return data, "", true
}

// orgLimiter bounds in-flight calls per (organization, operation).
type orgLimiter struct {
	mu    sync.Mutex
	slots map[string]chan struct{}
}

func (l *orgLimiter) acquire(ctx context.Context, key string, capacity int) (func(), bool) {
	l.mu.Lock()
	slot, ok := l.slots[key]
	if !ok {
		slot = make(chan struct{}, capacity)
		l.slots[key] = slot
	}
	l.mu.Unlock()
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, true
	case <-ctx.Done():
		return func() {}, false
	}
}

// ---------------------------------------------------------------- telemetry

// OperationReadLogArgs renders one Run for the trace: closed vocabularies,
// counts and digests. Never a subject id, a row value, a variable value or
// a credential. The caller appends request_id.
func OperationReadLogArgs(principal storage.Principal, read OperationRead) []any {
	args := []any{
		"org_id", contextfabric.SanitizeLogAttr(principal.OrgID),
		"operation", contextfabric.SanitizeLogAttr(read.Operation),
		"caller_class", contextfabric.SanitizeLogAttr(string(read.CallerClass)),
		"scope_class", contextfabric.SanitizeLogAttr(string(read.ScopeClass)),
		"decision", contextfabric.SanitizeLogAttr(read.Decision),
		"forced_by_grant", read.ForcedByGrant,
		"variables_rejected", read.VariablesRejected,
		"rows_checked", read.RowsChecked,
		"rows_foreign", read.RowsForeign,
		"paths_removed", read.PathsRemoved,
		"completeness", contextfabric.SanitizeLogAttr(string(read.Completeness)),
		"bytes", read.Bytes,
		"latency_ms", read.Latency.Milliseconds(),
		"schema_digest", contextfabric.SanitizeLogAttr(read.SchemaDigest),
	}
	if read.DocumentDigest != "" {
		args = append(args, "document_digest", contextfabric.SanitizeLogAttr(read.DocumentDigest))
	}
	if read.QueryPath != "" {
		args = append(args, "query_path", contextfabric.SanitizeLogAttr(read.QueryPath))
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
	if read.UpstreamStatus != 0 {
		args = append(args, "upstream_status", read.UpstreamStatus)
	}
	if read.GraphQLCode != "" {
		args = append(args, "graphql_code", contextfabric.SanitizeLogAttr(string(read.GraphQLCode)))
	}
	if read.Variable != "" {
		args = append(args, "variable", contextfabric.SanitizeLogAttr(read.Variable))
	}
	return args
}

func requestIDOf(ctx context.Context) (string, bool) {
	requestID, ok := observability.RequestIDFromContext(ctx)
	return string(requestID), ok
}

// SlogOperationRecorder is the production OperationRecorder.
type SlogOperationRecorder struct {
	logger *slog.Logger
}

// NewSlogOperationRecorder returns a recorder over logger (nil = default).
func NewSlogOperationRecorder(logger *slog.Logger) *SlogOperationRecorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogOperationRecorder{logger: logger}
}

// RecordOperationRead writes the "context fabric operation read" line.
func (s *SlogOperationRecorder) RecordOperationRead(ctx context.Context, principal storage.Principal, read OperationRead) {
	if s == nil || s.logger == nil {
		return
	}
	args := OperationReadLogArgs(principal, read)
	if requestID, ok := requestIDOf(ctx); ok {
		args = append(args, "request_id", contextfabric.SanitizeLogAttr(requestID))
	}
	s.logger.InfoContext(ctx, OperationReadLogMessage, args...)
}
