package falkorgraph

import (
	"reflect"
	"testing"
)

func TestMatchOrganizationGraphKeysTakesEveryEpochAndNothingElse(t *testing.T) {
	base := graphKey("acr", "org-a")
	other := graphKey("acr", "org-b")
	names := []string{
		base, base + "-e1", base + "-e2", base + "-e7", base + "-e12",
		base + "-e", base + "-ex", base + "-e1x", base + "-e-1", base + "x",
		other, other + "-e1", "unrelated",
	}
	want := []string{base, base + "-e1", base + "-e2", base + "-e7", base + "-e12"}
	if got := matchOrganizationGraphKeys(names, base); !reflect.DeepEqual(got, want) {
		t.Fatalf("matchOrganizationGraphKeys() = %v, want %v", got, want)
	}
}

func TestGraphKeysDifferByUUIDSpelling(t *testing.T) {
	const canonical = "abcdef01-2345-4678-89ab-cdef01234567"
	if graphKey("acr", canonical) == graphKey("acr", "ABCDEF01-2345-4678-89AB-CDEF01234567") {
		t.Fatal("graph keys are expected to differ by spelling: that is why purge cannot derive keys from one spelling")
	}
}

func TestIsProjectionGraphNameAcceptsOnlyTheProjectionShapes(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef"
	for name, want := range map[string]bool{
		"acr-" + hash: true, "acr-" + hash + "-e3": true, "acr-" + hash + "-e12": true,
		"acr-" + hash + "-e": false, "acr-" + hash + "-ex": false, "acr-" + hash + "x": false,
		"acr-" + hash[:31]: false, "acr-" + "0123456789ABCDEF0123456789ABCDEF": false,
		"other-" + hash: false, hash: false,
	} {
		if got := isProjectionGraphName(name, "acr"); got != want {
			t.Errorf("isProjectionGraphName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestNormalizeOrgSpellingCollapsesEveryUUIDForm(t *testing.T) {
	const canonical = "abcdef01-2345-4678-89ab-cdef01234567"
	for _, spelling := range []string{canonical, "ABCDEF01-2345-4678-89AB-CDEF01234567", "{ABCDEF01-2345-4678-89AB-CDEF01234567}", "abcdef0123454678" + "89abcdef01234567", "urn:uuid:" + canonical, "  " + canonical + " "} {
		if got := normalizeOrgSpelling(spelling); got != canonical {
			t.Errorf("normalizeOrgSpelling(%q) = %q, want %q", spelling, got, canonical)
		}
	}
	if got := normalizeOrgSpelling(" not-a-uuid "); got != "not-a-uuid" {
		t.Errorf("non-UUID = %q", got)
	}
}
