package factoracle

import (
	"math"
	"math/rand"
	"testing"
)

// The Float32 share rule (CHAOS-12950): relOpsFloat32 is the rounding bound of
// a Float32 cast, 2^-24, and nothing looser.

// TestRelOpsFloat32HoldsTheFloat32Cast: for shares in (0, 1], the relative
// change of float64(float32(x)) never exceeds relOpsFloat32, and the worst
// share found is within a factor of two of it, so the constant is not slack.
func TestRelOpsFloat32HoldsTheFloat32Cast(t *testing.T) {
	rng := rand.New(rand.NewSource(12950))
	worst := 0.0
	for i := 0; i < 2_000_000; i++ {
		x := rng.Float64()
		if x == 0 {
			continue
		}
		gap := math.Abs(float64(float32(x))-x) / x
		if gap > relOpsFloat32 {
			t.Fatalf("share %v: Float32 cast changes it by %g relative, above the bound %g", x, gap, relOpsFloat32)
		}
		worst = math.Max(worst, gap)
	}
	if worst < relOpsFloat32/2 {
		t.Fatalf("worst relative change %g of 2e6 shares is under half of the bound %g: the bound is slack", worst, relOpsFloat32)
	}
}

// TestRelOpsFloat32TolerancePlants: a theme value that moves by 1e-7 of its
// value (a drift above the Float32 bound) is outside themeTolerance; one that
// moves by half the bound is inside. The first plant is red with the old 1e-6.
func TestRelOpsFloat32TolerancePlants(t *testing.T) {
	const base = 1234.5
	for _, c := range []struct {
		name  string
		shift float64
		want  bool
	}{
		{"half the bound is rounding", relOpsFloat32 / 2, true},
		{"the bound itself is rounding", relOpsFloat32, true},
		{"1e-7 is a drift", 1e-7, false},
		{"1e-6 is a drift", 1e-6, false},
	} {
		got := base * (1 + c.shift)
		within := math.Abs(got-base) <= themeTolerance(got, base)
		if within != c.want {
			t.Errorf("%s: shift %g, within tolerance %v, want %v", c.name, c.shift, within, c.want)
		}
	}
}
