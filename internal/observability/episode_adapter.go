package observability

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/episode"
)

type episodeTerminalObserver struct {
	hooks Hooks
}

func NewEpisodeTerminalObserver(hooks Hooks) episode.TerminalObserver {
	return episodeTerminalObserver{hooks: hooks}
}

func (o episodeTerminalObserver) ObserveEpisodeTerminal(ctx context.Context, observation episode.TerminalObservation) {
	o.hooks.ObserveEpisode(ctx, EpisodeObservation{
		Outcome:        terminalOutcome(observation.Outcome),
		EpisodeOutcome: episodeOutcome(observation.Outcome),
		AuditDelivery:  auditDelivery(observation.AuditDelivery),
		Duration:       observation.Duration,
	})
}

type episodeStoreObserver struct{ hooks Hooks }

func NewEpisodeStoreObserver(hooks Hooks) episode.StoreObserver {
	return episodeStoreObserver{hooks: hooks}
}

func (o episodeStoreObserver) ObserveEpisodeStore(ctx context.Context, observation episode.StoreCallObservation) {
	o.hooks.ObserveStore(ctx, StoreObservation{
		QueryClass: StoreQueryEpisode, Backend: episodeStoreBackend(observation.Backend), Outcome: storeCallOutcome(observation.Outcome),
		Duration: observation.Duration, TimedOut: observation.TimedOut,
	})
}

func episodeStoreBackend(backend episode.StoreBackend) StoreBackend {
	switch backend {
	case episode.StoreBackendMemory:
		return StoreBackendMemory
	case episode.StoreBackendPostgres:
		return StoreBackendPostgres
	default:
		return StoreBackendUnknown
	}
}

func terminalOutcome(outcome episode.TerminalOutcome) Outcome {
	switch outcome {
	case episode.TerminalOutcomeSuccess, episode.TerminalOutcomeDuplicate, episode.TerminalOutcomeRedacted:
		return OutcomeSuccess
	case episode.TerminalOutcomeFailure:
		return OutcomeFailure
	default:
		return OutcomeUnknown
	}
}

func storeCallOutcome(outcome episode.StoreCallOutcome) Outcome {
	switch outcome {
	case episode.StoreCallSuccess:
		return OutcomeSuccess
	case episode.StoreCallCanceled:
		return OutcomeCanceled
	case episode.StoreCallFailure:
		return OutcomeFailure
	default:
		return OutcomeUnknown
	}
}

func episodeOutcome(outcome episode.TerminalOutcome) EpisodeOutcome {
	switch outcome {
	case episode.TerminalOutcomeSuccess:
		return EpisodeOutcomeSuccess
	case episode.TerminalOutcomeFailure:
		return EpisodeOutcomeFailure
	case episode.TerminalOutcomeDuplicate:
		return EpisodeOutcomeDuplicate
	case episode.TerminalOutcomeRedacted:
		return EpisodeOutcomeRedacted
	default:
		return EpisodeOutcomeUnknown
	}
}

func auditDelivery(delivery episode.AuditDelivery) AuditDelivery {
	switch delivery {
	case episode.AuditDeliveryDelivered:
		return AuditDeliveryDelivered
	case episode.AuditDeliveryFailed:
		return AuditDeliveryFailed
	case episode.AuditDeliverySkipped:
		return AuditDeliverySkipped
	default:
		return AuditDeliveryUnknown
	}
}
