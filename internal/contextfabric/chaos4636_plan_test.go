package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4636 (S5): the answer plan, the grouped cohort, and the three-stage
// budget.
//
// RED ON origin/main (f9d9688c): every symbol under test here -- PlanAnswer,
// BuildCohortGroups, NarrowGroupedCohort, ApplyGroupedCohortCompleteness,
// planAuthorizesRenderKind, applyCarriedPlan, ContextFabricCohortGroup --
// does not exist on the parent, so the package does not compile there. That
// is the same red-on-parent evidence S4 recorded for GateOffersByFamily.
// TestGroupAwareProjectionIsNotALeadingPrefix below is the one exception: it
// is a MUTATION red, and its own comment names the exact one-token mutation
// that turns it back to the pre-slice defect.

func planFixtureFacts(memberID, teamID, teamName string) CanonicalFact {
	str := func(v string) FactValue { s := v; return FactValue{String: &s} }
	return CanonicalFact{
		Kind:    FactMetrics,
		Subject: SubjectRef{Kind: SubjectProject, CanonicalID: memberID, Label: memberID},
		Fields: map[string]FactValue{
			"team_breakdown": {Rows: []FactValueRow{{Fields: map[string]FactValue{
				"team_id": str(teamID), "team_name": str(teamName),
			}}}},
		},
		SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
}

func planFixtureCohort(memberIDs ...string) *Cohort {
	members := make([]CohortMember, 0, len(memberIDs))
	for index, id := range memberIDs {
		members = append(members, CohortMember{
			Subject:          SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id},
			Rank:             index + 1,
			InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."},
		})
	}
	return &Cohort{Kind: SubjectProject, Members: members, Rationale: "fixture", Complete: true}
}

func TestPlanAnswerReadsTheGroupedFamilyRow(t *testing.T) {
	t.Parallel()
	plan := PlanAnswer(PlanAnswerInput{
		Family: QuestionFamilyOutcome{
			Family:        QuestionFamilyGroupedCohortStatus,
			Source:        QuestionFamilySourceModel,
			WinningSample: FamilySample{GroupKind: SubjectTeam},
		},
		Budget:           ResponseBudget{MaxItems: 30, MaxSerializedBytes: 1 << 20},
		MaxCohortMembers: 50,
	})
	if plan.Family != QuestionFamilyGroupedCohortStatus {
		t.Fatalf("Family = %q", plan.Family)
	}
	if plan.GroupKind != SubjectTeam {
		t.Fatalf("GroupKind = %q, want team -- the grouped family is the only one that reads the model's group signal", plan.GroupKind)
	}
	// RequireRanking is FALSE for a grouped status list. One ranking across
	// every team answers a question nobody asked.
	if plan.RequireRanking {
		t.Fatal("RequireRanking = true for grouped_cohort_status")
	}
	if !plan.RequireDrivers {
		t.Fatal("RequireDrivers = false, but the question asks for the main drivers")
	}
	// The plan is a WIRE document, so the family table's package-local axes
	// (group_kind, scope_anchor) must not appear on it.
	for _, axis := range plan.Axes {
		if !contractsv1.ValidContextFabricStructureNeedKind(axis) {
			t.Fatalf("plan carries axis %q, which is not a wire vocabulary member", axis)
		}
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("plan.Validate() = %v", err)
	}
}

// TestPlanAnswerNeverReadsAGroupKindOffANonGroupedFamily is the negative case
// CHAOS-4632's own gate measured for: a model that spuriously emits a group
// kind on a plain status question must not get its answer partitioned.
func TestPlanAnswerNeverReadsAGroupKindOffANonGroupedFamily(t *testing.T) {
	t.Parallel()
	for _, family := range []QuestionFamily{
		QuestionFamilySubjectInvestigation,
		QuestionFamilyDiscoveredCohortRanking,
		QuestionFamilyScopedCohortStatus,
		QuestionFamilyUnclassified,
	} {
		plan := PlanAnswer(PlanAnswerInput{
			Family: QuestionFamilyOutcome{
				Family: family, Source: QuestionFamilySourceModel,
				WinningSample: FamilySample{GroupKind: SubjectTeam},
			},
			Budget: ResponseBudget{MaxItems: 30},
		})
		if plan.GroupKind != "" {
			t.Fatalf("family %q read a spurious group kind %q", family, plan.GroupKind)
		}
	}
}

