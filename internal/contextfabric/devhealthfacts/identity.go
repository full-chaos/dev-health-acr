package devhealthfacts

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// IdentityProvider implements contextfabric.FactProvider for FactIdentity,
// reading repository identity from repos and work item identity from
// work_items -- the same two tables and columns
// devhealthsource/tables.go's queryRepositories and queryWorkItems already
// read.
type IdentityProvider struct {
	facts clickhouseFacts
}

func newIdentityProvider(client contextpacket.ClickHouseQueryClient) *IdentityProvider {
	return &IdentityProvider{facts: clickhouseFacts{client: client}}
}

func (p *IdentityProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactIdentity, "devhealthfacts.identity", []contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectWorkItem})
}

func (p *IdentityProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	if refused, unsupported := refuseHistoricalFact(query); unsupported {
		return refused, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	facts := make([]contextfabric.CanonicalFact, 0, len(query.Subjects))
	// CHAOS-5438 r2 P1: ONE owner for the shared output bound AND the
	// truncation verdict across BOTH branches -- see factBudget. A local
	// `truncated` flag beside a hand-written `len(facts) >=` guard is exactly
	// how the two drifted apart.
	budget := newFactBudget()
	rejectedCount := 0
	ownershipUnread, ownersCut := 0, 0
	var cutRepositories []contextfabric.SubjectRef
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.identity", contextfabric.FactIdentity, rejectedCount)
		}
	}()

	repoIDs, repoBySubject, repoRejected := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectRepository), "repository:")
	rejectedCount += repoRejected
	if len(repoIDs) > 0 {
		// CHAOS-4377: the SQL build + scan half moved to
		// github.com/full-chaos/dev-health-go/readers.ReadRepositoryIdentity.
		// CHAOS-5474: the repository branch probes too, and it MUST -- this
		// provider ORs both branches into ONE result-level `truncated`, so
		// leaving this side on `>=` kept an exactly-full repository page
		// marking the whole result truncated no matter how honest the
		// work-item branch became. Reproduced live before the fix: 200
		// repository rows returned Truncated=true on a complete page.
		rows, scanErr := readers.ReadRepositoryIdentityWithRowLimit(ctx, p.facts.client, orgID, repoIDs, maxFactRowsProbe)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository identity", scanErr)
		}
		for _, row := range rows {
			subject, ok := repoBySubject[row.ID]
			if !ok {
				continue
			}
			if !budget.admit() {
				continue
			}
			fields := map[string]contextfabric.FactValue{
				"id":   contextfabric.StringFactValue(row.ID),
				"name": stringOrNull(row.Slug),
			}
			if row.Provider != "" {
				fields["provider"] = contextfabric.StringFactValue(row.Provider)
			}
			facts = append(facts, contextfabric.CanonicalFact{
				Kind: contextfabric.FactIdentity, Subject: subject, Fields: fields,
				EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, row.ID)},
			})
		}
		budget.observe(len(rows))
		// An ownership read failure never fails the identity read: the facts
		// are served without owning_teams and the result says ownership is
		// unknown (owningTeamsUnreadReason), never that there are no owners.
		omitted, cutSubjects, err := p.attachOwningTeams(ctx, orgID, facts)
		if err != nil {
			ownershipUnread = len(repoIDs)
			slog.Warn("context_fabric_owning_teams_read_failed",
				"producer", "devhealthfacts.identity",
				"repositories", ownershipUnread,
				"error", contextfabric.SanitizeLogAttr(err.Error()),
			)
		}
		ownersCut = omitted
		cutRepositories = cutSubjects
	}

	workItemIDs, workItemBySubject, workItemRejected := v2Index(subjectsOfKind(query.Subjects, contextfabric.SubjectWorkItem), identity.KindWorkItem)
	rejectedCount += workItemRejected
	if len(workItemIDs) > 0 {
		// CHAOS-4377: the SQL build + scan half moved to
		// github.com/full-chaos/dev-health-go/readers.ReadWorkItemIdentity.
		// CHAOS-5438: PROBE one row past the output bound so a full page and
		// a truncated one are distinguishable -- see shared.go's
		// maxFactRowsProbe. CHAOS-5474 folded the REPOSITORY branch in
		// alongside it: this provider ORs both branches into one
		// result-level flag, so the two were never separable.
		rows, scanErr := readers.ReadWorkItemIdentityWithRowLimit(ctx, p.facts.client, orgID, workItemIDs, maxFactRowsProbe)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query work item identity", scanErr)
		}
		for _, row := range rows {
			subject, ok := workItemBySubject[row.RepoID+":"+row.ID]
			if !ok {
				continue
			}
			if !budget.admit() {
				continue
			}
			facts = append(facts, contextfabric.CanonicalFact{
				Kind: contextfabric.FactIdentity, Subject: subject,
				Fields: map[string]contextfabric.FactValue{
					"id":    contextfabric.StringFactValue(row.ID),
					"title": stringOrNull(row.Title),
				},
				EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, row.RepoID+":"+row.ID)},
			})
		}
		budget.observe(len(rows))
	}

	state, emptyReason := currentAxisReadState(len(facts))
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: budget.truncated()}
	if ownersCut > 0 {
		recordOwningTeamsTruncated(ownersCut)
		result.OmittedCount += ownersCut
		result.Truncated = true
		result.State = contextfabric.SourceTruncated
		addScopedReason(&result, query.Subjects, cutRepositories, owningTeamsTruncatedReason)
	}
	if ownershipUnread > 0 {
		result.OmittedCount += ownershipUnread
		result.Truncated = true
		result.State = contextfabric.SourceTruncated
		addScopedReason(&result, query.Subjects, subjectsOfKind(query.Subjects, contextfabric.SubjectRepository), owningTeamsUnreadReason)
	}
	return result, nil
}

