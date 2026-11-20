package mcp

import (
	"encoding/json"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The MCP Tasks extension (io.modelcontextprotocol/tasks, ext-tasks
// specification 2026-07-28): a client opts in per request, in the client
// capabilities its _meta carries; there is no session-level opt-in.
const (
	tasksExtension        = "io.modelcontextprotocol/tasks"
	tasksProtocolRevision = "2026-07-28"
)

// requestClientCapabilities returns the raw client capabilities THIS request
// carries in its _meta (revisions that send them per request), and false
// when the request carries none or they are not a JSON object. It reads only
// the request; no session state.
func requestClientCapabilities(params mcpsdk.Params) (json.RawMessage, bool) {
	if params == nil || isNilValue(params) {
		return nil, false
	}
	raw, ok := params.GetMeta()[mcpsdk.MetaKeyClientCapabilities]
	if !ok {
		return nil, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(encoded, &object) != nil || object == nil {
		return nil, false
	}
	return encoded, true
}

// requestProtocolRevision is the protocol revision THIS request names in its
// _meta, "" when it names none.
func requestProtocolRevision(params mcpsdk.Params) string {
	if params == nil || isNilValue(params) {
		return ""
	}
	revision, _ := params.GetMeta()[mcpsdk.MetaKeyProtocolVersion].(string)
	return revision
}

// clientDeclaresTasks reports whether THIS request declares the tasks
// extension: a 2026-07-28 (or later) request whose client capabilities list
// io.modelcontextprotocol/tasks under extensions.
func clientDeclaresTasks(params mcpsdk.Params) bool {
	if requestProtocolRevision(params) < tasksProtocolRevision {
		return false
	}
	raw, ok := requestClientCapabilities(params)
	if !ok {
		return false
	}
	var caps struct {
		Extensions map[string]json.RawMessage `json:"extensions"`
	}
	if json.Unmarshal(raw, &caps) != nil {
		return false
	}
	_, declared := caps.Extensions[tasksExtension]
	return declared
}
