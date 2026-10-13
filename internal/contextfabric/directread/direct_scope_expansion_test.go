package directread

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type scriptedExpander struct {
	mu       sync.Mutex
	requests []contextfabric.FactScopeExpansionRequest
	expand   func(contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult
}

func (e *scriptedExpander) ExpandFactScope(_ context.Context, request contextfabric.FactScopeExpansionRequest) (contextfabric.FactScopeExpansionResult, error) {
	e.mu.Lock()
	e.requests = append(e.requests, request)
	e.mu.Unlock()
	return e.expand(request), nil
}

func expandTo(targets ...contextfabric.SubjectRef) func(contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
	return func(contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		return contextfabric.FactScopeExpansionResult{Targets: targets, Counts: contextfabric.FactScopeExpansionCounts{CandidateCount: len(targets)}}
	}
}

func labelled(ref contextfabric.SubjectRef, label string) contextfabric.SubjectRef {
	ref.Label = label
	return ref
}

// perSubjectProvider answers one fact per subject of its first supported
// kind; a team rollup it also supports answers nothing, as on prod.
func perSubjectProvider(kind contextfabric.FactKind, supported ...contextfabric.SubjectKind) *stubProvider {
	capability := contextfabric.FactCapability{
		Kind: kind, Name: string(kind) + "_test", Version: "test.v1",
		SupportedSubjectKinds: supported,
		Dimension:             contextfabric.HealthDimensionCodeOwnershipRisk,
		SubjectRoles:          []contextfabric.FactRole{contextfabric.FactRoleSubject},
		Fields:                []contextfabric.FactFieldDeclaration{{Name: "n", Type: contextfabric.FactFieldInteger}},
	}
	return &stubProvider{capability: capability, read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		result := contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}
		for _, subject := range query.Subjects {
			if subject.Kind != supported[0] {
				continue
			}
			result.Facts = append(result.Facts, contextfabric.CanonicalFact{Kind: kind, Subject: subject, Fields: map[string]contextfabric.FactValue{"n": intValue(1)}})
		}
		return result, nil
	}}
}

func directExpandingReader(t *testing.T, graph *fakeGraph, expander contextfabric.FactScopeExpander, recorder FactsRecorder, providers ...contextfabric.FactProvider) *FactsReader {
	t.Helper()
	registry, err := contextfabric.NewFactCapabilityRegistry(providers, contextfabric.FactRegistryOptions{ScopeExpander: expander})
	if err != nil {
		t.Fatal(err)
	}
	return NewFactsReader(NewSubjectGate(graph, nil), NewFactReader(registry.DirectReadFactSource()), recorder)
}

func servedOn(response FactsResponse, kind string, subject contextfabric.SubjectRef) *ServedFact {
	for index := range response.Facts {
		fact := &response.Facts[index]
		if fact.Kind == kind && fact.Subject.Kind == string(subject.Kind) && fact.Subject.CanonicalID == subject.CanonicalID {
			return fact
		}
	}
	return nil
}