// owningTeamsUnreadReason is the closed-vocabulary reason an identity read
// discloses when the ownership read failed: the repositories are served
// without owning_teams, and ownership is unknown rather than empty.
const owningTeamsUnreadReason = "owning_teams_unread"

// owningTeamsTruncatedReason is the reason an identity read discloses when a
// repository has more owning teams than its table carries; the omitted count
// is on the fact and on the result.
const owningTeamsTruncatedReason = "owning_teams_truncated"

func recordOwningTeamsTruncated(omitted int) {
	slog.Warn("context_fabric_owning_teams_truncated",
		"producer", "devhealthfacts.identity",
		"omitted", omitted,
	)
}

// MembershipProvider implements contextfabric.FactProvider for
// FactMembership. There is no canonical team or project table in this
// repository (devhealthsource/teams_projects.go's TeamsProjectsSource is a
// documented no-op for the same reason), so the only membership relationship
// repos/work_items can honestly support is repository containment: which
// repository a work item belongs to, and which organization a repository
// belongs to (the latter needs no query beyond confirming the repository is
// actually in the caller's org -- membership itself is exactly
// principal.OrgID).
type MembershipProvider struct {
	facts clickhouseFacts
}

func newMembershipProvider(client contextpacket.ClickHouseQueryClient) *MembershipProvider {
	return &MembershipProvider{facts: clickhouseFacts{client: client}}
}

func (p *MembershipProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactMembership, "devhealthfacts.membership", []contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectWorkItem})
}

func (p *MembershipProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	if refused, unsupported := refuseHistoricalFact(query); unsupported {
		return refused, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	facts := make([]contextfabric.CanonicalFact, 0, len(query.Subjects))
	// CHAOS-5438 r2 P1: ONE owner for the shared output bound AND the
	// truncation verdict across BOTH branches -- see factBudget. A local
	// `truncated` flag beside a hand-written `len(facts) >=` guard is exactly
	// how the two drifted apart.
	budget := newFactBudget()
	rejectedCount := 0
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.membership", contextfabric.FactMembership, rejectedCount)
		}
	}()

	repoIDs, repoBySubject, repoRejected := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectRepository), "repository:")
	rejectedCount += repoRejected
	if len(repoIDs) > 0 {
		// CHAOS-4377: the SQL build + scan half moved to
		// github.com/full-chaos/dev-health-go/readers.ReadRepositoryIDs.
		// CHAOS-5474: see IdentityProvider's identical note -- one shared
		// truncation flag makes the two branches inseparable.
		rows, scanErr := readers.ReadRepositoryIDsWithRowLimit(ctx, p.facts.client, orgID, repoIDs, maxFactRowsProbe)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository membership", scanErr)
		}
		for _, row := range rows {
			subject, ok := repoBySubject[row.ID]
			if !ok {
				continue
			}
			if !budget.admit() {
				continue
			}
			facts = append(facts, contextfabric.CanonicalFact{
				Kind: contextfabric.FactMembership, Subject: subject,
				Fields:         map[string]contextfabric.FactValue{"organization_id": contextfabric.StringFactValue(orgID)},
				EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, row.ID)},
			})
		}
		budget.observe(len(rows))
	}

	workItemIDs, workItemBySubject, workItemRejected := v2Index(subjectsOfKind(query.Subjects, contextfabric.SubjectWorkItem), identity.KindWorkItem)
	rejectedCount += workItemRejected
	if len(workItemIDs) > 0 {
		// CHAOS-4377: the SQL build + scan half moved to
		// github.com/full-chaos/dev-health-go/readers.ReadWorkItemRepository.
		// CHAOS-5438: PROBE one row past the output bound so a full page and
		// a truncated one are distinguishable -- see shared.go's
		// maxFactRowsProbe. CHAOS-5474 folded the REPOSITORY branch in
		// alongside it: this provider ORs both branches into one
		// result-level flag, so the two were never separable.
		rows, scanErr := readers.ReadWorkItemRepositoryWithRowLimit(ctx, p.facts.client, orgID, workItemIDs, maxFactRowsProbe)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query work item membership", scanErr)
		}
		for _, row := range rows {
			subject, ok := workItemBySubject[row.RepoID+":"+row.ID]
			if !ok {
				continue
			}
			if !budget.admit() {
				continue
			}
			facts = append(facts, contextfabric.CanonicalFact{
				Kind: contextfabric.FactMembership, Subject: subject,
				Fields: map[string]contextfabric.FactValue{
					"repository_id":   contextfabric.StringFactValue(row.RepoID),
					"repository_name": stringOrNull(row.RepoSlug),
				},
				EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, row.RepoID+":"+row.ID)},
			})
		}
		budget.observe(len(rows))
	}

	state, emptyReason := currentAxisReadState(len(facts))
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: budget.truncated()}
	return result, nil
}

