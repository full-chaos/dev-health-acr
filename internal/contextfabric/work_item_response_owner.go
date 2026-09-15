package contextfabric

import (
	"context"
	"errors"
	"sync"
)

var (
	// ErrWorkItemResponseOwnerClosed means that the response owner has
	// completed and cannot accept a new membership lease.
	ErrWorkItemResponseOwnerClosed = errors.New("work item response owner is closed")

	// ErrWorkItemResponseOwnerLeaseConflict means that a response owner
	// already has a different active membership lease. The incoming lease is
	// released before Retain returns this error.
	ErrWorkItemResponseOwnerLeaseConflict = errors.New("work item response owner already has an active lease")

	// ErrWorkItemResponseOwnerUnavailable means that a nil owner cannot cover
	// a membership lease. Retain releases the incoming lease before returning
	// this error, so a caller cannot leak admission by passing a nil owner.
	ErrWorkItemResponseOwnerUnavailable = errors.New("work item response owner is unavailable")
)

// WorkItemResponseOwner owns the one membership lease that may support a
// response. It is deliberately request-local: callers carry it through a
// private context value rather than putting lease state in a request, result,
// persistence record, or wire payload.
//
// The scope that creates an owner owns its Complete call. Nested operations
// borrow the owner from context and may Retain or Release their lease, but
// must not complete the enclosing response scope.
type WorkItemResponseOwner struct {
	mu     sync.Mutex
	closed bool
	lease  *WorkItemMembershipLease
}

// workItemResponseOwnerContextKey keeps the owner private to this package.
// There is no process-wide owner or lease registry.
type workItemResponseOwnerContextKey struct{}

// NewWorkItemResponseOwner creates an open response owner with no active
// membership lease.
func NewWorkItemResponseOwner() *WorkItemResponseOwner {
	return &WorkItemResponseOwner{}
}

// WithWorkItemResponseOwner carries owner through a request context. A nil
// context is treated as Background, and a nil owner leaves the context
// unchanged. Both choices keep optional callers nil-safe without storing a
// typed nil in the context.
func WithWorkItemResponseOwner(ctx context.Context, owner *WorkItemResponseOwner) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if owner == nil {
		return ctx
	}
	return context.WithValue(ctx, workItemResponseOwnerContextKey{}, owner)
}

// NewWorkItemResponseOwnerContext creates an owner and carries it through a
// context. The returned owner is the creator's scope and therefore the only
// scope that should call Complete.
func NewWorkItemResponseOwnerContext(ctx context.Context) (context.Context, *WorkItemResponseOwner) {
	owner := NewWorkItemResponseOwner()
	return WithWorkItemResponseOwner(ctx, owner), owner
}

// WorkItemResponseOwnerFromContext borrows the request-local owner, when one
// is present. A nil context or a typed nil value is treated as absent.
func WorkItemResponseOwnerFromContext(ctx context.Context) (*WorkItemResponseOwner, bool) {
	if ctx == nil {
		return nil, false
	}
	owner, ok := ctx.Value(workItemResponseOwnerContextKey{}).(*WorkItemResponseOwner)
	return owner, ok && owner != nil
}

// Retain registers lease as the owner's active membership lease.
//
// Retaining nil is a harmless no-op. Retaining the same active lease is
// idempotent. A different lease cannot replace the active one: Retain
// releases the incoming lease and returns ErrWorkItemResponseOwnerLeaseConflict.
// A closed or nil owner likewise releases the incoming lease and returns an
// error. Lease release always runs after the owner lock is released.
func (o *WorkItemResponseOwner) Retain(lease *WorkItemMembershipLease) error {
	if lease == nil {
		return nil
	}
	if o == nil {
		lease.Release()
		return ErrWorkItemResponseOwnerUnavailable
	}

	o.mu.Lock()
	switch {
	case o.closed:
		o.mu.Unlock()
		lease.Release()
		return ErrWorkItemResponseOwnerClosed
	case o.lease == nil:
		o.lease = lease
		o.mu.Unlock()
		return nil
	case o.lease == lease:
		o.mu.Unlock()
		return nil
	default:
		o.mu.Unlock()
		lease.Release()
		return ErrWorkItemResponseOwnerLeaseConflict
	}
}

// Release relinquishes lease when it is the owner's active lease. A lease
// that was never registered with this owner is left alone, which prevents
// one request from releasing another request's permit. Repeated Release calls
// are harmless because the owner clears the slot and leases release through
// their own idempotent operation.
func (o *WorkItemResponseOwner) Release(lease *WorkItemMembershipLease) {
	if o == nil || lease == nil {
		return
	}

	o.mu.Lock()
	if o.lease != lease {
		o.mu.Unlock()
		return
	}
	o.lease = nil
	o.mu.Unlock()
	lease.Release()
}

// Complete closes the owner, clears its active lease, and releases that lease
// exactly once. The state transition is atomic under the owner lock; the
// potentially re-entrant lease release runs after unlocking. Completion is
// idempotent.
func (o *WorkItemResponseOwner) Complete() {
	if o == nil {
		return
	}

	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return
	}
	o.closed = true
	lease := o.lease
	o.lease = nil
	o.mu.Unlock()
	lease.Release()
}
