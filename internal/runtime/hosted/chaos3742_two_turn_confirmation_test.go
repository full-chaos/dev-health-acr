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

// singleSatisfierVerified reports whether the captured "evidence_round"
// shadow-stage event (kindInsensitivityProof's own trace point) recorded a
// would_commit outcome -- the INDEPENDENT, production-observed proof that
// the all-kinds census found exactly one satisfier, i.e. that a commit
// under inferred-tier narrowing is provably sound per design brief §2.0,
// not merely assumed because a commit happened at all.
func (c *twoTurnTraceCapture) singleSatisfierVerified() bool {
	for _, e := range c.events {
		if e.Stage == "evidence_round" && e.ShadowOutcome == string(graphrank.ShadowWouldCommit) {
			return true
		}
	}
	return false
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
	// TierRoutedCorrectly (inferred_tier arm only) asserts the injected
	// explicit value actually landed at Source=explicit_unattributed,
	// Provenance=inferred_default in the echo -- not merely "did not
	// commit" (codex round-1 finding #6: a tier-routing regression that
	// instead landed the value at question_stated could still fail to
	// commit for an unrelated census reason, silently passing a commit-only
	// check).
	TierRoutedCorrectly bool `json:"tier_routed_correctly,omitempty"`
	// SingleSatisfierVerified (inferred_tier arm only, orchestrator ruling
	// 2026-08-20 post-live-run): whether THIS commit's claim to the design
	// brief §2.0 kind-insensitivity exception (all-kinds-satisfier-count==1)
	// was INDEPENDENTLY OBSERVED, not assumed from "production shouldn't
	// have committed otherwise" (a circular argument if the enforcement
	// itself has a bug -- precisely the risk an untested-live pivot
	// carries). Read directly off the SAME "evidence_round" shadow-stage
	// trace event kindInsensitivityProof itself populates
	// (graphrank/chaos3899_evidence_round.go), captured in-process via
	// hosted.Options.ResolutionTracer -- never re-derived by a parallel
	// census implementation. Only meaningful when CommittedCount>0; false
	// otherwise (no commit to verify).
	SingleSatisfierVerified bool   `json:"single_satisfier_verified,omitempty"`
	CommittedCount          int    `json:"committed_count"`
	WrongCommit             bool   `json:"wrong_commit"`
	MutationProbe           string `json:"mutation_probe,omitempty"`
	MutationTripped         bool   `json:"mutation_tripped,omitempty"`
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
	// under the SAME struct with no version marker). "2" marks this
	// reshaped report (the rename plus the new controls/anti-vacuity/
	// single-satisfier fields below); "1" was the shape PR #167 shipped.
	// Bump this again on any future field rename or removal so a consumer
	// can detect drift instead of silently reading a stale key.
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
	GateReachableCount   int `json:"gate_reachable_count"`
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
	InferredTierAnyCommit                   int            `json:"inferred_tier_any_commit_count"`
	// InferredTierSingleSatisfierVerifiedCount is PER COMMIT (not per
	// case): how many of InferredTierAnyCommit's own commits carried an
	// INDEPENDENTLY-OBSERVED (SingleSatisfierVerified) proof that the
	// design brief §2.0 kind-insensitivity exception actually held for
	// that commit. If this is LESS than InferredTierAnyCommit, that is a
	// genuine, more serious finding than the bar violation alone: it means
	// at least one commit happened WITHOUT the exception's own proof
	// backing it.
	InferredTierSingleSatisfierVerifiedCount int `json:"inferred_tier_single_satisfier_verified_count"`
	// ConfirmedWrongRedeemedCount is PER APPLICABLE MEMBER (codex round-1
	// finding #4: a global scalar lets one member's success mask another
	// member's permanently-unredeemable designated negative).
	ConfirmedWrongRedeemedCount map[string]int `json:"confirmed_wrong_redeemed_committable_count"`
	AntiVacuityValid            bool           `json:"anti_vacuity_valid"`
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
	// ControlsTotal/ControlsWitnessed: see controlSeen/controlWitnessed's
	// own doc comment at the call site for the exact definition and its
	// limits (best-effort Status==no_match proxy, no attestation
	// visibility).
	ControlsTotal     int                 `json:"controls_total"`
	ControlsWitnessed int                 `json:"controls_witnessed"`
	Results           []twoTurnCaseResult `json:"results"`
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
	if trace != nil {
		trace.reset()
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := investigator.Investigate(callCtx, principal, req)
	if err != nil {
		res.Turn2Status = "error:" + contextFabricRejectionClass(err)
		res.ArmInvalidReason = "investigate error: " + contextFabricRejectionClass(err)
		return res
	}
	res.Turn2Status = string(result.Status)
	res.CommittedCount = len(result.SubjectResolution.Committed)
	if res.CommittedCount > 0 && trace != nil {
		res.SingleSatisfierVerified = trace.singleSatisfierVerified()
	}
	// Positive tier-routing proof (codex round-1 finding #6, codex round-2
	// finding: window's own echo is a SEPARATE mechanism -- window.go's
	// windowExplicitProvenance stamps EffectiveEvidenceWindow.Provenance,
	// never ConfirmedStructure; window is not part of composeConfirmedStructure
	// at all despite the design brief's aspirational "same uniform
	// mechanism" framing). Checked directly per member, not inferred from
	// "did not commit".
	if entry.Member == string(contractsv1.ContextFabricStructureNeedWindow) {
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
		ReportSchemaVersion: "2",
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

	// ACR_TEST_TRIAL_LIMIT bounds how many annex entries this run processes
	// (codex round-1 finding #11: run-two-turn.sh already exports this, but
	// nothing read it). Mirrors TestGenerativeTrialCorpus's own limit
	// semantics: cap, never reorder.
	entries := annex.Entries
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
	// controlSeen/controlWitnessed back the DP9 "controls X/19" report
	// (orchestrator ruling 2026-08-20): a control is a corpus case with no
	// expected answer (trialCase.ExpectID=="" -- the SAME definition
	// generative_trial_live_test.go's own IsControl already uses, not a
	// new band-based one). "Witnessed" here means turn 1's own Status was
	// no_match -- a best-effort proxy for D0's "no_match remains WITNESSED
	// (attestation present)": this harness has no visibility into the
	// internal census attestation bit itself, only the wire-level Status,
	// and says so explicitly in the report rather than claiming more than
	// it observed.
	controlSeen := map[int]bool{}
	controlWitnessed := map[int]bool{}
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
		turn1Ctx, turn1Cancel := context.WithTimeout(ctx, caseTimeout)
		turn1, err := investigator.Investigate(turn1Ctx, principal, turn1Req)
		turn1Cancel()
		if err != nil {
			t.Logf("case %d: turn 1 error: %v", entry.Index, err)
			continue
		}
		report.CasesRun++
		if tc.ExpectID == "" {
			if turn1.Status == contractsv1.ContextFabricInvestigationNoMatch {
				controlWitnessed[entry.Index] = true
			}
		}
		// codex round-2 finding #1: StructureNeeds and WindowClarification
		// are composed INDEPENDENTLY on the subjectless-terminal path
		// (unresolved.go) -- window is never added to
		// StructureOfferMaterial.Missing (structure.go's composeStructureNeeds
		// only tracks kind/anchor/handle), so a window-only stalled case can
		// have StructureNeeds==nil while WindowClarification is non-nil.
		// Skipping on StructureNeeds alone would silently drop every
		// window-only case from every arm.
		if turn1.StructureNeeds == nil && turn1.WindowClarification == nil {
			report.StructureAndWindowDisclosureAbsentCount++
			continue
		}

		positive := runTwoTurnPositiveArm(ctx, investigator, principal, entry.Index, tc, entry, turn1, caseTimeout)
		if positive.OfferMiss {
			report.OfferMissCount[entry.Member]++
		}
		if positive.Applied {
			report.PositiveAppliedCount++
		}
		if positive.CommittedCount > 0 {
			report.GateReachableCount++
		}
		if positive.WrongCommit {
			report.WrongCommitCount++
		}
		report.Results = append(report.Results, positive)

		inferred := runTwoTurnInferredTierArm(ctx, investigator, principal, entry.Index, tc, entry, caseTimeout, traceCapture)
		if inferred.ArmInvalidReason == "" && inferred.CommittedCount > 0 {
			report.InferredTierAnyCommit++
			if inferred.SingleSatisfierVerified {
				report.InferredTierSingleSatisfierVerifiedCount++
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

		t.Logf("case %d member=%s: positive(applied=%v miss=%v) inferred(commit=%d invalid=%q) confirmed_wrong(applied=%v wrong=%v invalid=%q)",
			entry.Index, entry.Member, positive.Applied, positive.OfferMiss, inferred.CommittedCount, inferred.ArmInvalidReason,
			confirmedWrong.Applied, confirmedWrong.WrongCommit, confirmedWrong.ArmInvalidReason)
	}
	// Per-member anti-vacuity (codex round-1 finding #4): valid only once
	// EVERY member with a designated committable negative has redeemed at
	// least one.
	var unsatisfiedMembers []string
	for member := range applicableMembers {
		if report.ConfirmedWrongRedeemedCount[member] < 1 {
			unsatisfiedMembers = append(unsatisfiedMembers, member)
		}
	}
	sort.Strings(unsatisfiedMembers)
	report.AntiVacuityValid = len(applicableMembers) > 0 && len(unsatisfiedMembers) == 0

	report.ControlsTotal = len(controlSeen)
	report.ControlsWitnessed = len(controlWitnessed)

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

	// Fail-open guard (codex round-3 finding #2): a run where every case
	// offer-missed or errored could otherwise pass -- zero wrong commits
	// and a satisfied anti-vacuity check prove nothing when NOTHING ever
	// actually converted. This is checked BEFORE anti-vacuity so a
	// completely broken harness fails on the more fundamental signal
	// first, not last.
	if report.PositiveAppliedCount == 0 {
		t.Errorf("positive_applied_count=0 across %d cases -- the positive arm never converted a single case, so this run proves nothing about conversion (fails open otherwise: zero wrong commits and a vacuously-true anti-vacuity check would not catch a harness that never actually confirms anything)", report.CasesRun)
	}
	if !report.AntiVacuityValid {
		t.Errorf("confirmed_wrong arm anti-vacuity check failed: members %v redeemed zero designated committable negatives (design brief v4/sol-r3 #4) -- the arm is INVALID for this run", unsatisfiedMembers)
	}
	if report.WrongCommitCount > 0 {
		t.Errorf("wrong_commit_count=%d, want 0 (DP9: ZERO wrong commits, period)", report.WrongCommitCount)
	}
	if report.InferredTierAnyCommit > 0 {
		t.Errorf("inferred_tier_any_commit_count=%d, want 0 (any commit under unconfirmed inferred-tier structure fails the run)", report.InferredTierAnyCommit)
	}
	// Distinct, LOUDER signal (orchestrator ruling 2026-08-20): an
	// inferred-tier commit with NO independently-observed single-satisfier
	// proof backing it is more serious than the bar violation above -- it
	// means the design brief §2.0 kind-insensitivity exception cannot be
	// verified for that commit, not merely that the zero-tolerance
	// instrument bar was violated (which every inferred-tier commit
	// violates by construction).
	if unverified := report.InferredTierAnyCommit - report.InferredTierSingleSatisfierVerifiedCount; unverified > 0 {
		t.Errorf("UNVERIFIED INFERRED-TIER COMMIT: %d of %d inferred-tier commits have NO independently-observed single-satisfier proof (design brief §2.0 kind-insensitivity exception) -- this is a correctness finding, not just a bar violation", unverified, report.InferredTierAnyCommit)
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
	// D0 controls (design brief: "no_match remains WITNESSED (attestation
	// present)"). ControlsWitnessed is a best-effort Status==no_match
	// proxy -- see controlSeen/controlWitnessed's own doc comment -- so a
	// miss here is reported as a finding, never silently passed.
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
		t.Errorf("controls_total=0: this run recorded NO control cases (entries with no expected answer) -- D0 cannot be reported and the run is INVALID for this check")
	} else if report.ControlsWitnessed < report.ControlsTotal {
		t.Errorf("controls_witnessed=%d/%d, want %d/%d (D0: every control case must be witnessed no_match at turn 1)", report.ControlsWitnessed, report.ControlsTotal, report.ControlsTotal, report.ControlsTotal)
	}
}
