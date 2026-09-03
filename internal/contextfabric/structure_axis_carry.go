package contextfabric

import (
	"context"

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
	Kind           contractsv1.ContextFabricSubjectKind
	SourceResultID string
	Outcome        KindCarryOutcome
	ChainDepth     int
}

// resolveCarriedKind walks this conversation's own prior-result chain for
// the nearest receipt-confirmed expected_kind. Fails closed on every
// ambiguity: "no carry" is always a safe, disclosed answer, never a guess.
//
// NOT IMPLEMENTED YET -- the red-first tests in structure_axis_carry_test.go
// pin the behavior this must have before any of it is written.
func (e *Engine) resolveCarriedKind(_ context.Context, _ storage.Principal, _ InvestigationRequest, _ []BoundSubjectReceipt, _ ResolvedGraphBinding) kindCarryResult {
	return kindCarryResult{}
}

// composeCarriedKindEntry renders the wire disclosure for a carried kind:
// a ConfirmedStructure entry with Source=carried naming the ORIGIN result
// it inherited from and no receipt id of its own, which is exactly the
// shape ContextFabricConfirmedStructureEntry.Validate already admits for
// any member (validate_context_fabric_structure.go). A carry is never
// silent.
//
// NOT IMPLEMENTED YET -- see resolveCarriedKind.
func composeCarriedKindEntry(_ kindCarryResult) *contractsv1.ContextFabricConfirmedStructureEntry {
	return nil
}
