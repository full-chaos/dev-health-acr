package contextfabric

import (
	"testing"
)

func TestClassifyWindow_ModelPickCompatibleWithShape(t *testing.T) {
	t.Parallel()
	interpreted := InterpretedQuestion{Shape: ShapeDiscoveredCohort}
	got := ClassifyWindow(interpreted, WindowClassTrendAssessment, WindowConfidenceHigh)
	if got.Class != WindowClassTrendAssessment || got.Source != WindowClassSourceModel || got.Downgraded {
		t.Fatalf("ClassifyWindow(compatible model pick) = %#v, want model-sourced trend_assessment, not downgraded", got)
	}
	if got.Confidence != WindowConfidenceHigh {
		t.Fatalf("ClassifyWindow(compatible model pick).Confidence = %q, want high (model's own)", got.Confidence)
	}
}

func TestClassifyWindow_ModelPickMissingConfidenceDefaultsLow(t *testing.T) {
	t.Parallel()
	interpreted := InterpretedQuestion{Shape: ShapeSingleSubject}
	got := ClassifyWindow(interpreted, WindowClassRecentActivityLookup, "")
	if got.Confidence != WindowConfidenceLow {
		t.Fatalf("ClassifyWindow(no confidence) = %#v, want confidence defaulted to low", got)
	}
}

func TestClassifyWindow_UnsetClassFallsBackByShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		shape InvestigationShape
		want  WindowClass
		ok    bool
	}{
		{ShapeDiscoveredCohort, WindowClassTrendAssessment, true},
		{ShapeSingleSubject, WindowClassRecentActivityLookup, true},
		{ShapeExplicitCohort, "", false},
		{ShapeOpen, "", false},
	}
	for _, tc := range cases {
		got := ClassifyWindow(InterpretedQuestion{Shape: tc.shape}, "", "")
		if tc.ok {
			if got.Class != tc.want || got.Source != WindowClassSourceFallback || got.Downgraded {
				t.Fatalf("ClassifyWindow(unset, shape=%s) = %#v, want fallback %s, not downgraded", tc.shape, got, tc.want)
			}
		} else if got.Source != WindowClassSourceNone {
			t.Fatalf("ClassifyWindow(unset, shape=%s) = %#v, want WindowClassSourceNone (refuse to guess)", tc.shape, got)
		}
		if got.Confidence != WindowConfidenceLow {
			t.Fatalf("ClassifyWindow(unset, shape=%s).Confidence = %q, want low", tc.shape, got.Confidence)
		}
	}
}

func TestClassifyWindow_IncompatiblePickDowngradesToFallback(t *testing.T) {
	t.Parallel()
	// Model says recent_activity_lookup on a discovered_cohort shape --
	// design brief §2's own named example of a structural mismatch.
	interpreted := InterpretedQuestion{Shape: ShapeDiscoveredCohort}
	got := ClassifyWindow(interpreted, WindowClassRecentActivityLookup, WindowConfidenceHigh)
	if !got.Downgraded {
		t.Fatalf("ClassifyWindow(incompatible pick) = %#v, want Downgraded=true", got)
	}
	if got.Class != WindowClassTrendAssessment || got.Source != WindowClassSourceFallback {
		t.Fatalf("ClassifyWindow(incompatible pick) = %#v, want the structurally compatible fallback (trend_assessment)", got)
	}
	if got.Confidence != WindowConfidenceLow {
		t.Fatalf("ClassifyWindow(incompatible pick).Confidence = %q, want low regardless of the model's own high", got.Confidence)
	}
}

