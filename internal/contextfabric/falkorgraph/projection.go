package falkorgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
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

// ApplyProjectionBatch is the write path's own entry point: graph/edge
// projection for one batch, plus (when an embedder is configured) the
// vector-embedding write path -- resolveWriteKey (lifecycle.go), the
// embed-or-clear decision (transient-failure design: an Embed() error
// clears rather than stalls the batch forever), and writeNodeVector's
// embedder-identity stamp that the READ path's fence later checks.
//
// Full pathway diagram (env resolution -> this write path -> per-epoch
// FalkorDB storage, incl. the epoch build-aside/swap hop -> KNN read path
// and its stored-vector invalidation fence): CHAOS-4133,
// docs/design/context-fabric-vector-retrieval.md §8.
func (a *Adapter) ApplyProjectionBatch(ctx context.Context, batch contextfabric.ProjectionBatch) (contextfabric.ProjectionReceipt, error) {
	if err := batch.Validate(); err != nil {
		return contextfabric.ProjectionReceipt{}, fmt.Errorf("projection batch: %w", err)
	}
	// CHAOS-3898 S2a: resolves the BUILD target epoch while one is open
	// for this org, else the ACTIVE epoch (design brief §3.1) -- a nil
	// Config.EpochResolver (every production composition root today)
	// falls back to epoch 0's key, byte-identical to pre-CHAOS-3898
	// behavior.
	key, err := a.resolveWriteKey(ctx, batch.OrgID)
	if err != nil {
		return contextfabric.ProjectionReceipt{}, err
	}
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
func subjectMergeAttrs(subject contextfabric.SubjectRef, authorization contextfabric.AuthorizationScope, evidence []string, observedAt time.Time, validFrom, validTo *time.Time, sourceVersion string, entityOwned *contextfabric.EntityProjection, includeBodies bool) map[string]interface{} {
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
		// CHAOS-3884: provider-qualified identity variants, same write
		// discipline as propAliases -- a distinct property so NodeCandidate
		// can tag MatchProviderKey separately from MatchAlias even though
		// both flow through this identical write path.
		attrs[propProviderAliases] = graphrank.UniqueSorted(entityOwned.ProviderAliases)
		attrs[propPreviousNames] = graphrank.UniqueSorted(entityOwned.PreviousNames)
		for k, v := range entityOwned.ProviderIDs {
			attrs[propProviderPrefix+safeName(k)] = v
		}
		for k, v := range entityOwned.Properties {
			attrs[propPropertyPrefix+safeName(k)] = scalarValue(v)
		}
		// CHAOS-3833: the ONE per-kind composition both retrieval arms
		// index -- see search_text.go. includeBodies is the §3 body
		// gate's effective value from Config, identical to what
		// collectEmbedTargets composes with.
		attrs[propSearchText] = subjectSearchText(*entityOwned, includeBodies)
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
	attrs := subjectMergeAttrs(entity.Subject, entity.Authorization, entity.EvidenceRefIDs, entity.ObservedAt, entity.ValidFrom, entity.ValidTo, entity.SourceVersion, &entity, a.config.IncludeEmbedBodies)
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
	fromAttrs := subjectMergeAttrs(relationship.From, relationship.Authorization, relationship.EvidenceRefIDs, relationship.ObservedAt, nil, nil, relationship.SourceVersion, nil, a.config.IncludeEmbedBodies)
	toAttrs := subjectMergeAttrs(relationship.To, relationship.Authorization, relationship.EvidenceRefIDs, relationship.ObservedAt, nil, nil, relationship.SourceVersion, nil, a.config.IncludeEmbedBodies)
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
	subjectAttrs := subjectMergeAttrs(content.Subject, content.Authorization, content.EvidenceRefIDs, content.ObservedAt, nil, nil, content.SourceVersion, nil, a.config.IncludeEmbedBodies)
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
	// CHAOS-3901: episode.EpisodeID (devhealthsource/episodes.go's
	// episodeCandidate) is ALREADY the full "episode:<uuid>" canonical id
	// -- the exact same value episode.Subject.CanonicalID carries, since
	// both are built from the same canonicalID local there. Re-prefixing
	// it here ("episode:"+episode.EpisodeID) doubled the prefix into
	// "episode:episode:<uuid>" for this producer's OWNED node, while the
	// STUB reference (episode.Subject, used verbatim below and by every
	// other entity that points AT an episode) stayed single-prefixed --
	// two different canonical ids for the same episode, so a MERGE meant
	// to unify stub and owned instead created two nodes, and an exact
	// canonical-id lookup for one silently missed the other.
	subjectAttrs := subjectMergeAttrs(episode.Subject, episode.Authorization, episode.EvidenceRefIDs, episode.EndedAt, nil, nil, episode.SourceVersion, nil, a.config.IncludeEmbedBodies)
	// CHAOS-3901 (continued): episodeLabel mirrors
	// devhealthsource/episodes.go's episodeCandidate truncation exactly
	// (goal, capped at 500 bytes via the same len()+slice idiom) -- the
	// same value episode.Subject.Label
	// already carries in the ordinary (self-referencing Subject) case.
	// This matters now that a and b can be the SAME node: when they are,
	// Cypher applies subjectAttrs' SET first and episodeAttrs' SET
	// second, so episodeAttrs' propLabel is the one that survives. Using
	// episode.EpisodeID there (the raw canonical id, not a human label)
	// used to be masked by a/b being two distinct nodes pre-fix; once
	// merged, it silently replaced a meaningful label with the id string
	// on every read.
	episodeLabel := episode.Goal
	if len(episodeLabel) > 500 {
		episodeLabel = episodeLabel[:500]
	}
	episodeSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectEpisode, CanonicalID: episode.EpisodeID, Label: episodeLabel}
	summary := episodeSearchText(episode)
	episodeAttrs := map[string]interface{}{
		propLabel: episodeLabel, propAuthzRepos: authorizationValue(episode.Authorization.RepositorySlugs),
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
	params := map[string]interface{}{"rid": episode.EpisodeID, "subjectAttrs": subjectAttrs, "episodeAttrs": episodeAttrs, "edgeAttrs": edgeAttrs}
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
	case "work_item_ref":
		// CHAOS-3898 §1.5: the ONE genuinely new tombstone shape this
		// design brief needs (the edge case above already existed -- see
		// devhealthsource/tables.go's own doc comment on
		// queryWorkItemDependencies for why). A work_item_ref stub is
		// non-authoritative and heals on re-sync; devhealthsource emits
		// this tombstone UNCONDITIONALLY whenever a target resolves,
		// idempotent whether or not the stub was ever minted -- but the
		// stub node itself must be deleted ONLY when no edge still
		// references it, never unconditionally the way every OTHER
		// subject tombstone's DETACH DELETE below is: a resolved row's
		// edge tombstone (the "relationship"/"edge" case above, applied
		// in the SAME batch) already retired the ref-form edge from
		// THIS row, but a DIFFERENT still-unresolved row's edge to the
		// SAME raw target id may legitimately still exist, and
		// DETACH DELETE-ing the node would silently strand it.
		//
		// Plain DELETE, not DETACH DELETE: defense in depth. FalkorDB
		// (matching Neo4j's Cypher semantics) refuses a DELETE against a
		// node that still has relationships, so a bug in the "NOT
		// (n)--()" guard below fails loudly here rather than silently
		// detaching and deleting a node another edge still points at.
		cypher := fmt.Sprintf("MATCH (n:%s {%s:$org, %s:$kind, %s:$id}) WHERE (n.%s IS NULL OR n.%s <= $effectiveNs) AND NOT (n)--() DELETE n",
			labelSubject, propOrgID, propKind, propCanonicalID, propObservedAtNs, propObservedAtNs)
		_, err := a.api.query(ctx, key, cypher, map[string]interface{}{
			"org": orgID, "kind": "work_item_ref", "id": tombstone.CanonicalID, "effectiveNs": effectiveNs,
		}, false)
		return classifyProjectionError("apply work_item_ref tombstone", err)
	}
	var kind, canonicalID string
	switch strings.ToLower(tombstone.Kind) {
	case "document", "content":
		kind, canonicalID = string(contextfabric.SubjectDocument), "content:"+tombstone.CanonicalID
	case "episode":
		// CHAOS-3901: episodes.go's episodeCandidate already stamps
		// tombstone.CanonicalID with the full "episode:<uuid>" id (the
		// same canonicalID it gives the owned node and the stub subject)
		// -- re-prefixing it here would target a node that was never
		// created (see projectEpisode's doc comment for the matching
		// owned-node half of this fix), so the tombstone would silently
		// match zero rows against a live episode node.
		kind, canonicalID = string(contextfabric.SubjectEpisode), tombstone.CanonicalID
	default:
		kind, canonicalID = tombstone.Kind, tombstone.CanonicalID
	}
	cypher := fmt.Sprintf("MATCH (n:%s {%s:$org, %s:$kind, %s:$id}) WHERE n.%s IS NULL OR n.%s <= $effectiveNs DETACH DELETE n",
		labelSubject, propOrgID, propKind, propCanonicalID, propObservedAtNs, propObservedAtNs)
	_, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "kind": kind, "id": canonicalID, "effectiveNs": effectiveNs}, false)
	return classifyProjectionError("apply subject tombstone", err)
}

