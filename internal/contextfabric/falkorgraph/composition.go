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
const RetrievalPolicyVersion = "rp1"

// EmbedRetrievalIdentityNone is the persisted embed-retrieval-identity value
// for a deployment with NO embedder configured. A literal, never the empty
// string: migration 0014's CHECK forbids empty, and pginvestigation maps an
// empty value to SQL NULL ("this row never participates"), which would make
// a lexical-only deployment's answers never reusable -- the wrong outcome,
// since they have no vector lineage to go stale.
const EmbedRetrievalIdentityNone = "none"

// embedTextTemplateVersion names the per-kind embed-text COMPOSITION the
// write path currently produces (spec §4 Layer B). "t2" is CHAOS-3833's
// per-kind template set (search_text.go), including the organization embed
// skip-list; "t1" was the pre-CHAOS-3833 composition (entitySearchText =
// Label + Aliases + PreviousNames for every kind; content = title + body;
// episode = goal/outcome/summary). Any change to what any kind's composed
// text contains -- template edit, cap move, skip-list membership, prefix
// implementation -- bumps this constant, which moves the composition tag,
// which fails the read fence closed and moves the answer-reuse key, both
// through the one string below.
const embedTextTemplateVersion = "t2"

// embedPrefixSelector names the model prefix pair applied to embed texts
// (spec L6). No prefixes are implemented yet, so the selector is the fixed
// literal "none"; a future nomic/e5 prefix implementation replaces it with a
// per-model selector and thereby moves the tag.
const embedPrefixSelector = "none"

// EmbedCompositionTag is the canonical composition-tag literal (spec §4
// Layer C): template version, embed rune cap, body gate, prefix selector --
// e.g. "t1:r2000:b0:pnone". A LITERAL, not a hash, so an operator can read a
// stamp (or a persisted reuse row) and know exactly what produced it.
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
func EmbedCompositionTag(maxTextRunes int, includeBodies bool) string {
	body := 0
	if includeBodies {
		body = 1
	}
	return fmt.Sprintf("%s:r%d:b%d:p%s", embedTextTemplateVersion, maxTextRunes, body, embedPrefixSelector)
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
	return cfg.Provider + "/" + cfg.Model + "#" + EmbedCompositionTag(cfg.MaxTextRunes, includeBodies), nil
}
