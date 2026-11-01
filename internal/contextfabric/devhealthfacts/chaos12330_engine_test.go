package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func chaos12330ProjectOnly() storage.Principal {
	return storage.Principal{OrgID: projectOnlyEngineOrg, ProjectScopes: []string{projectSubject("linear", "proj-1").CanonicalID}}
}

// chaos12330OrganizationNode is the caller's own organization node, the shape
// the graph projects.
func chaos12330OrganizationNode() map[string]interface{} {
	return map[string]interface{}{"subject_kind": "organization", "canonical_id": organizationSubject(projectOnlyEngineOrg).CanonicalID, "label": "Payments", "authorization_projects": []string{contractsv1.ContextFabricReservedOrganizationScopePrefix + projectOnlyEngineOrg}}
}

// chaos12330Subject is a subject the project-only node rule admits, of a kind
// the capability reads, with the node attributes that admit it.
func chaos12330Subject(t *testing.T, supported []contextfabric.SubjectKind) (contextfabric.SubjectRef, map[string]interface{}) {
	t.Helper()
	project := projectSubject("linear", "proj-1")
	for _, kind := range []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectOrganization, contextfabric.SubjectWorkItem} {
		if !slices.Contains(supported, kind) {
			continue
		}
		switch kind {
		case contextfabric.SubjectOrganization:
			return organizationSubject(projectOnlyEngineOrg), chaos12330OrganizationNode()
		case contextfabric.SubjectWorkItem:
			subject := workItemSubject("repo-1", "WI-1")
			return subject, map[string]interface{}{"subject_kind": "work_item", "canonical_id": subject.CanonicalID, "label": "Payments", "authorization_project_identities": []string{project.CanonicalID}}
		}
		return project, projectOnlyProjectNode(project)
	}
	return project, projectOnlyProjectNode(project)
}

func withheldDetailOf(result contextfabric.InvestigationResult, kind contextfabric.FactKind) *contextfabric.CoverageDetail {
	for i, detail := range result.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailProjectScopeWithheld && detail.FactKind == kind {
			return &result.Coverage.Details[i]
		}
	}
	return nil
}

// assertRefusedInTheAnswer is the state the refusal exists to reach: the reason
// is in the answer, the answer is partial, and nothing in it reads as a zero,
// as complete, or as a source that held no rows.
func assertRefusedInTheAnswer(t *testing.T, name string, run projectOnlyEngineRun, kind contextfabric.FactKind) {
	t.Helper()
	if run.err != nil {
		t.Fatalf("%s: Investigate() error = %v", name, run.err)
	}
	result := run.result
	if len(run.client.queries) != 0 || run.operations.calls != 0 {
		t.Errorf("%s: %d statements and %d operations ran for a refused read", name, len(run.client.queries), run.operations.calls)
	}
	if len(run.model.facts.Facts) != 0 {
		t.Errorf("%s: synthesis was given %d facts", name, len(run.model.facts.Facts))
	}
	if result.Status != contextfabric.InvestigationPartial {
		t.Errorf("%s: status = %q, want partial", name, result.Status)
	}
	if !result.Coverage.Partial {
		t.Errorf("%s: coverage is not partial", name)
	}
	if result.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Errorf("%s: completeness reads complete", name)
	}
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricProjectScopeWithheldLimitation) {
		t.Errorf("%s: limitations = %q, want the project grant sentence", name, result.Limitations)
	}
	detail := withheldDetailOf(result, kind)
	if detail == nil {
		t.Fatalf("%s: no project_scope_withheld detail for the kind in %+v", name, result.Coverage.Details)
	}
	if want := contractsv1.ContextFabricProjectScopeWithheldFactLabel(kind); detail.Label != want {
		t.Errorf("%s: label = %q, want %q", name, detail.Label, want)
	}
	if !detail.Degrading || detail.SourceState != contextfabric.SourceUnauthorized {
		t.Errorf("%s: degrading = %v, state = %q", name, detail.Degrading, detail.SourceState)
	}
	if !slices.Contains(result.Coverage.DegradedReasons, detail.Raw) {
		t.Errorf("%s: degraded reasons %q do not carry %q", name, result.Coverage.DegradedReasons, detail.Raw)
	}
	source := "canonical_fact:" + string(kind)
	found := false
	for _, observation := range result.Coverage.Sources {
		if observation.Source != source {
			continue
		}
		found = true
		if observation.State != contextfabric.SourceUnauthorized {
			t.Errorf("%s: source state = %q, want unauthorized", name, observation.State)
		}
	}
	if !found {
		t.Errorf("%s: no source observation for the kind", name)
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, untrue := range []string{"held no rows", "No " + contractsv1.ContextFabricFactKindLabel(kind) + " facts were found", string(contextfabric.SourceNoData)} {
		if strings.Contains(string(body), untrue) {
			t.Errorf("%s: the answer says %q", name, untrue)
		}
	}
}

