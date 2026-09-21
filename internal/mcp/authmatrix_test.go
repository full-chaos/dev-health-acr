package mcp_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The auth and isolation matrix for hosted MCP callers.
//
// One table of rows is executed by one runner against a target: an MCP
// endpoint URL plus one bearer per caller. The runner speaks only the wire
// (raw MCP 2026-07-28 requests over HTTP), so the identical matrix runs
//
//   - in CI, against acr-mcp in front of a real in-process acr-api
//     (TestAuthMatrixInProcess), where every row also carries in-process
//     evidence (the SDK handler never ran for a refused row, the stores were
//     or were not consulted, the request line and the stored-result decision
//     line each appear once); and
//   - against a deployed endpoint (TestAuthMatrixLive), configured from the
//     environment, where only the wire is observable.
//
// Callers:
//
//	A  org_1, context:read+evidence:read, grant [widget]      the owner of every fixture
//	B  org_2, same scopes,               grant [widget]       another organization naming the SAME repository
//	C  org_1, same scopes,               grant [other]        the owner's organization, narrower repository grant
//	D  A's grant, then revoked
//	E  A's grant, expired
//	F  no Authorization header
//	G  a malformed bearer
//	H  org_1, A's grant plus episode:write                     the only caller offered record_episode
//
// B and C are built so each isolates ONE guard. B's grant covers the
// repository, so only the organization scope of a store can refuse it. C is
// in the owner's organization, so only the repository grant can refuse it.
type matrixKind int

const (
	kindList     matrixKind = iota // tools/list answers the exact tool set
	kindOK                         // the call is served and carries its markers
	kindToolDeny                   // HTTP 200, a tool error, no data, same text as an unknown id
	kindHTTPDeny                   // refused before any MCP handler ran
	kindUnknown                    // the tool is not this caller's: never a success
	kindNoData                     // a tool error, or an answer that carries none of the owner's data
)

type matrixRow struct {
	id     string
	caller string
	tool   string // "" = tools/list
	args   map[string]any
	kind   matrixKind
	// guard names the guard whose absence must turn this row red; it is the
	// label the mutation battery's kill sites are recorded against.
	guard string
	// listed is the exact catalogue of a kindList row.
	listed []string
	// contains are markers an OK row's body must carry.
	contains []string
	// status and outcome are the wire refusal of a kindHTTPDeny row.
	status  int
	outcome string
	// text must appear in a kindToolDeny row's denial: the public error code
	// of the guard that refused it.
	text string
	// absent are markers of the owner's data a kindNoData row must not carry.
	absent []string
	// same names the row whose denial this row's denial must equal.
	same string
	// needs names an optional fixture; a target without it skips the row loudly.
	needs string
	// resultClass is the request line's result_class in process.
	resultClass string
}

var (
	toolsAnswerSet = []string{"context_for_task", "investigate_question", "investigation_result", "source_evidence"}
	toolsWriteSet  = []string{"context_for_task", "investigate_question", "investigation_result", "record_episode", "source_evidence"}
	allTools       = []string{"context_for_task", "source_evidence", "investigate_question", "investigation_result", "record_episode"}
)

// matrixFixture is what a target must know to build its rows.
type matrixFixture struct {
	resultA, resultB, resultNone      string
	evidenceA, evidenceNone           string
	repoA, repoOut                    string
	branch, commit                    string
	forbid                            []string
	contextMarkers                    []string // markers of the data context_for_task serves the owner
	investigateMark, investigateMarkB string   // "" = the target cannot say what an investigation answers
	// optional fixtures: a live target may not have them.
	hasResultB, hasH, canInvestigate bool
}

func (f matrixFixture) contextArgs(repo string) map[string]any {
	args := map[string]any{"goal": "auth matrix probe", "repository": map[string]any{"slug": repo}}
	scope := map[string]any{}
	if f.branch != "" {
		scope["branch"] = f.branch
	}
	if f.commit != "" {
		scope["commit_sha"] = f.commit
	}
	if len(scope) > 0 {
		args["scope"] = scope
	}
	return args
}

// wireDenials are the four callers refused before any MCP handler runs.
var wireDenials = []struct {
	caller  string
	status  int
	outcome string
	guard   string
}{
	{"D", 401, acrmcp.HTTPAuthInvalidCredential, "revocation_in_store_lookup"},
	{"E", 401, acrmcp.HTTPAuthInvalidCredential, "expiry_check"},
	{"F", 401, acrmcp.HTTPAuthMissingBearer, "bearer_required"},
	{"G", 401, acrmcp.HTTPAuthMalformedBearer, "bearer_shape_three_layers"},
}