// chaos4298SentinelEpoch is the fixed epoch value writeWatermark's ON MATCH
// branch self-heals a pre-epoch node to (a node written by the FIRST
// CHAOS-4298 push -- generation, no epoch -- before this follow-up fix
// shipped). A short, human-legible constant that can never collide with a
// real epoch nonce (always a stringified unix-nanos value, ~19 digits) --
// see writeWatermark's own doc comment for why this must be a FIXED
// constant, never a fresh per-call value, for the self-heal to converge.
const chaos4298SentinelEpoch = "chaos4298-pre-epoch"

// writeWatermark is the ONLY writer of the _AcrWatermark schema (every
// (org, source) sentinel in the graph flows through this one call, from
// ApplyProjectionBatch's own single call site) -- so the generation/epoch
// fields below are properties every existing and future watermark consumer
// sees, not something scoped to one caller.
//
// CHAOS-4298: `generation` is a monotonic per-(org,source) counter, bumped
// on EVERY write regardless of whether backend_watermark's own value
// actually changed. It closes an ABA gap chaos4155WatermarkSnapshot's own
// doc comment (falkorgraph) names: two point-in-time reads of
// backend_watermark ALONE cannot detect a write landing and then being
// followed by a second write that happens to restore the exact prior value
// (w1 -> w2 -> w1) between the reads -- that sequence read as "stable" even
// though a projection write genuinely occurred mid-read. generation cannot
// revert: `coalesce(w.generation, 0) + 1` treats a NEVER-before-generationed
// node (a fresh MERGE-created node, OR a pre-CHAOS-4298 node written before
// this field existed) identically -- both read the property as absent
// (coalesce's 0 fallback) and become generation=1 on this write, then
// increment normally on every write after -- so a source self-heals to a
// meaningful generation on its very next projection write with no backfill
// migration needed.
//
// CHAOS-4298 follow-up (team-lead ruling, 2026-08-26): generation ALONE is
// scoped to one graph node's lifetime -- PurgeOrganization deletes the
// whole graph, so the next write to a (org, source) creates a FRESH node
// whose generation self-heals to 1 again, indistinguishable from the
// SAME node still at generation 1. `epoch` closes this: a writer-generated
// nonce (this call's own projectedAt, nanosecond-resolution -- a real
// collision across two SEPARATE writes is not reachable on real hardware;
// see the ON CREATE branch below) assigned ONCE, the very first time a
// node is created under its current graph-key lifetime, and NEVER
// reassigned after that (ON MATCH only touches epoch via coalesce, which
// is a no-op once epoch is non-null). A purge-and-rebuild always takes the
// ON CREATE branch again (MERGE finds no existing node), so the rebuilt
// node gets a DIFFERENT epoch even if its generation happens to land back
// on 1 -- the two reads can then never agree on (epoch, generation) across
// a purge, closing the gap generation alone could not. A node that already
// has generation but no epoch (written by CHAOS-4298's own first push,
// before this follow-up) self-heals via ON MATCH's
// `coalesce(w.epoch, chaos4298SentinelEpoch)` -- assigned once, to the
// SAME fixed constant regardless of which write performs the heal, then
// stable (like any other epoch) until the next purge.
//
// Duplicated across the ON CREATE/ON MATCH branches rather than a single
// trailing plain SET (mirrors referencedSubjectStubMergeCypher's own
// established ON CREATE/ON MATCH shape, projection.go) -- both branches
// still run inside the ONE MERGE query FalkorDB executes atomically
// against this key, so no separate read-then-write round trip exists for
// a concurrent writer on the same (org, source) to race between.
func (a *Adapter) writeWatermark(ctx context.Context, key string, batch contextfabric.ProjectionBatch, watermark string) error {
	projectedAt := a.now().UTC()
	cypher := fmt.Sprintf(
		"MERGE (w:%s {%s:$org, source:$source}) "+
			"ON CREATE SET w.epoch = $epoch, w.generation = coalesce(w.generation, 0) + 1, w += $attrs "+
			"ON MATCH SET w.epoch = coalesce(w.epoch, $sentinelEpoch), w.generation = coalesce(w.generation, 0) + 1, w += $attrs",
		labelWatermark, propOrgID)
	attrs := map[string]interface{}{
		"cursor": batch.NextCursor, propSourceVersion: batch.SourceVersion, "backend_watermark": watermark,
		"projected_at": projectedAt.Format(time.RFC3339Nano), "projected_at_ns": projectedAt.UnixNano(),
	}
	_, err := a.api.query(ctx, key, cypher, map[string]interface{}{
		"org": batch.OrgID, "source": batch.Source, "attrs": attrs,
		"epoch": strconv.FormatInt(projectedAt.UnixNano(), 10), "sentinelEpoch": chaos4298SentinelEpoch,
	}, false)
	return classifyProjectionError("write projection watermark", err)
}

