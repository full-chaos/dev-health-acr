package synthesisprompt

import (
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// clientInputObservationPaths is the closed list of model input paths whose
// value can be the time a turn looked rather than a fact about the data. The
// client input leaves them out, so two turns over the same facts build the
// same bytes and input_sha256 names the facts the caller wrote from.
//
//   - coverage.sources[].observed_at is always the time the turn read the
//     source, and is always left out. A source's freshness fact is its
//     watermark, which stays.
//   - interpretation.time_context.{as_of,start,end} is left out only when
//     the turn's clock wrote it: a future instant pulled back to the time of
//     the read (SynthesisInput.ReadTimeClamps). The span then runs to the
//     read. An instant the caller or a stored window stated stays.
var clientInputObservationPaths = [...]string{
	"coverage.sources[].observed_at",
	"interpretation.time_context.as_of",
	"interpretation.time_context.start",
	"interpretation.time_context.end",
}

// ClientInputObservationPaths returns the paths the client input can leave out.
func ClientInputObservationPaths() []string {
	return append([]string(nil), clientInputObservationPaths[:]...)
}

// ClientInputFromDomain is the model input a caller writes the answer from:
// InputFromDomain without the values clientInputObservationPaths names.
func ClientInputFromDomain(orgID string, input contextfabric.SynthesisInput) Input {
	payload := InputFromDomain(orgID, input)
	sources := make([]contextfabric.SourceObservation, len(payload.Coverage.Sources))
	for i, source := range payload.Coverage.Sources {
		source.ObservedAt = nil
		sources[i] = source
	}
	payload.Coverage.Sources = sources
	timeContext := payload.Interpretation.TimeContext
	timeContext.AsOf = unlessReadTime(timeContext.AsOf, input.ReadTimeClamps)
	timeContext.Start = unlessReadTime(timeContext.Start, input.ReadTimeClamps)
	timeContext.End = unlessReadTime(timeContext.End, input.ReadTimeClamps)
	payload.Interpretation.TimeContext = timeContext
	return payload
}

func unlessReadTime(instant *time.Time, readTimes []time.Time) *time.Time {
	if instant == nil {
		return nil
	}
	for _, readTime := range readTimes {
		if instant.Equal(readTime) {
			return nil
		}
	}
	return instant
}

// ClientPayload returns the bounded JSON of ClientInputFromDomain.
func ClientPayload(orgID string, input contextfabric.SynthesisInput, maxBytes int) ([]byte, error) {
	return encodeBounded(ClientInputFromDomain(orgID, input), maxBytes)
}
