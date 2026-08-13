package devhealthsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// SourceName is the canonical Source value ClickHouseProjectionSource writes
// onto every batch it produces. Checkpoints and telemetry are keyed by it.
const SourceName = "dev_health_clickhouse"

// ClickHouseSourceVersion is bumped to v3 by CHAOS-3785: queryWorkItems,
// queryWorkItemDependencies, and queryWorkItemHierarchy relax their repos
// join from INNER to LEFT so a Linear-sourced work item (repo_id = the zero
// UUID at ingest) projects instead of being silently dropped. The bump is
// deliberate here too, for a reason distinct from v2's identity-scheme
// change: this cursor is an event-time watermark
// (docs/design/context-fabric-projection-worker.md, "Honest limitation:
// event-time watermarks miss backfilled/corrected rows") -- an
// already-projected organization's checkpoint may have advanced past many
// Linear work items' updated_at values on the strength of OTHER tables'
// rows sharing the same checkpoint, long before those work items could ever
// satisfy the old INNER JOIN. Left unversioned, ordinary incremental
// catch-up would never revisit them; only a full rebuild does. Forcing
// ErrProjectionSourceVersionChanged on every already-projected organization
// makes that rebuild happen deliberately (acr-projector rebuild --org)
// instead of leaving a silent, permanent gap for exactly the organizations
// this fix exists to help.
//
// (v2, CHAOS-3779 codex round-2 H2 residual: queryWorkItemDependencies'
// RelationshipID began embedding relationship_type (previously (source,
// target) only), and queryWorkItemHierarchy was a new producer.)
const ClickHouseSourceVersion = "devhealthsource.clickhouse.v3"

// Bounds keep a single batch inside ContextFabricProjectionBatch's v1 caps
// (1000 entities, 5000 relationships) with headroom for the episode and
// team/project sources sharing the same organization.
const (
	incrementalBatchCap = 200
	snapshotPerQueryCap = 150
)

// organizationAnchorTime is the fixed ObservedAt every full-snapshot
// organization entity uses; see the comment where it's applied.
var organizationAnchorTime = time.Unix(1, 0).UTC()

// ProducedRelationshipTypes lists every ContextFabricRelationshipType this
// package's queries can write into a ContextFabricRelationshipProjection
// (CHAOS-3779, AC-3779-9's second direction: every produced type must be a
// member of the closed vocabulary). RELATED_TO/RELATES_TO/BLOCKS/DUPLICATES
// come from queryWorkItemDependencies (work_item_dependencies.
// relationship_type, uppercased -- TRD §19.13 Correction 1's three live
// values plus the ifNull default); BELONGS_TO_REPOSITORY from
// belongsToRepository (queryWorkItems/queryPullRequests/queryDeployments/
// queryIncidents/queryCIRuns); BELONGS_TO_PULL_REQUEST from
// queryPullRequestReviews; CORRELATED_WITH_INCIDENT from
// queryDeploymentIncidentEdges; PART_OF from queryWorkItemHierarchy. See
// the AC-3779-9 cross-wiring test in cmd/acr-projector, the only caller
// today.
func ProducedRelationshipTypes() []contractsv1.ContextFabricRelationshipType {
	return []contractsv1.ContextFabricRelationshipType{
		contractsv1.ContextFabricRelationshipBelongsToRepository,
		contractsv1.ContextFabricRelationshipBelongsToPullRequest,
		contractsv1.ContextFabricRelationshipCorrelatedWithIncident,
		contractsv1.ContextFabricRelationshipRelatedTo,
		contractsv1.ContextFabricRelationshipBlocks,
		contractsv1.ContextFabricRelationshipPartOf,
		contractsv1.ContextFabricRelationshipRelatesTo,
		contractsv1.ContextFabricRelationshipDuplicates,
	}
}