// Every capability the production provider set declares, read by a
// project-only caller on a subject the node rule admits: no provider runs and
// the reason reaches the answer. A kind cannot be silently served or left
// unlabeled, and a new provider joins this table by being a provider.
func TestEveryProviderKindIsRefusedToAProjectOnlyCallerInTheAnswer(t *testing.T) {
	t.Parallel()
	providers := devhealthfacts.NewProvidersWithOperations(&fakeClient{}, sourceHealthHolder(&fakeOperationCaller{}))
	if len(providers) < 20 {
		t.Fatalf("found %d providers: the table measured nothing", len(providers))
	}
	covered := 0
	for _, provider := range providers {
		capability := provider.Capability()
		subject, attrs := chaos12330Subject(t, capability.SupportedSubjectKinds)
		if !slices.Contains(capability.SupportedSubjectKinds, subject.Kind) {
			continue // Reached only through scope expansion: pinned at the registry.
		}
		covered++
		run := runProjectOnlyEngine(t, chaos12330ProjectOnly(), subject, attrs, []contextfabric.FactKind{capability.Kind}, contextfabric.RequestedScope{})
		assertRefusedInTheAnswer(t, fmt.Sprintf("%s/%s", capability.Kind, subject.Kind), run, capability.Kind)
	}
	if covered < 15 {
		t.Fatalf("drove %d kinds: the table measured nothing", covered)
	}
}

// Both investment reads and every c3 inventory row, by subject, so a row of the
// CHAOS-12330 table cannot be covered only through another subject kind.
func TestTheInventoryRowsOfTheWithheldTableReachTheAnswer(t *testing.T) {
	t.Parallel()
	project := projectSubject("linear", "proj-1")
	organization := organizationSubject(projectOnlyEngineOrg)
	organizationNode := chaos12330OrganizationNode()
	for _, tc := range []struct {
		kind    contextfabric.FactKind
		subject contextfabric.SubjectRef
		attrs   map[string]interface{}
		reason  string
	}{
		{contextfabric.FactHealth, project, projectOnlyProjectNode(project), "owning teams"},
		{contextfabric.FactFlow, project, projectOnlyProjectNode(project), "owning teams"},
		{contextfabric.FactLandscape, project, projectOnlyProjectNode(project), "owning teams"},
		{contextfabric.FactMetrics, project, projectOnlyProjectNode(project), "owning teams"},
		{contextfabric.FactReadiness, project, projectOnlyProjectNode(project), "owning teams"},
		{contextfabric.FactWorkload, project, projectOnlyProjectNode(project), "owning teams"},
		{contextfabric.FactInvestment, project, projectOnlyProjectNode(project), "placed by repository"},
		{contextfabric.FactInvestment, organization, organizationNode, "span the organization"},
		{contextfabric.FactSourceHealth, organization, organizationNode, "organization-wide"},
		{contextfabric.FactPullRequests, project, projectOnlyProjectNode(project), "pull requests"},
		{contextfabric.FactBlockers, project, projectOnlyProjectNode(project), "exact project membership"},
		{contextfabric.FactCommitment, project, projectOnlyProjectNode(project), "exact project membership"},
		{contextfabric.FactActualCompletion, project, projectOnlyProjectNode(project), "exact project membership"},
	} {
		name := fmt.Sprintf("%s/%s", tc.kind, tc.subject.Kind)
		run := runProjectOnlyEngine(t, chaos12330ProjectOnly(), tc.subject, tc.attrs, []contextfabric.FactKind{tc.kind}, contextfabric.RequestedScope{})
		assertRefusedInTheAnswer(t, name, run, tc.kind)
		if detail := withheldDetailOf(run.result, tc.kind); detail != nil && !strings.Contains(detail.Label, tc.reason) {
			t.Errorf("%s: label %q does not state %q", name, detail.Label, tc.reason)
		}
	}
}

