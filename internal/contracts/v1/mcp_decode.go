package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// This file closes a decode-boundary gap the Oracle gate found in the MCP
// request/response contracts: none of contracts/jsonschema/v1/mcp_*.schema.json
// ever declares a nullable type, so every property that appears in a
// payload must be a real value if it appears at all - but plain
// encoding/json unmarshaling cannot tell "field omitted" (leave the Go
// zero value, which mcp_validate.go's Validate() methods correctly treat
// as "not specified") apart from "field explicitly null" (leave the exact
// same Go zero value, silently accepted even though the schema requires
// an object/string/integer/boolean when the key is present at all). For
// pointer fields such as MCPContextForTaskRequest.Repository this means a
// caller-sent `"repository": null` decodes identically to omitting
// repository entirely. For non-pointer fields whose zero value already
// happens to be schema-invalid (an empty goal) Validate() catches the null
// by accident; for one whose zero value is schema-legitimate (rendered_
// markdown.truncated: false) it does not catch it at all. mcpNullCheck
// below rejects explicit null for every field these four MCP contracts
// declare, at every nesting depth this package owns, independent of
// whether the resulting zero value would coincidentally fail Validate().

// mcpNullCheck walks one JSON object level looking for keys present with
// the literal JSON value null. keys lists every property name the wire
// schema declares for that level (required or optional - schema
// "required" only controls whether a field may be omitted, never whether
// it may be null). nested maps a key to the check its own value must pass
// once confirmed present and non-null, letting the walk recurse into
// repository/scope/budget and rendered_markdown without reaching into
// structured (ContextPacket/ExpandedEvidence), which belongs to the
// separate validate_packet.go/validate_evidence.go contract surface.
// apply also rejects raw itself being the literal null: the same
// no-nullable-type rule applies to the object a call represents, not
// only to the keys inside it, so a caller decoding a bare top-level
// `null` document gets the same rejection as a null field would.
type mcpNullCheck struct {
	keys   []string
	nested map[string]mcpNullCheck
}

