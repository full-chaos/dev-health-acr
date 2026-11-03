package contextfabric_test

import (
	"bytes"
	"log/slog"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12710 Fix A over the PRODUCTION capability list: the plan kinds per
// family before and after the committed-subject widening. BEFORE is the plan
// without the committed subject's required read rows (the only rows the
// widening reads); AFTER is the production plan. Every added kind must be
// required by one of the frame's own obligations for the committed subject's
// kind: the obligation seed serves that cell from at most as many kinds as a
// read quantifier demands (two), so the cell cannot be met without it.
func TestPerFamilyPlanKindsBeforeAndAfterTheCommittedSubjectWidening(t *testing.T) {
	capabilities := liveCapabilityList(t)
	seed := contextfabric.GenerateObligationSeed(capabilities)
	assess := []contextfabric.InvestigationGoal{contextfabric.GoalAssessState}
	current := contextfabric.TemporalIntentCurrent
	workload := contextfabric.InterpretedQuestion{FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactWorkload}}}
	// A scoped cohort's plan is the ranking formula's kinds, unchanged: no
	// committed subject, no subject-role row.
	cohortKinds := []contextfabric.FactKind{contextfabric.FactHealth, contextfabric.FactWorkload, contextfabric.FactReadiness, contextfabric.FactOperationalDeficiencies, contextfabric.FactInvestment}
	for _, cell := range []struct {
		id      string
		frame   contextfabric.QuestionFrame
		model   contextfabric.InterpretedQuestion
		subject contextfabric.SubjectKind
		before  []contextfabric.FactKind
		after   []contextfabric.FactKind
	}{
		{"Q1", traceBuildFrame(assess, traceNamed(contextfabric.SubjectTeam), current, nil, nil), contextfabric.InterpretedQuestion{}, contextfabric.SubjectTeam,
			nil, []contextfabric.FactKind{contextfabric.FactHealth}},
		{"Q1p", traceBuildFrame(assess, traceNamed(contextfabric.SubjectProject), current, nil, nil), contextfabric.InterpretedQuestion{}, contextfabric.SubjectProject,
			nil, []contextfabric.FactKind{contextfabric.FactHealth}},
		{"QB", traceBuildFrame(assess, traceScoped(contextfabric.SubjectProject), current, nil, nil), contextfabric.InterpretedQuestion{}, "",
			cohortKinds, cohortKinds},
		{"C5", traceBuildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate}, traceScoped(contextfabric.SubjectTeam), current, nil, nil), contextfabric.InterpretedQuestion{}, "",
			cohortKinds, cohortKinds},
		{"Nrepo", traceBuildFrame(assess, traceNamed(contextfabric.SubjectRepository), current, nil, nil), contextfabric.InterpretedQuestion{}, contextfabric.SubjectRepository,
			nil, []contextfabric.FactKind{contextfabric.FactHealth}},
		{"Npr", traceBuildFrame(assess, traceNamed(contextfabric.SubjectPullRequest), current, nil, nil), contextfabric.InterpretedQuestion{}, contextfabric.SubjectPullRequest,
			nil, []contextfabric.FactKind{contextfabric.FactPullRequests}},
		{"Nteam", traceBuildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState, contextfabric.GoalExplainDrivers}, traceNamed(contextfabric.SubjectTeam), current, nil, nil), contextfabric.InterpretedQuestion{}, contextfabric.SubjectTeam,
			nil, []contextfabric.FactKind{contextfabric.FactHealth}},
		{"F1", traceBuildFrame(assess, traceNamed(contextfabric.SubjectTeam), current, nil, []contextfabric.HealthDimension{contextfabric.HealthDimensionExecutionCompletion}), workload, contextfabric.SubjectTeam,
			[]contextfabric.FactKind{contextfabric.FactWorkload}, []contextfabric.FactKind{contextfabric.FactWorkload, contextfabric.FactHealth}},
	} {
		rows := contextfabric.DeriveRequirements(cell.frame, seed, capabilities)
		var withoutSubjectReads []contextfabric.DerivedRequirement
		for _, row := range rows {
			if row.Kind == contextfabric.ObligationKindRead && row.Role == contextfabric.SubjectRoleSubject {
				continue
			}
			withoutSubjectReads = append(withoutSubjectReads, row)
		}
		plan := func(requirements []contextfabric.DerivedRequirement) []contextfabric.FactKind {
			return contextfabric.PlanAnswer(contextfabric.PlanAnswerInput{
				Family:         contextfabric.QuestionFamilyOutcome{Family: contextfabric.DeriveQuestionFamily(cell.frame).Family, Source: contextfabric.QuestionFamilySourceModel},
				Interpretation: cell.model, Requirements: requirements,
			}).FactKinds
		}
		before, after := plan(withoutSubjectReads), plan(rows)
		if !slices.Equal(before, cell.before) || !slices.Equal(after, cell.after) {
			t.Errorf("%s: plan before = %v after = %v, want before = %v after = %v", cell.id, before, after, cell.before, cell.after)
		}
		for _, kind := range after {
			if slices.Contains(before, kind) {
				continue
			}
			required := false
			for _, obligation := range cell.frame.Obligations {
				served := seed.KindsFor(obligation, cell.subject)
				if slices.Contains(served, kind) && len(served) <= 2 {
					required = true
				}
			}
			if !required {
				t.Errorf("%s: added %s, which no frame obligation %v requires for a %s", cell.id, kind, cell.frame.Obligations, cell.subject)
			}
		}
	}
}

// TestPlanSubjectObligationReadsLineCertifies pins the line's keys and its
// closed vocabulary: added_kinds is the fact-kind vocabulary, nothing else,
// and a token outside it fails certification.
func TestPlanSubjectObligationReadsLineCertifies(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordSubjectObligationReadKinds(ctx, principalOrg1, 1, 1, []contextfabric.FactKind{contextfabric.FactHealth})
	pinKeys(t, bytes.NewBuffer(buf.Bytes()), []string{"time", "level", "msg", "org_id", "subject_rows", "kinds_named", "kinds_added", "added_kinds", "request_id"}, 1)
	certifyRequestLine(t, buf.Bytes(), eventspec.PlanSubjectObligationReads, map[string]any{"org_id": "org_1", "kinds_added": 1})

	var vocabulary []string
	for _, kind := range contractsv1.ContextFabricFactKindVocabulary() {
		vocabulary = append(vocabulary, string(kind))
	}
	var addedKinds *eventspec.Field
	for index := range eventspec.PlanSubjectObligationReads.Fields {
		if eventspec.PlanSubjectObligationReads.Fields[index].Key == "added_kinds" {
			addedKinds = &eventspec.PlanSubjectObligationReads.Fields[index]
		}
	}
	if addedKinds == nil || addedKinds.Type != eventspec.FieldStringSlice || !slices.Equal(addedKinds.ClosedVocabulary, vocabulary) {
		t.Fatalf("added_kinds field = %+v, want a string slice over exactly the fact-kind vocabulary %v", addedKinds, vocabulary)
	}

	var outside bytes.Buffer
	outsideSink, outsideCtx := answerLineSink(&outside, slog.LevelInfo)
	outsideSink.RecordSubjectObligationReadKinds(outsideCtx, principalOrg1, 1, 1, []contextfabric.FactKind{"not_a_kind"})
	log, err := certify.Parse(outside.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.PlanSubjectObligationReads, Want: map[string]any{"org_id": "org_1"}}); err == nil {
		t.Fatalf("a line naming a kind outside the fact-kind vocabulary certified:\n%s", outside.Bytes())
	}
}
