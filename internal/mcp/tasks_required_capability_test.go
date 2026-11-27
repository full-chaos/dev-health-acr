package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// wireError is the JSON-RPC error object of one reply (a JSON body or the
// last SSE data line), with its data field kept raw.
func wireError(t *testing.T, reply taskRPCReply) (code int64, message string, data json.RawMessage) {
	t.Helper()
	payload := reply.raw
	if s := string(reply.raw); strings.HasPrefix(s, "event:") {
		for _, line := range strings.Split(s, "\n") {
			if rest, ok := strings.CutPrefix(line, "data: "); ok {
				payload = []byte(rest)
			}
		}
	}
	var envelope struct {
		Error *struct {
			Code    int64           `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Error == nil {
		t.Fatalf("want a JSON-RPC error, got %s", reply.raw)
	}
	return envelope.Error.Code, envelope.Error.Message, envelope.Error.Data
}

const requiredTasksCapabilityData = `{"requiredCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}`

func TestTasksMissingCapabilityNamesTheRequiredExtension(t *testing.T) {
	s := newTasksStack(t, true)
	task := s.createTask(t, nil)
	for _, method := range []string{"tasks/get", "tasks/cancel", "tasks/update"} {
		t.Run(method, func(t *testing.T) {
			reply := s.rpc(t, s.caller.token, method, map[string]any{"taskId": task.TaskID, "_meta": tasksMeta("2026-07-28", false)})
			code, message, data := wireError(t, reply)
			if code != -32021 || message != "Missing required client capability: io.modelcontextprotocol/tasks" {
				t.Fatalf("%s: code %d message %q, want -32021 and today's message", method, code, message)
			}
			if string(data) == "" || string(data) == "null" {
				t.Fatalf("%s: error has no data, want %s: %s", method, requiredTasksCapabilityData, reply.raw)
			}
			var got, want any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal([]byte(requiredTasksCapabilityData), &want)
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(want)
			if string(g) != string(w) {
				t.Fatalf("%s: error.data %s, want %s", method, g, w)
			}
		})
	}
}
