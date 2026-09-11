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
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// saturatedCrowd is larger than resolveScoped's MaxSubjectCandidates budget
// (20) and is entirely of the CONFIRMED MEMBER KIND, which is itself
// reserved -- the shape that leaves the old victim rule with nothing to take.
const saturatedCrowd = 90

// resolveScopedWithCut is resolveScoped plus the CUT's own summary event.
//
// NON-VACUITY IS MEASURED AT THE CUT, NOT IN THE RETURNED OFFER, and that is
// not a stylistic choice. Counting member-kind candidates in res.Candidates to
// prove the crowd existed reads the OFFER, and a scope-anchored frame is
// exactly the shape where a later seam withholds member-kind candidates from
// the offer by design -- so such a guard reports "the fixture did not build
// the crowd" on a fixture that built ninety of them and had them cut and then
// withheld. The ranked-cut summary is where the crowd is still visible on
// every tree: candidate_count is the pre-cut population and survived_count is
// the budget.
func resolveScopedWithCut(t *testing.T, backend *fakeGraphBackend, frame *contextfabric.QuestionFrame,
	confirmedKind *contextfabric.ConfirmedExpectedKind, confirmedAnchor *contextfabric.ConfirmedAnchorSelection,
	receiptAnchorKind contextfabric.SubjectKind) (contextfabric.SubjectResolution, ResolutionTraceEvent) {
	t.Helper()
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("chaos"),
		deps, confirmedKind, confirmedAnchor, frame, receiptAnchorKind)
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	var summary ResolutionTraceEvent
	var found bool
	for _, e := range tracer.eventsForStage("ranked_cut") {
		if e.RankedCutSummary {
			summary, found = e, true
		}
	}
	if !found {
		t.Fatal("no ranked_cut SUMMARY event: this resolution never reached phase 4, so a truncation pin on it would be vacuous")
	}
	return res, summary
}

// requireSaturatedCut is the shared non-vacuity guard: the crowd really was
// built, really reached phase 4, and really exceeded the budget. Without all
// three the survival assertions below assert nothing.
func requireSaturatedCut(t *testing.T, cut ResolutionTraceEvent) {
	t.Helper()
	if cut.RankedCutCandidateCount <= cut.RankedCutMax {
		t.Fatalf("cut saw %d candidates against budget %d: nothing was truncated, so this pin proves nothing",
			cut.RankedCutCandidateCount, cut.RankedCutMax)
	}
	if cut.RankedCutCandidateCount < saturatedCrowd {
		t.Fatalf("cut saw %d candidates, want at least the %d-member crowd: the fixture did not build it",
			cut.RankedCutCandidateCount, saturatedCrowd)
	}
}

// logLineWithMsg returns the ONE JSON log line carrying this msg, and fails if
// there is not exactly one. Locating the line is the point: these four keys are
// emitted on two different lines, so an assertion made over the whole log
// passes on whichever line happens to be right.
func logLineWithMsg(t *testing.T, log, msg string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `"msg":"`+msg+`"`) {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("found %d log lines with msg %q, want exactly 1 -- the assertions below name a line and cannot be made against zero or several", len(found), msg)
	}
	return found[0]
}

// P1 -- SOURCE: RECEIPT, and the acceptance case.
//
// The receipt path is UNCONDITIONAL: a receipt-declared scope-anchor kind
// reaches kind-hinted retrieval with or without a confirmed member kind. That
// is what keeps this shape starved -- the members are in the pool, they are a
// reserved kind, and they fill the budget.
//
// NO CONFIRMED KIND, DELIBERATELY, and the reason is measured rather than
// stylistic. The contest set (chaos5422_contest_set.go) refuses member-kind
// candidates at the ADMISSION boundary, but only when a confirmed kind EQUALS
// the frame's member kind. Written with a confirmed member kind this fixture
// stops being starved at all -- the crowd never reaches phase 4 and the cut
// sees ONE candidate, so the pin would assert survival of an anchor nothing
// threatened. Written without one, the crowd is admitted and the cut sees
// ninety-two against a budget of twenty, on a tree with the contest set and
// on a tree without it alike.
func TestTheReceiptScopeAnchorSurvivesASaturatedMemberCrowd(t *testing.T) {
	t.Parallel()
	res, cut := resolveScopedWithCut(t, anchorOnlyByKindBackend("chaos", saturatedCrowd),
		scopedProjectsFrame("chaos"), nil, nil, contextfabric.SubjectTeam)
	requireSaturatedCut(t, cut)

	if kinds := candidateKinds(res); kinds[contextfabric.SubjectTeam] == 0 {
		t.Errorf("anchor candidates = 0, want >= 1; kinds=%v, cut saw %d against budget %d -- the decided scope anchor was truncated away by a crowd of reserved member kinds",
			kinds, cut.RankedCutCandidateCount, cut.RankedCutMax)
	}
}

