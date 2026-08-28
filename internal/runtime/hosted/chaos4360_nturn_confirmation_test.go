package hosted_test

// CHAOS-4360 hop 6, harness half ("conversations lack memory"). This file
// is a SIBLING to chaos3742_two_turn_confirmation_test.go, not a rewrite of
// it: it reuses that file's oracle-annex loader, trialCase/twoTurnRequest
// plumbing, trace capture, and env-wiring helpers verbatim (same package),
// and adds exactly one new thing that file cannot express -- an N-TURN
// case class.
//
// WHY a new file instead of a new arm inside the existing one: every arm in
// chaos3742_two_turn_confirmation_test.go is a FIXED two-call shape (turn 1
// ask, turn 2 confirm-or-inject). CHAOS-4355's live walkthrough (13:40
// 08-27, ticket comments) found a real defect that shape structurally
// cannot see: the live Workbench batches window+candidate receipts into
// one turn-2 call, and the engine's own structure-supersession guard
// (pginvestigation/store.go IsStructureSuperseded) can claim the candidate
// receipt before it applies -- superseded by the SAME call's own
// window-driven pool change -- forcing a third turn to redeem a FRESH
// candidate offer. This harness reproduces the underlying CHAOS-4360 gap
// directly, without depending on that specific batched-supersession path:
// runNTurnCase deliberately NEVER attaches window and candidate in the
// same call (see its own doc comment) -- window alone on one turn,
// candidate alone on a strictly LATER turn -- which guarantees any case
// needing both members takes a genuine third turn and isolates the gap
// precisely: nothing carries a confirmed window across turns server-side
// today, so turn 3 arrives with an INFERRED window, the window gate fires
// (WindowCanonicalizationGatedClassDefault), and the turn-3 candidate
// redemption can never land -- the question can never reach a decisive
// answer past two turns. Re-sending the turn-2 receipt instead of the
// fresh one (the Workbench stopgap, CHAOS-4355 comment 13:40) does not
// work either: the SAME supersession guard marks a resend of an
// already-claimed receipt "vetoed_stale" -- which is exactly why this
// harness's own never-resend contract (sentReceiptIDs) exists.
//
// This class exists to measure that gap directly against REAL acr (kiac
// data plane -- mock/e2e ACR servers do not model supersession, so a mock
// can never reproduce this class, per CHAOS-4355's own 13:40 finding), and
// to keep measuring it once lane-4360-acr's same-conversation carry lands.
//
// SCOPE, deliberately narrow: this class runs the SAME question across
// every turn (never authors new question text -- standing corpus-text
// rule), carrying window + subject_candidate structure only (the exact
// two members CHAOS-4355's live walkthrough exercised). It does not
// replicate chaos3742's kind/anchor/handle arms, its mutation probes, its
// confirmed_wrong anchor-negative seeding, or its graph-lifecycle
// read-proof -- none of those are the gap this class exists to close, and
// duplicating them here would be scope creep against a harness-only
// ticket. It reuses that file's shared setup helpers directly rather than
// re-deriving them.
//
// CARRY MEASUREMENT (presence-checked, not hard-coded): the wire already
// carries ContextFabricConfirmedStructureEntry.Source
// (contractsv1.ContextFabricStructureSource) with a closed pre-CHAOS-4360
// vocabulary of exactly three values -- receipt, explicit,
// explicit_unattributed (nTurnKnownPreCarrySource below). Per lane-4360-acr
// (PR #306, team communication 2026-08-27): a carried window keeps
// Provenance=clarification_confirmed unchanged and instead reports the
// carry on Source, a NEW closed-vocab value "carried". This class reads
// Source GENERICALLY (nTurnIsCarriedSource): any value OUTSIDE that closed
// set on a live result is, by construction, a new carry-source tier acr
// minted for CHAOS-4360. Provenance is ALSO read generically
// (nTurnIsCarriedProvenance) as a secondary, forward-compatible signal, in
// case a future change reports carry there too. Both together are what let
// the class run GREEN on origin/main today (neither field can carry an
// out-of-vocabulary value yet) and start MEASURING the fix the moment
// lane-4360-acr's change ships, with no further code change on the harness
// side and no coordination needed on an exact new enum spelling.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/runtime/hosted"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// nTurnReportSchemaVersion is this artifact family's OWN ladder, separate
// from twoTurnReport/expectedSchemaVersion's "39": nTurnReport is a
// brand-new shape, not a change to the two-turn report (which stays at 39
// unchanged -- CHAOS-4121's own precedent: a bump marks a meaning/shape
// change to THAT artifact, and nothing about twoTurnReport's own shape
// changed here). "40" continued the ONE shared version number
// cf-measurement-trials.md's run-history table tracks across every trial
// artifact type in this epic, for that table's own traceability -- not a
// claim that this JSON shape is byte-compatible with any prior version of
// itself (there was no prior version; v40 was this family's first artifact).
//
// "41" (CHAOS-4386, codex review round 2, P2, confirmed): nTurnCaseResult
// gains ResultBytes/EstTokens, and nTurnReport gains six run-level fields
// (MaxSerializedBytesConfigured/MaxResultBytes/P50ResultBytes/
// P99ResultBytes/OverMaxSerializedBytesCount/OverLegacy16KCount) -- see
// their own doc comments. A real, pre-existing v40 artifact from BEFORE
// this change exists (Run I, 2026-08-27/28) and carries none of these
// fields; without a bump, a v40-labeled artifact from either side of this
// change is indistinguishable, and a consumer cannot tell a legitimate
// zero from a field that simply did not exist yet.
const nTurnReportSchemaVersion = "41"

// nTurnKnownPreCarryProvenance is the CLOSED set of
// ContextFabricStructureProvenance values reachable before CHAOS-4360's
// acr-side same-conversation carry lands (context_fabric_structure_types.go:
// ContextFabricStructureInferredDefault/QuestionStated/
// ClarificationConfirmed -- exactly three, verified against that file).
// nTurnIsCarriedProvenance below is a presence-check kept for forward
// compatibility, but it is NOT the primary carry signal -- see
// nTurnKnownPreCarrySource/nTurnIsCarriedSource below, confirmed against
// lane-4360-acr's own PR #306 (team communication, 2026-08-27): a carried
// window keeps Provenance=clarification_confirmed (never a new value) and
// instead reports on ConfirmedStructureEntry.Source, a NEW closed-vocab
// value "carried" alongside today's receipt/explicit/explicit_unattributed
// (context_fabric_structure_types.go: ContextFabricStructureSourceReceipt/
// Explicit/ExplicitUnattributed -- exactly three on origin/main today).
var nTurnKnownPreCarryProvenance = map[string]bool{
	string(contractsv1.ContextFabricStructureInferredDefault):        true,
	string(contractsv1.ContextFabricStructureQuestionStated):         true,
	string(contractsv1.ContextFabricStructureClarificationConfirmed): true,
}

