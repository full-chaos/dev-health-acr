package falkorgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

const propRelationType = "relation_type"

func (a *Adapter) ApplyProjectionBatch(ctx context.Context, batch contextfabric.ProjectionBatch) (contextfabric.ProjectionReceipt, error) {
	if err := batch.Validate(); err != nil {
		return contextfabric.ProjectionReceipt{}, fmt.Errorf("projection batch: %w", err)
	}
	key := graphKey(a.config.GraphPrefix, batch.OrgID)
	if err := a.ensureOrgGraph(ctx, key); err != nil {
		return contextfabric.ProjectionReceipt{}, err
	}
	// Same processing order as zepgraph.(*Adapter).ApplyProjectionBatch:
	// relationships/contents/episodes before entities, so an entity
	// projection's authoritative canonical metadata (aliases, previous
	// names, provider IDs) always lands last within one batch and is never
	// shadowed by an implicit endpoint-stub write from an earlier
	// relationship/content/episode in the same batch.
	for _, relationship := range batch.Relationships {
		if err := a.projectRelationship(ctx, key, batch.OrgID, relationship); err != nil {
			return contextfabric.ProjectionReceipt{}, err
		}
	}
	for _, content := range batch.Contents {
		if err := a.projectContent(ctx, key, batch.OrgID, content); err != nil {
			return contextfabric.ProjectionReceipt{}, err
		}
	}
	for _, episode := range batch.Episodes {
		if err := a.projectEpisode(ctx, key, batch.OrgID, episode); err != nil {
			return contextfabric.ProjectionReceipt{}, err
		}
	}
	for _, entity := range batch.Entities {
		if err := a.projectEntity(ctx, key, batch.OrgID, entity); err != nil {
			return contextfabric.ProjectionReceipt{}, err
		}
	}
	for _, tombstone := range batch.Tombstones {
		if err := a.applyTombstone(ctx, key, batch.OrgID, tombstone); err != nil {
			return contextfabric.ProjectionReceipt{}, err
		}
	}
	watermark := projectionWatermark(batch)
	if err := a.writeWatermark(ctx, key, batch, watermark); err != nil {
		return contextfabric.ProjectionReceipt{}, err
	}
	return contextfabric.ProjectionReceipt{
		BatchID: batch.BatchID, AppliedAt: a.now().UTC(), BackendWatermark: watermark,
		EntitiesApplied: len(batch.Entities), EdgesApplied: len(batch.Relationships),
		ContentsApplied: len(batch.Contents), EpisodesApplied: len(batch.Episodes),
		TombstonesApplied: len(batch.Tombstones),
	}, nil
}

// subjectMergeCypher returns the MERGE clause for one subject node alias:
// matched/created by (org_id, subject_kind, canonical_id) -- canonical_id
// alone is not globally unique across kinds, matching zepgraph's nodeUUID,
// which hashes kind together with canonical_id for the same reason -- with
// `SET <alias>:<KindLabel>` immediately after to add the kind-specific
// label (idempotent: adding a label a node already carries is a no-op).
// Bootstrap only ever needs one constraint on the generic :Subject label
// (identity.go) rather than one per kind, since the kind-specific label is
// never part of the uniqueness key, only an additional label for
// kind-scoped reads.
func subjectMergeCypher(alias, kindLabelValue string) string {
	return fmt.Sprintf("MERGE (%s:%s {%s:$org, %s:$%sKind, %s:$%sId}) SET %s:%s",
		alias, labelSubject, propOrgID, propKind, alias, propCanonicalID, alias, alias, kindLabelValue)
}

func subjectMergeParams(alias string, subject contextfabric.SubjectRef, orgID string) map[string]interface{} {
	return map[string]interface{}{alias + "Kind": string(subject.Kind), alias + "Id": subject.CanonicalID, "org": orgID}
}

