package devhealthsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// SourceName is the canonical Source value ClickHouseProjectionSource writes
// onto every batch it produces. Checkpoints and telemetry are keyed by it.
const SourceName = "dev_health_clickhouse"

// ClickHouseSourceVersion is bumped to v6 by CHAOS-3916 (CHAOS-3898's own
// deploy-gating requirement, local/trial slice): CHAOS-3898 changed
// projected identity formats (work_item.v2, relationship.v2 digest ids,
// project.v2:<provider>:<id> via identity.Derive) WITHOUT a version bump --
// CHAOS-3916's own finding was that the moment a binary carrying those
// formats projects incrementally against a still-v5, flag-off graph, NEW-
// and OLD-format ids coexist in the SAME graph: mixed-format edges,
// duplicate nodes. Every already-projected organization (this repo's own
// standing stack included -- live-verified zero project.v2:-shaped nodes
// anywhere in the ground-truth org, CHAOS-4108's own re-projection decision
// matrix) is still v5/pre-.v2 today. Forcing
// ErrProjectionSourceVersionChanged makes the operator-prescribed rebuild
// (acr-projector rebuild --org, or the ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED
// build-aside-and-swap path this bump is sequenced with -- see that flag's
// own doc comment, pglifecycle/env.go) happen deliberately, the SAME
// discipline v2-v5 below already established for their own identity/schema
// changes, rather than leaving mixed-format state to accumulate silently.
// This bump alone changes nothing until an operator actually runs a
// rebuild -- CHAOS-3916's local/trial slice merges this bump and enables
// the lifecycle flag in the trial harness WITHOUT triggering any rebuild;
// prod cutover timing (which organizations, when) is chris-owned per
// CHAOS-3916's own text.
//
// ClickHouseSourceVersion was bumped to v5 by CHAOS-3833 (embed-text spec v2
// §2/§4 Layer A): the producers emit the per-kind embed-text fields --
// work_items gains the first-colon ticket-key ALIAS plus type/labels/
// project_name/native_team_key properties, git_pull_requests gains
// number/repo/branch and the 1,200-rune body head, deployments gain
// release_ref/repo, ci_pipeline_runs gain pipeline_name/repo,
// git_pull_request_reviews gain the joined PR title/number/repo, incidents
// gain the 800-rune description head, and repos gain parsed tags. Aliases
// and properties feed the shared search-text composition, so
// already-projected graphs hold nodes whose text (and vectors) no
// identical-looking recomposition could reproduce; the bump forces
// ErrProjectionSourceVersionChanged until the operator-prescribed
// `acr-projector rebuild --org` reprojects (and re-embeds) every row.
//
// (v4, CHAOS-3781: every producer in
// tables.go now emits a VALID-TIME window (ValidFrom/ValidTo) derived from
// its source row's own immutable interval columns -- see validity.go for
// the mapping and the half-open convention.
//
// The bump is required, not cosmetic. Before this change no canonical
// entity or relationship carried a validity window at all, so an
// already-projected organization's graph holds nodes and edges whose
// windows are ABSENT. The read side admits an unbounded element at EVERY
// requested time (falkorgraph, and see AC-3781-4), so those window-less
// rows would answer a historical question as though they had always been
// true -- precisely the false historical answer this issue exists to
// remove. Forcing ErrProjectionSourceVersionChanged makes the
// operator-prescribed rebuild (acr-projector rebuild --org) happen
// deliberately; only a rebuilt graph can honestly answer on a non-current
// axis.)
//
// (v3, CHAOS-3785: queryWorkItems,
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
// this fix exists to help.)
//
// (v2, CHAOS-3779 codex round-2 H2 residual: queryWorkItemDependencies'
// RelationshipID began embedding relationship_type (previously (source,
// target) only), and queryWorkItemHierarchy was a new producer.)
const ClickHouseSourceVersion = "devhealthsource.clickhouse.v6"

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
	logger *slog.Logger
}

func NewClickHouseProjectionSource(client contextpacket.ClickHouseQueryClient) (*ClickHouseProjectionSource, error) {
	if client == nil {
		return nil, fmt.Errorf("devhealthsource: clickhouse query client is required")
	}
	return &ClickHouseProjectionSource{client: client, now: time.Now, logger: slog.Default()}, nil
}