// nTurnIsCarriedProvenance reports whether value is a provenance tier
// OUTSIDE the closed pre-carry vocabulary above -- see this file's own
// header comment ("CARRY-PROVENANCE MEASUREMENT") for why this is read
// generically rather than pinned to one guessed spelling. Kept as a
// secondary, forward-compatible signal; nTurnIsCarriedSource is the
// primary one -- see its own doc comment.
func nTurnIsCarriedProvenance(value string) bool {
	return value != "" && !nTurnKnownPreCarryProvenance[value]
}

// nTurnKnownPreCarrySource is the CLOSED set of ContextFabricStructureSource
// values reachable before CHAOS-4360's acr-side same-conversation carry
// lands (context_fabric_structure_types.go: ContextFabricStructureSourceReceipt/
// Explicit/ExplicitUnattributed -- exactly three, verified against that
// file). This is the PRIMARY carry signal (lane-4360-acr, PR #306): a
// carried structure member sets Source to a NEW closed-vocab value
// "carried" (not yet in this repo's contractsv1 package as of this
// harness's own merge -- see nTurnIsCarriedSource's own doc comment for why
// it is still read generically rather than pinned to that literal).
var nTurnKnownPreCarrySource = map[string]bool{
	string(contractsv1.ContextFabricStructureSourceReceipt):              true,
	string(contractsv1.ContextFabricStructureSourceExplicit):             true,
	string(contractsv1.ContextFabricStructureSourceExplicitUnattributed): true,
}

// nTurnIsCarriedSource reports whether value is a structure-source tier
// OUTSIDE the closed pre-carry vocabulary above -- read generically (not
// pinned to the literal "carried") for the SAME reason
// nTurnIsCarriedProvenance is: contractsv1.ContextFabricStructureSourceCarried
// does not exist in this package as of this harness's own merge (lane-4360-acr's
// PR #306 was still in CI/review, unmerged), and hard-coding a string this
// harness's own compiled contractsv1 package cannot yet validate would
// silently stop matching if the real merged spelling differs by even one
// character. Once acr's own contractsv1.ContextFabricStructureSourceCarried
// constant is available in a future dependency bump, this may be tightened
// to compare against it directly -- not required for correctness today.
func nTurnIsCarriedSource(value string) bool {
	return value != "" && !nTurnKnownPreCarrySource[value]
}

// nTurnDecisive reports whether status is a terminal, answer-bearing
// outcome. Mirrors twoTurnPositiveFalseNoMatch's own "no_match is not
// decisive" reading and memberApplied's own decisive-status set.
func nTurnDecisive(status contractsv1.ContextFabricInvestigationStatus) bool {
	switch status {
	case contractsv1.ContextFabricInvestigationComplete,
		contractsv1.ContextFabricInvestigationPartial,
		contractsv1.ContextFabricInvestigationDegraded:
		return true
	default:
		return false
	}
}

// nTurnRowsCount sums ClaimedFactRow rows across every claimed fact on a
// result (CHAOS-4355 hop 5's own Rows field) -- the "≥1 Rows table
// rendered" half of this ticket's own acceptance bullet, measured
// end-to-end through the SAME wire shape Ask Dev renders from.
func nTurnRowsCount(facts []contractsv1.ContextFabricClaimedFact) int {
	count := 0
	for _, fact := range facts {
		count += len(fact.Rows)
	}
	return count
}

// nTurnSelectCandidateOffer scans result.StructureNeeds.CandidateOptions
// for the entry matching (kind, canonicalID) -- the oracle's own typed
// match key, mirroring selectOracleOffer's anchor case exactly (never "the
// offer the engine ranked first"). canonicalID is read from this run's
// oracle annex, the SAME subject_anchor-member PositiveAnchorCanonicalID
// the two-turn harness's own anchor arm already trusts -- see
// nTurnGroundTruthCandidate.
func nTurnSelectCandidateOffer(result contractsv1.ContextFabricInvestigationResult, kind, canonicalID string) (receiptID string, found bool) {
	if result.StructureNeeds == nil {
		return "", false
	}
	for _, opt := range result.StructureNeeds.CandidateOptions {
		if opt.CanonicalID == canonicalID && string(opt.Kind) == kind {
			return opt.ReceiptID, true
		}
	}
	return "", false
}

// nTurnSelectWindowOffer picks the FIRST offered window band,
// unconditional on which one: this class measures whether a CONFIRMED
// window carries across turns, never which band gets picked (band
// correctness is TestChaos3742TwoTurnConfirmationReplay's own window-member
// arm's job, oracle-verified there).
func nTurnSelectWindowOffer(result contractsv1.ContextFabricInvestigationResult) (receiptID string, found bool) {
	if result.StructureNeeds == nil || len(result.StructureNeeds.WindowOptions) == 0 {
		return "", false
	}
	return result.StructureNeeds.WindowOptions[0].ReceiptID, true
}

// nTurnWindowUnsafeCommit is this class's own CHAOS-4040 (W4) safety
// invariant: a decisive result must NEVER commit under an INFERRED window,
// independent of whether same-conversation carry exists yet. Evaluated
// only when EffectiveEvidenceWindow is present (nil legitimately means no
// window was ever in play for this result) -- a decisive result with a
// window in play but EffectiveEvidenceWindow absent would itself be a
// wire-contract violation, not this predicate's concern.
//
// codex review round 2 (P2, confirmed): the original version flagged
// ANYTHING other than clarification_confirmed as unsafe, which wrongly
// counted a legitimate question_stated window (the caller's own question
// text names an explicit window, e.g. "in the last 90 days") as unsafe --
// engine.go's own decisive-path gate (internal/contextfabric/engine.go)
// permits BOTH question_stated and clarification_confirmed; only
// inferred_default is the CHAOS-4040 gap. A carried window reports as
// clarification_confirmed (lane-4360-acr, PR #306), so this predicate
// needs no separate case for it.
func nTurnWindowUnsafeCommit(result contractsv1.ContextFabricInvestigationResult) bool {
	if !nTurnDecisive(result.Status) || result.EffectiveEvidenceWindow == nil {
		return false
	}
	return result.EffectiveEvidenceWindow.Provenance == contractsv1.ContextFabricWindowInferredDefault
}

