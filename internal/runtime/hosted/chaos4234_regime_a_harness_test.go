package hosted_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4234 harness helpers for the two-turn confirmation trial: the
// subject-level trace readers behind expected_subject_in_pool/
// expected_subject_rank/expected_subject_at_offer_boundary, the regime-A
// window-receipt attachment for the positive arm's turn 2, and the
// "answered" predicate behind regime_a_turn2_answered_count. Kept beside
// their own unit tests so the report's new fields are pinned by the
// SAME file that derives them.

// poolContainsSubject is poolContainsKind's subject-exact twin: true iff a
// "corroboration" event named this exact (kind, canonical id). Both must
// match -- a bare id could collide across kinds -- and an empty id never
// matches (a control case has no expected subject).
func (c *twoTurnTraceCapture) poolContainsSubject(kind, canonicalID string) bool {
	if kind == "" || canonicalID == "" {
		return false
	}
	for _, e := range c.events {
		if e.Stage == "corroboration" && string(e.Subject.Kind) == kind && e.Subject.CanonicalID == canonicalID {
			return true
		}
	}
	return false
}

// rankedCutFor reads the LAST "ranked_cut" batch (Rank==1 opens a batch;
// a re-decision emits a fresh one) and reports the expected subject's
// rank in it (0 = never reached the cut) and whether it reached the offer
// builders' shared input: survived the cut, OR bypassed it as a
// coverage-floor find (CoverageBypass companion, Rank 0, emitted after the
// batch). See graphrank.ResolutionTraceEvent.Rank's own doc comment.
func (c *twoTurnTraceCapture) rankedCutFor(kind, canonicalID string) (rank int, atOfferBoundary bool) {
	if kind == "" || canonicalID == "" {
		return 0, false
	}
	for _, e := range c.events {
		if e.Stage != "ranked_cut" {
			continue
		}
		if e.Rank == 1 {
			// A fresh batch supersedes everything read so far, bypass
			// companions included -- they are re-emitted after the batch
			// that actually feeds the offer builders.
			rank, atOfferBoundary = 0, false
		}
		if string(e.Subject.Kind) != kind || e.Subject.CanonicalID != canonicalID {
			continue
		}
		if e.CoverageBypass {
			atOfferBoundary = true
			continue
		}
		rank = e.Rank
		atOfferBoundary = e.Survived
	}
	return rank, atOfferBoundary
}

// retrievalSourceFor (CHAOS-4348, schema v37) reports "exact_name" or
// "kind_scoped" if an "exact_name_search"/"kind_hint_search" trace event
// (graphrank/chaos4348_reachability.go's traceExactNameSearch/
// traceKindHintSearch) named this exact (kind, canonical id) ANYWHERE in
// this resolution's trace, else "ordinary" (found by Search/SearchQuestion/
// AliasLookup/the coverage floor's own real-pool census kinds --
// everything that predates this ticket), else "absent" (poolContainsSubject
// is false: never reached the pool by any path).
//
// HONEST LIMITATION (codex review, Medium, confirmed): this reports whether
// the new arm FIRED for this subject, not that it was EXCLUSIVELY
// responsible -- ordinary Search has no per-subject trace event to compare
// against, so a subject ordinary search would have found anyway, but that
// the new arm ALSO (redundantly) rediscovers, is indistinguishable here
// from one only the new arm could reach. Reading this field as "the new arm
// was load-bearing" therefore over-claims; reading it as "the new arm fired
// for this subject" (which is what it actually measures) does not. Report
// consumers should treat exact_name/kind_scoped rates as an upper bound on
// how often the new arms mattered, not a proof each instance needed them.
func (c *twoTurnTraceCapture) retrievalSourceFor(kind, canonicalID string) string {
	if !c.poolContainsSubject(kind, canonicalID) {
		return "absent"
	}
	for _, e := range c.events {
		if string(e.Subject.Kind) != kind || e.Subject.CanonicalID != canonicalID {
			continue
		}
		switch e.Stage {
		case "exact_name_search":
			return "exact_name"
		case "kind_hint_search":
			return "kind_scoped"
		}
	}
	return "ordinary"
}

// twoTurnRegimeAWindowReceipt returns the turn-1 window receipt the
// positive arm attaches beside a non-window member's own receipt on a
// regime-A case: found iff the case has an oracle window band AND turn 1
// offered exactly that band. The window member's own positive arm already
// redeems it as ITS receipt, so nothing is attached there.
func twoTurnRegimeAWindowReceipt(turn1 contractsv1.ContextFabricInvestigationResult, member, regimeAWindowBand string) (contractsv1.ContextFabricBoundSubjectReceipt, bool) {
	if regimeAWindowBand == "" || member == string(contractsv1.ContextFabricStructureNeedWindow) {
		return contractsv1.ContextFabricBoundSubjectReceipt{}, false
	}
	receiptID, found := selectOracleOffer(turn1, string(contractsv1.ContextFabricStructureNeedWindow), oracleOfferQuery{windowBand: regimeAWindowBand})
	if !found {
		return contractsv1.ContextFabricBoundSubjectReceipt{}, false
	}
	return contractsv1.ContextFabricBoundSubjectReceipt{ResultID: turn1.ResultID, ReceiptID: receiptID}, true
}