// ClickHouseProjectionSource is the production contextfabric.ProjectionSource
// for canonical Dev Health repository, work item, pull request, deployment,
// and incident data. It reuses internal/contextpacket's existing ClickHouse
// read boundary rather than opening a second connection convention.
type ClickHouseProjectionSource struct {
	client contextpacket.ClickHouseQueryClient
	now    func() time.Time
}

func NewClickHouseProjectionSource(client contextpacket.ClickHouseQueryClient) (*ClickHouseProjectionSource, error) {
	if client == nil {
		return nil, fmt.Errorf("devhealthsource: clickhouse query client is required")
	}
	return &ClickHouseProjectionSource{client: client, now: time.Now}, nil
}

// candidate is a sortable, already-built projection item. Exactly one of
// Entity, Relationship, Episode, or Tombstone is set.
type candidate struct {
	observedAt   time.Time
	sortKey      string
	entity       *contractsv1.ContextFabricEntityProjection
	relationship *contractsv1.ContextFabricRelationshipProjection
	episode      *contractsv1.ContextFabricEpisodeProjection
	tombstone    *contractsv1.ContextFabricProjectionTombstone
}

func (s *ClickHouseProjectionSource) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	if s == nil || s.client == nil {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: source is not configured")
	}
	orgID := strings.TrimSpace(checkpoint.OrgID)
	if orgID == "" {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: organization is required")
	}
	if checkpoint.Cursor == "" {
		return s.fullSnapshot(ctx, orgID)
	}
	return s.incremental(ctx, orgID, checkpoint.Cursor)
}