// subjectMergeAttrs builds the SET n += $attrs payload for a subject node.
// Cypher's SET n += $map only ever touches the keys present in $map (verified
// live: docs/design/context-fabric-falkordb-adapter.md §4.3), so "does this
// write own aliases/previous_names/provider IDs/properties" is decided
// simply by whether entityOwned is non-nil -- no read-modify-write, no
// COALESCE trick, no second query. This replaces
// zepgraph.mergedSubjectAttributes' two-round-trip read-then-merge outright.
func subjectMergeAttrs(subject contextfabric.SubjectRef, authorization contextfabric.AuthorizationScope, evidence []string, observedAt time.Time, validFrom, validTo *time.Time, sourceVersion string, entityOwned *contextfabric.EntityProjection) map[string]interface{} {
	attrs := map[string]interface{}{
		propLabel:         subject.Label,
		propAuthzRepos:    authorizationValue(authorization.RepositorySlugs),
		propAuthzProjects: authorizationValue(authorization.ProjectIDs),
		propAuthzTeams:    authorizationValue(authorization.TeamIDs),
		propEvidenceRefs:  graphrank.UniqueSorted(evidence),
		propSourceVersion: sourceVersion,
	}
	if !observedAt.IsZero() {
		attrs[propObservedAt] = observedAt.UTC().Format(time.RFC3339Nano)
		attrs[propObservedAtNs] = nsTimestamp(observedAt)
	}
	if validFrom != nil {
		attrs[propValidFrom] = validFrom.UTC().Format(time.RFC3339Nano)
		attrs[propValidFromNs] = nsTimestamp(*validFrom)
	}
	if validTo != nil {
		attrs[propValidTo] = validTo.UTC().Format(time.RFC3339Nano)
		attrs[propValidToNs] = nsTimestamp(*validTo)
	}
	if entityOwned != nil {
		attrs[propAliases] = graphrank.UniqueSorted(entityOwned.Aliases)
		attrs[propPreviousNames] = graphrank.UniqueSorted(entityOwned.PreviousNames)
		for k, v := range entityOwned.ProviderIDs {
			attrs[propProviderPrefix+safeName(k)] = v
		}
		for k, v := range entityOwned.Properties {
			attrs[propPropertyPrefix+safeName(k)] = scalarValue(v)
		}
		attrs[propSearchText] = entitySearchText(*entityOwned)
	}
	return attrs
}

// authorizationValue produces graphrank's shared attribute-value convention
// directly (see graphrank's authorize.go doc comment): the literal string
// "*" for an unrestricted/empty scope, or the specific []string list
// otherwise. No pipe-encoding: FalkorDB stores lists natively.
func authorizationValue(values []string) interface{} {
	cleaned := graphrank.UniqueSorted(values)
	if len(cleaned) == 0 {
		return "*"
	}
	return cleaned
}

func (a *Adapter) projectEntity(ctx context.Context, key, orgID string, entity contextfabric.EntityProjection) error {
	attrs := subjectMergeAttrs(entity.Subject, entity.Authorization, entity.EvidenceRefIDs, entity.ObservedAt, entity.ValidFrom, entity.ValidTo, entity.SourceVersion, &entity)
	cypher := subjectMergeCypher("n", kindLabel(entity.Subject.Kind)) + " SET n += $attrs"
	params := subjectMergeParams("n", entity.Subject, orgID)
	params["attrs"] = attrs
	_, err := a.api.query(ctx, key, cypher, params, false)
	return classifyProjectionError("project entity", err)
}

func (a *Adapter) projectRelationship(ctx context.Context, key, orgID string, relationship contextfabric.RelationshipProjection) error {
	fromAttrs := subjectMergeAttrs(relationship.From, relationship.Authorization, relationship.EvidenceRefIDs, relationship.ObservedAt, relationship.ValidFrom, relationship.ValidTo, relationship.SourceVersion, nil)
	toAttrs := subjectMergeAttrs(relationship.To, relationship.Authorization, relationship.EvidenceRefIDs, relationship.ObservedAt, relationship.ValidFrom, relationship.ValidTo, relationship.SourceVersion, nil)
	edgeAttrs := map[string]interface{}{
		propRelationshipID: relationship.RelationshipID, propRelationType: graphrank.NormalizeRelation(relationship.Type),
		"derivation": string(relationship.Derivation), "epistemic_status": string(relationship.EpistemicStatus),
		propAuthzRepos: authorizationValue(relationship.Authorization.RepositorySlugs), propAuthzProjects: authorizationValue(relationship.Authorization.ProjectIDs),
		propAuthzTeams: authorizationValue(relationship.Authorization.TeamIDs), propEvidenceRefs: graphrank.UniqueSorted(relationship.EvidenceRefIDs),
		propSourceVersion: relationship.SourceVersion, propObservedAt: relationship.ObservedAt.UTC().Format(time.RFC3339Nano), propObservedAtNs: nsTimestamp(relationship.ObservedAt),
		"fact": relationshipFact(relationship),
	}
	if relationship.ValidFrom != nil {
		edgeAttrs[propValidFrom] = relationship.ValidFrom.UTC().Format(time.RFC3339Nano)
		edgeAttrs[propValidFromNs] = nsTimestamp(*relationship.ValidFrom)
	}
	if relationship.ValidTo != nil {
		edgeAttrs[propValidTo] = relationship.ValidTo.UTC().Format(time.RFC3339Nano)
		edgeAttrs[propValidToNs] = nsTimestamp(*relationship.ValidTo)
	}
	cypher := subjectMergeCypher("a", kindLabel(relationship.From.Kind)) + " SET a += $fromAttrs " +
		subjectMergeCypher("b", kindLabel(relationship.To.Kind)) + " SET b += $toAttrs " +
		fmt.Sprintf("MERGE (a)-[r:%s {%s:$rid}]->(b) SET r += $edgeAttrs", labelRelation, propRelationshipID)
	params := map[string]interface{}{"rid": relationship.RelationshipID, "fromAttrs": fromAttrs, "toAttrs": toAttrs, "edgeAttrs": edgeAttrs}
	mergeMaps(params, subjectMergeParams("a", relationship.From, orgID))
	mergeMaps(params, subjectMergeParams("b", relationship.To, orgID))
	_, err := a.api.query(ctx, key, cypher, params, false)
	return classifyProjectionError("project relationship", err)
}

