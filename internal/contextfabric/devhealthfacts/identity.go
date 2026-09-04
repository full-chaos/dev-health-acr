package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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
	truncated := false
	rejectedCount := 0
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
		rows, scanErr := readers.ReadRepositoryIdentity(ctx, p.facts.client, orgID, repoIDs)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository identity", scanErr)
		}
		for _, row := range rows {
			subject, ok := repoBySubject[row.ID]
			if !ok {
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
		truncated = truncated || len(rows) >= maxFactRowsPerQuery
	}

	workItemIDs, workItemBySubject, workItemRejected := v2Index(subjectsOfKind(query.Subjects, contextfabric.SubjectWorkItem), identity.KindWorkItem)
	rejectedCount += workItemRejected
	if len(workItemIDs) > 0 {
		// CHAOS-4377: the SQL build + scan half moved to
		// github.com/full-chaos/dev-health-go/readers.ReadWorkItemIdentity.
		rows, scanErr := readers.ReadWorkItemIdentity(ctx, p.facts.client, orgID, workItemIDs)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query work item identity", scanErr)
		}
		for _, row := range rows {
			subject, ok := workItemBySubject[row.RepoID+":"+row.ID]
			if !ok {
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
		truncated = truncated || len(rows) >= maxFactRowsPerQuery
	}

	state, emptyReason := currentAxisReadState(len(facts))
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: truncated}
	return result, nil
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
	truncated := false
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
		rows, scanErr := readers.ReadRepositoryIDs(ctx, p.facts.client, orgID, repoIDs)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository membership", scanErr)
		}
		for _, row := range rows {
			subject, ok := repoBySubject[row.ID]
			if !ok {
				continue
			}
			facts = append(facts, contextfabric.CanonicalFact{
				Kind: contextfabric.FactMembership, Subject: subject,
				Fields:         map[string]contextfabric.FactValue{"organization_id": contextfabric.StringFactValue(orgID)},
				EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, row.ID)},
			})
		}
		truncated = truncated || len(rows) >= maxFactRowsPerQuery
	}

	workItemIDs, workItemBySubject, workItemRejected := v2Index(subjectsOfKind(query.Subjects, contextfabric.SubjectWorkItem), identity.KindWorkItem)
	rejectedCount += workItemRejected
	if len(workItemIDs) > 0 {
		// CHAOS-4377: the SQL build + scan half moved to
		// github.com/full-chaos/dev-health-go/readers.ReadWorkItemRepository.
		rows, scanErr := readers.ReadWorkItemRepository(ctx, p.facts.client, orgID, workItemIDs)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query work item membership", scanErr)
		}
		for _, row := range rows {
			subject, ok := workItemBySubject[row.RepoID+":"+row.ID]
			if !ok {
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
		truncated = truncated || len(rows) >= maxFactRowsPerQuery
	}

	state, emptyReason := currentAxisReadState(len(facts))
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: truncated}
	return result, nil
}
