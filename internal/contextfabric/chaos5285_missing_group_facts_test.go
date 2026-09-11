package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// groupKindFact is one fact of the given kind rooted on a GROUP identity, as a
// group-rooted read returns it.
func groupKindFact(rawTeamKey string, kind FactKind) CanonicalFact {
	return CanonicalFact{
		Kind:        kind,
		Subject:     SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID(rawTeamKey), Label: rawTeamKey},
		Fields:      map[string]FactValue{"severity": StringFactValue("elevated")},
		SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
}

// groupReadServing answers the member read with the two-member fixture and the
// group read with health AND workload facts -- two kinds, which is what the
// `corroborated` quantifier demands -- for exactly the groups named in served.
// A group not named gets nothing, and no coverage observation says anything
// about it: its absence is silence, not a reported failure.
func groupReadServing(served ...string) *groupReadRecorder {
	return &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind != SubjectTeam {
				continue
			}
			for _, raw := range served {
				bundle.Facts = append(bundle.Facts, groupKindFact(raw, FactHealth), groupKindFact(raw, FactWorkload))
			}
			bundle.Coverage.Sources = []SourceObservation{
				{Source: "canonical_fact:health", State: SourceAvailable},
				{Source: "canonical_fact:workload", State: SourceAvailable},
			}
			return bundle
		}
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
}

// servedRequirementRows returns the ASSEMBLED-RESULT rows of the served
// document for the one published requirement whose scope is `scope`, plus that
// requirement's identity. More than one published requirement at that scope is
// a fixture defect, reported rather than silently resolved to the first.
func servedRequirementRows(t *testing.T, result InvestigationResult, scope CompletionScope) (string, []RequirementOutcomeRow) {
	t.Helper()
	if result.AnswerPlan == nil {
		t.Fatalf("the served document carries no answer plan, so no requirement row can be read")
	}
	identity := ""
	for _, requirement := range result.AnswerPlan.Requirements {
		if requirement.Scope != string(scope) {
			continue
		}
		if identity != "" {
			t.Fatalf("fixture defect: two published requirements at scope %q (%q, %q)", scope, identity, requirement.Requirement)
		}
		identity = requirement.Requirement
	}
	if identity == "" {
		t.Fatalf("the served plan publishes no requirement at scope %q", scope)
	}
	var rows []RequirementOutcomeRow
	for _, row := range result.Completeness.Outcomes {
		if row.Requirement == identity && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			rows = append(rows, row)
		}
	}
	return identity, rows
}

// TestMissingGroupFactsStayMissingAfterTheSecondRead is CHAOS-5285 test-table
// row 4: one of two groups served => `1/2 narrowed`.
//
// The second read makes `each_group` SATISFIABLE; it must not make it
// satisfied by default. A group the provider returned nothing for is a group
// the answer has no evidence about, and the only honest row for "one of two
// teams was read" is narrowed 1/2 -- scope, not depth, because the caller is
// shown fewer teams and what stands behind the one that remains is unchanged.
//
// WHY THIS FIXTURE DISCRIMINATES. Both members HAVE member evidence --
// project_a under team_security and project_b under team_platform each carry
// an available metrics fact. So an implementation that projected member
// evidence onto the groups, or credited a group with its members' reads,
// would report team_platform as read and the row as 2/2 satisfied. Only a
// row computed from the group read's OWN facts reads 1/2.
//
// RED where the second read does not exist: with no group-rooted request, no
// group fact reaches the evaluation and the row cannot read 1/2.
func TestMissingGroupFactsStayMissingAfterTheSecondRead(t *testing.T) {
	t.Parallel()

	recorder := groupReadServing("team_security")
	engine, request := groupReadEngineFixture(t, &recordingTelemetry{}, recorder)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	for index := range recorder.requests {
		t.Logf("fact request %d: root kinds=%v roots=%v", index, recorder.rootKinds(index), recorder.rootIDs(index))
	}
	identity, rows := servedRequirementRows(t, result, CompletionScopeEachGroup)
	for _, row := range rows {
		t.Logf("each_group row %q: outcome=%q impact=%q served=%d declared=%d cause=%q observed=%v",
			identity, row.Outcome, row.Impact, row.Served, row.Declared, row.CauseCoverage, row.CauseObserved)
	}
	if len(rows) != 1 {
		t.Fatalf("assembled-result rows for %q = %d, want exactly 1", identity, len(rows))
	}
	row := rows[0]
	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed || row.Served != 1 || row.Declared != 2 {
		t.Errorf("each_group row = %q %d/%d, want %q 1/2 -- one of two groups was read and the row must say exactly that, neither the 0/2 of a turn that never read its groups nor the 2/2 of member evidence projected onto them",
			row.Outcome, row.Served, row.Declared, contractsv1.ContextFabricRequirementNarrowed)
	}
	if row.Impact != contractsv1.ContextFabricAnswerImpactScope {
		t.Errorf("impact = %q, want %q -- an unread group removes a subject from the answer, it does not thin the evidence behind the read one",
			row.Impact, contractsv1.ContextFabricAnswerImpactScope)
	}
	// NOBODY REPORTED the missing group -- the provider was silent about it --
	// so the cause is the inferred one and the flag says inferred.
	if row.CauseCoverage != contractsv1.ContextFabricCoverageDetailFactNarrowed || row.CauseObserved {
		t.Errorf("cause = %q observed=%v, want %q observed=false -- a group no provider said anything about is absent by silence, and claiming a mechanism reported it would be a cause nobody named",
			row.CauseCoverage, row.CauseObserved, contractsv1.ContextFabricCoverageDetailFactNarrowed)
	}
	// The denominator is the POPULATION's: the unread group is still in the
	// served group list.
	if result.Cohort == nil || len(result.Cohort.Groups) != 2 {
		t.Errorf("served groups = %v, want both -- an unread group that disappears turns 1/2 into a complete-looking 1/1", result.Cohort)
	}
}

// TestBothGroupsReadMakeTheGroupRowSatisfied is the DISCRIMINATING CONTROL for
// row 4: the same fixture with BOTH groups returned must read 2/2 satisfied.
//
// Without it, a row that reports narrowed 1/2 on every grouped turn -- a
// hard-coded denominator, or an evaluator that never credits a second group
// -- would pass the pin above.
func TestBothGroupsReadMakeTheGroupRowSatisfied(t *testing.T) {
	t.Parallel()

	engine, request := groupReadEngineFixture(t, &recordingTelemetry{}, groupReadServing("team_security", "team_platform"))
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	identity, rows := servedRequirementRows(t, result, CompletionScopeEachGroup)
	if len(rows) != 1 {
		t.Fatalf("CONTROL BROKEN: assembled-result rows for %q = %d, want exactly 1", identity, len(rows))
	}
	row := rows[0]
	t.Logf("each_group row %q: outcome=%q served=%d declared=%d", identity, row.Outcome, row.Served, row.Declared)
	if row.Outcome != contractsv1.ContextFabricRequirementSatisfied || row.Served != 2 || row.Declared != 2 {
		t.Fatalf("CONTROL BROKEN: each_group row = %q %d/%d with both groups read, want %q 2/2 -- if a fully-read group axis cannot certify, the 1/2 pin measures nothing",
			row.Outcome, row.Served, row.Declared, contractsv1.ContextFabricRequirementSatisfied)
	}
}
