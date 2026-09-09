package graphrank

// A DECIDED SCOPE ANCHOR MUST SURVIVE PHASE-4 TRUNCATION.
//
// CHAOS-5393's fix put the fallback anchor kind INTO the reserved set
// (frameReservedKinds), and its own pin
// (TestTheFallbackAnchorKindIsReservedAgainstTruncation) deliberately stopped
// there, recording in prose what it could not assert: "it does not claim the
// anchor always survives truncation, because it does not ... a slot can only
// be taken from a candidate whose kind is NOT itself reserved, and on a
// scope-anchored frame the MEMBER kind is reserved too, so a pool saturated
// with members offers no eligible victim."
//
// These pins assert the property that prose describes as missing. They drive
// the PRODUCTION entry point with a member crowd larger than the budget, so
// none of them can pass by constructing the decision it asserts on.
//
// Design cover: CHAOS-4452 §13.5.2 phase-B invariant I11 ("the RESOLVED
// anchor's kind != MemberKind ... the anchor's kind is unknown until the term
// resolves") makes the anchor a subject this resolution must be able to
// commit, and the reserved slot is the CHAOS-4271 ruling's own mechanism
// ("a bounded per-kind reserved slot through truncation"). reservedPrefix's
// own doc comment states the guarantee this restores: a declared kind "does
// not disappear from the offered list purely because a lexically noisier kind
// filled the budget first."

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// saturatedCrowd is larger than resolveScoped's MaxSubjectCandidates budget
// (20) and is entirely of the CONFIRMED MEMBER KIND, which is itself
// reserved -- the shape that leaves the old victim rule with nothing to take.
const saturatedCrowd = 90

// P1 -- SOURCE: RECEIPT. The classification receipt declared the anchor kind.
func TestTheReceiptScopeAnchorSurvivesASaturatedMemberCrowd(t *testing.T) {
	t.Parallel()
	res := resolveScoped(t, anchorOnlyByKindBackend("chaos", saturatedCrowd),
		scopedProjectsFrame("chaos"), confirmedProject(), nil, contextfabric.SubjectTeam)

	kinds := candidateKinds(res)
	if kinds[contextfabric.SubjectProject] == 0 {
		t.Fatalf("member crowd = 0, want a saturated pool; kinds=%v -- the fixture did not build the crowd, so this pin proves nothing", kinds)
	}
	if kinds[contextfabric.SubjectTeam] == 0 {
		t.Errorf("anchor candidates = 0, want >= 1; kinds=%v -- the decided scope anchor was truncated away by a crowd of reserved member kinds", kinds)
	}
}

// P2 -- SOURCE: CONFIRMED-ANCHOR FALLBACK. The receipt carried no kind; the
// caller's own redeemed anchor selection supplied it. It must be
// indistinguishable from the receipt source at truncation.
func TestTheFallbackScopeAnchorSurvivesASaturatedMemberCrowd(t *testing.T) {
	t.Parallel()
	res := resolveScoped(t, anchorOnlyByKindBackend("chaos", saturatedCrowd),
		scopedProjectsFrame("chaos"), confirmedProject(),
		&contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:chaos"}, "")

	kinds := candidateKinds(res)
	if kinds[contextfabric.SubjectProject] == 0 {
		t.Fatalf("member crowd = 0, want a saturated pool; kinds=%v -- the fixture did not build the crowd, so this pin proves nothing", kinds)
	}
	if kinds[contextfabric.SubjectTeam] == 0 {
		t.Errorf("anchor candidates = 0, want >= 1; kinds=%v -- the FALLBACK-sourced anchor was truncated away while the receipt-sourced one survives; the two sources must be indistinguishable downstream", kinds)
	}
}

// P3 -- NEGATIVE CONTROL, THE THIRD SOURCE. With no receipt kind, no
// confirmed anchor and no confirmed member kind there is no DECIDED anchor,
// so there is no slot to reserve and nothing about this resolution may
// change. This is deliberately NOT the acceptance case: an anchor whose kind
// nothing declared is CHAOS-5445's contest to win, not this slot's.
func TestWithNoDecidedAnchorTheSlotIsInertOnASaturatedCrowd(t *testing.T) {
	t.Parallel()
	res := resolveScoped(t, anchorOnlyByKindBackend("chaos", saturatedCrowd),
		scopedProjectsFrame("chaos"), nil, nil, "")

	kinds := candidateKinds(res)
	if kinds[contextfabric.SubjectProject] == 0 {
		t.Fatalf("member crowd = 0; kinds=%v -- fixture defect, the control is vacuous", kinds)
	}
	if kinds[contextfabric.SubjectTeam] != 0 {
		t.Errorf("anchor candidates = %d, want 0; kinds=%v -- no anchor kind was decided, so the reserve must not go looking for one", kinds[contextfabric.SubjectTeam], kinds)
	}
}

// P4 -- NEGATIVE CONTROL, NOT A SCOPE-ANCHORED FRAME. A named-subject frame
// declares no anchor, so the slot must never fire and the cut must stay a
// plain prefix.
func TestANonScopeAnchoredFrameGetsNoAnchorSlot(t *testing.T) {
	t.Parallel()
	frame := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:  contextfabric.SubjectExpressionNamed,
			Named: &contextfabric.NamedSubjectExpression{Terms: []string{"chaos"}},
		},
	}
	res := resolveScoped(t, anchorOnlyByKindBackend("chaos", saturatedCrowd), frame, confirmedProject(), nil, "")

	kinds := candidateKinds(res)
	if kinds[contextfabric.SubjectTeam] != 0 {
		t.Errorf("anchor candidates = %d on a NON scope-anchored frame, want 0; kinds=%v", kinds[contextfabric.SubjectTeam], kinds)
	}
}

