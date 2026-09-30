// Package sourcerow expands a Context Fabric evidence ref to the Dev Health
// row its id names (CHAOS-6180, option S): a repository-level row authorized
// by the caller's repository grant, or a team or project row authorized by
// ownership through the direct data tools' subject gate (CHAOS-7227).
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
//
// Served row = authorized subject (#742 r1 P1 class). A read selects its row
// by a string the statement concatenates from the row's columns, and a
// concatenation can match a row of another subject. So after the read, the
// resolver recomputes the row's subject from the row's own columns with the
// producer's function (contractsv1.EvidenceRefID; for a team or a project
// also the canonical id the gate decided) and serves the row only when it is
// the subject the ref names and the grant or the gate admitted. A mismatch is
// refused as no_row (the persisted record answers) and flagged for one Warn
// line.
package sourcerow

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Rows is the ClickHouse side: *contextpacket.CatalogClickHouseRows.
type Rows interface {
	RepositoryByID(ctx context.Context, orgID, repoID string) ([]contractsv1.ResolvedScope, error)
	SourceRowRepositories(ctx context.Context, orgID string, discovery contextpacket.SourceRowDiscovery, entityID string) ([]contractsv1.ResolvedScope, error)
	ResolveSourceRow(ctx context.Context, orgID string, scope contractsv1.ResolvedScope, read contextpacket.SourceRowRead) ([]contextpacket.EvidenceReference, error)
	ResolveOrganizationRow(ctx context.Context, orgID string, read contextpacket.SourceRowRead) ([]contextpacket.OrganizationRowReference, error)
}

// SubjectGate is the direct data tools' subject authorization
// (*directread.SubjectGate): the ONE decision read_facts, run_operation,
// find_subjects and read_relationships take for a team or a project, derived
// from repository OWNERSHIP, never from membership (CHAOS-7227).
type SubjectGate interface {
	Authorize(ctx context.Context, principal storage.Principal, requested []contextfabric.SubjectRef) (directread.AuthorizedSubjects, directread.Authorization)
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
	gate     SubjectGate
}

// New returns a resolver. rows and expander are required. gate may be nil
// (no graph composed): the ownership kinds then stay on the persisted record
// (backend_absent).
func New(rows Rows, expander Expander, gate SubjectGate) (*Resolver, error) {
	if storage.IsNil(rows) || storage.IsNil(expander) {
		return nil, errors.New("sourcerow: rows and expander are required")
	}
	resolver := &Resolver{rows: rows, expander: expander}
	if !storage.IsNil(gate) {
		resolver.gate = gate
	}
	return resolver, nil
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
	case contextfabric.SourceRowRouteOwnership:
		return r.ownershipRow(ctx, principal, entityType, entityID, plan, decision)
	case contextfabric.SourceRowRouteEncoded:
		decision.Grammar = contextfabric.SourceRowGrammarEncoded
		kind := contractsv1.ContextFabricEvidenceEntityType(entityType)
		spec, ok := encodedSpecs[kind]
		if !ok {
			decision.Reason = contextfabric.SourceRowKindOnRecord
			return contractsv1.ExpandedEvidence{}, decision
		}
		values, ok := evidenceref.Parse(kind, entityID)
		// A repository segment the lookup binds as a UUID must be the
		// canonical text every producer mints (toString(UUID)): anything else
		// is refused here, before a read, never sent to ClickHouse.
		if ok && spec.anchorByID {
			ok = evidenceref.CanonicalUUID(values[spec.anchorSegment])
		}
		if !ok {
			decision.Reason = contextfabric.SourceRowIDMalformed
			return contractsv1.ExpandedEvidence{}, decision
		}
		read := contextpacket.SourceRowRead{QueryID: plan.Query, Locator: contractsv1.ContextFabricEvidenceRefPrefix + entityType + ":" + entityID, Components: values}
		targets, err = r.encodedTargets(ctx, principal, spec, values, read, &decision)
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
	// subject is the gated subject of an ownership row, and key the row's own
	// key column values (OrganizationRowQuery.KeyColumns).
	subject contextfabric.SubjectRef
	key     []string
}

