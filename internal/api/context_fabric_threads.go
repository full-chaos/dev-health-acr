package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The Ask Dev conversation routes (CHAOS-11948, contract
// docs/design/ask-dev-conversation-contract.md §3.2). Every route reads or
// deletes the caller's own threads only: the thread key includes the owner,
// so another principal's thread, an unknown one, a deleted or expired one and
// a malformed id are one answer.

// ContextFabricThreadsPath lists the caller's threads.
const ContextFabricThreadsPath = "/api/v1/context-fabric/threads"

// ContextFabricThreadPath reads or deletes one of them.
const ContextFabricThreadPath = "/api/v1/context-fabric/threads/{thread_id}"

// ContextFabricThreadLogMessage is every thread route's decision line:
// one per request.
const ContextFabricThreadLogMessage = "context fabric thread"

// ThreadRouteOutcome is the closed outcome of the thread line.
type ThreadRouteOutcome string

const (
	ThreadRouteListed           ThreadRouteOutcome = "listed"
	ThreadRouteRead             ThreadRouteOutcome = "read"
	ThreadRouteClosed           ThreadRouteOutcome = "closed"
	ThreadRouteNotOpen          ThreadRouteOutcome = "not_open"
	ThreadRouteNotFound         ThreadRouteOutcome = "not_found"
	ThreadRouteInvalidRequest   ThreadRouteOutcome = "invalid_request"
	ThreadRouteStoreUnavailable ThreadRouteOutcome = "store_unavailable"
	// ThreadRouteGateUnavailable: a done turn's serving decision could not
	// be taken, so the read fails whole (never a partial thread).
	ThreadRouteGateUnavailable ThreadRouteOutcome = "gate_unavailable"
	ThreadRouteInvalidRecord   ThreadRouteOutcome = "invalid_record"
	ThreadRouteOwnerUnknown    ThreadRouteOutcome = "owner_unknown"
)

// ThreadRouteOutcomeVocabulary is the closed set of outcomes.
func ThreadRouteOutcomeVocabulary() [10]ThreadRouteOutcome {
	return [10]ThreadRouteOutcome{
		ThreadRouteListed, ThreadRouteRead, ThreadRouteClosed, ThreadRouteNotOpen, ThreadRouteNotFound,
		ThreadRouteInvalidRequest, ThreadRouteStoreUnavailable, ThreadRouteGateUnavailable, ThreadRouteInvalidRecord, ThreadRouteOwnerUnknown,
	}
}

// threadRoute names the route on the line.
type threadRoute string

const (
	threadRouteList   threadRoute = "list"
	threadRouteGet    threadRoute = "get"
	threadRouteDelete threadRoute = "delete"
)

// threadReadCounts is the aggregated serving decision of one thread read:
// one line per read, never one StoredResultAuthorization line per turn
// (design §4: that event is one line per request).
type threadReadCounts struct {
	turns, done, served                         int
	withheldDenied, withheldNotFound            int
	withheldTuple, withheldFlood, earlierExists int
}

func (a *App) logThreadRoute(r *http.Request, principal storage.Principal, route threadRoute, outcome ThreadRouteOutcome, err error, extra ...any) {
	args := []any{
		"org_id", contextfabric.SanitizeLogAttr(principal.OrgID),
		"route", string(route),
		"outcome", string(outcome),
		"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())),
	}
	args = append(args, extra...)
	if err != nil {
		args = append(args, "error_class", submissionStoreErrorClass(err))
		a.logger.WarnContext(r.Context(), ContextFabricThreadLogMessage, args...)
		return
	}
	a.logger.InfoContext(r.Context(), ContextFabricThreadLogMessage, args...)
}

func (a *App) investigationThreads() contextfabric.ThreadStore {
	if a.runtime == nil || storage.IsNil(a.runtime.InvestigationThreads) {
		return nil
	}
	return a.runtime.InvestigationThreads
}

func (a *App) writeThreadNotFound(w http.ResponseWriter, r *http.Request, principal storage.Principal, route threadRoute) {
	a.logThreadRoute(r, principal, route, ThreadRouteNotFound, nil)
	writeError(w, r, http.StatusNotFound, "not_found", "Context Fabric thread was not found", false, nil)
}

func (a *App) writeThreadInvalid(w http.ResponseWriter, r *http.Request, principal storage.Principal, route threadRoute, field string) {
	a.logThreadRoute(r, principal, route, ThreadRouteInvalidRequest, nil, "field", field)
	writeError(w, r, http.StatusBadRequest, "invalid_request", "Context Fabric thread request is invalid", false, map[string]any{"field": field})
}

func (a *App) writeThreadStoreUnavailable(w http.ResponseWriter, r *http.Request, principal storage.Principal, route threadRoute, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		return
	}
	a.logThreadRoute(r, principal, route, ThreadRouteStoreUnavailable, err)
	writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "Context Fabric threads are temporarily unavailable", true, nil)
}

