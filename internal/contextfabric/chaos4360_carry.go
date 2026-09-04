package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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
	// WindowCarryMissConflictingWindows (codex R3 P1, fixed): two or more of
	// the SAME depth's directly-reachable candidates carried genuinely
	// DIFFERENT confirmed windows. The six receipt fields are validated
	// independently of one another (canonicalizeStructure/canonicalizeEvidenceWindow/
	// resolvePriorSubjectHints each check their OWN member against its OWN
	// named prior result), so a single request can legitimately redeem, say,
	// a candidate receipt from one prior result and a kind receipt from a
	// DIFFERENT one -- nothing requires them to share an origin. Picking
	// whichever candidate happened to load first (the pre-fix behavior)
	// could silently answer under an arbitrary one of two real but
	// disagreeing time windows. A genuine conflict fails closed, exactly
	// like every other carry ambiguity.
	WindowCarryMissConflictingWindows WindowCarryOutcome = "miss_conflicting_windows"
	// WindowCarryMissQuestionDrift: the request named a prior result through
	// parent_result_id, but that result answered a DIFFERENT question, so it
	// is not part of this conversation in any sense that would justify
	// inheriting its window. Applies to the caller-supplied root ONLY -- a
	// receipt is an accepted offer and a different follow-up question against
	// it stays legitimate (see carryFrontier).
	WindowCarryMissQuestionDrift WindowCarryOutcome = "miss_question_drift" // WindowCarryMissQuestionIndeterminate: the carry was rooted in parent_result_id
	// and one of the two questions canonicalizes to the empty string, so the
	// hash cannot tell it apart from every other punctuation-only question.
	// Refused, and reported as its own basis rather than folded into drift --
	// nothing was shown to DIFFER, the comparison simply has no identity to
	// work with.
	WindowCarryMissQuestionIndeterminate WindowCarryOutcome = "miss_question_indeterminate"
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
	// ViaStoredAncestry is true when the hit was reached through a stored
	// parent edge rather than a confirmation edge -- i.e. the walk only got
	// there because ancestry is persisted on every Save.
	//
	// A SEPARATE dimension from CarrySeedSource on purpose: seed source says
	// how the REQUEST was linked, this says how the WALK arrived. They answer
	// different questions and can disagree -- a parent_field-seeded request
	// can hit at depth 0 without ancestry mattering at all, and only the
	// deeper hops distinguish "the field linked this chain" from "persisted
	// ancestry is what kept the chain walkable". Without this, the rig cannot
	// tell whether the traversal fix is doing any work.
	ViaStoredAncestry bool
}

// carryReferencedResultIDs collects the distinct, non-empty ResultID values
// named by ANY of the six prior-receipt fields on request, in a fixed,
// deterministic field order (window first: most semantically related to a
// window carry, so a request naming several different prior results tries the
// most likely one first) -- first occurrence wins on a duplicate.
//
// RECEIPTS ONLY. parent_result_id is deliberately NOT collected here, and
// that separation is the whole containment design (CHAOS-5003). The two
// linkages carry different evidence and get different treatment: a receipt is
// an ACCEPTANCE of an offer the server chose to show this caller, so a carry
// rooted in one is ungated; parent_result_id is a bearer reference to any
// result id in the caller's own org, so a carry rooted in one is gated on
// question identity at the producer. Merging them into a single frontier is
// what made "is this hit parent-rooted?" a property that had to be propagated
// edge by edge -- and a property propagated edge by edge is a property that
// gets forgotten on the next edge someone adds. Two seeds, two walks, and the
// answer falls out of WHICH WALK produced the hit. See resolveCarriedWindow.
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
// SECOND CALLER, ADDED BY CHAOS-4998 -- read this before changing what this
// function returns. reuseBypassReason (answer_reuse.go) keys the reuse
// bypass on this same population, deliberately: the set of requests that
// must not be served a cached answer IS the set of requests a carry could
// walk, and every carry runs long after the reuse lookup. Widening what this
// function collects therefore widens the reuse bypass too, and narrowing it
// silently reopens the defect CHAOS-4998 closed (a request whose only prior-
// result reference was a window receipt was served a stored answer produced
// before that reference existed). That coupling is the point -- a bypass
// keyed on a hand-copied list of fields is exactly how the two drifted
// apart in the first place -- but it is invisible from the call site, so it
// is written down here.
//
// reuseBypassReason calls this with a nil validatedSubjectReceipts, which is
// sound only because it has already returned on a non-empty
// request.PriorSubjectReceipts; see its own doc comment and the test that
// pins that ordering.
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

