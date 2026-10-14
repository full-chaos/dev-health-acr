package devhealthfacts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type probeRows struct {
	count uint64
	read  bool
}

func (r *probeRows) Next() bool          { first := !r.read; r.read = true; return first }
func (r *probeRows) Scan(d ...any) error { *(d[0].(*uint64)) = r.count; return nil }
func (r *probeRows) Err() error          { return nil }
func (r *probeRows) Close() error        { return nil }

type probeClient struct {
	answers []uint64
	fail    bool
	calls   int
}

func (c *probeClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.calls++
	if c.fail {
		return nil, errors.New("down")
	}
	n := c.answers[0]
	if len(c.answers) > 1 {
		c.answers = c.answers[1:]
	}
	return &probeRows{count: n}, nil
}

func TestStatusColumnsProbeCachesPositiveRechecksNegativeAndNeverPinsAFailure(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	// A failed first probe answers "old shape" for this call only.
	failing := &probeClient{fail: true}
	probe := &changeFailureStatusColumnsProbe{}
	if probe.availableAt(context.Background(), failing, now) || probe.Known() {
		t.Fatal("a failed probe must answer no and cache nothing")
	}
	// A negative answer stands for the recheck window, then is asked again.
	c := &probeClient{answers: []uint64{0, 2}}
	probe = &changeFailureStatusColumnsProbe{}
	if probe.availableAt(context.Background(), c, now) {
		t.Fatal("0 columns is the old shape")
	}
	if probe.availableAt(context.Background(), c, now.Add(10*time.Second)) || c.calls != 1 {
		t.Fatalf("negative answer re-asked inside the window (calls %d)", c.calls)
	}
	if !probe.availableAt(context.Background(), c, now.Add(2*time.Minute)) || c.calls != 2 {
		t.Fatalf("migration not picked up after the window (calls %d)", c.calls)
	}
	// A positive answer is final, even when the next probe would fail.
	c.fail = true
	if !probe.availableAt(context.Background(), c, now.Add(time.Hour)) || c.calls != 2 {
		t.Fatalf("positive answer re-asked (calls %d)", c.calls)
	}
}
