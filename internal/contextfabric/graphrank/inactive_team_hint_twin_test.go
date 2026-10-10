package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	inactiveBare = contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	activeTwin   = contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:jira:platform", Label: "Platform"}
)

// The exact read yields nothing for an inactive team (the adapter treats it as
// absent) while the active twin is findable by name.
func inactiveTwinBackend(twinID string) *fakeGraphBackend {
	return &fakeGraphBackend{
		exactHints:    map[string]CandidateNode{},
		exactInactive: map[string]string{SubjectKey(inactiveBare): twinID},
		searchResults: map[string][]CandidateNode{"Platform": {candidateNode(activeTwin.Kind, activeTwin.CanonicalID, activeTwin.Label, 1, "*")}},
	}
}

func resolveInactiveHint(t *testing.T, backend *fakeGraphBackend, source string) contextfabric.SubjectResolution {
	t.Helper()
	request := testRequest()
	request.Question = "How is team Platform doing?"
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: inactiveBare.Kind, ID: inactiveBare.CanonicalID, Label: inactiveBare.Label, Source: source}}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("Platform"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolution
}

func TestResolveSubjectsInactiveTeamHintAnswersTeamInactiveAndNeverServesTheTwin(t *testing.T) {
	t.Parallel()
	backend := inactiveTwinBackend(activeTwin.CanonicalID)
	resolution := resolveInactiveHint(t, backend, "workbench")
	if len(resolution.Committed) != 0 || len(resolution.Candidates) != 0 {
		t.Fatalf("committed=%v candidates=%v: the active twin was served for a hint naming the inactive team", resolution.Committed, resolution.Candidates)
	}
	if len(backend.searchCalls) != 0 {
		t.Fatalf("searches = %v, want none for a hint answered team_inactive", backend.searchCalls)
	}
	want := []contractsv1.ContextFabricInactiveHint{{Subject: inactiveBare, Answer: "team_inactive", ActiveCanonicalID: activeTwin.CanonicalID}}
	if len(resolution.InactiveHints) != 1 || resolution.InactiveHints[0] != want[0] {
		t.Fatalf("inactive_hints = %#v, want %#v", resolution.InactiveHints, want)
	}
	if err := resolution.Validate(); err != nil {
		t.Fatalf("resolution invalid: %v", err)
	}
}

func TestResolveSubjectsInactiveTeamHintWithoutATwinNamesNone(t *testing.T) {
	t.Parallel()
	resolution := resolveInactiveHint(t, inactiveTwinBackend(""), "workbench")
	if len(resolution.InactiveHints) != 1 || resolution.InactiveHints[0].ActiveCanonicalID != "" || len(resolution.Committed) != 0 {
		t.Fatalf("resolution = %#v", resolution)
	}
}

// A receipt the engine minted is not the caller naming the team: it keeps the
// search it had.
func TestResolveSubjectsInactiveTeamReceiptHintKeepsItsSearch(t *testing.T) {
	t.Parallel()
	backend := inactiveTwinBackend(activeTwin.CanonicalID)
	resolution := resolveInactiveHint(t, backend, "prior_subject_receipt")
	if len(resolution.InactiveHints) != 0 || len(backend.searchCalls) == 0 {
		t.Fatalf("inactive_hints=%v searches=%v, want no hint answer and a search", resolution.InactiveHints, backend.searchCalls)
	}
}

// A second caller hint that resolves is served; the inactive one is still
// answered, and the twin is not added.
func TestResolveSubjectsInactiveTeamHintBesideAResolvedHint(t *testing.T) {
	t.Parallel()
	backend := inactiveTwinBackend(activeTwin.CanonicalID)
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	backend.exactHints[SubjectKey(project)] = candidateNode(project.Kind, project.CanonicalID, project.Label, 1, "*")
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		{Kind: inactiveBare.Kind, ID: inactiveBare.CanonicalID, Label: inactiveBare.Label, Source: "workbench"},
		{Kind: project.Kind, ID: project.CanonicalID, Label: project.Label, Source: "workbench"},
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("Platform"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != project || len(resolution.InactiveHints) != 1 {
		t.Fatalf("resolution = %#v", resolution)
	}
}

// A team the read does not report inactive (absent, or unreadable) keeps the
// ordinary search.
func TestResolveSubjectsAbsentTeamHintKeepsItsSearch(t *testing.T) {
	t.Parallel()
	backend := inactiveTwinBackend(activeTwin.CanonicalID)
	backend.exactInactive = map[string]string{}
	resolution := resolveInactiveHint(t, backend, "workbench")
	if len(resolution.InactiveHints) != 0 || len(backend.searchCalls) == 0 {
		t.Fatalf("inactive_hints=%v searches=%v", resolution.InactiveHints, backend.searchCalls)
	}
}

// Only a team hint reaches the inactive-team authority.
func TestResolveSubjectsNonTeamHintMissNeverAsksTheInactiveTeamAuthority(t *testing.T) {
	t.Parallel()
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_x", Label: "Platform"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{}, exactInactive: map[string]string{SubjectKey(project): "project_y"}}
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: project.Kind, ID: project.CanonicalID, Label: project.Label, Source: "workbench"}}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("Platform"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.InactiveHints) != 0 || len(backend.searchCalls) == 0 {
		t.Fatalf("inactive_hints=%v searches=%v", resolution.InactiveHints, backend.searchCalls)
	}
}