func (a *Adapter) projectContent(ctx context.Context, key, orgID string, content contextfabric.ContentProjection) error {
	subjectAttrs := subjectMergeAttrs(content.Subject, content.Authorization, content.EvidenceRefIDs, content.ObservedAt, nil, nil, content.SourceVersion, nil)
	documentSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectDocument, CanonicalID: "content:" + content.ContentID, Label: content.Title}
	documentAttrs := map[string]interface{}{
		propLabel: content.Title, propAuthzRepos: authorizationValue(content.Authorization.RepositorySlugs),
		propAuthzProjects: authorizationValue(content.Authorization.ProjectIDs), propAuthzTeams: authorizationValue(content.Authorization.TeamIDs),
		propEvidenceRefs: graphrank.UniqueSorted(content.EvidenceRefIDs), propSourceVersion: content.SourceVersion,
		propObservedAt: content.ObservedAt.UTC().Format(time.RFC3339Nano), propObservedAtNs: nsTimestamp(content.ObservedAt),
		"content_digest": content.ContentDigest, "body": content.Body, "untrusted": true,
		propSearchText: strings.TrimSpace(content.Title + "\n" + content.Body),
	}
	cypher := subjectMergeCypher("a", kindLabel(content.Subject.Kind)) + " SET a += $subjectAttrs " +
		subjectMergeCypher("b", kindLabel(documentSubject.Kind)) + " SET b += $docAttrs " +
		fmt.Sprintf("MERGE (a)-[r:%s {%s:$rid}]->(b) SET r += $edgeAttrs", labelRelation, propRelationshipID)
	// Codex P1: the attribution edge itself must carry authorization, or a
	// scoped principal's edge-level authorization check (DiscoverContext)
	// denies it unconditionally regardless of the document/episode's own
	// scope -- an absent authorization_* key means "deny" by convention
	// (graphrank's AuthorizedAttributes), not "unrestricted". This edge
	// inherits the content's own projected scope: the DOCUMENTED_BY fact
	// is exactly as authorized as the document it attributes.
	edgeAttrs := map[string]interface{}{
		propRelationType: "DOCUMENTED_BY", propEvidenceRefs: graphrank.UniqueSorted(content.EvidenceRefIDs),
		propAuthzRepos: authorizationValue(content.Authorization.RepositorySlugs), propAuthzProjects: authorizationValue(content.Authorization.ProjectIDs),
		propAuthzTeams: authorizationValue(content.Authorization.TeamIDs),
	}
	params := map[string]interface{}{"rid": "content:" + content.ContentID, "subjectAttrs": subjectAttrs, "docAttrs": documentAttrs, "edgeAttrs": edgeAttrs}
	mergeMaps(params, subjectMergeParams("a", content.Subject, orgID))
	mergeMaps(params, subjectMergeParams("b", documentSubject, orgID))
	_, err := a.api.query(ctx, key, cypher, params, false)
	return classifyProjectionError("project content", err)
}

