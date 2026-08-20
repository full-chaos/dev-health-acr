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
//     inferred_default/explicit_unattributed, never question_stated) --
//     ANY commit fails. Structurally exempt for subject_anchor: the MCP
//     surface has no explicit anchor field AT ALL (design brief §2.3,
//     "anchors are receipt-only") -- there is no wire path to construct
//     this arm for anchor, which is the invariant holding vacuously, not a
//     gap in this harness.
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

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/runtime/hosted"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// --- oracle annex: schema, loader, sign-off gate (pure logic) ---

// twoTurnOracleEntry is one case's withheld oracle, ids/enums/bands ONLY --
// authored from the corpus source-of-record, never from any engine output
// (design brief §5 head). PositiveWindowBand/NegativeWindowBand are 3900
// RelativeID enum values (e.g. "trailing_90d"), never free text.
type twoTurnOracleEntry struct {
	Index                     int    `json:"index"`
	Member                    string `json:"member"` // expected_kind | subject_anchor | subject_handle | window
	PositiveKind              string `json:"positive_kind,omitempty"`
	PositiveAnchorCanonicalID string `json:"positive_anchor_canonical_id,omitempty"`
	PositiveHandleValue       string `json:"positive_handle_value,omitempty"`
	PositiveWindowBand        string `json:"positive_window_band,omitempty"`
	NegativeKind              string `json:"negative_kind,omitempty"`
	NegativeAnchorCanonicalID string `json:"negative_anchor_canonical_id,omitempty"`
	NegativeHandleValue       string `json:"negative_handle_value,omitempty"`
	NegativeWindowBand        string `json:"negative_window_band,omitempty"`
	// NegativeCommittable marks this case's negative as one of DP10's
	// "designated committable negatives" -- the anti-vacuity pin requires
	// at least one NegativeCommittable==true entry per applicable member to
	// actually be redeemed in a confirmed_wrong run.
	NegativeCommittable bool `json:"negative_committable"`
}

// twoTurnOracleAnnex is the withheld annex file's top-level shape.
// SignedOff is the DP10 mechanism: the artifact -- INCLUDING which
// negatives are designated committable -- requires chris's sign-off before
// any run may treat a measurement against it as the acceptance number
// (design brief matrix DP10: "the sign-off includes the designated
// committable negatives per the §5 anti-vacuity rule").
type twoTurnOracleAnnex struct {
	CorpusSHA256 string               `json:"corpus_sha256"`
	SignedOff    bool                 `json:"signed_off"`
	Entries      []twoTurnOracleEntry `json:"entries"`
}

var twoTurnStructureMembers = map[string]bool{
	string(contractsv1.ContextFabricStructureNeedExpectedKind):  true,
	string(contractsv1.ContextFabricStructureNeedSubjectAnchor): true,
	string(contractsv1.ContextFabricStructureNeedSubjectHandle): true,
	string(contractsv1.ContextFabricStructureNeedWindow):        true,
}

