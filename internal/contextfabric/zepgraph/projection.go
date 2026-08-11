package zepgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	zep "github.com/getzep/zep-go/v3"
)

type Adapter struct {
	api    api
	config Config
	now    func() time.Time
}

func New(config Config) (*Adapter, error) {
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 30 * time.Second
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = 3
	}
	if config.MaxResults == 0 {
		config.MaxResults = 25
	}
	if strings.TrimSpace(config.GraphPrefix) == "" {
		config.GraphPrefix = "acr-cf"
	}
	client, err := newSDKAPI(config)
	if err != nil {
		return nil, err
	}
	return newWithAPI(config, client)
}

func newWithAPI(config Config, client api) (*Adapter, error) {
	if client == nil {
		return nil, errors.New("zep graph API is required")
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 30 * time.Second
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = 3
	}
	if config.MaxResults == 0 {
		config.MaxResults = 25
	}
	if strings.TrimSpace(config.GraphPrefix) == "" {
		config.GraphPrefix = "acr-cf"
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Adapter{api: client, config: config, now: time.Now}, nil
}

func (a *Adapter) ApplyProjectionBatch(ctx context.Context, batch contextfabric.ProjectionBatch) (contextfabric.ProjectionReceipt, error) {
	if err := batch.Validate(); err != nil {
		return contextfabric.ProjectionReceipt{}, fmt.Errorf("projection batch: %w", err)
	}
	if err := a.ensureGraph(ctx, batch.OrgID); err != nil {
		return contextfabric.ProjectionReceipt{}, err
	}
	for _, entity := range batch.Entities {
		if err := a.projectEntity(ctx, batch.OrgID, entity); err != nil {
			return contextfabric.ProjectionReceipt{}, err
		}
	}
	for _, relationship := range batch.Relationships {
		if err := a.projectRelationship(ctx, batch.OrgID, relationship); err != nil {
			return contextfabric.ProjectionReceipt{}, err
		}
	}
	for _, content := range batch.Contents {
		if err := a.projectContent(ctx, batch.OrgID, content); err != nil {
			return contextfabric.ProjectionReceipt{}, err
		}
	}
	for _, episode := range batch.Episodes {
		if err := a.projectEpisode(ctx, batch.OrgID, episode); err != nil {
			return contextfabric.ProjectionReceipt{}, err
		}
	}
	for _, tombstone := range batch.Tombstones {
		if err := a.applyTombstone(ctx, batch.OrgID, tombstone); err != nil {
			return contextfabric.ProjectionReceipt{}, err
		}
	}
	watermark := projectionWatermark(batch)
	if err := a.writeWatermark(ctx, batch, watermark); err != nil {
		return contextfabric.ProjectionReceipt{}, err
	}
	return contextfabric.ProjectionReceipt{
		BatchID: batch.BatchID, AppliedAt: a.now().UTC(), BackendWatermark: watermark,
		EntitiesApplied: len(batch.Entities), EdgesApplied: len(batch.Relationships),
		ContentsApplied: len(batch.Contents), EpisodesApplied: len(batch.Episodes),
		TombstonesApplied: len(batch.Tombstones),
	}, nil
}

func (a *Adapter) ProjectionWatermark(ctx context.Context, orgID, source string) (contextfabric.ProjectionWatermark, error) {
	if strings.TrimSpace(orgID) == "" || strings.TrimSpace(source) == "" {
		return contextfabric.ProjectionWatermark{}, errors.New("organization and source are required")
	}
	node, err := a.api.GetNode(ctx, nodeUUID(orgID, markerSubject(source)))
	if err != nil {
		return contextfabric.ProjectionWatermark{}, safeDependencyError("read projection watermark", err)
	}
	attributes := node.Attributes
	projectedAt, err := parseTimeAttribute(attributes, "projected_at")
	if err != nil {
		return contextfabric.ProjectionWatermark{}, err
	}
	return contextfabric.ProjectionWatermark{
		OrgID: orgID, Source: source,
		Cursor: stringAttribute(attributes, "cursor"), SourceVersion: stringAttribute(attributes, "source_version"),
		ProjectedAt: projectedAt, BackendWatermark: stringAttribute(attributes, "backend_watermark"),
	}, nil
}

func (a *Adapter) PurgeOrganization(ctx context.Context, orgID string) error {
	if strings.TrimSpace(orgID) == "" {
		return errors.New("organization is required")
	}
	err := a.api.DeleteGraph(ctx, graphID(a.config.GraphPrefix, orgID))
	if zepStatusCode(err) == 404 {
		return nil
	}
	return safeDependencyError("purge organization graph", err)
}

func (a *Adapter) ensureGraph(ctx context.Context, orgID string) error {
	id := graphID(a.config.GraphPrefix, orgID)
	if _, err := a.api.GetGraph(ctx, id); err == nil {
		return nil
	} else if zepStatusCode(err) != 404 {
		return safeDependencyError("get organization graph", err)
	}
	name := "ACR Context Fabric"
	description := "Server-owned Context Fabric graph for one organization."
	_, err := a.api.CreateGraph(ctx, &zep.CreateGraphRequest{GraphID: id, Name: &name, Description: &description})
	if zepStatusCode(err) == 409 {
		return nil
	}
	return safeDependencyError("create organization graph", err)
}

func (a *Adapter) projectEntity(ctx context.Context, orgID string, entity contextfabric.EntityProjection) error {
	root := organizationRoot(orgID)
	request := &zep.AddTripleRequest{
		GraphID: ptr(graphID(a.config.GraphPrefix, orgID)), Fact: "Organization contains " + entity.Subject.Label,
		FactName: "HAS_SUBJECT", FactUUID: ptr(relationshipUUID(orgID, "organization-root:"+subjectKey(entity.Subject))),
		SourceNodeUUID: ptr(nodeUUID(orgID, root)), SourceNodeName: ptr(root.Label), SourceNodeLabels: []string{zepLabel(root.Kind)},
		SourceNodeAttributes: subjectAttributes(root, contextfabric.AuthorizationScope{}, nil, time.Time{}, nil, nil, "system"),
		TargetNodeUUID:       ptr(nodeUUID(orgID, entity.Subject)), TargetNodeName: ptr(entity.Subject.Label), TargetNodeLabels: []string{zepLabel(entity.Subject.Kind)},
		TargetNodeSummary: ptr(entitySearchSummary(entity)), TargetNodeAttributes: projectionEntityAttributes(entity),
		CreatedAt: ptr(entity.ObservedAt.UTC().Format(time.RFC3339Nano)),
	}
	_, err := a.api.AddFactTriple(ctx, request)
	return safeDependencyError("project entity", err)
}

func (a *Adapter) projectRelationship(ctx context.Context, orgID string, relationship contextfabric.RelationshipProjection) error {
	request := &zep.AddTripleRequest{
		GraphID: ptr(graphID(a.config.GraphPrefix, orgID)), Fact: relationshipFact(relationship), FactName: normalizeRelation(relationship.Type),
		FactUUID:       ptr(relationshipUUID(orgID, relationship.RelationshipID)),
		SourceNodeUUID: ptr(nodeUUID(orgID, relationship.From)), SourceNodeName: ptr(relationship.From.Label), SourceNodeLabels: []string{zepLabel(relationship.From.Kind)},
		SourceNodeAttributes: subjectAttributes(relationship.From, relationship.Authorization, relationship.EvidenceRefIDs, relationship.ObservedAt, relationship.ValidFrom, relationship.ValidTo, relationship.SourceVersion),
		TargetNodeUUID:       ptr(nodeUUID(orgID, relationship.To)), TargetNodeName: ptr(relationship.To.Label), TargetNodeLabels: []string{zepLabel(relationship.To.Kind)},
		TargetNodeAttributes: subjectAttributes(relationship.To, relationship.Authorization, relationship.EvidenceRefIDs, relationship.ObservedAt, relationship.ValidFrom, relationship.ValidTo, relationship.SourceVersion),
		EdgeAttributes:       projectionRelationshipAttributes(relationship),
		CreatedAt:            ptr(relationship.ObservedAt.UTC().Format(time.RFC3339Nano)),
	}
	if relationship.ValidFrom != nil {
		request.ValidAt = ptr(relationship.ValidFrom.UTC().Format(time.RFC3339Nano))
	}
	if relationship.ValidTo != nil {
		request.InvalidAt = ptr(relationship.ValidTo.UTC().Format(time.RFC3339Nano))
	}
	if relationship.EpistemicStatus == contextfabric.EpistemicSuperseded && relationship.ValidTo != nil {
		request.ExpiredAt = ptr(relationship.ValidTo.UTC().Format(time.RFC3339Nano))
	}
	_, err := a.api.AddFactTriple(ctx, request)
	return safeDependencyError("project relationship", err)
}

func (a *Adapter) projectContent(ctx context.Context, orgID string, content contextfabric.ContentProjection) error {
	request := &zep.AddTripleRequest{
		GraphID: ptr(graphID(a.config.GraphPrefix, orgID)), Fact: content.Subject.Label + " is documented by " + content.Title,
		FactName: "DOCUMENTED_BY", FactUUID: ptr(relationshipUUID(orgID, "content:"+content.ContentID)),
		SourceNodeUUID: ptr(nodeUUID(orgID, content.Subject)), SourceNodeName: ptr(content.Subject.Label), SourceNodeLabels: []string{zepLabel(content.Subject.Kind)},
		SourceNodeAttributes: subjectAttributes(content.Subject, content.Authorization, content.EvidenceRefIDs, content.ObservedAt, nil, nil, content.SourceVersion),
		TargetNodeUUID:       ptr(contentUUID(orgID, "document", content.ContentID)), TargetNodeName: ptr(content.Title), TargetNodeLabels: []string{zepLabel(contextfabric.SubjectDocument)},
		TargetNodeSummary: ptr(content.Body), TargetNodeAttributes: map[string]interface{}{
			"canonical_id": content.ContentID, "subject_kind": string(contextfabric.SubjectDocument), "content_digest": content.ContentDigest,
			"untrusted": true, "authorization_repositories": encodeScope(content.Authorization.RepositorySlugs),
			"authorization_projects": encodeScope(content.Authorization.ProjectIDs), "authorization_teams": encodeScope(content.Authorization.TeamIDs),
			"evidence_refs": encodeScope(content.EvidenceRefIDs), "observed_at": content.ObservedAt.UTC().Format(time.RFC3339Nano), "source_version": content.SourceVersion,
		},
		CreatedAt: ptr(content.ObservedAt.UTC().Format(time.RFC3339Nano)),
	}
	_, err := a.api.AddFactTriple(ctx, request)
	return safeDependencyError("project content", err)
}

func (a *Adapter) projectEpisode(ctx context.Context, orgID string, episode contextfabric.EpisodeProjection) error {
	summary := strings.TrimSpace(episode.Goal + "\nOutcome: " + episode.Outcome + "\n" + episode.Summary)
	request := &zep.AddTripleRequest{
		GraphID: ptr(graphID(a.config.GraphPrefix, orgID)), Fact: episode.Subject.Label + " has episode " + episode.EpisodeID,
		FactName: "HAS_EPISODE", FactUUID: ptr(relationshipUUID(orgID, "episode:"+episode.EpisodeID)),
		SourceNodeUUID: ptr(nodeUUID(orgID, episode.Subject)), SourceNodeName: ptr(episode.Subject.Label), SourceNodeLabels: []string{zepLabel(episode.Subject.Kind)},
		SourceNodeAttributes: subjectAttributes(episode.Subject, episode.Authorization, episode.EvidenceRefIDs, episode.EndedAt, &episode.StartedAt, &episode.EndedAt, episode.SourceVersion),
		TargetNodeUUID:       ptr(contentUUID(orgID, "episode", episode.EpisodeID)), TargetNodeName: ptr(episode.EpisodeID), TargetNodeLabels: []string{zepLabel(contextfabric.SubjectEpisode)},
		TargetNodeSummary: ptr(summary), TargetNodeAttributes: map[string]interface{}{
			"canonical_id": episode.EpisodeID, "subject_kind": string(contextfabric.SubjectEpisode), "goal": episode.Goal,
			"outcome": episode.Outcome, "authorization_repositories": encodeScope(episode.Authorization.RepositorySlugs),
			"authorization_projects": encodeScope(episode.Authorization.ProjectIDs), "authorization_teams": encodeScope(episode.Authorization.TeamIDs),
			"evidence_refs": encodeScope(episode.EvidenceRefIDs), "started_at": episode.StartedAt.UTC().Format(time.RFC3339Nano),
			"ended_at": episode.EndedAt.UTC().Format(time.RFC3339Nano), "source_version": episode.SourceVersion,
		},
		CreatedAt: ptr(episode.EndedAt.UTC().Format(time.RFC3339Nano)), ValidAt: ptr(episode.StartedAt.UTC().Format(time.RFC3339Nano)),
	}
	_, err := a.api.AddFactTriple(ctx, request)
	return safeDependencyError("project episode", err)
}

func (a *Adapter) applyTombstone(ctx context.Context, orgID string, tombstone contextfabric.ProjectionTombstone) error {
	var err error
	switch strings.ToLower(tombstone.Kind) {
	case "relationship", "edge":
		err = a.api.DeleteEdge(ctx, relationshipUUID(orgID, tombstone.CanonicalID))
	case "document", "content":
		err = a.api.DeleteNode(ctx, contentUUID(orgID, "document", tombstone.CanonicalID))
	case "episode":
		err = a.api.DeleteNode(ctx, contentUUID(orgID, "episode", tombstone.CanonicalID))
	default:
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(tombstone.Kind), CanonicalID: tombstone.CanonicalID, Label: tombstone.CanonicalID}
		err = a.api.DeleteNode(ctx, nodeUUID(orgID, subject))
	}
	if zepStatusCode(err) == 404 {
		return nil
	}
	return safeDependencyError("apply graph tombstone", err)
}

