package falkorgraph

import "fmt"

// RetrievalPolicy is the CHAOS-3834 per-embedder-identity retrieval-time
// configuration (embed-text spec v2 §5 L2/L3/L4, §6 T4): the similarity
// floor tau, the ANN over-fetch multiplier K, and the HNSW efRuntime this
// identity has been CALIBRATED to use, replacing the single global
// embedprovider.DefaultSimilarityFloor-derived defaults for identities that
// have a measured entry in retrievalPolicyTable.
//
// A zero field means "not calibrated for this dimension": the caller keeps
// whatever it already had (the env-configured SimilarityFloor, multiplier 1,
// the server's own efRuntime default) rather than this policy overriding it
// with a literal zero -- a floor of 0 would disable the AC-3778-4 no-match
// guard entirely, a multiplier of 0 is already vectorSearchNodesWithOverFetch's
// own "treat as 1" sentinel, and an efRuntime of 0 is createVectorIndexWithOptions'
// own "omit the clause, let the server default apply" sentinel. Every zero
// value in this type therefore composes correctly with the code it feeds
// without a separate "was this set" flag.
//
// EfRuntime IS NOT A PER-QUERY PARAMETER. CHAOS-3832 verified live (§7 D3,
// graph module 42002) that the pinned FalkorDB module has no per-query
// efRuntime knob at all -- efRuntime is read ONLY from the vector index's
// CREATE OPTIONS clause (createVectorIndexWithOptions), fixed for the life
// of that index. Consequences for this field specifically:
//
//   - ensureVectorIndex applies a policy's EfRuntime ONLY when it creates a
//     brand-new index (an organization bootstrapping for the first time, or
//     one that just ran `acr-projector rebuild --org`, which drops and
//     recreates the index as a side effect). It is never consulted again
//     once an index exists.
//   - An EXISTING organization's index does NOT pick up a changed EfRuntime
//     automatically. Changing this field for an identity that organizations
//     are already running under is an OPERATIONAL action, not a code
//     deploy: an operator must explicitly rebuild the affected indexes
//     (the CHAOS-3832 sweep tooling's recreateVectorIndexWithOptions, or an
//     `acr-projector rebuild --org` if a full re-embed is also warranted)
//     to make the new value take effect. A policy-table edit alone changes
//     what NEW indexes are built with; it does not reach into a running
//     FalkorDB and rewrite one.
//   - Because of that asymmetry, EfRuntime deliberately does NOT join
//     RetrievalPolicyVersion's "vectors remain valid, only stored answers
//     need invalidating" story on quite the same terms as SimilarityFloor/K:
//     bumping RetrievalPolicyVersion when EfRuntime changes still correctly
//     invalidates stored ANSWERS (they may have been ranked under
//     lower-recall retrieval), but it does NOT by itself change what any
//     already-built index actually searches with. The version bump and the
//     operational rebuild are two separate, both-required steps -- exactly
//     the "coupling rule" §4 states for multi-layer changes.
type RetrievalPolicy struct {
	// SimilarityFloor is tau. See embedprovider.DefaultSimilarityFloor for
	// the AC-3778-4 no-match guard this gates, and CHAOS-3834's measurement
	// basis (embed-text spec §5 L4 / §6 T4) for why a single global tau
	// could not be calibrated as a precision cliff and instead becomes a
	// low RECALL GATE per identity.
	SimilarityFloor float64
	// OverFetchMultiplier is K, in spec §5 L3's `(multiplier*limit)+1`
	// formula (vectorSearchNodesWithOverFetch). Sizes the pool the tau/org
	// post-filters draw from, so a low recall-gate tau (which lets more
	// candidates survive per query) has enough headroom to still return a
	// full top-K after filtering.
	OverFetchMultiplier int
	// EfRuntime is the HNSW runtime search-breadth parameter this identity's
	// vector index should be BUILT with. See the type doc comment above --
	// this is an index-build-time value, not a per-query one.
	EfRuntime int
}

