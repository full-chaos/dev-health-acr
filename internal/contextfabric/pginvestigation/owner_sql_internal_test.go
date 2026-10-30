package pginvestigation

import "testing"

// Save's owner match is a constant (insertResultSQL is one); it must stay the
// same predicate ownerMatchSQL builds everywhere else (CHAOS-12091).
func TestSubmissionOwnerMatchIsOwnerMatchSQL(t *testing.T) {
	want := ownerMatchSQL("owner_auth_method", "owner_subject", "owner_user_id", "owner_surface", "$30", "$31", "$32", "$33")
	if submissionOwnerMatch != want {
		t.Fatalf("submissionOwnerMatch =\n%s\nwant ownerMatchSQL's\n%s", submissionOwnerMatch, want)
	}
}