func (a *Adapter) writeWatermark(ctx context.Context, batch contextfabric.ProjectionBatch, watermark string) error {
	root := organizationRoot(batch.OrgID)
	marker := markerSubject(batch.Source)
	projectedAt := a.now().UTC()
	request := &zep.AddTripleRequest{
		GraphID: ptr(graphID(a.config.GraphPrefix, batch.OrgID)), Fact: "Projection watermark for " + batch.Source,
		FactName: "HAS_WATERMARK", FactUUID: ptr(relationshipUUID(batch.OrgID, "watermark:"+batch.Source)),
		SourceNodeUUID: ptr(nodeUUID(batch.OrgID, root)), SourceNodeName: ptr(root.Label), SourceNodeLabels: []string{zepLabel(root.Kind)},
		SourceNodeAttributes: subjectAttributes(root, contextfabric.AuthorizationScope{}, nil, projectedAt, nil, nil, "system"),
		TargetNodeUUID:       ptr(nodeUUID(batch.OrgID, marker)), TargetNodeName: ptr(marker.Label), TargetNodeLabels: []string{zepLabel(marker.Kind)},
		TargetNodeAttributes: map[string]interface{}{
			"canonical_id": marker.CanonicalID, "subject_kind": string(marker.Kind), "source": batch.Source,
			"cursor": batch.NextCursor, "source_version": batch.SourceVersion, "backend_watermark": watermark,
			"projected_at": projectedAt.Format(time.RFC3339Nano), "authorization_repositories": "*", "authorization_projects": "*", "authorization_teams": "*",
		},
		CreatedAt: ptr(projectedAt.Format(time.RFC3339Nano)),
	}
	_, err := a.api.AddFactTriple(ctx, request)
	return safeDependencyError("write projection watermark", err)
}