// readTargets reads every target and serves exactly one distinct row. A
// row-repository kind (an incident mapped to several repositories) is ONE row
// (its id is the table key within the organization), the same in each; the
// first readable repository, in slug order, serves it. An encoded kind's
// targets are likewise all readings of the ONE row its id names.
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
		if route == contextfabric.SourceRowRouteRowRepository || route == contextfabric.SourceRowRouteEncoded {
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
	if !rowNamesSubject(entityType, entityID, rows[0]) {
		decision.Reason, decision.SubjectMismatch = contextfabric.SourceRowNoRow, true
		return contractsv1.ExpandedEvidence{}, decision
	}
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
	metadata["source_query_version"] = contextpacket.SourceRowQueryVersion
	if contextpacket.CatalogSourceQuery(row.target.read.QueryID) {
		metadata["catalog_version"] = contextpacket.SourceQueryCatalogVersionV1
	}
	expanded.Evidence.Metadata = metadata
	structured := make(map[string]any, len(expanded.Structured)+4)
	for key, value := range expanded.Structured {
		structured[key] = value
	}
	structured["entity_type"] = entityType
	if row.target.scope.RepoID != "" {
		structured["repository_id"] = row.target.scope.RepoID
		structured["repository"] = row.target.scope.RepoSlug
	}
	if row.subject.CanonicalID != "" {
		structured["subject"] = row.subject.CanonicalID
	}
	structured["id_grammar"] = row.target.grammar
	expanded.Structured = structured
	if err := expanded.Validate(); err != nil {
		return contractsv1.ExpandedEvidence{}, fmt.Errorf("validate source row expansion: %w", err)
	}
	return expanded, nil
}

// providerPattern is the colon-free provider segment of a project id. With it
// the project grammar <provider>:<project id> splits at its first ':' and is
// injective, whatever the project id holds.
var providerPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// ownershipSubject maps a team or project ref id to the graph subject the
// gate decides and the row's catalog evidence id. false: the id does not fit
// the kind's grammar.
func ownershipSubject(entityType, entityID string) (contextfabric.SubjectRef, string, bool) {
	locator := contractsv1.ContextFabricEvidenceRefPrefix + entityType + ":" + entityID
	switch contractsv1.ContextFabricEvidenceEntityType(entityType) {
	case contractsv1.ContextFabricEvidenceEntityTeam:
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: contextfabric.TeamCanonicalID(entityID)}, locator, true
	case contractsv1.ContextFabricEvidenceEntityProject:
		provider, projectID, found := strings.Cut(entityID, ":")
		if !found || projectID == "" || !providerPattern.MatchString(provider) {
			return contextfabric.SubjectRef{}, "", false
		}
		canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{provider, projectID}, nil)
		if err != nil || omitted || canonicalID == "" {
			return contextfabric.SubjectRef{}, "", false
		}
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: canonicalID}, locator, true
	}
	return contextfabric.SubjectRef{}, "", false
}

