package devhealthfacts_test

// CHAOS-5438, round r2's P1 -- the SHARED budget and the SINGLE truncation
// verdict for the two multi-branch providers.
//
// WHAT WENT WRONG, and why it needed its own pin. IdentityProvider and
// MembershipProvider each append into ONE facts slice across a repository
// branch and a work-item branch. The output guard was written against that
// shared slice; truncation was computed PER BRANCH from each branch's own
// 201-row probe. The two disagreed exactly where it mattered:
//
//	150 repository rows + 150 work-item rows
//	-> neither branch reaches its own probe limit, so both report "not
//	   truncated"
//	-> the shared cap silently drops 100 facts
//	-> served: facts=200, Truncated=false -- a completeness claim the answer
//	   had not earned
//
// Reproduced on the pushed tip before the fix, both providers, exactly that.
//
// It is the SAME shared-observable defect as this ticket's first one, a level
// down: that was a shared truncation FLAG across two branches, this was a
// shared output BUDGET. So the fix is not "re-split the cap" -- that would
// leave the guard and the verdict as two separate computations, free to drift
// a third time. One helper owns both, and Truncated means what the fact-scope
// ruling already says it means: at least one authorized candidate fact was not
// served, no matter which branch it came from.
//
// THE SHAPE SPACE, both providers, each pin asserting the FACT COUNT and the
// VERDICT together -- either alone can be right while the pair is wrong, which
// is precisely how the defect survived the first fix.

import (
	"context"
	"strconv"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// sharedBudgetArm is one of the two providers that read from both branches.
type sharedBudgetArm struct {
	name    string
	kind    contextfabric.FactKind
	repoRow func(i int) []any
	wiRow   func(i int) []any
}

func sharedBudgetArms() []sharedBudgetArm {
	return []sharedBudgetArm{
		{
			name: "identity", kind: contextfabric.FactIdentity,
			repoRow: func(i int) []any {
				return []any{"repo-" + strconv.Itoa(i), "acme/r" + strconv.Itoa(i), "github"}
			},
			wiRow: func(i int) []any {
				return []any{"WI-" + strconv.Itoa(i), "a title", chaos5438BudgetRepoID}
			},
		},
		{
			name: "membership", kind: contextfabric.FactMembership,
			repoRow: func(i int) []any { return []any{"repo-" + strconv.Itoa(i)} },
			wiRow: func(i int) []any {
				return []any{"WI-" + strconv.Itoa(i), chaos5438BudgetRepoID, "acme/widget-service"}
			},
		},
	}
}

// chaos5438BudgetRepoID is the repo the work-item rows belong to. It is
// deliberately NOT one of the "repo-N" ids the repository branch asks about,
// so the two branches contribute DISJOINT facts and the shared budget is the
// only thing that can bind.
const chaos5438BudgetRepoID = "repo-work-items"

// readBothBranches drives one provider with repoCount repository subjects and
// workItemCount work-item subjects, and canned rows for each branch.
func readBothBranches(t *testing.T, arm sharedBudgetArm, repoCount, workItemCount int) contextfabric.FactProviderResult {
	t.Helper()
	repoRows := make([][]any, repoCount)
	wiRows := make([][]any, workItemCount)
	subjects := make([]contextfabric.SubjectRef, 0, repoCount+workItemCount)
	for i := 0; i < repoCount; i++ {
		repoRows[i] = arm.repoRow(i)
		subjects = append(subjects, repoSubject("repo-"+strconv.Itoa(i)))
	}
	for i := 0; i < workItemCount; i++ {
		wiRows[i] = arm.wiRow(i)
		subjects = append(subjects, workItemSubject(chaos5438BudgetRepoID, "WI-"+strconv.Itoa(i)))
	}
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repos", rows: repoRows},
		{match: "FROM work_items", rows: wiRows},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), arm.kind)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: arm.kind, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("%s: ReadFacts: %v", arm.name, err)
	}
	return result
}

// TestChaos5438_TheSharedBudgetAndTheVerdictAgree walks the shape space.
func TestChaos5438_TheSharedBudgetAndTheVerdictAgree(t *testing.T) {
	t.Parallel()
	for _, shape := range []struct {
		repo, workItem int
		wantFacts      int
		wantTruncated  bool
		why            string
	}{
		{0, 0, 0, false, "nothing was read, so nothing was left behind"},
		{100, 100, 200, false, "exactly fills the shared budget with nothing refused -- a complete answer at the boundary"},
		{150, 150, 200, true, "THE DEFECT: neither branch overflows its own probe, but the shared budget refuses 100 facts"},
		{201, 0, 200, true, "the repository branch alone overflowed its 201-row probe"},
		{0, 201, 200, true, "the work-item branch alone overflowed its 201-row probe"},
		{201, 201, 200, true, "both branches overflowed AND the shared budget refused"},
	} {
		shape := shape
		for _, arm := range sharedBudgetArms() {
			arm := arm
			t.Run(arm.name+"/"+strconv.Itoa(shape.repo)+"+"+strconv.Itoa(shape.workItem), func(t *testing.T) {
				t.Parallel()
				result := readBothBranches(t, arm, shape.repo, shape.workItem)
				if len(result.Facts) != shape.wantFacts {
					t.Fatalf("%s (%d repo + %d work-item): len(Facts) = %d, want %d -- %s",
						arm.name, shape.repo, shape.workItem, len(result.Facts), shape.wantFacts, shape.why)
				}
				if result.Truncated != shape.wantTruncated {
					t.Fatalf("%s (%d repo + %d work-item): Truncated = %v, want %v -- %s",
						arm.name, shape.repo, shape.workItem, result.Truncated, shape.wantTruncated, shape.why)
				}
			})
		}
	}
}

