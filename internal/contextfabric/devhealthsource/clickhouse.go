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

const sourceVersion = "devhealthsource.clickhouse.v1"

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

func (s *ClickHouseProjectionSource) fullSnapshot(ctx context.Context, orgID string) (contextfabric.ProjectionBatch, bool, error) {
	var all []candidate
	for _, table := range entityTables {
		rows, truncated, err := table.query(ctx, s.client, orgID, cursorState{}, snapshotPerQueryCap)
		if err != nil {
			return contextfabric.ProjectionBatch{}, false, fmt.Errorf("%w: read %s: %v", contextfabric.ErrUnavailable, table.name, err)
		}
		if truncated {
			return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: organization %s exceeds full-snapshot capacity for %s; incremental catch-up is required instead of a rebuild at this scale", redactOrg(orgID), table.name)
		}
		all = append(all, rows...)
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
	batch, err := buildBatch(orgID, SourceName, sourceVersion, "", all, true, true, s.clock())
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
	var all []candidate
	for _, table := range entityTables {
		rows, _, err := table.query(ctx, s.client, orgID, state, incrementalBatchCap)
		if err != nil {
			return contextfabric.ProjectionBatch{}, false, fmt.Errorf("%w: read %s: %v", contextfabric.ErrUnavailable, table.name, err)
		}
		all = append(all, rows...)
	}
	if len(all) == 0 {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	sortCandidates(all)
	if len(all) > incrementalBatchCap {
		all = all[:incrementalBatchCap]
	}
	batch, err := buildBatch(orgID, SourceName, sourceVersion, cursor, all, false, false, s.clock())
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	return batch, true, nil
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

func organizationCandidate(orgID string, observedAt time.Time) candidate {
	entity := contractsv1.ContextFabricEntityProjection{
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectOrganization, CanonicalID: "organization:" + orgID, Label: orgID},
		// No canonical organization-name table exists in this repository yet
		// (see docs/design/context-fabric-projection-worker.md); the org ID
		// is the best available label until Dev Health Ops exposes one.
		Authorization:  contractsv1.ContextFabricAuthorizationScope{ProjectIDs: []string{"org:" + orgID}},
		EvidenceRefIDs: []string{"acr:v1:organization:" + orgID}, ObservedAt: observedAt, SourceVersion: sourceVersion,
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

func repoAuthorization(repoSlug string) contractsv1.ContextFabricAuthorizationScope {
	return contractsv1.ContextFabricAuthorizationScope{RepositorySlugs: []string{repoSlug}}
}

func belongsToRepository(from contractsv1.ContextFabricSubjectRef, repoSlug, repoID string, observedAt time.Time, evidenceRefID string) candidate {
	to := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:" + repoID, Label: repoSlug}
	relationship := contractsv1.ContextFabricRelationshipProjection{
		RelationshipID: "relationship:belongs_to_repository:" + from.CanonicalID, Type: "BELONGS_TO_REPOSITORY", From: from, To: to,
		Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
		Authorization: repoAuthorization(repoSlug), EvidenceRefIDs: []string{evidenceRefID}, ObservedAt: observedAt, SourceVersion: sourceVersion,
	}
	return candidate{observedAt: observedAt, sortKey: relationship.RelationshipID, relationship: &relationship}
}
