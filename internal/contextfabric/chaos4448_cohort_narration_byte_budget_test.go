package contextfabric

import (
	"encoding/json"
	"strconv"
	"testing"
)

// TestCohortDriverNarration_250MemberCohortStaysWithinByteBudget is the
// permanent assertion team-lead's "minting follows citation, not ranking"
// ruling required (CHAOS-4398 PR3b): a 250-member, ALL-FIVE-FAMILIES-
// qualified cohort must not let this PR's new bytes -- minted ClaimedFacts,
// per-driver SourceClaimedFactIDs provenance, and narrated
// ContextFabricDriverJudgment entries -- scale with cohort size.
//
// Before this ruling, RankCohort minted a ClaimedFact for every AVAILABLE
// signal on every member unconditionally: measured ~708 KB worst-case for
// this exact 250-member scenario, ~2.8x the 256 KB response budget. Under
// the citation-gated architecture, minting happens ONLY at narration time
// for a driver narrateCohortDriverJudgments actually decides to narrate --
// bounded by cohortDriverNarrationBudget's own cap (<=16 members * 3
// drivers = 48 claims/judgments), independent of how many members the
// cohort actually has. This test proves that bound holds at 250 members,
// not just asserts it in a comment.
func TestCohortDriverNarration_250MemberCohortStaysWithinByteBudget(t *testing.T) {
	t.Parallel()

	const memberCount = 250
	const byteBudget = 64 * 1024 // 64 KB, team-lead's stated ceiling

	members := make([]CohortMember, 0, memberCount)
	var facts []CanonicalFact
	for i := 0; i < memberCount; i++ {
		id := teamIDForByteBudgetTest(i)
		member := rankTestMember(id)
		member.EvidenceRefIDs = []string{"evidence_" + id + "_roster"}
		members = append(members, member)
		facts = append(facts,
			investmentFact(id, balancedThemes(), 0.1),
			healthFact(id, "elevated"),
			deficiencyFact(id, "critical"),
			readinessFact(id, 0.6),
			workloadFact(id, 20),
		)
	}
	cohort := &Cohort{Kind: SubjectTeam, Members: members}

	ranked, _, citations := RankCohort(cohort, facts, availableCoverage())
	for i, member := range ranked.Members {
		if !member.RankingComputed || member.Score == nil {
			t.Fatalf("member %d (%s) was not ranked as expected", i, member.Subject.CanonicalID)
		}
	}

	// synthesisDriverCount=0: give narration the full driver budget, the
	// worst case for how many members/drivers this PR can mint for.
	judgments, mintedClaims, event := narrateCohortDriverJudgments(ranked, nil, 0, citations, ItemAllocation{})
	if event.Outcome != CohortDriverNarrationEmitted {
		t.Fatalf("event.Outcome = %q, want %q", event.Outcome, CohortDriverNarrationEmitted)
	}
	if event.MembersNarrated == 0 || event.JudgmentsEmitted == 0 {
		t.Fatalf("expected real narration output, got event=%#v", event)
	}

	newBytes := 0
	newBytes += marshalSizeForByteBudgetTest(t, judgments)
	newBytes += marshalSizeForByteBudgetTest(t, mintedClaims)
	for _, member := range ranked.Members {
		for _, driver := range member.Drivers {
			if len(driver.SourceClaimedFactIDs) == 0 {
				continue
			}
			newBytes += marshalSizeForByteBudgetTest(t, driver.SourceClaimedFactIDs)
		}
	}

	t.Logf("250-member cohort (all 5 families qualified): %d judgments, %d minted claims, %d new bytes (budget %d)",
		len(judgments), len(mintedClaims), newBytes, byteBudget)

	if newBytes > byteBudget {
		t.Fatalf("new bytes from this PR = %d, want <= %d (250-member, all-families-qualified cohort)", newBytes, byteBudget)
	}
}

func teamIDForByteBudgetTest(i int) string {
	return "BUDGET" + strconv.Itoa(i)
}

func marshalSizeForByteBudgetTest(t *testing.T, v any) int {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return len(encoded)
}
