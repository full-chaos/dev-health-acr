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
// that keeps the class from recurring silently. The corpus half
// (validateCorpusWorkItemV2Scheme) is wired into loadTrialCorpus itself
// (generative_trial_live_test.go), so it guards every live corpus
// consumer in this package -- replay, D2B, W0, generative, frontier, and
// two-turn -- not only the two-turn confirmation replay this incident was
// first found in.

import (
	"strings"
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

// identityRoundTripsCleanly reports whether id is EXACTLY what
// identity.Derive would mint for kind from id's own decoded segments --
// not merely that it carries the right prefix and segment count.
//
// identity.Segments alone (codex sol/high review, P2) only checks the
// "<kind>.v2:" prefix and segment count; DecodeSegment never rejects
// malformed percent-escaping (e.g. a literal "%ZZ"), so a fixture that
// merely LOOKS like a v2 id can still be one Derive itself could never
// have produced -- a real committed subject's id would still fail exact
// comparison against it. Re-deriving from the decoded segments and
// requiring byte-for-byte equality closes that gap: only a value the
// producer function ITSELF could emit ever passes.
func identityRoundTripsCleanly(kind, id string) bool {
	values, ok := identity.Segments(kind, id)
	if !ok {
		return false
	}
	rederived, omitted, err := identity.Derive(kind, values, nil)
	if err != nil || omitted {
		return false
	}
	return rederived == id
}

// isWorkItemAnchorCandidate reports whether an annex anchor value should be
// validated as a work_item id.
//
// Checks the RAW id string's own "work_item.v2:" prefix, not (only)
// entry.PositiveKind/NegativeKind (codex sol/high review round 2, MEDIUM):
// a MALFORMED v2-looking id with the wrong segment count -- e.g.
// "work_item.v2:repo-1", missing its second segment -- makes
// identity.Segments fail for every registered kind, so splitAnchorKey
// falls back to its legacy single-colon split and returns the WRONG kind
// ("work_item.v2", not "work_item") for that one malformed entry. Gating
// only on kind=="work_item" would let exactly the malformed ids this
// preflight exists to catch evade it by breaking the very parser that
// would have classified them as work_item in the first place. The raw
// prefix check is independent of that parse, so a malformed id can never
// dodge this preflight by corrupting its own kind classification.
func isWorkItemAnchorCandidate(kind, canonicalID string) bool {
	if canonicalID == "" {
		return false
	}
	return kind == identity.KindWorkItem || strings.HasPrefix(canonicalID, identity.KindWorkItem+".v2:")
}

// twoTurnFindWorkItemV2SchemeViolations is the pure check both the real
// preflight and its own unit test share: every work_item identifier
// either fixture carries must round-trip cleanly through the live v2
// canonical scheme (identityRoundTripsCleanly, the exact inverse of the
// minting path in devhealthsource/tables.go's queryWorkItems). Returns
// every violation found (never stops at the first) so a single bad
// regeneration surfaces its full blast radius in one run, not one Fatalf
// at a time.
func twoTurnFindWorkItemV2SchemeViolations(annex twoTurnOracleAnnex, corpus []trialCase) []twoTurnV2SchemeViolation {
	var violations []twoTurnV2SchemeViolation
	for _, entry := range annex.Entries {
		if isWorkItemAnchorCandidate(entry.PositiveKind, entry.PositiveAnchorCanonicalID) {
			if !identityRoundTripsCleanly(identity.KindWorkItem, entry.PositiveAnchorCanonicalID) {
				violations = append(violations, twoTurnV2SchemeViolation{Source: "annex_positive", Index: entry.Index})
			}
		}
		if isWorkItemAnchorCandidate(entry.NegativeKind, entry.NegativeAnchorCanonicalID) {
			if !identityRoundTripsCleanly(identity.KindWorkItem, entry.NegativeAnchorCanonicalID) {
				violations = append(violations, twoTurnV2SchemeViolation{Source: "annex_negative", Index: entry.Index})
			}
		}
	}
	violations = append(violations, corpusFindWorkItemV2SchemeViolations(corpus, "corpus_expect_id")...)
	return violations
}

// corpusFindWorkItemV2SchemeViolations is the harness-agnostic half of the
// check (codex sol/high review, P2): every LIVE corpus consumer in this
// package -- not just the two-turn test -- loads the same withheld corpus
// via loadTrialCorpus and classifies results through exact
// trialCase.ExpectID equality (chaos3884_replay_harness_test.go,
// chaos3899_d2b_cardinality_test.go, chaos3900_w0_window_shadow_test.go,
// generative_trial_live_test.go, frontier_trial_live_test.go), so a stale
// pre-v2 work_item expect_id can produce spurious "correct" evidence in
// any of them, not only the two-turn confirmation replay. `source` names
// the caller in the returned violations for a corpus-safe diagnostic
// (case index only, never the identifier or corpus text).
func corpusFindWorkItemV2SchemeViolations(corpus []trialCase, source string) []twoTurnV2SchemeViolation {
	var violations []twoTurnV2SchemeViolation
	for i, tc := range corpus {
		if tc.ExpectKind == identity.KindWorkItem && tc.ExpectID != "" {
			if !identityRoundTripsCleanly(identity.KindWorkItem, tc.ExpectID) {
				violations = append(violations, twoTurnV2SchemeViolation{Source: source, Index: i})
			}
		}
	}
	return violations
}

// validateCorpusWorkItemV2Scheme is the call site every OTHER live corpus
// consumer in this package uses (the two-turn confirmation replay uses
// twoTurnValidateWorkItemV2Scheme below instead, which also covers its
// own annex): fails closed, naming every offending case index, before any
// live measurement work begins.
func validateCorpusWorkItemV2Scheme(t interface{ Fatalf(string, ...any) }, corpus []trialCase) {
	violations := corpusFindWorkItemV2SchemeViolations(corpus, "corpus_expect_id")
	if len(violations) == 0 {
		return
	}
	indices := make([]int, len(violations))
	for i, v := range violations {
		indices[i] = v.Index
	}
	t.Fatalf("trial corpus work_item identifiers do not round-trip through the live v2 canonical scheme (stale pre-v2 format? see CHAOS-4157) -- indices=%v", indices)
}

// twoTurnValidateWorkItemV2Scheme is the two-turn test's own preflight
// call site: fails closed, naming every offending case index, before any
// live measurement work begins.
//
// The corpus half this also checks is REDUNDANT with loadTrialCorpus's own
// internal call to validateCorpusWorkItemV2Scheme (below) -- harmless
// (a few dozen cases, checked twice), kept here so this call site alone
// still documents "both fixtures this test loads are validated" without a
// reader having to know loadTrialCorpus's own internals. The annex half is
// NOT folded into loadTwoTurnOracleAnnex/adaptSignedOracleAnnex the same
// way loadTrialCorpus was: those two functions are shared by unit tests
// elsewhere in this package that construct synthetic work_item fixtures in
// the pre-v2 plain form on purpose (testing the ADAPTATION shape, not
// canonical-id validity) -- baking this check into them would break those
// unrelated tests.
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
	// malformedV2 has the right prefix and segment COUNT (identity.Segments
	// alone would accept it) but DecodeSegment's own never-errors contract
	// means a bad percent-escape decodes silently -- re-deriving from
	// those decoded segments never re-produces this exact string (it would
	// re-encode "%ZZ" to "%25ZZ"). No real committed subject's id can ever
	// equal it, so the round-trip check must still flag it.
	const malformedV2 = "work_item.v2:repo-1:%ZZ"
	// wrongSegmentCountV2 has the RIGHT prefix but the WRONG segment count
	// (missing its second segment) -- identity.Segments fails for every
	// registered kind, so splitAnchorKey's own fallback would classify
	// this entry's PositiveKind as "work_item.v2" (codex sol/high review
	// round 2, MEDIUM), not "work_item". PositiveKind is set to match
	// exactly that real fallback output here, proving the check still
	// catches it via the raw id's own prefix rather than the (here,
	// corrupted) parsed kind.
	const wrongSegmentCountV2 = "work_item.v2:repo-1"

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
		{Index: 5, PositiveKind: identity.KindWorkItem, PositiveAnchorCanonicalID: malformedV2},
		{Index: 6, PositiveKind: "work_item.v2", PositiveAnchorCanonicalID: wrongSegmentCountV2},
	}}
	corpus := []trialCase{
		{ExpectKind: identity.KindWorkItem, ExpectID: validV2},     // index 0: clean
		{ExpectKind: identity.KindWorkItem, ExpectID: stalePlain},  // index 1: stale
		{ExpectKind: "repository", ExpectID: stalePlain},           // index 2: wrong kind, ignored
		{ExpectKind: identity.KindWorkItem, ExpectID: malformedV2}, // index 3: malformed escape
	}

	got := twoTurnFindWorkItemV2SchemeViolations(annex, corpus)
	want := map[twoTurnV2SchemeViolation]bool{
		{Source: "annex_positive", Index: 1}:   true,
		{Source: "annex_negative", Index: 2}:   true,
		{Source: "annex_positive", Index: 5}:   true,
		{Source: "annex_positive", Index: 6}:   true,
		{Source: "corpus_expect_id", Index: 1}: true,
		{Source: "corpus_expect_id", Index: 3}: true,
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

// TestCorpusFindWorkItemV2SchemeViolations exercises the harness-agnostic
// corpus-only half directly (the shape every OTHER live corpus consumer
// in this package calls, not just the two-turn test).
func TestCorpusFindWorkItemV2SchemeViolations(t *testing.T) {
	t.Parallel()
	validV2, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "WIDGET-101"}, nil)
	if err != nil {
		t.Fatalf("identity.Derive: %v", err)
	}
	corpus := []trialCase{
		{ExpectKind: identity.KindWorkItem, ExpectID: validV2},
		{ExpectKind: identity.KindWorkItem, ExpectID: "work_item:synthetic:WIDGET-101"},
		{ExpectKind: "repository", ExpectID: "work_item:synthetic:WIDGET-101"},
	}
	got := corpusFindWorkItemV2SchemeViolations(corpus, "replay_expect_id")
	if len(got) != 1 || got[0] != (twoTurnV2SchemeViolation{Source: "replay_expect_id", Index: 1}) {
		t.Fatalf("violations = %#v, want exactly one {replay_expect_id, 1}", got)
	}
}

