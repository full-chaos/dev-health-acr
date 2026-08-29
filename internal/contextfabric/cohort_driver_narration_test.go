package contextfabric

import (
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestCohortDriverNarrationBudget pins design doc §5a's "Ordering problem"
// formula: available = min(maxDrivers-synthesisDriverCount,
// maxClaimedFacts-synthesisClaimedFactCount), membersToNarrate =
// floor(available / 3), capped at 16, 3 drivers per narrated member.
//
// The claimed-facts dimension (codex R1, CHAOS-4398 PR3b) exists because a
// synthesis draft can legitimately carry up to ContextFabricClaimedFactsMaxCount
// (250) claims entirely on its own, independent of how many driver slots
// it used -- narration mints exactly one MORE claim per narrated driver,
// so the claimed-facts budget can bind even when the driver budget has
// plenty of room left (and vice versa): whichever resource is scarcer
// gates membersToNarrate.
func TestCohortDriverNarrationBudget(t *testing.T) {
	tests := []struct {
		name                      string
		maxDrivers                int
		synthesisDriverCount      int
		maxClaimedFacts           int
		synthesisClaimedFactCount int
		wantMembers               int
		wantDriversPerMember      int
	}{
		{
			name:       "no synthesis drivers at all -- full budget available, still capped at 16",
			maxDrivers: 50, synthesisDriverCount: 0, maxClaimedFacts: 250, synthesisClaimedFactCount: 0,
			wantMembers: 16, wantDriversPerMember: 3, // floor(50/3)=16, cap doesn't even bind yet
		},
		{
			name:       "synthesis used most of the driver budget -- only 2 members fit",
			maxDrivers: 50, synthesisDriverCount: 44, maxClaimedFacts: 250, synthesisClaimedFactCount: 0,
			wantMembers: 2, wantDriversPerMember: 3, // available=6, floor(6/3)=2
		},
		{
			name:       "synthesis used exactly the whole driver budget -- zero members narrated",
			maxDrivers: 50, synthesisDriverCount: 50, maxClaimedFacts: 250, synthesisClaimedFactCount: 0,
			wantMembers: 0, wantDriversPerMember: 0,
		},
		{
			name:       "synthesis somehow exceeded the driver budget -- fail safe to zero, not negative",
			maxDrivers: 50, synthesisDriverCount: 55, maxClaimedFacts: 250, synthesisClaimedFactCount: 0,
			wantMembers: 0, wantDriversPerMember: 0,
		},
		{
			name:       "available leaves fewer than 3 -- rounds down to zero members, not a partial narration",
			maxDrivers: 50, synthesisDriverCount: 48, maxClaimedFacts: 250, synthesisClaimedFactCount: 0,
			wantMembers: 0, wantDriversPerMember: 0, // available=2, floor(2/3)=0
		},
		{
			name:       "cap actually binds when maxDrivers is hypothetically larger",
			maxDrivers: 200, synthesisDriverCount: 0, maxClaimedFacts: 250, synthesisClaimedFactCount: 0,
			wantMembers: 16, wantDriversPerMember: 3, // floor(200/3)=66, capped to 16
		},
		{
			name:       "synthesis alone already used nearly the whole claimed-facts budget -- claims gate, not drivers",
			maxDrivers: 50, synthesisDriverCount: 0, maxClaimedFacts: 250, synthesisClaimedFactCount: 244,
			wantMembers: 2, wantDriversPerMember: 3, // availableDrivers=50, availableClaims=6 -- claims is the binding resource, floor(6/3)=2
		},
		{
			name:       "synthesis used exactly the whole claimed-facts budget -- zero members narrated even with driver room",
			maxDrivers: 50, synthesisDriverCount: 0, maxClaimedFacts: 250, synthesisClaimedFactCount: 250,
			wantMembers: 0, wantDriversPerMember: 0,
		},
		{
			name:       "synthesis somehow exceeded the claimed-facts budget -- fail safe to zero, not negative",
			maxDrivers: 50, synthesisDriverCount: 0, maxClaimedFacts: 250, synthesisClaimedFactCount: 255,
			wantMembers: 0, wantDriversPerMember: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotMembers, gotDriversPerMember := cohortDriverNarrationBudget(tc.maxDrivers, tc.synthesisDriverCount, tc.maxClaimedFacts, tc.synthesisClaimedFactCount)
			if gotMembers != tc.wantMembers {
				t.Errorf("membersToNarrate = %d, want %d", gotMembers, tc.wantMembers)
			}
			if gotDriversPerMember != tc.wantDriversPerMember {
				t.Errorf("driversPerMember = %d, want %d", gotDriversPerMember, tc.wantDriversPerMember)
			}
		})
	}
}