// TestChaos5438_EverySingleBranchProviderUsesTheSameOwner is the package-wide
// half of the same ruling.
//
// The five providers below read from ONE branch, so the guard and the verdict
// could not drift apart in them the way they did in the two multi-branch ones.
// They route through the same factBudget anyway, and this pins the OBSERVABLE
// consequence rather than the routing: at the cap the answer is complete, and
// one row past it the answer says so. A hand-rolled second copy that got
// either cell wrong shows up here.
//
// Three cells per provider -- 0, cap, cap+1 -- asserting the fact count AND
// the verdict together, for the same reason the shared-budget shape space
// does: either can be right while the pair is wrong.
func TestChaos5438_EverySingleBranchProviderUsesTheSameOwner(t *testing.T) {
	t.Parallel()
	for _, arm := range probeArms() {
		if arm.name == "identity_repository" || arm.name == "membership_repository" {
			// Covered by the shared-budget shape space above, which drives
			// BOTH of their branches at once.
			continue
		}
		arm := arm
		for _, cell := range []struct {
			rows          int
			wantFacts     int
			wantTruncated bool
			why           string
		}{
			{0, 0, false, "nothing was read, so nothing was left behind"},
			{factRowOutputBound, factRowOutputBound, false, "exactly the cap: complete, and every row of it served"},
			{factRowProbeLimit, factRowOutputBound, true, "one row past the cap: the overflow row proves the shortfall and is never served"},
		} {
			cell := cell
			t.Run(arm.name+"/"+strconv.Itoa(cell.rows), func(t *testing.T) {
				t.Parallel()
				_, result := readProbeArm(t, arm, cell.rows)
				if len(result.Facts) != cell.wantFacts {
					t.Fatalf("%s (%d rows): len(Facts) = %d, want %d -- %s", arm.name, cell.rows, len(result.Facts), cell.wantFacts, cell.why)
				}
				if result.Truncated != cell.wantTruncated {
					t.Fatalf("%s (%d rows): Truncated = %v, want %v -- %s", arm.name, cell.rows, result.Truncated, cell.wantTruncated, cell.why)
				}
			})
		}
	}
}

// TestChaos5438_AnOverflowOfSKIPPEDRowsIsStillTruncation closes a hole this
// lane found by running a mutation control that FAILED TO FIRE.
//
// The three-cell pin above drives rows that map one-to-one onto requested
// subjects, so at 201 rows the shared guard refuses the 201st fact and the
// verdict is true through `dropped`. Deleting `budget.observe` therefore did
// not kill it -- the assertion was VACUOUS for the observe path, and a control
// that does not fire is not a control.
//
// The path it missed is real: a provider can read a full probe page while most
// rows SKIP (their subject is not in the requested set), so few facts are ever
// built, the guard never refuses, and `observe` is the ONLY thing that knows
// rows were left behind in the database. That is a truncated read reporting
// itself complete -- this ticket's whole defect, on the branch nothing else
// covers.
func TestChaos5438_AnOverflowOfSKIPPEDRowsIsStillTruncation(t *testing.T) {
	t.Parallel()
	for _, arm := range probeArms() {
		if arm.name == "blockers" || arm.name == "required_children" {
			// These two attribute every row to ONE subject by design, so a
			// "skipped row" is not a shape they can take.
			continue
		}
		arm := arm
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			// A full probe page of rows, but only ONE subject requested: 200
			// of the 201 rows resolve to no requested subject and are skipped.
			client := &fakeClient{tables: []fakeTable{{match: arm.match, rows: arm.rowsFor(factRowProbeLimit)}}}
			provider := findProvider(t, devhealthfacts.NewProviders(client), arm.kind)
			result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
				Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind:     arm.kind,
				Subjects: arm.subjectsFor(1),
			})
			if err != nil {
				t.Fatalf("%s: ReadFacts: %v", arm.name, err)
			}
			if len(result.Facts) > 1 {
				t.Fatalf("%s: len(Facts) = %d, want at most 1 -- only one subject was requested", arm.name, len(result.Facts))
			}
			if !result.Truncated {
				t.Fatalf("%s: Truncated = false after reading a full probe page of %d rows -- the guard never refused because the rows were SKIPPED, so only the probe observation knows anything was left behind",
					arm.name, factRowProbeLimit)
			}
		})
	}
}
