package orgpurge

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

type fakeSet struct {
	allowed    bool
	refreshErr error
}

func (f fakeSet) RefreshOrgs(context.Context) error { return f.refreshErr }
func (f fakeSet) AllowsOrg(string) bool             { return f.allowed }

type fakeEntitlements struct{ err error }

func (f fakeEntitlements) HasEntitlement(context.Context, string, string) (bool, error) {
	return false, f.err
}

type panicLocker struct{ t *testing.T }

func (l panicLocker) Lock(context.Context, string) (func() error, error) {
	l.t.Fatal("refused run must not reach the lock")
	return nil, nil
}

type noGraph struct{}

func (noGraph) CountOrganizationGraphs(context.Context, string) (int, error)  { return 0, nil }
func (noGraph) DeleteOrganizationGraphs(context.Context, string) (int, error) { return 0, nil }

const testOrg = "11111111-2222-3333-4444-555555555555"

func refusalPurger(t *testing.T, set fakeSet, ent fakeEntitlements) *Purger {
	return &Purger{DB: &sql.DB{}, Graph: noGraph{}, Set: set, Entitlements: ent, Locker: panicLocker{t}}
}

func TestRunRefusesBeforeTouchingAnything(t *testing.T) {
	good := Options{AttestDeleted: true, ConfirmOrgKey: OrgKey(testOrg)}
	cases := map[string]struct {
		purger *Purger
		org    string
		opts   Options
	}{
		"not a uuid":            {refusalPurger(t, fakeSet{}, fakeEntitlements{err: errors.New("404")}), "acme", good},
		"no attestation":        {refusalPurger(t, fakeSet{}, fakeEntitlements{err: errors.New("404")}), testOrg, Options{ConfirmOrgKey: OrgKey(testOrg)}},
		"wrong confirm hash":    {refusalPurger(t, fakeSet{}, fakeEntitlements{err: errors.New("404")}), testOrg, Options{AttestDeleted: true, ConfirmOrgKey: "0000000000000000"}},
		"in the projector set":  {refusalPurger(t, fakeSet{allowed: true}, fakeEntitlements{err: errors.New("404")}), testOrg, good},
		"discovery failed":      {refusalPurger(t, fakeSet{refreshErr: errors.New("clickhouse down")}, fakeEntitlements{err: errors.New("404")}), testOrg, good},
		"dev health knows org":  {refusalPurger(t, fakeSet{}, fakeEntitlements{}), testOrg, good},
		"dry run still guarded": {refusalPurger(t, fakeSet{allowed: true}, fakeEntitlements{err: errors.New("404")}), testOrg, Options{DryRun: true}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tc.purger.Run(context.Background(), tc.org, tc.opts)
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("Run() error = %v, want ErrRefused", err)
			}
		})
	}
}

func TestOrgKeyIsStableAndHidesTheOrganization(t *testing.T) {
	key := OrgKey(testOrg)
	if len(key) != 16 || key != OrgKey(testOrg) || key == OrgKey("66666666-2222-3333-4444-555555555555") {
		t.Fatalf("OrgKey() = %q", key)
	}
}
