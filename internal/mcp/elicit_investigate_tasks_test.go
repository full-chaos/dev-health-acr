package mcp_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Tasks accept-first path maps the request without the window question:
// one create, no synchronous hosted call, no prompt (hosted HTTP has no STDIO
// elicitation transport).
func TestTasksNeverAskTheWindowQuestion(t *testing.T) {
	s := newTasksStack(t, true)
	reply := s.rpc(t, s.caller.token, "tools/call", investigateParams(map[string]any{"question": "Which repository carried the most operational work for last month?"}, tasksMeta("2026-07-28", true)))
	require.Contains(t, string(reply.Result), `"resultType":"task"`, string(reply.raw))
	require.Equal(t, 1, s.fake.createCount())
	require.Equal(t, 0, s.fake.syncCount())
	require.NotContains(t, string(s.e.logs.Bytes()), "mcp elicitation outcome", "the window question is never reached from Tasks")
	require.False(t, strings.Contains(string(s.e.logs.Bytes()), "investigate_question_window"))
}
