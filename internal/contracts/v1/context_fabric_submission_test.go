package v1

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

// CHAOS-11913: result_id is present exactly when the state is done, in the
// Go validator and in the published schema alike.
func TestSubmissionStatusResultIDIsPresentExactlyWhenDone(t *testing.T) {
	base := ContextFabricSubmissionStatus{
		SchemaVersion: ContextFabricSubmissionStatusSchema, SubmissionID: "submission-0001", State: ContextFabricSubmissionDone,
		ReservedResultID: "result_status_0001", ResultID: "result_status_0001", ThreadID: "thread-0001", UpdatedAt: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
	}
	for name, tc := range map[string]struct {
		mutate func(*ContextFabricSubmissionStatus)
		valid  bool
	}{
		"done with result":     {func(*ContextFabricSubmissionStatus) {}, true},
		"done without result":  {func(s *ContextFabricSubmissionStatus) { s.ResultID = "" }, false},
		"running with result":  {func(s *ContextFabricSubmissionStatus) { s.State = ContextFabricSubmissionRunning }, false},
		"running, no result":   {func(s *ContextFabricSubmissionStatus) { s.State, s.ResultID = ContextFabricSubmissionRunning, "" }, true},
		"unsaved with result":  {func(s *ContextFabricSubmissionStatus) { s.State = ContextFabricSubmissionDoneUnsaved }, false},
		"unknown state":        {func(s *ContextFabricSubmissionStatus) { s.State, s.ResultID = "lost", "" }, false},
		"malformed thread":     {func(s *ContextFabricSubmissionStatus) { s.ThreadID = "bad thread" }, false},
		"malformed submission": {func(s *ContextFabricSubmissionStatus) { s.SubmissionID = "short" }, false},
	} {
		status := base
		tc.mutate(&status)
		goErr := status.Validate()
		body, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		schemaErr := contractcheck.ValidateSerializedStrict("", "context_fabric_submission_status.v1.schema.json", body)
		if (goErr == nil) != tc.valid || (schemaErr == nil) != tc.valid {
			t.Errorf("%s: Validate = %v, schema = %v, want valid %t", name, goErr, schemaErr, tc.valid)
		}
	}
}
