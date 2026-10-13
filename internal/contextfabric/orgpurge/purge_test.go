package orgpurge

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

type fakeSet struct {
	members    []string
	refreshErr error
}

func (f fakeSet) RefreshOrgs(context.Context) error { return f.refreshErr }
func (f fakeSet) EffectiveOrgs() []string           { return f.members }

type fakeEntitlements struct {
	answers bool
	err     error
}

func (f fakeEntitlements) OrganizationUnknown(context.Context, string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return !f.answers, nil
}

type heldLocker struct{ seen *string }

func (h heldLocker) Lock(_ context.Context, org string) (func() error, error) {
	if h.seen != nil {
		*h.seen = org
	}
	return nil, errors.New("held")
}

type countingGraph struct{ calls int }

func (g *countingGraph) CountOrganizationGraphs(context.Context, string) (int, error) {
	g.calls++
	return 0, nil
}
func (g *countingGraph) DeleteOrganizationGraphs(context.Context, string) (int, error) {
	g.calls++
	return 0, nil
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
	good := Options{ConfirmOrgKey: OrgKey(testOrg)}
	gone := fakeEntitlements{}
	cases := map[string]struct {
		purger *Purger
		org    string
		opts   Options
	}{
		"not a uuid":             {refusalPurger(t, fakeSet{}, gone), "acme", good},
		"wrong confirm hash":     {refusalPurger(t, fakeSet{}, gone), testOrg, Options{ConfirmOrgKey: "0000000000000000"}},
		"in the projector set":   {refusalPurger(t, fakeSet{members: []string{testOrg}}, gone), testOrg, good},
		"set holds upper case":   {refusalPurger(t, fakeSet{members: []string{strings.ToUpper(testOrg)}}, gone), testOrg, good},
		"set holds braced upper": {refusalPurger(t, fakeSet{members: []string{"{" + strings.ToUpper(testOrg) + "}"}}, gone), testOrg, good},
		"input upper, set lower": {refusalPurger(t, fakeSet{members: []string{testOrg}}, gone), strings.ToUpper(testOrg), good},
		"discovery failed":       {refusalPurger(t, fakeSet{refreshErr: errors.New("clickhouse down")}, gone), testOrg, good},
		"dev health unavailable": {refusalPurger(t, fakeSet{}, fakeEntitlements{err: errors.New("outage")}), testOrg, good},
		"dev health answers 200": {refusalPurger(t, fakeSet{}, fakeEntitlements{answers: true}), testOrg, good},
		"dry run still guarded":  {refusalPurger(t, fakeSet{members: []string{testOrg}}, gone), testOrg, Options{DryRun: true}},
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

func TestRunRefusesWhenAnotherProjectorHoldsTheOrganizationLock(t *testing.T) {
	graph := &countingGraph{}
	purger := &Purger{DB: &sql.DB{}, Graph: graph, Set: fakeSet{}, Entitlements: fakeEntitlements{}, Locker: heldLocker{}}
	_, err := purger.Run(context.Background(), testOrg, Options{ConfirmOrgKey: OrgKey(testOrg)})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Run() error = %v, want ErrLocked", err)
	}
	if graph.calls != 0 {
		t.Fatalf("graph touched %d times while the lock was held", graph.calls)
	}
}

func TestRunUsesTheCanonicalUUIDSpelling(t *testing.T) {
	seen := ""
	purger := &Purger{DB: &sql.DB{}, Graph: noGraph{}, Set: fakeSet{}, Entitlements: fakeEntitlements{}, Locker: heldLocker{seen: &seen}}
	_, _ = purger.Run(context.Background(), "{"+strings.ToUpper(testOrg)+"}", Options{ConfirmOrgKey: OrgKey(testOrg)})
	if seen != testOrg {
		t.Fatalf("lock taken on %q, want the canonical lowercase %q", seen, testOrg)
	}
}
