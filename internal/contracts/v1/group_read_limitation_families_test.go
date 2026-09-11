package v1

import "testing"

// THE TWO GROUP-READ DISCLOSURE FAMILIES: a grouped answer some of whose
// groups could not be read, and a grouped question whose group list was larger
// than an answer can carry.
//
// Both are interpolated, so both are recognised by a PARSE, and a recogniser
// that is not registered leaves its sentence displaceable by the next composer
// -- the served answer then says nothing about groups it never read. Each
// property is walked over the WHOLE subject-kind vocabulary.

// groupReadFamily is one of the two families, with its composer and parse.
type groupReadFamily struct {
	name      string
	compose   func(ContextFabricSubjectKind) string
	recognise func(string) bool
}

func groupReadFamilies() []groupReadFamily {
	return []groupReadFamily{
		{"unread", ContextFabricGroupReadUnreadLimitation, IsContextFabricGroupReadUnreadLimitation},
		{"over-bound", ContextFabricGroupListOverBoundLimitation, IsContextFabricGroupListOverBoundLimitation},
	}
}

// TestBothGroupReadDisclosuresAreServiceAuthored is the displacement property:
// every composed sentence is recognised by its own parse and by the registry.
func TestBothGroupReadDisclosuresAreServiceAuthored(t *testing.T) {
	t.Parallel()
	checked := 0
	for _, family := range groupReadFamilies() {
		for _, kind := range ContextFabricSubjectKindVocabulary() {
			checked++
			sentence := family.compose(kind)
			if !family.recognise(sentence) {
				t.Errorf("%s: its own parse does not recognise %q", family.name, sentence)
			}
			if !IsContextFabricServiceAuthoredLimitation(sentence) {
				t.Errorf("%s: %q is not registered as service-authored, so the next composer may displace it", family.name, sentence)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no sentence was checked, so every assertion above is vacuous")
	}
}

// TestTheGroupReadRecognisersAdmitNothingElse is the non-aliasing and
// no-loose-match property, over every service family that could be confused
// with them: each group-read parse refuses the other group-read family, both
// grouping families, a model caveat opening with the same words, an empty
// kind, and an out-of-vocabulary kind.
func TestTheGroupReadRecognisersAdmitNothingElse(t *testing.T) {
	t.Parallel()
	families := groupReadFamilies()
	checked := 0
	for i, family := range families {
		for _, kind := range ContextFabricSubjectKindVocabulary() {
			other := families[1-i].compose(kind)
			near := []string{
				other,
				ContextFabricGroupingUnplaceableLimitation(kind),
				ContextFabricGroupingRefusalLimitation(kind, kind),
				family.compose(kind) + " And more.",
				"Note: " + family.compose(kind),
			}
			for _, candidate := range near {
				checked++
				if family.recognise(candidate) {
					t.Errorf("%s: the parse accepts %q, which it did not compose", family.name, candidate)
				}
			}
		}
		for _, bad := range []ContextFabricSubjectKind{"", "not_a_kind", "Team"} {
			checked++
			if family.recognise(family.compose(bad)) {
				t.Errorf("%s: a sentence composed with the out-of-vocabulary kind %q is recognised", family.name, bad)
			}
		}
	}
	// The grouping parses must refuse the group-read sentences too, or a
	// reader would be told a grouping was answered on another axis.
	for _, family := range families {
		for _, kind := range ContextFabricSubjectKindVocabulary() {
			checked++
			sentence := family.compose(kind)
			if IsContextFabricGroupingRefusalLimitation(sentence) || IsContextFabricGroupingUnplaceableLimitation(sentence) {
				t.Errorf("a grouping parse accepts the %s sentence %q", family.name, sentence)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no candidate was checked")
	}
}
