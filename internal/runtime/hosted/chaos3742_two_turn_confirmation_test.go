package hosted_test

// CHAOS-3742 acceptance debt (pivot-intent design brief, DESIGN-FINAL §5
// head + acceptance rows "P1+turn-2" and "P1+turn-2 + windows"; matrix
// DP3 RATIFIED, DP9 pre-registered bars, DP10 oracle-annex mechanism
// RATIFIED). This is the TWO-TURN CONFIRMATION REPLAY: the reach gate for
// the P1/P2/P3 merges, never delivered before those merges landed -- this
// file is that delivery.
//
// Turn 1 = a standing replay over the frozen corpus (interpret + resolve +
// census + gate, the SAME production Investigator TestGenerativeTrialCorpus
// drives via hosted.Open's own Dependencies.Runtime.Investigator),
// producing StructureNeeds offers on stalled/refused cases. Turn 2 replays
// the SAME question carrying confirmation receipts (or, for the
// inferred-tier arm, unconfirmed explicit structure), through FOUR
// mandatory arms (§5 head):
//
//   - positive:        confirm the oracle-matching offer -- measures conversion.
//   - inferred_tier:   inject the oracle's negative value WITHOUT a receipt
//     (explicit field, Consumer.Surface="mcp" so it lands at
//     inferred_default/explicit_unattributed, never question_stated).
//     CHAOS-4039 v5 measurement contract (team-lead ruling 2026-08-22,
//     superseding the v4 sol-max ruling 2026-08-20): window retains the
//     ORIGINAL "ANY commit fails" bar (WindowCommitCount). CHAOS-4040
//     (sol-max ruling 2026-08-21, PR #181) shipped an unconditional gate
//     over every inferred window instead of a kind/handle-style
//     noninterference proof -- see WindowGatedCount's own doc comment
//     (twoTurnReport) for the non-vacuity proof this harness needed on top
//     of that. kind/handle commits are no longer an unconditional failure:
//     each DECISIVE commit is classified baseline_equivalent (a paired
//     no-hint request reaches the SAME engine-deterministic decision
//     outcome and committed subject set) or kind_insensitivity_attested
//     (the all-kinds census itself certified this exact commit) or
//     unjustified (neither -- an immediate failure); see
//     twoTurnCaseResult.InferredClassification's own doc comment.
//     Structurally exempt for subject_anchor: the MCP surface has no
//     explicit anchor field AT ALL (design brief §2.3, "anchors are
//     receipt-only") -- there is no wire path to construct this arm for
//     anchor, which is the invariant holding vacuously, not a gap in this
//     harness.
//   - confirmed_wrong: REDEEM the negative-oracle offer as a receipt --
//     any resulting oracle-WRONG commit fails the run; the arm is valid
//     only if >=1 designated committable negative was actually redeemed
//     (anti-vacuity, design brief v4/sol-r3 #4). Per-member constructors
//     (design-final): kind/handle/window use a SETUP TURN (explicit field
//     -> receipt-bound offer -> redeem, the production upgrade path,
//     Consumer.Surface="mcp"); anchor uses the HARNESS-SEEDED
//     STORED-RESULT constructor -- this harness owns
//     rt.Dependencies.Runtime.InvestigationResults and seeds a stored
//     result whose AnchorOptions carry the annex-designated negative, then
//     redeems against it through the ordinary production redemption path
//     (receipt validation, claimant re-verification, census, gate --
//     nothing about redemption itself is test-only).
//   - mutation:        three non-vacuity probes against a converting case
//     -- (i) remove the confirming receipt -> the refusal must return,
//     (ii) corrupt the receipt id -> veto, (iii) redeem an offer a later
//     result has already superseded -> stale_superseded_offer veto. A run
//     whose mutation arm does not trip every probe it attempts is INVALID,
//     never silently passing (design brief's fails-toward-fine discipline).
//
// Oracle independence (design brief §5 head, sol's refutation adopted): the
// corpus's own expect_kind/expect_id annotations cannot select correct
// ANCHOR or WINDOW offers, and grading against "whatever the engine
// offered" grades the engine with the engine. The instrument therefore
// requires a WITHHELD ORACLE ANNEX authored from the corpus source-of-record
// (never derived from any engine output), referenced by path only --
// ACR_TEST_TWOTURN_ORACLE_ANNEX -- carrying ids/enums/bands, never question
// text (see twoTurnOracleAnnex's own doc comment). DP10 pins the annex as a
// CHRIS-RATIFIED ARTIFACT: SignedOff=false refuses the live run outright
// (requireAnnexSignedOff below), so an unsigned draft can never accidentally
// produce an acceptance number.
//
// Corpus/privacy discipline: mirrors chaos3884_replay_harness_test.go
// exactly -- question/term text stays in process memory only; the report
// artifact carries outcome data (index, member, arm, status, counts) never
// question or offer-label text. See TestTwoTurnCaseResultCarriesNoQuestionText.
//
// ATTESTATION BOUNDARY (CHAOS-4083, chris-approved 2026-08-22): what this
// instrument can and cannot attest.
//
// After three measurement-layer defect cycles (CHAOS-4039, CHAOS-4085's
// discovery chain, CHAOS-4135's shard-54 finding), CHAOS-4083 asked for the
// underlying CLASS to be characterized instead of patching a fourth
// instance: a harness layer that asserts a property the instrument
// structurally cannot measure fails in the dangerous direction -- it looks
// clean, not broken, right up until someone checks the artifact against an
// independent source. The audit below is that characterization, current as
// of the CHAOS-4135 merge (PR #224, `7ae4cab7`); see the CHAOS-4061 retro
// (Linear document, linked from the CHAOS-4061 ticket) for the full
// incident-by-incident history each line below references.
//
// The class has two shapes:
//
//  1. Bit-for-bit equality demanded across independently sampled live-LLM
//     calls. Free-text model output is not deterministic call-to-call with
//     no temperature pinning anywhere in the model runtime, so a classifier
//     built on it is close to unreachable by construction -- CHAOS-4039's
//     own v4 baseline_equivalent (canonicalInterpretationHash /
//     normalizedDecisionFingerprint, hashing InvestigationResult.Status and
//     Interpretation's own free text) measured 0/4 even on cases whose two
//     legs committed the byte-identical subject. FIXED (v5, PR #203,
//     schema v9): baseline_equivalent now compares only engine-deterministic
//     state -- the paired calls' own final decision-stage trace Outcome and
//     Kind+CanonicalID committed-subject SETS (twoTurnCommittedSubjectsEquivalent,
//     twoTurnInferredClassification) -- never a model-authored string. No
//     surviving gate or classification in this file, or in
//     cmd/acr-trial-merge-two-turn's evaluateGates, compares hashed or raw
//     free-text model output; confirmed by direct sweep of both, 2026-08-23.
//
//     RECURRENCE (CHAOS-4139, 2026-08-23, ruling "A'"): shape 1 is not
//     only about free-text hashing -- it is any property whose value a
//     live model call can move, hashed or not. v5's committed-subject SET
//     read off SubjectResolution.Committed on the final InvestigationResult,
//     which is downstream of CHAOS-4085's post-synthesis commit-affirmation
//     gate (chaos4085_commit_affirmation.go): a CommitBasisStatistical
//     commit can be RETRACTED from that exact field based on what an
//     independently-sampled synthesis call did or did not write, strictly
//     after the decision v5 meant to compare. CHAOS-4135's shard-54 rerun
//     (persisted paired ResolutionTraceEvent streams, PR #224) proved it
//     directly: two legs with byte-identical decision-stage trace events
//     (same CommitGate, same CommitBasis=statistical, same committed
//     Subject) still classified "unjustified", because one leg's synthesis
//     call affirmed the commit and the other's did not.
//     request.ExpectedKinds cannot explain the divergence (zero
//     occurrences in internal/contextfabric/genkitruntime, confirmed by
//     direct grep) -- it is exactly the kind of ordinary sampling noise a
//     genuinely engine-deterministic comparison must be immune to, which
//     is what v5 already intended and did not quite reach. FIXED:
//     twoTurnDecisionCommittedSubjects now reads the committed-subject SET
//     straight off each leg's own decision-stage trace event (captured
//     BEFORE synthesis ever runs), never off SubjectResolution.Committed.
//     wrong_commit/false_no_match and every other gate are UNCHANGED and
//     correctly still read the result layer -- they assert product
//     properties of what was actually returned to a caller, which is a
//     different question from "did the two legs reach the same engine
//     decision", the one thing this specific classification exists to
//     answer.
//
//  2. Engine trace fields that are structurally unpopulated or unreachable
//     in the scenarios this harness actually constructs, asserted anyway.
//     Two named instances, in different states:
//
//       - kind_insensitivity_attested (the all-kinds census attests a
//         kind/handle inferred-tier commit): CHAOS-4079 found the probe
//         never evaluates when the harness's own inferred_tier arm injects
//         a wrong-kind hint with ~zero overlap against the pool -- exactly
//         the case the mechanism exists to attest, invisible by
//         construction. FIXED, but NOT by widening what the harness
//         trusts: twoTurnKindAttested (this file) discriminates
//         mode=="narrowed" (the census hypothesis set actually changed and
//         the outcome held -- attestation) from an "observed_" mode (the
//         census was never narrowed, so a sound verdict is
//         necessary-but-not-sufficient -- request.ExpectedKinds still
//         reaches offer ranking and structure stamping this proof never
//         speaks for). Treating an observed_ verdict as attestation would
//         claim more than was proven; the mode gate refuses to.
//         CHAOS-4079 is deliberately pass/fail NEUTRAL for this harness --
//         it only ever ADDS trace-level observability, never loosens a bar.
//       - A handle-member equivalent of kind_insensitivity_attested does
//         NOT exist. CHAOS-4081 (team-lead ruling, path (a)) closed the
//         narrower gap that request.SubjectHandles never reached the shadow
//         evidence round AT ALL: a confirmed explicit subject_handle hint
//         now also reaches RunShadowEvidenceRound
//         (ShadowEvidenceRoundInput.ConfirmedHandle) and populates
//         Attestation.HandleInsensitivityEvaluated/Outcome -- but this is
//         OBSERVATION only, never attestation: those two fields can never
//         widen the round's own decisive Outcome/Kinds (see
//         ConfirmedHandle's own doc comment, chaos3899_evidence_round.go),
//         so this harness still has nothing to read for the handle member
//         that plays kind_insensitivity_attested's role. request.
//         SubjectHandles otherwise still feeds only handleOfferMaterial
//         (offer ranking, resolve.go) and explicit-structure stamping/echo
//         (resolveExplicitStructure, structure.go). OPEN, Backlog, and
//         correctly so: this is a genuine, currently permanent bound on
//         what the handle member can attest, not a bug to strip. Confirmed
//         by direct sweep: no handle-path ATTESTATION function exists
//         anywhere in this file or the merge tool: a handle-member decisive
//         inferred-tier commit can only ever land baseline_equivalent or
//         unjustified, never kind_insensitivity_attested -- the harness
//         already refuses to claim more than CHAOS-4081 leaves it able to
//         prove.
//
// A third shape, added by this audit rather than pre-named in CHAOS-4083:
// a paired-leg divergence with no available EXPLANATION is not evidence of
// anything by itself. Shard 54 of the 2026-08-23 re-measure hit exactly
// this: a correct commit (wrong_commit=false) classified unjustified
// because its paired, hint-free baseline leg diverged, every channel by
// which the hint could structurally have caused that was ruled out from
// the resolution-side trace, and -- until CHAOS-4135 -- there was no
// further trace to consult. Ruled D-plus: the gate stays zero-tolerance
// and unchanged (a real defect would still fail it), and CHAOS-4135 (PR
// #224) closed the actual gap -- paired ResolutionTraceEvent persistence
// for any bar-tripping or non-justified classification, on EITHER leg,
// error paths included -- so a recurrence of this shape now adjudicates
// itself from a persisted artifact instead of requiring a fresh live
// investigation. This is a boundary that MOVED, not one that was always
// open: before CHAOS-4135, "why did the paired legs diverge" was
// unattestable; after it, it usually is.
//
// It did, exactly as designed, the very next time this shape recurred:
// shard 54's SECOND occurrence (2026-08-23 rerun) adjudicated itself from
// the CHAOS-4135 persisted traces, field-by-field, no live call needed --
// and what it found was not a fresh unexplained divergence but the shape-1
// recurrence documented above (CHAOS-4139): the two legs' decision-stage
// traces were byte-identical: same CommitGate, same CommitBasis, same
// committed Subject. The paired-leg comparison itself was reading the
// wrong layer. CHAOS-4139 fixed the classifier, not the engine -- see the
// shape-1 RECURRENCE paragraph above for the mechanism and fix.
//
// A persistence-layer variant of the same general class, caught in
// CHAOS-4135's own review (HIGH severity, codex xhigh, pre-merge): the new
// paired-trace snapshot would have written Subject.Label -- a
// human-readable string that can carry a real work-item or PR title,
// which can itself echo the corpus question's own wording -- straight into
// a persisted file, unscrubbed. Not a measurement-validity bug, but the
// identical root shape: a new field on a new struct silently assumed a
// safety property (corpus-safe by construction) that nothing actually
// enforced there. FIXED before merge: labels are scrubbed at snapshot
// time, not left to whatever downstream reader happened to be careful.
// Recorded here because "can this instrument attest X" and "can this
// instrument accidentally leak Y" are the same discipline applied to
// different properties, and this file is exactly where a new persisted
// field would be added next.
//
// WHAT THIS INSTRUMENT CAN ATTEST, stated positively: engine-deterministic
// decision state only -- committed subject sets (Kind+CanonicalID), taken
// as of CHAOS-4139 from each leg's own decision-stage trace event, never
// from a post-affirmation result field -- closed-vocabulary trace
// Outcomes, gate_reachable, wrong_commit, window coverage, per-leg commit
// affirmation state (hinted/baseline_commit_affirmation, CHAOS-4139), and,
// as of CHAOS-4135, paired-leg divergence on any row a bar or
// classification flags, reconstructable from a persisted trace without a
// fresh live call. It cannot, and does not try to, attest anything about
// model-authored free text, a trace field a given scenario cannot reach
// (handle-path insensitivity today), or a divergence whose cause a
// persisted trace does not cover. wrong_commit/false_no_match and every
// other product-facing gate deliberately stay on the result layer -- they
// exist to assert what a caller actually received, a different question
// from paired-decision equivalence.
//
// AUDIT VERDICT (CHAOS-4083, 2026-08-23, reaffirmed CHAOS-4139
// 2026-08-23): no strip. Every gate in evaluateGates
// (cmd/acr-trial-merge-two-turn/main.go) and every classification
// vocabulary item in this file was swept directly against the class
// above; both named unmeasurable-property examples were already fixed by
// prior work (CHAOS-4039 v5, CHAOS-4079's mode gate) before this audit
// began, CHAOS-4081's gap was already correctly excluded from every gate
// rather than silently trusted, and CHAOS-4135 closed the one new gap
// this audit's own review surfaced. The class predicted its own next
// instance correctly: CHAOS-4139 found that CHAOS-4039 v5's
// "engine-deterministic" comparison was itself reading a
// post-synthesis-affirmation result field, not the decision-stage trace
// it claimed to -- shape 1, one layer deeper than the free-text check v5
// actually closed -- and moved it off that layer. No PR accompanied the
// original CHAOS-4083 audit; CHAOS-4139 is the PR that keeps this
// section's claims true.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/runtime/hosted"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// --- oracle annex: schema, loader, sign-off gate (pure logic) ---

// twoTurnOracleEntry is one case's withheld oracle, ids/enums/bands ONLY --
// authored from the corpus source-of-record, never from any engine output
// (design brief §5 head). PositiveWindowBand/NegativeWindowBand are 3900
// RelativeID enum values (e.g. "trailing_90d"), never free text.
// PositiveKind/NegativeKind double as the SUBJECT KIND for the
// subject_anchor/subject_handle members (typed matching, codex round-1
// finding #5: an untyped match on CanonicalID/Value alone can redeem the
// wrong offer when two candidates share an id/value across kinds) as well
// as the expected_kind member's own value.
type twoTurnOracleEntry struct {
	Index                     int    `json:"index"`
	Member                    string `json:"member"` // expected_kind | subject_anchor | subject_handle | window
	PositiveKind              string `json:"positive_kind,omitempty"`
	PositiveAnchorCanonicalID string `json:"positive_anchor_canonical_id,omitempty"`
	PositiveHandlePatternID   string `json:"positive_handle_pattern_id,omitempty"`
	PositiveHandleValue       string `json:"positive_handle_value,omitempty"`
	PositiveWindowBand        string `json:"positive_window_band,omitempty"`
	NegativeKind              string `json:"negative_kind,omitempty"`
	// NegativeAnchorCanonicalID names the negative anchor's entity; the
	// alias/label TERM needed to compute a redemption-passing hash
	// (codex round-1 finding #2) is looked up LIVE from the identity
	// universe by this canonical_id at seed time (orchestrator ruling,
	// 2026-08-20 -- see buildAnchorTermIndex's own doc comment), never
	// carried on this entry: the signed annex does not supply one, and
	// the term is graph-derived data, not oracle content.
	NegativeAnchorCanonicalID string `json:"negative_anchor_canonical_id,omitempty"`
	NegativeHandlePatternID   string `json:"negative_handle_pattern_id,omitempty"`
	NegativeHandleValue       string `json:"negative_handle_value,omitempty"`
	NegativeWindowBand        string `json:"negative_window_band,omitempty"`
	// NegativeCommittable marks this case's negative as one of DP10's
	// "designated committable negatives" -- the anti-vacuity pin requires
	// at least one NegativeCommittable==true entry PER APPLICABLE MEMBER
	// (codex round-1 finding #4: a global counter lets one member's success
	// mask another member's permanently-invalid negative) to actually be
	// redeemed in a confirmed_wrong run.
	NegativeCommittable bool `json:"negative_committable"`
}

// twoTurnOracleAnnex is this harness's OWN internal, flat shape -- one
// entry per (case, member) -- produced by adaptSignedOracleAnnex below.
// SignedOff is the DP10 mechanism: the artifact -- INCLUDING which
// negatives are designated committable -- requires chris's sign-off before
// any run may treat a measurement against it as the acceptance number
// (design brief matrix DP10: "the sign-off includes the designated
// committable negatives per the §5 anti-vacuity rule"). CorpusSHA256 here
// carries the annex's own sha8 PREFIX (not a full sha256 -- see
// signedOracleAnnex's own doc comment), compared against the loaded
// corpus's hash prefix at the call site.
type twoTurnOracleAnnex struct {
	CorpusSHA256 string               `json:"corpus_sha256"`
	SignedOff    bool                 `json:"signed_off"`
	Entries      []twoTurnOracleEntry `json:"entries"`
	// SignoffStale (CHAOS-4348, team-lead ruling 2026-08-27) mirrors the
	// report's own AnnexSignoffStale -- see that field's doc comment.
	SignoffStale bool `json:"signoff_stale"`
}

// --- adapting the CHRIS-SIGNED DP10 artifact (.remember/trial-results/oracle-annex-v1.json) ---
//
// This is consumption-adaptation of an ALREADY-RATIFIED artifact (chris
// sign-off dated 2026-08-19 10:57, orchestrator-confirmed 2026-08-20):
// the instrument adapts to the signed shape; the artifact is never
// regenerated, edited, or reshaped to fit the instrument.
//
// signedOracleAnnex is the real, on-disk schema -- one entry per CASE
// (keyed by decimal string index, not an array), carrying all FOUR
// members' oracles unconditionally (null/empty where a member does not
// apply to that case, e.g. handle for most cases, anchor for
// existence_probe controls with no true positive).
type signedOracleAnnex struct {
	Provenance struct {
		CorpusPath string `json:"corpus_path"`
		// CorpusSHA8 is an 8-HEX-CHAR PREFIX of the corpus's sha256, not
		// a full digest (matches the design brief's own "sha8 7204a2e6"
		// citation) -- compared against the loaded corpus hash's own
		// first 8 characters, never the full 64.
		CorpusSHA8 string `json:"corpus_sha8"`
		OrgID      string `json:"org_id"`
		// Ratification/Status are STALE top-level strings that predate
		// the nested Signoff block below (Ratification still reads
		// "..._sign_off_pending", Status still reads "DRAFT") -- the
		// OPERATIVE sign-off signal is Signoff.Status=="APPROVED", per
		// team-lead/orchestrator confirmation. Read but never trusted for
		// the gate decision.
		Ratification string `json:"ratification"`
		Status       string `json:"status"`
		Signoff      struct {
			By     string `json:"by"`
			AtPT   string `json:"at_pt"`
			Scope  string `json:"scope"`
			Status string `json:"status"`
			// ApprovedCorpusSHA8 (CHAOS-4348, team-lead ruling 2026-08-27,
			// HIGH #2 on PR #302's own codex review) is stamped ONCE by
			// cmd/acr-corpus-annex-sync the first time it mechanically
			// corrects corpus content -- the corpus_sha8 this signoff was
			// ACTUALLY approved against, distinct from the (possibly
			// since-updated) CorpusSHA8 above. Empty on an annex that has
			// never been mechanically synced. See twoTurnOracleAnnex.
			// SignoffStale's own doc comment for how this is used.
			ApprovedCorpusSHA8 string `json:"approved_corpus_sha8"`
		} `json:"signoff"`
	} `json:"provenance"`
	Cases map[string]signedOracleCase `json:"cases"`
}

type signedOracleCase struct {
	QuestionClass string `json:"question_class"`
	Band          string `json:"band"`
	Oracles       struct {
		Kind struct {
			Positive  string   `json:"positive"`
			Negatives []string `json:"negatives"`
		} `json:"kind"`
		Anchor struct {
			// PositiveKey/Negatives are COMPOSITE "kind:canonical_id"
			// strings (e.g. "work_item:linear:CHAOS-2476" -- the
			// canonical id itself may contain colons, so splitting takes
			// only the FIRST one). PositiveKey is nil for existence_probe
			// cases (no true positive subject exists).
			PositiveKey *string  `json:"positive_key"`
			Negatives   []string `json:"negatives"`
		} `json:"anchor"`
		Window struct {
			PositiveBand string   `json:"positive_band"`
			Negatives    []string `json:"negatives"`
		} `json:"window"`
		Handle struct {
			// Positive/Negatives carry the BARE handle value only (e.g.
			// "CHAOS-2476") -- no pattern_id. handlePatternIDForKind
			// derives it from the member's own subject kind against the
			// closed 3-entry handleGrammarRegistry
			// (graphrank/chaos3899_handle_grammar.go), which is
			// currently a strict 1:1 kind->pattern mapping.
			Positive  *string  `json:"positive"`
			Negatives []string `json:"negatives"`
		} `json:"handle"`
	} `json:"oracles"`
	CommittableNegativeDesignations []struct {
		Member      string `json:"member"` // kind | anchor | window | handle
		Value       string `json:"value"`
		Constructor string `json:"constructor"` // setup_turn | seeded_result
	} `json:"committable_negative_designations"`
}

// signedAnnexMember maps the signed artifact's short member names to the
// closed ContextFabricStructureNeedKind vocabulary this harness uses
// everywhere else.
var signedAnnexMember = map[string]string{
	"kind":   string(contractsv1.ContextFabricStructureNeedExpectedKind),
	"anchor": string(contractsv1.ContextFabricStructureNeedSubjectAnchor),
	"window": string(contractsv1.ContextFabricStructureNeedWindow),
	"handle": string(contractsv1.ContextFabricStructureNeedSubjectHandle),
}

// handlePatternIDForKind derives the closed handle-grammar pattern_id from
// a subject kind, per handleGrammarRegistry's current strict 1:1 mapping
// (graphrank/chaos3899_handle_grammar.go: pull_request_number/
// work_item_ticket_key/ci_run_id, one pattern per kind, no kind sharing
// two patterns today). The signed annex carries bare handle VALUES only,
// never a pattern_id, so this is the one piece of derivation the adapter
// must supply rather than read verbatim.
func handlePatternIDForKind(kind string) (string, bool) {
	switch kind {
	case string(contractsv1.ContextFabricSubjectPullRequest):
		return "pull_request_number", true
	case string(contractsv1.ContextFabricSubjectWorkItem):
		return "work_item_ticket_key", true
	case string(contractsv1.ContextFabricSubjectCIRun):
		return "ci_run_id", true
	default:
		return "", false
	}
}

// splitAnchorKey splits a signed-annex "kind:canonical_id" composite,
// taking only the FIRST colon (a canonical id may itself contain colons,
// e.g. "work_item:linear:CHAOS-2476" -> ("work_item", "linear:CHAOS-2476")).
func splitAnchorKey(key string) (kind, canonicalID string, ok bool) {
	// CHAOS-4157 fix (codex sol/high review, P1): a v2-scheme canonical id
	// ("<kind>.v2:<repo_id>:<enc(external_id)>", identity.Derive's own
	// format) carries its OWN internal colon before the traditional
	// kind/id boundary this function used to assume was always the FIRST
	// one -- a plain SplitN(key, ":", 2) on "work_item.v2:00000000-...
	// :linear%3ACHAOS-3792" returned kind="work_item.v2", not "work_item".
	// Try every registered kind's v2 form FIRST (identity.Segments is the
	// authoritative parser, the exact inverse of Derive) before falling
	// back to the legacy single-colon split every non-v2-scheme kind
	// (repository, project's pre-migration form, ...) still uses.
	for _, reg := range identity.Registry {
		if _, segOK := identity.Segments(reg.Kind, key); segOK {
			return reg.Kind, key, true
		}
	}
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// adaptSignedOracleAnnex flattens the signed artifact's per-CASE,
// all-four-members shape into this harness's own per-(case,member)
// twoTurnOracleEntry list -- one entry per member that has a positive
// and/or negative oracle value in that case. A member with neither (e.g.
// handle for most cases) contributes no entry for that case, matching the
// design brief's own class-conditional Missing rule (a member outside a
// question's frame is unconstructible, not offered).
//
// RESOLVED GAP: the signed annex's anchor oracles carry canonical_id only,
// never the raw alias/label TERM a real identity-universe row would carry.
// Reported to chris/team-lead; orchestrator ruling (2026-08-20): look the
// term up LIVE from the graph at seed time instead of extending the
// chris-signed artifact (a fresh authorship+sign-off cycle) -- the term is
// graph-derived data, not oracle content. See buildAnchorTermIndex's own
// doc comment for the lookup and seedAnchorNegativeResult for where it is
// consumed.
func adaptSignedOracleAnnex(signed signedOracleAnnex) twoTurnOracleAnnex {
	annex := twoTurnOracleAnnex{
		CorpusSHA256: signed.Provenance.CorpusSHA8,
		SignedOff:    signed.Provenance.Signoff.Status == "APPROVED" && strings.TrimSpace(signed.Provenance.Signoff.By) != "",
		// SignoffStale: the signoff names a DIFFERENT corpus than the one
		// currently pinned. Empty ApprovedCorpusSHA8 means "never
		// mechanically synced" -- not stale by this signal (the original
		// signoff still names whatever corpus_sha8 it always did).
		SignoffStale: signed.Provenance.Signoff.ApprovedCorpusSHA8 != "" &&
			signed.Provenance.Signoff.ApprovedCorpusSHA8 != signed.Provenance.CorpusSHA8,
	}

	indices := make([]int, 0, len(signed.Cases))
	byIndex := map[int]string{}
	for key := range signed.Cases {
		n, err := strconv.Atoi(key)
		if err != nil {
			continue // non-numeric case key: skip rather than crash on an unexpected shape
		}
		indices = append(indices, n)
		byIndex[n] = key
	}
	sort.Ints(indices)

	for _, index := range indices {
		c := signed.Cases[byIndex[index]]
		committable := map[string]struct{ value, constructor string }{}
		for _, d := range c.CommittableNegativeDesignations {
			committable[d.Member] = struct{ value, constructor string }{d.Value, d.Constructor}
		}

		// expected_kind
		if c.Oracles.Kind.Positive != "" || len(c.Oracles.Kind.Negatives) > 0 {
			entry := twoTurnOracleEntry{Index: index, Member: signedAnnexMember["kind"], PositiveKind: c.Oracles.Kind.Positive}
			if neg, ok := committable["kind"]; ok {
				entry.NegativeKind = neg.value
				entry.NegativeCommittable = true
			} else if len(c.Oracles.Kind.Negatives) > 0 {
				entry.NegativeKind = c.Oracles.Kind.Negatives[0]
			}
			annex.Entries = append(annex.Entries, entry)
		}

		// subject_anchor. CanonicalID keeps the annex's FULL "kind:id"
		// composite VERBATIM (live-run finding, orchestrator ruling
		// 2026-08-20): graphrank.IdentityRow.CanonicalID -- what
		// VerifyAnchorClaimantUnique itself compares against at redemption
		// -- carries this SAME full composite (confirmed live: a case's
		// negative "repository:d29d160a-..." matched an identity-universe
		// row whose own CanonicalID was that exact string), never a bare
		// id. A prior version of this adapter stripped the kind prefix via
		// splitAnchorKey before storing CanonicalID, which made every
		// anchor negative unmatchable regardless of whether a usable term
		// existed -- splitAnchorKey now runs ONLY to derive the separate
		// Kind field (typed offer matching), never to reshape CanonicalID.
		if c.Oracles.Anchor.PositiveKey != nil || len(c.Oracles.Anchor.Negatives) > 0 {
			entry := twoTurnOracleEntry{Index: index, Member: signedAnnexMember["anchor"]}
			if c.Oracles.Anchor.PositiveKey != nil {
				entry.PositiveAnchorCanonicalID = *c.Oracles.Anchor.PositiveKey
				if kind, _, ok := splitAnchorKey(*c.Oracles.Anchor.PositiveKey); ok {
					entry.PositiveKind = kind
				}
			}
			negValue := ""
			if neg, ok := committable["anchor"]; ok {
				negValue = neg.value
				entry.NegativeCommittable = true
			} else if len(c.Oracles.Anchor.Negatives) > 0 {
				negValue = c.Oracles.Anchor.Negatives[0]
			}
			if negValue != "" {
				entry.NegativeAnchorCanonicalID = negValue
				if kind, _, ok := splitAnchorKey(negValue); ok {
					entry.NegativeKind = kind
				}
			}
			// The alias/label term needed to seed a redeemable negative is
			// resolved LIVE at run time (buildAnchorTermIndex), not carried
			// on this entry -- see this function's own doc comment.
			annex.Entries = append(annex.Entries, entry)
		}

		// window
		if c.Oracles.Window.PositiveBand != "" || len(c.Oracles.Window.Negatives) > 0 {
			entry := twoTurnOracleEntry{Index: index, Member: signedAnnexMember["window"], PositiveWindowBand: c.Oracles.Window.PositiveBand}
			if neg, ok := committable["window"]; ok {
				entry.NegativeWindowBand = neg.value
				entry.NegativeCommittable = true
			} else if len(c.Oracles.Window.Negatives) > 0 {
				entry.NegativeWindowBand = c.Oracles.Window.Negatives[0]
			}
			annex.Entries = append(annex.Entries, entry)
		}

		// subject_handle: kind is derived from the SAME case's own
		// positive expected_kind (the frozen corpus is single-subject-term
		// dominant -- design brief §0 -- so a case's handle, when present,
		// is scoped to the case's own primary kind). Live-run finding,
		// orchestrator ruling 2026-08-20: entry.PositiveKind/NegativeKind
		// were never set here, leaving Kind="" on every handle request --
		// ContextFabricRequestedHandle.Validate rejects an empty Kind
		// before the request reaches production logic at all, and the
		// SAME empty Kind broke the positive arm's typed offer match
		// (oracleOfferQuery.kind) too. Both are now set from the case's
		// own positive kind, matching handlePatternIDForKind's own
		// assumption exactly.
		if (c.Oracles.Handle.Positive != nil && *c.Oracles.Handle.Positive != "") || len(c.Oracles.Handle.Negatives) > 0 {
			entry := twoTurnOracleEntry{
				Index: index, Member: signedAnnexMember["handle"],
				PositiveKind: c.Oracles.Kind.Positive, NegativeKind: c.Oracles.Kind.Positive,
			}
			if patternID, ok := handlePatternIDForKind(c.Oracles.Kind.Positive); ok {
				entry.PositiveHandlePatternID = patternID
				entry.NegativeHandlePatternID = patternID
			}
			if c.Oracles.Handle.Positive != nil {
				entry.PositiveHandleValue = *c.Oracles.Handle.Positive
			}
			if neg, ok := committable["handle"]; ok {
				entry.NegativeHandleValue = neg.value
				entry.NegativeCommittable = true
			} else if len(c.Oracles.Handle.Negatives) > 0 {
				entry.NegativeHandleValue = c.Oracles.Handle.Negatives[0]
			}
			annex.Entries = append(annex.Entries, entry)
		}
	}
	return annex
}

var twoTurnStructureMembers = map[string]bool{
	string(contractsv1.ContextFabricStructureNeedExpectedKind):  true,
	string(contractsv1.ContextFabricStructureNeedSubjectAnchor): true,
	string(contractsv1.ContextFabricStructureNeedSubjectHandle): true,
	string(contractsv1.ContextFabricStructureNeedWindow):        true,
}

// validateTwoTurnOracleAnnex checks the closed-vocabulary/shape rules a
// malformed annex would otherwise let slide silently into a run.
//
// CHAOS-4348 measurement-layer fix (codex adversarial review, HIGH,
// confirmed): the annex's own env var is ACR_TEST_TWOTURN_ORACLE_ANNEX, and
// both scripts/trial/run-two-turn.sh and run-two-turn-parallel.sh invoke
// `go test -run TestChaos3742TwoTurnConfirmationReplay` -- a standalone
// TestChaos4348OracleAnnexAnchorIDsMatchLiveIdentityScheme test (this
// file's first version of this fix) reading a DIFFERENT env var name
// (ORACLE_ANNEX) would never run for a single real trial invocation,
// sequential or parallel, regardless of whether the annex was actually
// stale. The scheme check below is wired in HERE instead -- inside the
// SAME validateTwoTurnOracleAnnex loadTwoTurnOracleAnnex already calls
// unconditionally, on every real load, before a single case executes --
// so a stale-scheme anchor id fails the run outright, not merely a
// counter a caller could fail to check.
func validateTwoTurnOracleAnnex(annex twoTurnOracleAnnex) error {
	if annex.CorpusSHA256 == "" {
		return fmt.Errorf("oracle annex: corpus_sha256 is required")
	}
	for i, entry := range annex.Entries {
		if !twoTurnStructureMembers[entry.Member] {
			return fmt.Errorf("oracle annex entry %d: member %q is not a closed StructureNeeds member", i, entry.Member)
		}
		if entry.Member != string(contractsv1.ContextFabricStructureNeedSubjectAnchor) {
			continue
		}
		if id := entry.PositiveAnchorCanonicalID; id != "" && !chaos4348KnownResidualStaleAnchorIDs[id] && oracleIDSchemeMismatch(entry.PositiveKind, id) {
			return fmt.Errorf("oracle annex entry %d (case %d, positive anchor): id %q does not match kind %q's live identity scheme (want prefix %q) -- run cmd/acr-annex-regen-project-ids (CHAOS-4348)",
				i, entry.Index, id, entry.PositiveKind, chaos4348ExpectedIDSchemePrefix(entry.PositiveKind))
		}
		if id := entry.NegativeAnchorCanonicalID; id != "" && !chaos4348KnownResidualStaleAnchorIDs[id] && oracleIDSchemeMismatch(entry.NegativeKind, id) {
			return fmt.Errorf("oracle annex entry %d (case %d, negative anchor): id %q does not match kind %q's live identity scheme (want prefix %q) -- run cmd/acr-annex-regen-project-ids (CHAOS-4348)",
				i, entry.Index, id, entry.NegativeKind, chaos4348ExpectedIDSchemePrefix(entry.NegativeKind))
		}
	}
	return nil
}

// requireAnnexSignedOff is the DP10 gate as a pure, directly-testable
// function: refuses an unsigned (draft) annex outright rather than letting
// a run silently produce a number nobody ratified.
func requireAnnexSignedOff(annex twoTurnOracleAnnex) error {
	if !annex.SignedOff {
		return fmt.Errorf("oracle annex is not signed off (DP10): this is a DRAFT artifact -- chris must ratify the annex, INCLUDING which negatives are designated committable, before any two-turn run may count as the acceptance measurement")
	}
	return nil
}

// loadTwoTurnOracleAnnex reads the CHRIS-SIGNED DP10 artifact (its real,
// on-disk schema -- signedOracleAnnex) and adapts it into this harness's
// own internal shape. The artifact itself is read-only input: never
// rewritten, reshaped, or regenerated by this loader.
func loadTwoTurnOracleAnnex(t *testing.T, path string) twoTurnOracleAnnex {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read oracle annex %s: %v", path, err)
	}
	var signed signedOracleAnnex
	if err := json.Unmarshal(data, &signed); err != nil {
		t.Fatalf("decode oracle annex %s: %v", path, err)
	}
	annex := adaptSignedOracleAnnex(signed)
	if err := validateTwoTurnOracleAnnex(annex); err != nil {
		t.Fatalf("oracle annex %s: %v", path, err)
	}
	return annex
}

// --- pure logic: offer selection, commit classification (unit-testable, no live infra) ---

// oracleOfferQuery is the typed match key selectOracleOffer scans for.
// Kind is checked for anchor/handle (codex round-1 finding #5: matching on
// CanonicalID/Value alone can redeem the wrong typed offer when two
// candidates share an id/value across different subject kinds).
type oracleOfferQuery struct {
	kind            string
	anchorID        string
	handlePatternID string
	handleValue     string
	windowBand      string
}

// selectOracleOffer scans result for the offer matching q for entry.Member
// (kind/anchor/handle/window), using ONLY the oracle's own typed fields as
// the match key (never "the offer the engine ranked first" -- design brief
// §5 head's oracle-independence rule). A case where no offer matches is
// scored offer_miss (found=false), never silently skipped.
//
// CHAOS-4121: window offers now read result.StructureNeeds.WindowOptions --
// the SAME unified surface kind/anchor/handle already read through above --
// instead of the legacy result.WindowClarification.Options this oracle read
// exclusively through the post-4117/4118/4120 re-measure (deliberately, per
// this file's own prior form -- see git blame -- to keep that re-measure's
// before/after comparable while StructureNeeds.WindowOptions was still new).
// window.go:1315 assigns StructureNeeds.WindowOptions the
// WindowClarification.Options SLICE VALUE ITSELF (never a copy), built once
// by the SAME composeWindowClarification call both fields share, so this
// switch is provably a no-op against every result this oracle has ever
// scored. twoTurnAssertWindowSurfacesAgree below -- called by every live
// caller of this window case -- Fatalf's, naming the case index, the
// instant a live result's two surfaces ever stop agreeing, so a future
// divergence becomes a loud measurement-integrity failure instead of a
// silently-chosen surface. Applied only once the post-4117/4118/4120
// re-measure baseline landed (cf-rulings.md's "Oracle freeze" ruling). No
// ReportSchemaVersion bump: the artifact's wire shape and every measured
// value are unchanged (team-lead ruling, CHAOS-4121 close-out) -- a bump
// marks a meaning/shape change, and this one provably has neither.
func selectOracleOffer(result contractsv1.ContextFabricInvestigationResult, member string, q oracleOfferQuery) (receiptID string, found bool) {
	switch member {
	case string(contractsv1.ContextFabricStructureNeedExpectedKind):
		if result.StructureNeeds == nil {
			return "", false
		}
		for _, opt := range result.StructureNeeds.KindOptions {
			if string(opt.Kind) == q.kind {
				return opt.ReceiptID, true
			}
		}
	case string(contractsv1.ContextFabricStructureNeedSubjectAnchor):
		if result.StructureNeeds == nil {
			return "", false
		}
		for _, opt := range result.StructureNeeds.AnchorOptions {
			if opt.CanonicalID == q.anchorID && string(opt.Kind) == q.kind {
				return opt.ReceiptID, true
			}
		}
	case string(contractsv1.ContextFabricStructureNeedSubjectHandle):
		if result.StructureNeeds == nil {
			return "", false
		}
		for _, opt := range result.StructureNeeds.HandleOptions {
			if opt.Value == q.handleValue && opt.PatternID == q.handlePatternID && string(opt.Kind) == q.kind {
				return opt.ReceiptID, true
			}
		}
	case string(contractsv1.ContextFabricStructureNeedWindow):
		if result.StructureNeeds == nil {
			return "", false
		}
		for _, opt := range result.StructureNeeds.WindowOptions {
			if string(opt.RelativeID) == q.windowBand {
				return opt.ReceiptID, true
			}
		}
	}
	return "", false
}

// twoTurnAssertWindowSurfacesAgree is CHAOS-4121's own measurement-
// discipline guard, live for as long as result.WindowClarification (CHAOS-
// 3900 W1) exists alongside its CHAOS-4118 mirror
// result.StructureNeeds.WindowOptions: selectOracleOffer's window case
// above now reads ONLY the unified StructureNeeds surface, on the strength
// of window.go:1315's own "same slice value, never a copy" guarantee. This
// Fatalf's, naming ONLY the case index (never any oracle/question content),
// the instant that guarantee stops holding for a LIVE result -- proving the
// no-op claim on every case actually measured, rather than trusting the
// static code-read once and letting a future window.go change silently
// pick one surface over the other with nothing to notice.
//
// Deliberately reflect.DeepEqual over the raw slices, not a semantic
// "same set of bands" comparison: the two fields are supposed to be the
// IDENTICAL value (same slice, same order, same ReceiptIDs) by
// construction, so anything looser would mask exactly the drift this guard
// exists to catch.
func twoTurnAssertWindowSurfacesAgree(t *testing.T, index int, result contractsv1.ContextFabricInvestigationResult) {
	t.Helper()
	if !twoTurnWindowSurfacesAgree(result) {
		t.Fatalf("case index %d: WindowClarification.Options and StructureNeeds.WindowOptions diverged -- CHAOS-4121's own parity guarantee no longer holds for this live result; selectOracleOffer's window case reads StructureNeeds.WindowOptions only, so this divergence would otherwise silently change what window offer_miss counts", index)
	}
}

// twoTurnWindowSurfacesAgree is the pure predicate
// twoTurnAssertWindowSurfacesAgree Fatal's on -- split out so it has a unit-
// test surface that never has to trigger a real t.Fatalf (a genuinely
// FAILING subtest would cascade its parent test, and ultimately this whole
// package's `go test` exit code, to fail even though catching the
// divergence IS the correct, intended behavior being tested).
func twoTurnWindowSurfacesAgree(result contractsv1.ContextFabricInvestigationResult) bool {
	var legacy, unified []contractsv1.ContextFabricWindowOption
	if result.WindowClarification != nil {
		legacy = result.WindowClarification.Options
	}
	if result.StructureNeeds != nil {
		unified = result.StructureNeeds.WindowOptions
	}
	return reflect.DeepEqual(legacy, unified)
}

// positiveQuery/negativeQuery build the typed oracleOfferQuery for this
// entry's own member from its positive/negative oracle fields.
func (entry twoTurnOracleEntry) positiveQuery() oracleOfferQuery {
	return oracleOfferQuery{
		kind: entry.PositiveKind, anchorID: entry.PositiveAnchorCanonicalID,
		handlePatternID: entry.PositiveHandlePatternID, handleValue: entry.PositiveHandleValue,
		windowBand: entry.PositiveWindowBand,
	}
}

// hasConstructiblePositiveOffer (CHAOS-4165, codex review round 2, P2,
// confirmed) reports whether entry carries a positive oracle COMPLETE
// enough for selectOracleOffer to ever possibly match it -- i.e. every
// field that member's own switch case in selectOracleOffer actually
// compares, mirrored here member-by-member rather than a single whole-
// struct zero-value test. That looser test (entry.positiveQuery() !=
// oracleOfferQuery{}) is NOT enough: adaptSignedOracleAnnex can leave a
// NEGATIVE-ONLY subject_handle entry with PositiveKind and
// PositiveHandlePatternID both non-empty (derived unconditionally from the
// case's own kind) while PositiveHandleValue stays empty -- a query
// selectOracleOffer's own three-way handle match (Value/PatternID/Kind)
// can never satisfy, exactly like a genuinely all-empty entry, but the
// whole-struct test would have missed it since two of the three fields
// were non-empty. window is never eligible for the mutation arm at all
// (see this function's own caller), so it is not handled here.
func (entry twoTurnOracleEntry) hasConstructiblePositiveOffer() bool {
	switch entry.Member {
	case string(contractsv1.ContextFabricStructureNeedExpectedKind):
		return entry.PositiveKind != ""
	case string(contractsv1.ContextFabricStructureNeedSubjectAnchor):
		return entry.PositiveKind != "" && entry.PositiveAnchorCanonicalID != ""
	case string(contractsv1.ContextFabricStructureNeedSubjectHandle):
		return entry.PositiveKind != "" && entry.PositiveHandlePatternID != "" && entry.PositiveHandleValue != ""
	default:
		return false
	}
}

func (entry twoTurnOracleEntry) negativeQuery() oracleOfferQuery {
	return oracleOfferQuery{
		kind: entry.NegativeKind, anchorID: entry.NegativeAnchorCanonicalID,
		handlePatternID: entry.NegativeHandlePatternID, handleValue: entry.NegativeHandleValue,
		windowBand: entry.NegativeWindowBand,
	}
}

// twoTurnTraceCapture is an in-process graphrank.ResolutionTracer
// (Options.ResolutionTracer), mirroring chaos3884_replay_harness_test.go's
// own replayTraceCapture exactly. The harness resets it immediately before
// a call it wants to observe and reads it immediately after -- the SAME
// sequential single-caller discipline replayTraceCapture uses, valid
// because this harness runs its calls one at a time (never concurrently).
//
// CHAOS-4103: also an in-process contextfabric.EngineTelemetry
// (Options.EngineTelemetry), embedding SlogEngineTelemetry so every method
// but one still emits the SAME slog WARN lines production does (an
// operator's grep-the-logs path is unchanged), and overriding
// RecordSynthesisStatusOverride below to additionally capture the outcome
// for this call -- reset/read under the identical single-caller discipline
// as the trace events above (see twoTurnStampDecision, the one place both
// get read into a twoTurnCaseResult).
type twoTurnTraceCapture struct {
	events []graphrank.ResolutionTraceEvent
	contextfabric.SlogEngineTelemetry
	synthesisOverride *contextfabric.SynthesisStatusOverrideOutcome
	// windowCanonicalization (CHAOS-4120) is the LAST captured
	// EngineTelemetry.RecordWindowCanonicalization outcome for this call --
	// "" means never recorded, distinct from every real outcome
	// (WindowCanonicalizationNone's own value is the non-empty "none").
	// "Last" matters, not merely "the only one": engine.go's own pre-Save
	// call (windowConfirmationRequiredResult's gate path, or the decisive
	// path after Interpret) fires unconditionally, but
	// recordWindowSupersessionRaceTelemetry (window.go, CHAOS-4003) can
	// issue a SECOND, corrective call after Save discovers a receipt-
	// redeemed window lost its atomic claim race -- codex review round 2
	// (P3, confirmed): an earlier version of this comment claimed the two
	// always coincide, which is true for turn 1's own call specifically
	// (it carries no window receipt to redeem, so that correction path is
	// unreachable there) but was stated as a general fact about the method,
	// which it is not. Reading the LAST value is what makes this correct
	// regardless -- the same "last wins" discipline finalDecisionEvents
	// already needs, per subject, for a stalled resolution's re-decision.
	windowCanonicalization contextfabric.WindowCanonicalizationOutcome
	// slogTee (CHAOS-4155 Phase 2, codex R1 High, confirmed): nil in every
	// existing use of this type (all the in-process unit tests below
	// construct twoTurnTraceCapture directly with events pre-populated,
	// never through hosted.Open) -- Trace()'s forward is a no-op for all
	// of them, unchanged behavior. Only the live trial-harness
	// construction below sets it. Without this, installing
	// twoTurnTraceCapture as Options.ResolutionTracer REPLACES
	// graphrank.NewSlogResolutionTracer entirely (open.go's own doc
	// comment: "an in-process caller can capture trace events directly
	// instead of only reaching them by parsing Debug-level slog output"),
	// so DebugContext-level stages -- confirmed_kind_scope and the
	// CHAOS-4155 vector census fields riding on it -- never reached slog
	// at all, regardless of ACR_LOG_LEVEL/ACR_TEST_TRIAL_LOG_LEVEL. This
	// tee restores that path without giving up the in-process capture the
	// two-turn report itself depends on.
	slogTee graphrank.ResolutionTracer
}

func (c *twoTurnTraceCapture) Trace(event graphrank.ResolutionTraceEvent) {
	c.events = append(c.events, event)
	if c.slogTee != nil {
		c.slogTee.Trace(event)
	}
}

// RecordSynthesisStatusOverride overrides SlogEngineTelemetry's own method
// of the same name (embedding promotes every other EngineTelemetry method
// unchanged) -- captures the outcome for reset()/reset-scoped reading below,
// AND still emits the production WARN line via the embedded sink, so
// nothing already grepping for it loses that signal.
func (c *twoTurnTraceCapture) RecordSynthesisStatusOverride(ctx context.Context, principal storage.Principal, outcome contextfabric.SynthesisStatusOverrideOutcome) {
	c.synthesisOverride = &outcome
	c.SlogEngineTelemetry.RecordSynthesisStatusOverride(ctx, principal, outcome)
}

// RecordWindowCanonicalization overrides SlogEngineTelemetry's own method
// of the same name -- CHAOS-4120's regime stamp reads the engine's own
// decision at its SOURCE (this telemetry stream already existed,
// CHAOS-3900 W1) rather than re-deriving it from response shape
// (WindowClarification presence etc.), which is exactly what the
// question-results decomposition could not do reliably: 4/212 rows were
// unplaceable, and CHAOS-4118 now makes the class-default gate ALSO
// compose a window-only StructureNeeds (Missing=[window], WindowOptions),
// which makes an output-shape inference (StructureNeeds/WindowClarification
// presence) ambiguous in a way this direct capture never is.
func (c *twoTurnTraceCapture) RecordWindowCanonicalization(ctx context.Context, principal storage.Principal, outcome contextfabric.WindowCanonicalizationOutcome) {
	c.windowCanonicalization = outcome
	c.SlogEngineTelemetry.RecordWindowCanonicalization(ctx, principal, outcome)
}

func (c *twoTurnTraceCapture) reset() {
	c.events = nil
	c.synthesisOverride = nil
	c.windowCanonicalization = ""
}

// censusRan reports whether CensusFunc was actually INVOKED (the per-kind
// census read genuinely ran) during the captured call --
// ControlsWitnessedNoMatchCensusBacked's own "WHERE A CENSUS RAN" half
// (design brief line 960). Deliberately checks for "evidence_probe", NOT
// "evidence_round" (codex xhigh review round 2, confirmed): every one of
// RunShadowEvidenceRound's early-return branches (non-current axis, scoped
// visibility, multi-handle, no discriminators, unregistered census kind,
// zero census kinds -- chaos3899_evidence_round.go:340-403) ALSO traces an
// "evidence_round" event via its own emit() wrapper, so that stage alone
// proves only that the round was ENTERED (resolve.go:1086's outer gate
// passed), never that CensusFunc was called. "evidence_probe" fires ONLY
// inside the per-kind loop that actually calls input.CensusFunc
// (chaos3899_evidence_round.go:426-451, appended to kindAttestations and
// traced by emit() at line ~328) -- a kind the loop skips via
// NonCensusedSurvivor's own `continue` never reaches CensusFunc and never
// produces one.
func (c *twoTurnTraceCapture) censusRan() bool {
	for _, e := range c.events {
		if e.Stage == "evidence_probe" {
			return true
		}
	}
	return false
}

// kindInsensitivityResult reports whether the kind-insensitivity proof was
// actually CONSULTED during the captured call and, when it was, its own
// closed-vocabulary verdict -- CHAOS-4039's v4 measurement contract
// (sol-max ruling 2026-08-20), replacing the prior singleSatisfierVerified
// proxy (a generic evidence_round/would_commit check the ruling found
// insufficient: it cannot distinguish "the proof ran and certified this
// exact commit" from "the round reached would_commit for an unrelated
// reason, or never ran the proof at all"). Read directly off
// ResolutionTraceEvent's own ShadowKindInsensitivityEvaluated/
// ShadowKindInsensitivityOutcome fields (resolve.go).
//
// CHAOS-4079 additionally returns ShadowKindInsensitivityMode, because
// those two fields are no longer produced by one mechanism (codex xhigh
// review round 3 follow-up: this comment used to say kindInsensitivityProof
// itself always ran, which stopped being true). Under mode=="narrowed" they
// come from RunShadowEvidenceRound's original PreNarrowingExplicitKinds-gated
// branch, where kindInsensitivityProof (chaos3900_structure_offers.go)
// genuinely re-censuses the pre-narrowing kind set. Under an "observed_"
// mode the census was never narrowed, so the identical verdict is DERIVED
// from the round's own already-collected census results with no second read.
// "evaluated" alone therefore no longer implies the verdict attests
// anything; callers MUST discriminate on mode. See twoTurnKindAttested, the
// only place this file turns a verdict into a classification.
func (c *twoTurnTraceCapture) kindInsensitivityResult() (evaluated bool, outcome, mode string) {
	for _, e := range c.events {
		if e.Stage == "evidence_round" && e.ShadowKindInsensitivityEvaluated {
			return true, e.ShadowKindInsensitivityOutcome, e.ShadowKindInsensitivityMode
		}
	}
	return false, "", ""
}

// twoTurnKindAttested is CHAOS-4039's kind_insensitivity_attested
// precondition, extracted (CHAOS-4079) so the mode gate below has a unit
// test surface independent of runTwoTurnInferredTierArm's full
// investigator/window-precondition machinery.
//
// THE MODE GATE (team-lead ruling 2026-08-22, CHAOS-4079 phase 1): a
// commit_sound verdict attests kind-insensitivity ONLY under
// mode=="narrowed" -- the census hypothesis set actually changed and the
// outcome held across the change. CHAOS-4079 made the probe evaluable for
// the "observed_" modes as well (a wrong-kind hint disjoint from the pool,
// which previously could not be observed at all), but there the census was
// never narrowed, so a sound verdict is necessary-but-NOT-sufficient
// evidence that the hint had no influence: request.ExpectedKinds still
// reaches explicit-structure member stamping (contextfabric/structure.go)
// and kind-offer ranking (graphrank's kindOfferMaterial), neither of which
// this proof speaks for. Treating an observed_ verdict as attestation
// would claim more than was proven AND would silently convert this run's
// own InferredUnjustifiedCount==0 pass condition into a weaker bar --
// exactly the confidently-wrong-measurement failure the PairInvalid rule
// above already exists to prevent.
//
// Consequence, stated so a future reader does not have to rediscover it:
// CHAOS-4079 is pass/fail NEUTRAL for this harness by construction. A run
// that would fail on InferredUnjustifiedCount before it still fails after;
// the observed_ rows gain trace-level observability
// (ShadowKindInsensitivity* on the row) and nothing else.
// TestTwoTurnKindAttestedRequiresNarrowedMode pins it.
func twoTurnKindAttested(trace *twoTurnTraceCapture, hintedCommitted []contractsv1.ContextFabricSubjectRef) bool {
	if trace == nil {
		return false
	}
	evaluated, outcome, mode := trace.kindInsensitivityResult()
	if !evaluated || outcome != "commit_sound" || mode != "narrowed" {
		return false
	}
	return trace.evidenceCensusCommitted(hintedCommitted)
}

// evidenceCensusCommitted reports whether a decision-stage event traced
// CommitGate=="evidence_census" (CHAOS-3896 Slice C's own commit-path
// label) for a Subject matching one of committed -- kind_insensitivity_attested's
// own "attested satisfier == committed subject" half (CHAOS-4039). Compares
// Kind+CanonicalID only: Label is presentation text, never compared or
// traced (this file's own no-question-text discipline,
// TestTwoTurnCaseResultCarriesNoQuestionText).
func (c *twoTurnTraceCapture) evidenceCensusCommitted(committed []contractsv1.ContextFabricSubjectRef) bool {
	for _, e := range c.events {
		if e.Stage != "decision" || e.CommitGate != "evidence_census" {
			continue
		}
		for _, subj := range committed {
			if e.Subject.Kind == subj.Kind && e.Subject.CanonicalID == subj.CanonicalID {
				return true
			}
		}
	}
	return false
}

// finalDecisionEvents returns, for THIS call's own captured trace stream,
// the LAST "decision"-stage event PER DISTINCT SUBJECT KEY (Kind+
// CanonicalID), in first-seen order -- CHAOS-4039's own
// twoTurnInferredClassification input.
//
// "Last per subject" generalizes the pre-CHAOS-4096 "last event, period"
// rule (finalDecisionEvent, singular) to a world where a resolution can now
// emit MORE than one decision event: a genuinely multi-subject commit (the
// pre_committed_exact_hint loop, resolution.go) or the caller-hint short
// circuit (resolve.go) both emit one event PER committed subject, and those
// are DIFFERENT subjects, not a re-decision of the same one -- collapsing
// them to "the last event" as before would silently drop every committed
// subject but one.
//
// codex R1 (Low, confirmed): the stalled-then-re-decided case this rule
// already existed for (TestResolveSubjects_EvidenceCensusCommitsAStalledCandidate,
// graphrank) does NOT collapse under this key scheme, and this comment
// previously claimed it did -- the FIRST (stalled) event is an ambiguous/
// no_commit outcome, which never carries a Subject at all (resolution.go's
// switch), so it lands under the zero-value key while the SECOND
// (census-enriched) event carries the real committed Subject and lands
// under a DIFFERENT key. Both survive in the returned slice. That is still
// CORRECT, not a regression: every existing reader either takes the LAST
// element (twoTurnStampDecision, twoTurnCaptureTurn1Facts, twoTurnLegOutcome
// -- always the census-enriched, actually-served decision) or unions only
// the "committed"-outcome events' own Subjects (twoTurnUnionCommittedSubjects
// -- the stalled event contributes nothing, Outcome!="committed"), so the
// non-collapse is invisible to every consumer. TWO subjectless events in a
// row (e.g. ambiguous then no_commit, neither ever committing) DO still
// collapse to the last one, sharing the same zero-value key -- that
// remains the pre-CHAOS-4096 behavior unchanged.
//
// Empty (never nil-vs-empty distinguished; callers already treat both that
// way) when this leg captured no decision-stage event at all.
func (c *twoTurnTraceCapture) finalDecisionEvents() []graphrank.ResolutionTraceEvent {
	order := make([]string, 0, 4)
	byKey := make(map[string]graphrank.ResolutionTraceEvent, 4)
	for _, e := range c.events {
		if e.Stage != "decision" {
			continue
		}
		key := string(e.Subject.Kind) + "|" + e.Subject.CanonicalID
		if _, seen := byKey[key]; !seen {
			order = append(order, key)
		}
		byKey[key] = e
	}
	events := make([]graphrank.ResolutionTraceEvent, 0, len(order))
	for _, key := range order {
		events = append(events, byKey[key])
	}
	return events
}

// snapshot (CHAOS-4135) returns a COPY of every event captured for the call
// most recently made against this tracer -- the full stream
// finalDecisionEvents/kindCoverageFloorEvent/passTruncation each already read
// a narrow projection of, now preserved whole. A copy, not the live slice:
// callers stash this onto a twoTurnCaseResult that outlives the next
// reset()/append() this same *twoTurnTraceCapture will do for the NEXT call
// in the same arm (runTwoTurnInferredTierArm's baseline-then-hinted pair,
// in particular) -- returning c.events directly would let that later call's
// append silently mutate an earlier snapshot still referenced by a result
// row already returned. nil in, nil out (an empty snapshot serializes to
// nothing under omitempty, matching every other optional field on
// twoTurnCaseResult).
//
// Also strips Subject.Label off every event (codex xhigh review, HIGH,
// confirmed): ResolutionTraceEvent's own doc comment (resolve.go) claims
// its Subject field carries "kind+canonical_id... never a label or matched
// term", but contextfabric.SubjectRef.Label has no such enforcement at the
// type level -- NodeCandidate populates it from the graph's real subject
// labels (PR/issue/repo titles), so a corroboration- or decision-stage
// event's Subject.Label DOES carry real content in production. That gap
// was harmless while ResolutionTraceEvent only ever reached
// SlogResolutionTracer's Debug-level log line; THIS function is the first
// place anything from that stream gets written to a FILE this harness
// persists as a shared artifact, so the label must be scrubbed here rather
// than trusted from upstream. Kind+CanonicalID (the graph's own stable,
// non-presentational identifier) are untouched -- only the free-text
// display label is cleared.
func (c *twoTurnTraceCapture) snapshot() []graphrank.ResolutionTraceEvent {
	if len(c.events) == 0 {
		return nil
	}
	events := append([]graphrank.ResolutionTraceEvent(nil), c.events...)
	for i := range events {
		events[i].Subject.Label = ""
	}
	return events
}

// kindCoverageFloorEvent returns the LAST captured "kind_coverage_floor"
// event (CHAOS-4086/CHAOS-4038), the same last-wins-per-key rule
// finalDecisionEvents applies and for the same reason: ResolveSubjects can resolve twice (the
// evidence-census re-resolve), and the run that produced the served answer is
// the later one.
//
// A SEPARATE reader from finalDecisionEvents because it is a separate stage --
// see ResolutionTraceEvent's own doc comment for why the floor's state is
// deliberately not carried on the decision event.
func (c *twoTurnTraceCapture) kindCoverageFloorEvent() (event graphrank.ResolutionTraceEvent, ok bool) {
	for _, e := range c.events {
		if e.Stage == "kind_coverage_floor" {
			event, ok = e, true
		}
	}
	return event, ok
}

// confirmedKindRescueEvent (CHAOS-4038 v18) is kindCoverageFloorEvent's own
// twin for the confirmed_kind_rescue stage (CHAOS-4132) -- same last-event-
// wins rule, same rationale (a separate stage from the decision event, read
// by a dedicated function rather than folded into finalDecisionEvents).
func (c *twoTurnTraceCapture) confirmedKindRescueEvent() (event graphrank.ResolutionTraceEvent, ok bool) {
	for _, e := range c.events {
		if e.Stage == "confirmed_kind_rescue" {
			event, ok = e, true
		}
	}
	return event, ok
}

// kindOfferEvent (CHAOS-4012 v20) is kindCoverageFloorEvent's own twin for
// the kind_offer stage (chaos3900_structure_offers.go) -- same last-event-
// wins rule. UNLIKE kindCoverageFloorEvent/confirmedKindRescueEvent above,
// this stage is UNCONDITIONAL: kindOfferMaterial runs on every resolution,
// so this event is present on every captured call a ResolveSubjects call
// reached (ok is only ever false when the trace is empty/nil, e.g. a
// pre-ResolveSubjects failure).
func (c *twoTurnTraceCapture) kindOfferEvent() (event graphrank.ResolutionTraceEvent, ok bool) {
	for _, e := range c.events {
		if e.Stage == "kind_offer" {
			event, ok = e, true
		}
	}
	return event, ok
}

// evidenceRoundEvent (CHAOS-4161 v19) is kindCoverageFloorEvent's own twin
// for the evidence_round stage (CHAOS-3899) -- same last-event-wins rule.
// Deliberately a SEPARATE reader from censusRan(): RunShadowEvidenceRound
// (chaos3899_evidence_round.go) emits exactly ONE "evidence_round" event on
// EVERY call that reaches it, including every one of its own early-return
// refusals (non-current axis, scoped visibility, multi-handle, no
// discriminators, unregistered census kind, zero census kinds surviving the
// per-kind loop) -- so this event's mere PRESENCE proves only that the
// round was ENTERED (resolve.go:1835's own outer gate,
// deps.CensusFunc != nil && len(resolution.Committed) == 0 && searchTruncated,
// passed), never that CensusFunc was actually invoked for any kind. That is
// exactly what census_ran()'s own doc comment already establishes by keying
// off "evidence_probe" instead. This reader exists to make the ENTRY fact
// itself observable, corpus-wide, independent of whether a census ever ran
// -- see evidenceRoundEntered/evidenceRoundReason, below, and CHAOS-4161's
// own investigation (census_ran=false read as "CensusFunc is nil" until
// this distinction was traced through).
func (c *twoTurnTraceCapture) evidenceRoundEvent() (event graphrank.ResolutionTraceEvent, ok bool) {
	for _, e := range c.events {
		if e.Stage == "evidence_round" {
			event, ok = e, true
		}
	}
	return event, ok
}

// passTruncation (CHAOS-4120) reports per-pass truncation across the
// captured call's retrieval: whether the per-term "search" pass truncated
// on ANY term, and whether the question-level "search_question" pass
// truncated -- the two-thirds of the breakdown resolve.go's own Truncated
// field newly carries (kind_coverage_floor's own KindCoverageFloorTruncated,
// read via kindCoverageFloorEvent above, is the third pass). Before
// resolve.go's Truncated field existed, both passes only ever fed ONE
// pooled resolution-wide flag (the decision event's own SearchTruncated),
// so a reader could tell "something truncated" but never which pass --
// exactly what the CHAOS-4120 question-results decomposition could not
// answer from the artifact. "ANY term" for search (not "the last term"):
// a resolution issues one search-stage event per term, and any one of them
// truncating is as load-bearing for the resolution-wide gate as all of
// them would be.
func (c *twoTurnTraceCapture) passTruncation() (termSearchTruncated, questionSearchTruncated bool) {
	for _, e := range c.events {
		switch e.Stage {
		case "search":
			if e.Truncated {
				termSearchTruncated = true
			}
		case "search_question":
			if e.Truncated {
				questionSearchTruncated = true
			}
		}
	}
	return termSearchTruncated, questionSearchTruncated
}

// poolContainsKind (CHAOS-4120, CHAOS-4038's exact question) reports
// whether ANY candidate of kind reached the merged candidate pool during
// the captured call -- read off the "corroboration" stage, which
// resolution.go emits exactly once per candidate in the FULL merged pool,
// before ranking or truncation to MaxSubjectCandidates. This is the fact
// CHAOS-4038 needed and could not get from the artifact: kindOfferMaterial
// only ever offers a kind already present in the pool (chaos3900_structure_
// offers.go), so an offer-miss for the expected kind is either "in the pool
// but the offer builder skipped it" (poolContainsKind==true -- an
// offer-layer question, CHAOS-4012's scope) or "retrieval never found a
// candidate of this kind at all" (poolContainsKind==false -- the candidate-
// pool/search recall gap CHAOS-4038 itself is scoped to). An empty kind
// always reports false: there is nothing to look for.
func (c *twoTurnTraceCapture) poolContainsKind(kind string) bool {
	if kind == "" {
		return false
	}
	for _, e := range c.events {
		if e.Stage == "corroboration" && string(e.Subject.Kind) == kind {
			return true
		}
	}
	return false
}

// boundaryContainsKind (CHAOS-4012 v22, team-lead ruling 2026-08-23,
// re-smoke follow-up) is poolContainsKind's call-boundary-scoped twin: it
// reads the kind_offer event's own KindOfferBoundaryKinds -- the distinct
// kinds present in the EXACT slice kindOfferMaterial/candidateOfferMaterial
// read at their shared call boundary (resolve.go) -- via the SAME
// last-event-wins kindOfferEvent() reader every other kind_offer field
// already uses.
//
// poolContainsKind reads "corroboration" instead, emitted for the FULL
// merged pool BEFORE ResolveFromMergedCandidatesWithGate's own final
// ranked-set truncation -- so it over-reports presence relative to what
// kindOfferMaterial/candidateOfferMaterial actually see: a candidate can
// corroborate early and still be gone from kindOfferCandidates by the time
// this stage fires (truncated upstream, e.g. by a small MaxSubjectCandidates
// already full of higher-confidence other-kind candidates). This function
// answers the narrower, boundary-scoped question instead: distinguishing
// "genuinely absent at the boundary -- candidate-list cannot fix this, a
// CHAOS-4038 upstream-truncation question" from "present at the boundary,
// still not ranked into CandidateOptions -- an offer-layer question,
// CHAOS-4012's own scope." An empty kind always reports false, same as
// poolContainsKind.
func (c *twoTurnTraceCapture) boundaryContainsKind(kind string) bool {
	if kind == "" {
		return false
	}
	event, ok := c.kindOfferEvent()
	if !ok {
		return false
	}
	for _, boundaryKind := range event.KindOfferBoundaryKinds {
		if boundaryKind == kind {
			return true
		}
	}
	return false
}

// boundaryContainsKindBeforeRepair (CHAOS-4183 phase 3, sol design consult,
// team-lead ratified 2026-08-23) is boundaryContainsKind's PRE-repair twin:
// it reads KindOfferBoundaryKindsBeforeRepair instead of KindOfferBoundaryKinds
// -- the exact pre-phase-3 boundary reading boundaryContainsKind itself used
// to compute, before that field's own meaning shifted to post-repair. See
// KindOfferBoundaryKindsBeforeRepair's own doc comment (ResolutionTraceEvent)
// for why the two readings can now diverge on a Shape-A row: this one still
// answers "was the kind actually present at the boundary before the repair
// ran," which is what a v25-vs-v26 diff needs to measure the repair's own
// effect.
func (c *twoTurnTraceCapture) boundaryContainsKindBeforeRepair(kind string) bool {
	if kind == "" {
		return false
	}
	event, ok := c.kindOfferEvent()
	if !ok {
		return false
	}
	for _, boundaryKind := range event.KindOfferBoundaryKindsBeforeRepair {
		if boundaryKind == kind {
			return true
		}
	}
	return false
}

// censusCount (CHAOS-4120) sums CensusCount across every "evidence_probe"
// event the captured call traced -- the per-kind row count evidence_probe
// carries (brief §1.3(3), "per-kind, never aggregated"), aggregated here
// ONLY for this one row-level summary field; the per-kind breakdown still
// lives in the underlying trace for anyone reading it directly.
//
// censusCount()==0 is NOT by itself "attested zero" -- codex review round 2
// (P2, confirmed against chaos3899_evidence_round.go:556-558): a kind whose
// CensusFunc call itself ERRORED traces CensusComplete=false with
// CensusCount left at its Go zero value (never set on the error branch),
// summing identically to a kind that genuinely attested zero rows. Reading
// census_ran=true, census_count=0 as a real census-backed absence, without
// also checking censusComplete(), would silently promote an unmeasured
// (errored) kind to the same reading as a confirmed one -- see
// censusComplete's own doc comment for the required pairing.
func (c *twoTurnTraceCapture) censusCount() int {
	var total int
	for _, e := range c.events {
		if e.Stage == "evidence_probe" {
			total += e.CensusCount
		}
	}
	return total
}

// censusComplete (CHAOS-4120, codex review round 2 P2 fix) reports whether
// EVERY "evidence_probe" event the captured call traced attested
// CensusComplete==true -- false when the census never ran at all (vacuously
// unmeasured, the same reading censusRan()==false already gives) OR when it
// ran but at least one probed kind's own CensusFunc call errored
// (chaos3899_evidence_round.go's `if err != nil { ka.Complete = false }`
// branch). The three-way read this exists to let a caller draw:
// censusRan()==false -> never measured; censusRan()==true &&
// censusComplete()==false -> ran but at least one kind is unmeasured
// (censusCount() is NOT a trustworthy "attested zero" in this case);
// censusRan()==true && censusComplete()==true && censusCount()==0 -> a REAL,
// census-backed absence across every probed kind.
func (c *twoTurnTraceCapture) censusComplete() bool {
	ran := false
	for _, e := range c.events {
		if e.Stage == "evidence_probe" {
			ran = true
			if !e.CensusComplete {
				return false
			}
		}
	}
	return ran
}

// memberApplied reports whether member was actually confirmed/applied on
// result. Window is a SEPARATE mechanism from every other member (codex
// round-3 finding, confirmed against engine.go:836-840/window.go:420-423):
// window redemption stamps EffectiveEvidenceWindow.Provenance=
// clarification_confirmed and NEVER adds a ConfirmedStructure entry --
// composeConfirmedStructure is built from structureCanon.Confirmed/Explicit,
// which resolveWindowReceipts never touches. Checking ConfirmedStructure
// alone for window (the round-1/round-2 code) can never detect a window
// confirmation at all.
func memberApplied(result contractsv1.ContextFabricInvestigationResult, member string) bool {
	if member == string(contractsv1.ContextFabricStructureNeedWindow) {
		return result.EffectiveEvidenceWindow != nil && result.EffectiveEvidenceWindow.Provenance == contractsv1.ContextFabricWindowClarificationConfirmed
	}
	for _, e := range result.ConfirmedStructure {
		if string(e.Member) == member && e.Disposition == contractsv1.ContextFabricStructureDispositionApplied {
			return true
		}
	}
	return false
}

// twoTurnCommittedWrong reports whether committed is a decisive commit that
// does NOT match the corpus's own ground truth -- the confirmed_wrong arm's
// pass/fail predicate (design brief §5 head: "ANY resulting oracle-wrong
// commit ALSO FAILS the run").
func twoTurnCommittedWrong(committed []contractsv1.ContextFabricSubjectRef, tc trialCase) bool {
	return len(committed) > 0 && !committedMatchesTrial(committed, tc)
}

// --- CHAOS-4039 v5 measurement contract: engine-deterministic decision-state
// equivalence (team-lead ruling 2026-08-22, superseding the v4
// canonicalInterpretationHash/normalizedDecisionFingerprint pairing proof) ---
//
// v4's pairing proof required BIT-FOR-BIT equality of two SHA-256 hashes,
// each built partly from RAW LIVE-MODEL OUTPUT: normalizedDecisionFingerprint
// hashed InvestigationResult.Status (SynthesisDraft.Status, model_runtime.go
// -- the synthesis model's own free-form completeness judgment), and
// canonicalInterpretationHash hashed InterpretedQuestion.SubjectTerms/
// ComparisonTerms/FactRequirements (genkitruntime's interpretationOutput --
// a SEPARATE live interpretation-model call's own free-text output). Both
// calls of a pair are INDEPENDENTLY SAMPLED Investigate() calls against the
// SAME production, live-model Investigator (this file's own header comment)
// with no temperature pinning anywhere in genkitruntime/modelprovider --
// so ordinary call-to-call model phrasing/judgment variance, not hint
// influence, was enough to flip either hash. A live run confirmed this:
// baseline_equivalent measured 0/4 even on cases whose baseline and hinted
// legs committed the byte-identical subject (same Kind+CanonicalID) --
// v4's own definition of "equivalent" was, in practice, close to
// unreachable, which is what this v5 replacement fixes.
//
// v5 compares ONLY engine-deterministic state, produced by graphrank's own
// resolution/decision logic, never by a model call:
//
//  1. the paired calls' own FINAL decision-stage trace event Outcome
//     (graphrank.ResolutionTraceEvent, resolution.go -- the closed
//     "committed"/"ambiguous"/"no_commit" vocabulary; "final" because a
//     stalled resolution can emit TWO decision events, the initial stalled
//     attempt then a census-enriched re-decision --
//     TestResolveSubjects_EvidenceCensusCommitsAStalledCandidate, graphrank
//     -- only the LAST one (per subject) describes what the caller
//     actually received, twoTurnTraceCapture.finalDecisionEvents) match, AND
//  2. the paired calls' own committed-subject SETS
//     (twoTurnCommittedSubjectsEquivalent: Kind+CanonicalID only, Label
//     dropped as presentation text -- this section's own former
//     subjectRefFingerprint discipline, twoTurnSubjectKindIDs already
//     established for the identical reason) are equal.
//
// Neither comparison touches InvestigationResult.Status, Interpretation, or
// any other model-authored free text -- see twoTurnInferredClassification's
// own doc comment for the full 3-way partition this feeds.
type twoTurnSubjectKey struct {
	Kind        string
	CanonicalID string
}

// twoTurnCommittedSubjectKeys reduces refs to its own Kind+CanonicalID SET
// (a map, so callers get order-independent, duplicate-tolerant comparison
// for free) -- Label is dropped, matching twoTurnSubjectKindIDs' own "no
// question/label text" discipline above.
func twoTurnCommittedSubjectKeys(refs []contractsv1.ContextFabricSubjectRef) map[twoTurnSubjectKey]struct{} {
	keys := make(map[twoTurnSubjectKey]struct{}, len(refs))
	for _, r := range refs {
		keys[twoTurnSubjectKey{Kind: string(r.Kind), CanonicalID: r.CanonicalID}] = struct{}{}
	}
	return keys
}

// twoTurnCommittedSubjectsEquivalent reports whether a and b commit the SAME
// set of subjects (Kind+CanonicalID only) -- CHAOS-4039 v5's own
// engine-deterministic half of baseline_equivalent (see this section's own
// header comment). Order-independent and duplicate-tolerant: what v5 treats
// as "the same decisive outcome" is the resulting subject SET, never the
// slice's own incidental order.
func twoTurnCommittedSubjectsEquivalent(a, b []contractsv1.ContextFabricSubjectRef) bool {
	ak, bk := twoTurnCommittedSubjectKeys(a), twoTurnCommittedSubjectKeys(b)
	if len(ak) != len(bk) {
		return false
	}
	for k := range ak {
		if _, ok := bk[k]; !ok {
			return false
		}
	}
	return true
}

// twoTurnDecisionCommittedSubjects (CHAOS-4139) extracts the committed-
// subject SET straight from a decision-stage graphrank.ResolutionTraceEvent
// -- the true engine-deterministic layer, one hop earlier than
// SubjectResolution.Committed on the final InvestigationResult.
//
// THE BUG THIS CLOSES: CHAOS-4039 v5's own doc comment (twoTurnCaseResult.
// InferredClassification, below) already CLAIMED "engine-deterministic
// decision state... never a model-authored string", but the code it
// described read hintedCommitted/baselineCommitted off
// result.SubjectResolution.Committed -- which is NOT engine-deterministic.
// CHAOS-4085's post-synthesis commit-affirmation gate
// (chaos4085_commit_affirmation.go, applyCommitAffirmation) can retract a
// CommitBasisStatistical subject from that exact field based on live,
// independently-sampled model synthesis output, strictly AFTER the
// decision v5 meant to compare. v5 moved away from hashing free-text model
// output (CHAOS-4039's own v4 defect, this file's ATTESTATION BOUNDARY
// section, CHAOS-4083) but landed one layer short of where model influence
// actually stops: the decision-stage trace event, captured BEFORE
// synthesis ever runs. CHAOS-4135's shard-54 rerun proved the gap directly
// -- two legs with byte-identical decision-stage trace events (same
// CommitGate=vector_margin_rescue, same CommitBasis=statistical, same
// committed Subject) still classified "unjustified" because one leg's own
// independent synthesis call did not affirm the commit while the other
// did. request.ExpectedKinds structurally cannot explain that divergence
// (confirmed: zero occurrences in internal/contextfabric/genkitruntime,
// production code) -- it is exactly the sampling noise a decision-layer
// comparison is supposed to be immune to, per v5's OWN stated intent.
//
// A single-element slice when Outcome=="committed" for exactly ONE decision
// event, nil otherwise -- twoTurnCommittedSubjectsEquivalent treats
// nil/empty identically to any other empty set. decision.Subject.Label is
// UNSCRUBBED here (this is a local, in-process comparison value, never
// serialized -- unlike twoTurnTraceCapture.snapshot(), which strips it
// before anything reaches the persisted artifact) but
// twoTurnCommittedSubjectKeys drops Label regardless, so it never reaches
// the comparison either way.
//
// CHAOS-4096: graphrank's own commit switch (resolution.go) and the
// caller-hint short circuit (resolve.go) can now both emit MORE than one
// decision event per leg for a genuinely multi-subject commit -- see
// twoTurnUnionCommittedSubjects below, which is what a caller iterating
// finalDecisionEvents' own plural result must use instead of calling this
// function once per event and keeping only the last.
func twoTurnDecisionCommittedSubjects(decision graphrank.ResolutionTraceEvent) []contractsv1.ContextFabricSubjectRef {
	if decision.Outcome != "committed" {
		return nil
	}
	return []contractsv1.ContextFabricSubjectRef{decision.Subject}
}

// twoTurnCommitAffirmationState (CHAOS-4139) reports what CHAOS-4085's
// post-synthesis commit-affirmation gate (chaos4085_commit_affirmation.go)
// did to decision's own committed subject.
//
//   - "" -- decision itself did not commit (nothing for the gate to act on).
//   - "exempt" -- decision.CommitBasis is IdentityProven
//     (CommitBasisCallerCanonicalID/CommitBasisAuthoritativeIdentity);
//     applyCommitAffirmation's own gate (line 458: `basis.IdentityProven()
//     || commitSubjectAffirmed(...)`) never evaluates these at all, so
//     there is nothing here to attribute to synthesis.
//   - "retracted" -- basis is not IdentityProven and decision.Subject's
//     Kind+CanonicalID is absent from result.SubjectResolution.Committed.
//   - "affirmed" -- basis is not IdentityProven and decision.Subject is
//     still present there.
//
// codex xhigh review round 2 (MEDIUM, confirmed): the first version of this
// function detected "retracted" by scanning result.Limitations for the
// FIXED ContextFabricCommitRetractionLimitation string, reasoning that the
// string "has exactly ONE append site in the whole engine". That reasoning
// only covers the production APPEND site -- it does not cover the TYPE.
// SynthesisDraft.Limitations ([]string, model_runtime.go) is model-authored
// free text, cloned verbatim into result.Limitations
// (`Limitations: cloneSlice(draft.Limitations)`, model_runtime.go). Nothing
// stops a synthesis call from independently emitting a string that happens
// to equal the constant's value, at which point this function would read
// "retracted" whether or not CHAOS-4085's gate actually fired for THIS
// leg -- precisely the model-influenceable-comparison hazard (ATTESTATION
// BOUNDARY shape 1) this same PR fixes one layer up. Fixed by comparing
// result.SubjectResolution.Committed instead: that field is engine-only
// (set once from graphrank's own resolution at engine.go:957, mutated only
// by applyCommitAffirmation's `result.SubjectResolution.Committed =
// retained` at chaos4085_commit_affirmation.go:477 -- computed purely from
// the pre-affirmation provisional list filtered by
// IdentityProven()/commitSubjectAffirmed(), never assigned from draft
// anywhere in model_runtime.go). Absence there is direct evidence the gate
// retracted this exact subject, immune to whatever free text a model wrote.
//
// Deliberately does NOT read result.Coverage.Partial, though
// applyCommitAffirmation sets that too (line 508) -- Partial is NOT
// exclusive to this gate: engine.go's retrieval-degradation path
// (withRetrievalDegradation) sets the SAME field for an UNRELATED reason
// (search truncation), and this exact case has search_truncated=true on
// both legs regardless of any retraction. Reading Partial alone here would
// have reported every row in a truncated search as "retracted" whether or
// not CHAOS-4085 ever fired -- exactly the kind of over-broad marker this
// function exists to avoid.
func twoTurnCommitAffirmationState(decision graphrank.ResolutionTraceEvent, result contractsv1.ContextFabricInvestigationResult) string {
	if decision.Outcome != "committed" {
		return ""
	}
	// codex xhigh review round 1 (MEDIUM, confirmed): CommitBasisUnknown
	// (the zero value, "") is NOT string(CommitBasisStatistical), so a
	// literal `!= statistical` comparison misreads it as exempt. Its own
	// doc comment (chaos4085_commit_basis.go) says the opposite: Unknown
	// is FAIL-CLOSED, "treated exactly like CommitBasisStatistical by
	// every consumer", and is not in IdentityProven's allow-list. Calling
	// IdentityProven() directly is both correct for Unknown and immune to
	// a future CommitBasis value landing on the wrong side by default.
	if contextfabric.CommitBasis(decision.CommitBasis).IdentityProven() {
		return "exempt"
	}
	still := twoTurnCommittedSubjectKeys(result.SubjectResolution.Committed)
	key := twoTurnSubjectKey{Kind: string(decision.Subject.Kind), CanonicalID: decision.Subject.CanonicalID}
	if _, ok := still[key]; ok {
		return "affirmed"
	}
	return "retracted"
}

// twoTurnUnionCommittedSubjects (CHAOS-4096) unions the committed subjects
// across EVERY decision-stage event a leg captured (finalDecisionEvents'
// own plural result) -- the generalization twoTurnDecisionCommittedSubjects'
// single-event form needs now that a multi-subject commit or the
// caller-hint short circuit can emit more than one decision event per leg.
// By construction (the resolution.go switch fires exactly ONE case: either
// N committed events sharing Outcome=="committed", or one ambiguous event,
// or one no_commit event) this is never a MIX of committed and
// non-committed events for the same leg, so a plain per-event union is
// exactly the committed-subject SET twoTurnCommittedSubjectsEquivalent
// already expects.
func twoTurnUnionCommittedSubjects(events []graphrank.ResolutionTraceEvent) []contractsv1.ContextFabricSubjectRef {
	var union []contractsv1.ContextFabricSubjectRef
	for _, event := range events {
		union = append(union, twoTurnDecisionCommittedSubjects(event)...)
	}
	return union
}

// twoTurnLegOutcome (CHAOS-4096) returns the shared decision-stage Outcome
// across a leg's own finalDecisionEvents() -- "" for an empty leg (no
// decision event captured at all, matching finalDecisionEvent's old
// ok==false case). Every element shares the SAME Outcome by construction
// (see twoTurnUnionCommittedSubjects' own doc comment), so the last one is
// representative.
func twoTurnLegOutcome(events []graphrank.ResolutionTraceEvent) string {
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1].Outcome
}

// twoTurnCommitAffirmationSeverity orders twoTurnCommitAffirmationState's
// own closed vocabulary from least to most alarming, so
// twoTurnLegCommitAffirmation can reduce several per-subject states to the
// single most severe one without inventing a new vocabulary item.
var twoTurnCommitAffirmationSeverity = map[string]int{"": 0, "exempt": 1, "affirmed": 2, "retracted": 3}

// twoTurnLegCommitAffirmation (CHAOS-4096) reduces a leg's own per-subject
// commit-affirmation states (one twoTurnCommitAffirmationState call per
// finalDecisionEvents() entry) to the single most severe one --
// retracted > affirmed > exempt > "". HintedCommitAffirmation/
// BaselineCommitAffirmation stay single STRINGS (see their own doc comment)
// rather than widening to a slice -- that keeps the trial-report JSON
// schema unchanged (no ReportSchemaVersion bump) -- so a multi-subject
// commit's worst-case affirmation is what the row reports: a retraction on
// ANY subject is exactly the correctness signal CHAOS-4085's gate exists to
// surface, and staying silent about it because a DIFFERENT subject in the
// same commit was merely affirmed would be a false-clean row.
func twoTurnLegCommitAffirmation(events []graphrank.ResolutionTraceEvent, result contractsv1.ContextFabricInvestigationResult) string {
	best := ""
	for _, event := range events {
		state := twoTurnCommitAffirmationState(event, result)
		if twoTurnCommitAffirmationSeverity[state] > twoTurnCommitAffirmationSeverity[best] {
			best = state
		}
	}
	return best
}

// twoTurnInferredClassification computes CHAOS-4039's 3-way partition for a
// DECISIVE (CommittedCount>0) kind/handle inferred-tier commit: hinted/
// baselineCommitted are the paired calls' own DECISION-LAYER committed-
// subject sets (CHAOS-4139: twoTurnDecisionCommittedSubjects off each leg's
// own final decision-stage trace event -- deliberately NOT
// SubjectResolution.Committed on the final InvestigationResult, which
// CHAOS-4085's post-synthesis affirmation gate can retract from
// independently per leg; see twoTurnDecisionCommittedSubjects' own doc
// comment for the full mechanism), hinted/baselineOutcome their own final
// decision-stage trace Outcome (twoTurnTraceCapture.finalDecisionEvents,
// reduced via twoTurnLegOutcome),
// and kindAttested is whether the all-kinds census itself certified this
// exact commit (runTwoTurnInferredTierArm's own kindInsensitivityResult/
// evidenceCensusCommitted check). Extracted from the classification switch
// (runTwoTurnInferredTierArm) purely so it has a unit test surface
// independent of that function's full investigator/window-precondition
// machinery -- mirrors twoTurnUnjustifiedShadowProbe's own extraction,
// immediately below.
func twoTurnInferredClassification(hintedCommitted, baselineCommitted []contractsv1.ContextFabricSubjectRef, hintedOutcome, baselineOutcome string, kindAttested bool) string {
	switch {
	case hintedOutcome == baselineOutcome && twoTurnCommittedSubjectsEquivalent(hintedCommitted, baselineCommitted):
		return "baseline_equivalent"
	case kindAttested:
		return "kind_insensitivity_attested"
	default:
		return "unjustified"
	}
}

// --- report shapes (outcome data only -- no question/label text) ---

type twoTurnArm string

const (
	twoTurnArmPositive       twoTurnArm = "positive"
	twoTurnArmInferredTier   twoTurnArm = "inferred_tier"
	twoTurnArmConfirmedWrong twoTurnArm = "confirmed_wrong"
	twoTurnArmMutation       twoTurnArm = "mutation"
)

// twoTurnSubjectKindID carries a committed subject's Kind+CanonicalID only
// -- never Label (presentation/corpus term text, this section's own "no
// question/label text" discipline; twoTurnSubjectKey/twoTurnCommittedSubjectKeys
// above drop it for the identical reason). CHAOS-4062: the
// shadow-insensitivity trace probe's own committed-subject identity, kept
// separate from twoTurnSubjectKey (that type is an internal comparison key,
// not a stable artifact field set this file's schema-version convention
// would need to track).
type twoTurnSubjectKindID struct {
	Kind        string `json:"kind"`
	CanonicalID string `json:"canonical_id"`
}

func twoTurnSubjectKindIDs(refs []contractsv1.ContextFabricSubjectRef) []twoTurnSubjectKindID {
	if len(refs) == 0 {
		return nil
	}
	out := make([]twoTurnSubjectKindID, 0, len(refs))
	for _, r := range refs {
		out = append(out, twoTurnSubjectKindID{Kind: string(r.Kind), CanonicalID: r.CanonicalID})
	}
	return out
}

// ---------------------------------------------------------------------------
// CHAOS-4086 instant-diagnosis stamping
// ---------------------------------------------------------------------------
//
// Three helpers rather than eleven assignments repeated across four arms.
// The arms already drifted once on exactly this axis -- the inferred arm
// grew a trace capture and the other three never did, which is why the gate
// value was "categorically unreachable on two arms" -- and a per-arm copy of
// the stamping logic is how that happens again. One definition each means a
// new arm either calls them or visibly does not.

// twoTurnStampOutcome records what an arm's turn actually committed and what
// the corpus expected of it.
//
// tc.Question is in scope here and is NEVER read: the corpus question is the
// one thing this artifact may not carry. Only tc.ExpectKind/tc.ExpectID --
// a closed kind enum and a canonical id -- cross into the row.
func twoTurnStampOutcome(res *twoTurnCaseResult, tc trialCase, committed []contractsv1.ContextFabricSubjectRef) {
	if res == nil {
		return
	}
	res.ExpectedKind = tc.ExpectKind
	res.ExpectedID = tc.ExpectID
	res.CommittedSubjects = twoTurnSubjectKindIDs(committed)
}

// twoTurnStampDecision copies the decision-stage trace event's shape onto the
// row. A trace that captured no decision event leaves every field zero, which
// is honest: "no decision event was observed" and "the gate was empty" are
// different facts, and CommitGate=="" alongside TiedStatisticalTop==false is
// how a reader sees the former.
//
// The caller is responsible for having reset the trace immediately before its
// own Investigate call -- see twoTurnStampDecisionFor's callers.
func twoTurnStampDecision(res *twoTurnCaseResult, trace *twoTurnTraceCapture) {
	if res == nil || trace == nil {
		return
	}
	// CHAOS-4135: unconditional -- every row stages its own decisive call's
	// full trace stream here, in memory only; the redaction pass at the end
	// of TestChaos3742TwoTurnConfirmationReplay (twoTurnCaseResultIsAnomalous)
	// clears it back to nil before the report is serialized unless this row
	// is anomalous. Staging here rather than at each of the ~5 call sites
	// makes this the ONE place every arm's own decisive-call capture lives,
	// the same "single site, not five" reasoning SynthesisStatusOverrideUncommittedCount's
	// own doc comment already applies to counting.
	res.TraceEvents = trace.snapshot()
	// CHAOS-4096: CommitGate/TiedStatisticalTop/SearchTruncated are
	// RESOLUTION-wide, not per-subject -- every decision event a single
	// resolution now emits (one per committed subject, on a multi-subject
	// commit) carries the identical value for all three, so the last
	// captured event is exactly as representative as it always was.
	if events := trace.finalDecisionEvents(); len(events) > 0 {
		event := events[len(events)-1]
		res.CommitGate = event.CommitGate
		res.TiedStatisticalTop = event.TiedStatisticalTop
		res.SearchTruncated = event.SearchTruncated
	}
	if event, ok := trace.kindCoverageFloorEvent(); ok {
		res.KindCoverageFloorFired = event.KindCoverageFloorFired
		res.KindCoverageMissingKinds = event.KindCoverageMissingKinds
		res.KindCoverageFloorTruncated = event.KindCoverageFloorTruncated
		res.KindCoverageMissingKindsList = event.KindCoverageMissingKindsList
	}
	// CHAOS-4038 v18: confirmed_kind_rescue's own twin capture, same
	// last-event-wins reader as kind_coverage_floor above.
	if event, ok := trace.confirmedKindRescueEvent(); ok {
		res.ConfirmedKindRescueFired = event.ConfirmedKindRescueFired
		res.ConfirmedKindRescueResultCount = event.ConfirmedKindRescueResultCount
		res.ConfirmedKindRescueTruncated = event.ConfirmedKindRescueTruncated
	}
	// CHAOS-4012 v20: kind_offer's own twin capture, same last-event-wins
	// reader as kind_coverage_floor/confirmed_kind_rescue above -- but
	// UNCONDITIONAL upstream (kindOfferMaterial runs every resolution), so
	// `ok` here is only false when trace itself never observed a
	// ResolveSubjects call at all.
	if event, ok := trace.kindOfferEvent(); ok {
		res.KindOfferExplicitHintCount = event.KindOfferExplicitHintCount
		res.KindOfferDistinctKindCount = event.KindOfferDistinctKindCount
		res.KindOfferSuppressedByCardinality = event.KindOfferSuppressedByCardinality
		// CHAOS-4012 v22: the candidate-list axis's own pair, same
		// unconditional last-event-wins reader as the kind-pick fields above.
		res.CandidateOfferCount = event.KindOfferCandidateOfferCount
		res.OfferKind = event.KindOfferOfferKind
		// CHAOS-4119, schema v27: the handle-offer graph-derived-source
		// axis's own pair, same unconditional last-event-wins reader.
		res.HandleOfferGraphDerivedCount = event.HandleOfferGraphDerivedCount
		res.HandleOfferGraphDerivedRejectedCount = event.HandleOfferGraphDerivedRejectedCount
	}
	// CHAOS-4103: folded (severity-gated), NOT set unconditionally -- by the
	// time this is called, an EARLIER call's override may already have been
	// folded into res (baseline, a setup turn, turn 1 itself), and an
	// unconditional overwrite here would silently regress an
	// already-recorded uncommitted (routing-bug) reason back to whatever
	// this LATEST call's own trace state says, undoing
	// twoTurnFoldSynthesisStatusOverride's entire severity guarantee. See
	// that function's own doc comment.
	twoTurnFoldSynthesisStatusOverride(res, trace.synthesisOverride)
}

// twoTurnSetSynthesisOverride writes outcome's fields onto res
// unconditionally (a nil outcome leaves res's fields at whatever they
// already were -- callers needing "clear to not-fired" construct a fresh
// twoTurnCaseResult, which every arm already does per row). Factored out so
// twoTurnStampDecision's ordinary stamp and twoTurnFoldSynthesisStatusOverride's
// severity-gated fold below write IDENTICAL fields from the same one place.
func twoTurnSetSynthesisOverride(res *twoTurnCaseResult, outcome *contextfabric.SynthesisStatusOverrideOutcome) {
	if res == nil || outcome == nil {
		return
	}
	res.SynthesisStatusOverrideFired = true
	res.SynthesisStatusOverrideFrom = string(outcome.From)
	res.SynthesisStatusOverrideTo = string(outcome.To)
	res.SynthesisStatusOverrideReason = string(outcome.Reason)
	res.SynthesisStatusOverrideCommittedCount = outcome.CommittedCount
}

// twoTurnFoldSynthesisStatusOverride folds an EARLIER call's captured
// override outcome into res, when res's own already-stamped state (from
// twoTurnStampDecision, which only ever sees the LATEST call's trace) does
// not already carry the more severe reason.
//
// CHAOS-4103, codex review round 1 (confirmed by direct inspection): an
// arm that makes more than one real Investigate() call before reading the
// shared trace capture -- runTwoTurnInferredTierArm's paired no-hint
// baseline, runTwoTurnConfirmedWrongArm's kind/handle/window setup turn --
// resets that capture before the LATER call it actually stamps from, the
// SAME "capture immediately after the call, before the next reset"
// discipline baselineDecision above already needs and already has. Without
// this fold, an EARLIER call's override -- including the uncommitted
// routing-bug shape this ticket's own blocking-defect bar exists to catch
// -- would be silently discarded the instant the arm resets the capture
// for its next call, understating (or entirely missing) the run-level
// count.
//
// "More severe" is a real tie-break, not an arbitrary pick: uncommitted
// (SynthesisStatusOverrideClarificationUnavailableUncommitted, a routing
// bug) always wins over the ordinary reason or over not-fired-at-all --
// the run-level SynthesisStatusOverrideUncommittedCount bar must never be
// masked by a LATER call firing the ordinary reason, or by a later call
// not firing at all. Both calls firing the SAME reason is a no-op either
// way (nothing changes which reason the row already carries).
func twoTurnFoldSynthesisStatusOverride(res *twoTurnCaseResult, earlier *contextfabric.SynthesisStatusOverrideOutcome) {
	if res == nil || earlier == nil {
		return
	}
	uncommitted := string(contextfabric.SynthesisStatusOverrideClarificationUnavailableUncommitted)
	earlierIsUncommitted := string(earlier.Reason) == uncommitted
	if !res.SynthesisStatusOverrideFired || (earlierIsUncommitted && res.SynthesisStatusOverrideReason != uncommitted) {
		twoTurnSetSynthesisOverride(res, earlier)
	}
}

// TestChaos4103_FoldSynthesisStatusOverridePreservesAnEarlierCall is the
// direct repro-or-refute pin for codex review round 1's finding: an
// earlier call's override (the paired baseline in runTwoTurnInferredTierArm,
// the setup turn in runTwoTurnConfirmedWrongArm) must survive being folded
// in, and the uncommitted routing-bug reason must never be masked by an
// ordinary reason from whichever call happens to be read last.
func TestChaos4103_FoldSynthesisStatusOverridePreservesAnEarlierCall(t *testing.T) {
	ordinary := contextfabric.SynthesisStatusOverrideOutcome{
		From: "clarification_required", To: "no_match",
		Reason: contextfabric.SynthesisStatusOverrideClarificationUnavailable, CommittedCount: 1,
	}
	uncommitted := contextfabric.SynthesisStatusOverrideOutcome{
		From: "clarification_required", To: "no_match",
		Reason: contextfabric.SynthesisStatusOverrideClarificationUnavailableUncommitted, CommittedCount: 0,
	}

	t.Run("hinted never fired, baseline did: baseline's outcome survives the fold", func(t *testing.T) {
		res := twoTurnCaseResult{}
		twoTurnFoldSynthesisStatusOverride(&res, &uncommitted)
		if !res.SynthesisStatusOverrideFired || res.SynthesisStatusOverrideReason != string(contextfabric.SynthesisStatusOverrideClarificationUnavailableUncommitted) {
			t.Fatalf("got %+v, want the baseline's uncommitted outcome folded in", res)
		}
	})
	t.Run("hinted fired ordinary, baseline fired uncommitted: the routing bug wins, never masked", func(t *testing.T) {
		res := twoTurnCaseResult{}
		twoTurnSetSynthesisOverride(&res, &ordinary)
		twoTurnFoldSynthesisStatusOverride(&res, &uncommitted)
		if res.SynthesisStatusOverrideReason != string(contextfabric.SynthesisStatusOverrideClarificationUnavailableUncommitted) {
			t.Fatalf("reason = %q, want the more severe uncommitted reason to win regardless of call order", res.SynthesisStatusOverrideReason)
		}
	})
	t.Run("hinted fired uncommitted, baseline fired ordinary: the routing bug is never overwritten by an ordinary later reason", func(t *testing.T) {
		res := twoTurnCaseResult{}
		twoTurnSetSynthesisOverride(&res, &uncommitted)
		twoTurnFoldSynthesisStatusOverride(&res, &ordinary)
		if res.SynthesisStatusOverrideReason != string(contextfabric.SynthesisStatusOverrideClarificationUnavailableUncommitted) {
			t.Fatalf("reason = %q, want the already-stamped uncommitted reason to survive an ordinary fold", res.SynthesisStatusOverrideReason)
		}
	})
	t.Run("neither call fired: no-op, row stays not-fired", func(t *testing.T) {
		res := twoTurnCaseResult{}
		twoTurnFoldSynthesisStatusOverride(&res, nil)
		if res.SynthesisStatusOverrideFired {
			t.Fatal("a nil earlier outcome must never mark the row as fired")
		}
	})
}

// ---------------------------------------------------------------------------
// CHAOS-4120 turn-1 fact stamping
// ---------------------------------------------------------------------------
//
// THE GAP THIS CLOSES: every arm shares ONE turn-1 call, and its own
// decision/kind-coverage/search-truncation trace and its own StructureNeeds
// (AnchorOptions/HandleOptions counts) and candidate pool are read here,
// captured immediately after turn 1's own Investigate call -- BEFORE the
// first arm resets the shared trace capture for its own call (the SAME
// "capture before the next reset" discipline turn1SynthesisOverride already
// needs). Before this, none of it reached a row: twoTurnStampDecision only
// ever reads the CALLING arm's own (turn-2) trace, so an offer-miss row --
// which never makes a turn-2 call at all -- reported CommitGate=="" and
// every kind-coverage field at its zero value, indistinguishable from "the
// gate was empty" even though turn 1's own trace already explained exactly
// why no offer existed.
//
// Deliberately separate, Turn1-prefixed fields rather than reusing
// CommitGate/TiedStatisticalTop/SearchTruncated/KindCoverage* -- this file's
// own established discipline for two provenances that must never be
// conflated into one field (ArmInvalidStage kept separate from
// ArmInvalidReason, CommitBasis kept separate from CommitGate,
// BaselineCommittedSubjects/HintedCommittedSubjects kept as two fields
// rather than one). Overloading the existing fields would make them mean
// "turn 1's decision" on some rows and "turn 2's decision" on others,
// depending on which arm happened to reach a turn-2 call -- exactly the
// silent-provenance-drift class this file's ShadowKindInsensitivityMode
// comment warns a reader must never be left to reconstruct.

// twoTurnTurn1Facts carries every observational fact this ticket's own
// decomposition needed that lives ONLY on turn 1's call: the same
// corpus-safe discipline as twoTurnCaseResult itself (counts, bools, a
// closed-vocabulary gate name) -- tc.Question is in scope at the one call
// site that fills this and is deliberately never read.
type twoTurnTurn1Facts struct {
	CommitGate                   string
	TiedStatisticalTop           bool
	SearchTruncated              bool
	KindCoverageFloorFired       bool
	KindCoverageMissingKinds     int
	KindCoverageFloorTruncated   bool
	KindCoverageMissingKindsList []string
	// ConfirmedKindRescueFired/ResultCount/Truncated (CHAOS-4038 v18) mirror
	// KindCoverageFloorFired/MissingKinds/Truncated above, read off turn 1's
	// own confirmed_kind_rescue stage (CHAOS-4132) instead.
	ConfirmedKindRescueFired       bool
	ConfirmedKindRescueResultCount int
	ConfirmedKindRescueTruncated   bool
	// KindOfferExplicitHintCount/DistinctKindCount/SuppressedByCardinality
	// (CHAOS-4012 v20) mirror the kind_offer trace stage, read off turn 1's
	// own kindOfferMaterial call.
	KindOfferExplicitHintCount       int
	KindOfferDistinctKindCount       int
	KindOfferSuppressedByCardinality bool
	// CandidateOfferCount/OfferKind (CHAOS-4012 v22) mirror the same
	// kind_offer trace stage's candidate-list axis pair, read off turn 1's
	// own kindOfferMaterial call.
	CandidateOfferCount     int
	OfferKind               string
	TermSearchTruncated     bool
	QuestionSearchTruncated bool
	// ExpectedInPool (CHAOS-4038's exact question) is poolContainsKind
	// evaluated against THIS case's own oracle expected kind -- "in the
	// pool but never offered" (true) versus "never retrieved at all"
	// (false).
	ExpectedInPool bool
	// ExpectedKindAtOfferBoundary (CHAOS-4012 v22, team-lead ruling
	// 2026-08-23, re-smoke follow-up) is boundaryContainsKind evaluated
	// against THIS case's own oracle expected kind -- ExpectedInPool's own
	// call-boundary-scoped refinement, needed because ExpectedInPool
	// over-reports: it is trace-wide ("corroboration," the full merged pool
	// before final truncation), while this reads the EXACT slice
	// kindOfferMaterial/candidateOfferMaterial actually read. Separates
	// "present at the boundary, still not ranked into CandidateOptions" (an
	// offer-layer question, CHAOS-4012's own scope -- candidate-list CAN fix
	// this) from "already absent at the boundary" (upstream truncation
	// already lost it -- candidate-list reads the SAME slice, so it cannot).
	ExpectedKindAtOfferBoundary bool
	// OfferComposedUnderWindowGate (CHAOS-4234, schema v29) mirrors the
	// kind_offer trace event's OfferedUnderWindowGate: turn 1 ran the
	// class-default gate's offers-only resolution, so any kind/handle/
	// candidate offer on this turn was composed BESIDE the window offer.
	OfferComposedUnderWindowGate bool
	// ExpectedSubjectInPool/ExpectedSubjectRank/ExpectedSubjectAtOfferBoundary
	// (CHAOS-4234, schema v29) are the SUBJECT-level twins of ExpectedInPool/
	// ExpectedKindAtOfferBoundary: whether the oracle's expect_id itself (kind
	// AND canonical id, not merely its kind) corroborated into the full
	// merged pool, its 1-based rank in the LAST ranked_cut batch (0 when it
	// never reached the cut), and whether it reached the offer builders'
	// shared input (survived the cut, or bypassed it as a coverage-floor
	// find). These close the split the kind-level fields could not make:
	// a handle miss whose KIND is at the boundary can still be a target
	// the cut dropped, or a target retrieval never found.
	ExpectedSubjectInPool          bool
	ExpectedSubjectRank            int
	ExpectedSubjectAtOfferBoundary bool
	// ExpectedSubjectRetrievalSource (CHAOS-4348, schema v37) is
	// twoTurnTraceCapture.retrievalSourceFor's own reading: "exact_name" /
	// "kind_scoped" / "both" / "ordinary" / "absent" -- see that method's
	// own doc comment (chaos4234_regime_a_harness_test.go). Report bar: repository/
	// project/team's expected_subject_in_pool rate and committed-positive
	// rate per kind, cross-tabbed against this field, is what proves the
	// new arms are actually load-bearing rather than redundant with
	// ordinary search.
	ExpectedSubjectRetrievalSource string
	// OracleIDSchemeMismatch (CHAOS-4348 measurement-layer fix, schema v38)
	// is oracleIDSchemeMismatch's own reading (chaos4348_oracle_id_scheme_test.go):
	// true iff tc.ExpectID does NOT carry tc.ExpectKind's live canonical-id
	// scheme prefix (identity.Derive's "<kind>.v2:" for a migrated kind,
	// else the stable pre-migration "<kind>:"). This is the "fail loudly
	// instead of silently reading absent" telemetry team-lead's GO
	// required: computed directly from the (kind, id) pair, independent of
	// poolContainsSubject's own search -- a malformed oracle id can never
	// again hide behind an otherwise-unremarkable ExpectedSubjectInPool=false
	// the way it did through Run F (the annex's project ids predated the
	// identity.v2 migration and could never string-match a live pool
	// entry, regardless of whether retrieval itself was working).
	OracleIDSchemeMismatch bool
	// ExpectedKindAtOfferBoundaryBeforeRepair/
	// KindOfferDistinctKindCountBeforeRepair/
	// KindOfferSuppressedByCardinalityBeforeRepair (CHAOS-4183 phase 3, sol
	// design consult, team-lead ratified 2026-08-23, schema v26) are the
	// PRE-repair twins of ExpectedKindAtOfferBoundary/
	// KindOfferDistinctKindCount/KindOfferSuppressedByCardinality above --
	// exactly what those three fields would have read had this phase's
	// post-decision kind-only boundary completion not run. Reading BOTH
	// readings off the SAME event is what lets a single artifact measure
	// the repair's own effect directly, without needing a v25 artifact to
	// diff against. ExpectedKindAtOfferBoundaryBeforeRepair is
	// boundaryContainsKindBeforeRepair evaluated against this case's own
	// oracle expected kind, same convention as ExpectedKindAtOfferBoundary
	// itself.
	ExpectedKindAtOfferBoundaryBeforeRepair      bool
	KindOfferDistinctKindCountBeforeRepair       int
	KindOfferSuppressedByCardinalityBeforeRepair bool
	// HandleOfferGraphDerivedCount/HandleOfferGraphDerivedRejectedCount
	// (CHAOS-4119, schema v27) mirror the same kind_offer trace stage's
	// handle-offer graph-derived-source axis pair, read off turn 1's own
	// handleOfferMaterial call.
	HandleOfferGraphDerivedCount         int
	HandleOfferGraphDerivedRejectedCount int
	// AnchorOptionsCount/HandleOptionsCount are turn 1's own
	// StructureNeeds.AnchorOptions/HandleOptions counts, zero when turn 1
	// carried no StructureNeeds at all (a window-only disclosure). They
	// distinguish, for anchor, a designed single-candidate suppression
	// (count==1 by construction) from a zero-claimant recall failure
	// (count==0); for handle, a regex that matched nothing (count==0)
	// from one that matched the WRONG claimant (count>0, just not the
	// expected one) -- OR, since CHAOS-4119 (schema v27), a pool-derived
	// match with no text match at all. HandleOptionsCountBeforeGraphSource
	// (below) carries the pre-CHAOS-4119 reading.
	AnchorOptionsCount                  int
	HandleOptionsCount                  int
	HandleOptionsCountBeforeGraphSource int
	// CensusRan/CensusComplete/CensusCount mirror twoTurnTraceCapture's own
	// censusRan()/censusComplete()/censusCount() -- see censusComplete's own
	// doc comment (codex review round 2, P2 fix) for why CensusComplete is
	// required alongside CensusCount: without it, a kind whose CensusFunc
	// call ERRORED (CensusCount left at zero, never a real attested count)
	// is indistinguishable from one that genuinely attested zero rows.
	CensusRan      bool
	CensusComplete bool
	CensusCount    int
	// EvidenceRoundEntered/EvidenceRoundReason (CHAOS-4161 v19) mirror
	// twoTurnTraceCapture.evidenceRoundEvent() -- whether the round was
	// entered at all (resolve.go:1835's outer gate passed) and, when it
	// was, the closed-vocabulary DegradationReason it refused with --
	// ShadowReason's own vocabulary, chaos3899_handle_grammar.go. MUST be
	// read together, exactly like CensusRan/CensusComplete/CensusCount
	// above: EvidenceRoundEntered==false -> never entered,
	// EvidenceRoundReason carries no meaning; EvidenceRoundEntered==true &&
	// EvidenceRoundReason!="" -> entered and refused for that named reason.
	// EvidenceRoundEntered==true && EvidenceRoundReason=="" is genuinely
	// TWO-WAY AMBIGUOUS (codex xhigh review, CHAOS-4161 R1) -- Reason stays
	// unset both for a terminal would_commit/would_no_match outcome AND for
	// the default would_clarify branch's own "genuinely ambiguous, no
	// single closed-vocabulary reason token names this case" outcome
	// (chaos3899_evidence_round.go's decisive switch, its own default arm)
	// -- so an empty Reason here means "no NAMED degradation reason," never
	// "terminal." Disambiguating those two needs ShadowOutcome, which this
	// ticket does not carry (out of scope; a future field, not this one).
	// An empty EvidenceRoundReason is NOT by itself the fail-closed "never
	// entered" reading either way -- EvidenceRoundEntered is the field that
	// draws that line; census_ran()==false with EvidenceRoundEntered==true
	// is exactly the "entered but the per-kind CensusFunc loop was never
	// reached" case CHAOS-4161 was filed to make observable.
	EvidenceRoundEntered bool
	EvidenceRoundReason  string
	// Regime (CHAOS-4120, coined by the 2026-08-22 question-results
	// decomposition off CHAOS-4118's own Mechanism section) is stamped from
	// the engine's OWN gate-path telemetry (EngineTelemetry.
	// RecordWindowCanonicalization, CHAOS-3900 W1 -- already wired), never
	// re-derived from response shape. See
	// twoTurnRegimeFromWindowCanonicalization's own doc comment for the
	// exact classification: twoTurnRegimeAWindowGated when turn 1's call
	// recorded WindowCanonicalizationGatedClassDefault (the CHAOS-4040
	// class-default gate fired BEFORE ResolveSubjects ran at all);
	// twoTurnRegimeBResolutionProceeded for the outcomes that genuinely
	// mean ordinary resolution proceeded (None/RequestStated/
	// ReceiptConfirmed/InferredDefault); empty ("") otherwise -- nothing was
	// recorded at all, OR a gate-1/refused-no-clarification/Veto* outcome
	// fired, none of which is "the class-default gate fired" or "resolution
	// proceeded ordinarily" (codex review round 2: silently reading one of
	// those as regime B would assert a claim the outcome does not support).
	// This engine-native signal is deliberately NEVER inferred from
	// WindowClarification/StructureNeeds presence -- CHAOS-4118 now makes
	// the class-default gate ALSO compose a window-only StructureNeeds
	// (Missing=[window], WindowOptions), which is exactly the ambiguity an
	// output-shape-based inference would otherwise hit, and the reason the
	// prior, shape-based inference left 4/212 rows unplaceable.
	Regime string
	// WindowExpandOffered/WindowExpandOptionReceiptID (CHAOS-4314, schema
	// v31) read turn 1's own StructureNeeds.WindowExpandOptions directly --
	// unlike every other field on this struct, NOT sourced from a
	// ResolutionTraceEvent (window_expand is composed in windowConfirmationRequiredResult,
	// which never emits a graphrank trace event at all -- see
	// contextfabric.composeWindowExpandOption's own doc comment).
	// WindowExpandOptionReceiptID is empty when Offered is false; carried
	// alongside so twoTurnWindowExpandAccepted (below) can check turn 2's
	// own redemption against the EXACT receipt this case recommended,
	// rather than merely "some window receipt was confirmed."
	WindowExpandOffered         bool
	WindowExpandOptionReceiptID string
	// TraceEvents (CHAOS-4183 phase "2c", team-lead ruling 2026-08-23) is
	// turn 1's own RAW ResolutionTraceEvent stream -- NOT populated by
	// twoTurnCaptureTurn1Facts itself (this struct is otherwise pure
	// scalar-summary fields, deliberately). Set by the ONE call site that
	// has both the shared trace capture and the corpus INDEX in scope
	// (TestChaos3742TwoTurnConfirmationReplay, immediately after
	// twoTurnCaptureTurn1Facts runs, before the first arm's own
	// trace.reset()), and ONLY when that index appears in
	// twoTurnForceTraceIndices' own set -- see that function's own doc
	// comment for the corpus-safety and debug-only discipline this field
	// exists under. nil on every ordinary run.
	TraceEvents []graphrank.ResolutionTraceEvent
}

const (
	twoTurnRegimeAWindowGated         = "regime_a_window_gated"
	twoTurnRegimeBResolutionProceeded = "regime_b_resolution_proceeded"
)

// twoTurnRegimeFromWindowCanonicalization is twoTurnTurn1Facts.Regime's own
// derivation, extracted as a pure function (mirrors twoTurnCommittedWrong/
// twoTurnPositiveFalseNoMatch's own precedent) for a direct unit-test
// surface. outcome is the LAST captured RecordWindowCanonicalization value
// for the call ("" if none was ever recorded).
//
// codex review round 2 (P3, confirmed): an earlier version of this function
// mapped every non-GatedClassDefault outcome to regime B on the (partly
// wrong) theory that every other WindowCanonicalizationOutcome value is
// structurally unreachable for turn 1's own bare headless request.
// windowVetoAxisConflict (window.go) is the counterexample -- unlike the
// other vetoes, it does NOT require a PriorWindowReceipts entry: it fires
// for a request_stated window (a time-window phrase the MODEL read out of
// the corpus question's own free text, precedence step 1) whose axis the
// model's own Interpret step then moved away from current -- a real corpus
// question could trigger this with no receipt involved at all. Regime B is
// team-lead's own positive claim that "turn-1 PROCEEDED THROUGH SUBJECT
// RESOLUTION" -- an axis-conflict veto (or any other veto/gate-1/refused-
// no-clarification outcome) is exactly as untrue for that claim as it is
// for regime A's "the class-default gate fired" one, so none of them may
// silently read as either. They classify as unobserved ("") instead --
// this predicate is not the whole story for those rows anyway; ArmInvalidReason/
// Turn1Status/Turn2Status still carry what actually happened.
func twoTurnRegimeFromWindowCanonicalization(outcome contextfabric.WindowCanonicalizationOutcome) string {
	switch outcome {
	case contextfabric.WindowCanonicalizationGatedClassDefault:
		return twoTurnRegimeAWindowGated
	case contextfabric.WindowCanonicalizationNone,
		contextfabric.WindowCanonicalizationRequestStated,
		contextfabric.WindowCanonicalizationReceiptConfirmed,
		contextfabric.WindowCanonicalizationInferredDefault:
		return twoTurnRegimeBResolutionProceeded
	default:
		// "" (never recorded), gate 1 (ExplicitUnconfirmed),
		// GatedRefusedNoClarification, and every Veto* value: none of these
		// is "the class-default gate fired" or "resolution proceeded
		// ordinarily" -- see this function's own doc comment.
		return ""
	}
}

// twoTurnWindowGateOutcome is the closed partition a window member's
// inferred_tier arm result falls into for WindowArmErrorCount/
// WindowInferredTierRanCount/WindowCommitCount/WindowGatedCount/
// WindowGatedOfferedCount/WindowGatedSilentCount. Extracted from the
// reporting loop (chaos3742_two_turn_confirmation_test.go) into
// twoTurnClassifyWindowGateOutcome so that function's own logic has a
// direct unit test, independent of running the full replay.
type twoTurnWindowGateOutcome struct {
	ArmError           bool
	Committed          bool
	Gated              bool
	GatedOffered       bool
	GatedAlreadyWidest bool
	// GatedSilent is NOT a field here -- callers compute it as
	// Gated && !GatedOffered && !GatedAlreadyWidest, the same way the
	// reporting site's own switch does, so there is exactly one place
	// (the switch) that can disagree with itself.
}

// twoTurnClassifyWindowGateOutcome (CHAOS-4336, fixing a defect present
// since CHAOS-4314's own merge, #288) classifies inferred -- the
// inferred_tier arm's OWN gated call (member=="window" only; sets
// TimeContext.EvidenceWindow to the case's NegativeWindowBand) -- entirely
// from ITS OWN fields. Before this ticket, the reporting site read
// turn1Facts.WindowExpandOffered here instead, on the claim that turn1Facts
// "is this entry's own (member=='window') turn-1 facts... WindowExpandOffered
// here is exactly the window gate's own recommendation on the SAME call
// that gated this row." That claim was FALSE: turn1Facts comes from the
// ONE shared, window-blind turn1 call every arm's row copies
// (twoTurnStampTurn1Facts, 4 call sites) -- NOT from inferred's own call,
// which is what the Gated condition below is actually evaluated against.
// turn1 sets no TimeContext.EvidenceWindow at all, so it frequently never
// reaches ANY window gate (window canonicalization outcome=none)
// regardless of what inferred's own gated call did -- undercounting
// WindowGatedOfferedCount and inflating WindowGatedSilentCount for every
// such case. Confirmed live: CHAOS-4314's Run C (9/65) and CHAOS-4336's own
// Run D (12/65, post-#290) both showed 100% of their "silent" rows sharing
// this exact turn1-never-gated signature; a live debug trace on case 11
// (Run D) proved inferred's own call was correctly gated while turn1
// disclosed no window need at all. Fixed by reading
// inferred.InferredWindowExpandOffered (computed directly off inferred's
// own result in runTwoTurnInferredTierArm) here instead.
//
// GatedAlreadyWidest (follow-up, same ticket): a gated call whose own
// effective window is already the registry's widest tier (all_time) has
// nothing wider pickWindowExpandTarget could ever recommend -- a
// legitimate non-offer, not a defect. Run E (16-shard kiac, tip a5f5f900)
// measured window_gated_silent=2/65 with both remaining rows (cases 53,
// 56) confirmed all_time via a by-hand annex cross-check; this field
// makes that partition code-level so WindowGatedSilentCount itself reads
// 0 on a genuinely clean run, no side lookup required.
func twoTurnClassifyWindowGateOutcome(inferred twoTurnCaseResult) twoTurnWindowGateOutcome {
	if inferred.ArmInvalidReason != "" {
		return twoTurnWindowGateOutcome{ArmError: true}
	}
	outcome := twoTurnWindowGateOutcome{Committed: inferred.CommittedCount > 0}
	if inferred.Turn2Status == string(contractsv1.ContextFabricInvestigationClarificationRequired) &&
		inferred.TierRoutedCorrectly && inferred.CommittedCount == 0 {
		outcome.Gated = true
		switch {
		case inferred.InferredWindowExpandOffered:
			outcome.GatedOffered = true
		case inferred.InferredWindowAlreadyWidest:
			outcome.GatedAlreadyWidest = true
		}
	}
	return outcome
}

// twoTurnCaptureTurn1Facts reads turn1Facts off turn 1's own result and its
// (about-to-be-reset) trace. Safe to call with a nil trace (every field the
// trace would have populated stays at its zero value, the same "not
// evaluated" reading every other trace-derived field in this file uses).
func twoTurnCaptureTurn1Facts(trace *twoTurnTraceCapture, turn1 contractsv1.ContextFabricInvestigationResult, tc trialCase) twoTurnTurn1Facts {
	var facts twoTurnTurn1Facts
	if turn1.StructureNeeds != nil {
		facts.AnchorOptionsCount = len(turn1.StructureNeeds.AnchorOptions)
		facts.HandleOptionsCount = len(turn1.StructureNeeds.HandleOptions)
	}
	if trace == nil {
		return facts
	}
	// CHAOS-4096: same resolution-wide-not-per-subject reasoning as
	// twoTurnStampDecision's own identical fields -- the last captured
	// event stays fully representative.
	if events := trace.finalDecisionEvents(); len(events) > 0 {
		event := events[len(events)-1]
		facts.CommitGate = event.CommitGate
		facts.TiedStatisticalTop = event.TiedStatisticalTop
		facts.SearchTruncated = event.SearchTruncated
	}
	if event, ok := trace.kindCoverageFloorEvent(); ok {
		facts.KindCoverageFloorFired = event.KindCoverageFloorFired
		facts.KindCoverageMissingKinds = event.KindCoverageMissingKinds
		facts.KindCoverageFloorTruncated = event.KindCoverageFloorTruncated
		facts.KindCoverageMissingKindsList = event.KindCoverageMissingKindsList
	}
	if event, ok := trace.confirmedKindRescueEvent(); ok {
		facts.ConfirmedKindRescueFired = event.ConfirmedKindRescueFired
		facts.ConfirmedKindRescueResultCount = event.ConfirmedKindRescueResultCount
		facts.ConfirmedKindRescueTruncated = event.ConfirmedKindRescueTruncated
	}
	if event, ok := trace.kindOfferEvent(); ok {
		facts.KindOfferExplicitHintCount = event.KindOfferExplicitHintCount
		facts.KindOfferDistinctKindCount = event.KindOfferDistinctKindCount
		facts.KindOfferSuppressedByCardinality = event.KindOfferSuppressedByCardinality
		facts.CandidateOfferCount = event.KindOfferCandidateOfferCount
		facts.OfferKind = event.KindOfferOfferKind
		facts.KindOfferDistinctKindCountBeforeRepair = event.KindOfferDistinctKindCountBeforeRepair
		facts.KindOfferSuppressedByCardinalityBeforeRepair = event.KindOfferSuppressedByCardinalityBeforeRepair
		facts.HandleOfferGraphDerivedCount = event.HandleOfferGraphDerivedCount
		facts.HandleOfferGraphDerivedRejectedCount = event.HandleOfferGraphDerivedRejectedCount
		facts.HandleOptionsCountBeforeGraphSource = event.HandleOfferCountBeforeGraphSource
		facts.OfferComposedUnderWindowGate = event.OfferedUnderWindowGate
	}
	facts.TermSearchTruncated, facts.QuestionSearchTruncated = trace.passTruncation()
	facts.ExpectedInPool = trace.poolContainsKind(tc.ExpectKind)
	facts.ExpectedKindAtOfferBoundary = trace.boundaryContainsKind(tc.ExpectKind)
	facts.ExpectedKindAtOfferBoundaryBeforeRepair = trace.boundaryContainsKindBeforeRepair(tc.ExpectKind)
	facts.ExpectedSubjectInPool = trace.poolContainsSubject(tc.ExpectKind, tc.ExpectID)
	facts.ExpectedSubjectRank, facts.ExpectedSubjectAtOfferBoundary = trace.rankedCutFor(tc.ExpectKind, tc.ExpectID)
	facts.ExpectedSubjectRetrievalSource = trace.retrievalSourceFor(tc.ExpectKind, tc.ExpectID)
	facts.OracleIDSchemeMismatch = oracleIDSchemeMismatch(tc.ExpectKind, tc.ExpectID)
	facts.CensusRan = trace.censusRan()
	facts.CensusComplete = trace.censusComplete()
	facts.CensusCount = trace.censusCount()
	if event, ok := trace.evidenceRoundEvent(); ok {
		facts.EvidenceRoundEntered = true
		facts.EvidenceRoundReason = event.ShadowReason
	}
	facts.Regime = twoTurnRegimeFromWindowCanonicalization(trace.windowCanonicalization)
	// CHAOS-4314: read directly off turn1's own composed StructureNeeds --
	// see this field's own doc comment for why (no trace event carries it).
	if turn1.StructureNeeds != nil && len(turn1.StructureNeeds.WindowExpandOptions) > 0 {
		facts.WindowExpandOffered = true
		facts.WindowExpandOptionReceiptID = turn1.StructureNeeds.WindowExpandOptions[0].ReceiptID
	}
	return facts
}

// twoTurnStampTurn1Facts writes facts onto res unconditionally -- every arm
// for this case gets the SAME turn-1 facts, the same "every row for this
// case shares one turn 1" sharing turn1SynthesisOverride's own fold already
// assumes, just without a severity gate: these are pure observations of a
// call that already completed before any arm ran, so there is nothing to
// arbitrate between "earlier" and "later" the way a synthesis-status
// override's severity does.
func twoTurnStampTurn1Facts(res *twoTurnCaseResult, facts twoTurnTurn1Facts) {
	if res == nil {
		return
	}
	res.Turn1CommitGate = facts.CommitGate
	res.Turn1TiedStatisticalTop = facts.TiedStatisticalTop
	res.Turn1SearchTruncated = facts.SearchTruncated
	res.Turn1KindCoverageFloorFired = facts.KindCoverageFloorFired
	res.Turn1KindCoverageMissingKinds = facts.KindCoverageMissingKinds
	res.Turn1KindCoverageFloorTruncated = facts.KindCoverageFloorTruncated
	res.Turn1KindCoverageMissingKindsList = facts.KindCoverageMissingKindsList
	res.Turn1ConfirmedKindRescueFired = facts.ConfirmedKindRescueFired
	res.Turn1ConfirmedKindRescueResultCount = facts.ConfirmedKindRescueResultCount
	res.Turn1ConfirmedKindRescueTruncated = facts.ConfirmedKindRescueTruncated
	res.Turn1KindOfferExplicitHintCount = facts.KindOfferExplicitHintCount
	res.Turn1KindOfferDistinctKindCount = facts.KindOfferDistinctKindCount
	res.Turn1KindOfferSuppressedByCardinality = facts.KindOfferSuppressedByCardinality
	res.Turn1KindOfferDistinctKindCountBeforeRepair = facts.KindOfferDistinctKindCountBeforeRepair
	res.Turn1KindOfferSuppressedByCardinalityBeforeRepair = facts.KindOfferSuppressedByCardinalityBeforeRepair
	res.Turn1CandidateOfferCount = facts.CandidateOfferCount
	res.Turn1OfferKind = facts.OfferKind
	res.Turn1HandleOfferGraphDerivedCount = facts.HandleOfferGraphDerivedCount
	res.Turn1HandleOfferGraphDerivedRejectedCount = facts.HandleOfferGraphDerivedRejectedCount
	res.Turn1TermSearchTruncated = facts.TermSearchTruncated
	res.Turn1QuestionSearchTruncated = facts.QuestionSearchTruncated
	res.ExpectedInPool = facts.ExpectedInPool
	res.ExpectedKindAtOfferBoundary = facts.ExpectedKindAtOfferBoundary
	res.ExpectedKindAtOfferBoundaryBeforeRepair = facts.ExpectedKindAtOfferBoundaryBeforeRepair
	res.Turn1OfferComposedUnderWindowGate = facts.OfferComposedUnderWindowGate
	res.ExpectedSubjectInPool = facts.ExpectedSubjectInPool
	res.ExpectedSubjectRank = facts.ExpectedSubjectRank
	res.ExpectedSubjectAtOfferBoundary = facts.ExpectedSubjectAtOfferBoundary
	res.ExpectedSubjectRetrievalSource = facts.ExpectedSubjectRetrievalSource
	res.OracleIDSchemeMismatch = facts.OracleIDSchemeMismatch
	res.AnchorOptionsCount = facts.AnchorOptionsCount
	res.HandleOptionsCount = facts.HandleOptionsCount
	res.HandleOptionsCountBeforeGraphSource = facts.HandleOptionsCountBeforeGraphSource
	res.CensusRan = facts.CensusRan
	res.CensusComplete = facts.CensusComplete
	res.CensusCount = facts.CensusCount
	res.EvidenceRoundEntered = facts.EvidenceRoundEntered
	res.EvidenceRoundReason = facts.EvidenceRoundReason
	res.Regime = facts.Regime
	res.Turn1WindowExpandOffered = facts.WindowExpandOffered
	// CHAOS-4183 phase "2c": nil on every ordinary run (facts.TraceEvents is
	// only ever non-nil when the call site force-traced this case) -- see
	// twoTurnTurn1Facts.TraceEvents' own doc comment.
	res.Turn1TraceEvents = facts.TraceEvents
}

// TestTwoTurnTraceCapturePassTruncation pins passTruncation's own per-pass
// discrimination: a "search" event's Truncated must never leak into the
// question reading and vice versa, and kind_coverage_floor (read via
// kindCoverageFloorEvent elsewhere, not this method) must not be mistaken
// for either.
func TestTwoTurnTraceCapturePassTruncation(t *testing.T) {
	t.Parallel()
	t.Run("neither pass truncated", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "search", Truncated: false},
			{Stage: "search_question", Truncated: false},
		}}
		term, question := trace.passTruncation()
		if term || question {
			t.Errorf("passTruncation() = (%v, %v), want (false, false)", term, question)
		}
	})
	t.Run("only the term search pass truncated", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "search", Truncated: true},
			{Stage: "search_question", Truncated: false},
		}}
		term, question := trace.passTruncation()
		if !term || question {
			t.Errorf("passTruncation() = (%v, %v), want (true, false)", term, question)
		}
	})
	t.Run("only the question pass truncated", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "search", Truncated: false},
			{Stage: "search_question", Truncated: true},
		}}
		term, question := trace.passTruncation()
		if term || !question {
			t.Errorf("passTruncation() = (%v, %v), want (false, true)", term, question)
		}
	})
	t.Run("ANY per-term search event truncating counts, not just the last", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "search", Truncated: true},
			{Stage: "search", Truncated: false},
		}}
		term, _ := trace.passTruncation()
		if !term {
			t.Error("passTruncation() term = false, want true when ANY search-stage event truncated")
		}
	})
	t.Run("kind_coverage_floor truncation does not leak into either reading", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "kind_coverage_floor", KindCoverageFloorTruncated: true},
		}}
		term, question := trace.passTruncation()
		if term || question {
			t.Errorf("passTruncation() = (%v, %v), want (false, false) -- kind_coverage_floor is a THIRD, separate pass", term, question)
		}
	})
}

// TestTwoTurnTraceCaptureSnapshot (CHAOS-4135) pins snapshot()'s two
// properties: nil in yields nil out (so an empty capture never turns into a
// non-nil-but-empty slice that would defeat omitempty on the serialized
// field), and a NON-nil result is an independent copy -- a later reset()+
// Trace() on the SAME *twoTurnTraceCapture (runTwoTurnInferredTierArm's
// baseline-then-hinted sequence, in particular) must never retroactively
// mutate a snapshot a caller already stashed on an earlier row.
func TestTwoTurnTraceCaptureSnapshot(t *testing.T) {
	t.Parallel()
	t.Run("empty capture snapshots to nil", func(t *testing.T) {
		trace := &twoTurnTraceCapture{}
		if got := trace.snapshot(); got != nil {
			t.Errorf("snapshot() = %#v, want nil", got)
		}
	})
	t.Run("snapshot is independent of later mutation", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{{Stage: "decision", Outcome: "committed"}}}
		baseline := trace.snapshot()
		trace.reset()
		trace.Trace(graphrank.ResolutionTraceEvent{Stage: "decision", Outcome: "ambiguous"})
		if len(baseline) != 1 || baseline[0].Outcome != "committed" {
			t.Errorf("baseline snapshot mutated by a later call: %+v", baseline)
		}
		hinted := trace.snapshot()
		if len(hinted) != 1 || hinted[0].Outcome != "ambiguous" {
			t.Errorf("hinted snapshot = %+v, want the later call's own event", hinted)
		}
	})
	// CHAOS-4135 codex xhigh review (HIGH, confirmed): ResolutionTraceEvent's
	// own doc comment claims Subject carries "never a label", but
	// contextfabric.SubjectRef.Label has no such enforcement and DOES carry
	// real content in production -- this function is what makes that
	// content-safe once it reaches a persisted artifact, so this pin is
	// load-bearing, not incidental.
	t.Run("Subject.Label is stripped, Kind/CanonicalID survive", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{{
			Stage: "corroboration",
			Subject: contractsv1.ContextFabricSubjectRef{
				Kind: "work_item", CanonicalID: "work_item:linear:CHAOS-1", Label: "a real subject title from the corpus",
			},
		}}}
		got := trace.snapshot()
		if len(got) != 1 {
			t.Fatalf("snapshot() returned %d events, want 1", len(got))
		}
		if got[0].Subject.Label != "" {
			t.Errorf("Subject.Label = %q, want stripped to empty", got[0].Subject.Label)
		}
		if got[0].Subject.Kind != "work_item" || got[0].Subject.CanonicalID != "work_item:linear:CHAOS-1" {
			t.Errorf("Subject Kind/CanonicalID = %+v, want untouched", got[0].Subject)
		}
	})
}

// TestTwoTurnResponderModel (CHAOS-4135) pins the fallback: unset (or
// blank/whitespace-only) reads as the literal "ambient-default", never
// empty -- an empty ResponderModel would be indistinguishable from the
// real_api transport, which never populates it at all (see this field's
// call site: gated on exchangeDir != "").
func TestTwoTurnResponderModel(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		if got := twoTurnResponderModel(); got != "ambient-default" {
			t.Errorf("twoTurnResponderModel() = %q, want %q", got, "ambient-default")
		}
	})
	t.Run("blank", func(t *testing.T) {
		t.Setenv("ACR_TEST_TRIAL_RESPONDER_MODEL", "   ")
		if got := twoTurnResponderModel(); got != "ambient-default" {
			t.Errorf("twoTurnResponderModel() = %q, want %q for a whitespace-only value", got, "ambient-default")
		}
	})
	t.Run("set", func(t *testing.T) {
		t.Setenv("ACR_TEST_TRIAL_RESPONDER_MODEL", "gpt-5.6-sol")
		if got := twoTurnResponderModel(); got != "gpt-5.6-sol" {
			t.Errorf("twoTurnResponderModel() = %q, want the explicit value", got)
		}
	})
}

// TestTwoTurnResponderTransport (CHAOS-4313) mirrors TestTwoTurnResponderModel's
// own pattern: unset (or blank/whitespace-only) reads as "api" -- the
// CHAOS-4313 cutover default scripts/trial/common.sh's own
// trial_responder_transport already applies -- never empty, which would be
// indistinguishable from the real_api transport (this field, like
// ResponderModel, is gated on exchangeDir != "" at its call site).
func TestTwoTurnResponderTransport(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		if got := twoTurnResponderTransport(); got != "api" {
			t.Errorf("twoTurnResponderTransport() = %q, want %q", got, "api")
		}
	})
	t.Run("blank", func(t *testing.T) {
		t.Setenv("ACR_TEST_TRIAL_RESPONDER_TRANSPORT", "   ")
		if got := twoTurnResponderTransport(); got != "api" {
			t.Errorf("twoTurnResponderTransport() = %q, want %q for a whitespace-only value", got, "api")
		}
	})
	t.Run("set", func(t *testing.T) {
		t.Setenv("ACR_TEST_TRIAL_RESPONDER_TRANSPORT", "codex")
		if got := twoTurnResponderTransport(); got != "codex" {
			t.Errorf("twoTurnResponderTransport() = %q, want the explicit value", got)
		}
	})
}

// TestTwoTurnResponderEffort (CHAOS-4313 follow-up) differs from
// TestTwoTurnResponderModel/TestTwoTurnResponderTransport immediately above
// on purpose: unset (or blank/whitespace-only) reads as the EMPTY string,
// never a substituted placeholder -- see twoTurnResponderEffort's own doc
// comment for why an empty value is itself the correct provenance record
// here, not a gap needing a stand-in like "ambient-default"/"api".
func TestTwoTurnResponderEffort(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		if got := twoTurnResponderEffort(); got != "" {
			t.Errorf("twoTurnResponderEffort() = %q, want empty", got)
		}
	})
	t.Run("blank", func(t *testing.T) {
		t.Setenv("ACR_TEST_TRIAL_RESPONDER_EFFORT", "   ")
		if got := twoTurnResponderEffort(); got != "" {
			t.Errorf("twoTurnResponderEffort() = %q, want empty for a whitespace-only value", got)
		}
	})
	t.Run("set", func(t *testing.T) {
		t.Setenv("ACR_TEST_TRIAL_RESPONDER_EFFORT", "xhigh")
		if got := twoTurnResponderEffort(); got != "xhigh" {
			t.Errorf("twoTurnResponderEffort() = %q, want the explicit value", got)
		}
	})
}

// TestTwoTurnResponderProvenance_EffortNeverRecordedForCodexTransport is the
// codex xhigh review round 1 (High, confirmed) red-first proof: an operator
// who sets ACR_TEST_TRIAL_RESPONDER_EFFORT while
// ACR_TEST_TRIAL_RESPONDER_TRANSPORT=codex must NOT get a report claiming
// that effort tier answered every call -- run-responder-codex.sh never
// reads the var at all, so recording it would be false provenance.
func TestTwoTurnResponderProvenance_EffortNeverRecordedForCodexTransport(t *testing.T) {
	t.Setenv("ACR_TEST_TRIAL_RESPONDER_TRANSPORT", "codex")
	t.Setenv("ACR_TEST_TRIAL_RESPONDER_EFFORT", "xhigh")
	_, _, transport, effort := twoTurnResponderProvenance(t, "/some/exchange/dir")
	if transport != "codex" {
		t.Fatalf("responderTransport = %q, want %q", transport, "codex")
	}
	if effort != "" {
		t.Errorf("responderEffort = %q, want empty -- run-responder-codex.sh never reads ACR_TEST_TRIAL_RESPONDER_EFFORT, so recording it here is false provenance", effort)
	}
}

// TestTwoTurnResponderProvenance_EffortRecordedForAPITransport is the
// companion positive case: the SAME env var, under transport=api, must be
// recorded -- this is the transport that actually wires it into a request
// (cmd/acr-trial-responder-api).
func TestTwoTurnResponderProvenance_EffortRecordedForAPITransport(t *testing.T) {
	t.Setenv("ACR_TEST_TRIAL_RESPONDER_TRANSPORT", "api")
	t.Setenv("ACR_TEST_TRIAL_RESPONDER_EFFORT", "xhigh")
	_, _, transport, effort := twoTurnResponderProvenance(t, "/some/exchange/dir")
	if transport != "api" {
		t.Fatalf("responderTransport = %q, want %q", transport, "api")
	}
	if effort != "xhigh" {
		t.Errorf("responderEffort = %q, want %q", effort, "xhigh")
	}
}

// fakeFatalfer records whether Fatalf was called, without genuinely
// failing (no runtime.Goexit) -- lets
// TestTwoTurnResponderProvenance_RejectsMalformedEffort assert the call
// happened rather than relying on t.Run's subtest-failure propagation,
// which would mark the asserting test itself as failed too.
type fakeFatalfer struct {
	called bool
	msg    string
}

func (f *fakeFatalfer) Helper() {}
func (f *fakeFatalfer) Fatalf(format string, args ...any) {
	f.called = true
	f.msg = fmt.Sprintf(format, args...)
}

// TestTwoTurnResponderProvenance_RejectsMalformedEffort is the codex xhigh
// review round 2 (Medium, confirmed) red-first proof: a malformed
// ACR_TEST_TRIAL_RESPONDER_EFFORT under transport=api must fail this
// process closed (Fatalf) BEFORE it can be persisted into a report
// artifact -- round 1's resolveResponderEffort only bounded the value the
// SEPARATE responder-binary process sends/logs; this closes the identical
// gap on the provenance side, in THIS process, which reads the same raw
// env var independently.
func TestTwoTurnResponderProvenance_RejectsMalformedEffort(t *testing.T) {
	t.Setenv("ACR_TEST_TRIAL_RESPONDER_TRANSPORT", "api")
	t.Setenv("ACR_TEST_TRIAL_RESPONDER_EFFORT", "not a valid tier!!")
	fake := &fakeFatalfer{}
	twoTurnResponderProvenance(fake, "/some/exchange/dir")
	if !fake.called {
		t.Fatal("twoTurnResponderProvenance did not call Fatalf on a malformed ACR_TEST_TRIAL_RESPONDER_EFFORT value -- it must fail closed before returning, not silently pass the value through")
	}
	if !strings.Contains(fake.msg, "ACR_TEST_TRIAL_RESPONDER_EFFORT") {
		t.Errorf("Fatalf message = %q, want it to name ACR_TEST_TRIAL_RESPONDER_EFFORT", fake.msg)
	}
}

// TestTwoTurnResponderProvenance_RealAPITransportRecordsNothing pins the
// pre-existing real_api (exchangeDir == "") shape: no responder script
// runs at all under this transport, so nothing here should ever be
// attributed to a model/transport/effort.
func TestTwoTurnResponderProvenance_RealAPITransportRecordsNothing(t *testing.T) {
	label, model, transport, effort := twoTurnResponderProvenance(t, "")
	if label != "real_api" {
		t.Fatalf("transportLabel = %q, want %q", label, "real_api")
	}
	if model != "" || transport != "" || effort != "" {
		t.Errorf("model/transport/effort = %q/%q/%q, want all empty for real_api", model, transport, effort)
	}
}

// TestTwoTurnTraceCapturePoolContainsKind pins CHAOS-4038's exact
// distinction: a corroboration-stage event for the expected kind means it
// reached the merged pool, regardless of what the (possibly absent)
// decision event says; an empty kind never matches anything.
func TestTwoTurnTraceCapturePoolContainsKind(t *testing.T) {
	t.Parallel()
	trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
		{Stage: "corroboration", Subject: contractsv1.ContextFabricSubjectRef{Kind: "repository"}},
		{Stage: "corroboration", Subject: contractsv1.ContextFabricSubjectRef{Kind: "work_item"}},
	}}
	if !trace.poolContainsKind("repository") {
		t.Error("poolContainsKind(repository) = false, want true (a corroboration event for that kind was traced)")
	}
	if trace.poolContainsKind("team") {
		t.Error("poolContainsKind(team) = true, want false (no corroboration event named that kind)")
	}
	if trace.poolContainsKind("") {
		t.Error(`poolContainsKind("") = true, want false (an empty kind matches nothing)`)
	}
	if (&twoTurnTraceCapture{}).poolContainsKind("repository") {
		t.Error("poolContainsKind on an empty trace = true, want false")
	}
}

// TestTwoTurnTraceCaptureCensusCount pins the "attested zero vs never ran"
// distinction censusCount exists to draw alongside censusRan(): summed
// across every evidence_probe event (per-kind, CHAOS-3899's own
// cardinality), never just the last.
func TestTwoTurnTraceCaptureCensusCount(t *testing.T) {
	t.Parallel()
	t.Run("never ran", func(t *testing.T) {
		trace := &twoTurnTraceCapture{}
		if trace.censusRan() {
			t.Error("censusRan() = true on an empty trace, want false")
		}
		if trace.censusComplete() {
			t.Error("censusComplete() = true on an empty trace, want false (nothing ran, so nothing completed)")
		}
		if got := trace.censusCount(); got != 0 {
			t.Errorf("censusCount() = %d, want 0 on an empty trace", got)
		}
	})
	t.Run("ran and attested zero rows for every kind", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "evidence_probe", CensusComplete: true, CensusCount: 0},
		}}
		if !trace.censusRan() {
			t.Error("censusRan() = false, want true (an evidence_probe event was traced)")
		}
		if !trace.censusComplete() {
			t.Error("censusComplete() = false, want true (the one probed kind completed)")
		}
		if got := trace.censusCount(); got != 0 {
			t.Errorf("censusCount() = %d, want 0 (ran, attested zero)", got)
		}
	})
	// codex review round 2 (P2, confirmed against chaos3899_evidence_round.go:
	// 556-558): a kind whose CensusFunc call ERRORED traces
	// CensusComplete=false with CensusCount left at zero -- indistinguishable
	// from a genuine zero-attestation by censusCount() alone. censusComplete()
	// is the required pairing that catches this.
	t.Run("one probed kind errored: NOT an attested absence", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "evidence_probe", CensusComplete: true, CensusCount: 2},
			{Stage: "evidence_probe", CensusComplete: false, CensusCount: 0},
		}}
		if !trace.censusRan() {
			t.Error("censusRan() = false, want true")
		}
		if trace.censusComplete() {
			t.Error("censusComplete() = true, want false -- one probed kind errored, so censusCount() cannot be read as a real attested absence")
		}
		if got := trace.censusCount(); got != 2 {
			t.Errorf("censusCount() = %d, want 2 (the errored kind contributes its zero-valued Count, same as it does in production)", got)
		}
	})
	t.Run("sums across multiple per-kind probes", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "evidence_probe", CensusComplete: true, CensusCount: 3},
			{Stage: "evidence_probe", CensusComplete: true, CensusCount: 5},
			{Stage: "decision", CensusCount: 99}, // wrong stage, must not be summed
		}}
		if got := trace.censusCount(); got != 8 {
			t.Errorf("censusCount() = %d, want 8 (3+5 across the two evidence_probe events, decision-stage noise excluded)", got)
		}
		if !trace.censusComplete() {
			t.Error("censusComplete() = false, want true (both probed kinds completed)")
		}
	})
}

// TestTwoTurnCaptureTurn1Facts pins twoTurnCaptureTurn1Facts's own
// aggregation: every source (decision trace, kind-coverage-floor trace,
// per-pass truncation, pool membership, StructureNeeds option counts,
// census) reaches the returned struct, and a nil trace degrades to
// "StructureNeeds-derived fields only, everything else zero" rather than
// panicking.
func TestTwoTurnCaptureTurn1Facts(t *testing.T) {
	t.Parallel()
	tc := trialCase{ExpectKind: "repository", ExpectID: "repository:r1"}

	t.Run("nil trace, no StructureNeeds", func(t *testing.T) {
		facts := twoTurnCaptureTurn1Facts(nil, contractsv1.ContextFabricInvestigationResult{}, tc)
		// CHAOS-4183 phase 2: twoTurnTurn1Facts gained a []string field
		// (KindCoverageMissingKindsList), which makes the struct no longer
		// `==`-comparable -- reflect.DeepEqual is the direct replacement,
		// same "the struct grew a slice" reasoning twoTurnCaseResult's own
		// field-by-field checks already document elsewhere in this file.
		if !reflect.DeepEqual(facts, twoTurnTurn1Facts{}) {
			t.Errorf("twoTurnCaptureTurn1Facts(nil trace) = %+v, want the zero value", facts)
		}
	})

	t.Run("StructureNeeds option counts survive a nil trace", func(t *testing.T) {
		turn1 := contractsv1.ContextFabricInvestigationResult{
			StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
				AnchorOptions: []contractsv1.ContextFabricAnchorOption{{OptionID: "a1"}},
				HandleOptions: []contractsv1.ContextFabricHandleOption{{OptionID: "h1"}, {OptionID: "h2"}},
			},
		}
		facts := twoTurnCaptureTurn1Facts(nil, turn1, tc)
		if facts.AnchorOptionsCount != 1 || facts.HandleOptionsCount != 2 {
			t.Errorf("AnchorOptionsCount=%d HandleOptionsCount=%d, want 1, 2", facts.AnchorOptionsCount, facts.HandleOptionsCount)
		}
	})

	t.Run("every trace-derived field is read from the shared capture", func(t *testing.T) {
		trace := &twoTurnTraceCapture{
			events: []graphrank.ResolutionTraceEvent{
				{Stage: "corroboration", Subject: contractsv1.ContextFabricSubjectRef{Kind: "repository"}},
				{Stage: "search", Truncated: true},
				{Stage: "search_question", Truncated: true},
				{Stage: "kind_coverage_floor", KindCoverageFloorFired: true, KindCoverageMissingKinds: 2, KindCoverageFloorTruncated: true,
					KindCoverageMissingKindsList: []string{"work_item", "repository"}},
				{Stage: "confirmed_kind_rescue", ConfirmedKindRescueFired: true, ConfirmedKindRescueResultCount: 1, ConfirmedKindRescueTruncated: true},
				{Stage: "kind_offer", KindOfferExplicitHintCount: 1, KindOfferDistinctKindCount: 2, KindOfferSuppressedByCardinality: false,
					KindOfferCandidateOfferCount: 5, KindOfferOfferKind: "both",
					KindOfferBoundaryKinds: []string{"repository"}},
				{Stage: "evidence_round", ShadowReason: ""},
				{Stage: "evidence_probe", CensusComplete: true, CensusCount: 4},
				{Stage: "decision", CommitGate: "lone_floor", TiedStatisticalTop: true, SearchTruncated: true},
			},
			windowCanonicalization: contextfabric.WindowCanonicalizationGatedClassDefault,
		}
		facts := twoTurnCaptureTurn1Facts(trace, contractsv1.ContextFabricInvestigationResult{}, tc)
		want := twoTurnTurn1Facts{
			CommitGate: "lone_floor", TiedStatisticalTop: true, SearchTruncated: true,
			KindCoverageFloorFired: true, KindCoverageMissingKinds: 2, KindCoverageFloorTruncated: true,
			KindCoverageMissingKindsList: []string{"work_item", "repository"},
			ConfirmedKindRescueFired:     true, ConfirmedKindRescueResultCount: 1, ConfirmedKindRescueTruncated: true,
			KindOfferExplicitHintCount: 1, KindOfferDistinctKindCount: 2, KindOfferSuppressedByCardinality: false,
			CandidateOfferCount: 5, OfferKind: "both",
			TermSearchTruncated: true, QuestionSearchTruncated: true,
			ExpectedInPool: true, ExpectedKindAtOfferBoundary: true, CensusRan: true, CensusComplete: true, CensusCount: 4,
			EvidenceRoundEntered: true, EvidenceRoundReason: "",
			Regime: twoTurnRegimeAWindowGated,
			// CHAOS-4348: the corroboration event above names kind
			// "repository" but no canonical id, so poolContainsSubject(tc)
			// -- which requires BOTH -- is false for tc.ExpectID
			// "repository:r1", and retrievalSourceFor's own first branch
			// reports "absent" for exactly that reason.
			ExpectedSubjectRetrievalSource: "absent",
		}
		// CHAOS-4183 phase 2: reflect.DeepEqual, same reason as the nil-trace
		// subtest above -- the struct grew a []string field.
		if !reflect.DeepEqual(facts, want) {
			t.Errorf("twoTurnCaptureTurn1Facts() = %+v, want %+v", facts, want)
		}
	})

	// CHAOS-4161: the exact case this ticket exists for -- the round is
	// ENTERED (an evidence_round event traces) but refuses BEFORE the
	// per-kind loop, so evidence_probe never fires and CensusRan stays
	// false. EvidenceRoundEntered must still read true, with the refusal's
	// own reason carried, proving this case is distinguishable from "the
	// round was never entered at all" (the next subtest).
	t.Run("evidence_round entered but refused before any census probe", func(t *testing.T) {
		trace := &twoTurnTraceCapture{
			events: []graphrank.ResolutionTraceEvent{
				{Stage: "search", Truncated: true},
				{Stage: "evidence_round", ShadowReason: string(graphrank.ReasonNoDiscriminators)},
				{Stage: "decision", CommitGate: "", SearchTruncated: true},
			},
		}
		facts := twoTurnCaptureTurn1Facts(trace, contractsv1.ContextFabricInvestigationResult{}, tc)
		if !facts.EvidenceRoundEntered {
			t.Error("EvidenceRoundEntered = false, want true -- an evidence_round event was traced")
		}
		if facts.EvidenceRoundReason != string(graphrank.ReasonNoDiscriminators) {
			t.Errorf("EvidenceRoundReason = %q, want %q", facts.EvidenceRoundReason, graphrank.ReasonNoDiscriminators)
		}
		if facts.CensusRan {
			t.Error("CensusRan = true, want false -- no evidence_probe event was traced")
		}
	})

	// The outer gate (resolve.go:1835) never entered the round at all --
	// e.g. deps.CensusFunc==nil, or resolution.Committed was non-empty, or
	// searchTruncated was false. No evidence_round event traces, so
	// EvidenceRoundEntered must read false and EvidenceRoundReason must
	// carry no meaning (stay at its zero value) -- distinct from the
	// "entered but refused" subtest above.
	t.Run("evidence_round never entered", func(t *testing.T) {
		trace := &twoTurnTraceCapture{
			events: []graphrank.ResolutionTraceEvent{
				{Stage: "search", Truncated: false},
				{Stage: "decision", CommitGate: "lone_floor"},
			},
		}
		facts := twoTurnCaptureTurn1Facts(trace, contractsv1.ContextFabricInvestigationResult{}, tc)
		if facts.EvidenceRoundEntered {
			t.Error("EvidenceRoundEntered = true, want false -- no evidence_round event was traced")
		}
		if facts.EvidenceRoundReason != "" {
			t.Errorf("EvidenceRoundReason = %q, want \"\" (carries no meaning when EvidenceRoundEntered is false)", facts.EvidenceRoundReason)
		}
	})

	// CHAOS-4012: the exact case this ticket investigates -- exactly one
	// distinct offerable kind survived (a CHAOS-4038 coverage-floor rescue
	// or an ordinary find, either way), the caller supplied no explicit
	// hint, and kindOfferMaterial's own cardinality gate suppressed the
	// offer. DistinctKindCount==1 is what distinguishes this from
	// "genuinely nothing offerable" (DistinctKindCount==0).
	t.Run("kind_offer suppressed with exactly one distinct kind in pool", func(t *testing.T) {
		trace := &twoTurnTraceCapture{
			events: []graphrank.ResolutionTraceEvent{
				{Stage: "kind_offer", KindOfferExplicitHintCount: 0, KindOfferDistinctKindCount: 1, KindOfferSuppressedByCardinality: true,
					KindOfferCandidateOfferCount: 5, KindOfferOfferKind: "candidate"},
				{Stage: "decision", CommitGate: ""},
			},
		}
		facts := twoTurnCaptureTurn1Facts(trace, contractsv1.ContextFabricInvestigationResult{}, tc)
		if facts.KindOfferExplicitHintCount != 0 {
			t.Errorf("KindOfferExplicitHintCount = %d, want 0", facts.KindOfferExplicitHintCount)
		}
		if facts.KindOfferDistinctKindCount != 1 {
			t.Errorf("KindOfferDistinctKindCount = %d, want 1 -- present, just not enough to clear the cardinality gate", facts.KindOfferDistinctKindCount)
		}
		if !facts.KindOfferSuppressedByCardinality {
			t.Error("KindOfferSuppressedByCardinality = false, want true")
		}
		// CHAOS-4012 v22: the exact scenario the candidate-list axis exists
		// for -- kind-pick suppressed, candidate-list still fires
		// independently.
		if facts.CandidateOfferCount != 5 {
			t.Errorf("CandidateOfferCount = %d, want 5", facts.CandidateOfferCount)
		}
		if facts.OfferKind != "candidate" {
			t.Errorf("OfferKind = %q, want %q", facts.OfferKind, "candidate")
		}
	})

	// CHAOS-4012 v22 (re-smoke follow-up, team-lead ruling 2026-08-23): the
	// exact gap that motivated ExpectedKindAtOfferBoundary -- a candidate
	// corroborates early (ExpectedInPool reads true) but is gone from the
	// exact slice kindOfferMaterial/candidateOfferMaterial read by the time
	// kind_offer fires (upstream truncation). ExpectedInPool must stay true
	// (it is trace-wide, unaffected by this change); ExpectedKindAtOfferBoundary
	// must read false, proving the two fields answer genuinely different
	// questions rather than one silently mirroring the other.
	t.Run("corroborated early but absent from the kind_offer call boundary", func(t *testing.T) {
		trace := &twoTurnTraceCapture{
			events: []graphrank.ResolutionTraceEvent{
				{Stage: "corroboration", Subject: contractsv1.ContextFabricSubjectRef{Kind: "repository"}},
				// The boundary slice survived with only "work_item" -- the
				// corroborated "repository" candidate did not make it this
				// far (e.g. dropped by MaxSubjectCandidates truncation
				// upstream).
				{Stage: "kind_offer", KindOfferDistinctKindCount: 1, KindOfferSuppressedByCardinality: true,
					KindOfferBoundaryKinds: []string{"work_item"}},
				{Stage: "decision", CommitGate: ""},
			},
		}
		facts := twoTurnCaptureTurn1Facts(trace, contractsv1.ContextFabricInvestigationResult{}, tc)
		if !facts.ExpectedInPool {
			t.Error("ExpectedInPool = false, want true -- a corroboration event for the expected kind was traced")
		}
		if facts.ExpectedKindAtOfferBoundary {
			t.Error("ExpectedKindAtOfferBoundary = true, want false -- the expected kind is absent from the boundary slice despite corroborating earlier")
		}
	})
}

// TestTwoTurnRegimeFromWindowCanonicalization pins CHAOS-4120's regime
// derivation: an EMPTY captured outcome ("never recorded") must stay
// unclassified rather than silently defaulting to regime B, since B's own
// definition ("the gate did not fire, resolution proceeded") is a positive
// claim about what DID happen, not merely the absence of A.
func TestTwoTurnRegimeFromWindowCanonicalization(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		outcome contextfabric.WindowCanonicalizationOutcome
		want    string
	}{
		{"never recorded stays unclassified", "", ""},
		{"gated class-default is regime A", contextfabric.WindowCanonicalizationGatedClassDefault, twoTurnRegimeAWindowGated},
		{"none (no window involvement) is regime B", contextfabric.WindowCanonicalizationNone, twoTurnRegimeBResolutionProceeded},
		{"request_stated is regime B", contextfabric.WindowCanonicalizationRequestStated, twoTurnRegimeBResolutionProceeded},
		{"receipt_confirmed is regime B", contextfabric.WindowCanonicalizationReceiptConfirmed, twoTurnRegimeBResolutionProceeded},
		{"inferred_default is regime B", contextfabric.WindowCanonicalizationInferredDefault, twoTurnRegimeBResolutionProceeded},
		// codex review round 2 (P3, confirmed): gate 1, the refused-no-
		// clarification outcome, and every Veto* value are neither "the
		// class-default gate fired" (regime A) nor "resolution proceeded
		// ordinarily" (regime B) -- see this function's own doc comment,
		// especially windowVetoAxisConflict, which is NOT receipt-gated and
		// so is not simply unreachable the way the others are for turn 1.
		// All of these must classify as unobserved rather than either
		// regime.
		{"gate 1 (explicit-unconfirmed) is unclassified, not regime B", contextfabric.WindowCanonicalizationGatedExplicitUnconfirmed, ""},
		{"refused-no-clarification is unclassified, not regime B", contextfabric.WindowCanonicalizationGatedRefusedNoClarification, ""},
		{"veto_unresolved is unclassified, not regime B", contextfabric.WindowCanonicalizationVetoUnresolved, ""},
		{"veto_conflict is unclassified, not regime B", contextfabric.WindowCanonicalizationVetoConflict, ""},
		{"veto_stale_superseded_offer is unclassified, not regime B", contextfabric.WindowCanonicalizationVetoStaleSupersededOffer, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := twoTurnRegimeFromWindowCanonicalization(tc.outcome); got != tc.want {
				t.Errorf("twoTurnRegimeFromWindowCanonicalization(%q) = %q, want %q", tc.outcome, got, tc.want)
			}
		})
	}
}

// TestTwoTurnTraceCaptureRecordWindowCanonicalization pins the capture
// method itself: it must record the LATEST outcome (mirroring
// RecordSynthesisStatusOverride's own "captures, does not accumulate"
// behavior) and reset() must clear it back to "never recorded" rather than
// leaving a PRIOR call's outcome to leak into the next case's regime.
func TestTwoTurnTraceCaptureRecordWindowCanonicalization(t *testing.T) {
	t.Parallel()
	trace := &twoTurnTraceCapture{SlogEngineTelemetry: contextfabric.NewSlogEngineTelemetry(nil)}
	if trace.windowCanonicalization != "" {
		t.Fatalf("windowCanonicalization = %q before any call, want \"\"", trace.windowCanonicalization)
	}
	trace.RecordWindowCanonicalization(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.WindowCanonicalizationGatedClassDefault)
	if trace.windowCanonicalization != contextfabric.WindowCanonicalizationGatedClassDefault {
		t.Fatalf("windowCanonicalization = %q, want %q", trace.windowCanonicalization, contextfabric.WindowCanonicalizationGatedClassDefault)
	}
	trace.RecordWindowCanonicalization(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.WindowCanonicalizationRequestStated)
	if trace.windowCanonicalization != contextfabric.WindowCanonicalizationRequestStated {
		t.Fatalf("windowCanonicalization = %q after a second call, want the LATEST outcome %q", trace.windowCanonicalization, contextfabric.WindowCanonicalizationRequestStated)
	}
	trace.reset()
	if trace.windowCanonicalization != "" {
		t.Fatalf("windowCanonicalization = %q after reset(), want \"\" -- a prior case's regime must never leak into the next", trace.windowCanonicalization)
	}
}

// TestTwoTurnStampTurn1Facts pins twoTurnStampTurn1Facts's own field-for-field
// write and its nil guard.
func TestTwoTurnStampTurn1Facts(t *testing.T) {
	t.Parallel()
	t.Run("nil res is a no-op, never panics", func(t *testing.T) {
		twoTurnStampTurn1Facts(nil, twoTurnTurn1Facts{CommitGate: "lone_floor"})
	})
	t.Run("every field lands on the row", func(t *testing.T) {
		facts := twoTurnTurn1Facts{
			CommitGate: "top_of_two", TiedStatisticalTop: true, SearchTruncated: true,
			KindCoverageFloorFired: true, KindCoverageMissingKinds: 3, KindCoverageFloorTruncated: true,
			// codex xhigh R2 (2026-08-23, LOW finding): three entries, matching
			// KindCoverageMissingKinds' count above -- production always keeps
			// them in lockstep (chaos4038_kind_coverage.go:270), so a
			// mismatched fixture could mask a future regression.
			KindCoverageMissingKindsList: []string{"work_item", "repository", "project"},
			ConfirmedKindRescueFired:     true, ConfirmedKindRescueResultCount: 1, ConfirmedKindRescueTruncated: true,
			KindOfferExplicitHintCount: 0, KindOfferDistinctKindCount: 1, KindOfferSuppressedByCardinality: true,
			CandidateOfferCount: 5, OfferKind: "candidate",
			TermSearchTruncated: true, QuestionSearchTruncated: true,
			ExpectedInPool: true, ExpectedKindAtOfferBoundary: true, AnchorOptionsCount: 1, HandleOptionsCount: 2,
			CensusRan: true, CensusComplete: true, CensusCount: 7,
			EvidenceRoundEntered: true, EvidenceRoundReason: string(graphrank.ReasonNoDiscriminators),
			Regime: twoTurnRegimeAWindowGated,
			// CHAOS-4183 phase "2c": a non-nil value here proves
			// twoTurnStampTurn1Facts copies it through -- an ordinary run
			// leaves this nil (twoTurnCaptureForcedTurn1Trace's own doc
			// comment), so a real value here is what proves the plumbing,
			// not merely the zero-value default.
			TraceEvents: []graphrank.ResolutionTraceEvent{{Stage: "decision", CommitGate: "top_of_two"}},
			// CHAOS-4348 measurement-layer fix: a non-default (true) value
			// here proves twoTurnStampTurn1Facts copies OracleIDSchemeMismatch
			// through, the same "prove the plumbing, not the zero-value
			// default" reasoning TraceEvents' own comment above already uses.
			OracleIDSchemeMismatch: true,
		}
		res := twoTurnCaseResult{}
		twoTurnStampTurn1Facts(&res, facts)
		// twoTurnCaseResult carries slice fields (CommittedSubjects etc.),
		// so it is not `==`-comparable -- check the CHAOS-4120 fields this
		// function actually writes, one by one, rather than the whole row.
		switch {
		case res.Turn1CommitGate != "top_of_two":
			t.Errorf("Turn1CommitGate = %q, want %q", res.Turn1CommitGate, "top_of_two")
		case !res.Turn1TiedStatisticalTop:
			t.Error("Turn1TiedStatisticalTop = false, want true")
		case !res.Turn1SearchTruncated:
			t.Error("Turn1SearchTruncated = false, want true")
		case !res.Turn1KindCoverageFloorFired:
			t.Error("Turn1KindCoverageFloorFired = false, want true")
		case res.Turn1KindCoverageMissingKinds != 3:
			t.Errorf("Turn1KindCoverageMissingKinds = %d, want 3", res.Turn1KindCoverageMissingKinds)
		case !res.Turn1KindCoverageFloorTruncated:
			t.Error("Turn1KindCoverageFloorTruncated = false, want true")
		// CHAOS-4183 phase 2 (codex xhigh review, LOW finding): this test
		// previously omitted the new list field entirely.
		case !reflect.DeepEqual(res.Turn1KindCoverageMissingKindsList, []string{"work_item", "repository", "project"}):
			t.Errorf("Turn1KindCoverageMissingKindsList = %v, want [work_item repository project]", res.Turn1KindCoverageMissingKindsList)
		case !res.Turn1ConfirmedKindRescueFired:
			t.Error("Turn1ConfirmedKindRescueFired = false, want true")
		case res.Turn1ConfirmedKindRescueResultCount != 1:
			t.Errorf("Turn1ConfirmedKindRescueResultCount = %d, want 1", res.Turn1ConfirmedKindRescueResultCount)
		case !res.Turn1ConfirmedKindRescueTruncated:
			t.Error("Turn1ConfirmedKindRescueTruncated = false, want true")
		case !res.Turn1TermSearchTruncated:
			t.Error("Turn1TermSearchTruncated = false, want true")
		case !res.Turn1QuestionSearchTruncated:
			t.Error("Turn1QuestionSearchTruncated = false, want true")
		case !res.ExpectedInPool:
			t.Error("ExpectedInPool = false, want true")
		case !res.ExpectedKindAtOfferBoundary:
			t.Error("ExpectedKindAtOfferBoundary = false, want true")
		case res.AnchorOptionsCount != 1:
			t.Errorf("AnchorOptionsCount = %d, want 1", res.AnchorOptionsCount)
		case res.HandleOptionsCount != 2:
			t.Errorf("HandleOptionsCount = %d, want 2", res.HandleOptionsCount)
		case !res.CensusRan:
			t.Error("CensusRan = false, want true")
		case !res.CensusComplete:
			t.Error("CensusComplete = false, want true")
		case res.CensusCount != 7:
			t.Errorf("CensusCount = %d, want 7", res.CensusCount)
		case res.Turn1KindOfferExplicitHintCount != 0:
			t.Errorf("Turn1KindOfferExplicitHintCount = %d, want 0", res.Turn1KindOfferExplicitHintCount)
		case res.Turn1KindOfferDistinctKindCount != 1:
			t.Errorf("Turn1KindOfferDistinctKindCount = %d, want 1", res.Turn1KindOfferDistinctKindCount)
		case !res.Turn1KindOfferSuppressedByCardinality:
			t.Error("Turn1KindOfferSuppressedByCardinality = false, want true")
		case res.Turn1CandidateOfferCount != 5:
			t.Errorf("Turn1CandidateOfferCount = %d, want 5", res.Turn1CandidateOfferCount)
		case res.Turn1OfferKind != "candidate":
			t.Errorf("Turn1OfferKind = %q, want %q", res.Turn1OfferKind, "candidate")
		case !res.EvidenceRoundEntered:
			t.Error("EvidenceRoundEntered = false, want true")
		case res.EvidenceRoundReason != string(graphrank.ReasonNoDiscriminators):
			t.Errorf("EvidenceRoundReason = %q, want %q", res.EvidenceRoundReason, graphrank.ReasonNoDiscriminators)
		case res.Regime != twoTurnRegimeAWindowGated:
			t.Errorf("Regime = %q, want %q", res.Regime, twoTurnRegimeAWindowGated)
		case !reflect.DeepEqual(res.Turn1TraceEvents, facts.TraceEvents):
			t.Errorf("Turn1TraceEvents = %+v, want %+v", res.Turn1TraceEvents, facts.TraceEvents)
		case !res.OracleIDSchemeMismatch:
			t.Error("OracleIDSchemeMismatch = false, want true")
		}
	})
}

// TestTwoTurnForceTraceIndices pins ACR_TEST_TRIAL_FORCE_TRACE_INDICES'
// parsing -- same shape as twoTurnShardCaseIndices' own contract
// (comma-separated, fail-closed on a malformed value), deliberately
// simpler: membership only, no "none" sentinel (a debug knob, not a
// correctness-load-bearing sharding assignment).
func TestTwoTurnForceTraceIndices(t *testing.T) {
	t.Run("unset returns nil", func(t *testing.T) {
		t.Setenv("ACR_TEST_TRIAL_FORCE_TRACE_INDICES", "")
		if got := twoTurnForceTraceIndices(t); got != nil {
			t.Errorf("twoTurnForceTraceIndices() = %v, want nil", got)
		}
	})
	t.Run("parses a comma-separated list", func(t *testing.T) {
		t.Setenv("ACR_TEST_TRIAL_FORCE_TRACE_INDICES", "5, 18, 12")
		got := twoTurnForceTraceIndices(t)
		want := map[int]struct{}{5: {}, 18: {}, 12: {}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("twoTurnForceTraceIndices() = %v, want %v", got, want)
		}
	})
}

// TestParseTwoTurnForceTraceIndices pins the pure parser's own fail-closed
// path directly -- see parseTwoTurnForceTraceIndices' own doc comment for
// why this lives on the pure function rather than twoTurnForceTraceIndices
// itself (t.Fatal is unsafe to provoke under direct test invocation).
func TestParseTwoTurnForceTraceIndices(t *testing.T) {
	t.Parallel()
	// codex xhigh R1 (2026-08-23, LOW finding): a value that is SET but
	// names nothing must fail loud, not silently return an empty (nil-like)
	// set indistinguishable from "unset".
	for _, raw := range []string{",", " , ", ",,,"} {
		if _, err := parseTwoTurnForceTraceIndices(raw); err == nil {
			t.Errorf("parseTwoTurnForceTraceIndices(%q) = nil error, want a fail-closed error -- a launcher that meant to force a capture and got the syntax wrong must never silently run an ordinary, uncaptured pass instead", raw)
		}
	}
	if _, err := parseTwoTurnForceTraceIndices("5,not-a-number"); err == nil {
		t.Error("parseTwoTurnForceTraceIndices(\"5,not-a-number\") = nil error, want a fail-closed error")
	}
	if _, err := parseTwoTurnForceTraceIndices(""); err != nil {
		t.Errorf("parseTwoTurnForceTraceIndices(\"\") = %v, want nil error (unset is not a malformed value)", err)
	}
}

// TestTwoTurnCaptureForcedTurn1Trace pins the ONE decision point
// ACR_TEST_TRIAL_FORCE_TRACE_INDICES controls: a nil trace, an unforced
// index, and a forced index each produce the exact output
// twoTurnCaseResult.Turn1TraceEvents' own doc comment promises.
func TestTwoTurnCaptureForcedTurn1Trace(t *testing.T) {
	t.Parallel()
	forceIndices := map[int]struct{}{18: {}}
	t.Run("nil trace returns nil regardless of forcing", func(t *testing.T) {
		if got := twoTurnCaptureForcedTurn1Trace(nil, 18, forceIndices); got != nil {
			t.Errorf("twoTurnCaptureForcedTurn1Trace(nil trace) = %v, want nil", got)
		}
	})
	t.Run("unforced index returns nil even with a populated trace", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{{Stage: "decision"}}}
		if got := twoTurnCaptureForcedTurn1Trace(trace, 7, forceIndices); got != nil {
			t.Errorf("twoTurnCaptureForcedTurn1Trace(unforced index) = %v, want nil", got)
		}
	})
	// codex xhigh R1 (2026-08-23, LOW finding): comparing against a
	// SECOND, freshly-computed trace.snapshot() call would not catch the
	// helper returning the LIVE slice (reflect.DeepEqual would still agree
	// on content) -- this subtest instead asserts real content, THEN
	// mutates the trace's own backing events afterward and confirms the
	// already-captured result is unaffected, directly proving the
	// no-aliasing claim this function's own doc comment makes.
	t.Run("forced index returns real content, independent of later mutation", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{{Stage: "decision", CommitGate: "lone_floor"}}}
		got := twoTurnCaptureForcedTurn1Trace(trace, 18, forceIndices)
		want := []graphrank.ResolutionTraceEvent{{Stage: "decision", CommitGate: "lone_floor"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("twoTurnCaptureForcedTurn1Trace(forced index) = %+v, want %+v", got, want)
		}
		// Mutate the trace's OWN backing slice, the same way a later arm's
		// trace.reset() (append after zeroing len) would -- got must not
		// see this.
		trace.events[0].CommitGate = "mutated_after_capture"
		trace.events = append(trace.events, graphrank.ResolutionTraceEvent{Stage: "kind_offer"})
		if !reflect.DeepEqual(got, want) {
			t.Errorf("twoTurnCaptureForcedTurn1Trace(forced index) = %+v after mutating the source trace, want it unchanged at %+v -- this proves an aliasing regression", got, want)
		}
	})
}

// TestTwoTurnPositiveFalseNoMatch pins CHAOS-4120's own positive-arm
// extension of the CHAOS-4039 false_no_match gate.
func TestTwoTurnPositiveFalseNoMatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		expectID string
		status   contractsv1.ContextFabricInvestigationStatus
		want     bool
	}{
		{"real expected answer, resolved no_match: false negative", "repository:r1", contractsv1.ContextFabricInvestigationNoMatch, true},
		{"real expected answer, resolved complete: no finding", "repository:r1", contractsv1.ContextFabricInvestigationComplete, false},
		{"no expected answer (a control case), no_match is not a false negative", "", contractsv1.ContextFabricInvestigationNoMatch, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := twoTurnPositiveFalseNoMatch(tc.expectID, tc.status); got != tc.want {
				t.Errorf("twoTurnPositiveFalseNoMatch(%q, %q) = %v, want %v", tc.expectID, tc.status, got, tc.want)
			}
		})
	}
}

// twoTurnStampArmFailure is the ONE way an error-derived ArmInvalidReason is
// set (codex xhigh review round 1, P2).
//
// The review found the confirmed-wrong arm's SETUP-turn failure setting a
// reason and no stage/type, so a whole class of arm-invalid row stayed
// non-diagnosable -- and the anchor-seed failure beside it had the same gap,
// unreported. Both are the predictable outcome of "remember to call the
// stamper too" being a second, separate step: seven error sites across four
// arms, and the two nobody was looking at were the ones that drifted.
//
// Pairing them in one call makes the omission unrepresentable rather than
// merely noticed. TestChaos4086_EveryErrorDerivedReasonIsStamped then refuses
// any assignment that goes around it.
//
// reason must already be closed-vocabulary (a classifier's output or a fixed
// string) -- this helper never derives it, so it cannot launder raw error
// text into the field.
func twoTurnStampArmFailure(res *twoTurnCaseResult, reason string, err error) {
	if res == nil {
		return
	}
	res.ArmInvalidReason = reason
	twoTurnStampArmError(res, err)
}

// twoTurnStampArmError records WHERE a failed Investigate call failed,
// alongside the closed-vocabulary reason the caller already sets.
//
// Both values are corpus-safe by construction: an InvestigationStage is a
// closed enum contextfabric maintains deliberately (stage.go), and a Go type
// name is a compile-time identifier, never message text.
func twoTurnStampArmError(res *twoTurnCaseResult, err error) {
	if res == nil || err == nil {
		return
	}
	if stage, ok := contextfabric.FailureStage(err); ok {
		res.ArmInvalidStage = string(stage)
	}
	res.ArmInvalidErrorType = twoTurnInnermostErrorType(err)
}

// twoTurnInnermostErrorType is CHAOS-4088's %T fingerprint, walking BOTH
// unwrap forms.
//
// The single-error form (Unwrap() error) is the one everybody remembers.
// fmt.Errorf with more than one %w verb returns *fmt.wrapErrors, which
// implements Unwrap() []error instead -- errors.Unwrap returns nil for it, so
// a naive loop stops dead and reports "*fmt.wrapErrors", which is pure noise.
// That is not a hypothetical: engine.go composes exactly that shape when it
// wraps a validation failure (`fmt.Errorf("%w: %w", ErrInvalidResult, err)`),
// which is the very error this field exists to fingerprint.
//
// On a multi-error node the LAST branch is followed: Go's own convention puts
// the sentinel first and the specific cause last, so the last branch is the
// one that says what actually went wrong.
func twoTurnInnermostErrorType(err error) string {
	for {
		switch unwrapped := err.(type) {
		case interface{ Unwrap() error }:
			next := unwrapped.Unwrap()
			if next == nil {
				return fmt.Sprintf("%T", err)
			}
			err = next
		case interface{ Unwrap() []error }:
			branches := unwrapped.Unwrap()
			if len(branches) == 0 {
				return fmt.Sprintf("%T", err)
			}
			err = branches[len(branches)-1]
		default:
			return fmt.Sprintf("%T", err)
		}
	}
}

// ---------------------------------------------------------------------------
// CHAOS-4100 shard selection and provisioning provenance
// ---------------------------------------------------------------------------

// twoTurnCaseIndicesFromResults derives a shard's covered case set from the
// ROWS it actually produced (CHAOS-4100, codex xhigh review round 1, P2).
//
// The first version read the post-shard-filter entry list, which is what
// the shard was ASSIGNED rather than what it RAN -- ACR_TEST_TRIAL_LIMIT
// truncates afterwards, so a deliberately-limited dry run recorded full
// coverage. Moving the derivation after the limit would have fixed that
// instance and left the ordering hazard: any future step that drops an
// entry after the derivation reintroduces it silently.
//
// Deriving from report.Results removes the ordering question entirely. A
// row exists if and only if an arm ran for that case, so this cannot claim
// a case the shard did not reach, whatever filters are added later or in
// what order. An empty shard correctly reports no coverage.
//
// It is the same property the merged artifact depends on: the union of
// these sets is the run's own statement of what it covered, and a reader
// checks that union against the annex to prove nothing was dropped.
func twoTurnCaseIndicesFromResults(results []twoTurnCaseResult) []int {
	seen := make(map[int]struct{}, len(results))
	indices := make([]int, 0, len(results))
	for _, r := range results {
		if _, exists := seen[r.Index]; exists {
			continue
		}
		seen[r.Index] = struct{}{}
		indices = append(indices, r.Index)
	}
	sort.Ints(indices)
	return indices
}

// twoTurnShardNoCasesSentinel is what a launcher sets for a shard it
// deliberately assigned NO cases -- distinct from leaving the variable
// unset, which means "select by modulo instead".
const twoTurnShardNoCasesSentinel = "none"

// twoTurnShardCaseIndices reads ACR_TEST_TRIAL_SHARD_CASE_INDICES -- a
// comma-separated list of corpus positions this shard should run.
//
// Returns (nil, nil) when unset, which is what keeps every pre-CHAOS-4100
// invocation on the modulo path selecting byte-identical cases.
//
// Fails closed on a malformed value rather than falling back to modulo: a
// launcher that meant to name an explicit chunk and produced garbage would
// otherwise silently run a DIFFERENT, modulo-selected set of cases, and the
// artifact would look perfectly well-formed while measuring the wrong slice.
func twoTurnShardCaseIndices(t *testing.T) ([]int, map[int]struct{}) {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("ACR_TEST_TRIAL_SHARD_CASE_INDICES"))
	if raw == "" {
		return nil, nil
	}
	// "none" is an EXPLICITLY EMPTY assignment, distinct from the variable
	// being unset (codex xhigh review round 4, P2).
	//
	// An empty STRING cannot carry that distinction: it reads as "the
	// launcher said nothing", so the shard falls back to selecting by
	// modulo. Today that is harmless -- the launcher computes its own
	// layout with the SAME modulo rule over the SAME index set, so a shard
	// the launcher left empty is a shard modulo also leaves empty, which
	// is why no duplicate rows occur and the reported consequence does not
	// reproduce. But it is harmless only because two independent
	// implementations happen to agree. The moment they diverge -- a new
	// assignment strategy, a reordered annex -- an "empty" shard would
	// silently run whatever modulo hands it, duplicating another shard's
	// cases, and the merge step would reject the run (or, worse, over-count
	// before anyone noticed why).
	//
	// A sentinel makes the two states distinguishable, so the launcher
	// says which one it means instead of relying on a coincidence.
	if raw == twoTurnShardNoCasesSentinel {
		return []int{}, map[int]struct{}{}
	}
	indices := make([]int, 0, 8)
	set := make(map[int]struct{}, 8)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		index, err := strconv.Atoi(part)
		if err != nil || index < 0 {
			t.Fatalf("ACR_TEST_TRIAL_SHARD_CASE_INDICES: %q is not a non-negative integer", part)
		}
		if _, exists := set[index]; exists {
			t.Fatalf("ACR_TEST_TRIAL_SHARD_CASE_INDICES lists index %d twice -- a shard runs each case once", index)
		}
		set[index] = struct{}{}
		indices = append(indices, index)
	}
	if len(indices) == 0 {
		t.Fatal("ACR_TEST_TRIAL_SHARD_CASE_INDICES is set but names no index -- unset it to select by modulo instead")
	}
	sort.Ints(indices)
	return indices, set
}

// twoTurnShardCaseIndicesEnvTripleError returns a non-nil error when
// ACR_TEST_TRIAL_SHARD_CASE_INDICES is set but ACR_TEST_TRIAL_SHARD_COUNT is
// not (the SHARD_INDEX/SHARD_COUNT pairing has its own guard, immediately
// above this check's call site).
//
// twoTurnShardCaseIndices is only ever called from inside the
// ACR_TEST_TRIAL_SHARD_COUNT block: an operator who sets CASE_INDICES alone
// gets no error and no filtering -- the run silently processes the FULL
// corpus, which is exactly the shape of the documented env trap that has
// bitten three separate lanes. A plain function (rather than one more
// t.Fatal call inline) so the trigger condition is unit-testable without
// running the live two-turn test.
func twoTurnShardCaseIndicesEnvTripleError() error {
	caseIndices := strings.TrimSpace(os.Getenv("ACR_TEST_TRIAL_SHARD_CASE_INDICES"))
	if caseIndices == "" {
		return nil
	}
	if os.Getenv("ACR_TEST_TRIAL_SHARD_COUNT") == "" {
		return fmt.Errorf("ACR_TEST_TRIAL_SHARD_CASE_INDICES=%q is set but ACR_TEST_TRIAL_SHARD_COUNT/ACR_TEST_TRIAL_SHARD_INDEX are not -- CASE_INDICES only takes effect inside the sharding block and is otherwise silently ignored, running the FULL corpus. Set all three together: ACR_TEST_TRIAL_SHARD_CASE_INDICES, ACR_TEST_TRIAL_SHARD_COUNT, ACR_TEST_TRIAL_SHARD_INDEX", caseIndices)
	}
	return nil
}

// twoTurnForceTraceIndices reads ACR_TEST_TRIAL_FORCE_TRACE_INDICES -- a
// comma-separated list of corpus positions whose turn-1 raw
// ResolutionTraceEvent stream should be captured onto every row that corpus
// case produces (twoTurnTurn1Facts.TraceEvents / twoTurnCaseResult.
// Turn1TraceEvents), mirroring ACR_TEST_TRIAL_SHARD_CASE_INDICES's own
// parsing discipline (twoTurnShardCaseIndices) -- same fail-closed-on-
// malformed-value reasoning, deliberately simpler otherwise: this is a
// DEBUG affordance, not a correctness-load-bearing sharding assignment, so
// it carries no "none" sentinel and returns membership only, no ordered
// slice.
//
// CHAOS-4183 phase "2c" (team-lead ruling, 2026-08-23): this is Option B
// from phase 2b's own proposal -- surgical, opt-in, and it generalizes to
// every future "I need one case's raw trace" investigation without
// redefining twoTurnCaseResultIsAnomalous's own general measurement-policy
// predicate (phase 2b's Option A, rejected: "a new anomaly predicate is
// measurement policy, not debug tooling").
//
// Returns nil when unset -- every ordinary run (measurement or otherwise)
// leaves this at its zero state, forcing nothing, exactly like an unset
// ACR_TEST_TRIAL_SHARD_CASE_INDICES falls back to modulo selection.
func twoTurnForceTraceIndices(t *testing.T) map[int]struct{} {
	t.Helper()
	set, err := parseTwoTurnForceTraceIndices(os.Getenv("ACR_TEST_TRIAL_FORCE_TRACE_INDICES"))
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// parseTwoTurnForceTraceIndices is twoTurnForceTraceIndices' own parsing
// logic, extracted as a pure function (error return, no *testing.T) so its
// fail-closed path is directly unit-testable -- calling t.Fatal from inside
// a helper under direct test invokes runtime.Goexit, which is unsafe to
// provoke and recover from outside the test framework's own goroutine
// machinery (this file's own established convention -- see
// twoTurnWindowSurfacesAgree's own doc comment for the identical
// split-into-a-pure-predicate reasoning, "a genuinely FAILING subtest would
// cascade its parent test... even though catching [it] IS the correct,
// intended behavior being tested").
func parseTwoTurnForceTraceIndices(raw string) (map[int]struct{}, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	set := make(map[int]struct{}, 8)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		index, err := strconv.Atoi(part)
		if err != nil || index < 0 {
			return nil, fmt.Errorf("ACR_TEST_TRIAL_FORCE_TRACE_INDICES: %q is not a non-negative integer", part)
		}
		set[index] = struct{}{}
	}
	// codex xhigh R1 (2026-08-23, LOW finding): a value that is set but
	// names nothing (e.g. a bare ",") must fail loud, same as
	// twoTurnShardCaseIndices' own "named no index" check -- a launcher
	// that meant to force a capture and got the syntax wrong should never
	// silently run an ordinary, uncaptured pass instead.
	if len(set) == 0 {
		return nil, fmt.Errorf("ACR_TEST_TRIAL_FORCE_TRACE_INDICES is set but names no index -- unset it to disable force-tracing instead")
	}
	return set, nil
}

// twoTurnCaptureForcedTurn1Trace (CHAOS-4183 phase "2c") returns turn 1's
// own raw ResolutionTraceEvent stream when index appears in forceIndices,
// nil otherwise -- the ONE decision point twoTurnForceTraceIndices' own
// opt-in knob controls. trace.snapshot() is a defensive copy
// (twoTurnTraceCapture's own contract), so the returned slice never aliases
// the shared capture's backing array a later arm's own trace.reset() would
// otherwise clobber.
//
// DEBUG AFFORDANCE, not a measurement-artifact mechanism: see
// twoTurnCaseResult.Turn1TraceEvents' own doc comment for why this output
// is never part of a ratified measurement and must stay LOCAL-ONLY --
// never quoted, attached, or paraphrased into Linear, a PR, or any other
// durable/shared report.
func twoTurnCaptureForcedTurn1Trace(trace *twoTurnTraceCapture, index int, forceIndices map[int]struct{}) []graphrank.ResolutionTraceEvent {
	if trace == nil {
		return nil
	}
	if _, forced := forceIndices[index]; !forced {
		return nil
	}
	return trace.snapshot()
}

// twoTurnDistinctCaseIndices returns the ascending set of corpus positions
// entries covers. Distinct, because an annex carries one entry per (case,
// member) and a shard's own case set is what a merged-union audit checks.
func twoTurnDistinctCaseIndices(entries []twoTurnOracleEntry) []int {
	seen := make(map[int]struct{}, len(entries))
	indices := make([]int, 0, len(entries))
	for _, entry := range entries {
		if _, exists := seen[entry.Index]; exists {
			continue
		}
		seen[entry.Index] = struct{}{}
		indices = append(indices, entry.Index)
	}
	sort.Ints(indices)
	return indices
}

// twoTurnEnvInt reads a non-negative integer the launcher passed for
// provenance. Absent means 0, which every field's own doc comment reads as
// "the launcher did not say" -- but a PRESENT and malformed value fails,
// because a provenance field silently reading 0 when the launcher meant 32
// is worse than no field at all.
func twoTurnEnvInt(t *testing.T, name string) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		t.Fatalf("%s must be a non-negative integer, got %q", name, raw)
	}
	return value
}

// twoTurnShardProvisioningMode reads the CLOSED provisioning label. An
// unrecognized value fails rather than being recorded: this field exists so
// a reader can attribute a contention flake to a substrate, and a free-text
// value would let a typo quietly create a third, meaningless population.
func twoTurnShardProvisioningMode(t *testing.T) string {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("ACR_TEST_TRIAL_SHARD_PROVISIONING_MODE"))
	switch raw {
	case "", "template_clone", "container":
		return raw
	default:
		t.Fatalf("ACR_TEST_TRIAL_SHARD_PROVISIONING_MODE must be \"template_clone\" or \"container\", got %q", raw)
		return ""
	}
}

// twoTurnResponderModel (CHAOS-4135) reads ACR_TEST_TRIAL_RESPONDER_MODEL
// -- see trialProvenance.ResponderModel's own doc comment
// (generative_trial_live_test.go) for the provenance gap this closes and
// why "ambient-default" is a deliberate placeholder rather than an attempt
// to read codex's own actual resolved model.
func twoTurnResponderModel() string {
	if v := strings.TrimSpace(os.Getenv("ACR_TEST_TRIAL_RESPONDER_MODEL")); v != "" {
		return v
	}
	return "ambient-default"
}

// twoTurnResponderTransport (CHAOS-4313) reads
// ACR_TEST_TRIAL_RESPONDER_TRANSPORT -- see
// trialProvenance.ResponderTransport's own doc comment
// (generative_trial_live_test.go) for what this closes. Defaults to "api"
// so a run that launched via a script predating this variable's export
// (should not happen post-cutover, but this function must be correct
// standalone) still records the transport scripts/trial/common.sh's own
// trial_responder_transport defaults to, rather than a misleading empty
// string.
func twoTurnResponderTransport() string {
	if v := strings.TrimSpace(os.Getenv("ACR_TEST_TRIAL_RESPONDER_TRANSPORT")); v != "" {
		return v
	}
	return "api"
}

// twoTurnResponderEffort (CHAOS-4313 follow-up, chris/team-lead 2026-08-26
// 10:36 PDT) reads ACR_TEST_TRIAL_RESPONDER_EFFORT -- see
// trialProvenance.ResponderEffort's own doc comment (generative_trial_live_test.go)
// for what this closes. Unlike twoTurnResponderModel/twoTurnResponderTransport
// above, there is deliberately NO substituted default here: an empty value
// is itself the correct, meaningful provenance record ("the request never
// set ReasoningEffort, the provider's own default applied"), not a gap to
// paper over with a placeholder string -- cmd/acr-trial-responder-api's own
// main() has the identical no-default shape for the same reason.
func twoTurnResponderEffort() string {
	return strings.TrimSpace(os.Getenv("ACR_TEST_TRIAL_RESPONDER_EFFORT"))
}

// validResponderEffortForProvenance (codex xhigh review round 2, Medium,
// confirmed) bounds ACR_TEST_TRIAL_RESPONDER_EFFORT's character set and
// length before it can reach a persisted provenance artifact.
//
// KEEP IN SYNC WITH cmd/acr-trial-responder-api/main.go's own
// validResponderEffort (same regex, same reasoning): this package cannot
// import that separate binary's package main, so nothing can assert
// cross-package agreement directly -- the SAME pre-existing limitation
// reportSchemaVersion's own doc comment already documents for
// expectedSchemaVersion. round-1's resolveResponderEffort fix only bounded
// the value the RESPONDER BINARY sends/logs; this closes the SAME gap on
// the PROVENANCE side -- twoTurnResponderEffort reads the identical raw env
// var independently, in this SEPARATE process (the go test, not the
// responder), so a malformed value could still reach a persisted report
// file even when main() correctly refuses to start the responder over it
// (the run would then fail on unanswered exchanges, but the artifact
// written at the end would still carry the raw string).
var validResponderEffortForProvenance = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// fatalfer is the minimal *testing.T surface twoTurnResponderProvenance
// needs (Helper + Fatalf) -- see that function's own doc comment for why
// this indirection exists instead of a direct *testing.T parameter.
type fatalfer interface {
	Helper()
	Fatalf(format string, args ...any)
}

// twoTurnResponderProvenance (codex xhigh review round 1, High, confirmed)
// computes the report's transport label plus responderModel/
// responderTransport/responderEffort together, extracted to its own
// function purely so it has a unit-test surface independent of
// TestChaos3742TwoTurnConfirmationReplay's full investigator/live-corpus
// machinery -- the SAME reasoning twoTurnRedactNonAnomalousTraceEvents'
// own doc comment already documents for that extraction.
//
// responderModel/responderTransport (CHAOS-4135, extended CHAOS-4313):
// empty for real_api -- that transport never shells out to a responder
// script at all, so there is nothing to attribute an answer to; see
// trialProvenance.ResponderModel/ResponderTransport's own doc comments.
//
// responderEffort is gated on responderTransport == "api" specifically,
// NOT merely exchangeDir != "" like ResponderModel/ResponderTransport
// above -- run-responder-codex.sh never reads
// ACR_TEST_TRIAL_RESPONDER_EFFORT at all (cmd/acr-trial-responder-api is
// the only responder that wires it into a request), so an operator who
// sets the env var while ACR_TEST_TRIAL_RESPONDER_TRANSPORT=codex would
// otherwise get a report falsely claiming that effort tier answered every
// call, when in fact no request ever carried it. Before this gate existed,
// the var was read unconditionally regardless of which responder actually
// ran.
//
// responderEffort is ALSO validated against validResponderEffortForProvenance
// (codex xhigh review round 2, Medium, confirmed) before being returned --
// t.Fatalf's immediately on a malformed value, mirroring
// preflightAnchorCausalChain's own "fail fast, in one case, before spending
// hours on the other 49" philosophy: a malformed
// ACR_TEST_TRIAL_RESPONDER_EFFORT would already make
// cmd/acr-trial-responder-api's own main() refuse to start (round-1's
// resolveResponderEffort fix), but that failure happens in a SEPARATE
// process from this one -- without this check, THIS process (the go test)
// would still read the identical raw env var independently and persist it
// into the final report artifact even though the responder never answered
// a single request over it.
//
// Takes a fatalfer, not *testing.T directly, purely so
// TestTwoTurnResponderProvenance_RejectsMalformedEffort can substitute a
// fake that records the call instead of genuinely failing -- a real
// *testing.T.Fatalf's runtime.Goexit always marks every ancestor test
// failed too, which would make a test whose entire point is "prove Fatalf
// fires" report as a failure itself. *testing.T satisfies this interface
// with no code changes at the real call site below.
func twoTurnResponderProvenance(t fatalfer, exchangeDir string) (transportLabel, responderModel, responderTransport, responderEffort string) {
	t.Helper()
	// Transport label reflects which transport actually ran (codex round-1
	// finding #10: hard-coding "real_api" while a file-exchange runtime is
	// wired gives the acceptance artifact false provenance).
	transportLabel = "real_api"
	if exchangeDir == "" {
		return transportLabel, "", "", ""
	}
	transportLabel = "file_exchange"
	responderModel = twoTurnResponderModel()
	responderTransport = twoTurnResponderTransport()
	if responderTransport == "api" {
		responderEffort = twoTurnResponderEffort()
		if responderEffort != "" && !validResponderEffortForProvenance.MatchString(responderEffort) {
			t.Fatalf("ACR_TEST_TRIAL_RESPONDER_EFFORT must be 1-32 characters of [A-Za-z0-9_-] -- refusing to persist an unbounded/unexpected value into the report artifact")
		}
	}
	return transportLabel, responderModel, responderTransport, responderEffort
}

// twoTurnUnjustifiedShadowProbe computes the CHAOS-4062 trace-observability
// fields for an "unjustified"-classified inferred commit: whether
// kindInsensitivityProof was evaluated on the hinted call and its verdict
// (off trace, already scoped to the hinted call only by
// runTwoTurnInferredTierArm's own reset-before-hinted-call discipline), plus
// both legs' own committed-subject Kind+CanonicalID. Extracted from the
// classification switch (runTwoTurnInferredTierArm) purely so it has a unit
// test surface independent of that function's full investigator/window-
// precondition machinery (TestTwoTurnUnjustifiedShadowProbe). Never
// consulted by, and never influences, the classification itself -- read-only
// observation of a decision already made by the caller.
func twoTurnUnjustifiedShadowProbe(trace *twoTurnTraceCapture, baselineCommitted, hintedCommitted []contractsv1.ContextFabricSubjectRef) (evaluated bool, outcome, mode string, baselineSubjects, hintedSubjects []twoTurnSubjectKindID) {
	if trace != nil {
		evaluated, outcome, mode = trace.kindInsensitivityResult()
	}
	return evaluated, outcome, mode, twoTurnSubjectKindIDs(baselineCommitted), twoTurnSubjectKindIDs(hintedCommitted)
}

// twoTurnCaseResult is one (case, arm) outcome. Every field is a count, a
// bool, an id, or a closed-vocabulary status string -- the same privacy
// discipline replayCaseOutcome pins (TestReplayCaseOutcomeCarriesNoQuestionOrTermText).
type twoTurnCaseResult struct {
	Index       int    `json:"index"`
	Member      string `json:"member"`
	Arm         string `json:"arm"`
	Turn1Status string `json:"turn1_status"`
	Turn2Status string `json:"turn2_status"`
	OfferMiss   bool   `json:"offer_miss"`
	Applied     bool   `json:"applied"`
	// Reused (CHAOS-4040 harness follow-up, codex round 3, confirmed):
	// this arm's own turn 2 InvestigationResult.Reused, so a caller can
	// tell a fresh decisive answer from a replayed stored row. Needed
	// because answer-reuse only rejects DECISIVE candidates carrying an
	// INFERRED window (answer_reuse.go) -- a confirmed-window decisive row
	// is fully reuse-eligible, so the positive arm's own "confirmed AND
	// decisive" claim could otherwise be satisfied by a stale row this
	// run's redemption code never actually produced. See
	// WindowPositiveAppliedCount's own doc comment (twoTurnReport).
	Reused bool `json:"reused"`
	// TierRoutedCorrectly (inferred_tier arm only) asserts the injected
	// explicit value actually landed at Source=explicit_unattributed,
	// Provenance=inferred_default in the echo -- not merely "did not
	// commit" (codex round-1 finding #6: a tier-routing regression that
	// instead landed the value at question_stated could still fail to
	// commit for an unrelated census reason, silently passing a commit-only
	// check).
	TierRoutedCorrectly bool `json:"tier_routed_correctly,omitempty"`
	// InferredClassification (inferred_tier arm, kind/handle members only;
	// CHAOS-4039 v5 measurement contract, team-lead ruling 2026-08-22,
	// REPLACES the v4 canonicalInterpretationHash/normalizedDecisionFingerprint
	// pairing proof -- that proof required BIT-FOR-BIT equality of raw
	// live-model output, SynthesisDraft.Status and Interpretation's
	// SubjectTerms/ComparisonTerms/FactRequirements, across two
	// INDEPENDENTLY SAMPLED Investigate() calls with no temperature pinning
	// anywhere in the model runtime -- so it was by-construction close to
	// unreachable on ordinary call-to-call model phrasing variance, never
	// actually measuring hint influence. v5 compares ONLY
	// engine-deterministic decision state: the paired no-hint baseline's own
	// final decision-stage trace Outcome (graphrank's closed
	// committed/ambiguous/no_commit vocabulary, never model-generated) and
	// committed-subject SET (twoTurnCommittedSubjectsEquivalent, Kind+
	// CanonicalID only) both match the hinted call's -- see
	// twoTurnInferredClassification's own doc comment) is the 3-way
	//
	// CHAOS-4139 (2026-08-23) CLOSED A GAP between this paragraph's own
	// claim and what the code actually compared: v5 shipped reading the
	// committed-subject SET off SubjectResolution.Committed on the final
	// InvestigationResult, which is NOT engine-deterministic --
	// CHAOS-4085's post-synthesis commit-affirmation gate can retract a
	// statistical commit from that exact field based on live model
	// synthesis output, one layer downstream of the decision this
	// paragraph always meant to compare. Now genuinely reads
	// twoTurnDecisionCommittedSubjects off each leg's own decision-stage
	// trace event -- see that function's own doc comment for the shard-54
	// discovery and the ATTESTATION BOUNDARY section (CHAOS-4083) this is
	// now also recorded under.
	// partition every DECISIVE (CommittedCount>0) kind/handle inferred-tier
	// outcome gets classified into exactly once: "baseline_equivalent" (a
	// paired no-hint request reached the SAME engine-deterministic decision
	// outcome and committed the SAME subject set, and the hinted result was
	// not itself served via answer-reuse -- the hint provably had ZERO
	// causal effect),
	// "kind_insensitivity_attested" (kindInsensitivityProof was actually
	// consulted and returned commit_sound, AND a decision-stage trace event
	// shows CommitGate==evidence_census for the committed subject -- the
	// all-kinds census itself certified this exact commit, not merely a
	// generic would_commit outcome the ruling found insufficient on its
	// own), or "unjustified" (neither -- an immediate run failure, see
	// InferredUnjustifiedCount). Empty for a non-decisive outcome (nothing
	// to classify) and for window (window keeps the OLD literal zero-commit
	// bar unconditionally -- CHAOS-4040's gate is structural, not a
	// per-commit noninterference proof, so there is nothing for this 3-way
	// partition to classify; see WindowCommitCount/WindowGatedCount).
	InferredClassification string `json:"inferred_classification,omitempty"`
	// PairInvalid (inferred_tier arm, kind/handle members only) is true
	// when the PAIRED no-hint baseline request itself errored/timed out,
	// making the pairing impossible to evaluate at all -- reported
	// separately from ArmInvalidReason/InferredClassification so a broken
	// baseline call is never silently absorbed into "unjustified" (which
	// means "evaluated and found wanting", not "could not be evaluated").
	PairInvalid bool `json:"pair_invalid,omitempty"`
	// PairRetried (CHAOS-4138) is true when this row's baseline/hinted-leg
	// pairing needed and used its ONE bounded retry: the first attempt's
	// own baseline-leg or hinted-leg Investigate() call itself errored --
	// an instrument failure, the harness could not take the measurement at
	// all -- so runTwoTurnInferredTierArmWithPairRetry re-ran the whole
	// pairing exactly once with a distinct request-ID prefix. Never set
	// for a pairing-precondition failure (Reused/VersionSet mismatch/
	// window-bounds disagreement, the pairInvalid check inside
	// runTwoTurnInferredTierArm), the subject_anchor structural exemption,
	// or a missing window-oracle-entry/window-precondition-setup failure:
	// every one of those is the harness correctly detecting an invalid
	// comparison or an unconstructable precondition, not an instrument
	// failure -- see twoTurnPairInvalidIsInstrumentFailure's own doc
	// comment for the exact, narrow eligibility test. A PairInvalid row
	// with PairRetried==false was never eligible for retry at all (the
	// first attempt's own failure, unretried). A PairInvalid row with
	// PairRetried==true IS the retry attempt's own row after the retry
	// ALSO failed -- bounded to exactly one retry, never a loop: a call
	// that fails twice is a real, reportable instrument failure, reported
	// exactly as failed as an unretried row would be, never silently
	// absorbed or retried again.
	PairRetried bool `json:"pair_retried,omitempty"`
	// PairRetryFirstArmInvalidReason/Stage/ErrorType (CHAOS-4138) preserve
	// the FIRST attempt's own error-derived fields when PairRetried is
	// true. This row's own ArmInvalidReason/ArmInvalidStage/
	// ArmInvalidErrorType always describe the RETRY attempt's own outcome
	// (empty on a successful retry; its own distinct failure otherwise) --
	// these three fields are the only place the first attempt's error
	// survives, so a reader can see both attempts' errors on one row
	// exactly the way they can already see turn1_regime, census_ran, etc.
	// Empty when PairRetried is false.
	PairRetryFirstArmInvalidReason    string `json:"pair_retry_first_arm_invalid_reason,omitempty"`
	PairRetryFirstArmInvalidStage     string `json:"pair_retry_first_arm_invalid_stage,omitempty"`
	PairRetryFirstArmInvalidErrorType string `json:"pair_retry_first_arm_invalid_error_type,omitempty"`
	// FalseNoMatch (positive AND inferred_tier arms, every member including
	// window -- CHAOS-4120 widened this from inferred_tier alone) is true
	// when this outcome resolved to the literal no_match terminal on a case
	// with a real expected answer (tc.ExpectID != "" -- codex review round 2,
	// P3, confirmed: absent for a CONTROL case, tc.ExpectID=="" by design,
	// which both twoTurnPositiveFalseNoMatch and this arm's own inline check
	// correctly exclude) -- the no-match-direction mirror of WrongCommit,
	// CHAOS-4039's own "false_no_match=0" pass condition.
	//
	// CHAOS-4120 extended the gate to the positive arm (previously the ONLY
	// arm this could not fire on): a positive-arm call redeems a
	// receipt-confirmed offer, a STRONGER signal than the inferred-tier
	// arm's mere explicit hint, so a no_match there is at least as much a
	// correctness finding. Before this widening, a positive-arm no_match was
	// structurally invisible to this bar (CHAOS-4108, ext65 index 57:
	// recurred across 4 independent runs while false_no_match_count read 0)
	// -- the gate failed toward fine exactly because it was scoped narrower
	// than the outcome it exists to catch.
	FalseNoMatch   bool `json:"false_no_match,omitempty"`
	CommittedCount int  `json:"committed_count"`
	// CanonicalFactsCount (CHAOS-4347) is twoTurnCanonicalFactsCount's own
	// result for this row's arm-terminal call -- populated whenever
	// CommittedCount > 0 (there is no "synthesize input" to count facts in
	// otherwise), 0 for every uncommitted row (the field's own zero value,
	// not a distinguishable "unset"). See report.FactlessCommittedCount's
	// own doc comment for why a committed-but-0 row matters: it is the
	// coverage gap CHAOS-4344 case 23 exposed, now directly reportable.
	CanonicalFactsCount int    `json:"canonical_facts_count"`
	WrongCommit         bool   `json:"wrong_commit"`
	MutationProbe       string `json:"mutation_probe,omitempty"`
	MutationTripped     bool   `json:"mutation_tripped,omitempty"`
	// ArmInvalidReason is a closed-vocabulary classification only (never
	// raw err.Error() text -- codex round-1 finding #9: an investigator
	// error can carry upstream detail this outcome-only artifact must not
	// persist).
	ArmInvalidReason string `json:"arm_invalid_reason,omitempty"`
	// ShadowKindInsensitivityEvaluated/ShadowKindInsensitivityOutcome
	// (CHAOS-4062 shadow-insensitivity trace probe, off the CHAOS-4039
	// analysis) mirror ResolutionTraceEvent's own same-named fields
	// (resolve.go) as read by kindInsensitivityResult off THIS case's
	// HINTED-call trace only (trace is reset immediately before the hinted
	// Investigate() call below, so any baseline-call events are already
	// gone by the time these are captured). Populated ONLY when
	// InferredClassification=="unjustified" -- trace-level observability
	// added to distinguish, per CHAOS-4039's open questions, "genuine kind
	// ambiguity without the hint" (evaluated=true,
	// outcome=would_no_match/would_clarify) from "the proof was never even
	// consulted" (evaluated=false) for the cases the 3-way partition
	// already rejected. No classification/semantics change: these fields
	// are read-only observations of a decision already made above.
	ShadowKindInsensitivityEvaluated bool   `json:"shadow_kind_insensitivity_evaluated,omitempty"`
	ShadowKindInsensitivityOutcome   string `json:"shadow_kind_insensitivity_outcome,omitempty"`
	// ShadowKindInsensitivityMode (CHAOS-4079, same unjustified-only gate)
	// mirrors ResolutionTraceEvent.ShadowKindInsensitivityMode: which
	// explicit-kind narrowing situation the two fields above were produced
	// under ("narrowed" | "observed_no_overlap" | "observed_subsumed").
	//
	// This field is what makes the two above finally informative for the
	// wrong-kind arm. Before CHAOS-4079 the probe was UNREACHABLE for a
	// hint disjoint from the candidate pool -- precisely what this arm
	// injects -- so every unjustified row reported evaluated=false and the
	// distinction the fields were added to draw could not be drawn at all.
	// Reading it: "observed_no_overlap" + commit_sound means the census was
	// provably untouched by the wrong hint, and whatever made this row
	// differ from its baseline came from somewhere OTHER than census
	// narrowing (member stamping or offer ranking are the candidates) --
	// which is why it is reported here rather than promoted to
	// kind_insensitivity_attested; see twoTurnKindAttested's own mode gate.
	ShadowKindInsensitivityMode string `json:"shadow_kind_insensitivity_mode,omitempty"`
	// BaselineCommittedSubjects/HintedCommittedSubjects (CHAOS-4062, same
	// unjustified-only gate as the Shadow* fields above) carry the paired
	// no-hint baseline's and the hinted call's own SubjectResolution.Committed
	// Kind+CanonicalID -- CHAOS-4039's "commit_sound on a DIFFERENT subject"
	// reading needs both sides' identity to tell that apart from an
	// identical commit reached by two independent, kind-insensitive paths.
	BaselineCommittedSubjects []twoTurnSubjectKindID `json:"baseline_committed_subjects,omitempty"`
	HintedCommittedSubjects   []twoTurnSubjectKindID `json:"hinted_committed_subjects,omitempty"`
	// ---------------------------------------------------------------
	// CHAOS-4135 anomalous-row trace persistence
	// ---------------------------------------------------------------
	//
	// THE BAR THIS EXISTS FOR: the shard-54 CHAOS-4117 diagnosis (2026-08-22)
	// could narrow a mismatched inferred_tier pairing to "the hinted call
	// committed, the paired no-hint baseline did not" -- BaselineCommittedSubjects
	// above already proved that half -- but could not go one level deeper
	// (WHY the two legs' own candidate pools/tie states diverged) without a
	// raw ResolutionTraceEvent stream neither leg's row persisted. The
	// harness already builds that full stream in-process for every call
	// (twoTurnTraceCapture.events); this closes the gap between capturing it
	// and being able to read it after the fact.
	//
	// TraceEvents is THIS row's own decisive call's full event stream (the
	// same call CommitGate/TiedStatisticalTop/SearchTruncated/KindCoverage*
	// above already read a narrow projection of -- see twoTurnStampDecision,
	// which stamps both). BaselineTraceEvents (inferred_tier, non-window
	// members only) is the SAME row's PAIRED no-hint baseline call's own
	// stream, captured immediately after that call, before its own reset for
	// the hinted call -- the SAME point BaselineCommittedSubjects' own
	// baseline.SubjectResolution.Committed is read from (runTwoTurnInferredTierArm).
	//
	// NEITHER is populated unconditionally: twoTurnCaseResultIsAnomalous
	// gates a redaction pass (TestChaos3742TwoTurnConfirmationReplay, run
	// immediately before the report is serialized) that clears both back to
	// nil on every row that does not trip one of this test's own
	// zero-tolerance bars and is not an inferred_tier pairing classified
	// "unjustified" -- a full per-call trace on every one of a run's ~800+
	// rows is a standing cost this diagnostic exists to avoid, not to add.
	//
	// CORPUS-SAFE BY CONSTRUCTION, inherited from ResolutionTraceEvent's own
	// doc comment (resolve.go): closed-vocabulary stage/gate/outcome
	// strings, SHA-256 term hashes, counts, confidences, and bools only --
	// never raw term or question text, by a rule that file calls having "no
	// exception". The standing rule downstream is unchanged regardless: a
	// persisted event's fields are for diagnosis in the raw JSON artifact,
	// never quoted into Linear/PR/commit text -- mechanism and index only,
	// as ever.
	TraceEvents         []graphrank.ResolutionTraceEvent `json:"trace_events,omitempty"`
	BaselineTraceEvents []graphrank.ResolutionTraceEvent `json:"baseline_trace_events,omitempty"`
	// Turn1TraceEvents (CHAOS-4183 phase "2c", team-lead ruling 2026-08-23)
	// is a DEBUG AFFORDANCE, distinct in kind from TraceEvents/
	// BaselineTraceEvents above: those two persist on the SAME
	// anomaly-gated redaction pass every ordinary measurement run already
	// applies. This field is gated on an entirely SEPARATE, opt-in knob --
	// ACR_TEST_TRIAL_FORCE_TRACE_INDICES (twoTurnForceTraceIndices' own doc
	// comment) -- and is NEVER touched by twoTurnRedactNonAnomalousTraceEvents;
	// it is nil on every run that does not explicitly name this row's own
	// corpus index. Turn 1's own raw stream was previously UNRECOVERABLE
	// from any artifact this harness could produce (CHAOS-4183 phase 2b's
	// own finding: twoTurnCaptureTurn1Facts consumes it into scalar
	// summary fields only, then every arm resets the shared capture before
	// its own call) -- this is the minimal addition phase 2b proposed to
	// close that gap for a targeted, single-case investigation.
	//
	// This output is NEVER part of a ratified measurement artifact -- a
	// force-traced run is a ONE-OFF debug capture, not a run whose report
	// gets merged, cited, or archived as evidence. And UNLIKE TraceEvents/
	// BaselineTraceEvents above (documented corpus-safe by construction,
	// "no exception"), this field is treated MORE conservatively per
	// team-lead's own ruling: a force-traced artifact stays LOCAL-ONLY on
	// the machine that produced it -- never quoted, attached, or
	// paraphrased into Linear, a PR, or any other durable/shared report.
	// Only mechanism and index (never trace content) belong in anything
	// durable, same standing rule as ever.
	Turn1TraceEvents []graphrank.ResolutionTraceEvent `json:"turn1_trace_events,omitempty"`
	// HintedCommitAffirmation/BaselineCommitAffirmation (CHAOS-4139,
	// inferred_tier non-window members only) are each leg's OWN
	// twoTurnCommitAffirmationState: "" (nothing committed at the decision
	// layer), "exempt" (IdentityProven commit basis -- CHAOS-4085's gate
	// never evaluates it), "affirmed" (statistical basis, gate evaluated
	// it, let it stand), or "retracted" (statistical basis, gate found no
	// synthesis support and discarded it from SubjectResolution.Committed).
	// Read directly off each leg's own result.SubjectResolution.Committed
	// (engine-only state; never off result.Limitations, which is
	// model-authored and could coincidentally match the fixed
	// ContextFabricCommitRetractionLimitation text) -- see that
	// function's own doc comment.
	//
	// THE BAR THIS EXISTS FOR: shard 54 of the 2026-08-23 re-measure showed
	// two legs with BYTE-IDENTICAL decision-stage trace events still
	// classify "unjustified" (pre-CHAOS-4139) because one leg's
	// independent synthesis call affirmed the statistical commit and the
	// other's did not -- CHAOS-4139 moved the classification itself off
	// this model-influenced layer (see InferredClassification's own doc
	// comment), but a reader watching THIS specific mechanism recur still
	// needs it visible on the row, not re-derived by re-investigating a
	// live run every time.
	HintedCommitAffirmation   string `json:"hinted_commit_affirmation,omitempty"`
	BaselineCommitAffirmation string `json:"baseline_commit_affirmation,omitempty"`
	// ---------------------------------------------------------------
	// CHAOS-4086 instant-diagnosis fields
	// ---------------------------------------------------------------
	//
	// THE BAR THESE EXIST FOR: a wrong_commit row must be fully
	// diagnosable from THIS REPORT ALONE -- which subject was committed,
	// which was expected, which gate fired, whether the coverage floor was
	// involved. Before them, every one of those facts was computed in
	// process and then discarded: Committed was reduced to a bool, the
	// expected subject lived only in the corpus annex, the gate value was
	// SHA-256'd into an opaque fingerprint, and the floor's effect was
	// never returned at all. Diagnosing CHAOS-4085 cost a re-read of raw
	// model-exchange files off a scratch directory; that is the archaeology
	// this closes.
	//
	// CORPUS-SAFE BY CONSTRUCTION, every one: canonical ids, closed
	// contract/graphrank enums, booleans and counts. Never a question,
	// never a label, never model text. trialCase.Question is in scope at
	// every site that fills these and is deliberately never read.

	// CommittedSubjects is what this arm's turn actually committed
	// (Kind+CanonicalID), the same shape as Baseline/HintedCommittedSubjects
	// above. Populated on EVERY arm whenever anything was committed, not
	// just on the wrong ones: a right commit and a wrong commit are the
	// same row shape, and only the pairing with ExpectedID below tells
	// them apart.
	CommittedSubjects []twoTurnSubjectKindID `json:"committed_subjects,omitempty"`
	// ExpectedKind/ExpectedID mirror trialCase's own oracle for this case,
	// so committed-versus-expected sit SIDE BY SIDE on one row.
	//
	// Stamped unconditionally, including on rows that committed nothing.
	// A false_no_match row is exactly as much a correctness finding as a
	// wrong_commit one (CHAOS-4039), and "expected this, returned no
	// subject" is unreadable without the left-hand side.
	ExpectedKind string `json:"expected_kind,omitempty"`
	ExpectedID   string `json:"expected_id,omitempty"`
	// CommitGate/TiedStatisticalTop/SearchTruncated mirror the
	// decision-stage graphrank.ResolutionTraceEvent for this arm's own
	// turn-2 call -- graphrank's closed gate vocabulary and the two
	// CHAOS-4085 shape flags.
	//
	// Together they make a REFUSAL artifact-attestable, which is what the
	// rerun proved impossible on v10: an ambiguous outcome with an empty
	// CommitGate cannot be told apart from a tied-top-under-truncation
	// refusal without them, and the harness read both in process and then
	// dropped them on the floor.
	CommitGate         string `json:"commit_gate,omitempty"`
	TiedStatisticalTop bool   `json:"tied_statistical_top,omitempty"`
	SearchTruncated    bool   `json:"search_truncated,omitempty"`
	// KindCoverageFloorFired/KindCoverageMissingKinds/
	// KindCoverageFloorTruncated mirror the kind_coverage_floor-stage trace
	// event (CHAOS-4038's floor; see ResolutionTraceEvent's own doc
	// comment). Read off a DIFFERENT stage than the three fields above, and
	// that separation is deliberate rather than incidental -- the floor's
	// truncation is explicitly not a commit-gate input.
	//
	// CHAOS-4183 phase 2 (team-lead ruling, 2026-08-23): `omitempty` DROPPED
	// on all three. This is the motivating incident, not a hypothetical --
	// a live CHAOS-4183 investigation queried this artifact with `jq` and
	// read a `null`/absent key as "the kind_coverage_floor event never
	// fired," when the event HAD fired with the real values
	// Fired=false/MissingKinds=0 (Go zero values), just dropped from the
	// JSON by `omitempty`. For a boolean/count field a reader uses to GATE
	// further analysis (this exact field decided which of two investigation
	// branches to follow), "absent" and "false/0" must be the SAME
	// observable state, or a query tool with no way to distinguish
	// key-absent from Go-zero-value silently reintroduces the
	// never-ran-vs-attested-zero ambiguity CensusRan/EvidenceRoundEntered
	// (CHAOS-3899/CHAOS-4161) and KindOfferDistinctKindCount (CHAOS-4012)
	// were each already filed specifically to resolve. Same reasoning,
	// applied retroactively to a field pair that predates it.
	KindCoverageFloorFired     bool `json:"kind_coverage_floor_fired"`
	KindCoverageMissingKinds   int  `json:"kind_coverage_missing_kinds"`
	KindCoverageFloorTruncated bool `json:"kind_coverage_floor_truncated"`
	// KindCoverageMissingKindsList (CHAOS-4183 phase 2) is
	// KindCoverageMissingKinds' own kind-IDENTITY twin -- closed-vocabulary
	// contextfabric SubjectKind values only (corpus-safe, same discipline
	// KindOfferBoundaryKinds/distinctCandidateKinds already established,
	// CHAOS-4012). Added because the bare COUNT alone could not disambiguate
	// a real re-smoke finding: whether the floor searched for the SAME kind
	// a later analysis cares about, or a different one, once more than one
	// floor kind could be missing for a single call. No omitempty, same
	// reasoning as the trio above -- an empty/nil list must read as
	// "genuinely nothing missing," not "never measured."
	KindCoverageMissingKindsList []string `json:"kind_coverage_missing_kinds_list"`
	// ConfirmedKindRescueFired/ConfirmedKindRescueResultCount/
	// ConfirmedKindRescueTruncated (CHAOS-4038 v18) mirror the three
	// KindCoverageFloor* fields above, read off the confirmed_kind_rescue
	// trace stage (CHAOS-4132) instead -- kindCoverageQueryLimit's shared-
	// const coupling with that mechanism (see the constant's own doc
	// comment, chaos4038_kind_coverage.go) is the reason this arm's own
	// rescue outcome must be adjudicable from this report alone.
	ConfirmedKindRescueFired       bool `json:"confirmed_kind_rescue_fired,omitempty"`
	ConfirmedKindRescueResultCount int  `json:"confirmed_kind_rescue_result_count,omitempty"`
	ConfirmedKindRescueTruncated   bool `json:"confirmed_kind_rescue_truncated,omitempty"`
	// KindOfferExplicitHintCount/KindOfferDistinctKindCount/
	// KindOfferSuppressedByCardinality (CHAOS-4012 v20) mirror the
	// kind_offer trace stage (kindOfferMaterial's own suppression check,
	// chaos3900_structure_offers.go) -- filed to resolve the SAME
	// never-ran-vs-attested-zero ambiguity CensusRan/CensusComplete/
	// CensusCount already resolve for the census round, one layer over on
	// the offer builder: DistinctKindCount==0 is "genuinely nothing
	// offerable in the pool"; ==1 with SuppressedByCardinality==true is
	// exactly the "expected_kind IS in the pool, still never offered" gap
	// CHAOS-4012 investigates. UNLIKE kind_coverage_floor/
	// confirmed_kind_rescue above, this stage is UNCONDITIONAL --
	// kindOfferMaterial runs on every resolution -- so, mirroring
	// CensusRan/CensusComplete/CensusCount's own reasoning, NO omitempty on
	// any of the three: 0/false is exactly the value that distinguishes
	// "genuinely zero/not suppressed" from an absent capture, and hiding it
	// would reintroduce the ambiguity these fields exist to resolve.
	KindOfferExplicitHintCount       int  `json:"kind_offer_explicit_hint_count"`
	KindOfferDistinctKindCount       int  `json:"kind_offer_distinct_kind_count"`
	KindOfferSuppressedByCardinality bool `json:"kind_offer_suppressed_by_cardinality"`
	// CandidateOfferCount/OfferKind (CHAOS-4012 v22) are the candidate-list
	// axis's own pair on the SAME kind_offer stage above -- both axes fire
	// independently (kind-pick unchanged; candidate-list on Committed==0 &&
	// pool non-empty), so OfferKind's closed vocabulary ("kind"/"candidate"/
	// "both"/"") is what tells a reader WHICH axis(es) actually fired on this
	// call, same unconditional no-omitempty reasoning as the trio above: ""
	// is exactly "neither axis fired," not an absent capture.
	CandidateOfferCount int    `json:"candidate_offer_count"`
	OfferKind           string `json:"offer_kind"`
	// HandleOfferGraphDerivedCount/HandleOfferGraphDerivedRejectedCount
	// (CHAOS-4119, schema v27) mirror the SAME kind_offer stage's
	// HandleOfferGraphDerivedCount/HandleOfferGraphDerivedRejectedCount
	// (ResolutionTraceEvent) -- see handleOfferDiagnostics' own doc comment
	// (chaos3900_structure_offers.go) for what each measures. Same
	// no-omitempty reasoning as CandidateOfferCount/OfferKind above: 0 is
	// exactly "the pool contributed/rejected nothing on this call," not an
	// absent capture.
	HandleOfferGraphDerivedCount         int `json:"handle_offer_graph_derived_count"`
	HandleOfferGraphDerivedRejectedCount int `json:"handle_offer_graph_derived_rejected_count"`
	// ArmInvalidStage/ArmInvalidErrorType pair with ArmInvalidReason, whose
	// closed vocabulary names the error CLASS but not where it came from.
	//
	// ArmInvalidStage is contextfabric's own closed InvestigationStage enum
	// (FailureStage). It is the load-bearing half: CHAOS-4098's defect
	// presented as a bare "invalid_result" and cost a re-read of raw
	// exchange files to place, when stage=="validation" would have named
	// the failing family immediately.
	//
	// ArmInvalidErrorType is CHAOS-4088's %T fingerprint of the INNERMOST
	// unwrapped error -- never .Error() text, which can carry upstream
	// detail this outcome-only artifact must not persist. Read it as a
	// hint, not an oracle: a rule written with fmt.Errorf carries no named
	// type, so this degrades to *errors.errorString for exactly the
	// validator rules one most wants named. See twoTurnInnermostErrorType
	// for the multi-%w wrapping that makes even reaching it non-obvious.
	ArmInvalidStage     string `json:"arm_invalid_stage,omitempty"`
	ArmInvalidErrorType string `json:"arm_invalid_error_type,omitempty"`
	// ---------------------------------------------------------------
	// CHAOS-4103 synthesis-status override provenance
	// ---------------------------------------------------------------
	//
	// THE BAR THESE EXIST FOR: applySynthesisStatusOverride's own occurrence
	// (chaos4098_synthesis_status.go) must be adjudicable from THIS REPORT
	// ALONE, exactly the same standard CHAOS-4086's block above holds.
	// Before them, the override was visible only as a WARN slog line in a
	// scratch gotest log -- proving it fired zero times across a run cost a
	// re-read of raw model-exchange files and log-level reasoning, the
	// exact archaeology this closes.
	//
	// SynthesisStatusOverrideFired is stamped on EVERY arm, unconditionally
	// -- false rather than omitted on a row the override never touched, the
	// same "stamp unconditionally" discipline ExpectedKind/ExpectedID use
	// (CHAOS-4086), so an absent-from-JSON override reads as
	// measured-and-did-not-fire, never as not-measured.
	SynthesisStatusOverrideFired bool `json:"synthesis_status_override_fired"`
	// SynthesisStatusOverrideFrom/To/Reason/CommittedCount mirror
	// SynthesisStatusOverrideOutcome's identically-named fields
	// byte-for-byte (contextfabric package) -- populated only when Fired is
	// true, meaningless (and left at their zero value) otherwise.
	//
	// Reason is the field this ticket exists for: a DISTINCT closed-vocab
	// value (SynthesisStatusOverrideClarificationUnavailableUncommitted)
	// when CommittedCount is zero, versus the ordinary
	// SynthesisStatusOverrideClarificationUnavailable when a subject WAS
	// committed -- see that constant's own doc comment for why the
	// uncommitted shape is a genuine engine routing bug rather than a
	// second instance of ordinary model under-claiming, and why a reader
	// must never be left to re-derive the distinction from
	// CommittedCount==0 alone.
	//
	// CommittedCount is carried here too, deliberately redundant with the
	// arm's own top-level CommittedCount field above (the override never
	// changes committed state, so the two always agree when Fired) --
	// self-contained on purpose, so this block never requires a reader to
	// cross-reference another field to know which shape fired. No
	// omitempty: 0 is exactly the value that distinguishes the two reasons,
	// and hiding it here would be the same defect this ticket exists to fix.
	SynthesisStatusOverrideFrom           string `json:"synthesis_status_override_from,omitempty"`
	SynthesisStatusOverrideTo             string `json:"synthesis_status_override_to,omitempty"`
	SynthesisStatusOverrideReason         string `json:"synthesis_status_override_reason,omitempty"`
	SynthesisStatusOverrideCommittedCount int    `json:"synthesis_status_override_committed_count"`
	// ---------------------------------------------------------------
	// CHAOS-4120 turn-1 facts (stamped identically onto EVERY arm's row
	// for this case -- see twoTurnStampTurn1Facts's own doc comment for
	// why these are separate, Turn1-prefixed fields rather than reusing
	// CommitGate/TiedStatisticalTop/SearchTruncated/KindCoverage* above)
	// ---------------------------------------------------------------
	//
	// Turn1CommitGate/Turn1TiedStatisticalTop/Turn1SearchTruncated mirror
	// CommitGate/TiedStatisticalTop/SearchTruncated above, but read off
	// TURN 1's own decision-stage trace event instead of this arm's own
	// (turn-2) call. Populated on every row for the case, including an
	// offer-miss row that never makes a turn-2 call at all -- the exact
	// gap this ticket closes: "every offer_miss is a turn-1 fact".
	Turn1CommitGate         string `json:"turn1_commit_gate,omitempty"`
	Turn1TiedStatisticalTop bool   `json:"turn1_tied_statistical_top,omitempty"`
	Turn1SearchTruncated    bool   `json:"turn1_search_truncated,omitempty"`
	// Turn1KindCoverageFloorFired/Turn1KindCoverageMissingKinds/
	// Turn1KindCoverageFloorTruncated mirror KindCoverageFloorFired/
	// KindCoverageMissingKinds/KindCoverageFloorTruncated above, read off
	// turn 1's own kind_coverage_floor trace stage. `omitempty` DROPPED
	// (CHAOS-4183 phase 2) for the SAME reason as the arm-level trio above
	// -- see that field's own doc comment for the motivating incident.
	Turn1KindCoverageFloorFired     bool `json:"turn1_kind_coverage_floor_fired"`
	Turn1KindCoverageMissingKinds   int  `json:"turn1_kind_coverage_missing_kinds"`
	Turn1KindCoverageFloorTruncated bool `json:"turn1_kind_coverage_floor_truncated"`
	// Turn1KindCoverageMissingKindsList (CHAOS-4183 phase 2) mirrors
	// KindCoverageMissingKindsList above, read off turn 1's own
	// kind_coverage_floor trace stage. No omitempty, same reasoning.
	Turn1KindCoverageMissingKindsList []string `json:"turn1_kind_coverage_missing_kinds_list"`
	// Turn1ConfirmedKindRescueFired/Turn1ConfirmedKindRescueResultCount/
	// Turn1ConfirmedKindRescueTruncated (CHAOS-4038 v18) mirror
	// ConfirmedKindRescueFired/ResultCount/Truncated above, read off turn
	// 1's own confirmed_kind_rescue trace stage instead of this arm's own
	// turn-2 call.
	Turn1ConfirmedKindRescueFired       bool `json:"turn1_confirmed_kind_rescue_fired,omitempty"`
	Turn1ConfirmedKindRescueResultCount int  `json:"turn1_confirmed_kind_rescue_result_count,omitempty"`
	Turn1ConfirmedKindRescueTruncated   bool `json:"turn1_confirmed_kind_rescue_truncated,omitempty"`
	// Turn1KindOfferExplicitHintCount/Turn1KindOfferDistinctKindCount/
	// Turn1KindOfferSuppressedByCardinality (CHAOS-4012 v20) mirror
	// KindOfferExplicitHintCount/DistinctKindCount/SuppressedByCardinality
	// above, read off turn 1's own kind_offer trace stage instead of this
	// arm's own turn-2 call. Same no-omitempty reasoning as the top-level
	// trio -- 0/false is meaningful here, not absence.
	Turn1KindOfferExplicitHintCount       int  `json:"turn1_kind_offer_explicit_hint_count"`
	Turn1KindOfferDistinctKindCount       int  `json:"turn1_kind_offer_distinct_kind_count"`
	Turn1KindOfferSuppressedByCardinality bool `json:"turn1_kind_offer_suppressed_by_cardinality"`
	// Turn1KindOfferDistinctKindCountBeforeRepair/
	// Turn1KindOfferSuppressedByCardinalityBeforeRepair (CHAOS-4183 phase 3,
	// sol design consult, team-lead ratified 2026-08-23, schema v26) mirror
	// KindOfferDistinctKindCountBeforeRepair/
	// KindOfferSuppressedByCardinalityBeforeRepair (ResolutionTraceEvent),
	// read off turn 1's own kind_offer trace stage -- the PRE-repair twins
	// of Turn1KindOfferDistinctKindCount/Turn1KindOfferSuppressedByCardinality
	// above, same no-omitempty reasoning.
	Turn1KindOfferDistinctKindCountBeforeRepair       int  `json:"turn1_kind_offer_distinct_kind_count_before_repair"`
	Turn1KindOfferSuppressedByCardinalityBeforeRepair bool `json:"turn1_kind_offer_suppressed_by_cardinality_before_repair"`
	// Turn1CandidateOfferCount/Turn1OfferKind (CHAOS-4012 v22) mirror
	// CandidateOfferCount/OfferKind above, read off turn 1's own kind_offer
	// trace stage instead of this arm's own turn-2 call.
	Turn1CandidateOfferCount int    `json:"turn1_candidate_offer_count"`
	Turn1OfferKind           string `json:"turn1_offer_kind"`
	// Turn1HandleOfferGraphDerivedCount/Turn1HandleOfferGraphDerivedRejectedCount
	// (CHAOS-4119, schema v27) mirror HandleOfferGraphDerivedCount/
	// HandleOfferGraphDerivedRejectedCount above, read off turn 1's own
	// kind_offer trace stage instead of this arm's own turn-2 call.
	Turn1HandleOfferGraphDerivedCount         int `json:"turn1_handle_offer_graph_derived_count"`
	Turn1HandleOfferGraphDerivedRejectedCount int `json:"turn1_handle_offer_graph_derived_rejected_count"`
	// Turn1TermSearchTruncated/Turn1QuestionSearchTruncated (the
	// per-pass truncation breakdown) are turn 1's own per-term "search"
	// pass and question-level "search_question" pass truncation signals,
	// read off resolve.go's newly per-event Truncated field
	// (twoTurnTraceCapture.passTruncation) -- the coverage-floor SearchKind
	// pass is the ALREADY-existing Turn1KindCoverageFloorTruncated above,
	// completing the 3-way breakdown (Search vs SearchQuestion vs
	// coverage-floor SearchKind) the decomposition could not draw from one
	// pooled SearchTruncated flag alone.
	Turn1TermSearchTruncated     bool `json:"turn1_term_search_truncated,omitempty"`
	Turn1QuestionSearchTruncated bool `json:"turn1_question_search_truncated,omitempty"`
	// ExpectedInPool (CHAOS-4038's exact question) is true when turn 1's
	// own merged candidate pool contained ANY candidate of this case's
	// oracle expected kind, whether or not it was ever offered back as a
	// KindOption -- the "in the pool but not offered" versus "never
	// retrieved" distinction CHAOS-4038 needed and could not draw from the
	// artifact alone. See twoTurnTraceCapture.poolContainsKind.
	ExpectedInPool bool `json:"expected_in_pool"`
	// ExpectedKindAtOfferBoundary (CHAOS-4012 v22, team-lead ruling
	// 2026-08-23, re-smoke follow-up) is ExpectedInPool's call-boundary-
	// scoped refinement: true only when the expected kind survived to the
	// EXACT slice kindOfferMaterial/candidateOfferMaterial read at their
	// shared call boundary, distinct from ExpectedInPool's trace-wide
	// "corroborated ANYWHERE, before final truncation" reading. Same
	// no-omitempty reasoning as CensusRan/ExpectedInPool's own siblings:
	// false is a real, meaningful boundary-absence signal, not an unset
	// field. See twoTurnTraceCapture.boundaryContainsKind.
	ExpectedKindAtOfferBoundary bool `json:"expected_kind_at_offer_boundary"`
	// ExpectedKindAtOfferBoundaryBeforeRepair (CHAOS-4183 phase 3, sol
	// design consult, team-lead ratified 2026-08-23, schema v26) is
	// ExpectedKindAtOfferBoundary's PRE-repair twin: true only when the
	// expected kind survived to the boundary WITHOUT this phase's
	// post-decision kind-only completion -- exactly what
	// ExpectedKindAtOfferBoundary itself measured before this phase
	// existed. A row where this reads false but ExpectedKindAtOfferBoundary
	// reads true is a Shape-A row the repair fixed; a row where both stay
	// false the repair could not reach (fullPool itself lacked the kind, or
	// something already committed). See
	// twoTurnTraceCapture.boundaryContainsKindBeforeRepair.
	ExpectedKindAtOfferBoundaryBeforeRepair bool `json:"expected_kind_at_offer_boundary_before_repair"`
	// Turn1OfferComposedUnderWindowGate (CHAOS-4234, schema v29) mirrors
	// twoTurnTurn1Facts.OfferComposedUnderWindowGate -- see that field's
	// own doc comment. No omitempty: a false on a regime-A row is the
	// finding ("the gate composed nothing"), not an absence.
	Turn1OfferComposedUnderWindowGate bool `json:"turn1_offer_composed_under_window_gate"`
	// ExpectedSubjectInPool/ExpectedSubjectRank/ExpectedSubjectAtOfferBoundary
	// (CHAOS-4234, schema v29) mirror twoTurnTurn1Facts' identically-named
	// fields -- the subject-level twins of ExpectedInPool/
	// ExpectedKindAtOfferBoundary. No omitempty on the bools, same reason as
	// above; Rank 0 means "never reached the cut" and is a real reading.
	ExpectedSubjectInPool          bool `json:"expected_subject_in_pool"`
	ExpectedSubjectRank            int  `json:"expected_subject_rank"`
	ExpectedSubjectAtOfferBoundary bool `json:"expected_subject_at_offer_boundary"`
	// ExpectedSubjectRetrievalSource (CHAOS-4348, schema v37) mirrors
	// twoTurnTurn1Facts.ExpectedSubjectRetrievalSource -- see that field's
	// own doc comment. "exact_name" / "kind_scoped" / "both" / "ordinary" / "absent".
	ExpectedSubjectRetrievalSource string `json:"expected_subject_retrieval_source"`
	// OracleIDSchemeMismatch (CHAOS-4348 measurement-layer fix, schema v38)
	// mirrors twoTurnTurn1Facts.OracleIDSchemeMismatch -- see that field's
	// own doc comment. No omitempty: a false is a reading (the oracle id
	// DOES match its kind's live scheme), not an absence.
	OracleIDSchemeMismatch bool `json:"oracle_id_scheme_mismatch"`
	// Turn2WindowReceiptAttached (CHAOS-4234, schema v29, positive arm only)
	// records the harness semantics change this ticket made: on a regime-A
	// case (turn 1 window-gated), the positive arm's turn 2 now carries the
	// oracle's window receipt BESIDE the member's own receipt, so turn 2
	// can clear both gates in one turn instead of re-gating on the window.
	// The baseline pair (schema v27) sent ONE receipt per turn 2 -- so
	// offer_miss aggregates stay engine-only comparable across the bump,
	// while turn-2 aggregates carry this change as part of the lever.
	Turn2WindowReceiptAttached bool `json:"turn2_window_receipt_attached"`
	// AnchorOptionsCount/HandleOptionsCount are turn 1's own
	// StructureNeeds.AnchorOptions/HandleOptions counts (zero when turn 1
	// carried no StructureNeeds). For anchor: count==1 is a designed
	// single-candidate suppression, count==0 is a zero-claimant recall
	// failure. For handle (CHAOS-4119, schema v27, MEANING change): before
	// this ticket, count==0 meant "neither an explicit handle nor
	// BindHandles' own question-text regex matched anything", and count>0
	// meant one of those two matched -- just possibly the wrong claimant.
	// HandleOptionsCount can now ALSO be non-zero purely from the
	// graph-derived source (a candidate the resolution's own pool already
	// found), with no explicit or question-text match at all --
	// HandleOptionsCountBeforeGraphSource (below) carries the OLD reading,
	// so a v26-comparable count is still recoverable from a v27 row.
	AnchorOptionsCount int `json:"anchor_options_count"`
	HandleOptionsCount int `json:"handle_options_count"`
	// HandleOptionsCountBeforeGraphSource (CHAOS-4119, schema v27) is
	// HandleOptionsCount's own PRE-graph-source twin: turn 1's
	// handleOfferDiagnostics.CountBeforeGraphSource (explicit + BindHandles
	// only, deduped, capped) -- exactly what HandleOptionsCount itself
	// measured before this ticket. Diffing the two isolates how many of
	// this row's handle options came from the graph-derived source alone.
	// Same shape as ExpectedKindAtOfferBoundaryBeforeRepair above: single,
	// turn-1-only, no omitempty (0 is a real "nothing from explicit/text"
	// reading, not an absent capture).
	HandleOptionsCountBeforeGraphSource int `json:"handle_options_count_before_graph_source"`
	// CensusRan/CensusComplete/CensusCount are turn 1's own
	// twoTurnTraceCapture.censusRan()/censusComplete()/censusCount() --
	// whether CensusFunc was invoked at all during turn 1, whether EVERY
	// kind it probed completed without error, and if so the total row count
	// attested summed across every kind. Read together: CensusRan==false
	// means never ran (unmeasured); CensusRan==true && CensusComplete==false
	// means it ran but at least one probed kind's own CensusFunc call
	// errored (codex review round 2, P2 -- a kind whose call errors traces
	// CensusCount==0 identically to one that genuinely attested zero rows,
	// chaos3899_evidence_round.go's own `if err != nil` branch, so
	// CensusCount is NOT a trustworthy "attested absence" reading here);
	// CensusRan==true && CensusComplete==true && CensusCount==0 means a
	// REAL, census-backed absence across every probed kind. No omitempty on
	// any of the three: 0/false is exactly the value that distinguishes
	// these states, and hiding it would reintroduce the very
	// never-ran-vs-attested-zero (and now errored-vs-attested-zero)
	// ambiguity these fields exist to resolve.
	CensusRan      bool `json:"census_ran"`
	CensusComplete bool `json:"census_complete"`
	CensusCount    int  `json:"census_count"`
	// EvidenceRoundEntered/EvidenceRoundReason (CHAOS-4161 v19) mirror
	// twoTurnTurn1Facts.EvidenceRoundEntered/EvidenceRoundReason -- see that
	// field's own doc comment for the exact reading (never-entered /
	// entered-and-named-refusal / entered-with-no-named-reason -- the last
	// of those is genuinely two-way ambiguous between a terminal
	// would_commit/would_no_match and the decisive switch's own
	// unnamed-ambiguity default arm; ShadowOutcome would be needed to tell
	// them apart and is out of this ticket's scope) and why an empty
	// EvidenceRoundReason is NOT itself the fail-closed "never entered"
	// signal either way -- EvidenceRoundEntered is. No omitempty on either,
	// the same reason CensusRan/CensusComplete/CensusCount above have none:
	// false and "" are exactly the values that distinguish "never entered"
	// from "entered, no named reason," and hiding them would reintroduce
	// the ambiguity this pair exists to resolve. Filed because
	// census_ran==false was being read as "CensusFunc is nil" corpus-wide
	// when it actually meant "the round entered (or not) but never reached
	// the per-kind CensusFunc loop" -- these two fields make that
	// distinction directly observable without needing the (redacted-on-
	// non-anomalous-rows) full trace.
	EvidenceRoundEntered bool   `json:"evidence_round_entered"`
	EvidenceRoundReason  string `json:"evidence_round_reason"`
	// Regime (CHAOS-4120) mirrors twoTurnTurn1Facts.Regime -- see that
	// field's own doc comment for the engine-native (never output-shape-
	// inferred) derivation and why this is a closed vocabulary string
	// rather than a bool: "regime_a_window_gated" |
	// "regime_b_resolution_proceeded", empty ("") when nothing was ever
	// recorded OR when the recorded outcome is neither claim (gate 1,
	// refused-no-clarification, or any Veto* value) -- see
	// twoTurnRegimeFromWindowCanonicalization's own doc comment for exactly
	// which outcomes fall into each bucket.
	Regime string `json:"turn1_regime,omitempty"`
	// Turn1WindowExpandOffered/Turn2WindowExpandAccepted (CHAOS-4314, schema
	// v31) mirror twoTurnTurn1Facts.WindowExpandOffered and the positive
	// arm's own ConfirmedStructure check (twoTurnWindowExpandAccepted) --
	// the offer_kind=window_expand accepted/declined pair: Offered is
	// stamped on every arm's row for the case (twoTurnStampTurn1Facts, same
	// as every other Turn1-prefixed field); Accepted is stamped ONLY on the
	// positive arm, since that is the only arm whose turn 2 can carry a
	// PriorWindowReceipts redemption at all. No omitempty on either: false
	// is a reading ("this case's gate had nothing to recommend" /
	// "the recommendation was not redeemed"), never an absence.
	Turn1WindowExpandOffered  bool `json:"turn1_window_expand_offered"`
	Turn2WindowExpandAccepted bool `json:"turn2_window_expand_accepted"`
	// InferredWindowExpandOffered (CHAOS-4336, schema v34) is the field
	// WindowGatedOfferedCount/WindowGatedSilentCount (chaos3742_two_turn_confirmation_test.go's
	// own reporting block) SHOULD have read from the start: whether window_expand
	// was offered on the inferred_tier arm's OWN gated call (member=="window"
	// only -- the call that sets TimeContext.EvidenceWindow to the case's
	// NegativeWindowBand and that the WindowGatedCount condition just above
	// this field's own reporting-block site is itself evaluated against),
	// computed directly off that SAME call's own result.StructureNeeds.WindowExpandOptions
	// in runTwoTurnInferredTierArm. Turn1WindowExpandOffered above is a
	// DIFFERENT, unrelated call -- the one shared, window-blind "turn1"
	// request every arm's row copies (twoTurnStampTurn1Facts) -- and reading
	// it for the window_gated_silent bar was CHAOS-4336's actual defect:
	// turn1 sets no window field at all, so it very often never reaches any
	// window gate (window canonicalization outcome=none), making
	// Turn1WindowExpandOffered false regardless of what the inferred_tier
	// arm's own gated call did. Meaningful only on member=="window" rows;
	// zero-value (never computed) elsewhere, same convention WrongCommit and
	// several other member-scoped fields on this struct already use.
	InferredWindowExpandOffered bool `json:"inferred_window_expand_offered"`
	// InferredWindowAlreadyWidest (CHAOS-4336 follow-up, schema v35) is
	// true when the inferred_tier arm's own gated call (member=="window"
	// only) resolved an effective window already at the registry's widest
	// tier (all_time) -- pickWindowExpandTarget has nothing wider to
	// recommend by design, so InferredWindowExpandOffered=false here is a
	// legitimate non-offer, not a defect. Excludes these rows from
	// WindowGatedSilentCount (twoTurnClassifyWindowGateOutcome), which
	// otherwise conflated "genuinely nothing wider exists" with "should
	// have offered but didn't" -- exactly the ambiguity Run E's
	// window_gated_silent=2/65 (cases 53/56, both confirmed all_time via
	// a by-hand annex cross-check) needed a side lookup to resolve.
	InferredWindowAlreadyWidest bool `json:"inferred_window_already_widest"`
}

// twoTurnCaseResultIsAnomalous (CHAOS-4135) reports whether res trips one of
// this test's own zero-tolerance bars, or is an inferred_tier pairing this
// run could not vouch for -- the exact population TraceEvents/
// BaselineTraceEvents (above) are retained for; the redaction pass at the
// tail of TestChaos3742TwoTurnConfirmationReplay clears both back to nil on
// every row this reports false for, immediately before the report is
// serialized.
//
// Mirrors the LITERAL per-row conditions the final gate loop
// (TestChaos3742TwoTurnConfirmationReplay) already computes its aggregate
// counts from (WrongCommitCount, FalseNoMatchCount,
// SynthesisStatusOverrideUncommittedCount, InferredPairInvalidCount,
// InferredUnjustifiedCount, window's own per-row WindowCommitCount and
// WindowGatedCount contributions, the tier-routing proof loop, and a
// mutation probe that ran without tripping) -- deliberately NOT re-derived
// from those run-level totals, which cannot be mapped back to the ONE row
// that caused them. See TestTwoTurnCaseResultIsAnomalous for the pin
// proving each bar is covered.
func twoTurnCaseResultIsAnomalous(res twoTurnCaseResult) bool {
	if res.WrongCommit || res.FalseNoMatch || res.PairInvalid {
		return true
	}
	if res.InferredClassification == "unjustified" {
		return true
	}
	if res.SynthesisStatusOverrideFired &&
		res.SynthesisStatusOverrideReason == string(contextfabric.SynthesisStatusOverrideClarificationUnavailableUncommitted) {
		return true
	}
	// window's own "ANY commit fails" bar (WindowCommitCount): mirrors the
	// run-level counting block's own three guards -- inferred_tier arm,
	// window member, a call that did not error (ArmInvalidReason=="" --
	// an errored call never reaches a commit at all).
	if res.Arm == string(twoTurnArmInferredTier) && res.Member == string(contractsv1.ContextFabricStructureNeedWindow) &&
		res.ArmInvalidReason == "" && res.CommittedCount > 0 {
		return true
	}
	// Tier-routing proof (codex xhigh review, MEDIUM, confirmed): mirrors
	// the "Positive tier-routing proof" loop at the end of
	// TestChaos3742TwoTurnConfirmationReplay -- every inferred_tier row
	// that ran (ArmInvalidReason=="") must show the injected value routed
	// to explicit_unattributed/inferred_default (window's own version:
	// EffectiveEvidenceWindow.Provenance==inferred_default). A miss here
	// is its own finding, independent of whether the row also committed
	// correctly.
	if res.Arm == string(twoTurnArmInferredTier) && res.ArmInvalidReason == "" && !res.TierRoutedCorrectly {
		return true
	}
	// window's own CHAOS-4040 gate-signature bar (codex xhigh review,
	// MEDIUM, confirmed): mirrors WindowGatedCount != WindowInferredTierRanCount
	// -- the per-row shape that increments WindowGatedCount at its own call
	// site is clarification_required + TierRoutedCorrectly + CommittedCount==0
	// TOGETHER (the gate's own signature), not merely "committed nothing".
	// A ran window row missing any of the three is exactly the population
	// that mismatch bar exists to catch; overlaps the CommittedCount>0
	// bar above for that one case, which is harmless (either check alone
	// already reports true).
	if res.Arm == string(twoTurnArmInferredTier) && res.Member == string(contractsv1.ContextFabricStructureNeedWindow) &&
		res.ArmInvalidReason == "" &&
		!(res.Turn2Status == string(contractsv1.ContextFabricInvestigationClarificationRequired) && res.TierRoutedCorrectly && res.CommittedCount == 0) {
		return true
	}
	// A mutation probe that ran but did not trip (design brief's own
	// fails-toward-fine discipline for that arm) -- MutationProbe is only
	// ever non-empty on a mutation-arm row.
	if res.MutationProbe != "" && !res.MutationTripped {
		return true
	}
	return false
}

// twoTurnRedactNonAnomalousTraceEvents (CHAOS-4135) clears TraceEvents/
// BaselineTraceEvents back to nil, in place, on every row
// twoTurnCaseResultIsAnomalous reports false for -- called exactly once,
// immediately before TestChaos3742TwoTurnConfirmationReplay serializes its
// report, and extracted to its own function (rather than left inline at
// that one call site) purely so it has a unit-test surface independent of
// that function's full investigator/live-corpus machinery
// (TestTwoTurnRedactNonAnomalousTraceEvents).
func twoTurnRedactNonAnomalousTraceEvents(results []twoTurnCaseResult) {
	for i := range results {
		if !twoTurnCaseResultIsAnomalous(results[i]) {
			results[i].TraceEvents = nil
			results[i].BaselineTraceEvents = nil
		}
	}
}

// reportSchemaVersion is twoTurnReport.ReportSchemaVersion's own literal --
// named (CHAOS-4307) so the version this process actually stamps and the
// version cmd/acr-trial-merge-two-turn/main.go's own expectedSchemaVersion
// requires can each be pinned by a dedicated test independent of the much
// larger live-corpus TestChaos3742TwoTurnConfirmationReplay.
//
// KEEP IN SYNC WITH cmd/acr-trial-merge-two-turn/main.go's expectedSchemaVersion
// (codex round 2, Low, confirmed): this file lives in package hosted_test and
// cannot be imported from that separate binary's package main, so nothing
// can assert cross-package agreement directly -- the SAME pre-existing
// limitation every one of this constant's 31 prior bumps has always had
// (TestRunRejectsSchemaVersionMismatch only proves the merger REFUSES a
// mismatched artifact at runtime, never that the two literals themselves
// agree at build time). Bump both in the SAME change; a mismatch surfaces
// live the moment a real producer artifact is merged, per that test.
//
// "38" (CHAOS-4348 measurement-layer fix): purely additive --
// twoTurnCaseResult gains OracleIDSchemeMismatch, twoTurnReport gains
// OracleIDSchemeMismatchCount (a new zero-tolerance gate, alongside
// WrongCommitCount). No merge arithmetic changes beyond a plain sum, same
// as WrongCommitCount/FalseNoMatchCount.
//
// "39" (CHAOS-4348 corpus/annex sync, team-lead ruling 2026-08-27):
// purely additive -- twoTurnReport gains AnnexSignoffStale (true iff the
// annex's chris-approved signoff names an older corpus_sha8 than the
// live one, per-run informational flag, cross-shard-consistency-checked
// and first-shard-wins like OracleAnnexSignedOff, no merge arithmetic).
const reportSchemaVersion = "39"

type twoTurnReport struct {
	// ReportSchemaVersion (codex round-1 finding #2, follow-up PR: field
	// renames are otherwise invisible to a consumer parsing the JSON --
	// StructureAndWindowDisclosureAbsentCount replaced NoDiscriminatorsCount
	// under the SAME struct with no version marker). "3" marks the CHAOS-3742
	// run-2 root-cause fix (team-lead ruling 2026-08-20): ControlsWitnessed's
	// MEANING changed (brief-conformant control-ok, not literal
	// Status==no_match) and ControlsWitnessedNoMatchCensusBacked was added
	// to carry the old, narrower signal informationally. "4" marks
	// CHAOS-4039's own v4 measurement contract (sol-max ruling
	// 2026-08-20): InferredTierAnyCommit/InferredTierSingleSatisfierVerifiedCount
	// (a blanket "any commit fails" bar for every member, with a generic
	// would_commit proof proxy) are REMOVED, replaced by the member-specific
	// fields below -- kind/handle commits are no longer an unconditional
	// failure, judged instead against the new baseline_equivalent/
	// kind_insensitivity_attested/unjustified partition; window keeps the
	// OLD literal zero-commit bar unchanged (WindowCommitCount). "5" marks
	// the CHAOS-4040 harness follow-up (PR #181's own companion, sol-max
	// ruling 2026-08-21): CHAOS-4040 shipped an unconditional window gate
	// (not the kind/handle-style noninterference proof), which makes
	// WindowCommitCount alone a VACUOUS bar (structurally always 0 now) --
	// WindowInferredTierRanCount/WindowArmErrorCount/WindowGatedCount/
	// WindowClassDefaultGatedCount were added to prove the gate itself is
	// what produced that zero, not an unmeasured or broken arm; see their
	// own doc comments. "3" marked the rename plus the controls/anti-
	// vacuity/single-satisfier fields below; "1" was the shape PR #167
	// shipped. "6" marks CHAOS-4033's shard-selection support (codex
	// round-3 finding): ApplicableMembers is new (the set backing this
	// process's own AntiVacuityValid, serialized so a sharded run's merge
	// step can recompute anti-vacuity over the union of shards), and the
	// shared trialProvenance struct gained ExecutionShape/ShardIndex/
	// ShardCount. "7" marks CHAOS-4058's timing observability follow-up:
	// Timings/TimingSummary are new (per-case/per-arm wall-clock and
	// file-exchange responder round-trip cost, plus a run-level
	// aggregate) -- purely additive, carries no measurement semantics, and
	// backs none of this file's pass/fail checks. "8" marks CHAOS-4062's
	// shadow-insensitivity trace probe: twoTurnCaseResult gained
	// ShadowKindInsensitivityEvaluated/ShadowKindInsensitivityOutcome and
	// BaselineCommittedSubjects/HintedCommittedSubjects, populated ONLY for
	// the "unjustified" InferredClassification outcome -- trace-level
	// observability only, no classification/semantics change, but a new
	// key nonetheless (cmd/acr-trial-merge-two-turn's own hand-maintained
	// mirror must be updated in lockstep before it can merge an artifact at
	// this version -- see that tool's own expectedSchemaVersion comment).
	// "10" marks CHAOS-4079's write-free shadow-probe observability: the
	// zero-overlap wiring gap described under "9" below is CLOSED (the
	// probe now evaluates for a wrong-kind hint), twoTurnCaseResult gained
	// ShadowKindInsensitivityMode, and ShadowKindInsensitivityEvaluated/
	// Outcome consequently populate on rows where they were structurally
	// always false/absent before -- a MEANING change for those two keys, so
	// a v9 run's unjustified rows do not compare field-for-field against a
	// v10 run's. The pass condition is UNCHANGED and pass/fail-neutral by
	// construction: kind_insensitivity_attested still requires
	// mode=="narrowed" (twoTurnKindAttested), so no row that counted
	// unjustified at v9 counts otherwise at v10.
	// "9" marks CHAOS-4039's v5 measurement-contract correction (team-lead
	// ruling 2026-08-22): InferredClassification's own baseline_equivalent
	// definition changed MEANING (no wire shape change -- inferred_classification
	// stays a closed-vocabulary string) from bit-for-bit equality of two
	// model-derived hashes (canonicalInterpretationHash/normalizedDecisionFingerprint,
	// which hashed raw live-model output -- InvestigationResult.Status and
	// InterpretedQuestion.SubjectTerms/ComparisonTerms/FactRequirements --
	// and was by-construction close to unreachable on ordinary
	// call-to-call model phrasing variance) to engine-deterministic
	// decision-state equivalence (the paired calls' own final
	// decision-stage trace Outcome plus their committed-subject SET,
	// twoTurnInferredClassification/twoTurnCommittedSubjectsEquivalent). A
	// prior run's "N/M unjustified" finding was measured under the old,
	// unsatisfiable definition and does not compare against a run merged at
	// this version. NOTE (RESOLVED at "10", CHAOS-4079): the CHAOS-4062
	// shadow kind-insensitivity probe's own zero-overlap wiring gap
	// (narrowPooledKindsByExplicitKinds treating a wrong-kind hint
	// identically to "no hint", so PreNarrowingExplicitKinds never
	// populates and the probe never evaluates for that case) made
	// ShadowKindInsensitivityEvaluated/Outcome false/absent for that case
	// in every v9 run. The first drafted fix was rejected on adversarial
	// review (codex xhigh, 2026-08-22): populating PreNarrowingExplicitKinds
	// there triggers an EXTRA, real (non-shadow) CensusFunc call whose
	// result chaos3896_slice_c_evidence_census.go's attestedSatisfier/
	// mergeCensusAttestedSatisfier consumes to decide a REAL commit -- not
	// observability-only, a genuine commit-behavior risk under census-read
	// drift. CHAOS-4079 shipped the write-free construction instead: the
	// verdict is DERIVED from census results the round already collected,
	// issuing zero additional CensusFunc calls and writing only
	// observability fields.
	//
	// "11" marks CHAOS-4086's instant-diagnosis fields: twoTurnCaseResult
	// gained CommittedSubjects/ExpectedKind/ExpectedID (populated on ALL
	// FOUR arms), CommitGate/TiedStatisticalTop/SearchTruncated read off the
	// decision-stage trace, KindCoverageFloorFired/KindCoverageMissingKinds/
	// KindCoverageFloorTruncated read off the new kind_coverage_floor stage,
	// and ArmInvalidStage/ArmInvalidErrorType beside ArmInvalidReason.
	//
	// Purely ADDITIVE observability: every one is a new key, no existing
	// field changed name or meaning, and none of them backs a pass/fail
	// check in this file. The bar they serve is diagnostic rather than
	// evaluative -- a wrong_commit row must be readable from this artifact
	// ALONE (what committed, what was expected, which gate fired, whether
	// the coverage floor was involved) instead of requiring a re-read of
	// raw model-exchange files after the run, which is what diagnosing
	// CHAOS-4085 and CHAOS-4098 actually cost.
	//
	// "12" marks CHAOS-4100's sharding provenance: trialProvenance gained
	// the `sharding` block (case_indices, granularity, concurrency_cap,
	// provisioning_mode, database_provision_millis). Purely additive, and
	// on the PROVENANCE rather than any result row, so no measurement
	// semantics change and no bar reads it.
	//
	// It is a schema bump anyway because the merge tool mirrors
	// trialProvenance too, and a field absent from that mirror is dropped
	// on decode -- which for THIS block would silently erase the record of
	// how a run was fanned out from every merged artifact, while the
	// per-shard artifacts still carried it and every count still agreed.
	//
	// "13" marks CHAOS-4103's synthesis-status override provenance:
	// twoTurnCaseResult gained the SynthesisStatusOverrideFired/From/To/
	// Reason/CommittedCount block (see that block's own doc comment), and
	// twoTurnReport gained SynthesisStatusOverrideUncommittedCount below.
	// Additive per-row keys, exactly CHAOS-4086's own "11" precedent --
	// Results concatenates verbatim across shards, so no merge arithmetic
	// changes for the row block itself. It is a schema bump anyway for the
	// SAME reason "11"/"12" were: the merge tool's twoTurnCaseResult mirror
	// must gain the block in the same change or json.Unmarshal silently
	// drops it, and the new run-level count needs the SAME `+=` merge
	// arithmetic WrongCommitCount/FalseNoMatchCount already get.
	//
	// The new count is a measurement-contract addition, not merely
	// observational: a row whose Reason is
	// clarification_unavailable_uncommitted is a blocking defect signal
	// (team-lead ruling 2026-08-22, CHAOS-4103) -- an engine routing bug,
	// not a soft status -- and this run fails when the count is nonzero.
	// Deliberately a SEPARATE bar from FalseNoMatchCount: the addendum's
	// floor-landing distinction (reaching no_match on a case with a real
	// expected answer is the reportable event; ordinary one-notch
	// complete/partial/degraded drift is corpus baseline, never a
	// regression) is FalseNoMatchCount's own concern and stays exactly as
	// it was -- this count never widens it and never substitutes for it.
	//
	// "14" marks CHAOS-4120's three-part addition (the 2026-08-22
	// question-results decomposition's own instrumentation gaps):
	//
	// (a) FalseNoMatch/FalseNoMatchCount widen from inferred_tier-only to
	// ALSO cover the positive arm -- a MEANING change for an existing key
	// (FalseNoMatch could structurally never be true on a positive-arm row
	// before this), not merely an added one, which is why this bumps
	// rather than riding along as additive passthrough. See FalseNoMatch's
	// own doc comment (twoTurnCaseResult) for why a positive-arm no_match
	// is at least as strong a finding as an inferred-tier one, and
	// CHAOS-4108 for the live case (ext65 index 57) this widening exists
	// to stop missing.
	//
	// (b) twoTurnCaseResult gained the CHAOS-4120 turn-1 facts block:
	// Turn1CommitGate/Turn1TiedStatisticalTop/Turn1SearchTruncated,
	// Turn1KindCoverageFloorFired/Turn1KindCoverageMissingKinds/
	// Turn1KindCoverageFloorTruncated, Turn1TermSearchTruncated/
	// Turn1QuestionSearchTruncated, ExpectedInPool, AnchorOptionsCount,
	// HandleOptionsCount, CensusRan, CensusComplete, CensusCount --
	// stamped identically onto every arm's row for a case from turn 1's
	// own trace/result, closing the gap where an offer-miss row (which
	// never makes a turn-2 call) carried NONE of the decision/kind-
	// coverage/search-truncation facts turn 1's own call already knew. See
	// twoTurnStampTurn1Facts's own doc comment for why these ride as
	// separate, Turn1-prefixed fields rather than overloading the existing
	// turn-2-scoped ones.
	//
	// (c) twoTurnCaseResult gained Regime ("regime_a_window_gated" |
	// "regime_b_resolution_proceeded" | "" for unclassified -- see
	// twoTurnRegimeFromWindowCanonicalization's own doc comment for exactly
	// which outcomes fall into each bucket), coined by CHAOS-4118's own
	// Mechanism section (team-lead ruling 2026-08-22): stamped from the
	// engine's OWN EngineTelemetry.RecordWindowCanonicalization outcome
	// (already-existing telemetry, CHAOS-3900 W1) rather than re-derived
	// from response shape. Required because the prior shape-based
	// inference left 4/212 rows unplaceable, and CHAOS-4118 now makes the
	// class-default gate ALSO compose a window-only StructureNeeds, which
	// makes an output-shape inference ambiguous in a way this direct
	// capture never is. See
	// twoTurnTurn1Facts.Regime's own doc comment.
	//
	// Purely additive per-row keys otherwise: no merge arithmetic changes,
	// because Results concatenates verbatim and every field on the row
	// rides along with it -- but the mirror in
	// cmd/acr-trial-merge-two-turn/main.go still had to gain both changes
	// in the SAME change, the same "an undeclared field is dropped on
	// decode" reason "11"/"12"/"13" already needed it for.
	//
	// "15" (CHAOS-4135, follow-up from the shard-54 CHAOS-4117 diagnosis,
	// 2026-08-22, team-lead ruling "D-plus"): twoTurnCaseResult gained
	// TraceEvents/BaselineTraceEvents -- the harness's already-captured
	// per-call graphrank.ResolutionTraceEvent stream, persisted (not newly
	// captured) for ANOMALOUS rows only (twoTurnCaseResultIsAnomalous: a row
	// tripping one of this test's own zero-tolerance bars, or an
	// inferred_tier pairing classified "unjustified") -- every other row has
	// both redacted to nil immediately before serialization (see the
	// redaction pass at this function's tail). Purely additive and
	// conditionally empty: no merge arithmetic changes (Results concatenates
	// verbatim), but the mirror in cmd/acr-trial-merge-two-turn/main.go
	// still had to gain both fields in the SAME change for the same
	// "an undeclared field is dropped on decode" reason every prior bump
	// needed it for -- there as json.RawMessage rather than a full mirror of
	// graphrank.ResolutionTraceEvent, since that tool never reads trace
	// content (see its own doc comment on why).
	//
	// Same v15 bump, scope addition (team-lead, 2026-08-22, from lane-4118's
	// CHAOS-4113 scoping): trialProvenance (generative_trial_live_test.go)
	// also gained ResponderModel -- see that field's own doc comment for the
	// provenance gap it closes (no artifact recorded which model actually
	// answered a file-exchange run's calls). Purely additive; the mirror
	// gained it too, for the same reason.
	//
	// "16" (CHAOS-4139, 2026-08-23, team-lead ruling "A'"): a MEANING
	// change, not purely additive -- the SAME class of bump v9 needed for
	// the identical reason. InferredClassification's own comparison basis
	// changed: twoTurnInferredClassification now reads
	// twoTurnDecisionCommittedSubjects (each leg's own decision-stage trace
	// event) instead of SubjectResolution.Committed on the final
	// InvestigationResult -- see that function's own doc comment for why
	// v15 and earlier's "engine-deterministic" claim was not actually true
	// (CHAOS-4085's post-synthesis affirmation gate can retract a
	// statistical commit from the OLD comparison field independently per
	// leg). A row this bug affected under v15 could classify
	// "unjustified"; the identical inputs under v16 classify
	// "baseline_equivalent" -- the SAME artifact shape, a DIFFERENT verdict
	// for some rows, which is exactly why v8->v9 bumped on a meaning
	// change with no field-shape change either. twoTurnCaseResult also
	// gained HintedCommitAffirmation/BaselineCommitAffirmation (purely
	// additive, own doc comment above) -- both changes land in the SAME
	// bump since they are one ticket's own before/after pair, not two
	// unrelated additions.
	//
	// "17" (CHAOS-4138, 2026-08-23): purely additive. twoTurnCaseResult
	// gained PairRetried/PairRetryFirstArmInvalidReason/
	// PairRetryFirstArmInvalidStage/PairRetryFirstArmInvalidErrorType (the
	// bounded-one-shot-retry disclosure contract for a baseline/hinted-leg
	// Investigate() error -- an instrument failure, never a product bar --
	// see PairRetried's own doc comment for the exact scope and why a
	// pairing-precondition failure or structural exemption never retries)
	// and this report gained PairRetriedCount, its own aggregate. No
	// merge arithmetic changes (Results still concatenates verbatim); the
	// mirror in cmd/acr-trial-merge-two-turn/main.go gained all five
	// fields in the same change, same "an undeclared field is dropped on
	// decode" reason every prior bump needed it for.
	//
	// "18" (CHAOS-4038 dial, 2026-08-23): purely additive, closing a gap
	// found while verifying the kindCoverageQueryLimit 5->20 bump's
	// observability. confirmed_kind_rescue (CHAOS-4132) has carried its own
	// trace stage (ConfirmedKindRescueFired/ResultCount/Truncated,
	// resolve.go/tracer.go) since that ticket landed, but unlike
	// kind_coverage_floor's identically-shaped fields, this harness never
	// captured it into twoTurnCaseResult -- so a merged trial artifact could
	// not show the rescue-fired subset kindCoverageQueryLimit's shared-const
	// coupling with CHAOS-4132 makes load-bearing (see that constant's own
	// doc comment, chaos4038_kind_coverage.go). twoTurnCaseResult gains
	// ConfirmedKindRescueFired/ConfirmedKindRescueResultCount/
	// ConfirmedKindRescueTruncated (this arm's own turn-2 decisive call,
	// mirroring CommitGate/KindCoverageFloor* above) and
	// Turn1ConfirmedKindRescueFired/Turn1ConfirmedKindRescueResultCount/
	// Turn1ConfirmedKindRescueTruncated (turn 1's call, mirroring
	// Turn1KindCoverageFloor* above) -- read via the new
	// twoTurnTraceCapture.confirmedKindRescueEvent(), the same
	// last-event-wins reader kindCoverageFloorEvent already uses. No merge
	// arithmetic changes (Results concatenates verbatim); the mirror in
	// cmd/acr-trial-merge-two-turn/main.go gained all six fields in the same
	// change, same "an undeclared field is dropped on decode" reason every
	// prior bump needed it for.
	//
	// "19" (CHAOS-4161, 2026-08-23): purely additive, closing a gap found
	// while diagnosing why census_ran read false on every work_item-anchored
	// stalled row in an ext65 rerun: RunShadowEvidenceRound
	// (chaos3899_evidence_round.go) traces an "evidence_round" event on
	// EVERY call that reaches it, including every one of its own 7 early-
	// return refusals -- so that stage's mere presence proves only the round
	// was ENTERED (resolve.go:1835's outer gate passed), never that
	// CensusFunc was invoked. census_ran() already knew this (it keys off
	// "evidence_probe" instead, per its own doc comment), but the harness
	// never captured the entry fact itself, so "never entered" (the outer
	// gate's own precondition failed) and "entered but refused before the
	// per-kind loop" were indistinguishable from the artifact alone.
	// twoTurnCaseResult gains EvidenceRoundEntered/EvidenceRoundReason, read
	// via the new twoTurnTraceCapture.evidenceRoundEvent() (kindCoverage
	// FloorEvent's own last-event-wins pattern) and populated turn-1-only,
	// the same single-field (no Turn1-prefixed twin) shape CensusRan/
	// CensusComplete/CensusCount already use. Survives CHAOS-4135's
	// non-anomalous-row trace redaction unchanged: that pass clears only
	// TraceEvents/BaselineTraceEvents, never these scalar summary fields.
	// No merge arithmetic changes (Results concatenates verbatim); the
	// mirror in cmd/acr-trial-merge-two-turn/main.go gained both fields in
	// the same change, same "an undeclared field is dropped on decode"
	// reason every prior bump needed it for.
	//
	// "20" (CHAOS-4012, 2026-08-23): purely additive, telemetry-only (team-
	// lead ruling: measure before any behavior change). kindOfferMaterial
	// (chaos3900_structure_offers.go) suppresses the entire expected_kind
	// offer whenever fewer than 2 distinct structureOfferKinds-registered
	// kinds survive in the pool AND no explicit hint was supplied -- and a
	// CHAOS-4038 coverage-floor rescue getting the expected kind INTO the
	// pool does not by itself satisfy that threshold, so a candidate can be
	// genuinely in-pool and still never reach KindOptions. No field
	// previously distinguished "genuinely 0 offerable kinds" from "exactly
	// 1, still suppressed" -- the SAME never-ran-vs-attested-zero shape
	// CensusRan/EvidenceRoundEntered already resolve, one layer over.
	// twoTurnCaseResult gains KindOfferExplicitHintCount/
	// KindOfferDistinctKindCount/KindOfferSuppressedByCardinality (this
	// arm's own turn-2 call, read via the new
	// twoTurnTraceCapture.kindOfferEvent(), kindCoverageFloorEvent's own
	// last-event-wins pattern) and the Turn1-prefixed twin of all three
	// (turn 1's own call) -- UNLIKE EvidenceRoundEntered/EvidenceRoundReason
	// above, kind_offer fires UNCONDITIONALLY (kindOfferMaterial runs on
	// every resolution, not gated behind a precondition), so this mirrors
	// KindCoverageFloorFired/ConfirmedKindRescueFired's own two-twin shape
	// instead of CensusRan's single-field one. No omitempty on any of the
	// six, same reasoning as CensusRan/CensusComplete/CensusCount. No merge
	// arithmetic changes (Results concatenates verbatim); the mirror in
	// cmd/acr-trial-merge-two-turn/main.go gained all six fields in the
	// same change, same "an undeclared field is dropped on decode" reason
	// every prior bump needed it for.
	//
	// "21" (CHAOS-4157, 2026-08-23): a MEANING change on an existing key,
	// not an added one -- BaseSHA used to be a wrapper-script-exported
	// `git rev-parse origin/main`, read AT LAUNCH TIME, a genuine
	// provenance defect caught live: origin/main can (and during a real
	// clean CHAOS-4100 re-measure DID -- three unrelated PRs landed mid-run)
	// move while a run is in flight, so the field could name a commit that
	// never actually produced the artifact. BaseSHA now reads
	// source.commit (requireGitSourceIdentity's own `git rev-parse HEAD`,
	// the SAME value SourceCommit already uses) on all four trial report
	// types (two-turn, replay, W0, D2B cardinality), so a v20 artifact's
	// base_sha is not comparable to a v21 one field-for-field even though
	// the wire shape is unchanged -- exactly the v8->v9/v16 class of bump.
	// No field added or removed, so cmd/acr-trial-merge-two-turn's own
	// mirror needs only its expectedSchemaVersion constant updated, not a
	// new field.
	//
	// "22" (CHAOS-4165, 2026-08-23): purely additive. twoTurnReport gains
	// MutationProbeEligibleCount (a per-run structural count) and
	// MutationProbeCoverage (a per-probe-kind {runs,required_min,adequate}
	// verdict, computed by the new mutationProbeCoverage function) -- see
	// mutationProbeCoverageFloor's own doc comment for why: the existing
	// MutationProbesTripped/MutationProbesRun ratio reads a probe that ran
	// once and tripped once identically to one with full statistical
	// power, so a genuine run-count collapse under a given responder
	// (CHAOS-4165's own finding, luna vs sol on the ext65 corpus) was
	// invisible to every existing gate. This is a SOFT, informational
	// signal only -- no hard gate changes, and MutationProbesTripped/
	// MutationProbesRun's own zero-tolerance tripped==ran check is
	// unchanged. No merge arithmetic beyond a plain sum for
	// MutationProbeEligibleCount (Coverage itself is RECOMPUTED from the
	// merged sums, never summed); the mirror in
	// cmd/acr-trial-merge-two-turn/main.go gained the matching fields plus
	// its own copy of mutationProbeKinds/mutationProbeCoverageFloor/
	// mutationProbeCoverage (hand-maintained derivation logic, not only
	// wire shape) in the same change.
	//
	// v22 unchanged by CHAOS-4121: window oracle source field swapped
	// (selectOracleOffer's window case now reads
	// StructureNeeds.WindowOptions instead of the legacy
	// WindowClarification.Options), byte-identical surfaces by
	// construction (window.go:1315's own "same slice value, never a copy"
	// guarantee), parity-guarded live by twoTurnAssertWindowSurfacesAgree
	// (Fatalf's, naming the case index, the instant that guarantee stops
	// holding for a real result) -- no wire-shape or measured-value change,
	// so no version bump (team-lead ruling, CHAOS-4121 close-out).
	//
	// "23" (CHAOS-4012, 2026-08-23, rebased onto "22"/CHAOS-4165 -- this
	// branch independently claimed "22" before that landed on main,
	// renumbered here): purely additive, telemetry-only --
	// candidateOfferMaterial (chaos3900_structure_offers.go) mints the
	// ranked-candidate-list offer axis, independent of kind_offer's own
	// kind-pick axis above (kind-pick unchanged; candidate-list fires on
	// Committed==0 && the read-only Slice-B candidate pool non-empty).
	// twoTurnCaseResult gains CandidateOfferCount/OfferKind (this arm's own
	// turn-2 call, read off the SAME kind_offer trace stage's new
	// KindOfferCandidateOfferCount/KindOfferOfferKind fields via the
	// existing twoTurnTraceCapture.kindOfferEvent() last-event-wins reader --
	// no new trace stage, no new capture method) and the Turn1-prefixed twin
	// of both (turn 1's own call), same two-twin shape as "20"'s trio. No
	// omitempty on either, same reasoning as the kind_offer trio: 0/""
	// distinguishes "genuinely neither axis fired" from an absent capture.
	//
	// Same "23" (team-lead ruling, 2026-08-23, re-smoke follow-up): also
	// gains ExpectedKindAtOfferBoundary -- ExpectedInPool's own
	// call-boundary-scoped refinement, needed because the 9-index re-smoke
	// found ExpectedInPool over-reports (trace-wide "corroborated anywhere,
	// before final truncation") relative to what kindOfferMaterial/
	// candidateOfferMaterial actually read at their shared call boundary.
	// Backed by the kind_offer trace stage's own new KindOfferBoundaryKinds
	// field (distinctCandidateKinds(kindOfferCandidates),
	// chaos3900_structure_offers.go) via the new
	// twoTurnTraceCapture.boundaryContainsKind() reader -- same
	// last-event-wins kindOfferEvent() this stage's every other field
	// already uses. Single field, turn-1-only, no Turn1-prefixed twin --
	// same shape as ExpectedInPool itself, the field it refines.
	//
	// No merge arithmetic changes for any of "23"'s fields (Results
	// concatenates verbatim); the mirror in
	// cmd/acr-trial-merge-two-turn/main.go gained all of them in the same
	// change, same "an undeclared field is dropped on decode" reason every
	// prior bump needed it for.
	//
	// Same "22" (team-lead ruling, 2026-08-23, re-smoke follow-up, folded
	// into this SAME unreleased bump rather than a new one): twoTurnCaseResult
	// also gains ExpectedKindAtOfferBoundary -- ExpectedInPool's own
	// call-boundary-scoped refinement, needed because the 9-index re-smoke
	// found ExpectedInPool over-reports (trace-wide "corroborated anywhere,
	// before final truncation") relative to what kindOfferMaterial/
	// candidateOfferMaterial actually read at their shared call boundary.
	// Backed by the kind_offer trace stage's own new KindOfferBoundaryKinds
	// field (distinctCandidateKinds(kindOfferCandidates),
	// chaos3900_structure_offers.go) via the new
	// twoTurnTraceCapture.boundaryContainsKind() reader -- same
	// last-event-wins kindOfferEvent() this stage's every other field
	// already uses. Single field, turn-1-only, no Turn1-prefixed twin --
	// same shape as ExpectedInPool itself, the field it refines. No merge
	// arithmetic changes; the mirror in cmd/acr-trial-merge-two-turn/main.go
	// gained the field in the same change, same reason as above.
	//
	// "24" (CHAOS-4183 phase 2, team-lead ruling, 2026-08-23): two changes,
	// both requiring the bump.
	//
	// (a) twoTurnCaseResult gains KindCoverageMissingKindsList and
	// Turn1KindCoverageMissingKindsList -- KindCoverageMissingKinds' own
	// kind-IDENTITY twin, closed-vocabulary (contextfabric.SubjectKind
	// values only, corpus-safe, same discipline KindOfferBoundaryKinds
	// already established). Purely additive. Motivated directly by a
	// CHAOS-4183 re-smoke finding the bare COUNT could not resolve: whether
	// the coverage floor searched for the SAME kind a later analysis cares
	// about, or a different one, once more than one floor kind could be
	// missing for a single call.
	//
	// (b) `omitempty` DROPPED from KindCoverageFloorFired/
	// KindCoverageMissingKinds/KindCoverageFloorTruncated and their
	// Turn1-prefixed twins -- a MEANING change on existing keys, not merely
	// additive. This is the motivating incident, not a hypothetical: a live
	// CHAOS-4183 investigation queried a v23 artifact with jq and read an
	// OMITTED key as "the kind_coverage_floor event never fired," when the
	// event had fired with the real values Fired=false/MissingKinds=0 (Go
	// zero values) that `omitempty` then silently dropped from the JSON.
	// For a boolean/count field a reader uses to GATE further analysis
	// (exactly what happened here -- the field decided which investigation
	// branch to follow), "key absent" and "key present with a zero value"
	// must be the SAME observable state, or a query tool with no way to
	// distinguish the two silently reintroduces the never-ran-vs-attested-
	// zero ambiguity CensusRan/EvidenceRoundEntered (CHAOS-3899/CHAOS-4161)
	// and KindOfferDistinctKindCount (CHAOS-4012) were each already filed
	// specifically to resolve -- applied here retroactively to a field
	// trio that predates both fixes. A v23 row with these six keys absent
	// and a v24 row with them present-as-false/0 are NOT the same claim;
	// this version number is what tells a reader which reading applies.
	//
	// No merge arithmetic changes for either (a) or (b) (Results
	// concatenates verbatim); the mirror in
	// cmd/acr-trial-merge-two-turn/main.go gained both in the same change,
	// same "an undeclared field is dropped on decode" reason every prior
	// bump needed it for.
	//
	// "25" (CHAOS-4183 phase "2c", team-lead ruling, 2026-08-23): purely
	// additive. twoTurnCaseResult gains Turn1TraceEvents -- turn 1's own
	// raw ResolutionTraceEvent stream, nil on every ordinary run, populated
	// ONLY when a launcher explicitly names this case's own corpus index
	// via ACR_TEST_TRIAL_FORCE_TRACE_INDICES (twoTurnForceTraceIndices).
	// This is a DEBUG AFFORDANCE, not a measurement-artifact field:
	// distinct in kind from TraceEvents/BaselineTraceEvents above (which
	// persist on the anomaly-gated redaction pass every ordinary run
	// already applies) -- Turn1TraceEvents is gated on an entirely
	// separate, opt-in knob, and is never touched by
	// twoTurnRedactNonAnomalousTraceEvents. A force-traced run's own
	// artifact is never part of a ratified measurement and stays
	// LOCAL-ONLY -- see Turn1TraceEvents' own doc comment for the full
	// corpus-safety discipline. No merge arithmetic changes (Results
	// concatenates verbatim); the mirror in
	// cmd/acr-trial-merge-two-turn/main.go gained the field in the same
	// change, same "an undeclared field is dropped on decode" reason every
	// prior bump needed it for.
	//
	// "26" (CHAOS-4183 phase 3, sol design consult, team-lead ratified
	// 2026-08-23): the POST-DECISION KIND-ONLY BOUNDARY COMPLETION for
	// Shape A -- stalled resolutions only (committedCount==0), no candidate
	// added, no score changed, no extra SearchKind call, no displacement.
	// projectKindOfferKinds (chaos3900_structure_offers.go) appends, in
	// FIXED closed-vocab order, every offerable kind present in the full
	// merged pool (candidatesBySubject) but absent from the visible
	// kind_offer boundary -- the exact Shape-A gap the CHAOS-4183 re-smoke
	// confirmed for 5/9 investigated indices (expected_in_pool=true,
	// expected_kind_at_offer_boundary=false). Two changes, both requiring
	// the bump.
	//
	// (a) MEANING change on an existing key: KindOfferBoundaryKinds
	// (ResolutionTraceEvent) is now the POST-repair boundary, not the raw
	// kindOfferCandidates snapshot it used to be -- so
	// ExpectedKindAtOfferBoundary (this struct) and boundaryContainsKind()
	// now read POST-repair too, automatically, with no code change of
	// their own. A v25 row's expected_kind_at_offer_boundary=false and a
	// v26 row's own false are NOT the same claim for a Shape-A case; this
	// version number is what tells a reader which reading applies -- same
	// "22"/"23" class of retroactive meaning shift.
	//
	// (b) Purely additive: ResolutionTraceEvent gains
	// KindOfferBoundaryKindsBeforeRepair/
	// KindOfferDistinctKindCountBeforeRepair/
	// KindOfferSuppressedByCardinalityBeforeRepair -- the PRE-repair twins
	// of the now-shifted trio, so a reader can still ask the OLD question
	// off the SAME event. twoTurnCaseResult gains
	// ExpectedKindAtOfferBoundaryBeforeRepair (single, turn-1-only, same
	// shape as ExpectedInPool/ExpectedKindAtOfferBoundary themselves) and
	// the Turn1-prefixed KindOfferDistinctKindCountBeforeRepair/
	// KindOfferSuppressedByCardinalityBeforeRepair twins (same shape as
	// "20"'s trio). candidateOfferMaterial/CandidateOptions and
	// resolution.Candidates/Committed are byte-unchanged by this phase --
	// see projectKindOfferKinds' own doc comment for the full mechanism
	// and TestResolveSubjects_KindBoundaryRepairCausalFixture
	// (chaos4183_kind_boundary_repair_test.go) for the causal proof.
	//
	// No merge arithmetic changes (Results concatenates verbatim); the
	// mirror in cmd/acr-trial-merge-two-turn/main.go gained all of it in
	// the same change, same "an undeclared field is dropped on decode"
	// reason every prior bump needed it for.
	//
	// "28" (CHAOS-4186 follow-up, team-lead ordered 2026-08-24): purely
	// additive provenance -- trialProvenance (generative_trial_live_test.go)
	// gains DataPlane/DataPlanePGHost/DataPlaneCHHost/DataPlaneFalkorHost,
	// recording which store backend (compose|kiac|override) and which
	// hosts (never credentials) this run actually hit, sourced verbatim
	// from ACR_TEST_TRIAL_DATA_PLANE/PG_HOST/CH_HOST/FALKOR_HOST (the
	// values scripts/trial/common.sh already resolves and exports). Closes
	// the gap the kiac cutover left: a kiac-run artifact carried zero
	// host/DSN references before this, so which stack a run hit was
	// inferred from operator memory, never recorded in the artifact
	// itself. No merge arithmetic changes (Provenance rides through from
	// the FIRST shard, same as ResponderModel -- see mergeReports' own
	// data_plane mismatch check, added in the same PR, for why a mixed-
	// plane launch across shards is refused rather than silently merged
	// under the first shard's label); mirrored in
	// cmd/acr-trial-merge-two-turn/main.go in the same change, same
	// undeclared-field-dropped-on-decode reason as every prior bump
	// needed it for.
	// "27" (CHAOS-4119, team-lead ratified 2026-08-24): handleOfferMaterial
	// (chaos3900_structure_offers.go) gains a THIRD handle source --
	// poolCandidates, the SAME final candidate pool kindOfferMaterial/
	// candidateOfferMaterial already read -- beside explicit and
	// BindHandles' own question-text scan, closing the 25/25 handle
	// offer_miss gap: a ticket key/PR#/CI-run# the resolution already found
	// is now offered even when never literally typed in the question.
	// Two changes, both requiring the bump.
	//
	// (a) MEANING change on an existing key: HandleOptionsCount (this
	// struct, read off turn 1's own StructureNeeds.HandleOptions) can now
	// include graph-derived entries -- a v26 row's handle_options_count>0
	// meant "BindHandles or an explicit handle matched"; a v27 row's own
	// >0 no longer implies either. HandleOptionsCountBeforeGraphSource
	// (new, below) carries the pre-CHAOS-4119 reading (explicit+BindHandles
	// only) so a v26-comparable count is still recoverable from a v27 row.
	//
	// (b) Purely additive: ResolutionTraceEvent gains
	// HandleOfferCountBeforeGraphSource/HandleOfferGraphDerivedCount/
	// HandleOfferGraphDerivedRejectedCount on the SAME unconditional
	// kind_offer stage kind/candidate-offer diagnostics already ride (see
	// handleOfferDiagnostics' own doc comment, chaos3900_structure_offers.go,
	// for what each measures). twoTurnCaseResult gains
	// HandleOptionsCountBeforeGraphSource (single, turn-1-only, same shape
	// as AnchorOptionsCount/HandleOptionsCount themselves) and the bare +
	// Turn1-prefixed HandleOfferGraphDerivedCount/
	// HandleOfferGraphDerivedRejectedCount pairs (same shape as "20"'s
	// KindOffer* trio -- this stage fires on every resolve call, turn 1 and
	// each arm's own turn-2 call alike).
	//
	// No merge arithmetic changes (Results concatenates verbatim); the
	// mirror in cmd/acr-trial-merge-two-turn/main.go gained all of it in
	// the same change, same "an undeclared field is dropped on decode"
	// reason every prior bump needed it for.
	//
	// "29" (CHAOS-4234, team-lead ruled 2026-08-24): the class-default
	// window gate now runs an offers-only resolution and composes kind/
	// handle/candidate offers beside the window offer (regime A). Three
	// changes, all requiring the bump. (a) MEANING change on existing keys
	// for regime-A rows: kind_offer-stage fields (kind_offer_*,
	// candidate_offer_count, handle_offer_*, expected_in_pool,
	// expected_kind_at_offer_boundary*) and the decision-stage fields
	// (turn1_search_truncated, turn1_tied_statistical_top, ...) are now
	// POPULATED on a window-gated turn 1, where v27 always read them as
	// their zero values because no resolution ran. (b) Purely additive:
	// turn1_offer_composed_under_window_gate, expected_subject_in_pool,
	// expected_subject_rank, expected_subject_at_offer_boundary,
	// turn2_window_receipt_attached on twoTurnCaseResult;
	// regime_a_offer_composed_count and regime_a_turn2_answered_count on
	// this report (summed by the merger). (c) HARNESS semantics change:
	// the positive arm's turn 2 on a regime-A case now carries the
	// oracle's window receipt beside the member receipt, so turn-2
	// aggregates (turn2_status, positive_applied_count,
	// gate_reachable_count, regime_a_turn2_answered_count) are NOT
	// comparable to a v27 run as engine-only numbers; offer_miss_count
	// stays engine-only. The mirror in cmd/acr-trial-merge-two-turn/
	// main.go gained all of it in the same change. (Schema v28, landed on
	// main between this branch's first draft and merge, is CHAOS-4186's
	// unrelated DataPlane provenance bump above -- this change re-numbers
	// to v29 to stay lockstep, not v28.)
	//
	// "30" (CHAOS-4307): purely additive -- ConfirmedKindVectorCensus*
	// (see that field group's own doc comment, below) is new; no existing
	// key's meaning, presence, or JSON name changes. The mirror in
	// cmd/acr-trial-merge-two-turn/main.go gained the same fields plus the
	// matching mergeReports sums in the same change.
	//
	// "31" (CHAOS-4313, chris ruling 2026-08-26 05:30 PDT): purely
	// additive -- responder_transport ("api"|"codex") on trialProvenance,
	// recording which out-of-process responder answered a file_exchange
	// run (cmd/acr-trial-responder-api's direct OpenAI API call, or
	// run-responder-codex.sh's `codex exec` subprocess), sourced from
	// ACR_TEST_TRIAL_RESPONDER_TRANSPORT the same way ResponderModel
	// already sources ACR_TEST_TRIAL_RESPONDER_MODEL. Empty for real_api
	// and for every run before this field existed, matching
	// ResponderModel's own precedent exactly -- no other field's shape or
	// meaning changes. The mirror in cmd/acr-trial-merge-two-turn/main.go
	// gained the field and its cross-shard consistency check in the same
	// change.
	//
	// Bump this again on any future field rename, removal, or meaning
	// change so a consumer can detect drift instead of silently reading a
	// stale key under a new meaning.
	ReportSchemaVersion string          `json:"report_schema_version"`
	Provenance          trialProvenance `json:"provenance"`
	// BaseSHA mirrors chaos3884_replay_harness_test.go's replayReport.BaseSHA
	// (codex round-3 finding #3: required provenance, team-lead ruling
	// 2026-08-17 -- the artifact must prove what code produced it).
	//
	// CHAOS-4157 fix-forward (2026-08-23): this used to be
	// requireEnv(t, "ACR_TEST_TRIAL_BASE_SHA"), a wrapper-script-exported
	// `git rev-parse origin/main` read AT LAUNCH TIME -- a genuine
	// provenance defect, caught live: origin/main can (and during a real
	// ~15min run did) move while the run is in flight, so that value can
	// name a commit that never actually ran. source.commit
	// (requireGitSourceIdentity's own `git rev-parse HEAD`, the SAME value
	// SourceCommit below is stamped from) is the worktree's actual
	// checked-out commit -- the code that is genuinely running -- so
	// BaseSHA and SourceCommit are now the identical value by construction,
	// never two independently-sourced facts that can silently diverge.
	BaseSHA              string `json:"base_sha"`
	OracleAnnexPath      string `json:"oracle_annex_path"`
	OracleAnnexCorpusSHA string `json:"oracle_annex_corpus_sha256"`
	OracleAnnexSignedOff bool   `json:"oracle_annex_signed_off"`
	// AnnexSignoffStale (CHAOS-4348, team-lead ruling 2026-08-27, HIGH #2
	// on PR #302's own codex review): true iff provenance.signoff.
	// approved_corpus_sha8 (what chris's APPROVED signoff actually
	// covered, stamped once by cmd/acr-corpus-annex-sync the first time
	// it mechanically corrects corpus content) differs from the annex's
	// CURRENT provenance.corpus_sha8 (OracleAnnexCorpusSHA above). Loud,
	// never fatal: OracleAnnexSignedOff alone cannot tell "chris approved
	// exactly this corpus" apart from "chris approved an EARLIER corpus,
	// and it has since been mechanically corrected" -- both read
	// SignedOff=true. A run with this true is INFORMATIVE, not the final
	// ratified acceptance measurement, until chris re-ratifies against
	// the current corpus_sha8.
	AnnexSignoffStale bool `json:"annex_signoff_stale"`
	CasesRun          int  `json:"cases_run"`
	// PositiveAppliedCount is the fail-open guard (codex round-3 finding
	// #2): a run where every case offer-misses or errors could otherwise
	// report zero wrong commits and pass the anti-vacuity check via
	// confirmed_wrong alone, having proven NOTHING about ordinary
	// conversion. The final assertion requires this > 0.
	PositiveAppliedCount int `json:"positive_applied_count"`
	// WindowPositiveAppliedCount (CHAOS-4040 harness follow-up, team-lead
	// ruling 2026-08-21, reconciling against sol's full non-vacuity list)
	// is the winr_ positive-control proof PositiveAppliedCount alone
	// cannot give: that field is POOLED across every member, so a run
	// where window's own receipt-redemption path is silently broken could
	// still read PositiveAppliedCount>0 entirely from kind/handle/anchor
	// conversions -- never proving window's own escape hatch out of the
	// gate actually works. Counted when the window member's positive arm
	// BOTH confirms (memberApplied's own Provenance==clarification_confirmed
	// check) AND reaches a real decisive path (CommittedCount>0) --
	// team-lead's own two-part bar, not confirmation alone. The
	// complementary half of the proof ("removing that receipt returns to
	// the gate") needs no separate call: the offer this positive-arm call
	// redeems can only exist because THIS SAME case's own turn 1 call (no
	// receipt) was gated in the first place (turn1's WindowClarification,
	// selectOracleOffer's own precondition) -- the paired evidence already
	// lives in this run, not a new one.
	//
	// Also requires BOTH calls fresh (!turn1.Reused && !positive.Reused,
	// codex round 3, confirmed): answer-reuse only rejects DECISIVE
	// candidates carrying an INFERRED window, never a confirmed-window
	// decisive row -- unguarded, a stale replayed row (either call) could
	// satisfy this bar without this run's redemption code ever actually
	// running, the same reuse-vacuity class WindowClassDefaultGatedCount's
	// own doc comment describes. Non-vacuity bar: must be > 0.
	WindowPositiveAppliedCount int `json:"window_positive_applied_count"`
	GateReachableCount         int `json:"gate_reachable_count"`
	// StructureAndWindowDisclosureAbsentCount (orchestrator ruling,
	// 2026-08-20, post-live-run: renamed from NoDiscriminatorsCount, which
	// this run proved gets misread as the P1 acceptance row's
	// "no_discriminators" bar). This field counts entries where BOTH
	// StructureNeeds AND WindowClarification were nil on turn 1 -- a much
	// narrower, purely structural signal than the census's own
	// no_discriminators REFUSAL REASON (a per-unique-case outcome
	// classification this harness does not reproduce). NOT COMPARABLE to
	// the DP9 "no_discriminators 41->=20" bar; report it as its own
	// number, never as that bar's measurement. Census-manageable-among-
	// stalls (the DP9 bar's other half) remains OUT OF SCOPE entirely --
	// not instrumented by this harness at all.
	StructureAndWindowDisclosureAbsentCount int            `json:"structure_and_window_disclosure_absent_count"`
	OfferMissCount                          map[string]int `json:"offer_miss_count"`
	WrongCommitCount                        int            `json:"wrong_commit_count"`
	// OracleIDSchemeMismatchCount (CHAOS-4348 measurement-layer fix, schema
	// v38) sums twoTurnCaseResult.OracleIDSchemeMismatch across every row,
	// ACROSS EVERY ARM (like FactlessCommittedCount, unlike
	// FalseNoMatchCount) -- a mismatched oracle id is a property of the
	// CASE's own expected id, not of which arm evaluated it, so a mismatch
	// is real regardless of which arm's row happens to be read. A ZERO-
	// TOLERANCE gate, alongside WrongCommitCount/FalseNoMatchCount/
	// SynthesisStatusOverrideUncommittedCount: unlike FactlessCommittedCount
	// (a legitimate, known-expected residual for some kinds), a nonzero
	// count here means the annex itself is broken -- retrieval correctness
	// cannot be measured AT ALL for that row until the annex is fixed
	// (cmd/acr-annex-regen-project-ids; TestChaos4348OracleAnnexAnchorIDsMatchLiveIdentityScheme
	// is the same rule checked directly against the annex file, red-first).
	OracleIDSchemeMismatchCount int `json:"oracle_id_scheme_mismatch_count"`
	// FalseNoMatchCount (CHAOS-4039 v4 contract; CHAOS-4120 widened to also
	// sum the positive arm) sums twoTurnCaseResult.FalseNoMatch across
	// every inferred_tier outcome (kind/handle AND window alike) AND every
	// positive-arm outcome -- one of the ruling's ZERO-tolerance pass
	// conditions, alongside WrongCommitCount, regardless of member or arm.
	FalseNoMatchCount int `json:"false_no_match_count"`
	// FactlessCommittedCount (CHAOS-4347, team-lead standing order: telemetry
	// baked into new logic, same change) sums every twoTurnCaseResult row,
	// ACROSS EVERY ARM (positive, inferred_tier, confirmed_wrong, mutation --
	// unlike FalseNoMatchCount, deliberately not scoped to any subset), where
	// CommittedCount > 0 AND CanonicalFactsCount == 0: the engine committed
	// to a real subject and told the synthesis step nothing canonical about
	// it, REGARDLESS of what status the synthesis step ultimately returned
	// (no_match, degraded, complete -- all count the same here). This is the
	// coverage bar CHAOS-4344's own case 23 exposed the harness never had:
	// a committed-but-factless row can produce a perfectly reasonable
	// "degraded" answer from graph paths alone and never trip
	// FalseNoMatchCount at all, while still being exactly the coverage gap
	// CHAOS-4347's status-category composition (internal/contextfabric/
	// chaos4347_status_category_composition.go) exists to close.
	//
	// OBSERVATIONAL ONLY -- deliberately NOT a zero-tolerance gate condition
	// (evaluateGates has no fail() check against this field): a nonzero
	// count here can be a genuine, expected residual (e.g. the `project`
	// kind, which has no registered fact producer at all and stays
	// factless-committed by construction until a separate, later ticket
	// adds one) rather than a defect signal the way FalseNoMatchCount is.
	// Also inherits twoTurnCanonicalFactsCount's own documented known
	// imprecision (a fact-bearing coverage state can legally carry zero
	// facts) -- another reason this stays report-only.
	FactlessCommittedCount int `json:"factless_committed_count"`
	// SynthesisStatusOverrideUncommittedCount (CHAOS-4103, team-lead ruling
	// 2026-08-22) sums twoTurnCaseResult rows whose
	// SynthesisStatusOverrideReason is
	// clarification_unavailable_uncommitted -- across EVERY arm, not scoped
	// to inferred_tier the way FalseNoMatchCount is, since the override can
	// fire on any arm's call. A nonzero count is a blocking defect signal
	// (an engine routing bug -- the subjectless terminal should have fired
	// and did not -- never a soft status), a THIRD zero-tolerance pass
	// condition alongside WrongCommitCount/FalseNoMatchCount. Deliberately
	// its own bar, not folded into FalseNoMatchCount: the two measure
	// different things (a false no_match a real answer existed for, versus
	// an override whose committed state proves the engine's own routing
	// skipped a terminal it should have composed) and conflating them would
	// hide which one a nonzero count is reporting.
	SynthesisStatusOverrideUncommittedCount int `json:"synthesis_status_override_uncommitted_count"`
	// WindowCommitCount (CHAOS-4039 v4 contract) is the window member's OWN
	// retained bar: window keeps the pre-v4 "ANY commit fails" rule
	// unconditionally. CHAOS-4040 (sol-max ruling 2026-08-21, PR #181)
	// shipped the mechanism this comment used to call missing: EVERY
	// inferred/unconfirmed window is now gated to a confirmation-required
	// terminal before it can reach resolution at all (internal/contextfabric/
	// window.go's windowConfirmationRequiredResult) -- but as an
	// UNCONDITIONAL structural gate, not the kind/handle-style
	// per-commit noninterference PROOF (baseline_equivalent/
	// kind_insensitivity_attested) window still has none of. That
	// distinction is exactly why WindowCommitCount alone is no longer a
	// meaningful bar: it is now STRUCTURALLY always 0 (the gate returns
	// before a commit is even possible), so it can no longer tell "the
	// gate fired correctly" from "the arm never ran" or "every case
	// errored". WindowInferredTierRanCount/WindowArmErrorCount/
	// WindowGatedCount below exist to close exactly that gap; see their
	// own doc comments. Reported and gated SEPARATELY from the kind/handle
	// fields further below -- never pooled with them the way the pre-v4
	// InferredTierAnyCommit was (that pooling is what hid the run-1/run-2
	// window/kind-handle breakdown sol-max's ruling had to reconstruct
	// from scratch).
	WindowCommitCount int `json:"window_commit_count"`
	// WindowInferredTierRanCount/WindowArmErrorCount/WindowGatedCount
	// (CHAOS-4040 harness follow-up, PR #181's own companion) are the
	// window arm's non-vacuity proof, mirroring InferredKindHandleDecisiveCount's
	// role for kind/handle (both guard the SAME failure shape: a bar that
	// reads "pass" because nothing was ever measured, not because the
	// thing being measured behaved correctly).
	//
	// WindowInferredTierRanCount counts every window inferred_tier case
	// whose Investigate() call did not itself error (ArmInvalidReason=="").
	// Non-vacuity bar: must be > 0 -- a run where the window arm never
	// once completed proves nothing about the gate, however clean
	// WindowCommitCount reads.
	WindowInferredTierRanCount int `json:"window_inferred_tier_ran_count"`
	// WindowArmErrorCount counts window inferred_tier cases where
	// Investigate() itself errored (ArmInvalidReason != "" -- for window
	// specifically this ALWAYS means the investigate error branch, window
	// has no PairInvalid/structural-exemption path of its own to
	// conflate it with, see runTwoTurnInferredTierArm). Reported for
	// visibility, not independently gated to 0 (transient errors happen)
	// -- but WindowInferredTierRanCount's own >0 bar already prevents a
	// 100%-erroring run from reading as a vacuous pass.
	WindowArmErrorCount int `json:"window_arm_error_count"`
	// WindowGatedCount counts, of WindowInferredTierRanCount, how many
	// cases show the ACTUAL CHAOS-4040 gate signature: Turn2Status ==
	// clarification_required, TierRoutedCorrectly (already proves
	// EffectiveEvidenceWindow.Provenance == inferred_default), and
	// CommittedCount == 0 together -- not merely "did not commit for
	// whatever reason". Pass condition: WindowGatedCount ==
	// WindowInferredTierRanCount -- EVERY window inferred-tier case that
	// ran must show the gate's own signature, proving the gate is what
	// produced the zero-commit outcome, not an unrelated resolution
	// failure that would also read as WindowCommitCount==0.
	WindowGatedCount int `json:"window_gated_count"`
	// WindowClassDefaultGatedCount (gate 2 coverage, CHAOS-4040) counts
	// turn 1 results (ANY member, the full corpus -- turn 1's own
	// twoTurnRequest never sets an explicit evidence_window field) whose
	// WindowClarification is non-nil. For a windowless MCP request,
	// composeWindowClarification's only possible source is gate 2
	// (windowConfirmationRequiredResult's class-default branch, engine.go)
	// -- gate 1 requires an explicit field turn 1 never sets, and the
	// decisive-path call to composeWindowClarification (engine.go, see its
	// own comment) is permanently unreachable post-gate-2 by construction.
	// This is gate 2's ONLY live-corpus coverage: the inferred_tier arm
	// above always injects an explicit field, which can only ever exercise
	// gate 1, never gate 2 -- the two gates are otherwise entirely
	// disjoint in what a live corpus request can reach. Non-vacuity bar:
	// must be > 0 (corpus-dependent how HIGH it runs -- not every question
	// classifies to a window class, see composeEffectiveWindow's own
	// ClassifyWindow/DefaultRelativeID refusal path -- so no exact-count
	// bar, matching InferredKindHandleDecisiveCount's own >0-only shape).
	WindowClassDefaultGatedCount int `json:"window_class_default_gated_count"`
	// RegimeAOfferComposedCount (CHAOS-4234, schema v29) counts turn-1 rows
	// whose window gate (regime A) composed kind/handle/candidate offers
	// beside the window offer -- the engine-side lever's own reach, before
	// any oracle match. RegimeATurn2AnsweredCount counts positive-arm rows
	// on a regime-A case whose turn 2 ended complete or partial (both
	// gates cleared in ONE turn, the case actually answered) -- the
	// turn-count effect the ruling named. Informational, summed by the
	// merger, no bar: the measurement pair decides.
	RegimeAOfferComposedCount int `json:"regime_a_offer_composed_count"`
	RegimeATurn2AnsweredCount int `json:"regime_a_turn2_answered_count"`
	// WindowGatedOfferedCount/WindowGatedSilentCount (CHAOS-4314, schema
	// v31) partition WindowGatedCount above: every window_gated row now
	// carries a window_expand recommendation (offered) or does not
	// (silent) -- chris "go" 2026-08-26's own success bar is
	// window_gated_silent -> 0. WindowGatedAlreadyWidestCount (CHAOS-4336
	// follow-up, schema v35) splits a THIRD, legitimate-non-offer case out
	// of silent: a gated call whose own effective window is already the
	// registry's widest tier has nothing wider to offer by design, not a
	// defect (see twoTurnClassifyWindowGateOutcome's own doc comment). All
	// three informational, summed by the merger; the one structural
	// invariant (offered+silent+already_widest==window_gated_count) is
	// checked post-merge, not here (see cmd/acr-trial-merge-two-turn's own
	// checkInvariants).
	WindowGatedOfferedCount       int `json:"window_gated_offered_count"`
	WindowGatedSilentCount        int `json:"window_gated_silent_count"`
	WindowGatedAlreadyWidestCount int `json:"window_gated_already_widest_count"`
	// InferredKindHandleDecisiveCount/InferredBaselineEquivalentCount/
	// InferredKindInsensitivityAttestedCount/InferredUnjustifiedCount/
	// InferredPairInvalidCount (CHAOS-4039 v4 contract, kind/handle members
	// ONLY -- window is exempt, see WindowCommitCount) are the member-specific
	// gates replacing the old blanket InferredTierAnyCommit>0 failure.
	// InferredKindHandleDecisiveCount is every CommittedCount>0 kind/handle
	// outcome; the next three fields PARTITION it exactly once each into
	// twoTurnCaseResult.InferredClassification's three buckets (their sum
	// must equal InferredKindHandleDecisiveCount minus InferredPairInvalidCount's
	// own overlap -- a pair-invalid outcome is never also classified, see
	// runTwoTurnInferredTierArm). The pass condition (design brief lines
	// 958/960-adjacent, sol-max ruling): InferredUnjustifiedCount==0 AND
	// InferredPairInvalidCount==0 -- an unjustified or unpaired commit is an
	// immediate run failure, never averaged away by a passing majority.
	//
	// CHAOS-4040 window-gate interaction (team-lead ruling, 2026-08-21,
	// RUN 3 finding): CHAOS-4040's unconditional class-default gate
	// intercepted EVERY kind/handle inferred-tier request at
	// clarification_required before any kind/handle decision logic could
	// run (72/72 valid records, RUN 3, 2026-08-21) -- this field measured
	// 0 for a purely STRUCTURAL reason, not because kind/handle
	// classification never fired. Ruled fix: runTwoTurnInferredTierArm now
	// confirms a window receipt via a setup turn and attaches it to BOTH
	// the no-hint baseline and the hinted call before applying the
	// kind/handle hint (see that function's own doc comment for the full
	// rationale) -- a confirmed window is a legitimate precondition, not a
	// contamination of the pair's single variable.
	InferredKindHandleDecisiveCount        int `json:"inferred_kind_handle_decisive_count"`
	InferredBaselineEquivalentCount        int `json:"inferred_baseline_equivalent_count"`
	InferredKindInsensitivityAttestedCount int `json:"inferred_kind_insensitivity_attested_count"`
	InferredUnjustifiedCount               int `json:"inferred_unjustified_count"`
	InferredPairInvalidCount               int `json:"inferred_pair_invalid_count"`
	// PairRetriedCount (CHAOS-4138) is every row whose PairRetried==true --
	// a pairing that needed and used its ONE bounded retry after an
	// instrument failure (see twoTurnCaseResult.PairRetried's own doc
	// comment). Purely observational: it does not gate this test's own
	// pass/fail (InferredPairInvalidCount already does, on whatever the
	// retry's own final outcome was) -- it exists so a reader can tell "0
	// pair-invalid, 0 retries needed" apart from "0 pair-invalid, 1 retry
	// quietly recovered a stochastic instrument failure" without diffing
	// individual rows.
	PairRetriedCount int `json:"pair_retried_count"`
	// ConfirmedWrongRedeemedCount is PER APPLICABLE MEMBER (codex round-1
	// finding #4: a global scalar lets one member's success mask another
	// member's permanently-unredeemable designated negative).
	ConfirmedWrongRedeemedCount map[string]int `json:"confirmed_wrong_redeemed_committable_count"`
	// ApplicableMembers (CHAOS-4033 parallel-shard support) is the sorted
	// set backing THIS process's own AntiVacuityValid below -- every member
	// this process's own entries designated at least one committable
	// negative for. Stored (not just derived-and-discarded) so a sharded
	// run's merge step can recompute anti-vacuity over the UNION of
	// shards' entries: a shard sharded by corpus case can legitimately see
	// zero cases for a member the corpus assigns to a different shard, so
	// per-shard AntiVacuityValid is not the authoritative signal for a
	// parallel run -- the merge of ApplicableMembers and
	// ConfirmedWrongRedeemedCount across every shard is.
	ApplicableMembers []string `json:"applicable_members"`
	AntiVacuityValid  bool     `json:"anti_vacuity_valid"`
	// AnchorAntiVacuityDenominator/AnchorNonEnumerableKindExcludedCount
	// (orchestrator ruling 2026-08-20): subject_anchor's anti-vacuity
	// requirement is scoped to negatives whose kind the live identity
	// universe actually enumerates (buildAnchorTermIndex/anchorMatchedTerm
	// -- e.g. this deployment's own IdentityUniverse covers repository/
	// project/team, never work_item/pull_request/ci_pipeline_run). A
	// negative naming a non-enumerable kind can NEVER be seeded, by
	// construction, regardless of any bug fix -- that is a SCOPE FACT
	// about this org's own identity-universe coverage, not a defect to
	// paper over by excluding it silently. AnchorAntiVacuityDenominator is
	// how many anchor-committable entries WERE within scope (kind
	// enumerable); AnchorNonEnumerableKindExcludedCount is how many were
	// excluded and why many.
	AnchorAntiVacuityDenominator         int            `json:"anchor_anti_vacuity_denominator"`
	AnchorNonEnumerableKindExcludedCount int            `json:"anchor_non_enumerable_kind_excluded_count"`
	MutationProbesTripped                map[string]int `json:"mutation_probes_tripped"`
	MutationProbesRun                    map[string]int `json:"mutation_probes_run"`
	// MutationProbeEligibleCount (CHAOS-4165) is the count of oracle
	// entries this run's own loop processed with Member != window -- the
	// SAME entry.Member != window precondition (alongside positive.Applied)
	// that gates whether runTwoTurnMutationArm ever runs for an entry.
	// Recorded from the corpus/annex alone, UNCONDITIONALLY, before any
	// live call and regardless of whether the positive arm ever applied --
	// it measures the CEILING this run's mutation-probe population could
	// have reached, not what it happened to reach, so it stays a fair,
	// responder-independent denominator for mutationProbeCoverage.
	MutationProbeEligibleCount int `json:"mutation_probe_eligible_count"`
	// MutationProbeCoverage (CHAOS-4165) is mutationProbeCoverage's own
	// output, computed once at report-assembly time -- see that function
	// and twoTurnMutationProbeCoverage's own doc comments for what each
	// field means and why a ratio alone (MutationProbesTripped/
	// MutationProbesRun) cannot answer the statistical-power question this
	// closes. Computed UNCONDITIONALLY here (mirrors AntiVacuityValid's own
	// "always computed, gated only at check time" discipline) -- a caller
	// checking this at the SHARD level (granularity=1: at most one entry
	// per shard) will correctly read almost every kind as inadequate; the
	// merged, full-corpus artifact (cmd/acr-trial-merge-two-turn) is the
	// only place this is a meaningful signal, exactly like every other
	// coverage/non-vacuity field on this struct.
	MutationProbeCoverage map[string]twoTurnMutationProbeCoverage `json:"mutation_probe_coverage"`
	// ControlsTotal/ControlsWitnessed: see controlSeen/controlOK's own doc
	// comment at the call site for the exact definition.
	//
	// ControlsWitnessed is brief-conformant (CHAOS-3742 run-2 root cause,
	// team-lead ruling 2026-08-20, design brief lines 958/960): a control
	// is "ok" when it commits NOTHING and the turn 1 terminal DISCLOSED
	// structure (StructureNeeds or WindowClarification present) -- brief
	// line 958's "clarify or no_match terminals" are BOTH acceptable
	// control outcomes. The run-2 proxy this replaced checked literal
	// Status==no_match only, which is narrower than the ratified bar and
	// systematically undercounts: real control questions overwhelmingly
	// retrieve at least one plausible-but-unconfident candidate and
	// correctly land on clarification_required, never no_match --
	// unresolved.go's resolveTerminalStatus returns clarification_required
	// for ANY non-empty candidate pool once AllowClarification=true (every
	// harness in this package sets it true), and no_match only for a
	// genuinely EMPTY pool (see
	// TestInvestigateConvertsAmbiguousResolutionToClarificationRequired /
	// TestInvestigateEmptyResolutionIsNoMatch, internal/contextfabric/unresolved_test.go).
	// The old proxy scored every one of those (correct) clarification_required
	// controls as a miss.
	//
	// ONE SCOPE LIMIT a reader must know before comparing this to the DP9
	// "controls 19/19" bar (codex round-3, documented rather than chased
	// further): ControlsTotal reflects only the annex entries THIS run
	// actually processed -- capped by ACR_TEST_TRIAL_LIMIT when set. It is
	// never asserted equal to the full annex's control count; compare it
	// to 19 externally, the same way GateReachableCount is compared to
	// its own >=10 bar rather than asserted against it.
	ControlsTotal     int `json:"controls_total"`
	ControlsWitnessed int `json:"controls_witnessed"`
	// ControlsWitnessedNoMatchCensusBacked is INFORMATIONAL ONLY (no pass/
	// fail bar): the narrower, stronger claim design brief line 960
	// separately makes -- "no_match remains WITNESSED (attestation
	// present) WHERE A CENSUS RAN". True only when turn 1's own Status was
	// the literal no_match AND the CensusFunc-gated evidence round
	// actually fired for that specific call (captured via the SAME
	// hosted.Options.ResolutionTracer hook SingleSatisfierVerified uses,
	// reset/read around the outer turn 1 call the same sequential-single-
	// caller way runTwoTurnInferredTierArm already does around its own
	// call). Expected to be near-zero for a well-designed corpus:
	// resolveTerminalStatus only reaches no_match on a genuinely EMPTY
	// candidate pool, while RunShadowEvidenceRound only runs when a
	// resolution stalled with candidates present (resolve.go:1086,
	// `len(resolution.Committed)==0 && searchTruncated`) -- the two
	// preconditions are close to mutually exclusive by construction. See
	// CHAOS-4039 for the related (distinct) inferred-tier finding this
	// same "committed/stalled resolutions pay nothing" architecture
	// produces.
	ControlsWitnessedNoMatchCensusBacked int                 `json:"controls_witnessed_no_match_census_backed"`
	Results                              []twoTurnCaseResult `json:"results"`
	// Timings/TimingSummary (CHAOS-4058, per-arm + per-model-call timing
	// observability, purely additive -- see twoTurnArmTiming's own doc
	// comment): per-case wall-clock and file-exchange responder round-trip
	// cost for turn 1 and each of the four measured arms, plus a
	// run-level aggregate over every case. Neither field backs any
	// pass/fail check in this file -- observational only.
	Timings       []twoTurnCaseTiming       `json:"timings,omitempty"`
	TimingSummary []twoTurnArmTimingSummary `json:"timing_summary,omitempty"`
	// ConfirmedKindVectorCensus* (CHAOS-4307, schema v30): run-level rollup
	// of the CHAOS-4155 shadow kind-scoped vector census's own outcome,
	// folded (foldConfirmedKindVectorCensus, below) from every
	// "confirmed_kind_scope" ResolutionTraceEvent this run's calls captured
	// -- turn 1, each arm's own turn-2 call, the inferred-tier arm's
	// separate baseline pass, and each mutation-probe result -- BEFORE
	// twoTurnRedactNonAnomalousTraceEvents clears the per-row TraceEvents/
	// BaselineTraceEvents this would otherwise have to be recomputed from.
	// That ordering is load-bearing, not incidental: a census outcome of
	// "complete" or "not_attempted" is not itself an anomaly
	// (twoTurnCaseResultIsAnomalous does not look at it), so on an ordinary
	// run nearly every row's own TraceEvents is redacted to nil before
	// serialization -- these rollups are the ONLY place this run's census
	// activity survives into the artifact at all. Existence of THIS FIELD
	// SET is the whole point of the ticket: CHAOS-4155 Phase 2's own
	// measurement needed four separate reachability-gap PRs (#269 env
	// wiring, #272 launcher deadlock, #274 log-level + slog tee) just to
	// grep the same facts out of DebugContext-level Slog lines across N
	// shard .gotest.log files -- landing here means any future measurement
	// run reads the merged JSON artifact directly, with no dependency on
	// log level, on the two-turn harness (or any future harness making the
	// same in-memory-tracer design choice) remembering to tee to slog, or
	// on shell-side log correlation across shards.
	//
	// ConfirmedKindVectorCensusStateCount is keyed by the closed-vocabulary
	// ConfirmedKindVectorScope* state strings (chaos4155_confirmed_kind_vector_scope.go)
	// this report's own per-row vector-census fields already use --
	// "complete", "over_budget", "malformed", "incomplete_snapshot_drift",
	// "failed", "not_attempted". The empty state (the confirmed_kind_scope
	// event's own vector-census sub-fields never populated at all -- the
	// ordinary, frequent case where buildConfirmedKindScopedSnapshot never
	// reached the branch that invokes the shadow arm in the first place,
	// e.g. no live vector mechanism configured) is DELIBERATELY never
	// tallied here: it is not a census outcome, it is the absence of an
	// attempt, and folding it in would make "how many times did the census
	// actually run" indistinguishable from "how many confirmed_kind_scope
	// events fired at all" -- a materially different, already-answerable
	// (via ConfirmedKindScopeCandidateCount's own sibling fields, out of
	// this ticket's scope) question. omitempty: a run with zero eligible
	// (plan_incomplete, vector-mechanism-configured) cases carries no key
	// at all, matching OfferMissCount's own convention for an unpopulated
	// map.
	ConfirmedKindVectorCensusStateCount map[string]int `json:"confirmed_kind_vector_census_state_count,omitempty"`
	// PopulationSum/ComparisonSum/QueryCountSum/RivalCountAboveTauSum/
	// DurationMSSum sum the correspondingly-named ConfirmedKindVectorScope*
	// field across EVERY confirmed_kind_scope event with a non-empty state
	// this run captured, regardless of which state -- a consumer wanting
	// e.g. "average population size over complete-state censuses" divides
	// PopulationSum by StateCount["complete"] only when that is the
	// quantity wanted; these sums are deliberately unconditional on state
	// so a reader can also ask "total comparison cost this run actually
	// spent" without re-deriving it per state. Sums, not per-case rows: see
	// this field group's own top-of-block comment for why a run-level
	// rollup, not per-case data, is what CHAOS-4307 asked for. omitempty:
	// zero on a run that never folded a single confirmed_kind_scope event
	// with a populated census, indistinguishable from (and no worse than)
	// every real zero-cost census this run may have folded -- StateCount's
	// own presence/absence is the signal for "did this run see the
	// mechanism at all," not these scalars.
	ConfirmedKindVectorCensusPopulationSum         int64 `json:"confirmed_kind_vector_census_population_sum,omitempty"`
	ConfirmedKindVectorCensusComparisonSum         int64 `json:"confirmed_kind_vector_census_comparison_sum,omitempty"`
	ConfirmedKindVectorCensusQueryCountSum         int   `json:"confirmed_kind_vector_census_query_count_sum,omitempty"`
	ConfirmedKindVectorCensusRivalCountAboveTauSum int64 `json:"confirmed_kind_vector_census_rival_count_above_tau_sum,omitempty"`
	ConfirmedKindVectorCensusDurationMSSum         int64 `json:"confirmed_kind_vector_census_duration_ms_sum,omitempty"`
}

// foldConfirmedKindVectorCensus is the single aggregation point for the
// CHAOS-4307 rollup above: it reads every "confirmed_kind_scope" stage event
// out of one already-captured resolve call's own trace (events) and folds
// each one's ConfirmedKindVectorScope* fields into report's run-level
// counters. An event with Stage != "confirmed_kind_scope", or one whose
// ConfirmedKindVectorScopeState is empty (the shadow arm's own branch never
// reached -- see ConfirmedKindVectorCensusStateCount's own doc comment), is
// silently skipped -- both are the ordinary, frequent, not-a-bug shape.
//
// Deliberately sums EVERY matching event rather than reducing to a single
// "last one wins" reading the way kindCoverageFloorEvent/confirmedKindRescueEvent
// do for their own stages: buildConfirmedKindScopedSnapshot can run more
// than once inside a single captured call (a stalled resolution's
// evidence-census re-resolve), and each attempt is a genuinely independent
// census outcome this run actually paid for -- collapsing to the last would
// silently drop real comparison/population cost from the rollup.
//
// Callers are responsible for invoking this EXACTLY ONCE per real resolve()
// call this harness makes into the SAME report -- turn 1 (read directly off
// the shared traceCapture before the first arm's own trace.reset(), the
// same "before the reset" discipline twoTurnCaptureTurn1Facts's own callers
// already follow), each arm's own turn-2 call (res.TraceEvents), the
// inferred-tier arm's separate baseline pass (res.BaselineTraceEvents), and
// each mutation-probe result (its own res.TraceEvents) -- NEVER via
// twoTurnStampTurn1Facts, which stamps the SAME turn-1 facts onto every
// arm's own row and would silently multiply-count turn 1's contribution
// once per arm if this were folded there instead.
func foldConfirmedKindVectorCensus(report *twoTurnReport, events []graphrank.ResolutionTraceEvent) {
	for _, event := range events {
		if event.Stage != "confirmed_kind_scope" || event.ConfirmedKindVectorScopeState == "" {
			continue
		}
		if report.ConfirmedKindVectorCensusStateCount == nil {
			report.ConfirmedKindVectorCensusStateCount = map[string]int{}
		}
		report.ConfirmedKindVectorCensusStateCount[event.ConfirmedKindVectorScopeState]++
		report.ConfirmedKindVectorCensusPopulationSum += event.ConfirmedKindVectorScopePopulationCount
		report.ConfirmedKindVectorCensusComparisonSum += event.ConfirmedKindVectorScopeComparisonCount
		report.ConfirmedKindVectorCensusQueryCountSum += event.ConfirmedKindVectorScopeQueryCount
		report.ConfirmedKindVectorCensusRivalCountAboveTauSum += event.ConfirmedKindVectorScopeRivalCountAboveTau
		report.ConfirmedKindVectorCensusDurationMSSum += event.ConfirmedKindVectorScopeDurationMS
	}
}

// TestTwoTurnCaseResultCarriesNoQuestionText mirrors
// TestReplayCaseOutcomeCarriesNoQuestionOrTermText's own canary for this
// file's new report type.
func TestTwoTurnCaseResultCarriesNoQuestionText(t *testing.T) {
	t.Parallel()
	forbidden := map[string]bool{
		"question": true, "term": true, "terms": true, "label": true, "text": true, "prompt": true,
	}
	typ := reflect.TypeOf(twoTurnCaseResult{})
	for i := 0; i < typ.NumField(); i++ {
		name := lowerASCII(typ.Field(i).Name)
		if forbidden[name] {
			t.Errorf("twoTurnCaseResult.%s: field name suggests free text reaching the outcome-only artifact", typ.Field(i).Name)
		}
	}
}

// --- CHAOS-4058: per-case/per-arm timing observability (purely additive,
// no measurement semantics -- these types back none of this file's
// pass/fail checks; see generative_trial_live_test.go's
// twoTurnTimedModelRuntime/twoTurnModelCallCapture for the underlying
// capture mechanism) ---

// twoTurnArmTiming is turn 1's or one arm's wall-clock and file-exchange
// responder round-trip cost for a single case. Arm is "turn1" for the
// preliminary disclosure call, or one of twoTurnArm's own string values
// for the four measured arms.
type twoTurnArmTiming struct {
	Arm            string `json:"arm"`
	WallDurationMS int64  `json:"wall_duration_ms"`
	// ResponderCallCount/ResponderCallTotalMS/ResponderCallMaxMS are zero
	// whenever the run used the real_api transport -- twoTurnModelCallCapture
	// is only wired for the file-exchange transport (see
	// TestChaos3742TwoTurnConfirmationReplay's own setup) -- and zero for
	// the mutation arm on a case where it never ran (member==window, the
	// positive arm did not apply, or no redeemable offer was found; see
	// mutationTiming's own zero-value default at the call site).
	ResponderCallCount   int   `json:"responder_call_count,omitempty"`
	ResponderCallTotalMS int64 `json:"responder_call_total_ms,omitempty"`
	ResponderCallMaxMS   int64 `json:"responder_call_max_ms,omitempty"`
}

// buildTwoTurnArmTiming reads elapsed wall time since started plus
// capture's accumulated model-call samples (reset by the caller
// immediately before the timed call, per twoTurnModelCallCapture's own
// reset-before/read-after discipline) into one twoTurnArmTiming record.
func buildTwoTurnArmTiming(arm string, started time.Time, capture *twoTurnModelCallCapture) twoTurnArmTiming {
	timing := twoTurnArmTiming{Arm: arm, WallDurationMS: time.Since(started).Milliseconds()}
	if capture != nil {
		count, total, max := capture.stats()
		timing.ResponderCallCount = count
		timing.ResponderCallTotalMS = total.Milliseconds()
		timing.ResponderCallMaxMS = max.Milliseconds()
	}
	return timing
}

// twoTurnCaseTiming is one case's full set of turn-1-plus-arm timings.
type twoTurnCaseTiming struct {
	Index  int                `json:"index"`
	Member string             `json:"member"`
	Arms   []twoTurnArmTiming `json:"arms"`
}

// twoTurnArmTimingSummary is one arm's (or turn 1's) run-level timing
// aggregate over every case that recorded it.
type twoTurnArmTimingSummary struct {
	Arm         string  `json:"arm"`
	SampleCount int     `json:"sample_count"`
	WallMeanMS  float64 `json:"wall_mean_ms"`
	WallP50MS   int64   `json:"wall_p50_ms"`
	WallMaxMS   int64   `json:"wall_max_ms"`
	// ResponderCallMaxMS (codex round-1 finding) is the max over every
	// per-case twoTurnArmTiming.ResponderCallMaxMS this arm recorded --
	// the run-level single-slowest-round-trip signal, not merely
	// recoverable from ResponderCallTotalMS/ResponderCallCount (a mean
	// hides one outlier case that alone explains a slow run).
	ResponderCallMaxMS   int64 `json:"responder_call_max_ms"`
	ResponderCallCount   int   `json:"responder_call_count"`
	ResponderCallTotalMS int64 `json:"responder_call_total_ms"`
}

// summarizeTwoTurnTiming reduces per-case timings into one aggregate per
// arm, preserving first-seen arm order (turn1, then whichever order each
// arm first appears in the run) so the summary reads in the same sequence
// the per-case loop itself executes.
func summarizeTwoTurnTiming(timings []twoTurnCaseTiming) []twoTurnArmTimingSummary {
	order := make([]string, 0, 5)
	byArm := map[string][]twoTurnArmTiming{}
	for _, ct := range timings {
		for _, at := range ct.Arms {
			if _, seen := byArm[at.Arm]; !seen {
				order = append(order, at.Arm)
			}
			byArm[at.Arm] = append(byArm[at.Arm], at)
		}
	}
	summaries := make([]twoTurnArmTimingSummary, 0, len(order))
	for _, arm := range order {
		samples := byArm[arm]
		wall := make([]int64, len(samples))
		var totalWall, maxWall int64
		var callCount int
		var callTotal, callMax int64
		for i, s := range samples {
			wall[i] = s.WallDurationMS
			totalWall += s.WallDurationMS
			if s.WallDurationMS > maxWall {
				maxWall = s.WallDurationMS
			}
			callCount += s.ResponderCallCount
			callTotal += s.ResponderCallTotalMS
			if s.ResponderCallMaxMS > callMax {
				callMax = s.ResponderCallMaxMS
			}
		}
		sort.Slice(wall, func(i, j int) bool { return wall[i] < wall[j] })
		summaries = append(summaries, twoTurnArmTimingSummary{
			Arm: arm, SampleCount: len(samples),
			WallMeanMS: float64(totalWall) / float64(len(samples)),
			WallP50MS:  wall[len(wall)/2], WallMaxMS: maxWall,
			ResponderCallCount: callCount, ResponderCallTotalMS: callTotal, ResponderCallMaxMS: callMax,
		})
	}
	return summaries
}

// --- pure-logic tests: no live infra, run unconditionally under `make verify` ---

func TestRequireAnnexSignedOff(t *testing.T) {
	t.Parallel()
	if err := requireAnnexSignedOff(twoTurnOracleAnnex{SignedOff: false}); err == nil {
		t.Error("requireAnnexSignedOff(unsigned) = nil, want an error refusing the run (DP10 gate)")
	}
	if err := requireAnnexSignedOff(twoTurnOracleAnnex{SignedOff: true}); err != nil {
		t.Errorf("requireAnnexSignedOff(signed) = %v, want nil", err)
	}
}

func TestValidateTwoTurnOracleAnnex(t *testing.T) {
	t.Parallel()
	if err := validateTwoTurnOracleAnnex(twoTurnOracleAnnex{}); err == nil {
		t.Error("validateTwoTurnOracleAnnex(no corpus_sha256) = nil, want an error")
	}
	if err := validateTwoTurnOracleAnnex(twoTurnOracleAnnex{
		CorpusSHA256: "deadbeef",
		Entries:      []twoTurnOracleEntry{{Index: 0, Member: "not_a_real_member"}},
	}); err == nil {
		t.Error("validateTwoTurnOracleAnnex(unknown member) = nil, want an error")
	}
	if err := validateTwoTurnOracleAnnex(twoTurnOracleAnnex{
		CorpusSHA256: "deadbeef",
		Entries:      []twoTurnOracleEntry{{Index: 0, Member: string(contractsv1.ContextFabricStructureNeedSubjectAnchor)}},
	}); err != nil {
		t.Errorf("validateTwoTurnOracleAnnex(valid) = %v, want nil", err)
	}
}

// TestAdaptSignedOracleAnnex pins the loader's adapter against a small,
// synthetic slice of the REAL signed-artifact shape (mirroring
// .remember/trial-results/oracle-annex-v1.json's own case 0, case 5, and
// case 30 verbatim in structure) -- sign-off detection from the nested
// Signoff block (never the stale top-level Ratification/Status strings),
// sha8 passthrough, per-(case,member) flattening, the anchor "kind:id"
// split, and handle pattern_id derivation from the case's own kind.
func TestAnchorMatchedTerm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		row  graphrank.IdentityRow
		want string
		ok   bool
	}{
		{"label wins", graphrank.IdentityRow{Label: "Ask Dev", Aliases: []string{"askdev"}}, "Ask Dev", true},
		{"falls back to alias when label blank", graphrank.IdentityRow{Label: "  ", Aliases: []string{"", "askdev"}}, "askdev", true},
		{"falls back to provider alias when label and aliases blank", graphrank.IdentityRow{ProviderAliases: []string{"gh:ask-dev"}}, "gh:ask-dev", true},
		{"nothing usable", graphrank.IdentityRow{}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := anchorMatchedTerm(tc.row)
			if got != tc.want || ok != tc.ok {
				t.Errorf("anchorMatchedTerm(%+v) = (%q, %v), want (%q, %v)", tc.row, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestBuildAnchorTermIndex(t *testing.T) {
	t.Parallel()
	rows := []graphrank.IdentityRow{
		{Kind: "repository", CanonicalID: "r1", Label: "Ask Dev"},
		{Kind: "work_item", CanonicalID: "w1", Aliases: []string{"CHAOS-1"}},
		{Kind: "repository", CanonicalID: "r2"}, // no usable term -- excluded
	}
	fn := func(ctx context.Context, orgID string) ([]graphrank.IdentityRow, time.Time, bool, error) {
		return rows, time.Now(), true, nil
	}
	index, err := buildAnchorTermIndex(context.Background(), fn, "org1")
	if err != nil {
		t.Fatalf("buildAnchorTermIndex: %v", err)
	}
	if got := index[anchorTermIndexKey("repository", "r1")]; got != "Ask Dev" {
		t.Errorf("index[repository/r1] = %q, want Ask Dev", got)
	}
	if got := index[anchorTermIndexKey("work_item", "w1")]; got != "CHAOS-1" {
		t.Errorf("index[work_item/w1] = %q, want CHAOS-1", got)
	}
	if _, ok := index[anchorTermIndexKey("repository", "r2")]; ok {
		t.Error("index[repository/r2] present, want absent (no usable term)")
	}

	// Fail-closed on incomplete enumeration (same rule VerifyAnchorClaimantUnique itself applies).
	incomplete := func(ctx context.Context, orgID string) ([]graphrank.IdentityRow, time.Time, bool, error) {
		return rows, time.Now(), false, nil
	}
	if _, err := buildAnchorTermIndex(context.Background(), incomplete, "org1"); err == nil {
		t.Error("buildAnchorTermIndex(incomplete) = nil error, want an error")
	}

	// Fail-closed when the identity universe read itself is not wired.
	if _, err := buildAnchorTermIndex(context.Background(), nil, "org1"); err == nil {
		t.Error("buildAnchorTermIndex(nil func) = nil error, want an error")
	}
}

func TestEnumerableAnchorKinds(t *testing.T) {
	t.Parallel()
	terms := anchorTermIndex{
		anchorTermIndexKey("repository", "repository:r1"): "Ask Dev",
		anchorTermIndexKey("repository", "repository:r2"): "Container",
		anchorTermIndexKey("project", "project:p1"):       "Fullchaos",
	}
	got := enumerableAnchorKinds(terms)
	want := map[string]bool{"repository": true, "project": true}
	if len(got) != len(want) || !got["repository"] || !got["project"] {
		t.Errorf("enumerableAnchorKinds(%v) = %v, want %v", terms, got, want)
	}
	if got["work_item"] {
		t.Error("enumerableAnchorKinds reported work_item as enumerable, want false (not present in the index)")
	}
	if empty := enumerableAnchorKinds(nil); len(empty) != 0 {
		t.Errorf("enumerableAnchorKinds(nil) = %v, want empty", empty)
	}
}

func TestAdaptSignedOracleAnnex(t *testing.T) {
	t.Parallel()
	const raw = `{
		"provenance": {
			"corpus_sha8": "7204a2e6",
			"ratification": "DP10_mechanism_ratified_chris_sign_off_pending",
			"status": "DRAFT",
			"signoff": {"by": "chris", "at_pt": "2026-08-19 10:57", "scope": "DP10 artifact sign-off incl. designated committable negatives", "status": "APPROVED"}
		},
		"cases": {
			"0": {
				"question_class": "subject_status", "band": "paraphrase",
				"oracles": {
					"kind": {"positive": "repository", "negatives": ["work_item"]},
					"anchor": {"positive_key": "repository:7b9583ee-4d24-2be7-4d09-34f815bebdd7", "negatives": ["repository:d29d160a-95fe-5b45-d4c1-fd1f5427b772"]},
					"window": {"positive_band": "all_time", "negatives": ["trailing_90d"]},
					"handle": {"positive": null, "negatives": []}
				},
				"committable_negative_designations": [
					{"member": "kind", "value": "work_item", "constructor": "setup_turn"},
					{"member": "anchor", "value": "repository:d29d160a-95fe-5b45-d4c1-fd1f5427b772", "constructor": "seeded_result"},
					{"member": "window", "value": "trailing_90d", "constructor": "setup_turn"}
				]
			},
			"5": {
				"question_class": "subject_status", "band": "paraphrase",
				"oracles": {
					"kind": {"positive": "work_item", "negatives": ["repository"]},
					"anchor": {"positive_key": "work_item:linear:CHAOS-2476", "negatives": ["work_item:linear:CHAOS-2393"]},
					"window": {"positive_band": "all_time", "negatives": ["trailing_90d"]},
					"handle": {"positive": "CHAOS-2476", "negatives": ["CHAOS-2393"]}
				},
				"committable_negative_designations": [
					{"member": "handle", "value": "CHAOS-2393", "constructor": "setup_turn"}
				]
			},
			"30": {
				"question_class": "existence_probe", "band": "false_friend",
				"oracles": {
					"kind": {"positive": "ci_pipeline_run", "negatives": ["work_item"]},
					"anchor": {"positive_key": null, "negatives": ["repository:7b110eba-4183-c29e-53b9-92fb058a29cb"]},
					"window": {"positive_band": "all_time", "negatives": ["trailing_90d"]},
					"handle": {"positive": null, "negatives": []}
				},
				"committable_negative_designations": []
			}
		}
	}`
	var signed signedOracleAnnex
	if err := json.Unmarshal([]byte(raw), &signed); err != nil {
		t.Fatalf("unmarshal synthetic signed annex: %v", err)
	}
	annex := adaptSignedOracleAnnex(signed)

	if !annex.SignedOff {
		t.Error("SignedOff = false, want true (nested signoff.status=APPROVED, by=chris -- the stale top-level ratification/status strings must NOT gate this)")
	}
	if annex.CorpusSHA256 != "7204a2e6" {
		t.Errorf("CorpusSHA256 = %q, want the sha8 passthrough %q", annex.CorpusSHA256, "7204a2e6")
	}

	byIndexMember := map[string]twoTurnOracleEntry{}
	for _, e := range annex.Entries {
		byIndexMember[fmt.Sprintf("%d/%s", e.Index, e.Member)] = e
	}

	// Case 0: kind/anchor/window present (handle absent -- both positive
	// and negatives empty), all three committable.
	if e, ok := byIndexMember["0/expected_kind"]; !ok || e.PositiveKind != "repository" || e.NegativeKind != "work_item" || !e.NegativeCommittable {
		t.Errorf("case 0 expected_kind entry = %+v, ok=%v, want positive=repository negative=work_item committable=true", e, ok)
	}
	if e, ok := byIndexMember["0/subject_anchor"]; !ok || e.PositiveKind != "repository" || e.PositiveAnchorCanonicalID != "repository:7b9583ee-4d24-2be7-4d09-34f815bebdd7" ||
		e.NegativeKind != "repository" || e.NegativeAnchorCanonicalID != "repository:d29d160a-95fe-5b45-d4c1-fd1f5427b772" || !e.NegativeCommittable {
		t.Errorf("case 0 subject_anchor entry = %+v, ok=%v, want CanonicalID to keep the FULL kind:id composite (live-run finding: graphrank.IdentityRow.CanonicalID carries this same composite) and committable=true", e, ok)
	}
	if _, ok := byIndexMember["0/subject_handle"]; ok {
		t.Error("case 0 subject_handle entry present, want none (positive=null, negatives=[])")
	}

	// Case 5: handle present, pattern_id derived from the case's own
	// positive kind (work_item -> work_item_ticket_key), committable.
	e, ok := byIndexMember["5/subject_handle"]
	if !ok {
		t.Fatal("case 5 subject_handle entry missing")
	}
	if e.PositiveKind != "work_item" || e.NegativeKind != "work_item" {
		t.Errorf("case 5 subject_handle Kind fields = positive=%q negative=%q, want work_item/work_item (live-run finding: these were never set, failing ContextFabricRequestedHandle.Validate universally)", e.PositiveKind, e.NegativeKind)
	}
	if e.PositiveHandlePatternID != "work_item_ticket_key" || e.PositiveHandleValue != "CHAOS-2476" {
		t.Errorf("case 5 subject_handle positive = pattern=%q value=%q, want work_item_ticket_key/CHAOS-2476", e.PositiveHandlePatternID, e.PositiveHandleValue)
	}
	if e.NegativeHandlePatternID != "work_item_ticket_key" || e.NegativeHandleValue != "CHAOS-2393" || !e.NegativeCommittable {
		t.Errorf("case 5 subject_handle negative = pattern=%q value=%q committable=%v, want work_item_ticket_key/CHAOS-2393/true", e.NegativeHandlePatternID, e.NegativeHandleValue, e.NegativeCommittable)
	}

	// Case 30 (existence_probe control): anchor positive_key is null (no
	// true positive subject), but a non-committable negative still
	// produces an entry -- the inferred-tier arm can still exercise it
	// even though confirmed_wrong's anti-vacuity cannot count it.
	e, ok = byIndexMember["30/subject_anchor"]
	if !ok {
		t.Fatal("case 30 subject_anchor entry missing")
	}
	if e.PositiveAnchorCanonicalID != "" {
		t.Errorf("case 30 subject_anchor positive_anchor_canonical_id = %q, want empty (no true positive)", e.PositiveAnchorCanonicalID)
	}
	if e.NegativeAnchorCanonicalID != "repository:7b110eba-4183-c29e-53b9-92fb058a29cb" || e.NegativeCommittable {
		t.Errorf("case 30 subject_anchor negative = %q committable=%v, want the negative present (full kind:id composite) but NOT committable (no designation)", e.NegativeAnchorCanonicalID, e.NegativeCommittable)
	}
}

func TestSelectOracleOffer(t *testing.T) {
	t.Parallel()
	base := contractsv1.ContextFabricInvestigationResult{
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
			KindOptions: []contractsv1.ContextFabricKindOption{
				{ReceiptID: "kindr_aaaaaaaaaaaaaaaaaaaaaaaa", Kind: contractsv1.ContextFabricSubjectPullRequest},
			},
			AnchorOptions: []contractsv1.ContextFabricAnchorOption{
				{ReceiptID: "ancr_bbbbbbbbbbbbbbbbbbbbbbbb", Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository_ask_dev"},
				// A second offer sharing the same CanonicalID under a
				// DIFFERENT kind -- proves kind-typed matching (codex
				// round-1 finding #5), not just CanonicalID equality.
				{ReceiptID: "ancr_zzzzzzzzzzzzzzzzzzzzzzzz", Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "repository_ask_dev"},
			},
			HandleOptions: []contractsv1.ContextFabricHandleOption{
				{ReceiptID: "handr_cccccccccccccccccccccc", Kind: contractsv1.ContextFabricSubjectPullRequest, PatternID: "pull_request_number", Value: "532"},
			},
			// CHAOS-4121: content-identical to WindowClarification.Options
			// below (a separate literal here, not the same slice value --
			// this fixture only needs matching CONTENT to prove
			// selectOracleOffer's window case now reads THIS field, not
			// WindowClarification; TestTwoTurnWindowSurfacesAgree and
			// chaos4138WindowSetupResult are what actually exercise the
			// "same slice, never a copy" production shape window.go:1315
			// guarantees).
			WindowOptions: []contractsv1.ContextFabricWindowOption{
				{ReceiptID: "winr_dddddddddddddddddddddd", RelativeID: "trailing_90d"},
			},
		},
		WindowClarification: &contractsv1.ContextFabricWindowClarification{
			Options: []contractsv1.ContextFabricWindowOption{
				{ReceiptID: "winr_dddddddddddddddddddddd", RelativeID: "trailing_90d"},
			},
		},
	}

	cases := []struct {
		name          string
		member        string
		q             oracleOfferQuery
		wantReceiptID string
		wantFound     bool
		nilResult     bool
	}{
		{"kind match", string(contractsv1.ContextFabricStructureNeedExpectedKind), oracleOfferQuery{kind: "pull_request"}, "kindr_aaaaaaaaaaaaaaaaaaaaaaaa", true, false},
		{"kind miss", string(contractsv1.ContextFabricStructureNeedExpectedKind), oracleOfferQuery{kind: "review"}, "", false, false},
		{"anchor match by kind", string(contractsv1.ContextFabricStructureNeedSubjectAnchor), oracleOfferQuery{kind: "repository", anchorID: "repository_ask_dev"}, "ancr_bbbbbbbbbbbbbbbbbbbbbbbb", true, false},
		{"anchor match other kind, same id", string(contractsv1.ContextFabricStructureNeedSubjectAnchor), oracleOfferQuery{kind: "project", anchorID: "repository_ask_dev"}, "ancr_zzzzzzzzzzzzzzzzzzzzzzzz", true, false},
		{"anchor miss on kind mismatch", string(contractsv1.ContextFabricStructureNeedSubjectAnchor), oracleOfferQuery{kind: "team", anchorID: "repository_ask_dev"}, "", false, false},
		{"anchor miss on id", string(contractsv1.ContextFabricStructureNeedSubjectAnchor), oracleOfferQuery{kind: "repository", anchorID: "repository_other"}, "", false, false},
		{"handle match", string(contractsv1.ContextFabricStructureNeedSubjectHandle), oracleOfferQuery{kind: "pull_request", handlePatternID: "pull_request_number", handleValue: "532"}, "handr_cccccccccccccccccccccc", true, false},
		{"handle miss on pattern", string(contractsv1.ContextFabricStructureNeedSubjectHandle), oracleOfferQuery{kind: "pull_request", handlePatternID: "wrong_pattern", handleValue: "532"}, "", false, false},
		{"window match (from StructureNeeds.WindowOptions, CHAOS-4121)", string(contractsv1.ContextFabricStructureNeedWindow), oracleOfferQuery{windowBand: "trailing_90d"}, "winr_dddddddddddddddddddddd", true, false},
		{"nil result", string(contractsv1.ContextFabricStructureNeedExpectedKind), oracleOfferQuery{kind: "pull_request"}, "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			if tc.nilResult {
				input = contractsv1.ContextFabricInvestigationResult{}
			}
			gotID, gotFound := selectOracleOffer(input, tc.member, tc.q)
			if gotFound != tc.wantFound || gotID != tc.wantReceiptID {
				t.Errorf("selectOracleOffer(%s) = (%q, %v), want (%q, %v)", tc.name, gotID, gotFound, tc.wantReceiptID, tc.wantFound)
			}
		})
	}
}

// TestSelectOracleOfferWindowIgnoresLegacyFieldAlone pins CHAOS-4121's own
// behavior change directly: a result carrying a POPULATED
// WindowClarification.Options but a nil StructureNeeds (the shape a
// pre-CHAOS-4118 result, or any future divergence, would have) must miss --
// proving the legacy field alone can no longer redeem a window offer.
func TestSelectOracleOfferWindowIgnoresLegacyFieldAlone(t *testing.T) {
	t.Parallel()
	input := contractsv1.ContextFabricInvestigationResult{
		WindowClarification: &contractsv1.ContextFabricWindowClarification{
			Options: []contractsv1.ContextFabricWindowOption{
				{ReceiptID: "winr_eeeeeeeeeeeeeeeeeeeeee", RelativeID: "trailing_90d"},
			},
		},
	}
	gotID, gotFound := selectOracleOffer(input, string(contractsv1.ContextFabricStructureNeedWindow), oracleOfferQuery{windowBand: "trailing_90d"})
	if gotFound || gotID != "" {
		t.Errorf("selectOracleOffer with WindowClarification populated but StructureNeeds nil = (%q, %v), want (\"\", false)", gotID, gotFound)
	}
}

// TestTwoTurnWindowSurfacesAgree exercises the parity guard's own pure
// predicate directly -- see twoTurnWindowSurfacesAgree's own doc comment
// for why the predicate is tested rather than driving
// twoTurnAssertWindowSurfacesAgree's real t.Fatalf (a genuinely failing
// subtest would cascade this whole package's test run to FAIL even though
// catching the divergence is the correct, intended behavior). Passes
// (true) when the two surfaces agree, including both nil (the "no window
// regime" case); false whenever they diverge.
func TestTwoTurnWindowSurfacesAgree(t *testing.T) {
	t.Parallel()
	agreeing := []contractsv1.ContextFabricWindowOption{{ReceiptID: "winr_ffffffffffffffffffffff", RelativeID: "trailing_90d"}}

	cases := []struct {
		name   string
		result contractsv1.ContextFabricInvestigationResult
		want   bool
	}{
		{"both nil (no window regime)", contractsv1.ContextFabricInvestigationResult{}, true},
		{
			"both populated, identical",
			contractsv1.ContextFabricInvestigationResult{
				WindowClarification: &contractsv1.ContextFabricWindowClarification{Options: agreeing},
				StructureNeeds:      &contractsv1.ContextFabricStructureNeeds{WindowOptions: agreeing},
			},
			true,
		},
		{
			"legacy populated, unified nil -- DIVERGE",
			contractsv1.ContextFabricInvestigationResult{
				WindowClarification: &contractsv1.ContextFabricWindowClarification{Options: agreeing},
			},
			false,
		},
		{
			"unified populated, legacy nil -- DIVERGE",
			contractsv1.ContextFabricInvestigationResult{
				StructureNeeds: &contractsv1.ContextFabricStructureNeeds{WindowOptions: agreeing},
			},
			false,
		},
		{
			"both populated, different content -- DIVERGE",
			contractsv1.ContextFabricInvestigationResult{
				WindowClarification: &contractsv1.ContextFabricWindowClarification{Options: agreeing},
				StructureNeeds: &contractsv1.ContextFabricStructureNeeds{WindowOptions: []contractsv1.ContextFabricWindowOption{
					{ReceiptID: "winr_gggggggggggggggggggggg", RelativeID: "all_time"},
				}},
			},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := twoTurnWindowSurfacesAgree(tc.result)
			if got != tc.want {
				t.Errorf("twoTurnWindowSurfacesAgree(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestTwoTurnCommittedWrong(t *testing.T) {
	t.Parallel()
	tc := trialCase{Question: "q", ExpectKind: "repository", ExpectID: "repository:r1"}
	right := []contractsv1.ContextFabricSubjectRef{{Kind: "repository", CanonicalID: "repository:r1"}}
	wrong := []contractsv1.ContextFabricSubjectRef{{Kind: "repository", CanonicalID: "repository:other"}}

	if twoTurnCommittedWrong(nil, tc) {
		t.Error("twoTurnCommittedWrong(no commit) = true, want false")
	}
	if twoTurnCommittedWrong(right, tc) {
		t.Error("twoTurnCommittedWrong(correct commit) = true, want false")
	}
	if !twoTurnCommittedWrong(wrong, tc) {
		t.Error("twoTurnCommittedWrong(wrong commit) = false, want true")
	}
}

func TestTwoTurnMutationProbe(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		applied        bool
		status         string
		committedCount int
		wantTripped    bool
	}{
		{"veto with no commit trips", false, "no_match", 0, true},
		{"applied never trips", true, "no_match", 0, false},
		{"decisive commit never trips", false, string(contractsv1.ContextFabricInvestigationComplete), 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := twoTurnMutationProbe(tc.applied, tc.status, tc.committedCount); got != tc.wantTripped {
				t.Errorf("twoTurnMutationProbe(%v, %q, %d) = %v, want %v", tc.applied, tc.status, tc.committedCount, got, tc.wantTripped)
			}
		})
	}
}

// TestMutationProbeCoverage (CHAOS-4165) pins mutationProbeCoverage's own
// three behaviors: the floor caps required_min at mutationProbeCoverageFloor
// when eligible exceeds it; a small eligible population caps required_min
// at eligible itself instead (never demanding more runs than the
// population could structurally supply); and a probe kind entirely ABSENT
// from `ran` still gets a returned entry with Runs=0, never silently
// dropped -- see mutationProbeKinds's own doc comment for the exact gap a
// naive `range ran` would reintroduce.
func TestMutationProbeCoverage(t *testing.T) {
	t.Parallel()
	t.Run("eligible exceeds floor: required_min is the floor", func(t *testing.T) {
		cov := mutationProbeCoverage(map[string]int{"remove_confirmation": 7, "corrupt_receipt": 7, "stale_superseded_offer": 7}, 65)
		for _, kind := range mutationProbeKinds {
			if got := cov[kind]; got.RequiredMin != mutationProbeCoverageFloor || !got.Adequate {
				t.Errorf("%s = %+v, want required_min=%d adequate=true", kind, got, mutationProbeCoverageFloor)
			}
		}
	})
	t.Run("low run count under the floor: adequate is false", func(t *testing.T) {
		cov := mutationProbeCoverage(map[string]int{"remove_confirmation": 1, "corrupt_receipt": 1, "stale_superseded_offer": 1}, 65)
		for _, kind := range mutationProbeKinds {
			if got := cov[kind]; got.Runs != 1 || got.RequiredMin != mutationProbeCoverageFloor || got.Adequate {
				t.Errorf("%s = %+v, want runs=1 required_min=%d adequate=false", kind, got, mutationProbeCoverageFloor)
			}
		}
	})
	t.Run("eligible population smaller than the floor: required_min caps at eligible, not the floor", func(t *testing.T) {
		cov := mutationProbeCoverage(map[string]int{"remove_confirmation": 2}, 2)
		got := cov["remove_confirmation"]
		if got.RequiredMin != 2 || !got.Adequate {
			t.Errorf("remove_confirmation = %+v, want required_min=2 adequate=true -- a 2-case eligible population must never be held to a 5-run floor it cannot structurally reach", got)
		}
	})
	t.Run("a probe kind absent from ran still gets an explicit zero-runs entry", func(t *testing.T) {
		cov := mutationProbeCoverage(map[string]int{"remove_confirmation": 7}, 65)
		if len(cov) != len(mutationProbeKinds) {
			t.Fatalf("mutationProbeCoverage returned %d kinds, want all %d of mutationProbeKinds regardless of what `ran` carries", len(cov), len(mutationProbeKinds))
		}
		if got := cov["stale_superseded_offer"]; got.Runs != 0 || got.Adequate {
			t.Errorf("stale_superseded_offer (absent from ran) = %+v, want runs=0 adequate=false, not silently missing", got)
		}
	})
	t.Run("zero eligible population: never adequate, even at runs=0 required_min=0", func(t *testing.T) {
		cov := mutationProbeCoverage(map[string]int{}, 0)
		for _, kind := range mutationProbeKinds {
			if got := cov[kind]; got.Adequate {
				t.Errorf("%s = %+v, want adequate=false -- a zero-eligible population's 0>=0 must never read as adequate (codex review finding)", kind, got)
			}
		}
	})
}

// TestHasConstructiblePositiveOffer (CHAOS-4165, codex review round 2, P2)
// pins the exact gap the whole-struct positiveQuery() zero-value test
// missed: a negative-only subject_handle entry where
// adaptSignedOracleAnnex still populates PositiveKind/PositiveHandlePatternID
// unconditionally from the case's own kind, leaving only PositiveHandleValue
// empty -- selectOracleOffer's own three-way match can never satisfy that,
// so it must read as NOT constructible despite two of its three fields
// being non-empty.
func TestHasConstructiblePositiveOffer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		entry twoTurnOracleEntry
		want  bool
	}{
		{"expected_kind with a positive kind", twoTurnOracleEntry{Member: "expected_kind", PositiveKind: "repository"}, true},
		{"expected_kind negative-only (no positive kind at all)", twoTurnOracleEntry{Member: "expected_kind"}, false},
		{"subject_anchor with kind and canonical id", twoTurnOracleEntry{Member: "subject_anchor", PositiveKind: "repository", PositiveAnchorCanonicalID: "repository:acme/widgets"}, true},
		{"subject_anchor negative-only (no positive canonical id)", twoTurnOracleEntry{Member: "subject_anchor", PositiveKind: "repository"}, false},
		{"subject_handle fully populated", twoTurnOracleEntry{Member: "subject_handle", PositiveKind: "repository", PositiveHandlePatternID: "repo_slug", PositiveHandleValue: "acme/widgets"}, true},
		{
			"subject_handle negative-only: kind+pattern derived unconditionally, value empty -- THE exact codex-flagged gap",
			twoTurnOracleEntry{Member: "subject_handle", PositiveKind: "repository", PositiveHandlePatternID: "repo_slug", PositiveHandleValue: ""},
			false,
		},
		{"window is never constructible via this helper (caller excludes it separately)", twoTurnOracleEntry{Member: "window", PositiveWindowBand: "last_7d"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.hasConstructiblePositiveOffer(); got != tc.want {
				t.Errorf("hasConstructiblePositiveOffer() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTwoTurnCommittedSubjectsEquivalent pins CHAOS-4039 v5's own
// engine-deterministic committed-subject-set comparison: Kind+CanonicalID
// only (Label-tolerant), order-independent, and count-sensitive.
func TestTwoTurnCommittedSubjectsEquivalent(t *testing.T) {
	t.Parallel()
	repoA := contractsv1.ContextFabricSubjectRef{Kind: "repository", CanonicalID: "repository:acme/widgets", Label: "widgets"}
	repoADifferentLabel := contractsv1.ContextFabricSubjectRef{Kind: "repository", CanonicalID: "repository:acme/widgets", Label: "a completely different label"}
	repoB := contractsv1.ContextFabricSubjectRef{Kind: "repository", CanonicalID: "repository:acme/other"}
	workItem := contractsv1.ContextFabricSubjectRef{Kind: "work_item", CanonicalID: "work_item:linear:CHAOS-1"}

	if !twoTurnCommittedSubjectsEquivalent([]contractsv1.ContextFabricSubjectRef{repoA}, []contractsv1.ContextFabricSubjectRef{repoADifferentLabel}) {
		t.Error("twoTurnCommittedSubjectsEquivalent = false for the SAME Kind+CanonicalID with a differing Label, want true (Label is presentation text, never compared)")
	}
	if !twoTurnCommittedSubjectsEquivalent(
		[]contractsv1.ContextFabricSubjectRef{repoA, workItem},
		[]contractsv1.ContextFabricSubjectRef{workItem, repoA},
	) {
		t.Error("twoTurnCommittedSubjectsEquivalent = false for the same set in a different ORDER, want true (set semantics, not slice-order)")
	}
	if twoTurnCommittedSubjectsEquivalent([]contractsv1.ContextFabricSubjectRef{repoA}, []contractsv1.ContextFabricSubjectRef{repoB}) {
		t.Error("twoTurnCommittedSubjectsEquivalent = true for two DIFFERENT canonical ids, want false")
	}
	if twoTurnCommittedSubjectsEquivalent([]contractsv1.ContextFabricSubjectRef{repoA}, []contractsv1.ContextFabricSubjectRef{repoA, workItem}) {
		t.Error("twoTurnCommittedSubjectsEquivalent = true for sets of different SIZE, want false")
	}
	if !twoTurnCommittedSubjectsEquivalent(nil, nil) {
		t.Error("twoTurnCommittedSubjectsEquivalent(nil, nil) = false, want true (both empty)")
	}
}

// TestTwoTurnInferredClassification is CHAOS-4039 v5's own regression test
// (team-lead ruling 2026-08-22): a paired baseline/hinted call that reached
// the SAME engine-deterministic decision outcome and committed the SAME
// subject set MUST classify baseline_equivalent -- the exact property v4's
// canonicalInterpretationHash/normalizedDecisionFingerprint pairing proof
// failed to deliver on a live run (all 4 committed-identical unjustified
// rows, CHAOS-4039/CHAOS-4062 diagnosis).
func TestTwoTurnInferredClassification(t *testing.T) {
	t.Parallel()
	repository := []contractsv1.ContextFabricSubjectRef{{Kind: "repository", CanonicalID: "repository:acme/widgets", Label: "widgets"}}
	repositoryDifferentLabel := []contractsv1.ContextFabricSubjectRef{{Kind: "repository", CanonicalID: "repository:acme/widgets", Label: "a different label entirely"}}
	otherRepository := []contractsv1.ContextFabricSubjectRef{{Kind: "repository", CanonicalID: "repository:acme/other"}}

	// The regression case: identical committed-subject SET (Label-only
	// divergence -- exactly what a live model's own per-call Label
	// rendering could vary, and what v4 never even compared) and identical
	// engine decision outcome -> baseline_equivalent, unconditionally
	// (baseline_equivalent takes priority over kindAttested in the switch).
	if got := twoTurnInferredClassification(repository, repositoryDifferentLabel, "committed", "committed", false); got != "baseline_equivalent" {
		t.Errorf(`twoTurnInferredClassification = %q, want "baseline_equivalent" for an identical committed-subject set (Label-only divergence) and matching decision outcome`, got)
	}
	if got := twoTurnInferredClassification(repository, repositoryDifferentLabel, "committed", "committed", true); got != "baseline_equivalent" {
		t.Errorf(`twoTurnInferredClassification = %q, want "baseline_equivalent" even when kindAttested is also true -- baseline_equivalent is checked first`, got)
	}

	// Different committed subject: kindAttested breaks the tie between
	// kind_insensitivity_attested and unjustified.
	if got := twoTurnInferredClassification(repository, otherRepository, "committed", "committed", true); got != "kind_insensitivity_attested" {
		t.Errorf(`twoTurnInferredClassification = %q, want "kind_insensitivity_attested" for a differing committed subject with kindAttested=true`, got)
	}
	if got := twoTurnInferredClassification(repository, otherRepository, "committed", "committed", false); got != "unjustified" {
		t.Errorf(`twoTurnInferredClassification = %q, want "unjustified" for a differing committed subject with kindAttested=false`, got)
	}

	// Same committed subject but a DIFFERENT engine decision outcome (e.g.
	// the hinted call committed via a differently-gated resolution path
	// than the baseline reached) is not equivalent -- Kind+CanonicalID
	// equality alone is not sufficient, only checking it would silently
	// re-admit the pre-v5 gap in the opposite direction.
	if got := twoTurnInferredClassification(repository, repository, "committed", "ambiguous", false); got != "unjustified" {
		t.Errorf(`twoTurnInferredClassification = %q, want "unjustified" when the paired decision outcomes differ despite an identical committed subject`, got)
	}
}

// TestTwoTurnDecisionCommittedSubjects (CHAOS-4139) pins the extraction:
// nil for any non-"committed" outcome (regardless of what garbage a
// zero-value Subject might otherwise carry), a single-element slice
// carrying exactly the decision's own Subject when committed.
func TestTwoTurnDecisionCommittedSubjects(t *testing.T) {
	t.Parallel()
	subject := contractsv1.ContextFabricSubjectRef{Kind: "work_item", CanonicalID: "work_item:linear:CHAOS-992"}
	for _, outcome := range []string{"ambiguous", "no_commit", ""} {
		t.Run("not committed: "+outcome, func(t *testing.T) {
			decision := graphrank.ResolutionTraceEvent{Stage: "decision", Outcome: outcome, Subject: subject}
			if got := twoTurnDecisionCommittedSubjects(decision); got != nil {
				t.Errorf("twoTurnDecisionCommittedSubjects(Outcome=%q) = %+v, want nil", outcome, got)
			}
		})
	}
	t.Run("committed", func(t *testing.T) {
		decision := graphrank.ResolutionTraceEvent{Stage: "decision", Outcome: "committed", Subject: subject}
		got := twoTurnDecisionCommittedSubjects(decision)
		if len(got) != 1 || got[0] != subject {
			t.Errorf("twoTurnDecisionCommittedSubjects(committed) = %+v, want [%+v]", got, subject)
		}
	})
}

// TestTwoTurnCommitAffirmationState (CHAOS-4139) pins all four states
// against the ACTUAL constant CHAOS-4085's gate uses for basis
// (contextfabric.CommitBasisStatistical/CommitBasisCallerCanonicalID/
// CommitBasisAuthoritativeIdentity/CommitBasisUnknown) and, per codex xhigh
// review round 2 (MEDIUM, confirmed), against a result.SubjectResolution
// shaped exactly as applyCommitAffirmation itself leaves it -- present in
// Committed for "affirmed", absent for "retracted" -- never against
// result.Limitations, which is model-authored free text
// (SynthesisDraft.Limitations, model_runtime.go) a synthesis call could in
// principle reproduce independent of whether this gate ever fired.
func TestTwoTurnCommitAffirmationState(t *testing.T) {
	t.Parallel()
	subject := contractsv1.ContextFabricSubjectRef{Kind: "work_item", CanonicalID: "work_item:linear:CHAOS-992"}
	otherSubject := contractsv1.ContextFabricSubjectRef{Kind: "repository", CanonicalID: "repository:github:full-chaos/other"}
	committedStatistical := graphrank.ResolutionTraceEvent{
		Stage: "decision", Outcome: "committed", Subject: subject,
		CommitBasis: string(contextfabric.CommitBasisStatistical),
	}
	committedIdentityProven := graphrank.ResolutionTraceEvent{
		Stage: "decision", Outcome: "committed", Subject: subject,
		CommitBasis: string(contextfabric.CommitBasisCallerCanonicalID),
	}
	committedAuthoritativeIdentity := graphrank.ResolutionTraceEvent{
		Stage: "decision", Outcome: "committed", Subject: subject,
		CommitBasis: string(contextfabric.CommitBasisAuthoritativeIdentity),
	}
	// committedUnknownBasis (codex xhigh review round 1, MEDIUM, confirmed):
	// CommitBasisUnknown is the zero value ("") -- a GraphReader or test
	// double that never populates CommitBasis lands here for every
	// committed subject by construction. Its own doc comment
	// (chaos4085_commit_basis.go) is explicit: FAIL-CLOSED, "treated
	// exactly like CommitBasisStatistical by every consumer", NOT
	// IdentityProven. A CommitBasis field left zero-valued on a
	// ResolutionTraceEvent (e.g. an older or partially-wired GraphReader)
	// must still be subject to this gate, never silently read as exempt.
	committedUnknownBasis := graphrank.ResolutionTraceEvent{
		Stage: "decision", Outcome: "committed", Subject: subject,
		CommitBasis: string(contextfabric.CommitBasisUnknown),
	}
	notCommitted := graphrank.ResolutionTraceEvent{Stage: "decision", Outcome: "ambiguous", Subject: subject}

	if got := twoTurnCommitAffirmationState(notCommitted, contractsv1.ContextFabricInvestigationResult{}); got != "" {
		t.Errorf(`twoTurnCommitAffirmationState(not committed) = %q, want ""`, got)
	}
	if got := twoTurnCommitAffirmationState(committedIdentityProven, contractsv1.ContextFabricInvestigationResult{}); got != "exempt" {
		t.Errorf(`twoTurnCommitAffirmationState(IdentityProven basis: caller_canonical_id) = %q, want "exempt"`, got)
	}
	if got := twoTurnCommitAffirmationState(committedAuthoritativeIdentity, contractsv1.ContextFabricInvestigationResult{}); got != "exempt" {
		t.Errorf(`twoTurnCommitAffirmationState(IdentityProven basis: authoritative_identity) = %q, want "exempt"`, got)
	}
	// "affirmed": decision.Subject is still present in
	// result.SubjectResolution.Committed, exactly as applyCommitAffirmation
	// leaves an un-retracted subject.
	stillCommitted := contractsv1.ContextFabricInvestigationResult{
		SubjectResolution: contractsv1.ContextFabricSubjectResolution{Committed: []contractsv1.ContextFabricSubjectRef{subject}},
	}
	if got := twoTurnCommitAffirmationState(committedUnknownBasis, stillCommitted); got != "affirmed" {
		t.Errorf(`twoTurnCommitAffirmationState(CommitBasisUnknown, still committed) = %q, want "affirmed" (Unknown is NOT exempt)`, got)
	}
	if got := twoTurnCommitAffirmationState(committedStatistical, stillCommitted); got != "affirmed" {
		t.Errorf(`twoTurnCommitAffirmationState(statistical, still committed) = %q, want "affirmed"`, got)
	}
	// "retracted": decision.Subject is ABSENT from
	// result.SubjectResolution.Committed -- either because Committed is
	// empty (the subject was the only candidate and got fully retracted)
	// or because Committed holds only some OTHER subject.
	if got := twoTurnCommitAffirmationState(committedUnknownBasis, contractsv1.ContextFabricInvestigationResult{}); got != "retracted" {
		t.Errorf(`twoTurnCommitAffirmationState(CommitBasisUnknown, absent from Committed) = %q, want "retracted"`, got)
	}
	otherCommitted := contractsv1.ContextFabricInvestigationResult{
		SubjectResolution: contractsv1.ContextFabricSubjectResolution{Committed: []contractsv1.ContextFabricSubjectRef{otherSubject}},
	}
	if got := twoTurnCommitAffirmationState(committedStatistical, otherCommitted); got != "retracted" {
		t.Errorf(`twoTurnCommitAffirmationState(statistical, a DIFFERENT subject committed) = %q, want "retracted"`, got)
	}
	// Coverage.Partial=true and a model-authored Limitations entry that
	// happens to equal the retraction disclosure string must NOT, on
	// their own, flip an otherwise-still-committed subject to "retracted"
	// -- both are exactly the false-positive shapes this function's own
	// doc comment explains avoiding (Partial is shared with an unrelated
	// retrieval-degradation path; Limitations is model-authored and could
	// coincidentally match).
	falsePositiveMarkers := contractsv1.ContextFabricInvestigationResult{
		Coverage:          contractsv1.ContextFabricCoverage{Partial: true},
		Limitations:       []string{contractsv1.ContextFabricCommitRetractionLimitation},
		SubjectResolution: contractsv1.ContextFabricSubjectResolution{Committed: []contractsv1.ContextFabricSubjectRef{subject}},
	}
	if got := twoTurnCommitAffirmationState(committedStatistical, falsePositiveMarkers); got != "affirmed" {
		t.Errorf(`twoTurnCommitAffirmationState(statistical, still committed despite Partial+matching Limitations text) = %q, want "affirmed" (neither marker is this gate's own evidence)`, got)
	}
}

// TestTwoTurnFinalDecisionEventsMultiSubjectCommit (CHAOS-4096) pins the
// generalization finalDecisionEvents needs now that a single resolution can
// emit MORE than one "decision" event: one per DIFFERENT subject (a
// multi-subject commit) must all survive, and the real stalled-then-
// re-decided shape (a SUBJECTLESS first event, a real-Subject second one --
// finalDecisionEvents' own doc comment on why this does NOT collapse, and
// why every existing consumer is unaffected regardless) must produce both
// events with the last one representative.
func TestTwoTurnFinalDecisionEventsMultiSubjectCommit(t *testing.T) {
	t.Parallel()
	subjectA := contractsv1.ContextFabricSubjectRef{Kind: "work_item", CanonicalID: "work_item:linear:CHAOS-1"}
	subjectB := contractsv1.ContextFabricSubjectRef{Kind: "work_item", CanonicalID: "work_item:linear:CHAOS-2"}

	t.Run("different subjects all survive", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "decision", Outcome: "committed", Subject: subjectA, CommitGate: "pre_committed_exact_hint"},
			{Stage: "decision", Outcome: "committed", Subject: subjectB, CommitGate: "pre_committed_exact_hint"},
		}}
		got := trace.finalDecisionEvents()
		if len(got) != 2 {
			t.Fatalf("finalDecisionEvents() = %+v, want 2 events (one per distinct subject)", got)
		}
		if got[0].Subject != subjectA || got[1].Subject != subjectB {
			t.Fatalf("finalDecisionEvents() = %+v, want [subjectA, subjectB] in first-seen order", got)
		}
	})

	t.Run("real stalled-then-census-enriched shape: both events survive, last is representative", func(t *testing.T) {
		// codex R1 (Low, confirmed): the REAL production shape
		// (TestResolveSubjects_EvidenceCensusCommitsAStalledCandidate,
		// graphrank) has a SUBJECTLESS first event -- resolution.go's
		// ambiguous/no_commit cases never set Subject -- so it does NOT
		// share a key with the second, real-Subject committed event. Both
		// survive here; what matters is that every existing consumer reads
		// the LAST element (or unions only "committed"-outcome events),
		// which still correctly picks the census-enriched decision either
		// way -- see finalDecisionEvents' own doc comment.
		stalled := graphrank.ResolutionTraceEvent{Stage: "decision", Outcome: "ambiguous"}
		censusEnriched := graphrank.ResolutionTraceEvent{Stage: "decision", Outcome: "committed", Subject: subjectA, CommitGate: "evidence_census"}
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{stalled, censusEnriched}}
		got := trace.finalDecisionEvents()
		if len(got) != 2 || !reflect.DeepEqual(got[0], stalled) || !reflect.DeepEqual(got[1], censusEnriched) {
			t.Fatalf("finalDecisionEvents() = %+v, want [stalled, censusEnriched] -- different keys, both retained", got)
		}
		if got := twoTurnLegOutcome(trace.finalDecisionEvents()); got != "committed" {
			t.Errorf(`twoTurnLegOutcome() = %q, want "committed" (the last element, the actually-served decision)`, got)
		}
		if got := twoTurnUnionCommittedSubjects(trace.finalDecisionEvents()); len(got) != 1 || got[0] != subjectA {
			t.Errorf("twoTurnUnionCommittedSubjects() = %+v, want [subjectA] (the subjectless stalled event contributes nothing)", got)
		}
	})

	t.Run("two subjectless events in a row still collapse to the last", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "decision", Outcome: "ambiguous"},
			{Stage: "decision", Outcome: "no_commit"},
		}}
		got := trace.finalDecisionEvents()
		if len(got) != 1 || got[0].Outcome != "no_commit" {
			t.Fatalf("finalDecisionEvents() = %+v, want exactly one event (the last, no_commit) -- both share the zero-value subject key", got)
		}
	})

	t.Run("no decision event captured returns empty", func(t *testing.T) {
		trace := &twoTurnTraceCapture{}
		if got := trace.finalDecisionEvents(); len(got) != 0 {
			t.Fatalf("finalDecisionEvents() = %+v, want empty", got)
		}
	})
}

// TestTwoTurnUnionCommittedSubjects (CHAOS-4096) pins the plural
// generalization of twoTurnDecisionCommittedSubjects: the union of every
// committed event's own Subject, in event order, empty for a leg with no
// committed event.
func TestTwoTurnUnionCommittedSubjects(t *testing.T) {
	t.Parallel()
	subjectA := contractsv1.ContextFabricSubjectRef{Kind: "work_item", CanonicalID: "work_item:linear:CHAOS-1"}
	subjectB := contractsv1.ContextFabricSubjectRef{Kind: "work_item", CanonicalID: "work_item:linear:CHAOS-2"}

	got := twoTurnUnionCommittedSubjects([]graphrank.ResolutionTraceEvent{
		{Stage: "decision", Outcome: "committed", Subject: subjectA},
		{Stage: "decision", Outcome: "committed", Subject: subjectB},
	})
	if len(got) != 2 || got[0] != subjectA || got[1] != subjectB {
		t.Fatalf("twoTurnUnionCommittedSubjects = %+v, want [subjectA, subjectB]", got)
	}

	if got := twoTurnUnionCommittedSubjects([]graphrank.ResolutionTraceEvent{{Stage: "decision", Outcome: "ambiguous"}}); got != nil {
		t.Fatalf("twoTurnUnionCommittedSubjects(ambiguous only) = %+v, want nil", got)
	}
	if got := twoTurnUnionCommittedSubjects(nil); got != nil {
		t.Fatalf("twoTurnUnionCommittedSubjects(nil) = %+v, want nil", got)
	}
}

// TestTwoTurnLegOutcome (CHAOS-4096) pins the empty-leg/representative-last
// contract twoTurnLegOutcome replaces bare Outcome field reads with.
func TestTwoTurnLegOutcome(t *testing.T) {
	t.Parallel()
	if got := twoTurnLegOutcome(nil); got != "" {
		t.Errorf(`twoTurnLegOutcome(nil) = %q, want ""`, got)
	}
	events := []graphrank.ResolutionTraceEvent{
		{Stage: "decision", Outcome: "committed", Subject: contractsv1.ContextFabricSubjectRef{Kind: "work_item", CanonicalID: "a"}},
		{Stage: "decision", Outcome: "committed", Subject: contractsv1.ContextFabricSubjectRef{Kind: "work_item", CanonicalID: "b"}},
	}
	if got := twoTurnLegOutcome(events); got != "committed" {
		t.Errorf(`twoTurnLegOutcome(multi-subject committed) = %q, want "committed"`, got)
	}
}

// TestTwoTurnLegCommitAffirmation (CHAOS-4096) pins the severity reduction:
// retracted outranks affirmed outranks exempt outranks "", so a
// multi-subject commit's single reported affirmation state is never quieter
// than its worst individual subject.
func TestTwoTurnLegCommitAffirmation(t *testing.T) {
	t.Parallel()
	subjectA := contractsv1.ContextFabricSubjectRef{Kind: "work_item", CanonicalID: "work_item:linear:CHAOS-1"}
	subjectB := contractsv1.ContextFabricSubjectRef{Kind: "work_item", CanonicalID: "work_item:linear:CHAOS-2"}
	exemptEvent := graphrank.ResolutionTraceEvent{Stage: "decision", Outcome: "committed", Subject: subjectA, CommitBasis: string(contextfabric.CommitBasisCallerCanonicalID)}
	affirmedResult := contractsv1.ContextFabricInvestigationResult{SubjectResolution: contractsv1.ContextFabricSubjectResolution{Committed: []contractsv1.ContextFabricSubjectRef{subjectB}}}
	affirmedEvent := graphrank.ResolutionTraceEvent{Stage: "decision", Outcome: "committed", Subject: subjectB, CommitBasis: string(contextfabric.CommitBasisStatistical)}
	retractedEvent := graphrank.ResolutionTraceEvent{Stage: "decision", Outcome: "committed", Subject: subjectA, CommitBasis: string(contextfabric.CommitBasisStatistical)}

	if got := twoTurnLegCommitAffirmation(nil, contractsv1.ContextFabricInvestigationResult{}); got != "" {
		t.Errorf(`twoTurnLegCommitAffirmation(nil) = %q, want ""`, got)
	}
	if got := twoTurnLegCommitAffirmation([]graphrank.ResolutionTraceEvent{exemptEvent}, contractsv1.ContextFabricInvestigationResult{}); got != "exempt" {
		t.Errorf(`twoTurnLegCommitAffirmation(exempt alone) = %q, want "exempt"`, got)
	}
	if got := twoTurnLegCommitAffirmation([]graphrank.ResolutionTraceEvent{exemptEvent, affirmedEvent}, affirmedResult); got != "affirmed" {
		t.Errorf(`twoTurnLegCommitAffirmation(exempt+affirmed) = %q, want "affirmed" (affirmed outranks exempt)`, got)
	}
	// retractedEvent's own subjectA is ABSENT from affirmedResult's Committed
	// (only subjectB is there), so it reads "retracted" -- mixed with
	// affirmedEvent (subjectB, present), the worse verdict must win.
	if got := twoTurnLegCommitAffirmation([]graphrank.ResolutionTraceEvent{affirmedEvent, retractedEvent}, affirmedResult); got != "retracted" {
		t.Errorf(`twoTurnLegCommitAffirmation(affirmed+retracted) = %q, want "retracted" (worst case wins)`, got)
	}
}

// TestTwoTurnCaseResultIsAnomalous (CHAOS-4135) pins every bar
// twoTurnCaseResultIsAnomalous mirrors against the final gate loop
// (TestChaos3742TwoTurnConfirmationReplay) -- one true case per zero-
// tolerance bar, plus an ordinary row for each that must read false, so a
// future gate added to that loop without a matching case here is visible as
// a gap in this table, not a silent miss.
func TestTwoTurnCaseResultIsAnomalous(t *testing.T) {
	t.Parallel()
	ordinary := twoTurnCaseResult{Index: 1, Member: "expected_kind", Arm: string(twoTurnArmPositive), CommittedCount: 1}
	if twoTurnCaseResultIsAnomalous(ordinary) {
		t.Errorf("ordinary row reported anomalous: %+v", ordinary)
	}

	tests := []struct {
		name string
		res  twoTurnCaseResult
	}{
		{"wrong_commit", twoTurnCaseResult{WrongCommit: true}},
		{"false_no_match", twoTurnCaseResult{FalseNoMatch: true}},
		{"pair_invalid", twoTurnCaseResult{PairInvalid: true}},
		{"inferred_unjustified", twoTurnCaseResult{InferredClassification: "unjustified"}},
		{
			"synthesis_status_override_uncommitted",
			twoTurnCaseResult{
				SynthesisStatusOverrideFired:  true,
				SynthesisStatusOverrideReason: string(contextfabric.SynthesisStatusOverrideClarificationUnavailableUncommitted),
			},
		},
		{
			"window_any_commit",
			twoTurnCaseResult{
				Arm: string(twoTurnArmInferredTier), Member: string(contractsv1.ContextFabricStructureNeedWindow),
				CommittedCount: 1,
			},
		},
		{
			"tier_routing_failure",
			twoTurnCaseResult{Arm: string(twoTurnArmInferredTier), Member: "expected_kind", TierRoutedCorrectly: false},
		},
		{
			"window_gate_signature_mismatch_wrong_status",
			twoTurnCaseResult{
				Arm: string(twoTurnArmInferredTier), Member: string(contractsv1.ContextFabricStructureNeedWindow),
				Turn2Status: "no_match", TierRoutedCorrectly: true, CommittedCount: 0,
			},
		},
		{"mutation_probe_ran_not_tripped", twoTurnCaseResult{MutationProbe: "remove_confirmation", MutationTripped: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !twoTurnCaseResultIsAnomalous(tt.res) {
				t.Errorf("twoTurnCaseResultIsAnomalous(%+v) = false, want true", tt.res)
			}
		})
	}

	// Narrowness checks: each bar's OWN non-triggering shape must not false-
	// positive on an unrelated field.
	notAnomalous := []struct {
		name string
		res  twoTurnCaseResult
	}{
		{"kind_insensitivity_attested is a justified outcome", twoTurnCaseResult{InferredClassification: "kind_insensitivity_attested"}},
		{"baseline_equivalent is a justified outcome", twoTurnCaseResult{InferredClassification: "baseline_equivalent"}},
		{"synthesis override fired but ordinary reason", twoTurnCaseResult{SynthesisStatusOverrideFired: true, SynthesisStatusOverrideReason: "clarification_unavailable"}},
		{"window arm errored before any commit could happen", twoTurnCaseResult{
			Arm: string(twoTurnArmInferredTier), Member: string(contractsv1.ContextFabricStructureNeedWindow),
			ArmInvalidReason: "investigate error", CommittedCount: 0,
		}},
		{"non-window inferred_tier commit is not the window bar", twoTurnCaseResult{
			Arm: string(twoTurnArmInferredTier), Member: "expected_kind", CommittedCount: 1, TierRoutedCorrectly: true,
		}},
		{"tier routing failure on a non-inferred_tier arm does not apply", twoTurnCaseResult{
			Arm: string(twoTurnArmPositive), Member: "expected_kind", TierRoutedCorrectly: false,
		}},
		{"window row satisfying the full CHAOS-4040 gate signature", twoTurnCaseResult{
			Arm: string(twoTurnArmInferredTier), Member: string(contractsv1.ContextFabricStructureNeedWindow),
			Turn2Status: string(contractsv1.ContextFabricInvestigationClarificationRequired), TierRoutedCorrectly: true, CommittedCount: 0,
		}},
		{"mutation probe ran and tripped", twoTurnCaseResult{MutationProbe: "remove_confirmation", MutationTripped: true}},
	}
	for _, tt := range notAnomalous {
		t.Run(tt.name, func(t *testing.T) {
			if twoTurnCaseResultIsAnomalous(tt.res) {
				t.Errorf("twoTurnCaseResultIsAnomalous(%+v) = true, want false", tt.res)
			}
		})
	}
}

// TestTwoTurnRedactNonAnomalousTraceEvents (CHAOS-4135) is the redaction
// pass's own pin: an anomalous row keeps both trace fields, an ordinary row
// loses both, in place, over a mixed slice -- proving the pass does not
// simply clear (or simply keep) everything regardless of content.
func TestTwoTurnRedactNonAnomalousTraceEvents(t *testing.T) {
	t.Parallel()
	events := []graphrank.ResolutionTraceEvent{{Stage: "decision", Outcome: "committed"}}
	results := []twoTurnCaseResult{
		{Index: 1, WrongCommit: true, TraceEvents: events, BaselineTraceEvents: events,
			CensusRan: true, EvidenceRoundEntered: true, EvidenceRoundReason: string(graphrank.ReasonNoDiscriminators),
			KindOfferDistinctKindCount: 1, KindOfferSuppressedByCardinality: true,
			CandidateOfferCount: 5, OfferKind: "candidate", ExpectedKindAtOfferBoundary: true},
		{Index: 2, TraceEvents: events, BaselineTraceEvents: events,
			CensusRan: true, EvidenceRoundEntered: true, EvidenceRoundReason: string(graphrank.ReasonNoDiscriminators),
			KindOfferDistinctKindCount: 1, KindOfferSuppressedByCardinality: true,
			CandidateOfferCount: 5, OfferKind: "candidate", ExpectedKindAtOfferBoundary: true},
	}
	twoTurnRedactNonAnomalousTraceEvents(results)

	if len(results[0].TraceEvents) == 0 || len(results[0].BaselineTraceEvents) == 0 {
		t.Errorf("anomalous row (index 1) had its trace events redacted: %+v", results[0])
	}
	if results[1].TraceEvents != nil || results[1].BaselineTraceEvents != nil {
		t.Errorf("ordinary row (index 2) kept its trace events: %+v", results[1])
	}
	// CHAOS-4161: redaction only clears TraceEvents/BaselineTraceEvents --
	// CensusRan/EvidenceRoundEntered/EvidenceRoundReason are separate scalar
	// summary fields, computed once (pre-redaction) from the in-process
	// trace capture, and must survive on BOTH rows regardless of anomaly
	// status. A regression here would silently reintroduce, for the
	// (overwhelmingly common) non-anomalous rows, exactly the
	// never-entered-vs-entered-but-refused ambiguity these fields exist to
	// resolve.
	for _, res := range results {
		if !res.CensusRan {
			t.Errorf("row %d: CensusRan redacted to false, want true (redaction must not touch summary scalars)", res.Index)
		}
		if !res.EvidenceRoundEntered {
			t.Errorf("row %d: EvidenceRoundEntered redacted to false, want true", res.Index)
		}
		if res.EvidenceRoundReason != string(graphrank.ReasonNoDiscriminators) {
			t.Errorf("row %d: EvidenceRoundReason redacted to %q, want %q", res.Index, res.EvidenceRoundReason, graphrank.ReasonNoDiscriminators)
		}
		// CHAOS-4012: same survival guarantee for the kind_offer summary
		// scalars.
		if res.KindOfferDistinctKindCount != 1 {
			t.Errorf("row %d: KindOfferDistinctKindCount redacted to %d, want 1", res.Index, res.KindOfferDistinctKindCount)
		}
		if !res.KindOfferSuppressedByCardinality {
			t.Errorf("row %d: KindOfferSuppressedByCardinality redacted to false, want true", res.Index)
		}
		// CHAOS-4012 v22: same survival guarantee for the candidate-list
		// axis's own pair.
		if res.CandidateOfferCount != 5 {
			t.Errorf("row %d: CandidateOfferCount redacted to %d, want 5", res.Index, res.CandidateOfferCount)
		}
		if res.OfferKind != "candidate" {
			t.Errorf("row %d: OfferKind redacted to %q, want %q", res.Index, res.OfferKind, "candidate")
		}
		// CHAOS-4012 v22 (re-smoke follow-up): ExpectedKindAtOfferBoundary is
		// the SAME kind of pre-redaction summary scalar as the fields above.
		if !res.ExpectedKindAtOfferBoundary {
			t.Errorf("row %d: ExpectedKindAtOfferBoundary redacted to false, want true", res.Index)
		}
	}
}

// TestWindowBoundsAgree covers windowBoundsAgree's own doc comment cases
// (team-lead verification request, 2026-08-21): all_time (nil/nil) must
// agree with ZERO tolerance -- checked here by passing tolerance=0, so a
// pass proves the all_time path never depends on the drift bound at all,
// only bounded bands do.
func TestWindowBoundsAgree(t *testing.T) {
	t.Parallel()
	confirmed := contractsv1.ContextFabricWindowClarificationConfirmed
	allTime := contractsv1.ContextFabricInvestigationResult{
		EffectiveEvidenceWindow: &contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: "all_time", Provenance: confirmed},
	}
	t2 := time.Unix(2000, 0)
	t2Plus5m := t2.Add(5 * time.Minute)
	t2Plus2h := t2.Add(2 * time.Hour)
	bounded := contractsv1.ContextFabricInvestigationResult{
		EffectiveEvidenceWindow: &contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: "trailing_90d", End: &t2, Provenance: confirmed},
	}
	boundedDriftedWithin := contractsv1.ContextFabricInvestigationResult{
		EffectiveEvidenceWindow: &contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: "trailing_90d", End: &t2Plus5m, Provenance: confirmed},
	}
	boundedDriftedBeyond := contractsv1.ContextFabricInvestigationResult{
		EffectiveEvidenceWindow: &contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: "trailing_90d", End: &t2Plus2h, Provenance: confirmed},
	}

	if !windowBoundsAgree(allTime, allTime, 0) {
		t.Error("windowBoundsAgree(all_time, all_time, tolerance=0) = false, want true -- nil/nil must agree exactly, never via the drift tolerance")
	}
	if windowBoundsAgree(allTime, bounded, 24*time.Hour) {
		t.Error("windowBoundsAgree(all_time, bounded) = true, want false -- one nil End and one non-nil is a genuinely different commitment, not a drift question")
	}
	if !windowBoundsAgree(bounded, boundedDriftedWithin, 10*time.Minute) {
		t.Error("windowBoundsAgree(bounded, bounded+5m, tolerance=10m) = false, want true -- within tolerance")
	}
	if windowBoundsAgree(bounded, boundedDriftedBeyond, 10*time.Minute) {
		t.Error("windowBoundsAgree(bounded, bounded+2h, tolerance=10m) = true, want false -- beyond tolerance")
	}
	noWindow := contractsv1.ContextFabricInvestigationResult{}
	if windowBoundsAgree(noWindow, allTime, 24*time.Hour) {
		t.Error("windowBoundsAgree(no EffectiveEvidenceWindow, all_time) = true, want false")
	}
}

func TestWindowConfirmedAsBand(t *testing.T) {
	t.Parallel()
	confirmed := contractsv1.ContextFabricInvestigationResult{
		EffectiveEvidenceWindow: &contractsv1.ContextFabricEffectiveEvidenceWindow{
			RelativeID: "all_time", Provenance: contractsv1.ContextFabricWindowClarificationConfirmed,
		},
	}
	if !windowConfirmedAsBand(confirmed, "all_time") {
		t.Error("windowConfirmedAsBand(confirmed all_time, \"all_time\") = false, want true")
	}
	if windowConfirmedAsBand(confirmed, "trailing_90d") {
		t.Error("windowConfirmedAsBand(confirmed all_time, \"trailing_90d\") = true, want false -- wrong band")
	}
	inferred := contractsv1.ContextFabricInvestigationResult{
		EffectiveEvidenceWindow: &contractsv1.ContextFabricEffectiveEvidenceWindow{
			RelativeID: "all_time", Provenance: contractsv1.ContextFabricWindowInferredDefault,
		},
	}
	if windowConfirmedAsBand(inferred, "all_time") {
		t.Error("windowConfirmedAsBand(inferred_default all_time, \"all_time\") = true, want false -- not receipt-confirmed")
	}
	if windowConfirmedAsBand(contractsv1.ContextFabricInvestigationResult{}, "all_time") {
		t.Error("windowConfirmedAsBand(no EffectiveEvidenceWindow, \"all_time\") = true, want false")
	}
}

// TestTwoTurnTraceCapture_KindInsensitivityResult pins the CHAOS-4039
// trace-reading contract: ShadowKindInsensitivityEvaluated gates whether
// the outcome is reported at all, mirroring resolve.go's own zero-value
// convention (an unevaluated proof reports outcome="" via the zero string,
// never a stale/misleading prior value).
func TestTwoTurnTraceCapture_KindInsensitivityResult(t *testing.T) {
	t.Parallel()
	t.Run("not evaluated", func(t *testing.T) {
		c := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "evidence_round", ShadowOutcome: "would_commit"},
		}}
		evaluated, outcome, mode := c.kindInsensitivityResult()
		if evaluated || outcome != "" || mode != "" {
			t.Errorf("kindInsensitivityResult() = (%v, %q, %q), want (false, \"\", \"\") when ShadowKindInsensitivityEvaluated was never set", evaluated, outcome, mode)
		}
	})
	t.Run("evaluated commit_sound narrowed", func(t *testing.T) {
		c := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "evidence_round", ShadowOutcome: "would_commit", ShadowKindInsensitivityEvaluated: true, ShadowKindInsensitivityOutcome: "commit_sound", ShadowKindInsensitivityMode: "narrowed"},
		}}
		evaluated, outcome, mode := c.kindInsensitivityResult()
		if !evaluated || outcome != "commit_sound" || mode != "narrowed" {
			t.Errorf("kindInsensitivityResult() = (%v, %q, %q), want (true, \"commit_sound\", \"narrowed\")", evaluated, outcome, mode)
		}
	})
	// CHAOS-4079: the write-free observation mode reaches this same reader,
	// and MUST arrive distinguishable -- a caller that cannot tell it from
	// "narrowed" would treat a census that was never narrowed as proof.
	t.Run("evaluated commit_sound observed_no_overlap", func(t *testing.T) {
		c := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "evidence_round", ShadowOutcome: "would_commit", ShadowKindInsensitivityEvaluated: true, ShadowKindInsensitivityOutcome: "commit_sound", ShadowKindInsensitivityMode: "observed_no_overlap"},
		}}
		evaluated, outcome, mode := c.kindInsensitivityResult()
		if !evaluated || outcome != "commit_sound" || mode != "observed_no_overlap" {
			t.Errorf("kindInsensitivityResult() = (%v, %q, %q), want (true, \"commit_sound\", \"observed_no_overlap\")", evaluated, outcome, mode)
		}
	})
}

// TestTwoTurnKindAttestedRequiresNarrowedMode (CHAOS-4079, team-lead ruling
// 2026-08-22) is THE pass/fail-neutrality proof for this ticket. CHAOS-4079
// makes the shadow kind-insensitivity probe evaluate for a wrong-kind hint
// that narrowed nothing -- previously unreachable, so those rows always
// reported evaluated=false and classified "unjustified". This test pins that
// they classify "unjustified" STILL: a commit_sound verdict earned under an
// "observed_" mode is necessary-but-not-sufficient evidence the hint had no
// influence (the census was never narrowed; the hint still reaches member
// stamping and offer ranking), so it must not promote a row to
// kind_insensitivity_attested and must not weaken this run's own
// InferredUnjustifiedCount==0 pass condition.
//
// If a future edit deletes the mode gate, this test fails -- which is the
// point: the deletion would silently convert a failing trial into a passing
// one.
func TestTwoTurnKindAttestedRequiresNarrowedMode(t *testing.T) {
	t.Parallel()
	hinted := []contractsv1.ContextFabricSubjectRef{{Kind: "repository", CanonicalID: "repository:acme/widgets"}}
	baselineDifferent := []contractsv1.ContextFabricSubjectRef{{Kind: "person", CanonicalID: "person:acme/j.doe"}}
	censusCommit := graphrank.ResolutionTraceEvent{
		Stage: "decision", CommitGate: "evidence_census",
		Subject: contractsv1.ContextFabricSubjectRef{Kind: "repository", CanonicalID: "repository:acme/widgets"},
	}
	round := func(mode string) graphrank.ResolutionTraceEvent {
		return graphrank.ResolutionTraceEvent{
			Stage: "evidence_round", ShadowOutcome: "would_commit",
			ShadowKindInsensitivityEvaluated: true, ShadowKindInsensitivityOutcome: "commit_sound",
			ShadowKindInsensitivityMode: mode,
		}
	}
	for _, tc := range []struct {
		mode           string
		wantAttested   bool
		wantClassified string
	}{
		{mode: "narrowed", wantAttested: true, wantClassified: "kind_insensitivity_attested"},
		{mode: "observed_no_overlap", wantAttested: false, wantClassified: "unjustified"},
		{mode: "observed_subsumed", wantAttested: false, wantClassified: "unjustified"},
		{mode: "", wantAttested: false, wantClassified: "unjustified"},
	} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{round(tc.mode), censusCommit}}
			if got := twoTurnKindAttested(trace, hinted); got != tc.wantAttested {
				t.Fatalf("twoTurnKindAttested(mode=%q) = %v, want %v -- only a genuine narrowing may attest", tc.mode, got, tc.wantAttested)
			}
			// Same committed-subject divergence in every case, so the mode
			// gate is the ONLY thing moving the classification.
			got := twoTurnInferredClassification(hinted, baselineDifferent, "committed", "committed", twoTurnKindAttested(trace, hinted))
			if got != tc.wantClassified {
				t.Errorf("twoTurnInferredClassification(mode=%q) = %q, want %q", tc.mode, got, tc.wantClassified)
			}
		})
	}
	// A sound verdict under "narrowed" still requires the census to have
	// committed THIS row's subject -- CHAOS-4039's other half, unchanged.
	t.Run("narrowed but census committed a different subject", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{round("narrowed"),
			{Stage: "decision", CommitGate: "evidence_census", Subject: contractsv1.ContextFabricSubjectRef{Kind: "repository", CanonicalID: "repository:other"}}}}
		if twoTurnKindAttested(trace, hinted) {
			t.Error("twoTurnKindAttested = true, want false when the evidence_census commit names a different subject")
		}
	})
	if twoTurnKindAttested(nil, hinted) {
		t.Error("twoTurnKindAttested(nil trace) = true, want false")
	}
}

// TestTwoTurnTraceCapture_EvidenceCensusCommitted pins the "attested
// satisfier == committed subject" half of kind_insensitivity_attested: a
// decision-stage CommitGate=="evidence_census" event must name the SAME
// subject the caller independently observed as committed, comparing
// Kind+CanonicalID only (Label is presentation text, never compared).
func TestTwoTurnTraceCapture_EvidenceCensusCommitted(t *testing.T) {
	t.Parallel()
	committed := []contractsv1.ContextFabricSubjectRef{{Kind: "repository", CanonicalID: "repository:acme/widgets"}}
	t.Run("matching subject", func(t *testing.T) {
		c := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "decision", CommitGate: "evidence_census", Subject: contractsv1.ContextFabricSubjectRef{Kind: "repository", CanonicalID: "repository:acme/widgets", Label: "irrelevant"}},
		}}
		if !c.evidenceCensusCommitted(committed) {
			t.Error("evidenceCensusCommitted() = false, want true for a decision event naming the same committed subject")
		}
	})
	t.Run("different subject", func(t *testing.T) {
		c := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "decision", CommitGate: "evidence_census", Subject: contractsv1.ContextFabricSubjectRef{Kind: "repository", CanonicalID: "repository:something_else"}},
		}}
		if c.evidenceCensusCommitted(committed) {
			t.Error("evidenceCensusCommitted() = true, want false when the traced subject does not match the committed one")
		}
	})
	t.Run("wrong commit gate", func(t *testing.T) {
		c := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "decision", CommitGate: "lone_floor", Subject: contractsv1.ContextFabricSubjectRef{Kind: "repository", CanonicalID: "repository:acme/widgets"}},
		}}
		if c.evidenceCensusCommitted(committed) {
			t.Error("evidenceCensusCommitted() = true, want false when CommitGate is not evidence_census -- a generic commit is insufficient (CHAOS-4039)")
		}
	})
}

// TestTwoTurnUnjustifiedShadowProbe (CHAOS-4062) pins twoTurnUnjustifiedShadowProbe
// against fabricated inputs shaped like the cases the "unjustified"
// classification actually covers -- proving the artifact's new
// Shadow*/*CommittedSubjects fields populate correctly without standing up
// the full runTwoTurnInferredTierArm investigator/window-precondition
// machinery.
func TestTwoTurnUnjustifiedShadowProbe(t *testing.T) {
	t.Parallel()
	baselineCommitted := []contractsv1.ContextFabricSubjectRef{
		{Kind: "repository", CanonicalID: "repository:acme/widgets", Label: "widgets (never persisted)"},
	}
	hintedCommitted := []contractsv1.ContextFabricSubjectRef{
		{Kind: "person", CanonicalID: "person:acme/j.doe", Label: "J. Doe (never persisted)"},
	}

	t.Run("proof never consulted", func(t *testing.T) {
		trace := &twoTurnTraceCapture{}
		evaluated, outcome, mode, baselineSubjects, hintedSubjects := twoTurnUnjustifiedShadowProbe(trace, baselineCommitted, hintedCommitted)
		if evaluated || outcome != "" || mode != "" {
			t.Errorf("evaluated,outcome,mode = (%v,%q,%q), want (false,\"\",\"\") when the hinted call's trace never set ShadowKindInsensitivityEvaluated", evaluated, outcome, mode)
		}
		wantBaseline := []twoTurnSubjectKindID{{Kind: "repository", CanonicalID: "repository:acme/widgets"}}
		wantHinted := []twoTurnSubjectKindID{{Kind: "person", CanonicalID: "person:acme/j.doe"}}
		if !reflect.DeepEqual(baselineSubjects, wantBaseline) {
			t.Errorf("baselineSubjects = %+v, want %+v (Kind+CanonicalID only, Label dropped)", baselineSubjects, wantBaseline)
		}
		if !reflect.DeepEqual(hintedSubjects, wantHinted) {
			t.Errorf("hintedSubjects = %+v, want %+v (Kind+CanonicalID only, Label dropped)", hintedSubjects, wantHinted)
		}
	})

	t.Run("proof evaluated but not commit_sound", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "evidence_round", ShadowKindInsensitivityEvaluated: true, ShadowKindInsensitivityOutcome: "would_no_match", ShadowKindInsensitivityMode: "narrowed"},
		}}
		evaluated, outcome, mode, _, _ := twoTurnUnjustifiedShadowProbe(trace, baselineCommitted, hintedCommitted)
		if !evaluated || outcome != "would_no_match" || mode != "narrowed" {
			t.Errorf("evaluated,outcome,mode = (%v,%q,%q), want (true,\"would_no_match\",\"narrowed\") -- genuine kind ambiguity without the hint (CHAOS-4062 reading)", evaluated, outcome, mode)
		}
	})

	// CHAOS-4079: the case the whole ticket exists for -- a wrong-kind hint
	// disjoint from the pool. Before it, this row's trace carried NOTHING
	// (the probe could not evaluate), so the fields the CHAOS-4062 probe was
	// added to report were structurally always empty here.
	t.Run("proof observed write-free under a zero-overlap hint", func(t *testing.T) {
		trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "evidence_round", ShadowKindInsensitivityEvaluated: true, ShadowKindInsensitivityOutcome: "commit_sound", ShadowKindInsensitivityMode: "observed_no_overlap"},
		}}
		evaluated, outcome, mode, _, _ := twoTurnUnjustifiedShadowProbe(trace, baselineCommitted, hintedCommitted)
		if !evaluated || outcome != "commit_sound" || mode != "observed_no_overlap" {
			t.Errorf("evaluated,outcome,mode = (%v,%q,%q), want (true,\"commit_sound\",\"observed_no_overlap\")", evaluated, outcome, mode)
		}
	})

	t.Run("nil trace", func(t *testing.T) {
		evaluated, outcome, mode, baselineSubjects, hintedSubjects := twoTurnUnjustifiedShadowProbe(nil, baselineCommitted, nil)
		if evaluated || outcome != "" || mode != "" {
			t.Errorf("evaluated,outcome,mode = (%v,%q,%q), want (false,\"\",\"\") for a nil trace", evaluated, outcome, mode)
		}
		if len(baselineSubjects) != 1 {
			t.Errorf("baselineSubjects = %+v, want 1 entry", baselineSubjects)
		}
		if hintedSubjects != nil {
			t.Errorf("hintedSubjects = %+v, want nil for an empty committed slice", hintedSubjects)
		}
	})
}

// --- arm runners: real production code (Investigate/Save), driven from the harness ---

// twoTurnRequest builds the base investigation request for a corpus case,
// Consumer.Surface="mcp" so explicit structure fields land at
// inferred_default/explicit_unattributed exactly as a real MCP-headless
// caller's would (design brief §2.0's surface split; the sidecar hardcodes
// this same value for the real MCP transport -- see investigate_question.go).
func twoTurnRequest(index int, tc trialCase, requestIDSuffix string) contractsv1.ContextFabricInvestigationRequest {
	return contractsv1.ContextFabricInvestigationRequest{
		SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema,
		RequestID:     fmt.Sprintf("request_twoturn%06d_%s", index, requestIDSuffix),
		Question:      tc.Question,
		TimeContext:   contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
		Options: contractsv1.ContextFabricInvestigationOptions{
			// MaxSubjectCandidates: 20, not the pre-CHAOS-4117 10 -- this
			// harness IS the truncation-observing mechanism CHAOS-4117's
			// root-cause finding traced (search_truncated=true on 90/90
			// decisive arms at 10). 20 is the measured safe ceiling; see
			// internal/mcp.defaultMaxSubjectCandidates' doc comment.
			MaxSubjectCandidates: 20, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contractsv1.ContextFabricConsumerInfo{Name: "chaos-3742-two-turn", Version: "0.1.0", Surface: "mcp"},
	}
}

// runTwoTurnPositiveArm confirms the oracle-matching offer via receipt and
// reports whether it converted.
func runTwoTurnPositiveArm(t *testing.T, ctx context.Context, investigator contextfabric.Investigator, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, turn1 contractsv1.ContextFabricInvestigationResult, timeout time.Duration, trace *twoTurnTraceCapture, regimeAWindowBand string) twoTurnCaseResult {
	res := twoTurnCaseResult{Index: index, Member: entry.Member, Arm: string(twoTurnArmPositive), Turn1Status: string(turn1.Status)}
	twoTurnStampOutcome(&res, tc, nil)
	if entry.Member == string(contractsv1.ContextFabricStructureNeedWindow) {
		twoTurnAssertWindowSurfacesAgree(t, index, turn1)
	}
	receiptID, found := selectOracleOffer(turn1, entry.Member, entry.positiveQuery())
	if !found {
		res.OfferMiss = true
		return res
	}
	req := twoTurnRequest(index, tc, "positive")
	setTwoTurnReceipt(&req, entry.Member, contractsv1.ContextFabricBoundSubjectReceipt{ResultID: turn1.ResultID, ReceiptID: receiptID})
	if windowReceipt, ok := twoTurnRegimeAWindowReceipt(turn1, entry.Member, regimeAWindowBand); ok {
		req.PriorWindowReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{windowReceipt}
		res.Turn2WindowReceiptAttached = true
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// CHAOS-4086: reset immediately before the call so finalDecisionEvents
	// below can only ever see THIS call's events. The capture is shared
	// across arms (one tracer is installed on the investigator for the
	// whole run), so without the reset a quiet arm would inherit the
	// previous arm's gate.
	if trace != nil {
		trace.reset()
	}
	turn2, err := investigator.Investigate(callCtx, principal, req)
	// CHAOS-4103 (codex review round 3, generalized): folded IMMEDIATELY,
	// before the error return just below, same as every other arm's own
	// call site in this file.
	if trace != nil {
		twoTurnFoldSynthesisStatusOverride(&res, trace.synthesisOverride)
		// CHAOS-4307 (codex round 2, Medium, confirmed): res.TraceEvents is
		// normally set only by twoTurnStampDecision on the success path
		// below -- an Investigate() call can emit a confirmed_kind_scope
		// event during resolution and still fail later at a downstream
		// stage (graph/fact/synthesis), which used to discard that event
		// entirely on this early return, silently undercounting the
		// CHAOS-4307 census rollup. Setting it here too is safe: on the
		// success path, twoTurnStampDecision's own trace.snapshot() call
		// below reassigns the SAME content (trace is not reset in
		// between), so this is never a double-fold at the caller's own
		// foldConfirmedKindVectorCensus(report, positive.TraceEvents) site.
		res.TraceEvents = trace.snapshot()
	}
	if err != nil {
		res.Turn2Status = "error:" + contextFabricRejectionClass(err)
		twoTurnStampArmFailure(&res, "investigate error: "+contextFabricRejectionClass(err), err)
		return res
	}
	res.Turn2Status = string(turn2.Status)
	res.CommittedCount = len(turn2.SubjectResolution.Committed)
	res.CanonicalFactsCount = twoTurnCanonicalFactsCount(turn2.Coverage)
	res.Applied = memberApplied(turn2, entry.Member)
	res.WrongCommit = twoTurnCommittedWrong(turn2.SubjectResolution.Committed, tc)
	res.Reused = turn2.Reused
	// FalseNoMatch (CHAOS-4120): extends CHAOS-4039's gate to the positive
	// arm -- see FalseNoMatch's own doc comment (twoTurnCaseResult) for why
	// a receipt-confirmed no_match here is at least as strong a finding as
	// the inferred-tier arm's own version of this check.
	res.FalseNoMatch = twoTurnPositiveFalseNoMatch(tc.ExpectID, turn2.Status)
	twoTurnStampOutcome(&res, tc, turn2.SubjectResolution.Committed)
	twoTurnStampDecision(&res, trace)
	// Turn2WindowExpandAccepted (CHAOS-4314): true only when THIS turn 2
	// actually redeemed the EXACT receipt turn 1's own window_expand
	// recommendation named -- see twoTurnWindowExpandAccepted's own doc
	// comment for why this is checked against ConfirmedStructure rather
	// than merely "some window receipt was confirmed" (regimeAWindowBand's
	// oracle-selected receipt need not be the SAME tier composeWindowExpandOption
	// recommended).
	res.Turn2WindowExpandAccepted = twoTurnWindowExpandAccepted(turn1, turn2.ConfirmedStructure)
	return res
}

// twoTurnWindowExpandAccepted (CHAOS-4314) reports whether confirmed (a
// turn's own ConfirmedStructure) carries a window-member entry whose
// ReceiptID matches turn1's own StructureNeeds.WindowExpandOptions[0] --
// the fail-closed "accepted" reading: this turn redeemed the EXACT receipt
// the window_expand offer named, not merely any window receipt.
func twoTurnWindowExpandAccepted(turn1 contractsv1.ContextFabricInvestigationResult, confirmed []contractsv1.ContextFabricConfirmedStructureEntry) bool {
	if turn1.StructureNeeds == nil || len(turn1.StructureNeeds.WindowExpandOptions) == 0 {
		return false
	}
	recommended := turn1.StructureNeeds.WindowExpandOptions[0].ReceiptID
	for _, entry := range confirmed {
		// Disposition == Applied is required (codex xhigh review round 3,
		// confirmed Medium finding): a matching Member/ReceiptID alone is
		// not proof of a genuine redemption -- structureSupersessionVetoResult
		// (window.go) places the SAME receipt into ConfirmedStructure with
		// Disposition VetoedStale when a stale-superseded-offer race is
		// discovered at Save, and that entry names the receipt precisely
		// because it is echoing what the race discarded, not what applied.
		// Without this check a stale race would falsely increment
		// turn2_window_expand_accepted.
		if entry.Member == contractsv1.ContextFabricStructureNeedWindow && entry.ReceiptID == recommended &&
			entry.Disposition == contractsv1.ContextFabricStructureDispositionApplied {
			return true
		}
	}
	return false
}

// TestCHAOS4314_TwoTurnWindowExpandAccepted_RejectsStaleSupersededDisposition
// is the codex xhigh review round-3 regression: structureSupersessionVetoResult
// (internal/contextfabric/window.go) can place a ConfirmedStructure entry
// naming the EXACT SAME window_expand-recommended receipt with Disposition
// VetoedStale when a stale-superseded-offer race is discovered at Save --
// that entry echoes what the race DISCARDED, not what applied. Before this
// fix, twoTurnWindowExpandAccepted matched on Member+ReceiptID alone and
// would have falsely counted that race as an accepted window_expand offer.
func TestCHAOS4314_TwoTurnWindowExpandAccepted_RejectsStaleSupersededDisposition(t *testing.T) {
	turn1 := contractsv1.ContextFabricInvestigationResult{
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			WindowExpandOptions: []contractsv1.ContextFabricWindowExpandOption{
				{ReceiptID: "winr_recommended90daaaaa", OptionID: "opt_90d", Label: "the last 90 days"},
			},
		},
	}
	staleConfirmed := []contractsv1.ContextFabricConfirmedStructureEntry{
		{
			Member: contractsv1.ContextFabricStructureNeedWindow, ReceiptID: "winr_recommended90daaaaa",
			AppliedValue: "trailing_90d", Source: contractsv1.ContextFabricStructureSourceReceipt,
			Provenance: contractsv1.ContextFabricStructureClarificationConfirmed,
			// The load-bearing field: a stale-superseded race, NOT a genuine
			// redemption.
			Disposition: contractsv1.ContextFabricStructureDispositionVetoedStale,
		},
	}
	if twoTurnWindowExpandAccepted(turn1, staleConfirmed) {
		t.Fatal("twoTurnWindowExpandAccepted = true, want false: a vetoed_stale entry is a discarded race, not an accepted redemption")
	}

	appliedConfirmed := []contractsv1.ContextFabricConfirmedStructureEntry{
		{
			Member: contractsv1.ContextFabricStructureNeedWindow, ReceiptID: "winr_recommended90daaaaa",
			AppliedValue: "trailing_90d", Source: contractsv1.ContextFabricStructureSourceReceipt,
			Provenance: contractsv1.ContextFabricStructureClarificationConfirmed,
			// Control: the identical entry, genuinely applied, must still
			// report accepted -- this fix must not regress the happy path
			// TestCHAOS4314_Redemption_WindowExpandReceiptRecordsAccepted
			// (internal/contextfabric) already proves at the engine layer.
			Disposition: contractsv1.ContextFabricStructureDispositionApplied,
		},
	}
	if !twoTurnWindowExpandAccepted(turn1, appliedConfirmed) {
		t.Fatal("twoTurnWindowExpandAccepted = false, want true: a genuinely applied entry naming the recommended receipt must count as accepted")
	}
}

// twoTurnPositiveFalseNoMatch is CHAOS-4120's own positive-arm false_no_match
// predicate, extracted as a pure function (mirrors twoTurnCommittedWrong/
// twoTurnMutationProbe's own precedent) so it has a direct unit-test surface
// independent of runTwoTurnPositiveArm's full investigator plumbing. True
// when a case with a real expected answer (expectID != "") resolved to the
// literal no_match terminal -- reachable here ONLY past the OfferMiss early
// return above, i.e. only when the engine held a receipt-confirmed offer
// and still failed to convert it.
func twoTurnPositiveFalseNoMatch(expectID string, status contractsv1.ContextFabricInvestigationStatus) bool {
	return expectID != "" && status == contractsv1.ContextFabricInvestigationNoMatch
}

// twoTurnCanonicalFactsCount (CHAOS-4347, team-lead standing order: telemetry
// baked into new logic, same change) counts the FACT-BEARING canonical-fact
// coverage sources on a result -- the coverage bar this harness never had.
// Deliberately a count of DISTINCT canonical_fact:* SOURCES (fact KINDS) the
// synthesis step could have cited, not a literal row count of
// CanonicalFactBundle.Facts (which the wire InvestigationResult does not
// expose at all -- Coverage.Sources is the only post-hoc signal available
// without a new engine-side capture channel/contract field, and "was there
// at least one fact of this kind" is exactly the distinction CHAOS-4344's
// own case 23 (repository status: canonical_facts_count=0, coverage showed
// nothing but a pruned canonical_fact:status source) needed to be reportable
// at all.
//
// A source is counted only when BOTH its Source is prefixed "canonical_fact:"
// (excluding "context-fabric:graph" and any other non-fact source) AND its
// State is one of the exact three fact-bearing states the engine itself
// recognizes (internal/contextfabric/fact_registry.go's stateRejectsFacts):
// Available, Stale, or Truncated -- pruned/unavailable/unconfigured/
// unauthorized/conflicted/not_applicable sources contributed nothing to what
// the model actually saw, so they must not inflate the count. Truncated is
// deliberately included: fact_registry.go's own comment is explicit that
// truncation means "there are more facts than these", never "these facts
// need less grounding" -- a truncated source is still fact-bearing.
//
// KNOWN IMPRECISION (codex round-1, PR #298, Medium/high-confidence,
// verified and accepted rather than silently left): a provider may legally
// return a fact-bearing state (most commonly Available) with ZERO facts --
// fact_registry.go only requires len(Facts)==0 when the state REJECTS
// facts, never the reverse -- so this count can overcount a source that
// ran and found nothing. Fixing that precisely needs a new wire contract
// field carrying an actual per-kind fact count, which is out of scope for
// this (already schema-bumping) change. This is why the field stays
// OBSERVATIONAL ONLY (see report.FactlessCommittedCount's own doc comment)
// and is not a gate condition.
func twoTurnCanonicalFactsCount(coverage contractsv1.ContextFabricCoverage) int {
	count := 0
	for _, source := range coverage.Sources {
		if !strings.HasPrefix(source.Source, "canonical_fact:") {
			continue
		}
		switch source.State {
		case contractsv1.ContextFabricSourceAvailable, contractsv1.ContextFabricSourceStale, contractsv1.ContextFabricSourceTruncated:
			count++
		}
	}
	return count
}

// TestTwoTurnCanonicalFactsCount pins twoTurnCanonicalFactsCount's own
// prefix+state contract (CHAOS-4347): RED on origin/main before this change,
// since the function did not exist there. In particular, case 23 (repository
// status, pre-composition) is the "pruned canonical_fact:status only" row
// below -- exactly the shape this helper must report as zero.
func TestTwoTurnCanonicalFactsCount(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		coverage contractsv1.ContextFabricCoverage
		want     int
	}{
		{
			name:     "no sources at all",
			coverage: contractsv1.ContextFabricCoverage{},
			want:     0,
		},
		{
			name: "case-23 shape: only a pruned canonical_fact:status source, no facts",
			coverage: contractsv1.ContextFabricCoverage{Sources: []contractsv1.ContextFabricSourceObservation{
				{Source: "canonical_fact:status", State: contractsv1.ContextFabricSourcePruned},
			}},
			want: 0,
		},
		{
			name: "composed set: metrics+health+identity all available counts 3",
			coverage: contractsv1.ContextFabricCoverage{Sources: []contractsv1.ContextFabricSourceObservation{
				{Source: "canonical_fact:metrics", State: contractsv1.ContextFabricSourceAvailable},
				{Source: "canonical_fact:health", State: contractsv1.ContextFabricSourceAvailable},
				{Source: "canonical_fact:identity", State: contractsv1.ContextFabricSourceAvailable},
			}},
			want: 3,
		},
		{
			name: "non-fact source (graph) never counts, regardless of state",
			coverage: contractsv1.ContextFabricCoverage{Sources: []contractsv1.ContextFabricSourceObservation{
				{Source: "context-fabric:graph", State: contractsv1.ContextFabricSourceAvailable},
			}},
			want: 0,
		},
		{
			name: "mixed: available and truncated count, unavailable and the non-fact source do not",
			coverage: contractsv1.ContextFabricCoverage{Sources: []contractsv1.ContextFabricSourceObservation{
				{Source: "canonical_fact:health", State: contractsv1.ContextFabricSourceAvailable},
				{Source: "canonical_fact:workload", State: contractsv1.ContextFabricSourceUnavailable},
				{Source: "canonical_fact:readiness", State: contractsv1.ContextFabricSourceTruncated},
				{Source: "context-fabric:graph", State: contractsv1.ContextFabricSourceAvailable},
			}},
			want: 2,
		},
		{
			name: "stale is fact-bearing too (fact_registry.go's own valid-state set), so it counts",
			coverage: contractsv1.ContextFabricCoverage{Sources: []contractsv1.ContextFabricSourceObservation{
				{Source: "canonical_fact:metrics", State: contractsv1.ContextFabricSourceStale},
			}},
			want: 1,
		},
		{
			name: "not_applicable and unconfigured never count -- neither is in the fact-bearing set",
			coverage: contractsv1.ContextFabricCoverage{Sources: []contractsv1.ContextFabricSourceObservation{
				{Source: "canonical_fact:identity", State: contractsv1.ContextFabricSourceNotApplicable},
				{Source: "canonical_fact:membership", State: contractsv1.ContextFabricSourceUnconfigured},
			}},
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := twoTurnCanonicalFactsCount(tc.coverage); got != tc.want {
				t.Errorf("twoTurnCanonicalFactsCount(%#v) = %d, want %d", tc.coverage, got, tc.want)
			}
		})
	}
}

// setTwoTurnReceipt attaches receipt to the request field matching member.
func setTwoTurnReceipt(req *contractsv1.ContextFabricInvestigationRequest, member string, receipt contractsv1.ContextFabricBoundSubjectReceipt) {
	switch member {
	case string(contractsv1.ContextFabricStructureNeedExpectedKind):
		req.PriorKindReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{receipt}
	case string(contractsv1.ContextFabricStructureNeedSubjectAnchor):
		req.PriorAnchorReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{receipt}
	case string(contractsv1.ContextFabricStructureNeedSubjectHandle):
		req.PriorHandleReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{receipt}
	case string(contractsv1.ContextFabricStructureNeedWindow):
		req.PriorWindowReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{receipt}
	}
}

// runTwoTurnInferredTierArm injects the negative-oracle value as an
// EXPLICIT field, unconfirmed. Structurally exempt for subject_anchor (see
// this file's own header comment) -- returns ArmInvalidReason in that case,
// never a false pass or fail.
//
// CHAOS-4039 v5 measurement contract (team-lead ruling 2026-08-22,
// superseding the v4 sol-max ruling 2026-08-20): for kind/handle members,
// the hinted call is PAIRED with an immediately-preceding no-hint baseline
// (the SAME question, no ExpectedKinds/SubjectHandles set) -- see
// twoTurnInferredClassification's own doc comment for why, and
// twoTurnCaseResult.InferredClassification for the 3-way classification
// this pairing feeds. window is EXEMPT from pairing (WindowCommitCount's
// own doc comment: W4 window-insensitivity is unimplemented, so window
// keeps the pre-v4 single-call path and its literal zero-commit bar
// unchanged).
//
// The pairing is VALID (pairInvalid==false) only when EVERY precondition
// sol's ruling names holds: same frozen corpus/base SHA, principal, model
// config, budgets, clock, retrieval policy -- all HELD BY CONSTRUCTION
// here (both calls run through the SAME investigator/principal, back to
// back, inside one process, against a base SHA/corpus this harness never
// mutates mid-run -- there is no code path in this file that could vary
// any of them between the two calls of one pair) -- PLUS two conditions
// this function actively asserts, because they are NOT automatically true
// even under all of the above: neither result may be Reused (an
// answer-reuse hit serves the ORIGINAL stored result's own
// interpretation/decision verbatim -- answer_reuse.go hashes by
// canonicalized QUESTION TEXT ONLY, which the paired requests share, so a
// hit would make any fingerprint match prove reuse occurred, not that the
// hint had no effect), and the two results' own VersionSet must be
// byte-identical (the closest wire-observable proxy for "same graph/
// backend/model state" sol's ruling requires -- ModelIdentity/
// ProjectionVersion/QueryVersion/BackendVersion are exactly the dimensions
// CHAOS-3782 answer-reuse itself binds identity to, context_fabric_types.go).
// A pair failing either check is PairInvalid, never silently classified --
// see this function's own PairInvalid assignment below for why that
// classification is skipped entirely rather than defaulted to "unjustified".
//
// windowConfirmedAsBand reports whether r landed a receipt-confirmed
// evidence window at exactly band -- never inferred from a bare successful
// call (a stale or superseded receipt echo would not necessarily surface
// as a Go error).
func windowConfirmedAsBand(r contractsv1.ContextFabricInvestigationResult, band string) bool {
	return r.EffectiveEvidenceWindow != nil &&
		r.EffectiveEvidenceWindow.Provenance == contractsv1.ContextFabricWindowClarificationConfirmed &&
		string(r.EffectiveEvidenceWindow.RelativeID) == band
}

// windowBoundsAgree reports whether a and b's own EffectiveEvidenceWindow
// bounds represent the SAME window (codex adversarial review, 2026-08-21,
// round 2, HIGH -- confirmed against window.go's own
// composeWindowClarification/relativeWindowBounds): two INDEPENDENT setup
// calls necessarily land at two DIFFERENT wall-clock instants --
// composeWindowClarification always recomputes a band's Start/End fresh
// from the engine's live now() for every call, with NO request-level way
// to pin an explicit value (deriveRequestedWindow only uses caller-supplied
// Start/End as a SKEW-TOLERANT sanity check against its own fresh
// derivation, never as an override) -- so byte-identical bounds across two
// separate Investigate() calls are not obtainable through the existing
// production request surface at all, only same-RelativeID bounds that
// differ by however long the intervening call(s) took.
//
// tolerance accepts that drift explicitly rather than silently: the
// caller passes the same per-call timeout already in scope (no single call
// can take longer before erroring), which is a rounding error against a
// 30/90/365-day window -- a few minutes' difference in where "trailing 90
// days" starts cannot plausibly move evidence across the window edge in a
// way that would explain a classification difference between the two
// legs, so tolerating it does not weaken the pair's isolation property in
// practice. What this still catches (a stale or superseded receipt echo,
// or a wrong-band redemption) is a GROSS mismatch: no confirmation at all,
// a different RelativeID, or a wrong-direction multi-day-scale drift.
//
// all_time (codex adversarial review, 2026-08-21, round 3, HIGH --
// confirmed against the ratified annex's own frozen data: its positive
// window band IS all_time for all 50 corpus cases) is compared with ZERO
// tolerance, never the drift bound above: RelativeWindowAllTime carries no
// Start/End at all (deriveRequestedWindow's own early-return, window.go),
// by design, on EVERY call -- nil on both sides means the two legs agree
// EXACTLY, not that either failed to confirm a window. See
// TestWindowBoundsAgree for the all_time/bounded/mismatched-shape cases
// this function must get right, and team-lead's 2026-08-21 verification
// request (this tolerance must never reach twoTurnInferredClassification's
// own comparison -- it doesn't: neither the decision-trace Outcome nor
// twoTurnCommittedSubjectsEquivalent references EffectiveEvidenceWindow/
// Start/End at all).
func windowBoundsAgree(a, b contractsv1.ContextFabricInvestigationResult, tolerance time.Duration) bool {
	if a.EffectiveEvidenceWindow == nil || b.EffectiveEvidenceWindow == nil {
		return false
	}
	aEnd, bEnd := a.EffectiveEvidenceWindow.End, b.EffectiveEvidenceWindow.End
	if aEnd == nil && bEnd == nil {
		return true
	}
	if aEnd == nil || bEnd == nil {
		// One leg landed a bounded window, the other all_time -- genuinely
		// different commitments, not a drift question.
		return false
	}
	drift := aEnd.Sub(*bEnd)
	if drift < 0 {
		drift = -drift
	}
	return drift <= tolerance
}

// requestIDPrefix (CHAOS-4138) roots every Investigate() call this function
// makes (the main/hinted request, both window-precondition setups, and the
// baseline leg) -- every production caller passes the fixed
// twoTurnInferredTierRequestIDPrefix, but runTwoTurnInferredTierArmWithPairRetry's
// own retry attempt passes a distinct prefix instead, so a retried pairing's
// four calls never collide (same RequestID) with the first attempt's own,
// already-exchanged four calls in the SAME run's file-exchange directory.
func runTwoTurnInferredTierArm(t *testing.T, ctx context.Context, investigator contextfabric.Investigator, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, timeout time.Duration, trace *twoTurnTraceCapture, windowBand string, requestIDPrefix string) twoTurnCaseResult {
	res := twoTurnCaseResult{Index: index, Member: entry.Member, Arm: string(twoTurnArmInferredTier)}
	// CHAOS-4086: stamped up front for the same reason the other arms do
	// it -- this arm has many early returns (structurally exempt anchor, a
	// missing window precondition, a failed baseline) and every one of them
	// produces a row a reader has to interpret.
	twoTurnStampOutcome(&res, tc, nil)
	req := twoTurnRequest(index, tc, requestIDPrefix)
	switch entry.Member {
	case string(contractsv1.ContextFabricStructureNeedExpectedKind):
		req.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectKind(entry.NegativeKind)}
	case string(contractsv1.ContextFabricStructureNeedSubjectHandle):
		// PatternID is REQUIRED by ContextFabricRequestedHandle.Validate
		// (codex round-1 finding #3: an omitted pattern_id makes the whole
		// request fail Validate() before structure canonicalization ever
		// runs, so this arm never reaches production handle logic at all).
		req.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{
			Kind: contractsv1.ContextFabricSubjectKind(entry.NegativeKind), PatternID: entry.NegativeHandlePatternID, Value: entry.NegativeHandleValue,
		}}
	case string(contractsv1.ContextFabricStructureNeedWindow):
		req.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowID(entry.NegativeWindowBand)}
	case string(contractsv1.ContextFabricStructureNeedSubjectAnchor):
		res.ArmInvalidReason = "structurally exempt: no explicit anchor field exists on the MCP surface (design brief §2.3) -- there is no wire path to construct this arm for anchor"
		return res
	default:
		res.ArmInvalidReason = fmt.Sprintf("unknown member %q", entry.Member)
		return res
	}

	isWindow := entry.Member == string(contractsv1.ContextFabricStructureNeedWindow)

	// CHAOS-4040/CHAOS-3742 window-gate ruling (team-lead, 2026-08-21,
	// documented here per that ruling's own instruction; see the matching
	// comment on CHAOS-4039 above InferredKindHandleDecisiveCount's
	// definition): the kind/handle inferred-tier pair carries NO confirmed
	// window by construction, so CHAOS-4040's unconditional class-default
	// gate (window.go) intercepts every one of these requests at
	// clarification_required before any kind/handle decision logic can
	// run -- InferredKindHandleDecisiveCount was measured at 0 across a
	// full 50-case run (RUN 3, 2026-08-21) for exactly this reason: the
	// partition is STRUCTURALLY unreachable, not a property of the
	// kind/handle classification logic under test.
	//
	// Fix (ruled, not proposed): confirm a window receipt via a SETUP TURN
	// -- the SAME production upgrade path the confirmed_wrong arm already
	// uses for kind/handle -- and attach a confirmed window to BOTH the
	// no-hint baseline and the hinted call, before the kind/handle hint is
	// applied, both windows carrying the IDENTICAL value (this SAME case's
	// own POSITIVE window band, sourced from the window-member oracle
	// entry, never the kind/handle hint under test -- mirrors the
	// established "subject_handle derives Kind from the case's own
	// positive expected_kind" precedent, adaptSignedOracleAnnex's own
	// comment, for the identical reason: the precondition must come from
	// the case's own facts, never be invented by this function). This is a
	// legitimate precondition, not contamination: (a) both pair calls
	// carry the SAME window VALUE, so the kind/handle hint remains the
	// pair's only variable; (b) window receipts are caller authority by
	// ratified design (CHAOS-3900), so a confirmed window is no different
	// in kind from the confirmed receipts the confirmed_wrong arm already
	// redeems; (c) CHAOS-4040's own non-vacuity design already contemplates
	// a confirmed-window decisive path as the way out of the class-default
	// gate.
	//
	// codex adversarial review (2026-08-21, HIGH, confirmed against
	// pginvestigation.Store's structureSupersessionClaims/verifyIdempotentReplay):
	// the FIRST version of this fix shared ONE receipt across both calls.
	// Redeeming a receipt is a single-use atomic claim on
	// (org, prior_result_id, member) -- ON CONFLICT DO NOTHING, whole
	// transaction rolled back on loss -- so the baseline's redemption wins
	// the claim and the hinted call's redemption of the SAME receipt
	// deterministically loses it (vetoed_stale/superseded), making the
	// hinted leg structurally unreachable and defeating the whole fix.
	// Each leg therefore mints and redeems its OWN independently-claimable
	// receipt from its OWN setup call (mintWindowPrecondition, called
	// twice below) -- the SAME "one setup turn, one redemption" shape the
	// confirmed_wrong arm's kind/handle members already use, just run once
	// per leg. Because a clarification_required offer is never a
	// decisive/reuse-eligible result (answer_reuse.go serves only
	// Complete/Partial/Degraded candidates), the two setup calls cannot
	// collapse into the same underlying result via answer-reuse either --
	// they are genuinely two separate offers.
	// Returns (receipt, reason, cause). CHAOS-4086: reason is the
	// closed-vocabulary string for ArmInvalidReason, and cause is the
	// ORIGINAL investigator error, kept alive so the caller can stamp its
	// stage and type. Before this, the cause was classified into a string
	// and dropped here, which is why a window-precondition failure could
	// name its class but never its stage. cause is nil for the offer-miss
	// branch, which is an engine-refusal finding rather than a failure with
	// a stage at all -- and twoTurnStampArmError leaves both fields empty
	// for a nil error, which is the honest reading.
	// mintWindowPrecondition's 4th return value (CHAOS-4103, codex review
	// round 2, confirmed): this closure's own Investigate() call is a REAL
	// call and can independently trip applySynthesisStatusOverride, SAME
	// class as baselineSynthesisOverride below -- reset immediately before
	// it (so the capture can only ever hold what THIS call produced, never
	// a leftover from the PREVIOUS leg's own setup call, since this
	// closure runs twice back to back with no reset between callers
	// otherwise) and return whatever it captured for the caller to fold
	// in once the row is far enough along to have one.
	mintWindowPrecondition := func(requestIDSuffix string) (receipt contractsv1.ContextFabricBoundSubjectReceipt, reason string, cause error, override *contextfabric.SynthesisStatusOverrideOutcome) {
		if trace != nil {
			trace.reset()
		}
		setupReq := twoTurnRequest(index, tc, requestIDSuffix)
		setupReq.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowID(windowBand)}
		setupCtx, setupCancel := context.WithTimeout(ctx, timeout)
		setupResult, setupErr := investigator.Investigate(setupCtx, principal, setupReq)
		setupCancel()
		if trace != nil {
			override = trace.synthesisOverride
		}
		if setupErr != nil {
			return contractsv1.ContextFabricBoundSubjectReceipt{}, "window precondition setup failed: " + contextFabricRejectionClass(setupErr), setupErr, override
		}
		twoTurnAssertWindowSurfacesAgree(t, index, setupResult)
		receiptID, found := selectOracleOffer(setupResult, string(contractsv1.ContextFabricStructureNeedWindow), oracleOfferQuery{windowBand: windowBand})
		if !found {
			return contractsv1.ContextFabricBoundSubjectReceipt{}, "window precondition setup turn did not offer the case's own window back as a receipt-bound offer (an engine-refusal finding, not this harness's own defect)", nil, override
		}
		return contractsv1.ContextFabricBoundSubjectReceipt{ResultID: setupResult.ResultID, ReceiptID: receiptID}, "", nil, override
	}

	if !isWindow && windowBand == "" {
		// No window-member oracle entry exists for this case (rare: every
		// case in the frozen corpus is expected to carry one) -- the
		// precondition this ruling requires cannot be constructed, so the
		// pair cannot be evaluated. PairInvalid, not a silent skip (this
		// file's own fails-toward-fine discipline).
		res.PairInvalid = true
		res.ArmInvalidReason = "no confirmed-window precondition available for this case (no window oracle entry)"
		return res
	}

	// Both preconditions are minted BACK TO BACK, before either the
	// baseline or hinted Investigate() call runs (codex adversarial
	// review, 2026-08-21, round 2 -- see windowDriftTolerance's own
	// comment below for why this ordering, not just the tolerance alone,
	// matters): this is the smallest achievable gap between the two legs'
	// window Start/End, since neither call has to wait on the OTHER leg's
	// full investigation first.
	var baselineWindow, hintedWindow contractsv1.ContextFabricBoundSubjectReceipt
	if !isWindow {
		var windowReason string
		var windowCause error
		var windowOverride *contextfabric.SynthesisStatusOverrideOutcome
		baselineWindow, windowReason, windowCause, windowOverride = mintWindowPrecondition(requestIDPrefix + "windowsetupbaseline")
		// CHAOS-4103 (codex review round 3, confirmed): folded IMMEDIATELY,
		// before the windowReason early return just below -- round 2's
		// shape deferred this to a single fold call at the function's
		// tail, which silently dropped this leg's own override whenever
		// the setup call itself failed (the exact branch directly below).
		twoTurnFoldSynthesisStatusOverride(&res, windowOverride)
		if windowReason != "" {
			res.PairInvalid = true
			twoTurnStampArmFailure(&res, windowReason, windowCause)
			return res
		}
		hintedWindow, windowReason, windowCause, windowOverride = mintWindowPrecondition(requestIDPrefix + "windowsetuphinted")
		twoTurnFoldSynthesisStatusOverride(&res, windowOverride)
		if windowReason != "" {
			res.PairInvalid = true
			twoTurnStampArmFailure(&res, windowReason, windowCause)
			return res
		}
	}

	var baseline contractsv1.ContextFabricInvestigationResult
	var baselineDecisions []graphrank.ResolutionTraceEvent
	if !isWindow {
		if trace != nil {
			trace.reset()
		}
		baselineReq := twoTurnRequest(index, tc, requestIDPrefix+"baseline")
		baselineReq.PriorWindowReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{baselineWindow}
		baselineCtx, baselineCancel := context.WithTimeout(ctx, timeout)
		var baselineErr error
		baseline, baselineErr = investigator.Investigate(baselineCtx, principal, baselineReq)
		baselineCancel()
		// baselineSynthesisOverride (CHAOS-4103, codex review rounds 1+3,
		// confirmed): the baseline call is a REAL Investigate() and can
		// independently trip applySynthesisStatusOverride -- folded
		// IMMEDIATELY, before the baselineErr early return just below
		// (round 1's shape deferred this and silently dropped it on that
		// exact branch, round 3's finding). twoTurnStampDecision's own fold
		// for the hinted call further below (severity-gated, see its doc
		// comment) makes this safe regardless of what that later call's own
		// trace state turns out to be.
		if trace != nil {
			twoTurnFoldSynthesisStatusOverride(&res, trace.synthesisOverride)
		}
		if baselineErr != nil {
			// PairInvalid, NOT ArmInvalidReason alone: this pairing could
			// not be evaluated AT ALL (the baseline itself never resolved),
			// distinct from "resolved and found unjustified" -- reported
			// separately so it is never silently absorbed into either
			// bucket (InferredPairInvalidCount's own doc comment).
			res.PairInvalid = true
			twoTurnStampArmFailure(&res, "baseline investigate error: "+contextFabricRejectionClass(baselineErr), baselineErr)
			// CHAOS-4135 (codex xhigh review, MEDIUM, confirmed): PairInvalid
			// makes this row anomalous (twoTurnCaseResultIsAnomalous), but
			// this return predates twoTurnStampDecision -- without this,
			// whatever partial trace the baseline call DID produce before
			// erroring would be silently discarded, and this row would
			// serialize with a nil BaselineTraceEvents despite tripping the
			// exact bar TraceEvents exists to explain. Whatever c.events
			// holds at the moment of the error (possibly nothing, if it
			// failed before resolution ever ran) is exactly as informative
			// here as on the success path just below.
			if trace != nil {
				res.BaselineTraceEvents = trace.snapshot()
			}
			return res
		}
		if trace != nil {
			// Captured BEFORE the hinted call's own reset below -- the
			// baseline's decision event(s) would otherwise be lost.
			baselineDecisions = trace.finalDecisionEvents()
			// CHAOS-4135: same "before the reset" urgency, for the SAME
			// reason -- staged unconditionally, redacted later by
			// twoTurnCaseResultIsAnomalous if this pairing turns out
			// ordinary. See TraceEvents/BaselineTraceEvents' own doc
			// comment (twoTurnCaseResult) for why this leg's full stream,
			// not just its decision event, is worth keeping around.
			res.BaselineTraceEvents = trace.snapshot()
		}
	}

	if !isWindow {
		// SAME window VALUE as the baseline's own precondition above (a
		// SEPARATE, independently-claimable receipt minted alongside it
		// -- see this function's own ruling comment for why one shared
		// receipt cannot work) -- the pair's only variable remains the
		// kind/handle hint.
		req.PriorWindowReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{hintedWindow}
	}
	if trace != nil {
		trace.reset()
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	result, err := investigator.Investigate(callCtx, principal, req)
	cancel()
	// CHAOS-4103 (codex review round 3, confirmed): folded IMMEDIATELY,
	// before the error return just below -- every EARLIER call in this
	// function (both window-precondition setups, the baseline) already
	// folds its own override in at its own call site; this is the hinted
	// call's turn. twoTurnFoldSynthesisStatusOverride's severity gate means
	// the order these folds happen in, and whether twoTurnStampDecision
	// below folds this SAME value again on the success path, cannot change
	// the outcome.
	if trace != nil {
		twoTurnFoldSynthesisStatusOverride(&res, trace.synthesisOverride)
	}
	if err != nil {
		res.Turn2Status = "error:" + contextFabricRejectionClass(err)
		twoTurnStampArmFailure(&res, "investigate error: "+contextFabricRejectionClass(err), err)
		// codex xhigh review round 1 (HIGH, confirmed): a hinted-call
		// error is exactly as much "this pairing could not be evaluated
		// AT ALL" as a baseline-call error is (this function's own doc
		// comment) -- window is exempt, it was never paired to begin
		// with. Without this, a hinted error would be silently excluded
		// via ArmInvalidReason alone, undercounting InferredPairInvalidCount
		// and letting a mixed run report pair_invalid=0 despite an
		// unevaluable pair.
		if !isWindow {
			res.PairInvalid = true
		}
		// CHAOS-4135 (codex xhigh review, MEDIUM, confirmed): same reason as
		// the baseline error branch above -- this return predates
		// twoTurnStampDecision, so without this a PairInvalid row (the
		// non-window case) would lose whatever partial trace the hinted
		// call produced before erroring. Captured unconditionally (window
		// included): harmless there since window's own error path never
		// trips the anomalous predicate, and the redaction pass clears it.
		if trace != nil {
			res.TraceEvents = trace.snapshot()
		}
		return res
	}
	res.Turn2Status = string(result.Status)
	res.CommittedCount = len(result.SubjectResolution.Committed)
	res.CanonicalFactsCount = twoTurnCanonicalFactsCount(result.Coverage)
	res.WrongCommit = twoTurnCommittedWrong(result.SubjectResolution.Committed, tc)
	// InferredWindowExpandOffered (CHAOS-4336): computed from THIS call's
	// own result -- the inferred_tier arm's own gated call -- not the
	// shared turn1 call twoTurnCaptureTurn1Facts reads for Turn1WindowExpandOffered.
	// Only meaningful for isWindow; result.StructureNeeds is whatever this
	// specific call composed, which for a non-window member never carries
	// WindowExpandOptions in the first place (window_expand is minted only
	// alongside a window-gated terminal), so no isWindow guard is strictly
	// required, but stating it explicitly documents the field's scope.
	if isWindow && result.StructureNeeds != nil && len(result.StructureNeeds.WindowExpandOptions) > 0 {
		res.InferredWindowExpandOffered = true
	}
	// InferredWindowAlreadyWidest (CHAOS-4336 follow-up): true when THIS
	// call's own effective window is already the registry's widest tier
	// (all_time) -- pickWindowExpandTarget (internal/contextfabric) has
	// nothing wider to recommend, by design, regardless of gate origin or
	// offers-only material. Run E (16-shard kiac, tip a5f5f900) measured
	// exactly this for both its remaining "silent" cases (53, 56; both
	// annex negative_window_band=all_time) -- a legitimate non-offer, not
	// a defect, that the by-hand annex cross-check had to establish
	// after the fact. This makes the partition code-level: WindowGatedSilentCount
	// now excludes these, so a genuinely clean run reads silent=0 without
	// needing a side lookup against the annex.
	if isWindow && result.EffectiveEvidenceWindow != nil &&
		result.EffectiveEvidenceWindow.RelativeID == contractsv1.ContextFabricRelativeWindowAllTime {
		res.InferredWindowAlreadyWidest = true
	}
	// CHAOS-4086: the HINTED call's own outcome and decision. The trace was
	// reset immediately before that call above, so the decision event this
	// reads cannot be the baseline's.
	twoTurnStampOutcome(&res, tc, result.SubjectResolution.Committed)
	twoTurnStampDecision(&res, trace)
	// false_no_match (CHAOS-4039): on a case with a real expected answer
	// (tc.ExpectID != "" -- codex review round 2, P3: absent for a CONTROL
	// case, excluded by this same check) a literal no_match terminal here is
	// as much a correctness finding as a wrong commit is, just in the
	// opposite direction. Checked on BOTH calls (team-lead ruling): a
	// baseline that falsely no-matches is just as much a measurement
	// problem as a hinted call that does.
	if tc.ExpectID != "" && (result.Status == contractsv1.ContextFabricInvestigationNoMatch || (!isWindow && baseline.Status == contractsv1.ContextFabricInvestigationNoMatch)) {
		res.FalseNoMatch = true
	}

	var hintedDecisions []graphrank.ResolutionTraceEvent
	if !isWindow {
		hintedDecisions = trace.finalDecisionEvents()
		// CHAOS-4139: per-leg affirmation visibility -- what CHAOS-4085's
		// post-synthesis gate did to EACH leg's own commit, independent of
		// whether the pairing below is valid for classification at all.
		// This is what makes a future "unjustified despite identical
		// decisions" row self-explanatory from the artifact alone, instead
		// of requiring the shard-54-style investigation that found this
		// mechanism in the first place. Stamped unconditionally (not
		// gated on res.CommittedCount, a RESULT-layer/hinted-only field --
		// see the classification gate below for why that would be exactly
		// the bug this ticket exists to fix, one line up).
		//
		// CHAOS-4096: reduced across every subject this leg's decision
		// trace committed (twoTurnLegCommitAffirmation's own doc comment) --
		// a multi-subject commit's worst-case affirmation, never just
		// whichever subject's event happened to be captured last.
		res.HintedCommitAffirmation = twoTurnLegCommitAffirmation(hintedDecisions, result)
		res.BaselineCommitAffirmation = twoTurnLegCommitAffirmation(baselineDecisions, baseline)
	}
	// codex xhigh review round 1 (MEDIUM, confirmed): this used to read
	// `res.CommittedCount > 0` -- len(result.SubjectResolution.Committed),
	// the HINTED leg's own RESULT-layer (post-CHAOS-4085-affirmation)
	// count. When CHAOS-4085 retracts the hinted leg's ONLY commit, that
	// count goes to zero and this whole block -- classification included
	// -- was silently skipped, even though the hinted leg's OWN
	// decision-stage trace shows a real "committed" outcome. That is the
	// exact shape this ticket fixes one layer up, just on the gate that
	// decides whether to classify at all rather than on the comparison
	// itself: team-lead's ruling ("classify regardless of post-synthesis
	// affirmation divergence") applies to both. Decision-layer outcome on
	// EITHER leg is now what makes a pairing worth classifying.
	if !isWindow && (twoTurnLegOutcome(hintedDecisions) == "committed" || twoTurnLegOutcome(baselineDecisions) == "committed") {
		// windowConfirmedAsBand (codex adversarial review, 2026-08-21,
		// round 2, HIGH -- confirmed against window.go's own
		// composeWindowClarification/relativeWindowBounds): safety net for
		// the window-precondition fix above. Two INDEPENDENT setup calls
		// necessarily land at two DIFFERENT wall-clock instants --
		// composeWindowClarification always recomputes a band's Start/End
		// fresh from the engine's live now() for every call, with NO
		// request-level way to pin an explicit value (deriveRequestedWindow
		// only uses caller-supplied Start/End as a SKEW-TOLERANT sanity
		// check against its own fresh derivation, never as an override) --
		// so byte-identical bounds across two separate Investigate() calls
		// are not obtainable through the existing production request
		// surface at all, only same-RelativeID bounds that differ by
		// however long the intervening call(s) took. windowDriftTolerance
		// accepts that drift explicitly rather than silently: bounded by
		// timeout (no single call can take longer before erroring above),
		// which is a rounding error against a 30/90/365-day window -- a
		// few minutes' difference in where "trailing 90 days" starts
		// cannot plausibly move evidence across the window edge in a way
		// that would explain a classification difference between the two
		// legs, so tolerating it does not weaken the pair's isolation
		// property in practice. What this check DOES still catch (a stale
		// or superseded receipt echo, or a wrong-band redemption) is a
		// GROSS mismatch: no confirmation at all, a different RelativeID,
		// or a WRONG-DIRECTION multi-day-scale drift -- flagged here as
		// PairInvalid rather than silently classified. mintWindowPrecondition
		// is called for both legs BACK TO BACK, before either the baseline
		// or hinted Investigate() call runs (see below), specifically to
		// keep this drift as small as achievable rather than merely
		// tolerated.
		// pairInvalid (team-lead ruling, widening sol's own list): errors
		// are handled above (return before this point); what remains to
		// check here is exactly the preconditions that are NOT guaranteed
		// by this function's own single-process, same-investigator
		// construction -- see this function's own doc comment for the full
		// precondition list and why the rest are held by construction.
		// windowConfirmedAsBand/windowBoundsAgree are standalone (below,
		// unit-tested by TestWindowBoundsAgree) rather than closures, so
		// the all_time-vs-drift-tolerance split team-lead asked to verify
		// (2026-08-21: "confirm the drift tolerance does NOT leak into the
		// decision-equivalence comparison for the all_time path") has a test
		// surface independent of a live run. It doesn't:
		// twoTurnInferredClassification (above) compares only the paired
		// calls' own decision-trace Outcome and committed-subject set
		// (twoTurnCommittedSubjectsEquivalent) -- neither references
		// EffectiveEvidenceWindow, Start, or End at all, so this tolerance
		// cannot reach that comparison by construction, not merely by
		// observation.
		pairInvalid := baseline.Reused || result.Reused || baseline.Versions != result.Versions ||
			!windowConfirmedAsBand(baseline, windowBand) || !windowConfirmedAsBand(result, windowBand) ||
			!windowBoundsAgree(baseline, result, timeout)
		if pairInvalid {
			// Classification SKIPPED entirely, not defaulted to
			// "unjustified": a reused or drifted pair proves nothing about
			// whether the hint had an effect either way (team-lead ruling:
			// "a reused or drifted pair silently classified as
			// baseline_equivalent is precisely the confidently-wrong-
			// measurement failure you're guarding against" -- the same
			// logic bars silently classifying it unjustified too).
			// InferredKindHandleDecisiveCount's own caller-side gate
			// excludes PairInvalid outcomes from the partition entirely.
			res.PairInvalid = true
		} else {
			res.InferredClassification = twoTurnInferredClassification(
				twoTurnUnionCommittedSubjects(hintedDecisions), twoTurnUnionCommittedSubjects(baselineDecisions),
				twoTurnLegOutcome(hintedDecisions), twoTurnLegOutcome(baselineDecisions),
				twoTurnKindAttested(trace, result.SubjectResolution.Committed))
			if res.InferredClassification == "unjustified" {
				// CHAOS-4062 shadow-insensitivity trace probe: observability
				// only, populated for the "unjustified" outcome alone -- see
				// twoTurnUnjustifiedShadowProbe's own doc comment and
				// twoTurnCaseResult's Shadow*/*CommittedSubjects field
				// comments. CHAOS-4079 added the mode, which is what makes
				// these rows finally informative: before it, a wrong-kind
				// hint could not evaluate the probe at all and every one of
				// these rows reported evaluated=false.
				res.ShadowKindInsensitivityEvaluated, res.ShadowKindInsensitivityOutcome, res.ShadowKindInsensitivityMode,
					res.BaselineCommittedSubjects, res.HintedCommittedSubjects =
					twoTurnUnjustifiedShadowProbe(trace, baseline.SubjectResolution.Committed, result.SubjectResolution.Committed)
			}
		}
	}

	// Positive tier-routing proof (codex round-1 finding #6, codex round-2
	// finding: window's own echo is a SEPARATE mechanism -- window.go's
	// windowExplicitProvenance stamps EffectiveEvidenceWindow.Provenance,
	// never ConfirmedStructure; window is not part of composeConfirmedStructure
	// at all despite the design brief's aspirational "same uniform
	// mechanism" framing). Checked directly per member, not inferred from
	// "did not commit".
	if isWindow {
		if result.EffectiveEvidenceWindow != nil && result.EffectiveEvidenceWindow.Provenance == contractsv1.ContextFabricWindowInferredDefault {
			res.TierRoutedCorrectly = true
		}
	} else {
		for _, e := range result.ConfirmedStructure {
			if string(e.Member) == entry.Member &&
				e.Source == contractsv1.ContextFabricStructureSourceExplicitUnattributed &&
				e.Provenance == contractsv1.ContextFabricStructureInferredDefault {
				res.TierRoutedCorrectly = true
			}
		}
	}
	return res
}

// twoTurnInferredTierRequestIDPrefix/twoTurnInferredTierRetryRequestIDPrefix
// (CHAOS-4138) are the two request-ID prefixes runTwoTurnInferredTierArm's
// own requestIDPrefix parameter takes in this file: the first attempt (every
// production call before this ticket) and runTwoTurnInferredTierArmWithPairRetry's
// own retry attempt. Distinct prefixes, not a shared one with a suffix
// appended at the call site, so the two attempts' request IDs can never
// collide inside the same run's file-exchange directory even if a future
// edit reorders which leg runs first.
const (
	twoTurnInferredTierRequestIDPrefix      = "inferredtier"
	twoTurnInferredTierRetryRequestIDPrefix = "inferredtierretry"
)

// twoTurnPairInvalidIsInstrumentFailure (CHAOS-4138) is the ONE place this
// retry's narrow eligibility is decided -- see CHAOS-4138's own ticket
// text for the full design rationale ("a smudged photo, never a failed
// bar"). A row qualifies ONLY when PairInvalid is true AND ArmInvalidReason
// carries one of the two literal prefixes runTwoTurnInferredTierArm's own
// baseline-leg or hinted-leg Investigate() error branches stamp
// (twoTurnStampArmFailure's own call sites, "baseline investigate error: "
// and "investigate error: "). Every OTHER PairInvalid reason in that
// function is deliberately excluded by this same prefix test, without a
// second, parallel exclusion list to keep in sync:
//
//   - the missing-window-oracle-entry structural gap ("no confirmed-window
//     precondition available...") -- not an Investigate() call at all;
//   - a window-precondition SETUP call's own failure ("window precondition
//     setup failed: ..." / "...did not offer..." -- a THIRD kind of call,
//     preceding both legs, not itself the baseline or hinted leg this
//     ticket scopes the retry to);
//   - the pairing-precondition check (Reused/VersionSet mismatch/
//     window-bounds disagreement) -- ArmInvalidReason stays empty there
//     (PairInvalid is set with no twoTurnStampArmFailure call), so it can
//     never match either prefix;
//   - subject_anchor's structural exemption -- PairInvalid is never even
//     set on that early return.
//
// A PairInvalid==false row (WrongCommit, FalseNoMatch, or an
// InferredClassification=="unjustified" outcome on a call that completed --
// every one of them a PRODUCT bar on a call that FINISHED, never an
// instrument failure) fails the leading PairInvalid check immediately and
// is never retried, regardless of ArmInvalidReason's content.
func twoTurnPairInvalidIsInstrumentFailure(res twoTurnCaseResult) bool {
	if !res.PairInvalid {
		return false
	}
	return strings.HasPrefix(res.ArmInvalidReason, "baseline investigate error: ") ||
		strings.HasPrefix(res.ArmInvalidReason, "investigate error: ")
}

// runTwoTurnInferredTierArmWithPairRetry (CHAOS-4138) wraps
// runTwoTurnInferredTierArm with a bounded, single retry -- never a retry
// loop -- when, and only when, twoTurnPairInvalidIsInstrumentFailure judges
// the first attempt's own failure an instrument failure rather than a
// product bar. The retry runs the WHOLE pairing again from scratch (both
// window preconditions, the baseline leg, the hinted leg), under a distinct
// request-ID prefix so its four calls cannot collide with the first
// attempt's already-exchanged four calls in the same run's file-exchange
// directory (see requestIDPrefix's own doc comment on
// runTwoTurnInferredTierArm).
//
// The row this function returns is the RETRY's own row whenever a retry
// ran (its own success, or its own distinct failure) -- never a merge of
// the two -- with PairRetried and the three PairRetryFirst* fields layered
// on top so the first attempt's own error is never lost (twoTurnCaseResult.
// PairRetried's own doc comment is the disclosure contract this satisfies).
// A retry that also fails is reported exactly as failed as a first attempt
// would be (report.InferredPairInvalidCount's own zero-tolerance gate still
// fires on it) -- this function only ever removes a STOCHASTIC instrument
// failure from the report, never a genuine, reproducing one.
func runTwoTurnInferredTierArmWithPairRetry(t *testing.T, ctx context.Context, investigator contextfabric.Investigator, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, timeout time.Duration, trace *twoTurnTraceCapture, windowBand string) twoTurnCaseResult {
	first := runTwoTurnInferredTierArm(t, ctx, investigator, principal, index, tc, entry, timeout, trace, windowBand, twoTurnInferredTierRequestIDPrefix)
	if !twoTurnPairInvalidIsInstrumentFailure(first) {
		return first
	}
	retry := runTwoTurnInferredTierArm(t, ctx, investigator, principal, index, tc, entry, timeout, trace, windowBand, twoTurnInferredTierRetryRequestIDPrefix)
	retry.PairRetried = true
	retry.PairRetryFirstArmInvalidReason = first.ArmInvalidReason
	retry.PairRetryFirstArmInvalidStage = first.ArmInvalidStage
	retry.PairRetryFirstArmInvalidErrorType = first.ArmInvalidErrorType
	return retry
}

// twoTurnResultStoreSaver is the narrow slice of contextfabric.InvestigationResultStore
// this harness needs to seed a stored result -- the harness-seeded
// stored-result constructor for the anchor member's confirmed-wrong arm
// (design brief §5 head).
type twoTurnResultStoreSaver interface {
	Save(context.Context, storage.Principal, contextfabric.InvestigationResult, contextfabric.SourceWatermarkSnapshot, contextfabric.RebuildEpoch, string, contextfabric.ReuseRetrievalIdentity, contextfabric.ReusePromptVersions, contextfabric.ReuseVersionAuthorities, int64) error
}

// --- live anchor-term lookup (orchestrator ruling, 2026-08-20) ---
//
// The signed DP10 annex names negative anchors by canonical_id only, never
// an alias/label term. seedAnchorNegativeResult needs a REAL term of that
// SAME entity to compute a hash graphrank.VerifyAnchorClaimantUnique will
// actually match at redemption time -- looked up LIVE from the org's own
// identity universe rather than requiring the (chris-signed, not
// unilaterally extensible) annex to carry it. The term is graph-derived
// data, not oracle content, so this lookup does not touch the artifact's
// authority.

// anchorTermIndex maps (kind, canonical_id) -> the term
// VerifyAnchorClaimantUnique would hash for that row, built ONCE per run
// from a single identity-universe read (never per-case: the SAME
// single-snapshot discipline chaos3898's ResolvedGraphBinding uses
// elsewhere in this codebase -- one consistent read, not N independently
// stale ones).
type anchorTermIndex map[string]string

func anchorTermIndexKey(kind, canonicalID string) string { return kind + "\x00" + canonicalID }

// enumerableAnchorKinds extracts the set of subject kinds actually present
// in an anchorTermIndex -- i.e. the kinds this deployment's own
// IdentityUniverse read enumerates AT ALL (orchestrator ruling 2026-08-20:
// "the 31/44 cap is a scope fact, not a bug" -- e.g. this deployment's
// IdentityUniverse covers repository/project/team, never work_item/
// pull_request/ci_pipeline_run; computed from the live read itself, never
// hardcoded, so it stays correct if a future deployment's coverage
// differs).
func enumerableAnchorKinds(terms anchorTermIndex) map[string]bool {
	kinds := map[string]bool{}
	for key := range terms {
		if idx := strings.IndexByte(key, 0); idx >= 0 {
			kinds[key[:idx]] = true
		}
	}
	return kinds
}

// anchorMatchedTerm picks the term identityRowCarriesTermHash
// (graphrank/chaos3900_structure_offers.go) would itself find FIRST for
// row: Label, else the first Alias, else the first ProviderAlias -- the
// SAME precedence order, so the term this harness seeds is provably one
// redemption-time re-verification will re-derive and match, not a
// lookalike chosen independently.
func anchorMatchedTerm(row graphrank.IdentityRow) (string, bool) {
	if strings.TrimSpace(row.Label) != "" {
		return row.Label, true
	}
	for _, alias := range row.Aliases {
		if strings.TrimSpace(alias) != "" {
			return alias, true
		}
	}
	for _, alias := range row.ProviderAliases {
		if strings.TrimSpace(alias) != "" {
			return alias, true
		}
	}
	return "", false
}

// buildAnchorTermIndex reads the org's full identity universe ONCE (fail-
// closed on error/incompleteness -- the SAME rule VerifyAnchorClaimantUnique
// itself applies at redemption: design brief 3917's "NO uniqueness proof on
// an incomplete enumeration") and indexes every row with a usable term.
func buildAnchorTermIndex(ctx context.Context, identityUniverse graphrank.IdentityUniverseFunc, orgID string) (anchorTermIndex, error) {
	if identityUniverse == nil {
		return nil, fmt.Errorf("identity universe read is not wired")
	}
	rows, _, complete, err := identityUniverse(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("read identity universe: %w", err)
	}
	if !complete {
		return nil, fmt.Errorf("identity universe read was incomplete -- refusing to seed anchor negatives against a partial enumeration")
	}
	index := make(anchorTermIndex, len(rows))
	for _, row := range rows {
		term, ok := anchorMatchedTerm(row)
		if !ok {
			continue
		}
		index[anchorTermIndexKey(string(row.Kind), row.CanonicalID)] = term
	}
	return index, nil
}

// errAnchorKindNotEnumerable/errAnchorTermNotFound (CHAOS-3742 stop-order
// closure argument, 2026-08-21, third round of the same defect class):
// seedAnchorNegativeResult's caller used to feed EVERY failure here through
// contextFabricRejectionClass -- an ENGINE-error classifier (its own
// sentinel list is context.Canceled/ErrModelOutput/etc., internal/
// contextfabric errors) applied to a plain, harness-native fmt.Errorf that
// matches NONE of those sentinels, so it fell to "unclassified" every
// single time regardless of WHICH of three structurally different causes
// actually fired: (1) the negative's kind is outside this deployment's
// enumerable set (project/repository/team only -- a documented "scope
// fact, not a bug", ~25/45 subject_anchor cases every run), (2) the kind
// IS enumerable but no row carries a usable term for this specific
// canonical_id (a real, rare gap), or (3) Validate/Save genuinely failed.
// All three produced the IDENTICAL "unclassified" string, which is exactly
// why the flag-wiring bug (run 3) and the stale-collision bug (run 3, same
// run) and the routine 25/45 structural exclusion (run 3-prime) were
// indistinguishable from the artifact alone -- every prior diagnosis
// required a live code+DB archaeology pass to tell them apart. These two
// sentinels close that: (1) and (2) now get distinct, closed-vocabulary
// reasons; only a genuine Validate/Save failure still falls to
// "unclassified", where it belongs (rare and actually worth chasing).
var (
	errAnchorKindNotEnumerable = errors.New("anchor kind not enumerable by this deployment's identity universe")
	errAnchorTermNotFound      = errors.New("no usable alias/label term found for this negative's canonical_id")
)

// anchorSeedRejectionClass classifies a seedAnchorNegativeResult error into
// the closed vocabulary above -- the harness-native counterpart to
// contextFabricRejectionClass (that function's own sentinels are all
// engine/context errors and correctly never match anything this function
// returns).
func anchorSeedRejectionClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errAnchorKindNotEnumerable):
		return "kind_not_enumerable"
	case errors.Is(err, errAnchorTermNotFound):
		return "term_not_found"
	default:
		return "unclassified"
	}
}

// seedAnchorNegativeResult is the harness-seeded STORED-RESULT constructor
// (design brief §5 head, per-member constructors): seeding is scaffolding
// for the offer's ORIGIN only -- everything downstream (receipt validation,
// claimant re-verification, census, gate) is the ordinary production
// redemption path run against this seeded row exactly as it would run
// against any engine-produced one.
//
// terms supplies the MatchedTermHash input, looked up LIVE from the org's
// own identity universe by (kind, canonical_id) -- orchestrator ruling
// (2026-08-20): the signed DP10 annex carries canonical_id only, never an
// alias/label term, and extending a chris-signed artifact needs a fresh
// authorship+sign-off cycle; the term itself is graph-derived data, not
// oracle content, so a live lookup at seed time doesn't touch the
// artifact's authority. If terms has no entry for this negative -- the
// canonical_id names no row the identity universe currently carries a
// usable Label/Alias/ProviderAlias for -- this arm is reported
// ArmInvalidReason for this case (the existing, already-honest fallback),
// never a fabricated hash.
func seedAnchorNegativeResult(ctx context.Context, store twoTurnResultStoreSaver, principal storage.Principal, index int, entry twoTurnOracleEntry, receiptID string, terms anchorTermIndex, runToken string) (resultID string, err error) {
	if !enumerableAnchorKinds(terms)[entry.NegativeKind] {
		return "", fmt.Errorf("%w: kind=%s", errAnchorKindNotEnumerable, entry.NegativeKind)
	}
	term, ok := terms[anchorTermIndexKey(entry.NegativeKind, entry.NegativeAnchorCanonicalID)]
	if !ok {
		return "", fmt.Errorf("%w: kind=%s canonical_id=%s", errAnchorTermNotFound, entry.NegativeKind, entry.NegativeAnchorCanonicalID)
	}
	// result_id is run-scoped (runToken, this test's own per-invocation
	// random token -- see its own doc comment) -- acr.context_fabric_
	// investigation_results is an IMMUTABLE store keyed on result_id alone,
	// and a bare case-index key collides with any prior run's row for the
	// same index forever (RUN 3 finding, 2026-08-21).
	resultID = fmt.Sprintf("result_twoturn_seed_anchor_%s_%06d", runToken, index)
	result := contextfabric.InvestigationResult{
		SchemaVersion: contextfabric.InvestigationResultSchemaV1,
		ResultID:      resultID,
		RequestID:     fmt.Sprintf("request_twoturn_seed_%s_%06d", runToken, index),
		GeneratedAt:   time.Now().UTC(),
		Status:        contractsv1.ContextFabricInvestigationClarificationRequired,
		Question:      "synthetic harness-seeded question, not corpus content",
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status_and_drivers",
			TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		},
		SubjectResolution: contextfabric.SubjectResolution{
			Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{},
			ClarificationPrompt: "synthetic harness-seeded clarification prompt, not corpus content",
		},
		StrongestPressures: []string{},
		Drivers:            []contextfabric.DriverJudgment{},
		RemainingWork:      []contextfabric.Finding{},
		ReadinessGaps:      []contextfabric.Finding{},
		Paths:              []contextfabric.RelationshipPath{},
		Conflicts:          []contextfabric.Finding{},
		Limitations:        []string{"harness-seeded anchor negative for the confirmed-wrong arm"},
		EvidenceRefIDs:     []string{},
		ClaimedFacts:       []contextfabric.ClaimedFact{},
		Coverage:           contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		Versions: contextfabric.VersionSet{
			ServiceVersion: "chaos-3742-two-turn", ContractVersion: contextfabric.InvestigationResultSchemaV1, Backend: "harness-seeded",
			ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1",
			SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1", ModelIdentity: "harness/seeded-v1",
		},
		DeterministicAnswer: "clarification is required before this question can be answered.",
		Warnings:            []string{},
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor},
			AnchorOptions: []contractsv1.ContextFabricAnchorOption{{
				ReceiptID: receiptID, OptionID: "opt_twoturn_seed_negative", Label: "harness-seeded negative anchor",
				Kind:        contractsv1.ContextFabricSubjectKind(entry.NegativeKind),
				CanonicalID: entry.NegativeAnchorCanonicalID,
				// HashAliasTerm is the SAME function VerifyAnchorClaimantUnique
				// re-verifies against at redemption time (codex round-1
				// finding #2, confirmed against
				// graphrank.VerifyAnchorClaimantUnique/identityRowCarriesTermHash):
				// a fabricated hash can never match any live identity-universe
				// row, making this arm structurally unredeemable. term was
				// looked up LIVE (this function's own doc comment) against a
				// REAL row this negative's own canonical_id names -- seeding
				// supplies the offer's ORIGIN only, never a shortcut around
				// real re-verification.
				MatchedTermHash: graphrank.HashAliasTerm(term),
				OfferSource:     contractsv1.ContextFabricStructureOfferEngine,
			}},
		},
	}
	if verr := result.Validate(); verr != nil {
		return "", fmt.Errorf("seeded anchor-negative result failed Validate: %w", verr)
	}
	timeAxisKey := contextfabric.TimeAxisKeyFor(result.Interpretation.TimeContext)
	if serr := store.Save(ctx, principal, result, nil, nil, timeAxisKey,
		contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0); serr != nil {
		return "", fmt.Errorf("save seeded anchor-negative result: %w", serr)
	}
	return resultID, nil
}

// runTwoTurnConfirmedWrongArm redeems the negative-oracle offer as a
// receipt. For subject_anchor it seeds its own stored offer first (the
// harness-seeded constructor); for kind/handle/window it runs a SETUP TURN
// (explicit field -> receipt-bound offer on the SAME response, the
// production upgrade path) to mint the negative offer, then redeems it on
// a third call.
// preflightAnchorCausalChain (CHAOS-3742 stop-order closure argument,
// 2026-08-21, third round of the same defect class -- team-lead ruling:
// "the invariant gets measured at run start from now on, not predicted
// from merged fixes"): a cheap, single-case, fail-fast proof that the
// FULL anchor causal chain is live BEFORE a multi-hour run is allowed to
// start -- flag reached config.Load(), a seeded negative is actually
// redeemable, and that redemption lands as a receipt-sourced applied
// ConfirmedStructure entry (the same observable event proves "a seeded
// negative is redeemable" and "an anchor offer can apply" -- redemption
// IS an offer being applied; see the diagnosis report's own note on why
// this is one check, not two, and why it is NOT scoped to the positive
// arm's independent, pre-existing, separately-ticketed offer_miss issue).
//
// This exists because RUN 3 and RUN 3-prime both burned hours of
// wall-clock/token cost before the SAME class of defect (something in the
// anchor causal chain was dead) was discovered from full-run evidence.
// Every one of those failure modes -- the flag not reaching config, the
// stale-collision seeding bug, and a genuinely broken redemption path --
// is detectable from ONE case, in seconds, without running the other 49.
//
// requireAnchorFlag should be true whenever the caller requested the flag
// (ACR_TEST_TRIAL_ANCHOR_MEMBERSHIP_ENABLED set) -- refusing to spend
// hours measuring a flag that silently never took effect is the whole
// point of this gate (see item 1 of the RUN 3 diagnosis).
func preflightAnchorCausalChain(ctx context.Context, investigator contextfabric.Investigator, store twoTurnResultStoreSaver, principal storage.Principal, cfg config.Config, requireAnchorFlag bool, anchorTerms anchorTermIndex, annex twoTurnOracleAnnex, corpus []trialCase, timeout time.Duration, runToken string) error {
	if requireAnchorFlag && !cfg.AnchorMembershipOffersEnabled {
		return errors.New("preflight: ACR_CONTEXT_FABRIC_ANCHOR_MEMBERSHIP_ENABLED was requested but did not reach config.Load() -- refusing to start a multi-hour run measuring a flag that is not actually active")
	}

	enumerable := enumerableAnchorKinds(anchorTerms)
	var probe *twoTurnOracleEntry
	for i := range annex.Entries {
		e := annex.Entries[i]
		if e.Member != string(contractsv1.ContextFabricStructureNeedSubjectAnchor) {
			continue
		}
		if !enumerable[e.NegativeKind] {
			continue
		}
		if _, ok := anchorTerms[anchorTermIndexKey(e.NegativeKind, e.NegativeAnchorCanonicalID)]; !ok {
			continue
		}
		probe = &e
		break
	}
	if probe == nil {
		return errors.New("preflight: no enumerable-kind, term-available subject_anchor case exists in this annex/identity-universe combination -- the anchor causal chain cannot be exercised at all this run")
	}

	receiptID := contractsv1.ContextFabricAnchorOptionReceiptPrefix + "preflightprobe000000000000"
	resultID, seedErr := seedAnchorNegativeResult(ctx, store, principal, probe.Index, *probe, receiptID, anchorTerms, "preflight-"+runToken)
	if seedErr != nil {
		return fmt.Errorf("preflight: seeding a known-good anchor negative (case %d) failed: %s -- the anchor arm cannot function this run", probe.Index, anchorSeedRejectionClass(seedErr))
	}

	req := twoTurnRequest(probe.Index, corpus[probe.Index], "preflightanchorredeem")
	setTwoTurnReceipt(&req, probe.Member, contractsv1.ContextFabricBoundSubjectReceipt{ResultID: resultID, ReceiptID: receiptID})
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	result, investigateErr := investigator.Investigate(callCtx, principal, req)
	cancel()
	if investigateErr != nil {
		return fmt.Errorf("preflight: redeeming the probe anchor negative (case %d) failed: %s", probe.Index, contextFabricRejectionClass(investigateErr))
	}
	for _, cs := range result.ConfirmedStructure {
		if cs.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor &&
			cs.Disposition == contractsv1.ContextFabricStructureDispositionApplied &&
			cs.Source == contractsv1.ContextFabricStructureSourceReceipt {
			return nil // causal chain proven live: flag on, seed redeemable, offer applied
		}
	}
	return fmt.Errorf("preflight: the probe anchor redemption (case %d) did not apply (no receipt-sourced ConfirmedStructure entry for subject_anchor) -- the anchor causal chain is not live this run", probe.Index)
}

func runTwoTurnConfirmedWrongArm(t *testing.T, ctx context.Context, investigator contextfabric.Investigator, store twoTurnResultStoreSaver, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, timeout time.Duration, anchorTerms anchorTermIndex, runToken string, trace *twoTurnTraceCapture) twoTurnCaseResult {
	res := twoTurnCaseResult{Index: index, Member: entry.Member, Arm: string(twoTurnArmConfirmedWrong)}
	// Stamped BEFORE the early returns below (a seeded-negative failure, a
	// missing offer): a row that never reached Investigate still says what
	// this case expected, which is what makes an arm-invalid row readable
	// without the corpus annex.
	twoTurnStampOutcome(&res, tc, nil)

	var offerResultID, receiptID string
	if entry.Member == string(contractsv1.ContextFabricStructureNeedSubjectAnchor) {
		receiptID = contractsv1.ContextFabricAnchorOptionReceiptPrefix + "twoturnseed0000000000000"
		var err error
		offerResultID, err = seedAnchorNegativeResult(ctx, store, principal, index, entry, receiptID, anchorTerms, runToken)
		if err != nil {
			twoTurnStampArmFailure(&res, "harness-seeded anchor negative could not be made redeemable: "+anchorSeedRejectionClass(err), err)
			return res
		}
	} else {
		setupReq := twoTurnRequest(index, tc, "confirmedwrongsetup")
		switch entry.Member {
		case string(contractsv1.ContextFabricStructureNeedExpectedKind):
			setupReq.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectKind(entry.NegativeKind)}
		case string(contractsv1.ContextFabricStructureNeedSubjectHandle):
			// PatternID is REQUIRED (codex round-1 finding #3, same as the
			// inferred-tier arm above).
			setupReq.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{
				Kind: contractsv1.ContextFabricSubjectKind(entry.NegativeKind), PatternID: entry.NegativeHandlePatternID, Value: entry.NegativeHandleValue,
			}}
		case string(contractsv1.ContextFabricStructureNeedWindow):
			setupReq.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowID(entry.NegativeWindowBand)}
		}
		// CHAOS-4103: reset immediately before this call, the SAME
		// discipline every other observed call in this file uses, so the
		// capture below can only ever hold what THIS call produced --
		// never a stale outcome left over from whatever ran before this
		// arm (this file never reads trace state across arm boundaries
		// otherwise, so nothing else depends on this reset's placement).
		if trace != nil {
			trace.reset()
		}
		setupCtx, cancel := context.WithTimeout(ctx, timeout)
		setupResult, err := investigator.Investigate(setupCtx, principal, setupReq)
		cancel()
		// CHAOS-4103 (codex review round 3, confirmed): folded IMMEDIATELY,
		// before either early return below -- deferring this to a single
		// fold call at the function's tail (round 1's shape) silently
		// dropped the setup turn's own override whenever the setup call
		// itself errored or its offer went missing, exactly the two
		// branches directly below.
		if trace != nil {
			twoTurnFoldSynthesisStatusOverride(&res, trace.synthesisOverride)
		}
		if err != nil {
			twoTurnStampArmFailure(&res, "setup turn failed: "+contextFabricRejectionClass(err), err)
			return res
		}
		offerResultID = setupResult.ResultID
		if entry.Member == string(contractsv1.ContextFabricStructureNeedWindow) {
			twoTurnAssertWindowSurfacesAgree(t, index, setupResult)
		}
		var found bool
		receiptID, found = selectOracleOffer(setupResult, entry.Member, entry.negativeQuery())
		if !found {
			res.ArmInvalidReason = "setup turn did not offer the designated negative back as a receipt-bound offer (an engine-refusal finding, not this harness's own defect)"
			return res
		}
	}

	req := twoTurnRequest(index, tc, "confirmedwrong")
	setTwoTurnReceipt(&req, entry.Member, contractsv1.ContextFabricBoundSubjectReceipt{ResultID: offerResultID, ReceiptID: receiptID})
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// CHAOS-4086: see runTwoTurnPositiveArm's own reset comment.
	if trace != nil {
		trace.reset()
	}
	turn2, err := investigator.Investigate(callCtx, principal, req)
	// CHAOS-4103: folded immediately, same reason as the setup call above --
	// an error return just below must not discard this call's own override
	// either. twoTurnStampDecision's OWN fold (severity-gated, see its doc
	// comment) makes this and the setup fold above safe in any order.
	if trace != nil {
		twoTurnFoldSynthesisStatusOverride(&res, trace.synthesisOverride)
		// CHAOS-4307 (codex round 2, Medium, confirmed): see
		// runTwoTurnPositiveArm's own identical comment -- this call
		// redeems a confirmed receipt (setTwoTurnReceipt above), so it CAN
		// reach the confirmed-kind-scoped census path and must not discard
		// that event on an error return either.
		res.TraceEvents = trace.snapshot()
	}
	if err != nil {
		res.Turn2Status = "error:" + contextFabricRejectionClass(err)
		twoTurnStampArmFailure(&res, "investigate error: "+contextFabricRejectionClass(err), err)
		return res
	}
	res.Turn2Status = string(turn2.Status)
	res.CommittedCount = len(turn2.SubjectResolution.Committed)
	res.CanonicalFactsCount = twoTurnCanonicalFactsCount(turn2.Coverage)
	res.Applied = memberApplied(turn2, entry.Member)
	res.WrongCommit = twoTurnCommittedWrong(turn2.SubjectResolution.Committed, tc)
	twoTurnStampOutcome(&res, tc, turn2.SubjectResolution.Committed)
	twoTurnStampDecision(&res, trace)
	return res
}

// twoTurnMutationProbe classifies a mutation-arm turn's outcome: tripped
// means the expected veto/refusal actually happened.
//
// The remove_confirmation probe specifically (runTwoTurnMutationArm's probe
// (i)) re-asks the case's own question with NO receipt attached at all --
// so committedCount can be >0 here even though `applied` is (correctly)
// false, whenever ordinary search alone is confident enough to commit the
// case's true subject without needing ANY structure confirmation. That is
// CHAOS-4039's own root-cause mechanism ("committed/stalled resolutions
// pay nothing", resolve.go:1086) showing up a second way: a case whose
// subject search resolves unassisted was never going to be blocked by
// removing its confirming receipt, so a 1-untripped-per-run result here is
// expected for exactly the same reason, not a probe defect to
// re-investigate -- confirm it is CHAOS-4039's mechanism by checking
// wrong_commit_count stays 0 for that case (twoTurnCommittedWrong), not by
// re-deriving the census/receipt-independence chain from scratch.
func twoTurnMutationProbe(applied bool, status string, committedCount int) (tripped bool) {
	return !applied && committedCount == 0 && status != string(contractsv1.ContextFabricInvestigationComplete)
}

// mutationProbeKinds (CHAOS-4165) is the closed, fixed set of MutationProbe
// values runTwoTurnMutationArm ever stamps ("remove_confirmation",
// "corrupt_receipt", "stale_superseded_offer" -- that function's own three
// run() calls). twoTurnReport.MutationProbesRun/MutationProbesTripped are
// built by map-key auto-vivification (report.MutationProbesRun[probe]++),
// so a probe kind that never ran even once this run is simply ABSENT from
// those maps, never present with a zero value -- ranging over either map to
// compute MutationProbeCoverage would silently skip exactly the
// zero-runs case this ticket exists to surface. This fixed list is the
// one place that closed vocabulary is named, so mutationProbeCoverage
// (below) always reports all three kinds regardless of what happened.
var mutationProbeKinds = []string{"remove_confirmation", "corrupt_receipt", "stale_superseded_offer"}

// mutationProbeCoverageFloor (CHAOS-4165) is the absolute minimum
// mutation-probe RUN count, per probe kind, a run needs before its own
// MutationProbesTripped[kind]/MutationProbesRun[kind] ratio carries real
// statistical weight -- see twoTurnMutationProbeCoverage's own doc comment
// for what this decides and why the existing tripped==ran gate cannot see
// this on its own (1/1 reads identically to 7/7).
//
// UNCALIBRATED, deliberately: chosen because it sits below every observed
// sol-baseline value on the ext65 corpus (7 per kind, CHAOS-4113 RUN A,
// 2026-08-23) while still requiring meaningfully more than the
// single-run collapse CHAOS-4165 found under luna (1 per kind, same
// corpus/tip) -- NOT a measured, live-corpus-calibrated ceiling the way
// kindCoverageQueryLimit (chaos4038_kind_coverage.go) is tied to
// CalibratedTopK. This repo's own CHAOS-3834/CHAOS-3829 calibration
// discipline would reject treating this as anything more than a
// conservative, always-safe starting default -- revisit once more
// corpora/responders establish a real distribution.
const mutationProbeCoverageFloor = 5

// twoTurnMutationProbeCoverage (CHAOS-4165) is one probe kind's own
// statistical-power verdict: Runs is MutationProbesRun[kind] (what
// actually happened this run); RequiredMin is min(the run's own
// MutationProbeEligibleCount, mutationProbeCoverageFloor) -- never
// demanding more runs than this run's own eligible population could
// structurally supply, so a genuinely small corpus/annex slice reads
// adequate at its own achievable ceiling rather than falsely low_power.
// Adequate is Runs >= RequiredMin. This is a SOFT, informational verdict
// -- unlike MutationProbesTripped/MutationProbesRun's own tripped==ran
// gate (a hard, zero-tolerance bar), an inadequate count is never itself
// a run failure: a low offer/apply rate under a given responder is a
// real PRODUCT finding (exactly what CHAOS-4165 found under luna), never
// a reason to fail the run that measured it.
type twoTurnMutationProbeCoverage struct {
	Runs        int  `json:"runs"`
	RequiredMin int  `json:"required_min"`
	Adequate    bool `json:"adequate"`
}

// mutationProbeCoverage (CHAOS-4165) computes every probe kind's own
// twoTurnMutationProbeCoverage from ran/eligible -- a pure function so the
// producer (this file) and a future consumer can share one definition of
// "adequate" rather than each re-deriving min()/comparison independently.
// Always returns all of mutationProbeKinds, regardless of which keys
// `ran` happens to carry -- see that variable's own doc comment for why.
func mutationProbeCoverage(ran map[string]int, eligible int) map[string]twoTurnMutationProbeCoverage {
	requiredMin := eligible
	if requiredMin > mutationProbeCoverageFloor {
		requiredMin = mutationProbeCoverageFloor
	}
	coverage := make(map[string]twoTurnMutationProbeCoverage, len(mutationProbeKinds))
	for _, kind := range mutationProbeKinds {
		runs := ran[kind]
		// codex review finding (P2, confirmed): eligible==0 (a sharded
		// single-case shard whose one entry is window-only or
		// negative-only, or a limited run that happens to select no
		// eligible entry at all) makes requiredMin==0 too, and runs==0
		// >= 0 read as "adequate" -- a zero-population ratio is
		// undefined, not clean. eligible>0 is required alongside
		// runs>=requiredMin so the worst case (no evidence at all) can
		// never read as adequate.
		coverage[kind] = twoTurnMutationProbeCoverage{Runs: runs, RequiredMin: requiredMin, Adequate: eligible > 0 && runs >= requiredMin}
	}
	return coverage
}

// runTwoTurnMutationArm runs the three non-vacuity probes (design brief §5
// head) against a case the positive arm already converted. Each probe's
// twoTurnCaseResult.MutationProbe names which one ran, and MutationTripped
// records whether the expected veto/refusal actually happened -- a probe
// that does NOT trip is itself a finding (the run is invalid, per the
// harness's own fails-toward-fine discipline), never silently ignored.
func runTwoTurnMutationArm(ctx context.Context, investigator contextfabric.Investigator, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, turn1ResultID, receiptID string, timeout time.Duration, trace *twoTurnTraceCapture) []twoTurnCaseResult {
	// run calls Investigate EXACTLY ONCE per probe (codex round-2 finding
	// #7: the original stale-probe implementation called Investigate a
	// SECOND time to inspect the disposition, so an error on the first call
	// could be silently overridden by whatever the second, independent call
	// happened to return -- two calls against the same request is itself
	// the bug, not merely "which call's result to trust"). requireStale, when
	// true, additionally demands the vetoed_stale disposition specifically
	// (probe iii's own tell, vs the generic non-apply the other two probes
	// accept).
	run := func(probe string, req contractsv1.ContextFabricInvestigationRequest, requireStale bool) twoTurnCaseResult {
		res := twoTurnCaseResult{Index: index, Member: entry.Member, Arm: string(twoTurnArmMutation), MutationProbe: probe}
		twoTurnStampOutcome(&res, tc, nil)
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		// CHAOS-4086: per PROBE, not per arm -- this closure runs three
		// times against three different requests, and each probe's row
		// carries its own decision. See runTwoTurnPositiveArm's own reset
		// comment for why the shared capture must be cleared here.
		if trace != nil {
			trace.reset()
		}
		result, err := investigator.Investigate(callCtx, principal, req)
		// CHAOS-4103 (codex review round 3, generalized): folded
		// IMMEDIATELY, before the error return just below -- twoTurnStampDecision
		// below never runs on that path.
		if trace != nil {
			twoTurnFoldSynthesisStatusOverride(&res, trace.synthesisOverride)
		}
		if err != nil {
			// codex round-1 finding #7: an investigator error is NOT
			// evidence the probe's expected veto/refusal fired -- a
			// timeout, a dependency outage, or an unrelated internal error
			// would previously have been silently counted as a trip. Left
			// untripped and reported as inconclusive; the run's own
			// tripped==ran invariant then correctly flags it as a finding
			// rather than a false pass.
			res.Turn2Status = "error:" + contextFabricRejectionClass(err)
			twoTurnStampArmFailure(&res, "investigate error (inconclusive, not counted as a trip): "+contextFabricRejectionClass(err), err)
			// CHAOS-4135 (codex xhigh review, MEDIUM, confirmed): res.MutationTripped
			// stays at its zero value (false) on this path, and MutationProbe
			// is already non-empty (set above) -- twoTurnCaseResultIsAnomalous
			// reports this row anomalous (a probe that "ran" without
			// tripping), but this return predates twoTurnStampDecision.
			// Same reasoning as the inferred_tier arm's own error branches.
			if trace != nil {
				res.TraceEvents = trace.snapshot()
			}
			return res
		}
		res.Turn2Status = string(result.Status)
		res.CommittedCount = len(result.SubjectResolution.Committed)
		res.CanonicalFactsCount = twoTurnCanonicalFactsCount(result.Coverage)
		res.Applied = memberApplied(result, entry.Member)
		twoTurnStampOutcome(&res, tc, result.SubjectResolution.Committed)
		twoTurnStampDecision(&res, trace)
		staleDisposition := false
		for _, e := range result.ConfirmedStructure {
			if string(e.Member) == entry.Member && e.Disposition == contractsv1.ContextFabricStructureDispositionVetoedStale {
				staleDisposition = true
			}
		}
		res.MutationTripped = twoTurnMutationProbe(res.Applied, res.Turn2Status, res.CommittedCount)
		if requireStale {
			res.MutationTripped = res.MutationTripped && staleDisposition
		}
		return res
	}

	results := make([]twoTurnCaseResult, 0, 3)

	// (i) remove the confirming receipt: the refusal must return.
	removeConfirmationReq := twoTurnRequest(index, tc, "mutation_remove_confirmation")
	results = append(results, run("remove_confirmation", removeConfirmationReq, false))

	// (ii) corrupt the receipt id: 400/veto, never an answer.
	corruptReq := twoTurnRequest(index, tc, "mutation_corrupt_receipt")
	setTwoTurnReceipt(&corruptReq, entry.Member, contractsv1.ContextFabricBoundSubjectReceipt{
		ResultID: turn1ResultID, ReceiptID: receiptID + "corrupted",
	})
	results = append(results, run("corrupt_receipt", corruptReq, false))

	// (iii) redeem the ALREADY-REDEEMED (now superseded) offer again ->
	// stale_superseded_offer veto. Requires the positive arm to have
	// already redeemed this exact (turn1ResultID, receiptID) pair once,
	// producing the result that superseded it.
	staleReq := twoTurnRequest(index, tc, "mutation_stale_superseded_offer")
	setTwoTurnReceipt(&staleReq, entry.Member, contractsv1.ContextFabricBoundSubjectReceipt{
		ResultID: turn1ResultID, ReceiptID: receiptID,
	})
	results = append(results, run("stale_superseded_offer", staleReq, true))

	return results
}

// --- live harness: env-gated exactly like TestChaos3884ReplayHarness/TestGenerativeTrialCorpus ---

// ACR_TEST_TRIAL_CORPUS=<path> ACR_TEST_TRIAL_ORG=<org> \
// ACR_TEST_TWOTURN_ORACLE_ANNEX=<path> ACR_TEST_TWOTURN_OUT=<path> \
// go test ./internal/runtime/hosted -run TestChaos3742TwoTurnConfirmationReplay -v -timeout 4h
func TestChaos3742TwoTurnConfirmationReplay(t *testing.T) {
	corpusPath := os.Getenv("ACR_TEST_TRIAL_CORPUS")
	if corpusPath == "" {
		t.Skip("ACR_TEST_TRIAL_CORPUS is not set; the CHAOS-3742 trial corpus is withheld and supplied at run time")
	}
	annexPath := os.Getenv("ACR_TEST_TWOTURN_ORACLE_ANNEX")
	if annexPath == "" {
		t.Skip("ACR_TEST_TWOTURN_ORACLE_ANNEX is not set; the DP10 oracle annex is withheld and supplied at run time")
	}
	orgID := requireEnv(t, "ACR_TEST_TRIAL_ORG")
	outPath := requireEnv(t, "ACR_TEST_TWOTURN_OUT")
	if _, err := os.Stat(outPath); err == nil {
		t.Fatalf("ACR_TEST_TWOTURN_OUT=%s already exists -- refusing to silently overwrite existing acceptance evidence (mirrors chaos3884_replay_harness_test.go's own rule)", outPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat ACR_TEST_TWOTURN_OUT=%s: %v", outPath, err)
	}
	runStartedAt := time.Now().UTC().Format(time.RFC3339)
	// runToken (lane-run3 RUN 3 finding, 2026-08-21): seedAnchorNegativeResult's
	// result_id was purely case-index-keyed (result_twoturn_seed_anchor%06d),
	// with NO run-scoped component, against acr.context_fabric_investigation_results
	// -- an IMMUTABLE, byte-for-byte idempotent-replay store keyed on result_id
	// alone (pginvestigation.Store.verifyIdempotentReplay). A stale row from
	// ANY prior run (RUN 3 found rows dated ~10-12h earlier, from a buggy
	// harness version that FABRICATED matched_term_hash from the canonical_id
	// instead of a live term lookup) permanently blocks every subsequent run's
	// fresh, correctly-computed seed for the same case index with an
	// unclassified "already exists with different content" error -- masking
	// the confirmed_wrong subject_anchor arm on both run 2 and run 3
	// identically, independent of whatever the arm's own logic does. Mixing a
	// fresh run-scoped token into the id removes the collision at its root
	// (rather than requiring an operator to remember to clean the table
	// between runs).
	runTokenBytes := make([]byte, 8)
	if _, err := rand.Read(runTokenBytes); err != nil {
		t.Fatalf("generate run-scoped result id token: %v", err)
	}
	runToken := hex.EncodeToString(runTokenBytes)

	annex := loadTwoTurnOracleAnnex(t, annexPath)
	if err := requireAnnexSignedOff(annex); err != nil {
		t.Fatalf("refusing to run the two-turn confirmation replay: %v", err)
	}

	corpus, corpusHash := loadTrialCorpus(t, corpusPath)
	// annex.CorpusSHA256 carries the signed artifact's sha8 PREFIX (see
	// signedOracleAnnex's own doc comment), compared against the loaded
	// corpus hash's own first 8 characters.
	if len(corpusHash) < 8 || annex.CorpusSHA256 != corpusHash[:8] {
		t.Fatalf("oracle annex corpus_sha8=%s does not match the loaded corpus hash prefix=%.8s -- refusing to run against a mismatched annex/corpus pair", annex.CorpusSHA256, corpusHash)
	}
	// CHAOS-4157 preflight: fail closed before any live measurement work
	// begins if either fixture's own work_item identifiers have drifted
	// out of the live v2 canonical scheme (chaos4157_v2_scheme_preflight_test.go).
	twoTurnValidateWorkItemV2Scheme(t, annex, corpus)
	// CHAOS-4348: the two files can each be internally scheme-clean (the
	// check above) while still DISAGREEING with each other -- exactly what
	// Run G found (cases 57/60 stale-corpus-id, case 45 outright kind/id
	// disagreement). See chaos4348_corpus_annex_agreement_test.go.
	twoTurnValidateCorpusAnnexAgreement(t, annex, corpus)
	source := requireGitSourceIdentity(t)

	// Subscription-only, no metered key, ever (standing rule for this
	// epic, chaos3884_replay_harness_test.go's own doc comment): when
	// ACR_TEST_TRIAL_EXCHANGE_DIR is set, every generative call routes
	// through the SAME file-exchange transport TestGenerativeTrialCorpus's
	// arm 5 and TestChaos3884ReplayHarness use, answered by an
	// out-of-process responder on the operator's subscription auth --
	// never modelprovider.New's direct, metered API path.
	exchangeDir := os.Getenv("ACR_TEST_TRIAL_EXCHANGE_DIR")
	wireProductionEnv(t, exchangeDir != "")
	// ACR_CONTEXT_FABRIC_ANCHOR_MEMBERSHIP_ENABLED (CHAOS-3742 RUN 3
	// finding, lane-run3, 2026-08-21; scoping corrected per codex
	// adversarial review, same date -- see wireProductionEnv's own comment
	// on why this is wired HERE, two-turn-specific, rather than in that
	// shared function): wireProductionEnv's clearAmbientACREnv wipes an
	// operator's own ACR_CONTEXT_FABRIC_ANCHOR_MEMBERSHIP_ENABLED export
	// before config.Load() below ever runs -- exactly the ambient-env bug
	// class this whole isolation discipline exists to prevent, which is
	// precisely why RUN 3 measured the CHAOS-4042 anchor-membership fix
	// with the flag OFF despite exporting it, identically to runs 1-2. The
	// trial-prefixed source var is read directly (never wiped -- it
	// doesn't start with the real flag's name) and is explicit, not
	// ambient; t.Setenv only fires when the caller actually provided one,
	// and this line runs AFTER wireProductionEnv's own clear, so nothing
	// here is subject to being wiped itself.
	if raw := os.Getenv("ACR_TEST_TRIAL_ANCHOR_MEMBERSHIP_ENABLED"); raw != "" {
		t.Setenv("ACR_CONTEXT_FABRIC_ANCHOR_MEMBERSHIP_ENABLED", raw)
	}
	// pglifecycle.EnvEnabled: UNLIKE the anchor-membership block above,
	// wireProductionEnv's own set() call ALREADY re-exports this one from
	// ACR_TEST_TRIAL_GRAPH_LIFECYCLE_ENABLED for every trial test sharing
	// that function (generative_trial_live_test.go, "DELIBERATELY absent
	// from acrEnvIsolationAllowlist" -- see that var's own doc comment) --
	// no second re-export needed here. This harness's OWN gap was never
	// the env-wiring (already shared/correct); it was that nothing in this
	// file ever built an epochResolver or recorded what it resolved, so
	// every run silently measured epoch 0 regardless of the flag. See the
	// read-proof block right after hosted.Open below.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	// CHAOS-4155 Phase 2 (lane-4155-p2): this used to hardcode
	// slog.LevelWarn, discarding cfg.LogLevel right after loading it --
	// no env var could ever raise this harness's own log level, so a
	// DebugContext-level event (e.g. graphrank's confirmed_kind_scope
	// stage, which CHAOS-4155's own shadow vector census telemetry rides
	// on) could never reach a trial run's logs regardless of what an
	// operator set. cfg.LogLevel is ConfigFromEnv's own ACR_LOG_LEVEL
	// value (default "info"); wireProductionEnv now threads
	// ACR_TEST_TRIAL_LOG_LEVEL onto it the same way every other optional
	// trial-input knob is threaded, so a measurement run sets that one
	// var to raise this harness's own level without touching any other
	// trial script sharing wireProductionEnv (all of which stay at the
	// "info" default, byte-identical to before).
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	// CHAOS-4103: traceCapture's embedded SlogEngineTelemetry is built from
	// the SAME logger every other sink in this function uses (not
	// slog.Default() -- see buildContextFabricGraphReader's own comment on
	// why that distinction matters), so the WARN line
	// RecordSynthesisStatusOverride still emits is gated by this run's
	// actual log level, not an unconfigured default.
	traceCapture := &twoTurnTraceCapture{
		SlogEngineTelemetry: contextfabric.NewSlogEngineTelemetry(logger),
		// CHAOS-4155 Phase 2: tee every resolution trace event to the SAME
		// production SlogResolutionTracer open.go would otherwise install
		// by default (see twoTurnTraceCapture.slogTee's own doc comment) --
		// gated by this run's actual cfg.LogLevel-derived logger, so a
		// measurement run with ACR_TEST_TRIAL_LOG_LEVEL=debug can grep a
		// shard's own .gotest.log for confirmed_kind_scope/vector_census_*
		// the same way any other production Debug-level stage is observed.
		slogTee: graphrank.NewSlogResolutionTracer(logger),
	}
	options := hosted.Options{
		ServiceVersion: "chaos-3742-two-turn", Logger: logger, Now: time.Now,
		ResolutionTracer: traceCapture,
		// CHAOS-4103: same in-process capture object also implements
		// contextfabric.EngineTelemetry (embedding + one override, see
		// twoTurnTraceCapture's own doc comment) -- reusing it here keeps
		// the reset() this harness already calls before every observed
		// call in sync for both signals, rather than adding a second
		// object with its own reset discipline to remember.
		EngineTelemetry: traceCapture,
	}
	caseTimeout := 240 * time.Second
	// modelCallCapture (CHAOS-4058, observability only) records each
	// InterpretQuestion/SynthesizeAnswer round-trip's wall-clock duration at
	// the harness's own wait-for-response site -- see
	// twoTurnTimedModelRuntime's own doc comment (generative_trial_live_test.go).
	// Wired into options.ModelRuntimeOverride only for the file-exchange
	// transport below; a real_api run leaves it empty (no samples ever
	// recorded), so every twoTurnArmTiming.ResponderCall* field reads zero
	// for that transport -- documented on that type, never silently
	// misread as "zero calls made".
	modelCallCapture := &twoTurnModelCallCapture{}
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
		t.Logf("using the FILE-EXCHANGE diagnostic transport at %s (case budget %s, session %s)", exchangeDir, caseTimeout, exchangeRuntime.nonce)
	}
	rt, err := hosted.Open(ctx, cfg, options)
	if err != nil {
		t.Fatalf("open hosted runtime: %v", err)
	}
	// CHAOS-4100 graph-lifecycle proof (mirrors chaos3884_replay_harness_test.go's
	// own "THE PROOF" comment exactly): hosted.Open above already wires its OWN
	// epochResolver into the engine internally via buildGraphLifecycleResolver
	// (open.go) -- driven by the SAME pglifecycle.EnvEnabled var wireProductionEnv
	// re-exports for every trial test sharing it -- but that internal resolver
	// is not exposed for this harness's own provenance. This second, independent
	// resolver instance
	// (built via chaos3884's own buildReplayEpochResolver, same package, same
	// DSN/org, same pglifecycle.ConfigFromEnv gate) queries the SAME graph
	// lifecycle table the engine just read, so its answer is a real read-proof
	// of what the engine itself saw -- never a guess, never merely logged. Runs
	// even when the flag is off (nil-safe, matches chaos3884's own convention):
	// reports resolved_active_epoch=0, GraphLifecycleEnabled=false, byte-
	// identical to every run before this fix existed.
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
			t.Fatalf("%s is set but ResolveActiveEpoch(%s) = %d -- want a positive epoch (a lifecycle row exists but reports no flip, or the flag is on against the wrong organization); refusing to silently measure epoch 0 under a flag that claims otherwise", pglifecycle.EnvEnabled, orgID, resolvedActiveEpoch)
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
	store, ok := rt.Dependencies.Runtime.InvestigationResults.(twoTurnResultStoreSaver)
	if !ok || rt.Dependencies.Runtime.InvestigationResults == nil {
		t.Fatal("investigation result store is nil or does not satisfy the harness's Save signature -- the confirmed_wrong arm's anchor constructor cannot run without it")
	}

	// Independent ClickHouse client for the LIVE anchor-term lookup
	// (buildAnchorTermIndex's own doc comment) -- mirrors
	// chaos3884_replay_harness_test.go's own construction exactly; hosted.Open
	// does not expose its internal ClickHouse client, so this harness builds
	// its own, same as that harness does for the identical reason.
	tlsConfig, err := runtimeclickhouse.TLSConfigFromCABundle(cfg.ClickHouseCACertPath)
	if err != nil {
		t.Fatalf("clickhouse TLS config: %v", err)
	}
	clickhouseClient, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: cfg.ClickHouseDSN, TLS: tlsConfig, MaxBytesToRead: cfg.ClickHouseMaxBytesToRead,
	})
	if err != nil {
		t.Fatalf("open clickhouse client: %v", err)
	}
	defer func() { _ = clickhouseClient.Close() }()
	identityUniverse := func(ctx context.Context, orgID string) ([]graphrank.IdentityRow, time.Time, bool, error) {
		return devhealthsource.IdentityUniverse(ctx, clickhouseClient, orgID)
	}
	anchorTerms, err := buildAnchorTermIndex(ctx, identityUniverse, orgID)
	if err != nil {
		t.Fatalf("build anchor term index: %v -- the subject_anchor confirmed_wrong arm cannot construct a redeemable negative without a live identity-universe read", err)
	}
	t.Logf("anchor term index: %d rows indexed", len(anchorTerms))

	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}

	// preflightAnchorCausalChain (CHAOS-3742 stop-order closure argument,
	// 2026-08-21): fail fast, in one case, before spending hours on the
	// other 49 -- see that function's own doc comment for the full
	// rationale. requireAnchorFlag=true whenever the operator asked for
	// the flag; a flag that was requested but never reached config.Load()
	// is exactly RUN 3's own root cause, and this run must not silently
	// repeat it.
	if err := preflightAnchorCausalChain(ctx, investigator, store, principal, cfg,
		os.Getenv("ACR_TEST_TRIAL_ANCHOR_MEMBERSHIP_ENABLED") != "", anchorTerms, annex, corpus, caseTimeout, runToken); err != nil {
		t.Fatalf("%v", err)
	}

	transportLabel, responderModel, responderTransport, responderEffort := twoTurnResponderProvenance(t, exchangeDir)
	report := twoTurnReport{
		ReportSchemaVersion: reportSchemaVersion,
		Provenance: trialProvenance{
			CorpusSHA256: corpusHash, Transport: transportLabel, RunStartedAt: runStartedAt,
			SourceCommit: source.commit, SourceDirty: source.dirty, SourceDiffDigest: source.diffDigest,
			AnchorMembershipOffersEnabled: cfg.AnchorMembershipOffersEnabled,
			ResponderModel:                responderModel,
			ResponderTransport:            responderTransport,
			ResponderEffort:               responderEffort,
			// DataPlane* (CHAOS-4186 follow-up, schema v28): read directly
			// from the env vars scripts/trial/common.sh already resolved
			// and exported for this exact purpose -- never re-derived,
			// never a credential (PG/CH/Falkor HOSTS only).
			DataPlane:           os.Getenv("ACR_TEST_TRIAL_DATA_PLANE"),
			DataPlanePGHost:     os.Getenv("ACR_TEST_TRIAL_PG_HOST"),
			DataPlaneCHHost:     os.Getenv("ACR_TEST_TRIAL_CH_HOST"),
			DataPlaneFalkorHost: os.Getenv("ACR_TEST_TRIAL_FALKOR_HOST"),
			// ResolvedActiveEpoch/GraphLifecycleEnabled (CHAOS-4100, the
			// 2026-08-23 graph-rebuild incident): the SECOND trial script to
			// populate these -- see their own doc comment (generative_trial_live_test.go)
			// for why they were always zero/false here before this fix, and
			// the read-proof comment above for how these two exact values
			// were obtained (never a guess at what the flag claims).
			ResolvedActiveEpoch:   resolvedActiveEpoch,
			GraphLifecycleEnabled: epochResolver != nil,
			// ExecutionShape (CHAOS-4033 follow-up, team-lead ruling
			// 2026-08-21): defaults to "sequential" here -- an unsharded run
			// WRITES this explicitly rather than relying on absence-means-
			// sequential (the field's own omitempty tag previously left it
			// out of the JSON entirely for every run before sharding
			// existed). Absence was unambiguous under schema v6 in
			// practice (every artifact so far predates sharding, or is
			// itself the sharded case, which always sets "parallel" below),
			// but an implicit default is a fails-toward-fine SHAPE -- a
			// future consumer reading an old field name or a stale schema
			// version could silently treat a missing key as "assume
			// sequential" for the WRONG reason. Overwritten to "parallel"
			// by the ACR_TEST_TRIAL_SHARD_COUNT block below when sharding
			// is active.
			ExecutionShape: "sequential",
		},
		BaseSHA:         source.commit,
		OracleAnnexPath: annexPath, OracleAnnexCorpusSHA: annex.CorpusSHA256, OracleAnnexSignedOff: annex.SignedOff,
		AnnexSignoffStale:           annex.SignoffStale,
		OfferMissCount:              map[string]int{},
		MutationProbesTripped:       map[string]int{},
		MutationProbesRun:           map[string]int{},
		ConfirmedWrongRedeemedCount: map[string]int{},
	}

	// ACR_TEST_TRIAL_SHARD_COUNT/ACR_TEST_TRIAL_SHARD_INDEX (CHAOS-4033):
	// split annex.Entries across N independent processes, one per isolated
	// environment, so the CASE axis parallelizes safely. This is
	// deliberately NOT arm-level parallelism: the four twoTurnArm values
	// run sequentially in-process for every entry and have real same-case
	// data dependencies (confirmed_wrong reads back a result positive
	// wrote via seedAnchorNegativeResult; mutation's
	// stale_superseded_offer probe needs positive's redeemed offer from
	// the SAME entry) -- there is no per-arm invocation knob to isolate,
	// and building one would mean re-running turn 1 independently per arm
	// or serializing result state across process boundaries. Corpus
	// entries, by contrast, are independent of each other once each
	// isolated environment gets its own fresh org/DB.
	//
	// SCOPE NOTE (codex round-3 finding, MEDIUM): this commit adds ONLY
	// the shard-selection knob and the per-shard coverage-gate skip below
	// (see "sharded" near the end of this function) -- it does NOT ship a
	// merge tool. A single sharded process's own artifact is therefore NOT
	// standalone valid evidence of anything: its coverage gates are
	// intentionally silent, and nothing yet re-checks them over the union
	// of shards. Do not invoke ACR_TEST_TRIAL_SHARD_COUNT for a real
	// measurement run until scripts/trial/run-two-turn-parallel.sh (the
	// CHAOS-4033 follow-up PR) exists and its merge step is what actually
	// gates validity -- until then this knob is dormant, exercised only by
	// this package's own shard-selection tests.
	//
	// Round-robin by entry.Index -- CORPUS CASE index, not the entry's
	// position in annex.Entries (live 4-way validation-run finding,
	// CHAOS-4033 follow-up): loadTwoTurnOracleAnnex expands each on-disk
	// corpus case into one twoTurnOracleEntry PER MEMBER it tests
	// (kind/anchor/window/handle), so annex.Entries typically has several
	// consecutive entries sharing the SAME Index. Sharding by POSITION
	// used to split those sibling entries across DIFFERENT shards --
	// harmless for simple per-entry sums, but the report's own
	// controlSeen-style bookkeeping (chaos3742_two_turn_confirmation_test.go,
	// ControlsTotal/ControlsWitnessed) dedups by corpus Index WITHIN one
	// process, so a control case whose members landed in N different
	// shards got counted N times once those shards' scalar counts were
	// simply summed at merge time -- a live validation run measured this
	// directly: controls_total 55 (4-way, position-sharded) vs 19
	// (sequential control), a ~2.9x overcount with zero live-model
	// dependency to explain it (D0 is a pure corpus-structural fact,
	// tc.ExpectID=="", so it must be byte-identical across ANY run of the
	// same corpus). Sharding by entry.Index instead guarantees every
	// entry belonging to the same corpus case -- all its members --
	// stays in ONE shard together, which is what the whole isolation
	// story here always assumed ("each isolated environment gets its own
	// fresh org/DB" per corpus CASE, not per corpus-case-and-member) and
	// makes every Index-keyed dedup correct via simple summation again,
	// closing this bug class at the root rather than patching one metric.
	// Corpus indices are small dense integers (0..len(corpus)-1), so this
	// still distributes evenly across shards. Applied BEFORE
	// ACR_TEST_TRIAL_LIMIT below so a limited dry run bounds EACH shard
	// independently, not the pre-shard total.
	entries := annex.Entries
	// codex round-1 finding: ACR_TEST_TRIAL_SHARD_INDEX set with
	// ACR_TEST_TRIAL_SHARD_COUNT unset used to be silently ignored (ran
	// the whole annex, unsharded) -- a partially configured parallel run
	// must fail closed, not fall back to "sequential" without saying so.
	if raw := os.Getenv("ACR_TEST_TRIAL_SHARD_INDEX"); raw != "" && os.Getenv("ACR_TEST_TRIAL_SHARD_COUNT") == "" {
		t.Fatalf("ACR_TEST_TRIAL_SHARD_INDEX=%q is set but ACR_TEST_TRIAL_SHARD_COUNT is not -- both or neither", raw)
	}
	// twoTurnShardCaseIndices (below) is only ever CALLED inside the
	// ACR_TEST_TRIAL_SHARD_COUNT block a few lines down -- so
	// ACR_TEST_TRIAL_SHARD_CASE_INDICES set WITHOUT SHARD_COUNT (and its
	// required partner SHARD_INDEX) is never read at all, and this run
	// silently executes the FULL corpus instead of the launcher's named
	// slice. Documented env trap, recurred across three separate lanes --
	// fail closed instead of relying on the operator to notice a coverage
	// artifact that looks plausible but measured the wrong cases.
	if err := twoTurnShardCaseIndicesEnvTripleError(); err != nil {
		t.Fatal(err)
	}
	// forceTraceIndices (CHAOS-4183 phase "2c") is read UNCONDITIONALLY,
	// independent of sharding -- a debug capture request is orthogonal to
	// how the run's own case set was assigned. See
	// twoTurnForceTraceIndices' own doc comment.
	forceTraceIndices := twoTurnForceTraceIndices(t)
	// explicitIndices is declared out here so the post-limit provenance
	// block below can still consult what the launcher ASKED for after the
	// shard block's own scope has closed.
	var explicitIndices []int
	if raw := os.Getenv("ACR_TEST_TRIAL_SHARD_COUNT"); raw != "" {
		shardCount, cerr := strconv.Atoi(raw)
		if cerr != nil || shardCount <= 0 {
			t.Fatalf("ACR_TEST_TRIAL_SHARD_COUNT must be a positive integer, got %q", raw)
		}
		indexRaw := requireEnv(t, "ACR_TEST_TRIAL_SHARD_INDEX")
		shardIndex, ierr := strconv.Atoi(indexRaw)
		if ierr != nil || shardIndex < 0 || shardIndex >= shardCount {
			t.Fatalf("ACR_TEST_TRIAL_SHARD_INDEX must be an integer in [0, %d), got %q", shardCount, indexRaw)
		}
		// codex round-1 finding: capacity (len(entries)+shardCount-1)/shardCount
		// overflows int for a huge shardCount (e.g. MaxInt64), producing a
		// negative make() capacity and a makeslice panic. len(entries) is
		// always a safe, overflow-free upper bound regardless of shardCount.
		// CHAOS-4100: an EXPLICIT case list takes precedence over the
		// modulo rule below.
		//
		// Modulo cannot express "the s-th contiguous chunk of an arbitrary
		// annex index set", which is what per-case granularity needs over a
		// SPARSE annex. For the full ext65 annex (indices 0..64) modulo
		// happens to give a clean 1:1 at shardCount=65, but for a narrower
		// annex -- ext15's indices 50..64, say -- shardCount=65 would spin
		// fifty EMPTY shards: fifty databases, fifty responders and fifty
		// go test processes that run no case at all.
		//
		// When the variable is absent the modulo path below runs unchanged,
		// so every pre-CHAOS-4100 invocation selects byte-identical cases.
		var explicitSet map[int]struct{}
		explicitIndices, explicitSet = twoTurnShardCaseIndices(t)
		filtered := make([]twoTurnOracleEntry, 0, len(entries))
		for _, entry := range entries {
			if explicitSet != nil {
				if _, wanted := explicitSet[entry.Index]; wanted {
					filtered = append(filtered, entry)
				}
				continue
			}
			// Go's % follows the dividend's sign, so a malformed
			// negative Index would otherwise silently match NO shard
			// (never a non-negative shardIndex) instead of surfacing at
			// the existing bounds check below (which only runs on the
			// post-shard, already-filtered entries) -- normalize into
			// [0, shardCount) so a bad entry still lands in some shard
			// and gets caught there with a clear fatal error, not
			// dropped silently before ever reaching that check.
			if ((entry.Index%shardCount)+shardCount)%shardCount == shardIndex {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
		report.Provenance.ExecutionShape = "parallel"
		report.Provenance.ShardIndex = &shardIndex
		report.Provenance.ShardCount = &shardCount
		// CaseIndices is NOT derived here: ACR_TEST_TRIAL_LIMIT can still
		// truncate `entries` below, and an index recorded before that
		// truncation is a case this shard claims to have run and did not.
		// The merge step UNIONS these claims into the merged artifact's
		// authoritative coverage record, so a limited run would present
		// itself as full-coverage. Derived after the limit instead -- see
		// the assignment below it.
		report.Provenance.Sharding.Granularity = twoTurnEnvInt(t, "ACR_TEST_TRIAL_SHARD_GRANULARITY")
		report.Provenance.Sharding.ConcurrencyCap = twoTurnEnvInt(t, "ACR_TEST_TRIAL_SHARD_CONCURRENCY_CAP")
		report.Provenance.Sharding.ProvisioningMode = twoTurnShardProvisioningMode(t)
		report.Provenance.Sharding.DatabaseProvisionMillis = int64(twoTurnEnvInt(t, "ACR_TEST_TRIAL_SHARD_DB_PROVISION_MS"))
	}

	// ACR_TEST_TRIAL_LIMIT bounds how many annex entries THIS PROCESS'S
	// share of the run (the whole annex when unsharded, or this shard's
	// own slice above) processes (codex round-1 finding #11: run-two-turn.sh
	// already exports this, but nothing read it). Mirrors
	// TestGenerativeTrialCorpus's own limit semantics: cap, never reorder.
	if raw := os.Getenv("ACR_TEST_TRIAL_LIMIT"); raw != "" {
		limit, lerr := strconv.Atoi(raw)
		if lerr != nil || limit <= 0 {
			t.Fatalf("ACR_TEST_TRIAL_LIMIT must be a positive integer, got %q", raw)
		}
		if limit < len(entries) {
			entries = entries[:limit]
		}
	}

	// CHAOS-4100: the launcher/annex closure check. CaseIndices itself is
	// NOT derived here -- see twoTurnCaseIndicesFromResults at the report
	// assembly below for why it comes from the ROWS instead.
	if report.Provenance.ExecutionShape == "parallel" {
		plannedIndices := twoTurnDistinctCaseIndices(entries)
		// A requested index this shard did not run is a LAUNCHER/annex
		// disagreement, and it fails the run rather than shrinking it
		// silently: the launcher computes each chunk from the annex
		// itself, so a mismatch means the two read different files, under
		// which the merged union no longer covers the corpus and every
		// population bar is measured against the wrong denominator.
		//
		// Checked only when no limit is in force. A limit is an OPERATOR
		// deliberately running less than the shard was given, which is a
		// dry run rather than a disagreement -- failing it would make
		// ACR_TEST_TRIAL_LIMIT unusable with an explicit case list.
		if explicitIndices != nil && os.Getenv("ACR_TEST_TRIAL_LIMIT") == "" {
			ran := make(map[int]struct{}, len(plannedIndices))
			for _, index := range plannedIndices {
				ran[index] = struct{}{}
			}
			missing := make([]int, 0, len(explicitIndices))
			for _, index := range explicitIndices {
				if _, ok := ran[index]; !ok {
					missing = append(missing, index)
				}
			}
			if len(missing) > 0 {
				t.Fatalf("ACR_TEST_TRIAL_SHARD_CASE_INDICES names %d case index(es) this shard did not run: %v -- the launcher and this process disagree about the corpus, so the merged union would not cover it", len(missing), missing)
			}
		}
	}

	// windowBandByIndex sources the confirmed-window PRECONDITION
	// runTwoTurnInferredTierArm's kind/handle pair now requires (CHAOS-4040/
	// CHAOS-3742 window-gate ruling, team-lead, 2026-08-21 -- see that
	// function's own doc comment): each case's window-member oracle entry
	// carries its own POSITIVE window band, looked up here from the FULL
	// annex.Entries (never the possibly-ACR_TEST_TRIAL_LIMIT-truncated
	// entries slice above -- a case's window entry must resolve regardless
	// of where the limit cut the kind/handle entries list).
	windowBandByIndex := map[int]string{}
	for _, entry := range annex.Entries {
		if entry.Member == string(contractsv1.ContextFabricStructureNeedWindow) {
			windowBandByIndex[entry.Index] = entry.PositiveWindowBand
		}
	}

	// applicableMembers/redeemedApplicableMembers back the PER-MEMBER
	// anti-vacuity check (codex round-1 finding #4): a member is
	// "applicable" iff the annex designates at least one committable
	// negative for it, and the run is valid only once EVERY applicable
	// member has actually redeemed one.
	applicableMembers := map[string]bool{}
	enumerableKinds := enumerableAnchorKinds(anchorTerms)
	for _, entry := range entries {
		if !entry.NegativeCommittable {
			continue
		}
		if entry.Member == string(contractsv1.ContextFabricStructureNeedSubjectAnchor) {
			// Scoped to enumerable kinds (orchestrator ruling 2026-08-20):
			// a negative naming a kind IdentityUniverse never enumerates
			// can NEVER be seeded, by construction -- that is a scope fact
			// about this deployment's own identity-universe coverage, not
			// something anti-vacuity should be held to.
			if !enumerableKinds[entry.NegativeKind] {
				report.AnchorNonEnumerableKindExcludedCount++
				continue
			}
			report.AnchorAntiVacuityDenominator++
		}
		applicableMembers[entry.Member] = true
	}
	// controlSeen/controlOK back the DP9 "controls X/19" report (CHAOS-3742
	// run-2 root cause, team-lead ruling 2026-08-20, design brief lines
	// 958/960). A control is a corpus case with no expected answer
	// (trialCase.ExpectID=="" -- the SAME definition
	// generative_trial_live_test.go's own IsControl already uses, not a
	// new band-based one).
	//
	// "OK" is brief-conformant: zero commits at turn 1 AND turn 1
	// DISCLOSED structure (StructureNeeds or WindowClarification present)
	// -- brief line 958's "clarify or no_match terminals" are BOTH
	// acceptable. The run-2 proxy this replaced required literal turn 1
	// Status==no_match specifically, which undercounted: real control
	// questions overwhelmingly surface at least one plausible-but-
	// unconfident candidate and correctly land on clarification_required
	// (unresolved.go's resolveTerminalStatus never returns no_match for a
	// non-empty candidate pool once AllowClarification=true, which every
	// harness in this package sets), so the old proxy scored every one of
	// those as a miss even though nothing wrongly committed.
	//
	// controlNoMatchCensusBacked separately backs
	// ControlsWitnessedNoMatchCensusBacked, the narrower/stronger claim
	// brief line 960 makes on top of "ok": literal no_match AND the
	// CensusFunc-gated evidence round actually ran for that call
	// (traceCapture.censusRan(), reset/read around the turn 1 call below
	// the SAME sequential-single-caller way runTwoTurnInferredTierArm
	// already does around its own call) -- informational only, no pass/
	// fail bar; see ControlsWitnessedNoMatchCensusBacked's own doc comment
	// for why it is expected to be near-zero by construction.
	controlSeen := map[int]bool{}
	controlOK := map[int]bool{}
	controlNoMatchCensusBacked := map[int]bool{}
	for _, entry := range entries {
		if entry.Index < 0 || entry.Index >= len(corpus) {
			t.Fatalf("oracle annex entry names index %d, corpus has %d cases", entry.Index, len(corpus))
		}
		tc := corpus[entry.Index]
		// Recorded BEFORE the call, from the corpus alone -- never gated
		// on success (codex round-1 finding: the prior version only added
		// a control to the denominator AFTER a successful turn 1, so an
		// investigator error on a control case silently shrank
		// ControlsTotal instead of counting as unwitnessed. A limited or
		// partially-erroring run must not be able to report a
		// vacuously-true X/X).
		if tc.ExpectID == "" {
			controlSeen[entry.Index] = true
		}
		// CHAOS-4165: recorded the SAME way as controlSeen above -- from
		// the annex alone, unconditionally, before any live call -- the
		// mutation arm's own scheduling precondition further down
		// (entry.Member != window, alongside positive.Applied) is
		// mirrored here minus the Applied half, so this counts the
		// CEILING regardless of what this run's own positive arm does.
		//
		// codex review finding (P2, confirmed, round 2): entry.Member !=
		// window alone is not enough, and neither is a whole-struct
		// positiveQuery() zero-value test -- adaptSignedOracleAnnex can
		// leave a negative-only entry with SOME positive fields
		// unconditionally populated (a subject_handle entry derives
		// PositiveKind/PositiveHandlePatternID from the case's own kind
		// regardless of whether THIS member has a positive oracle at
		// all), so a partial-field entry can read as eligible even though
		// selectOracleOffer's own per-member match can never satisfy it.
		// hasConstructiblePositiveOffer mirrors selectOracleOffer's own
		// switch, field-by-field, so this can never drift from what that
		// function actually requires to match.
		if entry.Member != string(contractsv1.ContextFabricStructureNeedWindow) && entry.hasConstructiblePositiveOffer() {
			report.MutationProbeEligibleCount++
		}

		turn1Req := twoTurnRequest(entry.Index, tc, "turn1")
		if traceCapture != nil {
			traceCapture.reset()
		}
		modelCallCapture.reset()
		turn1Started := time.Now()
		turn1Ctx, turn1Cancel := context.WithTimeout(ctx, caseTimeout)
		turn1, err := investigator.Investigate(turn1Ctx, principal, turn1Req)
		turn1Cancel()
		// turn1SynthesisOverride (CHAOS-4103, codex review round 3,
		// confirmed): turn 1 is itself a real Investigate() call and can
		// independently trip applySynthesisStatusOverride, but it produces
		// no twoTurnCaseResult row of its own -- every arm below shares
		// this ONE turn-1 answer, so its override (if any) is folded into
		// EVERY row this case produces, not just one arm. Captured
		// immediately, before any arm resets the shared capture for its
		// own call.
		var turn1SynthesisOverride *contextfabric.SynthesisStatusOverrideOutcome
		if traceCapture != nil {
			turn1SynthesisOverride = traceCapture.synthesisOverride
		}
		// turn1Facts (CHAOS-4120): mirrors turn1SynthesisOverride's own
		// capture discipline immediately above -- turn 1's own decision/
		// kind-coverage/search-truncation trace and StructureNeeds/pool-
		// membership facts, captured here before any arm resets the shared
		// trace capture for its own call, and stamped onto EVERY row this
		// case produces below (see twoTurnStampTurn1Facts's own doc
		// comment for why this closes the offer-miss "only turn-2 is
		// stamped" gap).
		turn1Facts := twoTurnCaptureTurn1Facts(traceCapture, turn1, tc)
		// CHAOS-4183 phase "2c": captured HERE, before the first arm's own
		// trace.reset() below -- the SAME "before the reset" urgency
		// turn1SynthesisOverride's own capture above already documents. See
		// twoTurnCaptureForcedTurn1Trace's own doc comment for the
		// debug-only, local-artifacts-only discipline this exists under.
		turn1Facts.TraceEvents = twoTurnCaptureForcedTurn1Trace(traceCapture, entry.Index, forceTraceIndices)
		// CHAOS-4307: fold turn 1's own confirmed_kind_scope events into the
		// report's run-level census rollup HERE -- before the first arm's
		// own trace.reset() below, the identical "before the reset" urgency
		// turn1SynthesisOverride/turn1Facts's own captures above already
		// document -- and reading traceCapture directly (not
		// turn1Facts.TraceEvents, which is nil on every non-forced-trace
		// case) so this rollup is never gated behind the debug-only
		// forceTraceIndices discipline. Exactly once per case: turn1Facts
		// itself gets stamped onto every arm's row below
		// (twoTurnStampTurn1Facts), so folding from any of THOSE rows
		// instead would multiply-count turn 1's contribution once per arm.
		if traceCapture != nil {
			foldConfirmedKindVectorCensus(&report, traceCapture.snapshot())
		}
		turn1Timing := buildTwoTurnArmTiming("turn1", turn1Started, modelCallCapture)
		// positiveTiming/inferredTiming/confirmedWrongTiming/mutationTiming
		// (CHAOS-4058) default to zero-duration/zero-call records naming
		// their own arm, declared here (before either early-`continue`
		// below) so a turn-1 error or a disclosure-absent case still
		// records a Timings entry for the case -- codex round-1 finding:
		// the call that actually consumed the time (turn 1) must not go
		// missing from the timing artifact just because the REST of the
		// case never ran. Reassigned (never redeclared) at each arm's own
		// call site below once it actually runs.
		positiveTiming := twoTurnArmTiming{Arm: string(twoTurnArmPositive)}
		inferredTiming := twoTurnArmTiming{Arm: string(twoTurnArmInferredTier)}
		confirmedWrongTiming := twoTurnArmTiming{Arm: string(twoTurnArmConfirmedWrong)}
		mutationTiming := twoTurnArmTiming{Arm: string(twoTurnArmMutation)}
		caseTiming := func() twoTurnCaseTiming {
			return twoTurnCaseTiming{
				Index: entry.Index, Member: entry.Member,
				Arms: []twoTurnArmTiming{turn1Timing, positiveTiming, inferredTiming, confirmedWrongTiming, mutationTiming},
			}
		}
		if err != nil {
			t.Logf("case %d: turn 1 error: %v", entry.Index, err)
			report.Timings = append(report.Timings, caseTiming())
			continue
		}
		report.CasesRun++
		if turn1Facts.Regime == twoTurnRegimeAWindowGated && twoTurnRegimeAOfferComposed(turn1) {
			report.RegimeAOfferComposedCount++
		}
		// codex round-2 finding #1: StructureNeeds and WindowClarification
		// are composed INDEPENDENTLY on the subjectless-terminal path
		// (unresolved.go) -- window is never added to
		// StructureOfferMaterial.Missing (structure.go's composeStructureNeeds
		// only tracks kind/anchor/handle), so a window-only stalled case can
		// have StructureNeeds==nil while WindowClarification is non-nil.
		// Computed once, shared by the control-ok check above and the
		// skip-to-next-entry check below -- the same disclosure fact
		// either way.
		disclosurePresent := turn1.StructureNeeds != nil || turn1.WindowClarification != nil
		// WindowClassDefaultGatedCount (CHAOS-4040 harness follow-up, gate
		// 2 coverage -- see twoTurnReport's own doc comment for why turn
		// 1's WindowClarification can ONLY originate from gate 2). Counted
		// on EVERY case, control or not: gate 2 is question-content-driven
		// (composeEffectiveWindow's own ClassifyWindow), not member/arm-
		// scoped, so a control case classifying to a window is just as
		// much gate-2 evidence as any other.
		//
		// !turn1.Reused is required (codex xhigh review round 2, confirmed
		// HIGH-confidence): tryReuse runs BEFORE gate 2 in Investigate
		// (engine.go), and answer_reuse.go's FindReusable only rejects
		// DECISIVE (Complete/Partial/Degraded) candidates carrying an
		// inferred window -- it says nothing about a clarification_required
		// row, which pre-#181 could ALREADY carry a non-nil
		// WindowClarification (the old, now-permanently-dead decisive-path
		// composeWindowClarification call, engine.go) and was saved with a
		// real (non-nil) watermark/epoch under the pre-#181 rules, so it
		// stays reuse-eligible today. Without this guard, a persistent
		// store carrying pre-#181 rows (a long-lived trial Postgres
		// instance, not a fresh-per-run store) could let this bar pass by
		// REPLAYING an old row, proving nothing about gate 2 actually
		// running on THIS call -- exactly the causal gap this whole field
		// exists to close for the gate-1 arm, reintroduced through reuse
		// on this gate-2 signal if unguarded.
		if turn1.WindowClarification != nil && !turn1.Reused {
			report.WindowClassDefaultGatedCount++
		}
		if tc.ExpectID == "" {
			if len(turn1.SubjectResolution.Committed) == 0 && disclosurePresent {
				controlOK[entry.Index] = true
			}
			if turn1.Status == contractsv1.ContextFabricInvestigationNoMatch && traceCapture != nil && traceCapture.censusRan() {
				controlNoMatchCensusBacked[entry.Index] = true
			}
		}
		// Skipping on StructureNeeds alone would silently drop every
		// window-only case from every arm.
		if !disclosurePresent {
			report.StructureAndWindowDisclosureAbsentCount++
			report.Timings = append(report.Timings, caseTiming())
			continue
		}

		modelCallCapture.reset()
		positiveStarted := time.Now()
		// CHAOS-4234: on a regime-A case the positive arm's turn 2 also
		// redeems the oracle's window receipt -- see
		// twoTurnCaseResult.Turn2WindowReceiptAttached's own doc comment.
		regimeAWindowBand := ""
		if turn1Facts.Regime == twoTurnRegimeAWindowGated {
			regimeAWindowBand = windowBandByIndex[entry.Index]
		}
		positive := runTwoTurnPositiveArm(t, ctx, investigator, principal, entry.Index, tc, entry, turn1, caseTimeout, traceCapture, regimeAWindowBand)
		twoTurnFoldSynthesisStatusOverride(&positive, turn1SynthesisOverride)
		twoTurnStampTurn1Facts(&positive, turn1Facts)
		positiveTiming = buildTwoTurnArmTiming(string(twoTurnArmPositive), positiveStarted, modelCallCapture)
		if positive.OfferMiss {
			report.OfferMissCount[entry.Member]++
		}
		if positive.Applied {
			// PositiveAppliedCount (pre-existing, pooled across every
			// member) does NOT check turn1.Reused/positive.Reused --
			// unchanged by this follow-up, since its own bar only ever
			// claimed "conversion happened", never "gate 2 ran fresh" the
			// way the window-specific counters below do.
			report.PositiveAppliedCount++
			// WindowPositiveAppliedCount (see its own doc comment): the
			// winr_ positive-control proof, scoped to window specifically
			// so it cannot hide behind kind/handle/anchor conversions the
			// way the pooled PositiveAppliedCount bar above can.
			//
			// !turn1.Reused && !positive.Reused (codex xhigh review round
			// 3, confirmed HIGH confidence): answer-reuse only rejects
			// DECISIVE candidates carrying an INFERRED window
			// (answer_reuse.go) -- a CONFIRMED-window decisive row is
			// fully reuse-eligible. Without both guards, this bar could be
			// satisfied by a stale row (turn 1's offer replayed, or turn
			// 2's own confirmed decisive answer replayed) that this run's
			// redemption code never actually produced -- the same
			// reuse-vacuity class round 2 found on WindowClassDefaultGatedCount,
			// reapplied here to both calls this arm makes.
			if entry.Member == string(contractsv1.ContextFabricStructureNeedWindow) &&
				positive.CommittedCount > 0 && !turn1.Reused && !positive.Reused {
				report.WindowPositiveAppliedCount++
			}
		}
		if positive.CommittedCount > 0 {
			report.GateReachableCount++
		}
		if positive.WrongCommit {
			report.WrongCommitCount++
		}
		// FalseNoMatchCount (CHAOS-4120): the positive arm's own contribution
		// to this widened, no-longer-inferred_tier-only bar -- see
		// FalseNoMatch's own doc comment (twoTurnCaseResult).
		if positive.FalseNoMatch {
			report.FalseNoMatchCount++
		}
		if turn1Facts.Regime == twoTurnRegimeAWindowGated && twoTurnStatusAnswered(positive.Turn2Status) {
			report.RegimeATurn2AnsweredCount++
		}
		// CHAOS-4307: fold BEFORE the tail redaction pass
		// (twoTurnRedactNonAnomalousTraceEvents) clears positive.TraceEvents
		// on every non-anomalous row -- see foldConfirmedKindVectorCensus's
		// own doc comment.
		foldConfirmedKindVectorCensus(&report, positive.TraceEvents)
		report.Results = append(report.Results, positive)

		modelCallCapture.reset()
		inferredStarted := time.Now()
		inferred := runTwoTurnInferredTierArmWithPairRetry(t, ctx, investigator, principal, entry.Index, tc, entry, caseTimeout, traceCapture, windowBandByIndex[entry.Index])
		twoTurnFoldSynthesisStatusOverride(&inferred, turn1SynthesisOverride)
		twoTurnStampTurn1Facts(&inferred, turn1Facts)
		inferredTiming = buildTwoTurnArmTiming(string(twoTurnArmInferredTier), inferredStarted, modelCallCapture)
		if inferred.PairInvalid {
			report.InferredPairInvalidCount++
		}
		if inferred.PairRetried {
			report.PairRetriedCount++
		}
		if inferred.FalseNoMatch {
			report.FalseNoMatchCount++
		}
		if inferred.WrongCommit {
			report.WrongCommitCount++
		}
		if entry.Member == string(contractsv1.ContextFabricStructureNeedWindow) {
			// CHAOS-4040 harness follow-up (non-vacuity, see
			// twoTurnReport's own doc comments on these fields): window
			// gets its OWN counting block, not the CommittedCount>0-gated
			// one below -- WindowInferredTierRanCount/WindowGatedCount
			// need to be computed for EVERY ran case, not only the
			// (should-be-nonexistent, post-gate) ones that committed.
			//
			// For window specifically, ArmInvalidReason is set ONLY by
			// runTwoTurnInferredTierArm's investigate-error branch (window
			// has no PairInvalid/structural-exemption path of its own to
			// conflate it with -- that function's own doc comment), so
			// ArmInvalidReason != "" here always means the call errored.
			//
			// twoTurnClassifyWindowGateOutcome (CHAOS-4336) is extracted
			// from this block so its own logic -- specifically, which field
			// WindowGatedOfferedCount/WindowGatedSilentCount reads -- has a
			// direct unit test; see that function's own doc comment for the
			// bug it fixes.
			outcome := twoTurnClassifyWindowGateOutcome(inferred)
			if outcome.ArmError {
				report.WindowArmErrorCount++
			} else {
				report.WindowInferredTierRanCount++
				if outcome.Committed {
					report.WindowCommitCount++
				}
				if outcome.Gated {
					report.WindowGatedCount++
					switch {
					case outcome.GatedOffered:
						report.WindowGatedOfferedCount++
					case outcome.GatedAlreadyWidest:
						report.WindowGatedAlreadyWidestCount++
					default:
						report.WindowGatedSilentCount++
					}
				}
			}
		} else if inferred.ArmInvalidReason == "" && inferred.CommittedCount > 0 && !inferred.PairInvalid {
			// PairInvalid outcomes are excluded from the partition
			// entirely (InferredClassification is deliberately left
			// empty for them, never defaulted into "unjustified" --
			// see runTwoTurnInferredTierArm's own comment on why).
			report.InferredKindHandleDecisiveCount++
			switch inferred.InferredClassification {
			case "baseline_equivalent":
				report.InferredBaselineEquivalentCount++
			case "kind_insensitivity_attested":
				report.InferredKindInsensitivityAttestedCount++
			default:
				report.InferredUnjustifiedCount++
			}
		}
		// CHAOS-4307: two independent resolve calls on this arm -- the
		// hinted call (TraceEvents) and, for non-window members, the paired
		// no-hint baseline call (BaselineTraceEvents) -- both folded, same
		// "before redaction" discipline as the positive arm above.
		foldConfirmedKindVectorCensus(&report, inferred.TraceEvents)
		foldConfirmedKindVectorCensus(&report, inferred.BaselineTraceEvents)
		report.Results = append(report.Results, inferred)

		modelCallCapture.reset()
		confirmedWrongStarted := time.Now()
		confirmedWrong := runTwoTurnConfirmedWrongArm(t, ctx, investigator, store, principal, entry.Index, tc, entry, caseTimeout, anchorTerms, runToken, traceCapture)
		twoTurnFoldSynthesisStatusOverride(&confirmedWrong, turn1SynthesisOverride)
		twoTurnStampTurn1Facts(&confirmedWrong, turn1Facts)
		confirmedWrongTiming = buildTwoTurnArmTiming(string(twoTurnArmConfirmedWrong), confirmedWrongStarted, modelCallCapture)
		if confirmedWrong.ArmInvalidReason == "" && confirmedWrong.Applied && entry.NegativeCommittable {
			report.ConfirmedWrongRedeemedCount[entry.Member]++
		}
		if confirmedWrong.WrongCommit {
			report.WrongCommitCount++
		}
		// CHAOS-4307: same "before redaction" discipline as the positive arm.
		foldConfirmedKindVectorCensus(&report, confirmedWrong.TraceEvents)
		report.Results = append(report.Results, confirmedWrong)

		// Mutation/non-vacuity arm: only meaningful against a case the
		// positive arm actually converted (probe (iii) needs a real
		// supersession to have happened; probes (i)/(ii) are cheap enough
		// to run alongside it rather than standing up a separate case).
		// EXCLUDES window (codex round-3 finding): window redemption has
		// NO supersession/staleness concept at all -- window.go's own
		// windowVetoReason vocabulary is
		// {confirmation_unresolved, confirmation_conflict, axis_conflict},
		// with nothing analogous to structure's stale_superseded_offer.
		// Running probe (iii) against window could never trip by
		// construction, which would misreport as a probe DEFECT rather
		// than the structural exemption it actually is.
		// mutationTiming (CHAOS-4058, declared above alongside turn1Timing)
		// keeps its zero-duration/zero-call default -- overwritten below
		// only when the mutation arm actually runs (positive.Applied,
		// non-window member, and a redeemable offer found), so a case
		// where it structurally never ran still reports "mutation" with an
		// honest zero rather than omitting the arm from Timings entirely.
		if positive.Applied && entry.Member != string(contractsv1.ContextFabricStructureNeedWindow) {
			receiptID, found := selectOracleOffer(turn1, entry.Member, entry.positiveQuery())
			if found {
				modelCallCapture.reset()
				mutationStarted := time.Now()
				mutationResults := runTwoTurnMutationArm(ctx, investigator, principal, entry.Index, tc, entry, turn1.ResultID, receiptID, caseTimeout, traceCapture)
				mutationTiming = buildTwoTurnArmTiming(string(twoTurnArmMutation), mutationStarted, modelCallCapture)
				for _, mutationResult := range mutationResults {
					twoTurnFoldSynthesisStatusOverride(&mutationResult, turn1SynthesisOverride)
					twoTurnStampTurn1Facts(&mutationResult, turn1Facts)
					report.MutationProbesRun[mutationResult.MutationProbe]++
					if mutationResult.MutationTripped {
						report.MutationProbesTripped[mutationResult.MutationProbe]++
					}
					// CHAOS-4307: same "before redaction" discipline as the
					// other three arms above.
					foldConfirmedKindVectorCensus(&report, mutationResult.TraceEvents)
					report.Results = append(report.Results, mutationResult)
				}
			}
		}

		report.Timings = append(report.Timings, caseTiming())

		t.Logf("case %d member=%s: positive(applied=%v miss=%v) inferred(commit=%d class=%q pair_invalid=%v false_no_match=%v invalid=%q) confirmed_wrong(applied=%v wrong=%v invalid=%q) timing(turn1=%dms positive=%dms/%dcalls/%dms_max inferred=%dms/%dcalls/%dms_max confirmed_wrong=%dms/%dcalls/%dms_max mutation=%dms/%dcalls/%dms_max)",
			entry.Index, entry.Member, positive.Applied, positive.OfferMiss,
			inferred.CommittedCount, inferred.InferredClassification, inferred.PairInvalid, inferred.FalseNoMatch, inferred.ArmInvalidReason,
			confirmedWrong.Applied, confirmedWrong.WrongCommit, confirmedWrong.ArmInvalidReason,
			turn1Timing.WallDurationMS,
			positiveTiming.WallDurationMS, positiveTiming.ResponderCallCount, positiveTiming.ResponderCallMaxMS,
			inferredTiming.WallDurationMS, inferredTiming.ResponderCallCount, inferredTiming.ResponderCallMaxMS,
			confirmedWrongTiming.WallDurationMS, confirmedWrongTiming.ResponderCallCount, confirmedWrongTiming.ResponderCallMaxMS,
			mutationTiming.WallDurationMS, mutationTiming.ResponderCallCount, mutationTiming.ResponderCallMaxMS)

		// CHAOS-4062 shadow-insensitivity trace probe: a SEPARATE log line
		// (the case log line above stays byte-for-byte as it was -- append
		// only), emitted ONLY for the "unjustified" classification, printing
		// exactly the discriminating fields off CHAOS-4039's analysis --
		// whether kindInsensitivityProof was even evaluated on the hinted
		// call, its verdict when it was, and both legs' own committed
		// Kind+CanonicalID -- so a run can be read for CHAOS-4039's open
		// questions without re-deriving them from a raw trace dump.
		// Observability only: does not alter InferredClassification or any
		// other decision made above.
		if inferred.InferredClassification == "unjustified" {
			t.Logf("case %d member=%s: inferred unjustified shadow_kind_insensitivity(evaluated=%v outcome=%q mode=%q) committed(baseline=%v hinted=%v)",
				entry.Index, entry.Member,
				inferred.ShadowKindInsensitivityEvaluated, inferred.ShadowKindInsensitivityOutcome,
				inferred.ShadowKindInsensitivityMode,
				inferred.BaselineCommittedSubjects, inferred.HintedCommittedSubjects)
		}
	}
	// Per-member anti-vacuity (codex round-1 finding #4): valid only once
	// EVERY member with a designated committable negative has redeemed at
	// least one.
	var unsatisfiedMembers []string
	for member := range applicableMembers {
		report.ApplicableMembers = append(report.ApplicableMembers, member)
		if report.ConfirmedWrongRedeemedCount[member] < 1 {
			unsatisfiedMembers = append(unsatisfiedMembers, member)
		}
	}
	sort.Strings(report.ApplicableMembers)
	sort.Strings(unsatisfiedMembers)
	report.AntiVacuityValid = len(applicableMembers) > 0 && len(unsatisfiedMembers) == 0

	// CHAOS-4165: computed unconditionally here, mirroring AntiVacuityValid
	// immediately above -- see MutationProbeCoverage's own doc comment for
	// why this is only a meaningful signal at the merged, full-corpus
	// level, and mutationProbeCoverage's own doc comment for the pure
	// function shared with any future consumer.
	report.MutationProbeCoverage = mutationProbeCoverage(report.MutationProbesRun, report.MutationProbeEligibleCount)

	report.ControlsTotal = len(controlSeen)
	report.ControlsWitnessed = len(controlOK)
	report.ControlsWitnessedNoMatchCensusBacked = len(controlNoMatchCensusBacked)

	// CHAOS-4103: recomputed from report.Results, never accumulated by an
	// inline counter at each of the four arms' own append site -- the
	// override can fire on ANY arm's call, and a future fifth arm that
	// forgot to increment a running counter would silently undercount a
	// blocking defect signal. Scanning the rows this shard actually
	// produced, once, immediately before the artifact is serialized (same
	// "derived from results, not accumulated in the loop" discipline
	// twoTurnCaseIndicesFromResults below already uses), makes that
	// omission structurally impossible instead of merely unlikely.
	for _, res := range report.Results {
		if res.SynthesisStatusOverrideReason == string(contextfabric.SynthesisStatusOverrideClarificationUnavailableUncommitted) {
			report.SynthesisStatusOverrideUncommittedCount++
		}
		// FactlessCommittedCount (CHAOS-4347): SAME "derived from the rows
		// this shard actually produced, once, immediately before
		// serialization" discipline as SynthesisStatusOverrideUncommittedCount
		// immediately above, for the identical reason -- this counts across
		// every arm, and a scattered per-arm increment is exactly the shape
		// that silently undercounts when a future arm is added.
		if res.CommittedCount > 0 && res.CanonicalFactsCount == 0 {
			report.FactlessCommittedCount++
		}
		// OracleIDSchemeMismatchCount (CHAOS-4348 measurement-layer fix):
		// SAME "derived from the rows this shard actually produced, once,
		// immediately before serialization" discipline as
		// SynthesisStatusOverrideUncommittedCount/FactlessCommittedCount
		// immediately above.
		if res.OracleIDSchemeMismatch {
			report.OracleIDSchemeMismatchCount++
		}
	}

	// CHAOS-4058: run-level timing aggregate, observational only -- see
	// twoTurnArmTimingSummary's own doc comment.
	report.TimingSummary = summarizeTwoTurnTiming(report.Timings)

	// CHAOS-4100: coverage recorded from the rows this shard actually
	// produced, immediately before the artifact is serialized -- see
	// twoTurnCaseIndicesFromResults for why it is derived from results
	// rather than from any earlier entry list.
	if report.Provenance.ExecutionShape == "parallel" {
		report.Provenance.Sharding.CaseIndices = twoTurnCaseIndicesFromResults(report.Results)
	}

	// CHAOS-4135: the redaction pass. Every row staged its own decisive
	// call's (and, for inferred_tier, its paired baseline's) full trace
	// stream unconditionally as it was produced (twoTurnStampDecision,
	// runTwoTurnInferredTierArm) -- immediately before serialization,
	// exactly ONCE, every row that is not anomalous
	// (twoTurnCaseResultIsAnomalous) has both cleared back to nil. Run here
	// rather than at each arm's own return so it applies uniformly
	// regardless of which of the four arms produced the row, mirroring
	// SynthesisStatusOverrideUncommittedCount's own "derived from results,
	// not accumulated in the loop" discipline immediately above.
	twoTurnRedactNonAnomalousTraceEvents(report.Results)

	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal two-turn report: %v", err)
	}
	f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create two-turn report at %s: %v (refusing to overwrite an existing artifact)", outPath, err)
	}
	_, writeErr := f.Write(raw)
	closeErr := f.Close()
	if writeErr != nil {
		t.Fatalf("write two-turn report: %v", writeErr)
	}
	if closeErr != nil {
		t.Fatalf("close two-turn report: %v", closeErr)
	}
	t.Logf("two-turn report written to %s: cases_run=%d gate_reachable=%d wrong_commits=%d anti_vacuity_valid=%v",
		outPath, report.CasesRun, report.GateReachableCount, report.WrongCommitCount, report.AntiVacuityValid)

	// sharded (codex round-2 finding, BLOCK): every non-vacuity/coverage
	// gate below assumes THIS PROCESS ran the whole corpus. A shard split
	// by corpus case can legitimately see zero cases of a given category
	// -- no window member, no control case, no kind/handle inferred-tier
	// commit, an applicable member whose OTHER committable entries landed
	// in a different shard -- while the overall sharded run's UNION covers
	// it fine. Those coverage bars are therefore SKIPPED per shard here
	// and re-evaluated ONLY by the merge step over every shard's combined
	// data (ApplicableMembers ∪ ConfirmedWrongRedeemedCount, summed
	// CasesRun/PositiveAppliedCount/etc -- see
	// scripts/trial/run-two-turn-parallel.sh's merge step, CHAOS-4033).
	// Genuine correctness bars (a wrong commit, a false no_match, an
	// unjustified inferred-tier commit, a mutation probe that ran but
	// never tripped) are NEVER skipped: a real per-case violation is real
	// regardless of how small the shard is. sharded is false (every check
	// below unconditional, byte-identical to before this field existed)
	// whenever ACR_TEST_TRIAL_SHARD_COUNT is unset.
	sharded := report.Provenance.ExecutionShape == "parallel"

	// Fail-open guard (codex round-3 finding #2): a run where every case
	// offer-missed or errored could otherwise pass -- zero wrong commits
	// and a satisfied anti-vacuity check prove nothing when NOTHING ever
	// actually converted. This is checked BEFORE anti-vacuity so a
	// completely broken harness fails on the more fundamental signal
	// first, not last.
	if !sharded && report.PositiveAppliedCount == 0 {
		t.Errorf("positive_applied_count=0 across %d cases -- the positive arm never converted a single case, so this run proves nothing about conversion (fails open otherwise: zero wrong commits and a vacuously-true anti-vacuity check would not catch a harness that never actually confirms anything)", report.CasesRun)
	}
	if !sharded && !report.AntiVacuityValid {
		t.Errorf("confirmed_wrong arm anti-vacuity check failed: members %v redeemed zero designated committable negatives (design brief v4/sol-r3 #4) -- the arm is INVALID for this run", unsatisfiedMembers)
	}
	if report.WrongCommitCount > 0 {
		t.Errorf("wrong_commit_count=%d, want 0 (DP9: ZERO wrong commits, period)", report.WrongCommitCount)
	}
	// CHAOS-4348 measurement-layer fix: NEVER skipped, even under sharding
	// (a malformed oracle id is a per-case annex defect, real regardless of
	// shard size, same reasoning WrongCommitCount/FalseNoMatchCount already
	// get). "Fail loudly instead of silently reading absent" -- the whole
	// point of this counter existing is that Run F's project 0/20 could NOT
	// distinguish "retrieval is broken" from "the annex is broken" until
	// this field existed; a nonzero count here means this run's OTHER
	// pool/retrieval-source measurements for the affected row(s) are not
	// trustworthy and the annex needs regenerating before re-measuring.
	if report.OracleIDSchemeMismatchCount > 0 {
		t.Errorf("oracle_id_scheme_mismatch_count=%d, want 0 (the oracle annex has stale-scheme expected ids -- run cmd/acr-annex-regen-project-ids before trusting any pool/retrieval-source measurement in this report)", report.OracleIDSchemeMismatchCount)
	}
	// CHAOS-4039 v4 measurement contract (sol-max ruling 2026-08-20,
	// option (c) -- amend the bar BY MEMBER, never widen resolve.go's
	// stalled-only census gate). window RETAINS the pre-v4 literal
	// zero-commit bar unconditionally -- kept as a cheap belt-and-suspenders
	// check even though WindowGatedCount below is now the bar that actually
	// PROVES why it reads zero.
	if report.WindowCommitCount > 0 {
		t.Errorf("window_commit_count=%d, want 0 (any commit under unconfirmed inferred-tier window fails the run)", report.WindowCommitCount)
	}
	// CHAOS-4040 harness follow-up (PR #181's own companion, sol-max
	// ruling 2026-08-21): WindowCommitCount==0 is now STRUCTURALLY
	// guaranteed by the gate (see that field's own doc comment), so on its
	// own it can no longer distinguish "the gate fired" from "the arm
	// never ran" or "every case errored" -- these three bars close that
	// gap. Checked in this order: non-vacuity first (mirrors
	// PositiveAppliedCount's own ordering above), then the causal proof.
	if !sharded && report.WindowInferredTierRanCount == 0 {
		t.Errorf("window_inferred_tier_ran_count=0 -- the window inferred-tier arm never once completed across %d cases, so window_commit_count=0 proves nothing about the CHAOS-4040 gate (non-vacuity)", report.CasesRun)
	}
	if report.WindowGatedCount != report.WindowInferredTierRanCount {
		t.Errorf("window_gated_count=%d, want %d (== window_inferred_tier_ran_count): every window inferred-tier case that ran must show the CHAOS-4040 gate's own signature (clarification_required, inferred_default provenance, zero commits) -- a case reaching zero commits any other way means window_commit_count's own zero is not actually proof the gate is what stopped it", report.WindowGatedCount, report.WindowInferredTierRanCount)
	}
	if report.WindowArmErrorCount > 0 {
		t.Logf("window_arm_error_count=%d (informational, not gated -- see that field's own doc comment)", report.WindowArmErrorCount)
	}
	// Gate 2 (class-default) coverage: the arm above only ever exercises
	// gate 1 (it always injects an explicit field) -- this is gate 2's
	// only live-corpus signal, see WindowClassDefaultGatedCount's own doc
	// comment for why turn 1 alone proves it.
	if !sharded && report.WindowClassDefaultGatedCount == 0 {
		t.Errorf("window_class_default_gated_count=0 across %d cases -- gate 2 (the engine's own class-table default, CHAOS-4040) never fired once on turn 1's windowless requests, so this run has zero live-corpus evidence gate 2 works at all (non-vacuity)", report.CasesRun)
	}
	// winr_ positive control (team-lead ruling 2026-08-21, reconciling
	// against sol's full CHAOS-4040 non-vacuity list): proves the escape
	// hatch out of the gate -- a confirmed receipt reaching a real
	// decisive answer -- actually works, scoped to window specifically so
	// it cannot hide behind PositiveAppliedCount's own pooled kind/handle/
	// anchor conversions. See WindowPositiveAppliedCount's own doc comment
	// for why "removing the receipt returns to the gate" needs no separate
	// call here.
	if !sharded && report.WindowPositiveAppliedCount == 0 {
		t.Errorf("window_positive_applied_count=0 across %d cases -- window's own winr_ receipt redemption never once reached a confirmed, decisive answer this run, so this run has zero live-corpus evidence the escape hatch out of the CHAOS-4040 gate actually works (non-vacuity)", report.CasesRun)
	}
	if report.FalseNoMatchCount > 0 {
		t.Errorf("false_no_match_count=%d, want 0 (a case with a real expected answer resolved to literal no_match -- the no-match-direction mirror of a wrong commit)", report.FalseNoMatchCount)
	}
	// CHAOS-4103 (team-lead ruling 2026-08-22): a THIRD zero-tolerance bar,
	// deliberately separate from FalseNoMatchCount above -- never checked
	// with !sharded (a real per-shard occurrence is real regardless of
	// shard size, the same reasoning WrongCommitCount/FalseNoMatchCount
	// already apply). The uncommitted synthesis-status override shape is a
	// genuine engine routing bug (the subjectless terminal should have
	// composed its own clarification terminal and did not), not a soft
	// status -- loud in this artifact, never silently absorbed into an
	// ordinary no_match count.
	if report.SynthesisStatusOverrideUncommittedCount > 0 {
		t.Errorf("synthesis_status_override_uncommitted_count=%d, want 0 -- the synthesis-status override fired with zero committed subjects, which chaos4098_synthesis_status.go's own doc comment calls a genuine engine routing bug (the subjectless terminal should have composed a clarification terminal and did not), not ordinary model under-claiming", report.SynthesisStatusOverrideUncommittedCount)
	}
	// kind/handle: no longer a blanket "any commit fails" bar -- a commit
	// is only a failure when it is UNJUSTIFIED (neither baseline_equivalent
	// nor kind_insensitivity_attested) or its pairing could not even be
	// evaluated (PairInvalid). Sol-max's own mechanism finding: unconfirmed
	// ExpectedKinds never filters the ordinary candidate pool (resolve.go).
	// CHAOS-4081 (team-lead ruling, path (a)) later threaded a CONFIRMED
	// explicit SubjectHandles hint into the census as ConfirmedHandle, but
	// for OBSERVATION only (Attestation.HandleInsensitivityEvaluated/
	// Outcome) -- it still cannot attest a decisive commit the way
	// kind_insensitivity_attested does (see that field's own doc comment,
	// chaos3899_evidence_round.go, and the ATTESTATION BOUNDARY note near
	// this file's own header) -- and neither hint reaches interpretation
	// or synthesis (genkitruntime/runtime.go) -- so for kind/handle the
	// correctness proposition is NONINTERFERENCE (baseline_equivalent),
	// not universal all-kinds census proof.
	if report.InferredPairInvalidCount > 0 {
		t.Errorf("inferred_pair_invalid_count=%d, want 0 -- a no-hint baseline call could not be evaluated, so the pairing this bar depends on is broken (investigate the baseline error, not the hinted call)", report.InferredPairInvalidCount)
	}
	if report.InferredUnjustifiedCount > 0 {
		t.Errorf("inferred_unjustified_count=%d, want 0 -- %d of %d kind/handle inferred-tier commits are neither baseline_equivalent (paired no-hint request diverged) nor kind_insensitivity_attested (no production-observed commit_sound proof tied to this exact commit) -- CHAOS-4039", report.InferredUnjustifiedCount, report.InferredUnjustifiedCount, report.InferredKindHandleDecisiveCount)
	}
	// Non-vacuity (mirrors PositiveAppliedCount's own fail-open guard
	// above): a run where kind/handle inferred-tier NEVER commits proves
	// nothing about whether this new bar can distinguish justified from
	// unjustified -- InferredPairInvalidCount==0 && InferredUnjustifiedCount==0
	// would otherwise pass vacuously.
	if !sharded && report.InferredKindHandleDecisiveCount == 0 {
		t.Errorf("inferred_kind_handle_decisive_count=0 -- kind/handle inferred-tier never committed a single case this run, so the v4 measurement contract proves nothing (non-vacuity)")
	}
	// A run whose mutation arm did not trip every probe it attempted is
	// INVALID, never silently passing (design brief's own fails-toward-fine
	// discipline for this arm).
	for probe, ran := range report.MutationProbesRun {
		if tripped := report.MutationProbesTripped[probe]; tripped != ran {
			t.Errorf("mutation probe %q tripped %d/%d attempts, want %d/%d -- the run is INVALID for this probe", probe, tripped, ran, ran, ran)
		}
	}
	// CHAOS-4165: a SOFT, informational report only -- deliberately
	// t.Logf, never t.Errorf. A low run count under a given responder is a
	// real PRODUCT finding (a low offer/apply rate), not a harness defect
	// to fail the run over; failing here would hide exactly the signal
	// this ticket exists to surface behind a red run instead of a
	// legible artifact field. Gated !sharded for the SAME reason every
	// other coverage/non-vacuity check on this function is: at
	// granularity=1 a single shard sees at most one entry, so almost
	// every kind would read low_power there regardless of the full run's
	// own health -- only the merged, full-corpus artifact (or an
	// unsharded sequential run, this branch) is a meaningful population.
	if !sharded {
		for _, kind := range mutationProbeKinds {
			if cov := report.MutationProbeCoverage[kind]; !cov.Adequate {
				t.Logf("mutation probe %q: low_power (runs=%d, required_min=%d, eligible=%d) -- informational, not a failure", kind, cov.Runs, cov.RequiredMin, report.MutationProbeEligibleCount)
			}
		}
	}
	// Positive tier-routing proof (codex round-1 finding #6): every
	// inferred-tier result that actually ran (not structurally exempt, no
	// investigate error) must show the injected value landed at
	// explicit_unattributed/inferred_default.
	for _, r := range report.Results {
		if r.Arm == string(twoTurnArmInferredTier) && r.ArmInvalidReason == "" && !r.TierRoutedCorrectly {
			t.Errorf("case %d member %q: inferred-tier injection did not route to explicit_unattributed/inferred_default in the echo -- tier-routing finding", r.Index, r.Member)
		}
	}
	// D0 controls (design brief lines 958/960: controls carry it on their
	// "clarify or no_match terminals" -- both acceptable -- and never a
	// wrong commit). ControlsWitnessed is the brief-conformant control-ok
	// count (CHAOS-3742 run-2 root cause, team-lead ruling 2026-08-20) --
	// see controlSeen/controlOK's own doc comment -- so a miss here is
	// reported as a finding, never silently passed. The narrower literal-
	// no_match-with-census claim (brief line 960's own stronger half) is
	// reported separately and informationally as
	// ControlsWitnessedNoMatchCensusBacked, with no pass/fail bar of its
	// own -- see that field's own doc comment for why.
	//
	// A zero denominator (codex round-2 finding, HIGH confidence) must
	// NOT read as a vacuous pass: a limited/truncated run (ACR_TEST_TRIAL_
	// LIMIT) or an annex that happens to name no control entries can drive
	// ControlsTotal to 0, and 0 < 0 is false either way -- the old
	// "ControlsTotal > 0 &&" guard change would not have helped, since the
	// comparison itself is vacuously satisfied at 0/0. The denominator
	// being zero is itself the finding: D0 cannot be reported at all, so
	// the run is INVALID for this check rather than silently green.
	if report.ControlsTotal == 0 {
		if !sharded {
			t.Errorf("controls_total=0: this run recorded NO control cases (entries with no expected answer) -- D0 cannot be reported and the run is INVALID for this check")
		}
	} else if report.ControlsWitnessed < report.ControlsTotal {
		t.Errorf("controls_witnessed=%d/%d, want %d/%d (D0: every control case must commit nothing and disclose structure at turn 1)", report.ControlsWitnessed, report.ControlsTotal, report.ControlsTotal, report.ControlsTotal)
	}
}