// P2 -- SOURCE: CONFIRMED-ANCHOR FALLBACK, pinned WHERE IT IS DECIDABLE.
//
// The ticket asks for the guarantee on both anchor sources. The receipt source
// is pinned end to end above. The fallback source is pinned here, at the cut,
// and NOT end to end -- because the end-to-end starved shape does not exist
// once the contest set ships, and a fixture that pretended otherwise would be
// asserting a property on a population no request can produce.
//
// WHY IT CANNOT EXIST, measured on a tree carrying the contest set. The
// fallback only arises when a confirmed kind is present: decideAnchorPoolKindScope
// returns none without one. So either that confirmed kind EQUALS the frame's
// member kind, and the contest set refuses every member candidate at
// admission -- the cut then sees ONE candidate against a budget of twenty --
// or it does not, and the pre-existing confirmed-kind filter removes them
// instead, with the same reading. Either way no saturated crowd of a RESERVED
// kind survives to starve the anchor. (The only kind in both the surviving set
// and the reserved set is then the anchor's own, and an anchor cannot starve
// itself.)
//
// SO THE GUARANTEE IS PINNED ON THE DECISION, NOT ON A FABRICATED POOL: a
// fallback-sourced scope must reserve exactly the kind a receipt-sourced one
// does, and must hold exactly the same slot at the cut. The source-conditional
// mutant (an arm that gates the slot on the receipt source alone) is killed by
// this pin together with TestTheAnchorSlotTakesTheLowestRankedSurplusMember,
// which drives a confirmed_anchor-sourced slot through reservedPrefix itself.
func TestTheFallbackSourcedAnchorHoldsTheSameSlotAsTheReceiptSourced(t *testing.T) {
	t.Parallel()
	frame := scopedProjectsFrame("chaos")
	fallback := decideAnchorPoolKindScope(frame, "",
		&contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:chaos"},
		confirmedProject())
	receipt := decideAnchorPoolKindScope(frame, contextfabric.SubjectTeam, nil, confirmedProject())

	if fallback.Kind == "" {
		t.Fatal("the fallback decided no anchor kind, so everything below is vacuous")
	}
	if fallback.Kind != receipt.Kind {
		t.Fatalf("fallback reserved %q, receipt reserved %q -- the two sources must be indistinguishable downstream",
			fallback.Kind, receipt.Kind)
	}
	if fallback.Source == receipt.Source {
		t.Fatalf("both sources report %q; this pin cannot tell them apart, so it cannot prove the slot ignores the difference", fallback.Source)
	}

	// The same saturated shape reservedPrefix sees on the receipt path, driven
	// with the FALLBACK-sourced slot: three members of the reserved member kind
	// in a two-slot budget, the anchor outside it.
	ordered := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_top"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_tail"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_out"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_anchor"}},
	}
	tiers := []int{2, 2, 2, 2}
	reserved := frameReservedKinds(frame, fallback.Kind)

	keptFallback, outFallback := reservedPrefix(ordered, tiers, 2, reserved,
		anchorReservedSlot{Kind: fallback.Kind, Source: fallback.Source})
	keptReceipt, outReceipt := reservedPrefix(ordered, tiers, 2, reserved,
		anchorReservedSlot{Kind: receipt.Kind, Source: receipt.Source})

	if !keptFallback[3] {
		t.Errorf("kept=%v: the FALLBACK-sourced anchor was not seated from a saturated reserved crowd", keptFallback)
	}
	if !reflect.DeepEqual(keptFallback, keptReceipt) {
		t.Errorf("fallback kept %v but receipt kept %v -- the slot must not depend on which source named the kind",
			keptFallback, keptReceipt)
	}
	if outFallback.Displaced != outReceipt.Displaced || outFallback.PoolTruncatedN != outReceipt.PoolTruncatedN {
		t.Errorf("fallback outcome %+v differs from receipt outcome %+v beyond the source token", outFallback, outReceipt)
	}
	if outFallback.Source != anchorPoolKindScopeConfirmedAnchor {
		t.Errorf("outcome.Source = %q, want the fallback token -- an operator must be able to tell the two sources apart on the line", outFallback.Source)
	}
}

