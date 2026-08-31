package contextfabric

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4632: the hand-labelled question set, and the checks that keep it
// usable as ground truth.
//
// WHAT THIS FILE DOES AND DOES NOT DO. It does NOT score a model -- that
// happens on the rig, against a live interpret, and its result is the
// slice's gating number. What it does is guarantee the SET ITSELF is fit
// to be that ground truth: enough negative cases to measure false
// emission, labels drawn from closed vocabularies, and labels CONSISTENT
// with the precedence table they will be scored through. A labelled set
// whose own labels contradict the table would silently produce a
// correctness number that means nothing, and nobody would be able to tell
// from the number.

type labelledQuestion struct {
	ID                    string   `json:"id"`
	Question              string   `json:"question"`
	Note                  string   `json:"note"`
	ExpectFamily          string   `json:"expect_family"`
	ExpectGroupKind       string   `json:"expect_group_kind"`
	ExpectScopeAnchor     string   `json:"expect_scope_anchor"`
	ExpectScopeAnchorAny  []string `json:"expect_scope_anchor_any"`
	ExpectScopeAnchorKind string   `json:"expect_scope_anchor_kind"`
	ExpectRequestedKind   string   `json:"expect_requested_kind"`
}

type labelledQuestionSet struct {
	Cases []labelledQuestion `json:"cases"`
}

// LoadLabelledQuestions reads the checked-in ground-truth set. Exported to
// the package so the rig measurement harness reads the SAME file these
// tests validate, rather than a copy that could drift from it.
func loadLabelledQuestions(t *testing.T) []labelledQuestion {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "chaos4632_labelled_questions.json"))
	if err != nil {
		t.Fatalf("read labelled question set: %v", err)
	}
	var set labelledQuestionSet
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatalf("parse labelled question set: %v", err)
	}
	if len(set.Cases) == 0 {
		t.Fatal("the labelled question set is empty")
	}
	return set.Cases
}

// TestLabelledSetCarriesEnoughNegativeCases is the assertion the design's
// Fable condition F4 asks for directly.
//
// §9-S2: the set "MUST include NEGATIVE cases -- non-grouped and
// non-scoped questions labelled with those fields EMPTY ... the gate
// therefore measures FALSE emission, not only correct emission."
//
// A majority threshold rather than a token one or two: precedence row 1
// fires on GroupKind ALONE, so the cost of a false positive is a plain
// status question answered as a grouped cohort. A set that is mostly
// positives would report a high correctness number while leaving exactly
// that failure unmeasured.
func TestLabelledSetCarriesEnoughNegativeCases(t *testing.T) {
	t.Parallel()
	cases := loadLabelledQuestions(t)
	var negativeGroup, negativeAnchor int
	for _, testCase := range cases {
		if testCase.ExpectGroupKind == "" {
			negativeGroup++
		}
		if testCase.ExpectScopeAnchor == "" {
			negativeAnchor++
		}
	}
	if negativeGroup*2 <= len(cases) {
		t.Errorf("only %d of %d cases are group_kind NEGATIVES; false emission is the failure this gate exists to catch, so negatives must be the majority", negativeGroup, len(cases))
	}
	if negativeAnchor*2 <= len(cases) {
		t.Errorf("only %d of %d cases are scope_anchor NEGATIVES; same reasoning", negativeAnchor, len(cases))
	}
	// And at least one POSITIVE of each, or the set cannot measure
	// correct emission at all.
	if negativeGroup == len(cases) {
		t.Error("no group_kind positives; the set cannot measure correct emission")
	}
	if negativeAnchor == len(cases) {
		t.Error("no scope_anchor positives; the set cannot measure correct emission")
	}
}

// TestLabelledSetLabelsAreClosedVocabularyMembers stops a typo in the
// ground truth from being scored as a model error forever.
func TestLabelledSetLabelsAreClosedVocabularyMembers(t *testing.T) {
	t.Parallel()
	for _, testCase := range loadLabelledQuestions(t) {
		if !ValidQuestionFamily(QuestionFamily(testCase.ExpectFamily)) {
			t.Errorf("case %q expects family %q, which is not a vocabulary member", testCase.ID, testCase.ExpectFamily)
		}
		for name, kind := range map[string]string{
			"expect_group_kind":        testCase.ExpectGroupKind,
			"expect_scope_anchor_kind": testCase.ExpectScopeAnchorKind,
			"expect_requested_kind":    testCase.ExpectRequestedKind,
		} {
			if kind == "" {
				continue
			}
			if !contractsv1.ValidContextFabricSubjectKind(contractsv1.ContextFabricSubjectKind(kind)) {
				t.Errorf("case %q %s = %q, which is not a subject-kind registry member", testCase.ID, name, kind)
			}
		}
	}
}

