package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestReadinessObserveSanitizesRequestID is the CHAOS-5558 real-handler pin
// for ReadinessTransitionLogger.Observe's own choke point (the emission
// site both InfoContext calls in Observe share): a malicious requestID --
// the shape a spoofed X-Request-ID header could carry before app.go's own
// middleware validation, and exactly the shape CodeQL's go/log-injection
// query traces regardless of that validation, since it does not recognize
// a hand-rolled loop as a barrier -- must not fracture the emitted JSON
// line into two, through a REAL slog.JSONHandler at production shape.
func TestReadinessObserveSanitizesRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	tracker := NewReadinessTransitionLogger()
	const malicious = "evil\nFAKE_LOG_LINE=injected\r\n"

	// First observation: both the per-check AND the aggregate transition
	// fire (NewReadinessTransitionLogger's own guarantee -- see its doc
	// comment), so exactly two lines are expected, not one.
	tracker.Observe(context.Background(), logger, malicious, "ready",
		[]ReadinessCheckObservation{{Name: "storage", Status: "ready"}})

	text := strings.TrimRight(buf.String(), "\n")
	if text == "" {
		t.Fatal("Observe wrote nothing")
	}
	lines := strings.Split(text, "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d log line(s), want exactly 2 (one check transition, one aggregate "+
			"transition) -- a forged line break would show up as extra lines: %q", len(lines), buf.String())
	}
	sawRequestID := false
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line is not valid JSON (a forged line break split the record): %q: %v", line, err)
		}
		requestID, ok := record["request_id"].(string)
		if !ok {
			t.Fatalf("record has no string request_id field: %v", record)
		}
		sawRequestID = true
		if strings.ContainsAny(requestID, "\n\r") {
			t.Fatalf("request_id = %q still carries a line break", requestID)
		}
	}
	if !sawRequestID {
		t.Fatal("neither emitted line carried a request_id field")
	}
}
