package hosted_test

import (
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// CHAOS-4348 measurement-layer fix: Run F measured project in-pool 0/20 and
// team in-pool 0/2 despite the ticket's own retrieval fix (PR #299,
// 34785cb9) proven live to work (idx 57/60 forced-trace probes: both
// anchors reached "corroboration" under their live project.v2 canonical
// id). The oracle annex (.remember/trial-results/oracle-annex-v2-ext65.json)
// compares subject ids by EXACT STRING -- its project anchor ids predate
// the CHAOS-3898/3916 identity migration and can never string-match
// identity.Derive's current "project.v2:<provider>:<raw>" output.
//
// chaos4348ExpectedIDSchemePrefix/oracleIDSchemeMismatch below are the
// SINGLE shared source of truth every layer of the fix reads:
// validateTwoTurnOracleAnnex's own load-time refusal (chaos3742_two_turn_confirmation_test.go,
// called unconditionally by loadTwoTurnOracleAnnex, on every real run), the
// red-first fixture test in this file, and poolContainsSubject's own
// telemetry (twoTurnCaseResult.OracleIDSchemeMismatch /
// twoTurnReport.OracleIDSchemeMismatchCount). One rule, checked at three
// layers.

// chaos4348ExpectedIDSchemePrefix returns the canonical-id PREFIX a kind's
// LIVE scheme actually produces today: "<kind>.v2:" for any kind
// identity.Registry has migrated (identity.Derive is the producer -- see
// devhealthsource/teams_projects.go's own call site for project), else the
// pre-migration "<kind>:" scheme repository/team still use unchanged
// (devhealthsource/teams_projects.go: repositoryCanonicalID/
// teamCanonicalID are still plain string concatenation -- see
// identity.Registry's own doc comment, "the five changed kinds", for why
// project is registered there and these two are not).
//
// Deliberately reads identity.Registry rather than hand-listing "project"
// as the one migrated kind: if a future ticket migrates repository or team
// to v2 too, this function's answer updates with it, with no matching
// hand-edit required here.
func chaos4348ExpectedIDSchemePrefix(kind string) string {
	if _, ok := identity.Lookup(kind); ok {
		return kind + ".v2:"
	}
	return kind + ":"
}

// oracleIDSchemeMismatch reports whether canonicalID does NOT carry kind's
// live scheme prefix -- computed directly from the (kind, id) pair the
// oracle annex supplies, independent of whatever poolContainsSubject's own
// pool-membership search finds. This is what makes the mismatch "fail
// loudly instead of silently reading absent" (team-lead GO, 2026-08-26):
// poolContainsSubject(kind, canonicalID) reads false for BOTH "the engine
// genuinely never retrieved this subject" and "the oracle id itself is
// unmatchable garbage" -- this function distinguishes the second case
// regardless of what the pool search does or does not find, so a
// regression in this specific field can never again hide behind an
// otherwise-innocuous "absent" reading the way it did through Run F.
func oracleIDSchemeMismatch(kind, canonicalID string) bool {
	if kind == "" || canonicalID == "" {
		return false
	}
	return !strings.HasPrefix(canonicalID, chaos4348ExpectedIDSchemePrefix(kind))
}

// chaos4348KnownResidualStaleAnchorIDs (CHAOS-4348 ticket comment,
// 2026-08-26): ids explicitly exempted from validateTwoTurnOracleAnnex's
// load-time scheme refusal because they no longer resolve against the
// CURRENT live trial dataset at all -- not merely a scheme problem.
// "project:272efdae-c682-45b6-ae30-e8877eff15f4" (case 46, a repository-
// kind case's negative-only anchor decoy) was confirmed via a forced-trace
// probe of case 46 whose 130+-event trace never once references it under
// ANY canonical id, even though a DIFFERENT live project
// (468890d1-7500-443c-80bb-ce648172e28e) surfaced in the same run -- an
// orphaned fixture from an earlier corpus generation. It does not affect
// the project positive-case bar (case 46's positive kind is repository).
// Tracked as a follow-up in the ticket, not silently ignored: this
// allowlist exists so ONE known, investigated residual does not either (a)
// mask a FUTURE unrelated regression by being lumped in with it, or (b)
// block every real trial run outright until it is resolved or the corpus
// is edited (a decision this fix does not make unilaterally).
var chaos4348KnownResidualStaleAnchorIDs = map[string]bool{
	"project:272efdae-c682-45b6-ae30-e8877eff15f4": true,
}

func TestChaos4348ExpectedIDSchemePrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind string
		want string
	}{
		{"project", "project.v2:"},          // identity.Registry-migrated
		{"work_item", "work_item.v2:"},      // identity.Registry-migrated
		{"deployment", "deployment.v2:"},    // identity.Registry-migrated
		{"repository", "repository:"},       // stable, unmigrated
		{"team", "team:"},                   // stable, unmigrated
		{"nonsense_kind", "nonsense_kind:"}, // unregistered -> stable-scheme default
	}
	for _, tc := range cases {
		if got := chaos4348ExpectedIDSchemePrefix(tc.kind); got != tc.want {
			t.Errorf("chaos4348ExpectedIDSchemePrefix(%q) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestOracleIDSchemeMismatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		kind         string
		canonicalID  string
		wantMismatch bool
	}{
		{"empty kind never mismatches (no expected subject)", "", "project:x", false},
		{"empty id never mismatches (no expected subject)", "project", "", false},
		{"stale pre-v2 project id mismatches", "project", "project:70d529e0-...", true},
		{"live v2 project id matches", "project", "project.v2:gitlab:70d529e0-3c06-4597-8480-794fd02328b6%3Agitlab%3A71133891", false},
		{"stable repository id matches", "repository", "repository:r1", false},
		{"stable team id matches", "team", "team:gh:ops-team", false},
		// A project id wrongly carrying repository's scheme (or vice
		// versa) must ALSO report a mismatch -- this function checks the
		// FULL scheme, not merely "does it have a colon".
		{"cross-kind id mismatches", "project", "repository:r1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := oracleIDSchemeMismatch(tc.kind, tc.canonicalID); got != tc.wantMismatch {
				t.Errorf("oracleIDSchemeMismatch(%q, %q) = %v, want %v", tc.kind, tc.canonicalID, got, tc.wantMismatch)
			}
		})
	}
}

