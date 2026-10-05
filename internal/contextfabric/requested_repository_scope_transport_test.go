package contextfabric

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestBuildFactQueryCopiesRequestedRepositoryScopeOutsideModelParameters(t *testing.T) {
	t.Parallel()

	work := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_item.v2:repo-a:W-1", Label: "W-1"}
	capability := FactCapability{
		Kind:                  FactStatus,
		SupportedSubjectKinds: []SubjectKind{SubjectWorkItem},
		AllowedParameters:     []string{"window_days"},
	}
	requirement := FactRequirement{
		Kind:       FactStatus,
		Parameters: map[string]string{"window_days": "7"},
	}
	allowed := map[string]SubjectRef{canonicalFactSubjectKey(work): work}

	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{name: "nil is unconstrained", input: nil, want: nil},
		{name: "empty stays an unconstrained raw request", input: []string{}, want: []string{}},
		{name: "values stay ordered and unchanged", input: []string{"repo-b", "repo-a", "repo-b"}, want: []string{"repo-b", "repo-a", "repo-b"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := CanonicalFactRequest{
				Question: InterpretedQuestion{TimeContext: TimeContext{Axis: TemporalCurrent}},
				Subjects: []SubjectRef{work},
				Requirements: []FactRequirement{{
					Kind: FactStatus,
				}},
				RequestedRepositoryScope: test.input,
			}
			query, err := buildFactQuery(request, requirement, capability, allowed, []SubjectRef{work})
			if err != nil {
				t.Fatalf("buildFactQuery() error = %v", err)
			}
			if (query.RequestedRepositoryScope == nil) != (test.want == nil) {
				t.Fatalf("RequestedRepositoryScope nil = %v, want %v", query.RequestedRepositoryScope == nil, test.want == nil)
			}
			if !reflect.DeepEqual(query.RequestedRepositoryScope, test.want) {
				t.Fatalf("RequestedRepositoryScope = %#v, want %#v", query.RequestedRepositoryScope, test.want)
			}
			if got := query.Parameters["window_days"]; got != "7" {
				t.Fatalf("model parameter = %q, want it preserved as an ordinary parameter", got)
			}

			if len(test.input) > 0 {
				test.input[0] = "caller-mutated-after-build"
				if query.RequestedRepositoryScope[0] != test.want[0] {
					t.Fatalf("query scope changed when the request input was mutated: %#v", query.RequestedRepositoryScope)
				}
				query.RequestedRepositoryScope[0] = "query-mutated"
				if request.RequestedRepositoryScope[0] != "caller-mutated-after-build" {
					t.Fatalf("request scope shares storage with the query: %#v", request.RequestedRepositoryScope)
				}
			}
		})
	}
}

func TestFactCapabilityRegistryRejectsRepositoryScopeModelParameter(t *testing.T) {
	provider := &factProviderStub{
		capability: FactCapability{
			Kind:                  FactStatus,
			Name:                  "status",
			Version:               "v1",
			SupportedSubjectKinds: []SubjectKind{SubjectWorkItem},
			Dimension:             HealthDimensionExecutionCompletion,
			SubjectRoles:          []FactRole{FactRoleSubject},
		},
		result: FactProviderResult{State: SourceAvailable, Version: "v1"},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	work := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_item.v2:repo-a:W-1", Label: "W-1"}
	_, err = registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
		Question: InterpretedQuestion{
			Shape:             ShapeSingleSubject,
			RequestedJudgment: "status",
			TimeContext:       TimeContext{Axis: TemporalCurrent},
		},
		Subjects: []SubjectRef{work}, Requirements: []FactRequirement{{
			Kind: FactStatus,
			// A model-authored value with the same conceptual name as the
			// request carrier must be rejected as a parameter. It cannot
			// become an authorization input by taking this route.
			Parameters: map[string]string{"repository_slugs": "forged/repository"},
		}},
		RequestedRepositoryScope: []string{"caller/repository"},
	})
	if err == nil {
		t.Fatal("ReadFacts() error = nil, want the model repository scope parameter rejected")
	}
	if !errors.Is(err, ErrInterpretationRejected) {
		t.Fatalf("ReadFacts() error = %v, want ErrInterpretationRejected", err)
	}
	if len(provider.queries) != 0 {
		t.Fatalf("provider queries = %#v, want none after rejecting the model parameter", provider.queries)
	}
}

