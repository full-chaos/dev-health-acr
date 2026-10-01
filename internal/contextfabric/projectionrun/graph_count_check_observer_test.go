package projectionrun_test

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestGraphCountCheckReportsOneCompletedCheckWhenHealthy(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000, contractsv1.ContextFabricSubjectRepository: 5},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000, contractsv1.ContextFabricSubjectRepository: 5},
		nil, nil, time.Minute)
	h.run(testStart)
	if len(h.observer.checks) != 1 {
		t.Fatalf("one completed check expected, got %+v", h.observer.checks)
	}
	got := h.observer.checks[0]
	if got.Outcome != projectionrun.GraphCountCheckCompleted || got.OrgID != "org-a" || got.SourcesChecked != 1 || got.KindsCompared != 2 || got.Gaps != 0 || got.Errors != 0 {
		t.Fatalf("unexpected check %+v", got)
	}
	h.run(testStart.Add(30 * time.Second))
	if len(h.observer.checks) != 1 {
		t.Fatalf("a check inside the interval must report nothing, got %d", len(h.observer.checks))
	}
	h.run(testStart.Add(time.Minute))
	if len(h.observer.checks) == 2 && (h.observer.checks[0].Pass != 1 || h.observer.checks[1].Pass != 2) {
		t.Fatalf("checks of one org are numbered 1, 2: %+v", h.observer.checks)
	}
	if len(h.observer.checks) != 2 {
		t.Fatalf("each completed check reports once, got %d", len(h.observer.checks))
	}
}

func TestGraphCountCheckReportCountsGapsAndErrors(t *testing.T) {
	t.Parallel()
	gap := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 900},
		nil, nil, time.Minute)
	gap.run(testStart)
	gap.run(testStart.Add(time.Minute))
	if len(gap.observer.checks) != 2 || gap.observer.checks[0].Gaps != 0 || gap.observer.checks[1].Gaps != 1 || gap.observer.checks[1].Outcome != projectionrun.GraphCountCheckCompleted {
		t.Fatalf("only the confirmed gap counts: %+v", gap.observer.checks)
	}
	for name, errs := range map[string][2]error{"source": {errors.New("down"), nil}, "graph": {nil, errors.New("down")}} {
		h := newCountHarness(t,
			map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
			map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 0},
			errs[0], errs[1], time.Minute)
		h.run(testStart)
		if len(h.observer.checks) != 1 || h.observer.checks[0].Errors != 1 || h.observer.checks[0].Outcome != projectionrun.GraphCountCheckFailed || h.observer.checks[0].Gaps != 0 {
			t.Fatalf("%s: a failed read counts as an error, never a gap: %+v", name, h.observer.checks)
		}
	}
}

func TestGraphCountCheckCancelledMidCheckIsNotCompleted(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		nil, nil, time.Minute)
	h.source.onCount = h.cancel
	h.run(testStart)
	if len(h.observer.checks) != 1 || h.observer.checks[0].Outcome != projectionrun.GraphCountCheckCancelled {
		t.Fatalf("a cancelled check must report outcome cancelled: %+v", h.observer.checks)
	}
}

func TestGraphCountCheckDisabledReportsNothing(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 1000},
		nil, nil, -1)
	h.run(testStart)
	if len(h.observer.checks) != 0 {
		t.Fatalf("a check that never runs reports nothing: %+v", h.observer.checks)
	}
}

func TestSlogObserverGraphCountCheckInfoLineIsCertified(t *testing.T) {
	t.Parallel()
	first := projectionrun.GraphCountCheck{OrgID: "org-a", SourcesChecked: 2, KindsCompared: 7, Gaps: 1, Errors: 3, Outcome: projectionrun.GraphCountCheckFailed, Pass: 1, Duration: 42 * time.Millisecond}
	second := projectionrun.GraphCountCheck{OrgID: "org-a", SourcesChecked: 2, KindsCompared: 7, Outcome: projectionrun.GraphCountCheckCompleted, Pass: 2, Duration: 5 * time.Millisecond}
	certifyLog := func(t *testing.T, checks []projectionrun.GraphCountCheck, want map[string]any) {
		t.Helper()
		var buffer bytes.Buffer
		observer := projectionrun.SlogObserver{Logger: slog.New(slog.NewJSONHandler(&buffer, nil))}
		for _, check := range checks {
			observer.ObserveGraphCountCheck(check)
		}
		parsed, err := certify.Parse(buffer.Bytes())
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.GraphCountCheckFinished, Want: want}); err != nil {
			t.Fatalf("certify: %v", err)
		}
	}
	certifyLog(t, []projectionrun.GraphCountCheck{first}, map[string]any{
		"org_id_hash": "527a4c0a7e94", "pass": 1, "outcome": "failed", "sources_checked": 2,
		"kinds_compared": 7, "gap_count": 1, "error_count": 3, "duration_ms": 42,
	})
	// Two recurring checks of one organization are two passes, not a duplicate.
	certifyLog(t, []projectionrun.GraphCountCheck{first, second}, map[string]any{
		"org_id_hash": "527a4c0a7e94", "pass": 2, "outcome": "completed", "sources_checked": 2,
		"kinds_compared": 7, "gap_count": 0, "error_count": 0, "duration_ms": 5,
	})
}
