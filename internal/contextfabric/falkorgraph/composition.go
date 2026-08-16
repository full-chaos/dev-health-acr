package falkorgraph

import (
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
)

// RetrievalPolicyVersion is the CHAOS-3833 retrieval-policy discriminator
// (embed-text spec v2 §4 R3): a manually bumped constant naming the current
// vector RETRIEVAL policy -- the similarity floor's default, the k/over-fetch
// shape, and the HNSW parameters this adapter relies on. Bump it whenever any
// of those defaults change semantics.
//
// It deliberately does NOT join the node identity stamp: a policy change
// reinterprets EXISTING vectors, which remain valid, so no rebuild is forced
// -- but a stored ANSWER was derived under the old policy, so the value is
// persisted and equality-compared as its own answer-reuse dimension
// (migration 0014). Its own dimension rather than a suffix on the embed
// retrieval identity, so a reuse miss stays attributable to policy-vs-embed
// specifically.
//
// CHAOS-3834 (embed-text spec §5 L4 / §6 T4) bumped this rp1 -> rp2: the
// per-embedder-identity RetrievalPolicy table (retrieval_policy.go) went
// from empty (every identity ran the single global, env-configured tau) to
// carrying a calibrated entry for
// "openai/text-embedding-3-large#t2:r2000:b0:pnone" that changes its
// effective tau and EfRuntime default. Per the "T4 only bumps the constant
// when it changes tau/K/HNSW defaults" rule, any FUTURE edit to an existing
// table entry, or addition of a new one, bumps this constant again in the
// same changeset -- see retrieval_policy.go's package doc comment.
//
// CHAOS-3835 integration (codex round-9 P1's t3 inheritance commit) bumped
// this rp2 -> rp3: the calibrated entry's KEY moved from the t2-tagged
// identity to the t3-tagged one (calibratedIdentityText3Large,
// retrieval_policy.go), an explicit inheritance decision rather than a
// re-measurement -- see that constant's doc comment for the inheritance
// rationale. The rp2 -> rp3 bump is what CHAOS-3834's table-entry-change
// rule requires here even though the SEMANTIC values (tau=0.30,
// efRuntime=200) are unchanged: a t3-tagged deployment's effective policy
// genuinely changes from this changeset (miss -> hit), which is exactly
// the "vectors remain valid, only stored answers need invalidating"
// scenario this constant exists to signal.
//
// CHAOS-3838 (spec L11/L13, §6 T8) bumped this rp3 -> rp4: read-side query
// expansion (union vector search over the question in addition to each
// term; domain-lexicon widening of the fulltext OR-query and, per
// applyLexiconToVectorArm, the embedded query text) changes WHAT a repeated,
// byte-identical question retrieves, exactly the "tau/K/HNSW reinterpret
// existing vectors" scenario this constant exists for -- no vector or node
// stamp moves (this is query-time only, nothing about what gets embedded
// and stored at projection time changes), but a stored answer computed
// under weaker retrieval (fewer/no lexical proposals on a paraphrase-heavy
// question) must not silently shadow a newly-achievable, better-corroborated
// commit for the identical question text.
//
// CHAOS-3829 (chris-ratified 2026-08-16) bumped this rp4 -> rp5: the
// calibrated identity's RetrievalPolicy entry (calibratedIdentityText3Large,
// retrieval_policy.go) gained VectorMarginCommitThreshold, activating
// graphrank.ResolveFromMergedCandidates' commit-path carve-out for the
// first time. A question that PREVIOUSLY resolved ambiguous (no commit,
// possibly a clarification prompt) can now COMMIT for the identical text --
// the exact "a stored answer computed under the old policy no longer
// matches what the current policy would produce" scenario this constant
// exists to signal, same class as the rp3 -> rp4 bump immediately above. No
// vector or node stamp moves; this is a read-side commit-decision change
// only.
const RetrievalPolicyVersion = "rp5"

// EmbedRetrievalIdentityNone is the persisted embed-retrieval-identity value
// for a deployment with NO embedder configured. A literal, never the empty
// string: migration 0014's CHECK forbids empty, and pginvestigation maps an
// empty value to SQL NULL ("this row never participates"), which would make
// a lexical-only deployment's answers never reusable -- the wrong outcome,
// since they have no vector lineage to go stale.
const EmbedRetrievalIdentityNone = "none"

