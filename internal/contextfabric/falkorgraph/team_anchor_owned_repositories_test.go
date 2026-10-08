package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func repositoriesOfAnchorFrame(term string) *contextfabric.QuestionFrame {
	frame := projectsOfAnchorFrame(term)
	frame.SubjectExpression.Scoped.MemberKind = contextfabric.SubjectRepository
	return frame
}

func fulltextRepositoryRow(id, label string) row {
	return row{"node": &node{Properties: map[string]interface{}{propKind: "repository", propCanonicalID: id, propLabel: label}}, "score": 1.0}
}

// ownedRepositoryFake is a team that owns ten repositories through the
// repository's OWNED_BY_TEAM edge, beside a repository only the question text
// matches.
func ownedRepositoryFake() (*fakeConn, []string) {
	var owned []string
	for i := 0; i < 10; i++ {
		owned = append(owned, fmt.Sprintf("repository:owned-%d", i))
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return []row{fulltextRepositoryRow("repository:text-only", "full-chaos/platform-docs")}, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "team:platform" {
				return nil, nil
			}
			var rows []row
			for _, id := range owned {
				rows = append(rows, row{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: "rel_" + id}},
					"srcKind": "repository", "srcId": id, "dstKind": "team", "dstId": "team:platform",
				})
			}
			return rows, nil
		default:
			id, _ := params["id"].(string)
			if id == "team:platform" {
				return []row{fakeSubjectNodeRow("team", id, "Platform")}, nil
			}
			if strings.HasPrefix(id, "repository:owned-") {
				return []row{fakeSubjectNodeRow("repository", id, "full-chaos/"+strings.TrimPrefix(id, "repository:"))}, nil
			}
			return nil, nil
		}
	}}
	return fake, owned
}

func discoverTeamRepositories(t *testing.T, fake *fakeConn, max int) *contextfabric.Cohort {
	t.Helper()
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	request := ownershipRoutingRequest(repositoriesOfAnchorFrame("Platform"), anchor)
	request.ScopeAnchorKind = contextfabric.SubjectTeam
	request.Request.Question = "which repositories does team Platform own?"
	request.Request.Options.MaxCohortMembers = max
	result, err := newTeamAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	return result.Cohort
}

// The team's own reach is the member set: every owned repository is a member,
// and a repository only the question text matched
// is not.
func TestDiscoverContextTeamAnchorServesEveryOwnedRepositoryAndNoTextMatch(t *testing.T) {
	fake, owned := ownedRepositoryFake()
	cohort := discoverTeamRepositories(t, fake, 12)
	ids := cohortIDs(cohort)
	for _, id := range owned {
		if !ids[id] {
			t.Fatalf("members = %v, want owned repository %s", ids, id)
		}
	}
	if ids["repository:text-only"] || len(ids) != len(owned) {
		t.Fatalf("members = %v, want exactly the %d owned repositories", ids, len(owned))
	}
	for _, m := range cohort.Members {
		if len(m.InclusionReasons) != 1 || m.InclusionReasons[0] != teamAnchorRepositoryInclusionReason {
			t.Fatalf("inclusion reasons = %v, want the team-reach reason", m.InclusionReasons)
		}
	}
	if cohort.Rationale != teamAnchorRepositoryCohortRationale {
		t.Fatalf("rationale = %q", cohort.Rationale)
	}
}

// A team with no ownership path to any repository must not yield a cohort of
// whatever repository the question text matched.
func TestDiscoverContextTeamAnchorWithoutOwnedRepositoriesDoesNotServeTextMatchedRepository(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "fulltext") {
			return []row{fulltextRepositoryRow("repository:text-only", "full-chaos/platform-docs")}, nil
		}
		return nil, nil
	}}
	if cohort := discoverTeamRepositories(t, fake, 12); cohort != nil {
		t.Fatalf("Cohort = %+v, want nil: the team has no ownership path to the text-matched repository", cohort)
	}
}

// A second committed subject's own reach is not the team's.
func TestDiscoverContextTeamAnchorExcludesRepositoriesOnlyAnotherCommittedSubjectReaches(t *testing.T) {
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:other", Label: "Other"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "UNION"):
			switch params["id"] {
			case "team:platform":
				return []row{{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: "rel_owned"}},
					"srcKind": "repository", "srcId": "repository:owned", "dstKind": "team", "dstId": "team:platform",
				}}, nil
			case "project:other":
				return []row{{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "RELATES_TO", propRelationshipID: "rel_other"}},
					"srcKind": "project", "srcId": "project:other", "dstKind": "repository", "dstId": "repository:other-only",
				}}, nil
			}
			return nil, nil
		default:
			switch params["id"] {
			case "team:platform":
				return []row{fakeSubjectNodeRow("team", "team:platform", "Platform")}, nil
			case "project:other":
				return []row{fakeSubjectNodeRow("project", "project:other", "Other")}, nil
			case "repository:owned":
				return []row{fakeSubjectNodeRow("repository", "repository:owned", "full-chaos/owned")}, nil
			case "repository:other-only":
				return []row{fakeSubjectNodeRow("repository", "repository:other-only", "full-chaos/other-only")}, nil
			}
			return nil, nil
		}
	}}
	request := ownershipRoutingRequest(repositoriesOfAnchorFrame("Platform"), team)
	request.ScopeAnchorKind = contextfabric.SubjectTeam
	request.Request.Question = "which repositories does team Platform own?"
	request.Resolution.Committed = append(request.Resolution.Committed, project)
	result, err := newTeamAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	ids := cohortIDs(result.Cohort)
	if !ids["repository:owned"] || ids["repository:other-only"] || len(ids) != 1 {
		t.Fatalf("members = %v, want exactly repository:owned", ids)
	}
}
