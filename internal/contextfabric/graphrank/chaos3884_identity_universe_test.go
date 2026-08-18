package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestMatchIdentityRows_NilOnEmptyInput(t *testing.T) {
	t.Parallel()
	if got := MatchIdentityRows(nil, []string{"foo"}); got != nil {
		t.Fatalf("MatchIdentityRows(nil rows) = %v, want nil", got)
	}
	if got := MatchIdentityRows([]IdentityRow{{Label: "foo"}}, nil); got != nil {
		t.Fatalf("MatchIdentityRows(nil terms) = %v, want nil", got)
	}
}

func TestMatchIdentityRows_LabelMatchIsMatchExact(t *testing.T) {
	t.Parallel()
	rows := []IdentityRow{{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "chaos-ops"}}
	got := MatchIdentityRows(rows, []string{"chaos-ops"})
	matches := got["chaos-ops"]
	if len(matches) != 1 || matches[0].Mechanism != contextfabric.MatchExact || matches[0].Row.CanonicalID != "p1" {
		t.Fatalf("MatchIdentityRows = %+v, want one MatchExact match for p1", got)
	}
}

func TestMatchIdentityRows_AliasMatchIsMatchAlias(t *testing.T) {
	t.Parallel()
	rows := []IdentityRow{{Kind: contextfabric.SubjectRepository, CanonicalID: "r1", Label: "full-chaos/dev-health-acr", Aliases: []string{"dev-health-acr"}}}
	got := MatchIdentityRows(rows, []string{"dev-health-acr"})
	matches := got["dev-health-acr"]
	if len(matches) != 1 || matches[0].Mechanism != contextfabric.MatchAlias {
		t.Fatalf("MatchIdentityRows = %+v, want one MatchAlias match", got)
	}
}

func TestMatchIdentityRows_ProviderAliasMatchIsMatchProviderKey(t *testing.T) {
	t.Parallel()
	rows := []IdentityRow{{Kind: contextfabric.SubjectRepository, CanonicalID: "r1", Label: "full-chaos/dev-health-acr", ProviderAliases: []string{"github:full-chaos/dev-health-acr"}}}
	got := MatchIdentityRows(rows, []string{"github:full-chaos/dev-health-acr"})
	matches := got["github:full-chaos/dev-health-acr"]
	if len(matches) != 1 || matches[0].Mechanism != contextfabric.MatchProviderKey {
		t.Fatalf("MatchIdentityRows = %+v, want one MatchProviderKey match", got)
	}
}

// TestMatchIdentityRows_LabelPriorityOverAlias pins the same key-class
// priority NodeCandidate itself enforces: a row whose LABEL and ALIAS both
// happen to equal the same term is reported once, as MatchExact only.
func TestMatchIdentityRows_LabelPriorityOverAlias(t *testing.T) {
	t.Parallel()
	rows := []IdentityRow{{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "chaos-ops", Aliases: []string{"chaos-ops"}}}
	got := MatchIdentityRows(rows, []string{"chaos-ops"})
	matches := got["chaos-ops"]
	if len(matches) != 1 || matches[0].Mechanism != contextfabric.MatchExact {
		t.Fatalf("MatchIdentityRows = %+v, want exactly one MatchExact match (label priority)", got)
	}
}

// TestMatchIdentityRows_CollisionProducesTwoClaimants is HIGH-5's core
// scenario at this layer: two DIFFERENT rows both claim the SAME
// normalized term.
func TestMatchIdentityRows_CollisionProducesTwoClaimants(t *testing.T) {
	t.Parallel()
	rows := []IdentityRow{
		{Kind: contextfabric.SubjectRepository, CanonicalID: "r1", Label: "full-chaos/chaos-ops", Aliases: []string{"chaos-ops"}},
		{Kind: contextfabric.SubjectTeam, CanonicalID: "t1", Label: "Chaos Team", Aliases: []string{"chaos-ops"}},
	}
	got := MatchIdentityRows(rows, []string{"chaos-ops"})
	matches := got["chaos-ops"]
	if len(matches) != 2 {
		t.Fatalf("MatchIdentityRows = %+v, want 2 claimants", matches)
	}
}

// TestMatchIdentityRows_NormalizesCaseAndWhitespace proves the match uses
// NormalizeAliasTerm on both sides, not a raw string comparison.
func TestMatchIdentityRows_NormalizesCaseAndWhitespace(t *testing.T) {
	t.Parallel()
	rows := []IdentityRow{{Kind: contextfabric.SubjectRepository, CanonicalID: "r1", Label: "full-chaos/Dev-Health-ACR", Aliases: []string{"Dev-Health-ACR"}}}
	got := MatchIdentityRows(rows, []string{"  dev-health-acr  "})
	matches := got["  dev-health-acr  "]
	if len(matches) != 1 || matches[0].Mechanism != contextfabric.MatchAlias {
		t.Fatalf("MatchIdentityRows = %+v, want a case/whitespace-insensitive alias match keyed by the ORIGINAL term", got)
	}
}

// TestMatchIdentityRows_RowMatchingMultipleTermsIsNotUnderCounted proves
// the recall fix: a row that genuinely claims TWO different query terms
// (via two different classes) is reported under BOTH, not collapsed to
// only the first one found.
func TestMatchIdentityRows_RowMatchingMultipleTermsIsNotUnderCounted(t *testing.T) {
	t.Parallel()
	rows := []IdentityRow{{
		Kind: contextfabric.SubjectRepository, CanonicalID: "r1", Label: "full-chaos/dev-health-acr",
		Aliases: []string{"dev-health-acr"}, ProviderAliases: []string{"github:full-chaos/dev-health-acr"},
	}}
	got := MatchIdentityRows(rows, []string{"dev-health-acr", "github:full-chaos/dev-health-acr"})
	if len(got["dev-health-acr"]) != 1 || got["dev-health-acr"][0].Mechanism != contextfabric.MatchAlias {
		t.Fatalf("MatchIdentityRows[\"dev-health-acr\"] = %+v, want one MatchAlias match", got["dev-health-acr"])
	}
	if len(got["github:full-chaos/dev-health-acr"]) != 1 || got["github:full-chaos/dev-health-acr"][0].Mechanism != contextfabric.MatchProviderKey {
		t.Fatalf("MatchIdentityRows[provider term] = %+v, want one MatchProviderKey match", got["github:full-chaos/dev-health-acr"])
	}
}

func TestMatchIdentityRows_NoMatchOmitsTerm(t *testing.T) {
	t.Parallel()
	rows := []IdentityRow{{Kind: contextfabric.SubjectRepository, CanonicalID: "r1", Label: "full-chaos/dev-health-acr"}}
	got := MatchIdentityRows(rows, []string{"unrelated-term"})
	if len(got) != 0 {
		t.Fatalf("MatchIdentityRows = %+v, want empty (no claimants for an unmatched term)", got)
	}
}
