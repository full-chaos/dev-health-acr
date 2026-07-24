package main

import (
	"strings"
	"testing"
)

func toolNames(t *testing.T, invocations []ToolInvocation) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, inv := range invocations {
		names[inv.Name] = true
	}
	return names
}

// Shape 1: a flat OpenAI-style function-call event stream with an explicit "type"
// discriminator and an "acr_"-underscore-namespaced tool name.
func TestOpencodeEvents_flatToolCallShape(t *testing.T) {
	data := []byte(strings.Join([]string{
		`{"type":"tool_call","tool":"acr_context_for_task","arguments":{"goal":"investigate"}}`,
		`{"type":"tool_result","tool":"acr_context_for_task","result_text":"{\"schema_version\":\"context_packet.v1\"}"}`,
		`{"type":"tool_call","tool":"source_evidence","arguments":{"evidence_ref_id":"acr:v1:ci:checkout-e2e-run-4821"}}`,
		`{"type":"assistant_message","text":"{\"schema_version\":\"context_fabric_agent_result.v1\"}"}`,
	}, "\n"))

	stream, err := newOpencodeEventsFromBytes(data)
	if err != nil {
		t.Fatalf("newOpencodeEventsFromBytes: %v", err)
	}
	invocations, err := stream.ToolInvocations()
	if err != nil {
		t.Fatalf("ToolInvocations: %v", err)
	}
	names := toolNames(t, invocations)
	if !names["context_for_task"] {
		t.Fatal("expected a normalized context_for_task invocation")
	}
	if !names["source_evidence"] {
		t.Fatal("expected a source_evidence invocation")
	}
	if names["record_episode"] {
		t.Fatal("must not report a record_episode invocation that was never in the stream")
	}

	final, err := stream.FinalAssistantText()
	if err != nil {
		t.Fatalf("FinalAssistantText: %v", err)
	}
	if !strings.Contains(final, "context_fabric_agent_result.v1") {
		t.Fatalf("FinalAssistantText() = %q, want the strict-JSON agent result", final)
	}
}

// Shape 2: Anthropic-style content blocks nested inside role-tagged messages, with the tool
// namespaced using a dot separator.
func TestOpencodeEvents_nestedContentBlockShape(t *testing.T) {
	data := []byte(strings.Join([]string{
		`{"role":"assistant","content":[{"type":"tool_use","name":"acr.context_for_task","input":{"goal":"investigate"}},{"type":"text","text":"thinking"}]}`,
		`{"role":"tool","content":[{"type":"tool_result","name":"acr.source_evidence","content":"expanded evidence"}]}`,
	}, "\n"))

	stream, err := newOpencodeEventsFromBytes(data)
	if err != nil {
		t.Fatalf("newOpencodeEventsFromBytes: %v", err)
	}
	invocations, err := stream.ToolInvocations()
	if err != nil {
		t.Fatalf("ToolInvocations: %v", err)
	}
	names := toolNames(t, invocations)
	if !names["context_for_task"] {
		t.Fatal("expected a normalized context_for_task invocation from a dot-namespaced name")
	}
	if !names["source_evidence"] {
		t.Fatal("expected a normalized source_evidence invocation from a dot-namespaced name")
	}
	if names["record_episode"] {
		t.Fatal("must not report a record_episode invocation that was never in the stream")
	}
}

// Shape 3: OpenCode's SST-style part/state wrapper, with no explicit "tool*" discriminator
// anywhere and the tool namespaced using an underscore.
func TestOpencodeEvents_partStateWrapperShape(t *testing.T) {
	data := []byte(strings.Join([]string{
		`{"type":"message.part.updated","part":{"id":"prt_1","tool":"acr_context_for_task","state":{"status":"completed","input":{"goal":"investigate"},"output":"packet json"}}}`,
		`{"type":"message.part.updated","part":{"id":"prt_2","tool":"acr_source_evidence","state":{"status":"completed","input":{"evidence_ref_id":"acr:v1:ci:checkout-e2e-run-4821"},"output":"evidence json"}}}`,
	}, "\n"))

	stream, err := newOpencodeEventsFromBytes(data)
	if err != nil {
		t.Fatalf("newOpencodeEventsFromBytes: %v", err)
	}
	invocations, err := stream.ToolInvocations()
	if err != nil {
		t.Fatalf("ToolInvocations: %v", err)
	}
	names := toolNames(t, invocations)
	if !names["context_for_task"] {
		t.Fatal("expected a normalized context_for_task invocation from the part/state wrapper shape")
	}
	if !names["source_evidence"] {
		t.Fatal("expected a normalized source_evidence invocation from the part/state wrapper shape")
	}
	if names["record_episode"] {
		t.Fatal("must not report a record_episode invocation that was never in the stream")
	}
}

