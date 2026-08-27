package contextfabric

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3927 P4 (pivot-intent design brief, DESIGN-FINAL, §2.4/§3.1):
// extends the CHAOS-3859 clarification-selection CAPTURE pipeline (shipped
// 2026-08-17, subjects only) to a SECOND, parallel event type recording
// structure-offer confirmations -- every time a caller's kindr_/ancr_/
// handr_ receipt successfully resolves against a member of a prior
// result's own StructureNeeds offer set, that is a labeled (question
// shape -> confirmed structure member) pair at real production
// distribution, exactly the training signal ClarificationSelectionEvent
// already captures for subject candidates. This file follows that file's
// OWN pattern member-for-member (parallel event type, never a field-graft
// onto ClarificationSelectionEvent -- design brief §2.4's own instruction:
// "extend 3859's pattern, parallel event types, never field-grafts").
//
// Same HARD SCOPE BOUNDARY as 3859 phase 1: this file and the write path
// it backs are CAPTURE ONLY. No feedback loop, no learned priors, nothing
// reads this back yet -- that is P5's own, separately chris-ratified
// scope (design brief §4's sharding table).

// StructureOfferedOption is one candidate from a StructureNeeds offer
// list's own member-specific slice (KindOptions/AnchorOptions/
// HandleOptions), captured alongside whether it was the one redeemed --
// mirrors ClarificationOfferedCandidate's own shape and rationale exactly:
// Rank is the offer's own 0-indexed position as ACTUALLY disclosed (never
// re-derived by re-sorting later), and AppliedValue is the typed id/enum
// value the offer represents (a SubjectKind string, a canonical anchor id,
// or a handle's literal value) -- never SubjectRef.Label or any other
// caller-facing display text, the same closed-vocabulary/opaque-id sink
// discipline every other structure-offer echo in this package already
// applies.
type StructureOfferedOption struct {
	ReceiptID      string `json:"receipt_id"`
	AppliedValue   string `json:"applied_value"`
	OfferSource    string `json:"offer_source"`
	PriorVersionID string `json:"prior_version_id,omitempty"`
	PriorEntryID   string `json:"prior_entry_id,omitempty"`
	Rank           int    `json:"rank"`
}

// StructureSelectionEvent is one observed "the caller redeemed a
// structure receipt, confirming this specific offer" fact --
// Engine.canonicalizeStructure builds one every time a
// PriorKindReceipts/PriorAnchorReceipts/PriorHandleReceipts entry
// successfully resolves to a confirmed member (design brief §2.4's own
// StructureSelectionEvent shape). Fires ONLY on success, mirroring
// captureClarificationSelection's own "a veto has nothing to label"
// reasoning: an unresolved, conflicted, or superseded receipt never
// redeemed anything a training signal could learn from.
type StructureSelectionEvent struct {
	OrgID string
	// CapturedAt is the APP clock at the moment Engine observed the
	// selection (e.now(), the same injectable clock ClarificationSelectionEvent's
	// own CapturedAt uses), not the DB clock -- see that field's own doc
	// comment for why.
	CapturedAt time.Time
	// QuestionHash = QuestionHash(prior.Question) -- the SAME
	// canonicalizing hash ClarificationSelectionEvent.QuestionHash already
	// reuses, for the identical reason: one hash function, never a second
	// independently-maintained one.
	QuestionHash string
	// PriorResultID names the StructureNeeds-bearing InvestigationResult
	// Offered/Selected were both read from -- the join key back to that
	// immutable row, exactly like ClarificationSelectionEvent.PriorResultID.
	PriorResultID string
	// Member is the closed StructureNeedKind vocabulary value this event
	// reports a selection for -- "expected_kind" | "subject_anchor" |
	// "subject_handle" | "subject_candidate" (CHAOS-4012 #242 added the
	// 4th; window rides its own, separately designed WindowSelectionEvent,
	// design brief §2.4 -- window confirmation is
	// canonicalizeEvidenceWindow's own code path, not
	// canonicalizeStructure's, so it never reaches this capture point).
	// pgstructureselection.knownMemberValues (migration 0034) is this
	// vocabulary's own DB-enforced mirror -- keep both in lockstep.
	Member string
	// Offered is the COMPLETE option set the prior result's StructureNeeds
	// offered for Member -- every entry of the relevant KindOptions/
	// AnchorOptions/HandleOptions slice, not only the one redeemed. A
	// training signal needs the negative examples (options NOT chosen) as
	// much as the positive one, mirroring
	// ClarificationSelectionEvent.OfferedCandidates' own reasoning.
	Offered []StructureOfferedOption
	// Selected is the single entry of Offered whose ReceiptID matched the
	// caller's redeemed receipt -- echoed separately so a consumer never
	// has to re-scan Offered to find it.
	Selected StructureOfferedOption
	// Accepted reports whether Selected was the TOP-RANKED (Rank==0)
	// option -- design brief §2.4: "Accepted (selected == engine/prior
	// proposal)" -- the signal curation needs to measure how often a
	// caller confirms the system's own leading proposal versus overriding
	// it with a lower-ranked (or prior-sourced) alternative.
	Accepted bool
	// SelectionMode is the closed vocabulary design brief §2.4 names:
	// "human_panel | agent_receipt | agent_explicit | agent_explicit_echo".
	// Only the first two are reachable through this file's own capture
	// point today -- explicit structure fields (agent_explicit/
	// agent_explicit_echo) are an MCP-surface (P3) request extension that
	// has not landed on the canonical request contract yet
	// (canonicalizeStructure's own SCOPE NOTE); this event's Member/Offered/
	// Selected shape is already P3-ready, so wiring those two modes in is
	// an additive follow-up at that point, not a redesign here.
	SelectionMode string
	// SelectionProvenance reuses clarificationSelectionProvenance's own
	// best-effort human-vs-agent proxy verbatim (the SAME two signals,
	// the SAME closed vocabulary, the SAME documented limitation -- see
	// that function's doc comment) rather than inventing a second,
	// structure-specific proxy over identical inputs.
	SelectionProvenance string
	// The remaining fields are the deployment-CURRENT pipeline/gate
	// config active at the MOMENT this selection was observed -- the
	// identical CHAOS-3833/3862 reuse-key dimensions
	// ClarificationSelectionEvent already carries, reused here rather
	// than a parallel shape (that type's own doc comment covers why).
	ProjectionVersion  string
	ModelIdentities    []string
	RetrievalIdentity  ReuseRetrievalIdentity
	PromptVersions     ReusePromptVersions
	VersionAuthorities ReuseVersionAuthorities
	// Consensus is the CHAOS-3860 P6 gap fix (design brief §2.4/§B5:
	// "events gain ... ConsensusEvidence (3860 panel ids + agreement
	// bits)"), absent from the original P4 shipment (migration 0024) and
	// added here additively (migration 0026). Present ONLY on events a
	// 3860 agent-user (multi-model panel) acceptance run captures; nil on
	// every other capture path (human_panel, agent_explicit,
	// agent_explicit_echo, and single-model agent_receipt confirmations
	// with no panel behind them).
	Consensus *ConsensusEvidence
}

