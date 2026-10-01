package devhealthfacts_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The production chain end to end against a real ClickHouse: the real scope
// expander reaches a team's pull requests and reviews, the real producers
// answer a fact for each, and the real registry merges them. A team question
// is served its pull request and review facts.
//
// Every subject the expander mints must pass the registry's own subject
// check, or the registry refuses every fact the producers return for it.
func TestTeamPullRequestAndReviewFactsAreServedThroughTheRealProducers(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	seedChaos4101TeamFixture(t, ctx, direct, time.Now().UTC())

	logs := &bytes.Buffer{}
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{
		ScopeExpander: devhealthfacts.NewScopeExpander(query),
		Logger:        slog.New(slog.NewJSONHandler(logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	bundle, err := registry.ReadFacts(ctx, storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{chaos4101RepoASlug, chaos4101RepoBSlug}}, contextfabric.CanonicalFactRequest{
		Question:     contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		Subjects:     []contextfabric.SubjectRef{chaos4101TeamSubject()},
		Requirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactPullRequests}, {Kind: contextfabric.FactReviews}},
	})

	if err != nil {
		t.Fatalf("ReadFacts error = %q, want nil", err)
	}
	wantLabels := map[string]string{
		"pull_request:" + chaos4101RepoAID + ":1":                      "PR #1",
		"pull_request:" + chaos4101RepoAID + ":2":                      "PR #2",
		"pull_request:" + chaos4101RepoBID + ":3":                      "PR #3",
		"pull_request_review.v2:" + chaos4101RepoAID + ":1:review-a-1": "PR #1 review",
	}
	if len(bundle.Scope.Derivations) != len(wantLabels) {
		t.Fatalf("derivations = %d, want %d: the fixture's 3 pull requests and 1 review", len(bundle.Scope.Derivations), len(wantLabels))
	}
	for _, derivation := range bundle.Scope.Derivations {
		target := derivation.Target
		if err := target.Validate(); err != nil {
			t.Fatalf("expansion target %+v is not a valid subject: %v", target, err)
		}
		if want, ok := wantLabels[target.CanonicalID]; !ok || target.Label != want {
			t.Fatalf("expansion target %+v, want label %q", target, want)
		}
	}
	wantStates := map[string]string{
		"pull_request:" + chaos4101RepoAID + ":1":                      "open",
		"pull_request:" + chaos4101RepoAID + ":2":                      "merged",
		"pull_request:" + chaos4101RepoBID + ":3":                      "open",
		"pull_request_review.v2:" + chaos4101RepoAID + ":1:review-a-1": "approved",
	}
	served := map[string]string{}
	for _, fact := range bundle.Facts {
		state := fact.Fields["state"].String
		if state == nil {
			t.Fatalf("fact %+v carries no state", fact)
		}
		served[fact.Subject.CanonicalID] = *state
		if fact.Subject.Label != wantLabels[fact.Subject.CanonicalID] {
			t.Fatalf("served fact subject %+v, want label %q", fact.Subject, wantLabels[fact.Subject.CanonicalID])
		}
		if len(fact.EvidenceRefIDs) == 0 {
			t.Fatalf("served fact for %s carries no evidence", fact.Subject.CanonicalID)
		}
	}
	if len(served) != len(wantStates) {
		t.Fatalf("served facts = %v, want %v", served, wantStates)
	}
	for id, want := range wantStates {
		if served[id] != want {
			t.Fatalf("served state for %s = %q, want %q (all served: %v)", id, served[id], want, served)
		}
	}
	for _, source := range bundle.Coverage.Sources {
		if source.State != contextfabric.SourceAvailable {
			t.Fatalf("coverage %+v, want every kind available", source)
		}
	}
	if len(bundle.Coverage.Sources) != 2 || bundle.Coverage.Partial {
		t.Fatalf("coverage = %+v, want two available kinds and no partial flag", bundle.Coverage)
	}
	if strings.Contains(logs.String(), "context fabric fact result rejected") {
		t.Fatalf("a served read logged a rejected result:\n%s", logs.String())
	}
}