func (a *Adapter) ProjectionWatermark(ctx context.Context, orgID, source string) (contextfabric.ProjectionWatermark, error) {
	if strings.TrimSpace(orgID) == "" || strings.TrimSpace(source) == "" {
		return contextfabric.ProjectionWatermark{}, errors.New("organization and source are required")
	}
	// CHAOS-3898 S2a: reads the ACTIVE epoch's watermark -- "freshness
	// telemetry and the reuse watermark snapshot read the ACTIVE epoch's
	// set" (design brief §3.4). Stamped projection_write: this call
	// inspects projection-pipeline state, not an investigation read.
	key, err := a.resolveReadKey(ctx, orgID, contextfabric.GraphKeyRoleProjectionWrite)
	if err != nil {
		return contextfabric.ProjectionWatermark{}, err
	}
	cypher := fmt.Sprintf("MATCH (w:%s {%s:$org, source:$source}) RETURN w", labelWatermark, propOrgID)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "source": source}, true)
	if err != nil {
		classified := safeDependencyError("read projection watermark", err)
		// The live path for "never projected" or "purged" (unlike the
		// rows==0/nil-node cases below, which cover a graph that DOES exist
		// but has no watermark node): GRAPH.RO_QUERY against a graph key
		// that never existed, or was just deleted, fails the query itself
		// with FalkorDB's "Invalid graph operation on empty key" --
		// classifyFalkorError already maps that onto ErrNotFound, which
		// safeDependencyError then re-wraps with this operation's prefix.
		// Re-wrap AGAIN here with the CHAOS-3882 backend-neutral sentinel so
		// this, the actual production not-found path, satisfies it too --
		// not just the two defensive branches below that a live FalkorDB
		// never actually takes.
		if errors.Is(classified, ErrNotFound) {
			return contextfabric.ProjectionWatermark{}, fmt.Errorf("%w: %w", classified, contextfabric.ErrProjectionWatermarkNotFound)
		}
		return contextfabric.ProjectionWatermark{}, classified
	}
	if len(rows) == 0 {
		return contextfabric.ProjectionWatermark{}, notFoundWatermarkErr()
	}
	w, ok := rows[0]["w"].(*node)
	if !ok || w == nil {
		return contextfabric.ProjectionWatermark{}, notFoundWatermarkErr()
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

// notFoundWatermarkErr wraps BOTH this package's own ErrNotFound (the
// existing contract adapter_live_integration_test.go's
// TestLiveReadOnlyPathAfterPurgeReturnsNotFoundWithoutAutoCreating pins, and
// cmd/acr-projector's readiness probe checks directly) and
// contextfabric.ErrProjectionWatermarkNotFound (CHAOS-3882's backend-neutral
// classification, ProjectionBackend's own doc comment) in one error, so
// existing in-package callers and the new backend-agnostic one both see the
// sentinel they check for.
func notFoundWatermarkErr() error {
	return fmt.Errorf("%w: %w", ErrNotFound, contextfabric.ErrProjectionWatermarkNotFound)
}

// PurgeOrganization is DELIBERATELY NOT wired through the CHAOS-3898 S2a
// KeyResolver: it still derives the legacy, unsuffixed key directly. This
// method purges whatever graph is currently reachable at that key
// unconditionally -- exactly the "unconditional GRAPH.DELETE" hazard the
// design brief's §3.5 lifecycle machine exists to close (a SERVING graph
// must never be purged by any path). Routing it through resolveReadKey
// today, with no accompanying CAS/guard machinery gating the call, would
// let a future EpochResolver wiring purge a currently-ACTIVE, already-
// flipped organization's graph the instant that wiring lands -- worse than
// today's unconditional-but-at-least-single-key behavior. The design
// brief's item 8 (MANDATORY conversion of PurgeOrganization/performRebuild/
// the CHAOS-3882 recovery path into lifecycle transitions -- a
// begin_retire-based "retire every epoch for this org" instead of a bare
// delete) is the follow-up slice that replaces this method's call sites
// with the safe path; see this repository's CHAOS-3898 S2a PR description
// for the explicit scope split.
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

// contentSearchText, episodeSearchText, and entitySearchText (the fallback
// the subjectSearchText switch routes every template-less entity kind to --
// decision and metric today, any future kind by default) exist so the
// projection write path and CHAOS-3778's embedding pass derive their text
// from ONE expression rather than two identical-looking ones. All three are
// UNBOUNDED compositions, deliberately uncapped (CHAOS-3833 review,
// ratified): the lexical arm indexes the full text today, and capping the
// shared composition would regress lexical retrieval -- the spec's own T3
// rollback criterion. The two arms therefore share the composition, not its
// full extent: lexical stores all of it, the embed side the first
// MaxTextRunes runes of the SAME string (collectEmbedTargets), so their
// agreement is a shared-prefix statement for this routing-defined class and
// exact byte-identity only for the kinds with a declared template
// (search_text.go's switch is the boundary). One expression is still what
// makes even the prefix claim a statement about MECHANISM rather than about
// which text each arm happened to see (see
// graphrank.DistinctMechanismCount) -- two copies of the concatenation
// would be one edit away from silently breaking it.
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
