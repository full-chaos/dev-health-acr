package v1

// CHAOS-3900 P1 (pivot-intent design brief, DESIGN-FINAL, §1.1/§2.1). The
// discriminator conjunction D = {kind, handle, anchor, window} (3896 §1.2)
// is the intent frame the census needs; interpretation today supplies none
// of it. StructureNeeds is the wire promotion of the same shape 3900 W1
// already shipped for window: a refusal receipt becomes a disclosure --
// typed, receipt-bound offers per missing frame member, so a caller (human
// panel or agent) can supply structure instead of hitting a dead end. This
// file adds the three NEW members (kind, anchor, handle); window rides
// W1's ContextFabricWindowOption/ContextFabricWindowClarification
// verbatim -- NOT a copy, per the brief's own instruction ("3900's type,
// verbatim").
//
// SHADOW-FIRST (P1's own scope line): these types round-trip and validate
// as of this changeset, but no surface (panel, MCP) yet reads or writes
// them -- that is P2/P3, separate tickets. The engine computes and
// persists StructureNeeds/ConfirmedStructure; nothing consumes them.

// ContextFabricStructureNeedKind is the closed enum for which intent-frame
// member is missing or ambiguous (design brief §2.1). CLASS-CONDITIONAL by
// construction at the call site (§1.3's NEVER-ELICIT rule): an
// aggregate/cohort-classed question can never construct a Missing entry
// naming subject_handle or subject_anchor -- that discipline lives in the
// engine derivation (internal/contextfabric), not here; this type only
// states the closed vocabulary the wire may carry.
type ContextFabricStructureNeedKind string

const (
	ContextFabricStructureNeedExpectedKind  ContextFabricStructureNeedKind = "expected_kind"
	ContextFabricStructureNeedSubjectAnchor ContextFabricStructureNeedKind = "subject_anchor"
	ContextFabricStructureNeedSubjectHandle ContextFabricStructureNeedKind = "subject_handle"
	ContextFabricStructureNeedWindow        ContextFabricStructureNeedKind = "window"
	// ContextFabricStructureNeedSubjectCandidate (CHAOS-4012) is a SEPARATE
	// disambiguation axis from expected_kind: a ranked list of the
	// resolution's own top candidates ("did you mean one of these?"),
	// offered whenever nothing committed and the pool is non-empty --
	// regardless of whether the pool spans one or many distinct kinds (chris
	// ruling, 2026-08-23: "why not offer the highest ranking list just like
	// codex, claude, and chatgpt"). Appended at the end of the vocabulary,
	// never reordering the existing three -- see
	// ContextFabricStructureNeedKindVocabulary's own elicitation-priority
	// doc comment; kind-pick still takes elicitation priority over
	// candidate-pick when both fire (kindOfferMaterial's own >=2-distinct
	// gate is UNCHANGED by this addition).
	ContextFabricStructureNeedSubjectCandidate ContextFabricStructureNeedKind = "subject_candidate"
)

var contextFabricStructureNeedKinds = [...]ContextFabricStructureNeedKind{
	ContextFabricStructureNeedExpectedKind,
	ContextFabricStructureNeedSubjectAnchor,
	ContextFabricStructureNeedSubjectHandle,
	ContextFabricStructureNeedWindow,
	ContextFabricStructureNeedSubjectCandidate,
}

// ContextFabricStructureNeedKindCount is the closed vocabulary's size.
const ContextFabricStructureNeedKindCount = len(contextFabricStructureNeedKinds)

// ContextFabricStructureNeedKindVocabulary returns the closed vocabulary in
// published order (kind, anchor, handle, window -- the §1.2 reading-1
// elicitation-priority order). An array return, copied on every call, per
// ContextFabricFactKindVocabulary's own precedent.
func ContextFabricStructureNeedKindVocabulary() [ContextFabricStructureNeedKindCount]ContextFabricStructureNeedKind {
	return contextFabricStructureNeedKinds
}

// ValidContextFabricStructureNeedKind reports whether value is a member of
// the closed registry.
func ValidContextFabricStructureNeedKind(value ContextFabricStructureNeedKind) bool {
	for _, kind := range contextFabricStructureNeedKinds {
		if value == kind {
			return true
		}
	}
	return false
}

