package pgprojection

import "testing"

func TestCanonicalOrgMapsEverySpellingOfOneUUIDToOneKey(t *testing.T) {
	const canonical = "f00d1234-5678-4abc-8def-0123456789ab"
	for _, spelling := range []string{
		canonical, "F00D1234-5678-4ABC-8DEF-0123456789AB", "{f00d1234-5678-4abc-8def-0123456789ab}",
		"urn:uuid:F00D1234-5678-4ABC-8DEF-0123456789AB", "f00d123456784abc8def0123456789ab", " F00D1234-5678-4ABC-8DEF-0123456789AB\t",
	} {
		got, ok := canonicalOrg(spelling)
		if !ok || got != canonical {
			t.Fatalf("canonicalOrg(%q) = %q, %v; want %q", spelling, got, ok, canonical)
		}
	}
	for _, notUUID := range []string{"", "org-1", "f00d1234-5678-4abc-8def-0123456789a", "f00d1234-5678-4abc-8def-0123456789abc"} {
		if got, ok := canonicalOrg(notUUID); ok {
			t.Fatalf("canonicalOrg(%q) = %q, want not a UUID", notUUID, got)
		}
	}
}