// fullSnapshot attempts one complete-enumeration batch (FullSnapshot: true,
// CompleteEnumeration: true -- ContextFabricProjectionBatch.Validate()
// requires both together). When the organization is too large for that
// single bounded batch, it falls back to pagedBatch from the same zero
// cursor instead of erroring -- CHAOS-3753 codex finding C6: refusing left
// any organization above the per-table cap permanently stuck (every
// subsequent tick re-attempted the same oversized single-batch snapshot
// and failed the same way; initial projection never completed). The
// fallback produces an ordinary bounded incremental-shaped batch per tick
// until caught up, exactly like any other incremental catch-up.
//
// "Too large" is detected two ways (codex round-2 finding K4): a single
// table individually truncated at snapshotPerQueryCap, OR the aggregate
// candidate count across every table exceeding the v1 contract's own
// per-batch bounds even when no single table was truncated -- N tables
// each just under their own per-table cap can still sum past the
// contract's aggregate entity/relationship bound (e.g. seven tables at
// 149 rows apiece is 1043 entities, over the 1000 cap). Checking only the
// per-table signal let that case reach buildBatch and fail contract
// validation instead of paging -- the same "stuck forever" shape C6 fixed
// for the per-table case, just triggered by an aggregate rather than a
// single oversized table.
func (s *ClickHouseProjectionSource) fullSnapshot(ctx context.Context, orgID string) (contextfabric.ProjectionBatch, bool, error) {
	var all []candidate
	oversized := false
	for _, table := range entityTables {
		rows, truncated, err := table.query(ctx, s.client, orgID, cursorState{}, snapshotPerQueryCap)
		if err != nil {
			return contextfabric.ProjectionBatch{}, false, fmt.Errorf("%w: read %s: %v", contextfabric.ErrUnavailable, table.name, err)
		}
		if truncated {
			oversized = true
		}
		all = append(all, rows...)
	}
	if !oversized {
		entities, relationships, tombstones := candidateCounts(all)
		// +1: every non-oversized path below adds one organization entity
		// candidate before calling buildBatch: this checks against exactly
		// what buildBatch is about to validate, not what's in all right now.
		if entities+1 > contractsv1.ContextFabricProjectionBatchMaxEntities || relationships > contractsv1.ContextFabricProjectionBatchMaxRelationships || tombstones > contractsv1.ContextFabricProjectionBatchMaxTombstones {
			oversized = true
		}
	}
	if oversized {
		return s.pagedBatch(ctx, orgID, "", cursorState{}, true)
	}
	// A fixed anchor, not the wall clock: the organization candidate must
	// sort identically across replays of the same underlying data so that
	// deterministicBatchID stays idempotent (ApplyProjectionBatch's
	// idempotency contract). It sorts before every real Dev Health
	// timestamp we ever project.
	all = append(all, organizationCandidate(orgID, organizationAnchorTime))
	if len(all) == 0 {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	sortCandidates(all)
	batch, err := buildBatch(orgID, SourceName, ClickHouseSourceVersion, "", all, true, true, s.clock())
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	return batch, true, nil
}

func (s *ClickHouseProjectionSource) incremental(ctx context.Context, orgID, cursor string) (contextfabric.ProjectionBatch, bool, error) {
	state, err := decodeCursor(cursor)
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	return s.pagedBatch(ctx, orgID, cursor, state, false)
}

// pagedBatch is the shared bounded-per-tick paging path for both ordinary
// incremental catch-up and the fullSnapshot oversized-organization
// fallback (C6). includeOrganization is true only for the very first page
// of a from-scratch catch-up (cursor == ""), so the synthesized
// Organization entity is projected exactly once, not on every page.
func (s *ClickHouseProjectionSource) pagedBatch(ctx context.Context, orgID, cursor string, state cursorState, includeOrganization bool) (contextfabric.ProjectionBatch, bool, error) {
	var all []candidate
	for _, table := range entityTables {
		rows, _, err := table.query(ctx, s.client, orgID, state, incrementalBatchCap)
		if err != nil {
			return contextfabric.ProjectionBatch{}, false, fmt.Errorf("%w: read %s: %v", contextfabric.ErrUnavailable, table.name, err)
		}
		all = append(all, rows...)
	}
	if includeOrganization {
		all = append(all, organizationCandidate(orgID, organizationAnchorTime))
	}
	if len(all) == 0 {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	sortCandidates(all)
	all = truncateToCompleteRows(all, incrementalBatchCap)
	if len(all) == 0 {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	batch, err := buildBatch(orgID, SourceName, ClickHouseSourceVersion, cursor, all, false, false, s.clock())
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	return batch, true, nil
}

// candidateCounts tallies candidates by kind -- CHAOS-3753 codex round-2
// finding K4 -- so a caller can check an aggregate count against the v1
// contract's per-batch bounds before calling buildBatch. devhealthsource's
// ClickHouse tables never produce episode or content candidates (those
// come from EpisodesProjectionSource / not implemented here), so this
// only needs to report the three kinds this file actually builds.
func candidateCounts(all []candidate) (entities, relationships, tombstones int) {
	for _, c := range all {
		switch {
		case c.entity != nil:
			entities++
		case c.relationship != nil:
			relationships++
		case c.tombstone != nil:
			tombstones++
		}
	}
	return entities, relationships, tombstones
}

func (s *ClickHouseProjectionSource) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func sortCandidates(all []candidate) {
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].observedAt.Equal(all[j].observedAt) {
			return all[i].observedAt.Before(all[j].observedAt)
		}
		return all[i].sortKey < all[j].sortKey
	})
}

