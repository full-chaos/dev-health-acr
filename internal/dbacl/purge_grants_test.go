package dbacl_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/orgpurge"
	"github.com/full-chaos/dev-health-acr/internal/dbacl"
)

// The purge role holds what the purge's statements need and nothing else. The
// need is derived from the SQL Run executes (orgpurge.Privileges), not from a
// list kept by hand; the only additions are dbacl.PurgeExtras.
func TestPurgeGrantsEqualThePrivilegesTheStatementsNeedPlusTheNamedExtras(t *testing.T) {
	want := map[string]bool{}
	for table, privileges := range orgpurge.Privileges() {
		for _, privilege := range privileges {
			want[table+"/"+privilege] = true
		}
	}
	for extra, reason := range dbacl.PurgeExtras {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("extra %s has no reason", extra)
		}
		want[extra] = true
	}
	held := map[string]bool{}
	for _, grant := range dbacl.PurgeGrants {
		if grant.Feature != dbacl.AlwaysOn {
			t.Errorf("%s: the purge role has no optional-feature rows", grant.Table)
		}
		for _, privilege := range grant.Privileges {
			held[grant.Table+"/"+privilege] = true
		}
	}
	var missing, surplus []string
	for key := range want {
		if !held[key] {
			missing = append(missing, key)
		}
	}
	for key := range held {
		if !want[key] {
			surplus = append(surplus, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(surplus)
	if len(missing) > 0 {
		t.Errorf("a purge statement needs privileges the purge role is not granted: %v", missing)
	}
	if len(surplus) > 0 {
		t.Errorf("the purge role is granted privileges no purge statement needs and no extra names: %v", surplus)
	}
}
