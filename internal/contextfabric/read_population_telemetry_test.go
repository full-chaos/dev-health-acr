package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// readPopulationSink drives the PRODUCTION SlogEngineTelemetry at the
// PRODUCTION default level with a REAL request context, and decodes the line.
//
// Production level, not Debug: a line demoted below the default disappears in
// prod exactly as it would here, and a test that raised the level would not
// notice. A real request context, not context.Background(): the join attrs are
// only emitted when one is present, so Background() would silently pass a test
// of a line that carries no request id in production.
func readPopulationSink(t *testing.T, event ReadRequirementPopulationEvent) map[string]any {
	t.Helper()
	logs := &bytes.Buffer{}
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(logs, nil)))
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
	telemetry.RecordReadRequirementPopulation(ctx, storage.Principal{OrgID: "org_1"}, event)

	const line = "context fabric read requirement population"
	var record map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if raw == "" {
			continue
		}
		var candidate map[string]any
		if err := json.Unmarshal([]byte(raw), &candidate); err != nil {
			t.Fatalf("sink emitted undecodable JSON: %v (%s)", err, raw)
		}
		if candidate["msg"] == line {
			record = candidate
		}
	}
	if record == nil {
		t.Fatalf("the sink emitted no %q line at the production default level; a demoted line is invisible in prod. logs: %s", line, logs.String())
	}
	return record
}

// TestTheReadPopulationRowReachesTheOperator asserts the sink line KEY BY KEY,
// in the sink's own encoding.
//
// NEVER A SUBSTRING: a substring cannot see a field disappear when its value
// also occurs elsewhere in the record, which is exactly how two mutants
// survived a Contains() assertion on this package's last outcome change.
func TestTheReadPopulationRowReachesTheOperator(t *testing.T) {
	t.Parallel()
	event := ReadRequirementPopulationEvent{
		Family:        QuestionFamilyScopedCohortStatus,
		Requirement:   "state/operand/team",
		Scope:         string(CompletionScopeEachOperand),
		Outcome:       contractsv1.ContextFabricRequirementNarrowed,
		Impact:        contractsv1.ContextFabricAnswerImpactScope,
		Cause:         contractsv1.ContextFabricCoverageDetailFactNarrowed,
		CauseObserved: false,
		Served:        1,
		Declared:      2,
		Units:         countUnitsPopulation,
		Census:        populationEnumerated,
	}
	record := readPopulationSink(t, event)

	for key, want := range map[string]any{
		"org_id":            "org_1",
		"request_id":        "req_0123456789abcdef0123456789abcdef",
		"family":            string(QuestionFamilyScopedCohortStatus),
		"requirement":       "state/operand/team",
		"scope":             string(CompletionScopeEachOperand),
		"outcome":           string(contractsv1.ContextFabricRequirementNarrowed),
		"impact":            string(contractsv1.ContextFabricAnswerImpactScope),
		"cause_coverage":    string(contractsv1.ContextFabricCoverageDetailFactNarrowed),
		"cause_observed":    false,
		"row_served":        float64(1),
		"row_declared":      float64(2),
		"count_units":       string(countUnitsPopulation),
		"population_census": string(populationEnumerated),
		"cohort_complete":   false,
		"cohort_truncated":  false,
	} {
		got, present := record[key]
		if !present {
			t.Errorf("the sink line carries no %q key; every key rides every line, including the zeroes", key)
			continue
		}
		if got != want {
			t.Errorf("%s = %v (%T), want %v (%T)", key, got, got, want, want)
		}
	}
}

