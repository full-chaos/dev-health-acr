package mcp_test

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP Tasks for investigate_question over the hosted transport, against a
// hosted fake that keeps accept-first submissions the way acr-api does
// (keyed by the caller's credential here; acr-api's own org and owner rules
// are its cells).

const tasksExtensionKey = "io.modelcontextprotocol/tasks"

type fakeSubmission struct {
	owner  string
	status contractsv1.ContextFabricSubmissionStatus
}

type fakeTasksAPI struct {
	mu         sync.Mutex
	subs       map[string]*fakeSubmission
	createMode string
	creates    []http.Header
	syncCalls  int
	result     contractsv1.ContextFabricInvestigationResult
}

func newFakeTasksAPI() *fakeTasksAPI {
	result := matrixResult("result_tasks_0001", "On track.", "Ask Dev", "project_ask_dev")
	result.GeneratedAt = time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	return &fakeTasksAPI{subs: map[string]*fakeSubmission{}, result: result}
}

func hostedError(w http.ResponseWriter, status int, code string) {
	writeHostedJSON(w, status, contractsv1.ErrorEnvelope{
		SchemaVersion: contractsv1.ErrorSchema, RequestID: "req_fixture",
		Error: contractsv1.ErrorDetail{Code: code, Message: code, HTTPStatus: status},
	})
}

func (f *fakeTasksAPI) routes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	mux.Handle("POST /api/v1/context-fabric/investigations", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, _ := auth.PrincipalFromContext(r.Context())
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get(contractsv1.ContextFabricPreferHeader) == "" {
			f.syncCalls++
			writeHostedJSON(w, http.StatusOK, contractsv1.ContextFabricInvestigationResponse{ContextFabricInvestigationResult: f.result})
			return
		}
		f.creates = append(f.creates, r.Header.Clone())
		switch f.createMode {
		case "stream_unavailable":
			hostedError(w, http.StatusNotAcceptable, contractsv1.ErrorCodeStreamUnavailable)
			return
		case "other406":
			hostedError(w, http.StatusNotAcceptable, "invalid_request")
			return
		}
		id := r.Header.Get(contractsv1.ContextFabricSubmissionIDHeader)
		status := contractsv1.ContextFabricSubmissionStatus{
			SchemaVersion: contractsv1.ContextFabricSubmissionStatusSchema, SubmissionID: id,
			State: contractsv1.ContextFabricSubmissionRunning, ReservedResultID: "result_tasks_0001", UpdatedAt: time.Now().UTC().Truncate(time.Second),
		}
		f.subs[id] = &fakeSubmission{owner: principal.CredentialID, status: status}
		writeHostedJSON(w, http.StatusAccepted, status)
	})))
	own := func(r *http.Request) *fakeSubmission {
		principal, _ := auth.PrincipalFromContext(r.Context())
		sub := f.subs[r.PathValue("submission_id")]
		if sub == nil || sub.owner != principal.CredentialID {
			return nil
		}
		return sub
	}
	mux.Handle("GET /api/v1/context-fabric/investigations/submissions/{submission_id}", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		sub := own(r)
		if sub == nil {
			hostedError(w, http.StatusNotFound, "not_found")
			return
		}
		writeHostedJSON(w, http.StatusOK, sub.status)
	})))
	mux.Handle("POST /api/v1/context-fabric/investigations/submissions/{submission_id}/cancel", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		sub := own(r)
		if sub == nil {
			hostedError(w, http.StatusNotFound, "not_found")
			return
		}
		if sub.status.State != contractsv1.ContextFabricSubmissionRunning {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		sub.status.State = contractsv1.ContextFabricSubmissionAbandoned
		sub.status.Terminal = &contractsv1.ContextFabricSubmissionTerminal{Kind: contractsv1.ContextFabricSubmissionTerminalError, ErrorCode: contractsv1.ErrorCodeCanceled}
		w.WriteHeader(http.StatusAccepted)
	})))
	mux.Handle("GET /api/v1/context-fabric/investigations/{result_id}", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.PathValue("result_id") != f.result.ResultID {
			hostedError(w, http.StatusNotFound, "not_found")
			return
		}
		writeHostedJSON(w, http.StatusOK, f.result)
	})))
}