// nTurnGroundTruthCandidate reads this index's subject_anchor-member
// oracle entry's PositiveAnchorCanonicalID out of the SAME twoTurnOracleAnnex
// the two-turn harness already loads and trusts -- the corpus's
// project/repository positive cases (the "idx 57/60 class" this class
// seeds from) carry their ground truth there, not under a dedicated
// subject_candidate oracle member (the signed annex predates
// CHAOS-4012/subject_candidate entirely; see CHAOS-4355 PR #304's own
// finding that subject_candidate never got its own oracle column). ok is
// false when this index has no anchor-member entry at all -- ArmInvalidReason,
// never a false pass or a fatal abort of the whole run.
func nTurnGroundTruthCandidate(annex twoTurnOracleAnnex, index int) (canonicalID string, ok bool) {
	for _, entry := range annex.Entries {
		if entry.Index == index && entry.Member == string(contractsv1.ContextFabricStructureNeedSubjectAnchor) && entry.PositiveAnchorCanonicalID != "" {
			return entry.PositiveAnchorCanonicalID, true
		}
	}
	return "", false
}

// nTurnReportHasUsableEvidence reports whether AT LEAST ONE case in report
// actually ran (as opposed to being skipped for missing an oracle entry, or
// erroring on every Investigate call) -- the "a measurement that did not
// happen must FAIL, loudly" invariant (AGENTS.md verification rule 4,
// codex review P1, confirmed). report.ArmInvalidCount counts BOTH the
// no-oracle-entry skip path and the investigate-error path (both set
// nTurnCaseResult.ArmInvalidReason), so a report where every result is
// arm-invalid produced zero usable live evidence even though
// WrongCommitCount/WindowUnsafeCommitCount would both read 0 -- neither of
// those counters can fire on a case that never ran far enough to attempt a
// commit at all. An empty report (zero selected cases) is also "no
// evidence", not vacuously true.
func nTurnReportHasUsableEvidence(report nTurnReport) bool {
	return len(report.Results) > 0 && len(report.Results) > report.ArmInvalidCount
}

// nTurnReportExercisedCarryTransition reports whether AT LEAST ONE case in
// report actually attempted the genuine carry-specific transition this
// class exists to walk: a turn sending PriorCandidateReceipts WITHOUT
// PriorWindowReceipts, i.e. redeeming a candidate after window was already
// settled on an earlier turn (codex review round 2, P1, confirmed). A run
// can pass nTurnReportHasUsableEvidence (cases genuinely ran, none
// arm-invalid) while every one of them converged or stalled inside turn 2
// alone -- exactly the shape the ORIGINAL 2026-08-27 baseline had before
// this fix, which is a real, reportable RED-baseline finding on its own
// (the harness's two-call construction never reached its own reason for
// existing), but must never be silently reported as "the class measured
// the gap" when it did not.
func nTurnReportExercisedCarryTransition(report nTurnReport) bool {
	for _, res := range report.Results {
		if res.CandidateOnlyTurnAttempted {
			return true
		}
	}
	return false
}

// nTurnParseCaseIndices parses ACR_TEST_NTURN_CASE_INDICES's own
// comma-separated format, rejecting a non-integer field, an empty result,
// or a REPEATED index (codex review round 2, P2, confirmed): a duplicate
// (e.g. "57,57") would otherwise run the same case twice under identical
// request ids, double-counting it in every aggregate report field --
// mirrors twoTurnShardCaseIndices' own duplicate-rejection discipline.
func nTurnParseCaseIndices(raw string) ([]int, error) {
	seen := map[int]bool{}
	var indices []int
	for _, field := range strings.Split(raw, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		parsed, err := strconv.Atoi(field)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", field)
		}
		if seen[parsed] {
			return nil, fmt.Errorf("index %d appears more than once -- a duplicate would run the same case twice and double-count it in every aggregate field", parsed)
		}
		seen[parsed] = true
		indices = append(indices, parsed)
	}
	if len(indices) == 0 {
		return nil, fmt.Errorf("parsed to zero indices")
	}
	return indices, nil
}

// nTurnTurnRecord is one turn's own observable outcome -- indices/kinds/
// enums only, per the standing corpus-text rule (never the question text,
// never a label/phrasing string).
type nTurnTurnRecord struct {
	TurnIndex                      int               `json:"turn_index"`
	Status                         string            `json:"status"`
	WindowCanonicalizationOutcome  string            `json:"window_canonicalization_outcome,omitempty"`
	ConfirmedStructureDispositions map[string]string `json:"confirmed_structure_dispositions,omitempty"`
	ConfirmedStructureProvenance   map[string]string `json:"confirmed_structure_provenance,omitempty"`
	// ConfirmedStructureSource is the PRIMARY carry signal -- see
	// nTurnIsCarriedSource's own doc comment. Recorded per member, same
	// shape as the Provenance/Disposition maps above.
	ConfirmedStructureSource        map[string]string `json:"confirmed_structure_source,omitempty"`
	PriorSubjectReceiptDispositions []string          `json:"prior_subject_receipt_dispositions,omitempty"`
	Missing                         []string          `json:"missing,omitempty"`
	CommittedCount                  int               `json:"committed_count"`
	SentWindowReceipt               bool              `json:"sent_window_receipt"`
	SentCandidateReceipt            bool              `json:"sent_candidate_receipt"`
	// WindowAlreadyAppliedThisTurn (codex review round 3, P1, confirmed) is
	// true only when an EARLIER turn already applied window before THIS
	// turn's own request was built -- the distinguishing fact between a
	// genuine carry-specific candidate-only turn (window survives across a
	// fresh call) and an ordinary candidate-only turn for a case whose
	// question never had a window in play at all.
	WindowAlreadyAppliedThisTurn bool `json:"window_already_applied_this_turn"`
}

