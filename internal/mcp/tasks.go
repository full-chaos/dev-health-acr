package mcp

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP Tasks (ext-tasks 2026-07-28) for investigate_question on the hosted
// transport. acr-api owns every task: a task is an accept-first investigation
// submission there, keyed by the caller's org and owner. acr-mcp keeps no
// task state; every tasks/* request resolves its caller from its own bearer
// and reads acr-api.

const (
	methodTasksGet    = "tasks/get"
	methodTasksUpdate = "tasks/update"
	methodTasksCancel = "tasks/cancel"

	// tasksPollIntervalMs is acr-api's submission retry cadence.
	tasksPollIntervalMs = 3000

	// codeMissingRequiredClientCapability is the protocol error of a tasks/*
	// request from a client that did not declare the extension on it.
	codeMissingRequiredClientCapability = -32021

	taskStatusWorking   = "working"
	taskStatusCompleted = "completed"
	taskStatusFailed    = "failed"
	taskStatusCancelled = "cancelled"

	taskIDPrefix   = "mcpt"
	taskIDMaxBytes = 128
)

// taskResult is CreateTaskResult (resultType "task") and GetTaskResult
// (resultType "complete"): the Task fields plus, on a terminal state, the
// result or the JSON-RPC error. ttlMs is always null: acr-api keeps a
// submission to its own expiry and answers an expired one as unknown.
type taskResult struct {
	mcpsdk.ResultBase
	ResultType     string                 `json:"resultType"`
	TaskID         string                 `json:"taskId"`
	Status         string                 `json:"status"`
	StatusMessage  string                 `json:"statusMessage,omitempty"`
	CreatedAt      string                 `json:"createdAt"`
	LastUpdatedAt  string                 `json:"lastUpdatedAt"`
	TTLMs          *int64                 `json:"ttlMs"`
	PollIntervalMs int                    `json:"pollIntervalMs"`
	Result         *mcpsdk.CallToolResult `json:"result,omitempty"`
	Error          *jsonrpc.Error         `json:"error,omitempty"`
}

// taskAck is the empty acknowledgement of tasks/update and tasks/cancel.
type taskAck struct {
	mcpsdk.ResultBase
	ResultType string `json:"resultType"`
}

type taskIDParams struct {
	mcpsdk.ParamsBase
	TaskID string `json:"taskId"`
}

type taskUpdateParams struct {
	mcpsdk.ParamsBase
	TaskID         string          `json:"taskId"`
	InputResponses json.RawMessage `json:"inputResponses,omitempty"`
}

// taskRender is the part of a tool call's arguments a stored result needs to
// render the answer the synchronous call gives. It rides in the taskId and is
// UNTRUSTED on every tasks/get: it is validated and clamped to the caller's
// live limits exactly as the same arguments on a fresh tools/call are.
type taskRender struct {
	Budget            *contractsv1.MCPInvestigationBudget
	IncludeFullResult bool
}

// The render tag is one flag byte, then one canonical uvarint per flagged
// budget field in this order, base64url without padding. A zero budget field
// is "not supplied" (as on a tools/call) and is never encoded, so every
// valid argument set has exactly one tag, at most 1+4*5 bytes.
const (
	renderFlagDrivers    byte = 1 << 0
	renderFlagCohort     byte = 1 << 1
	renderFlagEvidence   byte = 1 << 2
	renderFlagBytes      byte = 1 << 3
	renderFlagFullResult byte = 1 << 4
	renderFlagsKnown          = renderFlagDrivers | renderFlagCohort | renderFlagEvidence | renderFlagBytes | renderFlagFullResult
	renderFieldMax            = 1<<31 - 1
	renderTagMaxBytes         = 1 + 4*binary.MaxVarintLen32
)

// renderFields pairs each budget flag with its field, in tag order.
func renderFields(b *contractsv1.MCPInvestigationBudget) []struct {
	flag  byte
	value *int
} {
	return []struct {
		flag  byte
		value *int
	}{
		{renderFlagDrivers, &b.MaxDrivers}, {renderFlagCohort, &b.MaxCohortMembers},
		{renderFlagEvidence, &b.MaxEvidenceRefs}, {renderFlagBytes, &b.MaxSerializedBytes},
	}
}

