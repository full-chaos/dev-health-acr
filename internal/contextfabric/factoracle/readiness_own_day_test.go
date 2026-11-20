package factoracle

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ops reads each (provider, work scope, team) key at its own newest day, so a
// team with a key whose newest day is older than the team's latest day is
// summed over every key. The oracle must sum the same keys on both sides.
func TestReadinessSumsEveryKeyAtItsOwnNewestDay(t *testing.T) {
	_, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	// The venue's older keys all hold zero counts, which would hide a latest-day-only sum: give each one counts.
	newest := map[string]map[string]map[string]any{}
	latestDay := map[string]string{}
	for _, row := range extract.Tables[tableEstimateCoverageMetricsDaily] {
		team, _ := rowString(row, "team_id")
		day, _ := rowString(row, "day")
		provider, _ := rowString(row, "provider")
		scope, _ := rowString(row, "work_scope_id")
		key := provider + "\x00" + scope
		if newest[team] == nil {
			newest[team] = map[string]map[string]any{}
		}
		if prior, seen := newest[team][key]; !seen || func() bool { d, _ := rowString(prior, "day"); return day > d }() {
			newest[team][key] = row
		}
		if day > latestDay[team] {
			latestDay[team] = day
		}
	}
	for team, keys := range newest {
		for _, row := range keys {
			if day, _ := rowString(row, "day"); team != "" && day != latestDay[team] {
				row["estimated_count"], row["unestimated_count"], row["backlog_size"] = json.Number("4"), json.Number("2"), json.Number("6")
			}
		}
	}
	mutated, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}

	type keyRow struct {
		day    string
		at     time.Time
		fields map[string]any
	}
	perTeam := map[string]map[string]keyRow{}
	for _, row := range extract.Tables[tableEstimateCoverageMetricsDaily] {
		team, ok := rowString(row, "team_id")
		if !ok || team == "" {
			continue
		}
		day, _ := rowString(row, "day")
		provider, _ := rowString(row, "provider")
		scope, _ := rowString(row, "work_scope_id")
		at, err := rowTime(row, "computed_at")
		if err != nil {
			t.Fatal(err)
		}
		key := provider + "\x00" + scope
		if perTeam[team] == nil {
			perTeam[team] = map[string]keyRow{}
		}
		if prior, seen := perTeam[team][key]; seen && (day < prior.day || (day == prior.day && !at.After(prior.at))) {
			continue
		}
		fields := map[string]any{"day": day}
		for _, column := range []string{"estimated_count", "unestimated_count", "backlog_size"} {
			value, err := rowFloat(row, column)
			if err != nil {
				t.Fatal(err)
			}
			fields[column] = fmt.Sprint(int64(value))
		}
		perTeam[team][key] = keyRow{day: day, at: at, fields: fields}
	}
	sums := map[string]map[string]int64{}
	older := 0
	for team, keys := range perTeam {
		latest := ""
		for _, row := range keys {
			if row.day > latest {
				latest = row.day
			}
		}
		sums[team] = map[string]int64{}
		for _, row := range keys {
			if row.day != latest {
				older++
				break
			}
		}
		for _, row := range keys {
			for _, column := range []string{"estimated_count", "unestimated_count", "backlog_size"} {
				var v int64
				_, _ = fmt.Sscan(row.fields[column].(string), &v)
				sums[team][column] += v
			}
		}
	}
	if older == 0 {
		t.Fatal("the capture has no team with a coverage key whose newest day is older than the team's latest day")
	}

	planes := recordedThroughput(t, func(variables map[string]any, forecast map[string]any) {
		input, _ := variables["input"].(map[string]any)
		teams, _ := input["teamIds"].([]any)
		team, _ := teams[0].(string)
		total, ok := sums[strings.TrimPrefix(team, "team:")]
		if !ok {
			return
		}
		block := map[string]any{"__typename": "ThroughputEstimateCoverage", "estimatedCount": total["estimated_count"],
			"unestimatedCount": total["unestimated_count"], "backlogSize": total["backlog_size"], "ratio": nil,
			"grainCount": len(perTeam[strings.TrimPrefix(team, "team:")])}
		if total["backlog_size"] != 0 {
			block["ratio"] = float64(total["estimated_count"]) / float64(total["backlog_size"])
		}
		forecast["estimateCoverage"] = block
	})
	planes.facts = func(request FactsRequest) (json.RawMessage, error) {
		if request.Kinds[0] != "readiness" {
			return noFacts(request)
		}
		var facts, rows []any
		for _, subject := range request.Subjects {
			for _, row := range perTeam[strings.TrimPrefix(subject.CanonicalID, "team:")] {
				facts = append(facts, map[string]any{"kind": "readiness", "subject": subject, "fields": row.fields})
			}
			rows = append(rows, map[string]any{"kind": "readiness", "subject": subject, "outcome": "available"})
		}
		return json.Marshal(map[string]any{"status": "complete", "versions": map[string]any{"kinds": map[string]any{}}, "facts": facts, "coverage": rows})
	}
	rr := fakeRun(t, "throughputForecast", planes, func(o *Oracle) { o.Store = mutated }).Root("throughputForecast")
	for _, f := range rr.Findings {
		if f.Pair == "readiness" || f.Pair == "readiness_store" {
			t.Errorf("both planes and the store state the sum of every key at its own newest day, and %s has a finding: %+v", f.Pair, f)
		}
	}
}