// ContextFabricStructureOfferSource is the closed vocabulary distinguishing
// an engine-derived offer from a Bridge-proposed one (design brief §2.1,
// §2.4). P1 (this changeset) never produces "prior" -- the Bridge doesn't
// exist yet (P4/P5) -- but the field ships now so P5 is an additive
// value-population change, not a wire-shape change.
type ContextFabricStructureOfferSource string

const (
	ContextFabricStructureOfferEngine ContextFabricStructureOfferSource = "engine"
	ContextFabricStructureOfferPrior  ContextFabricStructureOfferSource = "prior"
)

func ValidContextFabricStructureOfferSource(value ContextFabricStructureOfferSource) bool {
	switch value {
	case ContextFabricStructureOfferEngine, ContextFabricStructureOfferPrior:
		return true
	default:
		return false
	}
}

// ContextFabricKindOptionReceiptPrefix is the closed namespace prefix for
// expected_kind offer receipts (design brief §2.1's receipt-namespace
// table), following winr_'s exact precedent (ContextFabricWindowOptionReceiptPrefix).
const ContextFabricKindOptionReceiptPrefix = "kindr_"

// ContextFabricAnchorOptionReceiptPrefix is the closed namespace prefix for
// subject_anchor offer receipts.
const ContextFabricAnchorOptionReceiptPrefix = "ancr_"

// ContextFabricHandleOptionReceiptPrefix is the closed namespace prefix for
// subject_handle offer receipts (design brief v4/sol-r3 #2: handles get the
// full symmetric receipt transport, by symmetry with kind/anchor/window).
const ContextFabricHandleOptionReceiptPrefix = "handr_"

// ContextFabricKindOption offers one census-kind choice, minted onto a
// stored result so a later turn can confirm it via kindr_ receipt
// redemption (design brief §2.1). Kind is a plain ContextFabricSubjectKind
// member of the closed census-kind registry (internal/contextfabric owns
// which members are registered for census purposes; this type only states
// the wire shape).
type ContextFabricKindOption struct {
	ReceiptID      string                            `json:"receipt_id"`
	OptionID       string                            `json:"option_id"`
	Label          string                            `json:"label"`
	Kind           ContextFabricSubjectKind          `json:"kind"`
	OfferSource    ContextFabricStructureOfferSource `json:"offer_source"`
	PriorVersionID string                            `json:"prior_version_id,omitempty"`
	PriorEntryID   string                            `json:"prior_entry_id,omitempty"`
	// Phrasing (CHAOS-4171 PR2) is an OPTIONAL model-generated rewrite of
	// Label, produced by a second bounded model call that runs AFTER
	// composeStructureNeeds mints this option (internal/contextfabric's
	// applyOfferPhrasing) -- never by the interpretation or synthesis
	// call, and never present when no phrasing model is configured, the
	// call failed or timed out, or the closed-vocabulary guard rejected
	// the response (fail-open to Label, never to the model). A consumer
	// SHOULD prefer Phrasing for display when non-empty and MUST always
	// have Label to fall back to -- Label is never removed or altered by
	// phrasing. Empty/absent carries no meaning beyond "no phrasing was
	// applied to this option."
	Phrasing string `json:"phrasing,omitempty"`
}

