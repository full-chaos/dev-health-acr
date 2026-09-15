package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkItemMembershipGateRefusesWhenPermitAndQueueAreFull(t *testing.T) {
	gate, err := NewWorkItemMembershipGate(1, 1)
	if err != nil {
		t.Fatalf("NewWorkItemMembershipGate: %v", err)
	}
	first, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	queuedDone := make(chan struct{})
	queuedResult := make(chan error, 1)
	queuedLease := make(chan *WorkItemMembershipLease, 1)
	queuedContext, cancelQueued := context.WithCancel(context.Background())
	defer cancelQueued()
	go func() {
		lease, acquireErr := gate.Acquire(queuedContext)
		queuedLease <- lease
		queuedResult <- acquireErr
		close(queuedDone)
	}()

	deadline := time.Now().Add(time.Second)
	for gate.Stats().Queued != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := gate.Stats().Queued; got != 1 {
		t.Fatalf("queued occupancy = %d, want 1", got)
	}
	if _, err := gate.Acquire(context.Background()); !errors.Is(err, ErrWorkItemMembershipQueueFull) {
		t.Fatalf("third Acquire error = %v, want ErrWorkItemMembershipQueueFull", err)
	}
	if got := gate.Stats().InFlight; got != 1 {
		t.Fatalf("in-flight occupancy after refusal = %d, want 1", got)
	}

	// The first lease remains held until its response owner releases it. A
	// query completing alone must not make the queued request jump the bound.
	first.Release()
	select {
	case <-queuedDone:
	case <-time.After(time.Second):
		t.Fatal("queued admission did not receive the released permit")
	}
	if acquireErr := <-queuedResult; acquireErr != nil {
		t.Fatalf("queued Acquire: %v", acquireErr)
	}
	lease := <-queuedLease
	if lease == nil {
		t.Fatal("queued admission returned a nil lease")
	}
	if got := gate.Stats().InFlight; got != 1 {
		t.Fatalf("in-flight occupancy after queued admission = %d, want 1", got)
	}
	lease.Release()
	lease.Release()
	if got := gate.Stats().InFlight; got != 0 {
		t.Fatalf("in-flight occupancy after idempotent release = %d, want 0", got)
	}
}

func TestWorkItemMembershipGateSupportsAnExplicitZeroQueue(t *testing.T) {
	gate, err := NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatalf("NewWorkItemMembershipGate: %v", err)
	}
	lease, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer lease.Release()
	if _, err := gate.Acquire(context.Background()); !errors.Is(err, ErrWorkItemMembershipQueueFull) {
		t.Fatalf("second Acquire error = %v, want ErrWorkItemMembershipQueueFull", err)
	}
}

func TestWorkItemMembershipGateRejectsInvalidBounds(t *testing.T) {
	for _, values := range [][2]int{{0, 1}, {1, -1}, {-1, 0}} {
		if _, err := NewWorkItemMembershipGate(values[0], values[1]); !errors.Is(err, ErrWorkItemMembershipGateInvalid) {
			t.Errorf("NewWorkItemMembershipGate(%d,%d) error = %v, want ErrWorkItemMembershipGateInvalid", values[0], values[1], err)
		}
	}
}

func TestWorkItemMembershipGateBoundedAcrossOneThousandOrganizationAdmissions(t *testing.T) {
	const (
		organizations = 1000
		inFlight      = 32
		queueCapacity = 32
	)
	gate, err := NewWorkItemMembershipGate(inFlight, queueCapacity)
	if err != nil {
		t.Fatalf("NewWorkItemMembershipGate: %v", err)
	}

	active := make([]*WorkItemMembershipLease, 0, inFlight)
	for organization := 0; organization < inFlight; organization++ {
		lease, acquireErr := gate.Acquire(context.Background())
		if acquireErr != nil {
			t.Fatalf("organization %d initial Acquire: %v", organization, acquireErr)
		}
		active = append(active, lease)
	}

	queued := make(chan *WorkItemMembershipLease, queueCapacity)
	queueErrors := make(chan error, queueCapacity)
	for organization := inFlight; organization < inFlight+queueCapacity; organization++ {
		go func(organization int) {
			lease, acquireErr := gate.Acquire(context.Background())
			if acquireErr != nil {
				queueErrors <- errors.New("organization queued admission failed: " + acquireErr.Error())
				return
			}
			queued <- lease
		}(organization)
	}

	deadline := time.Now().Add(time.Second)
	for gate.Stats().Queued != queueCapacity && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := gate.Stats().Queued; got != queueCapacity {
		t.Fatalf("queued occupancy = %d, want %d while first %d organizations hold leases", got, queueCapacity, inFlight)
	}

	refused := 0
	for organization := inFlight + queueCapacity; organization < organizations; organization++ {
		if _, acquireErr := gate.Acquire(context.Background()); !errors.Is(acquireErr, ErrWorkItemMembershipQueueFull) {
			t.Fatalf("organization %d overflow Acquire error = %v, want ErrWorkItemMembershipQueueFull", organization, acquireErr)
		}
		refused++
	}
	if want := organizations - inFlight - queueCapacity; refused != want {
		t.Fatalf("overflow refusals = %d, want %d", refused, want)
	}
	stats := gate.Stats()
	if stats.InFlight != inFlight || stats.Queued != queueCapacity {
		t.Fatalf("gate occupancy after %d admissions = %+v, want in_flight=%d queued=%d", organizations, stats, inFlight, queueCapacity)
	}

	for _, lease := range active {
		lease.Release()
	}
	queuedLeases := make([]*WorkItemMembershipLease, 0, queueCapacity)
	for organization := 0; organization < queueCapacity; organization++ {
		select {
		case acquireErr := <-queueErrors:
			t.Fatalf("queued organization admission: %v", acquireErr)
		case lease := <-queued:
			if lease == nil {
				t.Fatal("queued organization returned a nil lease")
			}
			queuedLeases = append(queuedLeases, lease)
		case <-time.After(time.Second):
			t.Fatal("queued organization did not receive a released permit")
		}
	}
	for _, lease := range queuedLeases {
		lease.Release()
	}

	deadline = time.Now().Add(time.Second)
	for gate.Stats().InFlight != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stats := gate.Stats(); stats.InFlight != 0 || stats.Queued != 0 {
		t.Fatalf("gate occupancy after all 1000 organization admissions complete = %+v, want zero", stats)
	}
}

func TestWorkItemMembershipGateRemovesCanceledQueueReservation(t *testing.T) {
	gate, err := NewWorkItemMembershipGate(1, 1)
	if err != nil {
		t.Fatalf("NewWorkItemMembershipGate: %v", err)
	}
	first, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer first.Release()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		lease, acquireErr := gate.Acquire(ctx)
		if lease != nil {
			lease.Release()
		}
		result <- acquireErr
	}()
	deadline := time.Now().Add(time.Second)
	for gate.Stats().Queued != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := gate.Stats().Queued; got != 1 {
		t.Fatalf("queued occupancy before cancellation = %d, want 1", got)
	}
	cancel()
	select {
	case acquireErr := <-result:
		if !errors.Is(acquireErr, context.Canceled) {
			t.Fatalf("canceled Acquire error = %v, want context.Canceled", acquireErr)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled queued admission did not return")
	}
	if got := gate.Stats().Queued; got != 0 {
		t.Fatalf("queued occupancy after cancellation = %d, want 0", got)
	}
}