// WithLogger overrides the default logger (slog.Default()) with one the
// caller actually wires to its output (CHAOS-3785 codex round-2 finding
// R2-3): logOrphanedWorkItems needs somewhere to surface a per-batch
// orphaned-work-item count, or that data-quality signal requires graph
// spelunking to notice. Optional and additive on purpose -- every existing
// call site keeps building a source exactly as before; only
// cmd/acr-projector wires a real logger in, matching how
// projectionrun.Coordinator's own Logger field works (a real logger from
// the caller, slog.Default() otherwise). Returns s for chaining; a nil
// logger is a no-op, not a panic.
func (s *ClickHouseProjectionSource) WithLogger(logger *slog.Logger) *ClickHouseProjectionSource {
	if logger != nil {
		s.logger = logger
	}
	return s
}

// candidate is a sortable, already-built projection item. At most one of
// entity, relationship, episode, or tombstone is set; a candidate with NONE
// of them is a progress marker (see progressCandidate).
type candidate struct {
	observedAt   time.Time
	sortKey      string
	entity       *contractsv1.ContextFabricEntityProjection
	relationship *contractsv1.ContextFabricRelationshipProjection
	episode      *contractsv1.ContextFabricEpisodeProjection
	tombstone    *contractsv1.ContextFabricProjectionTombstone
}

func (s *ClickHouseProjectionSource) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	if s == nil {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: source is not configured")
	}
	return s.plan().nextBatch(ctx, checkpoint)
}

// CurrentProjectionSourceVersion implements contextfabric.ProjectionSourceVersion
// (CHAOS-3887) so a per-tick freshness signal can be computed for a dormant
// organization -- one with no new ClickHouse rows since its last checkpoint,
// so NextProjectionBatch reports available=false and never builds a
// ProjectionBatch to read a current SourceVersion from.
func (s *ClickHouseProjectionSource) CurrentProjectionSourceVersion() string {
	return ClickHouseSourceVersion
}

