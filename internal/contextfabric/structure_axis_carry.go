package contextfabric

import (
	"context"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Same-conversation carry of a confirmed expected_kind across turns -- the
// structure-axis twin of the window carry in chaos4360_carry.go, which
// shipped that mechanism for the window axis alone and left the three
// structure axes without one.
//
// The defect: a confirmed structure axis is not authoritative server-side
// state. StructureSelectionSink (engine.go) is capture-only, and the
// per-turn offer path recomputes the expected_kind need from this turn's
// pool with no input from what an earlier turn confirmed (kindOfferMaterial,
// graphrank/chaos3900_structure_offers.go). A client that only re-presents
// receipts for needs the latest response still offers therefore answers the
// kind question, watches the offer disappear, sends nothing, and is asked
// again -- measured as an unbounded ask/answer oscillation on a live rig.
//
// WHAT IS CARRIED, and from where. Only a kind an earlier turn confirmed by
// REDEEMING A RECEIPT: the walk reads ConfirmedStructure entries whose
// Member is expected_kind and whose Source is receipt or carried, never an
// explicit or inferred value. That keeps the CHAOS-3972 §2.0
// kind-insensitivity rule intact by construction -- a carried kind is a
// receipt-confirmed kind reached by a different route, which is exactly what
// ConfirmedExpectedKind's own tripwire (ports.go) admits, and nothing weaker
// can enter through here.
//
// WHY THE SAME CHAIN WALK, not a new store. The only other candidate key is
// (org_id, question_hash) on acr.context_fabric_structure_selections, and
// that is not chain identity: two unrelated conversations asking the same
// question would inherit each other's axes. Chain identity in this codebase
// is the prior-result breadcrumb -- each hop's ConfirmedStructure entries
// name the PriorResultID they resolved from -- so this reuses
// carryReferencedResultIDs' seeding, the CHAOS-3898 §2.2 epoch taint gate,
// and the depth/visited bounds verbatim rather than growing a second,
// differently-bounded walk.
//
// KNOWN LIMIT, stated rather than discovered later: the walk can only be
// seeded from a prior result the REQUEST names, and a request names one only
// through a receipt. A turn whose predecessor offered nothing therefore
// names nothing, and every carry in this file misses with
// KindCarryMissNoReference -- the same miss the window carry takes on that
// turn today. Closing that requires chain identity the client can supply
// without a receipt, which is a contract question, not this file's.

// KindCarryOutcome is the closed, content-safe vocabulary the kind-carry
// telemetry reports: carry hits and the reason for every miss, so an
// operator can read the hit rate out of a run's own artifacts without
// re-running with instrumentation added afterwards. Never free text.
// Mirrors WindowCarryOutcome member-for-member; the two stay separate
// vocabularies because a reader must be able to tell which axis missed.
type KindCarryOutcome string

const (
	// KindCarryNotAttempted is the zero value -- the "nothing to report"
	// sentinel, never emitted on the wire or to telemetry. Carry is
	// attempted only when this turn confirmed no kind of its own.
	KindCarryNotAttempted KindCarryOutcome = ""
	// KindCarryHit: a receipt-confirmed kind was found in the chain and is
	// now this turn's confirmed kind.
	KindCarryHit KindCarryOutcome = "hit"
	// KindCarryMissNoReference: this request named no prior result at all --
	// there was nothing to walk. See this file's KNOWN LIMIT note.
	KindCarryMissNoReference KindCarryOutcome = "miss_no_reference"
	// KindCarryMissUnloadable: every reachable candidate failed to load or
	// named an empty result id.
	KindCarryMissUnloadable KindCarryOutcome = "miss_unloadable"
	// KindCarryMissStaleGraphEpoch: a candidate loaded but failed the
	// CHAOS-3898 §2.2 ingress taint gate -- the SAME fail-closed check
	// resolveCarriedWindow and resolvePriorSubjectHints already apply,
	// reused here rather than re-implemented.
	KindCarryMissStaleGraphEpoch KindCarryOutcome = "miss_stale_graph_epoch"
	// KindCarryMissNoConfirmedKind: every reachable, taint-gate-passing
	// result carried no receipt-confirmed expected_kind entry.
	KindCarryMissNoConfirmedKind KindCarryOutcome = "miss_no_confirmed_kind"
	// KindCarryMissDepthExceeded: the walk still had unvisited candidates
	// when carryChainMaxDepth/carryChainMaxVisited was reached.
	KindCarryMissDepthExceeded KindCarryOutcome = "miss_depth_exceeded"
	// KindCarryMissVetoedCarrier: a reachable result carried an expected_kind
	// entry the engine itself REFUSED -- any disposition other than applied
	// (vetoed_stale / vetoed_conflict / vetoed_unresolved). Its own value is
	// never carried. This has its own outcome rather than folding into
	// miss_no_confirmed_kind because the two mean opposite things to an
	// operator: "there was nothing to carry" versus "there was something and
	// it had already been refused".
	KindCarryMissVetoedCarrier KindCarryOutcome = "miss_vetoed_carrier"
	// KindCarryDroppedRedeemedKindDiffers: the walk found a kind, and this
	// turn's own receipts named a DIFFERENT one on the same axis, so the
	// carried value stood down. Not a miss -- the chain really did carry
	// something and it really was found -- which is why it has its own value
	// rather than being folded into a miss reason: an operator counting
	// carry hit rate needs to see that the mechanism worked and deferred,
	// not that it failed to find anything.
	KindCarryDroppedRedeemedKindDiffers KindCarryOutcome = "dropped_redeemed_kind_differs"
	// KindCarryMissConflictingKinds: two or more of the SAME depth's
	// directly-reachable candidates carried genuinely DIFFERENT confirmed
	// kinds. The six receipt fields validate independently of one another,
	// so one request can legitimately name two prior results; answering
	// under whichever loaded first would silently pick one of two real,
	// disagreeing confirmations. A genuine conflict fails closed.
	KindCarryMissConflictingKinds KindCarryOutcome = "miss_conflicting_kinds"
)

// kindCarryResult is resolveCarriedKind's return shape.
type kindCarryResult struct {
	Kind contractsv1.ContextFabricSubjectKind
	// RedeemedKind is set ONLY on a drop: the kind this turn's own receipt
	// named, which disagreed with Kind. Carried so the telemetry can report
	// BOTH sides of the disagreement -- "dropped" alone does not tell an
	// operator whether the caller pivoted or the chain was stale.
	RedeemedKind   contractsv1.ContextFabricSubjectKind
	SourceResultID string
	Outcome        KindCarryOutcome
	ChainDepth     int
}

// resolveCarriedKind walks this conversation's own prior-result chain for
// the nearest receipt-confirmed expected_kind. Fails closed on every
// ambiguity -- no reference, an unloadable candidate, a stale graph epoch, a
// chain exhausted before a hit, two carriers that disagree, or a depth that
// found a hit but could not be scanned to the end: "no carry" is always a
// safe, disclosed answer, never a guess.
//
// THAT LAST CASE IS WHERE THIS DIVERGES FROM resolveCarriedWindow, deliberately
// (codex round 1, finding 2). The window carry returns its first agreeing hit
// even when the visit cap cut the depth short or a sibling at that depth was
// unreadable, so an unexamined same-depth candidate could disagree and never
// be seen. The same shape was inherited here and is fixed here only: changing
// the window axis is a behaviour change to already-shipped carry semantics and
// belongs in its own change, not smuggled into this one.
//
// The walk is resolveCarriedWindow's, hop for hop and bound for bound --
// same seeding (carryReferencedResultIDs), same epoch taint gate, same
// carryChainMaxDepth/carryChainMaxVisited, same whole-depth scan before any
// decision. What differs is only WHERE the confirmation lives: a window
// rides EffectiveEvidenceWindow, a kind rides a ConfirmedStructure entry.
func (e *Engine) resolveCarriedKind(ctx context.Context, principal storage.Principal, request InvestigationRequest, validatedSubjectReceipts []BoundSubjectReceipt, binding ResolvedGraphBinding) kindCarryResult {
	if e.results == nil {
		return kindCarryResult{Outcome: KindCarryMissNoReference}
	}
	frontier := carryReferencedResultIDs(request, validatedSubjectReceipts)
	if len(frontier) == 0 {
		return kindCarryResult{Outcome: KindCarryMissNoReference}
	}
	visited := make(map[string]struct{}, carryChainMaxVisited)
	var sawUnloadable, sawStaleEpoch, capExceeded, sawVetoedCarrier bool
	for depth := 0; depth < carryChainMaxDepth && len(frontier) > 0; depth++ {
		var next []string
		// The WHOLE depth is scanned before any decision, for the reason
		// resolveCarriedWindow's own hits slice records: the six receipt
		// fields validate independently, so one request can legitimately
		// name two different prior results at the same depth, and deciding
		// on the first one seen would silently pick one of two real,
		// disagreeing confirmations.
		var hits []kindCarryResult
		// depthTruncated (codex round 1, finding 2): a hit is only
		// trustworthy if the WHOLE depth was scanned, because the conflict
		// check below can only see the candidates it actually visited. If
		// the visit cap cut this depth short, or a sibling at this depth
		// could not be read at all (unloadable, or refused by the epoch
		// taint gate), then an unexamined same-depth candidate may carry a
		// DIFFERENT kind and returning the hit would resolve an ambiguity
		// to a value. That is the one thing this mechanism must never do,
		// so a truncated depth fails closed even when it found a hit.
		depthTruncated := false
		for _, resultID := range frontier {
			if ctx.Err() != nil {
				return kindCarryResult{Outcome: KindCarryMissUnloadable}
			}
			if _, ok := visited[resultID]; ok {
				continue
			}
			if len(visited) >= carryChainMaxVisited {
				capExceeded = true
				depthTruncated = true
				break
			}
			visited[resultID] = struct{}{}
			fetched, err := carryLoadResult(ctx, e.results, principal, resultID)
			if err != nil {
				sawUnloadable = true
				depthTruncated = true
				continue
			}
			// CHAOS-3898 §2.2 ingress taint gate -- IDENTICAL check to
			// resolveCarriedWindow's and resolvePriorSubjectHints' own. A
			// rebuild between turns can change what a kind even denotes, so
			// a carrier from another epoch is refused outright rather than
			// trusted partially.
			if fetched.GraphEpoch == nil || *fetched.GraphEpoch != binding.Epoch {
				sawStaleEpoch = true
				depthTruncated = true
				continue
			}
			prior := fetched.Result
			kind, ok, refused := carriableConfirmedKind(prior)
			if refused {
				sawVetoedCarrier = true
			}
			if ok {
				sourceResultID := prior.ResultID
				if origin := carriedKindOrigin(prior); origin != "" {
					sourceResultID = origin
				}
				hits = append(hits, kindCarryResult{Kind: kind, SourceResultID: sourceResultID, Outcome: KindCarryHit, ChainDepth: depth})
				continue
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
		if depthTruncated {
			// This depth was not scanned to the end, so the walk stops here
			// -- it neither returns a hit found at this depth nor descends
			// past it (codex round 2). Both would rest on the same unproven
			// assumption: that the candidates we could NOT read carry
			// nothing that matters. An unread sibling at this depth may hold
			// a conflicting kind, and -- because "nearest confirmation wins"
			// is this walk's own rule -- it may equally hold a NEARER one
			// than anything a deeper hit could offer. Descending would
			// silently prefer the far confirmation over an unread near one.
			switch {
			case capExceeded:
				return kindCarryResult{Outcome: KindCarryMissDepthExceeded, ChainDepth: depth}
			case sawStaleEpoch:
				return kindCarryResult{Outcome: KindCarryMissStaleGraphEpoch, ChainDepth: depth}
			default:
				return kindCarryResult{Outcome: KindCarryMissUnloadable, ChainDepth: depth}
			}
		}
		if len(hits) > 0 {
			for _, h := range hits[1:] {
				if h.Kind != hits[0].Kind {
					return kindCarryResult{Outcome: KindCarryMissConflictingKinds, ChainDepth: depth}
				}
			}
			return hits[0]
		}
		frontier = next
	}
	if len(frontier) > 0 || capExceeded {
		return kindCarryResult{Outcome: KindCarryMissDepthExceeded}
	}
	switch {
	case sawStaleEpoch:
		return kindCarryResult{Outcome: KindCarryMissStaleGraphEpoch}
	case sawUnloadable:
		return kindCarryResult{Outcome: KindCarryMissUnloadable}
	case sawVetoedCarrier:
		// Ranked below the read failures above and above the generic miss: a
		// refused carrier is a definite fact about a result we DID read, so it
		// is more informative than "nothing found", but a result we could not
		// read at all is a wider unknown and outranks it.
		return kindCarryResult{Outcome: KindCarryMissVetoedCarrier}
	default:
		return kindCarryResult{Outcome: KindCarryMissNoConfirmedKind}
	}
}

// carriableConfirmedKind reports the expected_kind one stored result
// genuinely confirmed, if any. Source is the whole test: only receipt
// (redeemed here) and carried (inherited from a turn that redeemed it)
// are caller authority. An explicit or explicit_unattributed entry is an
// inferred-tier value -- CHAOS-3972 §2.0 requires a kind-insensitivity
// proof before anything decisive may rest on one -- and carrying it forward
// would launder it into authority a turn later, which is precisely what
// ConfirmedExpectedKind's type-level tripwire (ports.go) exists to prevent.
//
// The applied value must also still be a live member of the subject-kind
// vocabulary: a value persisted under an older vocabulary is not a kind
// this deployment can narrow a census by, and admitting it would push an
// unrecognized string down into the pool filter.
func carriableConfirmedKind(prior InvestigationResult) (kind contractsv1.ContextFabricSubjectKind, ok bool, refused bool) {
	for _, entry := range prior.ConfirmedStructure {
		if entry.Member != contractsv1.ContextFabricStructureNeedExpectedKind {
			continue
		}
		if entry.Source != contractsv1.ContextFabricStructureSourceReceipt && entry.Source != contractsv1.ContextFabricStructureSourceCarried {
			continue
		}
		// DISPOSITION AND PROVENANCE, not just member and source (design
		// review, critical). The contract admits a receipt-sourced
		// expected_kind entry marked vetoed_stale / vetoed_conflict /
		// vetoed_unresolved, and the save-time supersession path persists
		// exactly that shape. Without this check the walk carried such an
		// entry forward and composed a NEW one with Disposition=applied --
		// turning a kind the engine had REFUSED into caller authority one hop
		// later. An entry is caller authority only if it was applied AND
		// reached that state by confirmation.
		if entry.Disposition != contractsv1.ContextFabricStructureDispositionApplied {
			refused = true
			continue
		}
		if entry.Provenance != contractsv1.ContextFabricStructureClarificationConfirmed {
			refused = true
			continue
		}
		candidate := contractsv1.ContextFabricSubjectKind(entry.AppliedValue)
		if !contractsv1.ValidContextFabricSubjectKind(candidate) {
			continue
		}
		return candidate, true, refused
	}
	return "", false, refused
}

// carriedKindOrigin returns the ORIGIN result id a prior result's own carried
// expected_kind entry names, or "" when that result confirmed the kind itself.
//
// Mirrors carriedWindowOrigin (chaos4360_carry.go) and exists for the same
// contractual reason: a carried entry's prior_result_id names the ORIGINAL
// confirmation, not whichever result carried it most recently. Without this
// flattening a second hop pointed at the intermediate carrier, so the
// disclosure quietly meant something other than what the contract says it
// means -- and each further hop moved it further from the truth.
func carriedKindOrigin(result InvestigationResult) string {
	for _, entry := range result.ConfirmedStructure {
		if entry.Member == contractsv1.ContextFabricStructureNeedExpectedKind && entry.Source == contractsv1.ContextFabricStructureSourceCarried {
			return strings.TrimSpace(entry.PriorResultID)
		}
	}
	return ""
}

// effectiveConfirmedKind is what the two ResolveSubjects call sites read
// instead of confirmedExpectedKind alone: this turn's OWN receipt-confirmed
// kind when it has one, otherwise the carried kind.
//
// This turn's own receipt always wins. A caller redeeming a kindr_ receipt
// on this request is stating a kind now, and a value inherited from an
// earlier turn must never override what the caller just said -- the carry
// exists to fill a silence, never to argue with a statement.
//
// The carried kind enters through the SAME *ConfirmedExpectedKind the
// receipt path constructs, deliberately: everything downstream (the pool
// filter, and through it the kind offer's own cardinality suppression) then
// treats a carried kind exactly as it treats a kind confirmed on this turn,
// which is the whole point of a carry. A second, parallel path would make
// the two diverge.
// statedExpectedKindThisTurn reports whether THIS request already carries an
// expected_kind of its own -- by redeemed receipt (Confirmed) or as the
// caller's own explicit value (Explicit).
//
// Both block the carry, for one reason and one consequence (codex round 2).
// The reason: a kind stated on this turn is the caller speaking now, and the
// carry exists to fill a silence, not to argue with a statement -- the same
// precedence effectiveConfirmedKind applies to the receipt case. The
// consequence, which is what makes this load-bearing rather than tidy: an
// explicit member is echoed by composeConfirmedStructure, so appending a
// carried entry alongside it would put TWO expected_kind entries on one
// result, and the v1 result validator rejects that outright ("one entry per
// member"). The request would fail validation rather than merely disclose
// oddly.
//
// Note the asymmetry with ConfirmedExpectedKind: an explicit kind still does
// NOT become one (that type's own tripwire, ports.go, admits only
// receipt-confirmed values). It blocks the carry without gaining the carry's
// authority.
func statedExpectedKindThisTurn(request InvestigationRequest, canon requestStructureCanonicalization) bool {
	if confirmedExpectedKind(canon.Confirmed) != nil {
		return true
	}
	// request.ExpectedKinds DIRECTLY, not the echoed member (codex round 3).
	// resolveExplicitStructure echoes an explicit kind only when the caller
	// states exactly ONE -- a plural value narrows and shapes offers but has
	// no single value to echo. Reading the echo therefore treated a caller
	// who stated TWO kinds as having stated none, and let an inherited kind
	// override both, silently: plural has no echo to collide with, so
	// nothing failed loudly the way the singular case did. The caller spoke
	// either way, and the carry exists to fill a silence.
	if len(request.ExpectedKinds) > 0 {
		return true
	}
	// A receipt that merely PICKS A SUBJECT no longer blocks here. It used to,
	// and that was too blunt: on a turn linked by a candidate receipt whose
	// candidate does not commit, blocking the walk left the pool mixed, the
	// kind offer was re-raised, and the loop this mechanism closes re-opened.
	// Those members are now COMPARATORS instead -- see subjectAxisRedeemedKind
	// and effectiveConfirmedKind. Agreement keeps the carry; only a genuine
	// disagreement drops it.
	for _, e := range canon.Explicit {
		if e.Member == contractsv1.ContextFabricStructureNeedExpectedKind {
			return true
		}
	}
	return false
}

// subjectAxisRedeemedKinds returns the kinds this turn's own receipts named
// for a SOUGHT subject: subject_candidate (a specific ranked subject) and
// subject_handle (a keyed source row). Both describe a subject the caller is
// asking about, so both are comparable with a carried expected_kind.
//
// subject_anchor is DELIBERATELY EXCLUDED. Its kind is the SCOPE anchor's own
// kind, a different axis: it becomes an AnchorBinding used as the shadow
// evidence round's anchor discriminator (resolve.go) and never filters the
// candidate pool. "Which repositories does the Ops Team own" carries a TEAM
// anchor and seeks REPOSITORY subjects, so treating the anchor as a comparator
// would drop the carry on precisely the scoped questions this mechanism exists
// for -- a disagreement invented out of two axes that never disagreed.
func subjectAxisRedeemedKinds(confirmed []confirmedStructureMember) []contractsv1.ContextFabricSubjectKind {
	var kinds []contractsv1.ContextFabricSubjectKind
	for _, c := range confirmed {
		switch c.Member {
		case contractsv1.ContextFabricStructureNeedSubjectCandidate, contractsv1.ContextFabricStructureNeedSubjectHandle:
			if c.AppliedKind != "" {
				kinds = append(kinds, c.AppliedKind)
			}
		}
	}
	return kinds
}

func effectiveConfirmedKind(confirmed []confirmedStructureMember, carry kindCarryResult) *ConfirmedExpectedKind {
	if own := confirmedExpectedKind(confirmed); own != nil {
		return own
	}
	if carry.Outcome != KindCarryHit || carry.Kind == "" {
		return nil
	}
	return &ConfirmedExpectedKind{Kind: carry.Kind}
}

// applyCarryDrop is COMPARE AND DROP, and it is the single place the decision
// is made -- deliberately, because the drop has to be visible in three places
// at once and they must not be able to disagree: the resolution must not use
// the kind, the wire must not disclose it as applied, and the telemetry must
// record that the mechanism worked and then deferred. Flipping the outcome
// here achieves all three, because effectiveConfirmedKind and
// composeCarriedKindEntry both already key off it and recordKindCarry reports
// it. Deciding it inside effectiveConfirmedKind instead would fix only the
// first, and would leave a dropped carry disclosed on the wire as applied --
// the round-1 disclosure class read backwards.
//
// A subject-axis receipt redeemed this turn naming a DIFFERENT kind means the
// caller just picked something the carried kind would filter out of the pool,
// so the carried value stands down and the turn resolves as if there were
// none. Not a veto and not a conflict terminal: the caller's own pick stands,
// unfiltered.
//
// Two subject-axis members disagreeing with EACH OTHER are covered by the same
// rule, since at least one differs from the carried kind. That is deliberate:
// an inherited value has no business breaking a tie between two things the
// caller said on this turn, even when it matches one of them.
func applyCarryDrop(confirmed []confirmedStructureMember, carry kindCarryResult) kindCarryResult {
	if carry.Outcome != KindCarryHit || carry.Kind == "" {
		return carry
	}
	for _, redeemed := range subjectAxisRedeemedKinds(confirmed) {
		if redeemed != carry.Kind {
			return kindCarryResult{
				Kind:           carry.Kind,
				RedeemedKind:   redeemed,
				SourceResultID: carry.SourceResultID,
				Outcome:        KindCarryDroppedRedeemedKindDiffers,
				ChainDepth:     carry.ChainDepth,
			}
		}
	}
	return carry
}

// composeCarriedKindEntry renders the wire disclosure for a carried kind:
// a ConfirmedStructure entry with Source=carried naming the ORIGIN result
// it inherited from and no receipt id of its own, which is exactly the
// shape ContextFabricConfirmedStructureEntry.Validate already admits for
// any member (validate_context_fabric_structure.go). A carry is never
// silent.
//
// Provenance is clarification_confirmed rather than inferred_default
// because carriableConfirmedKind admits only receipt- and carried-sourced
// origins -- every kind that can reach here was confirmed by a caller
// redeeming an offer, one or more turns ago.
func composeCarriedKindEntry(carry kindCarryResult) *contractsv1.ContextFabricConfirmedStructureEntry {
	if carry.Outcome != KindCarryHit || carry.Kind == "" {
		return nil
	}
	return &contractsv1.ContextFabricConfirmedStructureEntry{
		Member:        contractsv1.ContextFabricStructureNeedExpectedKind,
		AppliedValue:  string(carry.Kind),
		Source:        contractsv1.ContextFabricStructureSourceCarried,
		PriorResultID: carry.SourceResultID,
		Provenance:    contractsv1.ContextFabricStructureClarificationConfirmed,
		Disposition:   contractsv1.ContextFabricStructureDispositionApplied,
	}
}

// recordKindCarry reports carry.Outcome to telemetry -- a no-op when
// telemetry is unconfigured or carry was never attempted, mirroring
// recordWindowCarry's own "once per non-zero signal" shape.
func (e *Engine) recordKindCarry(ctx context.Context, principal storage.Principal, carry kindCarryResult) {
	if e.telemetry == nil || carry.Outcome == KindCarryNotAttempted {
		return
	}
	e.telemetry.RecordKindCarry(ctx, principal, carry.Outcome, carry.ChainDepth)
}

// carryResultCacheKey scopes the per-request carry load cache below.
type carryResultCacheKey struct{}

// carryResultCacheEntry memoizes one SUCCESSFUL InvestigationResultStore.Get.
// Failures are deliberately not memoised -- see carryLoadResult.
type carryResultCacheEntry struct {
	stored StoredInvestigationResult
}

// withCarryResultCache returns a context carrying a per-REQUEST memo of
// prior-result loads, shared by every carry axis attempted on that request.
//
// WHY THIS EXISTS. The window carry and the kind carry are independent
// mechanisms -- each fails closed on its own, and neither may suppress the
// other -- but on the one turn where BOTH are eligible they walk the SAME
// chain from the SAME frontier, so without a memo each prior result is
// loaded twice. That is a duplicated store read on exactly the turn a
// clarification chain is already struggling to terminate.
//
// WHY A CONTEXT VALUE rather than a parameter, given this package's stated
// preference for positional parameters (ports.go): that rule is about
// SEMANTIC inputs, which must fail to compile when an implementation omits
// them. This is not one. The cache cannot change any carry's outcome -- an
// absent cache means every load goes straight to the store, which is
// byte-identical behavior and exactly what every direct-call unit test in
// this package gets by passing a bare context.
func withCarryResultCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, carryResultCacheKey{}, map[string]carryResultCacheEntry{})
}

// carryLoadResult loads one prior result through the request's carry cache
// when there is one, and straight from the store when there is not.
func carryLoadResult(ctx context.Context, results InvestigationResultStore, principal storage.Principal, resultID string) (StoredInvestigationResult, error) {
	cache, _ := ctx.Value(carryResultCacheKey{}).(map[string]carryResultCacheEntry)
	if cache != nil {
		if entry, ok := cache[resultID]; ok {
			return entry.stored, nil
		}
	}
	stored, err := results.Get(ctx, principal, resultID)
	// SUCCESSFUL READS ONLY (codex round 1). Caching the error too would
	// replay one axis's transient failure to the next axis and prevent the
	// independent second load from ever succeeding -- which directly
	// contradicts this mechanism's own rule that neither carry axis may
	// suppress the other. InvestigationResultStore.Get makes no promise that
	// its errors are stable within a request, so an error is retried per
	// axis; only the immutable stored result is memoised.
	if cache != nil && err == nil {
		cache[resultID] = carryResultCacheEntry{stored: stored}
	}
	return stored, err
}
