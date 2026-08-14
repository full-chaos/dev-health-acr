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
	// other bounds: a stored row still cannot carry an empty kind, nor one
	// oversized AFTER TRIMMING, because those were always enforced.
	//
	// Corrected in codex round 13: an earlier version of this comment said
	// no row could exist violating any of these, which was false for a value
	// padded past the maximum -- the old write path trimmed before
	// measuring, so raw-oversized rows were legally writable. That case is
	// covered by TestFindingKindLengthIsMeasuredRawOnWrite, which is why the
	// fixtures below trim to nothing or exceed the bound after trimming.
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

// TestFindingKindLengthIsMeasuredRawOnWrite closes codex round-13 F2.
//
// The length check measured strings.TrimSpace(f.Kind), so a value padded past
// the schema's maxLength passed validation: raw 130 with trimmed 128 is
// schema-invalid but was accepted. My round-12 comment justified stored-read
// leniency with "no row could ever have been written carrying them", and for
// the PADDED case that justification was false -- which is why the rule is
// restated here from evidence rather than from intent.
//
// EVIDENCE, world (b): the pre-branch write validator ALSO trimmed before
// measuring. At merge-base 81ac259b, validate_context_fabric_result.go:181
// reads stringLengthBetween(strings.TrimSpace(f.Kind), 1,
// ContextFabricFindingKindMaxLength), and that form dates to the field's
// introduction in cd9b338 (CHAOS-3770). So a raw-130 finding kind was
// legally writable for the whole life of the field, and rows carrying one may
// exist in storage. Rejecting them on READ would break reading data the
// service itself accepted.
//
// The rule, corrected: writes measure the RAW value, stored reads measure the
// trimmed value. The schema keeps describing the write contract, exactly as
// it does for the 250x4000 narrative allowance.
func TestFindingKindLengthIsMeasuredRawOnWrite(t *testing.T) {
	// Raw 130, trimmed 128: schema-invalid, but legally writable before now.
	padded := " " + strings.Repeat("x", ContextFabricFindingKindMaxLength) + " "
	if len([]rune(padded)) != ContextFabricFindingKindMaxLength+2 {
		t.Fatalf("fixture is not padded past the bound: raw %d", len([]rune(padded)))
	}
	if len([]rune(strings.TrimSpace(padded))) != ContextFabricFindingKindMaxLength {
		t.Fatal("fixture does not trim back to exactly the bound, so it would not isolate the padding")
	}

	if err := probeFinding(padded).validate(contextFabricLegacyBounds); err != nil {
		t.Errorf("a stored row carrying a raw-oversized kind is no longer readable (%v); such rows were legally writable, and they are immutable", err)
	}
	if err := probeFinding(padded).Validate(); err == nil {
		t.Error("the write path accepts a kind whose RAW length exceeds the schema maximum, so the service can emit a document that violates its own contract")
	}
}

// TestPaddedTextIsRejectedOnWriteAcrossTheClass is the class half: the padded
// hole was never specific to Finding.Kind. Every field measured after
// TrimSpace had it, and a bound that only holds after trimming is not the
// bound the schema publishes.
func TestPaddedTextIsRejectedOnWriteAcrossTheClass(t *testing.T) {
	pad := func(value string, bound int) string {
		return " " + value + strings.Repeat(" ", bound) // raw > bound, trims back under it
	}

	t.Run("driver title", func(t *testing.T) {
		driver := probeDriverJudgment()
		driver.Title = pad("Title", ContextFabricDriverTitleMaxLength)
		if err := driver.Validate(); err == nil {
			t.Error("write path accepts a driver title padded past the schema maximum")
		}
		if err := driver.validate(contextFabricLegacyBounds); err != nil {
			t.Errorf("stored read rejects a padded driver title that was legally writable: %v", err)
		}
	})

	t.Run("finding summary", func(t *testing.T) {
		finding := probeFinding("narrative")
		finding.Summary = pad("Summary.", ContextFabricFindingSummaryMaxLength)
		if err := finding.Validate(); err == nil {
			t.Error("write path accepts a finding summary padded past the schema maximum")
		}
		if err := finding.validate(contextFabricLegacyBounds); err != nil {
			t.Errorf("stored read rejects a padded finding summary that was legally writable: %v", err)
		}
	})
}

// probeDriverJudgment is a driver that validates cleanly, so a probe changing
// one text field measures that field alone.
func probeDriverJudgment() ContextFabricDriverJudgment {
	return ContextFabricDriverJudgment{
		DriverID: "driver_probe_0001", Standing: ContextFabricDriverPrincipal, Category: "status",
		Title: "Probe title", Summary: "Probe summary.",
		AffectedSubjects: []ContextFabricSubjectRef{{Kind: ContextFabricSubjectProject, CanonicalID: "project_probe", Label: "Probe"}},
		EvidenceRefIDs:   []string{"evidence_probe_0001"},
		ClaimedFactIDs:   []string{"claim_probe_00001"},
		Derivation:       ContextFabricDerivationCanonicalStructured,
		EpistemicStatus:  ContextFabricEpistemicObserved,
		Confidence:       0.5, Current: true,
	}
}
