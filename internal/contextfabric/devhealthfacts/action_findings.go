package devhealthfacts

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ActionFindingsSource serves the stored operational_deficiencies findings of
// the team(s) owning a repository to the context packet. Ownership comes from
// team_repo_ownership; the findings come from the existing provider, so no
// finding is ever evaluated here.
//
// Grant: a team's findings describe every repository the team owns, so they
// are served only when the caller may read ALL of them. A caller scoped to one
// repository of a multi-repository team gets none and the closed reason.
type ActionFindingsSource struct {
	facts    clickhouseFacts
	provider *OperationalDeficienciesProvider
}

func NewActionFindingsSource(client contextpacket.ClickHouseQueryClient) *ActionFindingsSource {
	return &ActionFindingsSource{facts: clickhouseFacts{client: client}, provider: newOperationalDeficienciesProvider(client)}
}

func (s *ActionFindingsSource) ActionFindings(ctx context.Context, principal storage.Principal, repoID string) (contextpacket.ActionFindingsResult, error) {
	if s == nil || s.facts.client == nil {
		return contextpacket.ActionFindingsResult{}, errors.New("devhealthfacts: action findings source requires a ClickHouse query client")
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextpacket.ActionFindingsResult{}, err
	}
	owned := ownedRepositoriesSource(ownershipValidityPredicate(factTimeBound{}))
	statement := `SELECT team_id, repo_full_name FROM ` + owned + ` AS own
WHERE team_id IN (SELECT team_id FROM ` + owned + ` AS mine WHERE repo_key = {repo_id:String})
ORDER BY team_id, repo_full_name`
	readable := map[string]bool{}
	var teamIDs []string
	rows, queryErr := s.facts.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}, {Name: "repo_id", Value: repoID}})
	if queryErr != nil {
		return contextpacket.ActionFindingsResult{}, readFailure("query repository owning teams", queryErr)
	}
	defer rows.Close()
	for rows.Next() {
		var teamID, slug string
		if err := rows.Scan(&teamID, &slug); err != nil {
			return contextpacket.ActionFindingsResult{}, readFailure("scan repository owning teams", err)
		}
		if _, seen := readable[teamID]; !seen {
			readable[teamID] = true
			teamIDs = append(teamIDs, teamID)
		}
		if !authorizedForRepository(principal, slug) {
			readable[teamID] = false
		}
	}
	if err := rows.Err(); err != nil {
		return contextpacket.ActionFindingsResult{}, readFailure("iterate repository owning teams", err)
	}
	if len(teamIDs) == 0 {
		return contextpacket.ActionFindingsResult{Reason: contextpacket.ActionUnavailableNoOwner}, nil
	}
	subjects := make([]contextfabric.SubjectRef, 0, len(teamIDs))
	for _, teamID := range teamIDs {
		if readable[teamID] {
			subjects = append(subjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: teamPrefix + teamID, Label: teamID})
		}
	}
	if len(subjects) == 0 {
		return contextpacket.ActionFindingsResult{Reason: contextpacket.ActionUnavailableNoGrant}, nil
	}
	result, err := s.provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactOperationalDeficiencies, Subjects: subjects,
	})
	if err != nil {
		return contextpacket.ActionFindingsResult{}, err
	}
	byTeam := map[string]*contextpacket.TeamActionFindings{}
	for _, fact := range result.Facts {
		if fact.Kind != contextfabric.FactOperationalDeficiencies || len(fact.EvidenceRefIDs) == 0 {
			continue
		}
		teamID := fact.Subject.CanonicalID[len(teamPrefix):]
		team, ok := byTeam[teamID]
		if !ok {
			team = &contextpacket.TeamActionFindings{TeamID: teamID, EvidenceRefID: fact.EvidenceRefIDs[0], ObservedAt: time.Time{}}
			byTeam[teamID] = team
		}
		if fact.ObservedAt != nil && fact.ObservedAt.After(team.ObservedAt) {
			team.ObservedAt = *fact.ObservedAt
		}
		team.Findings = append(team.Findings, contextpacket.ActionFinding{
			RuleID: factString(fact, "rule_id"), Severity: factString(fact, "severity"),
			Title: factString(fact, "title"), SuccessCriterion: factString(fact, "success_criterion"),
		})
	}
	out := contextpacket.ActionFindingsResult{}
	ids := make([]string, 0, len(byTeam))
	for id := range byTeam {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out.Teams = append(out.Teams, *byTeam[id])
	}
	if len(out.Teams) == 0 {
		out.Reason = contextpacket.ActionUnavailableNoneFired
	}
	return out, nil
}

func factString(fact contextfabric.CanonicalFact, field string) string {
	if value, ok := fact.Fields[field]; ok && value.String != nil {
		return *value.String
	}
	return ""
}
