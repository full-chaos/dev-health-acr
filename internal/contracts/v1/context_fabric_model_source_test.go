package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestModelSourceOverItsWholeInputDomain executes every cell of the field's
// domain: absent, each member, out of vocabulary, a case variant and a near
// miss.
func TestModelSourceOverItsWholeInputDomain(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		cell   string
		source ContextFabricModelSource
		valid  bool
	}{
		{"absent", "", true},
		{"organization", ContextFabricModelSourceOrganization, true},
		{"platform", ContextFabricModelSourcePlatform, true},
		{"caller", "caller", false},
		{"case variant", "Organization", false},
		{"near miss", "platform_", false},
		{"padded", " platform", false},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			t.Parallel()
			if err := testCase.source.Validate(); (err == nil) != testCase.valid {
				t.Fatalf("Validate() = %v, want valid=%v", err, testCase.valid)
			}
		})
	}
	got := ContextFabricModelSources()
	if len(got) != 2 || string(got[0]) != "organization" || string(got[1]) != "platform" {
		t.Fatalf("the wire vocabulary moved without this domain moving with it: %v", got)
	}
}

// The field is optional on the wire: absent when empty, the member's own text
// when set, on both the canonical result and the answer projection.
func TestModelSourceIsOmittedWhenEmptyAndNamedWhenSet(t *testing.T) {
	t.Parallel()
	for _, subject := range []struct {
		name string
		with func(ContextFabricModelSource) any
	}{
		{"result", func(s ContextFabricModelSource) any { return ContextFabricInvestigationResult{ModelSource: s} }},
		{"projection", func(s ContextFabricModelSource) any { return ContextFabricAnswerProjection{ModelSource: s} }},
	} {
		empty, err := json.Marshal(subject.with(""))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(empty), "model_source") {
			t.Fatalf("%s: an empty model_source was serialized: %s", subject.name, empty)
		}
		for _, member := range ContextFabricModelSources() {
			set, err := json.Marshal(subject.with(member))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(set), `"model_source":"`+string(member)+`"`) {
				t.Fatalf("%s: model_source %q missing from %s", subject.name, member, set)
			}
		}
	}
}

// A result and a projection refuse a model_source outside the vocabulary and
// accept each member, on writes and on stored reads alike.
func TestAResultRefusesAModelSourceOutsideTheVocabulary(t *testing.T) {
	result := buildFromTable(t, func(b answerBound) func(*ContextFabricInvestigationResult) { return b.Min })
	for _, member := range append([]ContextFabricModelSource{""}, ContextFabricModelSources()...) {
		result.ModelSource = member
		if err := result.Validate(); err != nil {
			t.Fatalf("model_source %q refused on write: %v", member, err)
		}
		if err := result.ValidateStored(); err != nil {
			t.Fatalf("model_source %q refused on a stored read: %v", member, err)
		}
	}
	result.ModelSource = "tenant"
	if err := result.Validate(); err == nil {
		t.Fatal("a model_source outside the vocabulary was accepted on write")
	}
	if err := result.ValidateStored(); err == nil {
		t.Fatal("a model_source outside the vocabulary was accepted on a stored read")
	}
}