// P7 -- THE BUDGET DISPLACES, IT NEVER GROWS. Phase 4's contract (this file's
// reservedPrefix doc comment, and CHAOS-5445's own restatement) is that a
// reserve takes a slot from a survivor rather than adding one.
func TestTheAnchorSlotDisplacesRatherThanGrowingTheBudget(t *testing.T) {
	t.Parallel()
	res := resolveScoped(t, anchorOnlyByKindBackend("chaos", saturatedCrowd),
		scopedProjectsFrame("chaos"), confirmedProject(), nil, contextfabric.SubjectTeam)

	if got := len(res.Candidates); got > 20 {
		t.Errorf("returned %d candidates, want <= the MaxSubjectCandidates budget 20 -- the reserve grew the budget instead of displacing", got)
	}
}

// P6 -- THE SLOT DECISION AND ITS VICTIM ARE OPERATOR-VISIBLE AT INFO.
//
// Driven through the REAL NewSlogResolutionTracer at the production default
// level, exactly like chaos5222_tracer_rig_visibility_test.go does, because a
// recording fake proves formatting only. Every key carries an explicit value
// on every pass, so a build that stopped deciding a slot can never read like
// a resolution that reserved none.
func TestTheAnchorSlotDecisionAndItsVictimAreVisibleAtProductionLogLevel(t *testing.T) {
	t.Parallel()
	backend := anchorOnlyByKindBackend("chaos", saturatedCrowd)
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20

	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("chaos"), deps,
		confirmedProject(), nil, scopedProjectsFrame("chaos"), contextfabric.SubjectTeam); err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}

	log := buf.String()
	if log == "" {
		t.Fatal("captured log is EMPTY -- every assertion below would be vacuous")
	}
	for _, key := range []string{"anchor_slot_reserved", "anchor_slot_source", "anchor_slot_displaced", "pool_truncated_n"} {
		if !strings.Contains(log, `"`+key+`"`) {
			t.Errorf("no %q key at the production default log level -- the slot decision is invisible on the rig; log: %s", key, log)
		}
	}
	if !strings.Contains(log, `"anchor_slot_displaced":1`) {
		t.Errorf("anchor_slot_displaced never reports 1 on a resolution whose anchor took a slot from the crowd -- a displacement that happens silently; log: %s", log)
	}
	if !strings.Contains(log, `"anchor_slot_source":"receipt"`) {
		t.Errorf("anchor_slot_source does not name the receipt source -- an operator cannot tell a model that stopped emitting scope_anchor_kind from a caller that stopped redeeming receipts; log: %s", log)
	}
	if !strings.Contains(log, `"stage":"anchor_slot_displaced"`) {
		t.Errorf("no anchor_slot_displaced stage line naming the displaced member -- the reserved slot must never displace a ranked member silently; log: %s", log)
	}
}

// P6b -- EXPLICIT ZEROS. The same four keys must be present, with their
// explicit none/0 values, on a pass that reserves NO anchor slot. A missing
// key and a measured zero must never read alike.
func TestTheAnchorSlotKeysCarryExplicitZerosWhenNoSlotIsReserved(t *testing.T) {
	t.Parallel()
	backend := anchorOnlyByKindBackend("chaos", saturatedCrowd)
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20

	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("chaos"), deps,
		nil, nil, scopedProjectsFrame("chaos"), ""); err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}

	log := buf.String()
	if !strings.Contains(log, `"anchor_slot_reserved":"none"`) {
		t.Errorf("anchor_slot_reserved is not the explicit none token on a pass that reserved no slot; log: %s", log)
	}
	if !strings.Contains(log, `"anchor_slot_source":"none"`) {
		t.Errorf("anchor_slot_source is not the explicit none token on a pass that reserved no slot; log: %s", log)
	}
	if !strings.Contains(log, `"anchor_slot_displaced":0`) {
		t.Errorf("anchor_slot_displaced is not an explicit 0 on a pass that displaced nothing; log: %s", log)
	}
	if !strings.Contains(log, `"pool_truncated_n"`) {
		t.Errorf("pool_truncated_n absent -- the count of candidates the cut dropped is what says whether a slot could have mattered; log: %s", log)
	}
}