func matrixRows(f matrixFixture) []matrixRow {
	investigate := map[string]any{"question": "what is the status of the project?"}
	rows := []matrixRow{
		// tools/list per caller.
		{id: "list.A", caller: "A", kind: kindList, listed: toolsAnswerSet, resultClass: acrmcp.HTTPResultOK, guard: "per_caller_catalogue"},
		{id: "list.B", caller: "B", kind: kindList, listed: toolsAnswerSet, resultClass: acrmcp.HTTPResultOK, guard: "per_caller_catalogue"},
		{id: "list.C", caller: "C", kind: kindList, listed: toolsAnswerSet, resultClass: acrmcp.HTTPResultOK, guard: "per_caller_catalogue"},
		{id: "list.H", caller: "H", kind: kindList, listed: toolsWriteSet, resultClass: acrmcp.HTTPResultOK, guard: "per_caller_catalogue", needs: "H"},

		// investigate_question: served on the caller's own identity.
		{id: "investigate.A.ok", caller: "A", tool: "investigate_question", args: investigate, kind: kindOK, contains: nonEmpty(f.investigateMark), resultClass: acrmcp.HTTPResultOK, guard: "per_request_identity", needs: "investigate"},
		{id: "investigate.B.ok", caller: "B", tool: "investigate_question", args: investigate, kind: kindOK, contains: nonEmpty(f.investigateMarkB), resultClass: acrmcp.HTTPResultOK, guard: "per_request_identity", needs: "investigate"},

		// investigation_result: a result id is never authorization.
		{id: "result.A.own", caller: "A", tool: "investigation_result", args: resultArgs(f.resultA), kind: kindOK, contains: []string{f.resultA}, resultClass: acrmcp.HTTPResultOK, guard: "result_served_to_owner"},
		{id: "result.B.own", caller: "B", tool: "investigation_result", args: resultArgs(f.resultB), kind: kindOK, contains: []string{f.resultB}, resultClass: acrmcp.HTTPResultOK, guard: "result_served_to_owner", needs: "resultB"},
		{id: "result.A.none", caller: "A", tool: "investigation_result", args: resultArgs(f.resultNone), kind: kindToolDeny, resultClass: acrmcp.HTTPResultToolError, guard: "unknown_id_control"},
		{id: "result.B.none", caller: "B", tool: "investigation_result", args: resultArgs(f.resultNone), kind: kindToolDeny, same: "result.A.none", resultClass: acrmcp.HTTPResultToolError, guard: "unknown_id_control"},
		{id: "result.C.none", caller: "C", tool: "investigation_result", args: resultArgs(f.resultNone), kind: kindToolDeny, same: "result.A.none", resultClass: acrmcp.HTTPResultToolError, guard: "unknown_id_control"},
		{id: "result.B.foreign", caller: "B", tool: "investigation_result", args: resultArgs(f.resultA), kind: kindToolDeny, same: "result.B.none", resultClass: acrmcp.HTTPResultToolError, guard: "result_org_scope"},
		{id: "result.C.foreign", caller: "C", tool: "investigation_result", args: resultArgs(f.resultA), kind: kindToolDeny, same: "result.C.none", resultClass: acrmcp.HTTPResultToolError, guard: "result_live_grant"},
		{id: "result.A.foreign", caller: "A", tool: "investigation_result", args: resultArgs(f.resultB), kind: kindToolDeny, same: "result.A.none", resultClass: acrmcp.HTTPResultToolError, guard: "result_org_scope", needs: "resultB"},

		// source_evidence: an evidence ref is never authorization either.
		{id: "evidence.A.own", caller: "A", tool: "source_evidence", args: evidenceArgs(f.evidenceA), kind: kindOK, contains: []string{f.evidenceA}, resultClass: acrmcp.HTTPResultOK, guard: "evidence_served_to_owner"},
		{id: "evidence.A.none", caller: "A", tool: "source_evidence", args: evidenceArgs(f.evidenceNone), kind: kindToolDeny, resultClass: acrmcp.HTTPResultToolError, guard: "unknown_id_control"},
		{id: "evidence.B.none", caller: "B", tool: "source_evidence", args: evidenceArgs(f.evidenceNone), kind: kindToolDeny, same: "evidence.A.none", resultClass: acrmcp.HTTPResultToolError, guard: "unknown_id_control"},
		{id: "evidence.C.none", caller: "C", tool: "source_evidence", args: evidenceArgs(f.evidenceNone), kind: kindToolDeny, same: "evidence.A.none", resultClass: acrmcp.HTTPResultToolError, guard: "unknown_id_control"},
		{id: "evidence.B.foreign", caller: "B", tool: "source_evidence", args: evidenceArgs(f.evidenceA), kind: kindToolDeny, same: "evidence.B.none", resultClass: acrmcp.HTTPResultToolError, guard: "evidence_org_scope"},
		{id: "evidence.C.foreign", caller: "C", tool: "source_evidence", args: evidenceArgs(f.evidenceA), kind: kindToolDeny, same: "evidence.C.none", resultClass: acrmcp.HTTPResultToolError, guard: "evidence_repository_grant"},

		// context_for_task: an explicit scope outside the caller's grant.
		{id: "context.A.own", caller: "A", tool: "context_for_task", args: f.contextArgs(f.repoA), kind: kindOK, contains: f.contextMarkers, resultClass: acrmcp.HTTPResultOK, guard: "context_served_to_owner"},
		{id: "context.A.outside", caller: "A", tool: "context_for_task", args: f.contextArgs(f.repoOut), kind: kindToolDeny, text: "repo_forbidden", resultClass: acrmcp.HTTPResultToolError, guard: "context_repository_grant"},
		{id: "context.B.foreign", caller: "B", tool: "context_for_task", args: f.contextArgs(f.repoA), kind: kindNoData, absent: f.contextMarkers, resultClass: acrmcp.HTTPResultOK, guard: "context_org_scope"},
		{id: "context.C.foreign", caller: "C", tool: "context_for_task", args: f.contextArgs(f.repoA), kind: kindToolDeny, text: "repo_forbidden", resultClass: acrmcp.HTTPResultToolError, guard: "context_repository_grant"},

		// record_episode is offered to H alone: everyone else names a tool
		// that is not theirs.
		{id: "episode.A.unlisted", caller: "A", tool: "record_episode", args: map[string]any{}, kind: kindUnknown, resultClass: acrmcp.HTTPResultProtocolError, guard: "per_caller_catalogue"},
		{id: "episode.B.unlisted", caller: "B", tool: "record_episode", args: map[string]any{}, kind: kindUnknown, resultClass: acrmcp.HTTPResultProtocolError, guard: "per_caller_catalogue"},
		{id: "episode.C.unlisted", caller: "C", tool: "record_episode", args: map[string]any{}, kind: kindUnknown, resultClass: acrmcp.HTTPResultProtocolError, guard: "per_caller_catalogue"},
	}
	// Every caller refused on the wire is refused for every tool.
	for _, denial := range wireDenials {
		for _, tool := range append([]string{""}, allTools...) {
			name := tool
			if name == "" {
				name = "list"
			}
			args := map[string]any{}
			switch tool {
			case "investigation_result":
				args = resultArgs(f.resultA)
			case "source_evidence":
				args = evidenceArgs(f.evidenceA)
			case "context_for_task":
				args = f.contextArgs(f.repoA)
			case "investigate_question":
				args = investigate
			}
			rows = append(rows, matrixRow{
				id: fmt.Sprintf("wire.%s.%s", denial.caller, name), caller: denial.caller, tool: tool, args: args,
				kind: kindHTTPDeny, status: denial.status, outcome: denial.outcome, guard: denial.guard,
				resultClass: acrmcp.HTTPResultAuthDenied,
			})
		}
	}
	return rows
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func resultArgs(id string) map[string]any   { return map[string]any{"result_id": id} }
func evidenceArgs(id string) map[string]any { return map[string]any{"evidence_ref_id": id} }

// expectedMatrixRows is the size of the table. The runner fails when the
// table drifts from it, so a row cannot be dropped without the drop being a
// visible edit.
const expectedMatrixRows = 27 + 4*6

// matrixTarget is one endpoint under test.
type matrixTarget struct {
	name    string
	url     string
	client  *http.Client
	bearers map[string]string // caller -> bearer; "" sends no Authorization header
	fixture matrixFixture
	// hooks are the in-process observations; nil against a live endpoint.
	hooks *matrixHooks
}

type rpcReply struct {
	status  int
	header  http.Header
	raw     []byte
	errCode int
	hasErr  bool
	result  json.RawMessage
}

type toolReply struct {
	IsError bool `json:"isError"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func (t toolReply) text() string {
	var b strings.Builder
	for _, c := range t.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// callRaw posts one MCP 2026-07-28 request as a client puts it on the wire.
func (m *matrixTarget) callRaw(tb testing.TB, caller, requestID string, tool string, args map[string]any) rpcReply {
	tb.Helper()
	var body []byte
	if tool == "" {
		body = rawToolsList()
	} else {
		body = rawToolsCall(tool, args)
	}
	req, err := http.NewRequest(http.MethodPost, m.url, bytes.NewReader(body))
	if err != nil {
		tb.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	if tool == "" {
		req.Header.Set("Mcp-Method", "tools/list")
	} else {
		req.Header.Set("Mcp-Method", "tools/call")
		req.Header.Set("Mcp-Name", tool)
	}
	req.Header.Set("X-Request-ID", requestID)
	if bearer := m.bearers[caller]; bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		tb.Errorf("%s: %v", requestID, err)
		return rpcReply{}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		tb.Errorf("%s: read body: %v", requestID, err)
	}
	return parseRPC(resp.StatusCode, resp.Header, raw)
}

// parseRPC reads the JSON-RPC envelope from a JSON or event-stream body.
func parseRPC(status int, header http.Header, raw []byte) rpcReply {
	reply := rpcReply{status: status, header: header, raw: raw}
	payload := bytes.TrimSpace(raw)
	if strings.HasPrefix(header.Get("Content-Type"), "text/event-stream") {
		payload = nil
		for _, line := range bytes.Split(raw, []byte("\n")) {
			if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
				payload = bytes.TrimSpace(data)
			}
		}
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &envelope) == nil {
		reply.result = envelope.Result
		if envelope.Error != nil {
			reply.hasErr, reply.errCode = true, envelope.Error.Code
		}
	}
	return reply
}

func (r rpcReply) tool() (toolReply, bool) {
	var t toolReply
	if len(r.result) == 0 || json.Unmarshal(r.result, &t) != nil || t.Content == nil && !t.IsError {
		return t, false
	}
	return t, true
}

func (r rpcReply) toolNames() []string {
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if json.Unmarshal(r.result, &listed) != nil {
		return nil
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// requestIDs and the like the denial text may carry are not part of the
// decision: they are stripped before two denials are compared.
var volatileToken = regexp.MustCompile(`(req|request|mcp)_[0-9a-zA-Z]+`)

func normalizeDenial(text string) string { return volatileToken.ReplaceAllString(text, "<id>") }

// matrixHooks are the in-process observations taken around every row.
type matrixHooks struct {
	sdkHits   func() int64
	apiMark   func() int
	apiSince  func(mark int) []apiCall
	apiLog    func() []byte
	evidence  func() (resolve, packets int)
	episodes  func() int
	endpoint  func() *certify.Log
	credByRow func(caller string) string
}

type hookMark struct {
	sdk              int64
	api              int
	apiLogLen        int
	resolve, packets int
	episodes         int
}

func (h *matrixHooks) mark() hookMark {
	resolve, packets := h.evidence()
	return hookMark{sdk: h.sdkHits(), api: h.apiMark(), apiLogLen: len(h.apiLog()), resolve: resolve, packets: packets, episodes: h.episodes()}
}

// storedResultLines returns the stored-result decision lines written after a mark.
func (h *matrixHooks) storedResultLines(t *testing.T, mark hookMark) []map[string]any {
	t.Helper()
	log, err := certify.Parse(h.apiLog()[mark.apiLogLen:])
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range log.LinesWithMsg(contextfabric.StoredResultAuthorizationLogMessage) {
		out = append(out, line)
	}
	return out
}

// runMatrix executes every row against the target.
func runMatrix(t *testing.T, target *matrixTarget) {
	t.Helper()
	rows := matrixRows(target.fixture)
	if len(rows) == 0 || len(rows) != expectedMatrixRows {
		t.Fatalf("the matrix has %d rows, want %d", len(rows), expectedMatrixRows)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.id] {
			t.Fatalf("duplicate row id %q", row.id)
		}
		seen[row.id] = true
	}
	for _, denial := range wireDenials {
		if _, ok := target.bearers[denial.caller]; !ok {
			t.Fatalf("target has no entry for caller %s", denial.caller)
		}
	}

	denials := map[string]string{} // row id -> normalized denial text
	executed := map[string]bool{}
	for _, row := range rows {
		t.Run(row.id, func(t *testing.T) {
			if skip := target.skipReason(row); skip != "" {
				t.Skipf("NOT EXECUTED: %s", skip)
			}
			executed[row.id] = true
			requestID := "matrix." + row.id
			var before hookMark
			if target.hooks != nil {
				before = target.hooks.mark()
			}
			reply := target.callRaw(t, row.caller, requestID, row.tool, row.args)
			checkRow(t, target, row, reply, denials)
			if target.hooks != nil {
				checkInProcess(t, target, row, requestID, reply, before)
			}
		})
	}
	// A denial must be indistinguishable from the denial of an id that never
	// existed: the difference between two denials is the existence leak.
	for _, row := range rows {
		if row.same == "" || !executed[row.id] || !executed[row.same] {
			continue
		}
		if denials[row.id] != denials[row.same] {
			t.Errorf("%s: denial %q differs from the denial of an id that never existed (%s: %q)", row.id, denials[row.id], row.same, denials[row.same])
		}
	}
	if len(executed) == 0 {
		t.Fatal("no row executed")
	}
}

func (m *matrixTarget) skipReason(row matrixRow) string {
	switch row.needs {
	case "":
		return ""
	case "H":
		if !m.fixture.hasH {
			return "no caller H (episode writer) is configured"
		}
	case "resultB":
		if !m.fixture.hasResultB {
			return "no result owned by caller B is configured"
		}
	case "investigate":
		if !m.fixture.canInvestigate {
			return "investigating was not requested for this target"
		}
	}
	return ""
}

func checkRow(t *testing.T, target *matrixTarget, row matrixRow, reply rpcReply, denials map[string]string) {
	t.Helper()
	body := string(reply.raw)
	shown := body
	if tr, ok := reply.tool(); ok {
		shown = tr.text()
	}
	t.Logf("observed: caller=%s status=%d body=%.900q", row.caller, reply.status, strings.Join(strings.Fields(shown), " "))
	switch row.kind {
	case kindHTTPDeny:
		var refusal struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(reply.raw, &refusal)
		if reply.status != row.status || refusal.Error != row.outcome {
			t.Errorf("status %d error %q, want %d %q (body %.200s)", reply.status, refusal.Error, row.status, row.outcome, body)
		}
		if reply.header.Get("WWW-Authenticate") == "" {
			t.Error("a 401 must carry a WWW-Authenticate challenge")
		}
		if len(reply.result) != 0 {
			t.Errorf("a refused request carried a result: %.200s", reply.result)
		}
	case kindList:
		if reply.status != http.StatusOK || !slices.Equal(reply.toolNames(), row.listed) {
			t.Errorf("status %d tools %v, want 200 %v", reply.status, reply.toolNames(), row.listed)
		}
	case kindOK:
		tr, ok := reply.tool()
		if reply.status != http.StatusOK || !ok || tr.IsError {
			t.Fatalf("status %d, want a served answer: %.400s", reply.status, body)
		}
		for _, marker := range row.contains {
			if !strings.Contains(body, marker) {
				t.Errorf("the served answer does not carry %q: %.400s", marker, body)
			}
		}
	case kindToolDeny:
		tr, ok := reply.tool()
		if reply.status != http.StatusOK || !ok || !tr.IsError {
			t.Fatalf("status %d, want a tool error and no data: %.400s", reply.status, body)
		}
		denials[row.id] = normalizeDenial(tr.text())
		if row.text != "" && !strings.Contains(tr.text(), row.text) {
			t.Errorf("the denial %q does not name the guard's public error code %q", tr.text(), row.text)
		}
		if tr.text() == "" {
			t.Error("a denial must say something a client can act on")
		}
		for _, secret := range target.fixture.forbid {
			if secret != "" && strings.Contains(body, secret) {
				t.Errorf("the denial leaks %q: %.400s", secret, body)
			}
		}
	case kindNoData:
		// The grant may refuse the request outright (a tool error) or the
		// store may answer it with an empty, degraded packet: either way
		// none of the owner's data reaches this caller.
		_, ok := reply.tool()
		if reply.status != http.StatusOK || !ok {
			t.Fatalf("status %d, want a tool error or an answer without the owner's data: %.400s", reply.status, body)
		}
		for _, secret := range append(slices.Clone(target.fixture.forbid), row.absent...) {
			if secret != "" && strings.Contains(body, secret) {
				t.Errorf("the answer carries the owner's data %q: %.400s", secret, body)
			}
		}
	case kindUnknown:
		tr, isTool := reply.tool()
		if reply.status == http.StatusOK && !reply.hasErr && isTool && !tr.IsError {
			t.Errorf("a tool that is not this caller's was served: %.400s", body)
		}
		if !reply.hasErr && reply.status == http.StatusOK && !(isTool && tr.IsError) {
			t.Errorf("status 200 with neither a protocol error nor a tool error: %.400s", body)
		}
	}
}

// checkInProcess asserts what only an in-process target can observe.
func checkInProcess(t *testing.T, target *matrixTarget, row matrixRow, requestID string, reply rpcReply, before hookMark) {
	t.Helper()
	h := target.hooks
	after := h.mark()

	// The SDK handler ran exactly for the requests that were not refused
	// on the wire.
	wantHits := int64(1)
	if row.kind == kindHTTPDeny {
		wantHits = 0
	}
	if got := after.sdk - before.sdk; got != wantHits {
		t.Errorf("the SDK handler ran %d times, want %d", got, wantHits)
	}

	// What acr-api saw: a credential the API refuses is refused by the API
	// on the caller's own bearer (one capabilities call, 401); a bearer the
	// endpoint could not read never reaches it.
	calls := h.apiSince(before.api)
	switch {
	case row.kind == kindHTTPDeny && (row.caller == "D" || row.caller == "E"):
		if len(calls) != 1 || calls[0].path != "/api/v1/agent-context/capabilities" || calls[0].status != http.StatusUnauthorized {
			t.Errorf("acr-api calls %v, want exactly one 401 on capabilities", calls)
		}
	case row.kind == kindHTTPDeny:
		if len(calls) != 0 {
			t.Errorf("a bearer the endpoint refused itself still reached acr-api: %v", calls)
		}
	default:
		if len(calls) < 1 || calls[0].path != "/api/v1/agent-context/capabilities" || calls[0].status != http.StatusOK {
			t.Errorf("acr-api calls %v, want the caller's own capabilities decision first", calls)
		}
	}

	// The episode sink is never reached by a matrix row.
	if after.episodes != before.episodes {
		t.Errorf("a row reached the episode sink")
	}

	// Which store a deny row reached tells which guard refused it.
	resolves, packets := after.resolve-before.resolve, after.packets-before.packets
	switch row.id {
	case "evidence.B.foreign", "evidence.C.foreign", "evidence.A.own", "evidence.B.none", "evidence.C.none", "evidence.A.none":
		if resolves != 1 {
			t.Errorf("the evidence store was consulted %d times, want 1", resolves)
		}
	case "context.A.outside", "context.C.foreign":
		if packets != 0 {
			t.Errorf("the repository grant must refuse before the assembler: store consulted %d times", packets)
		}
	case "context.B.foreign", "context.A.own":
		if packets == 0 {
			t.Errorf("the grant admits this repository, so the assembler must reach the store")
		}
	}

	// The stored-result decision line: one per decided read, none where the
	// organization scope answered first.
	switch row.id {
	case "result.A.own", "result.B.own", "result.C.foreign":
		lines := h.storedResultLines(t, before)
		want := "admitted"
		if row.id == "result.C.foreign" {
			want = "denied"
		}
		if len(lines) != 1 || lines[0]["decision"] != want {
			t.Errorf("stored-result decision lines %v, want exactly one %q", lines, want)
		}
	case "result.B.foreign", "result.A.foreign", "result.A.none", "result.B.none", "result.C.none":
		if lines := h.storedResultLines(t, before); len(lines) != 0 {
			t.Errorf("the store refused before the gate, yet the gate decided: %v", lines)
		}
	}

	// The request line: exactly one, with the row's own outcome.
	log := h.endpoint()
	var lines []certify.Line
	for _, line := range log.LinesWithMsg(eventspec.MCPHTTPRequest.Msg) {
		if line["request_id"] == requestID {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("%d request lines carry request_id %s, want exactly 1", len(lines), requestID)
	}
	want := map[string]any{
		"request_id": requestID, "transport": "http", "result_class": row.resultClass, "status": float64(reply.status),
		"auth_outcome": acrmcp.HTTPAuthAdmitted, "principal_class": acrmcp.PrincipalClassBearer,
	}
	if row.kind == kindHTTPDeny {
		want["auth_outcome"] = row.outcome
		if row.caller == "F" {
			want["principal_class"] = acrmcp.PrincipalClassNone
		}
		if row.caller == "G" {
			want["principal_class"] = acrmcp.PrincipalClassNone
		}
	}
	if row.tool == "" {
		want["method"] = "tools/list"
	} else {
		want["method"] = "tools/call"
	}
	if row.kind != kindHTTPDeny {
		want["tool"] = row.tool
		if row.tool == "" {
			want["tool"] = "none"
		}
		if !slices.Contains(acrmcp.HTTPToolVocabulary(), row.tool) && row.tool != "" {
			want["tool"] = "other"
		}
	}
	if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.MCPHTTPRequest, Want: want}); err != nil {
		t.Errorf("request line: %v", err)
	}
	if lines[0]["auth_outcome"] != want["auth_outcome"] {
		t.Errorf("auth_outcome %v, want %v", lines[0]["auth_outcome"], want["auth_outcome"])
	}
}

// matrixCallers issues the eight callers of the matrix on the stack.
type matrixCallers struct {
	a, b, c, d, e, h issued
}

func issueMatrixCallers(s *matrixStack) matrixCallers {
	reads := []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}
	expiredAt := time.Now().Add(-time.Hour)
	c := matrixCallers{
		a: s.issue(orgOne, reads, []string{repoWidget}, nil),
		b: s.issue(orgTwo, reads, []string{repoWidget}, nil),
		c: s.issue(orgOne, reads, []string{repoOther}, nil),
		d: s.issue(orgOne, reads, []string{repoWidget}, nil),
		e: s.issue(orgOne, reads, []string{repoWidget}, &expiredAt),
		h: s.issue(orgOne, append(slices.Clone(reads), auth.ScopeEpisodeWrite), []string{repoWidget}, nil),
	}
	s.revoke(orgOne, c.d)
	return c
}

func (c matrixCallers) bearers() map[string]string {
	return map[string]string{"A": c.a.token, "B": c.b.token, "C": c.c.token, "D": c.d.token, "E": c.e.token, "F": "", "G": "not-an-acr-bearer-token", "H": c.h.token}
}

// newMatrixEndpoint puts the real acr-mcp hosted handler in front of the
// stack, exactly as ServeHTTPTransport builds it.
func newMatrixEndpoint(t *testing.T, s *matrixStack) *endpoint {
	t.Helper()
	logs := &syncBuffer{}
	cfg, err := acrmcp.NewHTTPProcessConfig(s.sidecarConfig(), testIdentity, logs)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := acrmcp.NewHTTPHandler(cfg, acrmcp.HTTPHandlerOptions{BasePath: "/mcp", Identity: testIdentity, MaxRequestBodyBytes: 1 << 20, ResolveTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	e := &endpoint{handler: handler, logs: logs}
	acrmcp.WrapHTTPHandlerSDKForTest(handler, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			e.sdkHits.Add(1)
			next.ServeHTTP(w, r)
		})
	})
	e.server = httptest.NewServer(handler)
	t.Cleanup(e.server.Close)
	return e
}

func inProcessFixture(callers matrixCallers) matrixFixture {
	return matrixFixture{
		resultA: resultOwnedByA, resultB: resultOwnedByB, resultNone: resultNeverMade,
		evidenceA: evidenceRef, evidenceNone: evidenceNever,
		repoA: repoWidget, repoOut: "other-org/secret-service",
		branch: corpusBranch, commit: corpusCommit,
		contextMarkers:   []string{"checkout-e2e / flaky retry"},
		forbid:           []string{judgmentOfA, labelOfA, judgmentOfB, labelOfB, "checkout-e2e-run-4821", "add-to-cart", orgOne, orgTwo, callers.a.credentialID, callers.b.credentialID},
		investigateMark:  "investigated-for-" + orgOne + "/" + callers.a.credentialID,
		investigateMarkB: "investigated-for-" + orgTwo + "/" + callers.b.credentialID,
		hasResultB:       true, hasH: true, canInvestigate: true,
	}
}

func inProcessTarget(t *testing.T) (*matrixTarget, *matrixStack, *endpoint, matrixCallers) {
	t.Helper()
	stack := newMatrixStack(t)
	callers := issueMatrixCallers(stack)
	e := newMatrixEndpoint(t, stack)
	target := &matrixTarget{
		name: "in-process", url: e.url(), client: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 256}},
		bearers: callers.bearers(), fixture: inProcessFixture(callers),
		hooks: &matrixHooks{
			sdkHits: e.sdkHits.Load, apiMark: stack.calls.mark, apiSince: stack.calls.since, apiLog: stack.apiLogs.Bytes,
			evidence: stack.evidence.counts, episodes: stack.episodes.count,
			endpoint: func() *certify.Log {
				log, err := certify.Parse(e.logs.Bytes())
				if err != nil {
					t.Fatal(err)
				}
				return log
			},
		},
	}
	return target, stack, e, callers
}

// The whole matrix, in CI, against acr-mcp in front of a real acr-api.
func TestAuthMatrixInProcess(t *testing.T) {
	target, _, _, _ := inProcessTarget(t)
	runMatrix(t, target)
}

// Two organizations' callers, interleaved and parallel on one endpoint:
// every response belongs to its own bearer, nothing is cached across
// callers, and the tool catalogue follows each credential.
func TestAuthMatrixConcurrentCallersNeverBleedIdentity(t *testing.T) {
	t.Parallel()
	target, stack, e, callers := inProcessTarget(t)
	f := target.fixture
	const perCaller = 60

	var wg sync.WaitGroup
	start := make(chan struct{})
	launch := func(caller string, i int, work func(id func(step string) string)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			work(func(step string) string { return fmt.Sprintf("conc.%s.%03d.%s", caller, i, step) })
		}()
	}
	expectOK := func(t *testing.T, id string, reply rpcReply, markers ...string) {
		tr, ok := reply.tool()
		if reply.status != http.StatusOK || !ok || tr.IsError {
			t.Errorf("%s: status %d, want a served answer: %.300s", id, reply.status, reply.raw)
			return
		}
		for _, marker := range markers {
			if !strings.Contains(string(reply.raw), marker) {
				t.Errorf("%s: the answer does not carry %q: %.300s", id, marker, reply.raw)
			}
		}
	}
	expectDenied := func(t *testing.T, id string, reply rpcReply) {
		tr, ok := reply.tool()
		if reply.status != http.StatusOK || !ok || !tr.IsError {
			t.Errorf("%s: status %d, want a tool error: %.300s", id, reply.status, reply.raw)
			return
		}
		for _, secret := range f.forbid {
			if strings.Contains(string(reply.raw), secret) {
				t.Errorf("%s: the denial leaks %q", id, secret)
			}
		}
	}
	for i := range perCaller {
		launch("A", i, func(id func(string) string) {
			if names := target.callRaw(t, "A", id("list"), "", nil).toolNames(); !slices.Equal(names, toolsAnswerSet) {
				t.Errorf("A list %v", names)
			}
			expectOK(t, id("result"), target.callRaw(t, "A", id("result"), "investigation_result", resultArgs(f.resultA)), f.resultA)
			expectOK(t, id("evidence"), target.callRaw(t, "A", id("evidence"), "source_evidence", evidenceArgs(f.evidenceA)), f.evidenceA)
			expectOK(t, id("investigate"), target.callRaw(t, "A", id("investigate"), "investigate_question", map[string]any{"question": "what is the status of the project?"}), f.investigateMark)
		})
		launch("B", i, func(id func(string) string) {
			if names := target.callRaw(t, "B", id("list"), "", nil).toolNames(); !slices.Equal(names, toolsAnswerSet) {
				t.Errorf("B list %v", names)
			}
			expectDenied(t, id("foreign"), target.callRaw(t, "B", id("foreign"), "investigation_result", resultArgs(f.resultA)))
			expectOK(t, id("result"), target.callRaw(t, "B", id("result"), "investigation_result", resultArgs(f.resultB)), f.resultB)
			expectDenied(t, id("evidence"), target.callRaw(t, "B", id("evidence"), "source_evidence", evidenceArgs(f.evidenceA)))
			expectOK(t, id("investigate"), target.callRaw(t, "B", id("investigate"), "investigate_question", map[string]any{"question": "what is the status of the project?"}), f.investigateMarkB)
		})
		launch("H", i, func(id func(string) string) {
			if names := target.callRaw(t, "H", id("list"), "", nil).toolNames(); !slices.Equal(names, toolsWriteSet) {
				t.Errorf("H list %v", names)
			}
			expectOK(t, id("result"), target.callRaw(t, "H", id("result"), "investigation_result", resultArgs(f.resultA)), f.resultA)
		})
	}
	close(start)
	wg.Wait()
	if t.Failed() {
		return
	}

	// Each bearer's requests were attributed to exactly one principal, and
	// no two bearers share one.
	log, err := certify.Parse(e.logs.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]map[string]int{}
	maxInFlight := 0
	requests := 0
	for _, line := range log.LinesWithMsg(eventspec.MCPHTTPRequest.Msg) {
		id, _ := line["request_id"].(string)
		if !strings.HasPrefix(id, "conc.") {
			continue
		}
		requests++
		caller := strings.Split(id, ".")[1]
		ref, _ := line["principal_ref"].(string)
		if refs[caller] == nil {
			refs[caller] = map[string]int{}
		}
		refs[caller][ref]++
		if v, ok := line["in_flight"].(float64); ok && int(v) > maxInFlight {
			maxInFlight = int(v)
		}
	}
	if want := perCaller * (4 + 5 + 2); requests != want {
		t.Fatalf("%d request lines, want %d", requests, want)
	}
	owner := map[string]string{}
	for caller, byRef := range refs {
		if len(byRef) != 1 {
			t.Errorf("caller %s was attributed to %d principals: %v", caller, len(byRef), byRef)
		}
		for ref := range byRef {
			if other, ok := owner[ref]; ok {
				t.Errorf("callers %s and %s share principal_ref %s", caller, other, ref)
			}
			owner[ref] = caller
		}
	}
	if len(refs) != 3 {
		t.Errorf("%d callers observed, want 3", len(refs))
	}
	if maxInFlight < 2 {
		t.Errorf("the requests never overlapped (max in_flight %d): the concurrency was not exercised", maxInFlight)
	}
	if n := e.handler.InFlight(); n != 0 {
		t.Errorf("in-flight gauge %d after every request completed", n)
	}

	// acr-api decided every stored-result read on the caller's own
	// organization: A and H read org_1's result, B read org_2's, and no
	// read of the other organization's result reached the gate.
	byOrg := map[string]int{}
	for _, line := range stack.storedResultLinesAll(t) {
		if line["decision"] != "admitted" {
			t.Errorf("a concurrent read was decided %v", line["decision"])
		}
		org, _ := line["org_id"].(string)
		byOrg[org]++
	}
	if byOrg[orgOne] != 2*perCaller || byOrg[orgTwo] != perCaller || len(byOrg) != 2 {
		t.Errorf("stored-result decisions by organization %v, want %s=%d %s=%d", byOrg, orgOne, 2*perCaller, orgTwo, perCaller)
	}

	// No state survived the storm: a fresh request from each caller is
	// still answered for that caller alone.
	if names := target.callRaw(t, "A", "conc.after.A", "", nil).toolNames(); !slices.Equal(names, toolsAnswerSet) {
		t.Errorf("A after the storm: %v", names)
	}
	expectDenied(t, "conc.after.B", target.callRaw(t, "B", "conc.after.B", "investigation_result", resultArgs(f.resultA)))
	_ = callers
}

func (s *matrixStack) storedResultLinesAll(t *testing.T) []map[string]any {
	t.Helper()
	log, err := certify.Parse(s.apiLogs.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range log.LinesWithMsg(contextfabric.StoredResultAuthorizationLogMessage) {
		out = append(out, line)
	}
	return out
}

// The SDK's own client sees the same catalogue the raw rows saw: one
// session per caller, and no session state is shared between them.
func TestAuthMatrixSDKClientsSeeTheirOwnCatalogue(t *testing.T) {
	target, _, e, callers := inProcessTarget(t)
	_ = target
	for _, c := range []struct {
		name string
		cred issued
		want []string
	}{{"A", callers.a, toolsAnswerSet}, {"H", callers.h, toolsWriteSet}, {"C", callers.c, toolsAnswerSet}} {
		session := connectClient(t, e, &headerTransport{bearer: c.cred.token})
		if got := toolNames(t, session); !slices.Equal(got, c.want) {
			t.Errorf("%s: tools %v, want %v", c.name, got, c.want)
		}
	}
}

// Live mode: the same matrix against a deployed endpoint. Nothing is
// committed: the endpoint and every bearer come from the environment.
//
//	ACR_MCP_MATRIX_URL           the MCP endpoint, e.g. https://acr-mcp.example/mcp
//	ACR_MCP_MATRIX_BEARER_A      caller A: owner of the fixtures
//	ACR_MCP_MATRIX_BEARER_B      caller B: another organization
//	ACR_MCP_MATRIX_BEARER_C      caller C: A's organization, narrower repository grant
//	ACR_MCP_MATRIX_BEARER_D      caller D: a REVOKED credential
//	ACR_MCP_MATRIX_BEARER_E      caller E: an EXPIRED credential
//	ACR_MCP_MATRIX_RESULT_A      a stored result id owned by A
//	ACR_MCP_MATRIX_EVIDENCE_A    an evidence ref id readable by A
//	ACR_MCP_MATRIX_REPO_A        a repository slug in A's grant (and in neither B's nor C's)
//	ACR_MCP_MATRIX_REPO_OUT      a repository slug outside every grant
//	optional:
//	ACR_MCP_MATRIX_BEARER_H      a caller with episode:write (record_episode is then expected)
//	ACR_MCP_MATRIX_RESULT_B      a stored result id owned by B
//	ACR_MCP_MATRIX_BRANCH, ACR_MCP_MATRIX_COMMIT   scope of the context_for_task rows
//	ACR_MCP_MATRIX_INVESTIGATE=1 also run the investigate_question rows (they cost a model call)
//	ACR_MCP_MATRIX_CONTEXT_MARKER  text a context_for_task packet for A carries (also asserted absent for B)
//	ACR_MCP_MATRIX_FORBID        comma-separated text that must never appear in a denial
//	ACR_MCP_MATRIX_CA_FILE       PEM bundle that signs the endpoint's certificate
//	ACR_MCP_MATRIX_REQUIRE_LIVE=1  an unset endpoint FAILS instead of skipping
func TestAuthMatrixLive(t *testing.T) {
	runLiveMatrixFromEnv(t)
}

func runLiveMatrixFromEnv(t *testing.T) {
	t.Helper()
	target, missing, err := liveTargetFromEnv(os.Getenv)
	switch {
	case err != nil:
		t.Fatal(err)
	case target == nil && os.Getenv("ACR_MCP_MATRIX_REQUIRE_LIVE") == "1":
		t.Fatal("ACR_MCP_MATRIX_REQUIRE_LIVE=1 but ACR_MCP_MATRIX_URL is not set: the live matrix did not run")
	case target == nil:
		t.Skip("LIVE MATRIX NOT EXECUTED: ACR_MCP_MATRIX_URL is not set. The in-process matrix (TestAuthMatrixInProcess) is not a substitute for a deployed endpoint; see docs/mcp-auth-matrix.md")
	case len(missing) > 0:
		t.Fatalf("ACR_MCP_MATRIX_URL is set but %s are not: the live matrix cannot run", strings.Join(missing, ", "))
	}
	runMatrix(t, target)
}

// liveTargetFromEnv reads a deployed target from the environment. It returns
// a nil target when no endpoint is configured, and the names of the required
// settings that are missing when one is configured only in part.
func liveTargetFromEnv(getenv func(string) string) (*matrixTarget, []string, error) {
	endpointURL := getenv("ACR_MCP_MATRIX_URL")
	if endpointURL == "" {
		return nil, nil, nil
	}
	var missing []string
	need := func(name string) string {
		v := getenv(name)
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}
	bearers := map[string]string{
		"A": need("ACR_MCP_MATRIX_BEARER_A"), "B": need("ACR_MCP_MATRIX_BEARER_B"), "C": need("ACR_MCP_MATRIX_BEARER_C"),
		"D": need("ACR_MCP_MATRIX_BEARER_D"), "E": need("ACR_MCP_MATRIX_BEARER_E"),
		"F": "", "G": "not-an-acr-bearer-token", "H": getenv("ACR_MCP_MATRIX_BEARER_H"),
	}
	f := matrixFixture{
		resultA: need("ACR_MCP_MATRIX_RESULT_A"), resultB: getenv("ACR_MCP_MATRIX_RESULT_B"), resultNone: "result_matrix_never_made",
		evidenceA: need("ACR_MCP_MATRIX_EVIDENCE_A"), evidenceNone: "ev-never-existed-000",
		repoA: need("ACR_MCP_MATRIX_REPO_A"), repoOut: need("ACR_MCP_MATRIX_REPO_OUT"),
		branch: getenv("ACR_MCP_MATRIX_BRANCH"), commit: getenv("ACR_MCP_MATRIX_COMMIT"),
		hasResultB: getenv("ACR_MCP_MATRIX_RESULT_B") != "", hasH: bearers["H"] != "",
		canInvestigate: getenv("ACR_MCP_MATRIX_INVESTIGATE") == "1",
		contextMarkers: nonEmpty(getenv("ACR_MCP_MATRIX_CONTEXT_MARKER")),
	}
	for _, s := range strings.Split(getenv("ACR_MCP_MATRIX_FORBID"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			f.forbid = append(f.forbid, s)
		}
	}
	client := &http.Client{Timeout: 60 * time.Second}
	if ca := getenv("ACR_MCP_MATRIX_CA_FILE"); ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, nil, fmt.Errorf("%s carries no certificate", ca)
		}
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	return &matrixTarget{name: "live", url: endpointURL, client: client, bearers: bearers, fixture: f}, missing, nil
}

// The live runner, driven only by the environment, against the in-process
// endpoint: it proves the runner a deployed target would use reads its
// configuration, refuses a partial one, and passes on a correct deployment
// using nothing but the wire.
func TestAuthMatrixLiveRunnerAgainstAnInProcessEndpoint(t *testing.T) {
	target, _, _, callers := inProcessTarget(t)
	f := target.fixture
	env := map[string]string{
		"ACR_MCP_MATRIX_URL":            target.url,
		"ACR_MCP_MATRIX_BEARER_A":       callers.a.token,
		"ACR_MCP_MATRIX_BEARER_B":       callers.b.token,
		"ACR_MCP_MATRIX_BEARER_C":       callers.c.token,
		"ACR_MCP_MATRIX_BEARER_D":       callers.d.token,
		"ACR_MCP_MATRIX_BEARER_E":       callers.e.token,
		"ACR_MCP_MATRIX_BEARER_H":       callers.h.token,
		"ACR_MCP_MATRIX_RESULT_A":       f.resultA,
		"ACR_MCP_MATRIX_RESULT_B":       f.resultB,
		"ACR_MCP_MATRIX_EVIDENCE_A":     f.evidenceA,
		"ACR_MCP_MATRIX_REPO_A":         f.repoA,
		"ACR_MCP_MATRIX_REPO_OUT":       f.repoOut,
		"ACR_MCP_MATRIX_BRANCH":         f.branch,
		"ACR_MCP_MATRIX_COMMIT":         f.commit,
		"ACR_MCP_MATRIX_INVESTIGATE":    "1",
		"ACR_MCP_MATRIX_CONTEXT_MARKER": f.contextMarkers[0],
		"ACR_MCP_MATRIX_FORBID":         strings.Join([]string{judgmentOfA, labelOfA, judgmentOfB, labelOfB}, ","),
		"ACR_MCP_MATRIX_REQUIRE_LIVE":   "1",
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
	t.Run("configured", func(t *testing.T) { runLiveMatrixFromEnv(t) })

	// A partial configuration is reported, never run as a smaller matrix.
	t.Run("partial configuration is reported", func(t *testing.T) {
		partial := func(name string) string {
			if name == "ACR_MCP_MATRIX_BEARER_E" || name == "ACR_MCP_MATRIX_RESULT_A" {
				return ""
			}
			return os.Getenv(name)
		}
		target, missing, err := liveTargetFromEnv(partial)
		if err != nil || target == nil {
			t.Fatalf("target %v err %v", target, err)
		}
		if want := []string{"ACR_MCP_MATRIX_BEARER_E", "ACR_MCP_MATRIX_RESULT_A"}; !slices.Equal(missing, want) {
			t.Fatalf("missing %v, want %v", missing, want)
		}
	})
	t.Run("no endpoint configured", func(t *testing.T) {
		target, missing, err := liveTargetFromEnv(func(string) string { return "" })
		if target != nil || missing != nil || err != nil {
			t.Fatalf("%v %v %v", target, missing, err)
		}
	})
}

// The authenticator refuses a revoked or expired credential itself, not only
// through the store's lookup: a store that still returns the row (a lagging
// replica, a defect) must not turn the credential back on. Only the caller
// that is valid is admitted.
func TestAuthMatrixRefusesARevokedOrExpiredRowTheStoreStillReturns(t *testing.T) {
	server, caPath, stale, service, credentials := newStaleStoreAPI(t)
	issue := func(expires *time.Time) issued {
		credential, err := service.Create(context.Background(), auth.CreateCredentialRequest{
			OrgID: orgOne, Name: "stale-store", RepositoryScopes: []string{repoWidget}, Scopes: []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, CreatedBy: "test_actor", ExpiresAt: expires,
		})
		if err != nil {
			t.Fatal(err)
		}
		c := issued{token: credential.Token, credentialID: credential.Credential.CredentialID}
		stale.remember(c.token, c)
		return c
	}
	expiredAt := time.Now().Add(-time.Hour)
	valid, revoked, expired := issue(nil), issue(nil), issue(&expiredAt)
	if _, err := credentials.RevokeCredential(context.Background(), storage.CredentialRevocationInput{OrgID: orgOne, CredentialID: revoked.credentialID, ActorID: "admin"}); err != nil {
		t.Fatal(err)
	}

	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := acrmcp.NewHTTPProcessConfig(sidecar.Config{
		APIBaseURL: base, Timeout: 5 * time.Second, MaxResponseBytes: 1 << 20, MaxRequestBodyBytes: 256 << 10,
		ClientName: "test-sidecar", ClientVersion: "1.0.0", SidecarVersion: "1.0.0", CACertPath: caPath, AllowInsecureLoopback: true,
	}, testIdentity, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := acrmcp.NewHTTPHandler(cfg, acrmcp.HTTPHandlerOptions{BasePath: "/mcp", Identity: testIdentity, MaxRequestBodyBytes: 1 << 20, ResolveTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	e := &endpoint{handler: handler, server: httptest.NewServer(handler)}
	t.Cleanup(e.server.Close)

	for _, row := range []struct {
		name   string
		cred   issued
		status int
	}{{"valid", valid, 200}, {"revoked row returned by the store", revoked, 401}, {"expired row returned by the store", expired, 401}} {
		t.Run(row.name, func(t *testing.T) {
			resp := postMCP(t, e, http.MethodPost, rawToolsList(), bearerHeader(row.cred.token))
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != row.status {
				t.Fatalf("status %d, want %d: %.200s", resp.StatusCode, row.status, body)
			}
		})
	}
	// The control that makes the two refusals mean something: the store DID
	// return both rows.
	for name, c := range map[string]issued{"revoked": revoked, "expired": expired} {
		got, err := stale.FindByTokenHash(context.Background(), auth.HashToken(c.token))
		if err != nil || got.CredentialID != c.credentialID {
			t.Errorf("the %s row was not returned by the stale store: %v", name, err)
		}
	}
}