// truncateToCompleteRows caps all (already sorted by sortCandidates) at
// maxRows source rows, never splitting one row's candidates across the cut
// -- CHAOS-3753 codex round-2 finding K2. A single ClickHouse row can
// scan into more than one candidate (an entity plus its
// BELONGS_TO_REPOSITORY relationship; see fetch's doc comment in
// tables.go), and every candidate from one row shares the exact same
// (observedAt, sortKey) pair by convention, so after a stable sort they
// are always contiguous. The previous version sliced the flattened
// candidate list at a fixed candidate-count index, which could land
// inside such a group: the entity would be emitted, the relationship
// silently dropped, and -- because the emitted entity became the batch's
// last candidate, and therefore its NextCursor position -- the dropped
// relationship's row would never be revisited by any later page either
// (the next page's strict "since"/"after" predicate treats that row as
// already seen). Capping by whole rows instead means a row that doesn't
// fully fit is deferred, unsplit, to the next page: the cursor only ever
// advances past a row once every one of its candidates has been emitted.
func truncateToCompleteRows(all []candidate, maxRows int) []candidate {
	if maxRows <= 0 {
		return nil
	}
	rows := 0
	for i, c := range all {
		if i == 0 || !c.observedAt.Equal(all[i-1].observedAt) || c.sortKey != all[i-1].sortKey {
			rows++
			if rows > maxRows {
				return all[:i]
			}
		}
	}
	return all
}

func buildBatch(orgID, source, version, cursor string, all []candidate, fullSnapshot, completeEnumeration bool, generatedAt time.Time) (contextfabric.ProjectionBatch, error) {
	last := all[len(all)-1]
	nextCursor, err := encodeCursor(cursorState{Since: last.observedAt, After: last.sortKey})
	if err != nil {
		return contextfabric.ProjectionBatch{}, err
	}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: source, SourceVersion: version,
		Cursor: cursor, NextCursor: nextCursor, GeneratedAt: generatedAt,
		FullSnapshot: fullSnapshot, CompleteEnumeration: completeEnumeration,
		Entities: []contractsv1.ContextFabricEntityProjection{}, Relationships: []contractsv1.ContextFabricRelationshipProjection{},
		Contents: []contractsv1.ContextFabricContentProjection{}, Episodes: []contractsv1.ContextFabricEpisodeProjection{},
		Tombstones: []contractsv1.ContextFabricProjectionTombstone{},
	}
	for _, c := range all {
		switch {
		case c.entity != nil:
			batch.Entities = append(batch.Entities, *c.entity)
		case c.relationship != nil:
			batch.Relationships = append(batch.Relationships, *c.relationship)
		case c.episode != nil:
			batch.Episodes = append(batch.Episodes, *c.episode)
		case c.tombstone != nil:
			batch.Tombstones = append(batch.Tombstones, *c.tombstone)
		}
	}
	batch.BatchID = deterministicBatchID(orgID, source, cursor, nextCursor)
	if err := batch.Validate(); err != nil {
		return contextfabric.ProjectionBatch{}, fmt.Errorf("devhealthsource: built an invalid projection batch: %w", err)
	}
	return batch, nil
}

// deterministicBatchID makes ApplyProjectionBatch idempotent for replay: the
// same (org, source, cursor range) always yields the same batch ID.
func deterministicBatchID(orgID, source, cursor, nextCursor string) string {
	sum := sha256.Sum256([]byte(orgID + "\x00" + source + "\x00" + cursor + "\x00" + nextCursor))
	return "batch_" + hex.EncodeToString(sum[:16])
}

// organizationScopePrefix reserves a namespace inside
// ContextFabricAuthorizationScope.ProjectIDs for the synthesized
// organization-level entity below. ContextFabricAuthorizationScope has no
// dedicated organization field -- ACR derives one graph per organization
// already (ADR 0007), so this is defense in depth, not the primary
// isolation boundary -- but a real project ID that happened to equal this
// exact string would incorrectly inherit organization-wide authorization,
// so no other producer may ever emit a ProjectIDs value in this namespace.
// devhealthsource is the only producer today (queryRepositories,
// queryWorkItems, queryPullRequests, queryDeployments, queryIncidents,
// queryWorkItemDependencies, queryDeploymentIncidentEdges all populate
// Authorization via repoAuthorization, which sets RepositorySlugs, never
// ProjectIDs -- proved by TestOnlyTheOrganizationEntityPopulatesProjectIDs).
// The still-unimplemented TeamsProjectsSource (teams_projects.go) is the
// one future producer that WILL need to populate real ProjectIDs; it MUST
// call IsReservedAuthorizationScopeID on every value first and reject (not
// silently rename) any collision.
//
// This is no longer convention-only (CHAOS-3753 codex finding W2): both
// the prefix and the membership check now come straight from
// internal/contracts/v1 (ContextFabricReservedOrganizationScopePrefix /
// ContextFabricIsReservedOrganizationScopeID), which
// ContextFabricEntityProjection.Validate() et al. enforce at the contract
// boundary -- any non-organization producer that forgot the doc-comment
// obligation above now fails batch.Validate() outright, not just this
// package's own review discipline. See
// docs/design/context-fabric-projection-worker.md for the residual risk
// (a dedicated per-kind organization-scope field) still flagged for Reset
// 1B/1C's org-level authorization design.
const organizationScopePrefix = contractsv1.ContextFabricReservedOrganizationScopePrefix

