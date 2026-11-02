package devhealthfacts

import (
	"context"
	"strings"
	"sync"
)

// linkedSetMemo shares one team read's linked pull request sets between its
// readers: the team linked mix and, on a single-team read, the per-repository
// linked mix of repository_theme_breakdown (CHAOS-12580) take the same set of
// the same teams and window. Without it the set statement, its candidate read
// and its native-cover read ran twice on one served read. It lives in the
// context of one readTeamThemeMix call, so no set outlives the read.
type linkedSetMemo struct {
	mu      sync.Mutex
	entries []linkedSetMemoEntry
}

type linkedSetMemoEntry struct {
	teams                  string
	bound                  factTimeBound
	repos, numbers, linked []string
}

type linkedSetMemoKey struct{}

// withLinkedSetMemo returns ctx with an empty memo for one team read.
func withLinkedSetMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, linkedSetMemoKey{}, &linkedSetMemo{})
}

func linkedSetMemoKeyOf(chunk []string) string { return strings.Join(chunk, "\x00") }

// sameFactTimeBound is bound equality by instant, not by time.Time's internal
// representation.
func sameFactTimeBound(a, b factTimeBound) bool {
	return a.active == b.active && a.hasStart == b.hasStart && a.start.Equal(b.start) && a.end.Equal(b.end) &&
		a.endExclusive == b.endExclusive && a.currentAxis == b.currentAxis && a.statedWindow == b.statedWindow
}

func (m *linkedSetMemo) lookup(chunk []string, bound factTimeBound) (repos, numbers, teams []string, ok bool) {
	key := linkedSetMemoKeyOf(chunk)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if e.teams == key && sameFactTimeBound(e.bound, bound) {
			return e.repos, e.numbers, e.linked, true
		}
	}
	return nil, nil, nil, false
}

func (m *linkedSetMemo) store(chunk []string, bound factTimeBound, repos, numbers, teams []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, linkedSetMemoEntry{teams: linkedSetMemoKeyOf(chunk), bound: bound, repos: repos, numbers: numbers, linked: teams})
}

// readLinkedPullRequestSet is readLinkedPullRequestSetOnce through the read's
// memo, when its context carries one.
func (p *InvestmentProvider) readLinkedPullRequestSet(ctx context.Context, orgID string, chunk []string, bound factTimeBound) (repos, numbers, teams []string, err error) {
	memo, _ := ctx.Value(linkedSetMemoKey{}).(*linkedSetMemo)
	if memo == nil {
		return p.readLinkedPullRequestSetOnce(ctx, orgID, chunk, bound)
	}
	if repos, numbers, teams, ok := memo.lookup(chunk, bound); ok {
		return repos, numbers, teams, nil
	}
	repos, numbers, teams, err = p.readLinkedPullRequestSetOnce(ctx, orgID, chunk, bound)
	if err != nil {
		return nil, nil, nil, err
	}
	memo.store(chunk, bound, repos, numbers, teams)
	return repos, numbers, teams, nil
}