func (a *Adapter) projectEpisode(ctx context.Context, key, orgID string, episode contextfabric.EpisodeProjection) error {
	subjectAttrs := subjectMergeAttrs(episode.Subject, episode.Authorization, episode.EvidenceRefIDs, episode.EndedAt, &episode.StartedAt, &episode.EndedAt, episode.SourceVersion, nil)
	episodeSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectEpisode, CanonicalID: "episode:" + episode.EpisodeID, Label: episode.EpisodeID}
	summary := strings.TrimSpace(episode.Goal + "\nOutcome: " + episode.Outcome + "\n" + episode.Summary)
	episodeAttrs := map[string]interface{}{
		propLabel: episode.EpisodeID, propAuthzRepos: authorizationValue(episode.Authorization.RepositorySlugs),
		propAuthzProjects: authorizationValue(episode.Authorization.ProjectIDs), propAuthzTeams: authorizationValue(episode.Authorization.TeamIDs),
		propEvidenceRefs: graphrank.UniqueSorted(episode.EvidenceRefIDs), propSourceVersion: episode.SourceVersion,
		propObservedAt: episode.EndedAt.UTC().Format(time.RFC3339Nano), propObservedAtNs: nsTimestamp(episode.EndedAt),
		propValidFrom: episode.StartedAt.UTC().Format(time.RFC3339Nano), propValidFromNs: nsTimestamp(episode.StartedAt),
		propValidTo: episode.EndedAt.UTC().Format(time.RFC3339Nano), propValidToNs: nsTimestamp(episode.EndedAt),
		"goal": episode.Goal, "outcome": episode.Outcome, propSearchText: summary,
	}
	cypher := subjectMergeCypher("a", kindLabel(episode.Subject.Kind)) + " SET a += $subjectAttrs " +
		subjectMergeCypher("b", kindLabel(episodeSubject.Kind)) + " SET b += $episodeAttrs " +
		fmt.Sprintf("MERGE (a)-[r:%s {%s:$rid}]->(b) SET r += $edgeAttrs", labelRelation, propRelationshipID)
	// Codex P1: same reasoning as projectContent's DOCUMENTED_BY edge --
	// this attribution edge inherits the episode's own projected scope.
	edgeAttrs := map[string]interface{}{
		propRelationType: "HAS_EPISODE", propEvidenceRefs: graphrank.UniqueSorted(episode.EvidenceRefIDs),
		propAuthzRepos: authorizationValue(episode.Authorization.RepositorySlugs), propAuthzProjects: authorizationValue(episode.Authorization.ProjectIDs),
		propAuthzTeams: authorizationValue(episode.Authorization.TeamIDs),
	}
	params := map[string]interface{}{"rid": "episode:" + episode.EpisodeID, "subjectAttrs": subjectAttrs, "episodeAttrs": episodeAttrs, "edgeAttrs": edgeAttrs}
	mergeMaps(params, subjectMergeParams("a", episode.Subject, orgID))
	mergeMaps(params, subjectMergeParams("b", episodeSubject, orgID))
	_, err := a.api.query(ctx, key, cypher, params, false)
	return classifyProjectionError("project episode", err)
}

func mergeMaps(dst, src map[string]interface{}) {
	for k, v := range src {
		dst[k] = v
	}
}

// applyTombstone deletes a node or relationship in one atomic statement,
// with the staleness check folded directly into the WHERE clause -- a
// stale, out-of-order tombstone simply matches zero rows. This replaces
// zepgraph's two-round-trip deleteNodeIfNotNewer/deleteEdgeIfNotNewer
// (fetch, decide, maybe-delete) with a single query.
func (a *Adapter) applyTombstone(ctx context.Context, key, orgID string, tombstone contextfabric.ProjectionTombstone) error {
	effectiveNs := nsTimestamp(tombstone.EffectiveAt)
	switch strings.ToLower(tombstone.Kind) {
	case "relationship", "edge":
		cypher := fmt.Sprintf("MATCH ()-[r:%s {%s:$rid}]-() WHERE r.%s IS NULL OR r.%s <= $effectiveNs DELETE r",
			labelRelation, propRelationshipID, propObservedAtNs, propObservedAtNs)
		_, err := a.api.query(ctx, key, cypher, map[string]interface{}{"rid": tombstone.CanonicalID, "effectiveNs": effectiveNs}, false)
		return classifyProjectionError("apply relationship tombstone", err)
	}
	var kind, canonicalID string
	switch strings.ToLower(tombstone.Kind) {
	case "document", "content":
		kind, canonicalID = string(contextfabric.SubjectDocument), "content:"+tombstone.CanonicalID
	case "episode":
		kind, canonicalID = string(contextfabric.SubjectEpisode), "episode:"+tombstone.CanonicalID
	default:
		kind, canonicalID = tombstone.Kind, tombstone.CanonicalID
	}
	cypher := fmt.Sprintf("MATCH (n:%s {%s:$org, %s:$kind, %s:$id}) WHERE n.%s IS NULL OR n.%s <= $effectiveNs DETACH DELETE n",
		labelSubject, propOrgID, propKind, propCanonicalID, propObservedAtNs, propObservedAtNs)
	_, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "kind": kind, "id": canonicalID, "effectiveNs": effectiveNs}, false)
	return classifyProjectionError("apply subject tombstone", err)
}

