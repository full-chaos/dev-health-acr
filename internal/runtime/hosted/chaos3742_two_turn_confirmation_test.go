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
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	runtimeclickhouse "github.com/full-chaos/dev-health-acr/internal/runtime/clickhouse"
	"github.com/full-chaos/dev-health-acr/internal/runtime/hosted"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
// Window offers are read from result.WindowClarification.Options, NOT
// result.StructureNeeds.WindowOptions (codex round-1 finding #1, confirmed
// against structure.go's composeStructureNeeds: it populates KindOptions/
// AnchorOptions/HandleOptions only -- window offers are minted through the
// SEPARATE CHAOS-3900 W1 WindowClarification path; StructureNeeds.WindowOptions
// exists on the wire per the design brief's "3900's type, verbatim" but is
// not yet the field production actually fills).
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
		if result.WindowClarification == nil {
			return "", false
		}
		for _, opt := range result.WindowClarification.Options {
			if string(opt.RelativeID) == q.windowBand {
				return opt.ReceiptID, true
			}
		}
	}
	return "", false
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
}

func (c *twoTurnTraceCapture) Trace(event graphrank.ResolutionTraceEvent) {
	c.events = append(c.events, event)
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

func (c *twoTurnTraceCapture) reset() {
	c.events = nil
	c.synthesisOverride = nil
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

// finalDecisionEvent returns the LAST captured "decision"-stage event
// (zero value, ok=false if none) -- CHAOS-4039's own
// twoTurnInferredClassification input (its Outcome is the engine-
// deterministic half of baseline_equivalent). "Last" because a stalled
// resolution can emit a decision event
// TWICE (the initial stalled attempt, then a census-enriched re-decision
// -- TestResolveSubjects_EvidenceCensusCommitsAStalledCandidate, graphrank);
// only the final one describes what the caller actually received.
func (c *twoTurnTraceCapture) finalDecisionEvent() (event graphrank.ResolutionTraceEvent, ok bool) {
	for _, e := range c.events {
		if e.Stage == "decision" {
			event, ok = e, true
		}
	}
	return event, ok
}

// kindCoverageFloorEvent returns the LAST captured "kind_coverage_floor"
// event (CHAOS-4086/CHAOS-4038), the same last-wins rule finalDecisionEvent
// applies and for the same reason: ResolveSubjects can resolve twice (the
// evidence-census re-resolve), and the run that produced the served answer is
// the later one.
//
// A SEPARATE reader from finalDecisionEvent because it is a separate stage --
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
//     -- only the LAST one describes what the caller actually received,
//     twoTurnTraceCapture.finalDecisionEvent) match, AND
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

// twoTurnInferredClassification computes CHAOS-4039's 3-way partition for a
// DECISIVE (CommittedCount>0) kind/handle inferred-tier commit: hinted/
// baselineCommitted are the paired calls' own SubjectResolution.Committed,
// hinted/baselineOutcome their own final decision-stage trace Outcome
// (twoTurnTraceCapture.finalDecisionEvent), and kindAttested is whether the
// all-kinds census itself certified this exact commit (runTwoTurnInferredTierArm's
// own kindInsensitivityResult/evidenceCensusCommitted check). Extracted from
// the classification switch (runTwoTurnInferredTierArm) purely so it has a
// unit test surface independent of that function's full investigator/
// window-precondition machinery -- mirrors twoTurnUnjustifiedShadowProbe's
// own extraction, immediately below.
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
	if event, ok := trace.finalDecisionEvent(); ok {
		res.CommitGate = event.CommitGate
		res.TiedStatisticalTop = event.TiedStatisticalTop
		res.SearchTruncated = event.SearchTruncated
	}
	if event, ok := trace.kindCoverageFloorEvent(); ok {
		res.KindCoverageFloorFired = event.KindCoverageFloorFired
		res.KindCoverageMissingKinds = event.KindCoverageMissingKinds
		res.KindCoverageFloorTruncated = event.KindCoverageFloorTruncated
	}
	// CHAOS-4103: stamped unconditionally (SynthesisStatusOverrideFired is
	// never omitted -- see twoTurnCaseResult's own doc comment), same
	// discipline ExpectedKind/ExpectedID already use, so an absent-from-JSON
	// override reads as measured-and-did-not-fire rather than not-measured.
	twoTurnSetSynthesisOverride(res, trace.synthesisOverride)
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
	// FalseNoMatch (inferred_tier arm, every member including window) is
	// true when this outcome resolved to the literal no_match terminal on
	// a case with a real expected answer (tc.ExpectID != "" -- every
	// inferred-tier case has one, by construction) -- the no-match-direction
	// mirror of WrongCommit, CHAOS-4039's own "false_no_match=0" pass
	// condition.
	FalseNoMatch    bool   `json:"false_no_match,omitempty"`
	CommittedCount  int    `json:"committed_count"`
	WrongCommit     bool   `json:"wrong_commit"`
	MutationProbe   string `json:"mutation_probe,omitempty"`
	MutationTripped bool   `json:"mutation_tripped,omitempty"`
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
	KindCoverageFloorFired     bool `json:"kind_coverage_floor_fired,omitempty"`
	KindCoverageMissingKinds   int  `json:"kind_coverage_missing_kinds,omitempty"`
	KindCoverageFloorTruncated bool `json:"kind_coverage_floor_truncated,omitempty"`
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
}

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
	// Bump this again on any future field rename, removal, or meaning
	// change so a consumer can detect drift instead of silently reading a
	// stale key under a new meaning.
	ReportSchemaVersion string          `json:"report_schema_version"`
	Provenance          trialProvenance `json:"provenance"`
	// BaseSHA mirrors chaos3884_replay_harness_test.go's replayReport.BaseSHA
	// (codex round-3 finding #3: the wrapper script already exports
	// ACR_TEST_TRIAL_BASE_SHA -- required provenance, team-lead ruling
	// 2026-08-17 -- but the report never captured it, so the artifact could
	// not prove which origin/main baseline the run was based on).
	BaseSHA              string `json:"base_sha"`
	OracleAnnexPath      string `json:"oracle_annex_path"`
	OracleAnnexCorpusSHA string `json:"oracle_annex_corpus_sha256"`
	OracleAnnexSignedOff bool   `json:"oracle_annex_signed_off"`
	CasesRun             int    `json:"cases_run"`
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
	// FalseNoMatchCount (CHAOS-4039 v4 contract) sums twoTurnCaseResult.FalseNoMatch
	// across every inferred_tier outcome, kind/handle AND window alike --
	// one of the ruling's ZERO-tolerance pass conditions, alongside
	// WrongCommitCount, regardless of member.
	FalseNoMatchCount int `json:"false_no_match_count"`
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
		{"window match (from WindowClarification, not StructureNeeds)", string(contractsv1.ContextFabricStructureNeedWindow), oracleOfferQuery{windowBand: "trailing_90d"}, "winr_dddddddddddddddddddddd", true, false},
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
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contractsv1.ContextFabricConsumerInfo{Name: "chaos-3742-two-turn", Version: "0.1.0", Surface: "mcp"},
	}
}