// embedTextTemplateVersion names the per-kind embed-text COMPOSITION the
// write path currently produces (spec §4 Layer B). "t3" is CHAOS-3835's
// (T5) id-only skip decision (isPureIdentifierSubject, id_only.go): which
// ROWS get embedded is exactly as much a composition fact as which FIELDS
// do, per embedKindSkipped's own doc comment, so it rides the same
// discriminator. "t2" was CHAOS-3833's per-kind template set
// (search_text.go), including the organization embed skip-list; "t1" was
// the pre-CHAOS-3833 composition (entitySearchText = Label + Aliases +
// PreviousNames for every kind; content = title + body; episode =
// goal/outcome/summary). Any change to what any kind's composed text
// contains, or to which rows get embedded at all -- template edit, cap
// move, skip-list membership, id-only detector rule, prefix implementation
// -- bumps this constant, which moves the composition tag, which fails the
// read fence closed and moves the answer-reuse key, both through the one
// string below.
const embedTextTemplateVersion = "t3"

// EmbedPrefixTagComponentNone is the prefix component of the composition tag
// for a deployment with no prefix family configured -- the value
// embedprovider's PrefixTagComponent derives for PrefixFamilyNone. It exists
// here so a zero-valued EmbedderOptions (no captured component) and an
// explicitly-configured "none" family compose the SAME tag: the default must
// be byte-identical to the pre-CHAOS-3836 stamp, or every unconfigured
// deployment's read fence would fail closed on upgrade over a prefix pair
// that was never applied.
const EmbedPrefixTagComponentNone = "p" + string(embedprovider.PrefixFamilyNone)

// EmbedCompositionTag is the canonical composition-tag literal (spec §4
// Layer C): template version, embed rune cap, body gate, prefix component --
// e.g. "t3:r2000:b0:pnone". A LITERAL, not a hash, so an operator can read a
// stamp (or a persisted reuse row) and know exactly what produced it.
//
// prefixTagComponent is embedprovider's PrefixTagComponent literal, leading
// "p" included ("pnone", "pnomic"); the empty string means "no component was
// captured" and normalizes to EmbedPrefixTagComponentNone, per that
// constant's doc comment. The component is derived by embedprovider (the
// prefix authority) rather than re-derived here, so a new family added there
// cannot disagree with the tag stamped here.
//
// Every component is semantic: a different value on any of them means the
// same source row composes (or truncates, or transmits) differently, so two
// texts under different tags must never be treated as the same corpus. The
// tag is folded into BOTH identity-comparing surfaces -- the node stamp the
// write path records and the read fence verifies, and the persisted
// embed-retrieval-identity answer-reuse dimension -- so a semantic config
// change fails vectors closed to lexical until the prescribed
// `acr-projector rebuild --org`, and simultaneously stops stored answers
// from being reused across the change.
func EmbedCompositionTag(maxTextRunes int, includeBodies bool, prefixTagComponent string) string {
	body := 0
	if includeBodies {
		body = 1
	}
	if prefixTagComponent == "" {
		prefixTagComponent = EmbedPrefixTagComponentNone
	}
	return fmt.Sprintf("%s:r%d:b%d:%s", embedTextTemplateVersion, maxTextRunes, body, prefixTagComponent)
}

// EmbedRetrievalIdentityFromEnv computes the deployment-current embed
// retrieval identity for answer reuse (CHAOS-3833):
// "<provider>/<model>#<composition tag>" for a configured embedder, or
// EmbedRetrievalIdentityNone when vector retrieval is off.
//
// It reads the same embedprovider configuration EmbedderFromEnv builds the
// embedder from, so the persisted reuse dimension and the running embedder
// cannot disagree about provider/model/rune-cap within one process. It lives
// in this package, next to the composition it discriminates, so the tag's
// single authority (EmbedCompositionTag) serves both the node stamp and the
// reuse key.
func EmbedRetrievalIdentityFromEnv(lookup func(string) (string, bool)) (string, error) {
	if !embedprovider.Configured(lookup) {
		return EmbedRetrievalIdentityNone, nil
	}
	cfg, err := embedprovider.ConfigFromEnv(lookup)
	if err != nil {
		return "", err
	}
	includeBodies, err := embedprovider.BodiesIncluded(lookup)
	if err != nil {
		return "", err
	}
	return cfg.Provider + "/" + cfg.Model + "#" + EmbedCompositionTag(cfg.MaxTextRunes, includeBodies, cfg.PrefixTagComponent()), nil
}
