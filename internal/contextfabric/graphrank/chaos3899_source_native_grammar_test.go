package graphrank

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TestBindSourceNativeHandles_ProseIsNotSwallowed pins the precision
// discipline sourceNativeGrammarRegistry's own doc comment describes: none
// of these grammars should fire on ordinary prose that merely resembles
// their shape.
func TestBindSourceNativeHandles_ProseIsNotSwallowed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		question string
	}{
		{"numeric_fraction", "3/4 of the tests passed this run"},
		{"oncall_ratio", "we run 24/7 on-call coverage"},
		{"plain_run_id_all_decimal", "CI run 18234567 failed again"},
		{"large_plain_integer", "the byte count was 1234567 for that request"},
		{"bare_word_slash_word_no_letters", "1/2 of the reviewers approved"},
		{"ordinary_branching_sentence", "we should branch out into new markets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bound := BindSourceNativeHandles(tc.question, nil, true)
			for _, b := range bound {
				t.Errorf("BindSourceNativeHandles(%q) matched grammar %q on term %q -- ordinary prose must not swallow", tc.question, b.Grammar, b.Term)
			}
		})
	}
}

func TestBindSourceNativeHandles_GrammarShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		question string
		grammar  string
		term     string
	}{
		{"provider_qualified_name", "see github.com/full-chaos/dev-health-acr for details", "provider_qualified_name", "github.com/full-chaos/dev-health-acr"},
		{"repo_slug", "why did full-chaos/dev-health-acr fail to build?", "repo_slug", "full-chaos/dev-health-acr"},
		{"branch_name_keyword", `why did branch "release-2026-08" fail CI?`, "branch_name_keyword", "release-2026-08"},
		{"branch_name_prefix", "the fix/handle-grammar-widening change broke something", "branch_name_prefix", "fix/handle-grammar-widening"},
		{"commit_sha_short", "commit a1b2c3d broke the build", "commit_sha", "a1b2c3d"},
		{"commit_sha_long", "reverted 9f8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b2a1f0e", "commit_sha", "9f8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b2a1f0e"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bound := BindSourceNativeHandles(tc.question, nil, true)
			found := false
			for _, b := range bound {
				if b.Grammar == tc.grammar && b.Term == tc.term {
					found = true
				}
			}
			if !found {
				t.Fatalf("BindSourceNativeHandles(%q) = %#v, want a %s match on %q", tc.question, bound, tc.grammar, tc.term)
			}
		})
	}
}

// TestBindSourceNativeHandles_CommitSHANeverAllDecimal pins commit_sha's
// own precision guard: an all-decimal run, however long, must never bind
// -- it is indistinguishable from an ordinary large integer.
func TestBindSourceNativeHandles_CommitSHANeverAllDecimal(t *testing.T) {
	t.Parallel()
	bound := BindSourceNativeHandles("the run id was 1234567890123456 for that pipeline", nil, true)
	for _, b := range bound {
		if b.Grammar == "commit_sha" {
			t.Fatalf("commit_sha bound the all-decimal term %q -- must require a hex letter", b.Term)
		}
	}
}

