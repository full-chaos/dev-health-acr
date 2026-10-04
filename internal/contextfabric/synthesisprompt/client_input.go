package synthesisprompt

import (
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// clientInputObservationPaths is the closed list of client input paths whose
// value can be the time a turn looked rather than a fact about the data. The
// client input leaves them out, so two turns over the same facts build the
// same bytes and input_sha256 names the facts the caller wrote from.
//
//   - coverage.sources[].observed_at is always the time the turn read the
//     source, and is always left out. A source's freshness fact is its
//     watermark, which stays.
//   - interpretation.time_context.{as_of,start,end} and
//     evidence_window.{start,end} are left out only when the turn's clock
//     wrote them: a future instant pulled back to the time of the read
//     (SynthesisInput.ReadTimeClamp). The span then runs to the read. An
//     instant the caller or a stored window stated stays.
//   - canonical_facts[].fields.{window_start,window_end} are left out when
//     the fact's window_basis says the provider chose the window from its own
//     clock, or when the value is a bound this turn resolved from its clock:
//     a bound of a relative evidence window or an instant a clamp wrote. A
//     stated bound stays.
var clientInputObservationPaths = [...]string{
	"coverage.sources[].observed_at",
	"interpretation.time_context.as_of",
	"interpretation.time_context.start",
	"interpretation.time_context.end",
	"evidence_window.start",
	"evidence_window.end",
	"canonical_facts[].fields." + contextfabric.FactFieldWindowStart,
	"canonical_facts[].fields." + contextfabric.FactFieldWindowEnd,
}

// ClientInputObservationPaths returns the paths the client input can leave out.
func ClientInputObservationPaths() []string {
	return append([]string(nil), clientInputObservationPaths[:]...)
}

// ClientInput is the model input a caller writes the answer from: the input
// the service's own model is given, without the values
// clientInputObservationPaths names, and with the evidence window the facts
// were read over.
type ClientInput struct {
	Input
	EvidenceWindow *ClientEvidenceWindow `json:"evidence_window,omitempty"`
}

// ClientEvidenceWindow names the evidence window of the turn. A relative
// window is named by its id alone, because its bounds move with the clock; a
// stated window carries its bounds.
type ClientEvidenceWindow struct {
	RelativeID  contractsv1.ContextFabricRelativeWindowID `json:"relative_id,omitempty"`
	WindowClass contractsv1.ContextFabricWindowClass      `json:"window_class,omitempty"`
	Start       *time.Time                                `json:"start,omitempty"`
	End         *time.Time                                `json:"end,omitempty"`
}

// ClientInputFromDomain builds the ClientInput of input.
func ClientInputFromDomain(orgID string, input contextfabric.SynthesisInput) ClientInput {
	return canonicalClientInput(InputFromDomain(orgID, input), input.EvidenceWindow, input.ReadTimeClamp)
}

// canonicalClientInput leaves out of payload the values the turn's clock
// wrote and adds the window. It never changes the slices, maps or pointers
// payload shares with the domain input.
func canonicalClientInput(payload Input, window *contractsv1.ContextFabricEffectiveEvidenceWindow, clamp contextfabric.ReadTimeClamp) ClientInput {
	sources := make([]contextfabric.SourceObservation, len(payload.Coverage.Sources))
	for i, source := range payload.Coverage.Sources {
		source.ObservedAt = nil
		sources[i] = source
	}
	payload.Coverage.Sources = sources
	timeContext := payload.Interpretation.TimeContext
	timeContext.AsOf = unlessClockWrote(timeContext.AsOf, clamp.AsOf, clamp.At)
	timeContext.Start = unlessClockWrote(timeContext.Start, clamp.Start, clamp.At)
	timeContext.End = unlessClockWrote(timeContext.End, clamp.End, clamp.At)
	payload.Interpretation.TimeContext = timeContext
	payload.Facts = withoutClockWindowEchoes(payload.Facts, clockResolvedBounds(window, clamp))
	return ClientInput{Input: payload, EvidenceWindow: clientEvidenceWindow(window, clamp)}
}

func clientEvidenceWindow(window *contractsv1.ContextFabricEffectiveEvidenceWindow, clamp contextfabric.ReadTimeClamp) *ClientEvidenceWindow {
	if window == nil {
		return nil
	}
	descriptor := &ClientEvidenceWindow{RelativeID: window.RelativeID, WindowClass: window.WindowClass}
	if window.RelativeID == "" {
		descriptor.Start = unlessClockWrote(window.Start, clamp.WindowStart, clamp.At)
		descriptor.End = unlessClockWrote(window.End, clamp.WindowEnd, clamp.At)
	}
	return descriptor
}

// clockResolvedBounds returns, in the form a fact echoes a window bound, the
// bounds this turn resolved from its clock.
func clockResolvedBounds(window *contractsv1.ContextFabricEffectiveEvidenceWindow, clamp contextfabric.ReadTimeClamp) map[string]bool {
	bounds := map[string]bool{}
	if window != nil && window.RelativeID != "" {
		for _, bound := range []*time.Time{window.Start, window.End} {
			if bound != nil {
				bounds[bound.UTC().Format(time.RFC3339)] = true
			}
		}
	}
	if !clamp.At.IsZero() {
		bounds[clamp.At.UTC().Format(time.RFC3339)] = true
	}
	return bounds
}

func withoutClockWindowEchoes(facts []contextfabric.CanonicalFact, clockBounds map[string]bool) []contextfabric.CanonicalFact {
	out := make([]contextfabric.CanonicalFact, len(facts))
	for i, fact := range facts {
		out[i] = fact
		providerClock := stringField(fact.Fields, contextfabric.FactFieldWindowBasis) == contextfabric.FactWindowBasisDefaultTrailing
		var drop []string
		for _, name := range []string{contextfabric.FactFieldWindowStart, contextfabric.FactFieldWindowEnd} {
			value, ok := fact.Fields[name]
			if !ok || value.String == nil {
				continue
			}
			if providerClock || clockBounds[*value.String] {
				drop = append(drop, name)
			}
		}
		if len(drop) == 0 {
			continue
		}
		fields := make(map[string]contextfabric.FactValue, len(fact.Fields))
		for name, value := range fact.Fields {
			fields[name] = value
		}
		for _, name := range drop {
			delete(fields, name)
		}
		out[i].Fields = fields
	}
	return out
}

func stringField(fields map[string]contextfabric.FactValue, name string) string {
	if value, ok := fields[name]; ok && value.String != nil {
		return *value.String
	}
	return ""
}

// unlessClockWrote is nil when the clamp wrote this field and the field still
// holds the instant it wrote.
func unlessClockWrote(instant *time.Time, wrote bool, at time.Time) *time.Time {
	if instant == nil || !wrote || !instant.Equal(at) {
		return instant
	}
	return nil
}

// ClientPayload returns the bounded JSON of ClientInputFromDomain.
func ClientPayload(orgID string, input contextfabric.SynthesisInput, maxBytes int) ([]byte, error) {
	return encodeBounded(ClientInputFromDomain(orgID, input), maxBytes)
}
