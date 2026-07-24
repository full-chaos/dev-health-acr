package contextpacket

import (
	"sort"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func validatedEvidenceWatermarks(in []contractsv1.SourceWatermark, validation evidenceValidation, asOf time.Time) []contractsv1.SourceWatermark {
	latest := make(map[string]time.Time)
	for _, ref := range validation.valid {
		recordLatestEvidence(latest, ref.Source.System, ref.ObservedAt)
		if _, recognized := evidenceSourceCodes[ref.SourceVersion]; recognized {
			recordLatestEvidence(latest, ref.SourceVersion, ref.ObservedAt)
		}
	}
	affected := make(map[string]bool)
	for _, source := range validation.quarantinedWatermarkSources {
		affected[source] = true
	}

	out := append(make([]contractsv1.SourceWatermark, 0, len(in)), in...)
	for index := range out {
		if !affected[out[index].Source] {
			continue
		}
		observed, found := latest[out[index].Source]
		if !found {
			out[index].LastIngestedAt = nil
			if out[index].Status == "fresh" || out[index].Status == "stale" {
				out[index].Status = "missing"
			}
			continue
		}
		out[index].LastIngestedAt = &observed
		out[index].Status = "fresh"
		if observed.Before(asOf.Add(-staleAfterSeconds * time.Second)) {
			out[index].Status = "stale"
		}
	}
	return out
}

func recordLatestEvidence(latest map[string]time.Time, source string, observed time.Time) {
	if current, found := latest[source]; !found || observed.After(current) {
		latest[source] = observed
	}
}

func applyEvidenceQuarantine(packet *contractsv1.ContextPacket, validation evidenceValidation) {
	if len(validation.quarantined) == 0 {
		return
	}

	reasons := make([]string, 0, len(validation.quarantined))
	sources := make([]string, 0, len(validation.quarantined))
	for _, rowErr := range validation.quarantined {
		reasons = append(reasons, rowErr.reason())
		sources = append(sources, rowErr.safeSource())
	}
	packet.Coverage.Partial = true
	packet.Coverage.SourcesConsidered = sortedUnique(append(packet.Coverage.SourcesConsidered, sources...))
	packet.Coverage.DegradedReasons = sortedUnique(append(packet.Coverage.DegradedReasons, reasons...))
	packet.Warnings = sortedUnique(append(packet.Warnings, reasons...))
	if len(validation.valid) == 0 {
		packet.Summary = "Evidence was retrieved but failed validation for the requested goal."
	}
}

func evidenceQuarantineObservations(validation evidenceValidation) []EvidenceQuarantineObservation {
	counts := make(map[EvidenceQuarantineObservation]int, len(validation.quarantined))
	for _, rowErr := range validation.quarantined {
		observation := EvidenceQuarantineObservation{
			Source:   rowErr.safeSource(),
			RuleCode: evidenceRuleCode(rowErr.Rule),
		}
		counts[observation]++
	}
	observations := make([]EvidenceQuarantineObservation, 0, len(counts))
	for observation, count := range counts {
		observation.Count = count
		observations = append(observations, observation)
	}
	sort.Slice(observations, func(i, j int) bool {
		if observations[i].Source == observations[j].Source {
			return observations[i].RuleCode < observations[j].RuleCode
		}
		return observations[i].Source < observations[j].Source
	})
	return observations
}

func safeEvidenceQuarantineSource(source string) string {
	if _, ok := evidenceSourceCodes[source]; ok {
		return source
	}
	return "unknown_source"
}

// NormalizeEvidenceQuarantineObservation prevents callers of the observability
// seam from reflecting arbitrary source or rule values into telemetry.
func NormalizeEvidenceQuarantineObservation(observation EvidenceQuarantineObservation) EvidenceQuarantineObservation {
	observation.Source = safeEvidenceQuarantineSource(observation.Source)
	switch observation.RuleCode {
	case "invalid_shape", "invalid_confidence", "invalid_provenance", "invalid_availability":
	default:
		observation.RuleCode = "invalid_other"
	}
	return observation
}