// TestValidateTwoTurnOracleAnnex_RejectsStaleSchemeAnchorID is the
// red-first regression test for the CHAOS-4348 measurement-layer fix's
// real wiring point (codex adversarial review, HIGH, confirmed: a
// standalone test reading the annex file directly would never run inside
// scripts/trial/run-two-turn.sh or run-two-turn-parallel.sh, both of which
// invoke `go test -run TestChaos3742TwoTurnConfirmationReplay` -- ONLY
// validateTwoTurnOracleAnnex itself, called unconditionally by
// loadTwoTurnOracleAnnex on every real load, actually gates a real run).
// A pure fixture, not a read of the local annex file: runs in CI, needs no
// ORACLE_ANNEX-shaped env var, and fails on the pre-fix
// validateTwoTurnOracleAnnex (which had no scheme check at all).
func TestValidateTwoTurnOracleAnnex_RejectsStaleSchemeAnchorID(t *testing.T) {
	t.Parallel()
	t.Run("stale-scheme positive anchor id is refused", func(t *testing.T) {
		annex := twoTurnOracleAnnex{
			CorpusSHA256: "deadbeef",
			Entries: []twoTurnOracleEntry{
				{Index: 57, Member: "subject_anchor", PositiveKind: "project", PositiveAnchorCanonicalID: "project:70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891"},
			},
		}
		if err := validateTwoTurnOracleAnnex(annex); err == nil {
			t.Fatal("validateTwoTurnOracleAnnex() = nil, want an error for a stale pre-v2 project anchor id")
		}
	})
	t.Run("stale-scheme negative anchor id is refused", func(t *testing.T) {
		annex := twoTurnOracleAnnex{
			CorpusSHA256: "deadbeef",
			Entries: []twoTurnOracleEntry{
				{Index: 60, Member: "subject_anchor", NegativeKind: "project", NegativeAnchorCanonicalID: "project:70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891"},
			},
		}
		if err := validateTwoTurnOracleAnnex(annex); err == nil {
			t.Fatal("validateTwoTurnOracleAnnex() = nil, want an error for a stale pre-v2 project anchor negative id")
		}
	})
	t.Run("live v2 project anchor id passes", func(t *testing.T) {
		annex := twoTurnOracleAnnex{
			CorpusSHA256: "deadbeef",
			Entries: []twoTurnOracleEntry{
				{Index: 57, Member: "subject_anchor", PositiveKind: "project", PositiveAnchorCanonicalID: "project.v2:gitlab:70d529e0-3c06-4597-8480-794fd02328b6%3Agitlab%3A71133891"},
			},
		}
		if err := validateTwoTurnOracleAnnex(annex); err != nil {
			t.Fatalf("validateTwoTurnOracleAnnex() = %v, want nil for a live v2 project anchor id", err)
		}
	})
	t.Run("stable repository/team anchor ids pass unchanged", func(t *testing.T) {
		annex := twoTurnOracleAnnex{
			CorpusSHA256: "deadbeef",
			Entries: []twoTurnOracleEntry{
				{Index: 1, Member: "subject_anchor", PositiveKind: "repository", PositiveAnchorCanonicalID: "repository:r1"},
				{Index: 2, Member: "subject_anchor", PositiveKind: "team", PositiveAnchorCanonicalID: "team:gh:ops-team"},
			},
		}
		if err := validateTwoTurnOracleAnnex(annex); err != nil {
			t.Fatalf("validateTwoTurnOracleAnnex() = %v, want nil for stable repository/team ids", err)
		}
	})
	t.Run("the known residual decoy is allowlisted, not silently permissive for others", func(t *testing.T) {
		annex := twoTurnOracleAnnex{
			CorpusSHA256: "deadbeef",
			Entries: []twoTurnOracleEntry{
				{Index: 46, Member: "subject_anchor", NegativeKind: "project", NegativeAnchorCanonicalID: "project:272efdae-c682-45b6-ae30-e8877eff15f4"},
			},
		}
		if err := validateTwoTurnOracleAnnex(annex); err != nil {
			t.Fatalf("validateTwoTurnOracleAnnex() = %v, want nil -- this exact id is the documented known residual", err)
		}
		// A DIFFERENT stale id in the SAME run must still be caught -- the
		// allowlist is a single exact-string exemption, not a blanket
		// pass for the whole annex once one known residual is present.
		annex.Entries = append(annex.Entries, twoTurnOracleEntry{
			Index: 33, Member: "subject_anchor", PositiveKind: "project", PositiveAnchorCanonicalID: "project:c67b1602-31db-4422-8dec-a4a02bbcc513",
		})
		if err := validateTwoTurnOracleAnnex(annex); err == nil {
			t.Fatal("validateTwoTurnOracleAnnex() = nil, want an error for a stale id that is NOT the allowlisted residual")
		}
	})
	t.Run("non-anchor members are never scheme-checked", func(t *testing.T) {
		// expected_kind/window/handle carry kind names, bands, and handle
		// values -- none of them canonical subject ids. A stray colon in
		// one of those must never be misread as a scheme violation.
		annex := twoTurnOracleAnnex{
			CorpusSHA256: "deadbeef",
			Entries: []twoTurnOracleEntry{
				{Index: 1, Member: "expected_kind", PositiveKind: "project"},
				{Index: 1, Member: "window", PositiveWindowBand: "all_time"},
			},
		}
		if err := validateTwoTurnOracleAnnex(annex); err != nil {
			t.Fatalf("validateTwoTurnOracleAnnex() = %v, want nil for non-anchor members", err)
		}
	})
}

