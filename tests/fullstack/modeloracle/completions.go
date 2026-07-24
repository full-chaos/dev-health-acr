package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type toolDefinition struct {
	Type     string `json:"type"`
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type chatRequest struct {
	Model    string           `json:"model"`
	Stream   bool             `json:"stream"`
	Messages []chatMessage    `json:"messages"`
	Tools    []toolDefinition `json:"tools"`
}

// OpenCode namespaces MCP tools as "<server>_<tool>". Matching on a boundary-anchored suffix
// keeps the harness working if that separator ever changes, without matching an unrelated
// built-in tool that merely contains the word.
func matchToolName(tools []toolDefinition, bare string) (string, bool) {
	pattern := regexp.MustCompile(`(?i)(^|[_.\-*/])` + regexp.QuoteMeta(bare) + `$`)
	for _, tool := range tools {
		if tool.Function.Name == bare || pattern.MatchString(tool.Function.Name) {
			return tool.Function.Name, true
		}
	}
	return "", false
}

func textContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err == nil {
		var builder strings.Builder
		for _, part := range parts {
			if value, ok := part["text"].(string); ok {
				builder.WriteString(value)
			}
		}
		return builder.String()
	}
	return string(raw)
}

func (s *server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	s.touch()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusInternalServerError)
		return
	}
	var request chatRequest
	if err := json.Unmarshal(body, &request); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.requests++
	sequence := s.requests
	if s.toolNames == nil {
		s.toolNames = map[string]string{}
	}
	for _, bare := range []string{"context_for_task", "source_evidence", "record_episode"} {
		if name, ok := matchToolName(request.Tools, bare); ok {
			s.toolNames[bare] = name
		}
	}
	// Every tool result in the transcript is folded into the observation, so a retried or
	// replayed turn cannot lose an earlier reading.
	turn := 0
	var unobserved []string
	for _, message := range request.Messages {
		if message.Role != "tool" {
			continue
		}
		turn++
		payload := textContent(message.Content)
		reading := observeToolResult(payload)
		// A tool result the model cannot read is the hardest failure in this harness to
		// diagnose: the session completes, the answer is simply empty, and nothing says why.
		// Keeping the raw payload turns that into a one-look answer.
		if reading.isEmpty() {
			unobserved = append(unobserved, payload)
		}
		s.observed.merge(reading)
	}
	observed := s.observed
	plan := s.plan
	contextTool := s.toolNames["context_for_task"]
	evidenceTool := s.toolNames["source_evidence"]
	s.mu.Unlock()

	s.recordRequest(sequence, body, request)
	s.recordObservation(sequence, observed, unobserved)

	responseID := fmt.Sprintf("chatcmpl-context-fabric-%03d", sequence)

	switch {
	case turn == 0:
		if contextTool == "" {
			s.emitFailure(w, request, responseID, "context_for_task was not offered by the client")
			return
		}
		s.emitToolCall(w, request, responseID, contextTool, "call_context_for_task", plan.contextArguments())
	case turn < 1+expansions(plan, observed) && evidenceTool != "":
		next := nextEvidenceRef(observed, turn-1)
		if next == "" {
			s.emitFinal(w, request, responseID, plan, observed)
			return
		}
		s.emitToolCall(w, request, responseID, evidenceTool, fmt.Sprintf("call_source_evidence_%d", turn), map[string]any{
			"evidence_ref_id": next,
		})
	default:
		s.emitFinal(w, request, responseID, plan, observed)
	}
}

// expansions is how many source_evidence calls this run should make: never more than the
// packet actually returned, and never any at all for an empty or degraded packet.
func expansions(plan Plan, observed Observation) int {
	if plan.Fault == FaultSkipEvidence {
		return 0
	}
	if degraded(observed.PacketStatus) {
		return 0
	}
	// Expand every reference the packet returned, not merely the planned minimum. The
	// rendering a real client receives lists an item's evidence IDs but not the entity behind
	// them, so expanding is the only way the model can learn which reference supports which
	// claim — and a claim it cannot tie to returned evidence is one it must refuse to make.
	// The cap keeps a large packet from making the session unbounded.
	// The plan's minimum is deliberately not consulted: it is a floor the assertion layer
	// checks against the finished run, not a target the model aims at. Expanding fewer than
	// the packet returned could only hide evidence from the answer.
	wanted := len(observed.Sightings)
	if wanted > maxEvidenceExpansions {
		wanted = maxEvidenceExpansions
	}
	return wanted
}

// maxEvidenceExpansions bounds the session: one source_evidence call per returned reference,
// up to this many.
const maxEvidenceExpansions = 20

func nextEvidenceRef(observed Observation, index int) string {
	if index < 0 || index >= len(observed.Sightings) {
		return ""
	}
	return observed.Sightings[index].EvidenceRefID
}

func (s *server) recordRequest(sequence int, body []byte, request chatRequest) {
	names := make([]string, 0, len(request.Tools))
	for _, tool := range request.Tools {
		names = append(names, tool.Function.Name)
	}
	summary, err := json.MarshalIndent(map[string]any{
		"sequence":       sequence,
		"model":          request.Model,
		"stream":         request.Stream,
		"message_roles":  roles(request.Messages),
		"offered_tools":  names,
		"request_bytes":  len(body),
		"schema_version": "fullstack_model_request.v1",
	}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(s.logDir, fmt.Sprintf("model-request-%03d.json", sequence)), append(summary, '\n'), 0o600)
}

// recordObservation writes what the model actually learned from the live tool results, plus
// the verbatim payload of any tool result it could not read at all. Without the second half,
// an unreadable response looks identical to an empty one from outside.
func (s *server) recordObservation(sequence int, observed Observation, unobserved []string) {
	summary, err := json.MarshalIndent(map[string]any{
		"schema_version":          "fullstack_model_observation.v1",
		"sequence":                sequence,
		"packet_status":           observed.PacketStatus,
		"scope_resolution":        observed.ScopeResolution,
		"sightings":               observed.Sightings,
		"warnings":                observed.Warnings,
		"unreadable_tool_results": unobserved,
	}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(s.logDir, fmt.Sprintf("model-observation-%03d.json", sequence)), append(summary, '\n'), 0o600)
}

func roles(messages []chatMessage) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		out = append(out, message.Role)
	}
	return out
}

func (s *server) emitFinal(w http.ResponseWriter, request chatRequest, responseID string, plan Plan, observed Observation) {
	s.mu.Lock()
	s.finalSent = true
	s.mu.Unlock()
	s.emitText(w, request, responseID, encodeResult(buildResult(plan, observed)))
}

func (s *server) emitFailure(w http.ResponseWriter, request chatRequest, responseID, reason string) {
	// A missing tool is a harness failure, not an agent answer; emitting it as text keeps the
	// diagnostic in the captured event stream where the assertion report can quote it.
	s.emitText(w, request, responseID, `{"schema_version":"fullstack_model_failure.v1","reason":`+quote(reason)+`}`)
}

func quote(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `"unencodable"`
	}
	return string(encoded)
}
