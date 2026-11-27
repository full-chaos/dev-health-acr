package mcp_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// listen posts one subscriptions/listen and returns the HTTP status and body.
func (s *tasksStack) listen(t *testing.T, notifications map[string]any, meta map[string]any) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "subscriptions/listen", "params": map[string]any{"notifications": notifications, "_meta": meta}})
	if err != nil {
		t.Fatal(err)
	}
	header := bearerHeader(s.caller.token)
	header.Set("Mcp-Method", "subscriptions/listen")
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

func TestTasksListenWithTaskIDsNeedsTheExtension(t *testing.T) {
	s := newTasksStack(t, true)
	task := s.createTask(t, nil)
	status, raw := s.listen(t, map[string]any{"taskIds": []string{task.TaskID}}, tasksMeta("2026-07-28", false))
	reply := taskRPCReply{httpStatus: status, raw: []byte(raw)}
	if status != http.StatusBadRequest {
		t.Fatalf("listen with taskIds and no extension: http %d %s, want 400 -32021", status, raw)
	}
	code, message, data := wireError(t, reply)
	if code != -32021 || message != "Missing required client capability: io.modelcontextprotocol/tasks" || string(data) != requiredTasksCapabilityData {
		t.Fatalf("listen answered %s, want -32021 with %s", raw, requiredTasksCapabilityData)
	}
}

// Today's answers stay: no taskIds (with or without the extension), and
// taskIds with the extension, are acknowledged and not refused.
func TestTasksListenOtherRequestsKeepTodaysAnswer(t *testing.T) {
	s := newTasksStack(t, true)
	task := s.createTask(t, nil)
	for name, tc := range map[string]struct {
		notifications map[string]any
		declare       bool
	}{
		"no taskIds, no extension":    {map[string]any{"toolsListChanged": true}, false},
		"empty taskIds, no extension": {map[string]any{"taskIds": []string{}}, false},
		"no taskIds, extension":       {map[string]any{"toolsListChanged": true}, true},
		"taskIds, extension":          {map[string]any{"taskIds": []string{task.TaskID}}, true},
	} {
		status, raw := s.listen(t, tc.notifications, tasksMeta("2026-07-28", tc.declare))
		if status != http.StatusOK || !strings.Contains(raw, "notifications/subscriptions/acknowledged") || strings.Contains(raw, `"error"`) {
			t.Fatalf("%s: http %d %s, want the 200 acknowledged stream", name, status, raw)
		}
	}
}

func TestTasksListenFlagOffIgnoresTaskIDs(t *testing.T) {
	s := newTasksStack(t, false)
	status, raw := s.listen(t, map[string]any{"taskIds": []string{"mcpt.x"}}, tasksMeta("2026-07-28", false))
	if status != http.StatusOK || strings.Contains(raw, `"error"`) {
		t.Fatalf("flag off: http %d %s, want the unchanged 200 stream", status, raw)
	}
}