// P2b -- THE FALLBACK SOURCE REACHES THE CUT, AS THE FALLBACK SOURCE.
//
// r1 found the gap this closes, with an executed probe: the unit pin above
// proves reservedPrefix does not care which source named the kind, but it
// cannot see the WIRING. A mutant that handed the cut an EMPTY slot whenever
// the source was the confirmed anchor left reservedPrefix untouched, so the
// unit pin stayed green while the production path stopped reserving anything.
//
// SO THIS PIN ASSERTS THE WIRING, on every tree, from the ranked-cut summary
// the production entry point actually emitted: the slot the cut was handed
// reports the anchor's kind and the FALLBACK source token. That reading does
// not depend on whether the crowd was starved, so it survives a tree where the
// members are refused before the cut.
//
// The SURVIVAL half is asserted only where the cut is genuinely saturated,
// because a pin cannot assert that an anchor beat a crowd that never reached
// phase 4. Which arm ran is logged either way -- a test that quietly asserts
// nothing is the defect this comment exists to prevent.
func TestTheFallbackSourcedAnchorReachesTheCutAsTheFallbackSource(t *testing.T) {
	t.Parallel()
	res, cut := resolveScopedWithCut(t, anchorOnlyByKindBackend("chaos", saturatedCrowd),
		scopedProjectsFrame("chaos"), confirmedProject(),
		&contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:chaos"}, "")

	if cut.AnchorSlotReserved != string(contextfabric.SubjectTeam) {
		t.Errorf("cut was handed anchor_slot_reserved=%q, want %q -- the fallback-decided anchor kind did not reach the cut",
			cut.AnchorSlotReserved, contextfabric.SubjectTeam)
	}
	if cut.AnchorSlotSource != anchorPoolKindScopeConfirmedAnchor {
		t.Errorf("cut was handed anchor_slot_source=%q, want %q -- the wiring lost the fallback source, and an operator reading the line would be told a different consumer decided this",
			cut.AnchorSlotSource, anchorPoolKindScopeConfirmedAnchor)
	}

	if cut.RankedCutCandidateCount > cut.RankedCutMax {
		if kinds := candidateKinds(res); kinds[contextfabric.SubjectTeam] == 0 {
			t.Errorf("anchor candidates = 0, want >= 1; kinds=%v, cut saw %d against budget %d -- the FALLBACK-sourced anchor was truncated away end to end",
				kinds, cut.RankedCutCandidateCount, cut.RankedCutMax)
		}
		return
	}
	t.Logf("cut was not saturated (%d candidates against budget %d), so only the WIRING half of this pin ran; survival on a starved pool is asserted by the receipt pin, and this shape's own unreachability is explained on TestTheFallbackSourcedAnchorHoldsTheSameSlotAsTheReceiptSourced",
		cut.RankedCutCandidateCount, cut.RankedCutMax)
}

// P3 -- NEGATIVE CONTROL, THE THIRD SOURCE. With no receipt kind, no
// confirmed anchor and no confirmed member kind there is no DECIDED anchor,
// so there is no slot to reserve and nothing about this resolution may
// change. This is deliberately NOT the acceptance case: an anchor whose kind
// nothing declared is CHAOS-5445's contest to win, not this slot's.
func TestWithNoDecidedAnchorTheSlotIsInertOnASaturatedCrowd(t *testing.T) {
	t.Parallel()
	res, cut := resolveScopedWithCut(t, anchorOnlyByKindBackend("chaos", saturatedCrowd),
		scopedProjectsFrame("chaos"), nil, nil, "")
	requireSaturatedCut(t, cut)

	kinds := candidateKinds(res)
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
	res, cut := resolveScopedWithCut(t, anchorOnlyByKindBackend("chaos", saturatedCrowd),
		scopedProjectsFrame("chaos"), nil, nil, contextfabric.SubjectTeam)
	requireSaturatedCut(t, cut)

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
		nil, nil, scopedProjectsFrame("chaos"), contextfabric.SubjectTeam); err != nil {
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
	// THE VALUES, NOT JUST THE KEYS, AND PER LINE.
	//
	// Asserting presence alone let a mutant emit `none` for the reserved kind
	// and stay green (r1 P3): a key whose value is wrong is worse than an
	// absent key, because it reads as a measurement.
	//
	// AND THE ASSERTION MUST NAME ITS LINE. The first version of this fix
	// searched the whole log, and it did NOT kill that mutant either: the
	// same four keys are emitted on TWO lines -- the ranked-cut summary and
	// the displacement line -- so a substring search over the log passes on
	// the UNGUARDED one while the guarded one is wrong. That is the standing
	// two-line trap, and it is why each line is now located by its own `msg`
	// and asserted on its own.
	summaryLine := logLineWithMsg(t, log, "context fabric resolution trace: ranked cut summary")
	for _, want := range []string{
		`"anchor_slot_reserved":"` + string(contextfabric.SubjectTeam) + `"`,
		`"anchor_slot_source":"` + anchorPoolKindScopeReceipt + `"`,
		`"anchor_slot_displaced":1`,
		`"pool_truncated_n":72`,
	} {
		if !strings.Contains(summaryLine, want) {
			t.Errorf("ranked-cut SUMMARY line missing %s; a wrong value here reads as a measurement. line: %s", want, summaryLine)
		}
	}
	displacedLine := logLineWithMsg(t, log, "context fabric resolution trace: anchor slot displaced")
	for _, want := range []string{
		`"anchor_slot_reserved":"` + string(contextfabric.SubjectTeam) + `"`,
		`"anchor_slot_source":"` + anchorPoolKindScopeReceipt + `"`,
		`"subject_kind":"` + string(contextfabric.SubjectProject) + `"`,
	} {
		if !strings.Contains(displacedLine, want) {
			t.Errorf("anchor_slot_displaced line missing %s; the victim line must name what was displaced and under whose slot. line: %s", want, displacedLine)
		}
	}
	// THE ADMITTED ANCHOR IS NAMED, and it must agree with the displacement
	// the summary claims. r1 asked what regression would be invisible at
	// Info and answered "one that reports a displacement while dropping the
	// anchor". It would NOT be invisible -- the pre-existing
	// reserved_kind_admitted line names the admitted subject by canonical id
	// -- but nothing pinned the two lines agreeing, so a build could have
	// started reporting one without the other. This is that pin.
	if !strings.Contains(log, `"stage":"reserved_kind_admitted"`) ||
		!strings.Contains(log, `"subject_canonical_id":"team.v2:github:chaos"`) {
		t.Errorf("no reserved_kind_admitted line naming the admitted anchor; a displacement claim with no admission beside it is the regression this pair exists to make visible; log: %s", log)
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

// P8c -- THE TIER GUARD IS LOAD-BEARING, and the earlier tier pin did not
// prove it. In that fixture the lowest-ranked in-budget candidate was already
// tier 2, so the tail-first walk reached an eligible victim before it ever
// had to refuse a protected one -- a mutant deleting the tier check survived
// it. The discriminating shape is a budget filled ENTIRELY by protected
// tiers, with a surplus of that kind: only the tier guard stops the anchor
// from evicting a committed subject or a canonical parent, and the correct
// outcome is that the anchor is NOT seated, because phase 4's budget is a
// maximum and the protected tiers outrank the slot.
func TestTheAnchorSlotRefusesAProtectedOnlyBudgetEvenWithASurplus(t *testing.T) {
	t.Parallel()
	ordered := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "committed_member"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "parent_member"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_anchor"}},
	}
	// Two projects in budget, so present[project] == 2 > kindReserveSlotsPerKind:
	// the SURPLUS test passes and only the TIER test can refuse.
	tiers := []int{0, 1, 2}
	reserved := []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectTeam}

	kept, outcome := reservedPrefix(ordered, tiers, 2, reserved,
		anchorReservedSlot{Kind: contextfabric.SubjectTeam, Source: anchorPoolKindScopeReceipt})

	if !kept[0] {
		t.Error("the COMMITTED subject (tier 0) was evicted for the anchor slot; the phase list promises truncation can never drop it")
	}
	if !kept[1] {
		t.Error("the canonical PARENT (tier 1) was evicted for the anchor slot; that tier exists so an answer-bearing parent is not crowded out")
	}
	if kept[2] {
		t.Error("the anchor was seated out of a budget holding only protected tiers; the reserve must admit nothing rather than exceed the budget or evict a protected candidate")
	}
	if outcome.Displaced != 0 || outcome.DisplacedSubject != nil {
		t.Errorf("outcome reports a displacement that must not have happened: %+v", outcome)
	}
}

// P9 -- THE SLOT CHANGES WHAT IS OFFERED, NEVER WHAT WAS DECIDED.
//
// This is the property the rig proof would otherwise have had to establish,
// pinned instead at the seam it actually lives on (team-lead ruling,
// 2026-09-09): phase 3 takes the commit decision over the FULL untruncated
// candidate set BEFORE phase 4 runs (resolution.go's own phase list --
// "commit decision, over the FULL untruncated candidate set" then "truncation
// LAST"), so a slot admitted at the cut can only change which already-decided
// candidates come BACK. Asserted on the saturated crowd, over the whole
// phase-3 output: the committed set, the commit BASES and the decision
// DIGESTS must be identical with and without the slot.
//
// The prompt is EXCLUDED from the equality on purpose and asserted to change
// instead. It is built from the RETAINED set by design, and naming the anchor
// in it is the entire user-visible point of the reserve -- the same
// distinction TestReservedKinds_DoNotChangeCommitDecisions draws, and its
// author's first and wrong property.
func TestTheAnchorSlotChangesTheOfferedListAndNotTheDecision(t *testing.T) {
	t.Parallel()
	build := func() map[string]contextfabric.SubjectCandidate {
		pool := make(map[string]contextfabric.SubjectCandidate)
		// A SATURATED CROWD OF THE RESERVED MEMBER KIND, which is the shape
		// that leaves the ordinary victim rule with nothing to take. A crowd
		// of some non-reserved kind would be displaced by the OLD rule and
		// this pin would never reach the code it exists for.
		for i := 0; i < 6; i++ {
			c := contextfabric.SubjectCandidate{
				Subject:    contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: fmt.Sprintf("project_%d", i), Label: fmt.Sprintf("chaos project %d", i)},
				State:      contractsv1.ContextFabricResolutionAmbiguous,
				Confidence: 0.9,
			}
			pool[SubjectKey(c.Subject)] = c
		}
		team := contextfabric.SubjectCandidate{
			Subject:    contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_1", Label: "CHAOS Team"},
			State:      contractsv1.ContextFabricResolutionAmbiguous,
			Confidence: 0.4,
		}
		pool[SubjectKey(team.Subject)] = team
		return pool
	}
	reserved := []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectTeam}

	call := func(slot anchorReservedSlot) (contextfabric.SubjectResolution, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet) {
		return resolveFromMergedCandidatesWithAnchorSlot(
			build(), map[string]string{}, map[string]bool{}, 3, true, false,
			nil, 0, false, 10, 20, true,
			DefaultCommitGatePolicy(), nil, nil, false, nil, "", "", false, false, reserved, slot, nil, 1)
	}

	without, withoutBases, withoutDigests := call(anchorReservedSlot{})
	with, withBases, withDigests := call(anchorReservedSlot{Kind: contextfabric.SubjectTeam, Source: anchorPoolKindScopeReceipt})

	anchorIn := func(res contextfabric.SubjectResolution) bool {
		for _, c := range res.Candidates {
			if c.Subject.Kind == contextfabric.SubjectTeam {
				return true
			}
		}
		return false
	}
	// NON-VACUITY, both directions. Without these the equality below is
	// "nothing happened equals nothing happened".
	if anchorIn(without) {
		t.Fatal("setup is vacuous: the anchor survived truncation WITHOUT the slot, so this is not the starved shape")
	}
	if !anchorIn(with) {
		t.Fatal("setup is vacuous: the slot admitted nothing, so 'the decision is unchanged' proves nothing")
	}

	// THE PHASE-3 OUTPUT, whole.
	if len(without.Committed) != len(with.Committed) {
		t.Fatalf("committed COUNT changed: without=%v with=%v -- the slot moved a commit decision", without.Committed, with.Committed)
	}
	for i := range without.Committed {
		if without.Committed[i] != with.Committed[i] {
			t.Errorf("committed[%d] changed: %v -> %v", i, without.Committed[i], with.Committed[i])
		}
	}
	if !reflect.DeepEqual(withoutBases, withBases) {
		t.Errorf("commit BASES changed: %v -> %v -- the standing of a commit is part of the decision, not of the offer", withoutBases, withBases)
	}
	if !reflect.DeepEqual(withoutDigests, withDigests) {
		t.Errorf("commit decision DIGESTS changed: %v -> %v", withoutDigests, withDigests)
	}
	if without.RetrievalDegraded != with.RetrievalDegraded {
		t.Errorf("RetrievalDegraded changed: %v -> %v", without.RetrievalDegraded, with.RetrievalDegraded)
	}

	// WHAT IS ALLOWED TO CHANGE, asserted positively so a build that stopped
	// offering the anchor cannot pass this test by changing nothing at all.
	if len(without.Candidates) != len(with.Candidates) {
		t.Errorf("candidate COUNT changed: %d -> %d; the slot must displace, never grow the budget",
			len(without.Candidates), len(with.Candidates))
	}
	if with.ClarificationPrompt == without.ClarificationPrompt {
		t.Errorf("clarification prompt unchanged (%q); offering the anchor to the caller is the point of the slot", with.ClarificationPrompt)
	}
	if !strings.Contains(with.ClarificationPrompt, "CHAOS Team") {
		t.Errorf("clarification prompt = %q, want it to name the anchor the slot admitted", with.ClarificationPrompt)
	}
}
