package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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

func (p *IdentityProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	facts := make([]contextfabric.CanonicalFact, 0, len(query.Subjects))

	repoIDs, repoBySubject := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectRepository), "repository:")
	if len(repoIDs) > 0 {
		statement := `SELECT toString(r.id), ifNull(r.repo, ''), ifNull(r.provider, '')
FROM repos AS r FINAL
WHERE r.org_id = {org_id:String} AND toString(r.id) IN {ids:Array(String)}`
		scanErr := p.facts.query(ctx, statement, orgID, repoIDs, func(row contextpacket.ClickHouseRowScanner) error {
			var id, slug, provider string
			if err := row.Scan(&id, &slug, &provider); err != nil {
				return err
			}
			subject, ok := repoBySubject[id]
			if !ok {
				return nil
			}
			fields := map[string]contextfabric.FactValue{
				"id":   contextfabric.StringFactValue(id),
				"name": stringOrNull(slug),
			}
			if provider != "" {
				fields["provider"] = contextfabric.StringFactValue(provider)
			}
			facts = append(facts, contextfabric.CanonicalFact{
				Kind: contextfabric.FactIdentity, Subject: subject, Fields: fields,
				EvidenceRefIDs: []string{evidenceRefID("repository", id)},
			})
			return nil
		})
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository identity", scanErr)
		}
	}

	workItemIDs, workItemBySubject := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectWorkItem), "work_item:")
	if len(workItemIDs) > 0 {
		statement := `SELECT w.work_item_id, ifNull(w.title, '')
FROM work_items AS w FINAL
WHERE w.org_id = {org_id:String} AND w.work_item_id IN {ids:Array(String)}`
		scanErr := p.facts.query(ctx, statement, orgID, workItemIDs, func(row contextpacket.ClickHouseRowScanner) error {
			var id, title string
			if err := row.Scan(&id, &title); err != nil {
				return err
			}
			subject, ok := workItemBySubject[id]
			if !ok {
				return nil
			}
			facts = append(facts, contextfabric.CanonicalFact{
				Kind: contextfabric.FactIdentity, Subject: subject,
				Fields: map[string]contextfabric.FactValue{
					"id":    contextfabric.StringFactValue(id),
					"title": stringOrNull(title),
				},
				EvidenceRefIDs: []string{evidenceRefID("work-item", id)},
			})
			return nil
		})
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query work item identity", scanErr)
		}
	}

	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion}, nil
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

func (p *MembershipProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	facts := make([]contextfabric.CanonicalFact, 0, len(query.Subjects))

	repoIDs, repoBySubject := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectRepository), "repository:")
	if len(repoIDs) > 0 {
		statement := `SELECT toString(r.id)
FROM repos AS r FINAL
WHERE r.org_id = {org_id:String} AND toString(r.id) IN {ids:Array(String)}`
		scanErr := p.facts.query(ctx, statement, orgID, repoIDs, func(row contextpacket.ClickHouseRowScanner) error {
			var id string
			if err := row.Scan(&id); err != nil {
				return err
			}
			subject, ok := repoBySubject[id]
			if !ok {
				return nil
			}
			facts = append(facts, contextfabric.CanonicalFact{
				Kind: contextfabric.FactMembership, Subject: subject,
				Fields:         map[string]contextfabric.FactValue{"organization_id": contextfabric.StringFactValue(orgID)},
				EvidenceRefIDs: []string{evidenceRefID("repository", id)},
			})
			return nil
		})
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository membership", scanErr)
		}
	}

	workItemIDs, workItemBySubject := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectWorkItem), "work_item:")
	if len(workItemIDs) > 0 {
		statement := `SELECT w.work_item_id, toString(w.repo_id), ifNull(r.repo, '')
FROM work_items AS w FINAL INNER JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE w.org_id = {org_id:String} AND w.work_item_id IN {ids:Array(String)}`
		scanErr := p.facts.query(ctx, statement, orgID, workItemIDs, func(row contextpacket.ClickHouseRowScanner) error {
			var id, repoID, repoSlug string
			if err := row.Scan(&id, &repoID, &repoSlug); err != nil {
				return err
			}
			subject, ok := workItemBySubject[id]
			if !ok {
				return nil
			}
			facts = append(facts, contextfabric.CanonicalFact{
				Kind: contextfabric.FactMembership, Subject: subject,
				Fields: map[string]contextfabric.FactValue{
					"repository_id":   contextfabric.StringFactValue(repoID),
					"repository_name": stringOrNull(repoSlug),
				},
				EvidenceRefIDs: []string{evidenceRefID("work-item", id)},
			})
			return nil
		})
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query work item membership", scanErr)
		}
	}

	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion}, nil
}