// The direct path hands the expander the authenticated caller unchanged, and
// serves the derived facts named by the root they were reached from.
func TestDirectReadExpandsWithTheCallersPrincipal(t *testing.T) {
	for _, principal := range []storage.Principal{unrestrictedA(), restrictedToA()} {
		t.Run(principal.Subject, func(t *testing.T) {
			expander := &scriptedExpander{expand: expandTo(labelled(workA, "WI-A"))}
			recorder := &capturingFactsRecorder{}
			reader := directExpandingReader(t, graphOfOrgA(), expander, recorder, perSubjectProvider(contextfabric.FactBlockers, contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectTeam))
			response, err := reader.Read(requestContext(), principal, FactsRequest{Kinds: []string{"blockers"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(expander.requests) != 1 || !reflect.DeepEqual(expander.requests[0].Principal, principal) {
				t.Fatalf("expander saw %+v, want the caller %+v", expander.requests, principal)
			}
			if got := expander.requests[0].Origins; len(got) != 1 || got[0].CanonicalID != teamT.CanonicalID {
				t.Fatalf("expander origins %+v, want the admitted root only", got)
			}
			fact := servedOn(response, "blockers", workA)
			if fact == nil || !reflect.DeepEqual(fact.ReachedFrom, []ReachedFrom{{Kind: "team", CanonicalID: teamT.CanonicalID, Basis: "direct"}}) {
				t.Fatalf("derived fact not served with its root: %s", mustJSON(t, response))
			}
			row := coverageOf(t, response, "blockers", teamT)
			if row.Outcome != OutcomeFactServed || !strings.Contains(row.Reason, "scope expansion reached 1 work items; their facts name this subject in reached_from") {
				t.Fatalf("root row %+v", row)
			}
			validateAgainst(t, "mcp_read_facts_response.v1.schema.json", []byte(mustJSON(t, response)))
			record := recorder.records[0]
			if record.DerivedTargets != 1 || record.DerivedFactsServed != 1 || record.DerivedFactsWithheld != 0 || record.DerivedUnauthorized != 0 {
				t.Fatalf("record %+v", record)
			}
		})
	}
}

type plantingSource struct {
	inner *contextfabric.FactCapabilityRegistry
	plant func(*contextfabric.CanonicalFactBundle)
}

func (s plantingSource) ReadFacts(ctx context.Context, principal storage.Principal, request contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	bundle, err := s.inner.ReadFacts(ctx, principal, request)
	s.plant(&bundle)
	return bundle, err
}

func (s plantingSource) Capabilities() []contextfabric.FactCapability { return s.inner.Capabilities() }

// C.5 guard: a fact is served only on an admitted root or on a target the
// resolver derived from an admitted root for the fact's own kind. Each case
// plants one way a source could hand the reader a fact outside that set.
func TestDirectReadWithholdsFactsNotDerivedFromAdmittedRoots(t *testing.T) {
	stray := labelled(workB, "WI-B")
	strayFact := contextfabric.CanonicalFact{Kind: contextfabric.FactBlockers, Subject: stray, Fields: map[string]contextfabric.FactValue{"n": intValue(7)}}
	cases := []struct {
		name  string
		plant func(*contextfabric.CanonicalFactBundle)
	}{
		{"no derivation at all", func(bundle *contextfabric.CanonicalFactBundle) {
			bundle.Facts = append(bundle.Facts, strayFact)
		}},
		{"derived from a root the gate did not admit", func(bundle *contextfabric.CanonicalFactBundle) {
			bundle.Scope.DerivedSubjects[contextfabric.FactBlockers] = append(bundle.Scope.DerivedSubjects[contextfabric.FactBlockers], stray)
			bundle.Scope.Derivations = append(bundle.Scope.Derivations, contextfabric.FactScopeDerivation{
				Root: teamU, Target: stray, Policy: contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemBlockers, Basis: contextfabric.FactScopeBasisDirect,
			})
			bundle.Facts = append(bundle.Facts, strayFact)
		}},
		{"derived for another kind", func(bundle *contextfabric.CanonicalFactBundle) {
			bundle.Scope.DerivedSubjects[contextfabric.FactStatus] = append(bundle.Scope.DerivedSubjects[contextfabric.FactStatus], stray)
			bundle.Scope.Derivations = append(bundle.Scope.Derivations, contextfabric.FactScopeDerivation{
				Root: teamT, Target: stray, Policy: contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemStatus, Basis: contextfabric.FactScopeBasisDirect,
			})
			bundle.Scope.Events = append(bundle.Scope.Events, contextfabric.FactScopeExpansionEvent{
				RequirementKind: contextfabric.FactStatus, OriginKind: contractsv1.ContextFabricSubjectTeam,
				TargetKind: contractsv1.ContextFabricSubjectWorkItem, Policy: contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemStatus,
			})
			bundle.Facts = append(bundle.Facts, strayFact)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expander := &scriptedExpander{expand: expandTo(labelled(workA, "WI-A"))}
			registry, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{perSubjectProvider(contextfabric.FactBlockers, contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectTeam)}, contextfabric.FactRegistryOptions{ScopeExpander: expander})
			if err != nil {
				t.Fatal(err)
			}
			recorder := &capturingFactsRecorder{}
			reader := NewFactsReader(NewSubjectGate(graphOfOrgA(), nil), NewFactReader(plantingSource{inner: registry, plant: tc.plant}), recorder)
			response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"blockers"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}}})
			if err != nil {
				t.Fatal(err)
			}
			if fact := servedOn(response, "blockers", stray); fact != nil {
				t.Fatalf("fact on a subject not derived from an admitted root was served: %s", mustJSON(t, fact))
			}
			if servedOn(response, "blockers", workA) == nil {
				t.Fatalf("the admitted derivation lost its fact: %s", mustJSON(t, response))
			}
			if record := recorder.records[0]; record.DerivedFactsWithheld != 1 {
				t.Fatalf("withheld count %d, want 1", record.DerivedFactsWithheld)
			}
		})
	}
}