// threadHandler is the shared front of the three routes: the store, the
// principal and its owner. ok is false when the response is written.
func (a *App) threadHandler(w http.ResponseWriter, r *http.Request, route threadRoute) (contextfabric.ThreadStore, storage.Principal, contextfabric.SubmissionOwner, bool) {
	// After protectedRuntimeHandler, never before it: an unauthenticated
	// caller must not learn whether the store is configured.
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return nil, storage.Principal{}, contextfabric.SubmissionOwner{}, false
	}
	threads := a.investigationThreads()
	if threads == nil {
		a.logThreadRoute(r, principal, route, ThreadRouteStoreUnavailable, errSubmissionStoreNotConfigured)
		a.handleRuntimeUnavailable(w, r)
		return nil, principal, contextfabric.SubmissionOwner{}, false
	}
	owner, err := contextfabric.SubmissionOwnerOf(principal)
	if err != nil {
		a.logThreadRoute(r, principal, route, ThreadRouteOwnerUnknown, err)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric thread request could not be served", false, nil)
		return nil, principal, contextfabric.SubmissionOwner{}, false
	}
	return threads, principal, owner, true
}

// ContextFabricThreadListHandler serves GET /api/v1/context-fabric/threads.
func (a *App) ContextFabricThreadListHandler() http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		threads, principal, owner, ok := a.threadHandler(w, r, threadRouteList)
		if !ok {
			return
		}
		limit, ok := threadListLimit(r)
		if !ok {
			a.writeThreadInvalid(w, r, principal, threadRouteList, "limit")
			return
		}
		var after *contextfabric.ThreadListCursor
		if raw := r.URL.Query().Get("cursor"); raw != "" || r.URL.Query().Has("cursor") {
			cursor, valid := decodeThreadCursor(raw, principal, "")
			if !valid {
				a.writeThreadInvalid(w, r, principal, threadRouteList, "cursor")
				return
			}
			after = &contextfabric.ThreadListCursor{LastTurnAt: cursor.at, ThreadID: cursor.id}
		}
		records, more, err := threads.ListThreads(r.Context(), principal.OrgID, owner, after, limit, a.now())
		if err != nil {
			a.writeThreadStoreUnavailable(w, r, principal, threadRouteList, err)
			return
		}
		list := contractsv1.ContextFabricThreadList{SchemaVersion: contractsv1.ContextFabricThreadListSchema, Threads: make([]contractsv1.ContextFabricThreadSummary, 0, len(records))}
		for _, record := range records {
			list.Threads = append(list.Threads, threadSummary(record))
		}
		if more && len(records) > 0 {
			last := records[len(records)-1]
			list.NextCursor = encodeThreadCursor(principal, "", last.LastTurnAt, last.Key.ThreadID)
		}
		if err := list.Validate(); err != nil {
			a.logThreadRoute(r, principal, threadRouteList, ThreadRouteInvalidRecord, err)
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric threads could not be served", false, nil)
			return
		}
		a.logThreadRoute(r, principal, threadRouteList, ThreadRouteListed, nil, "threads", len(list.Threads), "more", more)
		a.recordReadAudit(r.Context(), principal, "context_fabric_thread_list_read", "context_fabric_thread", "", "success", map[string]any{"threads": len(list.Threads)})
		writeJSON(w, http.StatusOK, list)
	})
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextRead, true, true, handler)
}

// ContextFabricThreadHandler serves GET and DELETE
// /api/v1/context-fabric/threads/{thread_id}.
func (a *App) ContextFabricThreadHandler() http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := threadRouteGet
		if r.Method == http.MethodDelete {
			route = threadRouteDelete
		}
		threads, principal, owner, ok := a.threadHandler(w, r, route)
		if !ok {
			return
		}
		threadID := r.PathValue("thread_id")
		if !contextfabric.ValidSubmissionID(threadID) {
			a.writeThreadNotFound(w, r, principal, route)
			return
		}
		key := contextfabric.ThreadKey{OrgID: principal.OrgID, Owner: owner, ThreadID: threadID}
		if route == threadRouteDelete {
			a.deleteThread(w, r, threads, principal, key)
			return
		}
		a.readThread(w, r, threads, principal, key)
	})
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextRead, true, true, handler)
}

