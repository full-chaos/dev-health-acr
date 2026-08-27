package hosted_test

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
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
// SINGLE shared source of truth both halves of the fix read: the red-first
// annex validator test in this file, and poolContainsSubject's own
// telemetry counter (chaos3742_two_turn_confirmation_test.go's
// OracleIDSchemeMismatch field / OracleIDSchemeMismatchCount report
// aggregate). One rule, checked twice, at two different layers.

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

// chaos4348AnnexFile is the minimal shape TestChaos4348OracleAnnexAnchorIDsMatchLiveIdentityScheme
// needs out of the oracle annex -- only the anchor member's own fields
// (positive_key, negatives) and committable_negative_designations entries
// where member=="anchor" carry canonical subject ids; every other member
// (window, handle, kind) carries a DIFFERENT vocabulary (bands, handle
// strings, kind names) that this validator must never touch.
type chaos4348AnnexFile struct {
	Cases map[string]struct {
		Oracles struct {
			Anchor struct {
				PositiveKey *string  `json:"positive_key"`
				Negatives   []string `json:"negatives"`
			} `json:"anchor"`
		} `json:"oracles"`
		CommittableNegativeDesignations []struct {
			Member string `json:"member"`
			Value  string `json:"value"`
		} `json:"committable_negative_designations"`
	} `json:"cases"`
}

// TestChaos4348OracleAnnexAnchorIDsMatchLiveIdentityScheme is the red-first
// annex validation test the CHAOS-4348 measurement-layer fix GO required:
// it fails on main (today, before the annex regeneration this ticket
// shipped) because the annex's project anchor ids predate the identity.v2
// migration, and passes once acr-annex-regen-project-ids has regenerated
// them.
//
// LOCAL-ONLY, never CI-gated, by necessity, not by choice: the annex lives
// at .remember/trial-results/ -- untracked local trial state (this
// directory is not part of any repo's git history; the "dev-health"
// directory containing it is not even a git repository), so a CI checkout
// never has the file this test reads. Every other trial-harness affordance
// in this file (ACR_TEST_TRIAL_FORCE_TRACE_INDICES, ACR_TRIAL_DATA_PLANE)
// is the same kind of local-only-by-necessity signal; this one follows the
// same skip-if-absent discipline rather than inventing a new one.
func TestChaos4348OracleAnnexAnchorIDsMatchLiveIdentityScheme(t *testing.T) {
	path := os.Getenv("ORACLE_ANNEX")
	if path == "" {
		t.Skip("ORACLE_ANNEX not set -- this is a local-only annex correctness probe (CHAOS-4348 measurement-layer fix), never CI-gated: the annex is untracked local trial state (.remember/), not a repo artifact. Set ORACLE_ANNEX to the annex path to run it (scripts/trial/run-two-turn.sh already does).")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read oracle annex at %s: %v", path, err)
	}
	var annex chaos4348AnnexFile
	if err := json.Unmarshal(raw, &annex); err != nil {
		t.Fatalf("parse oracle annex at %s: %v", path, err)
	}

	// chaos4348KnownResidualStaleIDs (CHAOS-4348 ticket comment,
	// 2026-08-26): ids left deliberately unregenerated because they no
	// longer resolve against the CURRENT live trial dataset at all -- not
	// merely a scheme problem. Confirmed by a forced-trace probe (case 46,
	// idx 46) whose 130+-event trace never once references
	// "272efdae-c682-45b6-ae30-e8877eff15f4" under ANY canonical id, even
	// though a DIFFERENT live project (468890d1-...) surfaced in the same
	// run -- this raw id is an orphaned fixture from an earlier corpus
	// generation, not a live-but-unmatched scheme case. It is a negative-
	// only anchor decoy in case 46 (repository-kind positive; project
	// appears only as a committable_negative_designations "seeded_result"
	// entry), so it does not affect the project positive-case bar. Tracked
	// as a follow-up in the CHAOS-4348 ticket, not silently ignored here:
	// this allowlist exists so ONE known, investigated residual does not
	// mask a FUTURE unrelated regression elsewhere in the annex.
	knownResidualStaleIDs := map[string]bool{
		"project:272efdae-c682-45b6-ae30-e8877eff15f4": true,
	}

	type violation struct {
		caseIndex string
		field     string
		value     string
	}
	var violations []violation
	check := func(caseIndex, field, value string) {
		if value == "" || knownResidualStaleIDs[value] {
			return
		}
		colon := strings.IndexByte(value, ':')
		if colon < 0 {
			violations = append(violations, violation{caseIndex, field, value})
			return
		}
		kindGuess := strings.TrimSuffix(value[:colon], ".v2")
		if !strings.HasPrefix(value, chaos4348ExpectedIDSchemePrefix(kindGuess)) {
			violations = append(violations, violation{caseIndex, field, value})
		}
	}

	for _, caseIndex := range sortedAnnexCaseKeys(annex) {
		c := annex.Cases[caseIndex]
		if c.Oracles.Anchor.PositiveKey != nil {
			check(caseIndex, "oracles.anchor.positive_key", *c.Oracles.Anchor.PositiveKey)
		}
		for _, negative := range c.Oracles.Anchor.Negatives {
			check(caseIndex, "oracles.anchor.negatives[]", negative)
		}
		for _, designation := range c.CommittableNegativeDesignations {
			if designation.Member == "anchor" {
				check(caseIndex, "committable_negative_designations[].value", designation.Value)
			}
		}
	}

	if len(violations) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "oracle annex %s has %d anchor id(s) that do not match their kind's live identity scheme:\n", path, len(violations))
		for _, v := range violations {
			fmt.Fprintf(&b, "  case %s %s: %q\n", v.caseIndex, v.field, v.value)
		}
		b.WriteString("run cmd/acr-annex-regen-project-ids to regenerate stale project ids (CHAOS-4348)")
		t.Error(b.String())
	}
}

func sortedAnnexCaseKeys(annex chaos4348AnnexFile) []string {
	keys := make([]string, 0, len(annex.Cases))
	for k := range annex.Cases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