// ContextFabricAnchorOption offers one unique-claimant anchor candidate,
// minted onto a stored result so a later turn can confirm it via ancr_
// receipt redemption.
//
// CHANGE LOG (P1.E scoping): this type originally also carried
// ClaimantKey, asserted as "the identity-registry v2 key... what
// redemption re-verifies uniqueness against" -- P1.E's own scoping found
// that mechanism does not exist anywhere in the identity-universe code
// (BindAnchor/IdentityMatch prove uniqueness on a plain (Kind,CanonicalID)
// pair, nothing opaque or key-shaped): an asserted mechanism without a
// producer. Removed rather than left aspirational; (Kind, CanonicalID) is
// the real identity redemption re-verifies against -- see
// verifyAnchorClaimantUnique (internal/contextfabric/structure.go).
//
// CHANGE LOG (P1.E, follow-up): a second scoping pass found that
// (Kind, CanonicalID) alone cannot re-prove the PROPERTY that made this
// anchor uniquely offerable in the first place -- AliasLookup's own
// uniqueness is inherently per matched TEXT TERM, not per canonical id
// (graphrank.MatchIdentityRows), so a canonical-id-only re-check could not
// detect a rival claimant gaining the SAME term (the CHAOS-3917 class) and
// would have been a false sense of safety, not a weaker-but-honest check.
// MatchedTermHash restores the per-term claimant association the ratified
// 3917 design (doc #5 shape) always specified -- ClaimantKey above was the
// aspirational, wrong-shaped version of exactly this field. It carries the
// SHA-256 digest of the NORMALIZED matched term (graphrank.NormalizeAliasTerm),
// hex-truncated per this repo's digest idiom -- deliberately never the raw
// term itself: the term is question-derived text and this is a durable,
// server-minted offer (the repo's standing term-identity-via-hash rule).
type ContextFabricAnchorOption struct {
	ReceiptID       string                            `json:"receipt_id"`
	OptionID        string                            `json:"option_id"`
	Label           string                            `json:"label"`
	Kind            ContextFabricSubjectKind          `json:"kind"`
	CanonicalID     string                            `json:"canonical_id"`
	MatchedTermHash string                            `json:"matched_term_hash"`
	OfferSource     ContextFabricStructureOfferSource `json:"offer_source"`
	PriorVersionID  string                            `json:"prior_version_id,omitempty"`
	PriorEntryID    string                            `json:"prior_entry_id,omitempty"`
	// Phrasing (CHAOS-4171 PR2) -- see ContextFabricKindOption.Phrasing's
	// doc comment; identical contract, applied per option type.
	Phrasing string `json:"phrasing,omitempty"`
}

// ContextFabricHandleOption offers one grammar-valid handle candidate,
// minted onto a stored result so a later turn can confirm it via handr_
// receipt redemption (design brief v4/sol-r3 #2, the full symmetric
// transport). PatternID names the closed handle-grammar registry pattern
// (never regex text on the wire -- registry-pinned, matching
// AcceptedGrammar's own discipline); SourceColumn names the keyed source
// column redemption re-verifies existence against.
type ContextFabricHandleOption struct {
	ReceiptID      string                            `json:"receipt_id"`
	OptionID       string                            `json:"option_id"`
	Label          string                            `json:"label"`
	Kind           ContextFabricSubjectKind          `json:"kind"`
	PatternID      string                            `json:"pattern_id"`
	Value          string                            `json:"value"`
	SourceColumn   string                            `json:"source_column"`
	OfferSource    ContextFabricStructureOfferSource `json:"offer_source"`
	PriorVersionID string                            `json:"prior_version_id,omitempty"`
	PriorEntryID   string                            `json:"prior_entry_id,omitempty"`
	// Phrasing (CHAOS-4171 PR2) -- see ContextFabricKindOption.Phrasing's
	// doc comment; identical contract, applied per option type.
	Phrasing string `json:"phrasing,omitempty"`
}

// ContextFabricCandidateOptionReceiptPrefix is the closed namespace prefix
// for subject_candidate offer receipts (CHAOS-4012), following
// ContextFabricAnchorOptionReceiptPrefix's exact precedent.
const ContextFabricCandidateOptionReceiptPrefix = "candr_"

// ContextFabricCandidateOption offers one top-ranked SubjectCandidate from
// the resolution's own pool, minted onto a stored result so a later turn
// can confirm it via candr_ receipt redemption (chris ruling, 2026-08-23:
// "did you mean one of these" -- a ranked-candidate list, offered
// independently of whether the pool spans one or many distinct KINDS,
// unlike ContextFabricKindOption above).
//
// Field shape deliberately reuses ContextFabricAnchorOption's, minus
// MatchedTermHash: an anchor option claims per-TERM uniqueness (the reason
// MatchedTermHash exists at all -- see that type's own CHANGE LOG), a
// candidate option claims nothing beyond "this subject ranked in the
// resolution's own top N" -- no term-matching proof to re-verify, so no
// hash to carry. A NEW type, not a widened ContextFabricAnchorOption,
// mirroring the ContextFabricAnchorOptionV2 precedent (context_fabric_structure_types_v2.go):
// never repurpose an existing option type's meaning in place when the
// underlying claim differs.
type ContextFabricCandidateOption struct {
	ReceiptID      string                            `json:"receipt_id"`
	OptionID       string                            `json:"option_id"`
	Label          string                            `json:"label"`
	Kind           ContextFabricSubjectKind          `json:"kind"`
	CanonicalID    string                            `json:"canonical_id"`
	OfferSource    ContextFabricStructureOfferSource `json:"offer_source"`
	PriorVersionID string                            `json:"prior_version_id,omitempty"`
	PriorEntryID   string                            `json:"prior_entry_id,omitempty"`
	// Phrasing (CHAOS-4171 PR2) -- see ContextFabricKindOption.Phrasing's
	// doc comment; identical contract, applied per option type.
	Phrasing string `json:"phrasing,omitempty"`
}