// TestSelectMembersForDriverNarration proves the selection is exactly the
// top-N-by-AttentionRank members RankCohort actually ranked -- never a
// member RankCohort skipped (RankingComputed false), never a member below
// the cutoff, and in AttentionRank order regardless of pool order.
func TestSelectMembersForDriverNarration(t *testing.T) {
	member := func(rank int, ranked bool) CohortMember {
		return CohortMember{RankingComputed: ranked, AttentionRank: rank}
	}

	t.Run("returns nil for a non-positive count", func(t *testing.T) {
		members := []CohortMember{member(1, true)}
		if got := selectMembersForDriverNarration(members, 0); got != nil {
			t.Errorf("count=0: got %v, want nil", got)
		}
		if got := selectMembersForDriverNarration(members, -1); got != nil {
			t.Errorf("count=-1: got %v, want nil", got)
		}
	})

	t.Run("skips unranked members entirely, even within the cutoff window", func(t *testing.T) {
		members := []CohortMember{
			member(1, true),
			{RankingComputed: false}, // never ranked; AttentionRank zero-value, must never be selected
			member(2, true),
		}
		got := selectMembersForDriverNarration(members, 3)
		if len(got) != 2 {
			t.Fatalf("got %d members, want 2 (the unranked entry must be excluded): %+v", len(got), got)
		}
	})

	t.Run("selects exactly the top N by AttentionRank, reordering out of pool order", func(t *testing.T) {
		// Deliberately out of pool order: rank 3 appears before rank 1.
		members := []CohortMember{
			member(3, true),
			member(1, true),
			member(2, true),
			member(4, true),
		}
		got := selectMembersForDriverNarration(members, 2)
		if len(got) != 2 {
			t.Fatalf("got %d members, want 2", len(got))
		}
		if got[0].AttentionRank != 1 || got[1].AttentionRank != 2 {
			t.Errorf("got ranks [%d, %d], want [1, 2] in that order", got[0].AttentionRank, got[1].AttentionRank)
		}
	})

	t.Run("a count larger than the ranked pool returns only the ranked members", func(t *testing.T) {
		members := []CohortMember{member(1, true), member(2, true)}
		got := selectMembersForDriverNarration(members, 16)
		if len(got) != 2 {
			t.Errorf("got %d members, want 2 (cannot invent members beyond what was ranked)", len(got))
		}
	})
}

// ---------------------------------------------------------------------
// narrateCohortDriverJudgments -- the §5a composer itself.
// ---------------------------------------------------------------------