// TestTheTelemetryUnitsAndCensusCannotLie is §(vi)'s two repaired defects, and
// each half fails under the shape this event was redesigned away from.
func TestTheTelemetryUnitsAndCensusCannotLie(t *testing.T) {
	t.Parallel()
	alpha, beta := teamRef("team_alpha"), projectRef("project_beta")
	flow, health := contractsv1.ContextFabricFactFlow, contractsv1.ContextFabricFactHealth
	investment, workload := contractsv1.ContextFabricFactInvestment, contractsv1.ContextFabricFactWorkload

	t.Run("count_units names what the two integers count", func(t *testing.T) {
		t.Parallel()
		// A POPULATION-counted row and a KIND-counted row on ONE document.
		// The mixed-kind sameness failure is the kind-counted arm.
		teamReq := operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health, investment, workload)
		projectReq := operandRequirement(contractsv1.ContextFabricSubjectProject, CompletionQuantifierCorroborated, flow, health, investment, workload)
		memberReq := scopeRequirement(CompletionScopeEachMember, contractsv1.ContextFabricSubjectProject, SubjectRoleMember, CompletionQuantifierAtLeastOne, health)

		published := []contractsv1.ContextFabricPlanRequirement{teamReq, projectReq, memberReq}
		frame := namedOperandFrame(SubjectTeam, contractsv1.ContextFabricSubjectProject)
		facts := factsFor(alpha, kindList(flow, health), beta, kindList(investment, workload))
		coverage := factCoverage(flow, SourceAvailable, health, SourceAvailable,
			investment, SourceAvailable, workload, SourceAvailable)

		result := InvestigationResult{
			SubjectResolution: contractsv1.ContextFabricSubjectResolution{Committed: []SubjectRef{alpha, beta}},
			Coverage:          coverage,
		}
		plan := AnswerPlan{Requirements: published}
		result.Completeness.Outcomes = appendReadRequirementEvaluations(nil, published, coverage,
			readPopulationEvidenceFrom(frame, result, plan, facts))
		stamped := plan
		result.AnswerPlan = &stamped

		events := readRequirementPopulationEventsFrom(frame, result, plan, facts, QuestionFamilyScopedCohortStatus)
		if len(events) == 0 {
			t.Fatal("no population events for a document carrying distributive read rows")
		}
		for _, event := range events {
			// The units must match the ARM, and the numbers must match the ROW.
			row := rowFor(t, result.Completeness.Outcomes, event.Requirement)
			if event.Served != row.Served || event.Declared != row.Declared {
				t.Errorf("%s: event %d/%d while the served row says %d/%d -- the run's own artifacts hold two answers",
					event.Requirement, event.Served, event.Declared, row.Served, row.Declared)
			}
			want := countUnitsPopulation
			if row.Impact == contractsv1.ContextFabricAnswerImpactDepth {
				want = countUnitsKind
			}
			if event.Units != want {
				t.Errorf("%s: count_units = %q for an %q row, want %q", event.Requirement, event.Units, row.Impact, want)
			}
		}
		// NON-VACUITY: the document must actually carry BOTH unit kinds, or
		// this arm asserts a single case and calls it a discrimination.
		seen := map[countUnits]bool{}
		for _, event := range events {
			seen[event.Units] = true
		}
		if !seen[countUnitsKind] || !seen[countUnitsPopulation] {
			t.Fatalf("the fixture produced units %v; it must carry BOTH a population-counted and a kind-counted row or it proves nothing", seen)
		}
	})

	t.Run("population_census comes from the authority, not the row's cause", func(t *testing.T) {
		t.Parallel()
		// A cohort the OWNER reports incomplete, whose row's cause is a FACT
		// code by precedence (a member went unread, which outranks the census
		// arm). Deriving the census from `cause_coverage` -- v1's mapping --
		// publishes `enumerated` here and contradicts the authority.
		members := []SubjectRef{projectRef("project_1"), projectRef("project_2")}
		cohort := cohortWith(contractsv1.ContextFabricSubjectProject, members, nil, false)
		requirement := scopeRequirement(CompletionScopeEachMember, contractsv1.ContextFabricSubjectProject, SubjectRoleMember, CompletionQuantifierAtLeastOne, health)
		published := []contractsv1.ContextFabricPlanRequirement{requirement}
		coverage := factCoverage(health, SourceAvailable)
		facts := factsFor(members[0], kindList(health)) // project_2 unread

		result := InvestigationResult{Cohort: cohort, Coverage: coverage}
		plan := AnswerPlan{Requirements: published}
		result.Completeness.Outcomes = appendReadRequirementEvaluations(nil, published, coverage,
			readPopulationEvidenceFrom(nil, result, plan, facts))
		stamped := plan
		result.AnswerPlan = &stamped

		row := rowFor(t, result.Completeness.Outcomes, requirement.Requirement)
		if row.CauseCoverage == contractsv1.ContextFabricCoverageDetailPopulationTruncated {
			t.Fatalf("the premise moved: the row's cause is the census code, so this arm no longer "+
				"distinguishes the authority from the cause (row %+v)", row)
		}
		events := readRequirementPopulationEventsFrom(nil, result, plan, facts, QuestionFamilyScopedCohortStatus)
		if len(events) != 1 {
			t.Fatalf("population events = %d, want 1", len(events))
		}
		if events[0].Census != populationIncomplete {
			t.Fatalf("population_census = %q while the cohort OWNER reports the population incomplete; "+
				"the event must report the authority's verdict, not the row's cause", events[0].Census)
		}
	})
}
