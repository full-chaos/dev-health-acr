package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestCHAOS4418FactReadCarriesTheServerCanonicalizedWindow is codex R4
// finding 2 (P1), and executes CHAOS-4464's fix path.
//
// The engine canonicalizes exactly one evidence window per investigation --
// effectiveWindow (engine.go, composeEffectiveWindow/window carry) -- and
// that window is what the result advertises as
// EffectiveEvidenceWindow. But the fact read receives
// request.Question.TimeContext, which is the INTERPRETATION's own time
// context: the model never emits an evidence window
// (interpretationOutput.toDomain populates none), so a request whose window
// authority is a relative_id, a carried window or a redeemed receipt
// reaches buildFactQuery (fact_registry.go) with FactQuery.Time carrying no
// bounds at all. A provider then has nothing to read and defaults
// independently -- devhealthfacts' repository metrics series silently
// queries its own trailing 90 days while the answer claims to speak for
// trailing_30d.
//
// The invariant pinned here is the one that closes that class for every
// provider at once, not just the metrics series: the window the fact read
// is given IS the window the result advertises.
func TestCHAOS4418FactReadCarriesTheServerCanonicalizedWindow(t *testing.T) {
	t.Parallel()
	for _, relativeID := range []RelativeWindowID{
		RelativeWindowTrailing30D,
		RelativeWindowTrailing365D,
		RelativeWindowAllTime,
	} {
		t.Run(string(relativeID), func(t *testing.T) {
			t.Parallel()
			project := acceptanceProject()
			var captured CanonicalFactRequest
			facts := factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
				captured = request
				return bootstrapFactBundle(project), nil
			})
			graph := &acceptanceGraphReader{
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
				context:    bootstrapGraphContext(project),
			}
			engine := buildAcceptanceEngine(t, graph, facts, bootstrapInterpretation(), bootstrapDraft(project), newMapResultStore())

			request := validInvestigationRequest()
			// A "workbench"-surface explicit relative window carries
			// question_stated authority (windowExplicitProvenance), so it
			// passes the CHAOS-4040 confirmation gate and reaches the fact
			// read -- the same decisive path a redeemed window receipt or a
			// CHAOS-4360 carried window takes.
			request.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: relativeID}

			result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.EffectiveEvidenceWindow == nil {
				t.Fatalf("EffectiveEvidenceWindow = nil, want the server-canonicalized window this answer speaks for")
			}
			effective := result.EffectiveEvidenceWindow
			read := captured.Question.TimeContext.EvidenceWindow
			if read == nil {
				t.Fatalf("the fact read's own TimeContext.EvidenceWindow = nil while the answer advertises %+v -- every provider is left to default independently, so the answer's stated window and the window its evidence was read over are unrelated", effective)
			}
			if read.RelativeID != effective.RelativeID {
				t.Fatalf("fact-read RelativeID = %q, want the canonical %q", read.RelativeID, effective.RelativeID)
			}
			switch {
			case effective.Start == nil && effective.End == nil:
				// The all_time sentinel: no bounds exist by definition
				// (relativeWindowBounds' own contract). The fact read must
				// carry the sentinel WITHOUT inventing bounds for it --
				// bounds here would be a window nobody canonicalized.
				if read.Start != nil || read.End != nil {
					t.Fatalf("fact-read window = %+v, want the all_time sentinel carried with NO bounds -- never a window this engine did not derive", read)
				}
			default:
				if read.Start == nil || !read.Start.Equal(*effective.Start) {
					t.Fatalf("fact-read Start = %v, want the canonical %v", read.Start, *effective.Start)
				}
				if read.End == nil || !read.End.Equal(*effective.End) {
					t.Fatalf("fact-read End = %v, want the canonical %v", read.End, *effective.End)
				}
			}
		})
	}
}

// TestCHAOS4418FactReadWindowLeavesANonCurrentAxisAlone is the guard on the
// fix above: composeEffectiveWindow returns nil off the current axis, and a
// historical (as-of/range) question is bounded by its OWN axis instead
// (devhealthfacts' factTimeBound). Threading a window in there would
// override the requested instant with a trailing range -- a different
// question. Nothing may be written when there is no canonical window.
func TestCHAOS4418FactReadWindowLeavesANonCurrentAxisAlone(t *testing.T) {
	t.Parallel()
	project := acceptanceProject()
	var captured CanonicalFactRequest
	facts := factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
		captured = request
		return bootstrapFactBundle(project), nil
	})
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	asOf := historicalAsOf()
	interpretation := bootstrapInterpretation()
	interpretation.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &asOf}
	engine := buildAcceptanceEngine(t, graph, facts, interpretation, bootstrapDraft(project), newMapResultStore())

	request := validInvestigationRequest()
	request.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &asOf}

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.EffectiveEvidenceWindow != nil {
		t.Fatalf("EffectiveEvidenceWindow = %+v, want nil off the current axis", result.EffectiveEvidenceWindow)
	}
	if captured.Question.TimeContext.EvidenceWindow != nil {
		t.Fatalf("fact-read EvidenceWindow = %+v, want nil -- with no canonical window to carry, nothing may be invented", captured.Question.TimeContext.EvidenceWindow)
	}
	if captured.Question.TimeContext.Axis != TemporalValidTime || captured.Question.TimeContext.AsOf == nil {
		t.Fatalf("fact-read TimeContext = %+v, want the valid-time axis untouched", captured.Question.TimeContext)
	}
}
