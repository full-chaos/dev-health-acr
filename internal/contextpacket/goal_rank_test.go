package contextpacket_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestAssemblerRanksGoalMatchBeforeRecency(t *testing.T) {
	request := fixtureRequest("req-goal-rank", "main", "")
	request.Goal = "I am about to change the deploy pipeline. What should I know first?"
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	unrelated := testEvidence("ev-unrelated-newer", "git", now)
	unrelated.Source.DisplayLabel = "fix typo in readme"
	deploy := testEvidence("ev-deploy-older", "git", now.Add(-72*time.Hour))
	deploy.Source.DisplayLabel = "rework deploy pipeline stages"
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{unrelated, deploy},
	}}
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if got := itemEvidenceIDs(packet); got[0] != "ev-deploy-older" {
		t.Fatalf("served order = %v, want goal match first", got)
	}
}

func TestAssemblerGuidanceNamesTheItemsItRestsOn(t *testing.T) {
	request := fixtureRequest("req-guidance-names", "main", "")
	request.Goal = "change the deploy pipeline"
	deploy := testEvidence("ev-deploy-run", "ci", time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC))
	deploy.Source.DisplayLabel = "deploy pipeline run 4821"
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{deploy},
	}}
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(packet.RecommendedNextSteps) != 1 || !strings.Contains(packet.RecommendedNextSteps[0].Label, `"deploy pipeline run 4821"`) || !strings.Contains(packet.RequiredChecks[0].Label, `"deploy pipeline run 4821"`) {
		t.Fatalf("guidance does not name its item: %#v %#v", packet.RequiredChecks, packet.RecommendedNextSteps)
	}
}

func TestAssemblerSummaryReadsABareGraphTokenAsWords(t *testing.T) {
	request := fixtureRequest("req-graph-summary", "main", "")
	edge := testEvidence("ev-graph-edge", "dev_health", time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC))
	edge.Source.EntityType = "work_graph_edge"
	edge.Citation = "linear_attachment"
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{edge},
	}}
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if got := packet.Items[0].Summary; got != "linked via linear attachment" {
		t.Fatalf("summary = %q", got)
	}
}

func TestAssemblerBudgetKeepsTheBestGoalMatchAcrossCategories(t *testing.T) {
	request := fixtureRequest("req-goal-budget", "main", "")
	request.Goal = "change the deploy pipeline"
	request.Options.MaxItems = 1
	observed := time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC)
	state := testEvidence("ev-state-unrelated", "dev_health", observed)
	state.Source.EntityType = "deployment"
	state.Source.DisplayLabel = "release of the billing service"
	cause := testEvidence("ev-cause-deploy", "git", observed.Add(-48*time.Hour))
	cause.Source.DisplayLabel = "rework deploy pipeline stages"
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{state, cause},
	}}
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if got := itemEvidenceIDs(packet); len(got) != 1 || got[0] != "ev-cause-deploy" {
		t.Fatalf("served items = %v, want the goal match to survive the budget", got)
	}
}

func TestAssemblerGoalMatchesWholeWordsNotSubstrings(t *testing.T) {
	request := fixtureRequest("req-goal-words", "main", "")
	request.Goal = "api api api"
	observed := time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC)
	rapid := testEvidence("ev-rapid", "git", observed)
	rapid.Source.DisplayLabel = "rapid rapid rapid prototype"
	api := testEvidence("ev-api-gateway", "git", observed.Add(-48*time.Hour))
	api.Source.DisplayLabel = "api gateway"
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{rapid, api},
	}}
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if got := itemEvidenceIDs(packet); got[0] != "ev-api-gateway" {
		t.Fatalf("served order = %v, want the whole-word match first", got)
	}
}