// ContextFabricAcceptedGrammar discloses one grammar the engine accepts for
// explicit supply, so an agent (or a power user) can supply structure
// directly next turn instead of picking from offers (design brief §2.1).
// PatternID is a registry-pinned identifier, never regex text.
type ContextFabricAcceptedGrammar struct {
	Member    ContextFabricStructureNeedKind `json:"member"`
	Kind      ContextFabricSubjectKind       `json:"kind,omitempty"`
	PatternID string                         `json:"pattern_id"`
}

// ContextFabricStructureNeeds is the disclosure block: present whenever an
// investigation round ends short of decisive (clarification_required,
// ambiguous no_match, no_discriminators refusal), never dropped once
// present (Limitations discipline, design brief §2.1's "never-truncated
// pin"). relation_family and cohort_shape are UNREPRESENTABLE here by
// design (§1.1 demotion) -- the wire enum has no members for them, so no
// surface can offer or elicit either.
type ContextFabricStructureNeeds struct {
	// Missing is ordered by elicitation priority (kind before anchor
	// before window -- §1.2 reading 1; handle sits alongside kind/window
	// as an alternative decisive path, per the class's own frame).
	Missing       []ContextFabricStructureNeedKind `json:"missing"`
	KindOptions   []ContextFabricKindOption        `json:"kind_options,omitempty"`
	AnchorOptions []ContextFabricAnchorOption      `json:"anchor_options,omitempty"`
	HandleOptions []ContextFabricHandleOption      `json:"handle_options,omitempty"`
	// WindowOptions reuses CHAOS-3900 W1's own type verbatim -- not a
	// copy (design brief §2.1: "3900's type, verbatim").
	WindowOptions    []ContextFabricWindowOption    `json:"window_options,omitempty"`
	AcceptedGrammars []ContextFabricAcceptedGrammar `json:"accepted_grammars,omitempty"`
	// CandidateOptions (CHAOS-4012) fires INDEPENDENTLY of KindOptions above
	// -- both may be present on the same StructureNeeds -- see
	// ContextFabricStructureNeedSubjectCandidate's own doc comment.
	CandidateOptions []ContextFabricCandidateOption `json:"candidate_options,omitempty"`
	// WindowExpandOptions (CHAOS-4314) carries AT MOST ONE recommendation --
	// see ContextFabricWindowExpandOption's own doc comment for why it
	// deliberately duplicates one WindowOptions entry's ReceiptID/OptionID
	// verbatim rather than minting a fresh receipt namespace.
	//
	// Deliberately NOT paired with a "window_expand" StructureNeedKind
	// member (codex xhigh review, confirmed): AGENTS.md's contract rule
	// draws a hard line between "additive optional fields may stay in v1"
	// and "enum changes require a new major contract" -- a NEW closed-enum
	// value is unrecoverable for any v1-pinned consumer whose own type
	// system encodes the vocabulary as a closed string union (Ask Dev's
	// generated TS type, for one), where an ADDITIVE OPTIONAL FIELD like
	// this one is not: an old client simply never reads it. Presence is
	// discoverable directly (len(WindowExpandOptions) > 0), so Missing
	// needs no new member to carry the same signal.
	WindowExpandOptions []ContextFabricWindowExpandOption `json:"window_expand_options,omitempty"`
}

