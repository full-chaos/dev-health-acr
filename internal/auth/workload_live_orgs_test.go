package auth

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/authverify"
)

// fakeOrgPresence answers OrganizationKnownNow from a map: gone ids are
// ErrOrganizationGone, outage ids are an unreadable answer, every other id is
// live. calls records the organizations it was asked about.
type fakeOrgPresence struct {
	gone, outage map[string]bool
	calls        []string
}

var errFakeOrgOutage = errors.New("dev health unreachable")

func (p *fakeOrgPresence) OrganizationKnown(ctx context.Context, orgID string) error {
	return p.OrganizationKnownNow(ctx, orgID)
}

func (p *fakeOrgPresence) OrganizationKnownNow(_ context.Context, orgID string) error {
	p.calls = append(p.calls, orgID)
	switch {
	case p.outage[orgID]:
		return errFakeOrgOutage
	case p.gone[orgID]:
		return ErrOrganizationGone
	}
	return nil
}

func liveOrgsRow(id string) storage.WorkloadBinding {
	return storage.WorkloadBinding{BindingID: id, OrgScope: storage.WorkloadOrgScopeLiveOrgs, Role: "revoke", RepositoryScopes: []string{}}
}

func orgRow(id, org, role string) storage.WorkloadBinding {
	return storage.WorkloadBinding{BindingID: id, OrgID: org, OrgScope: storage.WorkloadOrgScopeOrg, Role: role, RepositoryScopes: []string{"*"}}
}

func liveOrgsResolve(t *testing.T, presence OrganizationPresence, selector string, rows ...storage.WorkloadBinding) (authverify.WorkloadBinding, error) {
	t.Helper()
	resolver, err := NewGrantResolver(&fakeWorkloadBindingStore{bindings: rows}, presence)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if selector != "" {
		ctx = WithWorkloadOrgSelector(ctx, selector)
	}
	return resolver.Resolve(ctx, authverify.SubjectIdentity{})
}

