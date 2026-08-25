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

// embedSkipCounts tallies, BY REASON, subjects the embed pass deliberately
// left unvectored in one batch (spec §7 D2: "the count must be
// distinguishable from other gating ... its own reason label"). Kind and
// IDOnly are independent counters rather than one combined number, because
// they are operationally different facts an operator must be able to tell
// apart: Kind says "this whole subject KIND is out of embed scope"
// (today: organization, CHAOS-3833); IDOnly says "this ROW carries no
// name to be found by" (CHAOS-3835). Collapsing them would make a sudden
// rise in one indistinguishable from the other -- the same reasoning that
// keeps RecordVectorRetrievalDegraded and RecordVectorRetrievalSuppressed
// as two signals rather than one with a string.
type embedSkipCounts struct {
	Kind   int
	IDOnly int
}

// Total is the combined count, for callers (accounting against the
// organization's total node count) that only need "how many were
// deliberately unembedded", not the breakdown.
func (c embedSkipCounts) Total() int { return c.Kind + c.IDOnly }

// collectEmbedTargets gathers the (node, search text) pairs a batch just
// wrote, from the BATCH ITSELF rather than by reading the graph back.
//
// Reading back would be both slower and wrong: another source's batch for the
// same organization may have merged into the same nodes in between, and this
// pass must embed the text THIS batch wrote, so the vector and the
// search_text it was derived from always agree.
//
// The text used is byte-for-byte the same value projection.go writes to
// propSearchText for each node kind -- the ONE per-kind composition
// (subjectSearchText for entities, contentSearchText for content,
// episodeSearchText for episodes; CHAOS-3833 closed the content path's
// inlined duplicate of that expression). That identity is the point:
// lexical and vector retrieval must search the SAME corpus so their
// agreement measures a difference in MECHANISM and nothing else (see
// docs/design/context-fabric-vector-retrieval.md §3 and graphrank's
// DistinctMechanismCount). The one owned divergence is the embed-side
// MaxTextRunes tail truncation of the UNBOUNDED compositions -- the class
// the subjectSearchText switch defines by routing (see search_text.go's
// header): episode text, content text, and any entity kind without a
// declared template, whose lexical arm indexes the full composed text
// while this pass embeds its first MaxTextRunes runes. The validation
// floor (embedprovider.MinimumMaxTextRunes) covers every complete
// template, so a templated kind is never truncated and the
// mechanism-agreement claim is exact for templated kinds, a shared-prefix
// statement for everything else.
//
// Relationships are deliberately absent. TRD §19.4.4 forbids a model in the
// write path of an EDGE; embedding a node's label is not creating an edge, and
// this pass never touches one. Referenced-but-not-owned endpoint stubs are
// also absent -- this batch does not own their text, so it must not write a
// vector derived from a label it did not author.
//
// Results are deduplicated and ordered deterministically so a replay of the
// same batch issues byte-identical requests.
//
// The second return value counts nodes DELIBERATELY skipped, BY REASON
// (embedSkipCounts; spec §2/§7 D2, extended by CHAOS-3835 §6.5 T5):
//   - Kind -- the whole-kind skip-list (CHAOS-3833) -- today only the
//     organization node, whose text is a raw org UUID: its vector is pure
//     noise and the organization resolves via ExactHint.
//   - IDOnly -- the CHAOS-3835 per-row id-only skip (isPureIdentifierSubject)
//     -- today only ci_pipeline_run rows whose name/branch carry no content
//     beyond a bare identifier.
//
// Both counts are REPORTED through RecordVectorProjection, never inferred:
// a skipped node is otherwise indistinguishable from a healthy corpus.
// Skipped subjects stay fully lexical (the write path still composes their
// search_text) and, for the id-only reason, must ALSO receive no
// embedder-identity stamp -- collectEmbedTargets never adds them to
// byKey, so writeNodeVector never runs for them and they stay invisible to
// the read-side fence's IS NOT NULL predicate (verifyStoredEmbedderIdentity).
//
// The third return value is the id-only-skipped set AS CLEARABLE TARGETS
// (kind + canonicalID only -- text is irrelevant to a clear). A subject can
// carry a STALE vector from a PRIOR batch, written back when its name/branch
// still had content, that a later batch's id-only verdict must not let
// survive: this batch never calls add() for that row, so nothing here would
// otherwise touch its old embedding, and a stale vector paired with a
// composition tag that still matches the CURRENT adapter would pass the
// read-side fence (verifyStoredEmbedderIdentity) and get served against
// search_text that no longer has any relationship to it. The caller
// (embedProjectionBatch) is responsible for feeding this slice through the
// SAME clearNodeVectors mechanism every other stale-vector path already
// uses, on every commit branch -- clearing a row with no prior vector is an
// idempotent no-op (clearNodeVectors' SET ... = NULL), so doing it
// unconditionally is always safe.
func collectEmbedTargets(batch contextfabric.ProjectionBatch, maxRunes int, includeBodies bool) ([]embedTarget, []embedTarget, embedSkipCounts) {
	byKey := make(map[string]embedTarget)
	skippedKindKeys := make(map[string]struct{})
	skippedIDOnly := make(map[string]embedTarget)
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
		if embedKindSkipped(entity.Subject.Kind) {
			skippedKindKeys[string(entity.Subject.Kind)+"\x00"+entity.Subject.CanonicalID] = struct{}{}
			continue
		}
		if isPureIdentifierSubject(entity) {
			// CHAOS-3835: a record-level decision, checked BEFORE the text is
			// even composed for embedding -- the row never reaches add(), so
			// it can never become a byKey entry, never get a vector, and
			// never get an embedder-identity stamp. The write path still
			// composes and stores this row's ordinary search_text
			// (subjectMergeAttrs), so lexical retrieval is unaffected; only
			// the embed decision changes. It IS, however, a clear candidate
			// (see the doc comment above) -- a prior batch may have left a
			// vector on this exact kind/canonicalID.
			key := string(entity.Subject.Kind) + "\x00" + entity.Subject.CanonicalID
			skippedIDOnly[key] = embedTarget{kind: string(entity.Subject.Kind), canonicalID: entity.Subject.CanonicalID}
			continue
		}
		add(entity.Subject.Kind, entity.Subject.CanonicalID, subjectSearchText(entity, includeBodies))
	}
	for _, content := range batch.Contents {
		add(contextfabric.SubjectDocument, "content:"+content.ContentID, contentSearchText(content))
	}
	for _, episode := range batch.Episodes {
		// CHAOS-3901: episode.EpisodeID (devhealthsource/episodes.go's
		// episodeCandidate) already carries the full "episode:<uuid>"
		// canonical id -- see projection.go's projectEpisode doc comment
		// for the full mismatch this re-prefixing caused (an embed target
		// keyed by the doubled id could never match the owned node the
		// fixed projectEpisode now writes under the single-prefixed id).
		add(contextfabric.SubjectEpisode, episode.EpisodeID, episodeSearchText(episode))
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
	idOnlyTargets := make([]embedTarget, 0, len(skippedIDOnly))
	for _, target := range skippedIDOnly {
		idOnlyTargets = append(idOnlyTargets, target)
	}
	sort.Slice(idOnlyTargets, func(i, j int) bool {
		if idOnlyTargets[i].kind == idOnlyTargets[j].kind {
			return idOnlyTargets[i].canonicalID < idOnlyTargets[j].canonicalID
		}
		return idOnlyTargets[i].kind < idOnlyTargets[j].kind
	})
	return targets, idOnlyTargets, embedSkipCounts{Kind: len(skippedKindKeys), IDOnly: len(skippedIDOnly)}
}