// Targets the expander dropped as not authorized for the caller produce no
// fact; the root's row says the expansion was cut, never not_applicable, and
// the read line counts the drop.
func TestDirectReadDisclosesTargetsDroppedAsUnauthorized(t *testing.T) {
	expander := &scriptedExpander{expand: func(contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		return contextfabric.FactScopeExpansionResult{Counts: contextfabric.FactScopeExpansionCounts{CandidateCount: 2, AuthorizationDroppedCount: 2}}
	}}
	recorder := &capturingFactsRecorder{}
	reader := directExpandingReader(t, graphOfOrgA(), expander, recorder, perSubjectProvider(contextfabric.FactDeployments, contractsv1.ContextFabricSubjectRepository))
	response, err := reader.Read(requestContext(), restrictedToA(), FactsRequest{Kinds: []string{"deployments"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Facts) != 0 {
		t.Fatalf("facts served for unauthorized targets: %s", mustJSON(t, response.Facts))
	}
	assertRowsHonest(t, response)
	row := coverageOf(t, response, "deployments", projectQ)
	if row.Outcome != OutcomeUnavailable || row.Expansion == nil || row.Expansion.Outcome != string(contextfabric.FactScopeMatchedUnauthorized) {
		t.Fatalf("row %+v", row)
	}
	if response.Status != StatusPartial {
		t.Fatalf("status %q", response.Status)
	}
	if record := recorder.records[0]; record.DerivedUnauthorized != 2 || record.DerivedFactsServed != 0 {
		t.Fatalf("record %+v", record)
	}
}

// The widest legal request (the subject cap times the kind cap, every kind
// reachable only by expansion, every expansion over its target limit) stays
// within one expansion per kind, the policy target limit and the response
// budget, and every cut is disclosed; nothing reads as failed.
func TestDirectReadWidestExpansionStaysBounded(t *testing.T) {
	graph := graphOfOrgA()
	subjects := make([]RequestSubject, 0, MaxSubjectsPerRead)
	for i := 0; i < MaxSubjectsPerRead; i++ {
		project := subject(contractsv1.ContextFabricSubjectProject, fmt.Sprintf("project:p%02d", i))
		graph.nodes[graphrank.SubjectKey(project)] = map[string]interface{}{"authorization_repositories": "*", "authorization_projects": []string{project.CanonicalID}}
		graph.reach[graphrank.SubjectKey(project)] = []string{"acme/a"}
		subjects = append(subjects, RequestSubject{Kind: "project", CanonicalID: project.CanonicalID})
	}
	targetKinds := map[contextfabric.FactKind]contextfabric.SubjectKind{
		contextfabric.FactMetrics:                 contractsv1.ContextFabricSubjectRepository,
		contextfabric.FactPullRequests:            contractsv1.ContextFabricSubjectPullRequest,
		contextfabric.FactReviews:                 contractsv1.ContextFabricSubjectPullRequestReview,
		contextfabric.FactContinuousIntegration:   contractsv1.ContextFabricSubjectRepository,
		contextfabric.FactDeployments:             contractsv1.ContextFabricSubjectRepository,
		contextfabric.FactIncidents:               contractsv1.ContextFabricSubjectIncident,
		contextfabric.FactOperationalDeficiencies: contractsv1.ContextFabricSubjectTeam,
		contextfabric.FactBlockers:                contractsv1.ContextFabricSubjectWorkItem,
	}
	kinds := make([]string, 0, len(targetKinds))
	providers := make([]contextfabric.FactProvider, 0, len(targetKinds))
	for kind, target := range targetKinds {
		kinds = append(kinds, string(kind))
		providers = append(providers, perSubjectProvider(kind, target))
	}
	if len(kinds) != MaxKindsPerRead {
		t.Fatalf("kinds %d, want the kind cap %d", len(kinds), MaxKindsPerRead)
	}
	expander := &scriptedExpander{expand: func(request contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		targets := make([]contextfabric.SubjectRef, 0, request.Limit+1)
		roots := map[string]contextfabric.SubjectRef{}
		for i := 0; i <= request.Limit; i++ {
			target := contextfabric.SubjectRef{Kind: request.TargetKind, CanonicalID: fmt.Sprintf("%s:%s:%04d", request.TargetKind, request.RequirementKind, i), Label: fmt.Sprintf("T%d", i)}
			targets = append(targets, target)
			roots[contextfabric.FactSubjectKey(target)] = request.Origins[i%len(request.Origins)]
		}
		return contextfabric.FactScopeExpansionResult{Targets: targets, TargetRoot: roots, Counts: contextfabric.FactScopeExpansionCounts{CandidateCount: len(targets)}}
	}}
	recorder := &capturingFactsRecorder{}
	reader := directExpandingReader(t, graph, expander, recorder, providers...)
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: kinds, Subjects: subjects, MaxBytes: MaxMaxBytes})
	if err != nil {
		t.Fatal(err)
	}
	if len(expander.requests) != MaxKindsPerRead {
		t.Fatalf("expansions %d, want one per kind (%d)", len(expander.requests), MaxKindsPerRead)
	}
	for _, request := range expander.requests {
		if request.Limit <= 0 || request.Limit > 200 {
			t.Fatalf("expansion limit %d outside the policy cap", request.Limit)
		}
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > MaxMaxBytes || response.Truncation == nil || response.Truncation.FactsOmitted == 0 || response.Truncation.CoverageOverBudget || len(response.Facts) == 0 {
		t.Fatalf("response %d bytes, truncation %+v", len(encoded), response.Truncation)
	}
	if response.Status != StatusPartial {
		t.Fatalf("status %q, want partial", response.Status)
	}
	if len(response.Coverage) != MaxKindsPerRead*MaxSubjectsPerRead {
		t.Fatalf("coverage rows %d", len(response.Coverage))
	}
	for _, row := range response.Coverage {
		switch row.Outcome {
		case OutcomeTruncated, OutcomeWithheldBudget:
		default:
			t.Fatalf("row %+v: every root of an over-limit expansion must read as cut, never served whole, failed or not applicable", row)
		}
		if strings.Contains(strings.ToLower(row.Reason), "failed") || row.KindState == string(contextfabric.SourceUnavailable) {
			t.Fatalf("row reads as failed: %+v", row)
		}
	}
	if record := recorder.records[0]; record.DerivedTargets == 0 || record.DerivedFactsServed == 0 || record.Bytes > MaxMaxBytes {
		t.Fatalf("record %+v", record)
	}
}

// The one read line carries the scope expansion counts at INFO, and no
// subject id.
func TestDirectReadLineCountsDerivedFacts(t *testing.T) {
	var buffer bytes.Buffer
	recorder := NewSlogFactsRecorder(slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo})))
	expander := &scriptedExpander{expand: func(contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		return contextfabric.FactScopeExpansionResult{Targets: []contextfabric.SubjectRef{labelled(workA, "WI-A")}, Counts: contextfabric.FactScopeExpansionCounts{CandidateCount: 2, AuthorizationDroppedCount: 1}}
	}}
	reader := directExpandingReader(t, graphOfOrgA(), expander, recorder, perSubjectProvider(contextfabric.FactBlockers, contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectTeam))
	if _, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"blockers"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}}}); err != nil {
		t.Fatal(err)
	}
	line := buffer.String()
	for _, want := range []string{"level=INFO", "msg=\"" + FactsReadLogMessage + "\"", "derived_targets=1", "derived_facts_served=1", "derived_facts_withheld=0", "derived_unauthorized=1"} {
		if !strings.Contains(line, want) {
			t.Errorf("read line lacks %q: %s", want, line)
		}
	}
	for _, id := range []string{teamT.CanonicalID, workA.CanonicalID, "WI-A"} {
		if strings.Contains(line, id) {
			t.Errorf("read line names %q: %s", id, line)
		}
	}
}