func TestLiveOrgs_aLiveOrganizationGetsTheRevokeScopeInThatOrganization(t *testing.T) {
	presence := &fakeOrgPresence{}
	got, err := liveOrgsResolve(t, presence, selectorOrgA, liveOrgsRow("wlb_live"))
	if err != nil {
		t.Fatal(err)
	}
	want := authverify.WorkloadBinding{BindingID: "wlb_live", OrgID: selectorOrgA, GrantedScopes: []string{ScopeCredentialLifecycleRevoke}, RepositoryScopes: []string{"*"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("binding = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(presence.calls, []string{selectorOrgA}) {
		t.Fatalf("organization reads = %v, want one fresh read of the selected organization", presence.calls)
	}
}

// acp condition 2: whatever the row holds, the grant is the revoke scope and
// the fixed repository scope.
func TestLiveOrgs_aWildcardOnTheRowStillYieldsTheRevokeScopeOnly(t *testing.T) {
	for name, held := range map[string][]string{"wildcard": {"*"}, "one repository": {"acme/api"}, "owner": {"acme/*"}, "none": nil} {
		row := liveOrgsRow("wlb_live")
		row.RepositoryScopes = held
		got, err := liveOrgsResolve(t, &fakeOrgPresence{}, selectorOrgA, row)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got.GrantedScopes, []string{ScopeCredentialLifecycleRevoke}) || !reflect.DeepEqual(got.RepositoryScopes, []string{"*"}) {
			t.Fatalf("%s: granted = %v repository = %v, want the revoke scope and the fixed repository scope", name, got.GrantedScopes, got.RepositoryScopes)
		}
	}
	got, _ := liveOrgsResolve(t, &fakeOrgPresence{}, selectorOrgA, liveOrgsRow("wlb_live"))
	got.RepositoryScopes[0] = "mutated"
	if liveOrgsRepositoryScopes[0] != "*" {
		t.Fatal("a caller mutating the grant changed the fixed repository scope")
	}
}

func TestLiveOrgs_aNonRevokeRoleOnTheRowIsNotBound(t *testing.T) {
	for _, role := range []string{"read", "ops", "reconcile", ""} {
		row := liveOrgsRow("wlb_live")
		row.Role = role
		presence := &fakeOrgPresence{}
		got, err := liveOrgsResolve(t, presence, selectorOrgA, row)
		if !errors.Is(err, ErrWorkloadOrgNotBound) || got.BindingID != "" || len(presence.calls) != 0 {
			t.Fatalf("role %q: binding = %#v err = %v reads = %v, want ErrWorkloadOrgNotBound before any read", role, got, err, presence.calls)
		}
	}
}

func TestLiveOrgs_aGoneOrUnknownOrganizationIsNotBound(t *testing.T) {
	// Dev Health answers both with the coded 404: the organization was
	// deleted (gone) or never existed (unknown).
	for name, org := range map[string]string{"gone": selectorOrgB, "unknown": selectorOrgC} {
		presence := &fakeOrgPresence{gone: map[string]bool{selectorOrgB: true, selectorOrgC: true}}
		got, err := liveOrgsResolve(t, presence, org, liveOrgsRow("wlb_live"))
		if !errors.Is(err, ErrWorkloadOrgNotBound) || errors.Is(err, ErrWorkloadOrgCheckUnavailable) || got.BindingID != "" {
			t.Fatalf("%s: binding = %#v err = %v, want ErrWorkloadOrgNotBound and no binding", name, got, err)
		}
	}
}

func TestLiveOrgs_anUnreadableOrganizationAnswerIsUnavailableNotNotBound(t *testing.T) {
	presence := &fakeOrgPresence{outage: map[string]bool{selectorOrgA: true}}
	got, err := liveOrgsResolve(t, presence, selectorOrgA, liveOrgsRow("wlb_live"))
	if !errors.Is(err, ErrWorkloadOrgCheckUnavailable) || errors.Is(err, ErrWorkloadOrgNotBound) || got.BindingID != "" {
		t.Fatalf("binding = %#v err = %v, want ErrWorkloadOrgCheckUnavailable and no binding", got, err)
	}
}

func TestLiveOrgs_noOrganizationCheckLeavesTheRowUnusable(t *testing.T) {
	got, err := liveOrgsResolve(t, nil, selectorOrgA, liveOrgsRow("wlb_live"))
	if !errors.Is(err, ErrWorkloadOrgNotBound) || got.BindingID != "" {
		t.Fatalf("binding = %#v err = %v, want ErrWorkloadOrgNotBound in local entitlement mode", got, err)
	}
	var typedNil *fakeOrgPresence
	got, err = liveOrgsResolve(t, typedNil, selectorOrgA, liveOrgsRow("wlb_live"))
	if !errors.Is(err, ErrWorkloadOrgNotBound) || got.BindingID != "" {
		t.Fatalf("typed nil: binding = %#v err = %v, want ErrWorkloadOrgNotBound", got, err)
	}
}

// A row bound to the selected organization wins over the live_orgs row. A
// revoke row is bound with no read, whatever the organization's state; a row
// of any other role reads the organization fresh and is not bound when it is
// gone.
func TestLiveOrgs_aRowBoundToTheSelectedOrganizationWins(t *testing.T) {
	presence := &fakeOrgPresence{gone: map[string]bool{selectorOrgA: true}}
	got, err := liveOrgsResolve(t, presence, selectorOrgA, orgRow("wlb_a", selectorOrgA, "revoke"), liveOrgsRow("wlb_live"))
	if err != nil || got.BindingID != "wlb_a" || got.OrgID != selectorOrgA || !reflect.DeepEqual(got.GrantedScopes, RoleScopes("revoke")) {
		t.Fatalf("binding = %#v err = %v, want the org row wlb_a", got, err)
	}
	if len(presence.calls) != 0 {
		t.Fatalf("organization reads = %v, want none for a revoke org row", presence.calls)
	}

	got, err = liveOrgsResolve(t, presence, selectorOrgA, orgRow("wlb_a", selectorOrgA, "read"), liveOrgsRow("wlb_live"))
	if !errors.Is(err, ErrWorkloadOrgNotBound) || got.BindingID != "" {
		t.Fatalf("read org row of a gone organization: binding = %#v err = %v, want ErrWorkloadOrgNotBound", got, err)
	}
	if len(presence.calls) != 1 {
		t.Fatalf("organization reads = %v, want one fresh read for a read org row", presence.calls)
	}
}

func TestLiveOrgs_aDisabledOrgRowFallsToTheLiveOrgsRow(t *testing.T) {
	disabledAt := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	row := orgRow("wlb_a", selectorOrgA, "revoke")
	row.DisabledAt = &disabledAt
	got, err := liveOrgsResolve(t, &fakeOrgPresence{}, selectorOrgA, row, liveOrgsRow("wlb_live"))
	if err != nil || got.BindingID != "wlb_live" || got.OrgID != selectorOrgA {
		t.Fatalf("binding = %#v err = %v, want the live_orgs row", got, err)
	}
}

func TestLiveOrgs_aDisabledLiveOrgsRowIsNotFound(t *testing.T) {
	disabledAt := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	row := liveOrgsRow("wlb_live")
	row.DisabledAt = &disabledAt
	presence := &fakeOrgPresence{}
	got, err := liveOrgsResolve(t, presence, selectorOrgA, row)
	if !errors.Is(err, authverify.ErrWorkloadBindingNotFound) || got.BindingID != "" || len(presence.calls) != 0 {
		t.Fatalf("binding = %#v err = %v reads = %v, want ErrWorkloadBindingNotFound", got, err, presence.calls)
	}
	// Beside an enabled row of another organization it reads as no binding for the selection.
	got, err = liveOrgsResolve(t, presence, selectorOrgA, orgRow("wlb_b", selectorOrgB, "revoke"), row)
	if !errors.Is(err, ErrWorkloadOrgNotBound) || got.BindingID != "" || len(presence.calls) != 0 {
		t.Fatalf("binding = %#v err = %v reads = %v, want ErrWorkloadOrgNotBound", got, err, presence.calls)
	}
}

func TestLiveOrgs_noSelectorIsRefused(t *testing.T) {
	for name, rows := range map[string][]storage.WorkloadBinding{
		"live_orgs row alone":       {liveOrgsRow("wlb_live")},
		"live_orgs row and org row": {orgRow("wlb_a", selectorOrgA, "revoke"), liveOrgsRow("wlb_live")},
	} {
		presence := &fakeOrgPresence{}
		got, err := liveOrgsResolve(t, presence, "", rows...)
		if !errors.Is(err, ErrWorkloadOrgSelectorRequired) || got.BindingID != "" || len(presence.calls) != 0 {
			t.Fatalf("%s: binding = %#v err = %v, want ErrWorkloadOrgSelectorRequired", name, got, err)
		}
	}
}
