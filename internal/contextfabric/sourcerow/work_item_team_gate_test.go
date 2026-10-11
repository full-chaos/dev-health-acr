package sourcerow_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A work-item-team.v2 row names a work item (read in the repository the
// caller may read) and a team. The team endpoint is decided by the direct
// data tools' subject gate (ownership reach, never membership), live on every
// read, for every caller, together with the work item's repository.
func TestWorkItemTeamV2TeamEndpointIsDecidedByTheSubjectGate(t *testing.T) {
	kind := contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2
	values := []string{zeroRepoID, "jira:ABC-1", "ari:cloud:identity::team/1", "native_team"}
	const query = "work_item_teams.v2"
	team := contextfabric.TeamCanonicalID(values[2])
	for _, tc := range []struct {
		name      string
		principal storage.Principal
		repo      contractsv1.ResolvedScope
		outcome   directread.SubjectOutcome
		served    bool
	}{
		{"restricted, repository granted, team owned", restricted, scope(grantedID, grantedRep), directread.SubjectAdmitted, true},
		{"restricted, repository granted, team not reached", restricted, scope(grantedID, grantedRep), directread.SubjectDenied, false},
		{"restricted, repository granted, team absent", restricted, scope(grantedID, grantedRep), directread.SubjectAbsent, false},
		{"restricted, repository not granted, team owned", restricted, scope(secretID, secretRep), directread.SubjectAdmitted, false},
		{"unrestricted, team owned", unrestricted, scope(grantedID, grantedRep), directread.SubjectAdmitted, true},
		{"unrestricted, team not reached", unrestricted, scope(grantedID, grantedRep), directread.SubjectDenied, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, id := mustMint(t, kind, values...)
			w := newWorld()
			w.discovered[workItemDiscovery+"|jira:ABC-1"] = []contractsv1.ResolvedScope{tc.repo}
			w.gate = &fakeGate{world: w, outcomes: map[string]directread.SubjectOutcome{team: tc.outcome}}
			w.addRow(tc.repo.RepoID, query, ref, catalogRow(query, ref, "work_item_team", id, tc.repo.RepoSlug))
			_, decision := resolver(t, w).ResolveSourceRow(context.Background(), tc.principal, string(kind), id)
			if got := decision.Reason == contextfabric.SourceRowServed; got != tc.served {
				t.Fatalf("served %v, want %v (%+v, calls %v)", got, tc.served, decision, w.calls)
			}
			want := []string{
				fmt.Sprintf("discover(%s,work_item_repositories,jira:ABC-1)", orgID),
				fmt.Sprintf("gate(%s,team,%s)", orgID, team),
			}
			if tc.served {
				want = append(want, fmt.Sprintf("row(%s,%s,%s,%s,)", orgID, tc.repo.RepoID, query, ref))
			}
			if !reflect.DeepEqual(w.calls, want) {
				t.Fatalf("calls = %v, want %v", w.calls, want)
			}
			if !tc.served && decision.Reason != contextfabric.SourceRowNoRow {
				t.Fatalf("refusal = %+v", decision)
			}
		})
	}
}