// twoTurnRegimeAOfferComposed (CHAOS-4234, codex round-1 finding 2,
// confirmed and fixed) is the "composed" predicate behind
// RegimeAOfferComposedCount. turn1's own OfferComposedUnderWindowGate
// (the kind_offer trace's OfferedUnderWindowGate) is true whenever the
// offers-only PASS RAN, not whether it actually produced an offer -- an
// empty pool or a single-kind pool runs the pass and still discloses
// window-only, so gating the counter on that flag alone counted
// resolution MODE, not composed offers. This reads turn 1's OWN
// persisted StructureNeeds.Missing directly instead:
// composeGatedStructureNeeds always prepends window first, so more than
// one entry means the offers-only material genuinely added a member --
// the SAME "composed" outcome chaos4234_offers_only.go's own
// GatedOfferResolutionComposed reports engine-side.
func twoTurnRegimeAOfferComposed(turn1 contractsv1.ContextFabricInvestigationResult) bool {
	return turn1.StructureNeeds != nil && len(turn1.StructureNeeds.Missing) > 1
}

// twoTurnStatusAnswered is the closed "the case actually answered"
// predicate behind regime_a_turn2_answered_count: complete or partial.
// degraded/clarification_required/no_match/errors are not answers.
func twoTurnStatusAnswered(status string) bool {
	switch status {
	case string(contractsv1.ContextFabricInvestigationComplete), string(contractsv1.ContextFabricInvestigationPartial):
		return true
	}
	return false
}

func chaos4234Subject(kind, id string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(kind), CanonicalID: id, Label: id}
}

func TestCHAOS4234_PoolContainsSubject_MatchesKindAndIDExactly(t *testing.T) {
	t.Parallel()
	trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
		{Stage: "corroboration", Subject: chaos4234Subject("work_item", "wi_7")},
		{Stage: "decision", Subject: chaos4234Subject("work_item", "wi_9")},
	}}
	if !trace.poolContainsSubject("work_item", "wi_7") {
		t.Fatal("poolContainsSubject(work_item, wi_7) = false, want true (corroborated)")
	}
	if trace.poolContainsSubject("pull_request", "wi_7") {
		t.Fatal("poolContainsSubject(pull_request, wi_7) = true, want false (kind must match too)")
	}
	if trace.poolContainsSubject("work_item", "wi_9") {
		t.Fatal("poolContainsSubject(work_item, wi_9) = true, want false (a decision event is not pool membership)")
	}
	if trace.poolContainsSubject("work_item", "") || trace.poolContainsSubject("", "wi_7") {
		t.Fatal("poolContainsSubject with an empty kind or id = true, want false")
	}
}

func TestCHAOS4234_RankedCutFor_ReadsTheLastBatchAndTheBypassCompanion(t *testing.T) {
	t.Parallel()
	target := chaos4234Subject("work_item", "wi_target")
	other := chaos4234Subject("pull_request", "pr_other")
	trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
		// First batch: target ranked 3rd, dropped by the cut.
		{Stage: "ranked_cut", Subject: other, Rank: 1, Survived: true},
		{Stage: "ranked_cut", Subject: chaos4234Subject("work_item", "wi_x"), Rank: 2, Survived: true},
		{Stage: "ranked_cut", Subject: target, Rank: 3, Survived: false},
		// Re-decision: fresh batch, target now 2nd and inside the cut.
		{Stage: "ranked_cut", Subject: other, Rank: 1, Survived: true},
		{Stage: "ranked_cut", Subject: target, Rank: 2, Survived: true},
	}}
	rank, boundary := trace.rankedCutFor("work_item", "wi_target")
	if rank != 2 || !boundary {
		t.Fatalf("rankedCutFor = (%d, %v), want (2, true): the LAST batch wins", rank, boundary)
	}

	dropped := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
		{Stage: "ranked_cut", Subject: other, Rank: 1, Survived: true},
		{Stage: "ranked_cut", Subject: target, Rank: 2, Survived: false},
	}}
	if rank, boundary := dropped.rankedCutFor("work_item", "wi_target"); rank != 2 || boundary {
		t.Fatalf("rankedCutFor(dropped) = (%d, %v), want (2, false)", rank, boundary)
	}

	bypass := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
		{Stage: "ranked_cut", Subject: other, Rank: 1, Survived: true},
		{Stage: "ranked_cut", Subject: target, CoverageBypass: true},
	}}
	if rank, boundary := bypass.rankedCutFor("work_item", "wi_target"); rank != 0 || !boundary {
		t.Fatalf("rankedCutFor(bypass) = (%d, %v), want (0, true): a coverage-floor find reaches the offer boundary without a rank", rank, boundary)
	}

	absent := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
		{Stage: "ranked_cut", Subject: other, Rank: 1, Survived: true},
	}}
	if rank, boundary := absent.rankedCutFor("work_item", "wi_target"); rank != 0 || boundary {
		t.Fatalf("rankedCutFor(absent) = (%d, %v), want (0, false)", rank, boundary)
	}

	staleBypass := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
		{Stage: "ranked_cut", Subject: target, CoverageBypass: true},
		{Stage: "ranked_cut", Subject: other, Rank: 1, Survived: true},
	}}
	if rank, boundary := staleBypass.rankedCutFor("work_item", "wi_target"); rank != 0 || boundary {
		t.Fatalf("rankedCutFor(stale bypass before a fresh batch) = (%d, %v), want (0, false): a fresh batch supersedes an earlier companion", rank, boundary)
	}
}