// ownershipRow serves a team or project row the subject gate admits. The
// gate is the whole authorization: denied, absent and ownership-unproven
// subjects are one no_row, and none of them reads ClickHouse. The gate takes
// the same graph reads for an absent subject and for one its node check
// denies (its ownership reach runs only after node admission), so a
// restricted caller's refusal and an absent subject run the same work.
func (r *Resolver) ownershipRow(ctx context.Context, principal storage.Principal, entityType, entityID string, plan contextfabric.SourceRowPlan, decision contextfabric.SourceRowDecision) (contractsv1.ExpandedEvidence, contextfabric.SourceRowDecision) {
	subject, locator, ok := ownershipSubject(entityType, entityID)
	if !ok {
		decision.Reason = contextfabric.SourceRowIDMalformed
		return contractsv1.ExpandedEvidence{}, decision
	}
	decision.Grammar = contextfabric.SourceRowGrammarOrgKeyed
	if r.gate == nil {
		decision.Reason = contextfabric.SourceRowBackendAbsent
		return contractsv1.ExpandedEvidence{}, decision
	}
	_, authorization := r.gate.Authorize(ctx, principal, []contextfabric.SubjectRef{subject})
	if authorization.Decision == directread.DecisionUnavailable {
		decision.Reason, decision.Err = contextfabric.SourceRowUnavailable, authorization.Err
		if decision.Err == nil {
			decision.Err = fmt.Errorf("sourcerow: subject gate unavailable: %s", authorization.Reason)
		}
		return contractsv1.ExpandedEvidence{}, decision
	}
	outcome := directread.SubjectOutcome("")
	if len(authorization.Outcomes) == 1 {
		outcome = authorization.Outcomes[0].Outcome
	}
	if outcome != directread.SubjectAbsent && outcome != directread.SubjectInvalid && outcome != "" {
		decision.Repositories = 1
	}
	if outcome != directread.SubjectAdmitted {
		decision.Reason = contextfabric.SourceRowNoRow
		return contractsv1.ExpandedEvidence{}, decision
	}
	decision.Admitted = 1
	read := contextpacket.SourceRowRead{QueryID: plan.Query, Locator: locator}
	references, err := r.rows.ResolveOrganizationRow(ctx, principal.OrgID, read)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		decision.Reason, decision.Err = contextfabric.SourceRowUnavailable, err
		return contractsv1.ExpandedEvidence{}, decision
	}
	decision.Rows = len(references)
	switch len(references) {
	case 0:
		decision.Reason = contextfabric.SourceRowNoRow
		return contractsv1.ExpandedEvidence{}, decision
	case 1:
	default:
		decision.Reason = contextfabric.SourceRowAmbiguous
		return contractsv1.ExpandedEvidence{}, decision
	}
	row := found{target: target{read: read, grammar: contextfabric.SourceRowGrammarOrgKeyed}, reference: references[0].Reference, subject: subject, key: references[0].Key}
	if !rowNamesSubject(entityType, entityID, row) {
		decision.Reason, decision.SubjectMismatch = contextfabric.SourceRowNoRow, true
		return contractsv1.ExpandedEvidence{}, decision
	}
	expanded, err := r.expand(ctx, entityType, entityID, row)
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

// rowNamesSubject recomputes the subject of the row a read returned from the
// row's own columns, with the producer's functions, and reports whether it is
// the subject the ref names and the grant or gate authorized (#742 r1 P1
// class). The row must carry the catalog id that was read, its ref re-minted
// with contractsv1.EvidenceRefID (the producer's call; evidenceref.Mint for a
// ".v2" kind) must equal the requested ref, and a team or project row's canonical id must equal the one
// the subject gate decided. A repository-level row's repository is the one
// its statement bound (repo_id), which the grant admitted.
func rowNamesSubject(entityType, entityID string, row found) bool {
	evidence := row.reference.Evidence
	if evidence.EvidenceRefID == "" || evidence.EvidenceRefID != row.target.read.Locator {
		return false
	}
	kind := contractsv1.ContextFabricEvidenceEntityType(entityType)
	rowID := evidence.Source.EntityID
	var minted string
	switch kind {
	case contractsv1.ContextFabricEvidenceEntityRepository:
		if rowID == "" || rowID != row.target.scope.RepoID {
			return false
		}
		minted = contractsv1.EvidenceRefID(kind, rowID)
	case contractsv1.ContextFabricEvidenceEntityWorkItem, contractsv1.ContextFabricEvidenceEntityPullRequest,
		contractsv1.ContextFabricEvidenceEntityReview, contractsv1.ContextFabricEvidenceEntityCI,
		contractsv1.ContextFabricEvidenceEntityDeployment:
		if rowID == "" || row.target.scope.RepoID == "" {
			return false
		}
		minted = contractsv1.EvidenceRefID(kind, row.target.scope.RepoID+":"+rowID)
	case contractsv1.ContextFabricEvidenceEntityIncident:
		if rowID == "" {
			return false
		}
		minted = contractsv1.EvidenceRefID(kind, rowID)
	case contractsv1.ContextFabricEvidenceEntityTeam:
		// TeamCanonicalID is a prefix, so with the re-minted ref below this
		// equality is implied; it is kept as the gate-side statement.
		if len(row.key) != 1 || row.key[0] == "" || row.subject.Kind != contextfabric.SubjectTeam ||
			contextfabric.TeamCanonicalID(row.key[0]) != row.subject.CanonicalID {
			return false
		}
		minted = contractsv1.EvidenceRefID(kind, row.key[0])
	case contractsv1.ContextFabricEvidenceEntityProject:
		// The re-minted ref alone cannot tell (acme:linear, SECRET) from
		// (acme, linear:SECRET): both serialize to acme:linear:SECRET. The
		// canonical id, derived from the row's own (provider, id), does.
		if len(row.key) != 2 || row.key[1] == "" || row.subject.Kind != contextfabric.SubjectProject {
			return false
		}
		canonicalID, omitted, err := identity.Derive(identity.KindProject, row.key, nil)
		if err != nil || omitted || canonicalID == "" || canonicalID != row.subject.CanonicalID {
			return false
		}
		minted = contractsv1.EvidenceRefID(kind, row.key[0]+":"+row.key[1])
	default:
		// A ".v2" row (CHAOS-7252): its id is the statement's rendering of
		// the row's own columns (evidenceref.IDSQL). It must parse as the
		// kind's grammar and re-mint through the producers' function
		// (evidenceref.Mint) to the requested ref, and where the id names
		// the row's repository, that repository must be the one the grant
		// admitted (the read's repo_id).
		spec, encoded := encodedSpecs[kind]
		if !encoded || rowID == "" {
			return false
		}
		values, parsed := evidenceref.Parse(kind, rowID)
		if !parsed || (spec.anchorByID && values[spec.anchorSegment] != row.target.scope.RepoID) {
			return false
		}
		minted, _ = evidenceref.Mint(kind, values...)
	}
	return minted == contractsv1.ContextFabricEvidenceRefPrefix+entityType+":"+entityID
}