func (a *Adapter) writeWatermark(ctx context.Context, key string, batch contextfabric.ProjectionBatch, watermark string) error {
	projectedAt := a.now().UTC()
	cypher := fmt.Sprintf("MERGE (w:%s {%s:$org, source:$source}) SET w += $attrs", labelWatermark, propOrgID)
	attrs := map[string]interface{}{
		"cursor": batch.NextCursor, propSourceVersion: batch.SourceVersion, "backend_watermark": watermark,
		"projected_at": projectedAt.Format(time.RFC3339Nano), "projected_at_ns": projectedAt.UnixNano(),
	}
	_, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": batch.OrgID, "source": batch.Source, "attrs": attrs}, false)
	return classifyProjectionError("write projection watermark", err)
}

func (a *Adapter) ProjectionWatermark(ctx context.Context, orgID, source string) (contextfabric.ProjectionWatermark, error) {
	if strings.TrimSpace(orgID) == "" || strings.TrimSpace(source) == "" {
		return contextfabric.ProjectionWatermark{}, errors.New("organization and source are required")
	}
	key := graphKey(a.config.GraphPrefix, orgID)
	cypher := fmt.Sprintf("MATCH (w:%s {%s:$org, source:$source}) RETURN w", labelWatermark, propOrgID)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "source": source}, true)
	if err != nil {
		return contextfabric.ProjectionWatermark{}, safeDependencyError("read projection watermark", err)
	}
	if len(rows) == 0 {
		return contextfabric.ProjectionWatermark{}, ErrNotFound
	}
	w, ok := rows[0]["w"].(*node)
	if !ok || w == nil {
		return contextfabric.ProjectionWatermark{}, ErrNotFound
	}
	projectedAt, err := parseRFC3339(propStringValue(w.Properties["projected_at"]))
	if err != nil {
		return contextfabric.ProjectionWatermark{}, fmt.Errorf("graph projected_at timestamp is invalid")
	}
	return contextfabric.ProjectionWatermark{
		OrgID: orgID, Source: source, Cursor: propStringValue(w.Properties["cursor"]),
		SourceVersion: propStringValue(w.Properties[propSourceVersion]), ProjectedAt: projectedAt,
		BackendWatermark: propStringValue(w.Properties["backend_watermark"]),
	}, nil
}

func (a *Adapter) PurgeOrganization(ctx context.Context, orgID string) error {
	if strings.TrimSpace(orgID) == "" {
		return errors.New("organization is required")
	}
	key := graphKey(a.config.GraphPrefix, orgID)
	err := a.api.deleteGraph(ctx, key)
	a.bootstrapMu.Lock()
	delete(a.bootstrapDone, key)
	a.bootstrapMu.Unlock()
	if err != nil && !errors.Is(err, ErrNotFound) {
		return safeDependencyError("purge organization graph", err)
	}
	return nil
}

func projectionWatermark(batch contextfabric.ProjectionBatch) string {
	parts := []string{batch.BatchID, batch.OrgID, batch.Source, batch.SourceVersion, batch.Cursor, batch.NextCursor}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "falkordb:" + hex.EncodeToString(digest[:16])
}

func relationshipFact(relationship contextfabric.RelationshipProjection) string {
	if value, ok := relationship.Properties["fact"]; ok {
		if text := scalarString(value); text != "" {
			return text
		}
	}
	return relationship.From.Label + " " + strings.ToLower(strings.ReplaceAll(relationship.Type, "_", " ")) + " " + relationship.To.Label
}

func entitySearchText(entity contextfabric.EntityProjection) string {
	parts := []string{entity.Subject.Label}
	if len(entity.Aliases) > 0 {
		parts = append(parts, strings.Join(graphrank.UniqueSorted(entity.Aliases), " "))
	}
	if len(entity.PreviousNames) > 0 {
		parts = append(parts, strings.Join(graphrank.UniqueSorted(entity.PreviousNames), " "))
	}
	return strings.Join(parts, "\n")
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

func safeName(value string) string {
	return graphrank.SafeAttributeName(value)
}

func propStringValue(value interface{}) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func parseRFC3339(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	return time.Parse(time.RFC3339Nano, value)
}

// classifyProjectionError narrows a raw conn error into ErrConstraintViolation
// when applicable, otherwise passes through safeDependencyError's
// classification. Kept separate from safeDependencyError so callers that
// need to react specifically to a constraint violation (none currently do,
// but the distinction matters for future retry/backoff policy) don't have
// to re-derive it from a flattened error string.
func classifyProjectionError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrConstraintViolation) {
		return err
	}
	return safeDependencyError(operation, err)
}
