package hosted_test

// CHAOS-4348 corpus/annex sync (2026-08-27): Run G found that the two-turn
// trial corpus (.remember/acr-3778-corpus-ext65.json, per-case `expect_id`/
// `expect_kind`) and the oracle annex (per-case `oracles.anchor.positive_key`/
// `oracles.kind.positive`) are TWO SEPARATE, REDUNDANT copies of the same
// information -- `trialCase.ExpectID`/`ExpectKind` (what poolContainsSubject/
// oracleIDSchemeMismatch/committedMatchesTrial actually check against) comes
// from the CORPUS, never from the annex, so regenerating the annex ALONE
// (CHAOS-4348's earlier PR #301) left the corpus's own stale copy untouched
// for cases 57/60 -- and separately exposed a genuine, pre-existing content
// disagreement at case 45 (corpus said expect_kind=project with an id the
// annex never references at all; the chris-signed annex says expect_kind=
// repository, no positive anchor -- an ambiguity/existence-probe case about
// a multi-claimant repository twin). Nothing before this checked that the
// two files agree; CHAOS-4157's twoTurnValidateWorkItemV2Scheme checks each
// file's OWN internal scheme correctness, never cross-file agreement.
//
// This file adds that missing check: red-first, fails closed, before any
// live measurement work begins -- generalizing CHAOS-4157's own
// established wiring pattern (twoTurnFindWorkItemV2SchemeViolations /
// twoTurnValidateWorkItemV2Scheme) to cross-file kind+id agreement instead
// of within-file scheme correctness. Team-lead ruling (2026-08-27): the
// annex is authoritative (human-annotated, chris-signed) wherever the two
// disagree -- this check flags disagreement, it does not resolve it in
// either file's favor; cmd/acr-corpus-annex-sync (new tool, same
// generator-not-hand-edits discipline as cmd/acr-annex-regen-project-ids)
// corrects the corpus to match.

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// twoTurnCorpusAnnexDisagreement names ONE case index where the corpus and
// annex disagree, and WHAT disagreed (kind, id, or both) -- never the
// identifier value or corpus question text itself, matching CHAOS-4157's
// own corpus-safe diagnostic convention.
type twoTurnCorpusAnnexDisagreement struct {
	Index      int
	KindDiffer bool
	IDDiffer   bool
	// CaseMissing (codex adversarial review, HIGH, confirmed) marks an
	// index the annex has NO entry for at all -- any member, not just
	// subject_anchor. Without this, an annex case that vanishes entirely
	// (a bad edit, a bad merge) is INDISTINGUISHABLE from one that
	// legitimately has no positive kind/id for a member: both read as the
	// zero value from the lookup maps, so a corpus entry that ALSO
	// happens to carry empty expect_kind/expect_id (a genuine
	// existence_probe/ambiguity case, e.g. today's cases 31/35/37) would
	// silently agree with a MISSING annex case, shrinking the harness's
	// own worklist (built from annex.Entries) with no preflight failure
	// to catch it.
	CaseMissing bool
}

// twoTurnAnnexExpectedKindByIndex/twoTurnAnnexPositiveAnchorIDByIndex read
// the two annex facts the corpus's own expect_kind/expect_id fields must
// agree with, straight off the SAME adapted twoTurnOracleEntry list every
// other annex consumer in this package already reads -- never a second,
// independent parse of the signed artifact.
func twoTurnAnnexExpectedKindByIndex(annex twoTurnOracleAnnex) map[int]string {
	byIndex := make(map[int]string, len(annex.Entries))
	for _, e := range annex.Entries {
		if e.Member == string(contractsv1.ContextFabricStructureNeedExpectedKind) {
			byIndex[e.Index] = e.PositiveKind
		}
	}
	return byIndex
}

func twoTurnAnnexPositiveAnchorIDByIndex(annex twoTurnOracleAnnex) (byIndex map[int]string, hasEntry map[int]bool) {
	byIndex = make(map[int]string, len(annex.Entries))
	hasEntry = make(map[int]bool, len(annex.Entries))
	for _, e := range annex.Entries {
		if e.Member == string(contractsv1.ContextFabricStructureNeedSubjectAnchor) {
			byIndex[e.Index] = e.PositiveAnchorCanonicalID
			hasEntry[e.Index] = true
		}
	}
	return byIndex, hasEntry
}