// TestNarrateCohortDriverJudgments_EmitsAValidJudgmentCitingTheDriver is
// the central end-to-end proof: a RankCohort-ranked member WITH its own
// EvidenceRefIDs set must produce at least one narrated
// ContextFabricDriverJudgment that (a) passes the full write-path
// Validate() on its own, (b) cites the driver's own SourceClaimedFactIDs
// as ClaimedFactIDs (a resolution, never a re-mint), and (c) never
// introduces a number absent from the driver/member it narrates.
func TestNarrateCohortDriverJudgments_EmitsAValidJudgmentCitingTheDriver(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{healthFact("A", "high"), investmentFact("A", balancedThemes(), 0)}
	member := rankTestMember("A")
	member.EvidenceRefIDs = []string{"evidence_team_a_roster"}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{member}}
	ranked, _, citations := RankCohort(cohort, facts, availableCoverage())

	judgments, mintedClaims, event := narrateCohortDriverJudgments(ranked, 0, 0, citations)
	if event.Outcome != CohortDriverNarrationEmitted {
		t.Fatalf("event.Outcome = %q, want %q", event.Outcome, CohortDriverNarrationEmitted)
	}
	if len(judgments) == 0 {
		t.Fatal("judgments = empty, want at least one")
	}
	if event.JudgmentsEmitted != len(judgments) || event.MembersNarrated != 1 || event.MembersSkippedNoEvidence != 0 {
		t.Fatalf("event = %+v, want JudgmentsEmitted=%d MembersNarrated=1 MembersSkippedNoEvidence=0", event, len(judgments))
	}
	if event.FactsMinted != len(mintedClaims) || len(mintedClaims) != len(judgments) {
		t.Fatalf("event.FactsMinted = %d, len(mintedClaims) = %d, len(judgments) = %d -- minting follows citation 1:1 with narrated judgments", event.FactsMinted, len(mintedClaims), len(judgments))
	}
	mintedByID := make(map[string]ClaimedFact, len(mintedClaims))
	for _, c := range mintedClaims {
		mintedByID[c.ClaimID] = c
	}

	rankedMember := ranked.Members[0]
	driversBySignal := make(map[string]CohortMemberDriver, len(rankedMember.Drivers))
	for _, d := range rankedMember.Drivers {
		driversBySignal[d.Signal] = d
	}

	sawPrincipal := false
	for _, j := range judgments {
		if err := j.Validate(); err != nil {
			t.Fatalf("judgment %+v .Validate() = %v, want nil", j, err)
		}
		driverSignal, ok := signalForDriverCategory(j.Category)
		if !ok {
			t.Fatalf("judgment.Category = %q, not a recognized cohort signal category", j.Category)
		}
		driver, ok := driversBySignal[driverSignal]
		if !ok {
			t.Fatalf("judgment cites signal %q, which has no matching driver on the ranked member", driverSignal)
		}
		if !reflect.DeepEqual(j.ClaimedFactIDs, driver.SourceClaimedFactIDs) {
			t.Fatalf("judgment.ClaimedFactIDs = %v, want the driver's own SourceClaimedFactIDs %v (a resolution, not a re-mint)", j.ClaimedFactIDs, driver.SourceClaimedFactIDs)
		}
		if len(j.ClaimedFactIDs) != 1 {
			t.Fatalf("judgment.ClaimedFactIDs = %v, want exactly one entry", j.ClaimedFactIDs)
		}
		if _, resolved := mintedByID[j.ClaimedFactIDs[0]]; !resolved {
			t.Fatalf("judgment cites claim %q, which is not among the minted claims %v -- a dangling reference", j.ClaimedFactIDs[0], mintedByID)
		}
		if len(j.AffectedSubjects) != 1 || j.AffectedSubjects[0] != rankedMember.Subject {
			t.Fatalf("judgment.AffectedSubjects = %+v, want exactly [%+v]", j.AffectedSubjects, rankedMember.Subject)
		}
		if j.Standing == DriverPrincipal {
			sawPrincipal = true
		}
	}
	if !sawPrincipal {
		t.Error("no judgment carries Standing=principal -- the highest-ranked member's top driver must")
	}
}

// signalForDriverCategory reverses cohortSignalDriverCategory for the test
// above's own assertions.
func signalForDriverCategory(category string) (string, bool) {
	for signal, cat := range cohortSignalDriverCategory {
		if string(cat) == category {
			return signal, true
		}
	}
	return "", false
}

// TestNarrateCohortDriverJudgments_SkipsMembersWithoutEvidence proves a
// ranked member with NO EvidenceRefIDs of its own is skipped entirely --
// never narrated with a fabricated evidence reference -- and that the skip
// is counted, not silently folded into "nothing happened".
func TestNarrateCohortDriverJudgments_SkipsMembersWithoutEvidence(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{healthFact("A", "high"), investmentFact("A", balancedThemes(), 0)}
	member := rankTestMember("A") // no EvidenceRefIDs set
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{member}}
	ranked, _, citations := RankCohort(cohort, facts, availableCoverage())

	judgments, _, event := narrateCohortDriverJudgments(ranked, 0, 0, citations)
	if len(judgments) != 0 {
		t.Fatalf("judgments = %+v, want empty (no evidence to cite)", judgments)
	}
	if event.Outcome != CohortDriverNarrationEmitted {
		t.Fatalf("event.Outcome = %q, want %q (budget and drivers were both available; only evidence was missing)", event.Outcome, CohortDriverNarrationEmitted)
	}
	if event.MembersSkippedNoEvidence != 1 {
		t.Fatalf("event.MembersSkippedNoEvidence = %d, want 1", event.MembersSkippedNoEvidence)
	}
}

// TestNarrateCohortDriverJudgments_BudgetExhausted proves a synthesis pass
// that already used the entire shared driver budget produces ZERO
// narrated judgments and reports CohortDriverNarrationBudgetExhausted --
// even when the cohort itself has real, available drivers.
func TestNarrateCohortDriverJudgments_BudgetExhausted(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{healthFact("A", "high"), investmentFact("A", balancedThemes(), 0)}
	member := rankTestMember("A")
	member.EvidenceRefIDs = []string{"evidence_team_a_roster"}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{member}}
	ranked, _, citations := RankCohort(cohort, facts, availableCoverage())

	judgments, _, event := narrateCohortDriverJudgments(ranked, 50, 0, citations) // synthesis alone used the whole budget
	if len(judgments) != 0 {
		t.Fatalf("judgments = %+v, want empty", judgments)
	}
	if event.Outcome != CohortDriverNarrationBudgetExhausted {
		t.Fatalf("event.Outcome = %q, want %q", event.Outcome, CohortDriverNarrationBudgetExhausted)
	}
}

