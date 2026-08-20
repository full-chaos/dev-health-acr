package contextfabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ErrPriorPointerConflict is pgstructurepriors' own CAS-conflict sentinel
// (mirrors contextfabric.ErrLifecycleConflict's own role for the graph
// epoch pointer) -- a concurrent flip/rollback/revoke already moved the
// pointer between this caller's read and its own CAS attempt; the caller
// must re-read and retry, never assume success.
var ErrPriorPointerConflict = errors.New("context fabric: prior pointer CAS conflict")

// ErrPriorVersionNotFound is returned by a flip/rollback attempting to
// point at a version that does not exist in acr.context_fabric_structure_priors
// for the target org -- refuses BEFORE the CAS, so this operation can never
// itself be the cause of a PriorDegradationPointerDangling read.
var ErrPriorVersionNotFound = errors.New("context fabric: prior version not found")

// CHAOS-3977 P5 (pivot-intent design brief, DESIGN-FINAL, §3.3/§3.4/§3.6):
// the Bridge prior store's domain types and runtime read port. This file is
// the shared substrate between the curation writer (offline batch, never in
// a serve path -- internal/contextfabric/structurepriorcuration) and the
// consultation reader (Engine.Investigate, this package's own
// priors_consult.go) -- exactly the split ports.go already establishes for
// InvestigationResultStore/GraphReader: I/O lives in an adapter package
// (pgstructurepriors), domain shape and read-time business rules live here.

// StructurePriorEntry is one curated prior -- a single (question feature ->
// proposed frame member value) mapping, support-weighted and independently
// revocable (design brief §3.2/§3.3).
//
// EntryID is DETERMINISTIC and STABLE across curation runs (never a
// per-version sequence number): re-curating the SAME (org, QuestionHash,
// Member, Value) triple from a later event-log watermark must produce the
// SAME EntryID, so a single per-entry revocation
// (acr_cf_structure_prior_revocations) keeps killing it in every future
// version that re-proposes it, not just the version revoked against --
// design brief §3.3's own "a revocation list consulted at read, for
// targeted kills between versions." See DeriveStructurePriorEntryID.
type StructurePriorEntry struct {
	EntryID      string
	QuestionHash string
	// Version is the owning StructurePriorSet's own Version -- denormalized
	// onto the entry so a PriorConsultant.Consult result is self-contained:
	// every entry knows which version it came from, the value
	// composeStructureNeeds' callers stamp onto
	// ContextFabricKindOption/AnchorOption/HandleOption's own
	// PriorVersionID field.
	Version int64
	Member  contractsv1.ContextFabricStructureNeedKind
	// Value is the ONE typed id/enum this entry proposes: expected_kind's
	// own SubjectKind string, subject_anchor's canonical id, subject_handle's
	// literal value, or window's RelativeID -- never free text (the same
	// closed-vocabulary/opaque-id discipline every structure-offer type in
	// this package already applies).
	Value string
	// Kind is populated for subject_anchor/subject_handle (the member the
	// proposed value belongs to); empty for expected_kind (Value already IS
	// the kind) and window (RelativeIDs are kind-independent).
	Kind contractsv1.ContextFabricSubjectKind
	// PatternID is populated for subject_handle only -- the closed
	// handle-grammar registry pattern this Value was matched against at
	// capture time (design brief §2.1's own pattern-id discipline).
	PatternID string
	// Support counts, per stratum -- design brief §3.2's own promotion-rule
	// inputs, kept apart rather than pre-summed so a later promotion-rule
	// change (or a drift report) never needs to re-derive them from raw
	// events. SupportConsensus stays 0 until a future changeset lands
	// CHAOS-3860's ConsensusEvidence onto the capture schema this curates
	// from (StructureSelectionEvent carries no such field today -- see
	// structurepriorcuration's own doc comment for why this is a reserved,
	// honestly-zero field, not a fabricated capability).
	SupportHumanPanel   int
	SupportAgentReceipt int
	SupportConsensus    int
	// Rank is the entry's own position among every entry curation produced
	// for the SAME (QuestionHash, Member) -- 0 is the strongest candidate
	// (highest total support), ties broken by EntryID for determinism.
	// Consultation ADDS prior offers in this order (design brief §2.4's ADD
	// verb); it never re-ranks an engine-derived offer (priors_consult.go's
	// own scoping note covers why RE-RANK is out of v1).
	Rank int
	// Revoked reports whether acr.context_fabric_structure_prior_revocations
	// names this EntryID for this org -- set by PriorStore.GetActive (the
	// store owns the join), consumed by Consult below. A revoked entry is
	// STILL returned by GetActive (never silently dropped at the I/O layer)
	// so the consultation layer can distinguish "nothing was ever proposed"
	// from "something was proposed and then suppressed" -- the
	// cf_prior_consulted{outcome=suppressed_revoked} signal design brief
	// §3.4 names.
	Revoked bool
}

