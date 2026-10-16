package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The compounding-risk rule's rationale carries the score the rule ran on.
// The health fact carries the latest computation. A served deficiency fact
// must name the rule-time value as its basis and carry the latest value, so
// a reader never takes the rule-time score for the team's current risk.
func TestDeficiencyFactNamesRuleBasisAndLatestHealth(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "max(window_end) OVER (PARTITION BY team_id)", rows: [][]any{{"team-1", "2026-10-10", uint32(1), uint8(1), uint8(1)}}},
		{match: "WHERE rn = 1 AND fired = 1", rows: [][]any{{
			"team-1", "compounding-risk", "v1", "warning", "Code risk is compounding where change pressure is highest.",
			"Compounding Risk score is 0.509 (severity: elevated). Churn, complexity trend, ownership concentration, and review latency are compounding above their tuned thresholds.",
			"criterion", "2026-10-03", "2026-10-10", "2026-10-10 06:00:00",
		}}},
		{match: "compounding_risk, computed_at, day,\n\t\trow_number()", rows: [][]any{{"team-1", "high", uint8(1), float64(0.72), "2026-10-12 00:30:00", "2026-10-11", uint8(1), uint8(1)}}},
		{match: "toString(day),\n\ttoUInt8(", rows: [][]any{{
			"team-1", "high", uint8(1), float64(0.72), "2026-10-12 00:30:00", "2026-10-11", uint8(1), uint8(1),
			uint8(1), float64(0.3), float64(0.4), uint8(1), float64(0.2), float64(0.3),
			uint8(1), float64(0.5), float64(0.2), uint8(1), float64(0.1), float64(0.1),
		}}},
	}}
	providers := devhealthfacts.NewProviders(client)
	principal := storage.Principal{OrgID: "org-1"}
	query := func(kind contextfabric.FactKind) contextfabric.FactQuery {
		return contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: kind, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
		}
	}
	health, err := findProvider(t, providers, contextfabric.FactHealth).ReadFacts(context.Background(), principal, query(contextfabric.FactHealth))
	if err != nil || len(health.Facts) != 1 {
		t.Fatalf("health ReadFacts() = %d facts, err %v", len(health.Facts), err)
	}
	if got := health.Facts[0].Fields["compounding_risk"].Number; got == nil || *got != 0.72 {
		t.Fatalf("health compounding_risk = %#v, want 0.72", health.Facts[0].Fields["compounding_risk"])
	}
	def, err := findProvider(t, providers, contextfabric.FactOperationalDeficiencies).ReadFacts(context.Background(), principal, query(contextfabric.FactOperationalDeficiencies))
	if err != nil || len(def.Facts) != 1 {
		t.Fatalf("deficiency ReadFacts() = %d facts, err %v", len(def.Facts), err)
	}
	fields := def.Facts[0].Fields
	if rationale := fields["rationale"].String; rationale == nil || !strings.Contains(*rationale, "0.509") {
		t.Fatalf("rationale = %#v, want the rule's own text kept verbatim", fields["rationale"])
	}
	if got := fields["rule_basis_compounding_risk"].Number; got == nil || *got != 0.509 {
		t.Fatalf("rule_basis_compounding_risk = %#v, want 0.509 named as the rule's basis", fields["rule_basis_compounding_risk"])
	}
	if got := fields["latest_compounding_risk"].Number; got == nil || *got != 0.72 {
		t.Fatalf("latest_compounding_risk = %#v, want 0.72 (the latest health computation)", fields["latest_compounding_risk"])
	}
	if got := fields["latest_severity"].String; got == nil || *got != "high" {
		t.Fatalf("latest_severity = %#v, want high", fields["latest_severity"])
	}
	if got := fields["score_moved_since_rule"].Boolean; got == nil || !*got {
		t.Fatalf("score_moved_since_rule = %#v, want true (elevated at rule time, high now)", fields["score_moved_since_rule"])
	}
}

