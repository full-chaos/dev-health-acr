package v1

import (
	"strings"
	"testing"
	"time"
)

func TestSubmissionStatusRunDetailValidate(t *testing.T) {
	base := func(state ContextFabricSubmissionState) ContextFabricSubmissionStatus {
		status := ContextFabricSubmissionStatus{
			SchemaVersion: ContextFabricSubmissionStatusSchema, SubmissionID: "submission-terminal",
			State: state, ReservedResultID: "result_terminal_01", UpdatedAt: time.Date(2026, 11, 20, 4, 0, 0, 0, time.UTC),
		}
		if state == ContextFabricSubmissionDone {
			status.ResultID = status.ReservedResultID
		}
		return status
	}
	terminal := func(kind ContextFabricSubmissionTerminalKind, code string) *ContextFabricSubmissionTerminal {
		return &ContextFabricSubmissionTerminal{Kind: kind, ErrorCode: code}
	}
	for _, tc := range []struct {
		name      string
		state     ContextFabricSubmissionState
		lastStage ContextFabricInvestigationStage
		terminal  *ContextFabricSubmissionTerminal
		refusal   string
	}{
		{"running, no detail", ContextFabricSubmissionRunning, "", nil, ""},
		{"running, last stage", ContextFabricSubmissionRunning, ContextFabricStageSynthesizing, nil, ""},
		{"running, last stage outside the vocabulary", ContextFabricSubmissionRunning, "thinking", nil, "last_stage"},
		{"running with a terminal", ContextFabricSubmissionRunning, "", terminal(ContextFabricSubmissionTerminalResult, ""), "absent while the state is running"},
		{"done, result", ContextFabricSubmissionDone, ContextFabricStageValidating, terminal(ContextFabricSubmissionTerminalResult, ""), ""},
		{"done_unsaved, unsaved", ContextFabricSubmissionDoneUnsaved, "", terminal(ContextFabricSubmissionTerminalUnsaved, ""), ""},
		{"abandoned, canceled", ContextFabricSubmissionAbandoned, "", terminal(ContextFabricSubmissionTerminalError, ErrorCodeCanceled), ""},
		{"abandoned, run_lost", ContextFabricSubmissionAbandoned, "", terminal(ContextFabricSubmissionTerminalError, ErrorCodeRunLost), ""},
		{"abandoned, no terminal yet", ContextFabricSubmissionAbandoned, "", nil, ""},
		{"kind outside the vocabulary", ContextFabricSubmissionAbandoned, "", terminal("crashed", ""), "terminal.kind"},
		{"empty kind", ContextFabricSubmissionAbandoned, "", terminal("", ""), "terminal.kind"},
		{"error kind without a code", ContextFabricSubmissionAbandoned, "", terminal(ContextFabricSubmissionTerminalError, ""), "error_code is present exactly"},
		{"result kind with a code", ContextFabricSubmissionDone, "", terminal(ContextFabricSubmissionTerminalResult, ErrorCodeCanceled), "error_code is present exactly"},
		{"code outside error.v1", ContextFabricSubmissionAbandoned, "", terminal(ContextFabricSubmissionTerminalError, "exploded"), "not an error.v1 code"},
	} {
		status := base(tc.state)
		status.LastStage, status.Terminal = tc.lastStage, tc.terminal
		err := status.Validate()
		switch {
		case tc.refusal == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal)):
			t.Errorf("%s: err = %v, want a refusal naming %q", tc.name, err, tc.refusal)
		}
	}
}
