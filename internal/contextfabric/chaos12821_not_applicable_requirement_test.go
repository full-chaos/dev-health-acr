package contextfabric

import (
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12821: a provider can report a fact kind `not_applicable` for a subject. On
// askdev-1 the jira ML team's health came back "the requested teams own projects; no
// linked pull request reaches a repository ... this is not a measured zero", and the
// read-requirement evaluator counted that as a failure: health/subject/team was published
// `unavailable` and the answer `degraded`. The fact layer had already decided otherwise
// (factStateDegrades does not degrade on not_applicable), and North Star check 12 keeps
// not-applicable apart from missing.
//
// The rule: a single-subject read row whose EVERY declared kind came back not_applicable
// from its provider is published `not_applicable` (impact none, cause
// fact_provider_reported, observed). A row with any other kind in play keeps today's arms,
// so a not-applicable kind can never make a row look served.

const subjectStateRequirement = "state/subject/team"

func TestAHealthReadItsProviderReportsNotApplicableIsPublishedNotApplicable(t *testing.T) {
	t.Parallel()
	for _, team := range providerMatrixTeams {
		team := team
		t.Run(team.CanonicalID, func(t *testing.T) {
			t.Parallel()

			// The askdev-1 ML shape: no model requirement, so health is the only planned kind.
			turn := runSubjectObligationTurnWith(t, team, SourceNotApplicable, []FactRequirement{})
			if !slices.Equal(turn.reads[0], []FactKind{FactHealth}) {
				t.Fatalf("fact request kinds = %v, want [health]", turn.reads[0])
			}
			health := assembledRow(t, turn.result, subjectHealthRequirement)
			if health.Outcome != contractsv1.ContextFabricRequirementNotApplicable ||
				health.Impact != contractsv1.ContextFabricAnswerImpactNone ||
				health.CauseCoverage != contractsv1.ContextFabricCoverageDetailFactProviderReported ||
				!health.CauseObserved || health.Served != 0 || health.Declared != 0 {
				t.Fatalf("%s = %+v, want not_applicable, impact none, fact_provider_reported observed, 0/0", subjectHealthRequirement, health)
			}
			// No false complete: the only state kind read does not apply, so nothing
			// establishes the team's state, and the answer still says so.
			state := assembledRow(t, turn.result, subjectStateRequirement)
			if state.Outcome != contractsv1.ContextFabricRequirementUnavailable {
				t.Fatalf("%s = %s/%s, want unavailable", subjectStateRequirement, state.Outcome, state.CauseCoverage)
			}
			if turn.result.Completeness.State != contractsv1.ContextFabricAnswerCompletenessDegraded {
				t.Fatalf("completeness = %s, want degraded (state is unavailable)", turn.result.Completeness.State)
			}
			if correction := turn.result.Completeness.StatusCorrection; correction == nil || correction.DecidingRequirement != subjectStateRequirement {
				t.Fatalf("status correction = %+v, want the deciding requirement %s, never health", correction, subjectStateRequirement)
			}

			// The forecast turn's shape: workload is planned too and serves state, so the
			// answer is short only by the state standard, not by a health "loss".
			withWorkload := runSubjectObligationTurn(t, team, SourceNotApplicable)
			if row := assembledRow(t, withWorkload.result, subjectHealthRequirement); row.Outcome != contractsv1.ContextFabricRequirementNotApplicable {
				t.Fatalf("with workload: %s = %s/%s, want not_applicable", subjectHealthRequirement, row.Outcome, row.CauseCoverage)
			}
			if row := assembledRow(t, withWorkload.result, subjectStateRequirement); row.Outcome != contractsv1.ContextFabricRequirementNarrowed || row.Served != 1 || row.Declared != 2 {
				t.Fatalf("with workload: %s = %s %d/%d, want narrowed 1/2", subjectStateRequirement, row.Outcome, row.Served, row.Declared)
			}
			if got := withWorkload.result.Completeness.State; got != contractsv1.ContextFabricAnswerCompletenessPartial {
				t.Fatalf("with workload: completeness = %s, want partial", got)
			}
		})
	}
}

// The arm's own clauses, one case each, at the row builder.
func TestOnlyARowWhoseEveryDeclaredKindDoesNotApplyIsNotApplicable(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	workload := contractsv1.ContextFabricFactWorkload
	healthOnly := readRequirement(CompletionQuantifierAtLeastOne)
	healthOnly.Requirement, healthOnly.Obligation = subjectHealthRequirement, string(ObligationHealth)
	healthOnly.FactKinds = []FactKind{health}
	twoKinds := readRequirement(CompletionQuantifierAtLeastOne)
	eachMember := readRequirement(CompletionQuantifierAtLeastOne)
	eachMember.Requirement, eachMember.Role, eachMember.Scope = "state/member/team", string(SubjectRoleMember), string(CompletionScopeEachMember)
	eachMember.FactKinds = []FactKind{health}
	carried := factCoverage(health, SourceNotApplicable)
	carried.Details = []CoverageDetail{{Code: contractsv1.ContextFabricCoverageDetailFactReadFailed, FactKind: health, SourceState: SourceNotApplicable}}

	for _, tc := range []struct {
		name        string
		requirement contractsv1.ContextFabricPlanRequirement
		coverage    Coverage
		want        contractsv1.ContextFabricPlanRequirementOutcome
	}{
		{"the one declared kind does not apply", healthOnly, factCoverage(health, SourceNotApplicable), contractsv1.ContextFabricRequirementNotApplicable},
		{"every declared kind does not apply", twoKinds, factCoverage(health, SourceNotApplicable, workload, SourceNotApplicable), contractsv1.ContextFabricRequirementNotApplicable},
		{"a declared kind was not read", twoKinds, factCoverage(health, SourceNotApplicable), contractsv1.ContextFabricRequirementUnavailable},
		{"another declared kind served", twoKinds, factCoverage(health, SourceNotApplicable, workload, SourceAvailable), contractsv1.ContextFabricRequirementNarrowed},
		{"another declared kind is missing", twoKinds, factCoverage(health, SourceNotApplicable, workload, SourceNoData), contractsv1.ContextFabricRequirementUnavailable},
		{"a distributive row keeps today's arm", eachMember, factCoverage(health, SourceNotApplicable), contractsv1.ContextFabricRequirementUnavailable},
		{"a carried cause other than the provider's report keeps today's arm", healthOnly, carried, contractsv1.ContextFabricRequirementUnavailable},
		{"no data is missing, not inapplicable", healthOnly, factCoverage(health, SourceNoData), contractsv1.ContextFabricRequirementUnavailable},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			threshold, ok := readQuantifierThreshold(tc.requirement.Quantifier)
			if !ok {
				t.Fatal("unrecognised quantifier")
			}
			evidence := evaluateReadRequirement(tc.requirement, tc.coverage, nil)
			row, published, cover := readRequirementOutcomeRow(tc.requirement, threshold, evidence, readPopulationEvidence{})
			if !published {
				t.Fatal("no row published")
			}
			if row.Outcome != tc.want {
				t.Fatalf("outcome = %s/%s, want %s", row.Outcome, row.CauseCoverage, tc.want)
			}
			if cover == nil || cover.Outcome != tc.want {
				t.Fatalf("cover outcome = %+v, want %s: the trace must name the row's own decision", cover, tc.want)
			}
			if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(contractsv1.ContextFabricPlanRequirementOutcomeRow(row)); err != nil {
				t.Fatalf("published row is not valid: %v", err)
			}
		})
	}
}