// ValidateStoredParentResultID is the ONE Go-side statement of what a stored
// ancestry parent may be, so the two InvestigationResultStore adapters cannot
// disagree with each other or with the database.
//
// IT MIRRORS THE MIGRATION'S OWN CHECK CONSTRAINTS, deliberately and by name:
//
//	ck_..._parent_result_id_len       char_length BETWEEN 8 AND 256
//	ck_..._parent_not_self            parent_result_id <> result_id
//
// WHY IT EXISTS. PostgreSQL enforced both; the in-memory store enforced
// neither, so a self-parent or an out-of-bounds id round-tripped in tests and
// dev and was rejected only in production. A parity suite that never fed the
// stores an invalid parent could not see the difference -- measured: memory
// accepted all three shapes Postgres rejects, with a valid-parent control
// proving the rejections were attributable to the parent and not to schema
// validation.
//
// A self-parent is refused rather than tolerated because a row that cannot be
// meaningful should not be storable: the carry walk's visited-set already
// terminates the loop, so this is about not persisting nonsense, not about
// walk safety.
//
// The empty string is VALID and means "no parent" -- most turns have none.
func ValidateStoredParentResultID(resultID, parentResultID string) error {
	parent := strings.TrimSpace(parentResultID)
	if parent == "" {
		return nil
	}
	// RUNES, not bytes. The migration states this bound with char_length and
	// the wire contract states it with utf8.RuneCountInString; both count
	// characters, and they agree with each other for every UTF-8 input. A byte
	// count agrees with neither the moment the id is not ASCII:
	//
	//	256 * "é" = 256 characters, 512 bytes -> byte count REJECTS what the
	//	  contract accepted and Postgres would have stored, so a valid request
	//	  fails at Save instead of returning its result
	//	  4 * "é" =   4 characters,   8 bytes -> byte count ACCEPTS what
	//	  Postgres rejects, so the parity this function exists to provide is
	//	  simply false there
	//
	// The ASCII cases could not show this, because for ASCII bytes, runes and
	// characters are the same number -- which is why the first version of this
	// check read correct and was not.
	if runes := utf8.RuneCountInString(parent); runes < 8 || runes > 256 {
		return fmt.Errorf("parent_result_id must be 8..256 runes, got %d", utf8.RuneCountInString(parent))
	}
	if parent == strings.TrimSpace(resultID) {
		return errors.New("parent_result_id must not name the result itself")
	}
	return nil
}

// carryParentSeed is the caller-supplied ancestry root, trimmed: the ONE id
// that seeds the gated second walk on every axis.
//
// It is its own function rather than an inline TrimSpace so that "the ids the
// receipts named" and "the id the request field named" are two distinct values
// at every site that handles them, and no site can merge them by accident. The
// compiler cannot enforce that, but a named function makes the merge visible
// in review, which is one more line of defence than the previous shape had.
func carryParentSeed(request InvestigationRequest) string {
	return strings.TrimSpace(request.ParentResultID)
}

// carryOriginVerdict is the result of the same-question comparison at a carry
// producer. THREE states, not a bool, because "the origin answered a different
// question" and "we could not read the origin to find out" are different facts
// and must not share a telemetry label. Both refuse the carry; only one of
// them is drift.
type carryOriginVerdict int

