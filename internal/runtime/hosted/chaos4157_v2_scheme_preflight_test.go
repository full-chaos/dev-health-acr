package hosted_test

// CHAOS-4157/CHAOS-4161 latent-class fix (2026-08-23): the CHAOS-4100
// rerun-#3 investigation found the two-turn trial corpus's own work_item
// identifiers -- both the corpus's per-case `expect_id` field and the
// oracle annex's `positive_key`/`negatives` anchor fields -- carried a
// STALE pre-v2 plain string ("work_item:<provider>:<external_id>"), while
// the live engine has minted work_item canonical ids as
// "work_item.v2:<repo_id>:<enc(external_id)>" since CHAOS-3898 S2b. The
// mismatch was LATENT: `committedMatchesTrial`/`twoTurnCommittedWrong` do
// exact string equality with zero normalization (by design -- a
// normalizer would permanently mask future scheme drift), so it only
// surfaced as a spurious wrong_commit trip the moment a work_item-anchored
// case actually reached a single committed subject, which happened for
// the first time under CHAOS-4100's graph-lifecycle epoch-2 wiring
// (cases 28/54). Both fixture files were regenerated at the producer
// (identity.Derive, never hand-typed) and are corpus-safe untracked
// artifacts outside this repo -- this preflight is the code-side guard
// that keeps the class from recurring silently.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// twoTurnV2SchemeViolation names ONE mismatch by kind and case index only
// -- never the identifier value itself, and never any corpus
// question/term text -- corpus-safe by construction, matching every other
// trial-harness diagnostic in this package.
type twoTurnV2SchemeViolation struct {
	Source string // "annex_positive" | "annex_negative" | "corpus_expect_id"
	Index  int
}

// twoTurnFindWorkItemV2SchemeViolations is the pure check both the real
// preflight and its own unit test share: every work_item identifier
// either fixture carries must parse under the live v2 canonical scheme
// (identity.Segments, the exact inverse of the minting path in
// devhealthsource/tables.go's queryWorkItems). Returns every violation
// found (never stops at the first) so a single bad regeneration surfaces
// its full blast radius in one run, not one Fatalf at a time.
func twoTurnFindWorkItemV2SchemeViolations(annex twoTurnOracleAnnex, corpus []trialCase) []twoTurnV2SchemeViolation {
	var violations []twoTurnV2SchemeViolation
	for _, entry := range annex.Entries {
		if entry.PositiveKind == identity.KindWorkItem && entry.PositiveAnchorCanonicalID != "" {
			if _, ok := identity.Segments(identity.KindWorkItem, entry.PositiveAnchorCanonicalID); !ok {
				violations = append(violations, twoTurnV2SchemeViolation{Source: "annex_positive", Index: entry.Index})
			}
		}
		if entry.NegativeKind == identity.KindWorkItem && entry.NegativeAnchorCanonicalID != "" {
			if _, ok := identity.Segments(identity.KindWorkItem, entry.NegativeAnchorCanonicalID); !ok {
				violations = append(violations, twoTurnV2SchemeViolation{Source: "annex_negative", Index: entry.Index})
			}
		}
	}
	for i, tc := range corpus {
		if tc.ExpectKind == identity.KindWorkItem && tc.ExpectID != "" {
			if _, ok := identity.Segments(identity.KindWorkItem, tc.ExpectID); !ok {
				violations = append(violations, twoTurnV2SchemeViolation{Source: "corpus_expect_id", Index: i})
			}
		}
	}
	return violations
}

// twoTurnValidateWorkItemV2Scheme is the real preflight call site: fails
// closed, naming every offending case index, before any live measurement
// work begins.
//
// Scoped to a dedicated call site (TestChaos3742TwoTurnConfirmationReplay,
// right after its own annex/corpus loads) rather than folded into
// loadTwoTurnOracleAnnex/loadTrialCorpus themselves: those two functions
// are shared by unit tests elsewhere in this package that construct
// synthetic work_item fixtures in the pre-v2 plain form on purpose (they
// are testing the ADAPTATION shape, not canonical-id validity) -- baking
// this check into the shared loaders would break those unrelated tests.
// A real trial run is the only caller that needs the guard, so it is the
// only caller that pays for it.
func twoTurnValidateWorkItemV2Scheme(t interface{ Fatalf(string, ...any) }, annex twoTurnOracleAnnex, corpus []trialCase) {
	violations := twoTurnFindWorkItemV2SchemeViolations(annex, corpus)
	if len(violations) == 0 {
		return
	}
	bySource := map[string][]int{}
	for _, v := range violations {
		bySource[v.Source] = append(bySource[v.Source], v.Index)
	}
	t.Fatalf("work_item identifiers do not parse under the live v2 canonical scheme (stale pre-v2 format? see CHAOS-4157) -- annex_positive indices=%v annex_negative indices=%v corpus_expect_id indices=%v",
		bySource["annex_positive"], bySource["annex_negative"], bySource["corpus_expect_id"])
}