func TestPlanAnswerClampsMembersBelowTheItemBudget(t *testing.T) {
	t.Parallel()
	plan := PlanAnswer(PlanAnswerInput{
		Family:           QuestionFamilyOutcome{Family: QuestionFamilyGroupedCohortStatus, Source: QuestionFamilySourceModel},
		Budget:           ResponseBudget{MaxItems: 30},
		MaxCohortMembers: 50,
	})
	// Every member costs one item before a single claimed fact or driver is
	// charged, and the plan holds back declared headroom for what synthesis
	// will add. So the clamp is strictly below the raw item budget.
	if plan.Budget.MaxMembers >= 30 {
		t.Fatalf("MaxMembers = %d, want strictly below the 30-item budget", plan.Budget.MaxMembers)
	}
	// The clamp is the TIGHTER of two reservations, not the headroom one
	// alone. The flat headroom (MaxItems - SynthesisHeadroom) models synthesis
	// as a fixed cost; the per-member prediction models it as a rate, and for
	// any profile with a measured rate the rate binds first. Asserting only
	// the headroom arm let a 10-member clamp stand against a measured 3.90
	// items/member -- a predicted 39 items against this same 30-item budget.
	// See cohort_item_prediction_test.go for the rig measurement that pins the
	// rate, and testdata/grouped_cohort_item_ratio.json for its authority.
	headroomArm := 30 - plan.Budget.SynthesisHeadroom
	predictedArm := predictedMemberAllowance(PlanBudgetGroupedCohort, 30)
	want := headroomArm
	if predictedArm > 0 && predictedArm < want {
		want = predictedArm
	}
	if plan.Budget.MaxMembers != want {
		t.Fatalf("MaxMembers = %d, want min(MaxItems(30)-headroom(%d)=%d, predicted-allowance=%d) = %d",
			plan.Budget.MaxMembers, plan.Budget.SynthesisHeadroom, headroomArm, predictedArm, want)
	}
	// Both arms must actually be exercised by this fixture, or the min above
	// is decided by an absent value and the assertion proves only that one
	// path works. This is the profile that HAS a measurement, so the
	// predicted arm must be the binding one here.
	if predictedArm <= 0 {
		t.Fatal("predicted allowance is zero for the grouped profile: the per-member arm is not being exercised")
	}
	if predictedArm >= headroomArm {
		t.Fatalf("predicted allowance %d does not bind against headroom arm %d; this fixture no longer tests the tighter-of-two rule",
			predictedArm, headroomArm)
	}
	if plan.Budget.NarrowingBasis != contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical {
		t.Fatalf("NarrowingBasis = %q -- stage 1 must DECLARE its arbitrary-but-stable order even when it does not act", plan.Budget.NarrowingBasis)
	}
}

// TestPlanNeverExceedsTheCallersOwnCohortCap: a caller asking for fewer
// members always gets fewer. The plan narrows, it never widens.
func TestPlanNeverExceedsTheCallersOwnCohortCap(t *testing.T) {
	t.Parallel()
	plan := PlanAnswer(PlanAnswerInput{
		Family:           QuestionFamilyOutcome{Family: QuestionFamilyDiscoveredCohortRanking, Source: QuestionFamilySourceModel},
		Budget:           ResponseBudget{MaxItems: 50},
		MaxCohortMembers: 3,
	})
	if plan.Budget.MaxMembers != 3 {
		t.Fatalf("MaxMembers = %d, want the caller's own cap of 3", plan.Budget.MaxMembers)
	}
}

