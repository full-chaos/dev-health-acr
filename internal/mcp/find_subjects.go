package mcp

import (
	"context"
	"encoding/json"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// handleFindSubjects implements the find_subjects tool: list subjects of one
// kind, or find them by exact name, through the hosted lookup. Every subject
// in the answer passed the hosted subject gate for the caller's credential
// in this call. The API JSON is the structured content, unchanged. No
// interpreter, synthesizer or embedding model is involved.
func handleFindSubjects(ctx context.Context, cfg *ProcessConfig, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	caller, callerErr := CallerFromContext(ctx)
	if callerErr != nil {
		return refuseWithoutCaller(ctx, cfg, toolFindSubjects), nil
	}
	var input contractsv1.MCPFindSubjectsRequest
	if err := decodeToolArguments(rawArgs(req), &input, false); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: toolArgumentsMessage(toolFindSubjects, err)}), nil
	}
	if err := input.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "find_subjects arguments failed schema validation"}), nil
	}
	raw, err := caller.client.FindSubjects(ctx, input)
	if err != nil {
		return toolErrorResult(err), nil
	}
	raw = pickAmbiguousSubject(ctx, newElicitTransport(cfg, req), raw)
	return rawToolResult(raw, sidecar.RenderFindSubjectsSummary(raw, sidecar.DataTextMaxBytes)), nil
}

const maxElicitedSubjects = 25

// pickAmbiguousSubject asks the user which of the offered subjects is meant
// when the answer is ambiguous. Any outcome but an accepted offered
// canonical_id returns raw unchanged.
func pickAmbiguousSubject(ctx context.Context, t elicitTransport, raw json.RawMessage) json.RawMessage {
	if t == nil {
		return raw
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return raw
	}
	var status string
	var subjects []map[string]json.RawMessage
	if json.Unmarshal(doc["status"], &status) != nil || status != "ambiguous" ||
		json.Unmarshal(doc["subjects"], &subjects) != nil ||
		len(subjects) < 2 || len(subjects) > maxElicitedSubjects {
		return raw
	}
	options := make([]elicitOption, 0, len(subjects))
	byID := make(map[string]map[string]json.RawMessage, len(subjects))
	for _, s := range subjects {
		var id, label, provider, kind string
		if json.Unmarshal(s["canonical_id"], &id) != nil || id == "" {
			return raw
		}
		_ = json.Unmarshal(s["label"], &label)
		_ = json.Unmarshal(s["provider"], &provider)
		_ = json.Unmarshal(s["kind"], &kind)
		if _, dup := byID[id]; dup {
			return raw
		}
		byID[id] = s
		title := label + " (" + kind
		if provider != "" {
			title += ", " + provider
		}
		options = append(options, elicitOption{Value: id, Title: title + ", " + id + ")"})
	}
	res := elicit(ctx, t, elicitChoice{
		Message: "More than one subject matches. Which one do you mean?",
		Field:   "subject",
		Title:   "Subject",
		Options: options,
	})
	if res.Outcome != elicitAccepted {
		return raw
	}
	picked, err := json.Marshal([]map[string]json.RawMessage{byID[res.Value]})
	if err != nil {
		return raw
	}
	doc["status"] = json.RawMessage(`"complete"`)
	doc["subjects"] = picked
	doc["population"] = narrowedCounts(doc["population"], "returned", "total_known")
	doc["page"] = narrowedCounts(doc["page"], "returned")
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}

func narrowedCounts(raw json.RawMessage, ones ...string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		m = map[string]json.RawMessage{}
	}
	for _, k := range ones {
		m[k] = json.RawMessage("1")
	}
	if _, ok := m["truncated"]; ok {
		m["truncated"] = json.RawMessage("false")
	}
	if _, ok := m["complete"]; ok {
		m["complete"] = json.RawMessage("true")
	}
	delete(m, "next_cursor")
	out, _ := json.Marshal(m)
	return out
}