const (
	// carryOriginSameQuestion: the origin answered THIS question. Carry.
	carryOriginSameQuestion carryOriginVerdict = iota
	// carryOriginDrifted: the origin answered a DIFFERENT question. Refuse,
	// and say so -- this is the containment doing its job.
	carryOriginDrifted
	// carryOriginUnverifiable: the origin could not be read, or the hit named
	// no origin at all. Refuse, but report unloadable rather than drift: a
	// predicate that could not be evaluated means NOT PROVEN, never "proceed",
	// and never a claim about what the origin said.
	carryOriginUnverifiable
	// carryOriginIndeterminateQuestion: one of the two questions has no
	// identity to compare. A question consisting only of terminal punctuation
	// ("?", "!!", "...") canonicalizes to the empty string, so EVERY such
	// question shares one hash -- they are not the same question, they are
	// questions the hash cannot tell apart.
	//
	// This is a THIRD refusal reason and not a spelling of either neighbour.
	// It is not drift: nothing was shown to differ. It is not unverifiable in
	// the unloadable sense: the origin read back perfectly. Folding it into
	// either would put a false basis in the telemetry, which is the same
	// mistake the three-state verdict exists to avoid.
	carryOriginIndeterminateQuestion
)

// carryOriginSameQuestionVerdict is THE same-question containment. It is the
// ONLY place any axis enforces it, and it is enforced on a VALUE rather than
// on a path.
//
// WHY THIS SHAPE, stated because three previous attempts had the other one.
// The containment used to be a property of EDGES: each hop in the walk asked
// "am I descending from the caller-supplied parent?" and re-applied the rule
// where the answer was yes. That requires enumerating every route by which a
// parent root can reach a value, and it escaped three review rounds in three
// different places (stored-ancestry edges; confirmation edges below a parent
// root; the plan carry, which had no gate at all). The failure mode is
// structural, not a series of oversights: the set of routes is open, and a
// rule that must be re-stated on each new route is a rule that will be missed
// on the next one.
//
// Here the comparison is made ONCE, against the hit's own SourceResultID --
// the ORIGIN the carried value was confirmed at, not the hop the walk happened
// to reach it through. Every axis already computes that origin (carriedWindowOrigin /
// carriedKindOrigin resolve it through however many carried hops precede it),
// so the route becomes irrelevant by construction: a value confirmed against a
// different question is refused no matter how many hops, ancestry edges or
// confirmation edges lie between the caller's parent and it. There is nothing
// to propagate, so there is nothing to forget to propagate.
//
// Reads through the per-request carry memo (carryLoadResult), so on the common
// path -- the origin is the parent the walk just loaded -- it costs no extra
// store round trip.
func (e *Engine) carryOriginSameQuestionVerdict(ctx context.Context, principal storage.Principal, request InvestigationRequest, originResultID string) carryOriginVerdict {
	origin := strings.TrimSpace(originResultID)
	if origin == "" || e.results == nil {
		return carryOriginUnverifiable
	}
	stored, err := carryLoadResult(ctx, e.results, principal, origin)
	if err != nil {
		return carryOriginUnverifiable
	}
	// EQUAL HASHES ARE NOT ENOUGH, and this guard is why (codex r1, HIGH).
	//
	// CanonicalizeQuestion strips trailing terminal punctuation, so "?", "!!"
	// and "..." all reduce to the empty string and share ONE hash. Without
	// this check, a result answering "?" satisfies the comparison for a
	// request asking "!!" -- two unrelated questions -- and the containment is
	// bypassed on every axis. Measured, not argued: all three axes returned
	// `hit` for exactly that pair before this guard existed.
	//
	// The answer-reuse path already fails closed on precisely this collision
	// and has since its own round-2 review (tryReuse, answer_reuse.go). The
	// class was known and fixed one seam over; this seam did not mirror it.
	// That is the whole lesson -- a guard that exists elsewhere in the same
	// package is not a guard here.
	//
	// BOTH sides are checked, not just the request's. The reuse guard only
	// needs the request's because the request is what it keys on; here the
	// ORIGIN is a caller-named bearer reference, so an origin with no
	// identity is exactly as unusable as a request with none.
	if CanonicalizeQuestion(request.Question) == "" || CanonicalizeQuestion(stored.Result.Question) == "" {
		return carryOriginIndeterminateQuestion
	}
	if QuestionHash(stored.Result.Question) != QuestionHash(request.Question) {
		return carryOriginDrifted
	}
	return carryOriginSameQuestion
}