// A cut expansion that served no fact for a root never reads as "no fact":
// the cut may have left that root's facts out.
func TestDirectReadCutExpansionWithoutFactIsNotReadNoFact(t *testing.T) {
	expander := &scriptedExpander{expand: func(request contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		targets := make([]contextfabric.SubjectRef, 0, request.Limit+1)
		for i := 0; i <= request.Limit; i++ {
			targets = append(targets, contextfabric.SubjectRef{Kind: request.TargetKind, CanonicalID: fmt.Sprintf("repository:r%04d", i), Label: fmt.Sprintf("R%d", i)})
		}
		return contextfabric.FactScopeExpansionResult{Targets: targets, Counts: contextfabric.FactScopeExpansionCounts{CandidateCount: len(targets)}}
	}}
	empty := perSubjectProvider(contextfabric.FactDeployments, contractsv1.ContextFabricSubjectRepository)
	empty.read = func(contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
	}
	reader := directExpandingReader(t, graphOfOrgA(), expander, nil, empty)
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"deployments"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}})
	if err != nil {
		t.Fatal(err)
	}
	assertRowsHonest(t, response)
	row := coverageOf(t, response, "deployments", projectQ)
	if row.Outcome != OutcomeUnavailable || row.Expansion == nil || row.Expansion.Outcome != string(contextfabric.FactScopeExpandedPartial) || response.Status != StatusPartial {
		t.Fatalf("row %+v status %q", row, response.Status)
	}
}