// ownerChunkRepos is how many repositories one ownership query covers. The
// per-repository cap (LIMIT .. BY) is the only limit that can bite: the query's
// own row limit is chunk size times that cap, so it never cuts a repository
// short, and a repository with more owners than a fact table carries is
// disclosed with its true owner total.
const ownerChunkRepos = 100

// ownerRowsPerRepo is the owner rows one repository's table carries.
const ownerRowsPerRepo = contextfabric.MaxFactValueRows

// owningTeamsStatement lists the active teams that currently own each
// repository: the one ownership resolver (ownedRepositoriesSource), the
// current-validity window, and the teams table for the name. Never
// membership. Each row also carries the repository's true owner total.
func owningTeamsStatement() string {
	return withRowLimitOf(`SELECT repo_key, team_id, name, toInt64(count() OVER (PARTITION BY repo_key)) AS owners_total
FROM (
	SELECT DISTINCT own.repo_key AS repo_key, toString(own.team_id) AS team_id, t.name AS name
	FROM `+ownedRepositoriesSource(ownershipValidityPredicate(factTimeBound{}))+` AS own
	INNER JOIN (SELECT id, name FROM teams FINAL WHERE org_id = {org_id:String} AND `+devhealthschema.ActiveTeamPredicate("")+`) AS t ON toString(t.id) = toString(own.team_id)
	WHERE own.repo_key IN {ids:Array(String)}
)
ORDER BY repo_key, team_id
LIMIT `+strconv.Itoa(ownerRowsPerRepo)+` BY repo_key`, ownerChunkRepos*ownerRowsPerRepo)
}

// attachOwningTeams adds the owning_teams table to every repository identity
// fact in facts and returns how many owner rows the per-repository cap left
// out. A repository no team owns keeps no table: missing is not empty.
func (p *IdentityProvider) attachOwningTeams(ctx context.Context, orgID string, facts []contextfabric.CanonicalFact) (int, []contextfabric.SubjectRef, error) {
	byRepo := make(map[string]int, len(facts))
	ids := make([]string, 0, len(facts))
	for i, fact := range facts {
		if fact.Subject.Kind != contextfabric.SubjectRepository {
			continue
		}
		id := strings.TrimPrefix(fact.Subject.CanonicalID, "repository:")
		byRepo[id] = i
		ids = append(ids, id)
	}
	rowsByRepo := map[string][]contextfabric.FactValueRow{}
	totalByRepo := map[string]int64{}
	for start := 0; start < len(ids); start += ownerChunkRepos {
		end := min(start+ownerChunkRepos, len(ids))
		err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadRepositoryOwningTeams", owningTeamsStatement(), orgID, ids[start:end], func(row contextpacket.ClickHouseRowScanner) error {
			var repoID, teamID, teamName string
			var total int64
			if err := row.Scan(&repoID, &teamID, &teamName, &total); err != nil {
				return err
			}
			rowsByRepo[repoID] = append(rowsByRepo[repoID], contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
				"team_id":   contextfabric.StringFactValue(teamID),
				"team_name": stringOrNull(teamName),
			}})
			totalByRepo[repoID] = total
			return nil
		})
		if err != nil {
			return 0, nil, readFailure("query repository owning teams", err)
		}
	}
	omittedTotal := 0
	var cut []contextfabric.SubjectRef
	for repoID, i := range byRepo {
		rows := rowsByRepo[repoID]
		if len(rows) == 0 {
			continue
		}
		emitted := map[string]contextfabric.FactValue{
			"owning_teams": contextfabric.TableFactValue(contextfabric.FactTable{
				Shape:        contextfabric.FactTableBreakdown,
				Key:          []string{"team_id"},
				Observations: []string{"team_name"},
				Rows:         rows,
			}),
		}
		if omitted := int(totalByRepo[repoID]) - len(rows); omitted > 0 {
			emitted["owning_teams_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
			omittedTotal += omitted
			cut = append(cut, facts[i].Subject)
		}
		for name, value := range emitted {
			facts[i].Fields[name] = value
		}
	}
	return omittedTotal, cut, nil
}