// CarrySeedSource names HOW this turn reached its prior result, as a closed,
// content-safe label. Without it a carry hit rate cannot be attributed: a rig
// run that shows the loop closing cannot say whether the chain-identity field
// is what closed it or whether those turns were carrying by receipt all
// along, which is precisely the question the field was built to answer.
//
// Reported on every carry outcome, hit or miss, so hits and misses share a
// denominator per source rather than being counted against different ones.
type CarrySeedSource string

const (
	// CarrySeedNone: the request named no prior result at all -- the
	// miss_no_reference population, and the baseline the field must shrink.
	CarrySeedNone CarrySeedSource = "none"
	// CarrySeedReceipt: linked only by a redeemed receipt, the pre-existing
	// mechanism.
	CarrySeedReceipt CarrySeedSource = "receipt"
	// CarrySeedParentField: linked ONLY by parent_result_id -- a turn that
	// could not have carried anything before this ticket.
	CarrySeedParentField CarrySeedSource = "parent_field"
	// CarrySeedBoth: linked by both. Counted apart from either rather than
	// folded into one, because a chain that would have carried anyway tells
	// you nothing about the field's own contribution.
	CarrySeedBoth CarrySeedSource = "both"
)

// carrySeedSource derives the label from the request alone -- never from
// whether a carry succeeded, so the measure stays independent of the outcome
// it is used to explain.
func carrySeedSource(request InvestigationRequest, validatedSubjectReceipts []BoundSubjectReceipt) CarrySeedSource {
	hasField := carryParentSeed(request) != ""
	hasReceipt := len(carryReferencedResultIDs(request, validatedSubjectReceipts)) > 0
	switch {
	case hasField && hasReceipt:
		return CarrySeedBoth
	case hasField:
		return CarrySeedParentField
	case hasReceipt:
		return CarrySeedReceipt
	default:
		return CarrySeedNone
	}
}

// receiptValidation is the state of prior-subject-receipt validation at an
// ancestry call site, made EXPLICIT so that "nothing has been validated yet"
// and "validation ran and produced these" are different values rather than
// both being a nil slice.
//
// This type exists because a nil-able convenience parameter is what let the
// same defect land twice. Three sites were fixed after the first review and a
// fourth was missed, because `nil` reads as a reasonable default at every call
// site and nothing forces the author to decide which situation they are in.
// Now the compiler asks.
type receiptValidation struct {
	ran      bool
	receipts []BoundSubjectReceipt
}

// receiptsNotYetValidated is the honest value for a call site that runs BEFORE
// resolvePriorSubjectHints. It is not "no receipts" -- it is "we do not know
// yet", and ancestry must not treat an unvalidated reference as confirmed.
func receiptsNotYetValidated() receiptValidation { return receiptValidation{} }

// receiptsValidated carries what resolvePriorSubjectHints actually confirmed.
func receiptsValidated(receipts []BoundSubjectReceipt) receiptValidation {
	return receiptValidation{ran: true, receipts: receipts}
}

