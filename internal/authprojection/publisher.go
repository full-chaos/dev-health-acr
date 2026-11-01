package authprojection

import (
	"context"
	"time"
)

type Event struct {
	AggregateID string
	Revision    int
	Body        []byte
}

type Store interface {
	PublishNext(ctx context.Context, held []string, send func(context.Context, Event) bool) (found bool, event Event, err error)
}

type Poster interface {
	Post(ctx context.Context, body []byte) Result
}

const (
	DefaultBackoff = 30 * time.Second
	DefaultMaxHeld = 1000
)

type Report struct {
	Applied, Gap, DigestMismatch, Refused, Other, NoAnswer int
	Stopped                                                Outcome
	Paused                                                 bool
	Full                                                   bool
	Held                                                   int
	HeldFull                                               bool
}

type hold struct {
	until    time.Time
	failures int
}

type Publisher struct {
	maxHeld int
	held    map[string]hold
}

func NewPublisher(Store, Poster, func() time.Time) *Publisher {
	return &Publisher{maxHeld: DefaultMaxHeld, held: map[string]hold{}}
}

func (p *Publisher) Tick(context.Context, int) (Report, error) { return Report{}, nil }
