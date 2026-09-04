package contextfabric

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// PR2B, quota side. This file holds the tests that name ONLY symbols which
// already exist, so it COMPILES at the fix parent and fails there on its own
// assertions. Everything naming the new allocator API lives in
// pr2b_allocator_symbols_test.go. A red-first proof that fails as a build
// error proves nothing, and keeping the split is not tidiness -- merging these
// two files destroys the proof.

// manyRankedMembersCohort returns a ranked cohort large enough that narration
// will want to narrate the maximum it believes it can afford.
//
// The point is the BUDGET it is offered, not the cohort: with 16 members
// available and the static contract caps in force, cohortDriverNarrationBudget
// authorises 16 x 3 = 48 driver judgments and one claim each, because 50 and
// 250 are the only ceilings it consults.
func manyRankedMembersCohort(t *testing.T, count int) (*Cohort, cohortMemberSignalCitations) {
	t.Helper()
	facts := make([]CanonicalFact, 0, count*2)
	members := make([]CohortMember, 0, count)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("T%02d", i)
		facts = append(facts, healthFact(name, "high"), investmentFact(name, balancedThemes(), 0))
		member := rankTestMember(name)
		member.EvidenceRefIDs = []string{"evidence_team_a_roster"}
		members = append(members, member)
	}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: members}
	ranked, _, citations := RankCohort(cohort, facts, availableCoverage())
	return ranked, citations
}

// TestNarrationCannotSpendPastThePlanItemBudget is CHAOS-5008's pinning test
// and the reason PR2B exists at all.
//
// cohort_driver_narration.go calls cohortDriverNarrationBudget with
// ContextFabricDriversMaxCount (50) and ContextFabricClaimedFactsMaxCount
// (250) -- the STATIC contract caps. It never sees plan.Budget.MaxItems. So at
// the configured ceiling the narration composer is a SECOND, independent
// spender on a budget it cannot see: with a 30-item ceiling and a synthesis
// draft that has barely spent anything, it will still authorise 16 members x 3
// drivers = 48 drivers plus 48 minted claims -- 96 items on its own, against a
// ceiling of 30.
//
// RED at the fix parent: the narrated output alone exceeds the plan's whole
// item budget. GREEN once the ONE allocator derives narration's allowance from
// plan.Budget.MaxItems like every other spender.
func TestNarrationCannotSpendPastThePlanItemBudget(t *testing.T) {
	t.Parallel()
	const maxItems = 30
	ranked, citations := manyRankedMembersCohort(t, 16)

	// A synthesis draft that has spent almost nothing, so nothing but the
	// narration budget itself can be blamed for the overrun.
	const synthesisDrivers = 2
	const synthesisClaims = 2

	// The plan whose ceiling narration must respect. Zero groups: this
	// fixture is about the ITEM budget, not the group split.
	plan := AnswerPlan{Budget: AnswerPlanBudget{MaxItems: maxItems, SynthesisHeadroom: 20}}
	allocation := AllocateItems(plan, 0, len(ranked.Members))

	judgments, minted, event := narrateCohortDriverJudgments(
		ranked, make([]DriverJudgment, synthesisDrivers), synthesisClaims, citations, allocation)

	if event.Outcome != CohortDriverNarrationEmitted {
		t.Fatalf("fixture drift: event.Outcome = %q, want narration to actually emit", event.Outcome)
	}

	// Narration's own charge against the item budget: every narrated driver
	// judgment and every minted claim is a charged item
	// (ContextFabricResultItemCounts charges Drivers and ClaimedFacts).
	narrationCharge := len(judgments) + len(minted)
	spentBySynthesis := synthesisDrivers + synthesisClaims

	if spentBySynthesis+narrationCharge > maxItems {
		t.Fatalf("narration charged %d items (%d drivers + %d claims) on top of synthesis' %d, against a plan ceiling of %d: "+
			"cohortDriverNarrationBudget is spending against the static contract caps (%d drivers / %d claims) "+
			"instead of plan.Budget.MaxItems, so it is a second spender on a budget it cannot see",
			narrationCharge, len(judgments), len(minted), spentBySynthesis, maxItems,
			contractsv1.ContextFabricDriversMaxCount, contractsv1.ContextFabricClaimedFactsMaxCount)
	}
}

