package dbacl

import "testing"

// CHAOS-12244: the retention job runs as the runtime role, so the runtime
// declaration holds every privilege the job's statements need.
func TestRuntimeGrantsHoldTheRetentionPrivileges(t *testing.T) {
	held := map[string]map[string]bool{}
	for _, grant := range RuntimeGrants {
		if held[grant.Table] == nil {
			held[grant.Table] = map[string]bool{}
		}
		for _, privilege := range grant.Privileges {
			held[grant.Table][privilege] = true
		}
	}
	if len(RetentionPrivileges) == 0 {
		t.Fatal("RetentionPrivileges is empty: nothing would be checked")
	}
	for _, need := range RetentionPrivileges {
		for _, privilege := range need.Privileges {
			if !held[need.Table][privilege] {
				t.Errorf("RuntimeGrants lacks %s on %s, which the retention job needs", privilege, need.Table)
			}
		}
	}
}
