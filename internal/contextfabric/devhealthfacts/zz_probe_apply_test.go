package devhealthfacts

import (
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"os"
	"testing"
)

func TestProbeEveryMutationApplies(t *testing.T) {
	for _, id := range []string{"S1_pr_type", "S2_pr_repository", "S3_issue_type", "S4a_key_first", "S4b_key_second", "S5_reverse_branch", "S6_anchor_exists", "S7_pull_count_type", "S8_status", "S9_time", "S10_authorization"} {
		os.Setenv("ACR_PROBE_MUTATION", "")
		base, _ := workItemRepositoryMembershipStatementFor(workItemRepositoryAuthorization(probePrincipal(), nil), 200, "completed_at")
		os.Setenv("ACR_PROBE_MUTATION", id)
		got, _ := workItemRepositoryMembershipStatementFor(workItemRepositoryAuthorization(probePrincipal(), nil), 200, "completed_at")
		os.Setenv("ACR_PROBE_MUTATION", "")
		if got == base {
			t.Errorf("%s changed nothing", id)
		}
		t.Logf("%s: %d -> %d bytes", id, len(base), len(got))
	}
}

func probePrincipal() storage.Principal {
	return storage.Principal{OrgID: "probe", RepositoryScopes: []string{"acme/api"}}
}
