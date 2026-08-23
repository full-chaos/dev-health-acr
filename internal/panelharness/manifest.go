// Package panelharness is the CHAOS-3860 P6 activation harness: a
// standalone driver that speaks the hosted ACR contract directly over HTTP
// (POST /api/v1/context-fabric/investigations), with a real per-panelist
// bearer credential, to run a multi-model panel through a bounded, N-turn
// select-and-continue confirmation flow (CHAOS-4146(a) generalizes the
// pivot-intent design brief's own two-turn flow, §2.4/§3.1, to as many
// rounds as MaxClarificationTurns allows) the sol-max architectural ruling
// on CHAOS-3860 (2026-08-20, adopting recommendation (b)) authorizes.
//
// SCOPE, PINNED BY THE RULING (do not relitigate here):
//   - Every panelist redemption is a REAL, credentialed hosted-API call.
//     Structure-offer receipts it redeems flow through the EXISTING P4
//     capture pipeline (internal/contextfabric/structure_capture.go,
//     internal/contextfabric/pgstructureselection) exactly like any other
//     agent_receipt confirmation -- this package adds no engine code, no
//     contract field, and never touches Postgres directly.
//   - This package NEVER constructs a non-nil
//     contextfabric.ConsensusEvidence and never writes to the
//     consensus_evidence column (it has no DB access at all -- HTTP only).
//     Consensus is computed here, client-side, from the harness's own
//     observations, and reported ONLY in the run manifest artifact this
//     file defines. Materializing verified consensus onto captured rows is
//     P5-scoped, later work, owned by a separately-ratified internal
//     annotator that reads these manifests -- not this package.
//   - The manifest schema below is a HARNESS-OWNED contract (mirrors
//     testdata/fullstack/v1's own "harness-owned schema... never enters
//     contracts/, not subject to the contract-first rule" precedent,
//     docs/fullstack-acceptance.md): it is the future P5 annotator's input,
//     documented in-repo (testdata/panelharness/v1/schema/panel_run_manifest.v1.schema.json,
//     docs/design/context-fabric-panel-run-manifest.md) as its own
//     versioned artifact, never folded into internal/contracts/v1.
package panelharness

import "time"

// ManifestSchemaVersion is this package's own schema identity, ladder
// mirroring contractsv1's own "<Name>Schema" version-bump discipline
// (see e.g. that package's ReportSchemaVersion v13/v17/v22 citations at
// each field's own introduction) -- ONE evolving const in this package,
// each new field's doc comment citing the ticket and version that added
// it, rather than a new schema file or directory per bump (CHAOS-4146(c):
// "started properly," this is that start).
//
//   - v1 (CHAOS-3860 P6): SchemaVersion, PanelRunID, OrgID, QuestionHash,
//     AlgorithmVersion, StartedAt, CompletedAt, Members. Retroactively also
//     covers ClarificationLogs (CHAOS-4146(a)) -- purely additive
//     (omitempty), so it shipped without its own bump; noted here so the
//     ladder is complete starting from v1, as CHAOS-4146(c) requires.
//   - v2 (CHAOS-4146(c)): CaseIndex (PanelRunManifest and PanelMemberRun),
//     RunTag, CorpusPath, CorpusSHA256 -- the batch corpus driver's own
//     provenance, all optional/omitempty for a single ad-hoc (-question)
//     run.
const ManifestSchemaVersion = "panel_run_manifest.v2"

// AlgorithmVersion identifies THIS package's own selection-and-consensus
// algorithm (how a panelist's redemption is recorded, how majority/agreement
// is computed) -- carried on every manifest so the future P5 annotator can
// tell which rules produced a given run's ConsensusSummary, and so a future
// algorithm change is a version bump here, never a silent behavior change
// under an unchanged manifest schema version.
const AlgorithmVersion = "panelharness.consensus.v1"

