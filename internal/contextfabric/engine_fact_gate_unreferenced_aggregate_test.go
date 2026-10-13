package contextfabric

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// projectRollupFacts serves one project pull request rollup: aggregate counts
// over every linked pull request, with no declared reference to any of them.
type projectRollupFacts struct{ subject SubjectRef }

func (r projectRollupFacts) ReadFacts(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
	facts := []CanonicalFact{}
	for _, subject := range request.Subjects {
		if subject.Kind != SubjectProject {
			continue
		}
		facts = append(facts, CanonicalFact{
			Kind: FactPullRequests, Subject: subject,
			Fields:      map[string]FactValue{"pull_requests_open_current": IntegerFactValue(4), "rollup_basis": StringFactValue("project_issues")},
			SourceState: SourceAvailable, Source: "test", SourceVersion: "v1",
		}, CanonicalFact{
			Kind: FactBlockers, Subject: subject,
			Fields:      map[string]FactValue{"blocked_work_items_current": IntegerFactValue(2)},
			SourceState: SourceAvailable, Source: "test", SourceVersion: "v1",
		})
	}
	return CanonicalFactBundle{
		Facts: facts, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
	}, nil
}

func (projectRollupFacts) Capabilities() []FactCapability {
	return []FactCapability{{Kind: FactPullRequests, Fields: []FactFieldDeclaration{
		{Name: "pull_requests_open_current", Type: FactFieldInteger, Aggregate: true, SubjectKinds: []SubjectKind{SubjectProject}},
		{Name: "rollup_basis", Type: FactFieldString, SubjectKinds: []SubjectKind{SubjectProject}},
	}}, {Kind: FactBlockers, Fields: []FactFieldDeclaration{
		{Name: "blocked_work_items_current", Type: FactFieldInteger, CallerScoped: true, SubjectKinds: []SubjectKind{SubjectProject}},
	}}}
}

func projectRollupEngine(t *testing.T, graph GraphReader, synthesized *[]SynthesisInput) *Engine {
	t.Helper()
	interpretation := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactPullRequests}, {Kind: FactBlockers}},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graph,
		Facts: projectRollupFacts{},
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			*synthesized = append(*synthesized, input)
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Nominal.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Nominal based on available context.", Warnings: []string{},
				Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"},
			}, nil
		}),
		Results: &staticResultStore{results: map[string]InvestigationResult{}}, Telemetry: &recordingTelemetry{},
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() }, NewResultID: func() string { return "result_99999999" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// A repository-restricted caller investigating a project gets the project's
// pull request rollup labelled with its aggregate scope: the counts span
// linked pull requests in repositories outside the grant, and the fact names
// none of them, so nothing is withheld that would otherwise carry the label.
// The direct read labels the same fact.
func TestEngineLabelsTheAggregateScopeOfAProjectRollupThatCitesNoLinkedSubject(t *testing.T) {
	for _, tc := range []struct {
		name      string
		principal storage.Principal
		labelled  bool
	}{
		{"restricted", storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/allowed"}}, true},
		{"unrestricted", storage.Principal{OrgID: "org_1"}, false},
	} {
		graph, project := chaos7080ProjectGraph(StoredSubjectAdmitted, nil)
		var synthesized []SynthesisInput
		if _, err := projectRollupEngine(t, graph, &synthesized).Investigate(context.Background(), tc.principal, validInvestigationRequest()); err != nil {
			t.Fatalf("%s: Investigate: %v", tc.name, err)
		}
		var rollup, blockers *CanonicalFact
		for _, input := range synthesized {
			for i, fact := range input.Facts.Facts {
				switch {
				case fact.Kind == FactPullRequests && fact.Subject == project:
					rollup = &input.Facts.Facts[i]
				case fact.Kind == FactBlockers && fact.Subject == project:
					blockers = &input.Facts.Facts[i]
				}
			}
		}
		// A caller-scoped count is over the caller's own items: no aggregate.
		if blockers == nil {
			t.Fatalf("%s: the project blocker rollup did not reach synthesis (measurement did not happen)", tc.name)
		}
		if _, ok := blockers.Fields[FactFieldAggregateScope]; ok {
			t.Errorf("%s: a caller-scoped project rollup carries aggregate_scope", tc.name)
		}
		if rollup == nil {
			t.Fatalf("%s: the project rollup did not reach synthesis (measurement did not happen)", tc.name)
		}
		label, ok := rollup.Fields[FactFieldAggregateScope]
		switch {
		case tc.labelled && (!ok || label.String == nil || *label.String != AggregateScopeAllOwnedRepositories):
			t.Errorf("%s: aggregate_scope = %+v, want %q", tc.name, label, AggregateScopeAllOwnedRepositories)
		case !tc.labelled && ok:
			t.Errorf("%s: an ungated caller's fact carries aggregate_scope", tc.name)
		}
	}
}