// ConsensusEvidence records a CHAOS-3860 multi-model panel's agreement
// shape for one StructureSelectionEvent -- design brief §2.4's own words,
// verbatim: "panel member model identity ids + per-member agreement bits
// -- ids/enums, nothing else". PanelModelIdentities names every panel
// member that independently produced a selection for this event's Member
// (e.g. sol/luna/opus model identities); AgreementBits is a PARALLEL slice
// (AgreementBits[i] corresponds to PanelModelIdentities[i]) reporting
// whether that panel member's own independently-derived selection matched
// Selected. Consensus curation (design brief §3.2's promotion rule) reads
// agreement across AgreementBits, never question text or free-form
// rationale -- neither field this type carries. Zero value (both slices
// nil/empty) is invalid wherever Consensus is non-nil; construct only with
// len(PanelModelIdentities) == len(AgreementBits) >= 2, distinct identities
// (pgstructureselection.validateEvent and migration 0027's own CHECK
// constraint -- ck_acr_cf_structure_selections_consensus_panel_size --
// both enforce this shape; migration 0026 owns the column itself and the
// SelectionMode gate).
//
// SCOPE NOTE (codex adversarial review, round 1, confirmed): this type and
// its validators enforce SHAPE only. Nothing here -- nor anywhere in this
// package -- can prove a given event's Consensus genuinely came from a
// real multi-model panel run rather than a single caller constructing a
// plausible-looking payload directly: SelectionMode=agent_receipt is
// shared by every credentialed confirmation, panel or not, and
// Engine.buildStructureSelectionEvent never sets this field today (no
// production writer exists yet). Closing that authenticity gap requires
// request-level provenance wiring that is an open architectural question,
// not this migration's scope -- see the CHAOS-3860 P6 activation report.
type ConsensusEvidence struct {
	PanelModelIdentities []string `json:"panel_model_identities"`
	AgreementBits        []bool   `json:"agreement_bits"`
}

// StructureSelectionSink is notified once per successfully-redeemed
// structure receipt (CHAOS-3927 P4). Optional dependency: nil means
// capture is off, matching ClarificationSelectionSink's own "absent means
// degrade, never fail" convention -- every other optional Context Fabric
// dependency behaves identically.
//
// RecordSelection MUST return promptly and carries no error return by
// design, for the SAME reason ClarificationSelectionSink.RecordSelection
// does (that method's own doc comment, unchanged here): Engine calls it
// SYNCHRONOUSLY from inside Investigate's hot path, and capture must NEVER
// break or delay an investigation. A durable-storage-backed implementation
// MUST do its actual write on its own background worker with its own
// bounded timeout, never inline in this call.
type StructureSelectionSink interface {
	RecordSelection(ctx context.Context, event StructureSelectionEvent)
}

const (
	structureSelectionModeHumanPanel   = "human_panel"
	structureSelectionModeAgentReceipt = "agent_receipt"
)

// structureSelectionMode derives StructureSelectionEvent.SelectionMode
// from the SAME two signals clarificationSelectionProvenance already
// reads (that function's own doc comment covers their limits identically
// here): AuthenticationMethodWebAssertion is this codebase's strongest
// available "a human is driving a browser" signal, so it maps to
// human_panel; every other authenticated caller redeeming a structure
// receipt today is, by construction, doing so through a credentialed
// (non-panel) surface -- agent_receipt. See StructureSelectionEvent.
// SelectionMode's own doc comment for why agent_explicit/
// agent_explicit_echo are not reachable yet.
func structureSelectionMode(principal storage.Principal) string {
	if principal.AuthenticationMethod == storage.AuthenticationMethodWebAssertion {
		return structureSelectionModeHumanPanel
	}
	return structureSelectionModeAgentReceipt
}