// StructurePriorSet is one immutable, versioned snapshot for one
// organization (design brief §3.3).
type StructurePriorSet struct {
	OrgID                string
	Version              int64
	Entries              []StructurePriorEntry
	CreatedFromWatermark string
	CurationRuleVersion  string
}

// CurationRuleVersionV1 names THIS changeset's own promotion-rule/feature-
// key logic (structurepriorcuration.Curate). A future rule change bumps
// this constant, never edits an already-published version's entries in
// place (design brief §3.2: "a curation-rule change is a version bump,
// never an in-place edit").
const CurationRuleVersionV1 = "structure-priors-v1"

// PriorDegradationState is the closed, engine-only vocabulary design brief
// §3.4 names for cf_prior_degradation{state}: every reachable way prior
// consultation can fail to produce proposals for an org that DOES have the
// feature flag on. Every state degrades consultation to engine-derived
// offers only and NEVER fails or delays the round (the nil-sink convention
// applied to reads) -- see PriorConsultant's own doc comment.
type PriorDegradationState string

const (
	// PriorDegradationNone is the ordinary case: either genuine proposals
	// were found, or none exist for this question/org (an empty prior set
	// is NOT degradation -- it is §3.7's cold-start state, identical in
	// shape to "the feature is off").
	PriorDegradationNone PriorDegradationState = ""
	// PriorDegradationStoreUnavailable: the prior store itself could not be
	// read (a Postgres error, a timeout).
	PriorDegradationStoreUnavailable PriorDegradationState = "store_unavailable"
	// PriorDegradationPointerDangling: the org's active-version pointer
	// names a version that does not exist (a retire outran its grace, or a
	// data-integrity fault) -- ALSO raises an operator signal per design
	// brief §3.4 ("additionally raises an operator signal because it means
	// a retire outran its grace"), beyond the ordinary degrade-and-continue
	// every other state gets. See pgstructurepriors.Store.GetActive.
	PriorDegradationPointerDangling PriorDegradationState = "pointer_dangling"
	// PriorDegradationEntryRevoked is named here for the closed vocabulary's
	// own completeness but is DELIBERATELY never returned by this
	// changeset's own Consult implementation (storePriorConsultant.Consult,
	// this file): revocation is reported at the FINER per-member
	// cf_prior_consulted{outcome=suppressed_revoked} granularity instead
	// (PriorConsultedSuppressedRevoked, priors_consult.go) -- a consult-wide
	// "some/all candidates were revoked" collapse would only lose precision
	// a caller already has cheaply available (every entry's own Revoked
	// flag is returned, never swallowed). Reserved for a future call site
	// that genuinely needs the coarser, consult-wide signal.
	PriorDegradationEntryRevoked PriorDegradationState = "entry_revoked"
	// PriorDegradationFlipCASConflict is reported by the WRITE side (a
	// concurrent pointer flip losing its CAS, pgstructurepriors' own
	// telemetry) -- named here, in the SAME closed enum design brief §3.4
	// defines it in, even though no READ-path consult ever produces it
	// itself; kept in one vocabulary rather than two so a dashboard never
	// needs to reconcile two separately-maintained closed sets for what is,
	// per the design brief, one signal family.
	PriorDegradationFlipCASConflict PriorDegradationState = "flip_cas_conflict"
)

// PriorConsultedOutcome is the closed vocabulary design brief §3.4 names
// for cf_prior_consulted{member, outcome}.
type PriorConsultedOutcome string

const (
	PriorConsultedOffered                PriorConsultedOutcome = "offered"
	PriorConsultedSuppressedVerification PriorConsultedOutcome = "suppressed_verification"
	PriorConsultedSuppressedRevoked      PriorConsultedOutcome = "suppressed_revoked"
	// PriorConsultedDegraded is named here for the closed vocabulary's own
	// completeness but is DELIBERATELY never emitted by this changeset's
	// own call sites: a consult-level failure (store unreadable, pointer
	// dangling) is reported once, consult-wide, via
	// EngineTelemetry.RecordPriorDegradation (PriorDegradationState) rather
	// than duplicated as a per-member cf_prior_consulted event for every
	// member that never got the chance to look for a candidate -- one
	// signal per failure, not N. Reserved for a future call site if a
	// per-member degraded distinction is ever needed.
	PriorConsultedDegraded PriorConsultedOutcome = "degraded"
)