func TestClassifyWindow_IncompatiblePickWithNoFallbackAvailable(t *testing.T) {
	t.Parallel()
	// The only path where Source==none AND Downgraded==true both hold
	// (M12): a model pick that's incompatible with the shape, on a shape
	// fallbackClass itself has no entry for (explicit_cohort/open) -- so
	// the downgrade has nowhere structurally compatible to land.
	for _, shape := range []InvestigationShape{ShapeExplicitCohort, ShapeOpen} {
		interpreted := InterpretedQuestion{Shape: shape}
		got := ClassifyWindow(interpreted, WindowClassRecentActivityLookup, WindowConfidenceHigh)
		if got.Source != WindowClassSourceNone || !got.Downgraded {
			t.Fatalf("ClassifyWindow(incompatible pick, shape=%s, no fallback) = %#v, want Source=none, Downgraded=true", shape, got)
		}
		if got.Class != "" {
			t.Fatalf("ClassifyWindow(incompatible pick, shape=%s, no fallback).Class = %q, want empty", shape, got.Class)
		}
	}
}

func TestClassifyWindow_ExplicitWindowNeverALegitimateModelPick(t *testing.T) {
	t.Parallel()
	// §2.1: explicit_window is caller-input-only by construction -- a
	// model asserting it is always treated as incompatible.
	interpreted := InterpretedQuestion{Shape: ShapeSingleSubject}
	got := ClassifyWindow(interpreted, WindowClassExplicitWindow, WindowConfidenceHigh)
	if !got.Downgraded || got.Class == WindowClassExplicitWindow {
		t.Fatalf("ClassifyWindow(model asserts explicit_window) = %#v, want downgraded away from explicit_window", got)
	}
}

func TestClassifyWindow_StateSnapshotAlwaysCompatible(t *testing.T) {
	t.Parallel()
	for _, shape := range []InvestigationShape{
		ShapeSingleSubject, ShapeExplicitCohort,
		ShapeDiscoveredCohort, ShapeOpen,
	} {
		got := ClassifyWindow(InterpretedQuestion{Shape: shape}, WindowClassStateSnapshot, WindowConfidenceHigh)
		if got.Class != WindowClassStateSnapshot || got.Source != WindowClassSourceModel || got.Downgraded {
			t.Fatalf("ClassifyWindow(state_snapshot, shape=%s) = %#v, want accepted as-is (no window is state_snapshot's own default)", shape, got)
		}
	}
}

func TestDefaultRelativeID(t *testing.T) {
	t.Parallel()
	trend := WindowClassOutcome{Class: WindowClassTrendAssessment, Source: WindowClassSourceModel}
	if got, ok := DefaultRelativeID(trend, WindowDefaultPolicy90D); !ok || got != RelativeWindowTrailing90D {
		t.Fatalf("DefaultRelativeID(trend, 90d policy) = (%q, %v), want (trailing_90d, true)", got, ok)
	}
	if got, ok := DefaultRelativeID(trend, WindowDefaultPolicy365D); !ok || got != RelativeWindowTrailing365D {
		t.Fatalf("DefaultRelativeID(trend, 365d policy) = (%q, %v), want (trailing_365d, true)", got, ok)
	}
	recent := WindowClassOutcome{Class: WindowClassRecentActivityLookup, Source: WindowClassSourceModel}
	if got, ok := DefaultRelativeID(recent, WindowDefaultPolicy90D); !ok || got != RelativeWindowTrailing30D {
		t.Fatalf("DefaultRelativeID(recent, 90d policy) = (%q, %v), want (trailing_30d, true)", got, ok)
	}
	if got, ok := DefaultRelativeID(recent, WindowDefaultPolicy365D); !ok || got != RelativeWindowTrailing90D {
		t.Fatalf("DefaultRelativeID(recent, 365d policy) = (%q, %v), want (trailing_90d, true)", got, ok)
	}
	stateSnapshot := WindowClassOutcome{Class: WindowClassStateSnapshot, Source: WindowClassSourceModel}
	if _, ok := DefaultRelativeID(stateSnapshot, WindowDefaultPolicy90D); ok {
		t.Fatal("DefaultRelativeID(state_snapshot) = ok=true, want ok=false -- state_snapshot never gets a window")
	}
	none := WindowClassOutcome{Source: WindowClassSourceNone}
	if _, ok := DefaultRelativeID(none, WindowDefaultPolicy90D); ok {
		t.Fatal("DefaultRelativeID(no class) = ok=true, want ok=false")
	}
}