// embedKindSkipped is the CHAOS-3833 embed kind skip-list. Membership is a
// COMPOSITION decision (spec §4 Layer B): adding or removing a kind changes
// which texts have vectors, so it must ride an embedTextTemplateVersion
// bump like any template change. CHAOS-3835 (T5) extends the skip decision
// to id-only CI-run texts via isPureIdentifierSubject -- a RECORD-level
// decision rather than a kind-wide one, so it is a separate function and a
// separate reported reason (embedSkipCounts.IDOnly), not a member of this
// list.
func embedKindSkipped(kind contextfabric.SubjectKind) bool {
	return kind == contextfabric.SubjectOrganization
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
		// CHAOS-3835 round-4 finding 1: no embedder configured (e.g.
		// ACR_CONTEXT_FABRIC_EMBED_BASE_URL unset) does NOT mean this
		// batch has nothing to do to the graph's vector state. A subject
		// may already carry a vector + embedder-identity stamp from an
		// EARLIER batch, written while an embedder WAS configured; if
		// THIS batch's projection makes that same subject id-only, the
		// stale vector must still be cleared, or it survives the entire
		// disabled interval and -- verified against ensureVectorReadable/
		// verifyStoredEmbedderIdentity -- passes the read fence again the
		// moment the embedder is RE-ENABLED with the same identity and
		// dimension: the fence only compares the stored identity string
		// to the CURRENTLY configured embedder, it never asks "should
		// this specific row have a vector at all". While disabled, reads
		// are safe (ensureVectorReadable/vectorIndexUsable both return
		// false when a.embedder is nil, so nothing is ever served from a
		// stale vector during the interval itself) -- the danger is
		// entirely in what re-enabling later finds.
		//
		// collectEmbedTargets and clearNodeVectors need no embedder --
		// collection is pure batch inspection and the clear is a plain
		// graph write -- so both run here exactly as they do on every
		// other commit path in this function. Embedding itself (Embed,
		// writeNodeVector, the index/dimension checks below) still
		// requires the embedder and stays out of this branch.
		_, idOnlyTargets, skipped := collectEmbedTargets(batch, a.embedBudgetRunes(), a.config.IncludeEmbedBodies)
		if err := a.clearNodeVectors(ctx, key, batch.OrgID, idOnlyTargets); err != nil {
			return err
		}
		// embedded=0 (no embedder, nothing was ever going to be embedded
		// this batch); cleared=0 for the same round-2 finding-1 reason as
		// every other id-only-only clear in this file (routine, not a
		// genuine stale/error event -- already covered by skipped.IDOnly).
		a.recordVectorProjection(ctx, batch.OrgID, 0, 0, skipped)
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
		stale, staleIDOnly, staleSkipped := collectEmbedTargets(batch, a.embedBudgetRunes(), a.config.IncludeEmbedBodies)
		// The id-only-skipped set rides the SAME clear as everything else
		// this branch is already invalidating (finding 1: a row that just
		// became id-only must not keep a stale vector any more than a row
		// whose dimension went stale does).
		if err := a.clearNodeVectors(ctx, key, batch.OrgID, append(stale, staleIDOnly...)); err != nil {
			return err
		}
		// Round-2 finding 1: `cleared` reports only the DIMENSION-MISMATCH
		// clear (stale, len(stale)) -- a genuine "this vector is now
		// invalid" event worth a Warn. staleIDOnly's clear happened (the
		// call above includes it) but is a ROUTINE, DETERMINISTIC
		// consequence of every id-only skip, already visible via
		// staleSkipped.IDOnly at Info (RecordVectorProjection's own
		// skippedIDOnly precedence). Folding it into `cleared` made the
		// T5 skip population (~22% of a live corpus) masquerade as a mass
		// stale-vector event on every batch that touched one.
		a.recordVectorProjection(ctx, batch.OrgID, 0, len(stale), staleSkipped)
		return nil
	}
	identity := a.embedder.Identity()
	targets, idOnlyTargets, skipped := collectEmbedTargets(batch, a.embedBudgetRunes(), a.config.IncludeEmbedBodies)
	if len(targets) == 0 {
		// Codex round-4 F2: a relationship-only or tombstone-only batch is
		// valid and produces no embedding targets. It must still report, so
		// the ABSENCE of a RecordVectorProjection signal means "no batch ran"
		// and never "a batch ran and had nothing to embed". An observability
		// gap that looks identical to inactivity is the same class of defect
		// as round-3 F2.
		//
		// Finding 1: even with no EMBED target this batch, an id-only-skipped
		// row may carry a vector a PRIOR batch wrote before it went id-only.
		// That must still be cleared here -- this branch commits (advances
		// the watermark) just like the others.
		if err := a.clearNodeVectors(ctx, key, batch.OrgID, idOnlyTargets); err != nil {
			return err
		}
		// Round-2 finding 1: NOT len(idOnlyTargets) -- nothing here is a
		// genuine stale/error clear (there was nothing to embed at all), so
		// `cleared` stays 0. The id-only clear attempt is already fully
		// accounted for via skipped.IDOnly (Info).
		a.recordVectorProjection(ctx, batch.OrgID, 0, 0, skipped)
		return nil
	}
	// CHAOS-3836 seam: the document-side task prefix is applied to the text
	// HANDED TO Embed, never to target.text -- target.text is the composed
	// search_text both retrieval arms share byte-identically (spec §0), and
	// clearNodeVectors/writeNodeVector keep addressing targets by kind/id.
	// No pre-truncation here: ApplyDocumentPrefix budgets the prefix into
	// MaxTextRunes itself, and collectEmbedTargets' own caps are a
	// composition property, not a transmission one.
	texts := make([]string, 0, len(targets))
	for _, target := range targets {
		texts = append(texts, a.documentPrefixed(target.text))
	}
	vectors, err := a.embedWithBoundedRetry(ctx, texts)
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
		// CHAOS-4259 codex R1 finding 3: report the embed failure itself
		// (increment the org's consecutive-failure streak, escalate if the
		// threshold is crossed) BEFORE attempting the clear, not after it
		// succeeds. The embed call already failed regardless of whether the
		// clear below also fails -- a genuine, sustained embed outage that
		// happens to ALSO hit clear failures every time must still reach the
		// escalation threshold, not silently never count because clearErr
		// short-circuits with `return err` first.
		a.reportEmbedFailure(ctx, batch.OrgID, err)
		// Finding 1: the id-only-skipped set is invalidated the same as
		// everything else in this batch -- clear it alongside targets rather
		// than leaving whatever those rows carried from a prior batch.
		toClear := append(append([]embedTarget{}, targets...), idOnlyTargets...)
		if err := a.clearNodeVectors(ctx, key, batch.OrgID, toClear); err != nil {
			return err
		}
		// Round-2 finding 1: `cleared` is len(targets) -- the genuine
		// embed-failure clear -- not len(toClear). idOnlyTargets' clear is
		// the same routine, already-reported-via-skipped.IDOnly event as
		// every other commit path in this function.
		a.recordVectorProjection(ctx, batch.OrgID, 0, len(targets), skipped)
		return nil
	}
	// The Embed call itself succeeded (possibly after a bounded retry): the
	// embed mechanism is healthy, so this organization's consecutive-failure
	// streak resets here, before the per-target write loop below -- a
	// FalkorDB write failure in that loop is a different subsystem and must
	// not carry this organization's embed-health streak forward.
	a.resetEmbedFailureStreak(batch.OrgID)
	for index, target := range targets {
		if err := a.writeNodeVector(ctx, key, batch.OrgID, target, vectors[index], identity); err != nil {
			// Same reasoning, mid-batch: targets before this one carry fresh
			// vectors and are fine; this one and everything after it still
			// carry yesterday's, so clear exactly those -- plus the
			// id-only-skipped set, for the same finding-1 reason as every
			// other commit path in this function.
			a.recordVectorDegraded(ctx, batch.OrgID)
			remaining := targets[index:]
			toClear := append(append([]embedTarget{}, remaining...), idOnlyTargets...)
			if clearErr := a.clearNodeVectors(ctx, key, batch.OrgID, toClear); clearErr != nil {
				return clearErr
			}
			// Round-2 finding 1: `cleared` is len(remaining) -- the genuine
			// write-failure clear -- not len(toClear); see the finding-1
			// comment above.
			a.recordVectorProjection(ctx, batch.OrgID, index, len(remaining), skipped)
			return nil
		}
	}
	// Finding 1: a fully successful batch still must not let an id-only-
	// skipped row keep a vector a PRIOR batch wrote before it went id-only --
	// the success path was the ORIGINAL gap: every failure/degrade branch
	// above already clears something, but a clean run previously never
	// touched idOnlyTargets at all.
	if err := a.clearNodeVectors(ctx, key, batch.OrgID, idOnlyTargets); err != nil {
		return err
	}
	// Round-2 finding 1: `cleared` stays 0 -- a fully successful batch has
	// no genuine stale/error clear; the id-only clear above is the same
	// routine event skipped.IDOnly already reports at Info. Reporting it
	// again here as `cleared` would trigger RecordVectorProjection's Warn
	// path on the routine, deterministic consequence of every id-only skip
	// -- exactly the T5-population-reads-as-mass-vector-loss defect this
	// finding closes.
	a.recordVectorProjection(ctx, batch.OrgID, len(targets), 0, skipped)
	return nil
}

