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

	judgments, mintedClaims, event := narrateCohortDriverJudgments(ranked, nil, 0, citations, ItemAllocation{})
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

	judgments, _, event := narrateCohortDriverJudgments(ranked, nil, 0, citations, ItemAllocation{})
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

	judgments, _, event := narrateCohortDriverJudgments(ranked, make([]DriverJudgment, 50), 0, citations, ItemAllocation{}) // synthesis alone used the whole budget
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
	judgments, _, event := narrateCohortDriverJudgments(ranked, nil, 0, citations, ItemAllocation{})
	if len(judgments) != 0 {
		t.Fatalf("judgments = %+v, want empty", judgments)
	}
	if event.Outcome != CohortDriverNarrationNoDrivers {
		t.Fatalf("event.Outcome = %q, want %q", event.Outcome, CohortDriverNarrationNoDrivers)
	}

	if judgments, _, event := narrateCohortDriverJudgments(nil, nil, 0, nil, ItemAllocation{}); len(judgments) != 0 || event.Outcome != CohortDriverNarrationNoDrivers {
		t.Fatalf("narrateCohortDriverJudgments(nil, nil, 0, nil, ItemAllocation{}) = (%v, %+v), want (empty, no_drivers)", judgments, event)
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

	judgments, _, event := narrateCohortDriverJudgments(ranked, nil, 0, citations, ItemAllocation{})
	if event.Outcome != CohortDriverNarrationEmitted || len(judgments) == 0 {
		t.Fatalf("expected at least one narrated judgment, got event=%+v judgments=%v", event, judgments)
	}
	for _, j := range judgments {
		if err := j.Validate(); err != nil {
			t.Fatalf("DriverJudgment.Validate() = %v for a max-length-label member's narrated judgment, want nil", err)
		}
	}
}

// cohortNarrationRequiredEpistemicStatus is the epistemic state
// docs/design/context-fabric-result-semantics.md §1 prescribes for each
// derivation kind: a rule-derived judgment is the model's own reasoning
// over a heuristic, not something ACR read from a fact provider, so it is
// an INFERENCE -- the one row of that table with no independent grounding
// check behind it. Stamping such a judgment `observed` tells a consumer
// filtering by grounding kind that a ranking heuristic's output is a
// canonical measurement, which is precisely the blur that document exists
// to prevent (and which genkitruntime/prompts.go:183 instructs the model
// itself never to commit).
var cohortNarrationRequiredEpistemicStatus = map[DerivationMethod]EpistemicStatus{
	DerivationRuleInferred: EpistemicInferred,
}

// TestNarrateCohortDriverJudgments_EpistemicStatusMatchesDerivation is
// codex R3 finding 1 (P1, CHAOS-4448). Every judgment this composer emits
// is derived by the RankCohort ranking heuristic -- it correctly carries
// Derivation=rule_inferred, but stamped EpistemicObserved it contradicted
// the result-semantics doc's own four-way split. The assertion is the
// PAIRING, not the literal value: a judgment's epistemic state must be the
// one its own derivation prescribes, so a future narration path that emits
// a differently-derived judgment cannot quietly inherit the wrong state.
func TestNarrateCohortDriverJudgments_EpistemicStatusMatchesDerivation(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{healthFact("A", "high"), investmentFact("A", balancedThemes(), 0)}
	member := rankTestMember("A")
	member.EvidenceRefIDs = []string{"evidence_team_a_roster"}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{member}}
	ranked, _, citations := RankCohort(cohort, facts, availableCoverage())

	judgments, _, event := narrateCohortDriverJudgments(ranked, nil, 0, citations, ItemAllocation{})
	if event.Outcome != CohortDriverNarrationEmitted || len(judgments) == 0 {
		t.Fatalf("expected at least one narrated judgment, got event=%+v judgments=%v", event, judgments)
	}
	for _, j := range judgments {
		want, known := cohortNarrationRequiredEpistemicStatus[j.Derivation]
		if !known {
			t.Fatalf("judgment %q carries derivation %q, which this test has no prescribed epistemic state for -- a new narration derivation must state its own grounding kind, not inherit one", j.DriverID, j.Derivation)
		}
		if j.EpistemicStatus != want {
			t.Errorf("judgment %q: EpistemicStatus = %q for derivation %q, want %q (docs/design/context-fabric-result-semantics.md §1)", j.DriverID, j.EpistemicStatus, j.Derivation, want)
		}
		if err := j.Validate(); err != nil {
			t.Errorf("judgment %q .Validate() = %v, want nil -- the prescribed epistemic state must also be write-side valid", j.DriverID, err)
		}
	}
}

