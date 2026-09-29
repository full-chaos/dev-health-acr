package eventspec

import (
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The hosted MCP transport's three Info lines and their closed vocabularies.
// internal/mcp emits them and refers to every value below by name (its own
// exported names are aliases of these), so the producer and the declaration
// cannot hold two lists.

// Transport names.
const (
	MCPTransportSTDIO = "stdio"
	MCPTransportHTTP  = "http"
)

// MCPTransportVocabulary lists every transport acr-mcp serves.
func MCPTransportVocabulary() []string { return []string{MCPTransportSTDIO, MCPTransportHTTP} }

// Auth outcomes of one hosted HTTP request: how the bearer it carried was
// decided before any MCP handler ran.
const (
	MCPHTTPAuthAdmitted                = "admitted"
	MCPHTTPAuthMissingBearer           = "missing_bearer"
	MCPHTTPAuthMalformedBearer         = "malformed_bearer"
	MCPHTTPAuthInvalidCredential       = "invalid_credential"
	MCPHTTPAuthInsufficientScope       = "insufficient_scope"
	MCPHTTPAuthInsufficientEntitlement = "insufficient_entitlement"
	MCPHTTPAuthRateLimited             = "rate_limited"
	MCPHTTPAuthUpstreamIncompatible    = "upstream_incompatible"
	MCPHTTPAuthUpstreamUnavailable     = "upstream_unavailable"
)

// MCPHTTPAuthOutcomeVocabulary lists every auth outcome, admitted first.
func MCPHTTPAuthOutcomeVocabulary() []string {
	return []string{
		MCPHTTPAuthAdmitted, MCPHTTPAuthMissingBearer, MCPHTTPAuthMalformedBearer, MCPHTTPAuthInvalidCredential,
		MCPHTTPAuthInsufficientScope, MCPHTTPAuthInsufficientEntitlement, MCPHTTPAuthRateLimited,
		MCPHTTPAuthUpstreamIncompatible, MCPHTTPAuthUpstreamUnavailable,
	}
}

// Result classes of one hosted HTTP request.
const (
	// MCPHTTPResultOK: the MCP exchange completed and no method it carried
	// answered an error.
	MCPHTTPResultOK = "ok"
	// MCPHTTPResultToolError: a tools/call ran and answered a tool error.
	MCPHTTPResultToolError = "tool_error"
	// MCPHTTPResultProtocolError: a method answered a JSON-RPC error.
	MCPHTTPResultProtocolError = "protocol_error"
	// MCPHTTPResultAuthDenied: the credential was refused; no MCP handler ran.
	MCPHTTPResultAuthDenied = "auth_denied"
	// MCPHTTPResultAuthUnavailable: the credential could not be decided
	// because the hosted API was unreachable or incompatible; no MCP handler
	// ran.
	MCPHTTPResultAuthUnavailable = "auth_unavailable"
	// MCPHTTPResultTransportRejected: the caller was admitted but the
	// transport refused the HTTP request itself (method, media type, body
	// size, protocol version, or an undispatchable method) before any MCP
	// method ran.
	MCPHTTPResultTransportRejected = "transport_rejected"
)

// MCPHTTPResultClassVocabulary lists every result class.
func MCPHTTPResultClassVocabulary() []string {
	return []string{
		MCPHTTPResultOK, MCPHTTPResultToolError, MCPHTTPResultProtocolError, MCPHTTPResultAuthDenied,
		MCPHTTPResultAuthUnavailable, MCPHTTPResultTransportRejected,
	}
}

// Principal classes. A request's caller is never logged; "bearer" lines
// carry principal_ref, an opaque per-process keyed digest of the bearer.
const (
	MCPPrincipalClassNone   = "none"
	MCPPrincipalClassBearer = "bearer"
)

// MCPPrincipalClassVocabulary lists every principal class.
func MCPPrincipalClassVocabulary() []string {
	return []string{MCPPrincipalClassNone, MCPPrincipalClassBearer}
}

// Buckets for untrusted request values: "none"/"unspecified" record that
// nothing was sent, "other" that the value is not in the closed list.
const (
	MCPHTTPValueNone        = "none"
	MCPHTTPValueOther       = "other"
	MCPHTTPValueUnspecified = "unspecified"
)

// MCPHTTPMethodVocabulary lists every MCP method name the request line
// records.
func MCPHTTPMethodVocabulary() []string {
	return []string{
		MCPHTTPValueNone, "server/discover", "initialize", "notifications/initialized", "ping",
		"tools/list", "tools/call", "prompts/list", "prompts/get", "resources/list",
		"resources/templates/list", "resources/read", "completion/complete", "logging/setLevel",
		"subscriptions/listen", "notifications/cancelled", MCPHTTPValueOther,
	}
}

// MCPHTTPToolVocabulary lists every tool name the request line records.
// internal/mcp pins that every tool it registers is a member.
func MCPHTTPToolVocabulary() []string {
	return []string{
		MCPHTTPValueNone, "context_for_task", "source_evidence", "investigate_question",
		"investigation_result", "read_facts", "record_episode", MCPHTTPValueOther,
	}
}

// MCPHTTPProtocolRevisionVocabulary lists every protocol revision value the
// request line records: every revision the linked SDK speaks, plus
// "unspecified" (the request named none) and "other" (one the SDK does not
// speak).
func MCPHTTPProtocolRevisionVocabulary() []string {
	return append(append([]string{MCPHTTPValueUnspecified}, mcpsdk.SupportedProtocolVersions()...), MCPHTTPValueOther)
}

// Readiness states and failure classes of the hosted liveness probe.
const (
	MCPReadinessReady    = "ready"
	MCPReadinessNotReady = "not_ready"

	MCPReadinessFailureNone        = "none"
	MCPReadinessFailureUnreachable = "unreachable"
	MCPReadinessFailureTimeout     = "timeout"
	MCPReadinessFailureNotLive     = "not_live"
)

// MCPReadinessStateVocabulary lists every readiness state.
func MCPReadinessStateVocabulary() []string {
	return []string{MCPReadinessReady, MCPReadinessNotReady}
}

// MCPReadinessFailureVocabulary lists every readiness failure class.
func MCPReadinessFailureVocabulary() []string {
	return []string{MCPReadinessFailureNone, MCPReadinessFailureUnreachable, MCPReadinessFailureTimeout, MCPReadinessFailureNotLive}
}

// Log messages of the three lines.
const (
	MCPHTTPRequestLogMessage   = "acr-mcp http request"
	MCPHTTPServingLogMessage   = "acr-mcp http serving"
	MCPHTTPReadinessLogMessage = "acr-mcp http readiness"
)

// MCPHTTPRequest is the one line every request to the hosted MCP endpoint
// produces, on every path: refused before the SDK handler (auth_outcome other
// than admitted), rejected by the transport, or served. It carries no
// credential, no caller identity, no request body and no tool arguments;
// method, tool and protocol_revision are bucketed onto closed lists.
var MCPHTTPRequest = Event{
	ID:                 "mcp.http_request",
	Msg:                MCPHTTPRequestLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per HTTP request on the MCP base path, written after the response; probe routes emit none",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "transport", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: MCPTransportVocabulary()},
		{Key: "server_version", Type: FieldString, Presence: PresenceRequired},
		{Key: "server_commit", Type: FieldString, Presence: PresenceRequired},
		{Key: "protocol_revision", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: MCPHTTPProtocolRevisionVocabulary()},
		{Key: "method", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: MCPHTTPMethodVocabulary()},
		{Key: "tool", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: MCPHTTPToolVocabulary()},
		{Key: "principal_class", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: MCPPrincipalClassVocabulary()},
		{Key: "principal_ref", Type: FieldString, Presence: PresenceConditional, Applicability: "written when principal_class=bearer; an opaque per-process keyed digest, never the credential or its store hash"},
		{Key: "auth_outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: MCPHTTPAuthOutcomeVocabulary()},
		{Key: "result_class", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: MCPHTTPResultClassVocabulary()},
		{Key: "status", Type: FieldInt, Presence: PresenceRequired},
		{Key: "latency_ms", Type: FieldInt, Presence: PresenceRequired},
		{Key: "in_flight", Type: FieldInt, Presence: PresenceRequired},
	},
}

// MCPHTTPServing is the one line the hosted transport writes when it starts
// accepting connections: where, on which path, as which server revision, and
// which protocol revisions it negotiates.
var MCPHTTPServing = Event{
	ID:                 "mcp.http_serving",
	Msg:                MCPHTTPServingLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"listen_address"},
	BoundedAggregation: "exactly one line per process start, after the listener is bound",
	Fields: []Field{
		{Key: "listen_address", Type: FieldString, Presence: PresenceRequired},
		{Key: "transport", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: MCPTransportVocabulary()},
		{Key: "base_path", Type: FieldString, Presence: PresenceRequired},
		{Key: "server_version", Type: FieldString, Presence: PresenceRequired},
		{Key: "server_commit", Type: FieldString, Presence: PresenceRequired},
		{Key: "protocol_revisions", Type: FieldStringSlice, Presence: PresenceRequired, ClosedVocabulary: MCPHTTPProtocolRevisionVocabulary()},
		{Key: "max_body_bytes", Type: FieldInt, Presence: PresenceRequired},
	},
}

// MCPHTTPReadiness records a change of the hosted transport's readiness: the
// hosted API's liveness route, probed with the process configuration alone.
var MCPHTTPReadiness = Event{
	ID:                 "mcp.http_readiness",
	Msg:                MCPHTTPReadinessLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"sequence"},
	BoundedAggregation: "one line per readiness state change, numbered by sequence; a probe that repeats the last state emits none",
	Fields: []Field{
		{Key: "sequence", Type: FieldInt, Presence: PresenceRequired},
		{Key: "transport", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: MCPTransportVocabulary()},
		{Key: "state", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: MCPReadinessStateVocabulary()},
		{Key: "previous_state", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{"unknown"}, MCPReadinessStateVocabulary()...)},
		{Key: "failure_class", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: MCPReadinessFailureVocabulary()},
	},
}