func TestValidateCorpusWorkItemV2SchemeFailsClosed(t *testing.T) {
	t.Parallel()
	validV2, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "WIDGET-101"}, nil)
	if err != nil {
		t.Fatalf("identity.Derive: %v", err)
	}

	fake := &fatalfSpy{}
	validateCorpusWorkItemV2Scheme(fake, []trialCase{{ExpectKind: identity.KindWorkItem, ExpectID: validV2}})
	if fake.called {
		t.Errorf("Fatalf called on a clean corpus: %q", fake.message)
	}

	fake = &fatalfSpy{}
	validateCorpusWorkItemV2Scheme(fake, []trialCase{{ExpectKind: identity.KindWorkItem, ExpectID: "work_item:synthetic:WIDGET-101"}})
	if !fake.called {
		t.Fatal("Fatalf never called on a stale corpus")
	}
}

// TestSplitAnchorKeyV2Scheme pins the codex sol/high review's P1 fix: a
// v2-scheme canonical id must resolve to its TRUE kind ("work_item"), not
// the whole "work_item.v2" prefix a naive first-colon split used to
// return -- see splitAnchorKey's own doc comment for the incident.
func TestSplitAnchorKeyV2Scheme(t *testing.T) {
	t.Parallel()
	validV2, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "WIDGET-101"}, nil)
	if err != nil {
		t.Fatalf("identity.Derive: %v", err)
	}

	cases := []struct {
		name     string
		key      string
		wantKind string
		wantID   string
		wantOK   bool
	}{
		{"v2 work_item", validV2, identity.KindWorkItem, validV2, true},
		{"legacy plain kind", "repository:7b9583ee-4d24-2be7-4d09-34f815bebdd7", "repository", "7b9583ee-4d24-2be7-4d09-34f815bebdd7", true},
		{"empty", "", "", "", false},
		{"no colon", "malformed", "", "", false},
		// Documents the known residual (isWorkItemAnchorCandidate's own
		// doc comment): a v2-prefixed id with the WRONG segment count
		// still falls through to the legacy split and returns the WRONG
		// kind ("work_item.v2", not "work_item") -- the preflight catches
		// this class via the raw prefix instead of trusting this return
		// value, precisely because this fallback cannot be trusted for a
		// malformed v2-looking key.
		{"malformed v2 wrong segment count", "work_item.v2:repo-1", "work_item.v2", "repo-1", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, canonicalID, ok := splitAnchorKey(c.key)
			if kind != c.wantKind || canonicalID != c.wantID || ok != c.wantOK {
				t.Errorf("splitAnchorKey(%q) = (%q, %q, %v), want (%q, %q, %v)", c.key, kind, canonicalID, ok, c.wantKind, c.wantID, c.wantOK)
			}
		})
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
