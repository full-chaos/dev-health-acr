package mcp_test

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ext-tasks 2026-07-28 Task.ttlMs: "Time-to-live duration from creation in
// integer milliseconds, null for unlimited. The server may discard the task
// after the TTL elapses." A task is an acr-api submission with no thread;
// with ACR_ASK_DEV_RETENTION_ENABLED on, acr-api answers its status read as
// unknown from admission + 24 hours (the retention-0 window,
// TestASubmissionWithNoThreadClosesTwentyFourHoursAfterItsAdmission in
// internal/api). So ttlMs may not be null, and the task may not be gone
// before createdAt + ttlMs.
func TestTaskTTLCoversTheUnthreadedSubmissionRetention(t *testing.T) {
	s := newTasksStack(t, true)
	task := s.createTask(t, nil)
	admitted := s.fake.updatedAt(submissionOf(task.TaskID))
	discardedAt := contractsv1.ContextFabricThreadExpiresAt(admitted, contractsv1.ContextFabricThreadRetentionDay)

	working := decodeTask(t, s.getTask(t, s.caller.token, task.TaskID))

	s.fake.discard(submissionOf(task.TaskID))
	if gone := s.getTask(t, s.caller.token, task.TaskID); gone.Error == nil || gone.Error.Code != -32602 {
		t.Fatalf("a discarded submission answered %s, want -32602", gone.raw)
	}
	for _, answer := range []struct {
		name string
		task taskWire
	}{{"tools/call", task}, {"tasks/get", working}} {
		if answer.task.TTLMs == nil {
			t.Fatalf("%s answered ttlMs null (unlimited), but the submission is discarded at admission + 24 hours", answer.name)
		}
		created, err := time.Parse(time.RFC3339, answer.task.CreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		if end := created.Add(time.Duration(*answer.task.TTLMs) * time.Millisecond); end.After(discardedAt) {
			t.Fatalf("%s: createdAt + ttlMs = %s is after the discard at %s", answer.name, end, discardedAt)
		}
	}
}
