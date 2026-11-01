package devhealthfacts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
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

// chaos12330Case is how one fact kind is driven: a subject the capability
// supports, and the node attributes that subject carries.
type chaos12330Case struct {
	subject contextfabric.SubjectRef
	attrs   map[string]interface{}
	// denied: the node rule denies the subject to a project-only caller, so the
	// graph commits it anyway and the registry's refusal is what is proved.
	denied bool
}

func chaos12330ProjectCase() chaos12330Case {
	project := projectSubject("linear", "proj-1")
	return chaos12330Case{subject: project, attrs: projectOnlyProjectNode(project)}
}

func chaos12330OrganizationCase() chaos12330Case {
	return chaos12330Case{subject: organizationSubject(projectOnlyEngineOrg), attrs: chaos12330OrganizationNode()}
}

func chaos12330WorkItemCase() chaos12330Case {
	subject := workItemSubject("repo-1", "WI-1")
	return chaos12330Case{subject: subject, attrs: map[string]interface{}{"subject_kind": "work_item", "canonical_id": subject.CanonicalID, "label": "Payments", "authorization_project_identities": []string{projectSubject("linear", "proj-1").CanonicalID}}}
}

func chaos12330ReviewSubject() contextfabric.SubjectRef {
	canonicalID, omitted, err := identity.Derive(identity.KindPullRequestReview, []string{"repo-1", "7", "review-1"}, nil)
	if err != nil || omitted {
		panic(fmt.Sprintf("review subject: omitted=%v err=%v", omitted, err))
	}
	return contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequestReview, CanonicalID: canonicalID, Label: "Review"}
}

func chaos12330DeniedCase(subject contextfabric.SubjectRef) chaos12330Case {
	return chaos12330Case{subject: subject, denied: true, attrs: map[string]interface{}{"subject_kind": string(subject.Kind), "canonical_id": subject.CanonicalID, "label": "Payments", "authorization_repositories": "*", "authorization_projects": "*", "authorization_teams": "*"}}
}

