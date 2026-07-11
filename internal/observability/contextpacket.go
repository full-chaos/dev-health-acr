package observability

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type evidenceExpansionObserver struct {
	hooks Hooks
}

func NewEvidenceExpansionObserver(hooks Hooks) contextpacket.EvidenceExpansionObserver {
	return evidenceExpansionObserver{hooks: hooks}
}

func (o evidenceExpansionObserver) ObserveEvidenceExpansion(ctx context.Context, observation contextpacket.EvidenceExpansionObservation) {
	fallback, coverage := evidenceDimensions(observation.Availability)
	o.hooks.ObserveEvidence(ctx, EvidenceObservation{
		Outcome:        assemblyOutcome(observation.Outcome),
		Duration:       observation.Duration,
		SourceFallback: fallback,
		SourceCoverage: coverage,
	})
}

func evidenceDimensions(availability contractsv1.EvidenceAvailability) (SourceFallback, SourceCoverage) {
	switch availability {
	case contractsv1.EvidenceAvailable:
		return SourceFallbackNone, SourceCoverageFull
	case contractsv1.EvidenceStale:
		return SourceFallbackNone, SourceCoveragePartial
	case contractsv1.EvidenceRedacted, contractsv1.EvidenceUnauthorized:
		return SourceFallbackUnavailable, SourceCoverageNone
	default:
		return SourceFallbackUnknown, SourceCoverageUnknown
	}
}