// deleteThread closes the thread: 204 for any well-formed id, whether or not
// the caller had such an open thread (idempotent, existence-neutral).
func (a *App) deleteThread(w http.ResponseWriter, r *http.Request, threads contextfabric.ThreadStore, principal storage.Principal, key contextfabric.ThreadKey) {
	closed, err := threads.CloseThread(r.Context(), key, a.now())
	if err != nil {
		a.writeThreadStoreUnavailable(w, r, principal, threadRouteDelete, err)
		return
	}
	outcome := ThreadRouteNotOpen
	if closed {
		outcome = ThreadRouteClosed
		a.recordReadAudit(r.Context(), principal, "context_fabric_thread_deleted", "context_fabric_thread", "", "success", nil)
	}
	a.logThreadRoute(r, principal, threadRouteDelete, outcome, nil)
	w.WriteHeader(http.StatusNoContent)
}

// readThread serves one page of the caller's open thread. Each done turn
// names its result id only when the read by id would serve the result to the
// caller now (decideStoredResultServing, surface thread); otherwise the turn
// carries result_withheld. A decision that cannot be taken fails the read.
func (a *App) readThread(w http.ResponseWriter, r *http.Request, threads contextfabric.ThreadStore, principal storage.Principal, key contextfabric.ThreadKey) {
	var before *contextfabric.ThreadTurnCursor
	if raw := r.URL.Query().Get("before"); raw != "" || r.URL.Query().Has("before") {
		cursor, valid := decodeThreadCursor(raw, principal, key.ThreadID)
		if !valid {
			a.writeThreadInvalid(w, r, principal, threadRouteGet, "before")
			return
		}
		before = &contextfabric.ThreadTurnCursor{AdmittedAt: cursor.at, SubmissionID: cursor.id}
	}
	page, err := threads.GetThread(r.Context(), key, before, contractsv1.ContextFabricThreadMaxServedTurns, a.now())
	switch {
	case errors.Is(err, contextfabric.ErrThreadNotFound):
		a.writeThreadNotFound(w, r, principal, threadRouteGet)
		return
	case err != nil:
		a.writeThreadStoreUnavailable(w, r, principal, threadRouteGet, err)
		return
	}
	results := a.investigationResults()
	gate := a.storedResultGate()
	counts := threadReadCounts{turns: len(page.Turns)}
	thread := contractsv1.ContextFabricThread{
		SchemaVersion: contractsv1.ContextFabricThreadSchema, ThreadID: page.Thread.Key.ThreadID, RetentionDays: page.Thread.RetentionDays,
		TurnCount: page.Thread.TurnCount, CreatedAt: page.Thread.CreatedAt.UTC(), LastTurnAt: page.Thread.LastTurnAt.UTC(), ExpiresAt: page.Thread.ExpiresAt.UTC(),
		Turns: make([]contractsv1.ContextFabricThreadTurn, 0, len(page.Turns)),
	}
	for _, turn := range page.Turns {
		served := contractsv1.ContextFabricThreadTurn{
			SubmissionID: turn.SubmissionID, State: contractsv1.ContextFabricSubmissionState(turn.State), Question: turn.Question,
			AdmittedAt: turn.AdmittedAt.UTC(), UpdatedAt: turn.UpdatedAt.UTC(),
		}
		if turn.State == contextfabric.SubmissionDone {
			counts.done++
			outcome := servingUnavailable
			if results != nil {
				outcome = decideStoredResultServing(r.Context(), principal, results, gate, turn.ResultID, contextfabric.StoredResultSurfaceThread).outcome
			}
			switch outcome {
			case servingServed:
				counts.served++
				served.ResultID = turn.ResultID
			case servingDenied:
				counts.withheldDenied++
				served.ResultWithheld = true
			case servingNotFound:
				counts.withheldNotFound++
				served.ResultWithheld = true
			case servingTupleWithheld:
				counts.withheldTuple++
				served.ResultWithheld = true
			case servingFloodWithheld:
				counts.withheldFlood++
				served.ResultWithheld = true
			default:
				if errors.Is(r.Context().Err(), context.Canceled) {
					return
				}
				a.logThreadRoute(r, principal, threadRouteGet, ThreadRouteGateUnavailable, nil, counts.args()...)
				writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Context Fabric is temporarily unavailable", true, nil)
				return
			}
		}
		thread.Turns = append(thread.Turns, served)
	}
	if page.EarlierTurns {
		counts.earlierExists = 1
		first := page.Turns[0]
		thread.EarlierTurnsCursor = encodeThreadCursor(principal, key.ThreadID, first.AdmittedAt, first.SubmissionID)
	}
	if err := thread.Validate(); err != nil {
		a.logThreadRoute(r, principal, threadRouteGet, ThreadRouteInvalidRecord, err, counts.args()...)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric thread could not be served", false, nil)
		return
	}
	a.logThreadRoute(r, principal, threadRouteGet, ThreadRouteRead, nil, counts.args()...)
	a.recordReadAudit(r.Context(), principal, "context_fabric_thread_read", "context_fabric_thread", "", "success", map[string]any{"turns": counts.turns})
	writeJSON(w, http.StatusOK, thread)
}

