package devhealthfacts

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// SourceHealthOperationName is the registered ops operation the source_health
// fact reads.
const SourceHealthOperationName = "sourceHealth"

// OperationOutcome is the closed result of one served-operation call. Served
// is true only when the operation ran and answered; Complete is the runner's
// own completeness verdict; Reason is the closed call status when not served.
type OperationOutcome struct {
	Served   bool
	Complete bool
	Reason   string
	Data     json.RawMessage
}

// OperationCaller runs one registered operation as the principal. The hosted
// runtime supplies the direct-read operation runner behind it; this package
// cannot import that package (it imports this one).
type OperationCaller interface {
	CallOperation(ctx context.Context, principal storage.Principal, operation string) (OperationOutcome, error)
}

// ErrOperationCallerUnset is returned by a holder that has no caller yet.
var ErrOperationCallerUnset = errors.New("devhealthfacts: operation caller not set")

// OperationHolder late-binds the OperationCaller: the fact registry is built
// before the operation runner exists. Hosted open sets it before serving and
// fails startup when it is still unset.
type OperationHolder struct {
	mu     sync.RWMutex
	caller OperationCaller
}

// NewOperationHolder returns an unset holder.
func NewOperationHolder() *OperationHolder { return &OperationHolder{} }

// Set binds the caller. A nil caller leaves the holder unset.
func (h *OperationHolder) Set(caller OperationCaller) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.caller = caller
}

// IsSet reports whether a caller is bound.
func (h *OperationHolder) IsSet() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.caller != nil
}

// CallOperation forwards to the bound caller.
func (h *OperationHolder) CallOperation(ctx context.Context, principal storage.Principal, operation string) (OperationOutcome, error) {
	if h == nil {
		return OperationOutcome{}, ErrOperationCallerUnset
	}
	h.mu.RLock()
	caller := h.caller
	h.mu.RUnlock()
	if caller == nil {
		return OperationOutcome{}, ErrOperationCallerUnset
	}
	return caller.CallOperation(ctx, principal, operation)
}