// plan binds this source's data (its table set, batch identity, the
// synthesized Organization seed, and the orphaned-work-item observer) to the
// shared assembly engine in assemble.go. The paging/truncation/oversized
// rules themselves live there and are shared verbatim with
// TeamsProjectsSource -- see sourcePlan's doc comment for why they are not
// re-derived per source.
func (s *ClickHouseProjectionSource) plan() sourcePlan {
	return sourcePlan{
		client:  s.client,
		source:  SourceName,
		version: ClickHouseSourceVersion,
		tables:  entityTables,
		now:     s.now,
		seed: func(orgID string) []candidate {
			// A fixed anchor, not the wall clock: the organization candidate
			// must sort identically across replays of the same underlying
			// data so that deterministicBatchID stays idempotent
			// (ApplyProjectionBatch's idempotency contract). It sorts before
			// every real Dev Health timestamp we ever project.
			return []candidate{organizationCandidate(orgID, organizationAnchorTime)}
		},
		observe: s.logOrphanedWorkItems,
	}
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

// orphanedWorkItemIDs returns the DISTINCT work_item canonical IDs
// workItemAuthorization marked orphaned in this batch -- a genuine, nonzero
// repo_id that never resolved against repos (CHAOS-3785 codex round-2
// finding R2-3), as opposed to noRepositorySentinel's "never had one by
// design." CHAOS-3785 codex round-3 finding R3-4: this counts WORK ITEMS,
// not candidates -- one orphaned item can carry any number of
// queryWorkItemDependencies/queryWorkItemHierarchy edges (its own entity
// candidate plus one relationship candidate per edge), and counting
// candidates would report that single item as however many edges it
// happens to have. Every orphan-scoped relationship's From endpoint is the
// orphaned work item that produced it (queryWorkItemDependencies derives a
// dependency edge's scope from its source_work_item_id's own repo;
// queryWorkItemHierarchy derives a PART_OF edge's scope from its child's),
// so entity and relationship candidates both resolve to the same ID space
// and de-duplicate against each other correctly. RepositorySlugs is checked
// as an exact single-element match (never a substring or prefix test):
// orphanedRepositorySentinel can only ever appear as workItemAuthorization's
// sole entry, so anything looser would risk matching a real repository slug
// that happened to contain the same text.
func orphanedWorkItemIDs(all []candidate) map[string]struct{} {
	isOrphaned := func(scope contractsv1.ContextFabricAuthorizationScope) bool {
		return len(scope.RepositorySlugs) == 1 && scope.RepositorySlugs[0] == orphanedRepositorySentinel
	}
	ids := map[string]struct{}{}
	for _, c := range all {
		switch {
		case c.entity != nil && isOrphaned(c.entity.Authorization):
			ids[c.entity.Subject.CanonicalID] = struct{}{}
		case c.relationship != nil && isOrphaned(c.relationship.Authorization):
			ids[c.relationship.From.CanonicalID] = struct{}{}
		}
	}
	return ids
}

// logOrphanedWorkItems surfaces orphanedWorkItemIDs as a per-batch log line
// -- CHAOS-3785 codex round-2 finding R2-3: without this, an orphaned work
// item (a nonzero repo_id that never resolved -- a sync race, a deleted
// repository, stale seed data) is indistinguishable from any other
// projected row unless someone goes looking for the sentinel value directly
// in the graph. Logged only when the count is positive, matching this
// signal's actual purpose (surfacing something worth investigating), not as
// an always-on per-tick line for the common all-zero case. batch_id/source/
// cursor (codex round-3 finding R3-3) match the vocabulary
// projectionrun.Coordinator's own per-run "projection batch applied" log
// already uses (coordinator.go), so an operator can correlate this WARN --
// emitted here, before the coordinator's Apply step even runs -- with that
// later line by batch_id, or notice its batch never reached one at all.
func (s *ClickHouseProjectionSource) logOrphanedWorkItems(ctx context.Context, batch contextfabric.ProjectionBatch, all []candidate) {
	if s.logger == nil {
		return
	}
	if ids := orphanedWorkItemIDs(all); len(ids) > 0 {
		s.logger.WarnContext(ctx, "devhealthsource projection batch contains orphaned work items",
			"org_id", redactOrg(batch.OrgID), "source", batch.Source, "batch_id", batch.BatchID,
			"cursor", batch.Cursor, "next_cursor", batch.NextCursor, "orphaned_work_items", len(ids))
	}
}

// progressCandidate represents a source row that was CONSUMED but emits
// nothing -- today, an ownership row omitted for an ambiguous project_key
// (queryProjectTeams). It carries the row's keyset identity and no payload,
// so buildBatch's switch appends it to nothing while buildBatch's cursor
// arithmetic still advances past it.
//
// This exists because omission happens in the scan, AFTER the raw-row limit
// (fetch), so an omitted row spends page budget. Returning no candidate at
// all for such a row made a page of them produce an EMPTY candidate set;
// pagedBatch then returned available=false without building a batch, so the
// cursor never moved and the next tick re-read, re-omitted and re-stopped on
// the same page forever -- reporting "caught up" while every valid row beyond
// the block stayed unreachable (CHAOS-3802 codex round-2 F1). Cursor
// advancement must follow raw rows consumed, never candidates emitted.
func progressCandidate(observedAt time.Time, sortKey string) candidate {
	return candidate{observedAt: observedAt, sortKey: sortKey}
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
//
// validFrom/validTo are the CHILD entity's own validity window
// (CHAOS-3781). The repository endpoint contributes no bound of its own:
// repos records no deletion column, so a repository's window is
// open-ended, and its created_at necessarily precedes anything it
// contains. Intersecting with an open-ended, earlier-starting interval
// leaves the child's window unchanged, so passing the child's window
// straight through IS the edge intersection here, not a shortcut around
// it. A membership therefore stops being valid exactly when the member
// does.
func belongsToRepository(from contractsv1.ContextFabricSubjectRef, repoSlug, repoID string, observedAt time.Time, evidenceRefID, rowKey string, validFrom, validTo *time.Time) candidate {
	to := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:" + repoID, Label: repoSlug}
	relationship := contractsv1.ContextFabricRelationshipProjection{
		RelationshipID: "relationship:belongs_to_repository:" + from.CanonicalID, Type: "BELONGS_TO_REPOSITORY", From: from, To: to,
		Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
		Authorization: repoAuthorization(repoSlug), EvidenceRefIDs: []string{evidenceRefID}, ObservedAt: observedAt,
		ValidFrom: validFrom, ValidTo: validTo, SourceVersion: ClickHouseSourceVersion,
	}
	return candidate{observedAt: observedAt, sortKey: rowKey, relationship: &relationship}
}
