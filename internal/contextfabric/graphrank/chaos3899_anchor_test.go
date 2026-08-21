package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestBindAnchor_UniqueClaimant(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"dev-health-acr": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchAlias}},
	}
	anchor, ok := BindAnchor(claimants, true)
	if !ok {
		t.Fatalf("BindAnchor: want ok=true")
	}
	if anchor.Kind != contextfabric.SubjectRepository || anchor.CanonicalID != "repository:r-1" {
		t.Fatalf("anchor = %#v", anchor)
	}
}

func TestBindAnchor_IncompleteReadRefuses(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"dev-health-acr": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchAlias}},
	}
	if _, ok := BindAnchor(claimants, false); ok {
		t.Fatalf("BindAnchor with complete=false: want ok=false (R4)")
	}
}

func TestBindAnchor_TwoClaimantsForOneTermRefuses(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"acr": {
			{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchAlias},
			{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-2"}, Mechanism: contextfabric.MatchAlias},
		},
	}
	if _, ok := BindAnchor(claimants, true); ok {
		t.Fatalf("BindAnchor with 2 claimants for one term: want ok=false (R4)")
	}
}

func TestBindAnchor_TwoDifferentTermsNamingDifferentAnchorsRefuses(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"acr":    {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchAlias}},
		"health": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-2"}, Mechanism: contextfabric.MatchAlias}},
	}
	if _, ok := BindAnchor(claimants, true); ok {
		t.Fatalf("BindAnchor with two genuinely different single-claimant anchors: want ok=false, an ambiguous anchor must refuse, never guess")
	}
}

func TestBindAnchor_SameAnchorFromTwoTermsAgrees(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"acr":            {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchAlias}},
		"dev-health-acr": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchExact}},
	}
	anchor, ok := BindAnchor(claimants, true)
	if !ok || anchor.CanonicalID != "repository:r-1" {
		t.Fatalf("anchor = %#v ok=%v, want the agreed repository:r-1", anchor, ok)
	}
}

func TestBindAnchor_NoClaimantsRefuses(t *testing.T) {
	t.Parallel()
	if _, ok := BindAnchor(map[string][]IdentityMatch{}, true); ok {
		t.Fatalf("BindAnchor with no claimants at all: want ok=false")
	}
}

// TestAnchorTermCandidates_TwoTermsSameEntityIsDeterministic pins the codex
// xhigh review finding (chaos-pivot-p1, first round, finding 3): two
// DIFFERENT unique-claimant terms ("repo-a" and "full-chaos/repo-a") can
// both name the SAME (kind, canonical_id) -- when that happens, exactly one
// term's info occupies anchorTermCandidates' own result, and repeated calls
// with the identical input must always pick the SAME one (the
// lexicographically-smallest term), never a value that varies with Go's
// randomized map iteration order.
func TestAnchorTermCandidates_TwoTermsSameEntityIsDeterministic(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"repo-a":            {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1", Label: "repo-a-label"}, Mechanism: contextfabric.MatchAlias}},
		"full-chaos/repo-a": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1", Label: "full-chaos-label"}, Mechanism: contextfabric.MatchAlias}},
	}
	key := anchorCandidateKey{kind: contextfabric.SubjectRepository, id: "repository:r-1"}
	for i := 0; i < 50; i++ {
		candidates := anchorTermCandidates(claimants, true)
		if len(candidates) != 1 {
			t.Fatalf("iteration %d: len(candidates) = %d, want 1", i, len(candidates))
		}
		info, ok := candidates[key]
		if !ok {
			t.Fatalf("iteration %d: candidates missing key %#v", i, key)
		}
		if info.term != "full-chaos/repo-a" {
			t.Fatalf("iteration %d: info.term = %q, want the lexicographically-smallest term %q (non-deterministic tie-break)", i, info.term, "full-chaos/repo-a")
		}
	}
}

// TestAnchorAmbiguousTermClaimants_SingleTermSurfacesAllClaimants pins the
// CHAOS-4012 widening's own base case: a term BindAnchor/anchorTermCandidates
// drop entirely (>=2 claimants) must have EVERY one of its claimants
// surfaced here, not just the ones anchorTermCandidates would have kept.
func TestAnchorAmbiguousTermClaimants_SingleTermSurfacesAllClaimants(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"the api repo": {
			{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1", Label: "org-a/api"}, Mechanism: contextfabric.MatchAlias},
			{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-2", Label: "org-b/api"}, Mechanism: contextfabric.MatchAlias},
		},
	}
	got := anchorAmbiguousTermClaimants(claimants, true)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	for _, id := range []string{"repository:r-1", "repository:r-2"} {
		key := anchorCandidateKey{kind: contextfabric.SubjectRepository, id: id}
		if info, ok := got[key]; !ok || info.term != "the api repo" {
			t.Errorf("got[%#v] = %#v, ok=%v, want term=%q", key, info, ok, "the api repo")
		}
	}
}

// TestAnchorAmbiguousTermClaimants_UniqueTermContributesNothing pins the
// boundary with anchorTermCandidates: a term with exactly ONE claimant is
// NOT ambiguous, and must contribute nothing here (it belongs solely to
// anchorTermCandidates' own decisive scan).
func TestAnchorAmbiguousTermClaimants_UniqueTermContributesNothing(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"dev-health-acr": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchAlias}},
	}
	if got := anchorAmbiguousTermClaimants(claimants, true); len(got) != 0 {
		t.Fatalf("got = %#v, want empty: a unique-claimant term is not ambiguous", got)
	}
}