// endpoint is one subject an encoded row names besides the repository it is
// read in, found by a discovery over one id segment.
type endpoint struct {
	discovery contextpacket.SourceRowDiscovery
	segment   int
	// optional: an endpoint the discovery finds nothing for does not
	// refuse the row. Only the dependency target is optional: the projector
	// stubs a target with no work item row and authorizes the edge by its
	// source alone (devhealthsource queryWorkItemDependencies, §1.5).
	optional bool
}

// encodedSpec is how one ".v2" kind's row is authorized and located
// (CHAOS-7252). The decision is the read_relationships edge gate: the edge's
// own repository and each endpoint must be readable to the caller. An
// endpoint that lives in several repositories is readable when one of them
// is, which is exactly "at least one projected edge of this row is visible"
// -- the grant never chooses between rows, because every target is the ONE
// row the id names.
type encodedSpec struct {
	// The repositories the row may be read in: the one a UUID segment names
	// (anchorByID), or those a discovery over a segment finds.
	anchorByID      bool
	anchorSegment   int
	anchorDiscovery contextpacket.SourceRowDiscovery
	endpoints       []endpoint
	// wildcardOnly: the row names a subject this resolver cannot decide yet
	// -- a team, whose ownership-derived gate CHAOS-7227 composes -- so the
	// row is served only to a caller that a "*" repository list admits
	// (unrestricted and "*" callers), and every repository-restricted caller
	// is refused (fail closed; strictly stronger than the edge gate).
	wildcardOnly bool
}

var encodedSpecs = map[contractsv1.ContextFabricEvidenceEntityType]encodedSpec{
	// (source, target, relation key): read where the source work item
	// lives; the target, when it resolves, must be readable too.
	contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2: {
		anchorDiscovery: contextpacket.SourceRowDiscoveryWorkItem, anchorSegment: 0,
		endpoints: []endpoint{{discovery: contextpacket.SourceRowDiscoveryWorkItem, segment: 1, optional: true}},
	},
	// (child repository, child, parent): read in the child's repository;
	// the parent must be a readable work item.
	contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2: {
		anchorByID: true, anchorSegment: 0,
		endpoints: []endpoint{{discovery: contextpacket.SourceRowDiscoveryWorkItem, segment: 2}},
	},
	// (attribution repo_id, work item, team, source): read where the work
	// item lives (the attribution's own repo_id is mostly the zero UUID);
	// the team is decided fail closed until CHAOS-7227.
	contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2: {
		anchorDiscovery: contextpacket.SourceRowDiscoveryWorkItem, anchorSegment: 1,
		wildcardOnly: true,
	},
	// (repository, deployment, incident, source): read in the repository
	// the row names (the edge and its deployment); the incident must be
	// readable through a current service mapping. An incident no service
	// maps has no readable repository and is refused.
	contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2: {
		anchorByID: true, anchorSegment: 0,
		endpoints: []endpoint{{discovery: contextpacket.SourceRowDiscoveryIncident, segment: 2}},
	},
}

