package contextfabric

import (
	"context"
	"fmt"
)

// These defaults follow the existing work-item scope admission bound and the
// existing 200-row reader convention. They are local to this dormant port;
// they are not a new public configuration surface.
const (
	DefaultWorkItemMembershipMaxInFlight   = maxWorkItemScopeInFlight
	DefaultWorkItemMembershipQueueCapacity = maxWorkItemScopeInFlight
)

// WorkItemMembershipGate is a per-process bounded permit plus bounded wait
// queue. A full queue refuses immediately, so admission cannot create an
// unbounded resident goroutine set.
type WorkItemMembershipGate struct {
	permits chan struct{}
	queue   chan struct{}
}

// NewWorkItemMembershipGate builds a gate. maxInFlight must be positive and
// queueCapacity may be zero when callers want immediate refusal once permits
// are occupied. The reader constructor supplies the proposed defaults.
func NewWorkItemMembershipGate(maxInFlight, queueCapacity int) (*WorkItemMembershipGate, error) {
	if maxInFlight < 1 || queueCapacity < 0 {
		return nil, fmt.Errorf("%w: max_in_flight=%d queue_capacity=%d", ErrWorkItemMembershipGateInvalid, maxInFlight, queueCapacity)
	}
	return &WorkItemMembershipGate{
		permits: make(chan struct{}, maxInFlight),
		queue:   make(chan struct{}, queueCapacity),
	}, nil
}

// WorkItemMembershipGateStats is a point-in-time operational view used by
// telemetry. It carries no request or subject identity.
type WorkItemMembershipGateStats struct {
	InFlight      int
	Queued        int
	MaxInFlight   int
	QueueCapacity int
}

// Stats returns the current occupancy of the two bounded channels.
func (g *WorkItemMembershipGate) Stats() WorkItemMembershipGateStats {
	if g == nil {
		return WorkItemMembershipGateStats{}
	}
	return WorkItemMembershipGateStats{
		InFlight:      len(g.permits),
		Queued:        len(g.queue),
		MaxInFlight:   cap(g.permits),
		QueueCapacity: cap(g.queue),
	}
}

// Acquire admits one S1 plus its future response. If no permit is free, one
// bounded queue slot is reserved. A queued caller waits only on its request
// context. The returned lease remains live until the response owner calls
// Release; this method does not release it after S1.
func (g *WorkItemMembershipGate) Acquire(ctx context.Context) (*WorkItemMembershipLease, error) {
	if g == nil || cap(g.permits) == 0 {
		return nil, ErrWorkItemMembershipGateInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	select {
	case g.permits <- struct{}{}:
		return g.lease(), nil
	default:
	}

	// The queue is an admission reservation, not an unbounded list of
	// goroutines. A caller that cannot reserve one refuses now.
	select {
	case g.queue <- struct{}{}:
		dequeued := false
		defer func() {
			if !dequeued {
				<-g.queue
			}
		}()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case g.permits <- struct{}{}:
			dequeued = true
			<-g.queue
			return g.lease(), nil
		}
	default:
		return nil, ErrWorkItemMembershipQueueFull
	}
}

func (g *WorkItemMembershipGate) lease() *WorkItemMembershipLease {
	return &WorkItemMembershipLease{release: func() { <-g.permits }}
}
