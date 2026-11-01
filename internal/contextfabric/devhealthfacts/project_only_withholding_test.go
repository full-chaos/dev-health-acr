package devhealthfacts_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func projectOnlyFactPrincipal() storage.Principal {
	return storage.Principal{OrgID: "org-1", ProjectScopes: []string{"project.v2:linear:proj-1"}}
}

// source_health reads an operation, not a statement: a project-only caller's
// organization read makes no call and serves no fact; an unrestricted
// caller's makes one.
//
// CHAOS-12330: the registry refuses every kind to a project-only caller before
// a provider runs, so this is a provider-level pin of the restricted arm: an
// emptied subject list is what left `available` with no fact, which read as a
// complete answer.
func TestSourceHealthIsWithheldFromAProjectOnlyCaller(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		principal storage.Principal
		calls     int
	}{{projectOnlyFactPrincipal(), 0}, {storage.Principal{OrgID: "org-1"}, 1}} {
		caller := &fakeOperationCaller{outcome: devhealthfacts.OperationOutcome{Served: true, Complete: true, Data: sourceHealthData(
			`{"provider":"github","scope":"git","lastSyncAt":"2026-10-08T01:00:00Z","lastFailure":null}`)}}
		result := readSourceHealth(t, sourceHealthProvider(t, sourceHealthHolder(caller)), tc.principal, organizationSubject("org-1"))
		if caller.calls != tc.calls || (tc.calls == 0 && len(result.Facts) != 0) {
			t.Errorf("%v: %d operation calls and %d facts, want %d calls", tc.principal.ProjectScopes, caller.calls, len(result.Facts), tc.calls)
		}
		if tc.calls != 0 {
			continue
		}
		if result.State != contextfabric.SourceNotApplicable {
			t.Errorf("a project-only organization read has state %q, want %q", result.State, contextfabric.SourceNotApplicable)
		}
		if result.Reason == "" {
			t.Error("a project-only organization read has no reason")
		}
	}
}

// CHAOS-12299 r1 F2, the codex repro at provider level: a member work item's
// blocker and required-child reads, and every other work-item read, issue for
// a project-only caller exactly the statements and bindings a grant naming no
// valid repository issues, a grant that denies every row. With the empty
// list read as "all", a member's blocker to a non-member was served.
func TestAProjectOnlyWorkItemReadIsTheDenyRead(t *testing.T) {
	t.Parallel()
	subject := workItemSubject("repo-1", "WI-MEMBER")
	for _, kind := range []contextfabric.FactKind{
		contextfabric.FactBlockers, contextfabric.FactRequiredChildren, contextfabric.FactActualCompletion,
		contextfabric.FactStatus, contextfabric.FactWork,
	} {
		read := func(principal storage.Principal) []capturedQuery {
			client := &fakeClient{}
			provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
			if _, err := provider.ReadFacts(context.Background(), principal, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind: kind, Subjects: []contextfabric.SubjectRef{subject},
			}); err != nil {
				t.Fatalf("%s: ReadFacts() error = %v", kind, err)
			}
			return client.queries
		}
		got := read(projectOnlyFactPrincipal())
		want := read(storage.Principal{OrgID: "org-1", RepositoryScopes: []string{" "}})
		if len(want) == 0 {
			t.Errorf("%s: the deny read issued no statement; this pin lost its control", kind)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: a project-only work-item read differs from the deny read (%d vs %d statements)", kind, len(got), len(want))
		}
		organizationWide := read(storage.Principal{OrgID: "org-1"})
		if reflect.DeepEqual(organizationWide, want) {
			t.Errorf("%s: the organization-wide read equals the deny read; this pin cannot tell them apart", kind)
		}
	}
}