// vetoingWindowReceiptID names the prior result whose window receipt caused a
// window confirmation veto, so ancestryRoot can refuse to record it.
//
// This is the reachable half of a claim I got wrong. The unresolvable-window
// veto fires BEFORE receipt validation, so "prefer a validated reference"
// cannot help there -- nothing is validated yet. But the id is not merely
// unconfirmed, it is DISPROVED: the veto exists precisely because that receipt
// did not resolve. Recording it guarantees the next turn's walk stops at
// miss_unloadable, recreating the chain hole this mechanism exists to close.
//
// Only the confirmation vetoes qualify. A veto for some other reason says
// nothing about whether the named result is loadable, and refusing an id on a
// path that never tested it would throw away usable ancestry.
func vetoingWindowReceiptID(request InvestigationRequest, veto windowVetoReason) string {
	switch veto {
	case windowVetoConfirmationUnresolved, windowVetoConfirmationConflict:
	default:
		return ""
	}
	for _, receipt := range request.PriorWindowReceipts {
		if id := strings.TrimSpace(receipt.ResultID); id != "" {
			return id
		}
	}
	return ""
}

// vetoingStructureReceiptID is vetoingWindowReceiptID's STRUCTURE twin, and
// it exists because the first version of this change shipped only the window
// half (codex r3, MEDIUM).
//
// Same argument, one member over: the pre-validation structure veto fires
// BEFORE resolvePriorSubjectHints, so "prefer a validated reference" cannot
// help there -- nothing is validated yet. But a confirmation veto does not
// merely leave the named receipt unconfirmed, it DISPROVES it: the veto exists
// precisely because that receipt could not be resolved. Recording it as this
// turn's ancestry guarantees the next turn's walk stops at miss_unloadable,
// recreating the chain hole the whole mechanism exists to close.
//
// Only the confirmation vetoes qualify. structureVetoStaleSupersededOffer says
// the OFFER was claimed by a later result, not that the named prior result is
// unreadable, so refusing its id there would throw away usable ancestry.
//
// THE THREE FIELDS ARE SCANNED IN carryReferencedResultIDs' OWN ORDER (kind,
// anchor, handle) rather than an order invented here, so the id this refuses
// is the id ancestryRoot's fallback would otherwise have picked.
func vetoingStructureReceiptID(request InvestigationRequest, veto structureVetoReason) string {
	switch veto {
	case structureVetoConfirmationUnresolved, structureVetoConfirmationConflict:
	default:
		return ""
	}
	for _, batch := range [][]BoundSubjectReceipt{request.PriorKindReceipts, request.PriorAnchorReceipts, request.PriorHandleReceipts} {
		for _, receipt := range batch {
			if id := strings.TrimSpace(receipt.ResultID); id != "" {
				return id
			}
		}
	}
	return ""
}

// ancestryRoot picks the ONE prior result this turn records as its parent --
// durable chain identity, written by every Save regardless of whether any axis
// was carried, disclosed, or even attempted.
//
// parent_result_id is the SINGLE ancestry root when the caller supplies it and
// it was not refused: the caller stating outright which turn they are
// continuing is a stronger claim than anything inferred. Receipt-derived roots
// are the FALLBACK, so existing clients that link by redeeming an offer still
// build walkable history instead of ancestry existing only for callers who
// adopted the new field.
//
// PREFERS A VALIDATED REFERENCE, falling back to the first id in
// carryReferencedResultIDs' own fixed order only when none is validated. Same
// ordered list, one extra filter -- not a second derivation, so the ancestry
// root and the carry frontier cannot drift apart.
//
// refusedIDs are ids this turn already PROVED unusable: a parent the drift
// gate rejected, or the receipt whose failure caused a veto. Recording one
// would be actively harmful rather than merely useless -- a drift-rejected
// parent becomes laundering material for a later turn (the gate refuses a
// direct hop and the recorded edge supplies the same value one hop deeper),
// and a veto-causing receipt guarantees the next turn's walk stops at
// miss_unloadable, recreating the chain hole this mechanism exists to close.
//
// A reference we merely cannot confirm is still better ancestry than none: a
// dangling parent costs a miss on a later hop, while no parent costs the whole
// chain past this turn. A reference we have DISPROVED is not.
func ancestryRoot(request InvestigationRequest, validation receiptValidation, refusedIDs ...string) string {
	refused := make(map[string]struct{}, len(refusedIDs))
	for _, id := range refusedIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			refused[trimmed] = struct{}{}
		}
	}
	if id := strings.TrimSpace(request.ParentResultID); id != "" {
		if _, bad := refused[id]; !bad {
			return id
		}
	}
	ids := carryReferencedResultIDs(request, validation.receipts)
	validated := make(map[string]struct{}, len(validation.receipts))
	for _, r := range validation.receipts {
		if id := strings.TrimSpace(r.ResultID); id != "" {
			validated[id] = struct{}{}
		}
	}
	for _, id := range ids {
		if _, bad := refused[id]; bad {
			continue
		}
		if _, ok := validated[id]; ok {
			return id
		}
	}
	for _, id := range ids {
		if _, bad := refused[id]; bad {
			continue
		}
		return id
	}
	return ""
}

