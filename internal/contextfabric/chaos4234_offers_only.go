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
// gated request and returns its StructureOfferMaterial (prior-offer
// consultation applied, same as the decisive path) plus windowExpandUnavailable
// (CHAOS-4336): true exactly for the four outcomes where this function
// never learned anything about the current window's own content --
// Refused/Disabled/Failed/NotProjected -- so composeWindowExpandOption has
// no informed basis to recommend a wider one either; false for
// Empty/Composed, where the pass genuinely ran and either found nothing
// (a real "no candidates in this window" fact, safe grounds for "try
// wider") or found something. Every error path also returns empty
// material: the gated terminal must never be blocked by a read whose only
// purpose is a better clarification.
func (e *Engine) gatedOfferMaterial(ctx context.Context, principal storage.Principal, request InvestigationRequest, graphRequest InvestigationRequest, interpretation InterpretedQuestion, familyOutcome QuestionFamilyOutcome, binding ResolvedGraphBinding, structureCanon requestStructureCanonicalization, priorEntries []StructurePriorEntry) (material StructureOfferMaterial, windowExpandUnavailable bool) {
	record := func(outcome GatedOfferResolutionOutcome) {
		if e.telemetry != nil {
			e.telemetry.RecordGatedOfferResolution(ctx, principal, outcome)
		}
	}
	if !request.Options.AllowClarification {
		record(GatedOfferResolutionRefused)
		return StructureOfferMaterial{}, true
	}
	if e.regimeAOffersDisabled {
		record(GatedOfferResolutionDisabled)
		return StructureOfferMaterial{}, true
	}
	// The SAME carried frame the main resolution gets. The offers-only read
	// exists to produce a better clarification for this very turn, so
	// resolving it with a different (or absent) frame would offer the user
	// candidates from a pool the real resolution never searched.
	_, resolved, _, _, err := e.graph.ResolveSubjects(WithOffersOnlyResolution(ctx), principal, graphRequest, interpretation, binding, confirmedExpectedKind(structureCanon.Confirmed), confirmedAnchorSelection(structureCanon.Confirmed), familyOutcome.Frame)
	if err != nil && !errors.Is(err, ErrGraphNotProjected) {
		record(GatedOfferResolutionFailed)
		return StructureOfferMaterial{}, true
	}
	if errors.Is(err, ErrGraphNotProjected) {
		record(GatedOfferResolutionNotProjected)
		return StructureOfferMaterial{}, true
	}
	resolved = e.consultPriorStructureOffers(ctx, principal, priorEntries, resolved)
	// CHAOS-4634 (subsumes CHAOS-4579/CHAOS-4531's §1.3 class-conditional
	// gate): applied AFTER the prior-offer consultation (a prior-sourced
	// anchor offer is exactly as inapplicable to a family that excludes
	// the anchor axis as an engine-sourced one) and BEFORE the
	// WouldDisclose check below, so the Empty/Composed outcome describes
	// what this pass will ACTUALLY disclose. A cohort question whose only
	// material was anchor/handle therefore records GatedOfferResolutionEmpty
	// -- accurate, and the cohort gate's own "applied" event beside it says
	// why, which is what keeps the two distinguishable in the artifacts
	// alone.
	//
	// This is the call site chris's 2026-08-29 turn 1 actually took: the
	// class-default window gate composes the disclosure that asked "Which
	// repository, project, or team?" and "Which specific item?" on a
	// plural teams question. familyOutcome is resolved by construction on
	// gate 2 (Interpret has already run and returned it, this function's
	// own parameter); gate 1 fires before Interpret and never reaches here
	// at all -- and never builds anchor or handle material either, so it
	// needs no gate.
	resolved, cohortGateOutcome := GateOffersByFamily(resolved, familyOutcome)
	recordCohortStructureGate(ctx, e.telemetry, principal, cohortGateOutcome, interpretation.Shape)
	if !StructureNeedsWouldDisclose(resolved) {
		record(GatedOfferResolutionEmpty)
		return StructureOfferMaterial{}, false
	}
	record(GatedOfferResolutionComposed)
	return resolved, false
}