func TestCHAOS4234_RegimeAWindowReceipt_AttachedOnlyForNonWindowMembersWithAnOfferedBand(t *testing.T) {
	t.Parallel()
	turn1 := contractsv1.ContextFabricInvestigationResult{
		ResultID: "result_turn1_4234",
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow, contractsv1.ContextFabricStructureNeedExpectedKind},
			WindowOptions: []contractsv1.ContextFabricWindowOption{
				{ReceiptID: "winr_4234aaaaaaaaaaaa", OptionID: "opt_90d", RelativeID: "trailing_90d"},
				{ReceiptID: "winr_4234bbbbbbbbbbbb", OptionID: "opt_all", RelativeID: "all_time"},
			},
		},
	}
	receipt, ok := twoTurnRegimeAWindowReceipt(turn1, string(contractsv1.ContextFabricStructureNeedExpectedKind), "trailing_90d")
	if !ok || receipt.ResultID != "result_turn1_4234" || receipt.ReceiptID != "winr_4234aaaaaaaaaaaa" {
		t.Fatalf("twoTurnRegimeAWindowReceipt(expected_kind, trailing_90d) = (%#v, %v), want the trailing_90d receipt bound to turn 1", receipt, ok)
	}
	if _, ok := twoTurnRegimeAWindowReceipt(turn1, string(contractsv1.ContextFabricStructureNeedWindow), "trailing_90d"); ok {
		t.Fatal("window member: attached, want not attached (its own positive arm already redeems the window receipt)")
	}
	if _, ok := twoTurnRegimeAWindowReceipt(turn1, string(contractsv1.ContextFabricStructureNeedExpectedKind), ""); ok {
		t.Fatal("empty band (not a regime-A case, or no window oracle entry): attached, want not attached")
	}
	if _, ok := twoTurnRegimeAWindowReceipt(turn1, string(contractsv1.ContextFabricStructureNeedExpectedKind), "trailing_30d"); ok {
		t.Fatal("band turn 1 never offered: attached, want not attached")
	}
}

func TestCHAOS4234_RegimeAOfferComposed_TrueOnlyWhenTurn1AddedAMemberBeyondWindow(t *testing.T) {
	t.Parallel()
	windowOnly := contractsv1.ContextFabricInvestigationResult{
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
		},
	}
	if twoTurnRegimeAOfferComposed(windowOnly) {
		t.Fatal("window-only Missing (offers-only pass ran, nothing to offer): composed = true, want false")
	}
	composed := contractsv1.ContextFabricInvestigationResult{
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow, contractsv1.ContextFabricStructureNeedExpectedKind},
		},
	}
	if !twoTurnRegimeAOfferComposed(composed) {
		t.Fatal("window + expected_kind in Missing: composed = false, want true")
	}
	if twoTurnRegimeAOfferComposed(contractsv1.ContextFabricInvestigationResult{}) {
		t.Fatal("nil StructureNeeds: composed = true, want false")
	}
}

func TestCHAOS4234_StatusAnswered_ClosedVocabulary(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]bool{
		"complete": true, "partial": true,
		"degraded": false, "clarification_required": false, "no_match": false, "": false, "error:timeout": false,
	} {
		if got := twoTurnStatusAnswered(status); got != want {
			t.Fatalf("twoTurnStatusAnswered(%q) = %v, want %v", status, got, want)
		}
	}
}