// P5 -- THE SLOT NEVER TAKES A RESERVED KIND'S LAST IN-BUDGET MEMBER.
//
// This is the property that keeps the widened victim rule from trading one
// starvation for another: on an ordinary grouped frame the group axis and the
// member axis are BOTH reserved, and a scope anchor must not empty either of
// them. Eligibility is a STRICT surplus (> kindReserveSlotsPerKind), so a kind
// holding exactly one in-budget candidate is never taken -- which is also why
// TestReservedPrefix_OneReservedKindNeverEvictsAnother stays green unchanged.
func TestTheAnchorSlotNeverTakesAReservedKindsLastInBudgetMember(t *testing.T) {
	t.Parallel()
	ordered := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_1"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repo_1"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_anchor"}},
	}
	tiers := []int{2, 2, 2}
	reserved := []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectRepository, contextfabric.SubjectTeam}

	kept, outcome := reservedPrefix(ordered, tiers, 2, reserved,
		anchorReservedSlot{Kind: contextfabric.SubjectTeam, Source: anchorPoolKindScopeReceipt})

	if !kept[0] || !kept[1] {
		t.Errorf("kept=%v: the anchor took the LAST in-budget member of a reserved kind; every reserved kind keeps its own slot", kept)
	}
	if kept[2] {
		t.Error("the anchor was admitted with no eligible victim; the budget must not grow")
	}
	if outcome.Displaced != 0 || outcome.DisplacedSubject != nil {
		t.Errorf("outcome reports a displacement that did not happen: %+v", outcome)
	}
}

// P5b -- WITH A SURPLUS, THE SLOT FIRES AND TAKES THE LOWEST-RANKED SURPLUS
// MEMBER. The saturated-crowd shape, at the unit the rule lives in: three
// members of one reserved kind in a two-slot budget leaves a surplus, so the
// anchor is seated by evicting the TAIL, never a higher-ranked member that a
// lower-ranked one could have paid for.
func TestTheAnchorSlotTakesTheLowestRankedSurplusMember(t *testing.T) {
	t.Parallel()
	ordered := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_top"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_tail"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_out"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_anchor"}},
	}
	tiers := []int{2, 2, 2, 2}
	reserved := []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectTeam}

	kept, outcome := reservedPrefix(ordered, tiers, 2, reserved,
		anchorReservedSlot{Kind: contextfabric.SubjectTeam, Source: anchorPoolKindScopeConfirmedAnchor})

	if !kept[3] {
		t.Fatalf("kept=%v: the anchor was not seated even though the member kind had a surplus", kept)
	}
	if !kept[0] {
		t.Errorf("kept=%v: the TOP-ranked member was displaced; the cost must be paid by the tail", kept)
	}
	if kept[1] {
		t.Errorf("kept=%v: the tail member was not the victim", kept)
	}
	n := 0
	for _, k := range kept {
		if k {
			n++
		}
	}
	if n != 2 {
		t.Errorf("kept %d, want exactly max=2 -- the reserve displaces, it never grows the budget", n)
	}
	if outcome.Displaced != 1 {
		t.Errorf("outcome.Displaced = %d, want 1", outcome.Displaced)
	}
	if outcome.DisplacedSubject == nil || outcome.DisplacedSubject.CanonicalID != "project_tail" {
		t.Errorf("outcome.DisplacedSubject = %+v, want project_tail -- a displacement nothing names is a silent one", outcome.DisplacedSubject)
	}
	if outcome.Reserved != string(contextfabric.SubjectTeam) || outcome.Source != anchorPoolKindScopeConfirmedAnchor {
		t.Errorf("outcome reserved/source = %q/%q, want team/confirmed_anchor", outcome.Reserved, outcome.Source)
	}
	if outcome.PoolTruncatedN != 2 {
		t.Errorf("outcome.PoolTruncatedN = %d, want 2 (4 candidates, budget 2)", outcome.PoolTruncatedN)
	}
}