func TestBuildCohortGroupsReadsTheOwningTeamOffMemberFacts(t *testing.T) {
	t.Parallel()
	plan := AnswerPlan{GroupKind: SubjectTeam}
	cohort := planFixtureCohort("project_a", "project_b", "project_c")
	facts := []CanonicalFact{
		planFixtureFacts("project_a", "team_1", "Platform"),
		planFixtureFacts("project_b", "team_2", "Growth"),
		planFixtureFacts("project_c", "team_1", "Platform"),
	}
	groups, ungrouped := BuildCohortGroups(plan, cohort, facts)
	if ungrouped != 0 {
		t.Fatalf("ungrouped = %d, want 0", ungrouped)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	// Groups are ordered by canonical id, and members keep the cohort's own
	// order inside a group. Both are deterministic on purpose: a group order
	// that varied between identical requests makes every before/after
	// comparison meaningless.
	if groups[0].Subject.CanonicalID != "team_1" || groups[1].Subject.CanonicalID != "team_2" {
		t.Fatalf("group order = %q, %q", groups[0].Subject.CanonicalID, groups[1].Subject.CanonicalID)
	}
	if got := groups[0].MemberCanonicalIDs; len(got) != 2 || got[0] != "project_a" || got[1] != "project_c" {
		t.Fatalf("team_1 members = %#v", got)
	}
	if groups[0].Subject.Label != "Platform" {
		t.Fatalf("group label = %q, want the team_name column's value", groups[0].Subject.Label)
	}
	if err := contractsv1.ValidateCohortGroups(groups, cohort.Members); err != nil {
		t.Fatalf("ValidateCohortGroups() = %v", err)
	}
}

// TestBuildCohortGroupsLeavesAnUnplaceableMemberUngrouped: on real data the
// providers carrying the team association join on compounding risk, so a
// member whose facts came back empty genuinely has no derivable group.
// Inventing one, or dropping the member, would both be worse than saying so.
func TestBuildCohortGroupsLeavesAnUnplaceableMemberUngrouped(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("project_a", "project_orphan")
	groups, ungrouped := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, cohort,
		[]CanonicalFact{planFixtureFacts("project_a", "team_1", "Platform")})
	if ungrouped != 1 {
		t.Fatalf("ungrouped = %d, want 1", ungrouped)
	}
	if len(groups) != 1 || len(groups[0].MemberCanonicalIDs) != 1 {
		t.Fatalf("groups = %#v", groups)
	}
	// The ungrouped member is still in the flattened list -- it was not
	// dropped, only unplaced.
	if len(cohort.Members) != 2 {
		t.Fatalf("cohort lost a member it could not group: %d remain", len(cohort.Members))
	}
}

// TestHealthScopeRowsOnlyGroupOnTeamScope: health's breakdown is per-scope,
// and a scope="project" row's ids belong to something else entirely. Reading
// one as a team is a WRONG grouping, not a missing one.
func TestHealthScopeRowsOnlyGroupOnTeamScope(t *testing.T) {
	t.Parallel()
	str := func(v string) FactValue { s := v; return FactValue{String: &s} }
	fact := CanonicalFact{
		Kind:    FactHealth,
		Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "a"},
		Fields: map[string]FactValue{"risk_breakdown": {Rows: []FactValueRow{
			{Fields: map[string]FactValue{"scope": str("project"), "scope_id": str("project_a"), "scope_name": str("A")}},
			{Fields: map[string]FactValue{"scope": str("team"), "scope_id": str("team_9"), "scope_name": str("Nine")}},
		}}},
		SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
	groups, _ := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, planFixtureCohort("project_a"), []CanonicalFact{fact})
	if len(groups) != 1 || groups[0].Subject.CanonicalID != "team_9" {
		t.Fatalf("groups = %#v, want the scope=\"team\" row's id", groups)
	}
}

func TestCohortCompletenessIsTheConjunctionOverGroups(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("a", "b")
	cohort.Groups = []contractsv1.ContextFabricCohortGroup{
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "t1", Label: "t1"}, MemberCanonicalIDs: []string{"a"}, Complete: true, Total: 1},
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "t2", Label: "t2"}, MemberCanonicalIDs: []string{"b"}, Truncated: true, Total: 4},
	}
	cohort.Complete = true
	ApplyGroupedCohortCompleteness(cohort)
	// The whole point: a reader that ignores Groups must get a CONSERVATIVE
	// answer, never Complete: true over a partially-truncated union.
	if cohort.Complete {
		t.Fatal("Complete stayed true while one group is truncated")
	}
	if !cohort.Truncated {
		t.Fatal("Truncated is false while one group is truncated")
	}
	if err := cohort.Validate(); err != nil {
		t.Fatalf("cohort.Validate() = %v", err)
	}
}

