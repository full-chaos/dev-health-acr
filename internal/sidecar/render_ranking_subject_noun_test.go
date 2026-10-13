package sidecar

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestRankingRowsNameTheirOwnSubjectKind(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ kind, noun string }{
		{"team", "team"}, {"project", "project"}, {"repository", "repository"},
		{"incident", "incident"}, {"pull_request", "pull request"}, {"deployment", "deployment"},
		{"", "subject"}, {"some_future_kind", "subject"},
	} {
		id, label, kind := "subject_1", "Subject One", tc.kind
		fields := map[string]contractsv1.ContextFabricScalarValue{
			"team_canonical_id": {String: &id}, "team_label": {String: &label},
		}
		if kind != "" {
			fields["subject_kind"] = contractsv1.ContextFabricScalarValue{String: &kind}
		}
		markdown, _ := RenderAnswerProjectionMarkdown(displayValuesProjection(nil, []contractsv1.ContextFabricClaimedFactRow{{Fields: fields}}), 1<<20)
		want := "- " + tc.noun + " canonical id: subject_1; " + tc.noun + " label: Subject One"
		if !strings.Contains(markdown, want) {
			t.Errorf("kind %q: markdown lacks %q:\n%s", tc.kind, want, markdown)
		}
		if tc.noun != "team" && (strings.Contains(markdown, "team canonical id") || strings.Contains(markdown, "team label")) {
			t.Errorf("kind %q still labelled as a team:\n%s", tc.kind, markdown)
		}
		if strings.Contains(markdown, "subject kind") {
			t.Errorf("kind %q: subject_kind leaked into the Rows line:\n%s", tc.kind, markdown)
		}
	}
}

func TestEveryCohortKindHasItsOwnRankingRowNoun(t *testing.T) {
	t.Parallel()
	kinds := append(contextfabric.ServableCohortKindsForAudit(), contextfabric.ScopedOnlyCohortKindsForAudit()...)
	if len(kinds) == 0 {
		t.Fatal("no cohort kinds to audit")
	}
	for _, kind := range kinds {
		noun, ok := rankingRowSubjectNouns[kind]
		if !ok || noun == "" || noun == rankingRowNeutralNoun {
			t.Errorf("cohort kind %q has no noun of its own for the Rows line (got %q)", kind, noun)
		}
	}
}
