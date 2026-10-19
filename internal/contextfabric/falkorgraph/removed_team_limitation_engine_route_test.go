package falkorgraph

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const removedTeamLimitation = "A named team is removed at its source: its work item, project and repository edges are kept and may be out of date; they are not retracted."

func askAboutTeamTango(t *testing.T, teamProps map[string]interface{}) routeAnswer {
	t.Helper()
	s := routeSeed{text: map[string]string{"team|team:tango": "tango"}, reach: map[string][]string{}, deployments: map[string][]string{}}
	s.nodes = append(s.nodes,
		seededNode{kind: "team", id: "team:tango", label: "tango", props: teamProps},
		seededNode{kind: "repository", id: "repository:github:acme/quiet", label: "acme/quiet", repos: []string{"acme/quiet"}})
	s.edges = append(s.edges, seededEdge{"OWNED_BY_TEAM", "repository", "repository:github:acme/quiet", "team", "team:tango", ""})
	return askAnchorDeployments(t, s, storage.Principal{OrgID: "org-1"}, contextfabric.SubjectTeam, "tango")
}

func countRemovedTeamLimitation(a routeAnswer) int {
	n := 0
	for _, l := range a.result.Limitations {
		if strings.Contains(l, removedTeamLimitation) {
			n++
		}
	}
	return n
}

func TestARemovedTeamIsFlaggedInTheServedLimitations(t *testing.T) {
	removed := askAboutTeamTango(t, map[string]interface{}{propPropertyPrefix + "gone_at": "2026-10-09 17:30:00.000000"})
	if len(removed.committed) != 1 || removed.committed[0].CanonicalID != "team:tango" {
		t.Fatalf("resolver committed %+v, want the named team", removed.committed)
	}
	if got := countRemovedTeamLimitation(removed); got != 1 {
		t.Fatalf("limitations = %q: the removed-team sentence appears %d times, want exactly once", removed.result.Limitations, got)
	}
}

func TestATeamNeverProvedGoneIsNotFlagged(t *testing.T) {
	live := askAboutTeamTango(t, nil)
	if len(live.committed) != 1 || live.committed[0].CanonicalID != "team:tango" {
		t.Fatalf("resolver committed %+v, want the named team", live.committed)
	}
	if got := countRemovedTeamLimitation(live); got != 0 {
		t.Fatalf("limitations = %q: an active team carries the removed-team sentence", live.result.Limitations)
	}
}

type capturedLogs struct {
	mu    sync.Mutex
	lines []map[string]any
}

func (c *capturedLogs) Write(p []byte) (int, error) {
	var line map[string]any
	if json.Unmarshal(p, &line) == nil {
		c.mu.Lock()
		c.lines = append(c.lines, line)
		c.mu.Unlock()
	}
	return len(p), nil
}

func (c *capturedLogs) named(msg string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, l := range c.lines {
		if l["msg"] == msg {
			out = append(out, l)
		}
	}
	return out
}

func TestTheRemovedTeamDecisionIsLoggedAtInfoWithCounts(t *testing.T) {
	logs := &capturedLogs{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	askAboutTeamTango(t, map[string]interface{}{propPropertyPrefix + "gone_at": "2026-10-09 17:30:00.000000"})
	lines := logs.named("context_fabric: removed team lookup")
	if len(lines) != 1 {
		t.Fatalf("%d removed team lookup lines at Info, want exactly one", len(lines))
	}
	if lines[0]["level"] != "INFO" || lines[0]["teams"] != float64(1) || lines[0]["removed"] != float64(1) {
		t.Fatalf("lookup line = %v, want level INFO, teams=1, removed=1", lines[0])
	}
}

func TestTheRemovedTeamLookupLineNamesWhichTeamWasFlagged(t *testing.T) {
	logs := &capturedLogs{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	askAboutTeamTango(t, map[string]interface{}{propPropertyPrefix + "gone_at": "2026-10-09 17:30:00.000000"})
	lines := logs.named("context_fabric: removed team lookup")
	if len(lines) != 1 {
		t.Fatalf("%d removed team lookup lines at Info, want exactly one", len(lines))
	}
	ids, _ := lines[0]["removed_ids"].([]any)
	if len(ids) != 1 || ids[0] != "team:tango" {
		t.Fatalf("lookup line = %v, want removed_ids [team:tango]: counts alone cannot say which team was flagged", lines[0])
	}
}