func (c mcpNullCheck) apply(raw []byte, path string) error {
	if isExplicitJSONNull(raw) {
		return fmt.Errorf("%s: must not be JSON null; omit the field to use its default, or provide a value", path)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		// raw is not a JSON object; the caller's own subsequent
		// json.Unmarshal into the typed field produces the real
		// "cannot unmarshal X into Y" error, so this is a no-op here.
		return nil
	}
	for _, key := range c.keys {
		value, present := obj[key]
		if !present {
			continue
		}
		fieldPath := mcpJoinPath(path, key)
		if isExplicitJSONNull(value) {
			return fmt.Errorf("%s: must not be JSON null; omit the field to use its default, or provide a value", fieldPath)
		}
		if nestedCheck, ok := c.nested[key]; ok {
			if err := nestedCheck.apply(value, fieldPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func isExplicitJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

func mcpJoinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// strictUnmarshal decodes data into v with two guarantees plain
// json.Unmarshal does not give a type alias decode: (1) an object key
// this Go type does not declare, at any nesting depth, is rejected rather
// than silently dropped -- the runtime tool-call decode boundary must
// match the wire schemas' additionalProperties: false as closely as the
// offline contractcheck validation already does; and (2) any
// non-whitespace content after the single JSON value is rejected. Every
// MCP request/response UnmarshalJSON method below calls this instead of
// json.Unmarshal for its alias-type decode step, once its own
// explicit-null check has already passed.
func strictUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("unexpected data after the JSON value")
	}
	return nil
}

var mcpRepositoryNullCheck = mcpNullCheck{keys: []string{"slug"}}

var mcpScopeNullCheck = mcpNullCheck{
	keys: []string{"branch", "commit_sha", "task_ref", "files", "as_of", "time_window_days", "include_changed_files"},
}

var mcpBudgetNullCheck = mcpNullCheck{
	keys: []string{"max_items", "max_output_tokens", "max_serialized_bytes"},
}

var mcpRenderedMarkdownNullCheck = mcpNullCheck{
	keys: []string{"markdown", "untrusted", "truncated"},
}

var mcpLocalContextNullCheck = mcpNullCheck{
	keys: []string{"provider", "status", "provider_version", "query_version", "indexed_at", "indexed_ref", "indexed_commit", "freshness", "warnings", "items", "evidence_refs"},
}

var mcpFederatedBudgetNullCheck = mcpNullCheck{
	keys: []string{"max_items", "max_output_tokens", "max_serialized_bytes", "hosted_items_used", "local_items_used", "total_items_used", "hosted_estimated_tokens", "local_estimated_tokens", "total_estimated_tokens", "hosted_serialized_bytes", "local_serialized_bytes", "total_serialized_bytes", "hosted_truncated", "local_truncated", "truncated"},
}

var mcpContextForTaskRequestNullCheck = mcpNullCheck{
	keys: []string{"goal", "repository", "scope", "budget", "requested_categories"},
	nested: map[string]mcpNullCheck{
		"repository": mcpRepositoryNullCheck,
		"scope":      mcpScopeNullCheck,
		"budget":     mcpBudgetNullCheck,
	},
}

var mcpSourceEvidenceRequestNullCheck = mcpNullCheck{
	keys: []string{"evidence_ref_id"},
}

var mcpContextForTaskResponseNullCheck = mcpNullCheck{
	keys: []string{"schema_version", "structured", "rendered_markdown", "local_context", "federated_budget"},
	nested: map[string]mcpNullCheck{
		"rendered_markdown": mcpRenderedMarkdownNullCheck,
		"local_context":     mcpLocalContextNullCheck,
		"federated_budget":  mcpFederatedBudgetNullCheck,
	},
}

var mcpSourceEvidenceResponseNullCheck = mcpNullCheck{
	keys: []string{"schema_version", "structured", "rendered_markdown"},
	nested: map[string]mcpNullCheck{
		"rendered_markdown": mcpRenderedMarkdownNullCheck,
	},
}

// UnmarshalJSON rejects explicit JSON null for repository, scope, and
// budget (and their own nested fields) while still leaving them nil when
// the caller omits them, per the goal-only-by-default ergonomic contract
// mcp_types.go documents. It otherwise decodes exactly as the default
// struct tags would; the local alias avoids recursing back into this
// method.
func (r *MCPContextForTaskRequest) UnmarshalJSON(data []byte) error {
	if err := mcpContextForTaskRequestNullCheck.apply(data, "context_for_task request"); err != nil {
		return err
	}
	type mcpContextForTaskRequestAlias MCPContextForTaskRequest
	var decoded mcpContextForTaskRequestAlias
	if err := strictUnmarshal(data, &decoded); err != nil {
		return err
	}
	*r = MCPContextForTaskRequest(decoded)
	return nil
}

// UnmarshalJSON rejects explicit JSON null for evidence_ref_id, the only
// field this request declares.
func (r *MCPSourceEvidenceRequest) UnmarshalJSON(data []byte) error {
	if err := mcpSourceEvidenceRequestNullCheck.apply(data, "source_evidence request"); err != nil {
		return err
	}
	type mcpSourceEvidenceRequestAlias MCPSourceEvidenceRequest
	var decoded mcpSourceEvidenceRequestAlias
	if err := strictUnmarshal(data, &decoded); err != nil {
		return err
	}
	*r = MCPSourceEvidenceRequest(decoded)
	return nil
}

// UnmarshalJSON rejects explicit JSON null for schema_version, structured,
// and rendered_markdown (and rendered_markdown's own nested fields).
// structured is intentionally not recursed into: ContextPacket is a
// separate contract owned by validate_packet.go/validate_packet_nested.go.
func (r *MCPContextForTaskResponse) UnmarshalJSON(data []byte) error {
	if err := mcpContextForTaskResponseNullCheck.apply(data, "context_for_task response"); err != nil {
		return err
	}
	if err := validateMCPLocalContextPayload(data); err != nil {
		return err
	}
	type mcpContextForTaskResponseAlias MCPContextForTaskResponse
	var decoded mcpContextForTaskResponseAlias
	if err := strictUnmarshal(data, &decoded); err != nil {
		return err
	}
	*r = MCPContextForTaskResponse(decoded)
	return nil
}

// UnmarshalJSON rejects explicit JSON null for schema_version, structured,
// and rendered_markdown (and rendered_markdown's own nested fields).
// structured is intentionally not recursed into: ExpandedEvidence is a
// separate contract owned by validate_evidence.go.
func (r *MCPSourceEvidenceResponse) UnmarshalJSON(data []byte) error {
	if err := mcpSourceEvidenceResponseNullCheck.apply(data, "source_evidence response"); err != nil {
		return err
	}
	type mcpSourceEvidenceResponseAlias MCPSourceEvidenceResponse
	var decoded mcpSourceEvidenceResponseAlias
	if err := strictUnmarshal(data, &decoded); err != nil {
		return err
	}
	*r = MCPSourceEvidenceResponse(decoded)
	return nil
}
