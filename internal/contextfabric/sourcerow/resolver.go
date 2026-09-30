// Package sourcerow expands a Context Fabric evidence ref to the Dev Health
// row its id names (CHAOS-6180, option S), authorized by the caller's
// repository grant.
//
// Authorization is the one the direct data tools use (read_facts,
// run_operation): graphrank.AuthorizedAttributes over the row's repository
// slug, the same call the subject gate makes for a repository node. An
// unrestricted caller (no grant list) reads any repository of its
// organization; a "*" grant reads any; a restricted caller reads the
// repositories its grant lists and nothing else.
//
// Equal refusal. The reads never carry the grant: the repository lookup of
// an id, and the repository discovery of a row, run with the same text and
// bindings for every caller. The grant is decided in Go after that lookup,
// and only a repository the caller may read is ever read further. An id whose
// repository is out of the caller's grant and an id that names nothing
// therefore run the same reads and end in the same SourceRowNoRow, and the
// hosted route answers both through the same persisted-record path. The one
// difference left is ClickHouse's own time for a lookup that finds a
// repository and one that does not.
package sourcerow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Rows is the ClickHouse side: *contextpacket.CatalogClickHouseRows.
type Rows interface {
	RepositoryByID(ctx context.Context, orgID, repoID string) ([]contractsv1.ResolvedScope, error)
	SourceRowRepositories(ctx context.Context, orgID string, discovery contextpacket.SourceRowDiscovery, entityID string) ([]contractsv1.ResolvedScope, error)
	ResolveSourceRow(ctx context.Context, orgID string, scope contractsv1.ResolvedScope, read contextpacket.SourceRowRead) ([]contextpacket.EvidenceReference, error)
}

// Expander turns a catalog row into an expansion:
// *contextpacket.EvidenceResolver.
type Expander interface {
	Expand(ctx context.Context, input contextpacket.EvidenceExpansionInput) (contractsv1.ExpandedEvidence, error)
}

// Resolver implements contextfabric.SourceRowResolver.
type Resolver struct {
	rows     Rows
	expander Expander
}

// New returns a resolver. Both arguments are required.
func New(rows Rows, expander Expander) (*Resolver, error) {
	if storage.IsNil(rows) || storage.IsNil(expander) {
		return nil, errors.New("sourcerow: rows and expander are required")
	}
	return &Resolver{rows: rows, expander: expander}, nil
}

var _ contextfabric.SourceRowResolver = (*Resolver)(nil)

// repositoryIDLength is the length of a repository UUID's canonical text.
const repositoryIDLength = 36

// target is one row read in one repository the caller may read.
type target struct {
	scope   contractsv1.ResolvedScope
	read    contextpacket.SourceRowRead
	grammar string
}

// ResolveSourceRow implements contextfabric.SourceRowResolver.
func (r *Resolver) ResolveSourceRow(ctx context.Context, principal storage.Principal, entityType, entityID string) (contractsv1.ExpandedEvidence, contextfabric.SourceRowDecision) {
	plan := contextfabric.SourceRowPlanFor(entityType)
	decision := contextfabric.SourceRowDecision{Query: plan.Query}
	if plan.Route == contextfabric.SourceRowRouteRecord || plan.Query == "" {
		decision.Reason = contextfabric.SourceRowKindOnRecord
		return contractsv1.ExpandedEvidence{}, decision
	}
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" || strings.TrimSpace(entityID) != entityID || entityID == "" {
		decision.Reason = contextfabric.SourceRowIDMalformed
		return contractsv1.ExpandedEvidence{}, decision
	}
	var targets []target
	var err error
	switch plan.Route {
	case contextfabric.SourceRowRouteRepository:
		repoID, rest, ok := splitRepositoryID(entityType, entityID)
		if !ok {
			decision.Reason = contextfabric.SourceRowIDMalformed
			return contractsv1.ExpandedEvidence{}, decision
		}
		decision.Grammar = contextfabric.SourceRowGrammarRepoAnchored
		read := contextpacket.SourceRowRead{QueryID: plan.Query, Locator: contractsv1.ContextFabricEvidenceRefPrefix + entityType + ":" + rest}
		if entityType == string(contractsv1.ContextFabricEvidenceEntityRepository) {
			read.Locator = contractsv1.ContextFabricEvidenceRefPrefix + entityType + ":" + repoID
		}
		if entityType == string(contractsv1.ContextFabricEvidenceEntityWorkItem) {
			read.TaskRef = rest
		}
		targets, err = r.repositoryTargets(ctx, principal, repoID, read, &decision)
	case contextfabric.SourceRowRouteRowRepository:
		decision.Grammar = contextfabric.SourceRowGrammarRowAnchored
		discovery, ok := rowDiscoveries[contractsv1.ContextFabricEvidenceEntityType(entityType)]
		if !ok {
			decision.Reason = contextfabric.SourceRowKindOnRecord
			return contractsv1.ExpandedEvidence{}, decision
		}
		read := contextpacket.SourceRowRead{QueryID: plan.Query, Locator: contractsv1.ContextFabricEvidenceRefPrefix + entityType + ":" + entityID}
		targets, err = r.discoveredTargets(ctx, principal, discovery, entityID, read, contextfabric.SourceRowGrammarRowAnchored, &decision)
	default:
		decision.Reason = contextfabric.SourceRowKindOnRecord
		return contractsv1.ExpandedEvidence{}, decision
	}
	if err != nil {
		if errors.Is(err, errSaturated) {
			decision.Reason = contextfabric.SourceRowAmbiguous
			return contractsv1.ExpandedEvidence{}, decision
		}
		decision.Reason, decision.Err = contextfabric.SourceRowUnavailable, err
		return contractsv1.ExpandedEvidence{}, decision
	}
	return r.readTargets(ctx, principal, entityType, entityID, plan.Route, targets, decision)
}

