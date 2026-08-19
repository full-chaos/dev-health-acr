package graphrank

import (
	"context"
	"reflect"
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
		{"branch_date_reference", "what shipped on branch 2026-08-19"},
		{"hex_speak_word_deadbeef", "the constant was deadbeef in that header"},
		{"hex_speak_word_cafebabe", "the magic number cafebabe showed up in the dump"},
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

// TestBindSourceNativeHandles_ProviderDomainRequiresRealHostBoundary pins
// the CodeQL go/regex/missing-regexp-anchor finding (confirmed and fixed,
// 2026-08-19): provider_qualified_name's pattern must never match
// "github.com"/"gitlab.com" when that literal is actually a SUFFIX or
// SUBDOMAIN of a DIFFERENT, unrelated hostname -- a plain \b anchor
// (Go RE2's word/non-word transition) is insufficient, since "-" and "."
// are both non-word characters and so both satisfy \b immediately before
// "github"/"gitlab" even though neither is a valid host-name boundary.
func TestBindSourceNativeHandles_ProviderDomainRequiresRealHostBoundary(t *testing.T) {
	t.Parallel()
	hostileCases := []struct {
		name     string
		question string
	}{
		{"hyphenated_prefix_host", "see evil-github.com/org/repo for details"},
		{"subdomain_host", "see sub.github.com/org/repo for details"},
		{"no_separator_prefix_host", "see xgithub.com/org/repo for details"},
		{"hyphenated_gitlab_prefix_host", "see evil-gitlab.com/org/repo for details"},
	}
	for _, tc := range hostileCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bound := BindSourceNativeHandles(tc.question, nil, true)
			for _, b := range bound {
				if b.Grammar == "provider_qualified_name" {
					t.Fatalf("provider_qualified_name matched %q inside a hostile/unrelated host -- want the real github.com/gitlab.com boundary enforced, got bind %#v", tc.question, b)
				}
			}
		})
	}

	// Sanity: the legitimate cases -- start-of-string and preceded by
	// ordinary prose (a space) -- must still match, proving this is a
	// boundary fix, not an over-correction that breaks the real case.
	legitCases := []struct {
		name     string
		question string
	}{
		{"start_of_string", "github.com/full-chaos/dev-health-acr is the repo"},
		{"preceded_by_space", "see github.com/full-chaos/dev-health-acr for details"},
	}
	for _, tc := range legitCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bound := BindSourceNativeHandles(tc.question, nil, true)
			found := false
			for _, b := range bound {
				if b.Grammar == "provider_qualified_name" && b.Term == "github.com/full-chaos/dev-health-acr" {
					found = true
				}
			}
			if !found {
				t.Fatalf("provider_qualified_name did not match the legitimate case %q: %#v", tc.question, bound)
			}
		})
	}
}

// TestBindSourceNativeHandles_ProviderURLNeverMisSplitsIntoRepoSlug pins
// the codex xhigh review finding (confirmed and fixed): a
// "github.com/org/repo" URL span must be reported ONCE, as a
// provider_qualified_name match, never ALSO (or instead) as a bogus,
// one-segment-short repo_slug match like "github.com/org" -- repo_slug's
// own avoidOverlapWith entry is what prevents this.
func TestBindSourceNativeHandles_ProviderURLNeverMisSplitsIntoRepoSlug(t *testing.T) {
	t.Parallel()
	bound := BindSourceNativeHandles("see github.com/full-chaos/dev-health-acr for details", nil, true)
	var sawProvider, sawRepoSlug bool
	for _, b := range bound {
		switch b.Grammar {
		case "provider_qualified_name":
			sawProvider = true
			if b.Term != "github.com/full-chaos/dev-health-acr" {
				t.Fatalf("provider_qualified_name term = %q, want the full URL span", b.Term)
			}
		case "repo_slug":
			sawRepoSlug = true
			t.Errorf("repo_slug ALSO matched inside the provider URL span: %#v -- want it suppressed by avoidOverlapWith", b)
		}
	}
	if !sawProvider {
		t.Fatalf("no provider_qualified_name match found in %#v", bound)
	}
	if sawRepoSlug {
		t.Fatalf("repo_slug matched inside a provider_qualified_name span")
	}
}

