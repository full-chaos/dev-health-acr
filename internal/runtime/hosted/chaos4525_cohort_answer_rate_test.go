package hosted_test

import (
	"encoding/json"
	"testing"
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

	cohortRow := func(expectedID string, cohort bool, terminalStatus string, claimed int) twoTurnCaseResult {
		return twoTurnCaseResult{
			Arm:                  string(twoTurnArmPositive),
			ExpectedID:           expectedID,
			CohortAnswerExpected: cohort,
			TerminalStatus:       terminalStatus,
			ClaimedFactsCount:    claimed,
		}
	}

	t.Run("cohort row with no expected id is eligible and can be answered", func(t *testing.T) {
		results := []twoTurnCaseResult{cohortRow("", true, "complete", 2)}
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
		results := []twoTurnCaseResult{cohortRow("", true, "degraded", 0)}
		if got, want := chaos4386TwoTurnAnswerRate(results), 0.0; got != want {
			t.Errorf("answer rate = %v, want %v", got, want)
		}
		// The denominator itself is what changed, and a bare 0.0 cannot
		// tell "1 eligible, 0 answered" from "0 eligible". Prove the
		// eligibility directly by pairing it with an answered row.
		paired := []twoTurnCaseResult{cohortRow("", true, "degraded", 0), cohortRow("", true, "complete", 1)}
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
		results := []twoTurnCaseResult{cohortRow("", false, "complete", 1)}
		if got, want := chaos4386TwoTurnAnswerRate(results), 0.0; got != want {
			t.Errorf("answer rate = %v, want %v (control row must stay out of the denominator)", got, want)
		}
	})

	t.Run("anchored rows still qualify on expected id alone", func(t *testing.T) {
		results := []twoTurnCaseResult{cohortRow("project.v2:linear:anchored", false, "complete", 1)}
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
