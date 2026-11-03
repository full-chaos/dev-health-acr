package contextfabric

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12716 (codex r2, executed): an answer saved before the org-scope rule
// holds the reader form the base rule gave an org-scoped project id: the graph read
// labelled the node with it and the repair named the fact's end with it. For an org
// UUID that starts with a letter the base rule read the UUID as a provider token, so
// "c67b…:gitlab:71133891" read "gitlab:71133891". The oracle is that literal text.
// A served answer restates it, and it is never taken for the project's name.
func TestAStoredReaderFormFromBeforeTheOrgScopeRuleIsRestated(t *testing.T) {
	t.Parallel()
	const raw = "c67b1602-31db-4422-8dec-a4a02bbcc513:gitlab:71133891"
	const stale = "gitlab:71133891"
	projectID, omitted, err := identity.Derive(identity.KindProject, []string{"gitlab", raw}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project: %v %v", omitted, err)
	}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: projectID, Label: stale}
	if got := SubjectReadableLabel(project); got != "71133891" {
		t.Errorf("node label %q served %q, want 71133891", stale, got)
	}
	itemID, _, err := identity.Derive(identity.KindWorkItem, []string{"00000000-0000-0000-0000-000000000000", "linear:CHAOS-2320"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	item := SubjectRef{Kind: SubjectWorkItem, CanonicalID: itemID, Label: "Generate taxonomy table"}
	for stored, want := range map[string]string{
		"CHAOS-2320 belongs to project " + stale:        "CHAOS-2320 belongs to project 71133891",
		"linear:CHAOS-2320 belongs to project " + raw:   "CHAOS-2320 belongs to project 71133891",
		"CHAOS-2320 belongs to project gitlab:71133892": "CHAOS-2320 belongs to project gitlab:71133892",
	} {
		if got := RepairRawEdgeFact(stored, "BELONGS_TO_PROJECT", item, project); got != want {
			t.Errorf("fact %q served %q, want %q", stored, got, want)
		}
	}
	// An end the answer carries with no label: the stale form is still read as
	// the raw id's (a fact end form, not the label).
	unlabelled := project
	unlabelled.Label = ""
	if got := RepairRawEdgeFact("CHAOS-2320 belongs to project "+stale, "BELONGS_TO_PROJECT", item, unlabelled); got != "CHAOS-2320 belongs to project 71133891" {
		t.Errorf("unlabelled end: fact served %q", got)
	}
	// A graph driver's title is composed from the To label (codex r3 opinion).
	for title, want := range map[string]string{
		"Belongs To Project: " + stale:       "Belongs To Project: 71133891",
		"Belongs To Project: " + raw:         "Belongs To Project: " + raw,
		"Belongs To Project: Payments":       "Belongs To Project: Payments",
		"Owned By Team: " + stale:            "Owned By Team: " + stale,
		"Belongs To Project: gitlab:7113389": "Belongs To Project: gitlab:7113389",
	} {
		if got := readerDriverTitle(title, "BELONGS_TO_PROJECT", project); got != want {
			t.Errorf("driver title %q served %q, want %q", title, got, want)
		}
	}
	var result InvestigationResult
	result.SubjectResolution.Committed = []SubjectRef{project}
	if got, _ := contractsv1.ContextFabricEvidenceRefLabelNamed("acr:v1:project:gitlab:"+raw, evidenceSubjectNames(result)); got != "Project: 71133891" {
		t.Errorf("evidence label %q, want Project: 71133891 (the stale form is no name)", got)
	}
	// A real name stands.
	result.SubjectResolution.Committed = []SubjectRef{{Kind: SubjectProject, CanonicalID: projectID, Label: "Payments"}}
	if got, _ := contractsv1.ContextFabricEvidenceRefLabelNamed("acr:v1:project:gitlab:"+raw, evidenceSubjectNames(result)); got != "Project: Payments" {
		t.Errorf("named evidence label %q, want Project: Payments", got)
	}
	named := SubjectRef{Kind: SubjectProject, CanonicalID: projectID, Label: "Payments"}
	if got := SubjectReadableLabel(named); got != "Payments" {
		t.Errorf("a named node read %q", got)
	}
}
