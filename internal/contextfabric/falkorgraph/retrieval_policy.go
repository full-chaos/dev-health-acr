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

// calibratedIdentityText2Large is the CHAOS-3834 measured entry's key.
//
// codex round-1 P2 REVERSED the earlier CHAOS-3835-contact fix, which had
// derived this key from EmbedCompositionTag so it would auto-follow a future
// composition-tag bump. That auto-following was itself the bug: the
// calibration below (tau=0.30, efRuntime=200) was measured against t2's
// composed text specifically -- the S+/S- distributions, floor_loss, and
// near-duplicate density all describe WHAT t2 PRODUCES. A t3 composition
// (CHAOS-3835 changes what text gets embedded) is a DIFFERENT corpus by this
// table's own scoping rule (see retrievalPolicyTable's doc comment below: "a
// rune-cap or body-gate flip ... is semantically a different corpus, and
// rightly falls back to the conservative default until calibrated in its own
// right"). Auto-rekeying this entry onto t3 would silently apply t2-measured
// numbers to un-measured t3 text -- trading the original silent-miss hazard
// for an equally silent, never-validated auto-inherit.
//
// So the key is PINNED to the literal composition it was measured against.
// A future composition-tag bump (t2 -> t3) does NOT move this entry with it;
// instead it makes TestCalibratedEntryDriftsLoudlyWithCompositionTag fail
// LOUDLY at integration, forcing an explicit human decision -- recalibrate
// against the new composition, or record an explicit inheritance decision as
// a new pinned entry -- rather than silently missing (the original
// contact-check concern) or silently auto-inheriting (this reversal's
// concern). Whether CHAOS-3834's t2 entry should be recalibrated or
// explicitly inherited for t3 is a decision CHAOS-3835's integration makes,
// not something this table decides on its own.
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
const calibratedIdentityText2Large = "openai/text-embedding-3-large#t2:r2000:b0:pnone#d3072"

// retrievalPolicyTable is keyed by EmbedRetrievalIdentityFromEnv's persisted
// string (identity.String() + "#" + EmbedCompositionTag(...), byte-identical
// to what migration 0014's embed_retrieval_identity column persists) PLUS a
// "#d<dimension>" suffix (codex round-3 P1 -- see calibratedIdentityText2Large's
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
	// top-20, 30 scored cases): hit=5, floor_loss=21 -- tau=0.55 (the
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
	calibratedIdentityText2Large: {
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

// EnvCalibrated gates whether retrievalPolicyTable is consulted AT ALL
// (codex round-8 P1): the shipped calibratedIdentityText2Large entry's own
// SEQUENCING GATE comment above explains that this identity's measured
// hard-negative reject rate is FAR below NegativeGateRejectThreshold
// (CalibrateFromReport itself reports ApplyReady=false for it) -- tau=0.30
// is a RECALL channel whose precision depends on the no-match/false-friend
// controls (hybrid ranking + corroboration) passing FIRST. Before this gate,
// LookupRetrievalPolicy applied the entry to ANY deployment matching the
// identity/dimension unconditionally, the moment it built with this table
// entry present -- silently reaching every such deployment regardless of
// whether chris's ratified sequencing decision had actually been acted on
// for it. The entry's constants (tau=0.30, efRuntime=200) remain
// chris-ratified for the T4 measurement program; this flag does not
// second-guess them or make them conditional on anything ABOUT the
// measurement. It makes the SEPARATE, previously-implicit "has this
// deployment's sequencing gate been satisfied" decision an explicit,
// mechanical opt-in instead of "does the identity string happen to match" --
// the dev/trial stack setting this flag on IS that explicit human decision,
// now recorded in config rather than inferred from a table entry's mere
// presence. Unset (the default): every deployment keeps today's
// conservative, env-configured defaults, exactly as if no calibrated entry
// existed at all.
const EnvCalibrated = "ACR_CONTEXT_FABRIC_RETRIEVAL_POLICY_CALIBRATED"

// LookupRetrievalPolicy returns the calibrated RetrievalPolicy for
// embedIdentity (the identity.String()+"#"+compositionTag form -- see
// retrievalPolicyTable's doc comment) AT dimension, and false when no
// calibrated entry exists for that exact (identity, dimension) pair, OR
// (codex round-8 P1) when EnvCalibrated is not explicitly set -- see its doc
// comment above. A false result means "keep the current conservative
// defaults": callers must not zero out whatever they already had.
//
// dimension is a SEPARATE parameter, not folded into embedIdentity by the
// caller, so this function is the single place the "#d<dimension>" suffix
// format is composed (codex round-3 P1) -- the same "single authority"
// posture EmbedCompositionTag already holds for the composition-tag
// component, now extended to the dimension component too. lookup is threaded
// through for the same reason (codex round-8 P1): this stays the single
// place BOTH the identity/dimension match AND the opt-in gate are decided,
// rather than splitting the gate check out to each call site.
func LookupRetrievalPolicy(lookup func(string) (string, bool), embedIdentity string, dimension int) (RetrievalPolicy, bool) {
	if !envBool(lookup, EnvCalibrated, false) {
		return RetrievalPolicy{}, false
	}
	policy, ok := retrievalPolicyTable[fmt.Sprintf("%s#d%d", embedIdentity, dimension)]
	return policy, ok
}