// ContextFabricStructureSource is the closed vocabulary for how a
// ConfirmedStructureEntry's value entered (design brief §2.1's echo).
// Distinct from ContextFabricWindowProvenance's near-identical values by
// design: this field states the WIRE MECHANISM (receipt vs explicit field),
// Provenance below states the resulting AUTHORITY TIER -- the two are
// correlated but not identical (an explicit field is always
// explicit_unattributed provenance on MCP, but is question_stated
// provenance on the panel surface with the SAME source=explicit).
//
// ContextFabricStructureSourceCarried (CHAOS-4360) is a FOURTH mechanism,
// v1 additive: the member was neither redeemed via a receipt nor supplied
// as an explicit field on THIS request -- it was INHERITED from an earlier
// turn's own confirmed structure in the same conversation (the request
// named a prior result and this request did not restate the member). A
// carried entry's Provenance is copied verbatim from the origin
// confirmation (never a value of its own), and PriorResultID names the
// ORIGIN result -- the nearest earlier turn where the member was actually
// receipt/explicit-confirmed, not merely the immediately-referenced prior
// result. Deliberately excluded from structureSupersessionClaims
// (pginvestigation/store.go): a carry reads already-stored confirmed
// structure, it never re-accepts a receipt, so it must never contend for or
// consume a single-use supersession claim.
//
// v1-additive, not a new major contract (codex R1 P1, reproduced and
// refuted against this exact repo's own precedent): appending a member to a
// closed string enum at the end is precisely what
// ContextFabricStructureNeedSubjectCandidate did (CHAOS-4012, commit
// f00dd436) -- a sibling closed enum in this same family, shipped under the
// UNCHANGED SchemaVersion (no ContextFabricInvestigationResultSchemaV2
// bump). AGENTS.md's "enum changes require a new major contract" targets
// changing an EXISTING member's meaning or removing one -- the class this
// repo's own ContextFabricInvestigationResultSchemaV2 fork (CHAOS-4042,
// context_fabric_structure_types_v2.go) exists for, where an existing
// field's semantics actually shift. A brand-new value nothing could
// previously produce is additive by the same reasoning ContextFabricPriorSubjectReceiptDisposition
// and every other closed-vocabulary echo in this file already relies on: a
// decoder for a plain Go/JSON string type never rejects an unrecognized
// value, and this package's OWN Valid* gates (which DO reject one) are
// updated in the same change that adds it, exactly like this one.
type ContextFabricStructureSource string

const (
	ContextFabricStructureSourceReceipt              ContextFabricStructureSource = "receipt"
	ContextFabricStructureSourceExplicit             ContextFabricStructureSource = "explicit"
	ContextFabricStructureSourceExplicitUnattributed ContextFabricStructureSource = "explicit_unattributed"
	ContextFabricStructureSourceCarried              ContextFabricStructureSource = "carried"
)

func ValidContextFabricStructureSource(value ContextFabricStructureSource) bool {
	switch value {
	case ContextFabricStructureSourceReceipt, ContextFabricStructureSourceExplicit, ContextFabricStructureSourceExplicitUnattributed, ContextFabricStructureSourceCarried:
		return true
	default:
		return false
	}
}

// ContextFabricStructureProvenance is the closed authority-tier vocabulary
// for a ConfirmedStructureEntry (design brief §2.0's authority table).
// Deliberately a DISTINCT type from ContextFabricWindowProvenance despite
// sharing the same three string values: window's own vocabulary and
// structure's are independently owned closed enums (each may grow
// independently without coupling the other), even though both currently
// enumerate the identical §2.0 authority tiers.
type ContextFabricStructureProvenance string

const (
	ContextFabricStructureInferredDefault        ContextFabricStructureProvenance = "inferred_default"
	ContextFabricStructureQuestionStated         ContextFabricStructureProvenance = "question_stated"
	ContextFabricStructureClarificationConfirmed ContextFabricStructureProvenance = "clarification_confirmed"
)

func ValidContextFabricStructureProvenance(value ContextFabricStructureProvenance) bool {
	switch value {
	case ContextFabricStructureInferredDefault, ContextFabricStructureQuestionStated, ContextFabricStructureClarificationConfirmed:
		return true
	default:
		return false
	}
}

// ContextFabricStructureDisposition is the closed vocabulary for what
// happened to one carried structure member (design brief §2.1's silent-drop
// closure -- "a veto the caller cannot see is the silent drop reborn").
type ContextFabricStructureDisposition string

