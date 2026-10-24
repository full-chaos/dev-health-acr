package hosted_test

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestTwoTurnNotApplicableReason(t *testing.T) {
	expected := trialCase{ExpectKind: "repository", ExpectID: "repository:r1"}
	committedExpected := []contractsv1.ContextFabricSubjectRef{{Kind: "repository", CanonicalID: "repository:r1"}}
	committedOther := []contractsv1.ContextFabricSubjectRef{{Kind: "repository", CanonicalID: "repository:other"}}
	cases := []struct {
		name      string
		facts     twoTurnTurn1Facts
		committed []contractsv1.ContextFabricSubjectRef
		member    string
		want      string
	}{
		{"identity commit of the expected subject (trial case 22)", twoTurnTurn1Facts{CommitGate: "identity_fast_path", KindOfferDistinctKindCount: 1, KindOfferSuppressedByCardinality: true}, committedExpected, "expected_kind", twoTurnNotApplicableIdentityCommit},
		{"identity commit, window member", twoTurnTurn1Facts{CommitGate: "identity_fast_path"}, committedExpected, "window", twoTurnNotApplicableIdentityCommit},
		{"identity commit of a WRONG subject stays scored", twoTurnTurn1Facts{CommitGate: "identity_fast_path"}, committedOther, "window", ""},
		{"identity gate with nothing committed stays scored", twoTurnTurn1Facts{CommitGate: "identity_fast_path"}, nil, "window", ""},
		{"another gate committing the expected subject stays scored", twoTurnTurn1Facts{CommitGate: "exact_index"}, committedExpected, "window", ""},
		{"kind offer suppressed to a single kind", twoTurnTurn1Facts{KindOfferDistinctKindCount: 1, KindOfferSuppressedByCardinality: true}, nil, "expected_kind", twoTurnNotApplicableKindOfferSingle},
		{"zero-kind pool is a retrieval miss, still scored", twoTurnTurn1Facts{KindOfferDistinctKindCount: 0, KindOfferSuppressedByCardinality: true}, nil, "expected_kind", ""},
		{"suppressed but two kinds remain", twoTurnTurn1Facts{KindOfferDistinctKindCount: 2, KindOfferSuppressedByCardinality: true}, nil, "expected_kind", ""},
		{"single kind but the offer was not suppressed", twoTurnTurn1Facts{KindOfferDistinctKindCount: 1}, nil, "expected_kind", ""},
		{"suppression only exempts expected_kind", twoTurnTurn1Facts{KindOfferDistinctKindCount: 1, KindOfferSuppressedByCardinality: true}, nil, "window", ""},
		{"a genuine miss: no commit, two kinds offered", twoTurnTurn1Facts{KindOfferDistinctKindCount: 2}, nil, "expected_kind", ""},
	}
	for _, c := range cases {
		if got := twoTurnNotApplicableReason(c.facts, c.committed, expected, c.member); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTwoTurnNotApplicableRowIsNeverAMiss(t *testing.T) {
	tc := trialCase{ExpectKind: "repository", ExpectID: "repository:r1"}
	row := twoTurnNotApplicableRow(22, "expected_kind", tc, contractsv1.ContextFabricInvestigationResult{}, nil, twoTurnTurn1Facts{CommitGate: "identity_fast_path"}, twoTurnNotApplicableIdentityCommit)
	if row.NotApplicableReason != twoTurnNotApplicableIdentityCommit {
		t.Errorf("NotApplicableReason = %q", row.NotApplicableReason)
	}
	if row.OfferMiss || row.PairInvalid || row.FalseNoMatch || row.WrongCommit || row.ArmInvalidReason != "" {
		t.Errorf("a not-applicable row must not read as offer_miss/pair_invalid/false_no_match/wrong_commit/arm_invalid: %+v", row)
	}
	if row.Turn1CommitGate != "identity_fast_path" {
		t.Errorf("turn-1 facts not stamped: %q", row.Turn1CommitGate)
	}
}

func TestTwoTurnNotApplicableSummaryNamesArmsReasonsAndDenominator(t *testing.T) {
	got := twoTurnNotApplicableSummary(map[string]int{"expected_kind/" + twoTurnNotApplicableIdentityCommit: 2, "window/" + twoTurnNotApplicableIdentityCommit: 1}, 20)
	for _, want := range []string{
		"n_not_applicable=3 of cases_run=20 (offer-arm denominator 17)",
		"arm=positive expected_kind/" + twoTurnNotApplicableIdentityCommit + " n_not_applicable=2",
		"arm=inferred_tier expected_kind/" + twoTurnNotApplicableIdentityCommit + " n_not_applicable=2",
		"arm=positive window/" + twoTurnNotApplicableIdentityCommit + " n_not_applicable=1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q lacks %q", got, want)
		}
	}
	if none := twoTurnNotApplicableSummary(nil, 5); !strings.Contains(none, "n_not_applicable=0 of cases_run=5 (offer-arm denominator 5)") {
		t.Errorf("empty summary = %q", none)
	}
}