// embedWithBoundedRetry issues one Embed call and, if it fails, retries up to
// Config.EmbedFailureMaxRetries times -- waiting Config.EmbedFailureRetryBackoff
// between attempts -- but ONLY while the failure classifies TRANSIENT
// (embedprovider.IsTransientEmbedError). A PERSISTENT failure returns
// immediately on the attempt that produced it, with zero retries spent: an
// identical request gets an identical answer from an auth/shape/identity
// problem, so retrying only delays every projection tick during an outage of
// that kind without changing the outcome (CHAOS-4147 item 3 / CHAOS-4259).
// The zero value of EmbedFailureMaxRetries (a Config built directly, e.g. by
// most existing tests) makes this a single call with no retry, byte-identical
// to pre-CHAOS-4259 behavior.
//
// Returns the LAST error observed if every attempt fails (or the first,
// persistent one) -- exactly the error embedProjectionBatch's existing
// clear-and-degrade branch already handles unchanged.
//
// The ctx handed to every Embed call here is marked with
// embedprovider.WithBatchCall (codex R1 finding 1): embedChunk must apply
// Config.BatchTimeout to this call regardless of how many texts it happens
// to carry -- a projection batch with exactly ONE embeddable target is a
// legitimate, common shape (not a read-path call), and inferring the
// timeout from len(texts) alone silently bounded that single-target write
// by the much shorter read-side Timeout instead.
func (a *Adapter) embedWithBoundedRetry(ctx context.Context, texts []string) ([][]float32, error) {
	ctx = embedprovider.WithBatchCall(ctx)
	vectors, err := a.embedder.Embed(ctx, texts)
	if err == nil {
		return vectors, nil
	}
	for attempt := 0; attempt < a.config.EmbedFailureMaxRetries; attempt++ {
		if !embedprovider.IsTransientEmbedError(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(a.config.EmbedFailureRetryBackoff):
		}
		vectors, err = a.embedder.Embed(ctx, texts)
		if err == nil {
			return vectors, nil
		}
	}
	return nil, err
}

