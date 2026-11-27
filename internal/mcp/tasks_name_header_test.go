package mcp_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// taskCall posts one tasks/* request with an explicit Mcp-Name header
// ("" sends none) and returns the HTTP status and body.
func (s *tasksStack) taskCall(t *testing.T, method, taskID, nameHeader string) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": map[string]any{"taskId": taskID, "_meta": tasksMeta("2026-07-28", true)}})
	if err != nil {
		t.Fatal(err)
	}
	header := bearerHeader(s.caller.token)
	header.Set("Mcp-Method", method)
	if nameHeader != "" {
		header.Set("Mcp-Name", nameHeader)
	}
	resp := postMCP(t, s.e, http.MethodPost, body, header)
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}

func TestTasksMcpNameMismatchIsRefusedLikeToolsCall(t *testing.T) {
	s := newTasksStack(t, true)
	task := s.createTask(t, nil)
	for _, method := range []string{"tasks/get", "tasks/cancel", "tasks/update"} {
		t.Run(method, func(t *testing.T) {
			status, raw := s.taskCall(t, method, task.TaskID, "mcpt.other")
			want := "header mismatch: Mcp-Name header value 'mcpt.other' does not match body value '" + task.TaskID + "'"
			if status != http.StatusBadRequest || !strings.Contains(raw, `"code":-32020`) || !strings.Contains(raw, want) {
				t.Fatalf("%s with a mismatched Mcp-Name: http %d %s, want 400 -32020 %q", method, status, raw, want)
			}
		})
	}
}

// Today's answers stay: a matching header, and no header, are served.
func TestTasksMcpNameMatchingOrAbsentKeepsTodaysAnswer(t *testing.T) {
	s := newTasksStack(t, true)
	task := s.createTask(t, nil)
	for name, header := range map[string]string{"matching": task.TaskID, "absent": ""} {
		for _, method := range []string{"tasks/get", "tasks/cancel", "tasks/update"} {
			status, raw := s.taskCall(t, method, task.TaskID, header)
			if status != http.StatusOK || strings.Contains(raw, `"error"`) || !strings.Contains(raw, `"resultType":"complete"`) {
				t.Fatalf("%s %s Mcp-Name: http %d %s, want the 200 answer", method, name, status, raw)
			}
		}
	}
}