// resolveCarriedWindow implements CHAOS-4360's same-conversation window
// carry: see this file's own package-level doc comment for the mechanism
// and the defect it closes. Called ONLY when this turn's own window
// canonicalization would otherwise be inferred_default (engine.go's call
// site) -- fails closed on every ambiguity (no reference, an unloadable
// candidate, a stale graph epoch, or a chain exhausted before a hit): "no
// carry" is always a safe, disclosed answer, never a guess.
//
// THIS IS THE WINDOW AXIS'S CHOKE POINT (CHAOS-5003). Every windowCarryResult
// carrying WindowCarryHit that reaches a caller passes through here, and the
// same-question containment is applied here and nowhere else. The walk itself
// (walkCarriedWindow) knows nothing about parents, receipts or question
// hashes; it is a pure "reach a confirmed window from these seeds" traversal.
//
// TWO WALKS, SEEDED SEPARATELY, and that is what makes "is this hit
// parent-rooted?" answerable without tagging a single edge:
//
//  1. Seed from RECEIPTS only. A hit here is receipt-rooted -- the caller
//     redeemed an offer the server chose to show them -- and is returned
//     UNGATED. A caller who accepts an offer and then asks a genuinely
//     different follow-up ("what about last quarter?") is doing something
//     legitimate that has worked since CHAOS-4360, and gating it would break
//     a working conversation to harden a different mechanism.
//  2. Only if (1) found nothing AND the request carries parent_result_id:
//     seed from the PARENT only. A hit here is parent-rooted by construction,
//     because nothing else seeded the walk, and it is GATED on the origin's
//     question hash.
//
// The provenance is a property of WHICH WALK RAN, so there is nothing to
// carry along an edge and nothing to forget to carry. That is the entire
// difference from the shape this replaces, and the reason it is a property
// rather than a habit.
//
// PRECEDENCE, stated because it is a deliberate behaviour choice: a
// receipt-rooted hit WINS outright -- the parent chain is not consulted at
// all, so a parent-chain candidate can no longer contend with a receipt
// candidate for miss_conflicting_windows. That follows from the two-tier rule
// itself (a receipt is the stronger link), and it is the same precedence
// ancestryRoot already applies in the other direction.
//
// COST: at most two traversals, each bounded by the existing
// carryChainMaxDepth/carryChainMaxVisited, and the second runs only when the
// first missed and a parent field is present. Both read through the
// per-request memo (carryLoadResult), so the second walk re-loads nothing the
// first already fetched.
func (e *Engine) resolveCarriedWindow(ctx context.Context, principal storage.Principal, request InvestigationRequest, validatedSubjectReceipts []BoundSubjectReceipt, binding ResolvedGraphBinding) windowCarryResult {
	if e.results == nil {
		return windowCarryResult{Outcome: WindowCarryMissNoReference}
	}
	receiptSeeds := carryReferencedResultIDs(request, validatedSubjectReceipts)
	parent := carryParentSeed(request)
	receiptWalk := windowCarryResult{Outcome: WindowCarryMissNoReference}
	if len(receiptSeeds) > 0 {
		receiptWalk = e.walkCarriedWindow(ctx, principal, binding, receiptSeeds)
		if receiptWalk.Outcome == WindowCarryHit {
			return receiptWalk
		}
	}
	if parent == "" {
		return receiptWalk
	}
	parentWalk := e.walkCarriedWindow(ctx, principal, binding, []string{parent})
	if parentWalk.Outcome != WindowCarryHit {
		// WHICH MISS TO REPORT. The parent walk's outcome wins only when the
		// receipt walk never ran; otherwise the receipt walk's does, because
		// the receipt link is the stronger claim and its failure is the more
		// actionable fact -- a caller who redeemed an offer and got nothing
		// back wants to know why THAT failed, not why a weaker secondary
		// reference also failed. Stated at each producer rather than shared,
		// because the three axes' outcome vocabularies are separate types and
		// a shared helper would only be able to compare them by convention.
		if receiptWalk.Outcome == WindowCarryMissNoReference {
			return parentWalk
		}
		return receiptWalk
	}
	// THE CHOKE POINT. The hit's SourceResultID is the ORIGIN the window was
	// confirmed at -- carriedWindowOrigin has already resolved it through
	// however many carried hops precede it -- so this refuses a drifted origin
	// no matter which route the walk took to reach it.
	switch e.carryOriginSameQuestionVerdict(ctx, principal, request, parentWalk.SourceResultID) {
	case carryOriginSameQuestion:
		return parentWalk
	case carryOriginDrifted:
		return windowCarryResult{Outcome: WindowCarryMissQuestionDrift}
	case carryOriginIndeterminateQuestion:
		return windowCarryResult{Outcome: WindowCarryMissQuestionIndeterminate}
	default:
		// Unverifiable: the origin could not be read. Refuse, but report what
		// actually happened rather than claiming the origin said something.
		return windowCarryResult{Outcome: WindowCarryMissUnloadable}
	}
}

