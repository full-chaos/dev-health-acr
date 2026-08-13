package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
)

// errVectorIndexNotReady reports that the organization's vector index exists
// but is not in a state this batch can reason about (still building, or
// reporting a status this adapter does not recognize). It is a REPLAYABLE
// condition: the batch fails, the checkpoint holds, and the next tick tries
// again once the index settles.
var errVectorIndexNotReady = errors.New("context fabric vector index is not ready for embedding")

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
func (a *Adapter) embedProjectionBatch(ctx context.Context, key string, batch contextfabric.ProjectionBatch) error {
	if a.embedder == nil {
		return nil
	}
	// Codex round-2 R2-1: the write side re-verifies the index every batch
	// rather than trusting anything bootstrap cached.
	//
	// Codex round-3 F1: what happens when that verification FAILS is the
	// failed-VERIFY door into the room R2-3 closed by the failed-CLEAR door.
	// The earlier form degraded and returned nil, so ApplyProjectionBatch went
	// on to write the watermark -- with the batch's NEW search_text durably
	// stored and the OLD vector still attached, still carrying the CONFIGURED
	// identity. Once the transient condition cleared, the read fence found
	// nothing wrong (the identity matches!) and served that stale vector
	// against text it was never derived from. Permanently, because the
	// checkpoint had advanced and nothing replays.
	//
	// The invariant, stated once: a batch may only commit when the stored
	// vector state is CONSISTENT with the text it just wrote -- every affected
	// node either re-embedded or cleared. Anything else fails the batch, so
	// the checkpoint holds and the next tick reconciles.
	dimension, state, err := a.vectorIndexDimension(ctx, key)
	switch {
	case err != nil:
		// Transient: the probe itself failed, so nothing is known about the
		// index and clearing cannot be reasoned about either. Replay.
		a.recordVectorDegraded(ctx, batch.OrgID)
		return fmt.Errorf("verify vector index before embedding: %w", err)
	case state == vectorIndexUnknown, state == vectorIndexAbsent:
		// UNDER CONSTRUCTION, an undecodable status, or an index that has
		// vanished. All are expected to resolve on their own -- bootstrap
		// creates and waits for the index -- so replay rather than commit
		// against an index this batch could not reason about.
		a.recordVectorDegraded(ctx, batch.OrgID)
		return errVectorIndexNotReady
	case dimension != a.embedder.Identity().Dimension:
		// The one PERSISTENT failure that operator action must clear
		// (AC-3778-7). Failing every batch forever would stall canonical
		// projection entirely over an optional retrieval feature, which is a
		// worse outcome than degraded retrieval. So instead of replaying, the
		// batch makes its own vector state honest -- clears the vectors it
		// just invalidated -- and commits. Reads are already fenced off by the
		// same dimension mismatch, so nothing can serve them in the meantime.
		//
		// If that clear fails, R2-3 applies and the batch fails.
		a.recordVectorDegraded(ctx, batch.OrgID)
		stale := collectEmbedTargets(batch, embedMaxRunes(a.embedder))
		if err := a.clearNodeVectors(ctx, key, batch.OrgID, stale); err != nil {
			return err
		}
		a.recordVectorProjection(ctx, batch.OrgID, 0, len(stale))
		return nil
	}
	identity := a.embedder.Identity()
	targets := collectEmbedTargets(batch, embedMaxRunes(a.embedder))
	if len(targets) == 0 {
		return nil
	}
	texts := make([]string, 0, len(targets))
	for _, target := range targets {
		texts = append(texts, target.text)
	}
	vectors, err := a.embedder.Embed(ctx, texts)
	if err != nil || len(vectors) != len(targets) {
		// Codex round-1 F3: the batch has ALREADY written new search_text to
		// these nodes and the watermark is about to advance. Leaving the OLD
		// vector attached would pair model-A's understanding of yesterday's
		// text with today's text, permanently and silently -- nothing retries
		// until a rebuild, and no read-side check can detect it, because the
		// vector is present, well-formed, and stamped with a matching
		// identity.
		//
		// So the vector is CLEARED instead. A node with no vector degrades
		// honestly to lexical retrieval; a node with a stale vector lies.
		a.recordVectorDegraded(ctx, batch.OrgID)
		if err := a.clearNodeVectors(ctx, key, batch.OrgID, targets); err != nil {
			return err
		}
		a.recordVectorProjection(ctx, batch.OrgID, 0, len(targets))
		return nil
	}
	for index, target := range targets {
		if err := a.writeNodeVector(ctx, key, batch.OrgID, target, vectors[index], identity); err != nil {
			// Same reasoning, mid-batch: targets before this one carry fresh
			// vectors and are fine; this one and everything after it still
			// carry yesterday's, so clear exactly those.
			a.recordVectorDegraded(ctx, batch.OrgID)
			if clearErr := a.clearNodeVectors(ctx, key, batch.OrgID, targets[index:]); clearErr != nil {
				return clearErr
			}
			a.recordVectorProjection(ctx, batch.OrgID, index, len(targets)-index)
			return nil
		}
	}
	a.recordVectorProjection(ctx, batch.OrgID, len(targets), 0)
	return nil
}

