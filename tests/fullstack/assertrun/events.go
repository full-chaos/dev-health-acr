package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// ToolInvocation is one observed call to an MCP tool inside the OpenCode session, as
// reconstructed from the headless event stream. Name is normalized to the bare tool name
// (see normalizeToolName); Arguments and ResultText are best-effort and may be empty if the
// event shape did not carry them. Status is the tool part's completion status ("completed"
// on success in the confirmed OpenCode 1.18.4 shape); it is empty when the event shape did
// not carry one.
type ToolInvocation struct {
	Name       string
	Arguments  json.RawMessage
	ResultText string
	Status     string
}

// Failed reports whether this invocation's status is present and not "completed".
func (t ToolInvocation) Failed() bool { return t.Status != "" && t.Status != "completed" }

// EventStream is the entire surface this package depends on for OpenCode's headless event
// format. When a future OpenCode version changes the confirmed shape, only this file (and
// its test) should need to change: reimplement opencodeEvents (or add a new implementation)
// satisfying this interface, and update newEventStream in assertrun.go if the constructor
// signature changes.
type EventStream interface {
	// ToolInvocations returns every tool call this run's event stream recorded, in the order
	// they were observed. It must not fabricate a call that was not present in the stream.
	ToolInvocations() ([]ToolInvocation, error)
	// FinalAssistantText returns the last assistant/model text message in the stream, which
	// for this suite's scripted turn sequence is expected to be the strict-JSON agent result.
	// It returns an empty string, not an error, if no assistant text was observed, and an
	// error if more than one non-empty final-text event was observed (a strict-JSON task
	// must emit exactly one; scripts/e2e/fullstack-opencode.sh's extract_agent_result
	// enforces the same invariant via jq before assert-run ever runs, so this is defense in
	// depth, not the only gate).
	FinalAssistantText() (string, error)
}

// knownToolSuffixes lists the bare MCP tool names this suite cares about. OpenCode may
// namespace a tool call under its configured MCP server name ("acr") using any of several
// separators observed across client versions: "acr_context_for_task", "acr.context_for_task",
// "acr*context_for_task", or no namespace at all. normalizeToolName strips a single leading
// "<anything><separator>" prefix when what remains is an exact, case-sensitive match for one
// of these bare names.
var knownToolSuffixes = []string{"context_for_task", "source_evidence", "record_episode"}

// normalizeToolName maps a possibly-namespaced tool identifier observed in the event stream
// onto its bare tool name. If name does not end in (or exactly equal) a known bare tool name,
// it is returned unchanged so callers can still see and report on unrecognized tool calls.
func normalizeToolName(name string) string {
	for _, bare := range knownToolSuffixes {
		if name == bare {
			return bare
		}
		if strings.HasSuffix(name, bare) {
			prefix := name[:len(name)-len(bare)]
			if len(prefix) > 0 {
				switch prefix[len(prefix)-1] {
				case '_', '.', '*', '/', ':', '-':
					return bare
				}
			}
		}
	}
	return name
}

// opencodeEvents implements EventStream against `opencode run --format json`'s empirically
// confirmed shape (OpenCode 1.18.4): one JSON object per line.
//
//	{"type":"tool_use","part":{"type":"tool","tool":"acr_context_for_task","callID":"...",
//	  "state":{"status":"completed","input":{...},"output":"<json-encoded string>","metadata":{...}},...}}
//	{"type":"text","part":{"text":"<json-encoded string>",...}}
//	{"type":"step_start", ...} / {"type":"step_finish", ...}   // ignored: no tool/text payload
//
// The tool name is namespaced "<mcpServerName>_<toolName>" with an underscore (our server is
// "acr"); .part.state.input is a JSON object; .part.state.output is a JSON-encoded STRING
// (callers that need structured data out of it must json.Unmarshal it a second time --
// ToolInvocation.ResultText is deliberately just the raw string, not decoded, since this
// package has no opinion on every tool's result shape); .part.state.status is "completed" on
// success.
//
// matchConfirmedToolUse/matchConfirmedText below match this shape exactly and are tried
// first. Every line that does not match either -- a future OpenCode version, or a step_*
// event -- falls back to a defensive structural scan (asToolInvocation/findAssistantText)
// so a schema drift degrades gracefully instead of silently losing invocations. The unit
// tests pin the confirmed shape verbatim plus three deliberately different synthetic shapes
// to guard the fallback.
type opencodeEvents struct {
	lines []map[string]any
	raw   [][]byte
}