// TestChaos4348LiveOracleAnnexLoadsCleanly is an OPTIONAL, local-only
// sanity check against the real on-disk annex, correctly reading the SAME
// env var the trial launchers actually export
// (ACR_TEST_TWOTURN_ORACLE_ANNEX, not the differently-named ORACLE_ANNEX
// an earlier version of this fix mistakenly checked -- codex adversarial
// review, HIGH, confirmed) -- going through loadTwoTurnOracleAnnex itself
// so this test exercises the EXACT same call path a real run does, not a
// hand-rolled JSON parse. Skips when unset: the annex is untracked local
// trial state (.remember/), never present in a CI checkout. The actual
// correctness GUARANTEE for a real run is validateTwoTurnOracleAnnex being
// unconditionally on the load path, proven above; this is a convenience
// check on top, not the guard itself.
func TestChaos4348LiveOracleAnnexLoadsCleanly(t *testing.T) {
	path := os.Getenv("ACR_TEST_TWOTURN_ORACLE_ANNEX")
	if path == "" {
		t.Skip("ACR_TEST_TWOTURN_ORACLE_ANNEX not set -- local-only sanity check against the real annex; scripts/trial/run-two-turn.sh already sets it for a real run, where loadTwoTurnOracleAnnex's own validateTwoTurnOracleAnnex call is the actual guard.")
	}
	loadTwoTurnOracleAnnex(t, path)
}