// TestNarrateCohortDriverJudgments_DeconflictsAGeneratedIDAlreadyTakenBySynthesis
// is codex R3 finding 2 (P2, CHAOS-4448). cohortDriverJudgmentID mints
// "cohort-driver-<rank>-<position>" from the member's rank alone -- a
// namespace the synthesis model is nowhere forbidden to use, and a draft
// carrying a driver_id like "cohort-driver-01-1" is entirely legal. The
// composer appended its colliding ID anyway, and validateDrivers, which
// enforces DriverID uniqueness across the WHOLE result.Drivers array,
// then rejected the ENTIRE investigation -- a model's harmless choice of
// identifier destroying an otherwise-valid answer.
//
// The fix must never resolve the clash by dropping the narrated driver
// (that would silently lose a judgment the cohort earned), so this asserts
// BOTH drivers survive with distinct IDs and the combined array validates.
func TestNarrateCohortDriverJudgments_DeconflictsAGeneratedIDAlreadyTakenBySynthesis(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{healthFact("A", "high"), investmentFact("A", balancedThemes(), 0)}
	member := rankTestMember("A")
	member.EvidenceRefIDs = []string{"evidence_team_a_roster"}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{member}}
	ranked, _, citations := RankCohort(cohort, facts, availableCoverage())

	// Exactly the ID this composer would generate for rank 1, position 1.
	collidingID := cohortDriverJudgmentID(ranked.Members[0], 0)
	synthesisDriver := DriverJudgment{
		DriverID:         collidingID,
		Standing:         DriverContributing,
		Category:         string(contractsv1.ContextFabricDriverCategoryNarrative),
		Title:            "a synthesis-authored driver that happens to use the cohort namespace",
		Summary:          "legal model output: nothing in the synthesis contract reserves the cohort-driver- prefix.",
		AffectedSubjects: []SubjectRef{ranked.Members[0].Subject},
		EvidenceRefIDs:   []string{"evidence_team_a_roster"},
		Derivation:       DerivationModelExtracted,
		EpistemicStatus:  EpistemicInferred,
		Confidence:       0.5,
		Current:          true,
	}

	judgments, _, event := narrateCohortDriverJudgments(ranked, []DriverJudgment{synthesisDriver}, 0, citations, ItemAllocation{})
	if event.Outcome != CohortDriverNarrationEmitted || len(judgments) == 0 {
		t.Fatalf("expected narrated judgments, got event=%+v judgments=%v", event, judgments)
	}

	all := append([]DriverJudgment{synthesisDriver}, judgments...)
	seen := make(map[string]int, len(all))
	for _, j := range all {
		seen[j.DriverID]++
		if err := j.Validate(); err != nil {
			t.Errorf("driver %q .Validate() = %v, want nil", j.DriverID, err)
		}
		if got := len(j.DriverID); got < contractsv1.ContextFabricModelMintedIDMinLength || got > contractsv1.ContextFabricModelMintedIDMaxLength {
			t.Errorf("driver id %q length = %d, outside the contract's [%d,%d]", j.DriverID, got, contractsv1.ContextFabricModelMintedIDMinLength, contractsv1.ContextFabricModelMintedIDMaxLength)
		}
	}
	for id, count := range seen {
		if count > 1 {
			t.Errorf("driver id %q appears %d times across synthesis+narration -- validateDrivers rejects the whole result for a duplicate", id, count)
		}
	}
	if len(seen) != len(all) {
		t.Fatalf("got %d distinct driver ids across %d drivers -- every driver must survive with its own id, none dropped", len(seen), len(all))
	}
	// The synthesis driver keeps the ID it authored; narration is what moves.
	if judgments[0].DriverID == collidingID {
		t.Errorf("narrated driver kept the colliding id %q -- narration must yield, never the model's own driver", collidingID)
	}
}

// TestDeconflictCohortDriverJudgmentID_IsDeterministicAndNeverDrops pins the
// deconfliction itself: same inputs always produce the same id (replay- and
// reuse-stable, the same property cohortDriverClaimID's hashed scheme
// carries), and a taken set that already contains the hashed candidate is
// resolved rather than looped on forever.
func TestDeconflictCohortDriverJudgmentID_IsDeterministicAndNeverDrops(t *testing.T) {
	t.Parallel()
	base := "cohort-driver-01-1"

	if got := deconflictCohortDriverJudgmentID(base, map[string]struct{}{}); got != base {
		t.Errorf("deconflict with nothing taken = %q, want the base id %q unchanged", got, base)
	}

	taken := map[string]struct{}{base: {}}
	first := deconflictCohortDriverJudgmentID(base, taken)
	if first == base {
		t.Fatalf("deconflict returned the taken base id %q", base)
	}
	if again := deconflictCohortDriverJudgmentID(base, taken); again != first {
		t.Errorf("deconflict is not deterministic: %q then %q for identical inputs", first, again)
	}

	// Saturate: base and its first candidate both taken -- must still yield
	// a fresh, distinct id rather than spinning or returning a duplicate.
	taken[first] = struct{}{}
	second := deconflictCohortDriverJudgmentID(base, taken)
	if second == base || second == first {
		t.Fatalf("deconflict returned an already-taken id %q (base=%q first=%q)", second, base, first)
	}
	if got := len(second); got > contractsv1.ContextFabricModelMintedIDMaxLength {
		t.Errorf("deconflicted id %q length = %d, over the %d bound", second, got, contractsv1.ContextFabricModelMintedIDMaxLength)
	}
}

