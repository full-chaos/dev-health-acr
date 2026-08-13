package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
)

// embedTarget is one node whose search text needs a vector.
type embedTarget struct {
	kind        string
	canonicalID string
	text        string
}

// collectEmbedTargets gathers the (node, search text) pairs a batch just
// wrote, from the BATCH ITSELF rather than by reading the graph back.
//
// Reading back would be both slower and wrong: another source's batch for the
// same organization may have merged into the same nodes in between, and this
// pass must embed the text THIS batch wrote, so the vector and the
// search_text it was derived from always agree.
//
// The text used is byte-for-byte the same value projection.go writes to
// propSearchText for each node kind (entitySearchText for entities,
// title+body for content, the summary for episodes). That identity is the
// point: lexical and vector retrieval must search the SAME corpus so their
// agreement measures a difference in MECHANISM and nothing else (see
// docs/design/context-fabric-vector-retrieval.md §3 and graphrank's
// DistinctMechanismCount).
//
// Relationships are deliberately absent. TRD §19.4.4 forbids a model in the
// write path of an EDGE; embedding a node's label is not creating an edge, and
// this pass never touches one. Referenced-but-not-owned endpoint stubs are
// also absent -- this batch does not own their text, so it must not write a
// vector derived from a label it did not author.
//
// Results are deduplicated and ordered deterministically so a replay of the
// same batch issues byte-identical requests.
func collectEmbedTargets(batch contextfabric.ProjectionBatch, maxRunes int) []embedTarget {
	byKey := make(map[string]embedTarget)
	add := func(kind contextfabric.SubjectKind, canonicalID, text string) {
		text = strings.TrimSpace(text)
		if text == "" || strings.TrimSpace(canonicalID) == "" {
			return
		}
		key := string(kind) + "\x00" + canonicalID
		byKey[key] = embedTarget{
			kind: string(kind), canonicalID: canonicalID,
			text: embedprovider.TruncateRunes(text, maxRunes),
		}
	}
	for _, entity := range batch.Entities {
		add(entity.Subject.Kind, entity.Subject.CanonicalID, entitySearchText(entity))
	}
	for _, content := range batch.Contents {
		add(contextfabric.SubjectDocument, "content:"+content.ContentID,
			strings.TrimSpace(content.Title+"\n"+content.Body))
	}
	for _, episode := range batch.Episodes {
		add(contextfabric.SubjectEpisode, "episode:"+episode.EpisodeID, episodeSearchText(episode))
	}
	targets := make([]embedTarget, 0, len(byKey))
	for _, target := range byKey {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].kind == targets[j].kind {
			return targets[i].canonicalID < targets[j].canonicalID
		}
		return targets[i].kind < targets[j].kind
	})
	return targets
}

// embedProjectionBatch writes one vector per node this batch authored.
//
// It runs AFTER every node write in the batch, so a vector is only ever
// attached to a node that already exists with the search text it was derived
// from.
//
// DEGRADED, NEVER FAILED. An embedder error, a timeout, or a graph write
// failure here returns nil: the batch's canonical projection already succeeded
// and is durable, and refusing to advance the checkpoint over a missing vector
// would stall the whole projection pipeline behind an optional retrieval
// improvement. A node without a vector is simply invisible to vector search
// and still fully reachable lexically -- degraded retrieval, not lost data.
// The next rebuild re-embeds it. This is the same "embeddings are projection
// artifacts" reasoning that makes AC-3778-7 fall out of the existing epoch
// machinery: a vector is derived, never a source of truth.
//
// One batched embed call is issued per MaxBatch-sized chunk, not one per node
// (contextfabric.Embedder.Embed takes a slice for exactly this reason).
func (a *Adapter) embedProjectionBatch(ctx context.Context, key string, batch contextfabric.ProjectionBatch) {
	if a.embedder == nil {
		return
	}
	identity := a.embedder.Identity()
	targets := collectEmbedTargets(batch, embedMaxRunes(a.embedder))
	if len(targets) == 0 {
		return
	}
	texts := make([]string, 0, len(targets))
	for _, target := range targets {
		texts = append(texts, target.text)
	}
	vectors, err := a.embedder.Embed(ctx, texts)
	if err != nil || len(vectors) != len(targets) {
		a.recordVectorDegraded(ctx, batch.OrgID)
		return
	}
	for index, target := range targets {
		if err := a.writeNodeVector(ctx, key, batch.OrgID, target, vectors[index], identity); err != nil {
			a.recordVectorDegraded(ctx, batch.OrgID)
			return
		}
	}
}

// writeNodeVector attaches one vector and its embedder identity to an EXISTING
// node.
//
// MATCH, never MERGE: this pass must never create a node. If the node is gone
// (a tombstone in the same batch, say) the write is a silent no-op, which is
// the correct outcome -- a vector for a subject that no longer exists is not
// something to resurrect the subject over.
//
// The identity and dimension travel WITH the vector, on the same node, so a
// later read can tell whether a stored vector was produced by the currently
// configured embedder without consulting anything else (AC-3778-7).
func (a *Adapter) writeNodeVector(ctx context.Context, key, orgID string, target embedTarget, vector []float32, identity contextfabric.EmbedderIdentity) error {
	if len(vector) == 0 {
		return nil
	}
	cypher := fmt.Sprintf(
		"MATCH (n:%s {%s:$org, %s:$kind, %s:$id}) "+
			"SET n.%s = vecf32($vec), n.%s = $identity, n.%s = $dimension",
		labelSubject, propOrgID, propKind, propCanonicalID,
		propEmbedding, propEmbedderIdentity, propEmbedderDimension,
	)
	params := map[string]interface{}{
		"org": orgID, "kind": target.kind, "id": target.canonicalID,
		"vec": vectorParam(vector), "identity": identity.String(), "dimension": int64(identity.Dimension),
	}
	_, err := a.api.query(ctx, key, cypher, params, false)
	if err != nil {
		return safeDependencyError("write node embedding", err)
	}
	return nil
}

// embedMaxRunes reads the per-text truncation budget from the concrete
// embedder when it exposes one, falling back to the package default. The port
// deliberately does not carry it -- it is a tuning knob of one implementation,
// not a contract every embedder must honor.
func embedMaxRunes(embedder contextfabric.Embedder) int {
	type runeBounded interface{ MaxTextRunes() int }
	if bounded, ok := embedder.(runeBounded); ok {
		if limit := bounded.MaxTextRunes(); limit > 0 {
			return limit
		}
	}
	return embedprovider.DefaultMaxTextRunes
}

// ensureVectorIndex creates the organization's vector index once vector
// retrieval is configured. Separate from bootstrapSchema's constraint work
// because it is CONDITIONAL: an organization graph bootstrapped before an
// embedder was configured is perfectly valid and must not be treated as
// broken.
func (a *Adapter) ensureVectorIndex(ctx context.Context, key string) error {
	if a.embedder == nil {
		return nil
	}
	return a.createVectorIndex(ctx, key, a.embedder.Identity().Dimension)
}
