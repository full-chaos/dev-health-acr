package hosted_test

import (
	"encoding/json"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4525 -- the discovered-cohort class must be able to enter the
// answer-rate denominator.
//
// WHY THIS EXISTS (Run J, CHAOS-4450): neither North Star bar question could
// be expressed in an answerable band. The project-status half was a pure
// corpus gap (no subject_status + project case carried a positive anchor) and
// is fixed by seeding alone. The team-cohort half was NOT a corpus gap: a
// discovered-cohort question has no single subject anchor by construction --
// the answer IS the ranked cohort -- so its annex entry's
// oracles.anchor.positive_key is null, the corpus's expect_id is empty, and
// chaos4386TwoTurnAnswerRate's "oracle expects an answer" gate
// (ExpectedID != "") excluded every cohort case in ext65 no matter what was
// seeded. Seeding without this change would have produced corpus rows that
// can never be measured -- coverage that reads as coverage and is not.
//
// RED-FIRST (AGENTS.md verification rule 2, "observe every guard failing"):
// with the CHAOS-4525 change reverted, every subtest below fails --
// TestChaos4525AnswerRateAdmitsCohortRows because the denominator stays 0 and
// the rate reads 0 instead of 1, TestChaos4525AnnexReadsCensusTerminalExpectation
// because twoTurnOracleAnnex has no CohortAnswerExpected field to populate,
// and TestChaos4525MergeRowFieldSurvivesDecode because the field is dropped
// on decode and the recomputed rate collapses to 0.

// TestChaos4525AnswerRateAdmitsCohortRows pins the WIDENED denominator: a
// positive-arm row with NO expected id but CohortAnswerExpected set is
// eligible, and the union never loses an anchored row.
func TestChaos4525AnswerRateAdmitsCohortRows(t *testing.T) {
	t.Parallel()

	// ranked is carried on every row because the CHAOS-4525 numerator
	// requires a ranked member for the cohort class (chaos4525RowAnswered)
	// -- a cohort row that claims facts but ranked nobody is a list, not an
	// answer. These denominator subtests set it to 1 wherever they mean
	// "this row DID answer", so a denominator assertion is never silently
	// satisfied by a numerator failure instead.
	cohortRow := func(expectedID string, cohort bool, terminalStatus string, claimed, scored int) twoTurnCaseResult {
		return twoTurnCaseResult{
			Arm:                     string(twoTurnArmPositive),
			ExpectedID:              expectedID,
			CohortAnswerExpected:    cohort,
			TerminalStatus:          terminalStatus,
			ClaimedFactsCount:       claimed,
			CohortScoredMemberCount: scored,
		}
	}

	t.Run("cohort row with no expected id is eligible and can be answered", func(t *testing.T) {
		results := []twoTurnCaseResult{cohortRow("", true, "complete", 2, 3)}
		if got, want := chaos4386TwoTurnAnswerRate(results), 1.0; got != want {
			t.Errorf("answer rate = %v, want %v -- a cohort row whose oracle expects an aggregate answer must be in the denominator even with expected_id empty", got, want)
		}
	})

	t.Run("cohort row that did not answer counts against the rate", func(t *testing.T) {
		// This is the shape Run J actually observed for the team-cohort
		// bar question: the engine ranked the cohort server-side and
		// synthesis then rejected its own output (HTTP 422,
		// CHAOS-4522), so nothing was delivered. Before this change that
		// failure was invisible to the rate; it must now read 0/1, not
		// 0/0 -> 0 (a vacuous zero and a measured zero are different
		// facts).
		results := []twoTurnCaseResult{cohortRow("", true, "degraded", 0, 0)}
		if got, want := chaos4386TwoTurnAnswerRate(results), 0.0; got != want {
			t.Errorf("answer rate = %v, want %v", got, want)
		}
		// The denominator itself is what changed, and a bare 0.0 cannot
		// tell "1 eligible, 0 answered" from "0 eligible". Prove the
		// eligibility directly by pairing it with an answered row.
		paired := []twoTurnCaseResult{cohortRow("", true, "degraded", 0, 0), cohortRow("", true, "complete", 1, 2)}
		if got, want := chaos4386TwoTurnAnswerRate(paired), 0.5; got != want {
			t.Errorf("answer rate = %v, want %v -- both cohort rows must be in the denominator", got, want)
		}
	})

	t.Run("a control row is still excluded: no expected id AND no cohort expectation", func(t *testing.T) {
		// The refusal cases (ext65 index 61, census terminal_expectation
		// witnessed_no_match) and the clarification case (index 63,
		// clarification_required) must NOT be admitted -- their correct
		// terminal state is not an answer, and counting them as
		// unanswered would punish correct behavior.
		results := []twoTurnCaseResult{cohortRow("", false, "complete", 1, 0)}
		if got, want := chaos4386TwoTurnAnswerRate(results), 0.0; got != want {
			t.Errorf("answer rate = %v, want %v (control row must stay out of the denominator)", got, want)
		}
	})

	t.Run("anchored rows still qualify on expected id alone", func(t *testing.T) {
		results := []twoTurnCaseResult{cohortRow("project.v2:linear:anchored", false, "complete", 1, 0)}
		if got, want := chaos4386TwoTurnAnswerRate(results), 1.0; got != want {
			t.Errorf("answer rate = %v, want %v -- the gate is a union, never a replacement", got, want)
		}
	})

	t.Run("non-positive arms are excluded even when the cohort expectation is set", func(t *testing.T) {
		results := []twoTurnCaseResult{{
			Arm: "confirmed_wrong", CohortAnswerExpected: true, TerminalStatus: "complete", ClaimedFactsCount: 1,
		}}
		if got, want := chaos4386TwoTurnAnswerRate(results), 0.0; got != want {
			t.Errorf("answer rate = %v, want %v (arm gate is unchanged)", got, want)
		}
	})
}

// TestChaos4525AnnexReadsCensusTerminalExpectation pins which census terminal
// expectations enter CohortAnswerExpected, straight off the signed annex's
// real on-disk shape.
func TestChaos4525AnnexReadsCensusTerminalExpectation(t *testing.T) {
	t.Parallel()

	// Built by unmarshalling the SAME JSON shape the real annex file
	// carries (see .remember/trial-results/oracle-annex-v2-ext65.json,
	// cases 51/61/63) rather than by filling the Go struct literally: the
	// field this change adds is a json tag, and a struct literal would
	// pass even if the tag were wrong.
	const raw = `{
      "provenance": {"corpus_sha8": "deadbeef", "signoff": {"by": "chris", "status": "APPROVED"}},
      "cases": {
        "10": {"question_class": "cohort_assessment", "band": "paraphrase",
               "oracles": {"kind": {"positive": "team", "negatives": []},
                           "anchor": {"positive_key": null, "negatives": []},
                           "window": {"positive_band": "all_time", "negatives": []},
                           "handle": {"positive": null, "negatives": []},
                           "census": {"must_run": true, "kind": "team",
                                      "row_count_expectation": "one_or_more",
                                      "terminal_expectation": "aggregate_assessment",
                                      "commit_expectation": "never"}}},
        "11": {"question_class": "cohort_assessment", "band": "no_match",
               "oracles": {"kind": {"positive": "team", "negatives": []},
                           "anchor": {"positive_key": null, "negatives": []},
                           "window": {"positive_band": "all_time", "negatives": []},
                           "handle": {"positive": null, "negatives": []},
                           "census": {"must_run": true, "kind": "team",
                                      "row_count_expectation": "zero",
                                      "terminal_expectation": "witnessed_no_match",
                                      "commit_expectation": "never"}}},
        "12": {"question_class": "cohort_assessment", "band": "ambiguity",
               "oracles": {"kind": {"positive": "repository", "negatives": []},
                           "anchor": {"positive_key": null, "negatives": []},
                           "window": {"positive_band": "all_time", "negatives": []},
                           "handle": {"positive": null, "negatives": []},
                           "census": {"must_run": true, "kind": "repository",
                                      "row_count_expectation": "multiple_claimants",
                                      "terminal_expectation": "clarification_required",
                                      "commit_expectation": "never"}}},
        "13": {"question_class": "subject_status", "band": "literal",
               "oracles": {"kind": {"positive": "project", "negatives": []},
                           "anchor": {"positive_key": "project.v2:linear:anchored", "negatives": []},
                           "window": {"positive_band": "all_time", "negatives": []},
                           "handle": {"positive": null, "negatives": []}}}
      }}`

	var signed signedOracleAnnex
	if err := json.Unmarshal([]byte(raw), &signed); err != nil {
		t.Fatalf("unmarshal annex fixture: %v", err)
	}
	annex := adaptSignedOracleAnnex(signed)

	for _, tc := range []struct {
		index int
		want  bool
		why   string
	}{
		{10, true, "aggregate_assessment: the run is expected to deliver a ranked cohort"},
		{11, false, "witnessed_no_match: the correct terminal state is a refusal"},
		{12, false, "clarification_required: the correct terminal state is a question"},
		{13, false, "no census block at all: an anchored subject question, covered by ExpectedID"},
	} {
		if got := annex.CohortAnswerExpected[tc.index]; got != tc.want {
			t.Errorf("CohortAnswerExpected[%d] = %v, want %v (%s)", tc.index, got, tc.want, tc.why)
		}
	}
}

// TestChaos4525CohortNumeratorAcceptsDegradedRankedAnswers is the NUMERATOR
// half of CHAOS-4525, added after team-lead's 2026-08-29 review of PR #330.
//
// WHY: the denominator fix alone left the numerator carrying anchored-subject
// semantics -- terminal_status == "complete". lane-4522's first live cohort
// success against real data (org 70d529e0, PR #329) is delivered as
// status=DEGRADED with 11 claimed facts and 3 ranked teams. A complete-only
// numerator scores that 0, which would have made answer_rate a FALSE Wall-B
// tracker: it would read "still broken" for exactly the outcome the fix
// produces. "degraded" is also the honest status there under North Star check
// 12 -- some cohort members genuinely have thin evidence -- so penalising it
// inverts the check.
//
// RED-FIRST at tip 3307bc83 (the denominator-only commit): the first subtest
// below fails with "answer rate = 0, want 1".
func TestChaos4525CohortNumeratorAcceptsDegradedRankedAnswers(t *testing.T) {
	t.Parallel()

	row := func(terminal string, claimed, scored int) twoTurnCaseResult {
		return twoTurnCaseResult{
			Arm:                     string(twoTurnArmPositive),
			CohortAnswerExpected:    true,
			TerminalStatus:          terminal,
			ClaimedFactsCount:       claimed,
			CohortScoredMemberCount: scored,
		}
	}

	t.Run("the #329 live shape counts as answered: degraded, 11 claims, 3 ranked teams", func(t *testing.T) {
		if got, want := chaos4386TwoTurnAnswerRate([]twoTurnCaseResult{row("degraded", 11, 3)}), 1.0; got != want {
			t.Errorf("answer rate = %v, want %v -- a delivered ranked cohort must count even when the contract honestly reports degraded coverage", got, want)
		}
	})

	t.Run("partial and complete count too", func(t *testing.T) {
		for _, terminal := range []string{"complete", "partial", "degraded"} {
			if got, want := chaos4386TwoTurnAnswerRate([]twoTurnCaseResult{row(terminal, 1, 1)}), 1.0; got != want {
				t.Errorf("terminal=%q: answer rate = %v, want %v", terminal, got, want)
			}
		}
	})

	t.Run("a cohort with NO ranked member is not an answer, whatever the status", func(t *testing.T) {
		// The condition that keeps the loosening honest: a delivered
		// cohort object carrying only discovered, unscored members is a
		// list ("we found three teams"), never an answer to "which teams
		// are struggling, and why". RankingComputed is the contract's own
		// line between the two.
		for _, terminal := range []string{"complete", "partial", "degraded"} {
			if got, want := chaos4386TwoTurnAnswerRate([]twoTurnCaseResult{row(terminal, 11, 0)}), 0.0; got != want {
				t.Errorf("terminal=%q, scored=0: answer rate = %v, want %v", terminal, got, want)
			}
		}
	})

	t.Run("claims are still required, and the two non-delivering statuses still fail", func(t *testing.T) {
		if got := chaos4386TwoTurnAnswerRate([]twoTurnCaseResult{row("degraded", 0, 3)}); got != 0 {
			t.Errorf("answer rate = %v, want 0 (zero claimed facts is not an answer)", got)
		}
		for _, terminal := range []string{"clarification_required", "no_match"} {
			if got := chaos4386TwoTurnAnswerRate([]twoTurnCaseResult{row(terminal, 11, 3)}); got != 0 {
				t.Errorf("terminal=%q: answer rate = %v, want 0 -- these two deliver nothing", terminal, got)
			}
		}
	})

	t.Run("ANCHORED rows keep the complete-only gate, unchanged", func(t *testing.T) {
		anchored := func(terminal string) twoTurnCaseResult {
			return twoTurnCaseResult{
				Arm: string(twoTurnArmPositive), ExpectedID: "project.v2:linear:anchored",
				TerminalStatus: terminal, ClaimedFactsCount: 3,
			}
		}
		if got := chaos4386TwoTurnAnswerRate([]twoTurnCaseResult{anchored("degraded")}); got != 0 {
			t.Errorf("answer rate = %v, want 0 -- loosening the cohort class must not loosen the anchored class", got)
		}
		if got := chaos4386TwoTurnAnswerRate([]twoTurnCaseResult{anchored("complete")}); got != 1 {
			t.Errorf("answer rate = %v, want 1", got)
		}
	})
}

// TestChaos4525CohortScoredMemberCount pins the count itself against the
// contract's own RankingComputed disambiguator -- len(Members) is deliberately
// NOT the count.
func TestChaos4525CohortScoredMemberCount(t *testing.T) {
	t.Parallel()

	t.Run("nil cohort reads 0", func(t *testing.T) {
		if got := chaos4525CohortScoredMemberCount(contractsv1.ContextFabricInvestigationResult{}); got != 0 {
			t.Errorf("count = %d, want 0", got)
		}
	})

	t.Run("only scored (qualified/provisional) members count", func(t *testing.T) {
		result := contractsv1.ContextFabricInvestigationResult{
			Cohort: &contractsv1.ContextFabricCohort{
				Kind: contractsv1.ContextFabricSubjectTeam,
				Members: []contractsv1.ContextFabricCohortMember{
					{Rank: 1, RankingComputed: true, Outcome: contractsv1.ContextFabricCohortOutcomeQualified},
					{Rank: 2, RankingComputed: true, Outcome: contractsv1.ContextFabricCohortOutcomeProvisional},
					// RankingComputed is TRUE here too -- that is the whole
					// R4 finding. Unexplained, so it must not count.
					{Rank: 3, RankingComputed: true, Outcome: contractsv1.ContextFabricCohortOutcomeInsufficientEvidence},
				},
			},
		}
		if got, want := chaos4525CohortScoredMemberCount(result), 2; got != want {
			t.Errorf("count = %d, want %d (len(Members)=3 is not the answer, and neither is RankingComputed)", got, want)
		}
	})

	t.Run("a cohort delivered with no explained member reads 0, not len(Members)", func(t *testing.T) {
		result := contractsv1.ContextFabricInvestigationResult{
			Cohort: &contractsv1.ContextFabricCohort{
				Members: []contractsv1.ContextFabricCohortMember{{Rank: 1}, {Rank: 2}, {Rank: 3}},
			},
		}
		if got := chaos4525CohortScoredMemberCount(result); got != 0 {
			t.Errorf("count = %d, want 0 -- nothing was scored for this cohort", got)
		}
	})
}

// TestChaos4525RunJShapeIsNotAnAnswer is the codex-R4 P1 regression, written
// from Run J's own observed data rather than an invented shape.
//
// Run J (CHAOS-4450) recorded outcome_counts={"insufficient_evidence":2,
// "provisional":1} for the live teams cohort. RankCohort sets
// RankingComputed=true on all three of those members (cohort_ranking.go:277)
// while giving the two insufficient_evidence ones no Score, no RankingBasis
// and no Drivers (lines 403-419). The v43 bar counted RankingComputed, so a
// cohort where NOTHING was explained satisfied it as long as synthesis
// emitted any claimed fact on a partial or degraded result.
//
// AGENTS.md check 8 is the rule: "Scores help prioritize; drivers explain --
// never a bare score." A bar that accepts a cohort with neither score nor
// driver is the degenerate case of that prohibition.
//
// RED-FIRST at tip 0b9a3820: the first subtest passes (rate 1) because the
// count keyed on RankingComputed; it must now read 0.
func TestChaos4525RunJShapeIsNotAnAnswer(t *testing.T) {
	t.Parallel()

	member := func(outcome contractsv1.ContextFabricCohortMemberOutcome) contractsv1.ContextFabricCohortMember {
		// RankingComputed is true on EVERY member here on purpose: that is
		// what the production code does, and it is exactly why it cannot be
		// the predicate.
		return contractsv1.ContextFabricCohortMember{RankingComputed: true, Outcome: outcome}
	}
	resultWith := func(outcomes ...contractsv1.ContextFabricCohortMemberOutcome) contractsv1.ContextFabricInvestigationResult {
		cohort := &contractsv1.ContextFabricCohort{Kind: contractsv1.ContextFabricSubjectTeam}
		for _, o := range outcomes {
			cohort.Members = append(cohort.Members, member(o))
		}
		return contractsv1.ContextFabricInvestigationResult{Cohort: cohort}
	}
	rowFrom := func(terminal string, claimed int, result contractsv1.ContextFabricInvestigationResult) twoTurnCaseResult {
		return twoTurnCaseResult{
			Arm: string(twoTurnArmPositive), CohortAnswerExpected: true,
			TerminalStatus: terminal, ClaimedFactsCount: claimed,
			CohortScoredMemberCount: chaos4525CohortScoredMemberCount(result),
		}
	}

	t.Run("all members insufficient_evidence, one claimed fact, partial -> NOT answered", func(t *testing.T) {
		res := resultWith(
			contractsv1.ContextFabricCohortOutcomeInsufficientEvidence,
			contractsv1.ContextFabricCohortOutcomeInsufficientEvidence,
			contractsv1.ContextFabricCohortOutcomeInsufficientEvidence,
		)
		if got := chaos4525CohortScoredMemberCount(res); got != 0 {
			t.Fatalf("scored member count = %d, want 0 (RankingComputed is true on all three; none carries a Score or a Driver)", got)
		}
		row := rowFrom("partial", 1, res)
		if got, want := chaos4386TwoTurnAnswerRate([]twoTurnCaseResult{row}), 0.0; got != want {
			t.Errorf("answer rate = %v, want %v -- a cohort nobody explained is not an answer", got, want)
		}
	})

	t.Run("not_applicable members are equally unexplained", func(t *testing.T) {
		res := resultWith(contractsv1.ContextFabricCohortOutcomeNotApplicable, contractsv1.ContextFabricCohortOutcomeNotApplicable)
		if got := chaos4525CohortScoredMemberCount(res); got != 0 {
			t.Errorf("scored member count = %d, want 0", got)
		}
	})

	t.Run("Run J's exact mix counts its ONE provisional member and answers", func(t *testing.T) {
		// The same run's third member WAS provisional, so it has a Score and
		// Drivers. One explained member is a real, if thin, answer -- the bar
		// must not become so strict that the honest degraded case fails it.
		res := resultWith(
			contractsv1.ContextFabricCohortOutcomeInsufficientEvidence,
			contractsv1.ContextFabricCohortOutcomeInsufficientEvidence,
			contractsv1.ContextFabricCohortOutcomeProvisional,
		)
		if got, want := chaos4525CohortScoredMemberCount(res), 1; got != want {
			t.Fatalf("scored member count = %d, want %d", got, want)
		}
		row := rowFrom("degraded", 11, res)
		if got, want := chaos4386TwoTurnAnswerRate([]twoTurnCaseResult{row}), 1.0; got != want {
			t.Errorf("answer rate = %v, want %v (the #329 live shape: degraded, 11 claims, one explained member)", got, want)
		}
	})

	t.Run("qualified members count", func(t *testing.T) {
		res := resultWith(contractsv1.ContextFabricCohortOutcomeQualified, contractsv1.ContextFabricCohortOutcomeInsufficientEvidence)
		if got, want := chaos4525CohortScoredMemberCount(res), 1; got != want {
			t.Errorf("scored member count = %d, want %d", got, want)
		}
	})
}