// A target two requested teams reach is attributed to one of them (the
// expander gives one root). The fact is served once; neither team's row
// claims a complete count: the team it was not attributed to reads
// unavailable, never "no fact".
func TestDirectReadDisclosesATargetSharedByTwoRequestedTeams(t *testing.T) {
	shared := labelled(workA, "WI-A")
	expander := &scriptedExpander{expand: func(request contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		return contextfabric.FactScopeExpansionResult{
			Targets:    []contextfabric.SubjectRef{shared},
			TargetRoot: map[string]contextfabric.SubjectRef{contextfabric.FactSubjectKey(shared): teamT},
			Counts:     contextfabric.FactScopeExpansionCounts{CandidateCount: 1},
		}
	}}
	reader := directExpandingReader(t, graphOfOrgA(), expander, nil, perSubjectProvider(contextfabric.FactBlockers, contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectTeam))
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"blockers"}, Subjects: []RequestSubject{
		{Kind: "team", CanonicalID: teamT.CanonicalID}, {Kind: "team", CanonicalID: teamU.CanonicalID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(expander.requests) != 1 || len(expander.requests[0].Origins) != 2 {
		t.Fatalf("expansion requests %+v, want one over both teams", expander.requests)
	}
	facts := 0
	for _, fact := range response.Facts {
		if fact.Kind == "blockers" && fact.Subject.CanonicalID == shared.CanonicalID {
			facts++
		}
	}
	if facts != 1 {
		t.Fatalf("shared work item served %d times, want once: %s", facts, mustJSON(t, response.Facts))
	}
	const caveat = "is counted under only one of them"
	attributed, other := coverageOf(t, response, "blockers", teamT), coverageOf(t, response, "blockers", teamU)
	if attributed.Outcome != OutcomeFactServed || strings.Contains(attributed.Reason, caveat) {
		t.Fatalf("attributed team row %+v", attributed)
	}
	if other.Outcome != OutcomeUnavailable || !strings.Contains(other.Reason, caveat) {
		t.Fatalf("other team row %+v: must not read as a complete zero", other)
	}
	if response.Status != StatusPartial {
		t.Fatalf("status %q, want partial", response.Status)
	}
}

// With two requested teams that each hold their own targets, neither count
// can be shown complete: a target both reach would sit under one of them.
func TestDirectReadMarksEveryShareOfAMultiRootExpansionAsALowerBound(t *testing.T) {
	a, b := labelled(workA, "WI-A"), labelled(workB, "WI-B")
	expander := &scriptedExpander{expand: func(contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		return contextfabric.FactScopeExpansionResult{
			Targets:    []contextfabric.SubjectRef{a, b},
			TargetRoot: map[string]contextfabric.SubjectRef{contextfabric.FactSubjectKey(a): teamT, contextfabric.FactSubjectKey(b): teamU},
			Counts:     contextfabric.FactScopeExpansionCounts{CandidateCount: 2},
		}
	}}
	reader := directExpandingReader(t, graphOfOrgA(), expander, nil, perSubjectProvider(contextfabric.FactBlockers, contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectTeam))
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"blockers"}, Subjects: []RequestSubject{
		{Kind: "team", CanonicalID: teamT.CanonicalID}, {Kind: "team", CanonicalID: teamU.CanonicalID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range []contextfabric.SubjectRef{teamT, teamU} {
		if row := coverageOf(t, response, "blockers", team); row.Outcome != OutcomeTruncated || !strings.Contains(row.Reason, "is counted under only one of them") {
			t.Fatalf("row %+v", row)
		}
	}
	if len(response.Facts) != 2 || response.Status != StatusPartial {
		t.Fatalf("facts %d status %q", len(response.Facts), response.Status)
	}
}

// teamRollupProvider answers each work item with a fact and each team with a
// rollup fact of its own.
func teamRollupProvider() *stubProvider {
	provider := perSubjectProvider(contextfabric.FactBlockers, contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectTeam)
	provider.read = func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		result := contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}
		for _, subject := range query.Subjects {
			result.Facts = append(result.Facts, contextfabric.CanonicalFact{Kind: contextfabric.FactBlockers, Subject: subject, Fields: map[string]contextfabric.FactValue{"n": intValue(1)}})
		}
		return result, nil
	}
	return provider
}