// TestTwoTurnFindWorkItemV2SchemeViolations exercises the check itself
// against deliberately-crafted SYNTHETIC fixtures (never real corpus
// content -- "repo-1"/"WIDGET-101" mirror registry_test.go's own synthetic
// precedent for identity.Derive): a valid v2-scheme id must never flag, a
// stale pre-v2 plain string must always flag (by source and index), and a
// non-work_item kind carrying the identical stale shape must be ignored --
// the check is kind-scoped, not string-shaped.
func TestTwoTurnFindWorkItemV2SchemeViolations(t *testing.T) {
	t.Parallel()
	validV2, omitted, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "WIDGET-101"}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive(work_item, repo-1, WIDGET-101) = (%q, %v, %v), want a valid id", validV2, omitted, err)
	}
	const stalePlain = "work_item:synthetic:WIDGET-101"

	annex := twoTurnOracleAnnex{Entries: []twoTurnOracleEntry{
		{Index: 0, PositiveKind: identity.KindWorkItem, PositiveAnchorCanonicalID: validV2},
		{Index: 1, PositiveKind: identity.KindWorkItem, PositiveAnchorCanonicalID: stalePlain},
		{Index: 2, NegativeKind: identity.KindWorkItem, NegativeAnchorCanonicalID: stalePlain},
		// A non-work_item kind carrying the SAME stale shape must never
		// flag -- the check is scoped by kind, not by string shape.
		{Index: 3, PositiveKind: "repository", PositiveAnchorCanonicalID: stalePlain},
		// An empty id is "no positive/negative for this member" (existing
		// annex convention, e.g. an existence_probe or ambiguity-band
		// case) -- never a violation.
		{Index: 4, PositiveKind: identity.KindWorkItem, PositiveAnchorCanonicalID: ""},
	}}
	corpus := []trialCase{
		{ExpectKind: identity.KindWorkItem, ExpectID: validV2},    // index 0: clean
		{ExpectKind: identity.KindWorkItem, ExpectID: stalePlain}, // index 1: stale
		{ExpectKind: "repository", ExpectID: stalePlain},          // index 2: wrong kind, ignored
	}

	got := twoTurnFindWorkItemV2SchemeViolations(annex, corpus)
	want := map[twoTurnV2SchemeViolation]bool{
		{Source: "annex_positive", Index: 1}:   true,
		{Source: "annex_negative", Index: 2}:   true,
		{Source: "corpus_expect_id", Index: 1}: true,
	}
	if len(got) != len(want) {
		t.Fatalf("violations = %#v, want exactly %d entries matching %#v", got, len(want), want)
	}
	for _, v := range got {
		if !want[v] {
			t.Errorf("unexpected violation %#v", v)
		}
	}
}

// TestTwoTurnValidateWorkItemV2SchemeFailsClosed proves the real preflight
// call site actually calls Fatalf (via a minimal fake, since *testing.T
// itself cannot be driven to Fatalf and inspected in-process) when a
// violation is present, and stays silent when the fixtures are clean.
func TestTwoTurnValidateWorkItemV2SchemeFailsClosed(t *testing.T) {
	t.Parallel()
	validV2, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "WIDGET-101"}, nil)
	if err != nil {
		t.Fatalf("identity.Derive: %v", err)
	}

	clean := twoTurnOracleAnnex{Entries: []twoTurnOracleEntry{
		{Index: 0, PositiveKind: identity.KindWorkItem, PositiveAnchorCanonicalID: validV2},
	}}
	fake := &fatalfSpy{}
	twoTurnValidateWorkItemV2Scheme(fake, clean, nil)
	if fake.called {
		t.Errorf("Fatalf called on a clean fixture set: %q", fake.message)
	}

	stale := twoTurnOracleAnnex{Entries: []twoTurnOracleEntry{
		{Index: 0, PositiveKind: identity.KindWorkItem, PositiveAnchorCanonicalID: "work_item:synthetic:WIDGET-101"},
	}}
	fake = &fatalfSpy{}
	twoTurnValidateWorkItemV2Scheme(fake, stale, nil)
	if !fake.called {
		t.Fatal("Fatalf never called on a stale fixture set")
	}
}

// fatalfSpy satisfies twoTurnValidateWorkItemV2Scheme's own minimal
// interface without pulling in a real *testing.T (which cannot be driven
// to Fatalf and then inspected within the same test process).
type fatalfSpy struct {
	called  bool
	message string
}

func (f *fatalfSpy) Fatalf(format string, args ...any) {
	f.called = true
	f.message = format
}
