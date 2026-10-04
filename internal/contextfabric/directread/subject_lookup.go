package directread

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// find_subjects (CHAOS-7036 C.4, E.2, G). Four modes:
//
//   - list: subjects of one kind, ordered by canonical id, keyset cursor.
//   - name: subjects whose label, alias or provider key equals a query
//     exactly, for the requested kinds.
//   - owned_by (CHAOS-7126): the repositories and projects a team owns, each
//     read through the OWNED_BY_TEAM edge and the edge gate
//     (subject_lookup_modes.go).
//   - handle (CHAOS-7126): a PR number, work-item key or CI run id, bound by
//     the engine's handle grammar and looked up by the engine's census
//     (subject_lookup_modes.go).
//
// The vector arm is off (K6):
// no read here calls an embedding model or an interpreter, and SubjectGraph
// has no method that could.
//
// Every returned node passed SubjectGate.Authorize for the calling principal
// in this same call. The gate is the S0 decision (shared predicate, the graph
// lookup for every principal, the ownership rule for a restricted team or
// project); this file adds no second rule. A refused node is not returned and
// is not counted: total_known counts admitted nodes only, and a refused
// (denied, absent, ownership unproven) node gives no different answer from a
// node that does not exist.

const (
	// DefaultFindLimit is the page size when the caller gives none.
	DefaultFindLimit = 25
	// MaxFindLimit is the largest page.
	MaxFindLimit = MaxLookupPageSize
	// MaxFindKinds bounds the kinds one name lookup searches (design G).
	MaxFindKinds = 8
	// MaxFindQueryRunes bounds a name query.
	MaxFindQueryRunes = 256
	// MaxFindScanNodes bounds how many nodes of one kind a list read
	// examines to count the admitted population. Past it the response is
	// partial and population.truncated is true, and total_known is the
	// admitted count of the nodes examined (a lower bound).
	MaxFindScanNodes = 2000
	// MaxFindNameScanMatches bounds how many name matches one kind's name
	// lookup examines (gating each page) before it reports the cut.
	MaxFindNameScanMatches = 10000
	maxCursorBytes         = 512

	// FindSubjectsTool is the tool name the telemetry line carries.
	FindSubjectsTool = "find_subjects"
	// DirectReadLogMessage is the one Info line each direct read writes.
	DirectReadLogMessage = "context fabric direct read"

	// ConsistencyBestEffort: graph nodes change in place, so pages can show a
	// change; the client de-duplicates by key.
	ConsistencyBestEffort = "best_effort"
)

// FindStatus is the terminal state of one find_subjects call that reached the
// graph. Invalid input and an unavailable graph are errors, not statuses.
type FindStatus string

const (
	FindComplete FindStatus = "complete"
	// FindPartial: more admitted subjects exist beyond this page, or the read
	// hit a bound. page.complete and population.truncated say which.
	FindPartial FindStatus = "partial"
	// FindEmpty: no admitted subject. searched_kinds names what was searched.
	FindEmpty FindStatus = "empty"
	// FindAmbiguous: a name matched more than one admitted subject.
	FindAmbiguous FindStatus = "ambiguous"
)

// FindStatusVocabulary is the closed set of find statuses.
func FindStatusVocabulary() [4]FindStatus {
	return [4]FindStatus{FindComplete, FindPartial, FindEmpty, FindAmbiguous}
}

// Match classes. graphrank's exact, alias and provider_key (lexical is not
// produced in S1a).
const (
	MatchExact       = string(contractsv1.ContextFabricMatchExact)
	MatchAlias       = string(contractsv1.ContextFabricMatchAlias)
	MatchProviderKey = string(contractsv1.ContextFabricMatchProviderKey)
)

// ErrFindInvalidRequest: the request is malformed (unknown kind, bad cursor,
// no mode). The tool answers invalid_request.
var ErrFindInvalidRequest = errors.New("find_subjects: invalid_request")

// ErrFindUnavailable: the gate or the graph failed, or no graph is wired. The
// tool answers unavailable and serves nothing.
var ErrFindUnavailable = errors.New("find_subjects: unavailable")

// DefaultLookupNameKinds are the kinds a name lookup searches when the caller
// names none (falkorgraph.DefaultExactNameKinds is the same set).
func DefaultLookupNameKinds() []string { return []string{"repository", "project", "team"} }