// TestLabelledSetIsInternallyConsistent catches label combinations that
// contradict themselves before a model is ever blamed for them.
func TestLabelledSetIsInternallyConsistent(t *testing.T) {
	t.Parallel()
	seen := map[string]struct{}{}
	for _, testCase := range loadLabelledQuestions(t) {
		if _, duplicate := seen[testCase.ID]; duplicate {
			t.Errorf("duplicate case id %q", testCase.ID)
		}
		seen[testCase.ID] = struct{}{}
		if strings.TrimSpace(testCase.Question) == "" {
			t.Errorf("case %q has no question text", testCase.ID)
		}
		if strings.TrimSpace(testCase.Note) == "" {
			t.Errorf("case %q has no note; a label without its reasoning cannot be reviewed, and an unreviewable ground truth is not ground truth", testCase.ID)
		}
		// An anchor and its kind travel together. An anchor with no kind
		// cannot establish row 2's asymmetry, so labelling one without
		// the other would encode an expectation the table can never meet.
		if (testCase.ExpectScopeAnchor == "") != (testCase.ExpectScopeAnchorKind == "") {
			t.Errorf("case %q labels anchor=%q with anchor_kind=%q; both or neither -- an anchor without a kind cannot establish the row-2 asymmetry", testCase.ID, testCase.ExpectScopeAnchor, testCase.ExpectScopeAnchorKind)
		}
		// A scoped question's anchor kind must DIFFER from what is being
		// asked about; that difference IS the family.
		if testCase.ExpectFamily == string(QuestionFamilyScopedCohortStatus) &&
			testCase.ExpectScopeAnchorKind == testCase.ExpectRequestedKind {
			t.Errorf("case %q is labelled scoped_cohort_status but its anchor kind equals its requested kind; there is no asymmetry, so row 2 could never fire", testCase.ID)
		}
		// A grouped question must carry the grouping kind, and a
		// non-grouped one must not.
		if (testCase.ExpectFamily == string(QuestionFamilyGroupedCohortStatus)) != (testCase.ExpectGroupKind != "") {
			t.Errorf("case %q labels family=%q with group_kind=%q; grouped_cohort_status and a set group_kind imply each other under precedence row 1", testCase.ID, testCase.ExpectFamily, testCase.ExpectGroupKind)
		}
	}
}

// TestLabelledSetExpectationsMatchThePrecedenceTable is the load-bearing
// consistency check, and the one that would catch the worst kind of
// mistake in this set.
//
// It feeds each case's OWN LABELS -- as if a model had emitted them
// perfectly -- through the real precedence table and requires the labelled
// family to come out. If a label set and the table disagree, then a model
// emitting exactly the right signals would still be scored wrong, and the
// gating number would measure the disagreement rather than the model. That
// error is invisible in the number itself, which is precisely why it is
// checked here.
//
// Shape is deliberately supplied as the value a correct interpretation
// would carry, and rows 1-3 do not read it anyway for the grouped, scoped
// and comparison cases -- which is the property
// TestPrecedenceDoesNotReadShapeUntilRowFour proves independently.
func TestLabelledSetExpectationsMatchThePrecedenceTable(t *testing.T) {
	t.Parallel()
	shapeFor := map[string]InvestigationShape{
		"qa-grouped-clean":                   ShapeDiscoveredCohort,
		"qa-grouped-typo":                    ShapeDiscoveredCohort,
		"qb-scoped":                          ShapeSingleSubject,
		"q1-bar-subject-status":              ShapeSingleSubject,
		"q2-bar-discovered-cohort":           ShapeDiscoveredCohort,
		"neg-single-subject-why":             ShapeSingleSubject,
		"neg-mentions-teams-but-not-grouped": ShapeSingleSubject,
		"neg-possessive-but-same-kind":       ShapeSingleSubject,
		"pos-scoped-repositories":            ShapeDiscoveredCohort,
		"pos-grouped-per-phrasing":           ShapeDiscoveredCohort,
		"neg-explicit-comparison":            ShapeExplicitCohort,
		"neg-open-question":                  ShapeOpen,
	}
	for _, testCase := range loadLabelledQuestions(t) {
		shape, ok := shapeFor[testCase.ID]
		if !ok {
			t.Errorf("case %q has no expected Shape in this test; add one so the case is actually scored rather than silently skipped", testCase.ID)
			continue
		}
		sample := FamilySample{
			Shape:           shape,
			GroupKind:       SubjectKind(testCase.ExpectGroupKind),
			ScopeAnchorTerm: testCase.ExpectScopeAnchor,
			ScopeAnchorKind: SubjectKind(testCase.ExpectScopeAnchorKind),
			RequestedKind:   SubjectKind(testCase.ExpectRequestedKind),
		}
		// The comparison case is the one whose family comes from subject
		// terms rather than the new signals, so it needs them.
		if testCase.ID == "neg-explicit-comparison" {
			sample.SubjectTerms = []string{"acr", "ask-dev"}
		}
		got := ResolveFamilyForSample(sample).Family
		if got != QuestionFamily(testCase.ExpectFamily) {
			t.Errorf("case %q: feeding its own labels through the precedence table yields %q, but the case is labelled %q -- the ground truth and the table disagree, so a model emitting PERFECT signals would still be scored wrong", testCase.ID, got, testCase.ExpectFamily)
		}
	}
}