// EncodedKinds lists the kinds with an encoded source-row spec, for the plan
// totality tests.
func EncodedKinds() []contractsv1.ContextFabricEvidenceEntityType {
	kinds := make([]contractsv1.ContextFabricEvidenceEntityType, 0, len(encodedSpecs))
	for kind := range encodedSpecs {
		kinds = append(kinds, kind)
	}
	return kinds
}

// admitsWildcard is the direct data tools' decision for a node whose
// repository list is "*" (CHAOS-7080): unrestricted and "*" callers only.
func admitsWildcard(principal storage.Principal) bool {
	return graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, map[string]interface{}{"authorization_repositories": []string{"*"}})
}

// encodedTargets runs every lookup of an encoded row -- the read
// repositories, then each endpoint, always all of them and in this order, so
// a refused row and an absent one run the same reads -- then decides the
// grant in Go and returns the repositories the row may be read in.
func (r *Resolver) encodedTargets(ctx context.Context, principal storage.Principal, spec encodedSpec, values []string, read contextpacket.SourceRowRead, decision *contextfabric.SourceRowDecision) ([]target, error) {
	var anchors []contractsv1.ResolvedScope
	var err error
	if spec.anchorByID {
		anchors, err = r.rows.RepositoryByID(ctx, principal.OrgID, values[spec.anchorSegment])
		if err == nil && len(anchors) > 1 {
			decision.Repositories += len(anchors)
			return nil, errSaturated
		}
	} else {
		anchors, err = r.discover(ctx, principal, spec.anchorDiscovery, values[spec.anchorSegment], decision)
	}
	if err != nil {
		return nil, err
	}
	found := make([][]contractsv1.ResolvedScope, len(spec.endpoints))
	for index, end := range spec.endpoints {
		if found[index], err = r.discover(ctx, principal, end.discovery, values[end.segment], decision); err != nil {
			return nil, err
		}
	}
	readable := make([]contractsv1.ResolvedScope, 0, len(anchors))
	for _, scope := range admitted(principal, anchors, decision) {
		if strings.TrimSpace(scope.RepoSlug) != "" {
			readable = append(readable, scope)
		}
	}
	allowed := len(readable) > 0
	for index, end := range spec.endpoints {
		admittedEnds := admitted(principal, found[index], decision)
		if len(found[index]) == 0 {
			allowed = allowed && end.optional
			continue
		}
		allowed = allowed && len(admittedEnds) > 0
	}
	if spec.wildcardOnly && !admitsWildcard(principal) {
		allowed = false
	}
	if !allowed {
		return nil, nil
	}
	targets := make([]target, 0, len(readable))
	for _, scope := range readable {
		targets = append(targets, target{scope: scope, read: read, grammar: contextfabric.SourceRowGrammarEncoded})
	}
	return targets, nil
}

// discover runs one repository discovery and refuses a saturated list.
func (r *Resolver) discover(ctx context.Context, principal storage.Principal, discovery contextpacket.SourceRowDiscovery, entityID string, decision *contextfabric.SourceRowDecision) ([]contractsv1.ResolvedScope, error) {
	scopes, err := r.rows.SourceRowRepositories(ctx, principal.OrgID, discovery, entityID)
	if err != nil {
		return nil, err
	}
	if len(scopes) > contextpacket.MaxSourceRowRepositories() {
		decision.Repositories += len(scopes)
		return nil, errSaturated
	}
	return scopes, nil
}