// Every other class is served as before: providers run and nothing is withheld.
func TestOtherClassesAreNotRefusedTheFactRead(t *testing.T) {
	t.Parallel()
	project := projectSubject("linear", "proj-1")
	for name, principal := range map[string]storage.Principal{
		"unrestricted": {OrgID: projectOnlyEngineOrg},
		"universal":    {OrgID: projectOnlyEngineOrg, RepositoryScopes: []string{"*"}},
		"restricted":   {OrgID: projectOnlyEngineOrg, RepositoryScopes: []string{"acme/allowed"}},
	} {
		run := runProjectOnlyEngine(t, principal, project, projectOnlyProjectNode(project), []contextfabric.FactKind{contextfabric.FactHealth}, contextfabric.RequestedScope{})
		if run.err != nil {
			t.Fatalf("%s: Investigate() error = %v", name, run.err)
		}
		if len(run.client.queries) == 0 {
			t.Errorf("%s: no statement ran; this pin lost its control", name)
		}
		if withheldDetailOf(run.result, contextfabric.FactHealth) != nil {
			t.Errorf("%s: a %s caller was refused a fact", name, name)
		}
		for _, limitation := range run.result.Limitations {
			if limitation == contractsv1.ContextFabricProjectScopeWithheldLimitation {
				t.Errorf("%s: limitations carry the project grant sentence", name)
			}
		}
	}
}

// Ruling B, the state: a project-only request that names projects stops before
// every read, the narrowing resolver's included. It is never answered over the
// whole grant and never reads as nothing found.
func TestAProjectNarrowedRequestIsRefusedBeforeEveryRead(t *testing.T) {
	t.Parallel()
	project := projectSubject("linear", "proj-1")
	for name, ids := range map[string][]string{"an id": {"proj-1"}, "several": {"proj-1", "proj-2"}, "a wildcard": {"*"}} {
		run := runProjectOnlyEngine(t, chaos12330ProjectOnly(), project, projectOnlyProjectNode(project), []contextfabric.FactKind{contextfabric.FactHealth}, contextfabric.RequestedScope{ProjectIDs: ids})
		if run.err != nil {
			t.Fatalf("%s: Investigate() error = %v", name, run.err)
		}
		if got := []int{run.graph.narrowings, run.graph.resolved, run.graph.discovered, run.model.interpreted, run.model.synthesized, len(run.client.queries)}; !slices.Equal(got, []int{0, 0, 0, 0, 0, 0}) {
			t.Errorf("%s: narrowings, resolved, discovered, interpreted, synthesized, statements = %v, want none", name, got)
		}
		result := run.result
		if result.Status != contextfabric.InvestigationNoMatch || !result.Coverage.Partial {
			t.Errorf("%s: status = %q, partial = %v", name, result.Status, result.Coverage.Partial)
		}
		if result.DeterministicAnswer != contractsv1.ContextFabricRequestedProjectNarrowingUnavailableLimitation {
			t.Errorf("%s: answer = %q", name, result.DeterministicAnswer)
		}
		if !slices.Contains(result.Limitations, contractsv1.ContextFabricRequestedProjectNarrowingUnavailableLimitation) {
			t.Errorf("%s: limitations = %q", name, result.Limitations)
		}
		if len(result.Coverage.Details) != 1 {
			t.Fatalf("%s: details = %+v, want the refusal", name, result.Coverage.Details)
		}
		detail := result.Coverage.Details[0]
		if detail.Code != contractsv1.ContextFabricCoverageDetailProjectScopeWithheld || detail.Kind != contextfabric.SubjectProject || detail.FactKind != "" || !detail.Degrading || detail.SourceState != contextfabric.SourceUnauthorized {
			t.Errorf("%s: detail = %+v", name, detail)
		}
		if !strings.EqualFold(detail.Label, "requested project narrowing not available yet") {
			t.Errorf("%s: label = %q", name, detail.Label)
		}
		// The refusal is a document: a reader of the result id gets the same one.
		stored, err := run.results.Get(context.Background(), chaos12330ProjectOnly(), result.ResultID)
		if err != nil {
			t.Fatalf("%s: the refusal was not saved: %v", name, err)
		}
		if stored.Result.Status != contextfabric.InvestigationNoMatch {
			t.Errorf("%s: stored status = %q", name, stored.Result.Status)
		}
	}
}