// calibratedIdentityText3Large is the CHAOS-3834 measured entry's key.
//
// codex round-1 P2 REVERSED the earlier CHAOS-3835-contact fix, which had
// derived this key from EmbedCompositionTag so it would auto-follow a future
// composition-tag bump. That auto-following was itself the bug: the
// calibration below (tau=0.30, efRuntime=200) was measured against t2's
// composed text specifically -- the S+/S- distributions, floor_loss, and
// near-duplicate density all describe WHAT t2 PRODUCES. So the key was
// PINNED to the literal composition it was measured against, deliberately
// NOT auto-following a future composition-tag bump: instead,
// TestCalibratedEntryDriftsLoudlyWithCompositionTag was designed to fail
// LOUDLY the day the live tag drifted, forcing an explicit human decision --
// recalibrate against the new composition, or record an explicit inheritance
// decision as a new pinned entry -- rather than silently missing or silently
// auto-inheriting.
//
// T3 INHERITANCE (codex round-9 P1, CHAOS-3835 integration -- the explicit
// decision the round-1 P2 doc comment above deferred to this integration):
// this entry is now keyed to t3, NOT t2. CHAOS-3835's t2 -> t3 composition
// change is narrower than a typical template-version bump: T5's id-only
// skip decision (isPureIdentifierSubject) does not alter what text gets
// COMPOSED for any subject that still gets embedded -- it only removes
// whole subjects (ci_pipeline_run rows whose pipeline_name/branch/aliases
// carry no content beyond a bare identifier) from the embedded population
// entirely. Every subject t2 measured a genuine S+/S- signal against text
// t3 still composes IDENTICALLY -- the t2-measured tau=0.30/efRuntime=200
// calibration therefore describes the t3 corpus too, MINUS a population of
// pure-noise vectors this measurement never depended on (an S+/S- pair
// built from a bare identifier's own embedding was never a source of the
// signal tau=0.30 was calibrated against). This is an EXPLICIT INHERITANCE
// decision, not a re-measurement: validation is the POST-REBUILD oracle
// re-measure (run the CHAOS-3831 harness again against a t3-rebuilt
// organization once CHAOS-3835's rebuild has run; a materially different
// result there is the trigger to revisit this inheritance, not a reason to
// have withheld it now). Decision recorded on CHAOS-3834.
//
// The OLD t2-keyed entry is DROPPED entirely, not kept alongside this one:
// a t3-constant binary (embedTextTemplateVersion, composition.go) can never
// produce a t2-tagged EmbedCompositionTag again, so a t2 key is permanently
// unreachable from any live deployment -- keeping it would only grow the
// table with a key nothing can ever look up. See
// TestLookupRetrievalPolicy_UnknownIdentityKeepsConservativeDefault's
// explicit t2-now-misses case for the pinning test on this.
//
// The trailing "#d3072" is codex round-3 P1: EmbedderIdentity.String()
// deliberately EXCLUDES Dimension (see that method's doc comment -- the
// node-identity stamp/read-fence checks dimension separately and
// numerically), and EmbedRetrievalIdentityFromEnv's persisted answer-reuse
// identity inherits that same exclusion. But a calibrated tau/efRuntime
// entry has NO separate numeric dimension check the way the read fence
// does -- without this suffix, a BYO endpoint serving "the same"
// provider/model at a DIFFERENT width (e.g. OpenAI's `dimensions` param
// truncating text-embedding-3-large to 1536, or a serving lookalike) would
// silently inherit tau=0.30/efRuntime=200, numbers measured specifically
// against 3072-wide vectors' cosine-similarity distribution. Same
// exact-measurement invariant as the composition-tag pin above, applied to
// the identity side: this entry is scoped to dimension 3072 exactly, and an
// unmatched width falls back to the conservative, uncalibrated defaults
// like any other uncalibrated identity.
const calibratedIdentityText3Large = "openai/text-embedding-3-large#t3:r2000:b0:pnone#d3072"