// chaos12330Cases names a case for every fact kind a provider serves. A kind
// missing here fails TestEveryProviderKindIsRefusedToAProjectOnlyCallerInTheAnswer,
// so a provider added later cannot join the registry unproved.
func chaos12330Cases() map[contextfabric.FactKind]chaos12330Case {
	return map[contextfabric.FactKind]chaos12330Case{
		contextfabric.FactIdentity:                chaos12330WorkItemCase(),
		contextfabric.FactMembership:              chaos12330WorkItemCase(),
		contextfabric.FactStatus:                  chaos12330WorkItemCase(),
		contextfabric.FactActualCompletion:        chaos12330ProjectCase(),
		contextfabric.FactWork:                    chaos12330WorkItemCase(),
		contextfabric.FactBlockers:                chaos12330ProjectCase(),
		contextfabric.FactRequiredChildren:        chaos12330WorkItemCase(),
		contextfabric.FactPullRequests:            chaos12330ProjectCase(),
		contextfabric.FactReviews:                 chaos12330DeniedCase(chaos12330ReviewSubject()),
		contextfabric.FactContinuousIntegration:   chaos12330DeniedCase(repoSubject("repo-1")),
		contextfabric.FactDeployments:             chaos12330DeniedCase(repoSubject("repo-1")),
		contextfabric.FactIncidents:               chaos12330DeniedCase(teamSubject("team-1")),
		contextfabric.FactMetrics:                 chaos12330ProjectCase(),
		contextfabric.FactHealth:                  chaos12330ProjectCase(),
		contextfabric.FactWorkload:                chaos12330ProjectCase(),
		contextfabric.FactInvestment:              chaos12330ProjectCase(),
		contextfabric.FactReadiness:               chaos12330ProjectCase(),
		contextfabric.FactOperationalDeficiencies: chaos12330DeniedCase(teamSubject("team-1")),
		contextfabric.FactSourceHealth:            chaos12330OrganizationCase(),
		contextfabric.FactFlow:                    chaos12330ProjectCase(),
		contextfabric.FactLandscape:               chaos12330ProjectCase(),
		contextfabric.FactSecurityAlerts:          chaos12330DeniedCase(repoSubject("repo-1")),
		contextfabric.FactCommitment:              chaos12330ProjectCase(),
		contextfabric.FactFileHotspots:            chaos12330DeniedCase(repoSubject("repo-1")),
	}
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

// Every kind the production provider set serves, read by a project-only caller
// on a subject its capability supports: no provider runs and the reason
// reaches the answer. A kind with no case FAILS, so a provider added later
// cannot be silently served or left unlabeled. A subject the node rule denies
// is committed anyway: the registry's refusal must hold without the graph.
func TestEveryProviderKindIsRefusedToAProjectOnlyCallerInTheAnswer(t *testing.T) {
	t.Parallel()
	providers := devhealthfacts.NewProvidersWithOperations(&fakeClient{}, sourceHealthHolder(&fakeOperationCaller{}))
	cases := chaos12330Cases()
	served := map[contextfabric.FactKind]bool{}
	for _, provider := range providers {
		capability := provider.Capability()
		served[capability.Kind] = true
		tc, ok := cases[capability.Kind]
		if !ok {
			t.Errorf("fact kind %q has no case: give it a subject in chaos12330Cases", capability.Kind)
			continue
		}
		if !slices.Contains(capability.SupportedSubjectKinds, tc.subject.Kind) {
			t.Errorf("fact kind %q: the case's subject kind %q is not one of %v", capability.Kind, tc.subject.Kind, capability.SupportedSubjectKinds)
			continue
		}
		model := &projectOnlyEngineModel{kinds: []contextfabric.FactKind{capability.Kind}, commitDenied: tc.denied}
		run := runProjectOnlyEngineWith(t, chaos12330ProjectOnly(), tc.subject, tc.attrs, model, contextfabric.RequestedScope{})
		assertRefusedInTheAnswer(t, fmt.Sprintf("%s/%s", capability.Kind, tc.subject.Kind), run, capability.Kind)
	}
	for kind := range cases {
		if !served[kind] {
			t.Errorf("fact kind %q has a case and no provider", kind)
		}
	}
	if len(served) != len(cases) {
		t.Errorf("drove %d kinds of %d", len(cases), len(served))
	}
}

// Both investment reads and every c3 inventory row, by subject, so a row of the
// CHAOS-12330 table cannot be covered only through another subject kind.
func TestTheInventoryRowsOfTheWithheldTableReachTheAnswer(t *testing.T) {
	t.Parallel()
	project := projectSubject("linear", "proj-1")
	organization := organizationSubject(projectOnlyEngineOrg)
	organizationNode := chaos12330OrganizationNode()
	team := chaos12330DeniedCase(teamSubject("team-1"))
	for _, tc := range []struct {
		kind    contextfabric.FactKind
		subject contextfabric.SubjectRef
		attrs   map[string]interface{}
		reason  string
		denied  bool
	}{
		{contextfabric.FactOperationalDeficiencies, team.subject, team.attrs, "owning teams", true},
		{contextfabric.FactHealth, project, projectOnlyProjectNode(project), "owning teams", false},
		{contextfabric.FactFlow, project, projectOnlyProjectNode(project), "owning teams", false},
		{contextfabric.FactLandscape, project, projectOnlyProjectNode(project), "owning teams", false},
		{contextfabric.FactMetrics, project, projectOnlyProjectNode(project), "owning teams", false},
		{contextfabric.FactReadiness, project, projectOnlyProjectNode(project), "owning teams", false},
		{contextfabric.FactWorkload, project, projectOnlyProjectNode(project), "owning teams", false},
		{contextfabric.FactInvestment, project, projectOnlyProjectNode(project), "placed by repository", false},
		{contextfabric.FactInvestment, organization, organizationNode, "span the organization", false},
		{contextfabric.FactSourceHealth, organization, organizationNode, "organization-wide", false},
		{contextfabric.FactPullRequests, project, projectOnlyProjectNode(project), "pull requests", false},
		{contextfabric.FactBlockers, project, projectOnlyProjectNode(project), "exact project membership", false},
		{contextfabric.FactCommitment, project, projectOnlyProjectNode(project), "exact project membership", false},
		{contextfabric.FactActualCompletion, project, projectOnlyProjectNode(project), "exact project membership", false},
	} {
		name := fmt.Sprintf("%s/%s", tc.kind, tc.subject.Kind)
		model := &projectOnlyEngineModel{kinds: []contextfabric.FactKind{tc.kind}, commitDenied: tc.denied}
		run := runProjectOnlyEngineWith(t, chaos12330ProjectOnly(), tc.subject, tc.attrs, model, contextfabric.RequestedScope{})
		assertRefusedInTheAnswer(t, name, run, tc.kind)
		if detail := withheldDetailOf(run.result, tc.kind); detail != nil && !strings.Contains(detail.Label, tc.reason) {
			t.Errorf("%s: label %q does not state %q", name, detail.Label, tc.reason)
		}
	}
}

const chaos12330ClassGolden = "testdata/chaos12330_class_answers.golden.json"

// chaos12330ClassAnswers serves one question to each class that is not
// project-only and returns the served documents by class. The clock is fixed,
// so the same code serves the same bytes.
func chaos12330ClassAnswers(t *testing.T) map[string]projectOnlyEngineRun {
	t.Helper()
	project := projectSubject("linear", "proj-1")
	runs := map[string]projectOnlyEngineRun{}
	for name, principal := range map[string]storage.Principal{
		"unrestricted": {OrgID: projectOnlyEngineOrg},
		"universal":    {OrgID: projectOnlyEngineOrg, RepositoryScopes: []string{"*"}},
		"restricted":   {OrgID: projectOnlyEngineOrg, RepositoryScopes: []string{"acme/allowed"}},
	} {
		run := runProjectOnlyEngine(t, principal, project, projectOnlyProjectNode(project), []contextfabric.FactKind{contextfabric.FactHealth}, contextfabric.RequestedScope{})
		if run.err != nil {
			t.Fatalf("%s: Investigate() error = %v", name, run.err)
		}
		runs[name] = run
	}
	return runs
}

// Every other class is served exactly as before, not merely without one
// detail: its status, its coverage and the bytes of the served document are
// pinned by a golden. Regenerate with CHAOS12330_UPDATE_GOLDEN=1, and read the
// diff: a change here is a change to what another class is told.
func TestOtherClassesAreNotRefusedTheFactRead(t *testing.T) {
	t.Parallel()
	runs := chaos12330ClassAnswers(t)
	served := map[string]json.RawMessage{}
	for name, run := range runs {
		if len(run.client.queries) == 0 {
			t.Errorf("%s: no statement ran; this pin lost its control", name)
		}
		if withheldDetailOf(run.result, contextfabric.FactHealth) != nil {
			t.Errorf("%s: a %s caller was refused a fact", name, name)
		}
		body, err := json.Marshal(run.result)
		if err != nil {
			t.Fatal(err)
		}
		served[name] = body
	}
	if os.Getenv("CHAOS12330_UPDATE_GOLDEN") != "" {
		body, err := json.MarshalIndent(served, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(chaos12330ClassGolden, append(body, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Fatal("wrote the golden: rerun without CHAOS12330_UPDATE_GOLDEN")
	}
	raw, err := os.ReadFile(chaos12330ClassGolden)
	if err != nil {
		t.Fatal(err)
	}
	golden := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden) != len(runs) {
		t.Fatalf("golden has %d classes, want %d", len(golden), len(runs))
	}
	for name, run := range runs {
		var want contextfabric.InvestigationResult
		if err := json.Unmarshal(golden[name], &want); err != nil {
			t.Fatalf("%s: golden: %v", name, err)
		}
		if run.result.Status != want.Status {
			t.Errorf("%s: status = %q, golden %q", name, run.result.Status, want.Status)
		}
		got, wanted := mustJSON(t, run.result.Coverage), mustJSON(t, want.Coverage)
		if got != wanted {
			t.Errorf("%s: coverage differs from the golden:\n got %s\nwant %s", name, got, wanted)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, golden[name]); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(served[name], compact.Bytes()) {
			t.Errorf("%s: the served document's bytes differ from the golden:\n got %s\nwant %s", name, served[name], compact.Bytes())
		}
	}
}

// The golden above is worth nothing if the same code can serve other bytes.
func TestAnotherClassesAnswerIsTheSameBytesOnEveryRun(t *testing.T) {
	t.Parallel()
	first, second := chaos12330ClassAnswers(t), chaos12330ClassAnswers(t)
	for name := range first {
		if a, b := mustJSON(t, first[name].result), mustJSON(t, second[name].result); a != b {
			t.Errorf("%s: two runs differ:\n%s\n%s", name, a, b)
		}
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
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
		if lookups := run.reuse.count(); lookups != 0 {
			t.Errorf("%s: the refused request made %d reuse lookups, want none: the stop must sit above tryReuse", name, lookups)
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
	// The control of the reuse spy: a request past the stop does look.
	if lookups := plain.reuse.count(); lookups != 1 {
		t.Errorf("a project-only request naming no project made %d reuse lookups, want 1: the spy lost its control", lookups)
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