var errSaturated = errors.New("sourcerow: row maps to more repositories than the bound")

// rowDiscoveries names the repository discovery of each row-anchored kind.
// Only a kind whose id is its table's key within the organization may be
// here: its repositories are then all mappings of ONE row, and the grant
// filter never chooses between distinct rows (CHAOS-7226 r2 P1 class).
var rowDiscoveries = map[contractsv1.ContextFabricEvidenceEntityType]contextpacket.SourceRowDiscovery{
	contractsv1.ContextFabricEvidenceEntityIncident: contextpacket.SourceRowDiscoveryIncident,
}

// splitRepositoryID splits <repo UUID>:<rest>. A repository id is the UUID
// alone. The UUID has a fixed length, so rest may hold ':' freely.
func splitRepositoryID(entityType, entityID string) (repoID, rest string, ok bool) {
	if entityType == string(contractsv1.ContextFabricEvidenceEntityRepository) {
		return entityID, "", isRepositoryID(entityID)
	}
	if len(entityID) < repositoryIDLength+2 || entityID[repositoryIDLength] != ':' {
		return "", "", false
	}
	repoID, rest = entityID[:repositoryIDLength], entityID[repositoryIDLength+1:]
	return repoID, rest, isRepositoryID(repoID) && rest != ""
}

// isRepositoryID reports the canonical 8-4-4-4-12 lower-case hexadecimal
// UUID text, the only form every producer mints (ClickHouse toString).
func isRepositoryID(value string) bool {
	if len(value) != repositoryIDLength {
		return false
	}
	for index, char := range value {
		switch index {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdef", char) {
				return false
			}
		}
	}
	return true
}

// admits is the direct data tools' repository decision for one repository
// slug: the attributes a repository node carries, decided by the shared
// graph predicate.
func admits(principal storage.Principal, slug string) bool {
	return graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, map[string]interface{}{"authorization_repositories": []string{slug}})
}

// admitted keeps the repositories principal may read, in lookup order.
func admitted(principal storage.Principal, scopes []contractsv1.ResolvedScope, decision *contextfabric.SourceRowDecision) []contractsv1.ResolvedScope {
	decision.Repositories += len(scopes)
	kept := make([]contractsv1.ResolvedScope, 0, len(scopes))
	for _, scope := range scopes {
		if strings.TrimSpace(scope.RepoID) != "" && admits(principal, scope.RepoSlug) {
			kept = append(kept, scope)
		}
	}
	decision.Admitted += len(kept)
	return kept
}

// repositoryTargets looks the id's repository up and keeps it when the caller
// may read it. Two repositories under one id cannot be told apart and are
// refused.
func (r *Resolver) repositoryTargets(ctx context.Context, principal storage.Principal, repoID string, read contextpacket.SourceRowRead, decision *contextfabric.SourceRowDecision) ([]target, error) {
	scopes, err := r.rows.RepositoryByID(ctx, principal.OrgID, repoID)
	if err != nil {
		return nil, err
	}
	if len(scopes) > 1 {
		decision.Repositories += len(scopes)
		return nil, errSaturated
	}
	var targets []target
	for _, scope := range admitted(principal, scopes, decision) {
		targets = append(targets, target{scope: scope, read: read, grammar: decision.Grammar})
	}
	return targets, nil
}

// discoveredTargets finds the repositories a row maps to and keeps the ones
// the caller may read.
func (r *Resolver) discoveredTargets(ctx context.Context, principal storage.Principal, discovery contextpacket.SourceRowDiscovery, entityID string, read contextpacket.SourceRowRead, grammar string, decision *contextfabric.SourceRowDecision) ([]target, error) {
	scopes, err := r.rows.SourceRowRepositories(ctx, principal.OrgID, discovery, entityID)
	if err != nil {
		return nil, err
	}
	if len(scopes) > contextpacket.MaxSourceRowRepositories() {
		decision.Repositories += len(scopes)
		return nil, errSaturated
	}
	var targets []target
	for _, scope := range admitted(principal, scopes, decision) {
		targets = append(targets, target{scope: scope, read: read, grammar: grammar})
	}
	return targets, nil
}

