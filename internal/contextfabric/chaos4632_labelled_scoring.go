package contextfabric

import "strings"

// CHAOS-4632 labelled-set scoring rules.
//
// WHY THIS IS PRODUCTION CODE AND NOT A TEST HELPER. The labelled question
// set (testdata/chaos4632_labelled_questions.json) declares that scoring
// accepts any listed anchor alternative. Codex round 4 found that claim was
// unbacked: the field was decoded and self-checked, but NO in-tree code
// applied the rule, so the only scorer that did was off-tree and the JSON's
// own promise rested on a program nobody could review. A rule stated in a
// data file and implemented nowhere is the same defect class as a telemetry
// field nothing logs.
//
// So the rule lives here, exported, and has exactly ONE implementation:
// the labelled-set test asserts it, and the measurement scorer that produces
// the reported numbers calls it. A second scorer that re-derived the rule
// could disagree with this one, and the disagreement would land inside the
// gating number where it would be indistinguishable from the model behaving
// differently -- the same reasoning that put ParseInterpretationOutputSignals
// behind one function.

// LabelledAnchorMatches reports whether an emitted scope-anchor term
// satisfies a labelled expectation.
//
// canonical is the label's own expect_scope_anchor; alternatives is its
// optional expect_scope_anchor_any. An EMPTY canonical means the label says
// the anchor must be absent, and only an empty emission satisfies it -- that
// is the negative case the whole gate turns on, so it is checked first and
// never relaxed by alternatives.
//
// Matching is case-insensitive after trimming, and accepts any listed
// alternative, because ScopeAnchorTerm is a RETRIEVAL POINTER, NOT A VALUE:
// nothing downstream branches on its text, only on whether it resolved. Two
// different verbatim substrings can name the same anchor -- "platform" and
// "platform team" both name the platform team -- and scoring one of them
// wrong measures the model's choice of substring rather than whether it
// found the right anchor. That is not what the anchor is for.
//
// It deliberately does NOT do prefix or substring matching. That would
// accept "platform" for an expected "platform infrastructure", which names a
// different team -- turning a real error into a pass. Alternatives are an
// explicit, reviewed list precisely so the latitude stays bounded by
// something a human wrote down.
func LabelledAnchorMatches(emitted, canonical string, alternatives []string) bool {
	normalize := func(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
	got := normalize(emitted)
	if normalize(canonical) == "" {
		// The label says "no anchor". Only an absent emission satisfies it.
		return got == ""
	}
	if got == "" {
		return false
	}
	if got == normalize(canonical) {
		return true
	}
	for _, alternative := range alternatives {
		if got == normalize(alternative) {
			return true
		}
	}
	return false
}
