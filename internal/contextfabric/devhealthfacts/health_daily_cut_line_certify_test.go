package devhealthfacts_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestTheDailyHealthCapCutLineCertifiesThroughTheHealthProducer reads health
// through the real provider with a daily_health table that the display cap cuts,
// once as the only reason for truncation (subject_local true) and once beside
// another reason (subject_local false), and certifies the line each read writes.
func TestTheDailyHealthCapCutLineCertifiesThroughTheHealthProducer(t *testing.T) {
	day := func(i int) string {
		return time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i).Format("2006-01-02")
	}
	cut := func() [][]any {
		var series [][]any
		for i := 0; i < contextfabric.MaxFactValueRows+10; i++ {
			series = append(series, healthTeamDailySeriesRow("CHECKOUT", day(i), "high", uint8(1), 0.61))
		}
		return series
	}
	for _, tc := range []struct {
		name         string
		teams        []string
		subjectLocal bool
	}{
		{"the cut is the only reason", []string{"CHECKOUT", "IDENTITY"}, true},
		{"another reason also truncates", nil, false},
	} {
		teams := tc.teams
		if teams == nil {
			for i := 0; i < 200; i++ {
				teams = append(teams, fmt.Sprintf("T%03d", i))
			}
			teams = append([]string{"CHECKOUT"}, teams...)
		}
		var scalar [][]any
		subjects := make([]contextfabric.SubjectRef, 0, len(teams))
		for _, team := range teams {
			scalar = append(scalar, healthRow(team))
			subjects = append(subjects, teamSubject(team))
		}
		client := &fakeClient{tables: []fakeTable{
			{match: healthScalarMatch, rows: scalar},
			{match: healthDailySeriesMatch, rows: cut()},
		}}
		var buf bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
		_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactHealth, Subjects: subjects,
		})
		slog.SetDefault(previous)
		if err != nil {
			t.Fatalf("%s: ReadFacts: %v", tc.name, err)
		}
		var line []byte
		for _, candidate := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
			if bytes.Contains(candidate, []byte(`"msg":"`+eventspec.HealthDailyHealthCapCut.Msg+`"`)) {
				line = candidate
			}
		}
		if line == nil {
			t.Fatalf("%s: no cut line was written: %s", tc.name, buf.String())
		}
		log, err := certify.Parse(line)
		if err != nil {
			t.Fatalf("certify.Parse: %v", err)
		}
		if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.HealthDailyHealthCapCut, Want: map[string]any{
			"org_id": "org-1", "subjects_cut": 1, "subjects": len(teams), "subject_local": tc.subjectLocal,
		}}); err != nil {
			t.Fatalf("%s: the line failed certification: %v\n%s", tc.name, err, line)
		}
	}
}