// runTwoTurnPositiveArm confirms the oracle-matching offer via receipt and
// reports whether it converted.
func runTwoTurnPositiveArm(ctx context.Context, investigator contextfabric.Investigator, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, turn1 contractsv1.ContextFabricInvestigationResult, timeout time.Duration, trace *twoTurnTraceCapture) twoTurnCaseResult {
	res := twoTurnCaseResult{Index: index, Member: entry.Member, Arm: string(twoTurnArmPositive), Turn1Status: string(turn1.Status)}
	twoTurnStampOutcome(&res, tc, nil)
	receiptID, found := selectOracleOffer(turn1, entry.Member, entry.positiveQuery())
	if !found {
		res.OfferMiss = true
		return res
	}
	req := twoTurnRequest(index, tc, "positive")
	setTwoTurnReceipt(&req, entry.Member, contractsv1.ContextFabricBoundSubjectReceipt{ResultID: turn1.ResultID, ReceiptID: receiptID})
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// CHAOS-4086: reset immediately before the call so finalDecisionEvent
	// below can only ever see THIS call's events. The capture is shared
	// across arms (one tracer is installed on the investigator for the
	// whole run), so without the reset a quiet arm would inherit the
	// previous arm's gate.
	if trace != nil {
		trace.reset()
	}
	turn2, err := investigator.Investigate(callCtx, principal, req)
	if err != nil {
		res.Turn2Status = "error:" + contextFabricRejectionClass(err)
		twoTurnStampArmFailure(&res, "investigate error: "+contextFabricRejectionClass(err), err)
		return res
	}
	res.Turn2Status = string(turn2.Status)
	res.CommittedCount = len(turn2.SubjectResolution.Committed)
	res.Applied = memberApplied(turn2, entry.Member)
	res.WrongCommit = twoTurnCommittedWrong(turn2.SubjectResolution.Committed, tc)
	res.Reused = turn2.Reused
	twoTurnStampOutcome(&res, tc, turn2.SubjectResolution.Committed)
	twoTurnStampDecision(&res, trace)
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

func runTwoTurnInferredTierArm(ctx context.Context, investigator contextfabric.Investigator, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, timeout time.Duration, trace *twoTurnTraceCapture, windowBand string) twoTurnCaseResult {
	res := twoTurnCaseResult{Index: index, Member: entry.Member, Arm: string(twoTurnArmInferredTier)}
	// CHAOS-4086: stamped up front for the same reason the other arms do
	// it -- this arm has many early returns (structurally exempt anchor, a
	// missing window precondition, a failed baseline) and every one of them
	// produces a row a reader has to interpret.
	twoTurnStampOutcome(&res, tc, nil)
	req := twoTurnRequest(index, tc, "inferredtier")
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
	mintWindowPrecondition := func(requestIDSuffix string) (receipt contractsv1.ContextFabricBoundSubjectReceipt, reason string, cause error) {
		setupReq := twoTurnRequest(index, tc, requestIDSuffix)
		setupReq.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowID(windowBand)}
		setupCtx, setupCancel := context.WithTimeout(ctx, timeout)
		setupResult, setupErr := investigator.Investigate(setupCtx, principal, setupReq)
		setupCancel()
		if setupErr != nil {
			return contractsv1.ContextFabricBoundSubjectReceipt{}, "window precondition setup failed: " + contextFabricRejectionClass(setupErr), setupErr
		}
		receiptID, found := selectOracleOffer(setupResult, string(contractsv1.ContextFabricStructureNeedWindow), oracleOfferQuery{windowBand: windowBand})
		if !found {
			return contractsv1.ContextFabricBoundSubjectReceipt{}, "window precondition setup turn did not offer the case's own window back as a receipt-bound offer (an engine-refusal finding, not this harness's own defect)", nil
		}
		return contractsv1.ContextFabricBoundSubjectReceipt{ResultID: setupResult.ResultID, ReceiptID: receiptID}, "", nil
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
		baselineWindow, windowReason, windowCause = mintWindowPrecondition("inferredtierwindowsetupbaseline")
		if windowReason != "" {
			res.PairInvalid = true
			twoTurnStampArmFailure(&res, windowReason, windowCause)
			return res
		}
		hintedWindow, windowReason, windowCause = mintWindowPrecondition("inferredtierwindowsetuphinted")
		if windowReason != "" {
			res.PairInvalid = true
			twoTurnStampArmFailure(&res, windowReason, windowCause)
			return res
		}
	}

	var baseline contractsv1.ContextFabricInvestigationResult
	var baselineDecision graphrank.ResolutionTraceEvent
	// baselineSynthesisOverride (CHAOS-4103, codex review round 1, confirmed):
	// the baseline call is a REAL Investigate() and can independently trip
	// applySynthesisStatusOverride -- captured below at the SAME point as
	// baselineDecision immediately above, and for the SAME reason: the
	// hinted call's own reset just below would otherwise silently discard
	// it before twoTurnStampDecision ever reads it, understating (or
	// entirely missing) this arm's own count toward the blocking-defect
	// bar.
	var baselineSynthesisOverride *contextfabric.SynthesisStatusOverrideOutcome
	if !isWindow {
		if trace != nil {
			trace.reset()
		}
		baselineReq := twoTurnRequest(index, tc, "inferredtierbaseline")
		baselineReq.PriorWindowReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{baselineWindow}
		baselineCtx, baselineCancel := context.WithTimeout(ctx, timeout)
		var baselineErr error
		baseline, baselineErr = investigator.Investigate(baselineCtx, principal, baselineReq)
		baselineCancel()
		if baselineErr != nil {
			// PairInvalid, NOT ArmInvalidReason alone: this pairing could
			// not be evaluated AT ALL (the baseline itself never resolved),
			// distinct from "resolved and found unjustified" -- reported
			// separately so it is never silently absorbed into either
			// bucket (InferredPairInvalidCount's own doc comment).
			res.PairInvalid = true
			twoTurnStampArmFailure(&res, "baseline investigate error: "+contextFabricRejectionClass(baselineErr), baselineErr)
			return res
		}
		if trace != nil {
			// Captured BEFORE the hinted call's own reset below -- the
			// baseline's decision event would otherwise be lost.
			baselineDecision, _ = trace.finalDecisionEvent()
			baselineSynthesisOverride = trace.synthesisOverride
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
		return res
	}
	res.Turn2Status = string(result.Status)
	res.CommittedCount = len(result.SubjectResolution.Committed)
	res.WrongCommit = twoTurnCommittedWrong(result.SubjectResolution.Committed, tc)
	// CHAOS-4086: the HINTED call's own outcome and decision. The trace was
	// reset immediately before that call above, so the decision event this
	// reads cannot be the baseline's.
	twoTurnStampOutcome(&res, tc, result.SubjectResolution.Committed)
	twoTurnStampDecision(&res, trace)
	// CHAOS-4103 (codex review round 1): fold the baseline's own override
	// occurrence back in -- twoTurnStampDecision above only ever sees the
	// HINTED call's trace state, so a baseline-only override would
	// otherwise vanish. See twoTurnFoldSynthesisStatusOverride's own doc
	// comment for the severity tie-break.
	twoTurnFoldSynthesisStatusOverride(&res, baselineSynthesisOverride)
	// false_no_match (CHAOS-4039): every inferred-tier case carries a real
	// expected answer (tc.ExpectID != "" by construction of this harness's
	// own corpus selection) -- a literal no_match terminal here is as much
	// a correctness finding as a wrong commit is, just in the opposite
	// direction. Checked on BOTH calls (team-lead ruling): a baseline that
	// falsely no-matches is just as much a measurement problem as a hinted
	// call that does.
	if tc.ExpectID != "" && (result.Status == contractsv1.ContextFabricInvestigationNoMatch || (!isWindow && baseline.Status == contractsv1.ContextFabricInvestigationNoMatch)) {
		res.FalseNoMatch = true
	}

	if !isWindow && res.CommittedCount > 0 {
		hintedDecision, _ := trace.finalDecisionEvent()
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
				result.SubjectResolution.Committed, baseline.SubjectResolution.Committed,
				hintedDecision.Outcome, baselineDecision.Outcome,
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

func runTwoTurnConfirmedWrongArm(ctx context.Context, investigator contextfabric.Investigator, store twoTurnResultStoreSaver, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, timeout time.Duration, anchorTerms anchorTermIndex, runToken string, trace *twoTurnTraceCapture) twoTurnCaseResult {
	res := twoTurnCaseResult{Index: index, Member: entry.Member, Arm: string(twoTurnArmConfirmedWrong)}
	// Stamped BEFORE the early returns below (a seeded-negative failure, a
	// missing offer): a row that never reached Investigate still says what
	// this case expected, which is what makes an arm-invalid row readable
	// without the corpus annex.
	twoTurnStampOutcome(&res, tc, nil)

	var offerResultID, receiptID string
	// setupSynthesisOverride (CHAOS-4103, same class as
	// runTwoTurnInferredTierArm's baselineSynthesisOverride): the setup
	// turn below is a REAL Investigate() call and can independently trip
	// applySynthesisStatusOverride, so it needs the SAME
	// reset-before/capture-after discipline or the redemption call's own
	// reset would silently discard it.
	var setupSynthesisOverride *contextfabric.SynthesisStatusOverrideOutcome
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
		if trace != nil {
			setupSynthesisOverride = trace.synthesisOverride
		}
		if err != nil {
			twoTurnStampArmFailure(&res, "setup turn failed: "+contextFabricRejectionClass(err), err)
			return res
		}
		offerResultID = setupResult.ResultID
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
	if err != nil {
		res.Turn2Status = "error:" + contextFabricRejectionClass(err)
		twoTurnStampArmFailure(&res, "investigate error: "+contextFabricRejectionClass(err), err)
		return res
	}
	res.Turn2Status = string(turn2.Status)
	res.CommittedCount = len(turn2.SubjectResolution.Committed)
	res.Applied = memberApplied(turn2, entry.Member)
	res.WrongCommit = twoTurnCommittedWrong(turn2.SubjectResolution.Committed, tc)
	twoTurnStampOutcome(&res, tc, turn2.SubjectResolution.Committed)
	twoTurnStampDecision(&res, trace)
	// CHAOS-4103 (codex review round 1, generalized): fold the setup
	// turn's own override occurrence back in -- see
	// twoTurnFoldSynthesisStatusOverride's own doc comment.
	twoTurnFoldSynthesisStatusOverride(&res, setupSynthesisOverride)
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
			return res
		}
		res.Turn2Status = string(result.Status)
		res.CommittedCount = len(result.SubjectResolution.Committed)
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// CHAOS-4103: traceCapture's embedded SlogEngineTelemetry is built from
	// the SAME logger every other sink in this function uses (not
	// slog.Default() -- see buildContextFabricGraphReader's own comment on
	// why that distinction matters), so the WARN line
	// RecordSynthesisStatusOverride still emits is gated by this run's
	// actual log level, not an unconfigured default.
	traceCapture := &twoTurnTraceCapture{SlogEngineTelemetry: contextfabric.NewSlogEngineTelemetry(logger)}
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

	// Transport label reflects which transport actually ran (codex round-1
	// finding #10: hard-coding "real_api" while a file-exchange runtime is
	// wired gives the acceptance artifact false provenance).
	transportLabel := "real_api"
	if exchangeDir != "" {
		transportLabel = "file_exchange"
	}
	report := twoTurnReport{
		ReportSchemaVersion: "13",
		Provenance: trialProvenance{
			CorpusSHA256: corpusHash, Transport: transportLabel, RunStartedAt: runStartedAt,
			SourceCommit: source.commit, SourceDirty: source.dirty, SourceDiffDigest: source.diffDigest,
			AnchorMembershipOffersEnabled: cfg.AnchorMembershipOffersEnabled,
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
		BaseSHA:         requireEnv(t, "ACR_TEST_TRIAL_BASE_SHA"),
		OracleAnnexPath: annexPath, OracleAnnexCorpusSHA: annex.CorpusSHA256, OracleAnnexSignedOff: annex.SignedOff,
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

		turn1Req := twoTurnRequest(entry.Index, tc, "turn1")
		if traceCapture != nil {
			traceCapture.reset()
		}
		modelCallCapture.reset()
		turn1Started := time.Now()
		turn1Ctx, turn1Cancel := context.WithTimeout(ctx, caseTimeout)
		turn1, err := investigator.Investigate(turn1Ctx, principal, turn1Req)
		turn1Cancel()
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
		positive := runTwoTurnPositiveArm(ctx, investigator, principal, entry.Index, tc, entry, turn1, caseTimeout, traceCapture)
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
		report.Results = append(report.Results, positive)

		modelCallCapture.reset()
		inferredStarted := time.Now()
		inferred := runTwoTurnInferredTierArm(ctx, investigator, principal, entry.Index, tc, entry, caseTimeout, traceCapture, windowBandByIndex[entry.Index])
		inferredTiming = buildTwoTurnArmTiming(string(twoTurnArmInferredTier), inferredStarted, modelCallCapture)
		if inferred.PairInvalid {
			report.InferredPairInvalidCount++
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
			if inferred.ArmInvalidReason != "" {
				report.WindowArmErrorCount++
			} else {
				report.WindowInferredTierRanCount++
				if inferred.CommittedCount > 0 {
					report.WindowCommitCount++
				}
				if inferred.Turn2Status == string(contractsv1.ContextFabricInvestigationClarificationRequired) &&
					inferred.TierRoutedCorrectly && inferred.CommittedCount == 0 {
					report.WindowGatedCount++
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
		report.Results = append(report.Results, inferred)

		modelCallCapture.reset()
		confirmedWrongStarted := time.Now()
		confirmedWrong := runTwoTurnConfirmedWrongArm(ctx, investigator, store, principal, entry.Index, tc, entry, caseTimeout, anchorTerms, runToken, traceCapture)
		confirmedWrongTiming = buildTwoTurnArmTiming(string(twoTurnArmConfirmedWrong), confirmedWrongStarted, modelCallCapture)
		if confirmedWrong.ArmInvalidReason == "" && confirmedWrong.Applied && entry.NegativeCommittable {
			report.ConfirmedWrongRedeemedCount[entry.Member]++
		}
		if confirmedWrong.WrongCommit {
			report.WrongCommitCount++
		}
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
					report.MutationProbesRun[mutationResult.MutationProbe]++
					if mutationResult.MutationTripped {
						report.MutationProbesTripped[mutationResult.MutationProbe]++
					}
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
	// ExpectedKinds never filters the ordinary candidate pool (resolve.go),
	// and SubjectHandles is not consumed by the census at all (handles are
	// bound from question text, chaos3899_evidence_round.go) -- neither
	// hint reaches interpretation or synthesis (genkitruntime/runtime.go)
	// -- so for kind/handle the correctness proposition is NONINTERFERENCE
	// (baseline_equivalent), not universal all-kinds census proof.
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