func projectionWatermark(batch contextfabric.ProjectionBatch) string {
	parts := []string{batch.BatchID, batch.OrgID, batch.Source, batch.SourceVersion, batch.Cursor, batch.NextCursor}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "zep:" + hex.EncodeToString(digest[:16])
}

func entitySearchSummary(entity contextfabric.EntityProjection) string {
	parts := []string{entity.Subject.Label}
	if len(entity.Aliases) > 0 {
		parts = append(parts, "Aliases: "+strings.Join(uniqueSorted(entity.Aliases), ", "))
	}
	if len(entity.PreviousNames) > 0 {
		parts = append(parts, "Previous names: "+strings.Join(uniqueSorted(entity.PreviousNames), ", "))
	}
	parts = append(parts, "Canonical kind: "+string(entity.Subject.Kind), "Canonical ID: "+entity.Subject.CanonicalID)
	return strings.Join(parts, "\n")
}

func projectionEntityAttributes(entity contextfabric.EntityProjection) map[string]interface{} {
	attributes := subjectAttributes(entity.Subject, entity.Authorization, entity.EvidenceRefIDs, entity.ObservedAt, entity.ValidFrom, entity.ValidTo, entity.SourceVersion)
	attributes["aliases"] = encodeScope(entity.Aliases)
	attributes["previous_names"] = encodeScope(entity.PreviousNames)
	for key, value := range entity.ProviderIDs {
		attributes["provider_"+safeAttributeName(key)] = value
	}
	for key, value := range entity.Properties {
		attributes["property_"+safeAttributeName(key)] = scalarValue(value)
	}
	return attributes
}

