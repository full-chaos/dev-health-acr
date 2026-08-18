package contextfabric

import "testing"

func TestValidWindowClass(t *testing.T) {
	t.Parallel()
	for _, class := range WindowClassVocabulary() {
		if !ValidWindowClass(class) {
			t.Fatalf("ValidWindowClass(%q) = false, want true (published vocabulary member)", class)
		}
	}
	if ValidWindowClass("") {
		t.Fatal("ValidWindowClass(\"\") = true, want false -- empty is \"unset\", not a vocabulary member")
	}
	if ValidWindowClass(WindowClass("not_a_window_class")) {
		t.Fatal("ValidWindowClass accepted a value outside the closed vocabulary")
	}
}

func TestWindowClassVocabularyIsACopy(t *testing.T) {
	t.Parallel()
	const forged = WindowClass("forged_class")
	vocabulary := WindowClassVocabulary()
	vocabulary[0] = forged
	if ValidWindowClass(forged) {
		t.Fatal("mutating the value returned by WindowClassVocabulary changed what ValidWindowClass accepts -- the accessor is handing out an alias, not a copy")
	}
	if fresh := WindowClassVocabulary(); fresh[0] == forged {
		t.Fatal("WindowClassVocabulary returned a mutated backing array on a second call")
	}
}

func TestValidWindowConfidence(t *testing.T) {
	t.Parallel()
	if !ValidWindowConfidence(WindowConfidenceHigh) || !ValidWindowConfidence(WindowConfidenceLow) {
		t.Fatal("ValidWindowConfidence rejected a published vocabulary member")
	}
	if ValidWindowConfidence("") || ValidWindowConfidence(WindowConfidence("medium")) {
		t.Fatal("ValidWindowConfidence accepted a non-member value")
	}
}

func TestSanitizeWindowClass(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw              string
		wantClass        WindowClass
		wantUnrecognized bool
	}{
		{"", "", false},
		{"   ", "", false},
		{"trend_assessment", WindowClassTrendAssessment, false},
		{"  recent_activity_lookup  ", WindowClassRecentActivityLookup, false},
		{"not_a_real_class", "", true},
		{"Trend_Assessment", "", true}, // exact, case-sensitive match only
	}
	for _, tc := range cases {
		gotClass, gotUnrecognized := SanitizeWindowClass(tc.raw)
		if gotClass != tc.wantClass || gotUnrecognized != tc.wantUnrecognized {
			t.Errorf("SanitizeWindowClass(%q) = (%q, %v), want (%q, %v)", tc.raw, gotClass, gotUnrecognized, tc.wantClass, tc.wantUnrecognized)
		}
	}
}

func TestSanitizeWindowConfidence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want WindowConfidence
	}{
		{"", ""},
		{"high", WindowConfidenceHigh},
		{" low ", WindowConfidenceLow},
		{"medium", ""},
	}
	for _, tc := range cases {
		if got := SanitizeWindowConfidence(tc.raw); got != tc.want {
			t.Errorf("SanitizeWindowConfidence(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