// nTurnCaseResult is one case's full N-turn replay. TurnsTaken is the
// number of Investigate calls actually made (turn 1 counts as 1); Decisive/
// FinalStatus/RowsCount/WrongCommit/WindowUnsafeCommit describe the LAST
// turn reached, whether that turn was decisive or the loop simply ran out
// of offers or turns.
type nTurnCaseResult struct {
	Index              int               `json:"index"`
	ExpectKind         string            `json:"expect_kind,omitempty"`
	Arm                string            `json:"arm"`
	TurnsTaken         int               `json:"turns_taken"`
	Turns              []nTurnTurnRecord `json:"turns"`
	Decisive           bool              `json:"decisive"`
	FinalStatus        string            `json:"final_status"`
	RowsCount          int               `json:"rows_count"`
	WrongCommit        bool              `json:"wrong_commit"`
	WindowUnsafeCommit bool              `json:"window_unsafe_commit"`
	OfferMiss          bool              `json:"offer_miss"`
	ArmInvalidReason   string            `json:"arm_invalid_reason,omitempty"`
	// CarriedStructureObserved is true when ANY turn's ConfirmedStructure
	// entry carries a Source (primary signal) or Provenance (secondary,
	// forward-compatible signal) value outside the closed pre-CHAOS-4360
	// vocabulary -- see nTurnIsCarriedSource/nTurnIsCarriedProvenance.
	CarriedStructureObserved bool `json:"carried_structure_observed"`
	// CandidateOnlyTurnAttempted is true when this case sent a turn
	// carrying PriorCandidateReceipts WITHOUT PriorWindowReceipts, AND an
	// EARLIER turn had already applied window (nTurnTurnRecord.
	// WindowAlreadyAppliedThisTurn) -- the genuine, carry-specific
	// transition this class exists to walk: window already settled from an
	// earlier turn, candidate redeemed on a fresh call. codex review round
	// 3 (P1, confirmed) tightened this: the round-2 version counted ANY
	// candidate-only turn, which wrongly credited a case whose question
	// never had a window in play at all (no earlier application to carry)
	// as if it had exercised the carry boundary. False for a case that
	// never got past window, never needed a candidate, or sent candidate
	// before window was ever applied.
	CandidateOnlyTurnAttempted bool `json:"candidate_only_turn_attempted"`
	// ResultBytes/EstTokens (CHAOS-4386) mirror
	// twoTurnCaseResult.ResultBytes/EstTokens exactly -- see that field's
	// own doc comment (chaos3742_two_turn_confirmation_test.go) and
	// chaos4386MeasureResult (chaos4386_result_bytes_helpers_test.go).
	// Measured off runNTurnCase's own lastGoodResult -- the LAST turn that
	// actually returned successfully, whether or not it was decisive --
	// NOT off the loop's `result` variable directly: a turn that errors
	// leaves `result` holding call's zero-value return (codex review round
	// 1, P2, confirmed), and measuring that would report a fake, tiny
	// result_bytes for a turn the HTTP route would have answered with an
	// error envelope, never a 200. Zero when turn 1 itself errored
	// (ArmInvalidReason set, no result ever produced at all).
	ResultBytes int64 `json:"result_bytes"`
	EstTokens   int64 `json:"est_tokens"`
}

// nTurnReport is this class's own top-level artifact -- a single-shard
// JSON (this class runs a small, explicit seed set, never the full
// 212-case grid, so no merge step exists or is needed for it; see this
// file's own header comment and scripts/trial/run-n-turn.sh).
type nTurnReport struct {
	ReportSchemaVersion string          `json:"report_schema_version"`
	Provenance          trialProvenance `json:"provenance"`

	OracleAnnexPath      string `json:"oracle_annex_path"`
	OracleAnnexCorpusSHA string `json:"oracle_annex_corpus_sha256"`
	OracleAnnexSignedOff bool   `json:"oracle_annex_signed_off"`
	AnnexSignoffStale    bool   `json:"annex_signoff_stale"`

	MaxTurns                        int `json:"max_turns"`
	CasesRun                        int `json:"cases_run"`
	DecisiveCount                   int `json:"decisive_count"`
	OfferMissCount                  int `json:"offer_miss_count"`
	ArmInvalidCount                 int `json:"arm_invalid_count"`
	WrongCommitCount                int `json:"wrong_commit_count"`
	WindowUnsafeCommitCount         int `json:"window_unsafe_commit_count"`
	CarryHitCount                   int `json:"carry_hit_count"`
	CandidateOnlyTurnAttemptedCount int `json:"candidate_only_turn_attempted_count"`
	TurnsTakenSum                   int `json:"turns_taken_sum"`

	// MaxSerializedBytesConfigured/MaxResultBytes/P50ResultBytes/
	// P99ResultBytes/OverMaxSerializedBytesCount/OverLegacy16KCount
	// (CHAOS-4386) mirror twoTurnReport's identically-named fields exactly
	// -- see that struct's own doc comment (chaos3742_two_turn_confirmation_test.go)
	// and chaos4386ResultByteStats (chaos4386_result_bytes.go). This class
	// runs a small, explicit seed set with no merge/sharding step (this
	// struct's own top-of-block comment), so these are computed once,
	// directly from report.Results, with no separate merge-tool mirror to
	// keep in lockstep.
	MaxSerializedBytesConfigured int64 `json:"max_serialized_bytes_configured"`
	MaxResultBytes               int64 `json:"max_result_bytes"`
	P50ResultBytes               int64 `json:"p50_result_bytes"`
	P99ResultBytes               int64 `json:"p99_result_bytes"`
	OverMaxSerializedBytesCount  int   `json:"over_max_serialized_bytes_count"`
	OverLegacy16KCount           int   `json:"over_legacy_16k_count"`

	Results []nTurnCaseResult `json:"results"`
}

// nTurnRecordTurn builds one turn's record from the result just returned
// and the SAME trace capture the caller reset immediately before making
// this call (mirrors runTwoTurnPositiveArm's own reset-then-call-then-read
// discipline exactly, so windowCanonicalization can only ever reflect THIS
// call's own outcome).
func nTurnRecordTurn(turnIndex int, result contractsv1.ContextFabricInvestigationResult, trace *twoTurnTraceCapture) nTurnTurnRecord {
	rec := nTurnTurnRecord{
		TurnIndex:      turnIndex,
		Status:         string(result.Status),
		CommittedCount: len(result.SubjectResolution.Committed),
	}
	if trace != nil {
		rec.WindowCanonicalizationOutcome = string(trace.windowCanonicalization)
	}
	if len(result.ConfirmedStructure) > 0 {
		rec.ConfirmedStructureDispositions = make(map[string]string, len(result.ConfirmedStructure))
		rec.ConfirmedStructureProvenance = make(map[string]string, len(result.ConfirmedStructure))
		rec.ConfirmedStructureSource = make(map[string]string, len(result.ConfirmedStructure))
		for _, entry := range result.ConfirmedStructure {
			rec.ConfirmedStructureDispositions[string(entry.Member)] = string(entry.Disposition)
			rec.ConfirmedStructureProvenance[string(entry.Member)] = string(entry.Provenance)
			rec.ConfirmedStructureSource[string(entry.Member)] = string(entry.Source)
		}
	}
	for _, entry := range result.SubjectResolution.PriorSubjectReceiptDispositions {
		rec.PriorSubjectReceiptDispositions = append(rec.PriorSubjectReceiptDispositions, string(entry.Disposition))
	}
	if result.StructureNeeds != nil {
		for _, member := range result.StructureNeeds.Missing {
			rec.Missing = append(rec.Missing, string(member))
		}
	}
	return rec
}