func projectionRelationshipAttributes(relationship contextfabric.RelationshipProjection) map[string]interface{} {
	attributes := map[string]interface{}{
		"relationship_id": relationship.RelationshipID, "relationship_type": relationship.Type,
		"derivation": string(relationship.Derivation), "epistemic_status": string(relationship.EpistemicStatus),
		"authorization_repositories": encodeScope(relationship.Authorization.RepositorySlugs),
		"authorization_projects":     encodeScope(relationship.Authorization.ProjectIDs), "authorization_teams": encodeScope(relationship.Authorization.TeamIDs),
		"evidence_refs": encodeScope(relationship.EvidenceRefIDs), "observed_at": relationship.ObservedAt.UTC().Format(time.RFC3339Nano),
		"source_version": relationship.SourceVersion,
	}
	if relationship.ValidFrom != nil {
		attributes["valid_from"] = relationship.ValidFrom.UTC().Format(time.RFC3339Nano)
	}
	if relationship.ValidTo != nil {
		attributes["valid_to"] = relationship.ValidTo.UTC().Format(time.RFC3339Nano)
	}
	for key, value := range relationship.Properties {
		attributes["property_"+safeAttributeName(key)] = scalarValue(value)
	}
	return attributes
}

func subjectAttributes(subject contextfabric.SubjectRef, authorization contextfabric.AuthorizationScope, evidence []string, observedAt time.Time, validFrom, validTo *time.Time, sourceVersion string) map[string]interface{} {
	attributes := map[string]interface{}{
		"canonical_id": subject.CanonicalID, "subject_kind": string(subject.Kind), "label": subject.Label,
		"authorization_repositories": encodeScope(authorization.RepositorySlugs), "authorization_projects": encodeScope(authorization.ProjectIDs),
		"authorization_teams": encodeScope(authorization.TeamIDs), "evidence_refs": encodeScope(evidence), "source_version": sourceVersion,
	}
	if !observedAt.IsZero() {
		attributes["observed_at"] = observedAt.UTC().Format(time.RFC3339Nano)
	}
	if validFrom != nil {
		attributes["valid_from"] = validFrom.UTC().Format(time.RFC3339Nano)
	}
	if validTo != nil {
		attributes["valid_to"] = validTo.UTC().Format(time.RFC3339Nano)
	}
	return attributes
}