// Shape 4: the confirmed real OpenCode `run --format json` shape used by
// scripts/e2e/fullstack-opencode.sh's extract_agent_result (jq: `select(.type == "text") |
// .part.text`) for the final assistant message, paired with a plausible tool-part sibling
// shape using the same {"type":..., "part":{...}} envelope.
// confirmedOpencodeEventLines is the empirically confirmed opencode 1.18.4 `run --format
// json` shape, verbatim (values adapted to this fixture's evidence/task names but the event
// structure -- keys, nesting, "type" discriminators -- copied exactly as reported). It
// includes the step_start/step_finish noise lines a real 2-tool-call run also emits
// (step_start x3, tool_use x2, step_finish x3, text x1), to prove those are silently ignored
// rather than misread as invocations.
var confirmedOpencodeEventLines = []string{
	`{"type":"step_start","timestamp":1784845051000,"sessionID":"ses_06ef"}`,
	`{"type":"tool_use","timestamp":1784845051430,"sessionID":"ses_06ef","part":{"type":"tool","tool":"acr_context_for_task","callID":"call_1_ctx","state":{"status":"completed","input":{"task":"investigate the flaky checkout test"},"output":"{\"schema_version\":\"context_packet.v1\",\"status\":\"partial\"}","metadata":{"truncated":false},"title":"","time":{"start":1,"end":2}},"id":"prt_1","sessionID":"ses_06ef","messageID":"msg_1"}}`,
	`{"type":"step_finish","timestamp":1784845051500,"sessionID":"ses_06ef"}`,
	`{"type":"step_start","timestamp":1784845051600,"sessionID":"ses_06ef"}`,
	`{"type":"tool_use","timestamp":1784845051700,"sessionID":"ses_06ef","part":{"type":"tool","tool":"acr_source_evidence","callID":"call_2_ev","state":{"status":"completed","input":{"evidence_ref_id":"acr:v1:ci:checkout-e2e-run-4821"},"output":"{\"schema_version\":\"expanded_evidence.v1\"}","metadata":{"truncated":false},"title":"","time":{"start":3,"end":4}},"id":"prt_2","sessionID":"ses_06ef","messageID":"msg_1"}}`,
	`{"type":"step_finish","timestamp":1784845051800,"sessionID":"ses_06ef"}`,
	`{"type":"step_start","timestamp":1784845051900,"sessionID":"ses_06ef"}`,
	`{"type":"text","timestamp":1784845052000,"sessionID":"ses_06ef","part":{"id":"prt_3","messageID":"msg_1","sessionID":"ses_06ef","type":"text","text":"{\"schema_version\":\"context_fabric_agent_result.v1\"}","time":{"start":5,"end":6}}}`,
	`{"type":"step_finish","timestamp":1784845052100,"sessionID":"ses_06ef"}`,
}

func TestOpencodeEvents_confirmedOpencodeShape(t *testing.T) {
	data := []byte(strings.Join(confirmedOpencodeEventLines, "\n"))

	stream, err := newOpencodeEventsFromBytes(data)
	if err != nil {
		t.Fatalf("newOpencodeEventsFromBytes: %v", err)
	}
	invocations, err := stream.ToolInvocations()
	if err != nil {
		t.Fatalf("ToolInvocations: %v", err)
	}
	// The confirmed-shape matcher is exact (no fallback double-counting), so a 2-tool-call
	// run must yield exactly 2 invocations, step_start/step_finish noise notwithstanding.
	if len(invocations) != 2 {
		t.Fatalf("ToolInvocations() returned %d entries, want exactly 2: %+v", len(invocations), invocations)
	}
	names := toolNames(t, invocations)
	if !names["context_for_task"] || !names["source_evidence"] {
		t.Fatalf("expected both tools observed (namespaced acr_context_for_task/acr_source_evidence normalized), got %v", names)
	}
	if names["record_episode"] {
		t.Fatal("must not report a record_episode invocation that was never in the stream")
	}
	for _, inv := range invocations {
		if inv.Status != "completed" {
			t.Fatalf("invocation %q status = %q, want %q", inv.Name, inv.Status, "completed")
		}
		if inv.Failed() {
			t.Fatalf("invocation %q should not be reported as failed", inv.Name)
		}
		if !strings.Contains(string(inv.Arguments), "{") {
			t.Fatalf("invocation %q Arguments should be the decoded JSON object from .part.state.input, got %q", inv.Name, inv.Arguments)
		}
		// .part.state.output is a JSON-encoded STRING; ResultText must be that raw string,
		// not further-decoded, since callers are responsible for unmarshaling it themselves.
		if !strings.HasPrefix(inv.ResultText, "{\"schema_version\"") {
			t.Fatalf("invocation %q ResultText = %q, want the raw JSON-encoded output string", inv.Name, inv.ResultText)
		}
	}

	final, err := stream.FinalAssistantText()
	if err != nil {
		t.Fatalf("FinalAssistantText: %v", err)
	}
	if !strings.Contains(final, "context_fabric_agent_result.v1") {
		t.Fatalf("FinalAssistantText() = %q, want the strict-JSON agent result", final)
	}
}

