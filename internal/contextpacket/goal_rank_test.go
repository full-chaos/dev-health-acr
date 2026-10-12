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
