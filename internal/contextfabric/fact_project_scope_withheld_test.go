package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func withheldProjectOnly() storage.Principal {
	return storage.Principal{OrgID: "org_1", ProjectScopes: []string{"project.v2:linear:proj-1"}}
}

func withheldRead(t *testing.T, principal storage.Principal) (CanonicalFactBundle, *factProviderStub, *recordingScopeExpander) {
	t.Helper()
	metrics := &factProviderStub{
		capability: planCapability(FactMetrics, "metrics", SubjectRepository),
		result: FactProviderResult{State: SourceAvailable, Version: "metrics-v1", Facts: []CanonicalFact{{
			Kind: FactMetrics, Subject: scopeRepo, Fields: map[string]FactValue{"lead_time_days": NumberFactValue(3.5)}, SourceState: SourceAvailable,
		}}},
	}
	expander := &recordingScopeExpander{targets: []SubjectRef{scopeRepo}}
	registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{ScopeExpander: expander, ScopePolicies: metricsProjectPolicyTable(0)})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), principal, scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent))
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	return bundle, metrics, expander
}

// CHAOS-12330: a kind reached only through scope expansion is refused like one
// read directly: no expansion read and no provider read run. The control proves
// the same read does both for any other caller.
func TestAProjectOnlyCallerMakesNoProviderAndNoExpansionRead(t *testing.T) {
	t.Parallel()
	bundle, metrics, expander := withheldRead(t, withheldProjectOnly())
	if len(metrics.queries) != 0 || expander.calls != 0 {
		t.Fatalf("provider queries %d, expander calls %d, want none", len(metrics.queries), expander.calls)
	}
	if len(bundle.Facts) != 0 {
		t.Fatalf("facts = %+v", bundle.Facts)
	}
	if !bundle.Coverage.Partial || len(bundle.Coverage.DegradedReasons) != 1 || len(bundle.Coverage.Details) != 1 {
		t.Fatalf("coverage = %+v", bundle.Coverage)
	}
	detail := bundle.Coverage.Details[0]
	if detail.Code != contractsv1.ContextFabricCoverageDetailProjectScopeWithheld || detail.FactKind != FactMetrics || detail.SourceState != SourceUnauthorized || !detail.Degrading {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.Raw != bundle.Coverage.DegradedReasons[0] {
		t.Fatalf("raw %q does not pair with %q", detail.Raw, bundle.Coverage.DegradedReasons[0])
	}
	if outcome := bundle.Outcomes[FactMetrics]; outcome.Branch != string(factReadWithheld) || outcome.State != SourceUnauthorized {
		t.Fatalf("outcome = %+v", outcome)
	}
	// The gap is the refusal here: left in the scope, the answer's disclosure
	// would state a second, weaker cause.
	if bundle.Scope == nil || len(bundle.Scope.Gaps) != 0 {
		t.Fatalf("scope = %+v, want no gap", bundle.Scope)
	}
	control, controlMetrics, controlExpander := withheldRead(t, storage.Principal{OrgID: "org_1"})
	if len(controlMetrics.queries) != 1 || controlExpander.calls != 1 || len(control.Facts) != 1 {
		t.Fatalf("control: provider queries %d, expander calls %d, facts %d", len(controlMetrics.queries), controlExpander.calls, len(control.Facts))
	}
}

// A kind read directly, with no scope decision to make: the refusal, not a gap,
// is what keeps the provider from running.
func TestAProjectOnlyCallerIsRefusedAKindReadDirectly(t *testing.T) {
	t.Parallel()
	metrics := &factProviderStub{capability: planCapability(FactMetrics, "metrics", SubjectProject), result: FactProviderResult{State: SourceAvailable, Version: "v1"}}
	registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), withheldProjectOnly(), canonicalFactRequest(scopeProject, FactMetrics))
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(metrics.queries) != 0 {
		t.Fatalf("the provider ran %d times for a refused kind", len(metrics.queries))
	}
	if len(bundle.Coverage.Details) != 1 || bundle.Coverage.Details[0].Code != contractsv1.ContextFabricCoverageDetailProjectScopeWithheld {
		t.Fatalf("details = %+v", bundle.Coverage.Details)
	}
}

// A pruned kind fits no subject for any caller, so nothing is refused; an
// unconfigured kind has no provider. Each keeps its own truthful state.
func TestAProjectOnlyCallerKeepsThePrunedAndUnconfiguredStates(t *testing.T) {
	t.Parallel()
	pruned := &factProviderStub{capability: planCapability(FactMetrics, "metrics", SubjectRepository), result: FactProviderResult{State: SourceAvailable, Version: "v1"}}
	registry, err := NewFactCapabilityRegistry([]FactProvider{pruned}, FactRegistryOptions{ScopePolicies: map[FactKind]map[SubjectKind]factScopePolicyRule{}})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), withheldProjectOnly(), canonicalFactRequest(scopeProject, FactMetrics, FactStatus))
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	states := map[string]SourceState{}
	for _, source := range bundle.Coverage.Sources {
		states[source.Source] = source.State
	}
	if states["canonical_fact:metrics"] != SourcePruned || states["canonical_fact:status"] != SourceUnconfigured {
		t.Fatalf("states = %v, want pruned and unconfigured", states)
	}
	if len(pruned.queries) != 0 {
		t.Fatalf("pruned provider queried %d times", len(pruned.queries))
	}
}

func withheldFloorResult(status InvestigationStatus) InvestigationResult {
	result := validInvestigationResult()
	result.Status = status
	detail := CoverageDetail{DetailID: "cov-01", Source: "canonical_fact:health", Code: contractsv1.ContextFabricCoverageDetailProjectScopeWithheld, Degrading: true, FactKind: FactHealth, SourceState: SourceUnauthorized, Raw: "health: refused"}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	result.Coverage = Coverage{Partial: true, Sources: []SourceObservation{}, DegradedReasons: []string{detail.Raw}, Details: []CoverageDetail{detail}}
	return result
}

