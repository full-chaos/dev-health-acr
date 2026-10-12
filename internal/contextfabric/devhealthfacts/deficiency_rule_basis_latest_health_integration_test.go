package devhealthfacts_test

// The deficiency fact names the team's latest compounding risk beside the
// rule's own basis. It must pick the SAME row the health fact picks. These
// rows seed compounding_risk_daily and recommendations_daily in a real
// ClickHouse and compare the two providers' reads for each case.

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestDeficiencyLatestRowEqualsHealthLatestRowAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	daysAgo := func(n int) time.Time { return today.AddDate(0, 0, -n) }
	at := func(day time.Time, hour int) time.Time { return day.Add(time.Duration(hour) * time.Hour) }

	const orgID = "org-deficiency-latest-row"
	const otherOrgID = "org-deficiency-latest-row-other"
	type riskRow struct {
		org      string
		team     string
		day      time.Time
		severity string
		risk     any
		computed time.Time
	}
	seedRisk := func(r riskRow) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO compounding_risk_daily (org_id, day, scope, scope_id, compounding_risk, severity, computed_at) VALUES (?,?,?,?,?,?,?)`,
			r.org, r.day, "team", r.team, r.risk, r.severity, r.computed); err != nil {
			t.Fatalf("seed compounding_risk_daily: %v", err)
		}
	}
	seedRule := func(team string) {
		t.Helper()
		windowEnd := daysAgo(1)
		if err := direct.Exec(ctx, `INSERT INTO recommendations_daily (team_id, org_id, rule_id, rule_version, window_start, window_end, fired, severity, title, rationale, success_criterion, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			team, orgID, "compounding-risk", "v1", windowEnd.AddDate(0, 0, -14), windowEnd, true, "warning", "Code risk is compounding where change pressure is highest.",
			ruleBasisRationale, "criterion", at(windowEnd, 6)); err != nil {
			t.Fatalf("seed recommendations_daily: %v", err)
		}
	}

	// fresh: a known band inside the window, a score.
	seedRisk(riskRow{orgID, "FRESH", daysAgo(1), "high", 0.72, at(daysAgo(1), 6)})
	// stale: the only known band is older than the freshness window; a newer unknown row follows it.
	seedRisk(riskRow{orgID, "STALE", daysAgo(40), "high", 0.8, at(daysAgo(40), 6)})
	seedRisk(riskRow{orgID, "STALE", daysAgo(1), "unknown", nil, at(daysAgo(1), 6)})
	// nullscore: a known fresh band with no score.
	seedRisk(riskRow{orgID, "NULLSCORE", daysAgo(1), "elevated", nil, at(daysAgo(1), 6)})
	// unknownscore: an unknown band that still carries a score.
	seedRisk(riskRow{orgID, "UNKNOWNSCORE", daysAgo(1), "unknown", 0.31, at(daysAgo(1), 6)})
	// sameday: two computations of one day; the later computed_at wins.
	seedRisk(riskRow{orgID, "SAMEDAY", daysAgo(1), "elevated", 0.5, at(daysAgo(1), 6)})
	seedRisk(riskRow{orgID, "SAMEDAY", daysAgo(1), "high", 0.7, at(daysAgo(1), 9)})
	// tie: identical day and computed_at, different values; both reads must break it the same way.
	seedRisk(riskRow{orgID, "TIE", daysAgo(1), "elevated", 0.55, at(daysAgo(1), 6)})
	seedRisk(riskRow{orgID, "TIE", daysAgo(1), "high", 0.65, at(daysAgo(1), 6)})
	// known earlier beats unknown later (health's known-band preference).
	seedRisk(riskRow{orgID, "KNOWNFIRST", daysAgo(3), "elevated", 0.45, at(daysAgo(3), 6)})
	seedRisk(riskRow{orgID, "KNOWNFIRST", daysAgo(1), "unknown", 0.9, at(daysAgo(1), 6)})
	// another organization's row for a team id this organization also uses.
	seedRisk(riskRow{otherOrgID, "FRESH", daysAgo(0), "low", 0.01, at(daysAgo(0), 7)})

	teams := []string{"FRESH", "STALE", "NULLSCORE", "UNKNOWNSCORE", "SAMEDAY", "TIE", "KNOWNFIRST"}
	subjects := make([]contextfabric.SubjectRef, 0, len(teams))
	for _, team := range teams {
		seedRule(team)
		subjects = append(subjects, teamSubject(team))
	}

	providers := devhealthfacts.NewProviders(query)
	read := func(kind contextfabric.FactKind) contextfabric.FactProviderResult {
		t.Helper()
		result, err := findProvider(t, providers, kind).ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: kind, Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("%s ReadFacts: %v", kind, err)
		}
		return result
	}
	healthByTeam := map[string]contextfabric.CanonicalFact{}
	for _, fact := range read(contextfabric.FactHealth).Facts {
		healthByTeam[fact.Subject.Label] = fact
	}
	deficiencyByTeam := map[string]contextfabric.CanonicalFact{}
	for _, fact := range read(contextfabric.FactOperationalDeficiencies).Facts {
		deficiencyByTeam[fact.Subject.Label] = fact
	}

	for _, team := range teams {
		health, haveHealth := healthByTeam[team]
		deficiency, haveDeficiency := deficiencyByTeam[team]
		if !haveHealth || !haveDeficiency {
			t.Errorf("%s: health fact present = %v, deficiency fact present = %v, want both", team, haveHealth, haveDeficiency)
			continue
		}
		hf, df := health.Fields, deficiency.Fields

		healthRisk, healthHasRisk := hf["compounding_risk"]
		latestRisk, latestHasRisk := df["latest_compounding_risk"]
		if healthHasRisk != latestHasRisk || (healthHasRisk && *healthRisk.Number != *latestRisk.Number) {
			t.Errorf("%s: latest_compounding_risk = %#v, health compounding_risk = %#v, want the same row's score", team, latestRisk, healthRisk)
		}
		if got, want := df["latest_computed_at"].String, hf["computed_at"].String; got == nil || want == nil || *got != *want {
			t.Errorf("%s: latest_computed_at = %#v, health computed_at = %#v, want the same row", team, got, want)
		}
		healthSeverity := hf["severity"].String
		latestSeverity, latestHasSeverity := df["latest_severity"]
		switch {
		case healthSeverity != nil && *healthSeverity != "unknown":
			if !latestHasSeverity || *latestSeverity.String != *healthSeverity {
				t.Errorf("%s: latest_severity = %#v, health severity = %q", team, latestSeverity, *healthSeverity)
			}
		default:
			if latestHasSeverity {
				t.Errorf("%s: latest_severity = %q served while health serves %v", team, *latestSeverity.String, healthSeverity)
			}
			want := hf["severity_unavailable_reason"].String
			got := df["latest_severity_unavailable_reason"].String
			if want == nil || got == nil || *want != *got {
				t.Errorf("%s: latest_severity_unavailable_reason = %#v, health severity_unavailable_reason = %#v", team, got, want)
			}
		}
		if got := df["rule_basis_compounding_risk"].Number; got == nil || *got != 0.509 {
			t.Errorf("%s: rule_basis_compounding_risk = %#v, want 0.509", team, got)
		}
	}
	if got := deficiencyByTeam["FRESH"].Fields["latest_compounding_risk"].Number; got == nil || *got != 0.72 {
		t.Errorf("FRESH: latest_compounding_risk = %#v, want 0.72 (another organization's row must not be read)", got)
	}
	if got := deficiencyByTeam["SAMEDAY"].Fields["latest_compounding_risk"].Number; got == nil || *got != 0.7 {
		t.Errorf("SAMEDAY: latest_compounding_risk = %#v, want the later computation 0.7", got)
	}
	if got := deficiencyByTeam["FRESH"].Fields["score_moved_since_rule"].Boolean; got == nil || !*got {
		t.Errorf("FRESH: score_moved_since_rule = %#v, want true (elevated at rule time, high now)", got)
	}
}
