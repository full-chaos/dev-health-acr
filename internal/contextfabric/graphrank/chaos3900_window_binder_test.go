package graphrank

import (
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestBindWindowSpans_TrailingPhrases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		question string
		want     contextfabric.RelativeWindowID
	}{
		{"what shipped over the last month", contextfabric.RelativeWindowTrailing30D},
		{"what shipped over the past month", contextfabric.RelativeWindowTrailing30D},
		{"how did the team do last quarter", contextfabric.RelativeWindowTrailing90D},
		{"how did the team do past quarter", contextfabric.RelativeWindowTrailing90D},
		{"what changed in the last year", contextfabric.RelativeWindowTrailing365D},
		{"what changed in the past year", contextfabric.RelativeWindowTrailing365D},
	}
	for _, tc := range cases {
		bound := BindWindowSpans(tc.question)
		if len(bound) != 1 {
			t.Fatalf("BindWindowSpans(%q) = %#v, want exactly 1 span", tc.question, bound)
		}
		if bound[0].RelativeID != tc.want {
			t.Fatalf("BindWindowSpans(%q)[0].RelativeID = %q, want %q", tc.question, bound[0].RelativeID, tc.want)
		}
	}
}

func TestBindWindowSpans_NoMatchOutsideRegistry(t *testing.T) {
	t.Parallel()
	cases := []string{
		"how is the team doing",
		"what happened yesterday",
		"is the project on track for next quarter", // "next quarter" is not in the closed slice-1 registry
		"lastly, what about the deploy",            // "lastly" must not be mistaken for "last"
		"the pastry shop project",                  // "pastry" must not be mistaken for "past"
	}
	for _, question := range cases {
		if bound := BindWindowSpans(question); len(bound) != 0 {
			t.Fatalf("BindWindowSpans(%q) = %#v, want no spans bound", question, bound)
		}
	}
}

func TestIsMultiWindowSpan(t *testing.T) {
	t.Parallel()
	if IsMultiWindowSpan(nil) {
		t.Fatal("IsMultiWindowSpan(nil) = true, want false")
	}
	one := []BoundWindowSpan{{Grammar: "trailing_month"}}
	if IsMultiWindowSpan(one) {
		t.Fatal("IsMultiWindowSpan(one span) = true, want false")
	}
	two := []BoundWindowSpan{{Grammar: "trailing_month"}, {Grammar: "trailing_quarter"}}
	if !IsMultiWindowSpan(two) {
		t.Fatal("IsMultiWindowSpan(two spans) = false, want true")
	}
}

func TestProposeWindowFromSpans_NoSpan(t *testing.T) {
	t.Parallel()
	got := ProposeWindowFromSpans("how is the team doing")
	if got.Reason != WindowBindNoSpan || got.RelativeID != "" || got.SpansBound != 0 {
		t.Fatalf("ProposeWindowFromSpans(no span) = %#v, want WindowBindNoSpan", got)
	}
}

func TestProposeWindowFromSpans_MultiSpanRefuses(t *testing.T) {
	t.Parallel()
	got := ProposeWindowFromSpans("compare last month to last quarter")
	if got.Reason != WindowBindSpanAmbiguous || got.RelativeID != "" {
		t.Fatalf("ProposeWindowFromSpans(multi-span) = %#v, want WindowBindSpanAmbiguous with no RelativeID", got)
	}
	if got.SpansBound != 2 {
		t.Fatalf("ProposeWindowFromSpans(multi-span).SpansBound = %d, want 2", got.SpansBound)
	}
}

func TestProposeWindowFromSpans_PrepositionRolePasses(t *testing.T) {
	t.Parallel()
	cases := []string{
		"what shipped within the last month",
		"what shipped over the last month",
		"what changed during the last quarter",
		"what broke since last month",
	}
	for _, question := range cases {
		got := ProposeWindowFromSpans(question)
		if got.Reason != WindowBindRoutedInferred {
			t.Fatalf("ProposeWindowFromSpans(%q) = %#v, want WindowBindRoutedInferred", question, got)
		}
		if got.RelativeID == "" {
			t.Fatalf("ProposeWindowFromSpans(%q) routed inferred with an empty RelativeID", question)
		}
	}
}