const (
	ContextFabricStructureDispositionApplied          ContextFabricStructureDisposition = "applied"
	ContextFabricStructureDispositionVetoedUnresolved ContextFabricStructureDisposition = "vetoed_unresolved"
	ContextFabricStructureDispositionVetoedConflict   ContextFabricStructureDisposition = "vetoed_conflict"
	ContextFabricStructureDispositionVetoedStale      ContextFabricStructureDisposition = "vetoed_stale"
)

func ValidContextFabricStructureDisposition(value ContextFabricStructureDisposition) bool {
	switch value {
	case ContextFabricStructureDispositionApplied, ContextFabricStructureDispositionVetoedUnresolved,
		ContextFabricStructureDispositionVetoedConflict, ContextFabricStructureDispositionVetoedStale:
		return true
	default:
		return false
	}
}

// ContextFabricConfirmedStructureEntry is the wire-visible disposition for
// one carried structure member -- present whenever the request carried ANY
// structure receipt or explicit structure field, one entry PER carried
// member, INCLUDING vetoed ones (design brief §2.1's silent-drop closure).
// Receipts are globally scoped by (PriorResultID, ReceiptID): a bare
// receipt id is only unique within its issuing result.
//
// This SHOULD hold independent of window: a request's kind/anchor/handle
// structure and its evidence window are two members carried on the SAME
// request, and neither one's own outcome should silently withhold the
// other's echo. CHAOS-4335 closed one concrete gap: a request whose window
// is UNCONFIRMED or VETOED (windowVetoResult, both its pre-Interpret and its
// post-Interpret axis-conflict call sites, engine.go) now echoes a bare
// explicit ExpectedKinds/SubjectHandles field -- previously it silently
// vanished, distinguishable from "no explicit field was sent" only by
// re-deriving the original request, which a caller reading just this
// response cannot do. See windowVetoResult's own explicitStructure parameter
// doc comment (window.go) for the exact mechanism and its own Save-race
// safety reasoning.
//
// Two related gaps remain, deliberately OUT OF SCOPE for CHAOS-4335 (own
// ticket owed): (1) windowConfirmationRequiredResult's gate 1/2 -- an
// UNconfirmed window that reaches a clarification_required terminal, rather
// than a veto -- does not thread structureCanon at gate 1 (it fires before
// canonicalizeStructure has run, same shape the veto side had), so a bare
// explicit field there still silently vanishes; (2) the axis-conflict veto
// specifically still does not echo window's OWN confirmed member even when a
// receipt successfully redeemed it moments before the axis conflict fired
// (window.go's windowVetoResult doc comment has the full reasoning for both).
type ContextFabricConfirmedStructureEntry struct {
	Member         ContextFabricStructureNeedKind    `json:"member"`
	AppliedValue   string                            `json:"applied_value"`
	Source         ContextFabricStructureSource      `json:"source"`
	PriorResultID  string                            `json:"prior_result_id,omitempty"`
	ReceiptID      string                            `json:"receipt_id,omitempty"`
	OfferSource    ContextFabricStructureOfferSource `json:"offer_source,omitempty"`
	PriorVersionID string                            `json:"prior_version_id,omitempty"`
	PriorEntryID   string                            `json:"prior_entry_id,omitempty"`
	Provenance     ContextFabricStructureProvenance  `json:"provenance"`
	Disposition    ContextFabricStructureDisposition `json:"disposition"`
}

// ContextFabricStructureOfferSnapshotEntry is one echoed offer inside a
// decisive result's structure_offer_snapshot (design brief §2.1, the B5
// gap: "a decisive result reached via confirmation lost the (offered,
// selected) pair the Bridge needs"). Ids/ranks/enums only, same sink
// discipline as every other offer -- never display text.
type ContextFabricStructureOfferSnapshotEntry struct {
	Member         ContextFabricStructureNeedKind    `json:"member"`
	OfferID        string                            `json:"offer_id"`
	Rank           int                               `json:"rank"`
	OfferSource    ContextFabricStructureOfferSource `json:"offer_source"`
	PriorVersionID string                            `json:"prior_version_id,omitempty"`
	PriorEntryID   string                            `json:"prior_entry_id,omitempty"`
}