// retrievalPolicyTable is keyed by EmbedRetrievalIdentityFromEnv's persisted
// string (identity.String() + "#" + EmbedCompositionTag(...), byte-identical
// to what migration 0014's embed_retrieval_identity column persists) PLUS a
// "#d<dimension>" suffix (codex round-3 P1 -- see calibratedIdentityText3Large's
// doc comment for why dimension, which EmbedderIdentity.String() deliberately
// excludes, must still be part of THIS key). Using the full composed string
// as the policy key (rather than, say, provider+model alone) means a policy
// is scoped to one exact composition AND width -- a rune-cap/body-gate flip
// or a dimension change is semantically a different corpus/measurement, and
// rightly falls back to the conservative default until calibrated in its
// own right. LookupRetrievalPolicy is the single authority that composes
// this suffix onto a live identity string; the table's own keys embed it as
// a literal, matching the composition-tag component's existing pin.
//
// An identity with NO entry here is UNCALIBRATED: LookupRetrievalPolicy
// reports found=false and every caller keeps today's conservative,
// env-configured behavior. Adding an entry -- or changing one already
// present -- is a retrieval-policy default change and must bump
// RetrievalPolicyVersion in the SAME changeset (see that constant's doc
// comment), so every previously stored answer for organizations running
// this identity stops being reused until freshly generated under the new
// policy.
var retrievalPolicyTable = map[string]RetrievalPolicy{
	// CHAOS-3834 measurement basis (2026-08-15, first full-universe oracle
	// baseline, identity openai/text-embedding-3-large#t2:r2000:b0:pnone,
	// top-20, 30 scored cases) -- MEASURED against t2 text; the entry below
	// is KEYED to t3, an explicit inheritance decision at CHAOS-3835
	// integration, not a re-measurement. See calibratedIdentityText3Large's
	// doc comment for the inheritance rationale and validation plan; the
	// numbers and reasoning below describe the ORIGINAL t2 measurement this
	// inheritance carries forward unchanged.
	//
	// hit=5, floor_loss=21 -- tau=0.55 (the
	// embedprovider.DefaultSimilarityFloor-derived value this identity was
	// running under) rejected the CORRECT subject in 70% of scored cases.
	// S+ (correct-pair) and S- (best-wrong-neighbor) distributions OVERLAP
	// at every candidate tau on this identity's measured text (repository
	// kind: medians 0.531/0.531 identical; work_item kind: S+ median 0.391
	// vs S- median 0.467, i.e. INVERTED) -- so tau cannot work as a
	// precision cliff here; CalibrateFromReport run against this
	// measurement's shape (see tau_calibration.go and
	// TestCalibrateFromReport_SyntheticOverlappingDistributions) recommends
	// a tau in the 0.30 band as a low RECALL GATE, leaving adjudication to
	// hybrid ranking + corroboration downstream (graphrank), not to the
	// floor.
	//
	// *** RATIFIED (CHAOS-3834) ***. tau=0.30 is the decided RECALL-channel
	// value: the v2 oracle baseline decomposition attributes 21/30 misses to
	// floor_loss at the prior tau=0.55, and S+/S- score distributions
	// overlap at every candidate tau on this identity's measured text -- so
	// tau cannot serve as a precision knob here; precision comes from
	// hybrid ranking + corroboration adjudication downstream (graphrank),
	// not the floor. efRuntime=200 is the decided recall knee from the
	// CHAOS-3832 sweep (recall@20 0.979 vs 0.853 at the server default 10,
	// same index-build cost). Evidence: oracle-report-v2-baseline; decision
	// recorded on CHAOS-3834. Values remain configurable as implemented --
	// changing them is still the one-line diff this table exists to make
	// possible, it is simply no longer pending confirmation.
	//
	// SEQUENCING GATE (codex round-2 P1): tau_calibration.go's
	// CalibrateFromReport reports this same identity's measured hard-negative
	// reject rate as FAR below NegativeGateRejectThreshold (ApplyReady=false)
	// -- tau=0.30 is a RECALL channel, not a precision knob, and admits most
	// impostors by design (adjudication is owned by hybrid ranking +
	// corroboration downstream, per the ratified conclusion above). This
	// entry is NOT the tool's auto-applied output and is therefore NOT gated
	// by ApplyReady: it is the explicit human ratification decision
	// ApplyReady=false is asking for, made by chris on CHAOS-3834, recorded
	// here by hand rather than mechanically emitted. The no-match/false-
	// friend controls this entry's precision actually depends on (hybrid
	// ranking + corroboration) must pass at THIS exact policy before any
	// index-recreate/trial conclusions are drawn -- see the CHAOS-3834
	// ship-time record for that sequencing.
	//
	// TOOL-VS-TABLE DOCTRINE SPLIT (codex round-8 P1, chris-ratified
	// resolution): ApplyReady's criteria (see its doc comment in
	// tau_calibration.go) measure TAU-LEVEL PRECISION -- the PRE-T4 doctrine
	// where tau itself is the no-match barrier a candidate must clear to be
	// trusted. ApplyReady=false here is EXPECTED, not a red flag this entry
	// silently overrides: under the RATIFIED T4 design, tau=0.30 is a RECALL
	// channel by construction (S+/S- overlap at every candidate tau on this
	// identity's measured text -- see the ratification note above -- so no
	// tau value could ever pass a tau-level precision gate on this text),
	// and precision is enforced DOWNSTREAM by hybrid ranking + corroboration
	// adjudication (graphrank), never by the floor. This entry auto-applies
	// unconditionally for its exact pinned identity (provider+model+t3-tag+
	// 3072-dim) -- no opt-in flag -- because the exact-identity pinning IS
	// the safety mechanism: retrievalPolicyTable's doc comment above
	// explains why any OTHER deployment shape (different provider, model,
	// composition tag, or dimension) falls to the conservative default by
	// construction, and an explicit ACR_CONTEXT_FABRIC_EMBED_SIMILARITY_FLOOR
	// still wins over this table entry per-knob (codex round-1 P1,
	// EmbedderFromEnv). The no-match/false-friend controls this entry's
	// actual precision depends on remain the sequencing gate recorded on
	// CHAOS-3834, tracked operationally, not encoded as a second flag here.
	calibratedIdentityText3Large: {
		// 0.30: inside the recall-gate band the measurement aggregates
		// support (tau=0.30 passed 24/30 correct and 29/30 best-wrong
		// neighbors in the cited baseline). Strictly a floor, not a
		// commit threshold -- graphrank's vectorRelevanceCeiling (0.70)
		// and the corroboration requirement for anything above it are
		// unchanged by this policy.
		SimilarityFloor: 0.30,
		// K unchanged (spec instruction for this initial entry): 0 keeps
		// vectorSearchNodesWithOverFetch's existing multiplier-1 behavior
		// byte-identical to pre-CHAOS-3834 over-fetch sizing. A lower tau
		// admits more candidates per query without needing a wider raw
		// fetch to still fill top-K; revisit only if post-deployment
		// truncation telemetry says otherwise.
		OverFetchMultiplier: 0,
		// 200: CHAOS-3832's measured efRuntime/efConstruction sweep knee
		// for this corpus size -- recall@20 rose 0.853 -> 0.979 at the
		// same index-build cost between efRuntime 10 (the server default)
		// and 200. See the type doc comment: this value governs only
		// NEWLY CREATED indexes until an operator runs the CHAOS-3832
		// recreate tooling against organizations already on this
		// identity.
		EfRuntime: 200,
	},
}