// validateTwoTurnOracleAnnex checks the closed-vocabulary/shape rules a
// malformed annex would otherwise let slide silently into a run.
func validateTwoTurnOracleAnnex(annex twoTurnOracleAnnex) error {
	if annex.CorpusSHA256 == "" {
		return fmt.Errorf("oracle annex: corpus_sha256 is required")
	}
	for i, entry := range annex.Entries {
		if !twoTurnStructureMembers[entry.Member] {
			return fmt.Errorf("oracle annex entry %d: member %q is not a closed StructureNeeds member", i, entry.Member)
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

func loadTwoTurnOracleAnnex(t *testing.T, path string) twoTurnOracleAnnex {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read oracle annex %s: %v", path, err)
	}
	var annex twoTurnOracleAnnex
	if err := json.Unmarshal(data, &annex); err != nil {
		t.Fatalf("decode oracle annex %s: %v", path, err)
	}
	if err := validateTwoTurnOracleAnnex(annex); err != nil {
		t.Fatalf("oracle annex %s: %v", path, err)
	}
	return annex
}

// --- pure logic: offer selection, commit classification (unit-testable, no live infra) ---

// selectOracleOffer scans needs for the offer matching the oracle value for
// entry.Member (kind/anchor/handle/window), using ONLY the oracle's own
// typed fields as the match key (never "the offer the engine ranked
// first" -- design brief §5 head's oracle-independence rule). A case where
// no offer matches is scored offer_miss (found=false), never silently
// skipped.
func selectOracleOffer(needs *contractsv1.ContextFabricStructureNeeds, member string, wantKind, wantAnchorID, wantHandleValue, wantWindowBand string) (receiptID string, found bool) {
	if needs == nil {
		return "", false
	}
	switch member {
	case string(contractsv1.ContextFabricStructureNeedExpectedKind):
		for _, opt := range needs.KindOptions {
			if string(opt.Kind) == wantKind {
				return opt.ReceiptID, true
			}
		}
	case string(contractsv1.ContextFabricStructureNeedSubjectAnchor):
		for _, opt := range needs.AnchorOptions {
			if opt.CanonicalID == wantAnchorID {
				return opt.ReceiptID, true
			}
		}
	case string(contractsv1.ContextFabricStructureNeedSubjectHandle):
		for _, opt := range needs.HandleOptions {
			if opt.Value == wantHandleValue {
				return opt.ReceiptID, true
			}
		}
	case string(contractsv1.ContextFabricStructureNeedWindow):
		for _, opt := range needs.WindowOptions {
			if string(opt.RelativeID) == wantWindowBand {
				return opt.ReceiptID, true
			}
		}
	}
	return "", false
}

// twoTurnCommittedWrong reports whether committed is a decisive commit that
// does NOT match the corpus's own ground truth -- the confirmed_wrong arm's
// pass/fail predicate (design brief §5 head: "ANY resulting oracle-wrong
// commit ALSO FAILS the run").
func twoTurnCommittedWrong(committed []contractsv1.ContextFabricSubjectRef, tc trialCase) bool {
	return len(committed) > 0 && !committedMatchesTrial(committed, tc)
}

// --- report shapes (outcome data only -- no question/label text) ---

type twoTurnArm string

const (
	twoTurnArmPositive       twoTurnArm = "positive"
	twoTurnArmInferredTier   twoTurnArm = "inferred_tier"
	twoTurnArmConfirmedWrong twoTurnArm = "confirmed_wrong"
	twoTurnArmMutation       twoTurnArm = "mutation"
)

// twoTurnCaseResult is one (case, arm) outcome. Every field is a count, a
// bool, an id, or a closed-vocabulary status string -- the same privacy
// discipline replayCaseOutcome pins (TestReplayCaseOutcomeCarriesNoQuestionOrTermText).
type twoTurnCaseResult struct {
	Index            int    `json:"index"`
	Member           string `json:"member"`
	Arm              string `json:"arm"`
	Turn1Status      string `json:"turn1_status"`
	Turn2Status      string `json:"turn2_status"`
	OfferMiss        bool   `json:"offer_miss"`
	Applied          bool   `json:"applied"`
	CommittedCount   int    `json:"committed_count"`
	WrongCommit      bool   `json:"wrong_commit"`
	MutationProbe    string `json:"mutation_probe,omitempty"`
	MutationTripped  bool   `json:"mutation_tripped,omitempty"`
	ArmInvalidReason string `json:"arm_invalid_reason,omitempty"`
}

type twoTurnReport struct {
	Provenance                  trialProvenance     `json:"provenance"`
	OracleAnnexPath             string              `json:"oracle_annex_path"`
	OracleAnnexCorpusSHA        string              `json:"oracle_annex_corpus_sha256"`
	OracleAnnexSignedOff        bool                `json:"oracle_annex_signed_off"`
	CasesRun                    int                 `json:"cases_run"`
	GateReachableCount          int                 `json:"gate_reachable_count"`
	NoDiscriminatorsCount       int                 `json:"no_discriminators_count"`
	OfferMissCount              map[string]int      `json:"offer_miss_count"`
	WrongCommitCount            int                 `json:"wrong_commit_count"`
	InferredTierAnyCommit       int                 `json:"inferred_tier_any_commit_count"`
	ConfirmedWrongRedeemedCount int                 `json:"confirmed_wrong_redeemed_committable_count"`
	AntiVacuityValid            bool                `json:"anti_vacuity_valid"`
	MutationProbesTripped       map[string]int      `json:"mutation_probes_tripped"`
	MutationProbesRun           map[string]int      `json:"mutation_probes_run"`
	Results                     []twoTurnCaseResult `json:"results"`
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

func TestSelectOracleOffer(t *testing.T) {
	t.Parallel()
	needs := &contractsv1.ContextFabricStructureNeeds{
		Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: []contractsv1.ContextFabricKindOption{
			{ReceiptID: "kindr_aaaaaaaaaaaaaaaaaaaaaaaa", Kind: contractsv1.ContextFabricSubjectPullRequest},
		},
		AnchorOptions: []contractsv1.ContextFabricAnchorOption{
			{ReceiptID: "ancr_bbbbbbbbbbbbbbbbbbbbbbbb", CanonicalID: "repository_ask_dev"},
		},
		HandleOptions: []contractsv1.ContextFabricHandleOption{
			{ReceiptID: "handr_cccccccccccccccccccccc", Value: "532"},
		},
		WindowOptions: []contractsv1.ContextFabricWindowOption{
			{ReceiptID: "winr_dddddddddddddddddddddd", RelativeID: "trailing_90d"},
		},
	}

	cases := []struct {
		name                                                   string
		member, wantKind, wantAnchorID, wantHandle, wantWindow string
		wantReceiptID                                          string
		wantFound                                              bool
	}{
		{"kind match", string(contractsv1.ContextFabricStructureNeedExpectedKind), "pull_request", "", "", "", "kindr_aaaaaaaaaaaaaaaaaaaaaaaa", true},
		{"kind miss", string(contractsv1.ContextFabricStructureNeedExpectedKind), "review", "", "", "", "", false},
		{"anchor match", string(contractsv1.ContextFabricStructureNeedSubjectAnchor), "", "repository_ask_dev", "", "", "ancr_bbbbbbbbbbbbbbbbbbbbbbbb", true},
		{"anchor miss", string(contractsv1.ContextFabricStructureNeedSubjectAnchor), "", "repository_other", "", "", "", false},
		{"handle match", string(contractsv1.ContextFabricStructureNeedSubjectHandle), "", "", "532", "", "handr_cccccccccccccccccccccc", true},
		{"window match", string(contractsv1.ContextFabricStructureNeedWindow), "", "", "", "trailing_90d", "winr_dddddddddddddddddddddd", true},
		{"nil needs", string(contractsv1.ContextFabricStructureNeedExpectedKind), "pull_request", "", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := needs
			if tc.name == "nil needs" {
				input = nil
			}
			gotID, gotFound := selectOracleOffer(input, tc.member, tc.wantKind, tc.wantAnchorID, tc.wantHandle, tc.wantWindow)
			if gotFound != tc.wantFound || gotID != tc.wantReceiptID {
				t.Errorf("selectOracleOffer(%s) = (%q, %v), want (%q, %v)", tc.name, gotID, gotFound, tc.wantReceiptID, tc.wantFound)
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
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contractsv1.ContextFabricConsumerInfo{Name: "chaos-3742-two-turn", Version: "0.1.0", Surface: "mcp"},
	}
}

// runTwoTurnPositiveArm confirms the oracle-matching offer via receipt and
// reports whether it converted.
func runTwoTurnPositiveArm(ctx context.Context, investigator contextfabric.Investigator, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, turn1 contractsv1.ContextFabricInvestigationResult, timeout time.Duration) twoTurnCaseResult {
	res := twoTurnCaseResult{Index: index, Member: entry.Member, Arm: string(twoTurnArmPositive), Turn1Status: string(turn1.Status)}
	receiptID, found := selectOracleOffer(turn1.StructureNeeds, entry.Member,
		entry.PositiveKind, entry.PositiveAnchorCanonicalID, entry.PositiveHandleValue, entry.PositiveWindowBand)
	if !found {
		res.OfferMiss = true
		return res
	}
	req := twoTurnRequest(index, tc, "positive")
	setTwoTurnReceipt(&req, entry.Member, contractsv1.ContextFabricBoundSubjectReceipt{ResultID: turn1.ResultID, ReceiptID: receiptID})
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	turn2, err := investigator.Investigate(callCtx, principal, req)
	if err != nil {
		res.Turn2Status = "error:" + contextFabricRejectionClass(err)
		return res
	}
	res.Turn2Status = string(turn2.Status)
	res.CommittedCount = len(turn2.SubjectResolution.Committed)
	for _, e := range turn2.ConfirmedStructure {
		if string(e.Member) == entry.Member && e.Disposition == contractsv1.ContextFabricStructureDispositionApplied {
			res.Applied = true
		}
	}
	res.WrongCommit = twoTurnCommittedWrong(turn2.SubjectResolution.Committed, tc)
	return res
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
func runTwoTurnInferredTierArm(ctx context.Context, investigator contextfabric.Investigator, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, timeout time.Duration) twoTurnCaseResult {
	res := twoTurnCaseResult{Index: index, Member: entry.Member, Arm: string(twoTurnArmInferredTier)}
	req := twoTurnRequest(index, tc, "inferredtier")
	switch entry.Member {
	case string(contractsv1.ContextFabricStructureNeedExpectedKind):
		req.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectKind(entry.NegativeKind)}
	case string(contractsv1.ContextFabricStructureNeedSubjectHandle):
		req.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{
			Kind: contractsv1.ContextFabricSubjectKind(entry.NegativeKind), Value: entry.NegativeHandleValue,
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
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := investigator.Investigate(callCtx, principal, req)
	if err != nil {
		res.Turn2Status = "error:" + contextFabricRejectionClass(err)
		return res
	}
	res.Turn2Status = string(result.Status)
	res.CommittedCount = len(result.SubjectResolution.Committed)
	return res
}

// twoTurnResultStoreSaver is the narrow slice of contextfabric.InvestigationResultStore
// this harness needs to seed a stored result -- the harness-seeded
// stored-result constructor for the anchor member's confirmed-wrong arm
// (design brief §5 head).
type twoTurnResultStoreSaver interface {
	Save(context.Context, storage.Principal, contextfabric.InvestigationResult, contextfabric.SourceWatermarkSnapshot, contextfabric.RebuildEpoch, string, contextfabric.ReuseRetrievalIdentity, contextfabric.ReusePromptVersions, contextfabric.ReuseVersionAuthorities, int64) error
}

// seedAnchorNegativeResult is the harness-seeded STORED-RESULT constructor
// (design brief §5 head, per-member constructors): seeding is scaffolding
// for the offer's ORIGIN only -- everything downstream (receipt validation,
// claimant re-verification, census, gate) is the ordinary production
// redemption path run against this seeded row exactly as it would run
// against any engine-produced one.
func seedAnchorNegativeResult(ctx context.Context, store twoTurnResultStoreSaver, principal storage.Principal, index int, entry twoTurnOracleEntry, receiptID string) (resultID string, err error) {
	resultID = fmt.Sprintf("result_twoturn_seed_anchor%06d", index)
	result := contextfabric.InvestigationResult{
		SchemaVersion: contextfabric.InvestigationResultSchemaV1,
		ResultID:      resultID,
		RequestID:     fmt.Sprintf("request_twoturn_seed%06d", index),
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
				Kind:            contractsv1.ContextFabricSubjectKind(entry.NegativeKind),
				CanonicalID:     entry.NegativeAnchorCanonicalID,
				MatchedTermHash: "000000000000000000000000",
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
func runTwoTurnConfirmedWrongArm(ctx context.Context, investigator contextfabric.Investigator, store twoTurnResultStoreSaver, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, timeout time.Duration) twoTurnCaseResult {
	res := twoTurnCaseResult{Index: index, Member: entry.Member, Arm: string(twoTurnArmConfirmedWrong)}

	var offerResultID, receiptID string
	if entry.Member == string(contractsv1.ContextFabricStructureNeedSubjectAnchor) {
		receiptID = contractsv1.ContextFabricAnchorOptionReceiptPrefix + "twoturnseed0000000000000"
		var err error
		offerResultID, err = seedAnchorNegativeResult(ctx, store, principal, index, entry, receiptID)
		if err != nil {
			res.ArmInvalidReason = "harness-seeded anchor negative could not be made redeemable: " + err.Error()
			return res
		}
	} else {
		setupReq := twoTurnRequest(index, tc, "confirmedwrongsetup")
		switch entry.Member {
		case string(contractsv1.ContextFabricStructureNeedExpectedKind):
			setupReq.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectKind(entry.NegativeKind)}
		case string(contractsv1.ContextFabricStructureNeedSubjectHandle):
			setupReq.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{
				Kind: contractsv1.ContextFabricSubjectKind(entry.NegativeKind), Value: entry.NegativeHandleValue,
			}}
		case string(contractsv1.ContextFabricStructureNeedWindow):
			setupReq.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowID(entry.NegativeWindowBand)}
		}
		setupCtx, cancel := context.WithTimeout(ctx, timeout)
		setupResult, err := investigator.Investigate(setupCtx, principal, setupReq)
		cancel()
		if err != nil {
			res.ArmInvalidReason = "setup turn failed: " + err.Error()
			return res
		}
		offerResultID = setupResult.ResultID
		var found bool
		receiptID, found = selectOracleOffer(setupResult.StructureNeeds, entry.Member,
			entry.NegativeKind, entry.NegativeAnchorCanonicalID, entry.NegativeHandleValue, entry.NegativeWindowBand)
		if !found {
			res.ArmInvalidReason = "setup turn did not offer the designated negative back as a receipt-bound offer (an engine-refusal finding, not this harness's own defect)"
			return res
		}
	}

	req := twoTurnRequest(index, tc, "confirmedwrong")
	setTwoTurnReceipt(&req, entry.Member, contractsv1.ContextFabricBoundSubjectReceipt{ResultID: offerResultID, ReceiptID: receiptID})
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	turn2, err := investigator.Investigate(callCtx, principal, req)
	if err != nil {
		res.Turn2Status = "error:" + contextFabricRejectionClass(err)
		return res
	}
	res.Turn2Status = string(turn2.Status)
	res.CommittedCount = len(turn2.SubjectResolution.Committed)
	for _, e := range turn2.ConfirmedStructure {
		if string(e.Member) == entry.Member && e.Disposition == contractsv1.ContextFabricStructureDispositionApplied {
			res.Applied = true
		}
	}
	res.WrongCommit = twoTurnCommittedWrong(turn2.SubjectResolution.Committed, tc)
	return res
}

// twoTurnMutationProbe classifies a mutation-arm turn's outcome: tripped
// means the expected veto/refusal actually happened.
func twoTurnMutationProbe(applied bool, status string, committedCount int) (tripped bool) {
	return !applied && committedCount == 0 && status != string(contractsv1.ContextFabricInvestigationComplete)
}

// runTwoTurnMutationArm runs the three non-vacuity probes (design brief §5
// head) against a case the positive arm already converted. Each probe's
// twoTurnCaseResult.MutationProbe names which one ran, and MutationTripped
// records whether the expected veto/refusal actually happened -- a probe
// that does NOT trip is itself a finding (the run is invalid, per the
// harness's own fails-toward-fine discipline), never silently ignored.
func runTwoTurnMutationArm(ctx context.Context, investigator contextfabric.Investigator, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, turn1ResultID, receiptID string, timeout time.Duration) []twoTurnCaseResult {
	run := func(probe string, req contractsv1.ContextFabricInvestigationRequest) twoTurnCaseResult {
		res := twoTurnCaseResult{Index: index, Member: entry.Member, Arm: string(twoTurnArmMutation), MutationProbe: probe}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		result, err := investigator.Investigate(callCtx, principal, req)
		if err != nil {
			res.Turn2Status = "error:" + contextFabricRejectionClass(err)
			res.MutationTripped = true // a hard rejection is itself a non-conversion, which is what every probe here expects
			return res
		}
		res.Turn2Status = string(result.Status)
		res.CommittedCount = len(result.SubjectResolution.Committed)
		for _, e := range result.ConfirmedStructure {
			if string(e.Member) == entry.Member && e.Disposition == contractsv1.ContextFabricStructureDispositionApplied {
				res.Applied = true
			}
		}
		res.MutationTripped = twoTurnMutationProbe(res.Applied, res.Turn2Status, res.CommittedCount)
		return res
	}

	results := make([]twoTurnCaseResult, 0, 3)

	// (i) remove the confirming receipt: the refusal must return.
	removeConfirmationReq := twoTurnRequest(index, tc, "mutation_remove_confirmation")
	results = append(results, run("remove_confirmation", removeConfirmationReq))

	// (ii) corrupt the receipt id: 400/veto, never an answer.
	corruptReq := twoTurnRequest(index, tc, "mutation_corrupt_receipt")
	setTwoTurnReceipt(&corruptReq, entry.Member, contractsv1.ContextFabricBoundSubjectReceipt{
		ResultID: turn1ResultID, ReceiptID: receiptID + "corrupted",
	})
	results = append(results, run("corrupt_receipt", corruptReq))

	// (iii) redeem the ALREADY-REDEEMED (now superseded) offer again ->
	// stale_superseded_offer veto. Requires the positive arm to have
	// already redeemed this exact (turn1ResultID, receiptID) pair once,
	// producing the result that superseded it.
	staleReq := twoTurnRequest(index, tc, "mutation_stale_superseded_offer")
	setTwoTurnReceipt(&staleReq, entry.Member, contractsv1.ContextFabricBoundSubjectReceipt{
		ResultID: turn1ResultID, ReceiptID: receiptID,
	})
	staleResult := run("stale_superseded_offer", staleReq)
	// The specific tell for THIS probe (vs the generic veto the other two
	// probes accept): the disposition must be vetoed_stale, not merely
	// "did not apply" -- a plain conflict veto would otherwise pass this
	// probe for the wrong reason.
	staleResult.MutationTripped = false
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	staleCheck, err := investigator.Investigate(callCtx, principal, staleReq)
	cancel()
	if err == nil {
		for _, e := range staleCheck.ConfirmedStructure {
			if string(e.Member) == entry.Member && e.Disposition == contractsv1.ContextFabricStructureDispositionVetoedStale {
				staleResult.MutationTripped = true
			}
		}
	}
	results = append(results, staleResult)

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

	annex := loadTwoTurnOracleAnnex(t, annexPath)
	if err := requireAnnexSignedOff(annex); err != nil {
		t.Fatalf("refusing to run the two-turn confirmation replay: %v", err)
	}

	corpus, corpusHash := loadTrialCorpus(t, corpusPath)
	if annex.CorpusSHA256 != corpusHash {
		t.Fatalf("oracle annex corpus_sha256=%s does not match the loaded corpus hash=%s -- refusing to run against a mismatched annex/corpus pair", annex.CorpusSHA256, corpusHash)
	}
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	options := hosted.Options{ServiceVersion: "chaos-3742-two-turn", Logger: logger, Now: time.Now}
	caseTimeout := 240 * time.Second
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
		options.ModelRuntimeOverride = exchangeRuntime
		caseTimeout = 2*timeout + 30*time.Second
		t.Logf("using the FILE-EXCHANGE diagnostic transport at %s (case budget %s, session %s)", exchangeDir, caseTimeout, exchangeRuntime.nonce)
	}
	rt, err := hosted.Open(ctx, cfg, options)
	if err != nil {
		t.Fatalf("open hosted runtime: %v", err)
	}
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

	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}

	report := twoTurnReport{
		Provenance: trialProvenance{
			CorpusSHA256: corpusHash, Transport: "real_api", RunStartedAt: runStartedAt,
			SourceCommit: source.commit, SourceDirty: source.dirty, SourceDiffDigest: source.diffDigest,
		},
		OracleAnnexPath: annexPath, OracleAnnexCorpusSHA: annex.CorpusSHA256, OracleAnnexSignedOff: annex.SignedOff,
		OfferMissCount:        map[string]int{},
		MutationProbesTripped: map[string]int{},
		MutationProbesRun:     map[string]int{},
	}

	confirmedWrongRedeemedCommittable := 0
	for _, entry := range annex.Entries {
		if entry.Index < 0 || entry.Index >= len(corpus) {
			t.Fatalf("oracle annex entry names index %d, corpus has %d cases", entry.Index, len(corpus))
		}
		tc := corpus[entry.Index]

		turn1Req := twoTurnRequest(entry.Index, tc, "turn1")
		turn1Ctx, turn1Cancel := context.WithTimeout(ctx, caseTimeout)
		turn1, err := investigator.Investigate(turn1Ctx, principal, turn1Req)
		turn1Cancel()
		if err != nil {
			t.Logf("case %d: turn 1 error: %v", entry.Index, err)
			continue
		}
		report.CasesRun++
		if turn1.StructureNeeds == nil {
			report.NoDiscriminatorsCount++
			continue
		}

		positive := runTwoTurnPositiveArm(ctx, investigator, principal, entry.Index, tc, entry, turn1, caseTimeout)
		if positive.OfferMiss {
			report.OfferMissCount[entry.Member]++
		}
		if positive.CommittedCount > 0 {
			report.GateReachableCount++
		}
		if positive.WrongCommit {
			report.WrongCommitCount++
		}
		report.Results = append(report.Results, positive)

		inferred := runTwoTurnInferredTierArm(ctx, investigator, principal, entry.Index, tc, entry, caseTimeout)
		if inferred.ArmInvalidReason == "" && inferred.CommittedCount > 0 {
			report.InferredTierAnyCommit++
		}
		report.Results = append(report.Results, inferred)

		confirmedWrong := runTwoTurnConfirmedWrongArm(ctx, investigator, store, principal, entry.Index, tc, entry, caseTimeout)
		if confirmedWrong.ArmInvalidReason == "" && confirmedWrong.Applied && entry.NegativeCommittable {
			confirmedWrongRedeemedCommittable++
		}
		if confirmedWrong.WrongCommit {
			report.WrongCommitCount++
		}
		report.Results = append(report.Results, confirmedWrong)

		// Mutation/non-vacuity arm: only meaningful against a case the
		// positive arm actually converted (probe (iii) needs a real
		// supersession to have happened; probes (i)/(ii) are cheap enough
		// to run alongside it rather than standing up a separate case).
		if positive.Applied {
			receiptID, found := selectOracleOffer(turn1.StructureNeeds, entry.Member,
				entry.PositiveKind, entry.PositiveAnchorCanonicalID, entry.PositiveHandleValue, entry.PositiveWindowBand)
			if found {
				for _, mutationResult := range runTwoTurnMutationArm(ctx, investigator, principal, entry.Index, tc, entry, turn1.ResultID, receiptID, caseTimeout) {
					report.MutationProbesRun[mutationResult.MutationProbe]++
					if mutationResult.MutationTripped {
						report.MutationProbesTripped[mutationResult.MutationProbe]++
					}
					report.Results = append(report.Results, mutationResult)
				}
			}
		}

		t.Logf("case %d member=%s: positive(applied=%v miss=%v) inferred(commit=%d invalid=%q) confirmed_wrong(applied=%v wrong=%v invalid=%q)",
			entry.Index, entry.Member, positive.Applied, positive.OfferMiss, inferred.CommittedCount, inferred.ArmInvalidReason,
			confirmedWrong.Applied, confirmedWrong.WrongCommit, confirmedWrong.ArmInvalidReason)
	}
	report.ConfirmedWrongRedeemedCount = confirmedWrongRedeemedCommittable
	report.AntiVacuityValid = confirmedWrongRedeemedCommittable >= 1

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

	if !report.AntiVacuityValid {
		t.Errorf("confirmed_wrong arm anti-vacuity check failed: %d designated committable negatives redeemed, want >=1 (design brief v4/sol-r3 #4) -- the arm is INVALID for this run", confirmedWrongRedeemedCommittable)
	}
	if report.WrongCommitCount > 0 {
		t.Errorf("wrong_commit_count=%d, want 0 (DP9: ZERO wrong commits, period)", report.WrongCommitCount)
	}
	if report.InferredTierAnyCommit > 0 {
		t.Errorf("inferred_tier_any_commit_count=%d, want 0 (any commit under unconfirmed inferred-tier structure fails the run)", report.InferredTierAnyCommit)
	}
	// A run whose mutation arm did not trip every probe it attempted is
	// INVALID, never silently passing (design brief's own fails-toward-fine
	// discipline for this arm).
	for probe, ran := range report.MutationProbesRun {
		if tripped := report.MutationProbesTripped[probe]; tripped != ran {
			t.Errorf("mutation probe %q tripped %d/%d attempts, want %d/%d -- the run is INVALID for this probe", probe, tripped, ran, ran, ran)
		}
	}
}
