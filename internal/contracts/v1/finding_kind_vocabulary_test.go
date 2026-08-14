package v1

import (
	"strings"
	"testing"
)

// This file closes codex round-12's High: ContextFabricFinding.Kind is the
// category-equivalent field for findings, governed by the same closed
// vocabulary as DriverJudgment.Category in the synthesis prompt and in
// ContextFabricDriverCategoryRequiresClaimedFact -- but nothing ENFORCED it.
// Finding.validate checked only non-emptiness and length, and the published
// schema left the field an unrestricted string while category carried the
// exact 16-member enum. A model could emit kind "source_disagreement" with
// valid evidence and produce a canonical result that validated.
//
// STRICT WRITE, LENIENT READ. The write path never enforced this, so stored
// rows may already carry an out-of-vocabulary kind. Tightening reads too
// would make those rows unreadable -- the failure this branch spent rounds
// preventing for narratives and evidence refs. Writes are closed; stored
// reads still accept what was legitimately written before the vocabulary was
// enforced, exactly as they do for the 250 x 4000 narrative allowance.

// probeFinding is a finding that validates cleanly, so a probe that changes
// only Kind measures the vocabulary and nothing else. It carries a claimed
// fact because a canonical-fact-shaped kind requires one.
func probeFinding(kind string) ContextFabricFinding {
	return ContextFabricFinding{
		FindingID:      "finding_probe_001",
		Kind:           kind,
		Summary:        "Probe summary.",
		Subjects:       []ContextFabricSubjectRef{{Kind: ContextFabricSubjectProject, CanonicalID: "project_probe", Label: "Probe"}},
		EvidenceRefIDs: []string{"evidence_probe_0001"},
		ClaimedFactIDs: []string{"claim_probe_00001"},
	}
}

// TestFindingKindIsAClosedVocabularyOnWrite is the strict half.
func TestFindingKindIsAClosedVocabularyOnWrite(t *testing.T) {
	for _, category := range ContextFabricDriverCategoryVocabulary() {
		if err := probeFinding(string(category)).Validate(); err != nil {
			t.Errorf("finding kind %q is in the declared vocabulary but the write path rejects it: %v", category, err)
		}
	}

	// The exact shape codex reported: a plausible, well-formed, entirely
	// invented kind, with everything else valid.
	for _, invented := range []string{"source_disagreement", "status ", "STATUS", "narrative_extra", "operational_deficiencies"} {
		if err := probeFinding(invented).Validate(); err == nil {
			t.Errorf("the write path accepts finding kind %q, which is not in the closed vocabulary; the prompt advertises a set the contract does not keep", invented)
		}
	}
}

// TestFindingKindStaysReadableForStoredRows is the lenient half.
//
// Every stored row written before this vocabulary was enforced must remain
// readable. A tightening that silently made existing rows unreadable would
// be a worse defect than the one it fixes: the rows are immutable and cannot
// be corrected.
func TestFindingKindStaysReadableForStoredRows(t *testing.T) {
	legacy := probeFinding("source_disagreement")

	if err := legacy.Validate(); err == nil {
		t.Fatal("precondition: this kind must be rejected on the WRITE path, or the test proves nothing about leniency")
	}
	if err := legacy.validate(contextFabricLegacyBounds); err != nil {
		t.Errorf("a stored finding carrying a pre-enforcement kind is no longer readable: %v", err)
	}

	// Leniency is about the VOCABULARY, not about abandoning the field's
	// other bounds: a stored row still cannot carry an empty or oversized
	// kind, because those were always enforced and no row can exist that
	// violates them.
	for _, malformed := range []string{"", "   ", strings.Repeat("x", ContextFabricFindingKindMaxLength+1)} {
		if err := probeFinding(malformed).validate(contextFabricLegacyBounds); err == nil {
			t.Errorf("the stored-read path accepts kind %q, which was never writable, so no stored row can legitimately carry it", malformed)
		}
	}
}

// TestFindingKindVocabularyMatchesTheDriverCategoryEnum pins the identity the
// synthesis prompt asserts: a finding's kind is "governed by the SAME closed
// set and the SAME rule as a driver's category". Two vocabularies that were
// allowed to differ would make that sentence false.
func TestFindingKindVocabularyMatchesTheDriverCategoryEnum(t *testing.T) {
	common := schemaDocuments(t)["common"]

	categoryEnum := schemaEnumAt(t, common, "$defs", "DriverJudgment", "properties", "category")
	findingEnum := schemaEnumAt(t, common, "$defs", "Finding", "properties", "kind")

	if strings.Join(categoryEnum, ",") != strings.Join(findingEnum, ",") {
		t.Errorf("the published Finding.kind and DriverJudgment.category vocabularies differ:\n  finding:  %v\n  category: %v", findingEnum, categoryEnum)
	}

	declared := make([]string, 0, ContextFabricDriverCategoryCount)
	for _, category := range ContextFabricDriverCategoryVocabulary() {
		declared = append(declared, string(category))
	}
	if strings.Join(findingEnum, ",") != strings.Join(declared, ",") {
		t.Errorf("the published Finding.kind enum and the Go vocabulary differ:\n  schema: %v\n  go:     %v", findingEnum, declared)
	}
}
