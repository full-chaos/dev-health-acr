package devhealthsource

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestBuildCardinalityWindow_NoBoundsIsOpen(t *testing.T) {
	t.Parallel()
	w, err := BuildCardinalityWindow(contextfabric.SubjectPullRequest, nil, nil, nil)
	if err != nil {
		t.Fatalf("BuildCardinalityWindow: %v", err)
	}
	if w.Bound || w.SQL != "" {
		t.Fatalf("w = %#v, want Bound=false SQL=\"\" -- no interpreted window at all", w)
	}
}

func TestBuildCardinalityWindow_StartAndEnd(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	w, err := BuildCardinalityWindow(contextfabric.SubjectPullRequest, &start, &end, nil)
	if err != nil {
		t.Fatalf("BuildCardinalityWindow: %v", err)
	}
	if !w.Bound {
		t.Fatalf("w.Bound = false, want true")
	}
	wantSQL := "p.last_synced >= {census_window_start:DateTime64(3,'UTC')} AND p.last_synced <= {census_window_end:DateTime64(3,'UTC')}"
	if w.SQL != wantSQL {
		t.Fatalf("SQL = %q, want %q", w.SQL, wantSQL)
	}
	if len(w.Bindings) != 2 {
		t.Fatalf("Bindings = %#v, want 2 entries", w.Bindings)
	}
}

func TestBuildCardinalityWindow_AsOfOnlyAppliesWhenNoStartOrEnd(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	w, err := BuildCardinalityWindow(contractsv1.ContextFabricSubjectCIRun, nil, nil, &asOf)
	if err != nil {
		t.Fatalf("BuildCardinalityWindow: %v", err)
	}
	if !w.Bound {
		t.Fatalf("w.Bound = false, want true for an AsOf-only window")
	}
	wantSQL := "coalesce(c.finished_at, c.started_at) <= {census_window_asof:DateTime64(3,'UTC')}"
	if w.SQL != wantSQL {
		t.Fatalf("SQL = %q, want %q", w.SQL, wantSQL)
	}
}

func TestBuildCardinalityWindow_StartTakesPrecedenceOverAsOf(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	w, err := BuildCardinalityWindow(contextfabric.SubjectPullRequest, &start, nil, &asOf)
	if err != nil {
		t.Fatalf("BuildCardinalityWindow: %v", err)
	}
	// AsOf must be IGNORED once Start is present -- it is a fallback for
	// "neither Start nor End", not an additional conjunct.
	wantSQL := "p.last_synced >= {census_window_start:DateTime64(3,'UTC')}"
	if w.SQL != wantSQL {
		t.Fatalf("SQL = %q, want %q (AsOf must not leak in when Start is present)", w.SQL, wantSQL)
	}
}

func TestBuildCardinalityWindow_UnregisteredKind(t *testing.T) {
	t.Parallel()
	if _, err := BuildCardinalityWindow(contextfabric.SubjectRepository, nil, nil, nil); err == nil {
		t.Fatalf("BuildCardinalityWindow(repository): want error, got nil -- repository is not a census kind")
	}
}