// PanelRunManifest is the IMMUTABLE, machine-readable artifact one full
// panel run produces: one file per (org, question) activation, covering
// every StructureNeeds member the panel drove toward a decisive
// confirmation. Immutable by convention (this package never mutates a
// manifest after WriteFile -- see run.go) exactly as the ruling requires;
// nothing in this repo re-opens or edits a written manifest.
//
// Ids/enums/hashes/bools only, matching the P4 capture-schema discipline
// this whole feature builds on: QuestionHash is the SAME canonicalizing
// hash contextfabric.QuestionHash produces, never the raw question text.
type PanelRunManifest struct {
	SchemaVersion    string    `json:"schema_version"`
	PanelRunID       string    `json:"panel_run_id"`
	OrgID            string    `json:"org_id"`
	QuestionHash     string    `json:"question_hash"`
	AlgorithmVersion string    `json:"algorithm_version"`
	StartedAt        time.Time `json:"started_at"`
	CompletedAt      time.Time `json:"completed_at"`
	// Members is one entry per StructureNeedKind (expected_kind |
	// subject_anchor | subject_handle) the panel actually drove -- window
	// rides its own, separately designed WindowSelectionEvent path (design
	// brief §2.4) and is out of this harness's scope, matching P4's own
	// StructureSelectionEvent.Member boundary exactly.
	Members []PanelMemberRun `json:"members"`
	// ClarificationLogs is one entry per panelist that attempted at least
	// one Investigate call, carrying that panelist's OWN turn-by-turn
	// clarification-loop history (CHAOS-4146(a)) -- independent of Members,
	// since a single turn's StructureNeeds can offer more than one member
	// at once. Omitted (never present) for a panelist rejected before any
	// call was attempted (e.g. the shared-credential guard). Capture-only,
	// exactly like AgreementBits below: no consensus/disagreement labeling
	// is derived from it here.
	ClarificationLogs []PanelistClarificationLog `json:"clarification_logs,omitempty"`
	// CaseIndex identifies which corpus case (0-based array position,
	// matching the two-turn trial harness's own oracle-annex indexing
	// convention) this manifest answers, when produced by the batch corpus
	// driver (cmd/acr-panel-harness -corpus) -- CHAOS-4146(c), schema v2.
	// Index only, NEVER the case's question text (corpus-safety discipline,
	// docs/design/context-fabric-panel-run-manifest.md §4). nil for a
	// single ad-hoc (-question) run.
	CaseIndex *int `json:"case_index,omitempty"`
	// RunTag groups every manifest one batch invocation writes -- mirrors
	// scripts/trial's own RUN_TAG discipline (UTC timestamp + PID,
	// collision-safe across concurrent invocations). Empty for a single
	// ad-hoc run. CHAOS-4146(c), schema v2.
	RunTag string `json:"run_tag,omitempty"`
	// CorpusPath/CorpusSHA256 are the batch run's own provenance: WHICH
	// corpus file drove it, and a hash proving it was not silently swapped
	// mid-batch -- path and hash only, the corpus's own question text is
	// never carried here. Empty for a single ad-hoc run. CHAOS-4146(c),
	// schema v2.
	CorpusPath   string `json:"corpus_path,omitempty"`
	CorpusSHA256 string `json:"corpus_sha256,omitempty"`
}

// ClarificationTurnOutcome is the closed-vocabulary classification for one
// clarification turn's own terminal-or-continuing outcome, CHAOS-4146(a)'s
// own per-turn telemetry requirement.
type ClarificationTurnOutcome string

const (
	// ClarificationTurnDecisive: the response carried no actionable
	// StructureNeeds -- a real terminal answer.
	ClarificationTurnDecisive ClarificationTurnOutcome = "decisive"
	// ClarificationTurnRefusedNoOffers: StructureNeeds named members
	// missing, but none of them are ones this package's Selector flow can
	// act on (e.g. window-only, CHAOS-4118) -- terminal, not a Selector
	// call.
	ClarificationTurnRefusedNoOffers ClarificationTurnOutcome = "refused_no_offers"
	// ClarificationTurnRefusedNotConfident: the Selector was invoked but
	// chose nothing -- terminal, an explicit refusal.
	ClarificationTurnRefusedNotConfident ClarificationTurnOutcome = "refused_not_confident"
	// ClarificationTurnContinued: the Selector chose at least one receipt
	// and the turn budget allows another round -- non-terminal.
	ClarificationTurnContinued ClarificationTurnOutcome = "continued"
	// ClarificationTurnExhausted: the Selector would have continued, but
	// this was the last turn the configured bound allows -- terminal.
	ClarificationTurnExhausted ClarificationTurnOutcome = "turn_exhausted"
)

// ClarificationTurnEvent is one turn's own outcome inside a single
// panelist's bounded clarification loop.
type ClarificationTurnEvent struct {
	// Turn is 1-indexed, matching the loop's own Investigate call count
	// (turn 1 is the initial ask, not a "turn 0").
	Turn    int                      `json:"turn"`
	Outcome ClarificationTurnOutcome `json:"outcome"`
	// OfferKinds is the distinct StructureNeedKind values this turn's
	// response actually offered (projectOffers' own member vocabulary),
	// in first-seen order -- omitted for a decisive turn, which offered
	// nothing.
	OfferKinds []string `json:"offer_kinds,omitempty"`
}