// twoTurnFindCorpusAnnexAgreementViolations is the pure check both the real
// preflight and its own unit test share. For every corpus case:
//   - expect_kind must equal the annex's expected_kind member's PositiveKind
//     for that index (both empty -- no positive kind either side -- agrees).
//   - expect_id must equal the annex's subject_anchor member's
//     PositiveAnchorCanonicalID for that index. A case with NO subject_anchor
//     entry at all in the annex is treated the same as one WITH an entry
//     whose PositiveAnchorCanonicalID is empty (an existence_probe/ambiguity
//     case has no true positive either way) -- both require corpus's own
//     expect_id to be empty too.
//
// Deliberately does not compare corpus's `question`/`subject_terms` (the
// annex carries no equivalent field to compare against, and this check's
// whole purpose is kind/id agreement, never a content judgment about
// question phrasing).
func twoTurnFindCorpusAnnexAgreementViolations(annex twoTurnOracleAnnex, corpus []trialCase) []twoTurnCorpusAnnexDisagreement {
	annexKind := twoTurnAnnexExpectedKindByIndex(annex)
	annexAnchorID, _ := twoTurnAnnexPositiveAnchorIDByIndex(annex)

	// annexHasCase is topology, not value agreement: TRUE iff the annex
	// carries at least one entry (any member) for this index at all.
	// Deliberately separate from annexKind/annexAnchorID's own presence
	// maps -- a case can legitimately have an expected_kind entry but no
	// subject_anchor entry (or vice versa); what must never happen
	// silently is the annex knowing NOTHING about an index the corpus
	// still carries a row for.
	annexHasCase := make(map[int]bool, len(annex.Entries))
	for _, e := range annex.Entries {
		annexHasCase[e.Index] = true
	}

	var violations []twoTurnCorpusAnnexDisagreement
	for i, tc := range corpus {
		if !annexHasCase[i] {
			violations = append(violations, twoTurnCorpusAnnexDisagreement{Index: i, CaseMissing: true})
			continue
		}
		kindDiffer := tc.ExpectKind != annexKind[i]
		idDiffer := tc.ExpectID != annexAnchorID[i]
		if kindDiffer || idDiffer {
			violations = append(violations, twoTurnCorpusAnnexDisagreement{Index: i, KindDiffer: kindDiffer, IDDiffer: idDiffer})
		}
	}
	return violations
}

// twoTurnValidateCorpusAnnexAgreement is the two-turn test's own preflight
// call site (wired beside twoTurnValidateWorkItemV2Scheme): fails closed,
// naming every offending case index and what disagreed, before any live
// measurement work begins.
func twoTurnValidateCorpusAnnexAgreement(t interface{ Fatalf(string, ...any) }, annex twoTurnOracleAnnex, corpus []trialCase) {
	violations := twoTurnFindCorpusAnnexAgreementViolations(annex, corpus)
	if len(violations) == 0 {
		return
	}
	var kindIndices, idIndices, missingIndices []int
	for _, v := range violations {
		if v.CaseMissing {
			missingIndices = append(missingIndices, v.Index)
			continue
		}
		if v.KindDiffer {
			kindIndices = append(kindIndices, v.Index)
		}
		if v.IDDiffer {
			idIndices = append(idIndices, v.Index)
		}
	}
	t.Fatalf("trial corpus and oracle annex disagree on expected kind/id (see CHAOS-4348) -- kind_disagrees=%v id_disagrees=%v annex_case_missing=%v -- run cmd/acr-corpus-annex-sync", kindIndices, idIndices, missingIndices)
}