const partialCoverageRationale = "Compounding Risk score is 0.636 (severity: elevated), computed from 80% of its inputs (churn, complexity trend, review latency). The inputs that were present are compounding above their tuned thresholds."

const ruleBasisRationale = "Compounding Risk score is 0.509 (severity: elevated). Churn, complexity trend, ownership concentration, and review latency are compounding above their tuned thresholds."

// Each row states what the served deficiency fact must say about the latest
// computation relative to the rule's own basis. A value that cannot be
// stated is absent, never zero.
func TestDeficiencyFactRuleBasisAgainstLatestRows(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name               string
		rationale          string
		latest             [][]any
		wantBasis          *float64
		wantLatest         *float64
		wantMoved          *bool
		wantReason         string
		wantSeverityReason string
		wantScoreReason    string
		wantSeverity       *string
	}{
		{name: "band changed", rationale: ruleBasisRationale, latest: [][]any{{"team-1", "high", uint8(1), 0.55, "2026-10-12 00:30:00", "2026-10-11", uint8(1), uint8(1)}}, wantBasis: ptrNum(0.509), wantLatest: ptrNum(0.55), wantMoved: ptrBool(true), wantSeverity: ptrStr("high")},
		{name: "partial coverage sentence", rationale: partialCoverageRationale, latest: [][]any{{"team-1", "high", uint8(1), 0.994, "2026-10-12 00:30:00", "2026-10-11", uint8(1), uint8(1)}}, wantBasis: ptrNum(0.636), wantLatest: ptrNum(0.994), wantMoved: ptrBool(true), wantSeverity: ptrStr("high")},
		{name: "partial coverage tail with an unknown input", rationale: "Compounding Risk score is 0.636 (severity: elevated), computed from 80% of its inputs (vibes). The inputs that were present are compounding above their tuned thresholds.", latest: [][]any{{"team-1", "high", uint8(1), 0.994, "2026-10-12 00:30:00", "2026-10-11", uint8(1), uint8(1)}}, wantLatest: ptrNum(0.994), wantSeverity: ptrStr("high")},
		{name: "same band, delta at threshold", rationale: ruleBasisRationale, latest: [][]any{{"team-1", "elevated", uint8(1), 0.609, "2026-10-12 00:30:00", "2026-10-11", uint8(1), uint8(1)}}, wantBasis: ptrNum(0.509), wantLatest: ptrNum(0.609), wantMoved: ptrBool(true), wantSeverity: ptrStr("elevated")},
		{name: "same band, small delta", rationale: ruleBasisRationale, latest: [][]any{{"team-1", "elevated", uint8(1), 0.53, "2026-10-12 00:30:00", "2026-10-11", uint8(1), uint8(1)}}, wantBasis: ptrNum(0.509), wantLatest: ptrNum(0.53), wantMoved: ptrBool(false), wantSeverity: ptrStr("elevated")},
		{name: "unparsed rationale", rationale: "The team's risk is compounding.", latest: [][]any{{"team-1", "high", uint8(1), 0.72, "2026-10-12 00:30:00", "2026-10-11", uint8(1), uint8(1)}}, wantLatest: ptrNum(0.72), wantSeverity: ptrStr("high")},
		{name: "no latest row", rationale: ruleBasisRationale, wantBasis: ptrNum(0.509), wantReason: "no_compounding_risk_row"},
		{name: "latest band unknown, no score", rationale: ruleBasisRationale, latest: [][]any{{"team-1", "unknown", uint8(0), 0.0, "2026-10-12 00:30:00", "2026-10-11", uint8(0), uint8(1)}}, wantBasis: ptrNum(0.509), wantSeverityReason: "no_known_severity_breakdown_rows", wantScoreReason: "compounding_risk_null_on_latest_row"},
		{name: "latest band unknown, score present", rationale: ruleBasisRationale, latest: [][]any{{"team-1", "unknown", uint8(1), 0.72, "2026-10-12 00:30:00", "2026-10-11", uint8(0), uint8(1)}}, wantBasis: ptrNum(0.509), wantLatest: ptrNum(0.72), wantMoved: ptrBool(true), wantSeverityReason: "no_known_severity_breakdown_rows"},
		{name: "known band outside the freshness window", rationale: ruleBasisRationale, latest: [][]any{{"team-1", "high", uint8(1), 0.72, "2026-10-12 00:30:00", "2026-08-01", uint8(1), uint8(0)}}, wantBasis: ptrNum(0.509), wantLatest: ptrNum(0.72), wantMoved: ptrBool(true), wantSeverityReason: "known_severity_stale_beyond_freshness_window"},
		{name: "same band, score missing", rationale: ruleBasisRationale, latest: [][]any{{"team-1", "elevated", uint8(0), 0.0, "2026-10-12 00:30:00", "2026-10-11", uint8(1), uint8(1)}}, wantBasis: ptrNum(0.509), wantSeverity: ptrStr("elevated"), wantScoreReason: "compounding_risk_null_on_latest_row"},
		{name: "different band, score missing", rationale: ruleBasisRationale, latest: [][]any{{"team-1", "high", uint8(0), 0.0, "2026-10-12 00:30:00", "2026-10-11", uint8(1), uint8(1)}}, wantBasis: ptrNum(0.509), wantMoved: ptrBool(true), wantSeverity: ptrStr("high"), wantScoreReason: "compounding_risk_null_on_latest_row"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{tables: []fakeTable{
				{match: "max(window_end) OVER (PARTITION BY team_id)", rows: [][]any{{"team-1", "2026-10-10", uint32(1), uint8(1), uint8(1)}}},
				{match: "WHERE rn = 1 AND fired = 1", rows: [][]any{{"team-1", "compounding-risk", "v1", "warning", "t", tc.rationale, "c", "2026-10-03", "2026-10-10", "2026-10-10 06:00:00"}}},
				{match: "compounding_risk, computed_at, day,\n\t\trow_number()", rows: tc.latest},
			}}
			result, err := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactOperationalDeficiencies).ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind: contextfabric.FactOperationalDeficiencies, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
			})
			if err != nil || len(result.Facts) != 1 {
				t.Fatalf("ReadFacts() = %d facts, err %v", len(result.Facts), err)
			}
			fields := result.Facts[0].Fields
			assertNumberField(t, fields, "rule_basis_compounding_risk", tc.wantBasis)
			assertNumberField(t, fields, "latest_compounding_risk", tc.wantLatest)
			moved, present := fields["score_moved_since_rule"]
			switch {
			case tc.wantMoved == nil && present:
				t.Fatalf("score_moved_since_rule = %#v, want absent", moved)
			case tc.wantMoved != nil && (moved.Boolean == nil || *moved.Boolean != *tc.wantMoved):
				t.Fatalf("score_moved_since_rule = %#v, want %v", moved, *tc.wantMoved)
			}
			reason := fields["latest_unavailable_reason"].String
			switch {
			case tc.wantReason == "" && reason != nil:
				t.Fatalf("latest_unavailable_reason = %q, want absent", *reason)
			case tc.wantReason != "" && (reason == nil || *reason != tc.wantReason):
				t.Fatalf("latest_unavailable_reason = %#v, want %q", reason, tc.wantReason)
			}
			scoreReason := fields["latest_score_unavailable_reason"].String
			switch {
			case tc.wantScoreReason == "" && scoreReason != nil:
				t.Fatalf("latest_score_unavailable_reason = %q, want absent", *scoreReason)
			case tc.wantScoreReason != "" && (scoreReason == nil || *scoreReason != tc.wantScoreReason):
				t.Fatalf("latest_score_unavailable_reason = %#v, want %q", scoreReason, tc.wantScoreReason)
			}
			severityReason := fields["latest_severity_unavailable_reason"].String
			switch {
			case tc.wantSeverityReason == "" && severityReason != nil:
				t.Fatalf("latest_severity_unavailable_reason = %q, want absent", *severityReason)
			case tc.wantSeverityReason != "" && (severityReason == nil || *severityReason != tc.wantSeverityReason):
				t.Fatalf("latest_severity_unavailable_reason = %#v, want %q", severityReason, tc.wantSeverityReason)
			}
			severity := fields["latest_severity"].String
			switch {
			case tc.wantSeverity == nil && severity != nil:
				t.Fatalf("latest_severity = %q, want absent", *severity)
			case tc.wantSeverity != nil && (severity == nil || *severity != *tc.wantSeverity):
				t.Fatalf("latest_severity = %#v, want %q", severity, *tc.wantSeverity)
			}
			if tc.wantBasis == nil {
				if got := fields["rule_basis_unavailable_reason"].String; got == nil || *got != "rationale_not_in_rule_sentence_form" {
					t.Fatalf("rule_basis_unavailable_reason = %#v, want rationale_not_in_rule_sentence_form", got)
				}
			} else if _, present := fields["rule_basis_unavailable_reason"]; present {
				t.Fatalf("rule_basis_unavailable_reason present on a parsed rationale: %#v", fields["rule_basis_unavailable_reason"])
			}
			if got := fields["rationale"].String; got == nil || *got != tc.rationale {
				t.Fatalf("rationale = %#v, want the rule's text verbatim", got)
			}
		})
	}
}

