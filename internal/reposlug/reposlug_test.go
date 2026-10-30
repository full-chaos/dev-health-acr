package reposlug

import (
	"strings"
	"testing"
)

func TestValidNameAcceptsGitHubNamesAndRefusesReservedOnes(t *testing.T) {
	long100 := strings.Repeat("a", 100)
	for _, test := range []struct {
		name string
		part string
		want bool
	}{
		{"plain", "widget-service", true},
		{"dot-leading org config repo", ".github", true},
		{"dot-leading single char tail", ".a", true},
		{"dot inside", "a.b", true},
		{"underscore inside", "a_b", true},
		{"one character", "a", true},
		{"one digit", "7", true},
		{"100 characters", long100, true},
		{"101 characters", long100 + "a", false},
		{"single dot", ".", false},
		{"double dot", "..", false},
		{"empty", "", false},
		{"leading hyphen", "-a", false},
		{"leading underscore", "_a", false},
		{"trailing dot", "a.", false},
		{"trailing hyphen", "a-", false},
		{"dot-leading trailing dot", ".a.", false},
		{"upper case (callers lower-case first)", ".GitHub", false},
		{"slash", "a/b", false},
		{"space", "a b", false},
		{"star", "*", false},
	} {
		if got := ValidName(test.part); got != test.want {
			t.Errorf("%s: ValidName(%q) = %v, want %v", test.name, test.part, got, test.want)
		}
	}
}

func TestValidOwnerStillRefusesALeadingDot(t *testing.T) {
	for part, want := range map[string]bool{"full-chaos": true, "a": true, ".github": false, ".": false, "..": false, "": false, "a-": false} {
		if got := ValidOwner(part); got != want {
			t.Errorf("ValidOwner(%q) = %v, want %v", part, got, want)
		}
	}
}

func TestFaultNamesTheReason(t *testing.T) {
	for _, test := range []struct {
		part  string
		owner bool
		want  string
	}{
		{".github", false, ""},
		{".github", true, "must start with a letter or digit"},
		{"..", false, "reserved path name"},
		{".", false, "reserved path name"},
		{"", false, "empty"},
		{strings.Repeat("a", 101), false, "longer than 100"},
		{"a-", false, "end with a letter or digit"},
	} {
		got := Fault(test.part, test.owner)
		if test.want == "" && got != "" || !strings.Contains(got, test.want) {
			t.Errorf("Fault(%q, owner=%v) = %q, want it to contain %q", test.part, test.owner, got, test.want)
		}
	}
}