// TestNarrowGroupedCohortIsMemberFirst is decision D2, ruled 2026-08-30:
// "for each team" is the question's own words, so dropping a team answers a
// question that was not asked.
func TestNarrowGroupedCohortIsMemberFirst(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("a1", "a2", "a3", "a4", "b1", "c1")
	cohort.Groups = []contractsv1.ContextFabricCohortGroup{
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "ta", Label: "ta"}, MemberCanonicalIDs: []string{"a1", "a2", "a3", "a4"}, Complete: true, Total: 4},
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "tb", Label: "tb"}, MemberCanonicalIDs: []string{"b1"}, Complete: true, Total: 1},
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "tc", Label: "tc"}, MemberCanonicalIDs: []string{"c1"}, Complete: true, Total: 1},
	}
	kept, groups, narrowed, _ := NarrowGroupedCohort(cohort, 3)
	if !narrowed {
		t.Fatal("narrowed = false")
	}
	if len(kept) != 3 {
		t.Fatalf("kept %d members, want 3", len(kept))
	}
	// EVERY group survives. The big one is thinned; the singletons are not
	// touched, because losing a singleton is losing a whole team.
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want every group to survive", len(groups))
	}
	for _, group := range groups {
		if len(group.MemberCanonicalIDs) == 0 {
			t.Fatalf("group %q lost every member", group.Subject.CanonicalID)
		}
	}
	if !groups[0].Truncated || groups[0].Total != 4 {
		t.Fatalf("the thinned group must disclose truncation against its ORIGINAL total: %#v", groups[0])
	}
	if groups[1].Truncated || groups[2].Truncated {
		t.Fatal("an untouched singleton group was marked truncated")
	}
	// Rank is a dense 1..N sequence the cohort validator enforces, so
	// removing members from the middle must renumber.
	for index, member := range kept {
		if member.Rank != index+1 {
			t.Fatalf("kept[%d].Rank = %d, want a dense 1..N sequence", index, member.Rank)
		}
	}
}

// TestNarrowGroupedCohortRefusesToDropAGroup: once every group is down to one
// member, narrowing stops. The planned refusal is the correct terminal case,
// not a silent group drop.
func TestNarrowGroupedCohortRefusesToDropAGroup(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("a1", "b1", "c1")
	cohort.Groups = []contractsv1.ContextFabricCohortGroup{
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "ta", Label: "ta"}, MemberCanonicalIDs: []string{"a1"}, Complete: true, Total: 1},
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "tb", Label: "tb"}, MemberCanonicalIDs: []string{"b1"}, Complete: true, Total: 1},
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "tc", Label: "tc"}, MemberCanonicalIDs: []string{"c1"}, Complete: true, Total: 1},
	}
	if _, _, narrowed, _ := NarrowGroupedCohort(cohort, 1); narrowed {
		t.Fatal("narrowing dropped a whole group to reach the target, which decision D2 forbids")
	}
}

// TestRetainFactsForCohortDropsRemovedMembersFacts is a CORRECTNESS
// requirement, not a size optimization: a claim minted about a subject the
// answer no longer contains is ungrounded, and the closure validator would
// reject the whole result -- turning a narrowed answer into a failed one.
func TestRetainFactsForCohortDropsRemovedMembersFacts(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{
		planFixtureFacts("project_a", "team_1", "Platform"),
		planFixtureFacts("project_b", "team_2", "Growth"),
		{Kind: FactHealth, Subject: SubjectRef{Kind: SubjectOrganization, CanonicalID: "org_1", Label: "org"},
			Fields: map[string]FactValue{}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1"},
	}
	removed := []CohortMember{{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "b"}}}
	retained := RetainFactsForCohort(facts, planFixtureCohort("project_a"), removed)
	if len(retained) != 2 {
		t.Fatalf("retained %d facts, want 2", len(retained))
	}
	// A fact no cohort member owns is untouched: a cohort answer also
	// carries organization- and subject-level facts, and dropping those
	// would remove evidence narrowing never asked about.
	if retained[1].Subject.Kind != SubjectOrganization {
		t.Fatalf("the organization-level fact was dropped: %#v", retained)
	}
}

