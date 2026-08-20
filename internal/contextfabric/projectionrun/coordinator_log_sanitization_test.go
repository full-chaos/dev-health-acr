package projectionrun_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// Codex round-5 P1: the OBSERVER emitted only a bounded failure class, but the
// coordinator then logged the same error's raw text at its OWN log site --
// a second record, beside the sanitized one, carrying whatever a source or
// checkpoint store returned. A DSN-shaped ClickHouse or episode-source failure
// therefore reached production telemetry with host, port and credentials
// intact.
//
// The previous observer test passed VACUOUSLY: it drove SlogObserver directly
// and so never exercised the follow-on log. This test therefore asserts at the
// COORDINATOR level -- it drives a real tick through a real Coordinator with a
// real logger and inspects the process's actual log output, which is the only
// place the bypass was visible.
func TestR5_CoordinatorNeverLogsRawSourceErrorText(t *testing.T) {
	t.Parallel()

	// Shaped exactly like a driver failure an operator would really see.
	const dsnShaped = "dial tcp 10.4.5.6:9440: connect: connection refused " +
		"(dsn=clickhouse://svc_acr:hunter2@warehouse.internal:9440/devhealth?secure=true)"

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, sanitizationTestHandlerOptions()))

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-a", Source: &fakeSource{name: "source-a", err: errors.New(dsnShaped)}},
		},
		Backend: newFakeBackend(), Checkpoints: newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(), Concurrency: 1, Logger: logger,
		// The real production pairing: a real observer alongside the
		// coordinator's own logging, so BOTH records land in one buffer.
		Observer: projectionrun.SlogObserver{Logger: logger},
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	logged := buffer.String()
	if strings.TrimSpace(logged) == "" {
		t.Fatal("the failing tick produced no log output at all; this test would pass vacuously")
	}
	for _, leak := range []string{
		"hunter2", "svc_acr", "10.4.5.6", "warehouse.internal", "9440",
		"clickhouse://", "dsn=", "connection refused", "dial tcp",
	} {
		if strings.Contains(logged, leak) {
			t.Fatalf("raw source error text leaked into coordinator telemetry (%q):\n%s", leak, logged)
		}
	}
	// And it must still be actionable: the failure has to be reported,
	// classified, and attributed to an organization.
	if !strings.Contains(logged, "failure_class") {
		t.Fatalf("a failing tick must report a bounded failure class:\n%s", logged)
	}
	if !strings.Contains(logged, "org-1") {
		t.Fatalf("a failing tick must still identify the organization:\n%s", logged)
	}
}

// The same guarantee for the lock/rebuild-marker paths, which log from their
// own sites inside the tick rather than through the observer.
func TestR5_CoordinatorNeverLogsRawLockOrRebuildErrorText(t *testing.T) {
	t.Parallel()

	const secret = "pq: password authentication failed for user \"acr_projector\" (host=db.internal port=5432)"

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, sanitizationTestHandlerOptions()))

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-1"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend: newFakeBackend(), Checkpoints: newFakeCheckpointStore(),
		// A rebuild-marker store whose CHECK fails with a credential-bearing
		// error drives the "check rebuild marker failed; skipping tick" site.
		RebuildMarkers: rebuildMarkerFailingCheck(errors.New(secret)),
		Concurrency:    1, Logger: logger, Observer: projectionrun.SlogObserver{Logger: logger},
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	logged := buffer.String()
	if strings.TrimSpace(logged) == "" {
		t.Fatal("the failing tick produced no log output at all; this test would pass vacuously")
	}
	for _, leak := range []string{"acr_projector", "db.internal", "5432", "password authentication", "pq:"} {
		if strings.Contains(logged, leak) {
			t.Fatalf("raw rebuild-marker error text leaked into coordinator telemetry (%q):\n%s", leak, logged)
		}
	}
}

// rebuildMarkerFailingCheck reuses the existing fake rather than adding a
// second one, so this test exercises the same type production tests do.
func rebuildMarkerFailingCheck(err error) *fakeRebuildMarker {
	marker := newFakeRebuildMarker()
	marker.checkErr = err
	return marker
}

// sanitizationTestHandlerOptions drops slog's own time attribute before
// either test above scans the serialized buffer for canary substrings.
//
// CHAOS-3894: the buffer these tests scan is the whole JSON-serialized log
// record, including slog's time field -- and a canary as short as "5432"
// (4 digits) can appear BY COINCIDENCE among a timestamp's nanosecond
// digits with no sanitization defect involved at all (observed on CI:
// "time":"2026-08-18T00:31:05.803154325Z" contains "5432" as a plain
// substring of 803154325). The sanitization contract these tests exist to
// prove governs the fields the coordinator/observer choose to log, not
// slog's own clock output, so the clock output is removed from the buffer
// before the substring scan runs -- the assertion then inspects only what
// the contract actually governs.
func sanitizationTestHandlerOptions() *slog.HandlerOptions {
	return &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}
}