// IsReservedAuthorizationScopeID reports whether id falls inside the
// reserved organization-scope namespace. Exported so any future canonical
// project/team producer (in this package or elsewhere) can guard against
// emitting a colliding real ID -- see organizationScopePrefix.
func IsReservedAuthorizationScopeID(id string) bool {
	return contractsv1.ContextFabricIsReservedOrganizationScopeID(id)
}

func organizationScopeID(orgID string) string { return organizationScopePrefix + orgID }

func organizationCandidate(orgID string, observedAt time.Time) candidate {
	entity := contractsv1.ContextFabricEntityProjection{
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectOrganization, CanonicalID: "organization:" + orgID, Label: orgID},
		// No canonical organization-name table exists in this repository yet
		// (see docs/design/context-fabric-projection-worker.md); the org ID
		// is the best available label until Dev Health Ops exposes one.
		Authorization:  contractsv1.ContextFabricAuthorizationScope{ProjectIDs: []string{organizationScopeID(orgID)}},
		EvidenceRefIDs: []string{"acr:v1:organization:" + orgID}, ObservedAt: observedAt, SourceVersion: ClickHouseSourceVersion,
	}
	return candidate{observedAt: observedAt, sortKey: entity.Subject.CanonicalID, entity: &entity}
}

func redactOrg(orgID string) string {
	sum := sha256.Sum256([]byte(orgID))
	return hex.EncodeToString(sum[:6])
}

func stringScalar(value string) contractsv1.ContextFabricScalarValue {
	return contractsv1.ContextFabricScalarValue{String: &value}
}

// zeroRepositoryID is the placeholder work_items.repo_id carries for a
// source row that is repo-less BY DESIGN (CHAOS-3785) -- a Linear issue is
// not tied to a single git repo, so Linear ingest writes this exact value
// rather than leaving repo_id null. It is what distinguishes
// workItemAuthorization's two non-repo cases from each other: this value
// means "never had a repository," anything else means "named one that
// didn't resolve" (see orphanedRepositorySentinel).
const zeroRepositoryID = "00000000-0000-0000-0000-000000000000"

