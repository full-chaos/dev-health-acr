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
//     CHAOS-4039 v4 measurement contract (sol-max ruling 2026-08-20):
//     window retains the ORIGINAL "ANY commit fails" bar (WindowCommitCount).
//     CHAOS-4040 (sol-max ruling 2026-08-21, PR #181) shipped an
//     unconditional gate over every inferred window instead of a
//     kind/handle-style noninterference proof -- see WindowGatedCount's own
//     doc comment (twoTurnReport) for the non-vacuity proof this harness
//     needed on top of that. kind/handle
//     commits are no longer an unconditional failure: each DECISIVE commit
//     is classified baseline_equivalent (a paired no-hint request reaches
//     an identical interpretation/decision) or kind_insensitivity_attested
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
	"crypto/sha256"
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
type twoTurnTraceCapture struct {
	events []graphrank.ResolutionTraceEvent
}

func (c *twoTurnTraceCapture) Trace(event graphrank.ResolutionTraceEvent) {
	c.events = append(c.events, event)
}

func (c *twoTurnTraceCapture) reset() {
	c.events = nil
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

// kindInsensitivityResult reports whether kindInsensitivityProof
// (chaos3900_structure_offers.go) was actually CONSULTED during the
// captured call and, when it was, its own closed-vocabulary verdict --
// CHAOS-4039's v4 measurement contract (sol-max ruling 2026-08-20),
// replacing the prior singleSatisfierVerified proxy (a generic
// evidence_round/would_commit check the ruling found insufficient: it
// cannot distinguish "the proof ran and certified this exact commit" from
// "the round reached would_commit for an unrelated reason, or never ran
// the proof at all"). Read directly off ResolutionTraceEvent's own
// ShadowKindInsensitivityEvaluated/ShadowKindInsensitivityOutcome fields
// (resolve.go), themselves populated only inside RunShadowEvidenceRound's
// PreNarrowingExplicitKinds-gated branch (chaos3899_evidence_round.go).
func (c *twoTurnTraceCapture) kindInsensitivityResult() (evaluated bool, outcome string) {
	for _, e := range c.events {
		if e.Stage == "evidence_round" && e.ShadowKindInsensitivityEvaluated {
			return true, e.ShadowKindInsensitivityOutcome
		}
	}
	return false, ""
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
// (zero value, ok=false if none) -- CHAOS-4039's own normalizedDecisionFingerprint
// input. "Last" because a stalled resolution can emit a decision event
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

// --- CHAOS-4039 v4 measurement contract: paired baseline hashing (sol-max
// ruling 2026-08-20, team-lead follow-up ruling 2026-08-20) ---
//
// baselineEquivalent (runTwoTurnInferredTierArm) classifies an inferred-tier
// kind/handle commit as "baseline_equivalent" when a PAIRED no-hint request
// -- the SAME question, issued immediately before the hinted one, with
// neither ExpectedKinds nor SubjectHandles set -- reaches an IDENTICAL
// canonicalInterpretationHash AND (separately -- these are TWO compared
// values, never blended into one hash) an identical
// normalizedDecisionFingerprint, AND the pairing itself is valid
// (pairInvalid in runTwoTurnInferredTierArm is false -- see that function's
// own doc comment for the full precondition list).
//
// Both hashes are SHA-256 over a purpose-built, explicitly-enumerated
// struct -- never the raw wire JSON -- so a field this contract does not
// name (a future addition to either wire type) cannot silently join the
// comparison. Excluded per the ruling: RequestID/ResultID/GeneratedAt
// (request identity), DeterministicAnswer (answer prose),
// StructureNeeds/WindowClarification (StructureOfferMaterial -- the
// hinted call's own receipt-bound offer echo, which the no-hint baseline
// can never produce, by construction, regardless of whether the DECISION
// was identical), and request-local Candidate.ReceiptID values (minted
// fresh per request even for the identical candidate). ConfirmedStructure
// is EXCLUDED for the identical reason StructureOfferMaterial is
// (team-lead ruling, confirmed): the hinted call's explicit-tier echo
// entry (Source=explicit_unattributed/Provenance=inferred_default,
// resolveExplicitStructure) is a mechanical consequence of the hint's mere
// PRESENCE on the wire, not evidence of what was decided -- including it
// would make baseline_equivalent structurally unreachable for every case,
// defeating the classification's own purpose.
//
// normalizedDecisionFingerprint hashes (team-lead ruling, quoting sol's
// exact spec): (a) final Status, (b) the COMPLETE SubjectResolution
// EXCLUDING request-local receipt IDs (Candidates, Committed,
// ClarificationPrompt, RetrievalDegraded -- everything else, unabridged),
// (c) the paired call's own FINAL decision-stage trace event's Outcome/
// Subject/CommitGate/WinningMechanism/SearchTruncated (graphrank's own
// ResolutionTraceEvent -- NOT on the wire InvestigationResult at all,
// which is why this function takes the caller's own captured event
// alongside the result). "Final" because a stalled resolution can emit
// TWO decision events (the initial stalled attempt, then a census-enriched
// re-decision -- TestResolveSubjects_EvidenceCensusCommitsAStalledCandidate,
// graphrank) -- only the LAST one describes what the caller actually
// received (twoTurnTraceCapture.finalDecisionEvent).
//
// Neither hash's OWN VALUE is ever logged or compared to a corpus-derived
// oracle -- only hash-to-hash equality between the two paired calls, the
// SAME one-way-hash discipline traceTermHash/QuestionHash already use
// elsewhere in this codebase for exactly this reason.
type interpretationFingerprintFields struct {
	Shape               string
	RequestedJudgment   string
	SubjectTerms        []string
	ComparisonTerms     []string
	TimeContext         contractsv1.ContextFabricTimeContext
	FactRequirements    []contractsv1.ContextFabricFactRequirement
	ClarificationNeeded bool
	ClarificationReason string
	WindowClass         string
	WindowConfidence    string
}

// canonicalInterpretationHash is the "canonical interpretation hash" half
// of CHAOS-4039's pairing proof: does the model understand the question
// IDENTICALLY with and without the hint present.
func canonicalInterpretationHash(interp contractsv1.ContextFabricInterpretedQuestion) string {
	fields := interpretationFingerprintFields{
		Shape: string(interp.Shape), RequestedJudgment: interp.RequestedJudgment,
		SubjectTerms: interp.SubjectTerms, ComparisonTerms: interp.ComparisonTerms,
		TimeContext: interp.TimeContext, FactRequirements: interp.FactRequirements,
		ClarificationNeeded: interp.ClarificationNeeded, ClarificationReason: interp.ClarificationReason,
		WindowClass: string(interp.WindowClass), WindowConfidence: string(interp.WindowConfidence),
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type subjectRefFingerprintFields struct {
	Kind        string
	CanonicalID string
	Label       string
}

func subjectRefFingerprint(ref contractsv1.ContextFabricSubjectRef) subjectRefFingerprintFields {
	return subjectRefFingerprintFields{Kind: string(ref.Kind), CanonicalID: ref.CanonicalID, Label: ref.Label}
}

type subjectCandidateFingerprintFields struct {
	// ReceiptID deliberately absent -- request-local, minted fresh per
	// request even for the identical candidate (see this section's own
	// header comment).
	Subject         subjectRefFingerprintFields
	State           string
	MatchedTerms    []string
	MatchReasons    []string
	Confidence      float64
	EvidenceRefIDs  []string
	MatchMechanisms []string
}

type decisionTraceFingerprintFields struct {
	Outcome          string
	Subject          subjectRefFingerprintFields
	CommitGate       string
	WinningMechanism string
	SearchTruncated  bool
}

type decisionFingerprintFields struct {
	Status              string
	Candidates          []subjectCandidateFingerprintFields
	Committed           []subjectRefFingerprintFields
	ClarificationPrompt string
	RetrievalDegraded   bool
	DecisionTrace       decisionTraceFingerprintFields
}

// normalizedDecisionFingerprint is the "normalized decision fingerprint"
// half: did resolution reach the SAME decisive outcome via the SAME
// commit path. decision is the paired call's own final decision-stage
// trace event (twoTurnTraceCapture.finalDecisionEvent) -- a zero-value
// ResolutionTraceEvent when the call never reached a decision (e.g. an
// axis/scope refusal before search ever ran), which hashes identically
// across a pair whenever it is legitimately absent from both, and
// differently the moment only one side has it.
func normalizedDecisionFingerprint(result contractsv1.ContextFabricInvestigationResult, decision graphrank.ResolutionTraceEvent) string {
	candidates := make([]subjectCandidateFingerprintFields, 0, len(result.SubjectResolution.Candidates))
	for _, c := range result.SubjectResolution.Candidates {
		mechanisms := make([]string, 0, len(c.MatchMechanisms))
		for _, m := range c.MatchMechanisms {
			mechanisms = append(mechanisms, string(m))
		}
		candidates = append(candidates, subjectCandidateFingerprintFields{
			Subject: subjectRefFingerprint(c.Subject), State: string(c.State),
			MatchedTerms: c.MatchedTerms, MatchReasons: c.MatchReasons,
			Confidence: c.Confidence, EvidenceRefIDs: c.EvidenceRefIDs, MatchMechanisms: mechanisms,
		})
	}
	committed := make([]subjectRefFingerprintFields, 0, len(result.SubjectResolution.Committed))
	for _, c := range result.SubjectResolution.Committed {
		committed = append(committed, subjectRefFingerprint(c))
	}
	fields := decisionFingerprintFields{
		Status: string(result.Status), Candidates: candidates, Committed: committed,
		ClarificationPrompt: result.SubjectResolution.ClarificationPrompt,
		RetrievalDegraded:   result.SubjectResolution.RetrievalDegraded,
		DecisionTrace: decisionTraceFingerprintFields{
			Outcome: decision.Outcome, Subject: subjectRefFingerprint(decision.Subject),
			CommitGate: decision.CommitGate, WinningMechanism: decision.WinningMechanism,
			SearchTruncated: decision.SearchTruncated,
		},
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
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
	// CHAOS-4039 v4 measurement contract, sol-max ruling 2026-08-20,
	// REPLACES the prior SingleSatisfierVerified proxy) is the 3-way
	// partition every DECISIVE (CommittedCount>0) kind/handle inferred-tier
	// outcome gets classified into exactly once: "baseline_equivalent" (a
	// paired no-hint request reached an identical canonicalInterpretationHash
	// + normalizedDecisionFingerprint, and the hinted result was not itself
	// served via answer-reuse -- the hint provably had ZERO causal effect),
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
	// shipped. Bump this again on any future field rename, removal, or
	// meaning change so a consumer can detect drift instead of silently
	// reading a stale key under a new meaning.
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

// TestCanonicalInterpretationHash pins CHAOS-4039's own pairing invariant:
// two interpretations with identical structural content hash identically,
// and the hash changes with any structural field -- the exact property
// baselineEquivalent depends on (runTwoTurnInferredTierArm).
func TestCanonicalInterpretationHash(t *testing.T) {
	t.Parallel()
	base := contractsv1.ContextFabricInterpretedQuestion{
		Shape: contractsv1.ContextFabricShapeOpen, RequestedJudgment: "status",
		SubjectTerms: []string{"acme/widgets"}, TimeContext: contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
	}
	same := base
	if canonicalInterpretationHash(base) != canonicalInterpretationHash(same) {
		t.Error("canonicalInterpretationHash(base) != canonicalInterpretationHash(same identical value), want equal")
	}
	differentJudgment := base
	differentJudgment.RequestedJudgment = "drivers"
	if canonicalInterpretationHash(base) == canonicalInterpretationHash(differentJudgment) {
		t.Error("canonicalInterpretationHash unchanged by RequestedJudgment, want a different hash")
	}
	differentClarify := base
	differentClarify.ClarificationNeeded = true
	if canonicalInterpretationHash(base) == canonicalInterpretationHash(differentClarify) {
		t.Error("canonicalInterpretationHash unchanged by ClarificationNeeded, want a different hash")
	}
}

// TestNormalizedDecisionFingerprint pins the exclusion list CHAOS-4039's
// ruling names (RequestID/ResultID/GeneratedAt/DeterministicAnswer,
// StructureNeeds/WindowClarification, ConfirmedStructure, and per-candidate
// ReceiptID) -- changing ONLY those must NOT change the fingerprint, since
// a paired baseline/hinted call legitimately differs in exactly those ways
// even when the underlying decision is identical -- while sol's named
// INCLUSIONS (the complete SubjectResolution otherwise, and the paired
// call's own final decision-trace fields) MUST each change it.
func TestNormalizedDecisionFingerprint(t *testing.T) {
	t.Parallel()
	decision := graphrank.ResolutionTraceEvent{
		Stage: "decision", Outcome: "committed",
		Subject:    contractsv1.ContextFabricSubjectRef{Kind: "repository", CanonicalID: "repository:acme/widgets"},
		CommitGate: "evidence_census", WinningMechanism: "identity_fast_path", SearchTruncated: true,
	}
	base := contractsv1.ContextFabricInvestigationResult{
		RequestID: "request_a", ResultID: "result_a", GeneratedAt: time.Unix(1000, 0),
		Status: contractsv1.ContextFabricInvestigationComplete, DeterministicAnswer: "the widget repo is healthy",
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{},
		ConfirmedStructure: []contractsv1.ContextFabricConfirmedStructureEntry{
			{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: "repository"},
		},
		SubjectResolution: contractsv1.ContextFabricSubjectResolution{
			Committed: []contractsv1.ContextFabricSubjectRef{{Kind: "repository", CanonicalID: "repository:acme/widgets", Label: "acme/widgets"}},
			Candidates: []contractsv1.ContextFabricSubjectCandidate{
				{ReceiptID: "receipt_a", Subject: contractsv1.ContextFabricSubjectRef{Kind: "repository", CanonicalID: "repository:acme/widgets"}, Confidence: 0.9},
			},
			ClarificationPrompt: "",
			RetrievalDegraded:   false,
		},
	}
	// Exclusions: RequestID/ResultID/GeneratedAt/DeterministicAnswer
	// (request identity, answer prose), StructureNeeds (StructureOfferMaterial),
	// ConfirmedStructure (the hint's own echo), and per-candidate ReceiptID
	// (request-local, minted fresh even for the identical candidate).
	excludedFieldsChanged := base
	excludedFieldsChanged.RequestID, excludedFieldsChanged.ResultID = "request_b", "result_b"
	excludedFieldsChanged.GeneratedAt = time.Unix(2000, 0)
	excludedFieldsChanged.DeterministicAnswer = "a completely different sentence"
	excludedFieldsChanged.StructureNeeds = nil
	excludedFieldsChanged.ConfirmedStructure = nil
	excludedFieldsChanged.SubjectResolution.Candidates = []contractsv1.ContextFabricSubjectCandidate{
		{ReceiptID: "receipt_b_totally_different", Subject: contractsv1.ContextFabricSubjectRef{Kind: "repository", CanonicalID: "repository:acme/widgets"}, Confidence: 0.9},
	}
	if normalizedDecisionFingerprint(base, decision) != normalizedDecisionFingerprint(excludedFieldsChanged, decision) {
		t.Error("normalizedDecisionFingerprint changed by RequestID/ResultID/GeneratedAt/DeterministicAnswer/StructureNeeds/ConfirmedStructure/Candidate.ReceiptID alone, want unchanged (CHAOS-4039's own named exclusions)")
	}

	// Inclusions: Status, Candidates (minus ReceiptID), Committed
	// (including Label -- sol's spec excludes ONLY receipt IDs from
	// SubjectResolution), ClarificationPrompt, RetrievalDegraded, and the
	// decision-trace fields.
	differentStatus := base
	differentStatus.Status = contractsv1.ContextFabricInvestigationClarificationRequired
	if normalizedDecisionFingerprint(base, decision) == normalizedDecisionFingerprint(differentStatus, decision) {
		t.Error("normalizedDecisionFingerprint unchanged by Status, want a different fingerprint")
	}
	differentCommitted := base
	differentCommitted.SubjectResolution.Committed = []contractsv1.ContextFabricSubjectRef{{Kind: "repository", CanonicalID: "repository:other"}}
	if normalizedDecisionFingerprint(base, decision) == normalizedDecisionFingerprint(differentCommitted, decision) {
		t.Error("normalizedDecisionFingerprint unchanged by Committed subject, want a different fingerprint")
	}
	labelOnlyChanged := base
	labelOnlyChanged.SubjectResolution.Committed = []contractsv1.ContextFabricSubjectRef{{Kind: "repository", CanonicalID: "repository:acme/widgets", Label: "a different label"}}
	if normalizedDecisionFingerprint(base, decision) == normalizedDecisionFingerprint(labelOnlyChanged, decision) {
		t.Error("normalizedDecisionFingerprint unchanged by Committed Label, want a different fingerprint -- sol's spec excludes ONLY request-local receipt IDs from SubjectResolution")
	}
	differentConfidence := base
	differentConfidence.SubjectResolution.Candidates = []contractsv1.ContextFabricSubjectCandidate{
		{ReceiptID: "receipt_a", Subject: contractsv1.ContextFabricSubjectRef{Kind: "repository", CanonicalID: "repository:acme/widgets"}, Confidence: 0.1},
	}
	if normalizedDecisionFingerprint(base, decision) == normalizedDecisionFingerprint(differentConfidence, decision) {
		t.Error("normalizedDecisionFingerprint unchanged by Candidate.Confidence, want a different fingerprint")
	}
	differentClarificationPrompt := base
	differentClarificationPrompt.SubjectResolution.ClarificationPrompt = "which one did you mean?"
	if normalizedDecisionFingerprint(base, decision) == normalizedDecisionFingerprint(differentClarificationPrompt, decision) {
		t.Error("normalizedDecisionFingerprint unchanged by ClarificationPrompt, want a different fingerprint")
	}
	differentRetrievalDegraded := base
	differentRetrievalDegraded.SubjectResolution.RetrievalDegraded = true
	if normalizedDecisionFingerprint(base, decision) == normalizedDecisionFingerprint(differentRetrievalDegraded, decision) {
		t.Error("normalizedDecisionFingerprint unchanged by RetrievalDegraded, want a different fingerprint")
	}
	differentDecision := decision
	differentDecision.CommitGate = "lone_floor"
	if normalizedDecisionFingerprint(base, decision) == normalizedDecisionFingerprint(base, differentDecision) {
		t.Error("normalizedDecisionFingerprint unchanged by the paired decision-trace event's CommitGate, want a different fingerprint")
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
		evaluated, outcome := c.kindInsensitivityResult()
		if evaluated || outcome != "" {
			t.Errorf("kindInsensitivityResult() = (%v, %q), want (false, \"\") when ShadowKindInsensitivityEvaluated was never set", evaluated, outcome)
		}
	})
	t.Run("evaluated commit_sound", func(t *testing.T) {
		c := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
			{Stage: "evidence_round", ShadowOutcome: "would_commit", ShadowKindInsensitivityEvaluated: true, ShadowKindInsensitivityOutcome: "commit_sound"},
		}}
		evaluated, outcome := c.kindInsensitivityResult()
		if !evaluated || outcome != "commit_sound" {
			t.Errorf("kindInsensitivityResult() = (%v, %q), want (true, \"commit_sound\")", evaluated, outcome)
		}
	})
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
	receiptID, found := selectOracleOffer(turn1, entry.Member, entry.positiveQuery())
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
		res.ArmInvalidReason = "investigate error: " + contextFabricRejectionClass(err)
		return res
	}
	res.Turn2Status = string(turn2.Status)
	res.CommittedCount = len(turn2.SubjectResolution.Committed)
	res.Applied = memberApplied(turn2, entry.Member)
	res.WrongCommit = twoTurnCommittedWrong(turn2.SubjectResolution.Committed, tc)
	res.Reused = turn2.Reused
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
// CHAOS-4039 v4 measurement contract (sol-max ruling 2026-08-20, team-lead
// follow-up ruling 2026-08-20): for kind/handle members, the hinted call
// is PAIRED with an immediately-preceding no-hint baseline (the SAME
// question, no ExpectedKinds/SubjectHandles set) -- see
// canonicalInterpretationHash/normalizedDecisionFingerprint's own doc
// comment for why, and twoTurnCaseResult.InferredClassification for the
// 3-way classification this pairing feeds. window is EXEMPT from pairing
// (WindowCommitCount's own doc comment: W4 window-insensitivity is
// unimplemented, so window keeps the pre-v4 single-call path and its
// literal zero-commit bar unchanged).
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
func runTwoTurnInferredTierArm(ctx context.Context, investigator contextfabric.Investigator, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, timeout time.Duration, trace *twoTurnTraceCapture) twoTurnCaseResult {
	res := twoTurnCaseResult{Index: index, Member: entry.Member, Arm: string(twoTurnArmInferredTier)}
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

	var baseline contractsv1.ContextFabricInvestigationResult
	var baselineDecision graphrank.ResolutionTraceEvent
	if !isWindow {
		if trace != nil {
			trace.reset()
		}
		baselineReq := twoTurnRequest(index, tc, "inferredtierbaseline")
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
			res.ArmInvalidReason = "baseline investigate error: " + contextFabricRejectionClass(baselineErr)
			return res
		}
		if trace != nil {
			// Captured BEFORE the hinted call's own reset below -- the
			// baseline's decision event would otherwise be lost.
			baselineDecision, _ = trace.finalDecisionEvent()
		}
	}

	if trace != nil {
		trace.reset()
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	result, err := investigator.Investigate(callCtx, principal, req)
	cancel()
	if err != nil {
		res.Turn2Status = "error:" + contextFabricRejectionClass(err)
		res.ArmInvalidReason = "investigate error: " + contextFabricRejectionClass(err)
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
		// pairInvalid (team-lead ruling, widening sol's own list): errors
		// are handled above (return before this point); what remains to
		// check here is exactly the two preconditions that are NOT
		// guaranteed by this function's own single-process, same-investigator
		// construction -- see this function's own doc comment for the full
		// precondition list and why the rest are held by construction.
		pairInvalid := baseline.Reused || result.Reused || baseline.Versions != result.Versions
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
			baselineEquivalent := canonicalInterpretationHash(result.Interpretation) == canonicalInterpretationHash(baseline.Interpretation) &&
				normalizedDecisionFingerprint(result, hintedDecision) == normalizedDecisionFingerprint(baseline, baselineDecision)
			kindAttested := false
			if trace != nil {
				if evaluated, outcome := trace.kindInsensitivityResult(); evaluated && outcome == "commit_sound" {
					kindAttested = trace.evidenceCensusCommitted(result.SubjectResolution.Committed)
				}
			}
			switch {
			case baselineEquivalent:
				res.InferredClassification = "baseline_equivalent"
			case kindAttested:
				res.InferredClassification = "kind_insensitivity_attested"
			default:
				res.InferredClassification = "unjustified"
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
func seedAnchorNegativeResult(ctx context.Context, store twoTurnResultStoreSaver, principal storage.Principal, index int, entry twoTurnOracleEntry, receiptID string, terms anchorTermIndex) (resultID string, err error) {
	term, ok := terms[anchorTermIndexKey(entry.NegativeKind, entry.NegativeAnchorCanonicalID)]
	if !ok {
		return "", fmt.Errorf("no usable alias/label term found in the live identity universe for negative anchor kind=%s canonical_id=%s -- cannot construct a redemption-passing hash", entry.NegativeKind, entry.NegativeAnchorCanonicalID)
	}
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
func runTwoTurnConfirmedWrongArm(ctx context.Context, investigator contextfabric.Investigator, store twoTurnResultStoreSaver, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, timeout time.Duration, anchorTerms anchorTermIndex) twoTurnCaseResult {
	res := twoTurnCaseResult{Index: index, Member: entry.Member, Arm: string(twoTurnArmConfirmedWrong)}

	var offerResultID, receiptID string
	if entry.Member == string(contractsv1.ContextFabricStructureNeedSubjectAnchor) {
		receiptID = contractsv1.ContextFabricAnchorOptionReceiptPrefix + "twoturnseed0000000000000"
		var err error
		offerResultID, err = seedAnchorNegativeResult(ctx, store, principal, index, entry, receiptID, anchorTerms)
		if err != nil {
			res.ArmInvalidReason = "harness-seeded anchor negative could not be made redeemable: " + contextFabricRejectionClass(err)
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
		setupCtx, cancel := context.WithTimeout(ctx, timeout)
		setupResult, err := investigator.Investigate(setupCtx, principal, setupReq)
		cancel()
		if err != nil {
			res.ArmInvalidReason = "setup turn failed: " + contextFabricRejectionClass(err)
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
	turn2, err := investigator.Investigate(callCtx, principal, req)
	if err != nil {
		res.Turn2Status = "error:" + contextFabricRejectionClass(err)
		res.ArmInvalidReason = "investigate error: " + contextFabricRejectionClass(err)
		return res
	}
	res.Turn2Status = string(turn2.Status)
	res.CommittedCount = len(turn2.SubjectResolution.Committed)
	res.Applied = memberApplied(turn2, entry.Member)
	res.WrongCommit = twoTurnCommittedWrong(turn2.SubjectResolution.Committed, tc)
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
func runTwoTurnMutationArm(ctx context.Context, investigator contextfabric.Investigator, principal storage.Principal, index int, tc trialCase, entry twoTurnOracleEntry, turn1ResultID, receiptID string, timeout time.Duration) []twoTurnCaseResult {
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
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
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
			res.ArmInvalidReason = "investigate error (inconclusive, not counted as a trip): " + contextFabricRejectionClass(err)
			return res
		}
		res.Turn2Status = string(result.Status)
		res.CommittedCount = len(result.SubjectResolution.Committed)
		res.Applied = memberApplied(result, entry.Member)
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	traceCapture := &twoTurnTraceCapture{}
	options := hosted.Options{ServiceVersion: "chaos-3742-two-turn", Logger: logger, Now: time.Now, ResolutionTracer: traceCapture}
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

	// Transport label reflects which transport actually ran (codex round-1
	// finding #10: hard-coding "real_api" while a file-exchange runtime is
	// wired gives the acceptance artifact false provenance).
	transportLabel := "real_api"
	if exchangeDir != "" {
		transportLabel = "file_exchange"
	}
	report := twoTurnReport{
		ReportSchemaVersion: "5",
		Provenance: trialProvenance{
			CorpusSHA256: corpusHash, Transport: transportLabel, RunStartedAt: runStartedAt,
			SourceCommit: source.commit, SourceDirty: source.dirty, SourceDiffDigest: source.diffDigest,
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
	// Round-robin by POSITION in annex.Entries (not by entry.Index, which
	// can already be a non-contiguous corpus index) so shards stay
	// balanced regardless of how entries cluster in the annex. Applied
	// BEFORE ACR_TEST_TRIAL_LIMIT below so a limited dry run bounds EACH
	// shard independently, not the pre-shard total.
	entries := annex.Entries
	// codex round-1 finding: ACR_TEST_TRIAL_SHARD_INDEX set with
	// ACR_TEST_TRIAL_SHARD_COUNT unset used to be silently ignored (ran
	// the whole annex, unsharded) -- a partially configured parallel run
	// must fail closed, not fall back to "sequential" without saying so.
	if raw := os.Getenv("ACR_TEST_TRIAL_SHARD_INDEX"); raw != "" && os.Getenv("ACR_TEST_TRIAL_SHARD_COUNT") == "" {
		t.Fatalf("ACR_TEST_TRIAL_SHARD_INDEX=%q is set but ACR_TEST_TRIAL_SHARD_COUNT is not -- both or neither", raw)
	}
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
		sharded := make([]twoTurnOracleEntry, 0, len(entries))
		for i, entry := range entries {
			if i%shardCount == shardIndex {
				sharded = append(sharded, entry)
			}
		}
		entries = sharded
		report.Provenance.ExecutionShape = "parallel"
		report.Provenance.ShardIndex = &shardIndex
		report.Provenance.ShardCount = &shardCount
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
		turn1Ctx, turn1Cancel := context.WithTimeout(ctx, caseTimeout)
		turn1, err := investigator.Investigate(turn1Ctx, principal, turn1Req)
		turn1Cancel()
		if err != nil {
			t.Logf("case %d: turn 1 error: %v", entry.Index, err)
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
			continue
		}

		positive := runTwoTurnPositiveArm(ctx, investigator, principal, entry.Index, tc, entry, turn1, caseTimeout)
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

		inferred := runTwoTurnInferredTierArm(ctx, investigator, principal, entry.Index, tc, entry, caseTimeout, traceCapture)
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

		confirmedWrong := runTwoTurnConfirmedWrongArm(ctx, investigator, store, principal, entry.Index, tc, entry, caseTimeout, anchorTerms)
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
		if positive.Applied && entry.Member != string(contractsv1.ContextFabricStructureNeedWindow) {
			receiptID, found := selectOracleOffer(turn1, entry.Member, entry.positiveQuery())
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

		t.Logf("case %d member=%s: positive(applied=%v miss=%v) inferred(commit=%d class=%q pair_invalid=%v false_no_match=%v invalid=%q) confirmed_wrong(applied=%v wrong=%v invalid=%q)",
			entry.Index, entry.Member, positive.Applied, positive.OfferMiss,
			inferred.CommittedCount, inferred.InferredClassification, inferred.PairInvalid, inferred.FalseNoMatch, inferred.ArmInvalidReason,
			confirmedWrong.Applied, confirmedWrong.WrongCommit, confirmedWrong.ArmInvalidReason)
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
