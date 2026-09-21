package mcp

import "net/http"

// This file exposes package internals to the external mcp_test package, whose
// tests drive the hosted transport only through its exported surface.

// OurSchemaVersionsForTest is the schema set the compatibility gate requires.
var OurSchemaVersionsForTest = ourSchemaVersions

// Tool names, for fixtures that build a hosted capability answer.
const (
	ToolContextForTaskForTest      = toolContextForTask
	ToolSourceEvidenceForTest      = toolSourceEvidence
	ToolInvestigateQuestionForTest = toolInvestigateQuestion
	ToolInvestigationResultForTest = toolInvestigationResult
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