func TestProposeWindowFromSpans_ClauseInitialAndFinalRolePasses(t *testing.T) {
	t.Parallel()
	cases := []string{
		"last quarter, how did the team do",
		"how did the team do last quarter",
		"last quarter?",
	}
	for _, question := range cases {
		got := ProposeWindowFromSpans(question)
		if got.Reason != WindowBindRoutedInferred {
			t.Fatalf("ProposeWindowFromSpans(%q) = %#v, want WindowBindRoutedInferred", question, got)
		}
	}
}

func TestProposeWindowFromSpans_EntityNameInTemporalRoleStillPassesRoleCheck(t *testing.T) {
	t.Parallel()
	// Design brief v4 round-4 stamp (B2), preserved here as a pin: the role
	// check is STRUCTURAL, not semantic -- "What failed in Last Year?"
	// binds "Last Year" behind the closed preposition "in" whether Last
	// Year is a timeframe or a project. W0 carries no collision guard
	// (v5.2 descope), so this MUST still route to inferred -- it is a
	// proposal only, never decisive authority, so the collision costs at
	// most one disclosed, gated default.
	got := ProposeWindowFromSpans("what failed in Last Year")
	if got.Reason != WindowBindRoutedInferred {
		t.Fatalf("ProposeWindowFromSpans(entity-name-in-temporal-role) = %#v, want WindowBindRoutedInferred (role check is structural, not semantic)", got)
	}
}

func TestProposeWindowFromSpans_NoRoleRefuses(t *testing.T) {
	t.Parallel()
	// "month" sits truly MID-CLAUSE: preceded by "heard" (not a closed
	// preposition, and not clause-initial even after stripping "the" --
	// "we heard the" still has "heard" left over) and followed by more
	// clause. Fails the role check.
	got := ProposeWindowFromSpans("we heard the last month numbers looked fine to everyone")
	if got.Reason != WindowBindSpanUnbound {
		t.Fatalf("ProposeWindowFromSpans(no role) = %#v, want WindowBindSpanUnbound", got)
	}
	if got.RelativeID != "" {
		t.Fatalf("ProposeWindowFromSpans(no role) set RelativeID %q, want empty on refusal", got.RelativeID)
	}
}

func TestProposeWindowFromSpans_ClauseInitialWithLeadingArticlePasses(t *testing.T) {
	t.Parallel()
	// I5 pin: a bare leading article before an otherwise clause-initial
	// span ("The last month has been rough") must still pass -- stripping
	// the article during the preposition check leaves nothing, which IS
	// clause-initial, not a failed preposition match.
	got := ProposeWindowFromSpans("the last month has been rough")
	if got.Reason != WindowBindRoutedInferred {
		t.Fatalf("ProposeWindowFromSpans(clause-initial with leading article) = %#v, want WindowBindRoutedInferred", got)
	}
}

func TestBoundWindowSpanCarriesNoSpanText(t *testing.T) {
	t.Parallel()
	// Corpus-safety structural pin: BoundWindowSpan must never grow a
	// field that could carry matched question text -- only offsets, the
	// fixed registry name, and the closed RelativeID are safe to trace.
	// A future field addition (e.g. a well-meaning "SpanText string" for
	// debugging) must fail THIS test, not merely be missed in review.
	typ := reflect.TypeOf(BoundWindowSpan{})
	allowed := map[string]bool{"Grammar": true, "RelativeID": true, "SpanStart": true, "SpanEnd": true}
	if typ.NumField() != len(allowed) {
		t.Fatalf("BoundWindowSpan has %d fields, want exactly %d (%v) -- a field was added without updating this corpus-safety pin", typ.NumField(), len(allowed), allowed)
	}
	forbidden := []string{"text", "span", "value", "term", "label", "matchedtext"}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !allowed[name] {
			t.Fatalf("BoundWindowSpan.%s is not in the allowed field set %v -- corpus-safety review required before adding a field here", name, allowed)
		}
		lower := lowerASCII(name)
		for _, bad := range forbidden {
			if lower == bad {
				t.Fatalf("BoundWindowSpan.%s: field name suggests free question text", name)
			}
		}
	}
}

// lowerASCII mirrors the identical helper other corpus-safety canaries in
// this repo use (e.g. internal/runtime/hosted's replay/D2(b)/W0 harnesses).
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