// P8 -- THE PROTECTED TIERS SURVIVE THE WIDENED RULE TOO. The phase list
// promises a committed subject can never be dropped by truncation, and tier 1
// exists so a document's answer-bearing parent is not crowded out. The
// original victim rule honoured both; widening eligibility must not quietly
// stop honouring them.
func TestTheAnchorSlotNeverDisplacesCommittedOrParentTiers(t *testing.T) {
	t.Parallel()
	ordered := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "committed_member"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "parent_member"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "surplus_member"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_anchor"}},
	}
	// Three projects in budget, so the surplus test alone would admit an
	// eviction; only the tier guard keeps tiers 0 and 1 out of reach.
	tiers := []int{0, 1, 2, 2}
	reserved := []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectTeam}

	kept, outcome := reservedPrefix(ordered, tiers, 3, reserved,
		anchorReservedSlot{Kind: contextfabric.SubjectTeam, Source: anchorPoolKindScopeReceipt})

	if !kept[0] {
		t.Error("the COMMITTED subject (tier 0) was displaced by the anchor slot")
	}
	if !kept[1] {
		t.Error("the canonical PARENT (tier 1) was displaced by the anchor slot")
	}
	if !kept[3] {
		t.Errorf("kept=%v: the anchor was not seated from the only eligible tier-2 surplus member", kept)
	}
	if outcome.DisplacedSubject == nil || outcome.DisplacedSubject.CanonicalID != "surplus_member" {
		t.Errorf("outcome.DisplacedSubject = %+v, want surplus_member", outcome.DisplacedSubject)
	}
}

// P8b -- THE ANCHOR NEVER EVICTS ITS OWN KIND, and the whole mechanism is
// inert for a kind the frame did not reserve. A caller must not be able to buy
// the widened eligibility by asserting a kind: frameReservedKinds already
// excludes request.ExpectedKinds, and this is the second gate on the same
// property, at the cut itself.
func TestTheAnchorSlotIsInertForAKindTheFrameDidNotReserve(t *testing.T) {
	t.Parallel()
	ordered := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_1"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_2"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_3"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_1"}},
	}
	tiers := []int{2, 2, 2, 2}

	// team is NOT in reservedKinds -- a caller-asserted kind, not a frame
	// or receipt declaration.
	kept, outcome := reservedPrefix(ordered, tiers, 2,
		[]contextfabric.SubjectKind{contextfabric.SubjectProject},
		anchorReservedSlot{Kind: contextfabric.SubjectTeam, Source: anchorPoolKindScopeReceipt})

	if kept[3] {
		t.Error("a kind the FRAME did not reserve bought itself a guaranteed slot at the cut")
	}
	if outcome.Displaced != 0 {
		t.Errorf("outcome.Displaced = %d, want 0", outcome.Displaced)
	}
}

// P3b -- THE EMPTY SLOT IS BYTE-IDENTICAL TO THE PRE-TICKET PREFIX. Every
// caller except resolve.go's own first pass passes an empty slot, which is
// what makes this change need no behavioural review at those sites. Asserted
// against the plain prefix over a saturated reserved crowd -- the exact shape
// where the widened rule WOULD have fired had a slot been decided.
func TestAnEmptyAnchorSlotLeavesTheCutUnchanged(t *testing.T) {
	t.Parallel()
	ordered := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_1"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_2"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_3"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_1"}},
	}
	tiers := []int{2, 2, 2, 2}
	reserved := []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectTeam}

	withSlot, _ := reservedPrefix(ordered, tiers, 2, reserved,
		anchorReservedSlot{Kind: contextfabric.SubjectTeam, Source: anchorPoolKindScopeReceipt})
	empty, outcome := reservedPrefix(ordered, tiers, 2, reserved, anchorReservedSlot{})

	if empty[3] {
		t.Error("an EMPTY anchor slot admitted the anchor; the pre-ticket callers must see the old cut")
	}
	// Non-vacuity: the same inputs WITH a slot must differ, or this control
	// proves only that both calls did nothing.
	if !withSlot[3] {
		t.Fatal("the slot did not fire on the discriminating input, so the comparison above is vacuous")
	}
	if outcome.Reserved != anchorSlotNone || outcome.Source != anchorSlotNone {
		t.Errorf("empty slot outcome reserved/source = %q/%q, want the explicit none tokens", outcome.Reserved, outcome.Source)
	}
	if outcome.Displaced != 0 {
		t.Errorf("outcome.Displaced = %d, want an explicit 0", outcome.Displaced)
	}
	if outcome.PoolTruncatedN != 2 {
		t.Errorf("outcome.PoolTruncatedN = %d, want 2 even with no slot -- the count describes the CUT, not the slot", outcome.PoolTruncatedN)
	}
}
