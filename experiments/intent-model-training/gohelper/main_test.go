package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func runCLI(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestCLIUsageErrorsExitTwo(t *testing.T) {
	if code, _, _ := runCLI(t, ""); code != 2 {
		t.Fatalf("no subcommand: exit %d", code)
	}
	if code, _, _ := runCLI(t, "", "nope"); code != 2 {
		t.Fatalf("unknown subcommand: exit %d", code)
	}
	if code, _, stderr := runCLI(t, `{"raw":"{}"}`, "validate"); code != 2 || !strings.Contains(stderr, "request") {
		t.Fatalf("missing request: exit %d stderr %q", code, stderr)
	}
	if code, _, _ := runCLI(t, `{"request":{"question":"q","time_context":{"axis":"current"}}}`, "validate"); code != 2 {
		t.Fatalf("missing raw: exit %d", code)
	}
}

func TestCLIInvalidTargetIsExitZero(t *testing.T) {
	input, _ := json.Marshal(map[string]any{"raw": "not json", "request": json.RawMessage(testRequest)})
	code, stdout, _ := runCLI(t, string(input), "validate")
	if code != 0 {
		t.Fatalf("an invalid target is data: exit %d", code)
	}
	var env Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil || env.JSONOK {
		t.Fatalf("bad envelope: %v %s", err, stdout)
	}
}

func TestCLIValidateBatchKeepsOneLinePerID(t *testing.T) {
	lines := []string{}
	for _, item := range []struct {
		id  any
		raw string
		req string
	}{
		{"a", validTarget, testRequest},
		{2, "not json", testRequest},
		{"c", validTarget, `{"question":"q","time_context":{"axis":"current"},"unexpected":true}`},
	} {
		encoded, _ := json.Marshal(map[string]any{"id": item.id, "raw": item.raw, "request": json.RawMessage(item.req)})
		lines = append(lines, string(encoded))
	}
	code, stdout, stderr := runCLI(t, strings.Join(lines, "\n")+"\n", "validate-batch")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	out := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(out) != 3 {
		t.Fatalf("want 3 result lines, got %d", len(out))
	}
	var first, second, third map[string]any
	for i, target := range []*map[string]any{&first, &second, &third} {
		if err := json.Unmarshal([]byte(out[i]), target); err != nil {
			t.Fatal(err)
		}
	}
	if first["id"] != "a" || first["strict_ok"] != true {
		t.Fatalf("line 1: %v", first)
	}
	if second["id"] != float64(2) || second["json_ok"] != false {
		t.Fatalf("line 2: %v", second)
	}
	if third["id"] != "c" || third["helper_error"] == nil {
		t.Fatalf("line 3 should carry a per-line helper_error: %v", third)
	}
}

func TestCLIUnknownTransportIsUsageError(t *testing.T) {
	input, _ := json.Marshal(map[string]any{"raw": validTarget, "request": json.RawMessage(testRequest), "transport": "carrier-pigeon"})
	if code, _, stderr := runCLI(t, string(input), "validate"); code != 2 || !strings.Contains(stderr, "transport") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestCLIInfoSubcommands(t *testing.T) {
	for _, sub := range []string{"version", "system-prompt", "system-message", "schema"} {
		code, stdout, stderr := runCLI(t, "", sub)
		if code != 0 {
			t.Fatalf("%s: exit %d %s", sub, code, stderr)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
	}
}