// clearNodeVectors removes the embedding and its identity properties from
// every named node, in ONE round trip.
//
// Verified live (graph module 42002): `SET n.embedding = NULL` both removes
// the property and drops the node out of the vector index, so a cleared node
// genuinely stops being a vector-search result rather than merely losing its
// metadata.
//
// A FAILED CLEAR FAILS THE BATCH (codex round-2 R2-3, orchestrator ruling).
// This was previously best-effort with telemetry, which left a genuinely
// unrecoverable state: when the embed failed AND the clear also failed, the
// stale vector kept the CONFIGURED identity and dimension, so the read-side
// fence saw nothing wrong and served it -- permanently, because the watermark
// advanced and nothing retries until a rebuild. Telemetry is not containment.
//
// Returning an error here stops ApplyProjectionBatch before it writes the
// watermark, so the projection checkpoint does not advance past a batch whose
// vector state is unreconciled. Projection is idempotent, so the next tick
// replays the batch and reconciles. A stalled checkpoint is loud, bounded, and
// self-healing; a silently-serving stale vector is none of those.
func (a *Adapter) clearNodeVectors(ctx context.Context, key, orgID string, targets []embedTarget) error {
	if len(targets) == 0 {
		return nil
	}
	list := make([]interface{}, 0, len(targets))
	for _, target := range targets {
		list = append(list, map[string]interface{}{"kind": target.kind, "id": target.canonicalID})
	}
	cypher := fmt.Sprintf(
		"UNWIND $targets AS t "+
			"MATCH (n:%s {%s:$org, %s:t.kind, %s:t.id}) "+
			"SET n.%s = NULL, n.%s = NULL, n.%s = NULL",
		labelSubject, propOrgID, propKind, propCanonicalID,
		propEmbedding, propEmbedderIdentity, propEmbedderDimension,
	)
	if _, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "targets": list}, false); err != nil {
		a.recordVectorDegraded(ctx, orgID)
		return safeDependencyError("clear stale node embeddings", err)
	}
	return nil
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
// retrieval is configured, and enforces AC-3778-7's dimension fence.
//
// Separate from bootstrapSchema's constraint work because it is CONDITIONAL:
// an organization graph bootstrapped before an embedder was configured is
// perfectly valid and must not be treated as broken.
//
// AC-3778-7 ("changing the embedder identity or dimension invalidates the
// affected vectors and forces a deterministic rebuild; a stale-dimension
// vector is never queried") is enforced here rather than per query, because
// the answer cannot change without a rebuild and a per-query check would cost
// a round trip on the AC-3778-5 budget for a constant.
//
// A dimension mismatch DISABLES vector retrieval for that organization rather
// than failing the request or silently rebuilding:
//
//   - Failing would take down lexical retrieval too, over an optional
//     improvement, for every request until an operator noticed.
//   - Silently dropping and recreating the index would discard every stored
//     vector for the organization on a config typo, with no operator
//     decision -- and would still leave every NODE carrying a stale vector of
//     the old width until a full reprojection, so it would not even be
//     correct.
//
// The prescribed recovery is the existing `acr-projector rebuild --org` path,
// which already resets checkpoints and bumps the rebuild epoch (invalidating
// CHAOS-3782 answer reuse). Until then the organization answers lexically,
// which is exactly the pre-CHAOS-3778 behavior.
//
// FalkorDB's own hard "Vector dimension mismatch" error on a wrong-width query
// (verified live) is a second, independent fail-closed layer underneath this
// one.
func (a *Adapter) ensureVectorIndex(ctx context.Context, key string) error {
	if a.embedder == nil {
		return nil
	}
	want := a.embedder.Identity().Dimension
	existing, state, err := a.vectorIndexDimension(ctx, key)
	if err != nil {
		return err
	}
	if state == vectorIndexAbsent {
		if err := a.createVectorIndex(ctx, key, want); err != nil {
			return err
		}
		// Codex round-3 (bootstrap gap): wait for it, exactly as bootstrap
		// already waits for constraints. Index creation is asynchronous, so
		// without this the very first batch after bootstrap could find the
		// index UNDER CONSTRUCTION, skip embedding, and -- before F1 -- still
		// advance its watermark past nodes that were never embedded. F1 now
		// fails that batch rather than committing it, but making the first
		// batch FAIL on a condition bootstrap could simply have waited out
		// would be a self-inflicted stall.
		return a.pollVectorIndexOperational(ctx, key)
	}
	// An index that exists but is unknown or mismatched is NOT created and NOT
	// repaired here. Whether it may be used is decided fresh at each read and
	// each write by vectorIndexUsable -- see R2-1 below for why no verdict is
	// cached.
	_ = existing
	return nil
}

