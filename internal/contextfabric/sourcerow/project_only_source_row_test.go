package sourcerow_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299 r1 F2 at resolver.go readTargets: a project-only principal with
// an empty repository list reads no source row on the production path, and
// is served none; the unrestricted principal's same read serves the row.
func TestAProjectOnlyPrincipalReadsNoSourceRow(t *testing.T) {
	for _, tc := range []struct {
		principal storage.Principal
		served    bool
	}{{storage.Principal{OrgID: orgID, ProjectScopes: []string{"project.v2:github:P-1"}}, false}, {unrestricted, true}} {
		w := newWorld()
		w.repos[grantedID] = grantedRep
		locator := "acr:v1:pull-request:7"
		w.addRow(grantedID, "pull_requests.v1", locator, catalogRow("pull_requests.v1", locator, "pull_request", "7", grantedRep))
		_, decision := resolver(t, w).ResolveSourceRow(context.Background(), tc.principal, "pull-request", grantedID+":7")
		if got := decision.Reason == contextfabric.SourceRowServed; got != tc.served {
			t.Errorf("%v: served = %v, want %v (%+v)", tc.principal.ProjectScopes, got, tc.served, decision)
		}
		if !tc.served && len(w.reads) != 0 {
			t.Errorf("%v: %d source rows read for a project-only principal, want none", tc.principal.ProjectScopes, len(w.reads))
		}
	}
}