// composeGatedStructureNeeds composes the class-default gate's disclosure:
// the window member FIRST (it is still the gate's own ask, and Missing's
// order is elicitation priority), then whatever the offers-only material
// adds, with receipts minted by the SAME composeStructureNeeds every
// decisive terminal uses -- so a kindr_/handr_/cand_ receipt minted here
// redeems through structure.go's ordinary lookup against the persisted
// StructureNeeds, no grammar extension needed. Empty material reduces the
// kind/handle/candidate half to CHAOS-4118's window-only block
// byte-for-byte -- but (CHAOS-4336) no longer implies an empty
// WindowExpandOptions too: a genuinely-empty offers-only pass still gets
// composeWindowExpandOption's own recommendation when windowExpandUnavailable
// is false.
//
// window_expand (CHAOS-4314; windowExpandUnavailable added CHAOS-4336 --
// see composeWindowExpandOption's own doc comment) is set DIRECTLY on
// WindowExpandOptions, deliberately NEVER added to Missing: Missing's
// entries are StructureNeedKind, a CLOSED v1 enum that AGENTS.md's
// contract rule bars from ever gaining a member without a new major
// contract (codex xhigh review, confirmed HIGH finding -- an additive
// optional field like WindowExpandOptions is fine to stay in v1; a new
// enum value is not, for any v1-pinned consumer whose own type system
// encodes the vocabulary as a closed union). Presence is discoverable
// directly from the field itself.
func composeGatedStructureNeeds(material StructureOfferMaterial, windowExpandUnavailable bool, resultID string, windowOptions []contractsv1.ContextFabricWindowOption, effective contractsv1.ContextFabricEffectiveEvidenceWindow) *contractsv1.ContextFabricStructureNeeds {
	needs := composeStructureNeeds(material, resultID)
	if needs == nil {
		needs = &contractsv1.ContextFabricStructureNeeds{}
	}
	if expandOpt := composeWindowExpandOption(material, windowExpandUnavailable, windowOptions, effective); expandOpt != nil {
		needs.WindowExpandOptions = []contractsv1.ContextFabricWindowExpandOption{*expandOpt}
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

// composeWindowExpandOption (CHAOS-4314, chris "go" 2026-08-26; CHAOS-4336,
// 2026-08-26) builds the window_expand recommendation for a gated
// request: "a wider window is available to try" -- a statement about the
// window tier ALONE, not about whether the offers-only pool (kind/handle/
// candidate material) found anything in the current window.
//
// CHAOS-4314's original version gated this behind
// StructureNeedsWouldDisclose(material), which conflated two different
// callers of this same empty-material shape: gate 2 (class-default,
// engine.go's second window gate) with a genuinely-ran offers-only pass,
// and gate 1 (explicit-unconfirmed, engine.go's FIRST window gate, fired
// before Interpret/ResolveSubjects ever run -- "GATE ALL INFERRED WINDOWS
// ... pays for zero interpreter/graph/fact/synthesis work") where
// gatedOfferMaterial is never even attempted, StructureOfferMaterial{}
// unconditionally by construction. Both looked identical to this
// function, so both went silent. Measured live: CHAOS-4314's own Run C
// (16-shard kiac trial, tip f5361b84) put window_gated_silent_count at
// 9/65 -- all 9 were gate-1 explicit-unconfirmed cases (confirmed by
// direct log correlation: every one paired with a "window canonicalization
// outcome=gated_explicit_unconfirmed" event, never gated_class_default),
// even though pickWindowExpandTarget had a real wider tier to recommend
// every time and gate 1 needs no graph read to know that.
//
// CHAOS-4336's fix: windowExpandUnavailable (gatedOfferMaterial's own
// second return value) is the caller's explicit signal for "I could not
// learn anything about the current window's content" -- true only for
// gate 2's Refused/Disabled/Failed/NotProjected outcomes, where
// recommending a wider window would be an uninformed guess; false for
// gate 2's Empty/Composed (a completed pass, safe grounds either way) AND
// for gate 1 (no claim about pool content is being made at all -- the
// tier-ordering fact alone is enough). windowExpandCandidateHint's own
// ok=false path already handles empty material gracefully
// (CandidateLabel/CandidateKind simply stay unset) -- no other caller
// needed to change.
func composeWindowExpandOption(material StructureOfferMaterial, windowExpandUnavailable bool, windowOptions []contractsv1.ContextFabricWindowOption, effective contractsv1.ContextFabricEffectiveEvidenceWindow) *contractsv1.ContextFabricWindowExpandOption {
	if windowExpandUnavailable {
		return nil
	}
	target := pickWindowExpandTarget(effective, windowOptions)
	if target == nil {
		return nil
	}
	opt := &contractsv1.ContextFabricWindowExpandOption{
		ReceiptID:   target.ReceiptID,
		OptionID:    target.OptionID,
		Label:       target.Label,
		RelativeID:  target.RelativeID,
		WindowClass: effective.WindowClass,
	}
	if label, kind, ok := windowExpandCandidateHint(material); ok {
		opt.CandidateLabel = label
		opt.CandidateKind = kind
	}
	return opt
}

// windowExpandCandidateHint names the top pool member the gate's
// offers-only resolution found, for window_expand's own CandidateLabel/
// CandidateKind annotation -- priority order CandidateOptions (CHAOS-4012's
// own ranked "did you mean" list, the most specific signal) then
// AnchorOptions, HandleOptions, KindOptions (least specific: a kind choice
// names no particular subject). All four option types carry Label/Kind in
// the identical shape (see each type's own struct in
// internal/contracts/v1/context_fabric_structure_types.go), so the first
// non-empty list's own first entry is the hint. false when every list is
// empty -- genuinely reachable since CHAOS-4336 removed
// composeWindowExpandOption's StructureNeedsWouldDisclose guard (an empty
// offers-only pool no longer suppresses the window_expand recommendation
// itself, only this hint): the explicit ok return lets the caller leave
// CandidateLabel/CandidateKind unset rather than mint an empty-string
// value.
func windowExpandCandidateHint(material StructureOfferMaterial) (label string, kind contractsv1.ContextFabricSubjectKind, ok bool) {
	if len(material.CandidateOptions) > 0 {
		return material.CandidateOptions[0].Label, material.CandidateOptions[0].Kind, true
	}
	if len(material.AnchorOptions) > 0 {
		return material.AnchorOptions[0].Label, material.AnchorOptions[0].Kind, true
	}
	if len(material.HandleOptions) > 0 {
		return material.HandleOptions[0].Label, material.HandleOptions[0].Kind, true
	}
	if len(material.KindOptions) > 0 {
		return material.KindOptions[0].Label, material.KindOptions[0].Kind, true
	}
	return "", "", false
}

// pickWindowExpandTarget selects the SINGLE window_expand recommendation
// from the SAME closed 4-tier registry windowOptions already carries
// (composeWindowClarification), by identity against the entry already
// minted for that tier -- never a fresh mint (see
// ContextFabricWindowExpandOption's own doc comment). The next tier wider
// than the currently-bound RelativeID, in
// ContextFabricRelativeWindowIDVocabulary's own published order
// (trailing_30d < trailing_90d < trailing_365d < all_time). nil when
// effective's own RelativeID is already the widest tier the registry
// offers (nothing wider to recommend). When effective.RelativeID is empty
// or not itself a registry member (an explicit Start/End bound rather than
// a relative one, CHAOS-3900 §5.1 allows both), all_time is the only
// defensible "definitely wider" answer, so it is targeted by default.
func pickWindowExpandTarget(effective contractsv1.ContextFabricEffectiveEvidenceWindow, windowOptions []contractsv1.ContextFabricWindowOption) *contractsv1.ContextFabricWindowOption {
	registry := contractsv1.ContextFabricRelativeWindowIDVocabulary()
	targetID := contractsv1.ContextFabricRelativeWindowAllTime
	for i, id := range registry {
		if id != effective.RelativeID {
			continue
		}
		if i+1 >= len(registry) {
			return nil
		}
		targetID = registry[i+1]
		break
	}
	for i := range windowOptions {
		if windowOptions[i].RelativeID == targetID {
			return &windowOptions[i]
		}
	}
	return nil
}