// nTurnMaxTurnsDefault bounds the loop. CHAOS-4355's live walkthrough
// reproduces the defect by turn 3; this leaves headroom to observe a fixed
// engine actually CONVERGE (turn 3 decisive) without letting a genuinely
// stuck case spin turns (and API spend) indefinitely.
const nTurnMaxTurnsDefault = 5

// runNTurnCase drives one case through the N-turn window+candidate carry
// class: turn 1 asks the (unmodified, corpus-verbatim) question; each
// subsequent turn attaches EXACTLY the receipts this run has not yet
// successfully applied and that the immediately-prior response actually
// offered -- carrying prior_result_id (ContextFabricBoundSubjectReceipt.
// ResultID) forward each time, and NEVER re-sending a receipt (applied or
// not) this case has already sent in an earlier turn, per this ticket's
// own "never re-send an applied receipt" instruction and the CHAOS-4355
// 13:40 finding that a resend of ANY already-claimed receipt (applied or
// vetoed) is itself a vetoed_stale trap. The loop stops the instant a turn
// is decisive, the instant no eligible offer remains to attach (OfferMiss),
// or maxTurns is reached, whichever comes first.
func runNTurnCase(t *testing.T, ctx context.Context, investigator contextfabric.Investigator, principal storage.Principal, index int, tc trialCase, canonicalID string, maxTurns int, timeout time.Duration, trace *twoTurnTraceCapture) nTurnCaseResult {
	t.Helper()
	if maxTurns < 2 {
		maxTurns = 2
	}
	res := nTurnCaseResult{Index: index, ExpectKind: tc.ExpectKind, Arm: "nturn_window_candidate_carry"}

	call := func(req contractsv1.ContextFabricInvestigationRequest) (contractsv1.ContextFabricInvestigationResult, error) {
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if trace != nil {
			trace.reset()
		}
		return investigator.Investigate(callCtx, principal, req)
	}

	result, err := call(twoTurnRequest(index, tc, "nturn01"))
	if err != nil {
		res.ArmInvalidReason = "turn 1 investigate error: " + contextFabricRejectionClass(err)
		return res
	}
	res.Turns = append(res.Turns, nTurnRecordTurn(1, result, trace))
	res.TurnsTaken = 1
	// lastGoodResult (CHAOS-4386, codex review round 1, P2, confirmed)
	// tracks the last SUCCESSFUL Investigate() result, separate from
	// `result` itself: a later turn's error still overwrites `result` with
	// call's zero-value return, and measuring THAT would report a fake,
	// tiny result_bytes for a case that never actually got an answer at
	// that turn -- the HTTP route would have returned an error envelope,
	// never a 200 with a near-empty body. Only ResultBytes/EstTokens read
	// this; FinalStatus/Decisive/RowsCount/WrongCommit/WindowUnsafeCommit
	// below are unchanged pre-existing behavior, out of this fix's scope.
	lastGoodResult := result

	appliedWindow := false
	appliedCandidate := false
	// sentReceiptIDs (codex review, P2, confirmed): tracks EVERY receipt ID
	// this case has ever sent, independent of whether it applied. The
	// appliedWindow/appliedCandidate booleans above only flip once a member
	// genuinely applies -- a receipt that came back VETOED (not applied)
	// would otherwise still look "not yet applied" to the guard above, so a
	// later turn re-offering that SAME receipt id (a stale-but-still-listed
	// offer) would be resent. CHAOS-4355's own 13:40 08-27 finding is that
	// resending ANY already-claimed receipt -- applied or vetoed -- is a
	// vetoed_stale trap, so this set closes on the wire id itself, not on
	// the outcome.
	sentReceiptIDs := map[string]bool{}

	for turnIndex := 2; turnIndex <= maxTurns; turnIndex++ {
		if nTurnDecisive(result.Status) {
			break
		}
		next := twoTurnRequest(index, tc, fmt.Sprintf("nturn%02d", turnIndex))
		attachedWindow := false
		attachedCandidate := false
		// WINDOW FIRST, CANDIDATE ONLY ONCE WINDOW IS SETTLED (codex review
		// round 2, P1, confirmed): the two are deliberately NEVER attached in
		// the SAME call. Batching them (the original version's behavior,
		// mirroring runTwoTurnPositiveArm's own two-turn pattern) let both
		// members apply -- or fail -- inside turn 2 alone for this class's
		// own project-candidate seed set (idx 57/60), so the live baseline
		// never actually reached a genuine turn 3 candidate-only exchange --
		// the exact transition this class exists to walk. Trying window
		// alone first, and candidate only on a LATER turn once window has
		// either applied or is no longer offered, guarantees any case that
		// needs both members takes at least 3 turns: turn 1 ask, turn 2
		// window, turn 3+ candidate -- a real multi-turn carry attempt, not
		// a two-call shape wearing an N-turn label.
		//
		// "attemptedWindow" (not "appliedWindow") gates the fallthrough to
		// candidate: a case whose question never needs a window at all
		// (window simply never offered) must still reach candidate on turn
		// 2, not stall forever waiting for a window offer that will never
		// arrive.
		attemptedWindow := false
		if !appliedWindow {
			if receiptID, found := nTurnSelectWindowOffer(result); found && !sentReceiptIDs[receiptID] {
				next.PriorWindowReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: result.ResultID, ReceiptID: receiptID}}
				attachedWindow = true
				attemptedWindow = true
				sentReceiptIDs[receiptID] = true
			}
		}
		// windowAlreadyAppliedThisTurn (codex review round 3, P1, confirmed):
		// captured BEFORE this turn's own call, so it reflects an EARLIER
		// turn's own genuine window application -- not "window was simply
		// never needed for this case at all". Only the former is the real
		// carry-specific transition (window survives across a fresh call);
		// a case whose question never has a window in play sends
		// candidate-only turns too, but that is an ordinary redemption, not
		// evidence this class's own carry gap was exercised.
		windowAlreadyAppliedThisTurn := appliedWindow
		if !attemptedWindow && !appliedCandidate {
			if receiptID, found := nTurnSelectCandidateOffer(result, tc.ExpectKind, canonicalID); found && !sentReceiptIDs[receiptID] {
				next.PriorCandidateReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: result.ResultID, ReceiptID: receiptID}}
				attachedCandidate = true
				sentReceiptIDs[receiptID] = true
			}
		}
		if !attachedWindow && !attachedCandidate {
			res.OfferMiss = true
			break
		}
		result, err = call(next)
		if err != nil {
			res.ArmInvalidReason = fmt.Sprintf("turn %d investigate error: %s", turnIndex, contextFabricRejectionClass(err))
			break
		}
		lastGoodResult = result
		rec := nTurnRecordTurn(turnIndex, result, trace)
		rec.SentWindowReceipt = attachedWindow
		rec.SentCandidateReceipt = attachedCandidate
		rec.WindowAlreadyAppliedThisTurn = windowAlreadyAppliedThisTurn
		res.Turns = append(res.Turns, rec)
		res.TurnsTaken = turnIndex
		for _, entry := range result.ConfirmedStructure {
			if entry.Disposition != contractsv1.ContextFabricStructureDispositionApplied {
				continue
			}
			switch entry.Member {
			case contractsv1.ContextFabricStructureNeedWindow:
				appliedWindow = true
			case contractsv1.ContextFabricStructureNeedSubjectCandidate:
				appliedCandidate = true
			}
		}
	}

	res.FinalStatus = string(result.Status)
	res.Decisive = nTurnDecisive(result.Status)
	res.RowsCount = nTurnRowsCount(result.ClaimedFacts)
	res.WrongCommit = twoTurnCommittedWrong(result.SubjectResolution.Committed, tc)
	res.WindowUnsafeCommit = nTurnWindowUnsafeCommit(result)
	for _, rec := range res.Turns {
		if rec.SentCandidateReceipt && !rec.SentWindowReceipt && rec.WindowAlreadyAppliedThisTurn {
			res.CandidateOnlyTurnAttempted = true
		}
		for _, source := range rec.ConfirmedStructureSource {
			if nTurnIsCarriedSource(source) {
				res.CarriedStructureObserved = true
			}
		}
		for _, provenance := range rec.ConfirmedStructureProvenance {
			if nTurnIsCarriedProvenance(provenance) {
				res.CarriedStructureObserved = true
			}
		}
	}
	// CHAOS-4386: measured off lastGoodResult, never `result` directly --
	// see that variable's own doc comment (codex review round 1, P2,
	// confirmed) for why a turn that errored must not contribute its
	// zero-value return to this measurement.
	res.ResultBytes, res.EstTokens = chaos4386MeasureResult(lastGoodResult)
	return res
}