// walkCarriedWindow traverses the prior-result chain from the given seeds
// looking for the nearest confirmed window. PURE TRAVERSAL: it does not know
// how its seeds were derived and does not apply the same-question rule --
// that belongs to resolveCarriedWindow, its sole caller, so that the rule has
// exactly one site (CHAOS-5003).
func (e *Engine) walkCarriedWindow(ctx context.Context, principal storage.Principal, binding ResolvedGraphBinding, seeds []string) windowCarryResult {
	frontier := seeds
	if len(frontier) == 0 {
		return windowCarryResult{Outcome: WindowCarryMissNoReference}
	}
	visited := make(map[string]struct{}, carryChainMaxVisited)
	// reachedViaAncestry records which frontier ids were reachable ONLY
	// because a stored parent pointed at them -- see ViaStoredAncestry.
	reachedViaAncestry := make(map[string]struct{}, carryChainMaxVisited)
	var sawUnloadable, sawStaleEpoch, capExceeded bool
	for depth := 0; depth < carryChainMaxDepth && len(frontier) > 0; depth++ {
		var next []string
		// hits (codex R3 P1, fixed) collects EVERY carriable window found at
		// THIS depth, not just the first -- the six receipt fields validate
		// independently of one another, so a single request can legitimately
		// name two DIFFERENT prior results at the same depth (e.g. a
		// candidate receipt from one, a kind receipt from another). Deciding
		// on the first one seen silently picked an arbitrary window when two
		// real, disagreeing ones were both reachable. The whole depth is
		// scanned before any decision is made.
		var hits []windowCarryResult
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
			_, viaAncestry := reachedViaAncestry[resultID]
			fetched, err := carryLoadResult(ctx, e.results, principal, resultID)
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
				hits = append(hits, windowCarryResult{Window: window, SourceResultID: sourceResultID, Outcome: WindowCarryHit, ChainDepth: depth, ViaStoredAncestry: viaAncestry})
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
			// DURABLE ANCESTRY, not just the wire-visible edges above.
			//
			// ConfirmedStructure only points somewhere when this result
			// actually CARRIED or CONFIRMED something -- so a turn that was
			// vetoed, or that simply carried nothing, has no wire edge out of
			// it and the walk stops dead there. That is a hole: the turn had a
			// real predecessor, and every turn after the hole is cut off from
			// everything before it.
			//
			// The stored parent is what closes it. It is recorded by EVERY
			// Save regardless of what was carried, which is exactly why it can
			// bridge a turn that carried nothing. Appended AFTER the
			// confirmation edges so a chain that does have wire-visible
			// provenance still prefers it -- this widens what is reachable
			// without re-ordering what was already reachable.
			//
			// NO SAME-QUESTION CHECK HERE, and its absence is the design
			// (CHAOS-5003). An edge-level check is what this file used to do
			// and what escaped three review rounds; the containment now lives
			// at the producer, on the ORIGIN of whatever value this walk
			// eventually returns, so widening reachability here cannot widen
			// what a drifted parent can inherit.
			if id := strings.TrimSpace(fetched.ParentResultID); id != "" {
				if _, ok := visited[id]; !ok {
					// Only marked when NO confirmation edge already produced
					// this id: the flag must mean "ancestry is why this was
					// reachable", not merely "ancestry also pointed here".
					if !slices.Contains(next, id) {
						reachedViaAncestry[id] = struct{}{}
					}
					next = append(next, id)
				}
			}
		}
		if len(hits) > 0 {
			for _, h := range hits[1:] {
				if !windowsEquivalent(hits[0].Window, h.Window) {
					return windowCarryResult{Outcome: WindowCarryMissConflictingWindows, ChainDepth: depth}
				}
			}
			return hits[0]
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

// windowsEquivalent reports whether a and b describe the SAME evidence
// window (codex R3 P1) -- the test two same-depth carry candidates must
// pass to avoid a reported conflict. RelativeID is the ordinary
// discriminator (every window this package mints from the closed relative
// registry carries one); a window with none (an absolute-bounds-only
// origin) falls back to comparing Start/End directly. Deliberately ignores
// WindowClass/Confidence/Provenance -- those describe HOW a window was
// derived, not WHICH evidence it names, and two independently confirmed
// windows naming the identical range are not a conflict merely because one
// was question_stated and the other clarification_confirmed.
func windowsEquivalent(a, b *contractsv1.ContextFabricEffectiveEvidenceWindow) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.RelativeID != "" || b.RelativeID != "" {
		return a.RelativeID == b.RelativeID
	}
	return timePtrEqual(a.Start, b.Start) && timePtrEqual(a.End, b.End)
}

func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
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

// appendCarriedStructureEntry appends each non-nil entry to entries -- the
// single merge point both terminalResult (unresolved.go) and Investigate's
// own decisive path (engine.go) use so a carry is disclosed on every result
// shape that carries a ConfirmedStructure echo at all.
//
// Variadic because carries are per-AXIS and a single turn can legitimately
// carry more than one: a window from the window carry, an expected_kind from
// the structure-axis carry (structure_axis_carry.go). Each axis composes its
// own entry independently and they merge here, rather than one axis's miss
// being able to suppress another's hit.
func appendCarriedStructureEntry(entries []contractsv1.ContextFabricConfirmedStructureEntry, carried ...*contractsv1.ContextFabricConfirmedStructureEntry) []contractsv1.ContextFabricConfirmedStructureEntry {
	for _, entry := range carried {
		if entry == nil {
			continue
		}
		entries = append(entries, *entry)
	}
	return entries
}

// recordWindowCarry reports carry.Outcome to telemetry -- a no-op when
// telemetry is unconfigured or carry was never attempted (WindowCarryNotAttempted),
// mirroring every other "once per non-zero signal" telemetry call in this
// package.
func (e *Engine) recordWindowCarry(ctx context.Context, principal storage.Principal, carry windowCarryResult, seedSource CarrySeedSource) {
	if e.telemetry == nil || carry.Outcome == WindowCarryNotAttempted {
		return
	}
	e.telemetry.RecordWindowCarry(ctx, principal, carry.Outcome, carry.ChainDepth, seedSource, carry.ViaStoredAncestry)
}
