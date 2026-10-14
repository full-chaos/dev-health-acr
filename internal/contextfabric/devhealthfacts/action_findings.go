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
// Grant: the provider holds no per-caller rule, so the decision is the one
// read_facts takes: the subject gate (directread.SubjectGate) decides each
// owning team for the caller's principal on every call, injected as a
// TeamAuthorizer. No decision is cached and no second rule is written here.
type ActionFindingsSource struct {
	facts      clickhouseFacts
	provider   *OperationalDeficienciesProvider
	authorizer TeamAuthorizer
}

// TeamAuthorizer returns the canonical ids of the requested team subjects the
// principal may read; an error or a nil authorizer fails closed.
type TeamAuthorizer func(ctx context.Context, principal storage.Principal, teams []contextfabric.SubjectRef) (map[string]bool, error)

func NewActionFindingsSource(client contextpacket.ClickHouseQueryClient, authorizer TeamAuthorizer) *ActionFindingsSource {
	return &ActionFindingsSource{facts: clickhouseFacts{client: client}, provider: newOperationalDeficienciesProvider(client), authorizer: authorizer}
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
	statement := `SELECT DISTINCT team_id FROM ` + owned + ` AS own WHERE repo_key = {repo_id:String} ORDER BY team_id`
	rows, queryErr := s.facts.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}, {Name: "repo_id", Value: repoID}})
	if queryErr != nil {
		return contextpacket.ActionFindingsResult{}, readFailure("query repository owning teams", queryErr)
	}
	defer rows.Close()
	var requested []contextfabric.SubjectRef
	for rows.Next() {
		var teamID string
		if err := rows.Scan(&teamID); err != nil {
			return contextpacket.ActionFindingsResult{}, readFailure("scan repository owning teams", err)
		}
		requested = append(requested, contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: teamPrefix + teamID, Label: teamID})
	}
	if err := rows.Err(); err != nil {
		return contextpacket.ActionFindingsResult{}, readFailure("iterate repository owning teams", err)
	}
	if len(requested) == 0 {
		return contextpacket.ActionFindingsResult{Reason: contextpacket.ActionUnavailableNoOwner}, nil
	}
	if s.authorizer == nil {
		return contextpacket.ActionFindingsResult{Reason: contextpacket.ActionUnavailableNoGrant}, nil
	}
	admitted, authErr := s.authorizer(ctx, principal, requested)
	if authErr != nil {
		return contextpacket.ActionFindingsResult{}, authErr
	}
	subjects := make([]contextfabric.SubjectRef, 0, len(requested))
	for _, subject := range requested {
		if admitted[subject.CanonicalID] {
			subjects = append(subjects, subject)
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
	out := contextpacket.ActionFindingsResult{WithheldTeams: len(requested) - len(subjects)}
	if notEvaluated := len(subjects) - len(result.EvaluatedSubjects); notEvaluated > 0 {
		out.NotEvaluatedTeams = notEvaluated
	}
	ids := make([]string, 0, len(byTeam))
	for id := range byTeam {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out.Teams = append(out.Teams, *byTeam[id])
	}
	if len(out.Teams) == 0 && out.NotEvaluatedTeams > 0 {
		out.Reason = contextpacket.ActionUnavailableNotEvaluated
	}
	return out, nil
}

func factString(fact contextfabric.CanonicalFact, field string) string {
	if value, ok := fact.Fields[field]; ok && value.String != nil {
		return *value.String
	}
	return ""
}