// A team whose own rollup is served but whose reached work item was
// attributed to another requested team is not a complete row.
func TestDirectReadSharedTeamWithItsOwnRollupIsNotComplete(t *testing.T) {
	shared := labelled(workA, "WI-A")
	expander := &scriptedExpander{expand: func(contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		return contextfabric.FactScopeExpansionResult{
			Targets:    []contextfabric.SubjectRef{shared},
			TargetRoot: map[string]contextfabric.SubjectRef{contextfabric.FactSubjectKey(shared): teamT},
			Counts:     contextfabric.FactScopeExpansionCounts{CandidateCount: 1},
		}
	}}
	reader := directExpandingReader(t, graphOfOrgA(), expander, nil, teamRollupProvider())
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"blockers"}, Subjects: []RequestSubject{
		{Kind: "team", CanonicalID: teamT.CanonicalID}, {Kind: "team", CanonicalID: teamU.CanonicalID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if servedOn(response, "blockers", teamU) == nil {
		t.Fatalf("team U rollup not served: %s", mustJSON(t, response.Facts))
	}
	if row := coverageOf(t, response, "blockers", teamU); row.Outcome != OutcomeTruncated || !strings.Contains(row.Reason, "is counted under only one of them") {
		t.Fatalf("team U row %+v, want truncated with the lower-bound note", row)
	}
	if response.Status != StatusPartial {
		t.Fatalf("status %q, want partial", response.Status)
	}
}

// A directly supported team whose additive expansion was cut and served no
// fact never reads as a complete "no fact".
func TestDirectReadCutAdditiveExpansionWithoutFactIsNotComplete(t *testing.T) {
	expander := &scriptedExpander{expand: func(request contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		targets := make([]contextfabric.SubjectRef, 0, request.Limit+1)
		for i := 0; i <= request.Limit; i++ {
			targets = append(targets, contextfabric.SubjectRef{Kind: request.TargetKind, CanonicalID: fmt.Sprintf("work_item.v2:w%04d", i), Label: fmt.Sprintf("W%d", i)})
		}
		return contextfabric.FactScopeExpansionResult{Targets: targets, Counts: contextfabric.FactScopeExpansionCounts{CandidateCount: len(targets)}}
	}}
	empty := perSubjectProvider(contextfabric.FactBlockers, contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectTeam)
	empty.read = func(contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
	}
	reader := directExpandingReader(t, graphOfOrgA(), expander, nil, empty)
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"blockers"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}}})
	if err != nil {
		t.Fatal(err)
	}
	if row := coverageOf(t, response, "blockers", teamT); row.Outcome != OutcomeUnavailable || row.Expansion == nil || row.Expansion.Outcome != string(contextfabric.FactScopeExpandedPartial) {
		t.Fatalf("row %+v, want unavailable naming the cut", row)
	}
	if response.Status != StatusPartial {
		t.Fatalf("status %q, want partial", response.Status)
	}
}