// reportEmbedFailure increments this organization's consecutive embed-batch
// failure streak and, once it reaches Config.EmbedFailureEscalateAfter,
// fires the louder RecordVectorProjectionEmbedFailuresEscalated signal --
// on every failing batch from the threshold onward, not just once at the
// crossing, so the signal stays live for the duration of a sustained outage
// (CHAOS-4147 item 3 / CHAOS-4259). A zero/negative EmbedFailureEscalateAfter
// (the Config zero value) disables escalation: the streak is still tracked
// (harmless), but the threshold check never passes.
//
// KNOWN, ACCEPTED LIMITATION (codex R1 finding 4): the streak value passed
// to telemetry is read under embedFailureMu, so it is always the exact
// count AT THE MOMENT this failure incremented it -- never a stale re-read.
// What is NOT guaranteed is the ORDER telemetry calls are OBSERVED in if
// two batches for the SAME organization race concurrently on this Adapter
// (one failing past the threshold, another concurrently succeeding and
// resetting via resetEmbedFailureStreak): the mutex only serializes the map
// mutation, not the RecordVectorProjectionEmbedFailuresEscalated call that
// follows it outside the lock, so a reset's effect could be observed by an
// operator before an already-in-flight escalation for a higher count is.
// Holding embedFailureMu across the telemetry call would fix the ordering
// but means blocking every OTHER organization's embed-failure reporting on
// this shared Adapter for however long a caller-supplied GraphTelemetry
// implementation takes -- Telemetry is a pluggable interface this package
// does not control, so that trade is worse than the cosmetic reordering it
// would close. Not a concern in production: projectionrun.Coordinator
// single-flights each organization, so two batches for the same org never
// run concurrently on a real deployment; this only matters for a caller
// that constructs its own concurrent Adapter usage directly.
func (a *Adapter) reportEmbedFailure(ctx context.Context, orgID string, err error) {
	a.embedFailureMu.Lock()
	if a.consecutiveEmbedBatchFailures == nil {
		a.consecutiveEmbedBatchFailures = make(map[string]int)
	}
	a.consecutiveEmbedBatchFailures[orgID]++
	streak := a.consecutiveEmbedBatchFailures[orgID]
	a.embedFailureMu.Unlock()

	if a.config.EmbedFailureEscalateAfter > 0 && streak >= a.config.EmbedFailureEscalateAfter {
		a.recordVectorProjectionEmbedFailuresEscalated(ctx, orgID, streak, embedprovider.IsTransientEmbedError(err))
	}
}