// The floor lowers a complete answer and only that: it never promotes, and it
// leaves a refusal alone.
func TestTheProjectScopeFloorLowersOnlyACompleteAnswer(t *testing.T) {
	t.Parallel()
	complete := withheldFloorResult(InvestigationComplete)
	applyProjectScopeWithheldStatusFloor(&complete)
	if complete.Status != InvestigationPartial {
		t.Fatalf("a complete answer stayed %q", complete.Status)
	}
	found := false
	for _, limitation := range complete.Limitations {
		found = found || limitation == contractsv1.ContextFabricProjectScopeWithheldLimitation
	}
	if !found {
		t.Fatalf("limitations = %q, want the answer's own sentence", complete.Limitations)
	}
	for _, status := range []InvestigationStatus{InvestigationNoMatch, InvestigationDegraded} {
		result := withheldFloorResult(status)
		applyProjectScopeWithheldStatusFloor(&result)
		if result.Status != status {
			t.Fatalf("%q moved to %q", status, result.Status)
		}
	}
	refused := withheldFloorResult(InvestigationComplete)
	refused.RefusalBasis = contractsv1.ContextFabricRefusalBasisUnspecified
	applyProjectScopeWithheldStatusFloor(&refused)
	if refused.Status != InvestigationComplete {
		t.Fatalf("a refusal moved to %q", refused.Status)
	}
	plain := withheldFloorResult(InvestigationComplete)
	plain.Coverage = Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}
	applyProjectScopeWithheldStatusFloor(&plain)
	if plain.Status != InvestigationComplete {
		t.Fatalf("an answer with no refusal moved to %q", plain.Status)
	}
}

func refusedKindDocument(plan AnswerPlan) InvestigationResult {
	noMember := CoverageDetail{DetailID: "cov-01", Source: "context-fabric:graph", Code: contractsv1.ContextFabricCoverageDetailGraphNoMemberFound, Degrading: true, Kind: SubjectTeam, Raw: "no_member_found:team"}
	other := CoverageDetail{DetailID: "cov-02", Source: "context-fabric:graph", Code: contractsv1.ContextFabricCoverageDetailGraphNoMemberFound, Degrading: true, Kind: SubjectDeployment, Raw: "no_member_found:deployment"}
	return InvestigationResult{AnswerPlan: &plan, Coverage: Coverage{Partial: true, Sources: []SourceObservation{}, DegradedReasons: []string{noMember.Raw, other.Raw}, Details: []CoverageDetail{noMember, other}}}
}

// An asked kind is named once per finalization path, a found-none row of that
// kind goes with its reason, and every other row stays.
func TestAnAskedRefusedKindReplacesItsNoneFoundRow(t *testing.T) {
	t.Parallel()
	plan := AnswerPlan{Requirements: []contractsv1.ContextFabricPlanRequirement{{Subject: SubjectTeam}}}
	result := discloseProjectScopeRefusedKinds(withheldProjectOnly(), refusedKindDocument(plan))
	result = discloseProjectScopeRefusedKinds(withheldProjectOnly(), result)
	if len(result.Coverage.Details) != 2 || len(result.Coverage.DegradedReasons) != 2 {
		t.Fatalf("coverage = %+v", result.Coverage)
	}
	if kept := result.Coverage.Details[0]; kept.Code != contractsv1.ContextFabricCoverageDetailGraphNoMemberFound || kept.Kind != SubjectDeployment {
		t.Fatalf("the other row went: %+v", kept)
	}
	if added := result.Coverage.Details[1]; added.Code != contractsv1.ContextFabricCoverageDetailProjectScopeWithheld || added.Kind != SubjectTeam {
		t.Fatalf("detail = %+v", added)
	}
	if got := result.Coverage.DegradedReasons; got[0] != "no_member_found:deployment" || got[1] != result.Coverage.Details[1].Raw {
		t.Fatalf("reasons = %q do not pair", got)
	}
}

func TestARefusedKindIsNamedOnlyWhenAskedAndOnlyForAProjectOnlyCaller(t *testing.T) {
	t.Parallel()
	asked := func(result InvestigationResult) bool {
		for _, detail := range result.Coverage.Details {
			if detail.Code == contractsv1.ContextFabricCoverageDetailProjectScopeWithheld {
				return true
			}
		}
		return false
	}
	for name, tc := range map[string]struct {
		plan AnswerPlan
		want bool
	}{
		"a member kind":     {AnswerPlan{MemberKind: SubjectPullRequest}, true},
		"a group kind":      {AnswerPlan{GroupKind: SubjectRepository}, true},
		"a requirement row": {AnswerPlan{Requirements: []contractsv1.ContextFabricPlanRequirement{{Subject: SubjectTeam}}}, true},
		"an admitted kind":  {AnswerPlan{MemberKind: SubjectWorkItem, Requirements: []contractsv1.ContextFabricPlanRequirement{{Subject: SubjectProject}}}, false},
	} {
		if got := asked(discloseProjectScopeRefusedKinds(withheldProjectOnly(), refusedKindDocument(tc.plan))); got != tc.want {
			t.Errorf("%s: disclosed = %v, want %v", name, got, tc.want)
		}
		if asked(discloseProjectScopeRefusedKinds(storage.Principal{OrgID: "org_1"}, refusedKindDocument(tc.plan))) {
			t.Errorf("%s: an unrestricted caller was told a project grant stops it", name)
		}
	}
	if asked(discloseProjectScopeRefusedKinds(withheldProjectOnly(), InvestigationResult{})) {
		t.Error("a document with no plan was disclosed")
	}
}
