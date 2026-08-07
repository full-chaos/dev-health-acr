package hosted

import (
	"context"
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type recordingEpisodeCreator struct {
	episode contractsv1.AgentEpisode
	called  bool
}

func (f *recordingEpisodeCreator) Create(context.Context, storage.Principal, contractsv1.AgentEpisodeCreate) (contractsv1.AgentEpisode, bool, error) {
	f.called = true
	return f.episode, false, nil
}

func TestCohortScopedEpisodeCreator_allowsOnlyConfiguredCohortOrgs(t *testing.T) {
	next := &recordingEpisodeCreator{episode: contractsv1.AgentEpisode{EpisodeID: "episode_01"}}
	creator := newCohortScopedEpisodeCreator(next, []string{"org_design_partner_1", "org_design_partner_2"})

	cohortPrincipal := storage.Principal{OrgID: "org_design_partner_1"}
	episode, duplicate, err := creator.Create(context.Background(), cohortPrincipal, contractsv1.AgentEpisodeCreate{})
	if err != nil || duplicate || episode.EpisodeID != "episode_01" || !next.called {
		t.Fatalf("cohort org create = (%#v, %t, %v), delegated=%t", episode, duplicate, err, next.called)
	}
}

func TestCohortScopedEpisodeCreator_rejectsOrgOutsideCohortWithoutDelegating(t *testing.T) {
	next := &recordingEpisodeCreator{episode: contractsv1.AgentEpisode{EpisodeID: "episode_01"}}
	creator := newCohortScopedEpisodeCreator(next, []string{"org_design_partner_1"})

	outsider := storage.Principal{OrgID: "org_not_in_cohort"}
	_, _, err := creator.Create(context.Background(), outsider, contractsv1.AgentEpisodeCreate{})
	if !errors.Is(err, ErrWritebackNotEnabledForOrg) {
		t.Fatalf("outsider create error = %v, want ErrWritebackNotEnabledForOrg", err)
	}
	if next.called {
		t.Fatal("outsider create must never reach the underlying creator")
	}
}

func TestCohortScopedEpisodeCreator_emptyCohortRejectsEveryOrg(t *testing.T) {
	next := &recordingEpisodeCreator{}
	creator := newCohortScopedEpisodeCreator(next, nil)

	_, _, err := creator.Create(context.Background(), storage.Principal{OrgID: "any_org"}, contractsv1.AgentEpisodeCreate{})
	if !errors.Is(err, ErrWritebackNotEnabledForOrg) || next.called {
		t.Fatalf("empty-cohort create = (err=%v, delegated=%t), want ErrWritebackNotEnabledForOrg without delegation", err, next.called)
	}
}