func newOpencodeEventsFromFile(path string) (*opencodeEvents, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read opencode event stream: %w", err)
	}
	return newOpencodeEventsFromBytes(data)
}

func newOpencodeEventsFromBytes(data []byte) (*opencodeEvents, error) {
	stream := &opencodeEvents{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal(line, &decoded); err != nil {
			// Not every line need be a JSON object (some formats interleave blank lines
			// or non-JSON banners); skip rather than fail the whole stream.
			continue
		}
		stream.lines = append(stream.lines, decoded)
		stream.raw = append(stream.raw, append([]byte(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan opencode event stream: %w", err)
	}
	return stream, nil
}

var toolDiscriminator = regexp.MustCompile(`(?i)tool[-_]?(call|use|invocation|result)?`)

// matchConfirmedToolUse matches a line against the confirmed {"type":"tool_use","part":
// {"tool":...,"state":{...}}} shape exactly (case-sensitive: OpenCode's own "type" values
// are fixed lowercase tokens, not something to fuzzy-match).
func matchConfirmedToolUse(line map[string]any) (ToolInvocation, bool) {
	if typ, _ := stringField(line, "type"); typ != "tool_use" {
		return ToolInvocation{}, false
	}
	part, ok := line["part"].(map[string]any)
	if !ok {
		return ToolInvocation{}, false
	}
	name, ok := stringField(part, "tool")
	if !ok || name == "" {
		return ToolInvocation{}, false
	}
	inv := ToolInvocation{Name: normalizeToolName(name)}
	if state, ok := part["state"].(map[string]any); ok {
		if input, ok := state["input"]; ok {
			if encoded, err := json.Marshal(input); err == nil {
				inv.Arguments = encoded
			}
		}
		if output, ok := stringField(state, "output"); ok {
			inv.ResultText = output
		}
		if status, ok := stringField(state, "status"); ok {
			inv.Status = status
		}
	}
	return inv, true
}

// matchConfirmedText matches a line against the confirmed {"type":"text","part":{"text":...}}
// shape exactly.
func matchConfirmedText(line map[string]any) (string, bool) {
	if typ, _ := stringField(line, "type"); typ != "text" {
		return "", false
	}
	part, ok := line["part"].(map[string]any)
	if !ok {
		return "", false
	}
	return stringField(part, "text")
}

func (s *opencodeEvents) ToolInvocations() ([]ToolInvocation, error) {
	var out []ToolInvocation
	for _, line := range s.lines {
		if inv, ok := matchConfirmedToolUse(line); ok {
			out = append(out, inv)
			continue
		}
		out = append(out, extractToolInvocations(line)...)
	}
	return out, nil
}

// extractToolInvocations walks a single decoded event line (or, recursively, any object
// nested within it) looking for tool-call-shaped objects. It returns every match found in
// this line, since some event formats batch multiple tool calls per line (e.g. as a "parts"
// array).
func extractToolInvocations(node any) []ToolInvocation {
	var out []ToolInvocation
	switch value := node.(type) {
	case map[string]any:
		if inv, ok := asToolInvocation(value); ok {
			out = append(out, inv)
		}
		for _, child := range value {
			out = append(out, extractToolInvocations(child)...)
		}
	case []any:
		for _, child := range value {
			out = append(out, extractToolInvocations(child)...)
		}
	}
	return out
}

var toolNameKeys = []string{"tool", "tool_name", "toolName", "name"}
var toolArgsKeys = []string{"arguments", "args", "input", "params", "parameters"}
var toolResultKeys = []string{"result_text", "resultText", "output", "text", "content"}

// asToolInvocation reports whether obj itself is shaped like a single tool call/result, using
// the discriminator-then-structural strategy described on opencodeEvents.
func asToolInvocation(obj map[string]any) (ToolInvocation, bool) {
	// Some event shapes nest the tool identity and payload under a "part" wrapper (message
	// part events) and/or a further "state" wrapper (OpenCode's SST-style
	// {tool, state: {input, output}} part shape). Fold both in, unconditionally, before
	// looking for a discriminator or a name: the wrapper's own "type" (if any) becomes the
	// discriminator candidate, and its "input"/"output" become directly visible fields.
	if part, ok := obj["part"].(map[string]any); ok {
		obj = mergeShallow(obj, part)
	}
	if state, ok := obj["state"].(map[string]any); ok {
		obj = mergeShallow(obj, state)
	}

	discriminated := false
	if typ, ok := stringField(obj, "type"); ok && toolDiscriminator.MatchString(typ) {
		discriminated = true
	}

	name, nameOK := firstStringField(obj, toolNameKeys)
	if !nameOK {
		return ToolInvocation{}, false
	}
	normalized := normalizeToolName(name)
	isKnown := false
	for _, bare := range knownToolSuffixes {
		if normalized == bare {
			isKnown = true
			break
		}
	}
	if !discriminated && !isKnown {
		// Without an explicit discriminator, only accept names that normalize to a tool
		// this suite actually knows about; otherwise nearly any object with a "name" field
		// (e.g. a repository or evidence source) would be misread as a tool call.
		return ToolInvocation{}, false
	}
	if !discriminated {
		// A bare "name" field alone is weak evidence; also require an args-shaped field so
		// plain metadata objects (e.g. {"name": "context_for_task_button"}) are not misread.
		if _, ok := firstAnyField(obj, toolArgsKeys); !ok {
			return ToolInvocation{}, false
		}
	}

	inv := ToolInvocation{Name: normalized}
	if args, ok := firstAnyField(obj, toolArgsKeys); ok {
		if encoded, err := json.Marshal(args); err == nil {
			inv.Arguments = encoded
		}
	}
	if text, ok := firstStringField(obj, toolResultKeys); ok {
		inv.ResultText = text
	}
	return inv, true
}

func stringField(obj map[string]any, key string) (string, bool) {
	v, ok := obj[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func firstStringField(obj map[string]any, keys []string) (string, bool) {
	for _, key := range keys {
		if s, ok := stringField(obj, key); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

func firstAnyField(obj map[string]any, keys []string) (any, bool) {
	for _, key := range keys {
		if v, ok := obj[key]; ok {
			return v, true
		}
	}
	return nil, false
}

func mergeShallow(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// assistantTextDiscriminator matches event "type"/"role" values plausibly identifying an
// assistant/model text message.
var assistantTextDiscriminator = regexp.MustCompile(`(?i)^(assistant|message|text|final)`)

func (s *opencodeEvents) FinalAssistantText() (string, error) {
	var nonEmpty []string
	sawConfirmedTextLine := false
	for _, line := range s.lines {
		if text, ok := matchConfirmedText(line); ok {
			sawConfirmedTextLine = true
			if text != "" {
				nonEmpty = append(nonEmpty, text)
			}
		}
	}
	if sawConfirmedTextLine {
		if len(nonEmpty) > 1 {
			return "", fmt.Errorf("opencode event stream contains %d non-empty final text parts, want at most 1 for a strict-JSON task", len(nonEmpty))
		}
		if len(nonEmpty) == 1 {
			return nonEmpty[0], nil
		}
		return "", nil
	}

	// No line matched the confirmed shape at all (a different OpenCode version); fall back
	// to the defensive structural scan rather than reporting "no assistant text" outright.
	var last string
	for _, line := range s.lines {
		if text, ok := findAssistantText(line); ok {
			last = text
		}
	}
	return last, nil
}

func findAssistantText(node any) (string, bool) {
	obj, ok := node.(map[string]any)
	if !ok {
		return "", false
	}
	role, _ := stringField(obj, "role")
	typ, _ := stringField(obj, "type")
	isAssistant := strings.EqualFold(role, "assistant") || assistantTextDiscriminator.MatchString(typ)
	if isAssistant {
		if text, ok := firstStringField(obj, []string{"text", "content", "message"}); ok {
			return text, true
		}
		// The discriminator (e.g. {"type":"text","part":{"text":"..."}}) can live on the
		// wrapper while the text itself lives one level down, on "part", without "part"
		// carrying its own type/role. Look there directly rather than requiring the child
		// to independently re-qualify as an assistant message.
		if part, ok := obj["part"].(map[string]any); ok {
			if text, ok := firstStringField(part, []string{"text", "content", "message"}); ok {
				return text, true
			}
		}
	}
	if part, ok := obj["part"].(map[string]any); ok {
		if text, ok := findAssistantText(part); ok {
			return text, true
		}
	}
	var found string
	var foundDeeper bool
	for _, v := range obj {
		if child, ok := v.(map[string]any); ok {
			if text, ok := findAssistantText(child); ok {
				found, foundDeeper = text, true
			}
		}
		if arr, ok := v.([]any); ok {
			for _, item := range arr {
				if text, ok := findAssistantText(item); ok {
					found, foundDeeper = text, true
				}
			}
		}
	}
	return found, foundDeeper
}