func relationshipFact(relationship contextfabric.RelationshipProjection) string {
	if value, ok := relationship.Properties["fact"]; ok {
		if text := scalarString(value); text != "" {
			return text
		}
	}
	return relationship.From.Label + " " + strings.ToLower(strings.ReplaceAll(relationship.Type, "_", " ")) + " " + relationship.To.Label
}

func scalarValue(value contextfabric.ScalarValue) interface{} {
	switch {
	case value.String != nil:
		return *value.String
	case value.Integer != nil:
		return *value.Integer
	case value.Number != nil:
		return *value.Number
	case value.Boolean != nil:
		return *value.Boolean
	case value.Null:
		return nil
	default:
		return nil
	}
}

func scalarString(value contextfabric.ScalarValue) string {
	if value.String == nil {
		return ""
	}
	return strings.TrimSpace(*value.String)
}

func safeAttributeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
		if builder.Len() >= 64 {
			break
		}
	}
	if builder.Len() == 0 {
		return "value"
	}
	return builder.String()
}

func normalizeRelation(value string) string {
	value = strings.ToUpper(safeAttributeName(value))
	if value == "" {
		return "RELATES_TO"
	}
	return value
}

func zepLabel(kind contextfabric.SubjectKind) string {
	parts := strings.Split(string(kind), "_")
	for index, part := range parts {
		if part == "" {
			continue
		}
		parts[index] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

func stringAttribute(attributes map[string]interface{}, key string) string {
	value, _ := attributes[key].(string)
	return value
}

func parseTimeAttribute(attributes map[string]interface{}, key string) (time.Time, error) {
	value := stringAttribute(attributes, key)
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("graph %s timestamp is invalid", key)
	}
	return parsed.UTC(), nil
}

func ptr[T any](value T) *T { return &value }

var _ contextfabric.ProjectionBackend = (*Adapter)(nil)
