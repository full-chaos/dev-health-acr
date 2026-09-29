package api

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// The direct HTTP route clamps (does not refuse) a find limit above the
// maximum of 200 (chris ruling 2026-09-29).
func TestEffectiveFindLimitClampsAt200(t *testing.T) {
	for in, want := range map[int]int{0: directread.DefaultFindLimit, 25: 25, 200: 200, 201: 200, 5000: 200} {
		if got := effectiveFindLimit(in); got != want {
			t.Errorf("effectiveFindLimit(%d)=%d want %d", in, got, want)
		}
	}
	if directread.MaxFindLimit != 200 {
		t.Fatalf("MaxFindLimit %d", directread.MaxFindLimit)
	}
}
