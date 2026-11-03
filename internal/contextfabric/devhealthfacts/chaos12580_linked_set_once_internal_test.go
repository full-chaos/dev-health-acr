package devhealthfacts

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// chaos12580_linked_set_once_internal_test.go pins that a single-team read
// reads the team's linked pull request set once: the team linked mix and the
// per-repository linked mix of repository_theme_breakdown take the same set.
// The container test TestTeamLinkedMixMemoryWithProdWidthUnitIDs counted two
// set statements on one served read before the set was shared.

type setCountingClient struct {
	linkStubClient
	mu   sync.Mutex
	sets int
}

func (c *setCountingClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, "groupArray(r), groupArray(n)") {
		c.mu.Lock()
		c.sets++
		c.mu.Unlock()
	}
	return c.linkStubClient.Query(ctx, statement, bindings)
}

func TestCHAOS12580OneTeamReadReadsItsLinkedSetOnce(t *testing.T) {
	t.Parallel()
	client := &setCountingClient{linkStubClient: linkStubClient{setRows: [][]any{{[]string{"r1"}, []string{"1"}, []string{"t1"}, []string{"i1"}, []uint8{3}}}}}
	p := newInvestmentProvider(client)
	ctx := withLinkedSetMemo(context.WithValue(context.Background(), loadedMembershipScopeKey{}, loadedMembershipScope{orgID: "org-1", runID: "run-1", scope: membershipScope{mode: membershipScopeNone}}))
	current := factTimeBound{active: true, hasStart: true, start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), end: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	prior := factTimeBound{active: true, hasStart: true, start: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), end: current.start}
	if _, err := p.readLinkedPullRequestMixRows(ctx, "org-1", []string{"t1"}, current); err != nil {
		t.Fatal(err)
	}
	if _, err := p.readLinkedPullRequestMixRows(ctx, "org-1", []string{"t1"}, prior); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.readLinkedPullRequestRepoMix(ctx, "org-1", "t1", current); err != nil {
		t.Fatal(err)
	}
	// The current window's set serves both of its readers; the prior window is
	// another set, and it is read on its own.
	if client.sets != 2 {
		t.Fatalf("one team read sent %d linked set statements for 2 windows, want 2", client.sets)
	}
	// Without the memo (another read), every reader sends its own set.
	if _, _, err := p.readLinkedPullRequestRepoMix(context.WithValue(context.Background(), loadedMembershipScopeKey{}, loadedMembershipScope{orgID: "org-1", runID: "run-1", scope: membershipScope{mode: membershipScopeNone}}), "org-1", "t1", current); err != nil {
		t.Fatal(err)
	}
	if client.sets != 3 {
		t.Fatalf("a read without the memo sent %d set statements in all, want 3", client.sets)
	}
}

// A window is the memo's key by every instant and flag: a 7-day and a 30-day
// window that end together are two sets, and a copy of a window is the same.
func TestCHAOS12580TheLinkedSetMemoKeysOnTheWholeWindow(t *testing.T) {
	t.Parallel()
	end := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	month := factTimeBound{active: true, hasStart: true, start: end.AddDate(0, 0, -30), end: end, endExclusive: true}
	memo := &linkedSetMemo{}
	memo.store([]string{"t1"}, month, []string{"r1"}, []string{"1"}, []string{"t1"})
	same := month
	same.start = month.start.In(time.FixedZone("x", 3600))
	if _, _, _, ok := memo.lookup([]string{"t1"}, same); !ok {
		t.Fatal("the same window in another zone missed the memo")
	}
	for name, mutate := range map[string]func(*factTimeBound){
		"start":         func(b *factTimeBound) { b.start = end.AddDate(0, 0, -7) },
		"end":           func(b *factTimeBound) { b.end = end.AddDate(0, 0, 1) },
		"active":        func(b *factTimeBound) { b.active = false },
		"has start":     func(b *factTimeBound) { b.hasStart = false },
		"end exclusive": func(b *factTimeBound) { b.endExclusive = false },
		"current axis":  func(b *factTimeBound) { b.currentAxis = true },
		"stated window": func(b *factTimeBound) { b.statedWindow = true },
	} {
		other := month
		mutate(&other)
		if _, _, _, ok := memo.lookup([]string{"t1"}, other); ok {
			t.Errorf("a window that differs in %s took the memo's set", name)
		}
	}
	if _, _, _, ok := memo.lookup([]string{"t1", "t2"}, month); ok {
		t.Error("another team chunk took the memo's set")
	}
}