func ptrNum(v float64) *float64 { return &v }
func ptrStr(v string) *string   { return &v }
func ptrBool(v bool) *bool      { return &v }

func assertNumberField(t *testing.T, fields map[string]contextfabric.FactValue, name string, want *float64) {
	t.Helper()
	got := fields[name].Number
	switch {
	case want == nil && got != nil:
		t.Fatalf("%s = %v, want absent", name, *got)
	case want != nil && (got == nil || *got != *want):
		t.Fatalf("%s = %#v, want %v", name, got, *want)
	}
}

// The rule reads the score of the day before its window end, so the served
// basis names that day beside the score. A rule row written before Ops named
// the day serves no day field, never an empty or a guessed one.
func TestDeficiencyFactServesTheRuleBasisDayOnlyWhenTheRationaleNamesIt(t *testing.T) {
	t.Parallel()
	const withDay = "Compounding Risk score is 0.739 (severity: high). Score as of 2026-10-14. Churn is compounding above its tuned threshold."
	cases := []struct {
		name, rationale, wantDay string
	}{
		{"named", withDay, "2026-10-14"},
		{"not named", ruleBasisRationale, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := &fakeClient{tables: []fakeTable{
				{match: "WHERE rn = 1 AND fired = 1", rows: [][]any{{
					"team-1", "compounding-risk", "v1", "warning", "Code risk is compounding where change pressure is highest.",
					c.rationale, "criterion", "2026-10-03", "2026-10-15", "2026-10-15 06:00:00",
				}}},
			}}
			def, err := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactOperationalDeficiencies).ReadFacts(
				context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
					Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
					Kind: contextfabric.FactOperationalDeficiencies, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
				})
			if err != nil || len(def.Facts) != 1 {
				t.Fatalf("ReadFacts() = %d facts, err %v", len(def.Facts), err)
			}
			fields := def.Facts[0].Fields
			if fields["rule_basis_compounding_risk"].Number == nil {
				t.Fatalf("rule basis score not served: %#v", fields)
			}
			day, served := fields["rule_basis_as_of_day"]
			if c.wantDay == "" {
				if served {
					t.Errorf("rule_basis_as_of_day = %#v, want it absent", day)
				}
				return
			}
			if day.String == nil || *day.String != c.wantDay {
				t.Errorf("rule_basis_as_of_day = %#v, want %q", day, c.wantDay)
			}
		})
	}
}