// set moves one submission to a state, a stage and a terminal.
func (f *fakeTasksAPI) set(submissionID string, state contractsv1.ContextFabricSubmissionState, stage contractsv1.ContextFabricInvestigationStage, terminal *contractsv1.ContextFabricSubmissionTerminal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sub := f.subs[submissionID]
	sub.status.State, sub.status.LastStage, sub.status.Terminal = state, stage, terminal
	sub.status.ResultID = ""
	if state == contractsv1.ContextFabricSubmissionDone {
		sub.status.ResultID = sub.status.ReservedResultID
	}
}

func (f *fakeTasksAPI) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates)
}

func (f *fakeTasksAPI) syncCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.syncCalls
}

type tasksStack struct {
	hosted *hostedAPI
	fake   *fakeTasksAPI
	e      *endpoint
	caller issued
}

func newTasksStack(t *testing.T, enabled bool) *tasksStack {
	t.Helper()
	fake := newFakeTasksAPI()
	hosted := newHostedAPIWith(t, hostedOptions{Routes: fake.routes})
	logs := &syncBuffer{}
	cfg, err := acrmcp.NewHTTPProcessConfig(hosted.sidecarConfig(), testIdentity, logs)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		acrmcp.EnableTasksForTest(cfg)
	}
	handler, err := acrmcp.NewHTTPHandler(cfg, acrmcp.HTTPHandlerOptions{BasePath: "/mcp", Identity: testIdentity, MaxRequestBodyBytes: 1 << 20, ResolveTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	e := &endpoint{handler: handler, logs: logs}
	e.server = httptest.NewServer(handler)
	t.Cleanup(e.server.Close)
	return &tasksStack{hosted: hosted, fake: fake, e: e, caller: hosted.issue(readScopes, []string{repoAnswers}, nil)}
}

func tasksMeta(revision string, declare bool) map[string]any {
	caps := map[string]any{}
	if declare {
		caps["extensions"] = map[string]any{tasksExtensionKey: map[string]any{}}
	}
	return map[string]any{mcpsdk.MetaKeyProtocolVersion: revision, mcpsdk.MetaKeyClientCapabilities: caps}
}

type taskRPCReply struct {
	httpStatus int
	raw        []byte
	Result     json.RawMessage `json:"result"`
	Error      *struct {
		Code    int64  `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// rpc posts one JSON-RPC request as token and reads its one answer (a JSON
// body or one SSE message).
func (s *tasksStack) rpc(t *testing.T, token, method string, params map[string]any) taskRPCReply {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	header := bearerHeader(token)
	if id, ok := params["taskId"].(string); ok {
		header.Set("Mcp-Name", id)
	}
	if revision, _ := params["_meta"].(map[string]any)[mcpsdk.MetaKeyProtocolVersion].(string); revision != "" {
		header.Set("Mcp-Protocol-Version", revision)
	}
	resp := postMCP(t, s.e, http.MethodPost, body, header)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	reply := taskRPCReply{httpStatus: resp.StatusCode, raw: raw}
	payload := raw
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		payload = nil
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 1<<20), 1<<22)
		for scanner.Scan() {
			if line, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
				payload = []byte(line)
			}
		}
	}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &reply)
	}
	return reply
}

func investigateParams(args map[string]any, meta map[string]any) map[string]any {
	if args == nil {
		args = map[string]any{"question": "Is Ask Dev on track?"}
	}
	return map[string]any{"name": acrmcp.ToolInvestigateQuestionForTest, "arguments": args, "_meta": meta}
}

type taskWire struct {
	ResultType     string          `json:"resultType"`
	TaskID         string          `json:"taskId"`
	Status         string          `json:"status"`
	StatusMessage  string          `json:"statusMessage"`
	CreatedAt      string          `json:"createdAt"`
	LastUpdatedAt  string          `json:"lastUpdatedAt"`
	TTLMs          *int64          `json:"ttlMs"`
	PollIntervalMs int             `json:"pollIntervalMs"`
	Result         json.RawMessage `json:"result"`
	Error          json.RawMessage `json:"error"`
	InputRequests  json.RawMessage `json:"inputRequests"`
}

func decodeTask(t *testing.T, reply taskRPCReply) taskWire {
	t.Helper()
	if reply.Error != nil || len(reply.Result) == 0 {
		t.Fatalf("want a task, got http=%d %s", reply.httpStatus, reply.raw)
	}
	var task taskWire
	if err := json.Unmarshal(reply.Result, &task); err != nil {
		t.Fatal(err)
	}
	return task
}

// createTask answers a declared investigate_question call with a task.
func (s *tasksStack) createTask(t *testing.T, args map[string]any) taskWire {
	t.Helper()
	task := decodeTask(t, s.rpc(t, s.caller.token, "tools/call", investigateParams(args, tasksMeta("2026-07-28", true))))
	if task.ResultType != "task" || task.Status != "working" || task.TaskID == "" || task.PollIntervalMs != 3000 {
		t.Fatalf("create answered %+v, want a working task", task)
	}
	return task
}

func (s *tasksStack) getTask(t *testing.T, token, id string) taskRPCReply {
	t.Helper()
	return s.rpc(t, token, "tasks/get", map[string]any{"taskId": id, "_meta": tasksMeta("2026-07-28", true)})
}

func submissionOf(taskID string) string {
	parts := strings.Split(taskID, ".")
	return strings.Join(parts[:3], ".")
}

// toolResultJSON is the comparable part of a tool result.
func toolResultJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var result struct {
		Content           json.RawMessage `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	return string(encoded)
}

// syncAnswer is the synchronous answer to the same call: no extension.
func (s *tasksStack) syncAnswer(t *testing.T, args map[string]any) string {
	t.Helper()
	reply := s.rpc(t, s.caller.token, "tools/call", investigateParams(args, tasksMeta("2026-07-28", false)))
	if reply.Error != nil || len(reply.Result) == 0 {
		t.Fatalf("sync call answered %s", reply.raw)
	}
	return toolResultJSON(t, reply.Result)
}

func TestTasksCapabilityAbsentSyncBytesEqual(t *testing.T) {
	on, off := newTasksStack(t, true), newTasksStack(t, false)
	for name, meta := range map[string]map[string]any{"2026-07-28 without the extension": tasksMeta("2026-07-28", false)} {
		got := on.rpc(t, on.caller.token, "tools/call", investigateParams(nil, meta))
		want := off.rpc(t, off.caller.token, "tools/call", investigateParams(nil, meta))
		if toolResultJSON(t, got.Result) != toolResultJSON(t, want.Result) || got.httpStatus != want.httpStatus {
			t.Fatalf("%s: flag on answered\n%s\nflag off answered\n%s", name, got.raw, want.raw)
		}
		if on.fake.createCount() != 0 || on.fake.syncCount() != 1 {
			t.Fatalf("%s: %d accept-first creates, %d sync calls; want 0 and 1", name, on.fake.createCount(), on.fake.syncCount())
		}
	}
}

func TestTasksFlagOffIgnoresExtension(t *testing.T) {
	s := newTasksStack(t, false)
	reply := s.rpc(t, s.caller.token, "tools/call", investigateParams(nil, tasksMeta("2026-07-28", true)))
	if strings.Contains(string(reply.Result), `"resultType":"task"`) || s.fake.createCount() != 0 || s.fake.syncCount() != 1 {
		t.Fatalf("flag off answered %s (%d creates)", reply.raw, s.fake.createCount())
	}
}

func TestTasksOldProtocolNoTask(t *testing.T) {
	s := newTasksStack(t, true)
	reply := s.rpc(t, s.caller.token, "tools/call", investigateParams(nil, tasksMeta("2025-11-25", true)))
	if strings.Contains(string(reply.Result), `"resultType":"task"`) || s.fake.createCount() != 0 {
		t.Fatalf("an older revision with the extension answered %s (%d creates)", reply.raw, s.fake.createCount())
	}
}

func TestTasksClientSynthesisStaysSync(t *testing.T) {
	s := newTasksStack(t, true)
	s.rpc(t, s.caller.token, "tools/call", investigateParams(map[string]any{"question": "Is Ask Dev on track?", "synthesis": "client"}, tasksMeta("2026-07-28", true)))
	if s.fake.createCount() != 0 || s.fake.syncCount() != 1 {
		t.Fatalf("synthesis client: %d creates, %d sync calls; want 0 and 1", s.fake.createCount(), s.fake.syncCount())
	}
}

func TestTasksSuppliedInterpretationStaysSync(t *testing.T) {
	s := newTasksStack(t, true)
	params := map[string]any{"name": acrmcp.ToolInvestigateWithInterpretationForTest, "arguments": map[string]any{"question": "Is Ask Dev on track?"}, "_meta": tasksMeta("2026-07-28", true)}
	reply := s.rpc(t, s.caller.token, "tools/call", params)
	if strings.Contains(string(reply.Result), `"resultType":"task"`) || s.fake.createCount() != 0 {
		t.Fatalf("investigate_with_interpretation answered %s (%d creates)", reply.raw, s.fake.createCount())
	}
}

func TestTasksPollToCompletedEqualsSync(t *testing.T) {
	s := newTasksStack(t, true)
	args := map[string]any{"question": "Is Ask Dev on track?", "budget": map[string]any{"max_drivers": 3}, "include_full_result": true}
	task := s.createTask(t, args)
	if working := decodeTask(t, s.getTask(t, s.caller.token, task.TaskID)); working.Status != "working" || working.ResultType != "complete" {
		t.Fatalf("first poll %+v, want working", working)
	}
	s.fake.set(submissionOf(task.TaskID), contractsv1.ContextFabricSubmissionRunning, contractsv1.ContextFabricStageReadingFacts, nil)
	if working := decodeTask(t, s.getTask(t, s.caller.token, task.TaskID)); working.StatusMessage != "Reading the facts" {
		t.Fatalf("stage poll %+v, want the reading_facts line", working)
	}
	s.fake.set(submissionOf(task.TaskID), contractsv1.ContextFabricSubmissionDone, contractsv1.ContextFabricStageValidating,
		&contractsv1.ContextFabricSubmissionTerminal{Kind: contractsv1.ContextFabricSubmissionTerminalResult})
	done := decodeTask(t, s.getTask(t, s.caller.token, task.TaskID))
	if done.Status != "completed" || done.CreatedAt != task.CreatedAt || done.TTLMs != nil {
		t.Fatalf("final poll %+v, want completed with the create's createdAt and a null ttl", done)
	}
	if got, want := toolResultJSON(t, done.Result), s.syncAnswer(t, args); got != want {
		t.Fatalf("completed result differs from the synchronous answer\ngot  %s\nwant %s", got, want)
	}
}

func TestTasksFallbackOnlyOnStreamUnavailableCode(t *testing.T) {
	s := newTasksStack(t, true)
	s.fake.createMode = "stream_unavailable"
	reply := s.rpc(t, s.caller.token, "tools/call", investigateParams(nil, tasksMeta("2026-07-28", true)))
	if strings.Contains(string(reply.Result), `"resultType":"task"`) || s.fake.syncCount() != 1 {
		t.Fatalf("stream_unavailable: %s (%d sync calls), want the synchronous answer", reply.raw, s.fake.syncCount())
	}
	s.fake.createMode = "other406"
	reply = s.rpc(t, s.caller.token, "tools/call", investigateParams(nil, tasksMeta("2026-07-28", true)))
	if s.fake.syncCount() != 1 || !strings.Contains(string(reply.Result), `"isError":true`) {
		t.Fatalf("a 406 with another code: %s (%d sync calls), want its tool error and no fallback", reply.raw, s.fake.syncCount())
	}
}

func TestTasksTerminalMappings(t *testing.T) {
	s := newTasksStack(t, true)
	for _, tc := range []struct {
		name     string
		terminal contractsv1.ContextFabricSubmissionTerminal
		state    contractsv1.ContextFabricSubmissionState
		status   string
		isError  bool
	}{
		{"unsaved", contractsv1.ContextFabricSubmissionTerminal{Kind: contractsv1.ContextFabricSubmissionTerminalUnsaved}, contractsv1.ContextFabricSubmissionDoneUnsaved, "completed", true},
		{"run_lost", contractsv1.ContextFabricSubmissionTerminal{Kind: contractsv1.ContextFabricSubmissionTerminalError, ErrorCode: contractsv1.ErrorCodeRunLost}, contractsv1.ContextFabricSubmissionAbandoned, "completed", true},
		{"run error", contractsv1.ContextFabricSubmissionTerminal{Kind: contractsv1.ContextFabricSubmissionTerminalError, ErrorCode: "upstream_unavailable"}, contractsv1.ContextFabricSubmissionAbandoned, "completed", true},
		{"canceled", contractsv1.ContextFabricSubmissionTerminal{Kind: contractsv1.ContextFabricSubmissionTerminalError, ErrorCode: contractsv1.ErrorCodeCanceled}, contractsv1.ContextFabricSubmissionAbandoned, "cancelled", false},
	} {
		task := s.createTask(t, nil)
		terminal := tc.terminal
		s.fake.set(submissionOf(task.TaskID), tc.state, "", &terminal)
		got := decodeTask(t, s.getTask(t, s.caller.token, task.TaskID))
		if got.Status != tc.status || len(got.Error) != 0 || (tc.isError != strings.Contains(string(got.Result), `"isError":true`)) {
			t.Fatalf("%s: %+v (result %s), want %s isError=%v and no JSON-RPC error", tc.name, got, got.Result, tc.status, tc.isError)
		}
	}
}

func TestTasksFinalWithoutTerminalStillWorking(t *testing.T) {
	s := newTasksStack(t, true)
	task := s.createTask(t, nil)
	s.fake.set(submissionOf(task.TaskID), contractsv1.ContextFabricSubmissionDone, contractsv1.ContextFabricStageValidating, nil)
	if got := decodeTask(t, s.getTask(t, s.caller.token, task.TaskID)); got.Status != "working" {
		t.Fatalf("a final record whose log holds no end: %+v, want working", got)
	}
}

func TestTasksCancelForwards(t *testing.T) {
	s := newTasksStack(t, true)
	task := s.createTask(t, nil)
	reply := s.rpc(t, s.caller.token, "tasks/cancel", map[string]any{"taskId": task.TaskID, "_meta": tasksMeta("2026-07-28", true)})
	if reply.Error != nil || !strings.Contains(string(reply.Result), `"resultType":"complete"`) {
		t.Fatalf("cancel answered %s", reply.raw)
	}
	if got := decodeTask(t, s.getTask(t, s.caller.token, task.TaskID)); got.Status != "cancelled" {
		t.Fatalf("after cancel %+v, want cancelled", got)
	}
}

func retag(t *testing.T, taskID string, render map[string]any) string {
	t.Helper()
	parts := strings.Split(taskID, ".")
	encoded, err := json.Marshal(render)
	if err != nil {
		t.Fatal(err)
	}
	parts[3] = base64.RawURLEncoding.EncodeToString(encoded)
	return strings.Join(parts, ".")
}

func TestTasksNotFoundInvalidParams(t *testing.T) {
	s := newTasksStack(t, true)
	task := s.createTask(t, nil)
	other := s.hosted.issue(readScopes, []string{repoAnswers}, nil)
	parts := strings.Split(task.TaskID, ".")
	shifted := append([]string(nil), parts...)
	shifted[2] = "zzzzzz"
	for name, tc := range map[string]struct{ token, id string }{
		"unknown":               {s.caller.token, "mcpt.aaaaaaaaaaaaaaaaaaaaaaaaaa.t5x0kg.e30"},
		"another credential":    {other.token, task.TaskID},
		"tampered created time": {s.caller.token, strings.Join(shifted, ".")},
		"garbled":               {s.caller.token, task.TaskID + "!"},
		"not an id":             {s.caller.token, "task_1"},
	} {
		for _, method := range []string{"tasks/get", "tasks/cancel", "tasks/update"} {
			reply := s.rpc(t, tc.token, method, map[string]any{"taskId": tc.id, "_meta": tasksMeta("2026-07-28", true)})
			if reply.Error == nil || reply.Error.Code != -32602 {
				t.Fatalf("%s %s: %s, want -32602", name, method, reply.raw)
			}
		}
	}
}

func TestTasksMethodsNeedTheCapability(t *testing.T) {
	s := newTasksStack(t, true)
	task := s.createTask(t, nil)
	for _, method := range []string{"tasks/get", "tasks/cancel", "tasks/update"} {
		reply := s.rpc(t, s.caller.token, method, map[string]any{"taskId": task.TaskID, "_meta": tasksMeta("2026-07-28", false)})
		if reply.Error == nil || reply.Error.Code != -32021 {
			t.Fatalf("%s without the extension: %s, want -32021", method, reply.raw)
		}
	}
	for _, method := range []string{"tasks/list", "tasks/result"} {
		reply := s.rpc(t, s.caller.token, method, map[string]any{"taskId": task.TaskID, "_meta": tasksMeta("2026-07-28", true)})
		if reply.Error == nil || reply.Error.Code != -32601 {
			t.Fatalf("%s: %s, want -32601 (not in the 2026-07-28 extension)", method, reply.raw)
		}
	}
}

func TestTasksGetAuthenticatesEveryCall(t *testing.T) {
	s := newTasksStack(t, true)
	task := s.createTask(t, nil)
	s.hosted.revoke(s.caller)
	reply := s.getTask(t, s.caller.token, task.TaskID)
	if reply.httpStatus == http.StatusOK && reply.Error == nil {
		t.Fatalf("tasks/get with a revoked bearer answered %d %s", reply.httpStatus, reply.raw)
	}
}

func TestTasksCreateHeaders(t *testing.T) {
	s := newTasksStack(t, true)
	task := s.createTask(t, nil)
	s.fake.mu.Lock()
	header := s.fake.creates[0]
	s.fake.mu.Unlock()
	if header.Get(contractsv1.ContextFabricPreferHeader) != contractsv1.ContextFabricRespondAsyncPreference ||
		header.Get(contractsv1.ContextFabricSubmissionIDHeader) != submissionOf(task.TaskID) ||
		header.Get(contractsv1.ContextFabricWaitBudgetHeader) != "" {
		t.Fatalf("create headers %v, want Prefer, the submission id and no wait budget", header)
	}
}

func TestTasksNeverInputRequired(t *testing.T) {
	s := newTasksStack(t, true)
	task := s.createTask(t, nil)
	reply := s.rpc(t, s.caller.token, "tasks/update", map[string]any{"taskId": task.TaskID, "inputResponses": map[string]any{"k1": map[string]any{"action": "accept"}}, "_meta": tasksMeta("2026-07-28", true)})
	if reply.Error != nil || !strings.Contains(string(reply.Result), `"resultType":"complete"`) {
		t.Fatalf("update answered %s, want an empty ack", reply.raw)
	}
	for _, stage := range []contractsv1.ContextFabricInvestigationStage{"", contractsv1.ContextFabricStageInterpreting, contractsv1.ContextFabricStageSynthesizing} {
		s.fake.set(submissionOf(task.TaskID), contractsv1.ContextFabricSubmissionRunning, stage, nil)
		got := decodeTask(t, s.getTask(t, s.caller.token, task.TaskID))
		if got.Status == "input_required" || len(got.InputRequests) != 0 {
			t.Fatalf("stage %q answered %+v: input_required is never answered", stage, got)
		}
	}
}

func TestTasksDiscoverExtension(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		s := newTasksStack(t, enabled)
		reply := s.rpc(t, s.caller.token, "server/discover", map[string]any{"_meta": tasksMeta("2026-07-28", true)})
		if got := strings.Contains(string(reply.Result), tasksExtensionKey); got != enabled {
			t.Fatalf("enabled=%v: discover %s", enabled, reply.raw)
		}
	}
}

// The render arguments in a task id are untrusted: tasks/get answers what a
// fresh synchronous call with the same arguments answers.
func TestTasksTamperedRenderArgumentsAreTheSyncAnswer(t *testing.T) {
	s := newTasksStack(t, true)
	task := s.createTask(t, nil)
	s.fake.set(submissionOf(task.TaskID), contractsv1.ContextFabricSubmissionDone, "", &contractsv1.ContextFabricSubmissionTerminal{Kind: contractsv1.ContextFabricSubmissionTerminalResult})
	for name, tc := range map[string]struct {
		render map[string]any
		args   map[string]any
	}{
		"budget above the live bound": {map[string]any{"b": map[string]any{"max_serialized_bytes": 1048576, "max_drivers": 50}}, map[string]any{"question": "Is Ask Dev on track?", "budget": map[string]any{"max_serialized_bytes": 1048576, "max_drivers": 50}}},
		"budget out of range":         {map[string]any{"b": map[string]any{"max_serialized_bytes": 4096}}, map[string]any{"question": "Is Ask Dev on track?", "budget": map[string]any{"max_serialized_bytes": 4096}}},
		"include_full_result":         {map[string]any{"f": true}, map[string]any{"question": "Is Ask Dev on track?", "include_full_result": true}},
	} {
		got := decodeTask(t, s.getTask(t, s.caller.token, retag(t, task.TaskID, tc.render)))
		if got.Status != "completed" {
			t.Fatalf("%s: %+v, want completed", name, got)
		}
		if answer, want := toolResultJSON(t, got.Result), s.syncAnswer(t, tc.args); answer != want {
			t.Fatalf("%s: tasks/get answered\n%s\nthe synchronous call answers\n%s", name, answer, want)
		}
	}
}