// encodeRenderTag encodes the render arguments. A negative or over-range
// field (the tool's validation refuses both before a task is made) is an
// error, never a silent clamp.
func encodeRenderTag(render taskRender) (string, error) {
	var flags byte
	var body []byte
	if render.Budget != nil {
		budget := *render.Budget
		for _, field := range renderFields(&budget) {
			if *field.value < 0 || *field.value > renderFieldMax {
				return "", errors.New("render argument out of range")
			}
			if *field.value != 0 {
				flags |= field.flag
				body = binary.AppendUvarint(body, uint64(*field.value))
			}
		}
	}
	if render.IncludeFullResult {
		flags |= renderFlagFullResult
	}
	return base64.RawURLEncoding.EncodeToString(append([]byte{flags}, body...)), nil
}

// decodeRenderTag reads a render tag. Anything but the one canonical
// encoding of a valid argument set is false: no flag byte, an unknown flag
// bit, a short, non-minimal or zero uvarint, a field over range, or trailing
// bytes.
func decodeRenderTag(tag string) (taskRender, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(tag)
	if err != nil || len(raw) == 0 || len(raw) > renderTagMaxBytes || base64.RawURLEncoding.EncodeToString(raw) != tag {
		return taskRender{}, false
	}
	flags, rest := raw[0], raw[1:]
	if flags&^renderFlagsKnown != 0 {
		return taskRender{}, false
	}
	var budget contractsv1.MCPInvestigationBudget
	present := false
	for _, field := range renderFields(&budget) {
		if flags&field.flag == 0 {
			continue
		}
		value, n := binary.Uvarint(rest)
		if n <= 0 || value == 0 || value > renderFieldMax || len(binary.AppendUvarint(nil, value)) != n {
			return taskRender{}, false
		}
		*field.value = int(value)
		rest = rest[n:]
		present = true
	}
	if len(rest) != 0 {
		return taskRender{}, false
	}
	render := taskRender{IncludeFullResult: flags&renderFlagFullResult != 0}
	if present {
		render.Budget = &budget
	}
	return render, true
}

// taskID is a task's identifier: the acr-api submission id (a random part
// and the admission time, both inside acr-api's key, so a changed time is an
// unknown task) and the render arguments after it.
type taskID struct {
	submissionID string
	createdAt    time.Time
	render       taskRender
}

var taskIDRandom = base32.StdEncoding.WithPadding(base32.NoPadding)

// taskIDEncodeFault, when set (tests only), fails the task id encoding.
var taskIDEncodeFault func() error

// mintTaskID mints a task's identity locally: 128 random bits and the
// admission second make the acr-api submission id.
func mintTaskID(now time.Time, render taskRender) (taskID, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return taskID{}, err
	}
	created := now.UTC().Truncate(time.Second)
	submission := taskIDPrefix + "." + strings.ToLower(taskIDRandom.EncodeToString(random)) + "." + strconv.FormatInt(created.Unix(), 36)
	return taskID{submissionID: submission, createdAt: created, render: render}, nil
}

// encode is the full task id: the submission id and the render tag.
func (id taskID) encode() (string, error) {
	if taskIDEncodeFault != nil {
		if err := taskIDEncodeFault(); err != nil {
			return "", err
		}
	}
	tag, err := encodeRenderTag(id.render)
	if err != nil {
		return "", err
	}
	raw := id.submissionID + "." + tag
	if len(raw) > taskIDMaxBytes || !contractsv1.ValidContextFabricClientCorrelationID(id.submissionID) {
		return "", errors.New("task id over its bounds")
	}
	return raw, nil
}

// newTaskID mints and encodes a task id.
func newTaskID(now time.Time, render taskRender) (taskID, string, error) {
	id, err := mintTaskID(now, render)
	if err != nil {
		return taskID{}, "", err
	}
	raw, err := id.encode()
	if err != nil {
		return taskID{}, "", err
	}
	return id, raw, nil
}