// TestScopeAnchorAlternativesAreThemselvesConsistent guards the field added
// after the live run scored a correct answer as wrong.
//
// expect_scope_anchor_any exists because ScopeAnchorTerm is a RETRIEVAL
// POINTER, not a value: two different verbatim substrings can name the same
// anchor ("platform" and "platform team" both name the platform team), and
// scoring one of them as an error measures the model's choice of substring
// rather than whether it found the right anchor. That is not what the anchor
// is for, and it is the mistake this field corrects.
//
// The guard: whenever alternatives are listed, the canonical
// expect_scope_anchor must be one of them. Otherwise the two fields could
// drift into disagreeing about the same case, and a scorer reading either
// one alone would be right by accident.
func TestScopeAnchorAlternativesAreThemselvesConsistent(t *testing.T) {
	t.Parallel()
	for _, testCase := range loadLabelledQuestions(t) {
		if len(testCase.ExpectScopeAnchorAny) == 0 {
			continue
		}
		if testCase.ExpectScopeAnchor == "" {
			t.Errorf("case %q lists anchor alternatives but no canonical expect_scope_anchor", testCase.ID)
			continue
		}
		found := false
		for _, alternative := range testCase.ExpectScopeAnchorAny {
			if strings.EqualFold(strings.TrimSpace(alternative), strings.TrimSpace(testCase.ExpectScopeAnchor)) {
				found = true
			}
		}
		if !found {
			t.Errorf("case %q canonical anchor %q is not among its own alternatives %v", testCase.ID, testCase.ExpectScopeAnchor, testCase.ExpectScopeAnchorAny)
		}
	}
}

// TestLabelledAnchorMatchesImplementsTheDeclaredRule is the guard codex
// round 4 asked for: the JSON declares that scoring accepts any listed
// alternative, and until this test existed NOTHING in the tree applied that
// rule -- the claim rested entirely on an off-tree program.
//
// It also pins the boundaries of the latitude, which matter more than the
// latitude itself: an EMPTY label admits ONLY an empty emission (that is the
// negative case the whole gate turns on), and matching is exact-per-member,
// never prefix or substring -- otherwise "platform" would satisfy an
// expected "platform infrastructure", which names a different team, and a
// real error would score as a pass.
func TestLabelledAnchorMatchesImplementsTheDeclaredRule(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name         string
		emitted      string
		canonical    string
		alternatives []string
		want         bool
	}{
		{"canonical matches", "platform", "platform", []string{"platform", "platform team"}, true},
		{"listed alternative matches -- the round-2 live case", "platform team", "platform", []string{"platform", "platform team"}, true},
		{"case and whitespace folded", "  Platform Team ", "platform", []string{"platform", "platform team"}, true},
		{"unlisted value rejected", "infrastructure", "platform", []string{"platform", "platform team"}, false},
		{"NO prefix matching: a longer real name is not satisfied by its prefix", "platform", "platform infrastructure", nil, false},
		{"NO substring matching either", "platform infrastructure", "platform", nil, false},
		{"empty label admits ONLY an empty emission", "", "", nil, true},
		{"empty label REJECTS any emission -- the negative case the gate turns on", "anything", "", []string{"anything"}, false},
		{"non-empty label rejects an omission", "", "platform", []string{"platform team"}, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := LabelledAnchorMatches(testCase.emitted, testCase.canonical, testCase.alternatives); got != testCase.want {
				t.Errorf("LabelledAnchorMatches(%q, %q, %v) = %v, want %v", testCase.emitted, testCase.canonical, testCase.alternatives, got, testCase.want)
			}
		})
	}
}

// TestEveryLabelledCaseIsScorableByTheDeclaredRule closes the loop: every
// case's OWN canonical anchor must score as a match under the rule, and
// every alternative it lists must too. A label the scorer would reject is a
// label that can never be satisfied, and the gating number would silently
// carry that as a model failure forever.
func TestEveryLabelledCaseIsScorableByTheDeclaredRule(t *testing.T) {
	t.Parallel()
	for _, testCase := range loadLabelledQuestions(t) {
		if !LabelledAnchorMatches(testCase.ExpectScopeAnchor, testCase.ExpectScopeAnchor, testCase.ExpectScopeAnchorAny) {
			t.Errorf("case %q: its own canonical anchor %q does not satisfy its own label", testCase.ID, testCase.ExpectScopeAnchor)
		}
		for _, alternative := range testCase.ExpectScopeAnchorAny {
			if !LabelledAnchorMatches(alternative, testCase.ExpectScopeAnchor, testCase.ExpectScopeAnchorAny) {
				t.Errorf("case %q lists alternative %q that the scoring rule rejects", testCase.ID, alternative)
			}
		}
	}
}
