package mcp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

// This file exposes package internals to the external mcp_test package, whose
// tests drive the hosted transport only through its exported surface.

// OurSchemaVersionsForTest is the schema set the compatibility gate requires.
var OurSchemaVersionsForTest = ourSchemaVersions

// Tool names, for fixtures that build a hosted capability answer.
const (
	ToolContextForTaskForTest                = toolContextForTask
	ToolSourceEvidenceForTest                = toolSourceEvidence
	ToolInvestigateQuestionForTest           = toolInvestigateQuestion
	ToolInvestigationResultForTest           = toolInvestigationResult
	ToolInvestigateWithInterpretationForTest = toolInvestigateWithInterpretation
)

// HostedRepositoryRequiredMessageForTest is the refusal a hosted
// context_for_task call without a repository answers.
const HostedRepositoryRequiredMessageForTest = hostedRepositoryRequiredMessage

// WrapHTTPHandlerSDKForTest replaces the SDK handler the endpoint delegates
// to with wrap(the SDK handler), so a test can observe whether a request ever
// reached it.
func WrapHTTPHandlerSDKForTest(h *HTTPHandler, wrap func(http.Handler) http.Handler) {
	h.mcp = wrap(h.mcp)
}

// ServeHTTPOnForTest runs the serve command's own path (the process
// configuration ServeHTTPTransport builds, then serveHTTPOn) on an
// already-bound listener, so a test learns the port.
func ServeHTTPOnForTest(ctx context.Context, listener net.Listener, sidecarCfg sidecar.Config, identity version.Info, diagnostics io.Writer, opts ServeOptions) error {
	cfg, err := newServeProcessConfig(sidecarCfg, identity, diagnostics, opts)
	if err != nil {
		_ = listener.Close()
		return err
	}
	return serveHTTPOn(ctx, listener, cfg, identity, opts)
}

// EnableTasksForTest turns MCP Tasks on for a hosted process configuration,
// as ACR_MCP_TASKS_ENABLED=true does through the serve options.
func EnableTasksForTest(cfg *ProcessConfig) { cfg.tasksEnabled = true }

// RenderTagForTest encodes render arguments as a task id's render tag.
func RenderTagForTest(budget *contractsv1.MCPInvestigationBudget, includeFullResult bool) string {
	tag, err := encodeRenderTag(taskRender{Budget: budget, IncludeFullResult: includeFullResult})
	if err != nil {
		panic(err)
	}
	return tag
}

// FailTaskIDEncodingForTest makes every task id encoding fail until the
// returned restore runs.
func FailTaskIDEncodingForTest() (restore func()) {
	taskIDEncodeFault = func() error { return errors.New("task id encoding failed") }
	return func() { taskIDEncodeFault = nil }
}