type failingProvider struct{ capability contextfabric.FactCapability }

func (p failingProvider) Capability() contextfabric.FactCapability { return p.capability }

func (failingProvider) ReadFacts(context.Context, storage.Principal, contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	return contextfabric.FactProviderResult{}, fmt.Errorf("deployments store down")
}

// A provider failure on the reached subjects keeps its own cause in the
// root's row, and the row does not claim the subjects were read.
func TestDirectReadReachedRootKeepsTheProviderFailureCause(t *testing.T) {
	expander := &scriptedExpander{expand: expandTo(labelled(repoA, "acme/a"))}
	provider := failingProvider{capability: perSubjectProvider(contextfabric.FactDeployments, contractsv1.ContextFabricSubjectRepository).capability}
	reader := directExpandingReader(t, graphOfOrgA(), expander, nil, provider)
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"deployments"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}})
	if err != nil {
		t.Fatal(err)
	}
	assertRowsHonest(t, response)
	row := coverageOf(t, response, "deployments", projectQ)
	ledger := ""
	if row.Outcome != OutcomeUnavailable || strings.Contains(row.Reason, "read through") || !strings.Contains(row.Reason, "scope expansion reached 1 repositories") {
		t.Fatalf("row %+v", row)
	}
	for _, source := range []string{"failed", "unavailable", "error"} {
		if strings.Contains(strings.ToLower(row.Reason), source) {
			ledger = source
		}
	}
	if ledger == "" {
		t.Fatalf("row reason %q does not keep the provider failure cause", row.Reason)
	}
}

