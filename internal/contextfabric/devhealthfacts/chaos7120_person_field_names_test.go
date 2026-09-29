package devhealthfacts_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// CHAOS-7120, decision K1: no fact kind declares a field or table column
// that names a person. The direct read tool serves only declared names, so a
// person field could reach a client only through a declaration; this test
// scans every declared field and column of every registered provider.
//
// A name matches when one of its "_"-separated tokens is a banned token, or
// it contains a banned phrase. Tokens, not substrings, so "owned_*" or
// "code_ownership_gini" do not match "owner". "member" is not banned:
// actual_completion's member_kind names the rollup's member entity kind
// ("work_item"), and membership facts are repository membership.
var personTokens = map[string]bool{
	"author": true, "authors": true, "assignee": true, "assignees": true,
	"reviewer": true, "reviewers": true, "reporter": true, "reporters": true,
	"creator": true, "creators": true, "committer": true, "committers": true,
	"user": true, "users": true, "username": true, "usernames": true,
	"login": true, "logins": true, "email": true, "emails": true,
	"person": true, "people": true, "persons": true, "actor": true, "actors": true,
	"requester": true, "approver": true, "approvers": true, "owner": true, "owners": true,
	"handle": true, "avatar": true, "developer": true, "developers": true,
	"engineer": true, "engineers": true, "contributor": true, "contributors": true,
	"individual": true, "employee": true, "employees": true,
}

var personPhrases = []string{
	"display_name", "full_name", "first_name", "last_name", "real_name",
	"merged_by", "closed_by", "created_by", "opened_by", "resolved_by", "updated_by", "assigned_to",
}

func personLikeName(name string) (string, bool) {
	lower := strings.ToLower(name)
	for _, token := range strings.Split(lower, "_") {
		if personTokens[token] {
			return token, true
		}
	}
	for _, phrase := range personPhrases {
		if strings.Contains(lower, phrase) {
			return phrase, true
		}
	}
	return "", false
}

func TestCHAOS7120NoDeclaredFieldOrColumnNamesAPerson(t *testing.T) {
	scanned, kinds := 0, 0
	for _, provider := range devhealthfacts.NewProviders(&fakeClient{}) {
		capability := provider.Capability()
		kinds++
		if len(capability.Fields) == 0 {
			t.Errorf("%s: declares no fields; the scan would be vacuous for it", capability.Kind)
		}
		for _, field := range capability.Fields {
			scanned++
			if match, bad := personLikeName(field.Name); bad {
				t.Errorf("%s: declared field %q names a person (%q)", capability.Kind, field.Name, match)
			}
			for _, column := range field.Columns {
				scanned++
				if match, bad := personLikeName(column.Name); bad {
					t.Errorf("%s: declared column %s.%s names a person (%q)", capability.Kind, field.Name, column.Name, match)
				}
			}
		}
	}
	if kinds != 21 || scanned == 0 {
		t.Fatalf("scanned %d names across %d kinds, want all 21 kinds", scanned, kinds)
	}
}

// The matcher itself: tokens and phrases match, look-alikes do not.
func TestCHAOS7120PersonNameMatcher(t *testing.T) {
	for name, want := range map[string]bool{
		"author": true, "pr_author_login": true, "assignee_id": true, "reviewer": true,
		"reporter_email": true, "display_name": true, "merged_by": true, "owner_login": true,
		"team_name": false, "owned_repository_count": false, "code_ownership_gini": false,
		"repository_name": false, "identity_count": false, "blocked_by_work_item_id": false,
		"member_kind": false,
	} {
		if _, got := personLikeName(name); got != want {
			t.Errorf("personLikeName(%q) = %v, want %v", name, got, want)
		}
	}
}
