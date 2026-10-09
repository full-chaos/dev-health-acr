package devhealthfacts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func TestMembershipScopeCacheServesAnEntryOnlyWithinItsTTLAndForItsRun(t *testing.T) {
	t.Parallel()
	cache := newMembershipScopeCache()
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	cache.put("org", "run-1", []string{"a"}, at)

	if _, ok := cache.get("org", "run-1", at.Add(membershipScopeTTL-time.Second)); !ok {
		t.Fatal("an entry inside its TTL must be served")
	}
	if _, ok := cache.get("org", "run-1", at.Add(membershipScopeTTL)); ok {
		t.Fatal("an entry at its TTL must be reloaded: a set read while a run's rows were still becoming visible may not stay")
	}
	if _, ok := cache.get("org", "run-2", at); ok {
		t.Fatal("an entry must never be served for another run")
	}
	if _, ok := cache.get("other-org", "run-1", at); ok {
		t.Fatal("an entry must never be served for another organization")
	}
}

type scopeStubScanner struct {
	rows [][]any
	next int
}

func (s *scopeStubScanner) Next() bool { s.next++; return s.next <= len(s.rows) }
func (s *scopeStubScanner) Scan(dest ...any) error {
	row := s.rows[s.next-1]
	for i, d := range dest {
		switch v := d.(type) {
		case *string:
			*v = row[i].(string)
		case *[]string:
			*v = row[i].([]string)
		default:
			return errors.New("unsupported scan destination")
		}
	}
	return nil
}
func (s *scopeStubScanner) Err() error   { return nil }
func (s *scopeStubScanner) Close() error { return nil }

// scopeStubClient answers the run read at once and holds the unit-ids read
// until release is closed, failing it with the context's error if that
// context is cancelled first.
type scopeStubClient struct {
	started chan struct{}
	release chan struct{}
}

func (c *scopeStubClient) Query(ctx context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, "work_unit_membership_runs") {
		return &scopeStubScanner{rows: [][]any{{"run-1"}}}, nil
	}
	select {
	case c.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.release:
		return &scopeStubScanner{rows: [][]any{{[]string{"wu-1", "wu-2"}}}}, nil
	}
}

// One caller's cancellation must not fail the callers sharing its load.
func TestMembershipScopeLoadSurvivesTheFirstCallersCancellation(t *testing.T) {
	t.Parallel()
	client := &scopeStubClient{started: make(chan struct{}, 4), release: make(chan struct{})}
	provider := newInvestmentProvider(client)

	first, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := provider.resolveMembershipScope(first, "org-a")
		firstDone <- err
	}()
	<-client.started

	second := make(chan struct {
		scope membershipScope
		err   error
	}, 1)
	go func() {
		scope, err := provider.resolveMembershipScope(context.Background(), "org-a")
		second <- struct {
			scope membershipScope
			err   error
		}{scope, err}
	}()
	// The second caller joins the same load before the first one leaves.
	time.Sleep(50 * time.Millisecond)
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled caller returned %v, want context.Canceled", err)
	}
	close(client.release)
	got := <-second
	if got.err != nil {
		t.Fatalf("a healthy caller sharing the load failed with %v after the first caller was cancelled", got.err)
	}
	if got.scope.mode != membershipScopeIDs || strings.Join(got.scope.ids, ",") != "wu-1,wu-2" {
		t.Fatalf("scope = %+v, want the run's ids", got.scope)
	}
}