// TestAnchorAmbiguousTermClaimants_IncompleteReadReturnsNil pins the same
// fail-closed shape anchorTermCandidates itself uses: an incomplete
// identity-universe read proves nothing about any term's claimant set.
func TestAnchorAmbiguousTermClaimants_IncompleteReadReturnsNil(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"the api repo": {
			{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1"}, Mechanism: contextfabric.MatchAlias},
			{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-2"}, Mechanism: contextfabric.MatchAlias},
		},
	}
	if got := anchorAmbiguousTermClaimants(claimants, false); got != nil {
		t.Fatalf("got = %#v, want nil for complete=false", got)
	}
}

// TestAnchorAmbiguousTermClaimants_ClaimantOrderIsDeterministic pins the
// per-term first-write-wins tie-break: when the SAME term's claimant slice
// contains two rows naming the SAME (kind, canonical_id) under different
// labels (a defensive, not expected, shape -- but the sort-before-scan
// discipline must hold regardless), repeated calls must always keep the
// SAME one (the sorted-first row, "repository:r-1" sorts before
// "repository:r-2" regardless of the input slice's own order), never a
// value that varies with Go's slice/map iteration.
func TestAnchorAmbiguousTermClaimants_ClaimantOrderIsDeterministic(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"the api repo": {
			{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1", Label: "second-in-slice"}, Mechanism: contextfabric.MatchAlias},
			{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r-1", Label: "first-in-slice"}, Mechanism: contextfabric.MatchExact},
		},
	}
	key := anchorCandidateKey{kind: contextfabric.SubjectRepository, id: "repository:r-1"}
	for i := 0; i < 50; i++ {
		got := anchorAmbiguousTermClaimants(claimants, true)
		if len(got) != 1 {
			t.Fatalf("iteration %d: len(got) = %d, want 1 (both rows share one key)", i, len(got))
		}
		info, ok := got[key]
		if !ok || info.label != "second-in-slice" {
			t.Fatalf("iteration %d: got[%#v] = %#v, ok=%v, want the FIRST slice entry's label %q to win", i, key, info, ok, "second-in-slice")
		}
	}
}

func TestClaimantsFromCandidateNodes(t *testing.T) {
	t.Parallel()
	node := CandidateNode{Attributes: map[string]interface{}{
		"subject_kind": "repository", "canonical_id": "repository:r-1", "label": "dev-health-acr",
	}, Mechanism: contextfabric.MatchAlias}
	out := claimantsFromCandidateNodes(map[string][]CandidateNode{"acr": {node}})
	matches, ok := out["acr"]
	if !ok || len(matches) != 1 {
		t.Fatalf("claimantsFromCandidateNodes = %#v", out)
	}
	if matches[0].Row.Kind != contextfabric.SubjectRepository || matches[0].Row.CanonicalID != "repository:r-1" {
		t.Fatalf("matches[0] = %#v", matches[0])
	}
}

// TestClaimantsFromCandidateNodes_PopulatesAliases pins the codex xhigh
// review round-3 finding (confirmed and fixed, 2026-08-19): every
// IdentityRow this function builds used to leave Aliases/ProviderAliases
// at their nil zero value, making chaos3899_source_native_grammar.go's own
// resolveSourceNative structurally unable to ever match a row via those
// fields in the REAL production path (ShadowEvidenceRoundInput.AliasClaimants
// is built from exactly this function's own output, resolve.go:1327).
// AliasAttributes/ProviderAliasAttributes (subject.go) already read a
// node's "aliases"/"provider_aliases" graph properties for the existing,
// non-shadow identity mechanism (candidate.go) -- this function must now
// read the SAME two properties off the SAME already-in-memory
// node.Attributes map.
func TestClaimantsFromCandidateNodes_PopulatesAliases(t *testing.T) {
	t.Parallel()
	node := CandidateNode{Attributes: map[string]interface{}{
		"subject_kind": "repository", "canonical_id": "repository:r-1", "label": "dev-health-acr",
		"aliases":          []string{"dev-health-acr"},
		"provider_aliases": []string{"github:full-chaos/dev-health-acr"},
	}, Mechanism: contextfabric.MatchAlias}
	out := claimantsFromCandidateNodes(map[string][]CandidateNode{"acr": {node}})
	matches, ok := out["acr"]
	if !ok || len(matches) != 1 {
		t.Fatalf("claimantsFromCandidateNodes = %#v", out)
	}
	row := matches[0].Row
	if len(row.Aliases) != 1 || row.Aliases[0] != "dev-health-acr" {
		t.Fatalf("row.Aliases = %#v, want [dev-health-acr]", row.Aliases)
	}
	if len(row.ProviderAliases) != 1 || row.ProviderAliases[0] != "github:full-chaos/dev-health-acr" {
		t.Fatalf("row.ProviderAliases = %#v, want [github:full-chaos/dev-health-acr]", row.ProviderAliases)
	}
}

// TestClaimantsFromCandidateNodes_MissingAliasAttributesStayEmpty pins the
// nil-safe fallback: a node with no "aliases"/"provider_aliases"
// attribute at all (an older projection cycle, or a kind that never
// carries them) produces an IdentityRow with nil Aliases/ProviderAliases,
// never a panic -- AliasAttributes/ProviderAliasAttributes' own
// "attribute absent -> nil" convention, unchanged.
func TestClaimantsFromCandidateNodes_MissingAliasAttributesStayEmpty(t *testing.T) {
	t.Parallel()
	node := CandidateNode{Attributes: map[string]interface{}{
		"subject_kind": "repository", "canonical_id": "repository:r-1", "label": "dev-health-acr",
	}, Mechanism: contextfabric.MatchAlias}
	out := claimantsFromCandidateNodes(map[string][]CandidateNode{"acr": {node}})
	row := out["acr"][0].Row
	if row.Aliases != nil || row.ProviderAliases != nil {
		t.Fatalf("row = %#v, want nil Aliases/ProviderAliases when the node carries neither attribute", row)
	}
}