// TestNarrationStillNarratesWhenTheBudgetAllowsIt is the positive control for
// the test above. Bounding narration to zero would satisfy that assertion and
// silently delete a feature; this fails if the fix is "narrate nothing".
func TestNarrationStillNarratesWhenTheBudgetAllowsIt(t *testing.T) {
	t.Parallel()
	ranked, citations := manyRankedMembersCohort(t, 3)

	plan := AnswerPlan{Budget: AnswerPlanBudget{MaxItems: 30, SynthesisHeadroom: 20}}
	judgments, _, event := narrateCohortDriverJudgments(ranked, nil, 0, citations, AllocateItems(plan, 0, len(ranked.Members)))
	if event.Outcome != CohortDriverNarrationEmitted || len(judgments) == 0 {
		t.Fatalf("narration emitted nothing on a cohort with room: outcome=%q judgments=%d", event.Outcome, len(judgments))
	}
}

// allowanceArithmeticFunction is S7c's, and stays S7c's.
const allowanceArithmeticFunction = "narrowCandidatesToBudget"

// TestTheAllowanceArithmeticIsComputedInExactlyOnePlace makes team-lead's
// "one authority per number" ruling STRUCTURAL rather than a convention.
//
// The allocator (PR2B) owns the QUOTA and passes it INTO narrowing as an
// input; narrowCandidatesToBudget (S7c) remains the sole ENFORCER, and the
// allowance subtraction lives there and nowhere else. Two authorities over one
// number is a defect that no compiler catches and that a reviewer sees only if
// they happen to read both sites in the same sitting -- so it is pinned here.
//
// Read from source in the manner limitation_append_closure_test.go already
// establishes for this package.
func TestTheAllowanceArithmeticIsComputedInExactlyOnePlace(t *testing.T) {
	fileSet := token.NewFileSet()
	packages, err := parser.ParseDir(fileSet, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package directory: %v", err)
	}

	var sites []string
	for _, pkg := range packages {
		for fileName, file := range pkg.Files {
			if strings.HasSuffix(fileName, "_test.go") {
				continue
			}
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Body == nil {
					continue
				}
				name := function.Name.Name
				ast.Inspect(function.Body, func(node ast.Node) bool {
					binary, ok := node.(*ast.BinaryExpr)
					if !ok || binary.Op != token.SUB {
						return true
					}
					// The shape of the allowance: something minus a
					// Budgeted() call. That subtraction is the enforcement
					// decision, wherever it is written.
					if !mentionsBudgetedCall(binary) {
						return true
					}
					sites = append(sites, name)
					return true
				})
			}
		}
	}

	for _, site := range sites {
		if site != allowanceArithmeticFunction {
			t.Errorf("the allowance subtraction (X - measurement.Items.Budgeted()) appears in %q as well as %q: "+
				"the quota allocator must pass its numbers INTO narrowing as inputs and never recompute the allowance, "+
				"or the quota and the enforcement become two authorities over one number",
				site, allowanceArithmeticFunction)
		}
	}
	if len(sites) == 0 {
		t.Fatalf("no allowance subtraction found at all: %s no longer computes it, so this guard is watching nothing",
			allowanceArithmeticFunction)
	}
}

// mentionsBudgetedCall reports whether the expression contains a call to
// .Budgeted(), which is what makes a subtraction the ALLOWANCE rather than
// arbitrary arithmetic.
func mentionsBudgetedCall(expression ast.Expr) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Budgeted" {
			found = true
		}
		return true
	})
	return found
}

// TestPredictionIsNotAFunctionOfAMeasuredPerMemberRate guards against
// reintroducing a model this package already refuted.
//
// testdata/grouped_cohort_item_ratio.json records the refutation: a revision
// predicted from a measured items-per-member ratio and clamped the cohort to
// 7, and was reverted because the 7-member totals (21-41) completely overlap
// the 10-member ones (28-39) and the implied per-member rate ROSE as the
// cohort shrank.
//
// WHAT THIS ASSERTS, precisely: that prediction does not USE a per-member
// rate. It does NOT assert that items are insensitive to member count -- that
// artifact's own `claim` field calls its evidence narrow (n=5 and n=7, two
// builds, one org, "a refutation of one specific clamp, not a general law").
// Both directions remain unproven, and this test must not be read as settling
// the converse.
func TestPredictionIsNotAFunctionOfAMeasuredPerMemberRate(t *testing.T) {
	t.Parallel()
	plan := AnswerPlan{Budget: AnswerPlanBudget{MaxItems: 30, SynthesisHeadroom: 20}}

	// A rate model would make prediction super-linear or scale-varying in
	// members. The plan's prediction must move by exactly one item per
	// member -- the member's own row -- and by nothing else.
	for members := 1; members <= 12; members++ {
		got := PredictedItemsForPlan(plan, members)
		next := PredictedItemsForPlan(plan, members+1)
		if delta := next - got; delta != 1 {
			t.Fatalf("prediction moved by %d items when the cohort grew from %d to %d members: "+
				"a per-member RATE has been reintroduced, which testdata/grouped_cohort_item_ratio.json refutes",
				delta, members, members+1)
		}
	}
}
