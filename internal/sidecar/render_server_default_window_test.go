package sidecar

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestWindowBoundsTextRendersEveryEffectiveWindowForm (CHAOS-11601): the MCP
// rendering states the snapshot form's read instant, never "unstated window".
func TestWindowBoundsTextRendersEveryEffectiveWindowForm(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 10, 30, 21, 2, 11, 0, time.UTC)
	start, end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		window contractsv1.ContextFabricEffectiveEvidenceWindow
		want   string
	}{
		{contractsv1.ContextFabricEffectiveEvidenceWindow{AsOf: &asOf, WindowClass: contractsv1.ContextFabricWindowClassStateSnapshot, Provenance: contractsv1.ContextFabricWindowServerDefault}, "as of 2026-10-30T21:02:11Z"},
		{contractsv1.ContextFabricEffectiveEvidenceWindow{Start: &start, End: &end, RelativeID: contractsv1.ContextFabricRelativeWindowTrailing90D, Provenance: contractsv1.ContextFabricWindowServerDefault}, "2026-08-01T00:00:00Z to 2026-10-30T00:00:00Z"},
		{contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowAllTime, Provenance: contractsv1.ContextFabricWindowQuestionStated}, "all time"},
	} {
		window := tc.window
		if got := windowBoundsText(&window); got != tc.want {
			t.Errorf("windowBoundsText(%+v) = %q, want %q", tc.window, got, tc.want)
		}
	}
}
