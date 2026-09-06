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

// TestEveryArmsCountUnitsMatchItsQuantity is the SWEEP, and it is the closure
// for a defect that survived 100 % statement coverage.
//
// `rowCountUnits` reads a FINISHED ROW rather than being told by the branch
// that chose the counts, so it can drift from the arms without any line going
// unexecuted. A keystone review reproduced exactly that: `dimension` is carried
// by this layer's not-enumerable arm AND by two KIND-level arms, and labelling
// the latter `population` reported the wrong denominator on 21 combinations.
//
// So this enumerates every arm that can reach the event and asserts its units
// against what the row's numbers actually count. A new arm that acquires a
// silent label fails here.
//
// WHAT IT DOES NOT DO, corrected after a keystone review read this comment as
// a stronger claim than the code makes: it drives CONSTRUCTED rows straight
// through `rowCountUnits`. It does not run the event BUILDER, so it cannot
// show that the builder reaches these arms or that it hands them these rows --
// only that `rowCountUnits` labels each shape correctly once it has one. The
// builder's own reachability is pinned elsewhere; a comment claiming coverage
// this test does not have is how a gap gets banked as closed.
func TestEveryArmsCountUnitsMatchItsQuantity(t *testing.T) {
	t.Parallel()
	for _, arm := range []struct {
		name string
		row  RequirementOutcomeRow
		want countUnits
		why  string
	}{
		{
			name: "read everywhere",
			row:  RequirementOutcomeRow{Impact: contractsv1.ContextFabricAnswerImpactNone, Served: 2, Declared: 2},
			want: countUnitsPopulation, why: "satisfied over the population",
		},
		{
			name: "partially read",
			row: RequirementOutcomeRow{Impact: contractsv1.ContextFabricAnswerImpactScope,
				CauseCoverage: contractsv1.ContextFabricCoverageDetailFactNarrowed, Served: 1, Declared: 2},
			want: countUnitsPopulation, why: "subjects read over subjects declared",
		},
		{
			name: "census incomplete",
			row: RequirementOutcomeRow{Impact: contractsv1.ContextFabricAnswerImpactScope,
				CauseCoverage: contractsv1.ContextFabricCoverageDetailPopulationTruncated, Served: 2, Declared: 2},
			want: countUnitsPopulation, why: "the owner's own population counts",
		},
		{
			name: "sameness shortfall",
			row: RequirementOutcomeRow{Impact: contractsv1.ContextFabricAnswerImpactDepth,
				CauseCoverage: contractsv1.ContextFabricCoverageDetailFactNarrowed, Served: 1, Declared: 2},
			want: countUnitsKind, why: "shared KINDS over the kind standard",
		},
		{
			name: "population NOT ENUMERABLE",
			row: RequirementOutcomeRow{Impact: contractsv1.ContextFabricAnswerImpactDimension,
				CauseCoverage: contractsv1.ContextFabricCoverageDetailReadPopulationUnverified},
			want: countUnitsPopulation, why: "0/0 means nothing was counted OVER A POPULATION",
		},
		{
			// THE ARM THE OLD DERIVATION GOT WRONG.
			name: "KIND-level: the requirement was never planned",
			row: RequirementOutcomeRow{Impact: contractsv1.ContextFabricAnswerImpactDimension,
				CauseCoverage: contractsv1.ContextFabricCoverageDetailRequirementReadNotPlanned,
				Served:        0, Declared: 2},
			want: countUnitsKind, why: "zero SOURCES against the quantifier's own demand",
		},
		{
			// AND ITS SIBLING, also `dimension`, also kind-counted.
			name: "KIND-level: nothing came back fact-bearing",
			row: RequirementOutcomeRow{Impact: contractsv1.ContextFabricAnswerImpactDimension,
				CauseCoverage: contractsv1.ContextFabricCoverageDetailFactProviderReported,
				Served:        0, Declared: 2},
			want: countUnitsKind, why: "kind counts from the kind-level evaluation",
		},
	} {
		arm := arm
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			if got := rowCountUnits(arm.row); got != arm.want {
				t.Fatalf("count_units = %q, want %q -- the row carries %s", got, arm.want, arm.why)
			}
		})
	}

	// NON-VACUITY: the sweep must actually exercise BOTH members, or a
	// function stuck at one value would pass a table that only asserts it.
	seen := map[countUnits]bool{}
	for _, impact := range []contractsv1.ContextFabricAnswerImpactKind{
		contractsv1.ContextFabricAnswerImpactNone,
		contractsv1.ContextFabricAnswerImpactScope,
		contractsv1.ContextFabricAnswerImpactDepth,
		contractsv1.ContextFabricAnswerImpactDimension,
	} {
		seen[rowCountUnits(RequirementOutcomeRow{Impact: impact})] = true
	}
	if !seen[countUnitsKind] || !seen[countUnitsPopulation] {
		t.Fatalf("the sweep produced units %v; it must reach BOTH members or it proves nothing", seen)
	}
}

// TestTheEventBuilderCarriesTheCohortFlagsFromTheDocument closes the third
// mutant r1 confirmed SURVIVES: deleting the cohort-flag copy in the event
// builder.
//
// It also answers r1's separate objection that the sink test hand-builds its
// event: this one drives `readRequirementPopulationEventsFrom` — the real
// builder — over a real served document, so a builder that stops copying the
// flags fails here rather than passing a fixture that never called it.
func TestTheEventBuilderCarriesTheCohortFlagsFromTheDocument(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	members := []SubjectRef{projectRef("project_1"), projectRef("project_2")}
	requirement := scopeRequirement(CompletionScopeEachMember, contractsv1.ContextFabricSubjectProject,
		SubjectRoleMember, CompletionQuantifierAtLeastOne, health)
	published := []contractsv1.ContextFabricPlanRequirement{requirement}
	coverage := factCoverage(health, SourceAvailable)
	facts := factsFor(members[0], kindList(health), members[1], kindList(health))

	// A cohort the owner reports INCOMPLETE and TRUNCATED, so both flags are
	// non-zero: a fixture with both false cannot tell a copied flag from a
	// dropped one.
	cohort := cohortWith(contractsv1.ContextFabricSubjectProject, members, nil, false)
	cohort.Truncated = true

	result := InvestigationResult{Cohort: cohort, Coverage: coverage}
	plan := AnswerPlan{Requirements: published}
	result.Completeness.Outcomes = appendReadRequirementEvaluations(nil, published, coverage,
		readPopulationEvidenceFrom(nil, result, plan, facts))
	stamped := plan
	result.AnswerPlan = &stamped

	events := readRequirementPopulationEventsFrom(nil, result, plan, facts, QuestionFamilyScopedCohortStatus)
	if len(events) != 1 {
		t.Fatalf("population events = %d, want 1", len(events))
	}
	event := events[0]
	if event.CohortComplete {
		t.Fatalf("cohort_complete = %v, want false -- the document's cohort is incomplete", event.CohortComplete)
	}
	if !event.CohortTruncated {
		t.Fatalf("cohort_truncated = %v, want true -- deleting the flag copy leaves it false and "+
			"an operator reads a truncated cohort as intact", event.CohortTruncated)
	}

	// AND THE LINE ITSELF, through the production sink, so the flags are
	// asserted where an operator actually reads them.
	record := readPopulationSink(t, event)
	if record["cohort_truncated"] != true {
		t.Fatalf("cohort_truncated = %v on the sink line, want true", record["cohort_truncated"])
	}
	if record["cohort_complete"] != false {
		t.Fatalf("cohort_complete = %v on the sink line, want false", record["cohort_complete"])
	}
}
