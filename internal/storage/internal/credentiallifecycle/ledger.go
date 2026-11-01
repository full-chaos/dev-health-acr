package credentiallifecycle

import (
	"context"
	"slices"
	"sync"
)

// Ledger is the in-memory Sink the memory adapter appends to.
type Ledger struct {
	mu   sync.Mutex
	rows []Row
}

func NewLedger() *Ledger { return &Ledger{} }

func (l *Ledger) MaxRevision(_ context.Context, aggregateID string) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	max := 0
	for _, row := range l.rows {
		if row.AggregateID == aggregateID && row.Revision > max {
			max = row.Revision
		}
	}
	return max, nil
}

func (l *Ledger) Insert(_ context.Context, row Row) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, existing := range l.rows {
		if existing.AggregateID == row.AggregateID && existing.Revision == row.Revision {
			return ErrRevisionConflict
		}
	}
	row.Payload = slices.Clone(row.Payload)
	l.rows = append(l.rows, row)
	return nil
}

// Rows returns a copy of every row in write order.
func (l *Ledger) Rows() []Row {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Row, len(l.rows))
	for i, row := range l.rows {
		row.Payload = slices.Clone(row.Payload)
		out[i] = row
	}
	return out
}

// Len is the number of rows written so far.
func (l *Ledger) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.rows)
}

// TruncateTo drops every row written after the first n, so a failed
// multi-row append leaves no partial stream.
func (l *Ledger) TruncateTo(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n >= 0 && n < len(l.rows) {
		l.rows = l.rows[:n]
	}
}

// LatestEventType is the event type of the aggregate's highest revision.
func (l *Ledger) LatestEventType(_ context.Context, aggregateID string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	latest, revision := "", 0
	for _, row := range l.rows {
		if row.AggregateID == aggregateID && row.Revision > revision {
			latest, revision = row.EventType, row.Revision
		}
	}
	return latest, nil
}