// FindRequest is one find_subjects call. Kind alone is list mode. Query is
// name mode, over Kinds (and Kind, when both are set).
type FindRequest struct {
	Kind   string
	Query  string
	Kinds  []string
	Limit  int
	Cursor string
	// OwnedBy is owned_by mode: a team canonical id.
	OwnedBy string
	// Handle is handle mode: a handle such as "PR 532" or "CHAOS-123".
	Handle string
	// Anchor is the optional handle-mode anchor (CHAOS-7158): a repository or
	// project that narrows the handle to one subject.
	Anchor *contractsv1.MCPFindSubjectsAnchor
}

// FoundSubject is one admitted subject.
type FoundSubject struct {
	Kind        string `json:"kind"`
	CanonicalID string `json:"canonical_id"`
	Label       string `json:"label"`
	Match       string `json:"match"`
}

// FindPopulation counts ADMITTED nodes only.
type FindPopulation struct {
	Kind       string `json:"kind,omitempty"`
	Returned   int    `json:"returned"`
	TotalKnown int    `json:"total_known"`
	Truncated  bool   `json:"truncated"`
}

// FindPage describes this page. NextCursor is opaque and carries no
// permission: every page runs the gate again.
type FindPage struct {
	Returned   int    `json:"returned"`
	Complete   bool   `json:"complete"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// FindResponse is the find_subjects answer.
type FindResponse struct {
	Status        FindStatus     `json:"status"`
	Subjects      []FoundSubject `json:"subjects"`
	Population    FindPopulation `json:"population"`
	Page          FindPage       `json:"page"`
	SearchedKinds []string       `json:"searched_kinds,omitempty"`
	Consistency   string         `json:"consistency"`
}

// FindTelemetry is what one call writes: closed vocabulary and counts only.
// Never an id, a label or a query.
type FindTelemetry struct {
	Tool         string
	Mode         string
	Kinds        []string
	SubjectKinds []string
	Count        int
	Status       string
	LatencyMS    int64
	ErrorClass   string
	// Anchor is the true server-side decision on a handle-mode anchor
	// (CHAOS-7158): "admitted" or "refused" (a refused, unreadable or missing
	// anchor). Empty without an anchor. It never reaches the caller.
	Anchor string
}

// FindRecorder receives one FindTelemetry per call.
type FindRecorder interface {
	RecordFindSubjects(ctx context.Context, principal storage.Principal, telemetry FindTelemetry)
}

// SubjectLookup is the find_subjects read.
type SubjectLookup struct {
	graph    SubjectGraph
	gate     *SubjectGate
	recorder FindRecorder
	now      func() time.Time
	// edges, census and nodes serve owned_by and handle (CHAOS-7126);
	// see WithOwnershipAndHandles.
	edges         EdgeGraph
	census        graphrank.CensusFunc
	nodes         SubjectNodeReader
	anchorSupport CensusAnchorSupport
}

// NewSubjectLookup builds the read. A nil graph or gate makes every call
// unavailable (fail closed); a nil recorder records nothing.
func NewSubjectLookup(graph SubjectGraph, gate *SubjectGate, recorder FindRecorder) *SubjectLookup {
	lookup := &SubjectLookup{gate: gate, recorder: recorder, now: time.Now}
	if !storage.IsNil(graph) {
		lookup.graph = graph
	}
	return lookup
}

// EncodeFindCursor is the opaque cursor: the last canonical id, base64url. It
// is a position, never permission.
func EncodeFindCursor(canonicalID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(canonicalID))
}

func decodeFindCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) == 0 || len(raw) > maxCursorBytes || !utf8.Valid(raw) {
		return "", fmt.Errorf("%w: cursor does not decode", ErrFindInvalidRequest)
	}
	return string(raw), nil
}

type findPlan struct {
	list   bool
	mode   string
	kind   string
	owner  string
	handle graphrank.BoundHandle
	query  string
	kinds  []string
	limit  int
	cursor string
	// hasKeyToken: the handle holds a work-item-key-shaped token of any
	// prefix (CHAOS-7200).
	hasKeyToken bool
	// anchor is the validated handle-mode anchor (CHAOS-7158), or nil.
	anchor *contextfabric.SubjectRef
	// anchorDecision receives the gate decision on the anchor for telemetry
	// (a pointer, so the value copies of the plan share it).
	anchorDecision *string
}

func planFind(request FindRequest) (findPlan, error) {
	plan := findPlan{query: strings.TrimSpace(request.Query), limit: request.Limit}
	switch {
	case plan.limit == 0:
		plan.limit = DefaultFindLimit
	case plan.limit < 0:
		return plan, fmt.Errorf("%w: limit must not be negative", ErrFindInvalidRequest)
	case plan.limit > MaxFindLimit:
		plan.limit = MaxFindLimit
	}
	cursor, err := decodeFindCursor(request.Cursor)
	if err != nil {
		return plan, err
	}
	plan.cursor = cursor

	seen := map[string]struct{}{}
	add := func(kind string) error {
		kind = strings.TrimSpace(kind)
		if !contractsv1.ValidContextFabricSubjectKind(contractsv1.ContextFabricSubjectKind(kind)) {
			return fmt.Errorf("%w: kind is not a subject kind", ErrFindInvalidRequest)
		}
		if _, dup := seen[kind]; !dup {
			seen[kind] = struct{}{}
			plan.kinds = append(plan.kinds, kind)
		}
		return nil
	}
	if strings.TrimSpace(request.Kind) != "" {
		if err := add(request.Kind); err != nil {
			return plan, err
		}
	}
	for _, kind := range request.Kinds {
		if err := add(kind); err != nil {
			return plan, err
		}
	}
	if len(plan.kinds) > MaxFindKinds {
		return plan, fmt.Errorf("%w: at most %d kinds", ErrFindInvalidRequest, MaxFindKinds)
	}
	owner, handle := strings.TrimSpace(request.OwnedBy), strings.TrimSpace(request.Handle)
	if request.Anchor != nil {
		// CHAOS-7158: the anchor is handle mode only, both fields, and a
		// repository or project. Shape only: existence is never checked here.
		id := strings.TrimSpace(request.Anchor.ID)
		rawRunes := utf8.RuneCountInString(request.Anchor.ID)
		kind := contractsv1.ContextFabricSubjectKind(request.Anchor.Kind)
		if handle == "" || id == "" || rawRunes > MaxFindQueryRunes ||
			(kind != contractsv1.ContextFabricSubjectRepository && kind != contractsv1.ContextFabricSubjectProject) {
			return plan, fmt.Errorf("%w: anchor is a repository or project id and only for handle mode", ErrFindInvalidRequest)
		}
		plan.anchor = &contextfabric.SubjectRef{Kind: kind, CanonicalID: id}
		plan.anchorDecision = new(string)
	}
	modes := 0
	for _, set := range []bool{plan.query != "", owner != "", handle != ""} {
		if set {
			modes++
		}
	}
	if modes > 1 {
		return plan, fmt.Errorf("%w: query, owned_by and handle are separate modes", ErrFindInvalidRequest)
	}
	switch {
	case owner != "":
		return planOwnedBy(plan, owner)
	case handle != "":
		return planHandle(plan, handle)
	case plan.query == "":
		if strings.TrimSpace(request.Kind) == "" || len(request.Kinds) > 0 {
			return plan, fmt.Errorf("%w: list mode needs exactly one kind; name mode needs a query", ErrFindInvalidRequest)
		}
		plan.list = true
		plan.mode = FindModeList
		plan.kind = plan.kinds[0]
	case utf8.RuneCountInString(plan.query) > MaxFindQueryRunes:
		return plan, fmt.Errorf("%w: query is too long", ErrFindInvalidRequest)
	case len(plan.kinds) == 0:
		plan.kinds = DefaultLookupNameKinds()
	}
	if !plan.list {
		plan.mode = FindModeName
	}
	return plan, nil
}

// Find runs one find_subjects call for principal.
func (l *SubjectLookup) Find(ctx context.Context, principal storage.Principal, request FindRequest) (response FindResponse, err error) {
	started := time.Now()
	if l != nil && l.now != nil {
		started = l.now()
	}
	plan, planErr := planFind(request)
	defer func() {
		if l == nil || l.recorder == nil {
			return
		}
		telemetry := FindTelemetry{Tool: FindSubjectsTool, Mode: plan.mode, Kinds: sortedKinds(plan.kinds), Count: len(response.Subjects), Status: string(response.Status)}
		if telemetry.Mode == "" {
			telemetry.Mode = FindModeName
		}
		if latency := l.now().Sub(started); latency > 0 {
			telemetry.LatencyMS = latency.Milliseconds()
		}
		kinds := make([]string, 0, len(response.Subjects))
		for _, subject := range response.Subjects {
			kinds = append(kinds, subject.Kind)
		}
		telemetry.SubjectKinds = sortedKinds(kinds)
		if plan.anchorDecision != nil {
			telemetry.Anchor = *plan.anchorDecision
		}
		switch {
		case errors.Is(err, ErrFindScopeRequired):
			// The typed refusal keeps its reason on the Info line
			// (CHAOS-7160 r1).
			telemetry.Status, telemetry.ErrorClass = "invalid_request", "scope_required"
		case errors.Is(err, ErrFindInvalidRequest):
			telemetry.Status, telemetry.ErrorClass = "invalid_request", "invalid_request"
		case err != nil:
			telemetry.Status, telemetry.ErrorClass = "unavailable", findErrorClass(err)
		}
		l.recorder.RecordFindSubjects(ctx, principal, telemetry)
	}()
	// CHAOS-7200: for a repository-restricted caller the authorization
	// refusal precedes every existence-derived step. A handle holding a
	// work-item-key-shaped token is refused as scope_required whether or not
	// its prefix is registered, before binding and before any upstream call,
	// with the same telemetry kind, so a known and an unknown prefix cannot
	// be told apart (reason, message, telemetry, calls). Shape rejections
	// (kind given, mixed modes, too long) stay first, for everyone.
	if plan.mode == FindModeHandle && plan.hasKeyToken && (planErr == nil || errors.Is(planErr, errFindHandleBoundCount)) && ClassifyPrincipal(principal) == ClassRestricted {
		plan.kinds = []string{string(contractsv1.ContextFabricSubjectWorkItem)}
		return FindResponse{}, fmt.Errorf("%w: %w: work_item handles cannot be looked up inside a repository grant", ErrFindInvalidRequest, ErrFindScopeRequired)
	}
	if planErr != nil {
		return FindResponse{}, planErr
	}
	if l == nil || l.graph == nil || l.gate == nil {
		return FindResponse{}, fmt.Errorf("%w: no graph or gate", ErrFindUnavailable)
	}

	if plan.anchor != nil && l.anchorSupport != nil && !l.anchorSupport(plan.handle.Kind, plan.anchor.Kind) {
		// A pair the census cannot scope is a static refusal, the same for
		// every caller (CHAOS-7158).
		return FindResponse{}, fmt.Errorf("%w: a %s handle cannot be anchored on a %s", ErrFindInvalidRequest, plan.handle.Kind, plan.anchor.Kind)
	}

	binding, bindErr := l.graph.ResolveInvestigationBinding(ctx, principal)
	if bindErr != nil {
		return l.emptyOrUnavailable(plan, bindErr)
	}

	var admitted []FoundSubject
	var truncated bool
	switch plan.mode {
	case FindModeList:
		admitted, truncated, err = l.scanList(ctx, principal, binding, plan)
	case FindModeOwnedBy:
		admitted, truncated, err = l.scanOwnedBy(ctx, principal, binding, plan)
	case FindModeHandle:
		admitted, truncated, err = l.scanHandle(ctx, principal, binding, plan)
	default:
		admitted, truncated, err = l.scanName(ctx, principal, binding, plan)
	}
	if errors.Is(err, ErrFindScopeRequired) {
		// A typed refusal, not an outage: the request is refused for this
		// caller (invalid_request, reason scope_required).
		return FindResponse{}, fmt.Errorf("%w: %w", ErrFindInvalidRequest, err)
	}
	if err != nil {
		return l.emptyOrUnavailable(plan, err)
	}
	return buildFindResponse(plan, admitted, truncated), nil
}

func (l *SubjectLookup) emptyOrUnavailable(plan findPlan, err error) (FindResponse, error) {
	if errors.Is(err, contextfabric.ErrGraphNotProjected) {
		return buildFindResponse(plan, nil, false), nil
	}
	return FindResponse{}, fmt.Errorf("%w: %w", ErrFindUnavailable, err)
}

// scanList reads the kind page by page, gating each page, until the kind ends
// or MaxFindScanNodes nodes were examined. The admitted list is complete up to
// that bound, so total_known counts admitted nodes only.
func (l *SubjectLookup) scanList(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, plan findPlan) ([]FoundSubject, bool, error) {
	var admitted []FoundSubject
	after, scanned := "", 0
	for {
		page, err := l.graph.ListSubjectsByKind(ctx, principal, binding, plan.kind, after, MaxLookupPageSize)
		if err != nil {
			return nil, false, err
		}
		batch, err := l.gateNodes(ctx, principal, page.Nodes)
		if err != nil {
			return nil, false, err
		}
		admitted = append(admitted, batch...)
		scanned += len(page.Nodes)
		if !page.More {
			return admitted, false, nil
		}
		last := ""
		if len(page.Nodes) > 0 {
			last = page.Nodes[len(page.Nodes)-1].CanonicalID
		}
		// A page that cannot advance, or the scan bound, ends the read
		// loudly as truncated: never a silent claim of a complete kind.
		if last == "" || last <= after || scanned >= MaxFindScanNodes {
			return admitted, true, nil
		}
		after = last
	}
}

// scanName reads the matches of each requested kind page by page, in
// canonical-id order, and gates each page. The store evaluates the name
// equality over every node of the kind (falkorgraph FindSubjectsByExactName),
// so a match is found wherever it sorts. The bounds are on matches: at most
// MaxFindNameScanMatches matches examined per kind and MaxFindScanNodes
// admitted subjects per call; a cut at either bound with matches left is
// reported as truncated, never as a complete answer.
func (l *SubjectLookup) scanName(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, plan findPlan) ([]FoundSubject, bool, error) {
	var admitted []FoundSubject
	truncated := false
	for _, kind := range plan.kinds {
		after, examined := "", 0
		for {
			page, err := l.graph.FindSubjectsByExactName(ctx, principal, binding, plan.query, kind, after, MaxLookupPageSize)
			if err != nil {
				return nil, false, err
			}
			batch, err := l.gateNodes(ctx, principal, page.Nodes)
			if err != nil {
				return nil, false, err
			}
			admitted = append(admitted, batch...)
			examined += len(page.Nodes)
			truncated = truncated || page.Truncated
			if !page.More {
				break
			}
			// A page that cannot advance, or a bound, ends the read loudly
			// as truncated: never a silent claim of a complete answer.
			if page.After == "" || page.After <= after || examined >= MaxFindNameScanMatches || len(admitted) >= MaxFindScanNodes {
				truncated = true
				break
			}
			after = page.After
		}
	}
	return admitted, truncated, nil
}

// gateNodes runs candidates through the S0 subject gate and keeps only the
// admitted ones, in candidate order, with the label and match the graph read
// gave (the gate drops labels by design). A gate that is unavailable fails the
// call: nothing is served.
func (l *SubjectLookup) gateNodes(ctx context.Context, principal storage.Principal, nodes []LookupNode) ([]FoundSubject, error) {
	var out []FoundSubject
	for start := 0; start < len(nodes); start += MaxLookupPageSize {
		end := min(start+MaxLookupPageSize, len(nodes))
		batch := nodes[start:end]
		requested := make([]contextfabric.SubjectRef, 0, len(batch))
		for _, node := range batch {
			requested = append(requested, contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(node.Kind), CanonicalID: node.CanonicalID})
		}
		authorized, decision := l.gate.Authorize(ctx, principal, requested)
		if decision.Decision == DecisionUnavailable {
			return nil, fmt.Errorf("subject gate unavailable: %w", gateError(decision))
		}
		if authorized.Len() == 0 {
			continue
		}
		if !authorized.IssuedTo(principal) {
			return nil, fmt.Errorf("%w: gate issued to another principal", ErrUngatedRead)
		}
		allowed := map[string]struct{}{}
		for _, subject := range authorized.Subjects() {
			allowed[subjectKey(subject)] = struct{}{}
		}
		for _, node := range batch {
			key := subjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(node.Kind), CanonicalID: strings.TrimSpace(node.CanonicalID)})
			if _, ok := allowed[key]; !ok {
				continue
			}
			delete(allowed, key) // one entry per distinct subject
			match := node.Match
			if match == "" {
				match = MatchExact
			}
			out = append(out, FoundSubject{Kind: node.Kind, CanonicalID: strings.TrimSpace(node.CanonicalID), Label: node.Label, Match: match})
		}
	}
	return out, nil
}

func gateError(decision Authorization) error {
	if decision.Err != nil {
		return decision.Err
	}
	return contextfabric.ErrUnavailable
}

func buildFindResponse(plan findPlan, admitted []FoundSubject, truncated bool) FindResponse {
	sort.SliceStable(admitted, func(i, j int) bool {
		if admitted[i].CanonicalID != admitted[j].CanonicalID {
			return admitted[i].CanonicalID < admitted[j].CanonicalID
		}
		return admitted[i].Kind < admitted[j].Kind
	})
	total := len(admitted)
	after := admitted
	if plan.cursor != "" {
		after = nil
		for _, subject := range admitted {
			if subject.CanonicalID > plan.cursor {
				after = append(after, subject)
			}
		}
	}
	pageSubjects := after
	if len(pageSubjects) > plan.limit {
		pageSubjects = pageSubjects[:plan.limit]
	}
	remaining := len(after) - len(pageSubjects)

	response := FindResponse{
		Subjects:    append([]FoundSubject{}, pageSubjects...),
		Consistency: ConsistencyBestEffort,
		Population:  FindPopulation{Returned: len(pageSubjects), TotalKnown: total, Truncated: truncated},
		Page:        FindPage{Returned: len(pageSubjects), Complete: remaining == 0},
	}
	if len(plan.kinds) == 1 {
		response.Population.Kind = plan.kinds[0]
	}
	if remaining > 0 {
		response.Page.NextCursor = EncodeFindCursor(pageSubjects[len(pageSubjects)-1].CanonicalID)
	}
	switch {
	case len(pageSubjects) == 0 && !truncated:
		response.Status = FindEmpty
		response.SearchedKinds = append([]string{}, plan.kinds...)
	case len(pageSubjects) == 0:
		// A read bound was hit before any readable subject was found: that
		// is not "none exists" (CHAOS-7126: a handle census over its
		// budget names no satisfier).
		response.Status = FindPartial
		response.SearchedKinds = append([]string{}, plan.kinds...)
	case (plan.mode == FindModeName || plan.mode == FindModeHandle) && total > 1:
		response.Status = FindAmbiguous
	case remaining > 0 || truncated:
		response.Status = FindPartial
	default:
		response.Status = FindComplete
	}
	return response
}

func sortedKinds(kinds []string) []string {
	set := map[string]struct{}{}
	for _, kind := range kinds {
		if contractsv1.ValidContextFabricSubjectKind(contractsv1.ContextFabricSubjectKind(kind)) {
			set[kind] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for kind := range set {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

func findErrorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, contextfabric.ErrUnavailable):
		return "dependency_unavailable"
	default:
		return "graph_error"
	}
}

// FindLogArgs renders a call for the trace: the tool, closed-vocabulary
// kinds, counts, status and latency. Never an id, a label or the query.
func FindLogArgs(principal storage.Principal, telemetry FindTelemetry) []any {
	args := []any{
		"org_id", contextfabric.SanitizeLogAttr(principal.OrgID),
		"tool", contextfabric.SanitizeLogAttr(telemetry.Tool),
		"mode", contextfabric.SanitizeLogAttr(telemetry.Mode),
		"kinds", contextfabric.SanitizeLogStrings(append([]string{}, telemetry.Kinds...)),
		"subject_kinds", contextfabric.SanitizeLogStrings(append([]string{}, telemetry.SubjectKinds...)),
		"count", telemetry.Count,
		"status", contextfabric.SanitizeLogAttr(telemetry.Status),
		"latency_ms", telemetry.LatencyMS,
	}
	if telemetry.ErrorClass != "" {
		args = append(args, "error_class", contextfabric.SanitizeLogAttr(telemetry.ErrorClass))
	}
	if telemetry.Anchor != "" {
		args = append(args, "anchor", contextfabric.SanitizeLogAttr(telemetry.Anchor))
	}
	return args
}

// SlogFindRecorder is the production FindRecorder.
type SlogFindRecorder struct{ logger *slog.Logger }

// NewSlogFindRecorder returns a recorder over logger; nil uses the default
// logger.
func NewSlogFindRecorder(logger *slog.Logger) *SlogFindRecorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogFindRecorder{logger: logger}
}

// RecordFindSubjects writes the line.
func (r *SlogFindRecorder) RecordFindSubjects(ctx context.Context, principal storage.Principal, telemetry FindTelemetry) {
	if r == nil || r.logger == nil {
		return
	}
	args := FindLogArgs(principal, telemetry)
	if requestID, ok := observability.RequestIDFromContext(ctx); ok {
		args = append(args, "request_id", contextfabric.SanitizeLogAttr(string(requestID)))
	}
	r.logger.InfoContext(ctx, DirectReadLogMessage, args...)
}
