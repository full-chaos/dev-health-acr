package contextfabric

import (
	"context"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4632 §4.3: family-resolution telemetry.
//
// CLOSED ENUMS ONLY -- no question text, no subject identifier, no scope
// anchor text. ScopeAnchorTerm in particular is free-form model output and
// NEVER appears in a telemetry field; only whether it was set.
//
// WHY THE PER-SAMPLE ROWS. The design's round-2 review found the original
// event recorded only the OUTCOME, so a downgraded decision could not be
// diagnosed from the run's own artifacts -- which is precisely the bar
// AGENTS.md's CANONICAL ARCHITECTURE section sets ("a defect there must be
// diagnosable from the run's own completed artifacts alone... never by
// re-reading source, re-running with instrumentation added after the
// fact") and the same class lane-4579's codex round found in its finding
// 5. Singular fields cannot represent two samples failing DIFFERENT
// precedence rows with DIFFERENT attempted families, which is exactly the
// diagnosis a split consensus needs. So: one row per sample.
//
// WHY IT FIRES ON EVERY INVESTIGATION, including unclassified ones: the
// DENOMINATOR has to be countable. An event that fires only on success
// makes "the resolver never classifies anything" indistinguishable from
// "the resolver never ran" -- lane-4579 wrote this up in its §4 and codex
// confirmed it by mutation.

// QuestionFamilySampleRow is one sample's row in the resolution event.
type QuestionFamilySampleRow struct {
	// Shape is this sample's own InvestigationShape -- a closed enum.
	Shape InvestigationShape
	// AttemptedFamily is what the model picked for this sample, empty
	// when it picked nothing or picked an unrecognized name (an
	// unrecognized name is reported through Reason, never echoed).
	AttemptedFamily QuestionFamily
	// ResolvedFamily is what the precedence table produced for it.
	ResolvedFamily QuestionFamily
	// Row names which precedence rule fired.
	Row FamilyPrecedenceRow
	// Reason names why AttemptedFamily was not used, empty when it was.
	Reason FamilyIncompatibilityReason
	// GroupKindSet / ScopeAnchorSet are BOOLEANS, deliberately. The
	// grouping kind is a closed vocabulary member and could safely be
	// reported, but the scope anchor is free text and cannot; reporting
	// one as a value and one as a boolean invites a later edit that
	// "makes them consistent" by promoting the anchor. Both stay
	// booleans, and the family the group kind produced is already
	// visible through Row == group_kind_set.
	GroupKindSet   bool
	ScopeAnchorSet bool
}

// QuestionFamilyResolutionEvent is the whole §4.3 event.
type QuestionFamilyResolutionEvent struct {
	Family QuestionFamily
	Source QuestionFamilySource
	// SampleFamilies is the per-sample post-filter distribution: closed
	// keys, counts only.
	SampleFamilies map[QuestionFamily]int
	Samples        []QuestionFamilySampleRow
	// DowngradedCount is how many samples were downgraded.
	DowngradedCount int
	// ConsensusFieldDivergence counts majority samples that agreed on the
	// family but disagreed with the winner on GroupKind or the anchor --
	// the state that should later justify ASKING rather than assuming.
	ConsensusFieldDivergence int
	// EnsembleSize is N as actually run, so a cost question ("what is this
	// costing per turn 1?") is answerable from the telemetry stream
	// itself rather than from configuration archaeology.
	EnsembleSize int
	// FamilyVersion is the definition-table version.
	FamilyVersion string
	// Shadow is the frame-projection comparison (CHAOS-4452 stage 2,
	// design 13.4.1), carried on THIS event rather than on one of its own.
	//
	// ONE LINE PER DECISION, because the two halves are only meaningful
	// together. A separate event would make an operator join two streams
	// to answer "what did we route, what would the frame have routed, and
	// why did they differ" -- and a join that can fail is a diagnosis that
	// can fail, which is the artifact-diagnosability bar this event was
	// widened once already to meet.
	//
	// Zero value when no frame reached validation on this interpretation,
	// and Shadow.FrameObserved is what says so. It is NOT the same state
	// as a comparison that ran and agreed.
	Shadow FamilyAgreementShadow
}

// QuestionFamilyResolutionEventFrom projects a resolver outcome into the
// telemetry event. Kept as its own function, not folded into the resolver,
// so the resolver stays free of any telemetry concern and so a test can
// assert the PROJECTION independently of the aggregation.
func QuestionFamilyResolutionEventFrom(outcome QuestionFamilyOutcome, samples []FamilySample) QuestionFamilyResolutionEvent {
	event := QuestionFamilyResolutionEvent{
		Family:                   outcome.Family,
		Source:                   outcome.Source,
		SampleFamilies:           outcome.SampleFamilies,
		DowngradedCount:          outcome.DowngradedCount,
		ConsensusFieldDivergence: outcome.ConsensusFieldDivergence,
		EnsembleSize:             len(samples),
		FamilyVersion:            outcome.Version,
	}
	event.Samples = make([]QuestionFamilySampleRow, 0, len(outcome.Samples))
	for i, resolved := range outcome.Samples {
		row := QuestionFamilySampleRow{
			AttemptedFamily: resolved.AttemptedFamily,
			ResolvedFamily:  resolved.Family,
			Row:             resolved.Row,
			Reason:          resolved.IncompatibilityReason,
		}
		if i < len(samples) {
			row.Shape = samples[i].Shape
			row.GroupKindSet = samples[i].GroupKind != ""
			row.ScopeAnchorSet = samples[i].ScopeAnchorTerm != ""
		}
		event.Samples = append(event.Samples, row)
	}
	return event
}

// QuestionFamilyTelemetry is the port the family resolver reports through.
//
// A SEPARATE, EXPLICITLY-WIRED interface rather than a method bolted onto
// EngineTelemetry -- and explicitly NOT an optional interface discovered
// by type assertion. CHAOS-4085's whole lesson (see
// chaos4085_telemetry_sink_test.go's header) is that
// CommitAffirmationTelemetry was optional, nothing in production
// implemented it, every retraction failed a type assertion, and the entire
// event disappeared with tests passing throughout. SlogEngineTelemetry
// implements this interface directly and the engine holds it as a required
// dependency, so a build in which nothing emits these events does not
// compile.
type QuestionFamilyTelemetry interface {
	// RecordQuestionFamilyResolution reports ONE investigation's family
	// resolution. Fired on EVERY investigation that reaches the resolver,
	// including when the outcome is unclassified and including when the
	// ensemble was rejected for having no majority.
	RecordQuestionFamilyResolution(ctx context.Context, principal storage.Principal, event QuestionFamilyResolutionEvent)
}

// sortedFamilyDistribution renders SampleFamilies as a stable, sorted
// slice of alternating family/count pairs for logging.
//
// Sorted because a map's iteration order is randomized in Go: logging it
// directly would make two identical resolutions produce different log
// lines, which breaks both grep-based operations and any log-diffing
// regression check. Flat pairs rather than a nested object because slog's
// JSON handler renders a []any of scalars as a plain array an operator can
// read, without a per-event type.
func sortedFamilyDistribution(distribution map[QuestionFamily]int) []any {
	families := make([]QuestionFamily, 0, len(distribution))
	for family := range distribution {
		families = append(families, family)
	}
	sort.Slice(families, func(a, b int) bool { return families[a] < families[b] })
	out := make([]any, 0, len(families)*2)
	for _, family := range families {
		out = append(out, string(family), distribution[family])
	}
	return out
}