// TestNarrateCohortDriverJudgments_CountsDriversSkippedForMissingCitation
// is codex R3 finding 3 (P2, CHAOS-4448). The available-zero deficiency
// driver legitimately ranks (its zero Value counts toward Score) but can
// never be cited -- OperationalDeficienciesProvider emits a row only for a
// rule that actually fired, so "zero fired rules" is zero rows and there
// is no real field anywhere to name. The composer therefore eliminates it
// with `citation == nil`, which is correct and stays correct.
//
// What was missing is the record of that elimination. Root AGENTS.md
// requires every decision branch that changes an outcome -- a candidate
// elimination among them -- to emit closed-vocabulary telemetry naming
// what decided and why, in the same change that adds the branch, so a
// defect is diagnosable from the run's own artifacts alone. Without this
// dimension, a driver selected into the top-3 and then dropped is
// indistinguishable, in the emitted event, from one never selected: the
// operator sees JudgmentsEmitted=2 and no reason it was not 3.
func TestNarrateCohortDriverJudgments_CountsDriversSkippedForMissingCitation(t *testing.T) {
	t.Parallel()
	// availableCoverage + facts for health/investment ONLY: deficiency is
	// available-zero, so it ranks into the top-3 and is then eliminated
	// for want of a citation.
	facts := []CanonicalFact{healthFact("A", "high"), investmentFact("A", balancedThemes(), 0)}
	member := rankTestMember("A")
	member.EvidenceRefIDs = []string{"evidence_team_a_roster"}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{member}}
	ranked, _, citations := RankCohort(cohort, facts, availableCoverage())

	// Guard the premise: this scenario must actually rank the uncitable
	// deficiency driver, otherwise the test would pass without exercising
	// the branch at all.
	ranksDeficiency := false
	for _, d := range ranked.Members[0].Drivers {
		if d.Signal == RankingSignalDeficiencySeverity {
			ranksDeficiency = true
		}
	}
	if !ranksDeficiency {
		t.Fatal("premise broken: the scenario no longer ranks the available-zero deficiency driver, so no citation-skip branch is exercised")
	}

	judgments, _, event := narrateCohortDriverJudgments(ranked, nil, 0, citations, ItemAllocation{})

	if got := event.DriversSkipped[string(CohortDriverSkipNoCitation)]; got != 1 {
		t.Errorf("event.DriversSkipped[%q] = %d, want 1 -- the eliminated zero-fired deficiency driver must be counted with its reason", CohortDriverSkipNoCitation, got)
	}
	// The behaviour itself is unchanged: it still ranks and is still never
	// cited or narrated.
	for _, j := range judgments {
		if j.Category == string(contractsv1.ContextFabricDriverCategoryDeficiency) {
			t.Errorf("narrated the available-zero deficiency driver %+v -- 'ranks but never cites' must be unchanged", j)
		}
	}
	if event.JudgmentsEmitted != len(judgments) || event.MembersNarrated != 1 {
		t.Errorf("event = %+v, want JudgmentsEmitted=%d MembersNarrated=1", event, len(judgments))
	}
}

// TestNarrateCohortDriverJudgments_SkipCountsAreAbsentWhenNothingIsSkipped
// keeps the dimension honest in the other direction: a fully citable
// member must report no skips at all, so a non-zero count always means a
// real elimination happened rather than being a permanent fixture of the
// event.
func TestNarrateCohortDriverJudgments_SkipCountsAreAbsentWhenNothingIsSkipped(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{
		investmentFact("A", balancedThemes(), 0),
		healthFact("A", "elevated"),
		deficiencyFact("A", "critical"),
		readinessFact("A", 0.6),
		workloadFact("A", 20),
	}
	member := rankTestMember("A")
	member.EvidenceRefIDs = []string{"evidence_team_a_roster"}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{member}}
	ranked, _, citations := RankCohort(cohort, facts, availableCoverage())

	judgments, _, event := narrateCohortDriverJudgments(ranked, nil, 0, citations, ItemAllocation{})
	if len(judgments) == 0 {
		t.Fatal("expected narrated judgments for a fully-citable member")
	}
	for reason, count := range event.DriversSkipped {
		t.Errorf("event.DriversSkipped[%q] = %d, want no skip entries at all when every selected driver was citable", reason, count)
	}
}