// noRepositorySentinel and orphanedRepositorySentinel (CHAOS-3785;
// orphanedRepositorySentinel is codex round-1 finding F2) are the
// RepositorySlugs values workItemAuthorization uses for a work item whose
// LEFT JOIN to repos found no match. ContextFabricAuthorizationScope.
// Validate() rejects an empty scope outright ("authorization scope must not
// be empty"), so neither case can fall back to a bare empty scope --
// confirmed early against real ClickHouse output that doing so would fail
// batch.Validate() and take down projection for the whole organization, not
// just the affected rows. The reserved organization-scope ProjectIDs
// namespace (organizationScopePrefix) is not an option either: it is
// exclusively for the synthesized Organization entity --
// TestOnlyTheOrganizationEntityPopulatesProjectIDs proves nothing else may
// populate it.
//
// The two sentinels stay DISTINCT rather than collapsing to one "no repo"
// value: work_items.repo_id = zeroRepositoryID is repo-less by design
// (every Linear-sourced row), but a work item can also carry a genuine,
// nonzero repo_id that simply never resolves against repos -- a sync race,
// a deleted repository, or (live-verified: 5 such rows across 5
// organizations in dev ClickHouse today, all pre-existing CHAOS-2698 test
// fixture data) stale seed data. That second case is a data-quality signal
// worth surfacing and counting on its own, not one that should silently
// masquerade as an intentionally repo-less Linear item.
//
// Both sentinels share the same authorization consequence: a principal
// scoped to specific repositories (storage.Principal.RepositoryScopes)
// never matches either value and so never sees these rows, while an
// org-wide/unrestricted principal does (graphrank.AuthorizedAttributes only
// gates on RepositorySlugs when the principal or the request actually names
// one). Neither value can collide with a real Dev Health repo slug
// ("owner/repo" shaped, always contains '/').
const (
	noRepositorySentinel       = "acr-context-fabric:no-repository"
	orphanedRepositorySentinel = "acr-context-fabric:orphaned-repository"
)

// repoAuthorization is every OTHER producer's authorization builder
// (queryPullRequests, queryDeployments, queryIncidents,
// queryPullRequestReviews, queryCIRuns, queryDeploymentIncidentEdges): each
// still INNER JOINs repos, so repoSlug is always non-empty here. CHAOS-3785's
// three work-item producers (queryWorkItems, queryWorkItemDependencies,
// queryWorkItemHierarchy), whose LEFT JOIN can legitimately find no repos
// match, route through workItemAuthorization below instead.
func repoAuthorization(repoSlug string) contractsv1.ContextFabricAuthorizationScope {
	return contractsv1.ContextFabricAuthorizationScope{RepositorySlugs: []string{repoSlug}}
}

// workItemAuthorization is queryWorkItems / queryWorkItemDependencies /
// queryWorkItemHierarchy's authorization builder -- see the sentinel
// constants' doc comment above for why the zeroRepositoryID/orphan split
// exists. repoID is the row's own (or, for a dependency/hierarchy edge, its
// source/child work item's own) work_items.repo_id; repoSlug is what the
// LEFT JOIN to repos actually resolved, ” when it found no match.
func workItemAuthorization(repoID, repoSlug string) contractsv1.ContextFabricAuthorizationScope {
	if repoSlug != "" {
		return repoAuthorization(repoSlug)
	}
	if repoID == zeroRepositoryID {
		return contractsv1.ContextFabricAuthorizationScope{RepositorySlugs: []string{noRepositorySentinel}}
	}
	return contractsv1.ContextFabricAuthorizationScope{RepositorySlugs: []string{orphanedRepositorySentinel}}
}

// belongsToRepository's rowKey must equal the exact same value the entity
// candidate for this same source row used as its sortKey (see tables.go's
// sincePredicate doc comment) -- CHAOS-3753 codex finding C5. Before the
// fix this used the relationship's own RelationshipID, which is a
// different string than the row's keyset-pagination identity, so a page
// boundary landing between an entity and its relationship candidate (both
// from the same row, sharing one timestamp) would have resumed from the
// wrong position.
func belongsToRepository(from contractsv1.ContextFabricSubjectRef, repoSlug, repoID string, observedAt time.Time, evidenceRefID, rowKey string) candidate {
	to := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:" + repoID, Label: repoSlug}
	relationship := contractsv1.ContextFabricRelationshipProjection{
		RelationshipID: "relationship:belongs_to_repository:" + from.CanonicalID, Type: "BELONGS_TO_REPOSITORY", From: from, To: to,
		Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
		Authorization: repoAuthorization(repoSlug), EvidenceRefIDs: []string{evidenceRefID}, ObservedAt: observedAt, SourceVersion: ClickHouseSourceVersion,
	}
	return candidate{observedAt: observedAt, sortKey: rowKey, relationship: &relationship}
}