// TestGroupedPlanRefusesTheCrossGroupBarChart: North Star check 10 made
// structural. A grouped status list plans RenderKinds=[table], so a cohort
// attention bar -- one ranking drawn across every group -- is not merely
// wrong, it was not requested.
func TestGroupedPlanRefusesTheCrossGroupBarChart(t *testing.T) {
	t.Parallel()
	if planAuthorizesRenderKind(&AnswerPlan{RenderKinds: []contractsv1.ContextFabricRenderKind{contractsv1.ContextFabricRenderKindTable}}, contractsv1.ContextFabricRenderKindSeries) {
		t.Fatal("a table-only plan authorized a series")
	}
	// A NIL plan authorizes everything, which is what keeps this slice
	// non-regressive for every result written before the planning stage.
	if !planAuthorizesRenderKind(nil, contractsv1.ContextFabricRenderKindSeries) {
		t.Fatal("a nil plan refused a series; results predating the plan must render exactly as they do today")
	}
	// So does an EMPTY list: a family that has not declared its render kinds
	// has not declared a restriction, and inferring one from silence is how
	// a chart quietly disappears.
	if !planAuthorizesRenderKind(&AnswerPlan{}, contractsv1.ContextFabricRenderKindSeries) {
		t.Fatal("an empty RenderKinds list was read as a restriction")
	}
}

func TestApplyCarriedPlanNeverOverridesThisTurnsOwnReading(t *testing.T) {
	t.Parallel()
	carry := planCarryResult{Family: QuestionFamilyGroupedCohortStatus, GroupKind: SubjectTeam, Outcome: PlanCarryHit}
	own := QuestionFamilyOutcome{Family: QuestionFamilySubjectInvestigation, Source: QuestionFamilySourceModel}
	got, applied := applyCarriedPlan(own, carry)
	if applied || got.Family != QuestionFamilySubjectInvestigation {
		t.Fatal("a carried family overrode a family this turn resolved for itself; a caller who changes subject must be able to")
	}
	// It DOES apply when this turn classified nothing.
	got, applied = applyCarriedPlan(QuestionFamilyOutcome{Family: QuestionFamilyUnclassified}, carry)
	if !applied || got.Family != QuestionFamilyGroupedCohortStatus {
		t.Fatalf("carry did not apply to an unclassified turn: %+v", got)
	}
	if got.Source != QuestionFamilySourceCarried {
		t.Fatalf("Source = %q, want carried -- the warrant for the value differs from the model's own", got.Source)
	}
	if got.WinningSample.GroupKind != SubjectTeam {
		t.Fatal("a carried grouped family arrived with no group axis to group by")
	}
}

// TestCarriablePlanNeverCarriesUnclassified: unclassified is the
// refuse-to-guess member. Propagating it spreads a NON-classification through
// a conversation while looking like a decision.
func TestCarriablePlanNeverCarriesUnclassified(t *testing.T) {
	t.Parallel()
	if carriablePlan(InvestigationResult{AnswerPlan: &AnswerPlan{Family: QuestionFamilyUnclassified}}) != nil {
		t.Fatal("unclassified was treated as carriable")
	}
	if carriablePlan(InvestigationResult{}) != nil {
		t.Fatal("a result with no plan was treated as carriable")
	}
}

// TestPlanCarryFailsClosedOnConflictingReadings: the receipt fields validate
// independently, so one request can legitimately name two prior results.
// Picking whichever loaded first would silently answer under an arbitrary one
// of two real, disagreeing readings.
func TestPlanCarryFailsClosedOnConflictingReadings(t *testing.T) {
	t.Parallel()
	outcome, applied := applyCarriedPlan(
		QuestionFamilyOutcome{Family: QuestionFamilyUnclassified},
		planCarryResult{Outcome: PlanCarryMissConflictingPlans},
	)
	if applied || outcome.Family != QuestionFamilyUnclassified {
		t.Fatal("a conflicting carry was applied instead of failing closed")
	}
}