// pollVectorIndexOperational waits for the vector index to become usable,
// mirroring pollConstraintsOperational's contract and bound.
//
// Same strict-allowlist posture: only a KNOWN, readable dimension counts as
// ready. An unknown state keeps polling until the deadline rather than being
// accepted, because "I could not read it" must never resolve to "it is fine"
// (the F5 lesson).
func (a *Adapter) pollVectorIndexOperational(ctx context.Context, key string) error {
	deadline := a.now().Add(a.config.RequestTimeout)
	for {
		_, state, err := a.vectorIndexDimension(ctx, key)
		if err != nil {
			return err
		}
		if state == vectorIndexKnown {
			return nil
		}
		if a.now().After(deadline) {
			return fmt.Errorf("%w: vector index did not become operational for key %q", errVectorIndexNotReady, key)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// vectorIndexUsable reports whether this organization's vector index exists,
// is OPERATIONAL, and was built at the dimension the configured embedder
// produces.
//
// Codex round-2 R2-1: this is evaluated FRESH, never cached across requests.
// The earlier design cached an ENABLED verdict for the process lifetime on the
// premise that "the configured embedder cannot change without a restart". That
// premise was true per PROCESS and false per DEPLOYMENT: acr-api and
// acr-projector construct their embedders independently, from their own
// environments, and identical configuration between them is DOCUMENTED, not
// enforced. A projector configured with a different same-dimension model would
// write identity-B vectors into a graph whose reader had already cached
// ENABLED and would therefore never probe again -- serving another model's
// vectors indefinitely, which is exactly the corruption the fence exists to
// stop.
func (a *Adapter) vectorIndexUsable(ctx context.Context, key string) (bool, error) {
	if a.embedder == nil {
		return false, nil
	}
	dimension, state, err := a.vectorIndexDimension(ctx, key)
	if err != nil {
		return false, err
	}
	return state == vectorIndexKnown && dimension == a.embedder.Identity().Dimension, nil
}

// vectorIndexState classifies what vectorIndexDimension could learn.
type vectorIndexState int

const (
	// vectorIndexAbsent: no vector index on Subject.embedding exists.
	vectorIndexAbsent vectorIndexState = iota
	// vectorIndexKnown: an index exists and reported its dimension.
	vectorIndexKnown
	// vectorIndexUnknown: an index exists but its dimension or status could
	// not be determined. Codex round-1 F5 -- this must never be conflated
	// with absent, because the recovery for absent (create it) silently
	// succeeds against an existing index and leaves it enabled.
	vectorIndexUnknown
)

// vectorIndexDimension reports the dimension an existing vector index on
// Subject.embedding was built with, and whether that answer is trustworthy.
//
// Fail-closed rules (codex round-1 F5): an index whose status is not
// OPERATIONAL, or which does not report a usable dimension, is
// vectorIndexUnknown -- never treated as a match and never treated as absent.
func (a *Adapter) vectorIndexDimension(ctx context.Context, key string) (int, vectorIndexState, error) {
	indexes, err := a.api.indexes(ctx, key)
	if err != nil {
		return 0, vectorIndexUnknown, safeDependencyError("inspect vector index", err)
	}
	for _, index := range indexes {
		if index.Label != labelSubject {
			continue
		}
		types, ok := index.Types[propEmbedding]
		if !ok {
			continue
		}
		isVector := false
		for _, indexType := range types {
			if strings.EqualFold(indexType, "VECTOR") {
				isVector = true
				break
			}
		}
		if !isVector {
			continue
		}
		// Strict allowlist on status, mirroring pollConstraintsOperational's
		// posture: only an explicitly OPERATIONAL index may be queried. A
		// blank status (a server version that does not report one) is
		// unknown, not acceptable.
		if !strings.EqualFold(strings.TrimSpace(index.Status), "OPERATIONAL") {
			return 0, vectorIndexUnknown, nil
		}
		dimension, ok := index.Dimension()
		if !ok {
			return 0, vectorIndexUnknown, nil
		}
		return dimension, vectorIndexKnown, nil
	}
	return 0, vectorIndexAbsent, nil
}

// verifyStoredEmbedderIdentity is the READ-side half of the AC-3778-7 fence
// (codex round-1 F2).
//
// The fence was WRITE-only: embedder_identity was stamped onto every node but
// never read back, and the hosted API's read path never runs bootstrap at all,
// so it checked neither the index dimension nor the stored identity. A
// same-dimension model swap -- nomic and embeddinggemma are both 768, which is
// exactly the live LM Studio scenario -- would therefore serve stale model-A
// vectors into resolution, where they can alter ranking or, via corroboration,
// commit a subject. The dimension check cannot see it, and the stored identity
// says what was ASKED for at write time, so only comparing that stored value
// against the CURRENTLY configured embedder closes it.
//
// This composes with embedprovider's response-model verification rather than
// duplicating it: that one catches a server serving the wrong model at WRITE
// time, this one catches a graph already holding vectors from a different
// model at READ time. Same invariant, two boundaries, and neither subsumes the
// other -- a graph embedded before the check existed has no write-time
// protection at all.
//
// The query is a bounded existence probe (LIMIT 1), not a count: it asks "does
// any node carry an identity other than the configured one", and stops at the
// first. It runs once per graph key per process, cached, so a full scan in the
// worst case is paid once rather than per request.
func (a *Adapter) verifyStoredEmbedderIdentity(ctx context.Context, key, orgID string) (bool, error) {
	identity := a.embedder.Identity().String()
	// Codex round-2 R2-2: the predicate is anchored on the EMBEDDING being
	// present, not on the identity being present.
	//
	// The earlier form asked only "is there a node whose identity is set and
	// differs", which let a node with an indexed embedding and a NULL identity
	// pass as clean -- treating UNKNOWN PROVENANCE as verified provenance.
	// That is the same mistake as F5's absent-versus-unknown conflation, one
	// layer down: a vector whose producer cannot be named is exactly as
	// unusable as one produced by the wrong model, because nothing can rule
	// out that it came from a different embedder. Such a node is reachable in
	// practice -- anything written before identity stamping existed, or by a
	// producer that wrote a vector without stamping one.
	cypher := fmt.Sprintf(
		"MATCH (n:%s {%s:$org}) WHERE n.%s IS NOT NULL AND (n.%s IS NULL OR n.%s <> $identity) RETURN n.%s LIMIT 1",
		labelSubject, propOrgID, propEmbedding, propEmbedderIdentity, propEmbedderIdentity, propCanonicalID,
	)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "identity": identity}, true)
	if err != nil {
		return false, safeDependencyError("verify stored embedder identity", err)
	}
	return len(rows) == 0, nil
}

// ensureVectorReadable is the read path's own fence check, run before any
// vector query for an organization (codex round-1 F2).
//
// NO VERDICT IS CACHED ACROSS REQUESTS (codex round-2 R2-1). It is memoized
// only for the lifetime of one ResolveSubjects call, by resolutionFence, so a
// resolution pays one bounded probe rather than one per interpreted term. See
// vectorIndexUsable for why a longer-lived cache was wrong.
//
// Any error checking the fence DISABLES vector retrieval for this request
// rather than enabling it. An unverifiable fence is not a passed fence.
func (a *Adapter) ensureVectorReadable(ctx context.Context, key, orgID string) bool {
	if a.embedder == nil {
		return false
	}
	usable, err := a.vectorIndexUsable(ctx, key)
	if err != nil || !usable {
		return false
	}
	matches, err := a.verifyStoredEmbedderIdentity(ctx, key, orgID)
	if err != nil || !matches {
		return false
	}
	return true
}
