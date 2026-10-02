package interpreq

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Fixture requests (fictional names). They cover conversation turns, scope
// hints, receipts and a bare question.
var fixtureRequests = []string{
	`{"question":"Why is the Harbor Lights team slower than last quarter?","conversation":[{"turn_id":"t1","role":"user","content":"How is the Harbor Lights team doing?","created_at":"2026-09-01T10:00:00Z"},{"turn_id":"t2","role":"assistant","content":"Harbor Lights appears to have more open blockers than usual.","created_at":"2026-09-01T10:00:05Z"}],"requested_scope":{"subject_hints":[{"kind":"team","label":"Harbor Lights","source":"ui_selection"}]},"time_context":{"axis":"current"},"prior_subject_receipts":[{"result_id":"res_0000demo01","receipt_id":"rcpt_0000demo01"}]}`,
	`{"question":"What is blocking the Kestrel project?","time_context":{"axis":"current"}}`,
	`{"question":"Which repositories need attention <now> & why?","time_context":{"axis":"current"}}`,
}

// builtHelper is the interpretation helper the tests run. TestMain builds it
// from ../../gohelper for every test run: a helper that does not build fails
// the run, and no test depends on a binary that someone built earlier.
var builtHelper string

func TestMain(m *testing.M) {
	os.Exit(runWithHelper(m))
}

func runWithHelper(m *testing.M) int {
	dir, err := os.MkdirTemp("", "interp-helper-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "interpreq tests: temp dir for the helper: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "interp-helper")
	build := exec.Command("go", "build", "-o", bin, "../../gohelper")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "interpreq tests: the interpretation helper does not build: %v\n%s", err, out)
		return 1
	}
	builtHelper = bin
	return m.Run()
}

func helperBinary(t *testing.T) string {
	t.Helper()
	if builtHelper == "" {
		// A missing helper is a failed measurement, never a skip.
		t.Fatal("the helper was not built for this test run")
	}
	return builtHelper
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