// parseTaskID reads a task id. Any part out of its form is false: the
// caller answers it as an unknown task.
func parseTaskID(raw string) (taskID, bool) {
	if len(raw) > taskIDMaxBytes {
		return taskID{}, false
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 4 || parts[0] != taskIDPrefix || len(parts[1]) != 26 || parts[1] != strings.ToLower(parts[1]) {
		return taskID{}, false
	}
	if _, err := taskIDRandom.DecodeString(strings.ToUpper(parts[1])); err != nil {
		return taskID{}, false
	}
	seconds, err := strconv.ParseInt(parts[2], 36, 64)
	if err != nil || seconds <= 0 || strconv.FormatInt(seconds, 36) != parts[2] {
		return taskID{}, false
	}
	render, ok := decodeRenderTag(parts[3])
	if !ok {
		return taskID{}, false
	}
	submission := strings.Join(parts[:3], ".")
	if !contractsv1.ValidContextFabricClientCorrelationID(submission) {
		return taskID{}, false
	}
	return taskID{submissionID: submission, createdAt: time.Unix(seconds, 0).UTC(), render: render}, true
}

func taskTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func taskProtocolError(code int64, message string) *jsonrpc.Error {
	return &jsonrpc.Error{Code: code, Message: message}
}

var (
	errTaskUnknown           = taskProtocolError(jsonrpc.CodeInvalidParams, "Failed to retrieve task: Task not found")
	errTaskCapabilityMissing = taskProtocolError(codeMissingRequiredClientCapability, "Missing required client capability: "+tasksExtension)
	errTaskInternal          = taskProtocolError(jsonrpc.CodeInternalError, "The task could not be read")
)

// The outcomes of a declared investigate_question call (the closed
// vocabulary of eventspec.MCPTaskOutcome).
const (
	taskOutcomeTask                      = "task"
	taskOutcomeFallbackStreamUnavailable = "fallback_stream_unavailable"
	taskOutcomeSyncIneligible            = "sync_ineligible"
	taskOutcomeCreateRefused             = "create_refused"
	taskOutcomeTaskIDFailed              = "task_id_failed"
)

// logTaskOutcome writes the one Info line of a declared call: the tool and
// what became of it. No argument, id or subject value is on it.
func logTaskOutcome(ctx context.Context, cfg *ProcessConfig, outcome string) {
	if cfg == nil || cfg.diagnostics == nil {
		return
	}
	fields := eventspec.NewMCPTaskOutcomeFields(toolInvestigateQuestion, outcome)
	cfg.diagnostics.InfoContext(ctx, eventspec.MCPTaskOutcome.Msg, fields.SlogArgs()...)
}

// tasksMiddleware answers an investigate_question tools/call with a task
// when THIS request declares the tasks extension (clientDeclaresTasks) and
// the call can be rendered from its stored result. Every other request, and
// every refusal before the accept-first create, runs the normal handler
// unchanged. It is registered innermost, so the caller is resolved and the
// unlisted-tool and integer-argument checks already ran.
func tasksMiddleware(cfg *ProcessConfig) mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			call, ok := req.(*mcpsdk.CallToolRequest)
			if !ok || call.Params == nil || call.Params.Name != toolInvestigateQuestion || !clientDeclaresTasks(call.Params) {
				return next(ctx, method, req)
			}
			caller, err := CallerFromContext(ctx)
			if err != nil || !hostedToolEnabled(caller, toolInvestigateQuestion) {
				logTaskOutcome(ctx, cfg, taskOutcomeSyncIneligible)
				return next(ctx, method, req)
			}
			input, eligible := taskEligibleInput(call)
			if !eligible {
				logTaskOutcome(ctx, cfg, taskOutcomeSyncIneligible)
				return next(ctx, method, req)
			}
			// The whole task id is built before acr-api is called: a failure
			// here creates no submission, and an eligible call never falls
			// back to the synchronous answer for it.
			id, err := mintTaskID(time.Now(), taskRender{Budget: input.Budget, IncludeFullResult: input.IncludeFullResult})
			if err != nil {
				logTaskOutcome(ctx, cfg, taskOutcomeTaskIDFailed)
				return nil, errTaskInternal
			}
			raw, err := id.encode()
			if err != nil {
				logTaskOutcome(ctx, cfg, taskOutcomeTaskIDFailed)
				return nil, errTaskInternal
			}
			status, err := caller.client.InvestigateAsync(ctx, investigationHostedRequest(caller, input), id.submissionID)
			var apiErr *sidecar.APIError
			if errors.As(err, &apiErr) && apiErr.Code == contractsv1.ErrorCodeStreamUnavailable {
				// The deployment runs no accept-first investigations: the
				// synchronous call answers. Only this code falls back.
				logTaskOutcome(ctx, cfg, taskOutcomeFallbackStreamUnavailable)
				return next(ctx, method, req)
			}
			if err != nil {
				logTaskOutcome(ctx, cfg, taskOutcomeCreateRefused)
				return writeBackAwareErrorResult(err), nil
			}
			logTaskOutcome(ctx, cfg, taskOutcomeTask)
			return &taskResult{
				ResultType: "task", TaskID: raw, Status: taskStatusWorking,
				CreatedAt: taskTime(id.createdAt), LastUpdatedAt: taskTime(status.UpdatedAt), PollIntervalMs: tasksPollIntervalMs,
			}, nil
		}
	}
}