func TestFactCapabilityRegistryUsesFreshRequestedRepositoryScopePerRead(t *testing.T) {
	provider := &factProviderStub{
		capability: FactCapability{
			Kind:                  FactStatus,
			Name:                  "status",
			Version:               "v1",
			SupportedSubjectKinds: []SubjectKind{SubjectWorkItem},
			Dimension:             HealthDimensionExecutionCompletion,
			SubjectRoles:          []FactRole{FactRoleSubject},
		},
		result: FactProviderResult{State: SourceAvailable, Version: "v1"},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}

	work := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_item.v2:repo-a:W-1", Label: "W-1"}
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{name: "first request", input: []string{"repo-a"}, want: []string{"repo-a"}},
		{name: "second request replaces first", input: []string{"repo-b"}, want: []string{"repo-b"}},
		{name: "explicit empty does not inherit second", input: []string{}, want: []string{}},
		{name: "nil does not inherit empty", input: nil, want: nil},
	}
	for _, test := range tests {
		request := CanonicalFactRequest{
			Question: InterpretedQuestion{
				Shape:             ShapeSingleSubject,
				RequestedJudgment: "status",
				TimeContext:       TimeContext{Axis: TemporalCurrent},
			},
			Subjects:                 []SubjectRef{work},
			Requirements:             []FactRequirement{{Kind: FactStatus}},
			RequestedRepositoryScope: test.input,
		}
		if _, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
			t.Fatalf("ReadFacts(%s) error = %v", test.name, err)
		}
		if len(test.input) > 0 {
			test.input[0] = "caller-mutated-after-read"
		}
	}

	if len(provider.queries) != len(tests) {
		t.Fatalf("provider queries = %d, want %d", len(provider.queries), len(tests))
	}
	for index, test := range tests {
		query := provider.queries[index]
		if (query.RequestedRepositoryScope == nil) != (test.want == nil) {
			t.Fatalf("query %d (%s) nil = %v, want %v", index, test.name, query.RequestedRepositoryScope == nil, test.want == nil)
		}
		if !reflect.DeepEqual(query.RequestedRepositoryScope, test.want) {
			t.Fatalf("query %d (%s) scope = %#v, want %#v", index, test.name, query.RequestedRepositoryScope, test.want)
		}
	}

	provider.queries[0].RequestedRepositoryScope[0] = "first-query-mutated"
	if got := provider.queries[1].RequestedRepositoryScope[0]; got != "repo-b" {
		t.Fatalf("query 1 scope changed after mutating query 0: %q", got)
	}
}

func TestEngineCopiesRequestedRepositoryScopeIntoCanonicalFactRequest(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	var observed CanonicalFactRequest
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape:             ShapeOpen,
				RequestedJudgment: "status",
				TimeContext:       TimeContext{Axis: TemporalCurrent},
				FactRequirements:  []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			context: GraphContext{
				Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
				EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			observed = request
			return emptyFactBundle(), nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return validInvestigationResult(), nil
		}),
		Results: &resultStoreStub{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(100, 0).UTC() },
		NewResultID:    func() string { return "result_57510001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestedScope.RepositorySlugs = []string{"owner/repo-a", "owner/repo-b"}
	want := []string{"owner/repo-a", "owner/repo-b"}
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	request.RequestedScope.RepositorySlugs[0] = "caller-mutated-after-investigate"

	if !reflect.DeepEqual(observed.RequestedRepositoryScope, want) {
		t.Fatalf("fact request scope = %#v, want %#v", observed.RequestedRepositoryScope, want)
	}
	if !observed.scopeSelectedWorkItems {
		t.Fatal("an investigation's fact request does not say its work items were selected under the requested scope: their own facts would be filtered again on their own repository")
	}
	observed.RequestedRepositoryScope[0] = "fact-request-mutated"
	if request.RequestedScope.RepositorySlugs[0] != "caller-mutated-after-investigate" {
		t.Fatalf("fact request scope shares storage with the caller request: %#v", request.RequestedScope.RepositorySlugs)
	}
}

