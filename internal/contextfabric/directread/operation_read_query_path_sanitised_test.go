package directread_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// controlPathReporter is a QueryClient whose reported path holds control characters.
type controlPathReporter struct {
	*directread.HTTPQueryClient
	path string
}

func (c controlPathReporter) QueryPath() string { return c.path }

// A custom QueryPathReporter cannot forge or break the operation read record:
// the Info record carries a sanitised query_path and still certifies against
// eventspec.OperationRead.
func TestOperationReadInfoRecordSanitisesACustomReportersQueryPath(t *testing.T) {
	upstream := newPathRecorder(t)
	base, err := directread.NewHTTPQueryClientWithPath(upstream.serve.URL, 5*time.Second, "/query")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	cat, _ := directread.DefaultCatalogue()
	runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{
		Catalogue: cat,
		Gate:      directread.NewSubjectGate(newOpGraph(), nil),
		Client:    controlPathReporter{HTTPQueryClient: base, path: "/query\n{\"msg\":\"forged\"}\r\x1b[31m\x00"},
		Logger:    logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "securityAlerts"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reads := 0
	for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			t.Fatalf("a log line is not a JSON record %q: %v", raw, err)
		}
		if rec["msg"] == "forged" {
			t.Fatalf("a forged record reached the log: %s", raw)
		}
		if rec["msg"] == directread.OperationReadLogMessage {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("operation read records = %d, want 1: %s", reads, logs.String())
	}
	for _, raw := range []string{`\n`, `\r`, `\u001b`, `\u0000`} {
		if strings.Contains(logs.String(), raw) {
			t.Fatalf("a control character reached the record (%s): %s", raw, logs.String())
		}
	}
	line := opLineOf(t, logs.String(), directread.OperationReadLogMessage)
	opCertify(t, line, map[string]any{
		"org_id":     opOrgA,
		"operation":  "securityAlerts",
		"query_path": "/query?{\"msg\":\"forged\"}??[31m?",
	})
}