// taskEligibleInput decodes and validates the call's arguments the way the
// synchronous handler does. A call that handler would refuse, a write-back
// call, and a synthesis "client" turn (its synthesis_input is only in the
// create's own answer) are not task-eligible: they run synchronously.
func taskEligibleInput(call *mcpsdk.CallToolRequest) (contractsv1.MCPInvestigateQuestionRequest, bool) {
	var input contractsv1.MCPInvestigateQuestionRequest
	args, _, err := expandBareReceiptIDs(rawArgs(call), toolInvestigateQuestion)
	if err != nil || contractsv1.MCPArgumentsCarrySynthesisWriteBack(args) {
		return input, false
	}
	if decodeToolArguments(args, &input, false) != nil || input.Validate() != nil {
		return input, false
	}
	if input.Synthesis == contractsv1.ContextFabricSynthesisModeClient {
		return input, false
	}
	return input, true
}

// registerTaskMethods serves tasks/get, tasks/update and tasks/cancel. The
// 2026-07-28 extension defines no other tasks/* method (there is no
// tasks/list and no tasks/result), so none other is registered.
func registerTaskMethods(server *mcpsdk.Server, cfg *ProcessConfig) error {
	if err := mcpsdk.AddReceivingCustomMethod(server, methodTasksGet, func(ctx context.Context, _ *mcpsdk.ServerSession, params *taskIDParams) (*taskResult, error) {
		return getTask(ctx, cfg, params)
	}); err != nil {
		return err
	}
	if err := mcpsdk.AddReceivingCustomMethod(server, methodTasksUpdate, func(ctx context.Context, _ *mcpsdk.ServerSession, params *taskUpdateParams) (*taskAck, error) {
		if params == nil || !clientDeclaresTasks(params) {
			return nil, errTaskCapabilityMissing
		}
		caller, id, err := taskCaller(ctx, params.TaskID)
		if err != nil {
			return nil, err
		}
		// No input request is ever outstanding: every inputResponses key is
		// ignored. The task must exist.
		if _, err := caller.client.SubmissionStatus(ctx, id.submissionID); err != nil {
			return nil, taskReadError(err)
		}
		return &taskAck{ResultType: "complete"}, nil
	}); err != nil {
		return err
	}
	return mcpsdk.AddReceivingCustomMethod(server, methodTasksCancel, func(ctx context.Context, _ *mcpsdk.ServerSession, params *taskIDParams) (*taskAck, error) {
		if params == nil || !clientDeclaresTasks(params) {
			return nil, errTaskCapabilityMissing
		}
		caller, id, err := taskCaller(ctx, params.TaskID)
		if err != nil {
			return nil, err
		}
		if err := caller.client.CancelSubmission(ctx, id.submissionID); err != nil {
			var apiErr *sidecar.APIError
			if errors.As(err, &apiErr) && apiErr.Code == contractsv1.ErrorCodeSubmissionInProgress {
				// Admitted without a lease: nothing can stop it. Cancel is
				// cooperative, so the request is still acknowledged.
				return &taskAck{ResultType: "complete"}, nil
			}
			return nil, taskReadError(err)
		}
		return &taskAck{ResultType: "complete"}, nil
	})
}

// taskCaller resolves the caller of THIS request and parses the task id.
func taskCaller(ctx context.Context, raw string) (*CallerContext, taskID, error) {
	id, ok := parseTaskID(raw)
	if !ok {
		return nil, taskID{}, errTaskUnknown
	}
	caller, err := CallerFromContext(ctx)
	if err != nil || caller.client == nil {
		return nil, taskID{}, errTaskInternal
	}
	return caller, id, nil
}

// taskReadError maps an acr-api read failure: not found (another org's or
// another owner's id, an expired one) is the unknown task; anything else is
// an internal error.
func taskReadError(err error) error {
	var apiErr *sidecar.APIError
	if errors.As(err, &apiErr) && apiErr.HTTPStatus == http.StatusNotFound {
		return errTaskUnknown
	}
	return errTaskInternal
}