// PriorStore is the read-only production port a PriorConsultant wraps
// (pgstructurepriors.Store's own contract): return every entry of the org's
// active version (revoked ones INCLUDED, flagged -- see
// StructurePriorEntry.Revoked's own doc comment for why). All I/O; no
// business rule (question-hash matching, member gating, identity
// re-verification) belongs here -- that is priors_consult.go's own job,
// kept testable without a database.
type PriorStore interface {
	// GetActive returns the org's active version. found=false with
	// state==PriorDegradationNone means "no active version" (§3.7 cold
	// start) -- not an error, not degradation. A non-empty state ALWAYS
	// pairs with found=false: a genuinely degraded read never returns
	// partial entries a caller might mistake for a complete set.
	GetActive(ctx context.Context, orgID string) (set StructurePriorSet, found bool, state PriorDegradationState, err error)
}

// PriorConsultant is P5's own read-only runtime port (design brief §3.4):
// consult the org's active prior version for proposals at EXACTLY the two
// DP4(a)-ruled sites (Engine.consultPriorStructureOffers,
// Engine.resolveWindowPriorProposal -- priors_consult.go), never anywhere
// else. ONE method, called at MOST once per Investigate call (both sites
// share its result, via Engine.fetchPriorEntries -- see Investigate's own
// call site for why one read suffices): fail-open by construction (a
// PriorStore error surfaces as
// PriorDegradationStoreUnavailable, never a Go error this interface could
// propagate), org-scoped absolute (a caller passing an empty orgID gets no
// entries, never a cross-org read).
//
// nil is a fully valid EngineDependencies.PriorConsultant -- exactly the
// "feature does not exist yet" degrade every other optional Context Fabric
// dependency in this package uses (StructureSelectionSink's own doc
// comment, the convention this mirrors). ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_ENABLED
// gates whether composition ever wires a non-nil value at all (default OFF
// -- design brief §3.4's "Flag-gated per org... default OFF").
type PriorConsultant interface {
	// Consult returns questionHash's own matching entries for orgID's
	// active version, REVOKED ones included (sorted by Rank, revoked or
	// not, so a caller iterating in order sees the same relative ordering
	// regardless of revocation) -- callers apply Revoked filtering
	// themselves so they can report the distinct suppressed_revoked
	// outcome (priors_consult.go). state is non-empty ONLY for a genuine
	// store-level failure; an ordinary empty match is
	// (nil, PriorDegradationNone).
	Consult(ctx context.Context, orgID, questionHash string) (entries []StructurePriorEntry, state PriorDegradationState)
}

// NewPriorConsultant adapts a PriorStore into a PriorConsultant: read the
// org's active set once and filter to QuestionHash's own exact-match
// entries (design brief §3.2's "QuestionHash exact" feature key -- coarse
// feature enums (window_class/cohort_shape/pool kind-set signature) are a
// named, documented v2 follow-on, never silently claimed as implemented
// here; see this repository's PR description for the scoping note).
func NewPriorConsultant(store PriorStore) PriorConsultant {
	if store == nil {
		return nil
	}
	return storePriorConsultant{store: store}
}

type storePriorConsultant struct {
	store PriorStore
}

func (c storePriorConsultant) Consult(ctx context.Context, orgID, questionHash string) ([]StructurePriorEntry, PriorDegradationState) {
	orgID = strings.TrimSpace(orgID)
	questionHash = strings.TrimSpace(questionHash)
	if orgID == "" || questionHash == "" {
		return nil, PriorDegradationNone
	}
	set, found, state, err := c.store.GetActive(ctx, orgID)
	if err != nil {
		return nil, PriorDegradationStoreUnavailable
	}
	if state != PriorDegradationNone {
		return nil, state
	}
	if !found {
		// §3.7 cold start: no active version. Not degradation.
		return nil, PriorDegradationNone
	}
	var matched []StructurePriorEntry
	for _, entry := range set.Entries {
		if entry.QuestionHash == questionHash {
			entry.Version = set.Version
			matched = append(matched, entry)
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].Member != matched[j].Member {
			return matched[i].Member < matched[j].Member
		}
		return matched[i].Rank < matched[j].Rank
	})
	return matched, PriorDegradationNone
}

// DeriveStructurePriorEntryID computes the deterministic, curation-run-
// stable identifier StructurePriorEntry.EntryID's own doc comment requires
// -- a SHA-256 digest of the entry's own identity tuple (never the question
// text itself, which this function never receives), truncated to 32 hex
// characters (128 bits, ample collision resistance for one organization's
// own entry space, and short enough to stay a comfortable primary-key/JSON
// value). Curation and any test asserting entry-id stability MUST call this
// SAME function -- never a hand-rolled second derivation that could drift.
func DeriveStructurePriorEntryID(orgID string, member contractsv1.ContextFabricStructureNeedKind, questionHash, kind, patternID, value string) string {
	h := sha256.New()
	for _, part := range []string{orgID, string(member), questionHash, kind, patternID, value} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
