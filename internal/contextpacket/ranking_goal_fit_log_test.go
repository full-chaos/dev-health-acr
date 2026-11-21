package contextpacket_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

func rankingDecidedLog(t *testing.T, goal string) (map[string]any, []string) {
	t.Helper()
	var out bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ids := rankedIDs(t, goal, 10, codeHealthEvidence())
	for _, line := range strings.Split(out.String(), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil && record["event"] == "ranking_decided" {
			return record, ids
		}
	}
	t.Fatalf("no ranking_decided log in %q", out.String())
	return nil, nil
}

func TestRankingDecidedLogsTheGoalFitAppliedAndRowsLifted(t *testing.T) {
	cases := []struct {
		name, goal          string
		failure, codeHealth bool
		lifted              float64
		wantOrder           []string
	}{
		{"failure goal", "reduce failed deployments", true, false, 2, nil},
		{"code health goal", "reduce churn in the checkout service", false, true, 2, []string{"ev-hotspot", "ev-complexity", "ev-pr-redesign", "ev-commit", "ev-dep-fail"}},
		{"neutral goal", "update the checkout copy", false, false, 0, []string{"ev-commit", "ev-pr-redesign", "ev-hotspot", "ev-complexity", "ev-dep-fail"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record, ids := rankingDecidedLog(t, tc.goal)
			if record["failure_fit"] != tc.failure || record["code_health_fit"] != tc.codeHealth || record["goal_fit_lifted"] != tc.lifted {
				t.Fatalf("log = %v, want failure_fit=%v code_health_fit=%v goal_fit_lifted=%v", record, tc.failure, tc.codeHealth, tc.lifted)
			}
			if tc.wantOrder != nil && !slices.Equal(ids, tc.wantOrder) {
				t.Fatalf("order = %v, want %v: the log must not change the ranking", ids, tc.wantOrder)
			}
		})
	}
}
