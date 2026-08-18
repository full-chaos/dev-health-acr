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
