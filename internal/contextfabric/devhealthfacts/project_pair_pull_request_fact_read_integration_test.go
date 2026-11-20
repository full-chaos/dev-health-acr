package devhealthfacts_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A comparison of two projects reads their pull requests in one fact read,
// through the real scope expander, producers and registry. The project path
// reaches pull requests through the same expander function as the team path,
// so a pull request subject minted without a label refuses the whole kind
// here too. A pull request linked from an issue of each project is served once
// and counts in both projects' rollups.
func TestTwoProjectPullRequestFactsAreServedThroughTheRealProducers(t *testing.T) {
	for _, pullRequestFacts := range []bool{false, true} {
		t.Run(fmt.Sprintf("pull_request_facts=%t", pullRequestFacts), func(t *testing.T) {
			ctx := context.Background()
			query, direct := newCHAOS3780IntegrationClient(t, ctx)
			s := newProjectRollupSeed(t, ctx, direct)
			s.exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
				"proj-r", projectRollupOrg, "linear", nil, "proj-r", uint8(1), "active", "", s.at)
			s.workItem("i-p1", "repo-a", "issue", "open", "proj-p")
			s.workItem("i-p2", "repo-a", "issue", "open", "proj-p")
			s.workItem("i-r1", "repo-a", "issue", "open", "proj-r")
			s.workItem("i-r2", "repo-a", "issue", "open", "proj-r")
			for number := uint32(1); number <= 3; number++ {
				s.pullRequest("repo-a", number, s.at, nil, nil)
			}
			s.link("repo-a", "i-p1", 1, "native")
			s.link("repo-a", "i-r1", 2, "native")
			s.link("repo-a", "i-p2", 3, "native")
			s.link("repo-a", "i-r2", 3, "native")

			logs := &bytes.Buffer{}
			registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{
				ScopeExpander: devhealthfacts.NewScopeExpander(query),
				Logger:        slog.New(slog.NewJSONHandler(logs, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			projectP, projectR := projectRollupRef(t, "proj-p"), projectRollupRef(t, "proj-r")
			bundle, err := registry.ReadFacts(ctx, storage.Principal{OrgID: projectRollupOrg, RepositoryScopes: []string{"*"}}, contextfabric.CanonicalFactRequest{
				Question:         contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
				Subjects:         []contextfabric.SubjectRef{projectP, projectR},
				Requirements:     []contextfabric.FactRequirement{{Kind: contextfabric.FactPullRequests}},
				PullRequestFacts: pullRequestFacts,
			})
			if err != nil {
				t.Fatalf("ReadFacts error = %q, want nil", err)
			}
			if strings.Contains(logs.String(), "context fabric fact result rejected") {
				t.Fatalf("the pull request result was rejected:\n%s", logs.String())
			}
			var pullRequestSource *contextfabric.SourceObservation
			for index, source := range bundle.Coverage.Sources {
				if source.Source == "canonical_fact:pull_requests" {
					pullRequestSource = &bundle.Coverage.Sources[index]
				}
			}
			if pullRequestSource == nil || pullRequestSource.State != contextfabric.SourceAvailable {
				t.Fatalf("canonical_fact:pull_requests coverage = %+v, want available (sources %+v)", pullRequestSource, bundle.Coverage.Sources)
			}

			wantLabels := map[string]string{}
			for number := 1; number <= 3; number++ {
				wantLabels[fmt.Sprintf("pull_request:%s:%d", repoUUID("repo-a"), number)] = fmt.Sprintf("PR #%d", number)
			}
			// Both projects reach repo-a, so each reaches all 3 of its pull
			// requests by the repository proxy; only a pull request linked from
			// one of the project's issues carries that link's tier.
			pair := func(project contextfabric.SubjectRef, number int) string {
				return fmt.Sprintf("%s|pull_request:%s:%d", project.CanonicalID, repoUUID("repo-a"), number)
			}
			wantTiers := map[string]string{
				pair(projectP, 1): "native", pair(projectP, 2): "", pair(projectP, 3): "native",
				pair(projectR, 1): "", pair(projectR, 2): "native", pair(projectR, 3): "native",
			}
			gotTiers := map[string]string{}
			for _, derivation := range bundle.Scope.Derivations {
				target := derivation.Target
				if err := target.Validate(); err != nil {
					t.Fatalf("expansion target %+v is not a valid subject: %v", target, err)
				}
				if want, ok := wantLabels[target.CanonicalID]; !ok || target.Label != want {
					t.Fatalf("expansion target %+v, want one of the 3 pull requests with its label", target)
				}
				key := derivation.Root.CanonicalID + "|" + target.CanonicalID
				if _, dup := gotTiers[key]; dup {
					t.Fatalf("derivation %s appears twice: %+v", key, bundle.Scope.Derivations)
				}
				gotTiers[key] = derivation.LinkTier
			}
			if fmt.Sprint(gotTiers) != fmt.Sprint(wantTiers) {
				t.Fatalf("derivation link tiers = %v, want %v", gotTiers, wantTiers)
			}

			served := map[string]int{}
			var rollups contextfabric.FactProviderResult
			for _, fact := range bundle.Facts {
				switch fact.Subject.Kind {
				case contextfabric.SubjectPullRequest:
					served[fact.Subject.CanonicalID]++
				case contextfabric.SubjectProject:
					rollups.Facts = append(rollups.Facts, fact)
				}
			}
			if len(served) != len(wantLabels) {
				t.Fatalf("pull request facts = %v, want each of %v once", served, wantLabels)
			}
			for id := range wantLabels {
				if served[id] != 1 {
					t.Fatalf("pull request %s served %d times, want once: %v", id, served[id], served)
				}
			}
			if len(rollups.Facts) != 2 {
				t.Fatalf("project rollups = %d, want one per project: %+v", len(rollups.Facts), rollups.Facts)
			}
			wantInt(t, factFor(t, rollups, projectP).Fields, "pull_requests_open_current", 2)
			wantInt(t, factFor(t, rollups, projectR).Fields, "pull_requests_open_current", 2)
		})
	}
}