// TestChaos4360NTurnConfirmationCarry is the LIVE class: real acr (kiac
// data plane, per this repo's standing "kiac is the only trial plane"
// rule), a small explicit seed set (never the full 212-case grid -- see
// ACR_TEST_NTURN_CASE_INDICES below), producing one per-shard JSON report.
//
// This test is DELIBERATELY not gated on DecisiveCount>0: on origin/main
// today (pre-CHAOS-4360-acr), every case is expected to stall past turn 2
// exactly as CHAOS-4355's live walkthrough found -- that IS the RED
// baseline this run exists to record, not a bug in this test. Only the two
// SAFETY invariants (WrongCommitCount, WindowUnsafeCommitCount) fail the
// test -- see their own t.Errorf calls below -- because those must hold
// regardless of whether carry exists yet: a missing carry may only ever
// make this class UNDECISIVE, never wrong or unsafe.
func TestChaos4360NTurnConfirmationCarry(t *testing.T) {
	corpusPath := os.Getenv("ACR_TEST_TRIAL_CORPUS")
	if corpusPath == "" {
		t.Skip("ACR_TEST_TRIAL_CORPUS is not set; the CHAOS-3742 trial corpus is withheld and supplied at run time")
	}
	annexPath := os.Getenv("ACR_TEST_NTURN_ORACLE_ANNEX")
	if annexPath == "" {
		t.Skip("ACR_TEST_NTURN_ORACLE_ANNEX is not set; the DP10 oracle annex is withheld and supplied at run time")
	}
	indicesRaw := os.Getenv("ACR_TEST_NTURN_CASE_INDICES")
	if indicesRaw == "" {
		t.Skip("ACR_TEST_NTURN_CASE_INDICES is not set; this class runs an explicit, small seed set only -- never a default full-corpus sweep")
	}
	orgID := requireEnv(t, "ACR_TEST_TRIAL_ORG")
	outPath := requireEnv(t, "ACR_TEST_NTURN_OUT")
	if _, err := os.Stat(outPath); err == nil {
		t.Fatalf("ACR_TEST_NTURN_OUT=%s already exists -- refusing to silently overwrite existing acceptance evidence (mirrors chaos3742's own ACR_TEST_TWOTURN_OUT rule)", outPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat ACR_TEST_NTURN_OUT=%s: %v", outPath, err)
	}
	maxTurns := nTurnMaxTurnsDefault
	if raw := os.Getenv("ACR_TEST_NTURN_MAX_TURNS"); raw != "" {
		parsed, perr := strconv.Atoi(raw)
		if perr != nil || parsed < 2 {
			t.Fatalf("ACR_TEST_NTURN_MAX_TURNS=%q must be an integer >= 2", raw)
		}
		maxTurns = parsed
	}
	caseIndices, perr := nTurnParseCaseIndices(indicesRaw)
	if perr != nil {
		t.Fatalf("ACR_TEST_NTURN_CASE_INDICES=%q: %v", indicesRaw, perr)
	}
	if len(caseIndices) > 20 {
		t.Fatalf("ACR_TEST_NTURN_CASE_INDICES names %d cases -- this class's own RED-baseline run is capped at 20 (never the full corpus); pass a smaller seed set", len(caseIndices))
	}

	runStartedAt := time.Now().UTC().Format(time.RFC3339)
	annex := loadTwoTurnOracleAnnex(t, annexPath)
	if err := requireAnnexSignedOff(annex); err != nil {
		t.Fatalf("refusing to run the N-turn confirmation carry class: %v", err)
	}
	corpus, corpusHash := loadTrialCorpus(t, corpusPath)
	if len(corpusHash) < 8 || annex.CorpusSHA256 != corpusHash[:8] {
		t.Fatalf("oracle annex corpus_sha8=%s does not match the loaded corpus hash prefix=%.8s -- refusing to run against a mismatched annex/corpus pair", annex.CorpusSHA256, corpusHash)
	}
	// Operator-supplied corpus pin (codex review, P2, confirmed): mirrors
	// chaos3884_replay_harness_test.go's own ACR_TEST_TRIAL_CORPUS_SHA256
	// check exactly. The sha8-prefix agreement just above only proves the
	// LOADED corpus and the LOADED annex agree with each other -- it says
	// nothing about whether the loaded corpus is the one an operator
	// explicitly pinned via ACR_TRIAL_CORPUS_SHA256 (run-n-turn.sh's own
	// pass-through). A different corpus file paired with a matching annex
	// would pass the check above while silently running under unintended
	// data.
	if expected := os.Getenv("ACR_TEST_TRIAL_CORPUS_SHA256"); expected != "" && expected != corpusHash {
		t.Fatalf("corpus SHA-256 mismatch: got %s, want %s (ACR_TEST_TRIAL_CORPUS_SHA256) -- refusing to run against unexpected corpus content", corpusHash, expected)
	}
	// CHAOS-4348 corpus/annex SEMANTIC agreement (codex review, P1,
	// confirmed): the sha8-prefix check above proves the two FILES agree;
	// it does NOT prove the annex's per-case expected_kind/canonical_id
	// agrees with the corpus's own per-case content at the SAME index --
	// chaos4348_corpus_annex_agreement_test.go exists specifically because
	// Run G found cases 57/60 (this class's own named seed set) stale in
	// exactly that way once before. Skipping this preflight would let a
	// drifted pair select-by-annex-id but score-against-corpus, producing
	// a false offer_miss/false green baseline instead of refusing.
	twoTurnValidateCorpusAnnexAgreement(t, annex, corpus)
	source := requireGitSourceIdentity(t)

	exchangeDir := os.Getenv("ACR_TEST_TRIAL_EXCHANGE_DIR")
	wireProductionEnv(t, exchangeDir != "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	// CHAOS-4386 (codex review round 2, P2, confirmed): fail fast -- see
	// chaos3742's own identical check for why.
	chaos4386RequireCompatibleConfig(t, int64(cfg.MaxSerializedBytes))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	traceCapture := &twoTurnTraceCapture{SlogEngineTelemetry: contextfabric.NewSlogEngineTelemetry(logger)}
	caseTimeout := 240 * time.Second
	modelCallCapture := &twoTurnModelCallCapture{}
	options := hosted.Options{
		ServiceVersion: "chaos-4360-nturn", Logger: logger, Now: time.Now,
		ResolutionTracer: traceCapture,
		EngineTelemetry:  traceCapture,
	}
	if exchangeDir != "" {
		timeout := 10 * time.Minute
		if raw := os.Getenv("ACR_TEST_TRIAL_EXCHANGE_TIMEOUT"); raw != "" {
			parsed, perr := time.ParseDuration(raw)
			if perr != nil {
				t.Fatalf("ACR_TEST_TRIAL_EXCHANGE_TIMEOUT: %v", perr)
			}
			timeout = parsed
		}
		exchangeRuntime, ferr := newFileExchangeRuntime(exchangeDir, os.Getenv("ACR_TEST_TRIAL_ARM"), timeout)
		if ferr != nil {
			t.Fatalf("create file-exchange runtime: %v", ferr)
		}
		options.ModelRuntimeOverride = &twoTurnTimedModelRuntime{underlying: exchangeRuntime, capture: modelCallCapture}
		caseTimeout = 2*timeout + 30*time.Second
	}
	rt, err := hosted.Open(ctx, cfg, options)
	if err != nil {
		t.Fatalf("open hosted runtime: %v", err)
	}
	// CHAOS-4100 graph-lifecycle read-proof (codex review, P1, confirmed;
	// mirrors chaos3742_two_turn_confirmation_test.go's own "THE PROOF"
	// comment and chaos3884_replay_harness_test.go's buildReplayEpochResolver
	// exactly). run-n-turn.sh calls trial_wire_graph_lifecycle_env for every
	// launch, which re-exports pglifecycle.EnvEnabled the SAME way every
	// other trial script sharing wireProductionEnv does -- hosted.Open above
	// already wires ITS OWN epochResolver into the engine internally when
	// that flag is on, but never exposes what it resolved to THIS harness's
	// own report. Without this second, independent resolver instance (same
	// DSN/org, same pglifecycle.ConfigFromEnv gate), this report would
	// silently serialize resolved_active_epoch=0/graph_lifecycle_enabled=false
	// even when the engine itself is reading a live, rebuilt epoch --
	// exactly the CHAOS-4100 rerun #2 incident this proof exists to prevent
	// a repeat of. Runs even when the flag is off (nil-safe).
	epochResolver, err := buildReplayEpochResolver(t, ctx, os.Getenv("ACR_TEST_TRIAL_POSTGRES_DSN"))
	if err != nil {
		t.Fatalf("build epoch resolver: %v", err)
	}
	var resolvedActiveEpoch int64
	if epochResolver != nil {
		resolvedActiveEpoch, err = epochResolver.ResolveActiveEpoch(ctx, orgID)
		if err != nil {
			t.Fatalf("resolve active epoch for %s: %v", orgID, err)
		}
		if resolvedActiveEpoch <= 0 {
			t.Fatalf("%s is set but ResolveActiveEpoch(%s) = %d -- want a positive epoch; refusing to silently measure epoch 0 under a flag that claims otherwise", pglifecycle.EnvEnabled, orgID, resolvedActiveEpoch)
		}
	}
	t.Logf("CHAOS-4100 graph-lifecycle proof: org=%s resolved_active_epoch=%d (epochResolver wired=%v)", orgID, resolvedActiveEpoch, epochResolver != nil)
	defer func() {
		if cerr := rt.Close(); cerr != nil {
			t.Logf("runtime close: %v", cerr)
		}
	}()
	investigator := rt.Dependencies.Runtime.Investigator
	if investigator == nil {
		t.Fatal("investigator is nil -- graph reads not enabled or FalkorDB not configured")
	}
	// CHAOS-4386 (codex review round 2, P1, confirmed): wraps every
	// Investigate() call this run makes -- turn 1s and every later turn --
	// so the run-level result_bytes distribution sees every response the
	// real HTTP route would have gated, not merely each case's own final
	// (lastGoodResult) result. See chaos4386MeasuringInvestigator's own
	// doc comment.
	resultByteMeasurer := &chaos4386MeasuringInvestigator{Investigator: investigator}
	investigator = resultByteMeasurer
	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}

	transportLabel, responderModel, responderTransport, responderEffort := twoTurnResponderProvenance(t, exchangeDir)
	report := nTurnReport{
		ReportSchemaVersion:  nTurnReportSchemaVersion,
		OracleAnnexPath:      annexPath,
		OracleAnnexCorpusSHA: annex.CorpusSHA256,
		OracleAnnexSignedOff: annex.SignedOff,
		AnnexSignoffStale:    annex.SignoffStale,
		MaxTurns:             maxTurns,
		Provenance: trialProvenance{
			CorpusSHA256: corpusHash, Transport: transportLabel, RunStartedAt: runStartedAt,
			SourceCommit: source.commit, SourceDirty: source.dirty, SourceDiffDigest: source.diffDigest,
			ResponderModel:        responderModel,
			ResponderTransport:    responderTransport,
			ResponderEffort:       responderEffort,
			DataPlane:             os.Getenv("ACR_TEST_TRIAL_DATA_PLANE"),
			DataPlanePGHost:       os.Getenv("ACR_TEST_TRIAL_PG_HOST"),
			DataPlaneCHHost:       os.Getenv("ACR_TEST_TRIAL_CH_HOST"),
			ResolvedActiveEpoch:   resolvedActiveEpoch,
			GraphLifecycleEnabled: epochResolver != nil,
		},
	}

	for _, index := range caseIndices {
		if index < 0 || index >= len(corpus) {
			t.Fatalf("ACR_TEST_NTURN_CASE_INDICES names index %d, out of range for a %d-case corpus", index, len(corpus))
		}
		tc := corpus[index]
		canonicalID, ok := nTurnGroundTruthCandidate(annex, index)
		if !ok {
			report.Results = append(report.Results, nTurnCaseResult{
				Index: index, ExpectKind: tc.ExpectKind, Arm: "nturn_window_candidate_carry",
				ArmInvalidReason: "no subject_anchor oracle entry for this index -- not eligible for the N-turn candidate-carry class",
			})
			report.ArmInvalidCount++
			continue
		}
		res := runNTurnCase(t, ctx, investigator, principal, index, tc, canonicalID, maxTurns, caseTimeout, traceCapture)
		report.Results = append(report.Results, res)
		report.CasesRun++
		report.TurnsTakenSum += res.TurnsTaken
		if res.ArmInvalidReason != "" {
			report.ArmInvalidCount++
		}
		if res.Decisive {
			report.DecisiveCount++
		}
		if res.OfferMiss {
			report.OfferMissCount++
		}
		if res.WrongCommit {
			report.WrongCommitCount++
		}
		if res.WindowUnsafeCommit {
			report.WindowUnsafeCommitCount++
		}
		if res.CarriedStructureObserved {
			report.CarryHitCount++
		}
		if res.CandidateOnlyTurnAttempted {
			report.CandidateOnlyTurnAttemptedCount++
		}
		t.Logf("case %d: turns_taken=%d decisive=%v final_status=%s carried_structure_observed=%v candidate_only_turn_attempted=%v", index, res.TurnsTaken, res.Decisive, res.FinalStatus, res.CarriedStructureObserved, res.CandidateOnlyTurnAttempted)
	}

	// A measurement that did not happen must FAIL, loudly (AGENTS.md
	// verification rule 4; codex review, P1, confirmed) -- see
	// nTurnReportHasUsableEvidence's own doc comment.
	if !nTurnReportHasUsableEvidence(report) {
		t.Fatalf("every selected case (%d) was arm-invalid (no oracle entry, or every Investigate call errored) -- this run produced NO usable live evidence; refusing to report exit=0 for a measurement that did not happen", len(report.Results))
	}
	// The carry-specific transition itself must have been exercised at
	// least once (codex review round 2, P1, confirmed) -- see
	// nTurnReportExercisedCarryTransition's own doc comment for why
	// nTurnReportHasUsableEvidence alone is not sufficient.
	if !nTurnReportExercisedCarryTransition(report) {
		t.Fatalf("none of the %d selected cases ever sent a candidate-only turn (PriorCandidateReceipts without PriorWindowReceipts) -- every case converged or stalled inside window-only turns, so this run never exercised the carry-specific transition CHAOS-4360 exists to measure; pass a seed set that needs a subject_candidate redemption after window settles", len(report.Results))
	}

	// CHAOS-4386: result_bytes run-level distribution, derived from the
	// rows this run actually produced, once, immediately before
	// serialization -- see nTurnReport's own doc comment.
	// MaxSerializedBytesConfigured is the EFFECTIVE cap the HTTP route
	// would actually apply -- see chaos3742's own identical comment
	// (codex review round 1, P2, confirmed) for why cfg.MaxSerializedBytes
	// alone is not the right threshold here either (runNTurnCase's calls
	// go through the SAME twoTurnRequest, same fixed 262144 request option).
	report.MaxSerializedBytesConfigured = chaos4386EffectiveMaxSerializedBytes(int64(cfg.MaxSerializedBytes))
	// resultByteMeasurer.snapshot() (codex review round 2, P1, confirmed):
	// every turn this run made, not merely each case's own final
	// lastGoodResult -- see chaos4386MeasuringInvestigator's own doc
	// comment for why an oversized intermediate turn must not go unseen.
	resultByteValues := resultByteMeasurer.snapshot()
	report.MaxResultBytes, report.P50ResultBytes, report.P99ResultBytes,
		report.OverMaxSerializedBytesCount, report.OverLegacy16KCount =
		chaos4386ResultByteStats(resultByteValues, report.MaxSerializedBytesConfigured)

	raw, merr := json.MarshalIndent(report, "", "  ")
	if merr != nil {
		t.Fatalf("marshal n-turn report: %v", merr)
	}
	// O_EXCL (codex review, P2, confirmed): the os.Stat check above only
	// proves outPath was absent AT THAT MOMENT -- two concurrent
	// invocations sharing the same explicit ACR_TEST_NTURN_OUT could both
	// pass that check before either writes, and a plain os.WriteFile then
	// truncates whichever run's artifact wrote second, silently discarding
	// the first run's acceptance evidence. O_CREATE|O_EXCL makes the
	// no-overwrite guarantee this test already states hold under
	// concurrency too, mirroring the two-turn report writer's own
	// discipline.
	outFile, operr := os.OpenFile(outPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if operr != nil {
		t.Fatalf("create %s: %v", outPath, operr)
	}
	if _, werr := outFile.Write(raw); werr != nil {
		_ = outFile.Close()
		t.Fatalf("write %s: %v", outPath, werr)
	}
	if cerr := outFile.Close(); cerr != nil {
		t.Fatalf("close %s: %v", outPath, cerr)
	}
	t.Logf("N-turn report written to %s: cases_run=%d decisive_count=%d carry_hit_count=%d turns_taken_sum=%d", outPath, report.CasesRun, report.DecisiveCount, report.CarryHitCount, report.TurnsTakenSum)

	if report.WrongCommitCount > 0 {
		t.Errorf("wrong_commit_count=%d, want 0 -- a decisive commit under this class must never select the wrong subject, regardless of whether carry exists yet", report.WrongCommitCount)
	}
	if report.WindowUnsafeCommitCount > 0 {
		t.Errorf("window_unsafe_commit_count=%d, want 0 -- CHAOS-4040: a decisive commit must never happen under an unconfirmed (inferred) window", report.WindowUnsafeCommitCount)
	}
}