// A project anchor of a kind it answers directly with a rollup AND reaches
// per item through its additive expansion (pull requests): the project's
// coverage row is the derived row, never an unexpanded gap. The project that
// received the shared pull request reads served; the other requested project
// whose expansion lost it reads truncated with the lower-bound note.
func TestDirectReadProjectRollupRootCountsItsDerivedPullRequests(t *testing.T) {
	pull := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: "pull_request:shared", Label: "#1"}
	expander := &scriptedExpander{expand: func(request contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		return contextfabric.FactScopeExpansionResult{
			Targets:    []contextfabric.SubjectRef{pull},
			TargetRoot: map[string]contextfabric.SubjectRef{contextfabric.FactSubjectKey(pull): projectP},
			Counts:     contextfabric.FactScopeExpansionCounts{CandidateCount: 1},
		}
	}}
	provider := perSubjectProvider(contextfabric.FactPullRequests, contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectProject)
	provider.read = func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		result := contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}
		for _, subject := range query.Subjects {
			result.Facts = append(result.Facts, contextfabric.CanonicalFact{Kind: contextfabric.FactPullRequests, Subject: subject, Fields: map[string]contextfabric.FactValue{"n": intValue(1)}})
		}
		return result, nil
	}
	reader := directExpandingReader(t, graphOfOrgA(), expander, nil, provider)
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"pull_requests"}, Subjects: []RequestSubject{
		{Kind: "project", CanonicalID: projectP.CanonicalID}, {Kind: "project", CanonicalID: projectQ.CanonicalID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(expander.requests) != 1 {
		t.Fatalf("expansions %d, want the additive project expansion once", len(expander.requests))
	}
	fact := servedOn(response, "pull_requests", pull)
	if fact == nil || len(fact.ReachedFrom) != 1 || fact.ReachedFrom[0].CanonicalID != projectP.CanonicalID {
		t.Fatalf("derived pull request fact %s", mustJSON(t, response.Facts))
	}
	received, other := coverageOf(t, response, "pull_requests", projectP), coverageOf(t, response, "pull_requests", projectQ)
	t.Logf("project rows: received=%+v other=%+v", received, other)
	for _, row := range []CoverageRow{received, other} {
		if row.Expansion != nil && row.Expansion.Outcome == string(contextfabric.FactScopePolicyUnavailable) {
			t.Fatalf("project row %+v reads as an unexpanded gap", row)
		}
	}
	if received.Outcome != OutcomeFactServed || !strings.Contains(received.Reason, "scope expansion reached 1 pull requests; their facts name this subject in reached_from") {
		t.Fatalf("receiving project row %+v", received)
	}
	if other.Outcome != OutcomeTruncated || !strings.Contains(other.Reason, "is counted under only one of them") {
		t.Fatalf("other project row %+v", other)
	}
}

// A work item a requested project and a requested team both reach is derived
// from the project only (the first origin kind); the team, whose own rollup
// is served, is not a complete row.
func TestDirectReadTargetSharedAcrossOriginKindsIsALowerBoundForTheOtherRoot(t *testing.T) {
	shared := labelled(workA, "WI-A")
	expander := &scriptedExpander{expand: func(request contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		return contextfabric.FactScopeExpansionResult{
			Targets:    []contextfabric.SubjectRef{shared},
			TargetRoot: map[string]contextfabric.SubjectRef{contextfabric.FactSubjectKey(shared): request.Origins[0]},
			Counts:     contextfabric.FactScopeExpansionCounts{CandidateCount: 1},
		}
	}}
	provider := perSubjectProvider(contextfabric.FactBlockers, contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectProject)
	provider.read = func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		result := contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}
		for _, subject := range query.Subjects {
			result.Facts = append(result.Facts, contextfabric.CanonicalFact{Kind: contextfabric.FactBlockers, Subject: subject, Fields: map[string]contextfabric.FactValue{"n": intValue(1)}})
		}
		return result, nil
	}
	reader := directExpandingReader(t, graphOfOrgA(), expander, nil, provider)
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"blockers"}, Subjects: []RequestSubject{
		{Kind: "project", CanonicalID: projectQ.CanonicalID}, {Kind: "team", CanonicalID: teamT.CanonicalID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(expander.requests) != 2 {
		t.Fatalf("expansions %d, want the project and the team expansion", len(expander.requests))
	}
	fact := servedOn(response, "blockers", shared)
	if fact == nil || len(fact.ReachedFrom) != 1 || fact.ReachedFrom[0].Kind != "project" {
		t.Fatalf("shared work item %s, want one fact derived from the project", mustJSON(t, response.Facts))
	}
	if row := coverageOf(t, response, "blockers", projectQ); row.Outcome != OutcomeFactServed {
		t.Fatalf("project row %+v", row)
	}
	if row := coverageOf(t, response, "blockers", teamT); row.Outcome != OutcomeTruncated || !strings.Contains(row.Reason, "is counted under only one of them") {
		t.Fatalf("team row %+v, want truncated with the lower-bound note", row)
	}
	if response.Status != StatusPartial {
		t.Fatalf("status %q, want partial", response.Status)
	}
}

// A budget cut that drops a root's derived facts leaves a reason that is
// still true: it names what the expansion reached, never a served count.
func TestDirectReadBudgetCutKeepsTheDerivedReasonTrue(t *testing.T) {
	targets := make([]contextfabric.SubjectRef, 0, 40)
	for i := 0; i < 40; i++ {
		targets = append(targets, contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: fmt.Sprintf("work_item.v2:budget-%02d-%s", i, strings.Repeat("x", 60)), Label: strings.Repeat("L", 80)})
	}
	expander := &scriptedExpander{expand: expandTo(targets...)}
	reader := directExpandingReader(t, graphOfOrgA(), expander, nil, perSubjectProvider(contextfabric.FactBlockers, contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectTeam))
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"blockers"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}}, MaxBytes: MinMaxBytes})
	if err != nil {
		t.Fatal(err)
	}
	if response.Truncation == nil || response.Truncation.FactsOmitted == 0 {
		t.Fatalf("truncation %+v, want facts cut by the budget", response.Truncation)
	}
	row := coverageOf(t, response, "blockers", teamT)
	if strings.Contains(row.Reason, "served") || !strings.Contains(row.Reason, "scope expansion reached 40 work items") {
		t.Fatalf("row %+v: the reason must name what was reached, never a served count the budget changed", row)
	}
}
