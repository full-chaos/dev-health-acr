package authprojection

import (
	"context"
	"sort"
	"time"
)

// Event is one outbox row as the publisher sends it: Body is the stored envelope, posted byte for byte.
type Event struct {
	AggregateID string
	Revision    int
	Body        []byte
}

// Store claims one event at a time. PublishNext claims the oldest event whose aggregate is not in held and has no
// earlier unpublished revision, calls send inside the claim and marks the event published only when send returns
// true. found is false when nothing is eligible. An error is a store failure.
type Store interface {
	PublishNext(ctx context.Context, held []string, send func(context.Context, Event) bool) (found bool, event Event, err error)
}

// Poster is *Client's Post.
type Poster interface {
	Post(ctx context.Context, body []byte) Result
}

const (
	// DefaultBackoff is the first hold of an aggregate, and the pause of a busy answer that names no Retry-After.
	DefaultBackoff = 30 * time.Second
	// DefaultMaxHeld bounds the held set; a hold that would exceed it stops the tick instead.
	DefaultMaxHeld = 1000
	// staleAfter drops a hold nobody renewed: an aggregate retried after its backoff is held again at once, so a hold
	// idle this long belongs to an aggregate that was acknowledged by another pod or no longer exists.
	staleAfter = 2 * MaxRetryAfter
)

// Report counts one tick. Stopped is the outcome that ended it early, empty when the batch ran out or filled.
type Report struct {
	Applied, Gap, DigestMismatch, Refused, Other, NoAnswer int
	Stopped                                                Outcome
	// Paused is a tick skipped inside a Retry-After.
	Paused bool
	// Full is a batch that reached its limit: more events wait for the next tick.
	Full bool
	// Held is the number of aggregates excluded from the claim after this tick.
	Held int
	// HeldFull is a stop because the held set is at its bound.
	HeldFull bool
}

type hold struct {
	until    time.Time
	failures int
}

// Publisher is the delivery policy: one event per transaction, per-aggregate head-of-line order, a bounded in-memory
// held set, and a pause for a busy endpoint. It is driven by one goroutine; its state is lost on restart, which only
// retries what it held.
type Publisher struct {
	store      Store
	poster     Poster
	now        func() time.Time
	maxHeld    int
	held       map[string]hold
	pauseUntil time.Time
}

// NewPublisher returns a publisher; a nil now is time.Now.
func NewPublisher(store Store, poster Poster, now func() time.Time) *Publisher {
	if now == nil {
		now = time.Now
	}
	return &Publisher{store: store, poster: poster, now: now, maxHeld: DefaultMaxHeld, held: map[string]hold{}}
}

// Tick publishes up to limit events. A store failure ends the tick with its error; every endpoint answer is
// classified into the report and never returned as an error.
func (p *Publisher) Tick(ctx context.Context, limit int) (Report, error) {
	var report Report
	now := p.now()
	if now.Before(p.pauseUntil) {
		report.Paused = true
		report.Held = p.heldCount()
		return report, nil
	}
	for id, h := range p.held {
		if now.After(h.until.Add(staleAfter)) {
			delete(p.held, id)
		}
	}
	for sent := 0; sent < limit; sent++ {
		var result Result
		found, event, err := p.store.PublishNext(ctx, p.heldIDs(), func(ctx context.Context, event Event) bool {
			result = p.poster.Post(ctx, event.Body)
			return result.Outcome == OutcomeApplied
		})
		if err != nil {
			report.Held = p.heldCount()
			return report, err
		}
		if !found {
			report.Held = p.heldCount()
			return report, nil
		}
		if p.apply(&report, event.AggregateID, result) {
			report.Held = p.heldCount()
			return report, nil
		}
		if sent == limit-1 {
			report.Full = true
		}
	}
	report.Held = p.heldCount()
	return report, nil
}

// apply records one result and reports whether the tick stops.
func (p *Publisher) apply(report *Report, aggregateID string, result Result) bool {
	switch result.Outcome {
	case OutcomeApplied:
		report.Applied++
		delete(p.held, aggregateID)
	case OutcomeGap:
		report.Gap++
		return p.hold(report, aggregateID)
	case OutcomeDigestMismatch:
		report.DigestMismatch++
		return p.hold(report, aggregateID)
	case OutcomeRefused:
		report.Refused++
		return p.hold(report, aggregateID)
	case OutcomeNoAnswer:
		report.NoAnswer++
		p.hold(report, aggregateID)
		report.Stopped = OutcomeNoAnswer
		return true
	case OutcomeBusy:
		wait := result.RetryAfter
		if wait <= 0 {
			wait = DefaultBackoff
		}
		p.pauseUntil = p.now().Add(wait)
		report.Stopped = OutcomeBusy
		return true
	case OutcomeAuthRefused, OutcomeNoToken:
		report.Stopped = result.Outcome
		return true
	default:
		report.Other++
		return p.hold(report, aggregateID)
	}
	return false
}

// hold excludes the aggregate from the claim for an exponentially growing time; at the bound it stops the tick.
func (p *Publisher) hold(report *Report, aggregateID string) bool {
	h, held := p.held[aggregateID]
	if !held && len(p.held) >= p.maxHeld {
		report.HeldFull = true
		report.Stopped = "held_full"
		return true
	}
	h.failures++
	wait := DefaultBackoff
	for i := 1; i < h.failures && wait < MaxRetryAfter; i++ {
		wait *= 2
	}
	if wait > MaxRetryAfter {
		wait = MaxRetryAfter
	}
	h.until = p.now().Add(wait)
	p.held[aggregateID] = h
	return false
}

func (p *Publisher) heldCount() int { return len(p.heldIDs()) }

// heldIDs are the aggregates still inside their backoff, sorted.
func (p *Publisher) heldIDs() []string {
	now := p.now()
	ids := make([]string, 0, len(p.held))
	for id, h := range p.held {
		if now.Before(h.until) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
