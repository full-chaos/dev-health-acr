package episode

import (
	"context"
	"errors"
	"time"
)

type TerminalOutcome string

const (
	TerminalOutcomeSuccess   TerminalOutcome = "success"
	TerminalOutcomeFailure   TerminalOutcome = "failure"
	TerminalOutcomeDuplicate TerminalOutcome = "duplicate"
	TerminalOutcomeRedacted  TerminalOutcome = "redacted"
)

type AuditDelivery string

const (
	AuditDeliveryDelivered AuditDelivery = "delivered"
	AuditDeliveryFailed    AuditDelivery = "failed"
	AuditDeliverySkipped   AuditDelivery = "skipped"
)

// TerminalObservation contains only bounded operational dimensions.
type TerminalObservation struct {
	Outcome       TerminalOutcome
	AuditDelivery AuditDelivery
	Duration      time.Duration
}

type StoreCallOutcome string

const (
	StoreCallSuccess  StoreCallOutcome = "success"
	StoreCallFailure  StoreCallOutcome = "failure"
	StoreCallCanceled StoreCallOutcome = "canceled"
)

type StoreCallObservation struct {
	Outcome  StoreCallOutcome
	Backend  StoreBackend
	Duration time.Duration
	TimedOut bool
}

type StoreBackend string

const (
	StoreBackendUnknown  StoreBackend = "unknown"
	StoreBackendMemory   StoreBackend = "memory"
	StoreBackendPostgres StoreBackend = "postgres"
)

type TerminalObserver interface {
	ObserveEpisodeTerminal(context.Context, TerminalObservation)
}

type StoreObserver interface {
	ObserveEpisodeStore(context.Context, StoreCallObservation)
}

func (s *Service) observeTerminal(ctx context.Context, observation TerminalObservation) {
	if s.observer == nil {
		return
	}
	defer func() { _ = recover() }()
	s.observer.ObserveEpisodeTerminal(context.WithoutCancel(ctx), observation)
}

func (s *Service) observeStoreCall(ctx context.Context, started time.Time, err error) {
	if s.storeObserver == nil {
		return
	}
	outcome := StoreCallSuccess
	if err != nil {
		outcome = StoreCallFailure
	}
	if errors.Is(err, context.Canceled) {
		outcome = StoreCallCanceled
	}
	observation := StoreCallObservation{
		Outcome: outcome, Backend: s.storeBackend, Duration: max(s.now().Sub(started), 0),
		TimedOut: errors.Is(err, context.DeadlineExceeded),
	}
	defer func() { _ = recover() }()
	s.storeObserver.ObserveEpisodeStore(context.WithoutCancel(ctx), observation)
}