func TestGroupedFactRequestCopiesRequestedRepositoryScope(t *testing.T) {
	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	engine, request := groupReadEngineFixture(t, &recordingTelemetry{}, recorder)
	request.RequestedScope.RepositorySlugs = []string{"owner/repo-a", "owner/repo-b"}
	want := []string{"owner/repo-a", "owner/repo-b"}
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	request.RequestedScope.RepositorySlugs[0] = "caller-mutated-after-investigate"

	if len(recorder.requests) == 0 {
		t.Fatal("grouped investigation issued no fact requests")
	}
	for index, factRequest := range recorder.requests {
		if !reflect.DeepEqual(factRequest.RequestedRepositoryScope, want) {
			t.Fatalf("fact request %d scope = %#v, want %#v", index, factRequest.RequestedRepositoryScope, want)
		}
		factRequest.RequestedRepositoryScope[0] = "recorded-request-mutated"
		if request.RequestedScope.RepositorySlugs[0] != "caller-mutated-after-investigate" {
			t.Fatalf("fact request %d scope shares storage with the caller request", index)
		}
	}
}

// TestAWorkItemsOwnFactsAreNotFilteredAgainOnItsOwnRepository: in an
// investigation the requested repository scope selected the work items (by
// the rule that follows the link), so a work item's own status, title and
// completion are read under the caller's grants; every other read keeps the
// requested scope.
func TestAWorkItemsOwnFactsAreNotFilteredAgainOnItsOwnRepository(t *testing.T) {
	t.Parallel()
	work := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_item.v2:00000000-0000-0000-0000-000000000000:linear:ENG-1", Label: "ENG-1"}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project.v2:linear:p-1", Label: "p-1"}
	child := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_item.v2:00000000-0000-0000-0000-000000000000:linear:ENG-2", Label: "ENG-2"}
	allowed := map[string]SubjectRef{canonicalFactSubjectKey(work): work, canonicalFactSubjectKey(project): project, canonicalFactSubjectKey(child): child}
	scope := []string{"acme/svc"}
	for _, c := range []struct {
		name     string
		selected bool
		kind     FactKind
		subjects []SubjectRef
		want     []string
	}{
		{"status of a work item", true, FactStatus, []SubjectRef{work}, nil},
		{"title of a work item", true, FactWork, []SubjectRef{work}, nil},
		{"completion of a work item", true, FactActualCompletion, []SubjectRef{work}, nil},
		{"completion roll-up of a project", true, FactActualCompletion, []SubjectRef{work, project}, scope},
		{"blockers of a work item", true, FactBlockers, []SubjectRef{work}, scope},
		{"a read outside an investigation", false, FactStatus, []SubjectRef{work}, scope},
		{"status of a work item the fact scope derived", true, FactStatus, []SubjectRef{work, child}, scope},
	} {
		capability := FactCapability{Kind: c.kind, SupportedSubjectKinds: []SubjectKind{SubjectWorkItem, SubjectProject}}
		request := CanonicalFactRequest{
			scopeSelectedWorkItems: c.selected,
			Question:               InterpretedQuestion{TimeContext: TimeContext{Axis: TemporalCurrent}},
			Subjects:               []SubjectRef{work, project}, Requirements: []FactRequirement{{Kind: c.kind}},
			RequestedRepositoryScope: scope,
		}
		query, err := buildFactQuery(request, FactRequirement{Kind: c.kind}, capability, allowed, c.subjects)
		if err != nil {
			t.Fatalf("%s: buildFactQuery() error = %v", c.name, err)
		}
		if !reflect.DeepEqual(query.RequestedRepositoryScope, c.want) {
			t.Errorf("%s: requested scope %#v, want %#v", c.name, query.RequestedRepositoryScope, c.want)
		}
	}
}