// LookupRetrievalPolicy returns the calibrated RetrievalPolicy for
// embedIdentity (the identity.String()+"#"+compositionTag form -- see
// retrievalPolicyTable's doc comment) AT dimension, and false when no
// calibrated entry exists for that exact (identity, dimension) pair. A
// false result means "keep the current conservative defaults": callers must
// not zero out whatever they already had.
//
// No opt-in flag gates this lookup (codex round-8 P1, REVISED -- chris
// overruled an initial env-flag ruling): the exact-identity pinning IS the
// safety mechanism. retrievalPolicyTable's doc comment explains why any
// deployment shape OTHER than the exact pinned provider+model+composition-
// tag+dimension falls to the conservative default by construction, so a
// second gate on top of that exact match would only ever matter for the
// ONE deployment this entry was measured against and ratified for -- see
// calibratedIdentityText3Large's doc comment for the tool-vs-table doctrine
// split (ApplyReady measures tau-level precision; this entry is a ratified
// recall-channel decision that is EXPECTED to fail that gate).
//
// dimension is a SEPARATE parameter, not folded into embedIdentity by the
// caller, so this function is the single place the "#d<dimension>" suffix
// format is composed (codex round-3 P1) -- the same "single authority"
// posture EmbedCompositionTag already holds for the composition-tag
// component, now extended to the dimension component too.
func LookupRetrievalPolicy(embedIdentity string, dimension int) (RetrievalPolicy, bool) {
	policy, ok := retrievalPolicyTable[fmt.Sprintf("%s#d%d", embedIdentity, dimension)]
	return policy, ok
}
