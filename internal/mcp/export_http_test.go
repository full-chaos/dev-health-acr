package mcp

import "net/http"

// This file exposes package internals to the external mcp_test package, whose
// tests import the eventspec certifier (eventspec imports this package, so an
// in-package test cannot).

// OurSchemaVersionsForTest is the schema set the compatibility gate requires.
var OurSchemaVersionsForTest = ourSchemaVersions

// Tool names, for fixtures that build a hosted capability answer.
const (
	ToolContextForTaskForTest      = toolContextForTask
	ToolSourceEvidenceForTest      = toolSourceEvidence
	ToolInvestigateQuestionForTest = toolInvestigateQuestion
	ToolInvestigationResultForTest = toolInvestigationResult
)

// WrapHTTPHandlerSDKForTest replaces the SDK handler the endpoint delegates
// to with wrap(the SDK handler), so a test can observe whether a request ever
// reached it.
func WrapHTTPHandlerSDKForTest(h *HTTPHandler, wrap func(http.Handler) http.Handler) {
	h.mcp = wrap(h.mcp)
}
