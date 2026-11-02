package contextfabric_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

func workItemRef(t *testing.T, repoID, raw string) contextfabric.SubjectRef {
	t.Helper()
	id, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, raw}, nil)
	if err != nil || omitted {
		t.Fatalf("derive %s: %v %v", raw, omitted, err)
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id, Label: raw}
}

const zeroRepo = "00000000-0000-0000-0000-000000000000"

// CHAOS-12619: a raw source id reads as a reader names the subject, for every
// provider, read back from the canonical id alone.
func TestARawSourceIDReadsAsItsKey(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		"linear:CHAOS-2320":                   "CHAOS-2320",
		"jira:OPS-11":                         "OPS-11",
		"gh:full-chaos/dev-health-ops#5035":   "full-chaos/dev-health-ops#5035",
		"ghpr:full-chaos/dev-health-ops#5063": "full-chaos/dev-health-ops#5063",
		"gitlab:group/project#9":              "group/project#9",
		"gitlab:group/project!12":             "group/project!12",
	} {
		ref := workItemRef(t, zeroRepo, raw)
		if got, ok := contextfabric.RawSubjectLabel(ref.Kind, ref.CanonicalID); !ok || got != raw {
			t.Errorf("%s: raw = %q %v", raw, got, ok)
		}
		if got := contextfabric.SubjectReadableLabel(ref); got != want {
			t.Errorf("%s: label = %q, want %q", raw, got, want)
		}
	}
	refID, omitted := identity.DeriveWorkItemRef("extkey:CHAOS-11405", nil)
	if omitted {
		t.Fatal("ref omitted")
	}
	stub := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItemRef, CanonicalID: refID, Label: "extkey:CHAOS-11405"}
	if got := contextfabric.SubjectReadableLabel(stub); got != "CHAOS-11405" {
		t.Errorf("work item ref = %q", got)
	}
	project, _, _ := identity.Derive(identity.KindProject, []string{"linear", "c67b1602-31db-4422-8dec-a4a02bbcc513"}, nil)
	if got := contextfabric.SubjectReadableLabel(contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: project, Label: "c67b1602-31db-4422-8dec-a4a02bbcc513"}); got != "project" {
		t.Errorf("project stub = %q, want the kind word", got)
	}
	team := contextfabric.TeamCanonicalID("linear:CHAOS")
	if got := contextfabric.SubjectReadableLabel(contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: team, Label: "linear:CHAOS"}); got != "CHAOS" {
		t.Errorf("team stub = %q, want CHAOS", got)
	}
}

// A label that is not the subject's raw id stands, whatever it looks like: a
// title, a name, and text shaped like another subject's id.
func TestALabelThatIsNotTheRawIDStands(t *testing.T) {
	t.Parallel()
	ref := workItemRef(t, zeroRepo, "linear:CHAOS-2320")
	for _, label := range []string{"Generate taxonomy table", "linear:CHAOS-9999", "CHAOS-2320", ""} {
		ref.Label = label
		if got := contextfabric.SubjectReadableLabel(ref); got != label {
			t.Errorf("label %q read as %q", label, got)
		}
	}
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: contextfabric.RepositoryCanonicalID("920f9442-07df-4217-4dc4-c5833c0b8268"), Label: "920f9442-07df-4217-4dc4-c5833c0b8268"}
	if got := contextfabric.SubjectReadableLabel(repo); got != repo.Label {
		t.Errorf("a repository is not this change's: %q", got)
	}
}

// A fact's raw ends read as the reader names them; each end is matched by its
// endpoint's own raw id, never by text that merely looks like one.
func TestARawEdgeFactReadsInReaderWords(t *testing.T) {
	t.Parallel()
	item := workItemRef(t, zeroRepo, "linear:CHAOS-2320")
	item.Label = "Generate taxonomy table"
	project, _, _ := identity.Derive(identity.KindProject, []string{"linear", "c67b1602-31db-4422-8dec-a4a02bbcc513"}, nil)
	named := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: project, Label: "Investment View Documentation"}
	stub := named
	stub.Label = "c67b1602-31db-4422-8dec-a4a02bbcc513"
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: contextfabric.TeamCanonicalID("linear:CHAOS"), Label: "Fullchaos"}
	pr := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:920f9442-07df-4217-4dc4-c5833c0b8268:872", Label: "PR #872"}
	other := workItemRef(t, zeroRepo, "jira:OPS-11")
	for _, tc := range []struct {
		fact     string
		from, to contextfabric.SubjectRef
		want     string
	}{
		{"linear:CHAOS-2320 links pull request PR #872", item, pr, "CHAOS-2320 links pull request PR #872"},
		{"linear:CHAOS-2320 belongs to project c67b1602-31db-4422-8dec-a4a02bbcc513", item, named, "CHAOS-2320 belongs to project Investment View Documentation"},
		{"linear:CHAOS-2320 belongs to project c67b1602-31db-4422-8dec-a4a02bbcc513", item, stub, "CHAOS-2320 belongs to project"},
		{"linear:CHAOS-2320 owned by team linear:CHAOS", item, team, "CHAOS-2320 owned by team Fullchaos"},
		{"jira:OPS-11 blocks linear:CHAOS-2320", other, item, "OPS-11 blocks CHAOS-2320"},
		// Already reader words: the writer after CHAOS-12619.
		{"CHAOS-2320 links pull request PR #872", item, pr, "CHAOS-2320 links pull request PR #872"},
		// Text shaped like an id that is not this endpoint's stays.
		{"linear:CHAOS-9999 links pull request PR #872", item, pr, "linear:CHAOS-9999 links pull request PR #872"},
		{"a title that says linear:CHAOS-2320", pr, item, "a title that says CHAOS-2320"},
	} {
		if got := contextfabric.RepairRawEdgeFact(tc.fact, tc.from, tc.to); got != tc.want {
			t.Errorf("%q = %q, want %q", tc.fact, got, tc.want)
		}
	}
}