// PanelistClarificationLog is one panelist's own full turn-by-turn history
// for one panel run -- independent of Members, since a single turn's
// StructureNeeds can offer (and a single Selector call can choose among)
// more than one member at once.
type PanelistClarificationLog struct {
	CanonicalModelIdentity string                   `json:"canonical_model_identity"`
	Turns                  []ClarificationTurnEvent `json:"turns"`
}

// PanelMemberRun is one structure-need member's panel outcome: every
// panelist's independent redemption for that member, plus this package's
// own client-side consensus summary over them.
type PanelMemberRun struct {
	Member    string              `json:"member"`
	Panelists []PanelistSelection `json:"panelists"`
	// Complete reports whether EVERY configured panelist produced a
	// selection for this member (required invariant, ruling text: "exactly
	// one authenticated complete panel run matches org/question/member").
	// false whenever any panelist's turn errored, timed out, or offered no
	// matching receipt -- the P5 annotator's own promotion rule reads this
	// before ever trusting Consensus below.
	Complete bool `json:"complete"`
	// DistinctIdentities reports whether every Panelists[].CanonicalModelIdentity
	// value is unique (required invariant: "distinct canonical
	// identities") -- false means this member's panel cannot carry
	// multi-model authority no matter how the votes split, because two
	// "panelists" would really be the same model counted twice.
	DistinctIdentities bool             `json:"distinct_identities"`
	Consensus          ConsensusSummary `json:"consensus"`
	// CaseIndex mirrors PanelRunManifest.CaseIndex (denormalized onto every
	// member row so a flattened/joined reader over Members does not need
	// to carry the parent manifest's context) -- CHAOS-4146(c), schema v2.
	// nil for a single ad-hoc run, identical to the top-level field.
	CaseIndex *int `json:"case_index,omitempty"`
}

// PanelistSelection is one panelist's own, independently-derived redemption
// for one structure-need member.
type PanelistSelection struct {
	// CanonicalModelIdentity names the panelist (e.g. "anthropic/sol-max",
	// "openai-compatible/gpt-5.6-luna") -- an id/enum-shaped string, never a
	// display label.
	CanonicalModelIdentity string `json:"canonical_model_identity"`
	// PriorResultID/ReceiptID identify the offer this panelist redeemed --
	// the SAME (org_id, prior_result_id, member, selected_receipt_id)
	// tuple the ruling's row-resolution rule names as the future
	// annotator's lookup key into acr.context_fabric_structure_selections
	// (a bare result id is insufficient there; this manifest carries every
	// field that lookup needs).
	PriorResultID string `json:"prior_result_id"`
	ReceiptID     string `json:"receipt_id"`
	// AppliedValue is the typed id/enum value the redeemed offer
	// represents (a SubjectKind string, a canonical anchor id, or a handle
	// value) -- never caller-facing display text, mirroring
	// StructureOfferedOption.AppliedValue's own discipline exactly.
	AppliedValue string `json:"applied_value"`
	// Accepted reports whether AppliedValue was the top-ranked (rank 0)
	// offer -- same semantics as StructureSelectionEvent.Accepted.
	Accepted bool `json:"accepted"`
	// ConfirmedResultID is the turn-2 result_id where this redemption
	// actually landed (the decisive/continued result, distinct from
	// PriorResultID, which named the turn-1 result the offer came FROM).
	ConfirmedResultID string `json:"confirmed_result_id"`
}

// ConsensusSummary is this package's own client-side tally over one
// member's panelist selections -- reported data only, never a promotion
// decision (the actual authority threshold is P5/curation's own,
// separately-ratified rule; this package does not guess it).
type ConsensusSummary struct {
	// ValueCounts maps each distinct AppliedValue observed to how many
	// panelists picked it -- the full histogram, not just the winner, so a
	// 2-1-1 split is distinguishable from a clean 4-0 majority downstream.
	ValueCounts map[string]int `json:"value_counts"`
	// MajorityValue is the AppliedValue with the highest count (ties break
	// on the lexicographically smaller value, deterministic and
	// reproducible from ValueCounts alone -- never on panelist arrival
	// order, which a retry could reshuffle). Empty when Panelists is empty.
	MajorityValue string `json:"majority_value,omitempty"`
	// AgreementBits is parallel to PanelMemberRun.Panelists:
	// AgreementBits[i] reports whether Panelists[i].AppliedValue ==
	// MajorityValue -- the exact ConsensusEvidence.AgreementBits shape
	// (internal/contextfabric/structure_capture.go), computed here for
	// REPORTING only; this package never writes it to that column.
	AgreementBits []bool `json:"agreement_bits"`
}
