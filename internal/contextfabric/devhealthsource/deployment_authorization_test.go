package devhealthsource_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A deployment carries exactly its repository's grant, so a caller who cannot
// read repository R can neither anchor on R nor list or count R's deployments,
// and a caller who can read R sees both. The projected entities feed the same
// per-node authorization the cohort discovery applies.
func TestDeploymentMembersFollowTheirRepositoryGrant(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	tables := baseTables(at)
	for i, table := range tables {
		switch table.match {
		case "FROM repos":
			tables[i] = fakeTable{match: table.match, cursorOf: repoCursorOf, rows: [][]any{
				{"repo-1", "example-org/widget-service", "synthetic", at, at, ""},
				{"repo-2", "example-org/other-service", "synthetic", at, at, ""},
			}}
		case "FROM deployments AS d":
			tables[i] = fakeTable{match: table.match, rows: [][]any{
				{"repo-1", "example-org/widget-service", "deploy-1", "success", "production", at, uint8(1), at, uint8(0), zeroTime, "v1"},
				{"repo-1", "example-org/widget-service", "deploy-2", "success", "production", at, uint8(1), at, uint8(0), zeroTime, "v2"},
				{"repo-2", "example-org/other-service", "deploy-3", "success", "production", at, uint8(1), at, uint8(0), zeroTime, "v3"},
			}}
		}
	}
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil || !available {
		t.Fatalf("batch available=%v err=%v", available, err)
	}

	repoGrant := map[string][]string{}
	var nodes []graphrank.CandidateNode
	deployments := 0
	for _, entity := range batch.Entities {
		slugs := entity.Authorization.RepositorySlugs
		switch entity.Subject.Kind {
		case contextfabric.SubjectRepository:
			repoGrant[entity.Subject.Label] = slugs
		case contextfabric.SubjectDeployment:
			deployments++
			nodes = append(nodes, graphrank.CandidateNode{
				UUID: "node-" + entity.Subject.CanonicalID, Name: entity.Subject.Label, Relevance: graphrank.Normalized(0.9),
				Attributes: map[string]interface{}{
					"canonical_id": entity.Subject.CanonicalID, "subject_kind": string(entity.Subject.Kind), "label": entity.Subject.Label,
					"evidence_refs": []string{"evidence_identity_1234"}, "authorization_repositories": slugs,
				},
			})
			repo := entity.Properties["repo"].String
			if repo == nil {
				t.Fatalf("deployment %q carries no repo property", entity.Subject.CanonicalID)
			}
			if want := []string{*repo}; !reflect.DeepEqual(slugs, want) {
				t.Fatalf("deployment %q authorization = %v, want its repository's grant %v", entity.Subject.CanonicalID, slugs, want)
			}
		}
	}
	if deployments != 3 || len(nodes) != 3 {
		t.Fatalf("deployments projected = %d, want 3", deployments)
	}
	for slug, grant := range repoGrant {
		if !reflect.DeepEqual(grant, []string{slug}) {
			t.Fatalf("repository %q grant = %v, want itself", slug, grant)
		}
	}

	kind := contextfabric.SubjectDeployment
	frame := contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:   contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"widget-service"}, MemberKind: kind},
		},
		Temporal: contextfabric.TemporalIntentCurrent, Version: contextfabric.QuestionFrameVersion,
	}
	discovery := contextfabric.GraphDiscoveryRequest{
		Interpretation: contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		Resolution:     contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
		Frame:          &frame,
	}
	discovery.Request.Options.MaxCohortMembers = 10
	noInternal := func(contextfabric.SubjectRef) bool { return false }

	for _, tc := range []struct {
		name       string
		scopes     []string
		wantMember int
	}{
		{"caller with the repository", []string{"example-org/widget-service"}, 2},
		{"caller with another repository only", []string{"example-org/other-service"}, 1},
		{"caller with an unrelated repository", []string{"unrelated/repo"}, 0},
	} {
		cohort, _, _, _, _, population := graphrank.DiscoveredCohort(storage.Principal{OrgID: "org-1", RepositoryScopes: tc.scopes}, discovery, nodes, false, noInternal)
		members := 0
		if cohort != nil {
			members = len(cohort.Members)
		}
		if members != tc.wantMember || population != tc.wantMember {
			t.Errorf("%s: members=%d population=%d, want %d for both", tc.name, members, population, tc.wantMember)
		}
	}
}
