package contextfabric

import (
	"context"
	"errors"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4234 (team-lead ruling 2026-08-24): kind/handle/candidate offers
// under the class-default window gate ("regime A").
//
// Before this change the gate (engine.go, WindowCanonicalizationGatedClassDefault)
// returned before ResolveSubjects ever ran, so windowConfirmationRequiredResult
// could only compose a window-ONLY disclosure (CHAOS-4118 option (a)). The
// two kiac replicates of record showed the cost: 45/50 and 43/48 positive-arm
// expected_kind offer misses, and 14/25, 13/24 subject_handle misses, were
// regime-A rows with no candidate pool at all -- not a ranking or recall
// problem, a "never ran" problem -- and a case that spends turn 1 on the
// window and turn 2 on the kind never reaches an answer inside the harness.
//
// The ruling: run ResolveSubjects under the gate in an OFFERS-ONLY mode,
// keep its StructureOfferMaterial, DISCARD every commit-bearing output
// (resolution, commit bases, commit digests), and compose the kind/handle/
// candidate offers beside the window offer. Ratified with it: "offers
// minted under an inferred window are non-decisive disclosure, the same
// class as the window offer itself" -- so the CHAOS-4040 bar (no committed
// material may reach a terminal under an inferred window; window_commit_count
// stays 0; gate order unchanged) is untouched, CHAOS-4118(a) is extended
// rather than reversed, and CHAOS-4039's noninterference proof is untouched
// because the unconfirmed-hint path never changes and offers never feed the
// commit gate.
//
// Two layers make this safe, and only the FIRST is load-bearing:
//
//  1. The engine discards the resolution unconditionally (gatedOfferMaterial
//     below keeps only the material). This holds for ANY GraphReader,
//     including one that ignores the mode flag entirely.
//  2. The mode flag (WithOffersOnlyResolution, read by graphrank's
//     resolveSubjects) is a COST optimisation: graphrank skips the shadow
//     evidence round/census, the confirmed-kind truncation scoping pass,
//     and survivors-first reordering -- commit mechanisms whose output the
//     engine would throw away anyway. It rides ctx, not the GraphReader
//     port signature, deliberately: a reader that never learns the flag
//     merely pays for reads it did not need, it cannot produce a wrong
//     outcome (the team-lead veto on ctx-carried LOAD-BEARING data,
//     engine.go's reuseWatermarkSnapshot comment, does not apply to a
//     flag whose omission is safe by construction).
//
// The offers-only pass is window-agnostic by construction: the inferred
// window lives in effectiveWindow (composeEffectiveWindow, window.go), never
// in interpretation.TimeContext, and the graph request threaded into
// ResolveSubjects is the SAME graphRequest the decisive path would use --
// so an unconfirmed window cannot shape which candidates are offered.
//
// Diagram of the gated path and the trace/report fields:
// docs/design/context-fabric-regime-a-offers.md.

type offersOnlyResolutionKey struct{}

// WithOffersOnlyResolution marks ctx so graphrank.ResolveSubjects runs in
// offers-only mode -- see this file's own doc comment for what that skips
// and why omitting the mark is safe.
func WithOffersOnlyResolution(ctx context.Context) context.Context {
	return context.WithValue(ctx, offersOnlyResolutionKey{}, true)
}

// OffersOnlyResolution reports whether ctx carries WithOffersOnlyResolution's
// mark. False for a nil-valued or unmarked ctx.
func OffersOnlyResolution(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(offersOnlyResolutionKey{}).(bool)
	return marked
}

// GatedOfferResolutionOutcome is the closed vocabulary
// EngineTelemetry.RecordGatedOfferResolution reports for every class-default
// gated request -- the engine-side twin of the harness's own
// turn1_offer_composed_under_window_gate row field.
type GatedOfferResolutionOutcome string

const (
	// GatedOfferResolutionComposed: the offers-only pass ran and produced
	// at least one offerable member beside the window.
	GatedOfferResolutionComposed GatedOfferResolutionOutcome = "composed"
	// GatedOfferResolutionEmpty: the pass ran and had nothing to offer
	// (empty pool, single-kind pool, no handles) -- window-only disclosure.
	GatedOfferResolutionEmpty GatedOfferResolutionOutcome = "empty"
	// GatedOfferResolutionFailed: the pass returned an error; the gated
	// terminal fell OPEN to the window-only disclosure it always composed.
	GatedOfferResolutionFailed GatedOfferResolutionOutcome = "failed"
	// GatedOfferResolutionDisabled: EngineOptions.RegimeAOffersDisabled is
	// set -- pre-CHAOS-4234 behaviour, no graph read under the gate.
	GatedOfferResolutionDisabled GatedOfferResolutionOutcome = "disabled"
	// GatedOfferResolutionRefused: the request disallowed clarification, so
	// the gate composes a refused no_match terminal with nothing to offer
	// and the pass is never run.
	GatedOfferResolutionRefused GatedOfferResolutionOutcome = "refused"
	// GatedOfferResolutionNotProjected (codex round-2 finding #2): the
	// pass returned ErrGraphNotProjected -- this organization's graph does
	// not exist yet, distinct from a genuinely empty projected pool. The
	// gated terminal still degrades to window-only disclosure exactly as
	// GatedOfferResolutionEmpty does (same as the decisive path's own
	// ErrGraphNotProjected handling, engine.go), but a reader must be able
	// to tell "nothing to offer" apart from "no graph to search" without
	// transcript archaeology -- the same distinction
	// subjectlessTerminalReasons' "graph_not_projected" value already
	// makes for the decisive path (see TestEngineInvestigate_
	// NeverProjectedOrgDegradesToCleanTerminal, chaos4077_never_projected_org_test.go).
	GatedOfferResolutionNotProjected GatedOfferResolutionOutcome = "not_projected"
)

// gatedOfferMaterial runs the offers-only resolution for a class-default
// gated request and returns ONLY its StructureOfferMaterial (prior-offer
// consultation applied, same as the decisive path). Every error path
// returns empty material: the gated terminal must never be blocked by a
// read whose only purpose is a better clarification.
func (e *Engine) gatedOfferMaterial(ctx context.Context, principal storage.Principal, request InvestigationRequest, graphRequest InvestigationRequest, interpretation InterpretedQuestion, binding ResolvedGraphBinding, structureCanon requestStructureCanonicalization, priorEntries []StructurePriorEntry) StructureOfferMaterial {
	record := func(outcome GatedOfferResolutionOutcome) {
		if e.telemetry != nil {
			e.telemetry.RecordGatedOfferResolution(ctx, principal, outcome)
		}
	}
	if !request.Options.AllowClarification {
		record(GatedOfferResolutionRefused)
		return StructureOfferMaterial{}
	}
	if e.regimeAOffersDisabled {
		record(GatedOfferResolutionDisabled)
		return StructureOfferMaterial{}
	}
	_, material, _, _, err := e.graph.ResolveSubjects(WithOffersOnlyResolution(ctx), principal, graphRequest, interpretation, binding, confirmedExpectedKind(structureCanon.Confirmed), confirmedAnchorSelection(structureCanon.Confirmed))
	if err != nil && !errors.Is(err, ErrGraphNotProjected) {
		record(GatedOfferResolutionFailed)
		return StructureOfferMaterial{}
	}
	if errors.Is(err, ErrGraphNotProjected) {
		record(GatedOfferResolutionNotProjected)
		return StructureOfferMaterial{}
	}
	material = e.consultPriorStructureOffers(ctx, principal, priorEntries, material)
	if !StructureNeedsWouldDisclose(material) {
		record(GatedOfferResolutionEmpty)
		return StructureOfferMaterial{}
	}
	record(GatedOfferResolutionComposed)
	return material
}

// composeGatedStructureNeeds composes the class-default gate's disclosure:
// the window member FIRST (it is still the gate's own ask, and Missing's
// order is elicitation priority), then whatever the offers-only material
// adds, with receipts minted by the SAME composeStructureNeeds every
// decisive terminal uses -- so a kindr_/handr_/cand_ receipt minted here
// redeems through structure.go's ordinary lookup against the persisted
// StructureNeeds, no grammar extension needed. Empty material reduces to
// CHAOS-4118's window-only block byte-for-byte.
func composeGatedStructureNeeds(material StructureOfferMaterial, resultID string, windowOptions []contractsv1.ContextFabricWindowOption) *contractsv1.ContextFabricStructureNeeds {
	needs := composeStructureNeeds(material, resultID)
	if needs == nil {
		needs = &contractsv1.ContextFabricStructureNeeds{}
	}
	missing := make([]contractsv1.ContextFabricStructureNeedKind, 0, 1+len(needs.Missing))
	missing = append(missing, contractsv1.ContextFabricStructureNeedWindow)
	for _, member := range needs.Missing {
		if member == contractsv1.ContextFabricStructureNeedWindow {
			continue
		}
		missing = append(missing, member)
	}
	needs.Missing = missing
	needs.WindowOptions = windowOptions
	return needs
}
