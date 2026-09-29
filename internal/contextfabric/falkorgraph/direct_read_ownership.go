package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var _ directread.GraphAuthority = (*Adapter)(nil)

// OwnershipReachedRepositories implements the ownership side of the
// direct-read subject gate (internal/contextfabric/directread, CHAOS-7071).
// For each subject it returns the repository slugs the subject reaches
// through OWNERSHIP, read from the caller's own organization graph:
//
//   - team: the team node's own authorization_repositories list, which
//     devhealthsource.queryTeams derives from the team's CURRENT
//     team_repo_ownership rows (CHAOS-4390). The "*" wildcard (a team node
//     projected before CHAOS-4390) is NOT a reach: it proves no ownership.
//   - project: the union of those lists over every team the project's
//     CURRENT OWNED_BY_TEAM edges name (devhealthsource.queryProjectTeams,
//     from team_project_ownership). A project node's own repository list is
//     always "*" and is never read here.
//   - any other kind: no reach (the gate never asks).
//
// "Current" is the valid-time predicate at the adapter's clock, on the edge
// and on the team node, so an ended ownership never admits.
func (a *Adapter) OwnershipReachedRepositories(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return nil, fmt.Errorf("%w: authenticated organization is required", contextfabric.ErrUnavailable)
	}
	key, err := a.effectiveKey(ctx, orgID, binding)
	if err != nil {
		return nil, err
	}
	now := a.now()
	current := newTemporalFilter(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &now})

	reach := make([]map[string]struct{}, len(subjects))
	teamIndexes := map[string][]int{}
	projectIndexes := map[string][]int{}
	for index, subject := range subjects {
		reach[index] = map[string]struct{}{}
		switch subject.Kind {
		case contractsv1.ContextFabricSubjectTeam:
			teamIndexes[subject.CanonicalID] = append(teamIndexes[subject.CanonicalID], index)
		case contractsv1.ContextFabricSubjectProject:
			projectIndexes[subject.CanonicalID] = append(projectIndexes[subject.CanonicalID], index)
		}
	}

	teamCypher := fmt.Sprintf("UNWIND $ids AS id MATCH (t:%s {%s:$org, %s:$team, %s:id}) WHERE true%s RETURN id, t",
		labelSubject, propOrgID, propKind, propCanonicalID, current.predicate("t"))
	projectCypher := fmt.Sprintf("UNWIND $ids AS id MATCH (p:%s {%s:$org, %s:$project, %s:id})-[r:%s]->(t:%s {%s:$org, %s:$team}) WHERE r.%s = $owned%s%s RETURN id, t",
		labelSubject, propOrgID, propKind, propCanonicalID, labelRelation, labelSubject, propOrgID, propKind,
		propRelationType, current.predicate("r"), current.predicate("t"))

	for _, lookup := range []struct {
		cypher  string
		indexes map[string][]int
	}{{teamCypher, teamIndexes}, {projectCypher, projectIndexes}} {
		ids := make([]string, 0, len(lookup.indexes))
		for id := range lookup.indexes {
			ids = append(ids, id)
		}
		for start := 0; start < len(ids); start += storedSubjectBatch {
			end := min(start+storedSubjectBatch, len(ids))
			batch := make([]interface{}, 0, end-start)
			for _, id := range ids[start:end] {
				batch = append(batch, id)
			}
			params := current.bind(map[string]interface{}{
				"org": orgID, "ids": batch,
				"team": string(contractsv1.ContextFabricSubjectTeam), "project": string(contractsv1.ContextFabricSubjectProject),
				"owned": string(contractsv1.ContextFabricRelationshipOwnedByTeam),
			})
			rows, err := a.api.query(ctx, key, lookup.cypher, params, true)
			if err != nil {
				return nil, graphNotProjectedError(safeDependencyError("read ownership reach", err))
			}
			for _, row := range rows {
				id, _ := row["id"].(string)
				team, ok := row["t"].(*node)
				if !ok || team == nil {
					continue
				}
				repositories, isList := team.Properties[propAuthzRepos].([]string)
				if !isList {
					// "*" or absent: proves no ownership.
					continue
				}
				for _, index := range lookup.indexes[id] {
					for _, repository := range repositories {
						if repository = strings.TrimSpace(repository); repository != "" && repository != "*" {
							reach[index][repository] = struct{}{}
						}
					}
				}
			}
		}
	}

	out := make([][]string, len(subjects))
	for index, set := range reach {
		out[index] = make([]string, 0, len(set))
		for repository := range set {
			out[index] = append(out[index], repository)
		}
		sort.Strings(out[index])
	}
	return out, nil
}
