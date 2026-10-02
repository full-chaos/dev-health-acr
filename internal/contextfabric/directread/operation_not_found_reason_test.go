package directread_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

func opLogLines(t *testing.T, h *opHarness, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(h.logs.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

func TestRunOperationNotFoundReadsTheTypedUpstreamReason(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	ops := cat.Operations(directread.CallerUnrestricted)
	op := ops[0]
	cases := []struct {
		name, body, reason string
	}{
		{"root_not_enabled", `{"errors":[{"message":"m","extensions":{"code":"MCP_REFUSED","reason":"root_field_not_enabled"}}]}`, directread.ListenerNotFoundRootNotEnabled},
		{"non_json", `SECRET-UPSTREAM not registered`, directread.ListenerNotFoundUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOpHarness(t, func(opRecorded) (int, string) { return 404, tc.body }, opHarnessOptions{})
			resp := h.run(t, opUnrestricted(opOrgA), op.Name, opMinimalVariables(t, op))
			if resp.Call != directread.CallOperationUnavailable || len(resp.Errors) != 1 || resp.Errors[0].Class != directread.UpstreamNotFound || resp.Refusal != nil {
				t.Fatalf("answer changed: %+v", resp)
			}
			lines := opLogLines(t, h, directread.OperationNotFoundLog)
			if len(lines) != 1 {
				t.Fatalf("want one not-found line, got %d in %s", len(lines), h.logs.String())
			}
			if lines[0]["level"] != "WARN" || lines[0]["listener_reason"] != tc.reason || lines[0]["operation"] != op.Name {
				t.Fatalf("log line: %v", lines[0])
			}
			if strings.Contains(h.logs.String(), "SECRET-UPSTREAM") || strings.Contains(h.logs.String(), `"level":"ERROR"`) {
				t.Fatalf("body echoed or ERROR logged: %s", h.logs.String())
			}
		})
	}
}