// taskStageMessages are the fixed progress lines of a working task, one per
// stage of acr-api's closed vocabulary; no hosted text reaches the client.
var taskStageMessages = map[contractsv1.ContextFabricInvestigationStage]string{
	contractsv1.ContextFabricStageInterpreting:      "Interpreting the question",
	contractsv1.ContextFabricStageResolvingSubjects: "Resolving the subjects",
	contractsv1.ContextFabricStageReadingFacts:      "Reading the facts",
	contractsv1.ContextFabricStageSynthesizing:      "Writing the answer",
	contractsv1.ContextFabricStageValidating:        "Checking the answer",
}

const (
	taskUnsavedMessage   = "the answer was not stored, so it cannot be delivered; ask the question again"
	taskCancelledMessage = "The investigation was canceled"
)

func getTask(ctx context.Context, cfg *ProcessConfig, params *taskIDParams) (*taskResult, error) {
	if params == nil || !clientDeclaresTasks(params) {
		return nil, errTaskCapabilityMissing
	}
	caller, id, err := taskCaller(ctx, params.TaskID)
	if err != nil {
		return nil, err
	}
	status, err := caller.client.SubmissionStatus(ctx, id.submissionID)
	if err != nil {
		return nil, taskReadError(err)
	}
	task := &taskResult{
		ResultType: "complete", TaskID: params.TaskID, Status: taskStatusWorking,
		CreatedAt: taskTime(id.createdAt), LastUpdatedAt: taskTime(status.UpdatedAt), PollIntervalMs: tasksPollIntervalMs,
	}
	// A final record whose run log holds no end yet is still working: the
	// delivery can end in an error after the record is final.
	if status.State == contractsv1.ContextFabricSubmissionRunning || status.Terminal == nil {
		task.StatusMessage = taskStageMessages[status.LastStage]
		return task, nil
	}
	switch status.Terminal.Kind {
	case contractsv1.ContextFabricSubmissionTerminalResult:
		task.Status, task.Result = taskStatusCompleted, completedTaskResult(ctx, cfg, caller, id, status)
	case contractsv1.ContextFabricSubmissionTerminalUnsaved:
		task.Status, task.Result = taskStatusCompleted, toolErrorResult(&classifiedError{category: "unavailable", message: taskUnsavedMessage})
	default:
		if status.Terminal.ErrorCode == contractsv1.ErrorCodeCanceled {
			task.Status, task.StatusMessage = taskStatusCancelled, taskCancelledMessage
			return task, nil
		}
		// The run ended with an error.v1 envelope: the synchronous call
		// would have answered that hosted error as a tool error.
		task.Status, task.Result = taskStatusCompleted, toolErrorResult(&sidecar.APIError{
			Code: status.Terminal.ErrorCode, HTTPStatus: terminalErrorStatus(status.Terminal.ErrorCode), Message: terminalErrorMessage(status.Terminal.ErrorCode),
		})
	}
	return task, nil
}

// completedTaskResult renders the stored result with the render arguments of
// the id, validated and clamped against THIS caller's live limits as a fresh
// call with those arguments is. A refusal is the tool error the synchronous
// call gives for the same arguments.
func completedTaskResult(ctx context.Context, cfg *ProcessConfig, caller *CallerContext, id taskID, status contractsv1.ContextFabricSubmissionStatus) *mcpsdk.CallToolResult {
	input := contractsv1.MCPInvestigateQuestionRequest{Budget: id.render.Budget, IncludeFullResult: id.render.IncludeFullResult}
	if input.Budget != nil && input.Budget.Validate() != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "investigate_question arguments failed schema validation"})
	}
	result, err := caller.client.InvestigationResult(ctx, status.ResultID)
	if err != nil {
		return writeBackAwareErrorResult(err)
	}
	rendered, err := renderInvestigationAnswer(ctx, cfg, caller, toolInvestigateQuestion, input, result, nil, id.submissionID)
	if err != nil {
		return toolErrorResult(&classifiedError{category: "internal", message: "the assembled response failed contract validation"})
	}
	return rendered
}

func terminalErrorStatus(code string) int {
	switch code {
	case contractsv1.ErrorCodeRunLost:
		return contractsv1.ContextFabricRunLostStatus
	case "budget_refusal":
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusServiceUnavailable
}

func terminalErrorMessage(code string) string {
	if code == contractsv1.ErrorCodeRunLost {
		return contractsv1.ContextFabricRunLostMessage
	}
	return "the investigation ended without an answer (" + code + ")"
}
