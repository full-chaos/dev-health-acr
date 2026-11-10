package storage

import (
	"os"
	"strings"
	"testing"
)

// The closed reason set is the IN list of the newest migration that sets the
// column's CHECK: a reason missing there fails at the database.
func TestBrowserFamilyRevokeReasonVocabularyMatchesTheMigration(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/postgres/0073_browser_token_family_org_deprovisioned.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "revoked_reason IN (")
	if start < 0 {
		t.Fatal("migration 0073 has no revoked_reason IN list")
	}
	list := text[start+len("revoked_reason IN (") : start+strings.Index(text[start:], ")")]
	listed := map[string]bool{}
	for _, item := range strings.Split(list, ",") {
		listed[strings.Trim(strings.TrimSpace(item), "'")] = true
	}
	vocabulary := BrowserFamilyRevokeReasonVocabulary()
	if len(listed) != len(vocabulary) {
		t.Fatalf("migration lists %v, vocabulary has %v", listed, vocabulary)
	}
	for _, reason := range vocabulary {
		if !listed[string(reason)] || !reason.Valid() {
			t.Fatalf("reason %q: in migration %v, valid %v", reason, listed[string(reason)], reason.Valid())
		}
	}
}

func TestBrowserFamilyReasonOf(t *testing.T) {
	for text, want := range map[string]BrowserFamilyRevokeReason{
		"browser family reuse":             BrowserFamilyRevokeReuse,
		"browser family sign_out":          BrowserFamilyRevokeSignOut,
		"browser family org_deprovisioned": BrowserFamilyRevokeOrgDeprovisioned,
		"browser family member_revoked":    BrowserFamilyRevokeMemberRevoked,
		"browser family other":             BrowserFamilyRevokeMemberRevoked,
		"":                                 BrowserFamilyRevokeMemberRevoked,
		"operator said so":                 BrowserFamilyRevokeMemberRevoked,
	} {
		if got := BrowserFamilyReasonOf(text); got != want {
			t.Fatalf("BrowserFamilyReasonOf(%q) = %q, want %q", text, got, want)
		}
	}
}
