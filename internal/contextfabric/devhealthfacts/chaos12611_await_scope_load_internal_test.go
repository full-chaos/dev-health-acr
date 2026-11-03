package devhealthfacts

import (
	"context"
	"errors"
	"slices"
	"testing"

	"golang.org/x/sync/singleflight"
)

// CHAOS-12611: a caller whose context is done gets its own error from the
// scope wait, never the shared load's result. Here both cases of the wait are
// ready on every call: the caller is cancelled and the result is already in
// its channel. Without the second context check, select returns the result
// about half the time, so 64 calls all pass by chance with a probability of
// 2^-64.
func TestACancelledCallerNeverGetsTheScopeLoadResult(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := range 64 {
		result := make(chan singleflight.Result, 1)
		result <- singleflight.Result{Val: []string{"wu-1", "wu-2"}}
		res, err := awaitScopeLoad(ctx, result)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("call %d: err = %v, want the caller's context.Canceled", i+1, err)
		}
		if res.Val != nil || res.Err != nil {
			t.Fatalf("call %d: a cancelled caller got the load's result %+v", i+1, res)
		}
	}
}

// A live caller gets the load's result as it is, a failed load included (the
// caller then falls back to the scope subqueries).
func TestALiveCallerGetsTheScopeLoadResult(t *testing.T) {
	t.Parallel()
	result := make(chan singleflight.Result, 1)
	result <- singleflight.Result{Val: []string{"wu-1", "wu-2"}}
	res, err := awaitScopeLoad(context.Background(), result)
	if err != nil {
		t.Fatalf("a live caller failed with %v", err)
	}
	if ids, _ := res.Val.([]string); !slices.Equal(ids, []string{"wu-1", "wu-2"}) || res.Err != nil {
		t.Fatalf("result = %+v, want the load's ids", res)
	}
	failed := errors.New("scope load failed")
	result <- singleflight.Result{Err: failed}
	if res, err = awaitScopeLoad(context.Background(), result); err != nil || !errors.Is(res.Err, failed) {
		t.Fatalf("a failed load reached the live caller as (%+v, %v), want its own error in the result", res, err)
	}
}
