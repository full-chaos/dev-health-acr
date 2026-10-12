package contextfabric

import (
	"strings"
	"testing"
)

func TestComposeCurrentStateUsesDisplayNounsAndTwoDecimals(t *testing.T) {
	t.Parallel()
	risk := 0.993773746751932
	gap := 0.31907399881
	whole := 3.0
	basis := "pr_ref_share_partition_over_team_repo_ownership"
	subject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:a", Label: "Team A"}
	got := composeCurrentState(SynthesisDraft{ClaimedFacts: []ClaimedFact{
		{ClaimID: "c1", Kind: FactHealth, Field: "compounding_risk", Subject: subject, Value: ScalarValue{Number: &risk}},
		{ClaimID: "c2", Kind: FactReadiness, Field: "coverage_gap", Subject: subject, Value: ScalarValue{Number: &gap}},
		{ClaimID: "c3", Kind: FactWorkload, Field: "forecast_pressure", Subject: subject, Value: ScalarValue{Number: &whole}},
		{ClaimID: "c4", Kind: FactInvestment, Field: "attribution_basis", Subject: subject, Value: ScalarValue{String: &basis}},
	}})
	want := "Current observed values: compounding risk score: 0.99 (Team A); coverage gap score: 0.32 (Team A); forecast pressure score: 3 (Team A); attribution basis: share of pull request references over the team's owned repositories (Team A)."
	if got != want {
		t.Fatalf("CurrentState = %q, want %q", got, want)
	}
	for _, raw := range []string{"0.9937", "0.3190", "compounding_risk", "coverage_gap", "forecast_pressure", "pr_ref_share_partition", "health.", "="} {
		if strings.Contains(got, raw) {
			t.Fatalf("CurrentState %q still carries %q", got, raw)
		}
	}
}

func TestComposeFailedSynthesisCurrentStateUsesDisplayValues(t *testing.T) {
	t.Parallel()
	risk := 0.993773746751932
	got := composeFailedSynthesisCurrentState([]ClaimedFact{
		{ClaimID: "c1", Kind: FactHealth, Field: "compounding_risk", Subject: SubjectRef{Label: "Team A"}, Value: ScalarValue{Number: &risk}},
	})
	if want := "Canonical values read: compounding risk score: 0.99 (Team A)."; got != want {
		t.Fatalf("CurrentState = %q, want %q", got, want)
	}
}

func TestComposeCurrentStateKeepsSameFieldOfDifferentKindsApart(t *testing.T) {
	t.Parallel()
	open := "open"
	subject := SubjectRef{Label: "Team A"}
	got := composeCurrentState(SynthesisDraft{ClaimedFacts: []ClaimedFact{
		{ClaimID: "c1", Kind: FactWork, Field: "status", Subject: subject, Value: ScalarValue{String: &open}},
		{ClaimID: "c2", Kind: FactDeployments, Field: "status", Subject: subject, Value: ScalarValue{String: &open}},
	}})
	if want := "Current observed values: work status: open (Team A); deployments status: open (Team A)."; got != want {
		t.Fatalf("CurrentState = %q, want %q", got, want)
	}
}

func TestComposeFailedSynthesisCurrentStateRoundsRationaleAndWordsReasonTokens(t *testing.T) {
	t.Parallel()
	rationale := "Compounding Risk score is 0.993773746751932 (severity: high)."
	reason := "rationale_not_in_rule_sentence_form"
	subject := SubjectRef{Label: "Team A"}
	got := composeFailedSynthesisCurrentState([]ClaimedFact{
		{ClaimID: "c1", Kind: FactOperationalDeficiencies, Field: "rationale", Subject: subject, Value: ScalarValue{String: &rationale}},
		{ClaimID: "c2", Kind: FactOperationalDeficiencies, Field: "rule_basis_unavailable_reason", Subject: subject, Value: ScalarValue{String: &reason}},
	})
	for _, raw := range []string{"0.9937", "rationale_not_in_rule_sentence_form"} {
		if strings.Contains(got, raw) {
			t.Fatalf("CurrentState %q still carries %q", got, raw)
		}
	}
}

func TestComposeCurrentStateLeavesNeverInProseFieldsOut(t *testing.T) {
	t.Parallel()
	cursor := "opaque-cursor"
	title := "Fix the thing"
	subject := SubjectRef{Label: "Team A"}
	got := composeCurrentState(SynthesisDraft{ClaimedFacts: []ClaimedFact{
		{ClaimID: "c1", Kind: FactInvestment, Field: "next_cursor", Subject: subject, Value: ScalarValue{String: &cursor}},
		{ClaimID: "c2", Kind: FactWork, Field: "title", Subject: subject, Value: ScalarValue{String: &title}},
	}})
	if want := "Current observed values: title: Fix the thing (Team A)."; got != want {
		t.Fatalf("CurrentState = %q, want %q", got, want)
	}
}