// repositoryClaimants is a small test helper: wraps one IdentityRow (with
// its own Aliases/ProviderAliases already populated the way the identity
// universe actually produces them) into a claimantsByTerm-shaped map,
// keyed by an ARBITRARY, UNRELATED term string -- standing in for
// "whatever SubjectTerm the question interpreter happened to extract that
// independently matched this row via SOME alias" (codex xhigh review
// finding round 2: resolveSourceNative must find a row via its own
// CONTENT, never via the caller correlating a specific map key, so these
// fixtures deliberately do NOT use the alias string as the map key --
// using an unrelated key is what actually exercises the fix; using the
// alias string as the key would only prove the OLD, wrong map-key-lookup
// behavior still works by coincidence).
func repositoryClaimants(canonicalID string, aliases, providerAliases []string) map[string][]IdentityMatch {
	row := IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: canonicalID, Aliases: aliases, ProviderAliases: providerAliases}
	return map[string][]IdentityMatch{
		"unrelated-interpreter-term": {{Row: row, Mechanism: contextfabric.MatchAlias}},
	}
}

// TestBindSourceNativeHandles_ResolvesViaIdentityUniverse pins the R4
// completeness+uniqueness discipline this file reuses from BindAnchor,
// the alias-shape lookupKey discipline (codex xhigh review round 1: a
// repository's real alias shapes are its bare name and
// "provider:org/repo", never the raw "org/repo" or "github.com/org/repo"
// text a question actually contains), AND the reachability discipline
// (codex xhigh review round 2: resolution must find a claimant by
// SCANNING every row's own alias content already present in
// claimantsByTerm's VALUES, never by treating the transformed alias as a
// MAP KEY -- claimantsByTerm's keys are the interpreter's own,
// un-normalized SubjectTerms, which this grammar's regex match has no way
// to predict or influence).
func TestBindSourceNativeHandles_ResolvesViaIdentityUniverse(t *testing.T) {
	t.Parallel()

	t.Run("repo_slug_resolves_via_bare_name_alias", func(t *testing.T) {
		t.Parallel()
		question := "why did full-chaos/dev-health-acr fail to build?"
		claimants := repositoryClaimants("repository:1", []string{"dev-health-acr"}, nil)
		bound := BindSourceNativeHandles(question, claimants, true)
		found := false
		for _, b := range bound {
			if b.Grammar == "repo_slug" {
				found = true
				if b.Term != "full-chaos/dev-health-acr" {
					t.Fatalf("repo_slug Term = %q, want the raw as-typed slug (Term must NOT be normalized -- only the search value is)", b.Term)
				}
				if !b.Resolved || b.Kind != contextfabric.SubjectRepository {
					t.Fatalf("repo_slug bind = %#v, want Resolved=true Kind=repository", b)
				}
			}
		}
		if !found {
			t.Fatalf("no repo_slug bind found in %#v", bound)
		}
	})

	t.Run("provider_qualified_name_resolves_via_provider_colon_alias", func(t *testing.T) {
		t.Parallel()
		question := "see github.com/full-chaos/dev-health-acr for details"
		claimants := repositoryClaimants("repository:1", nil, []string{"github:full-chaos/dev-health-acr"})
		bound := BindSourceNativeHandles(question, claimants, true)
		found := false
		for _, b := range bound {
			if b.Grammar == "provider_qualified_name" {
				found = true
				if !b.Resolved || b.Kind != contextfabric.SubjectRepository {
					t.Fatalf("provider_qualified_name bind = %#v, want Resolved=true Kind=repository", b)
				}
			}
		}
		if !found {
			t.Fatalf("no provider_qualified_name bind found in %#v", bound)
		}
	})

	t.Run("gitlab_domain_normalizes_too", func(t *testing.T) {
		t.Parallel()
		claimants := repositoryClaimants("repository:2", nil, []string{"gitlab:acme/widgets"})
		bound := BindSourceNativeHandles("check gitlab.com/acme/widgets please", claimants, true)
		found := false
		for _, b := range bound {
			if b.Grammar == "provider_qualified_name" && b.Resolved {
				found = true
			}
		}
		if !found {
			t.Fatalf("gitlab.com provider match did not resolve via the gitlab: alias: %#v", bound)
		}
	})

	t.Run("resolves_regardless_of_which_map_key_the_interpreter_used", func(t *testing.T) {
		t.Parallel()
		// The load-bearing case for the round-2 fix: the map key here is
		// "totally-unrelated-term" (standing in for whatever term the
		// interpreter actually extracted for some OTHER part of the
		// question) -- resolution must still succeed because it scans the
		// ROW's own Aliases, never the key that happens to reach it.
		row := IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:1", Aliases: []string{"dev-health-acr"}}
		claimants := map[string][]IdentityMatch{
			"totally-unrelated-term": {{Row: row, Mechanism: contextfabric.MatchAlias}},
		}
		bound := BindSourceNativeHandles("why did full-chaos/dev-health-acr fail to build?", claimants, true)
		found := false
		for _, b := range bound {
			if b.Grammar == "repo_slug" && b.Resolved {
				found = true
			}
		}
		if !found {
			t.Fatalf("repo_slug did not resolve via row content when reached through an unrelated map key: %#v", bound)
		}
	})

	t.Run("no_claimant_stays_unresolved", func(t *testing.T) {
		t.Parallel()
		bound := BindSourceNativeHandles("why did full-chaos/dev-health-acr fail to build?", map[string][]IdentityMatch{}, true)
		for _, b := range bound {
			if b.Grammar == "repo_slug" && b.Resolved {
				t.Fatalf("repo_slug resolved with no claimant present: %#v", b)
			}
		}
	})

	t.Run("no_matching_alias_stays_unresolved", func(t *testing.T) {
		t.Parallel()
		// A row IS present, but none of ITS OWN aliases match -- proves
		// this is a genuine content check, not "any row present resolves".
		claimants := repositoryClaimants("repository:9", []string{"some-other-repo"}, nil)
		bound := BindSourceNativeHandles("why did full-chaos/dev-health-acr fail to build?", claimants, true)
		for _, b := range bound {
			if b.Grammar == "repo_slug" && b.Resolved {
				t.Fatalf("repo_slug resolved against a row whose own aliases do not match: %#v", b)
			}
		}
	})

	t.Run("ambiguous_claimants_stay_unresolved", func(t *testing.T) {
		t.Parallel()
		// TWO DIFFERENT rows (different canonical ids) both carry the SAME
		// bare-name alias -- R4 refuses on ambiguity rather than guessing.
		claimants := map[string][]IdentityMatch{
			"term-a": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:1", Aliases: []string{"dev-health-acr"}}, Mechanism: contextfabric.MatchAlias}},
			"term-b": {{Row: IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:2", Aliases: []string{"dev-health-acr"}}, Mechanism: contextfabric.MatchAlias}},
		}
		bound := BindSourceNativeHandles("why did full-chaos/dev-health-acr fail to build?", claimants, true)
		for _, b := range bound {
			if b.Grammar == "repo_slug" && b.Resolved {
				t.Fatalf("repo_slug resolved with 2 distinct claimant rows present (R4 violated): %#v", b)
			}
		}
	})

	t.Run("incomplete_read_stays_unresolved", func(t *testing.T) {
		t.Parallel()
		claimants := repositoryClaimants("repository:1", []string{"dev-health-acr"}, nil)
		bound := BindSourceNativeHandles("why did full-chaos/dev-health-acr fail to build?", claimants, false)
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
// match for a row already in that map. Both must produce byte-identical
// Outcome/Reason/DIdentity/PreconditionUnproven/NonCensusedSurvivor and an
// IDENTICAL Kinds slice (not just length -- codex xhigh review finding:
// comparing len(Kinds) alone could miss a content divergence within an
// unchanged-length slice) -- the widening measurement (evidence_source_native/
// evidence_source_native_probe trace events) is observable ONLY via the
// tracer, never via the returned Attestation's decisive fields.
func TestRunShadowEvidenceRound_SourceNativeWideningNeverChangesDecisiveOutcome(t *testing.T) {
	t.Parallel()
	claimants := repositoryClaimants("repository:1", []string{"dev-health-acr"}, nil)

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

	// Sanity: the source-native grammar DID fire (and resolve) differently
	// between the two questions (otherwise this test would vacuously pass
	// without exercising the widening at all).
	withoutBinds := BindSourceNativeHandles("why did PR 532 fail in CI?", claimants, true)
	withBinds := BindSourceNativeHandles("why did full-chaos/dev-health-acr and PR 532 fail in CI?", claimants, true)
	if len(withoutBinds) != 0 || len(withBinds) == 0 || !withBinds[0].Resolved {
		t.Fatalf("test setup did not actually exercise the widening: withoutBinds=%#v withBinds=%#v", withoutBinds, withBinds)
	}

	if without.Outcome != with.Outcome || without.Reason != with.Reason || without.DIdentity != with.DIdentity ||
		without.PreconditionUnproven != with.PreconditionUnproven || without.NonCensusedSurvivor != with.NonCensusedSurvivor {
		t.Fatalf("source-native widening changed the decisive Attestation: without=%#v with=%#v", without, with)
	}
	if len(without.Kinds) != len(with.Kinds) {
		t.Fatalf("source-native widening changed Kinds length: without=%#v with=%#v", without.Kinds, with.Kinds)
	}
	for i := range without.Kinds {
		// CensusReadAt is real wall-clock time (time.Now().UTC(), stamped
		// independently by each of the two run() calls above) -- excluded
		// from this comparison deliberately, not because it is allowed to
		// diverge for a reason THIS test cares about, but because it is
		// EXPECTED to diverge between any two calls regardless of the
		// widening, and comparing it here would make this test flaky
		// rather than more precise. Every other field is compared.
		a, b := without.Kinds[i], with.Kinds[i]
		a.CensusReadAt, b.CensusReadAt = time.Time{}, time.Time{}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("source-native widening changed Kinds[%d] content: without=%#v with=%#v", i, without.Kinds[i], with.Kinds[i])
		}
	}
}

// TestBindSourceNativeHandles_ResolvesThroughRealProductionPath is the
// codex xhigh review round-3 finding pinned end-to-end (confirmed and
// fixed, 2026-08-19): every OTHER resolution test in this file builds its
// claimantsByTerm fixture directly as map[string][]IdentityMatch, seeding
// IdentityRow.Aliases/ProviderAliases by hand -- which proved
// resolveSourceNative's own scan logic correct but never exercised
// whether a REAL production AliasLookup result (map[string][]CandidateNode,
// graph nodes with "aliases"/"provider_aliases" ATTRIBUTES, converted via
// claimantsFromCandidateNodes -- resolve.go:1327, the actual
// ShadowEvidenceRoundInput.AliasClaimants call site) would ever produce a
// row resolveSourceNative could find at all. Round 3 found it would NOT
// have (claimantsFromCandidateNodes dropped the alias attributes
// entirely) -- this test starts from a CandidateNode, exactly as
// AliasLookup would return one, and proves the fix closes the gap through
// the SAME conversion function production actually calls.
func TestBindSourceNativeHandles_ResolvesThroughRealProductionPath(t *testing.T) {
	t.Parallel()
	node := CandidateNode{
		Attributes: map[string]interface{}{
			"subject_kind": "repository", "canonical_id": "repository:r-1", "label": "dev-health-acr",
			"aliases":          []string{"dev-health-acr"},
			"provider_aliases": []string{"github:full-chaos/dev-health-acr"},
		},
		Mechanism: contextfabric.MatchAlias,
	}
	// The map key ("some-interpreter-term") is deliberately unrelated to
	// either alias, mirroring how an interpreter's own SubjectTerm would
	// key this exact same claimant in production.
	claimantsByTerm := claimantsFromCandidateNodes(map[string][]CandidateNode{"some-interpreter-term": {node}})

	repoSlugBound := BindSourceNativeHandles("why did full-chaos/dev-health-acr fail to build?", claimantsByTerm, true)
	foundRepoSlug := false
	for _, b := range repoSlugBound {
		if b.Grammar == "repo_slug" && b.Resolved {
			foundRepoSlug = true
		}
	}
	if !foundRepoSlug {
		t.Fatalf("repo_slug did not resolve through the real production conversion path: %#v", repoSlugBound)
	}

	providerBound := BindSourceNativeHandles("see github.com/full-chaos/dev-health-acr for details", claimantsByTerm, true)
	foundProvider := false
	for _, b := range providerBound {
		if b.Grammar == "provider_qualified_name" && b.Resolved {
			foundProvider = true
		}
	}
	if !foundProvider {
		t.Fatalf("provider_qualified_name did not resolve through the real production conversion path: %#v", providerBound)
	}
}
