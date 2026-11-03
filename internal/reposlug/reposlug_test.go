package reposlug

import (
	"regexp"
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

func TestValidSlugAcceptsNestedGroupsUpToTheCaps(t *testing.T) {
	segments := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = "g"
		}
		return strings.Join(parts, "/")
	}
	// 255 bytes: 85 segments of 2 would pass the byte cap, so build 20 segments.
	bytes255 := strings.Repeat("a", 100) + "/" + strings.Repeat("b", 100) + "/" + strings.Repeat("c", 53)
	bytes256 := bytes255 + "c"
	for _, test := range []struct {
		name string
		slug string
		want bool
	}{
		{"two segments", "acme/web", true},
		{"three segments", "acme/grp/web", true},
		{"four segments", "acme/grp/sub/web", true},
		{"twenty segments", segments(20), true},
		{"twenty-one segments", segments(21), false},
		{"one segment", "acme", false},
		{"empty", "", false},
		{"255 bytes", bytes255, true},
		{"256 bytes", bytes256, false},
		{"empty middle segment", "acme//web", false},
		{"trailing slash", "acme/grp/", false},
		{"leading slash", "/acme/web", false},
		{"dot segment", "acme/./web", false},
		{"dot-dot segment", "acme/../web", false},
		{"dot-dot last", "acme/grp/..", false},
		{"dot last", "acme/grp/.", false},
		{"leading dot in a group", "acme/.grp/web", false},
		{"leading dot in the owner", ".acme/web", false},
		{"leading dot in the last segment", "acme/grp/.github", true},
		{"uppercase", "Acme/grp/web", false},
		{"wildcard segment", "acme/*/web", false},
		{"trailing wildcard", "acme/grp/*", false},
		{"space", "acme/g rp/web", false},
	} {
		if got := ValidSlug(test.slug); got != test.want {
			t.Errorf("%s: ValidSlug(%q) = %v, want %v", test.name, test.slug, got, test.want)
		}
	}
}

func TestValidScopeAcceptsTheGlobalOwnerAndRepositoryForms(t *testing.T) {
	for _, test := range []struct {
		scope string
		want  bool
	}{
		{"*", true},
		{"acme/*", true},
		{"acme/web", true},
		{"acme/grp/web", true},
		{"acme/grp/*", false},
		{"acme/*/web", false},
		{"/*", false},
		{"acme/**", false},
		{"acme", false},
		{"", false},
		{".acme/*", false},
		{"acme/grp/.github", true},
	} {
		if got := ValidScope(test.scope); got != test.want {
			t.Errorf("ValidScope(%q) = %v, want %v", test.scope, got, test.want)
		}
	}
}

// The ClickHouse spelling of the key grammar must agree with ValidSlug on
// everything ValidSlug accepts or refuses below the byte cap (the statement
// carries the cap as its own length test).
func TestSlugPatternAgreesWithValidSlug(t *testing.T) {
	pattern := regexp.MustCompile(`^` + SlugPattern + `$`)
	twenty := strings.Repeat("g/", 19) + "x"
	for _, slug := range []string{
		"acme/web", "acme/grp/web", "acme/grp/sub/web", twenty, twenty + "/y",
		"acme", "acme/", "acme//web", "acme/./web", "acme/../web", "acme/.grp/web",
		"acme/grp/.github", ".acme/web", "acme/grp/", "/acme/web", "acme/*/web",
	} {
		if got, want := pattern.MatchString(slug), ValidSlug(slug); got != want {
			t.Errorf("SlugPattern(%q) = %v, ValidSlug = %v", slug, got, want)
		}
	}
}
