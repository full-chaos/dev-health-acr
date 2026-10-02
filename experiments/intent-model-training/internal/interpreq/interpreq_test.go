package interpreq

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Fixture requests (fictional names). They cover conversation turns, scope
// hints, receipts and a bare question.
var fixtureRequests = []string{
	`{"question":"Why is the Harbor Lights team slower than last quarter?","conversation":[{"turn_id":"t1","role":"user","content":"How is the Harbor Lights team doing?","created_at":"2026-09-01T10:00:00Z"},{"turn_id":"t2","role":"assistant","content":"Harbor Lights appears to have more open blockers than usual.","created_at":"2026-09-01T10:00:05Z"}],"requested_scope":{"subject_hints":[{"kind":"team","label":"Harbor Lights","source":"ui_selection"}]},"time_context":{"axis":"current"},"prior_subject_receipts":[{"result_id":"res_0000demo01","receipt_id":"rcpt_0000demo01"}]}`,
	`{"question":"What is blocking the Kestrel project?","time_context":{"axis":"current"}}`,
	`{"question":"Which repositories need attention <now> & why?","time_context":{"axis":"current"}}`,
}

func helperBinary(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	bin := filepath.Join(filepath.Dir(file), "..", "..", "bin", "interp-helper")
	if _, err := os.Stat(bin); err != nil {
		// A missing helper is a failed measurement, never a skip.
		t.Fatalf("helper binary missing at %s: build it with the INTERFACES §6 command", bin)
	}
	return bin
}

// T14: interpreq's request_sha256 and input_sha256 equal the helper's render.
func TestRenderMatchesHelper(t *testing.T) {
	bin := helperBinary(t)
	for i, raw := range fixtureRequests {
		ours, err := Render([]byte(raw))
		if err != nil {
			t.Fatalf("fixture %d: %v", i, err)
		}
		input, _ := json.Marshal(map[string]json.RawMessage{"request": json.RawMessage(raw)})
		cmd := exec.Command(bin, "render")
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("fixture %d: helper render: %v", i, err)
		}
		var theirs struct {
			SHA256        string `json:"sha256"`
			RequestSHA256 string `json:"request_sha256"`
			UserPayload   string `json:"user_payload"`
		}
		if err := json.Unmarshal(out, &theirs); err != nil {
			t.Fatalf("fixture %d: %v", i, err)
		}
		if ours.InputSHA256 != theirs.SHA256 || ours.RequestSHA256 != theirs.RequestSHA256 || ours.Payload != theirs.UserPayload {
			t.Fatalf("fixture %d: interpreq (%s, %s) != helper (%s, %s)", i, ours.InputSHA256, ours.RequestSHA256, theirs.SHA256, theirs.RequestSHA256)
		}
	}
}

func TestDecodeRejectsDuplicateUnknownAndTrailing(t *testing.T) {
	for _, raw := range []string{
		`{"question":"q","question":"r","time_context":{"axis":"current"}}`,
		`{"question":"q","time_context":{"axis":"current"},"extra":1}`,
		`{"question":"q","time_context":{"axis":"current"}} {}`,
		``,
		`null`,
	} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
