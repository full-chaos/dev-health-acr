package falkorgraph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

const chaos11095Question = "self-hosted CI executor setup repository"

func chaos11095Run(t *testing.T, embedErr error, telemetry func(*bytes.Buffer, *[]string) GraphTelemetry) (*bytes.Buffer, []string) {
	t.Helper()
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		t.Fatal("the graph must not be read after the query embed failed")
		return nil, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}, err: embedErr}, 0.55)
	var logs bytes.Buffer
	var causes []string
	adapter.config.Telemetry = telemetry(&logs, &causes)
	got, truncated, degraded, err := adapter.kindScopedSemanticSearchNodes(context.Background(), "k", "org-1", chaos11095Question, contextfabric.SubjectRepository, 20, temporalFilter{})
	if err != nil || truncated || !degraded || len(got) != 0 {
		t.Fatalf("got=%d truncated=%v degraded=%v err=%v, want a degraded empty result", len(got), truncated, degraded, err)
	}
	return &logs, causes
}

func chaos11095Sink(logs *bytes.Buffer, causes *[]string) GraphTelemetry {
	return SlogTelemetry{
		Logger:             slog.New(slog.NewJSONHandler(logs, nil)),
		QueryEmbedFallback: func(_ context.Context, cause string) { *causes = append(*causes, cause) },
	}
}

func chaos11095Lines(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("log line is not JSON: %v: %s", err, raw)
		}
		out = append(out, line)
	}
	return out
}

func TestChaos11095_AQueryEmbedTimeoutFallsBackLoudlyOnceAndCounts(t *testing.T) {
	logs, causes := chaos11095Run(t, context.DeadlineExceeded, chaos11095Sink)
	lines := chaos11095Lines(t, logs)
	if len(lines) != 1 || lines[0]["level"] != "WARN" {
		t.Fatalf("log lines = %v, want exactly ONE WARN", lines)
	}
	if lines[0]["cause"] != "timeout" || lines[0]["org_id"] != "org-1" {
		t.Fatalf("WARN fields = %v, want cause=timeout org_id=org-1", lines[0])
	}
	if raw := logs.String(); strings.Contains(raw, "executor") || strings.Contains(raw, "keysecret") {
		t.Fatalf("the WARN carries question text or a secret: %s", raw)
	}
	if len(causes) != 1 || causes[0] != "timeout" {
		t.Fatalf("counter calls = %v, want one timeout", causes)
	}
}

func TestChaos11095_AQueryEmbedErrorCountsAsErrorNotTimeout(t *testing.T) {
	logs, causes := chaos11095Run(t, errors.New("provider returned 500"), chaos11095Sink)
	if lines := chaos11095Lines(t, logs); len(lines) != 1 || lines[0]["cause"] != "error" {
		t.Fatalf("log lines = %v, want one WARN with cause=error", lines)
	}
	if len(causes) != 1 || causes[0] != "error" {
		t.Fatalf("counter calls = %v, want one error", causes)
	}
}

func TestChaos11095_ASinkWithoutTheFallbackHookKeepsTheOldDegradedSignal(t *testing.T) {
	recording := &recordingTelemetry{}
	chaos11095Run(t, context.DeadlineExceeded, func(*bytes.Buffer, *[]string) GraphTelemetry { return recording })
	if recording.degraded != 1 {
		t.Fatalf("RecordVectorRetrievalDegraded calls = %d, want 1 for a sink that has no fallback hook", recording.degraded)
	}
}
