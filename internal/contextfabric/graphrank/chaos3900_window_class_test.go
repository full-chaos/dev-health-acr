package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestClassifyWindow_ModelPickCompatibleWithShape(t *testing.T) {
	t.Parallel()
	interpreted := contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeDiscoveredCohort}
	got := ClassifyWindow(interpreted, contextfabric.WindowClassTrendAssessment, contextfabric.WindowConfidenceHigh)
	if got.Class != contextfabric.WindowClassTrendAssessment || got.Source != WindowClassSourceModel || got.Downgraded {
		t.Fatalf("ClassifyWindow(compatible model pick) = %#v, want model-sourced trend_assessment, not downgraded", got)
	}
	if got.Confidence != contextfabric.WindowConfidenceHigh {
		t.Fatalf("ClassifyWindow(compatible model pick).Confidence = %q, want high (model's own)", got.Confidence)
	}
}

func TestClassifyWindow_ModelPickMissingConfidenceDefaultsLow(t *testing.T) {
	t.Parallel()
	interpreted := contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject}
	got := ClassifyWindow(interpreted, contextfabric.WindowClassRecentActivityLookup, "")
	if got.Confidence != contextfabric.WindowConfidenceLow {
		t.Fatalf("ClassifyWindow(no confidence) = %#v, want confidence defaulted to low", got)
	}
}

func TestClassifyWindow_UnsetClassFallsBackByShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		shape contextfabric.InvestigationShape
		want  contextfabric.WindowClass
		ok    bool
	}{
		{contextfabric.ShapeDiscoveredCohort, contextfabric.WindowClassTrendAssessment, true},
		{contextfabric.ShapeSingleSubject, contextfabric.WindowClassRecentActivityLookup, true},
		{contextfabric.ShapeExplicitCohort, "", false},
		{contextfabric.ShapeOpen, "", false},
	}
	for _, tc := range cases {
		got := ClassifyWindow(contextfabric.InterpretedQuestion{Shape: tc.shape}, "", "")
		if tc.ok {
			if got.Class != tc.want || got.Source != WindowClassSourceFallback || got.Downgraded {
				t.Fatalf("ClassifyWindow(unset, shape=%s) = %#v, want fallback %s, not downgraded", tc.shape, got, tc.want)
			}
		} else if got.Source != WindowClassSourceNone {
			t.Fatalf("ClassifyWindow(unset, shape=%s) = %#v, want WindowClassSourceNone (refuse to guess)", tc.shape, got)
		}
		if got.Confidence != contextfabric.WindowConfidenceLow {
			t.Fatalf("ClassifyWindow(unset, shape=%s).Confidence = %q, want low", tc.shape, got.Confidence)
		}
	}
}

func TestClassifyWindow_IncompatiblePickDowngradesToFallback(t *testing.T) {
	t.Parallel()
	// Model says recent_activity_lookup on a discovered_cohort shape --
	// design brief §2's own named example of a structural mismatch.
	interpreted := contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeDiscoveredCohort}
	got := ClassifyWindow(interpreted, contextfabric.WindowClassRecentActivityLookup, contextfabric.WindowConfidenceHigh)
	if !got.Downgraded {
		t.Fatalf("ClassifyWindow(incompatible pick) = %#v, want Downgraded=true", got)
	}
	if got.Class != contextfabric.WindowClassTrendAssessment || got.Source != WindowClassSourceFallback {
		t.Fatalf("ClassifyWindow(incompatible pick) = %#v, want the structurally compatible fallback (trend_assessment)", got)
	}
	if got.Confidence != contextfabric.WindowConfidenceLow {
		t.Fatalf("ClassifyWindow(incompatible pick).Confidence = %q, want low regardless of the model's own high", got.Confidence)
	}
}

func TestClassifyWindow_ExplicitWindowNeverALegitimateModelPick(t *testing.T) {
	t.Parallel()
	// §2.1: explicit_window is caller-input-only by construction -- a
	// model asserting it is always treated as incompatible.
	interpreted := contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject}
	got := ClassifyWindow(interpreted, contextfabric.WindowClassExplicitWindow, contextfabric.WindowConfidenceHigh)
	if !got.Downgraded || got.Class == contextfabric.WindowClassExplicitWindow {
		t.Fatalf("ClassifyWindow(model asserts explicit_window) = %#v, want downgraded away from explicit_window", got)
	}
}

func TestClassifyWindow_StateSnapshotAlwaysCompatible(t *testing.T) {
	t.Parallel()
	for _, shape := range []contextfabric.InvestigationShape{
		contextfabric.ShapeSingleSubject, contextfabric.ShapeExplicitCohort,
		contextfabric.ShapeDiscoveredCohort, contextfabric.ShapeOpen,
	} {
		got := ClassifyWindow(contextfabric.InterpretedQuestion{Shape: shape}, contextfabric.WindowClassStateSnapshot, contextfabric.WindowConfidenceHigh)
		if got.Class != contextfabric.WindowClassStateSnapshot || got.Source != WindowClassSourceModel || got.Downgraded {
			t.Fatalf("ClassifyWindow(state_snapshot, shape=%s) = %#v, want accepted as-is (no window is state_snapshot's own default)", shape, got)
		}
	}
}

func TestDefaultRelativeID(t *testing.T) {
	t.Parallel()
	trend := WindowClassOutcome{Class: contextfabric.WindowClassTrendAssessment, Source: WindowClassSourceModel}
	if got, ok := DefaultRelativeID(trend, WindowDefaultPolicy90D); !ok || got != contextfabric.RelativeWindowTrailing90D {
		t.Fatalf("DefaultRelativeID(trend, 90d policy) = (%q, %v), want (trailing_90d, true)", got, ok)
	}
	if got, ok := DefaultRelativeID(trend, WindowDefaultPolicy365D); !ok || got != contextfabric.RelativeWindowTrailing365D {
		t.Fatalf("DefaultRelativeID(trend, 365d policy) = (%q, %v), want (trailing_365d, true)", got, ok)
	}
	recent := WindowClassOutcome{Class: contextfabric.WindowClassRecentActivityLookup, Source: WindowClassSourceModel}
	if got, ok := DefaultRelativeID(recent, WindowDefaultPolicy90D); !ok || got != contextfabric.RelativeWindowTrailing30D {
		t.Fatalf("DefaultRelativeID(recent, 90d policy) = (%q, %v), want (trailing_30d, true)", got, ok)
	}
	if got, ok := DefaultRelativeID(recent, WindowDefaultPolicy365D); !ok || got != contextfabric.RelativeWindowTrailing90D {
		t.Fatalf("DefaultRelativeID(recent, 365d policy) = (%q, %v), want (trailing_90d, true)", got, ok)
	}
	stateSnapshot := WindowClassOutcome{Class: contextfabric.WindowClassStateSnapshot, Source: WindowClassSourceModel}
	if _, ok := DefaultRelativeID(stateSnapshot, WindowDefaultPolicy90D); ok {
		t.Fatal("DefaultRelativeID(state_snapshot) = ok=true, want ok=false -- state_snapshot never gets a window")
	}
	none := WindowClassOutcome{Source: WindowClassSourceNone}
	if _, ok := DefaultRelativeID(none, WindowDefaultPolicy90D); ok {
		t.Fatal("DefaultRelativeID(no class) = ok=true, want ok=false")
	}
}