// The stop is only for a project-only caller, and only when projects are named.
func TestTheNarrowingStopFiresOnlyForAProjectOnlyRequestThatNamesProjects(t *testing.T) {
	t.Parallel()
	project := projectSubject("linear", "proj-1")
	kinds := []contextfabric.FactKind{contextfabric.FactHealth}
	unrestricted := runProjectOnlyEngine(t, storage.Principal{OrgID: projectOnlyEngineOrg}, project, projectOnlyProjectNode(project), kinds, contextfabric.RequestedScope{ProjectIDs: []string{"proj-1"}})
	if unrestricted.err != nil || unrestricted.graph.narrowings != 1 || unrestricted.model.interpreted != 1 {
		t.Errorf("an unrestricted narrowed request: err %v, narrowings %d, interpreted %d, want it resolved and interpreted", unrestricted.err, unrestricted.graph.narrowings, unrestricted.model.interpreted)
	}
	plain := runProjectOnlyEngine(t, chaos12330ProjectOnly(), project, projectOnlyProjectNode(project), kinds, contextfabric.RequestedScope{})
	if plain.err != nil || plain.graph.resolved != 1 || plain.model.interpreted != 1 {
		t.Errorf("a project-only request naming no project: err %v, resolved %d, interpreted %d, want it investigated", plain.err, plain.graph.resolved, plain.model.interpreted)
	}
}

// Graph-kind details: only when the plan asks for the kind. The sentence states
// the grant class, and nothing says a subject of the kind exists.
func TestAProjectOnlyCallerIsToldTheKindItsPlanAskedFor(t *testing.T) {
	t.Parallel()
	project := projectSubject("linear", "proj-1")
	for kind, want := range map[contextfabric.SubjectKind]string{
		contextfabric.SubjectTeam:        "Teams are not in a project grant yet",
		contextfabric.SubjectPullRequest: "Pull requests are not in a project grant yet",
		contextfabric.SubjectRepository:  "Repositories are not in a project grant",
	} {
		model := &projectOnlyEngineModel{kinds: []contextfabric.FactKind{contextfabric.FactHealth}, memberKind: kind}
		run := runProjectOnlyEngineWith(t, chaos12330ProjectOnly(), project, projectOnlyProjectNode(project), model, contextfabric.RequestedScope{})
		if run.err != nil {
			t.Fatalf("%s: Investigate() error = %v", kind, run.err)
		}
		if run.result.AnswerPlan == nil {
			t.Fatalf("%s: no plan", kind)
		}
		// The kind is asked for and found by nothing: no cohort, so no member
		// kind. "None found" is a claim the search could not make.
		if run.result.AnswerPlan.MemberKind != "" {
			t.Fatalf("%s: plan member kind = %q, want the requirement rows to be what names the kind", kind, run.result.AnswerPlan.MemberKind)
		}
		for _, detail := range run.result.Coverage.Details {
			if detail.Code == contractsv1.ContextFabricCoverageDetailGraphNoMemberFound {
				t.Errorf("%s: the answer claims none found: %q", kind, detail.Label)
			}
		}
		var details []contextfabric.CoverageDetail
		for _, detail := range run.result.Coverage.Details {
			if detail.Code == contractsv1.ContextFabricCoverageDetailProjectScopeWithheld && detail.Kind != "" {
				details = append(details, detail)
			}
		}
		if len(details) != 1 || details[0].Kind != kind || details[0].Label != want || !details[0].Degrading {
			t.Errorf("%s: details = %+v, want one saying %q", kind, details, want)
		}
		if !slices.Contains(run.result.Coverage.DegradedReasons, want) {
			t.Errorf("%s: degraded reasons %q do not carry it", kind, run.result.Coverage.DegradedReasons)
		}
	}
	// A plan that asks for no refused kind gets no such detail.
	plain := runProjectOnlyEngine(t, chaos12330ProjectOnly(), project, projectOnlyProjectNode(project), []contextfabric.FactKind{contextfabric.FactHealth}, contextfabric.RequestedScope{})
	for _, detail := range plain.result.Coverage.Details {
		if detail.Kind != "" {
			t.Errorf("a plan asking for no kind got %+v", detail)
		}
	}
}
