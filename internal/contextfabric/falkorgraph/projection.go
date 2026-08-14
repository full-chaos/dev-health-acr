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

// documentedByRelationType and hasEpisodeRelationType (CHAOS-3779 codex
// round-1 finding L4) are the ONE place these two literal type strings are
// spelled. projectContent and projectEpisode below set propRelationType
// from these constants (not an inline string literal), and
// ProducedRelationshipTypes returns exactly these same constants -- so the
// AC-3779-9 cross-check in cmd/acr-projector reads a value that is
// compiler-tied to what the write path actually emits, not a hand-list
// that could silently drift from it.
const (
	documentedByRelationType = "DOCUMENTED_BY"
	hasEpisodeRelationType   = "HAS_EPISODE"
)

// ProducedRelationshipTypes lists every ContextFabricRelationshipType this
// package writes as an edge property OTHER than through
// ContextFabricRelationshipProjection.Type (CHAOS-3779, AC-3779-9's second
// direction). DOCUMENTED_BY and HAS_EPISODE are synthesized directly here
// -- projectContent and projectEpisode below -- from a ContentProjection /
// EpisodeProjection's implicit attribution to its Subject, never from a
// caller-supplied RelationshipProjection.Type; devhealthsource.
// ProducedRelationshipTypes covers every type produced that way instead.
// See the AC-3779-9 cross-wiring test in cmd/acr-projector, the only
// caller today.
func ProducedRelationshipTypes() []contextfabric.RelationshipType {
	return []contextfabric.RelationshipType{
		contextfabric.RelationshipType(documentedByRelationType),
		contextfabric.RelationshipType(hasEpisodeRelationType),
	}
}

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
	// CHAOS-3778: attach vectors AFTER every node write in this batch, so a
	// vector only ever lands on a node that already carries the search text
	// it was derived from. Deliberately not error-returning -- see
	// embedProjectionBatch's doc comment for why a missing vector degrades
	// retrieval rather than stalling the projection pipeline.
	// Codex round-2 R2-3: a batch whose vector state could not be reconciled
	// must NOT advance the checkpoint -- see clearNodeVectors. Every other
	// embedding failure still degrades silently and lets the batch commit.
	if err := a.embedProjectionBatch(ctx, key, batch); err != nil {
		return contextfabric.ProjectionReceipt{}, err
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

// ownedSubjectMergeCypher returns the MERGE clause for one subject node alias:
// matched/created by (org_id, subject_kind, canonical_id) -- canonical_id
// alone is not globally unique across kinds, matching zepgraph's nodeUUID,
// which hashes kind together with canonical_id for the same reason -- with
// `SET <alias>:<KindLabel>` immediately after to add the kind-specific
// label (idempotent: adding a label a node already carries is a no-op).
// Bootstrap only ever needs one constraint on the generic :Subject label
// (identity.go) rather than one per kind, since the kind-specific label is
// never part of the uniqueness key, only an additional label for
// kind-scoped reads.
func ownedSubjectMergeCypher(alias, kindLabelValue string) string {
	return fmt.Sprintf("MERGE (%s:%s {%s:$org, %s:$%sKind, %s:$%sId}) SET %s:%s",
		alias, labelSubject, propOrgID, propKind, alias, propCanonicalID, alias, alias, kindLabelValue)
}

func subjectMergeParams(alias string, subject contextfabric.SubjectRef, orgID string) map[string]interface{} {
	return map[string]interface{}{alias + "Kind": string(subject.Kind), alias + "Id": subject.CanonicalID, "org": orgID}
}

// referencedSubjectStubMergeCypher is ownedSubjectMergeCypher's counterpart for a subject
// node a writer references but does not OWN -- a relationship's From/To
// endpoint, or the canonical subject a content/episode record attaches to
// (CHAOS-3785 codex round-1 finding F1, widened to every such writer by
// codex round-2 finding R2-1). The class this closes: every subject-node
// write in this file falls into exactly one of two roles --
//
//   - OWNED (ownedSubjectMergeCypher, unconditional "SET += $attrs" both ways):
//     the ONE producer authoritative for this exact node -- projectEntity
//     for its own entity subject, projectContent/projectEpisode for the
//     document/episode node they themselves synthesize (content:ID /
//     episode:ID; nothing else in this package ever writes those). An
//     authoritative write must always win, including on match: it is the
//     single source of truth for that node's canonical data.
//   - REFERENCED (referencedSubjectStubMergeCypher, this function): a writer that
//     merely points at a subject some OTHER, unrelated producer owns --
//     projectRelationship's From/To, projectContent/projectEpisode's own
//     attachment subject. ON CREATE seeds attrsParam in full (nothing else
//     has ever described this node yet, so it is the only data available).
//     ON MATCH sets NOTHING beyond the kind label (idempotent, carries no
//     canonical information) -- not authorization (round-1 F1: a
//     mismatched edge/content/episode scope must never replace an
//     endpoint's own, independently-projected authorization_* attributes),
//     and not label/evidence/source_version/temporal fields either (round-2
//     R2-2: devhealthsource's dependency/hierarchy producers set a
//     relationship endpoint's Label to the bare work-item ID, not its
//     title -- ON MATCH SET-ing that onto an already-canonical node would
//     silently replace a real title with an ID). Same-batch ordering
//     (ApplyProjectionBatch: relationships/contents/episodes before
//     entities) only protects an OWNED write within the SAME batch; a
//     paged/incremental batch can easily land a subject's real entity write
//     and a REFERENCED write in two different batches (devhealthsource's
//     sortCandidates/truncateToCompleteRows sort and cap every producer
//     table's candidates together by (observedAt, sortKey), not grouped by
//     subject), so cross-batch protection has to live here, not there.
//
// A fourth writer added later cannot reintroduce this class by construction
// -- it inherits whichever of the two builders its role calls for -- rather
// than by every author remembering the invariant independently. The names
// themselves carry the decision (CHAOS-3785 codex round-3 finding R3-2:
// "subjectMergeCypher"/"subjectStubMergeCypher" read as interchangeable
// helpers, not as an ownership contract), and
// chaos3785_round3_fake_test.go's TestOwnedSubjectMergeCypherCallSitesArePinned
// / TestReferencedSubjectStubMergeCypherCallSitesArePinned pin every call
// site of both by (enclosing function, line): a new call site anywhere else
// fails those tests with a "which side are you on" message instead of
// silently compiling.
func referencedSubjectStubMergeCypher(alias, kindLabelValue, attrsParam string) string {
	return fmt.Sprintf(
		"MERGE (%s:%s {%s:$org, %s:$%sKind, %s:$%sId}) ON CREATE SET %s += $%s, %s:%s ON MATCH SET %s:%s",
		alias, labelSubject, propOrgID, propKind, alias, propCanonicalID, alias,
		alias, attrsParam, alias, kindLabelValue,
		alias, kindLabelValue,
	)
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
	if entityOwned != nil {
		// R3-1 (CHAOS-3785 codex round 3): the OWNED/authoritative write
		// must assert valid_from/valid_to either way, not merely add them
		// when present. `SET n += $map` only touches keys the map contains
		// (subjectMergeAttrs' own doc comment) -- omitting a key when
		// validFrom/validTo is nil, as the referenced/stub branch below
		// still does, would leave a STALE validity window in place forever
		// if some earlier referenced write (e.g. an episode's own
		// StartedAt/EndedAt, seeded into this same node via
		// referencedSubjectStubMergeCypher's ON CREATE before the canonical entity
		// ever arrived) had set one. A canonical entity with no validity
		// window of its own must actively clear whatever a stub happened to
		// seed, not just leave it stacked underneath the real data.
		// Verified live against FalkorDB: `SET n += {k: null}` REMOVES key
		// k (confirmed via GRAPH.QUERY, not assumed from generic Cypher
		// semantics -- this FalkorDB version's exact "Properties removed"
		// counters bore it out). safeParamValue explicitly allows a nil
		// parameter value (client.go), so this reaches FalkorDB as a real
		// Cypher null, not a marshaling error.
		attrs[propValidFrom], attrs[propValidFromNs] = validTimeAttrs(validFrom)
		attrs[propValidTo], attrs[propValidToNs] = validTimeAttrs(validTo)
	}
	// A REFERENCED stub deliberately writes NO validity window at all
	// (CHAOS-3781 round-1 F3). It previously copied the window of
	// whatever record happened to mention the subject, which is a
	// different thing's interval: a relationship valid for one week, or
	// an episode that ran for an hour, would stamp that window onto the
	// work item it referenced. A historical read then excluded that work
	// item everywhere outside the unrelated record's window -- and which
	// window won depended on projection ORDER, since the next referencing
	// record overwrote it.
	//
	// This is the same discipline CHAOS-3785 established for stubs
	// generally: a stub asserts identity and nothing canonical. Only the
	// authoritative entity write (the OWNED branch above, which asserts
	// the window either way including an explicit nil) may state when a
	// subject was valid.
	//
	// A stub therefore carries both bounds absent, which the read side
	// already handles honestly: temporalFilter.predicate admits an
	// unbounded element at every requested time, and countUnboundedValidity
	// counts it into the coverage disclosure. An over-admitted stub is
	// visible; a wrongly-excluded real subject is not.
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

// validTimeAttrs returns (formatted RFC3339Nano string, nanosecond int64)
// for t, or (nil, nil) when t is nil -- the pair subjectMergeAttrs' OWNED
// branch assigns directly to attrs[propValidFrom]/attrs[propValidFromNs] (or
// the propValidTo equivalents) so the key is always present, letting a nil
// actively clear a stale value on write (see that branch's doc comment).
func validTimeAttrs(t *time.Time) (interface{}, interface{}) {
	if t == nil {
		return nil, nil
	}
	return t.UTC().Format(time.RFC3339Nano), nsTimestamp(*t)
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
	cypher := ownedSubjectMergeCypher("n", kindLabel(entity.Subject.Kind)) + " SET n += $attrs"
	params := subjectMergeParams("n", entity.Subject, orgID)
	params["attrs"] = attrs
	_, err := a.api.query(ctx, key, cypher, params, false)
	return classifyProjectionError("project entity", err)
}

func (a *Adapter) projectRelationship(ctx context.Context, key, orgID string, relationship contextfabric.RelationshipProjection) error {
	// nil/nil validity: these are referenced STUBS, and a relationship's
	// own window is not its endpoints' window (round-1 F3). The edge
	// itself still carries the window, below.
	fromAttrs := subjectMergeAttrs(relationship.From, relationship.Authorization, relationship.EvidenceRefIDs, relationship.ObservedAt, nil, nil, relationship.SourceVersion, nil)
	toAttrs := subjectMergeAttrs(relationship.To, relationship.Authorization, relationship.EvidenceRefIDs, relationship.ObservedAt, nil, nil, relationship.SourceVersion, nil)
	edgeAttrs := map[string]interface{}{
		propRelationshipID: relationship.RelationshipID, propRelationType: graphrank.NormalizeRelation(string(relationship.Type)),
		"derivation": string(relationship.Derivation), "epistemic_status": string(relationship.EpistemicStatus),
		propAuthzRepos: authorizationValue(relationship.Authorization.RepositorySlugs), propAuthzProjects: authorizationValue(relationship.Authorization.ProjectIDs),
		propAuthzTeams: authorizationValue(relationship.Authorization.TeamIDs), propEvidenceRefs: graphrank.UniqueSorted(relationship.EvidenceRefIDs),
		propSourceVersion: relationship.SourceVersion, propObservedAt: relationship.ObservedAt.UTC().Format(time.RFC3339Nano), propObservedAtNs: nsTimestamp(relationship.ObservedAt),
		"fact": relationshipFact(relationship),
	}
	// R4-1 (CHAOS-3785 codex round 4): same class as R3-1, on the edge
	// itself rather than an endpoint node. The edge write IS this
	// relationship_id's one authoritative/owned writer (no other producer
	// legitimately shares it), but "owned" still means the write must
	// assert the WHOLE attribute set on every re-projection, not merely add
	// what's present -- a relationship_id re-projected on a later tick with
	// no validity window this time (the source data's temporal info became
	// unavailable, or never had one to begin with on a later pass) must
	// actively clear a window an earlier projection set, not leave it
	// stacked underneath. Read side confirms this is live-connected, not
	// cosmetic: queries.go's toCandidateEdge feeds valid_from/valid_to
	// straight into graphrank.CandidateEdge.ValidAt/InvalidAt. Reuses
	// validTimeAttrs (R3-1), whose null-removal semantics were already
	// live-verified against FalkorDB.
	edgeAttrs[propValidFrom], edgeAttrs[propValidFromNs] = validTimeAttrs(relationship.ValidFrom)
	edgeAttrs[propValidTo], edgeAttrs[propValidToNs] = validTimeAttrs(relationship.ValidTo)
	cypher := referencedSubjectStubMergeCypher("a", kindLabel(relationship.From.Kind), "fromAttrs") + " " +
		referencedSubjectStubMergeCypher("b", kindLabel(relationship.To.Kind), "toAttrs") + " " +
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
		propSearchText: contentSearchText(content),
	}
	// a (content.Subject) is REFERENCED, not owned -- see
	// referencedSubjectStubMergeCypher's doc comment (round-2 R2-1): projectContent
	// does not own whatever real entity this content attaches to, so a
	// later content write with a different scope must not clobber that
	// entity's own authorization or canonical metadata. b (documentSubject)
	// IS owned: this is the only producer that ever writes a content:ID
	// node, so its write stays authoritative.
	cypher := referencedSubjectStubMergeCypher("a", kindLabel(content.Subject.Kind), "subjectAttrs") + " " +
		ownedSubjectMergeCypher("b", kindLabel(documentSubject.Kind)) + " SET b += $docAttrs " +
		fmt.Sprintf("MERGE (a)-[r:%s {%s:$rid}]->(b) SET r += $edgeAttrs", labelRelation, propRelationshipID)
	// Codex P1: the attribution edge itself must carry authorization, or a
	// scoped principal's edge-level authorization check (DiscoverContext)
	// denies it unconditionally regardless of the document/episode's own
	// scope -- an absent authorization_* key means "deny" by convention
	// (graphrank's AuthorizedAttributes), not "unrestricted". This edge
	// inherits the content's own projected scope: the DOCUMENTED_BY fact
	// is exactly as authorized as the document it attributes.
	edgeAttrs := map[string]interface{}{
		propRelationType: documentedByRelationType, propEvidenceRefs: graphrank.UniqueSorted(content.EvidenceRefIDs),
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
	// nil/nil validity: episode.Subject is a referenced STUB, and the
	// episode's run window is the EPISODE's interval, not the subject's
	// (round-1 F3). A work item does not stop being valid because an
	// episode about it ended. The episode node itself keeps the window,
	// below.
	subjectAttrs := subjectMergeAttrs(episode.Subject, episode.Authorization, episode.EvidenceRefIDs, episode.EndedAt, nil, nil, episode.SourceVersion, nil)
	episodeSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectEpisode, CanonicalID: "episode:" + episode.EpisodeID, Label: episode.EpisodeID}
	summary := episodeSearchText(episode)
	episodeAttrs := map[string]interface{}{
		propLabel: episode.EpisodeID, propAuthzRepos: authorizationValue(episode.Authorization.RepositorySlugs),
		propAuthzProjects: authorizationValue(episode.Authorization.ProjectIDs), propAuthzTeams: authorizationValue(episode.Authorization.TeamIDs),
		propEvidenceRefs: graphrank.UniqueSorted(episode.EvidenceRefIDs), propSourceVersion: episode.SourceVersion,
		propObservedAt: episode.EndedAt.UTC().Format(time.RFC3339Nano), propObservedAtNs: nsTimestamp(episode.EndedAt),
		propValidFrom: episode.StartedAt.UTC().Format(time.RFC3339Nano), propValidFromNs: nsTimestamp(episode.StartedAt),
		propValidTo: episode.EndedAt.UTC().Format(time.RFC3339Nano), propValidToNs: nsTimestamp(episode.EndedAt),
		"goal": episode.Goal, "outcome": episode.Outcome, propSearchText: summary,
	}
	// Same OWNED/REFERENCED split as projectContent above: a (episode.Subject)
	// is referenced, not owned; b (episodeSubject) is this producer's own
	// episode:ID node and stays authoritative.
	cypher := referencedSubjectStubMergeCypher("a", kindLabel(episode.Subject.Kind), "subjectAttrs") + " " +
		ownedSubjectMergeCypher("b", kindLabel(episodeSubject.Kind)) + " SET b += $episodeAttrs " +
		fmt.Sprintf("MERGE (a)-[r:%s {%s:$rid}]->(b) SET r += $edgeAttrs", labelRelation, propRelationshipID)
	// Codex P1: same reasoning as projectContent's DOCUMENTED_BY edge --
	// this attribution edge inherits the episode's own projected scope.
	edgeAttrs := map[string]interface{}{
		propRelationType: hasEpisodeRelationType, propEvidenceRefs: graphrank.UniqueSorted(episode.EvidenceRefIDs),
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
	return relationship.From.Label + " " + strings.ToLower(strings.ReplaceAll(string(relationship.Type), "_", " ")) + " " + relationship.To.Label
}

// contentSearchText and episodeSearchText exist so the projection write path
// and CHAOS-3778's embedding pass derive their text from ONE expression rather
// than two identical-looking ones. The lexical index and the vector index must
// search byte-identical corpora -- that is what makes their agreement a
// statement about MECHANISM rather than about which text each happened to see
// (see graphrank.DistinctMechanismCount). Two copies of the concatenation
// would be one edit away from silently breaking that.
func contentSearchText(content contextfabric.ContentProjection) string {
	return strings.TrimSpace(content.Title + "\n" + content.Body)
}

func episodeSearchText(episode contextfabric.EpisodeProjection) string {
	return strings.TrimSpace(episode.Goal + "\nOutcome: " + episode.Outcome + "\n" + episode.Summary)
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
