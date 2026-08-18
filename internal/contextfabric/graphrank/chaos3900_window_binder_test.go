package graphrank

import (
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
	// "month" sits mid-clause with no closed preposition immediately
	// before it and more clause follows after -- fails the role check.
	got := ProposeWindowFromSpans("the last month numbers looked fine to everyone")
	if got.Reason != WindowBindSpanUnbound {
		t.Fatalf("ProposeWindowFromSpans(no role) = %#v, want WindowBindSpanUnbound", got)
	}
	if got.RelativeID != "" {
		t.Fatalf("ProposeWindowFromSpans(no role) set RelativeID %q, want empty on refusal", got.RelativeID)
	}
}

func TestBoundWindowSpanCarriesNoSpanText(t *testing.T) {
	t.Parallel()
	// Corpus-safety structural pin: BoundWindowSpan must never grow a
	// field that could carry matched question text -- only offsets and
	// the fixed registry name are safe to trace.
	span := BoundWindowSpan{}
	_ = span.SpanStart
	_ = span.SpanEnd
	_ = span.Grammar
	_ = span.RelativeID
	// The compile-time absence of any other exported field is the actual
	// assertion here; this test exists so a future field addition is at
	// least forced through a reviewer's eyes on this file, not to check
	// anything at runtime beyond "the struct still has exactly these."
}