// found is one read that returned exactly one row.
type found struct {
	target    target
	reference contextpacket.EvidenceReference
}

// readTargets reads every target and serves exactly one distinct row. A
// row-repository kind (an incident mapped to several repositories) is ONE row
// (its id is the table key within the organization), the same in each; the
// first readable repository, in slug order, serves it.
func (r *Resolver) readTargets(ctx context.Context, principal storage.Principal, entityType, entityID string, route contextfabric.SourceRowRoute, targets []target, decision contextfabric.SourceRowDecision) (contractsv1.ExpandedEvidence, contextfabric.SourceRowDecision) {
	var rows []found
	seen := map[string]bool{}
	for _, candidate := range targets {
		references, err := r.rows.ResolveSourceRow(ctx, principal.OrgID, candidate.scope, candidate.read)
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			decision.Reason, decision.Err = contextfabric.SourceRowUnavailable, err
			return contractsv1.ExpandedEvidence{}, decision
		}
		decision.Rows += len(references)
		if len(references) > 1 {
			decision.Reason = contextfabric.SourceRowAmbiguous
			return contractsv1.ExpandedEvidence{}, decision
		}
		if len(references) == 0 {
			continue
		}
		key := candidate.scope.RepoID + "\x00" + candidate.read.Locator
		if seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, found{target: candidate, reference: references[0]})
		if route == contextfabric.SourceRowRouteRowRepository {
			break
		}
	}
	switch {
	case len(rows) == 0:
		decision.Reason = contextfabric.SourceRowNoRow
		return contractsv1.ExpandedEvidence{}, decision
	case len(rows) > 1:
		decision.Reason = contextfabric.SourceRowAmbiguous
		return contractsv1.ExpandedEvidence{}, decision
	}
	decision.Grammar = rows[0].target.grammar
	expanded, err := r.expand(ctx, entityType, entityID, rows[0])
	if err != nil {
		decision.Reason, decision.Err = contextfabric.SourceRowInvalid, err
		if ctxErr := ctx.Err(); ctxErr != nil {
			decision.Reason, decision.Err = contextfabric.SourceRowUnavailable, ctxErr
		}
		return contractsv1.ExpandedEvidence{}, decision
	}
	decision.Reason = contextfabric.SourceRowServed
	return expanded, decision
}

// expand builds the expansion from the catalog row: the packet path's own
// expansion of that row, re-keyed to the Context Fabric ref and labelled as
// the current source row.
func (r *Resolver) expand(ctx context.Context, entityType, entityID string, row found) (contractsv1.ExpandedEvidence, error) {
	evidence := row.reference.Evidence
	expanded, err := r.expander.Expand(ctx, contextpacket.EvidenceExpansionInput{Evidence: evidence, Excerpt: row.reference.Excerpt})
	if err != nil {
		return contractsv1.ExpandedEvidence{}, fmt.Errorf("expand source row: %w", err)
	}
	if expanded.Availability != contractsv1.EvidenceAvailable && expanded.Availability != contractsv1.EvidenceStale {
		return contractsv1.ExpandedEvidence{}, fmt.Errorf("expand source row: availability %q", expanded.Availability)
	}
	expanded.Evidence.EvidenceRefID = contractsv1.ContextFabricEvidenceRefPrefix + entityType + ":" + entityID
	metadata := make(map[string]any, len(expanded.Evidence.Metadata)+6)
	for key, value := range expanded.Evidence.Metadata {
		metadata[key] = value
	}
	metadata["record"] = "source_row"
	metadata["row_state"] = "current"
	metadata["row_observed_at"] = evidence.ObservedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	metadata["source_query"] = row.target.read.QueryID
	metadata["source_query_version"] = contextpacket.SourceRowQueryVersionV1
	metadata["catalog_version"] = contextpacket.SourceQueryCatalogVersionV1
	expanded.Evidence.Metadata = metadata
	structured := make(map[string]any, len(expanded.Structured)+4)
	for key, value := range expanded.Structured {
		structured[key] = value
	}
	structured["entity_type"] = entityType
	structured["repository_id"] = row.target.scope.RepoID
	structured["repository"] = row.target.scope.RepoSlug
	structured["id_grammar"] = row.target.grammar
	expanded.Structured = structured
	if err := expanded.Validate(); err != nil {
		return contractsv1.ExpandedEvidence{}, fmt.Errorf("validate source row expansion: %w", err)
	}
	return expanded, nil
}