// TestTwoTurnFindCorpusAnnexAgreementViolations exercises the check against
// synthetic fixtures shaped after the real Run G findings (never real
// corpus content): a case where both files agree, a case where only the id
// differs (the case-57/60 shape -- corpus stale, annex regenerated), a case
// where BOTH kind and id differ (the case-45 shape -- corpus says a kind
// the annex never claims for that index), and a case with no positive on
// either side (the existence_probe/ambiguity shape) that must never flag.
func TestTwoTurnFindCorpusAnnexAgreementViolations(t *testing.T) {
	t.Parallel()
	annex := twoTurnOracleAnnex{Entries: []twoTurnOracleEntry{
		// index 0: agrees on both kind and id.
		{Index: 0, Member: string(contractsv1.ContextFabricStructureNeedExpectedKind), PositiveKind: "project"},
		{Index: 0, Member: string(contractsv1.ContextFabricStructureNeedSubjectAnchor), PositiveKind: "project", PositiveAnchorCanonicalID: "project.v2:gitlab:abc"},
		// index 1: annex kind agrees, but annex's anchor id was
		// regenerated (v2) while corpus still carries the stale id
		// (case 57/60 shape) -- id disagreement only.
		{Index: 1, Member: string(contractsv1.ContextFabricStructureNeedExpectedKind), PositiveKind: "project"},
		{Index: 1, Member: string(contractsv1.ContextFabricStructureNeedSubjectAnchor), PositiveKind: "project", PositiveAnchorCanonicalID: "project.v2:linear:def"},
		// index 2: annex says repository/no-positive; corpus (below)
		// says project/some-id -- the case-45 shape, both kind and id
		// disagree.
		{Index: 2, Member: string(contractsv1.ContextFabricStructureNeedExpectedKind), PositiveKind: "repository"},
		{Index: 2, Member: string(contractsv1.ContextFabricStructureNeedSubjectAnchor), PositiveKind: "repository", PositiveAnchorCanonicalID: ""},
		// index 3: no subject_anchor entry in the annex AT ALL (a pure
		// kind-only case) -- corpus (below) also carries no expect_id,
		// so this must NOT flag despite the annex having zero anchor
		// entries for this index.
		{Index: 3, Member: string(contractsv1.ContextFabricStructureNeedExpectedKind), PositiveKind: "work_item"},
		// index 4 has NO entries at all -- deliberately absent from the
		// annex entirely (codex adversarial review, HIGH, confirmed): the
		// corpus row below ALSO carries empty expect_kind/expect_id, the
		// same shape a legitimate existence_probe case has -- proving
		// this must still flag (CaseMissing), not silently agree just
		// because both sides happen to read as the zero value.
	}}
	corpus := []trialCase{
		{ExpectKind: "project", ExpectID: "project.v2:gitlab:abc"},     // index 0: agrees
		{ExpectKind: "project", ExpectID: "project:stale-old-id"},      // index 1: stale id only
		{ExpectKind: "project", ExpectID: "project:70d529e0-77145099"}, // index 2: wrong kind AND id
		{ExpectKind: "work_item", ExpectID: ""},                        // index 3: agrees (no positive either side)
		{ExpectKind: "", ExpectID: ""},                                 // index 4: annex has NO case here at all
	}

	got := twoTurnFindCorpusAnnexAgreementViolations(annex, corpus)
	want := map[twoTurnCorpusAnnexDisagreement]bool{
		{Index: 1, KindDiffer: false, IDDiffer: true}: true,
		{Index: 2, KindDiffer: true, IDDiffer: true}:  true,
		{Index: 4, CaseMissing: true}:                 true,
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

// TestTwoTurnValidateCorpusAnnexAgreementFailsClosed proves the real
// preflight call site actually calls Fatalf on disagreement and stays
// silent when the fixtures agree.
func TestTwoTurnValidateCorpusAnnexAgreementFailsClosed(t *testing.T) {
	t.Parallel()
	agree := twoTurnOracleAnnex{Entries: []twoTurnOracleEntry{
		{Index: 0, Member: string(contractsv1.ContextFabricStructureNeedExpectedKind), PositiveKind: "project"},
		{Index: 0, Member: string(contractsv1.ContextFabricStructureNeedSubjectAnchor), PositiveKind: "project", PositiveAnchorCanonicalID: "project.v2:gitlab:abc"},
	}}
	fake := &fatalfSpy{}
	twoTurnValidateCorpusAnnexAgreement(fake, agree, []trialCase{{ExpectKind: "project", ExpectID: "project.v2:gitlab:abc"}})
	if fake.called {
		t.Errorf("Fatalf called on agreeing fixtures: %q", fake.message)
	}

	fake = &fatalfSpy{}
	twoTurnValidateCorpusAnnexAgreement(fake, agree, []trialCase{{ExpectKind: "project", ExpectID: "project:stale"}})
	if !fake.called {
		t.Fatal("Fatalf never called on disagreeing fixtures")
	}
}