func (c threadReadCounts) args() []any {
	return []any{
		"turns", c.turns, "done_turns", c.done, "served_turns", c.served,
		"withheld_denied", c.withheldDenied, "withheld_not_found", c.withheldNotFound,
		"withheld_tuple", c.withheldTuple, "withheld_flood", c.withheldFlood, "earlier_turns", c.earlierExists == 1,
	}
}

func threadSummary(record contextfabric.ThreadRecord) contractsv1.ContextFabricThreadSummary {
	return contractsv1.ContextFabricThreadSummary{
		ThreadID: record.Key.ThreadID, RetentionDays: record.RetentionDays, TurnCount: record.TurnCount,
		CreatedAt: record.CreatedAt.UTC(), LastTurnAt: record.LastTurnAt.UTC(), ExpiresAt: record.ExpiresAt.UTC(),
	}
}

// threadListLimit reads the list's limit: absent is the default; one integer
// literal in 1..ContextFabricThreadListMaxLimit; anything else is invalid.
func threadListLimit(r *http.Request) (int, bool) {
	values, present := r.URL.Query()["limit"]
	if !present {
		return contractsv1.ContextFabricThreadListDefaultLimit, true
	}
	if len(values) != 1 {
		return 0, false
	}
	limit, err := strconv.Atoi(values[0])
	if err != nil || limit < 1 || limit > contractsv1.ContextFabricThreadListMaxLimit || strconv.Itoa(limit) != values[0] {
		return 0, false
	}
	return limit, true
}

// threadCursor is the opaque cursor of both pages: a position and the owner
// (and, for a turn cursor, the thread) it was issued to, so a cursor another
// principal or another thread issued is refused, not followed.
type threadCursor struct {
	Version int    `json:"v"`
	Owner   string `json:"o"`
	Thread  string `json:"h,omitempty"`
	At      int64  `json:"t"`
	ID      string `json:"i"`
	at      time.Time
	id      string
}

// threadCursorOwner is the cursor's owner tag: a digest of the organization
// and the owner, not a secret (a cursor only positions the caller's own
// rows); it makes a foreign cursor a refusal instead of a silent page.
func threadCursorOwner(principal storage.Principal) string {
	method, subject := principal.AuditActor()
	sum := sha256.Sum256([]byte(principal.OrgID + "\x00" + method + "\x00" + subject))
	return hex.EncodeToString(sum[:8])
}

func encodeThreadCursor(principal storage.Principal, threadID string, at time.Time, id string) string {
	body, _ := json.Marshal(threadCursor{Version: 1, Owner: threadCursorOwner(principal), Thread: threadID, At: at.UTC().UnixNano(), ID: id})
	return base64.RawURLEncoding.EncodeToString(body)
}

func decodeThreadCursor(raw string, principal storage.Principal, threadID string) (threadCursor, bool) {
	if !contractsv1.ValidContextFabricThreadCursor(raw) {
		return threadCursor{}, false
	}
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return threadCursor{}, false
	}
	var cursor threadCursor
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.Version != 1 || cursor.Owner != threadCursorOwner(principal) ||
		cursor.Thread != threadID || !contextfabric.ValidSubmissionID(cursor.ID) || cursor.At <= 0 {
		return threadCursor{}, false
	}
	cursor.at, cursor.id = time.Unix(0, cursor.At).UTC(), cursor.ID
	return cursor, true
}