// ---------------------------------------------------------------------
// CHAOS-4580: teams-answer wordiness -- grammar template and answer
// narrative recomposition.
// ---------------------------------------------------------------------

// TestCohortDriverJudgmentSummary_NoStrayArticleOrDoubleNoun pins the exact
// grammar chris quoted (CHAOS-4580, CHAOS-4533's narrator half). The prior
// template read "...contributed 20.0 points to Fullchaos's a 46.7
// attention score." -- a stray article baked into scoreText plus
// "points"/"score" swapped against the rest of the sentence. Both the
// scored and unranked (nil Score) shapes are pinned so neither can
// regress independently.
func TestCohortDriverJudgmentSummary_NoStrayArticleOrDoubleNoun(t *testing.T) {
	t.Parallel()
	member := CohortMember{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"}}
	driver := CohortMemberDriver{Signal: RankingSignalReadinessGap, Weight: 15, Value: 1.00, WeightContributed: 20.0}

	t.Run("scored member", func(t *testing.T) {
		score := 46.7
		scored := member
		scored.Score = &score
		got := cohortDriverJudgmentSummary(scored, driver)
		want := "readiness gap (weight 15, value 1.00) contributed 20.0 of Fullchaos's 46.7 attention points."
		if got != want {
			t.Fatalf("cohortDriverJudgmentSummary() = %q, want %q", got, want)
		}
		if strings.Contains(got, "'s a ") {
			t.Fatalf("cohortDriverJudgmentSummary() = %q, still contains the stray possessive-article bug", got)
		}
	})

	t.Run("unranked member (nil Score)", func(t *testing.T) {
		got := cohortDriverJudgmentSummary(member, driver)
		want := "readiness gap (weight 15, value 1.00) contributed 20.0 of Fullchaos's unranked attention points."
		if got != want {
			t.Fatalf("cohortDriverJudgmentSummary() = %q, want %q", got, want)
		}
	})
}

// TestRecomposeCohortAnswerNarrative proves the CHAOS-4690 rewrite (design
// §5, sol r1 F1): CHAOS-4580's principal-driver-clause splice is superseded,
// not reworded -- BOTH DirectJudgment and DeterministicAnswer become the
// bare status sentence, independently truncated at their own bound. No
// scoring arithmetic ("(weight ", "attention points") and no new
// deterministic display language appear in either field; the driver's own
// numbers stay on its structured Weight/Value/WeightContributed fields and
// cohortDriverJudgmentSummary text (unchanged), never spliced into the
// lead. A prior version of this test asserted the CHAOS-4580 splice text
// ("Principal driver(s): ...") -- that assertion is gone, not weakened,
// per the CHAOS-4690 ruling.
func TestRecomposeCohortAnswerNarrative(t *testing.T) {
	t.Parallel()
	wantSentence := "This investigation is partial: some canonical or graph coverage was unavailable."

	directJudgment, deterministicAnswer := recomposeCohortAnswerNarrative(InvestigationPartial, SubjectResolution{})
	if directJudgment != wantSentence {
		t.Fatalf("directJudgment = %q, want %q (status sentence alone)", directJudgment, wantSentence)
	}
	if deterministicAnswer != wantSentence {
		t.Fatalf("deterministicAnswer = %q, want %q (status sentence alone -- CHAOS-4690 supersedes CHAOS-4580's driver-clause splice)", deterministicAnswer, wantSentence)
	}
	if strings.Contains(deterministicAnswer, "(weight ") || strings.Contains(directJudgment, "(weight ") {
		t.Fatalf("narrative must never carry scoring arithmetic: directJudgment=%q deterministicAnswer=%q", directJudgment, deterministicAnswer)
	}
	if strings.Contains(deterministicAnswer, "attention points") || strings.Contains(directJudgment, "attention points") {
		t.Fatalf("narrative must never carry scoring arithmetic: directJudgment=%q deterministicAnswer=%q", directJudgment, deterministicAnswer)
	}
	if strings.Contains(deterministicAnswer, "Principal driver") || strings.Contains(directJudgment, "Principal driver") {
		t.Fatalf("narrative must never carry the CHAOS-4580 driver clause: directJudgment=%q deterministicAnswer=%q", directJudgment, deterministicAnswer)
	}
	if strings.Contains(deterministicAnswer, "Canonical facts:") {
		t.Fatalf("deterministicAnswer = %q, must never restate the raw key=value facts list -- that lives in CurrentState only", deterministicAnswer)
	}
}