// TestNarrateCohortDriverJudgments_NoDriversAtAll proves a cohort that
// never ranked (or ranked with zero drivers on every member) reports
// CohortDriverNarrationNoDrivers -- distinct from budget_exhausted, and
// checked BEFORE the budget math runs at all.
func TestNarrateCohortDriverJudgments_NoDriversAtAll(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{rankTestMember("A")}}
	ranked, _, citations := RankCohort(cohort, nil, deficiencyPrunedCoverage()) // zero signals available at all
	judgments, _, event := narrateCohortDriverJudgments(ranked, 0, 0, citations)
	if len(judgments) != 0 {
		t.Fatalf("judgments = %+v, want empty", judgments)
	}
	if event.Outcome != CohortDriverNarrationNoDrivers {
		t.Fatalf("event.Outcome = %q, want %q", event.Outcome, CohortDriverNarrationNoDrivers)
	}

	if judgments, _, event := narrateCohortDriverJudgments(nil, 0, 0, nil); len(judgments) != 0 || event.Outcome != CohortDriverNarrationNoDrivers {
		t.Fatalf("narrateCohortDriverJudgments(nil, 0, 0, nil) = (%v, %+v), want (empty, no_drivers)", judgments, event)
	}
}

// TestCohortDriverJudgmentTitle_TruncatesAMaxLengthLabelToStayWithinBounds
// is codex R2's finding (CHAOS-4398 PR3b): ContextFabricSubjectRef.Label
// permits up to 512 runes, the SAME bound as ContextFabricDriverTitleMaxLength,
// so "<Label>: <display name>" could exceed the title bound for a legal,
// near-max-length label -- rejecting an otherwise-valid investigation at
// the very last step. The title must stay within bounds regardless.
func TestCohortDriverJudgmentTitle_TruncatesAMaxLengthLabelToStayWithinBounds(t *testing.T) {
	t.Parallel()
	maxLabel := strings.Repeat("x", contractsv1.ContextFabricSubjectRefLabelMaxLength)
	member := CohortMember{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CHAOS", Label: maxLabel}}
	driver := CohortMemberDriver{Signal: RankingSignalHealthRisk}

	title := cohortDriverJudgmentTitle(member, driver)

	if got := len([]rune(title)); got > contractsv1.ContextFabricDriverTitleMaxLength {
		t.Fatalf("title length = %d runes, want <= %d for a max-length label", got, contractsv1.ContextFabricDriverTitleMaxLength)
	}
}

// TestNarrateCohortDriverJudgments_MaxLengthSubjectLabelProducesAValidJudgment
// is the result-level regression codex asked for: a narrated judgment for
// a member with a legal maximum-length Label must still pass the FULL
// write-path DriverJudgment.Validate() -- proving the title bound actually
// holds end-to-end, not just inside cohortDriverJudgmentTitle in isolation.
func TestNarrateCohortDriverJudgments_MaxLengthSubjectLabelProducesAValidJudgment(t *testing.T) {
	t.Parallel()
	maxLabel := strings.Repeat("x", contractsv1.ContextFabricSubjectRefLabelMaxLength)
	facts := []CanonicalFact{healthFact("A", "high"), investmentFact("A", balancedThemes(), 0)}
	member := rankTestMember("A")
	member.Subject.Label = maxLabel
	member.EvidenceRefIDs = []string{"evidence_team_a_roster"}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{member}}
	ranked, _, citations := RankCohort(cohort, facts, availableCoverage())

	judgments, _, event := narrateCohortDriverJudgments(ranked, 0, 0, citations)
	if event.Outcome != CohortDriverNarrationEmitted || len(judgments) == 0 {
		t.Fatalf("expected at least one narrated judgment, got event=%+v judgments=%v", event, judgments)
	}
	for _, j := range judgments {
		if err := j.Validate(); err != nil {
			t.Fatalf("DriverJudgment.Validate() = %v for a max-length-label member's narrated judgment, want nil", err)
		}
	}
}