// resetEmbedFailureStreak clears this organization's consecutive
// embed-batch failure streak after a batch whose Embed call succeeded --
// see embedProjectionBatch's call site for why this fires on Embed success
// alone, independent of the per-target write loop that follows it.
func (a *Adapter) resetEmbedFailureStreak(orgID string) {
	a.embedFailureMu.Lock()
	delete(a.consecutiveEmbedBatchFailures, orgID)
	a.embedFailureMu.Unlock()
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
		// CHAOS-3833: the stamp is identity#compositionTag -- the ONE
		// string the read fence compares (verifyStoredEmbedderIdentity),
		// suffixed rather than stored as a second property, because a
		// second property is a second comparison that can drift from the
		// first. Text-lineage changes (template, rune cap, body gate,
		// prefix selector) move the tag; the fence then fails closed to
		// lexical until the prescribed rebuild. The clear paths
		// (clearNodeVectors) stay identity-AGNOSTIC by design and are
		// untouched.
		"vec": vectorParam(vector), "identity": a.stampedEmbedderIdentity(identity), "dimension": int64(identity.Dimension),
	}
	_, err := a.api.query(ctx, key, cypher, params, false)
	if err != nil {
		return safeDependencyError("write node embedding", err)
	}
	return nil
}

// stampedEmbedderIdentity is the ONE string both identity-comparing sites
// use -- writeNodeVector's stamp and verifyStoredEmbedderIdentity's
// expectation -- "<provider>/<model>#<composition tag>". The tag is computed
// from this adapter's own effective semantic configuration (rune cap and
// prefix component captured at construction, body gate from Config), the
// same authority EmbedRetrievalIdentityFromEnv derives the persisted
// answer-reuse dimension from.
func (a *Adapter) stampedEmbedderIdentity(identity contextfabric.EmbedderIdentity) string {
	return identity.String() + "#" + EmbedCompositionTag(a.embedBudgetRunes(), a.config.IncludeEmbedBodies, a.embedPrefixTagComponent())
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
//
// codex round-8 P2: RetrievalPolicy.EfRuntime (a.efRuntime) is applied ONLY
// in the vectorIndexAbsent branch below, at CREATE time -- never here for an
// index that already exists. That is not an oversight this function should
// close on its own: the CHAOS-3835 t3 composition rebuild is a MANDATORY,
// runbook'd, already-merged operational step that drops and recreates every
// organization's vector index as a side effect, and CHAOS-3834/CHAOS-3835
// deploy together sharing that ONE rebuild -- an org that runs it picks up
// BOTH the new composition text AND the calibrated efRuntime in the same
// operation. See docs/operations.md's CHAOS-3835 rebuild section (extended
// by this round) for the operator-facing statement that an un-rebuilt org
// keeps server-default ANN behavior while still stamping RetrievalPolicyVersion
// rp2. This function deliberately does NOT compare-and-recreate on its own:
// disproportionate machinery given the mandatory rebuild already in the
// deploy path -- see the vectorIndexKnown branch below for the DETECTION
// (not correction) this round adds instead.
func (a *Adapter) ensureVectorIndex(ctx context.Context, key string) error {
	if a.embedder == nil {
		return nil
	}
	want := a.embedder.Identity().Dimension
	existing, state, err := a.vectorIndexDimension(ctx, key)
	if err != nil {
		return err
	}
	switch state {
	case vectorIndexAbsent:
		// CHAOS-3834: a.efRuntime is the calibrated per-identity HNSW value
		// (RetrievalPolicy), applied only here -- at CREATE time -- because
		// the pinned FalkorDB module has no per-query efRuntime (see
		// RetrievalPolicy's doc comment). Zero (no calibrated policy for
		// this identity) omits the clause exactly as createVectorIndex
		// always has, so an uncalibrated identity's first bootstrap is
		// byte-for-byte unchanged.
		if err := a.createVectorIndexWithOptions(ctx, key, want, hnswIndexOptions{EfRuntime: a.efRuntime}); err != nil {
			return err
		}
		// Index creation is asynchronous, so wait for it exactly as bootstrap
		// already waits for constraints.
		return a.pollVectorIndexOperational(ctx, key)
	case vectorIndexUnknown:
		// Codex round-4 F1: a PRE-EXISTING index that is still building, or
		// reports a status this adapter does not recognize, must be polled to
		// readiness on the SAME terms as one this bootstrap just created.
		//
		// The earlier form polled only after CREATING an absent index and
		// returned success for any pre-existing one. That produced a silent
		// LIVELOCK, which is worse than the failure it replaced: bootstrap
		// cached success, and the round-3 F1 containment then correctly failed
		// every subsequent batch with errVectorIndexNotReady -- holding the
		// organization's checkpoint indefinitely while the LOUD, bounded
		// timeout built for exactly this condition was never reached. A
		// deliberate stall that nothing announces is indistinguishable from an
		// idle organization.
		//
		// Polling here means a never-settling pre-existing index fails
		// bootstrap loudly, within RequestTimeout, instead of stalling
		// forever. bootstrapDone is only set on success, so the next tick
		// retries.
		return a.pollVectorIndexOperational(ctx, key)
	default:
		// vectorIndexKnown. A DIMENSION MISMATCH deliberately does not block
		// bootstrap: it is the persistent, operator-fixable state whose
		// batches clear-and-commit rather than replay (see
		// embedProjectionBatch), so blocking here would reintroduce the very
		// stall that exception exists to avoid. Usability is decided fresh at
		// each read and each write by vectorIndexUsable.
		_ = existing
		// codex round-8 P2: a best-effort DETECTION-only check (see this
		// function's doc comment for why no compare-and-recreate). FalkorDB's
		// db.indexes() introspection ALREADY exposes the built efRuntime --
		// verified live (indexStatus.HNSWOptions(), conn.go, the same read
		// recreateVectorIndexWithOptions uses to capture "whatever was there
		// before" a possible restore) -- so a policy/actual mismatch on an
		// operational index is at least loud at bootstrap time, once per key
		// (this whole branch runs only inside bootstrapSchema's
		// bootstrapDone-guarded, once-per-process-per-key path), rather than
		// silently invisible until someone thinks to check. a.efRuntime==0
		// means no calibrated policy at all for this identity -- nothing to
		// compare against, so skip. A read failure here is diagnostic-only
		// and must not fail bootstrap over it.
		//
		// codex round-9 P2 wiring fix: reported through
		// recordVectorIndexEfRuntimeMismatch (telemetry), NOT a bare
		// slog.Default() call -- the earlier direct call bypassed whatever
		// sink/level an operator configured via Config.Telemetry, same class
		// as CHAOS-3835's telemetry fix elsewhere in this package. Nil-safe:
		// an operator who declined telemetry sees nothing, rather than this
		// falling back to an unconfigured global default.
		if a.efRuntime != 0 {
			if current, ok, hnswErr := a.currentVectorIndexHNSWOptions(ctx, key); hnswErr == nil && ok && current.EfRuntime != a.efRuntime {
				a.recordVectorIndexEfRuntimeMismatch(ctx, key, a.efRuntime, current.EfRuntime)
			}
		}
		return nil
	}
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
	// CHAOS-3833: the expectation carries the composition tag, so a vector
	// embedded under different TEXT semantics fails this fence exactly as
	// a vector from a different model does -- identity matches were never
	// enough once text lineage could move independently of the model.
	identity := a.stampedEmbedderIdentity(a.embedder.Identity())
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
//
// This is the plain-bool wrapper every pre-CHAOS-3890 caller (including
// every existing test) already uses -- byte-identical behavior, unchanged
// signature. See vectorFenceCheck for the same verdict WITH the reason
// the fence discarded before this ticket; resolutionFence.readable is the
// one production caller that reads it.
func (a *Adapter) ensureVectorReadable(ctx context.Context, key, orgID string) bool {
	enabled, _ := a.vectorFenceCheck(ctx, key, orgID)
	return enabled
}

// vectorFenceCheck is ensureVectorReadable's own body (CHAOS-3890),
// additionally reporting WHICH of the fence's checks produced a
// non-passing verdict -- see VectorFenceResult's own doc comment for the
// closed vocabulary and config.go's GraphTelemetry.RecordVectorFence for
// where the reason is reported. Pure refactor of ensureVectorReadable's
// pre-existing logic: no check added, removed, or reordered, so every
// existing ensureVectorReadable test still exercises the identical
// decision path through this function.
//
// The a.embedder==nil branch is unreachable from resolutionFence.readable
// in production -- both of its call sites (hybridSearchNodes,
// questionVectorSearchNodes) already check a.embedder==nil themselves and
// return before ever calling fence.readable -- kept here only because
// ensureVectorReadable's own pre-existing defensive check must stay
// intact for its other (non-fence.readable) callers. VectorFenceIndexAbsent
// is the closest-fit reason for it: "no embedder configured" and "no
// vector index built" both mean the same thing to a caller deciding
// whether to trust this bool -- there is no vector capability to check at
// all -- and the ticket's reason vocabulary has no dedicated value for it.
func (a *Adapter) vectorFenceCheck(ctx context.Context, key, orgID string) (bool, VectorFenceResult) {
	if a.embedder == nil {
		return false, VectorFenceIndexAbsent
	}
	dimension, state, err := a.vectorIndexDimension(ctx, key)
	if err != nil {
		return false, VectorFenceQueryError
	}
	switch state {
	case vectorIndexAbsent:
		return false, VectorFenceIndexAbsent
	case vectorIndexUnknown:
		return false, VectorFenceIndexUnknown
	}
	if dimension != a.embedder.Identity().Dimension {
		return false, VectorFenceDimMismatch
	}
	matches, err := a.verifyStoredEmbedderIdentity(ctx, key, orgID)
	if err != nil {
		return false, VectorFenceQueryError
	}
	if !matches {
		return false, VectorFenceIdentityMismatch
	}
	return true, VectorFenceOK
}
