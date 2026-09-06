package v1

import (
	"strings"
	"testing"
)

// THE TWO GROUPING DISCLOSURE FAMILIES, and why they need a test of their own.
//
// Both answer "you asked for a breakdown and did not get one", both are
// INTERPOLATED, and both therefore have to be recognised by a PARSE rather
// than by the equality check the fixed disclosures use. That recognition is
// not cosmetic: everything consulting the service-authored registry is
// deciding whether a string may be DISPLACED to make room for another
// disclosure, so an unrecognised service sentence is silently displaceable --
// which is the shipped round-3 defect this file's subject records.
//
// The new hazard, which the mismatch family did not have on its own: the two
// share their opening words AND their closing six. Neither segment alone
// separates them, so each parse must be shown to refuse the other's sentence,
// in BOTH directions, over the whole subject-kind vocabulary.

// TestBothGroupingDisclosuresAreServiceAuthored is the displacement property.
//
// Walks the whole subject-kind vocabulary rather than sampling one kind: the
// recognisers gate on vocabulary membership, so a kind added later must be
// covered the day it is added, not the day someone remembers this test.
func TestBothGroupingDisclosuresAreServiceAuthored(t *testing.T) {
	t.Parallel()
	kinds := ContextFabricSubjectKindVocabulary()
	// The vocabulary is a fixed-size array, so `len(kinds) == 0` would be a
	// constant-false guard that can never fire. Count what the loop actually
	// reached instead, and assert it after.
	checked := 0
	for _, planned := range kinds {
		checked++
		unplaceable := ContextFabricGroupingUnplaceableLimitation(planned)
		if !IsContextFabricGroupingUnplaceableLimitation(unplaceable) {
			t.Errorf("IsContextFabricGroupingUnplaceableLimitation(%q) = false: its own composer's output is unrecognisable", unplaceable)
		}
		if !IsContextFabricServiceAuthoredLimitation(unplaceable) {
			t.Errorf("the unplaceable disclosure for %q is not registered as service-authored, so the next composer may displace it and the served answer states nothing about having been answered ungrouped", planned)
		}
		for _, source := range kinds {
			mismatch := ContextFabricGroupingRefusalLimitation(planned, source)
			if !IsContextFabricServiceAuthoredLimitation(mismatch) {
				t.Errorf("the mismatch disclosure for (%q, %q) is not registered as service-authored", planned, source)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no subject kind was checked, so every assertion above is vacuous")
	}
}

// TestTheTwoGroupingRecognisersDoNotAliasEachOther is the non-aliasing
// property, asserted in BOTH directions.
//
// A prefix match, or a match on the shared tail alone, would make a mismatch
// sentence read as an unplaceable one and the reverse -- two different
// statements about two different defects, told to the reader interchangeably.
func TestTheTwoGroupingRecognisersDoNotAliasEachOther(t *testing.T) {
	t.Parallel()
	kinds := ContextFabricSubjectKindVocabulary()
	checked := 0
	for _, planned := range kinds {
		unplaceable := ContextFabricGroupingUnplaceableLimitation(planned)
		if IsContextFabricGroupingRefusalLimitation(unplaceable) {
			t.Errorf("the MISMATCH parse accepts an unplaceable sentence: %q -- a reader would be told the facts group by something else when the facts named nothing", unplaceable)
		}
		for _, source := range kinds {
			mismatch := ContextFabricGroupingRefusalLimitation(planned, source)
			if IsContextFabricGroupingUnplaceableLimitation(mismatch) {
				t.Errorf("the UNPLACEABLE parse accepts a mismatch sentence: %q", mismatch)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no (planned, source) pair was checked, so the mismatch direction proved nothing")
	}
}

// TestTheGroupingDisclosuresShareTheirFraming is the deliberate half of the
// hazard above, pinned so it is a decision rather than an accident.
//
// The two sentences are meant to open and close alike -- a reader meeting
// either should recognise the same framing. What must not be shared is
// RECOGNITION. Stating the resemblance here means an edit that breaks it is
// visible as a choice, and an edit that DEEPENS it (making the tails equal
// too) reddens the non-aliasing test next door rather than quietly merging
// the families.
func TestTheGroupingDisclosuresShareTheirFraming(t *testing.T) {
	t.Parallel()
	const tail = ", so the answer is presented ungrouped."
	unplaceable := ContextFabricGroupingUnplaceableLimitation(ContextFabricSubjectTeam)
	mismatch := ContextFabricGroupingRefusalLimitation(ContextFabricSubjectTeam, ContextFabricSubjectProject)

	for _, sentence := range []string{unplaceable, mismatch} {
		if !strings.HasPrefix(sentence, "This question asked for a breakdown by ") {
			t.Errorf("%q does not open with the shared framing", sentence)
		}
		if !strings.HasSuffix(sentence, tail) {
			t.Errorf("%q does not close with the shared framing", sentence)
		}
	}
	if unplaceable == mismatch {
		t.Fatal("the two disclosures are the same string; they describe different defects and a reader cannot be told which one happened")
	}
}

// TestAHalfComposedGroupingDisclosureIsNotRecognised is the attribution
// control for both recognisers: the empty kind is not a vocabulary member, so
// a sentence built from one must not become undisplaceable.
//
// Without this, a recogniser that returned true on a bare prefix/suffix match
// would pass every assertion above.
func TestAHalfComposedGroupingDisclosureIsNotRecognised(t *testing.T) {
	t.Parallel()
	for _, impostor := range []string{
		ContextFabricGroupingUnplaceableLimitation(""),
		ContextFabricGroupingUnplaceableLimitation("not_a_kind"),
		ContextFabricGroupingUnplaceableLimitation("team but also words"),
		"This question asked for a breakdown by team, so the answer is presented ungrouped.",
		"This question asked for a breakdown by team.",
		", but none of the items in this answer could be placed under one, so the answer is presented ungrouped.",
	} {
		if IsContextFabricGroupingUnplaceableLimitation(impostor) {
			t.Errorf("IsContextFabricGroupingUnplaceableLimitation(%q) = true: a string the composer never writes became undisplaceable, and a model caveat opening with this wording could take a real disclosure's place", impostor)
		}
	}
	// Positive control in the same test: the recogniser CAN say yes, so the
	// refusals above are decisions rather than a broken predicate.
	if !IsContextFabricGroupingUnplaceableLimitation(ContextFabricGroupingUnplaceableLimitation(ContextFabricSubjectTeam)) {
		t.Fatal("control failed: the recogniser rejects its own composer's output, so every assertion above measured nothing")
	}
}

// TestTheUnplaceableDisclosureNamesThePlannedKindAndNoOther pins what the
// sentence actually SAYS, which no parse-level test above can see.
//
// The mismatch sentence names two kinds because there are two; this one names
// the planned kind alone, because the source named nothing and inventing a
// second kind is the failure mode the whole grouping seam exists to prevent.
func TestTheUnplaceableDisclosureNamesThePlannedKindAndNoOther(t *testing.T) {
	t.Parallel()
	kinds := ContextFabricSubjectKindVocabulary()
	sentence := ContextFabricGroupingUnplaceableLimitation(ContextFabricSubjectTeam)
	if !strings.Contains(sentence, string(ContextFabricSubjectTeam)) {
		t.Fatalf("%q does not name the planned kind", sentence)
	}
	// No OTHER kind appears. Guards against a future edit that interpolates a
	// second value "for context" and thereby names an axis no source reported.
	for _, other := range kinds {
		if other == ContextFabricSubjectTeam {
			continue
		}
		if strings.Contains(sentence, string(other)) {
			t.Errorf("%q names %q as well as the planned kind; the source named no kind at all, so a second one is an invention", sentence, other)
		}
	}
	// And no digits: the ungrouped count rides the telemetry line and
	// CohortGroupingOutcome, deliberately, so the recogniser never has to
	// accept an arbitrary digit run inside a service-authored sentence.
	if strings.ContainsAny(sentence, "0123456789") {
		t.Errorf("%q carries a number; interpolating the count widens what a model caveat can impersonate", sentence)
	}
}

// ── 413 measurement ──────────────────────────────────────────────────────────

// unplaceableDisclosureMaxRunes is the measured worst-case length of the
// unplaceable disclosure, over the whole subject-kind vocabulary.
//
// PINNED, not logged. This number is what the PR body's byte-impact statement
// reports, and a measurement a downstream artifact depends on has to be
// ASSERTED in the test that derives it -- printing it is narration, and a
// narrated number cannot fail when the sentence is reworded or a longer kind
// is added.
//
// If you changed the wording or added a longer subject kind ON PURPOSE, update
// this pin in the same commit and say so in the message.
const unplaceableDisclosureMaxRunes = 161

// TestTheUnplaceableDisclosureCostsTheMeasuredBytes derives the worst case
// from the vocabulary rather than naming a kind, the way longestCoverageDetailCode
// measures rather than guesses.
func TestTheUnplaceableDisclosureCostsTheMeasuredBytes(t *testing.T) {
	t.Parallel()
	kinds := ContextFabricSubjectKindVocabulary()
	worst := 0
	var worstKind ContextFabricSubjectKind
	for _, kind := range kinds {
		if runes := len([]rune(ContextFabricGroupingUnplaceableLimitation(kind))); runes > worst {
			worst, worstKind = runes, kind
		}
	}
	if worst != unplaceableDisclosureMaxRunes {
		t.Errorf("worst-case unplaceable disclosure = %d runes (kind %q), pinned at %d -- if you reworded the sentence or added a longer subject kind on purpose, update the pin in this commit and say so",
			worst, worstKind, unplaceableDisclosureMaxRunes)
	}
	// It has to FIT, or the disclosure the reader is owed is the one the
	// validator rejects.
	if worst > ContextFabricLimitationMaxLength {
		t.Errorf("worst-case disclosure %d runes exceeds the per-limitation bound %d", worst, ContextFabricLimitationMaxLength)
	}
	// Every kind's sentence is ASCII, so runes == bytes and the PR body may
	// state one number for both. Asserted rather than assumed: a non-ASCII
	// kind would silently make the byte cost larger than the rune cost.
	for _, kind := range kinds {
		sentence := ContextFabricGroupingUnplaceableLimitation(kind)
		if len(sentence) != len([]rune(sentence)) {
			t.Errorf("the disclosure for %q is not ASCII (%d bytes, %d runes), so the byte impact is not the rune count", kind, len(sentence), len([]rune(sentence)))
		}
	}
}

// TestTheMaximalDocumentDoesNotGrowFromThisDisclosure is the BOUNDED half of
// the 413 statement, and the counter-intuitive one.
//
// The maximal fixture's limitation list is already AT the count cap and every
// entry but one is a maximum-length caveat. A disclosure appended there does
// not add bytes -- it DISPLACES a 2000-rune caveat and the document gets
// smaller. So the honest byte impact is two numbers with opposite signs, and
// reporting only the positive one would overstate the cost against exactly the
// budget that matters.
func TestTheMaximalDocumentDoesNotGrowFromThisDisclosure(t *testing.T) {
	t.Parallel()
	full := maximalLimitations()
	if len(full) != ContextFabricLimitationsMaxCount {
		t.Fatalf("maximalLimitations() = %d entries, want the cap %d -- this test measures displacement and needs a FULL list, or it measures an append instead",
			len(full), ContextFabricLimitationsMaxCount)
	}
	longestCaveat := 0
	for _, limitation := range full {
		if IsContextFabricServiceAuthoredLimitation(limitation) {
			continue
		}
		if runes := len([]rune(limitation)); runes > longestCaveat {
			longestCaveat = runes
		}
	}
	if longestCaveat == 0 {
		t.Fatal("the maximal fixture holds no model-authored caveat, so nothing could be displaced and this measurement is vacuous")
	}
	delta := unplaceableDisclosureMaxRunes - longestCaveat
	if delta >= 0 {
		t.Errorf("displacing a %d-rune caveat with a %d-rune disclosure changes the maximal document by %+d runes; a non-negative delta means this disclosure can grow the document at its ceiling, which the PR body must state as a cost rather than a saving",
			longestCaveat, unplaceableDisclosureMaxRunes, delta)
	}
}
