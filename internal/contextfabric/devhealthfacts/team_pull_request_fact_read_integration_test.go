package devhealthfacts_test

import (
	"bytes"
	"context"
	"encoding/json"
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
// answer a fact for each, and the real registry merges them.
//
// The expander mints those targets with no label, so the registry refuses
// every fact the producers return for them. That refusal must cost the two
// kinds and nothing else: the read returns, and coverage names both. When the
// expander mints a label these two kinds are served, and the expectations
// below change with it.
func TestTeamPullRequestFactsThroughTheRealProducersDegradeAndDoNotEndTheRead(t *testing.T) {
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
		t.Fatalf("ReadFacts error = %q, want nil: a team's pull request read must never end the investigation", err)
	}
	reached := map[contextfabric.SubjectKind]int{}
	for _, derivation := range bundle.Scope.Derivations {
		reached[derivation.Target.Kind]++
	}
	if reached[contextfabric.SubjectPullRequest] != 3 || reached[contextfabric.SubjectKind("pull_request_review")] != 1 {
		t.Fatalf("expansion reached %v, want the fixture's 3 pull requests and 1 review: the producers were not exercised", reached)
	}
	states := map[string]contextfabric.SourceObservation{}
	for _, source := range bundle.Coverage.Sources {
		states[source.Source] = source
	}
	for _, kind := range []contextfabric.FactKind{contextfabric.FactPullRequests, contextfabric.FactReviews} {
		source, ok := states["canonical_fact:"+string(kind)]
		if !ok {
			t.Fatalf("coverage has no %s source: %+v", kind, bundle.Coverage.Sources)
		}
		if source.State != contextfabric.SourceUnavailable || !strings.Contains(source.Reason, "canonical fact provider returned a result that was rejected") {
			t.Fatalf("%s coverage = %+v, want unavailable with the rejection sentence", kind, source)
		}
	}
	if len(bundle.Facts) != 0 {
		t.Fatalf("bundle carries %d facts from refused results, want 0", len(bundle.Facts))
	}
	if err := bundle.Coverage.Validate(); err != nil {
		t.Fatalf("the degraded coverage is not contract-valid: %v", err)
	}
	causes := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		entry := map[string]any{}
		if json.Unmarshal([]byte(line), &entry) != nil || entry["msg"] != "context fabric fact result rejected" {
			continue
		}
		causes[entry["kind"].(string)], _ = entry["rejection_cause"].(string)
	}
	if causes["pull_requests"] != "fact_subject_invalid" || causes["reviews"] != "fact_subject_invalid" {
		t.Fatalf("rejection causes = %v, want fact_subject_invalid for pull_requests and reviews", causes)
	}
}