func TestOpencodeEvents_confirmedShapeFailedToolStatusSurfaces(t *testing.T) {
	data := []byte(`{"type":"tool_use","part":{"type":"tool","tool":"acr_context_for_task","state":{"status":"error","input":{},"output":""}}}`)
	stream, err := newOpencodeEventsFromBytes(data)
	if err != nil {
		t.Fatalf("newOpencodeEventsFromBytes: %v", err)
	}
	invocations, err := stream.ToolInvocations()
	if err != nil {
		t.Fatalf("ToolInvocations: %v", err)
	}
	if len(invocations) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(invocations))
	}
	if !invocations[0].Failed() {
		t.Fatalf("invocation with status %q should be reported as failed", invocations[0].Status)
	}
}

// TestOpencodeEvents_confirmedShapeMultipleNonEmptyTextPartsFails is the case the team lead
// asked for explicitly: a strict-JSON task emitting more than one non-empty final text part
// must fail loudly (an error), not silently pick the last one.
func TestOpencodeEvents_confirmedShapeMultipleNonEmptyTextPartsFails(t *testing.T) {
	data := []byte(strings.Join([]string{
		`{"type":"text","part":{"type":"text","text":"here is some prose before the JSON"}}`,
		`{"type":"text","part":{"type":"text","text":"{\"schema_version\":\"context_fabric_agent_result.v1\"}"}}`,
	}, "\n"))
	stream, err := newOpencodeEventsFromBytes(data)
	if err != nil {
		t.Fatalf("newOpencodeEventsFromBytes: %v", err)
	}
	if _, err := stream.FinalAssistantText(); err == nil {
		t.Fatal("expected an error for more than one non-empty final text part")
	}
}

func TestOpencodeEvents_confirmedShapeSingleTextPartOK(t *testing.T) {
	data := []byte(`{"type":"text","part":{"type":"text","text":"{\"schema_version\":\"context_fabric_agent_result.v1\"}"}}`)
	stream, err := newOpencodeEventsFromBytes(data)
	if err != nil {
		t.Fatalf("newOpencodeEventsFromBytes: %v", err)
	}
	final, err := stream.FinalAssistantText()
	if err != nil {
		t.Fatalf("FinalAssistantText: %v", err)
	}
	if !strings.Contains(final, "context_fabric_agent_result.v1") {
		t.Fatalf("FinalAssistantText() = %q", final)
	}
}

func TestOpencodeEvents_blankAndNonJSONLinesAreSkipped(t *testing.T) {
	data := []byte("\n  \nnot json at all\n{\"type\":\"tool_call\",\"tool\":\"context_for_task\",\"arguments\":{}}\n")
	stream, err := newOpencodeEventsFromBytes(data)
	if err != nil {
		t.Fatalf("newOpencodeEventsFromBytes: %v", err)
	}
	invocations, err := stream.ToolInvocations()
	if err != nil {
		t.Fatalf("ToolInvocations: %v", err)
	}
	if !toolNames(t, invocations)["context_for_task"] {
		t.Fatal("expected the one well-formed line to still be parsed")
	}
}

func TestNormalizeToolName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"context_for_task", "context_for_task"},
		{"acr_context_for_task", "context_for_task"},
		{"acr.context_for_task", "context_for_task"},
		{"acr*context_for_task", "context_for_task"},
		{"acr:context_for_task", "context_for_task"},
		{"acr-context_for_task", "context_for_task"},
		{"acr/context_for_task", "context_for_task"},
		{"source_evidence", "source_evidence"},
		{"acr_source_evidence", "source_evidence"},
		{"acr_record_episode", "record_episode"},
		{"some_other_tool", "some_other_tool"},
		{"acrcontext_for_task", "acrcontext_for_task"}, // no separator: not normalized
	}
	for _, tc := range cases {
		if got := normalizeToolName(tc.in); got != tc.want {
			t.Errorf("normalizeToolName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
