package contextfabric

import (
	"context"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4360: same-conversation carry of a confirmed evidence window across
// turns.
//
// The defect this closes (live-proven twice on the kiac pilot, cf-rulings.md
// 2026-08-27 06:30/09:10/13:40): turn 2 confirms a window via winr_ receipt
// (Provenance=clarification_confirmed) and the engine asks for a fresh
// subject clarification; the Workbench's accumulate-and-re-ask-ONCE batching
// means turn 3 carries only the NEW candidate pick, not the window receipt
// -- and re-sending the SAME window receipt on turn 3 is correctly VETOED by
// IsStructureSuperseded (pginvestigation/store.go), because receipts are
// single-use by design (that guard is unchanged by this file). Without a
// carry mechanism, turn 3's composeEffectiveWindow (window.go) falls through
// to the class-table/binder default, effectiveWindow.Provenance becomes
// inferred_default, the CHAOS-4234 gated-class-default branch fires
// (engine.go), and composePriorSubjectReceiptDispositions can only classify
// every PriorSubjectReceipts entry as skipped_failed_reauth (the gate's own
// resolution is offers-only and discarded by ruling) -- a project-status
// question can never reach a decisive answer past two turns.
//
// The fix is SERVER-side and reads only what a PRIOR turn already durably
// confirmed and persisted: when this request names a prior result (via any
// of the six BoundSubjectReceipt-shaped fields on InvestigationRequest) and
// this turn's own window canonicalization did not resolve one, the engine
// walks that prior-result chain looking for the nearest turn that carries a
// genuinely CONFIRMED (never inferred) window, and inherits it -- disclosed,
// never silently, via a NEW ContextFabricStructureSourceCarried entry on the
// wire ConfirmedStructure list. A carried window is NOT inferred (the
// CHAOS-4040 bar -- no commit under an inferred window -- is unchanged: this
// mechanism can only ever produce Provenance values composeEffectiveWindow's
// own gate at engine.go already treats as decisive), so the CHAOS-4234 gate
// no longer fires, ResolveSubjects runs its REAL, decisive resolution, and
// PriorSubjectReceipts re-verification -- an existing, unmodified mechanism
// -- runs against that real resolution instead of a discarded one, so
// "applied" becomes reachable again.
//
// The chain walk is real, not a fixed one-hop lookup: each hop's own
// ConfirmedStructure entries name the PriorResultID they themselves resolved
// from (receipt-sourced or, via this same mechanism, carried), so a result
// N turns deep can be reached by following those breadcrumbs backward,
// bounded by carryChainMaxDepth/carryChainMaxVisited. "Nearest confirmation
// wins": the FIRST hop (in traversal order) whose own window is genuinely
// confirmed answers the carry, and every result this mechanism composes
// re-persists that confirmation (as a carried entry, still non-inferred),
// so the very next turn's carry lookup only ever needs one hop back in the
// common case -- the deeper walk exists for a chain with a gap (a hop that
// itself never re-confirmed for some other reason).

// carryChainMaxDepth bounds how many hops resolveCarriedWindow walks back
// through a conversation's own prior-result chain before giving up --
// insurance against a malformed or adversarial chain, never expected to be
// exhausted in the ordinary "nearest confirmation wins" case above.
const carryChainMaxDepth = 5

// carryChainMaxVisited bounds the total number of distinct prior results a
// single carry attempt may load, independent of depth (a single hop can
// fan out to several PriorResultID references at once) -- the same
// "bounded, not unbounded" discipline carryChainMaxDepth applies along the
// other axis.
const carryChainMaxVisited = 20

// WindowCarryOutcome is the closed, content-safe vocabulary
// EngineTelemetry.RecordWindowCarry reports (CHAOS-4360): carry hits and the
// reason for every miss, so an operator can see the hit rate the N-turn
// harness measures without re-reading a trace. Never free text.
type WindowCarryOutcome string

const (
	// WindowCarryNotAttempted is the zero value -- recordWindowCarry's own
	// "nothing to report" sentinel, never emitted on the wire or to
	// telemetry. Carry is attempted only when this turn's own window would
	// otherwise be inferred_default (see resolveCarriedWindow's call site,
	// engine.go) -- the same "once per non-zero signal" convention every
	// other counted decision in this package already follows.
	WindowCarryNotAttempted WindowCarryOutcome = ""
	// WindowCarryHit: a confirmed (non-inferred) window was found somewhere
	// in the chain and is now this turn's effective window.
	WindowCarryHit WindowCarryOutcome = "hit"
	// WindowCarryMissNoReference: this request named no prior result at all
	// (every one of the six BoundSubjectReceipt-shaped fields was empty) --
	// there was nothing to walk.
	WindowCarryMissNoReference WindowCarryOutcome = "miss_no_reference"
	// WindowCarryMissUnloadable: every reachable candidate either failed to
	// load (InvestigationResultStore.Get error) or named an empty
	// ResultID/ReceiptID pair.
	WindowCarryMissUnloadable WindowCarryOutcome = "miss_unloadable"
	// WindowCarryMissStaleGraphEpoch: a candidate loaded but failed the
	// CHAOS-3898 §2.2 ingress taint gate (its own GraphEpoch is absent or
	// differs from this investigation's ResolvedGraphBinding) -- the SAME
	// fail-closed check resolvePriorSubjectHints already applies, reused
	// here rather than re-implemented, never partially trusted.
	WindowCarryMissStaleGraphEpoch WindowCarryOutcome = "miss_stale_graph_epoch"
	// WindowCarryMissNoConfirmedWindow: every reachable, taint-gate-passing
	// result in the chain carried either no window at all or one that was
	// ITSELF inferred_default -- nothing to carry.
	WindowCarryMissNoConfirmedWindow WindowCarryOutcome = "miss_no_confirmed_window"
	// WindowCarryMissDepthExceeded: the chain walk still had unvisited
	// candidates when carryChainMaxDepth/carryChainMaxVisited was reached --
	// the same fail-closed treatment as any other bounded walk in this
	// codebase (never silently keep going past the bound).
	WindowCarryMissDepthExceeded WindowCarryOutcome = "miss_depth_exceeded"
)

// windowCarryResult is resolveCarriedWindow's own return shape.
type windowCarryResult struct {
	// Window is the carried window, copied verbatim from the origin result
	// -- nil unless Outcome == WindowCarryHit.
	Window *contractsv1.ContextFabricEffectiveEvidenceWindow
	// SourceResultID is the ORIGIN result id -- the nearest earlier turn
	// where this window was actually receipt/explicit-confirmed, not merely
	// the immediately-referenced prior result (see carriedWindowOrigin).
	SourceResultID string
	Outcome        WindowCarryOutcome
	// ChainDepth is how many hops the walk needed past the directly
	// referenced result(s) to find the hit -- 0 means a directly-referenced
	// prior result itself carried the confirmed window.
	ChainDepth int
}

// carryReferencedResultIDs collects the distinct, non-empty ResultID values
// named by ANY of the six prior-receipt fields on request, in a fixed,
// deterministic field order (window first: most semantically related to a
// window carry, so a request naming several different prior results tries
// the most likely one first) -- first occurrence wins on a duplicate.
//
// validatedSubjectReceipts (codex R1 P1, fixed): the SIX fields are not
// symmetric. PriorKindReceipts/PriorAnchorReceipts/PriorHandleReceipts/
// PriorCandidateReceipts/PriorWindowReceipts are canonicalizeStructure's and
// canonicalizeEvidenceWindow's own atomic-batch inputs -- an entry naming a
// prior result that does not check out VETOES THE WHOLE REQUEST before this
// function is ever reached (structureVetoConfirmationUnresolved /
// windowVetoConfirmationUnresolved, engine.go's own early returns), so
// anything still in those raw request fields by the time carry runs already
// passed validation. PriorSubjectReceipts is the ONE field CHAOS-3478
// deliberately made best-effort: a receipt naming no matching candidate in
// its referenced prior result classifies skipped_no_match and the
// investigation proceeds anyway -- resolvePriorSubjectHints' own doc comment
// calls this out by design. Seeding the walk from the RAW field would let
// exactly that kind of receipt -- one that matched NOTHING -- reach into an
// unrelated prior result purely to steal its window, turning an otherwise
// inert bad receipt into a live gate-bypass. validatedSubjectReceipts is
// resolvePriorSubjectHints' own `validated` return (a strict subset of
// request.PriorSubjectReceipts: only entries that matched a real candidate
// in a real, taint-gate-passing prior result), so this function is called
// with that instead of the raw field.
func carryReferencedResultIDs(request InvestigationRequest, validatedSubjectReceipts []BoundSubjectReceipt) []string {
	var ids []string
	seen := make(map[string]struct{}, 8)
	add := func(receipts []BoundSubjectReceipt) {
		for _, r := range receipts {
			id := strings.TrimSpace(r.ResultID)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	add(request.PriorWindowReceipts)
	add(request.PriorCandidateReceipts)
	add(validatedSubjectReceipts)
	add(request.PriorKindReceipts)
	add(request.PriorAnchorReceipts)
	add(request.PriorHandleReceipts)
	return ids
}

// resolveCarriedWindow implements CHAOS-4360's same-conversation window
// carry: see this file's own package-level doc comment for the mechanism
// and the defect it closes. Called ONLY when this turn's own window
// canonicalization would otherwise be inferred_default (engine.go's call
// site) -- fails closed on every ambiguity (no reference, an unloadable
// candidate, a stale graph epoch, or a chain exhausted before a hit): "no
// carry" is always a safe, disclosed answer, never a guess.
func (e *Engine) resolveCarriedWindow(ctx context.Context, principal storage.Principal, request InvestigationRequest, validatedSubjectReceipts []BoundSubjectReceipt, binding ResolvedGraphBinding) windowCarryResult {
	if e.results == nil {
		return windowCarryResult{Outcome: WindowCarryMissNoReference}
	}
	frontier := carryReferencedResultIDs(request, validatedSubjectReceipts)
	if len(frontier) == 0 {
		return windowCarryResult{Outcome: WindowCarryMissNoReference}
	}
	visited := make(map[string]struct{}, carryChainMaxVisited)
	var sawUnloadable, sawStaleEpoch, capExceeded bool
	for depth := 0; depth < carryChainMaxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, resultID := range frontier {
			if ctx.Err() != nil {
				return windowCarryResult{Outcome: WindowCarryMissUnloadable}
			}
			if _, ok := visited[resultID]; ok {
				continue
			}
			if len(visited) >= carryChainMaxVisited {
				// codex R1 P2 (fixed): the unvisited remainder of THIS
				// frontier -- and everything past it -- is being dropped
				// right here. Record that explicitly rather than letting an
				// empty `next` read as "walked everything, found nothing":
				// the two are different decision bases and AGENTS.md's own
				// diagnosability bar requires telling them apart.
				capExceeded = true
				break
			}
			visited[resultID] = struct{}{}
			fetched, err := e.results.Get(ctx, principal, resultID)
			if err != nil {
				sawUnloadable = true
				continue
			}
			// CHAOS-3898 §2.2 ingress taint gate -- IDENTICAL check to
			// resolvePriorSubjectHints' own (engine.go): a carrier whose
			// GraphEpoch is absent or names a different epoch than this
			// investigation's own binding is never trusted, partially or
			// otherwise.
			if fetched.GraphEpoch == nil || *fetched.GraphEpoch != binding.Epoch {
				sawStaleEpoch = true
				continue
			}
			prior := fetched.Result
			if window := carriableWindow(prior.EffectiveEvidenceWindow); window != nil {
				sourceResultID := prior.ResultID
				if origin := carriedWindowOrigin(prior); origin != "" {
					sourceResultID = origin
				}
				return windowCarryResult{Window: window, SourceResultID: sourceResultID, Outcome: WindowCarryHit, ChainDepth: depth}
			}
			for _, entry := range prior.ConfirmedStructure {
				id := strings.TrimSpace(entry.PriorResultID)
				if id == "" {
					continue
				}
				if _, ok := visited[id]; ok {
					continue
				}
				next = append(next, id)
			}
		}
		frontier = next
	}
	if len(frontier) > 0 || capExceeded {
		return windowCarryResult{Outcome: WindowCarryMissDepthExceeded}
	}
	switch {
	case sawStaleEpoch:
		return windowCarryResult{Outcome: WindowCarryMissStaleGraphEpoch}
	case sawUnloadable:
		return windowCarryResult{Outcome: WindowCarryMissUnloadable}
	default:
		return windowCarryResult{Outcome: WindowCarryMissNoConfirmedWindow}
	}
}

// carriableWindow returns window unchanged (copied, never aliased) when it
// exists and is not itself an inferred default -- the CHAOS-4040 bar
// applied to the SOURCE side of a carry: an inferred window can never BE a
// source, only ever a destination.
func carriableWindow(window *contractsv1.ContextFabricEffectiveEvidenceWindow) *contractsv1.ContextFabricEffectiveEvidenceWindow {
	if window == nil || window.Provenance == contractsv1.ContextFabricWindowInferredDefault {
		return nil
	}
	copied := *window
	return &copied
}

// carriedWindowOrigin returns the ORIGIN result id a prior result's own
// window carried from, when that prior result's window was ITSELF a carry
// (Source=carried) rather than a fresh confirmation on that turn -- so a
// multi-turn chain always discloses the true point of confirmation, never
// merely the immediately-preceding turn's id ("nearest confirmation wins"
// names the CONFIRMATION, not the hop).
func carriedWindowOrigin(result InvestigationResult) string {
	for _, entry := range result.ConfirmedStructure {
		if entry.Member == contractsv1.ContextFabricStructureNeedWindow && entry.Source == contractsv1.ContextFabricStructureSourceCarried {
			return strings.TrimSpace(entry.PriorResultID)
		}
	}
	return ""
}

// composeCarriedWindowEntry builds the wire disclosure for a window carry
// hit -- nil for any other outcome. Source=carried (never receipt) is what
// keeps structureSupersessionClaims (pginvestigation/store.go) from ever
// treating this as a receipt redemption: a carry reads already-stored
// confirmed structure, it does not re-accept a receipt, so it must never
// contend for a single-use supersession claim. AppliedValue prefers the
// carried window's own RelativeID (the ordinary case -- every window this
// package mints carries one); an absolute-bounds-only window (no
// RelativeID) falls back to its own Start/End so Validate's non-empty
// applied_value requirement is still met honestly.
func composeCarriedWindowEntry(carry windowCarryResult) *contractsv1.ContextFabricConfirmedStructureEntry {
	if carry.Outcome != WindowCarryHit || carry.Window == nil {
		return nil
	}
	appliedValue := string(carry.Window.RelativeID)
	if appliedValue == "" {
		switch {
		case carry.Window.Start != nil && carry.Window.End != nil:
			appliedValue = carry.Window.Start.UTC().Format(time.RFC3339) + "/" + carry.Window.End.UTC().Format(time.RFC3339)
		default:
			appliedValue = "carried"
		}
	}
	return &contractsv1.ContextFabricConfirmedStructureEntry{
		Member:        contractsv1.ContextFabricStructureNeedWindow,
		AppliedValue:  appliedValue,
		Source:        contractsv1.ContextFabricStructureSourceCarried,
		PriorResultID: carry.SourceResultID,
		Provenance:    carriedStructureProvenance(carry.Window.Provenance),
		Disposition:   contractsv1.ContextFabricStructureDispositionApplied,
	}
}

// carriedStructureProvenance maps the origin window's OWN
// ContextFabricWindowProvenance onto the ContextFabricStructureProvenance
// this echo entry carries (codex R1 P2, fixed): carriableWindow already
// guarantees the input is one of exactly two values (question_stated or
// clarification_confirmed -- an inferred one can never reach here), but a
// hard-coded clarification_confirmed silently overwrote a question_stated
// origin, giving EffectiveEvidenceWindow.Provenance (copied verbatim,
// unaffected by this function) and this entry's own Provenance two
// different authority histories for the identical carried window.
func carriedStructureProvenance(windowProvenance contractsv1.ContextFabricWindowProvenance) contractsv1.ContextFabricStructureProvenance {
	if windowProvenance == contractsv1.ContextFabricWindowQuestionStated {
		return contractsv1.ContextFabricStructureQuestionStated
	}
	return contractsv1.ContextFabricStructureClarificationConfirmed
}

// appendCarriedStructureEntry appends entry to entries when non-nil,
// otherwise returns entries unchanged -- the single merge point both
// terminalResult (unresolved.go) and Investigate's own decisive path
// (engine.go) use so a carried window is disclosed on every result shape
// that carries a ConfirmedStructure echo at all.
func appendCarriedStructureEntry(entries []contractsv1.ContextFabricConfirmedStructureEntry, entry *contractsv1.ContextFabricConfirmedStructureEntry) []contractsv1.ContextFabricConfirmedStructureEntry {
	if entry == nil {
		return entries
	}
	return append(entries, *entry)
}

// recordWindowCarry reports carry.Outcome to telemetry -- a no-op when
// telemetry is unconfigured or carry was never attempted (WindowCarryNotAttempted),
// mirroring every other "once per non-zero signal" telemetry call in this
// package.
func (e *Engine) recordWindowCarry(ctx context.Context, principal storage.Principal, carry windowCarryResult) {
	if e.telemetry == nil || carry.Outcome == WindowCarryNotAttempted {
		return
	}
	e.telemetry.RecordWindowCarry(ctx, principal, carry.Outcome, carry.ChainDepth)
}
