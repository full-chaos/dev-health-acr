package projectionrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// Codex round-4 F3, RED->GREEN: the observer must log a CLASSIFICATION, never
// the error's own text. Outcome.Err is whatever a source, checkpoint store, or
// backend returned; a ClickHouse driver error or Postgres checkpoint error
// arrives unbounded and would put raw dependency text into production
// telemetry.
func TestR4_F3_ObserverNeverLogsRawErrorText(t *testing.T) {
	const secret = "dial tcp 10.1.2.3:9000: connect: connection refused (user=svc_acr password=hunter2)"
	var buffer bytes.Buffer
	observer := SlogObserver{Logger: slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))}

	observer.ObserveProjectionOutcome(Outcome{
		OrgID: "org_1", Source: "devhealth", Duration: 3 * time.Second,
		Err: fmt.Errorf("read batch: %w", errors.New(secret)),
	})

	logged := buffer.String()
	for _, leak := range []string{"hunter2", "10.1.2.3", "password", "connection refused", "dial tcp"} {
		if strings.Contains(logged, leak) {
			t.Fatalf("raw dependency error text leaked into telemetry (%q): %s", leak, logged)
		}
	}
	// It must still say SOMETHING actionable.
	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(logged)), &record); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}
	if record["failure_class"] != failureClassUnclassified {
		t.Fatalf("an unrecognized error must classify as %q, got %v", failureClassUnclassified, record["failure_class"])
	}
	if record["org_id"] != "org_1" {
		t.Fatalf("the organization must still be identified, got %v", record["org_id"])
	}
}

// The vocabulary is closed and each known failure maps to its own class, so
// "unclassified" genuinely means "a failure this vocabulary does not name"
// rather than "classification is not wired".
func TestR4_F3_KnownFailuresClassifyDistinctly(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{context.Canceled, failureClassCanceled},
		{context.DeadlineExceeded, failureClassCanceled},
		{contextfabric.ErrProjectionConflict, failureClassConflict},
		{ErrOrgLocked, failureClassLocked},
		{contextfabric.ErrProjectionSourceVersionChanged, failureClassRebuildNeeded},
		{contextfabric.ErrRateLimited, failureClassRateLimited},
		{contextfabric.ErrUnavailable, failureClassUnavailable},
		{contextfabric.ErrInvalidResult, failureClassInvalidResult},
		{errors.New("something new"), failureClassUnclassified},
		{nil, ""},
	}
	for _, testCase := range cases {
		// Wrapped, because a real tick failure always arrives wrapped.
		err := testCase.err
		if err != nil {
			err = fmt.Errorf("tick: %w", err)
		}
		if got := classifyOutcomeError(err); got != testCase.want {
			t.Fatalf("classifyOutcomeError(%v) = %q, want %q", testCase.err, got, testCase.want)
		}
	}
}

// A successful tick logs at debug and carries no failure class.
func TestR4_F3_SuccessfulTickLogsNoFailureClass(t *testing.T) {
	var buffer bytes.Buffer
	observer := SlogObserver{Logger: slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	observer.ObserveProjectionOutcome(Outcome{OrgID: "org_1", Source: "devhealth", Duration: time.Second})
	if strings.Contains(buffer.String(), "failure_class") {
		t.Fatalf("a successful tick must carry no failure class: %s", buffer.String())
	}
}