// TestBindSourceNativeHandles_ResolvesViaIdentityUniverse pins the R4
// completeness+uniqueness discipline this file reuses from BindAnchor:
// exactly one claimant + complete read -> Resolved; anything else ->
// unresolved (a syntactic-match-only "false positive" in the
// pre-registration's own sense).
func TestBindSourceNativeHandles_ResolvesViaIdentityUniverse(t *testing.T) {
	t.Parallel()
	question := "why did full-chaos/dev-health-acr fail to build?"

	t.Run("unique_claimant_resolves", func(t *testing.T) {
		t.Parallel()
		claimants := map[string][]IdentityMatch{
			"full-chaos/dev-health-acr": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:1"}, Mechanism: contextfabric.MatchAlias}},
		}
		bound := BindSourceNativeHandles(question, claimants, true)
		found := false
		for _, b := range bound {
			if b.Grammar == "repo_slug" {
				found = true
				if !b.Resolved || b.Kind != contextfabric.SubjectRepository {
					t.Fatalf("repo_slug bind = %#v, want Resolved=true Kind=repository", b)
				}
			}
		}
		if !found {
			t.Fatalf("no repo_slug bind found in %#v", bound)
		}
	})

	t.Run("no_claimant_stays_unresolved", func(t *testing.T) {
		t.Parallel()
		bound := BindSourceNativeHandles(question, map[string][]IdentityMatch{}, true)
		for _, b := range bound {
			if b.Grammar == "repo_slug" && b.Resolved {
				t.Fatalf("repo_slug resolved with no claimant present: %#v", b)
			}
		}
	})

	t.Run("ambiguous_claimants_stay_unresolved", func(t *testing.T) {
		t.Parallel()
		claimants := map[string][]IdentityMatch{
			"full-chaos/dev-health-acr": {
				{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:1"}, Mechanism: contextfabric.MatchAlias},
				{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:2"}, Mechanism: contextfabric.MatchAlias},
			},
		}
		bound := BindSourceNativeHandles(question, claimants, true)
		for _, b := range bound {
			if b.Grammar == "repo_slug" && b.Resolved {
				t.Fatalf("repo_slug resolved with 2 claimants present (R4 violated): %#v", b)
			}
		}
	})

	t.Run("incomplete_read_stays_unresolved", func(t *testing.T) {
		t.Parallel()
		claimants := map[string][]IdentityMatch{
			"full-chaos/dev-health-acr": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:1"}, Mechanism: contextfabric.MatchAlias}},
		}
		bound := BindSourceNativeHandles(question, claimants, false)
		for _, b := range bound {
			if b.Grammar == "repo_slug" && b.Resolved {
				t.Fatalf("repo_slug resolved on an INCOMPLETE read (R4 violated): %#v", b)
			}
		}
	})
}

// TestRunShadowEvidenceRound_SourceNativeWideningNeverChangesDecisiveOutcome
// is CHAOS-3899's own shadow-only guarantee, pinned directly: two rounds
// share the IDENTICAL AliasClaimants map (so BindAnchor's own,
// PRE-EXISTING computation -- which reads that same map independent of
// question content -- is held constant across both calls) and differ ONLY
// in whether the question text also contains a source-native grammar
// match for an entry already in that map. Both must produce byte-identical
// Outcome/Reason/DIdentity/PreconditionUnproven/NonCensusedSurvivor/Kinds
// -- the widening measurement (evidence_source_native/
// evidence_source_native_probe trace events) is observable ONLY via the
// tracer, never via the returned Attestation's decisive fields.
func TestRunShadowEvidenceRound_SourceNativeWideningNeverChangesDecisiveOutcome(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"full-chaos/dev-health-acr": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:1"}, Mechanism: contextfabric.MatchAlias}},
	}

	run := func(question string) Attestation {
		census := withCensus(contextfabric.SubjectPullRequest, CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC()}, nil)
		input := baseInput()
		input.Question = question
		input.PooledKinds = []CensusKind{contextfabric.SubjectPullRequest}
		input.AliasClaimants = claimants
		input.CensusFunc = census.fn
		return RunShadowEvidenceRound(context.Background(), input, nil)
	}

	without := run("why did PR 532 fail in CI?")
	with := run("why did full-chaos/dev-health-acr and PR 532 fail in CI?")

	// Sanity: the source-native grammar DID fire differently between the
	// two questions (otherwise this test would vacuously pass without
	// exercising the widening at all).
	withoutBinds := BindSourceNativeHandles("why did PR 532 fail in CI?", claimants, true)
	withBinds := BindSourceNativeHandles("why did full-chaos/dev-health-acr and PR 532 fail in CI?", claimants, true)
	if len(withoutBinds) != 0 || len(withBinds) == 0 || !withBinds[0].Resolved {
		t.Fatalf("test setup did not actually exercise the widening: withoutBinds=%#v withBinds=%#v", withoutBinds, withBinds)
	}

	if without.Outcome != with.Outcome || without.Reason != with.Reason || without.DIdentity != with.DIdentity ||
		without.PreconditionUnproven != with.PreconditionUnproven || without.NonCensusedSurvivor != with.NonCensusedSurvivor ||
		len(without.Kinds) != len(with.Kinds) {
		t.Fatalf("source-native widening changed the decisive Attestation: without=%#v with=%#v", without, with)
	}
}
