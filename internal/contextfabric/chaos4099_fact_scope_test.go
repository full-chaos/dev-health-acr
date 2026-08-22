package contextfabric

// CHAOS-4099 stage 1 -- the fact-read scope resolver, its vocabulary, and
// the disclosure that replaces a false prune.
//
// WHAT THESE TESTS ARE ORACLES FOR. The defect is not "a fact was missing".
// It is "the system asserted a PROOF it did not hold": SourcePruned is
// documented as "the capability could not have produced a single admissible
// fact", it is deliberately excluded from factStateDegrades on that ground,
// and it was being recorded for a project subject whose facts are reachable
// through a typed chain nobody had written the traversal for. Every
// assertion below is therefore about what the system CLAIMS, not only about
// what it returns -- a test that only checked "zero facts came back" would
// have passed throughout the whole life of the defect.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

var (
	// scopeProject is the committed subject shape from the observed defect:
	// a work-tracking project, which NO canonical-fact capability declares
	// in SupportedSubjectKinds.
	scopeProject = SubjectRef{Kind: SubjectProject, CanonicalID: "project:linear:TITAN", Label: "Titan"}
	scopeTeam    = SubjectRef{Kind: SubjectTeam, CanonicalID: "team:PLATFORM", Label: "Platform"}
	scopeRepo    = SubjectRef{Kind: SubjectRepository, CanonicalID: "repo:github:api", Label: "api"}
)

// scopeCapabilities is the real production subject-kind shape of the three
// capabilities this ticket is about (devhealthfacts/metrics.go,
// pullrequests.go): metrics answers for repositories, pull_requests for pull
// requests, reviews for pull request reviews. None answers for a project.
//
// Restated here rather than imported because devhealthfacts imports THIS
// package -- an import from these tests would be a cycle. That makes any
// assertion in this file about "the real capabilities" a tautology, so the
// real guard lives where the real declarations do:
// devhealthfacts/chaos4099_capability_kinds_test.go pins these exact values
// against the production providers, and fails if one is widened.
func scopeCapabilities() map[FactKind]FactCapability {
	return map[FactKind]FactCapability{
		FactMetrics:      planCapability(FactMetrics, "metrics", SubjectRepository),
		FactPullRequests: planCapability(FactPullRequests, "pull_requests", SubjectPullRequest),
		FactReviews:      planCapability(FactReviews, "reviews", contractsv1.ContextFabricSubjectPullRequestReview),
	}
}

// caseSixtyRequirements is the fact-requirement set of the regression case
// (ext65 corpus index 60): pull requests, reviews and metrics. No corpus
// text appears anywhere -- the case is identified by path and index only.
func caseSixtyRequirements() []FactRequirement {
	return []FactRequirement{
		{Kind: FactPullRequests}, {Kind: FactReviews}, {Kind: FactMetrics},
	}
}

func scopeRequest(subjects []SubjectRef, requirements []FactRequirement, axis TemporalAxis) CanonicalFactRequest {
	return CanonicalFactRequest{
		Question:     InterpretedQuestion{TimeContext: TimeContext{Axis: axis}},
		Subjects:     subjects,
		Requirements: requirements,
	}
}

func scopeRegistry(t *testing.T) (*FactCapabilityRegistry, map[FactKind]*factProviderStub) {
	t.Helper()
	stubs := map[FactKind]*factProviderStub{}
	providers := make([]FactProvider, 0, 3)
	for kind, capability := range scopeCapabilities() {
		stub := &factProviderStub{capability: capability, result: FactProviderResult{State: SourceAvailable}}
		stubs[kind] = stub
		providers = append(providers, stub)
	}
	registry, err := NewFactCapabilityRegistry(providers, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	return registry, stubs
}

// scopeServableResult is affirmationResult() stamped with the identity and
// version fields Investigate would have filled in, so a unit-level
// disclosure test can call the REAL contract validator.
//
// The validation matters here specifically: LimitationsDisplaced carries a
// coherence rule (a positive displaced count requires a service-authored
// disclosure present), and a disclosure that made a result unservable would
// be a worse defect than the one being fixed -- that is exactly how
// CHAOS-4098 produced a 500.
func scopeServableResult() InvestigationResult {
	result := affirmationResult()
	result.SchemaVersion = InvestigationResultSchemaV1
	result.ResultID = "result_12345678"
	result.RequestID = "request_12345678"
	result.GeneratedAt = time.Unix(100, 0).UTC()
	result.Question = "how is this project doing?"
	result.Interpretation = InterpretedQuestion{
		Shape: ShapeSingleSubject, RequestedJudgment: "release_readiness_and_drivers",
		TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{},
		SubjectTerms: []string{}, ComparisonTerms: []string{},
	}
	result.Versions = VersionSet{
		ServiceVersion: "acr-test", ContractVersion: InvestigationResultSchemaV1,
		Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
		InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
		CanonicalServiceVersion: "ops-v1", ModelIdentity: "unwired",
	}
	result.Status = InvestigationNoMatch
	result.DirectJudgment = composeDirectJudgmentFrom(result.Status, result.Drivers, result.SubjectResolution)
	result.DeterministicAnswer = composeDeterministicAnswerFrom(result.Status, result.Drivers, result.ClaimedFacts, result.SubjectResolution)
	return result
}

func coverageBySource(bundle CanonicalFactBundle) map[string]SourceObservation {
	bySource := make(map[string]SourceObservation, len(bundle.Coverage.Sources))
	for _, source := range bundle.Coverage.Sources {
		bySource[source.Source] = source
	}
	return bySource
}

// ---------------------------------------------------------------------------
// The regression pin
// ---------------------------------------------------------------------------

// TestChaos4099_ProjectSubjectNoLongerClaimsAProofOfAbsence is THE regression
// test for the reported defect (ext65 corpus index 60).
//
// The pre-fix behavior it forbids, exactly: all three canonical-fact
// capabilities recorded SourcePruned with
// FactPruneReasonSubjectKindUnsupported, Coverage.Partial stayed false, and
// the answer went out reporting no match with nothing to say about why. The
// assertions are written against each half of that separately, because
// fixing only one half -- degrading the coverage while still calling it a
// prune, or renaming the reason while leaving the state non-degrading --
// would leave the answer just as misleading as before.
func TestChaos4099_ProjectSubjectNoLongerClaimsAProofOfAbsence(t *testing.T) {
	t.Parallel()

	registry, stubs := scopeRegistry(t)
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeProject}, caseSixtyRequirements(), TemporalCurrent))
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	for kind, stub := range stubs {
		if len(stub.queries) != 0 {
			t.Fatalf("%s provider was queried %d time(s); stage 1 reads no new facts", kind, len(stub.queries))
		}
	}
	bySource := coverageBySource(bundle)
	for _, kind := range []FactKind{FactPullRequests, FactReviews, FactMetrics} {
		observation, ok := bySource["canonical_fact:"+string(kind)]
		if !ok {
			t.Fatalf("coverage = %+v, want an observation for %s", bundle.Coverage.Sources, kind)
		}
		if observation.State == SourcePruned {
			t.Fatalf("%s state = pruned -- that asserts a proof that nothing is missing, which is the defect", kind)
		}
		if !factStateDegrades(observation.State) {
			t.Fatalf("%s state = %q, want a degrading state: the answer IS missing evidence it could have had", kind, observation.State)
		}
		if strings.HasPrefix(observation.Reason, FactPruneReasonSubjectKindUnsupported) {
			t.Fatalf("%s reason = %q, want the unexpanded vocabulary, not the prune vocabulary", kind, observation.Reason)
		}
		wantPrefix := FactScopeReasonUnexpanded + ":" + string(FactScopePolicyUnavailable)
		if !strings.HasPrefix(observation.Reason, wantPrefix) {
			t.Fatalf("%s reason = %q, want prefix %q", kind, observation.Reason, wantPrefix)
		}
	}
	if !bundle.Coverage.Partial {
		t.Fatal("Coverage.Partial = false -- a gap the system never looked into is not explained absence")
	}
	if len(bundle.Coverage.DegradedReasons) != 3 {
		t.Fatalf("DegradedReasons = %v, want one per unreached requirement", bundle.Coverage.DegradedReasons)
	}
}

// TestChaos4099_EachProjectPolicyIsNamedOnItsOwnRequirement pins the
// requirement -> policy mapping the ruling names, so a rewiring that pointed
// two requirements at one policy (or silently dropped one) fails here rather
// than being discovered as a coverage gap in a rerun.
func TestChaos4099_EachProjectPolicyIsNamedOnItsOwnRequirement(t *testing.T) {
	t.Parallel()

	want := map[FactKind]struct {
		policy FactScopePolicy
		target SubjectKind
	}{
		FactMetrics:      {FactScopePolicyProjectWorkItemRepository, SubjectRepository},
		FactPullRequests: {FactScopePolicyProjectWorkItemPullRequest, SubjectPullRequest},
		FactReviews:      {FactScopePolicyProjectWorkItemPullRequestReview, contractsv1.ContextFabricSubjectPullRequestReview},
	}
	scope := NewFactReadScopeResolver(nil).Resolve(
		context.Background(),
		newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject}, caseSixtyRequirements(), TemporalCurrent)),
		scopeCapabilities(),
	)
	if len(scope.Events) != len(want) {
		t.Fatalf("events = %+v, want one per requirement", scope.Events)
	}
	for _, event := range scope.Events {
		expected, known := want[event.RequirementKind]
		if !known {
			t.Fatalf("unexpected event for %s", event.RequirementKind)
		}
		if event.Policy != expected.policy {
			t.Fatalf("%s policy = %q, want %q", event.RequirementKind, event.Policy, expected.policy)
		}
		if event.TargetKind != expected.target {
			t.Fatalf("%s target = %q, want %q", event.RequirementKind, event.TargetKind, expected.target)
		}
		if event.Basis != FactScopeBasisActivityProxy {
			t.Fatalf("%s basis = %q -- the project chain proves ACTIVITY, never ownership", event.RequirementKind, event.Basis)
		}
		if event.OriginKind != SubjectProject {
			t.Fatalf("%s origin = %q, want project", event.RequirementKind, event.OriginKind)
		}
	}
}

// ---------------------------------------------------------------------------
// Acceptance point 9 -- team and non-current axis stay disabled AND disclosed
// ---------------------------------------------------------------------------

// TestChaos4099_TeamOriginIsDisabledButDisclosed is CHAOS-4101's half of
// acceptance point 9. The team gap is real and confirmed; what is missing is
// the product ruling on the attribution edge, not the knowledge that the gap
// exists. So the requirement must NOT report a proof of absence, and must
// NOT mint a policy name that pre-empts that ruling.
func TestChaos4099_TeamOriginIsDisabledButDisclosed(t *testing.T) {
	t.Parallel()

	registry, _ := scopeRegistry(t)
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeTeam}, caseSixtyRequirements(), TemporalCurrent))
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if !bundle.Coverage.Partial {
		t.Fatal("a team-origin gap must degrade the answer exactly as a project-origin one does")
	}
	for _, observation := range bundle.Coverage.Sources {
		if observation.State == SourcePruned {
			t.Fatalf("%s claims a proof of absence over the confirmed team gap", observation.Source)
		}
	}
	if len(bundle.Scope.Events) != 3 {
		t.Fatalf("events = %+v, want one per requirement", bundle.Scope.Events)
	}
	seen := map[FactKind]bool{}
	for _, event := range bundle.Scope.Events {
		seen[event.RequirementKind] = true
		if event.OriginKind != SubjectTeam {
			t.Fatalf("origin = %q, want team", event.OriginKind)
		}
		if event.Policy != FactScopePolicyNone {
			t.Fatalf("policy = %q -- naming a team policy here pre-empts CHAOS-4101's product ruling", event.Policy)
		}
		if event.Outcome != FactScopePolicyUnavailable {
			t.Fatalf("outcome = %q, want policy_unavailable", event.Outcome)
		}
	}
	for _, kind := range []FactKind{FactPullRequests, FactReviews, FactMetrics} {
		if !seen[kind] {
			t.Fatalf("no team-origin decision recorded for %s", kind)
		}
	}
}

// TestChaos4099_NonCurrentAxisIsRefusedNotSilentlyAnsweredAsCurrent pins the
// v1 current-axis-only gate, and pins it as a HARD refusal.
//
// The failure it forbids is the subtle one: the work_item->project edge
// carries ObservedAt but no ValidFrom/ValidTo, so a traversal run for an
// as-of question would silently answer with TODAY's project membership and
// let the answer carry a historical temporal label. That is a wrong answer
// presented as a right one, which is strictly worse than the disclosed gap.
//
// The rule table is temporarily enabled here because stage 1 ships every
// policy dark, and a test that could not tell the axis gate from the
// disabled gate would assert nothing about the axis at all.
func TestChaos4099_NonCurrentAxisIsRefusedNotSilentlyAnsweredAsCurrent(t *testing.T) {
	restore := factScopePolicies
	t.Cleanup(func() { factScopePolicies = restore })
	factScopePolicies = map[FactKind]map[SubjectKind]factScopePolicyRule{
		FactMetrics: {SubjectProject: {
			Policy:     FactScopePolicyProjectWorkItemRepository,
			TargetKind: SubjectRepository,
			Basis:      FactScopeBasisActivityProxy,
			Enabled:    true,
		}},
	}
	expander := &recordingScopeExpander{targets: []SubjectRef{scopeRepo}}

	for _, axis := range []TemporalAxis{
		contractsv1.ContextFabricTemporalValidTime,
		contractsv1.ContextFabricTemporalObservedTime,
		contractsv1.ContextFabricTemporalRange,
	} {
		scope := NewFactReadScopeResolver(expander).Resolve(
			context.Background(),
			newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactMetrics}}, axis)),
			scopeCapabilities(),
		)
		if len(scope.Events) != 1 || scope.Events[0].Outcome != FactScopePolicyUnavailable {
			t.Fatalf("axis %q: events = %+v, want a single policy_unavailable", axis, scope.Events)
		}
		if len(scope.DerivedSubjects) != 0 {
			t.Fatalf("axis %q admitted derived subjects: %+v", axis, scope.DerivedSubjects)
		}
	}
	if expander.calls != 0 {
		t.Fatalf("expander ran %d time(s) on a non-current axis -- the gate must refuse BEFORE traversing", expander.calls)
	}

	// Control: the SAME table on the current axis does expand, proving the
	// refusals above are attributable to the axis and nothing else.
	scope := NewFactReadScopeResolver(expander).Resolve(
		context.Background(),
		newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent)),
		scopeCapabilities(),
	)
	if len(scope.Events) != 1 || scope.Events[0].Outcome != FactScopeExpanded {
		t.Fatalf("current axis: events = %+v, want expanded", scope.Events)
	}
}

// ---------------------------------------------------------------------------
// The prune that must SURVIVE
// ---------------------------------------------------------------------------

// TestChaos4099_AnIneligiblePairStillPrunesWithoutDegrading is the
// false-positive pin, and it is as load-bearing as the regression test.
//
// CHAOS-3783's non-degrading prune is what keeps Coverage.Partial meaningful:
// if every subject-kind mismatch degraded, every correctly-scoped
// investigation would read as compromised and the flag would stop being a
// signal. So the fix must be BOUNDED to the pairs whose reachability the
// design spike actually verified. A pair with no established path keeps the
// honest proof.
func TestChaos4099_AnIneligiblePairStillPrunesWithoutDegrading(t *testing.T) {
	t.Parallel()

	// A work_item subject against a repository-only capability. No policy
	// claims a path from work_item to repository-scoped metrics, so
	// "nothing is missing" remains a statement the system can make.
	registry, _ := scopeRegistry(t)
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{subject(SubjectWorkItem, "work_item:linear:ABC-1")},
			[]FactRequirement{{Kind: FactMetrics}}, TemporalCurrent))
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(bundle.Coverage.Sources) != 1 {
		t.Fatalf("coverage = %+v, want one observation", bundle.Coverage.Sources)
	}
	if got := bundle.Coverage.Sources[0].State; got != SourcePruned {
		t.Fatalf("state = %q, want pruned -- CHAOS-3783's proof must survive for pairs with no known path", got)
	}
	if bundle.Coverage.Partial {
		t.Fatal("Coverage.Partial = true -- widening the gap vocabulary past the verified pairs destroys the signal")
	}
	if len(bundle.Scope.Events) != 0 {
		t.Fatalf("events = %+v, want none: an ineligible pair is not an expansion decision", bundle.Scope.Events)
	}
}

// TestChaos4099_ADirectlySupportedSubjectEmitsNoEventAndNoGap pins the
// ordinary path. An event per answerable requirement would bury the signal
// this stream exists to carry under the base rate.
func TestChaos4099_ADirectlySupportedSubjectEmitsNoEventAndNoGap(t *testing.T) {
	t.Parallel()

	registry, stubs := scopeRegistry(t)
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeRepo}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent))
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(stubs[FactMetrics].queries) != 1 {
		t.Fatalf("metrics queried %d time(s), want 1", len(stubs[FactMetrics].queries))
	}
	if len(bundle.Scope.Events) != 0 || len(bundle.Scope.Gaps) != 0 {
		t.Fatalf("scope = %+v, want silence on an answerable requirement", bundle.Scope)
	}
	if bundle.Coverage.Partial {
		t.Fatal("an answerable requirement must not degrade the answer")
	}
}

// TestChaos4099_MixedDirectAndUnreachableSubjectsNarrowRatherThanGap pins
// the boundary between the two mechanisms. When SOME root is directly
// supported the capability RUNS, and CHAOS-3783's narrowing note is what
// explains the dropped subjects -- expansion is not needed and must not
// fire, because widening a requirement that already has a live answer would
// change what the caller asked for.
func TestChaos4099_MixedDirectAndUnreachableSubjectsNarrowRatherThanGap(t *testing.T) {
	t.Parallel()

	registry, stubs := scopeRegistry(t)
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeRepo, scopeProject}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent))
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(stubs[FactMetrics].queries) != 1 {
		t.Fatalf("metrics queried %d time(s), want 1 -- a mixed set still runs", len(stubs[FactMetrics].queries))
	}
	if len(bundle.Scope.Events) != 0 {
		t.Fatalf("events = %+v, want none when the requirement is answerable directly", bundle.Scope.Events)
	}
	if !strings.HasPrefix(bundle.Coverage.Sources[0].Reason, FactNarrowReasonSubjectKindUnsupported) {
		t.Fatalf("reason = %q, want the narrowing note", bundle.Coverage.Sources[0].Reason)
	}
}

// ---------------------------------------------------------------------------
// Invariant 7 as a property, not an example
// ---------------------------------------------------------------------------

// TestChaos4099_NoExpansionOutcomeEverMapsToPruned is ruling invariant 7
// asserted over the WHOLE vocabulary rather than the values that happen to
// be reachable today.
//
// An example-based test would keep passing if a future outcome were added
// and mapped to SourcePruned. The entire defect is one vocabulary having
// been used to say what another one means, so the guarantee is stated as a
// property over every value the type can take.
func TestChaos4099_NoExpansionOutcomeEverMapsToPruned(t *testing.T) {
	t.Parallel()

	// The EXACT mapping, not merely "not pruned" (codex review: a loose
	// assertion here would pass for any wrong-but-unpruned state).
	wantState := map[FactScopeExpansionOutcome]SourceState{
		FactScopePolicyUnavailable:  SourceUnconfigured,
		FactScopeExpandedPartial:    SourceTruncated,
		FactScopeFailed:             SourceUnavailable,
		FactScopeTargetKindMismatch: SourceUnavailable,
		FactScopeAttemptedEmpty:     SourceNoData,
	}
	for outcome, want := range wantState {
		if got := factScopeGapSourceState(outcome); got != want {
			t.Fatalf("factScopeGapSourceState(%q) = %q, want %q", outcome, got, want)
		}
	}
	for _, outcome := range []FactScopeExpansionOutcome{
		FactScopeNotNeeded, FactScopePolicyUnavailable, FactScopeAttemptedEmpty,
		FactScopeTargetKindMismatch, FactScopeExpanded, FactScopeExpandedPartial, FactScopeFailed,
	} {
		state := factScopeGapSourceState(outcome)
		if state == SourcePruned {
			t.Fatalf("outcome %q maps to SourcePruned -- that vocabulary asserts a proof this one does not hold", outcome)
		}
		if !validFactSourceState(state) {
			t.Fatalf("outcome %q maps to %q, which no provider may return either", outcome, state)
		}
	}
	// An outcome this code has never heard of must FAIL CLOSED, or the
	// defect returns one enum addition at a time.
	unknown := FactScopeExpansionOutcome("something_new_v2")
	if !factScopeGapDegrades(unknown) {
		t.Fatal("an unrecognised outcome must degrade -- defaulting to 'nothing is missing' is this ticket's defect")
	}
	if got := factScopeGapSourceState(unknown); got != SourceUnavailable {
		t.Fatalf("unknown outcome maps to %q, want unavailable", got)
	}
	// And the degrading split is exactly the outcomes that mean "the system
	// did not, or could not, look".
	for outcome, wantDegrades := range map[FactScopeExpansionOutcome]bool{
		FactScopePolicyUnavailable:  true,
		FactScopeExpandedPartial:    true,
		FactScopeFailed:             true,
		FactScopeTargetKindMismatch: true,
		FactScopeAttemptedEmpty:     false,
		FactScopeNotNeeded:          false,
		FactScopeExpanded:           false,
	} {
		if got := factScopeGapDegrades(outcome); got != wantDegrades {
			t.Fatalf("factScopeGapDegrades(%q) = %v, want %v", outcome, got, wantDegrades)
		}
	}
}

// TestChaos4099_TheCoverageReasonLeaksNoIdentity holds the content-safety
// line every coverage reason holds. The oracle is an ALLOW-LIST of the
// closed vocabularies the reason may contain, so a leak nobody anticipated
// still fails.
func TestChaos4099_TheCoverageReasonLeaksNoIdentity(t *testing.T) {
	t.Parallel()

	registry, _ := scopeRegistry(t)
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeProject}, caseSixtyRequirements(), TemporalCurrent))
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	for _, observation := range bundle.Coverage.Sources {
		for _, forbidden := range []string{
			scopeProject.CanonicalID, scopeProject.Label, "TITAN", "Titan",
		} {
			if strings.Contains(observation.Reason, forbidden) {
				t.Fatalf("reason %q leaks subject identity %q", observation.Reason, forbidden)
			}
		}
		// AuthorizationDroppedCount must never surface: "targets existed but
		// you cannot see them" is an existence side-channel (invariant 9).
		if strings.Contains(observation.Reason, "authorization") {
			t.Fatalf("reason %q mentions authorization -- that is a telemetry-only count", observation.Reason)
		}
	}
}

// ---------------------------------------------------------------------------
// Invariants 1/2/3/4 -- what expansion must NOT touch
// ---------------------------------------------------------------------------

// TestChaos4099_ScopeFixtureKindsMatchWhatTheResolverAssumes checks this
// FILE's fixture, not production -- see scopeCapabilities' doc comment for
// why an import of devhealthfacts would be a cycle, and where the real
// acceptance-point-3 guard lives
// (devhealthfacts/chaos4099_capability_kinds_test.go).
//
// It still earns its place: if someone "fixes" a failing test here by
// widening the fixture instead of the code, every other assertion in this
// file silently stops testing the project gap at all.
func TestChaos4099_ScopeFixtureKindsMatchWhatTheResolverAssumes(t *testing.T) {
	t.Parallel()

	for kind, capability := range scopeCapabilities() {
		if supportsSubjectKind(capability.SupportedSubjectKinds, SubjectProject) {
			t.Fatalf("%s fixture declares project support -- every project-gap assertion in this file would then be vacuous", kind)
		}
		if supportsSubjectKind(capability.SupportedSubjectKinds, SubjectTeam) {
			t.Fatalf("%s fixture declares team support -- same reason", kind)
		}
	}
}

// TestChaos4099_ResolutionAndRequestSubjectsAreNeverMutated is ruling
// invariants 1 and 2 at the seam they apply to: the resolver may grant a
// fact-READ permission and nothing else. RootSubjects go in and come out
// byte-identical, and no derived target is ever appended to them.
func TestChaos4099_ResolutionAndRequestSubjectsAreNeverMutated(t *testing.T) {
	t.Parallel()

	roots := []SubjectRef{scopeProject, scopeTeam}
	request := scopeRequest(roots, caseSixtyRequirements(), TemporalCurrent)
	before := append([]SubjectRef(nil), request.Subjects...)

	registry, _ := scopeRegistry(t)
	if _, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if !reflect.DeepEqual(request.Subjects, before) {
		t.Fatalf("request.Subjects = %+v, want byte-identical %+v", request.Subjects, before)
	}
	if request.Scope != nil {
		t.Fatal("ReadFacts wrote scope back onto the caller's request value -- expansion must not be observable as a mutation")
	}
}

// TestChaos4099_DerivedTargetsEnterScopeOnlyThroughProvenance is ruling
// invariant 4. investigationScopeSubjectSet is the gate buildFactQuery and
// mergeFactProviderResult both check against, so what it admits is exactly
// what may be read and what facts may be kept. It must widen from the
// resolver's PROVENANCE list and from nothing else -- a rule about kinds
// here would be the global widen option D was ratified instead of.
func TestChaos4099_DerivedTargetsEnterScopeOnlyThroughProvenance(t *testing.T) {
	t.Parallel()

	request := scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent)

	// A scope that ADMITTED the repository widens the gate.
	request.Scope = &FactReadScope{
		DerivedSubjects: map[FactKind][]SubjectRef{FactMetrics: {scopeRepo}},
		Derivations: []FactScopeDerivation{{
			Root: scopeProject, Target: scopeRepo,
			Policy: FactScopePolicyProjectWorkItemRepository, Basis: FactScopeBasisActivityProxy,
		}},
		Gaps: map[FactKind]FactScopeGap{},
	}
	if _, ok := investigationScopeSubjectSet(request)[canonicalFactSubjectKey(scopeRepo)]; !ok {
		t.Fatal("an admitted derived target must be in the investigation scope set, or its own read is rejected as out of scope")
	}

	// A scope carrying the same subject in DerivedSubjects but with NO
	// provenance does not. Provenance is the record that authorization ran.
	request.Scope = &FactReadScope{
		DerivedSubjects: map[FactKind][]SubjectRef{FactMetrics: {scopeRepo}},
		Gaps:            map[FactKind]FactScopeGap{},
	}
	if _, ok := investigationScopeSubjectSet(request)[canonicalFactSubjectKey(scopeRepo)]; ok {
		t.Fatal("a subject with no derivation record widened scope -- that is ID smuggling past authorization")
	}
}

// TestChaos4099_DerivedSubjectsAppendToRootsAndDeduplicate pins ruling
// invariant 8's ordering half and the dedup that keeps buildFactQuery's
// uniqueness check from turning a successful expansion into a failed
// investigation.
func TestChaos4099_DerivedSubjectsAppendToRootsAndDeduplicate(t *testing.T) {
	t.Parallel()

	roots := []SubjectRef{scopeRepo}
	other := subject(SubjectRepository, "repo:github:web")
	got := appendDerivedReadSubjects(roots, []SubjectRef{other, scopeRepo, other})
	want := []SubjectRef{scopeRepo, other}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subjects = %+v, want roots first then deduplicated derived %+v", got, want)
	}
	if len(roots) != 1 {
		t.Fatalf("the roots slice was mutated: %+v", roots)
	}
	// Deterministic across repeated calls -- the property that makes two
	// runs over the same graph produce byte-identical plans.
	if !reflect.DeepEqual(appendDerivedReadSubjects(roots, []SubjectRef{other, scopeRepo}), want) {
		t.Fatal("appendDerivedReadSubjects is not deterministic")
	}
}

// TestChaos4099_ARequirementOverrideStaysAuthoritative is ruling invariant 3.
// A requirement that names its own subjects is honored as given; expansion
// decides from THOSE, never from the investigation-wide set, so an override
// cannot be widened out from under the caller.
func TestChaos4099_ARequirementOverrideStaysAuthoritative(t *testing.T) {
	t.Parallel()

	// Investigation scope holds a repository (metrics-answerable) AND the
	// project. The requirement scopes itself to the project alone, so the
	// requirement is unreachable even though the investigation as a whole
	// has an answerable subject.
	request := scopeRequest(
		[]SubjectRef{scopeRepo, scopeProject},
		[]FactRequirement{{Kind: FactMetrics, Subjects: []SubjectRef{scopeProject}}},
		TemporalCurrent,
	)
	registry, stubs := scopeRegistry(t)
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(stubs[FactMetrics].queries) != 0 {
		t.Fatal("the override named an unreachable subject; the repository in investigation scope must not be substituted for it")
	}
	if len(bundle.Scope.Events) != 1 || bundle.Scope.Events[0].OriginCount != 1 {
		t.Fatalf("events = %+v, want one decision over the override's single subject", bundle.Scope.Events)
	}
}

// ---------------------------------------------------------------------------
// Engine-level: telemetry emission and answer disclosure
// ---------------------------------------------------------------------------

func scopeEngine(t *testing.T, telemetry EngineTelemetry, subjects []SubjectRef) (*Engine, InvestigationRequest) {
	t.Helper()
	registry, _ := scopeRegistry(t)
	return scopeEngineWithRegistry(t, telemetry, subjects, registry)
}

func scopeEngineWithRegistry(t *testing.T, telemetry EngineTelemetry, subjects []SubjectRef, registry *FactCapabilityRegistry) (*Engine, InvestigationRequest) {
	t.Helper()
	interpretation := InterpretedQuestion{
		Shape: ShapeSingleSubject, RequestedJudgment: "release_readiness_and_drivers",
		TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: caseSixtyRequirements(),
	}
	// The caller supplies a REAL registry, over real FactProvider
	// implementations, so the planner, the scope resolver, the coverage
	// merge and the bundle all run for real. A CanonicalFactReader double
	// here would let the whole mechanism under test be skipped.
	committed := subjects[0]
	candidate := affirmationCandidate(ResolutionCommitted)
	candidate.Subject = committed

	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{candidate}, Committed: subjects},
			context:    emptyAffirmationGraph(),
			bases:      provenCommitBases(subjects...),
		},
		Facts: registry,
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			draft := affirmationResult()
			draft.SubjectResolution = SubjectResolution{}
			draft.Status = InvestigationNoMatch
			// The synthesizer merges the bundle's coverage, as production
			// composition does -- so the disclosure path is exercised
			// against a result that already carries the fact coverage.
			draft.Coverage = input.Facts.Coverage
			draft.Versions = VersionSet{
				Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
				InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
			}
			return draft, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(100, 0).UTC() },
		NewResultID:    func() string { return "result_12345678" },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.Question = "how is this project doing?"
	return engine, request
}

// TestChaos4099_ExpansionDecisionsReachTheEngineTelemetry is THE MUTATION
// CHECK for the emission call.
//
// Delete `e.recordFactScopeExpansion(...)` from Investigate and this test
// fails. That is its whole job, and it is named so the failure message says
// what was removed. CHAOS-4085 shipped a telemetry event nothing in
// production emitted and nobody noticed for a whole ticket cycle; the
// standing order (CHAOS-4089) is that a decision branch ships with a test
// that fails when its signal stops flowing.
func TestChaos4099_ExpansionDecisionsReachTheEngineTelemetry(t *testing.T) {
	telemetry := &recordingTelemetry{}
	engine, request := scopeEngine(t, telemetry, []SubjectRef{scopeProject})

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	if len(telemetry.factScopeExpansions) != 3 {
		t.Fatalf("expansion events = %d, want 3 (one per requirement) -- if this is 0, the emission call was removed from Investigate", len(telemetry.factScopeExpansions))
	}
	for _, event := range telemetry.factScopeExpansions {
		if event.Outcome != FactScopePolicyUnavailable {
			t.Fatalf("outcome = %q, want policy_unavailable in stage 1", event.Outcome)
		}
		if event.OriginKind != SubjectProject || event.OriginCount != 1 {
			t.Fatalf("event = %+v, want a single project origin", event)
		}
		if event.AdmittedCount != 0 || event.CandidateCount != 0 {
			t.Fatalf("event = %+v, want no traversal counts: stage 1 runs no traversal", event)
		}
	}
}

// TestChaos4099_AnAnswerableInvestigationEmitsNoExpansionEvent is the
// counterpart: a spurious event on every ordinary answer makes the rate
// signal useless.
func TestChaos4099_AnAnswerableInvestigationEmitsNoExpansionEvent(t *testing.T) {
	telemetry := &recordingTelemetry{}
	engine, request := scopeEngine(t, telemetry, []SubjectRef{scopeRepo})

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	// metrics is answerable for a repository; pull_requests and reviews are
	// not, and a repository is not an expansion-eligible origin, so they
	// prune honestly and emit nothing.
	if len(telemetry.factScopeExpansions) != 0 {
		t.Fatalf("events = %+v, want none for a repository-scoped investigation", telemetry.factScopeExpansions)
	}
}

// TestChaos4099_TheAnswerDisclosesTheGapAndStaysValid is the answer-facing
// half, asserted through the REAL contract validator.
//
// The pre-fix answer was a bare no_match with an empty limitation list and
// Coverage.Partial false: nothing in it distinguished "this project has no
// pull requests" from "nobody looked". Validate() is called because a
// disclosure that made the result unservable would be a worse defect than
// the one being fixed -- that is exactly how CHAOS-4098 got its 500.
func TestChaos4099_TheAnswerDisclosesTheGapAndStaysValid(t *testing.T) {
	telemetry := &recordingTelemetry{}
	engine, request := scopeEngine(t, telemetry, []SubjectRef{scopeProject})

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the disclosed result must be servable: %v", err)
	}
	found := false
	for _, limitation := range result.Limitations {
		if limitation == contractsv1.ContextFabricFactScopeUnexpandedLimitation {
			found = true
		}
	}
	if !found {
		t.Fatalf("limitations = %v, want the fact-scope disclosure", result.Limitations)
	}
	if !result.Coverage.Partial {
		t.Fatal("Coverage.Partial = false on a result that disclosed a gap")
	}
	// The committed subject is untouched: expansion grants a read
	// permission, never a second commit (ruling invariants 1 and 2).
	if len(result.SubjectResolution.Committed) != 1 || result.SubjectResolution.Committed[0] != scopeProject {
		t.Fatalf("Committed = %+v, want the project unchanged", result.SubjectResolution.Committed)
	}
}

// TestChaos4099_TheDisclosureIsServiceAuthoredAndNamesNothing pins both the
// registration (so it can displace a model caveat but never BE the caveat
// displaced, and so LimitationsDisplaced's coherence rule accepts it) and
// the content-safety line.
func TestChaos4099_TheDisclosureIsServiceAuthoredAndNamesNothing(t *testing.T) {
	t.Parallel()

	disclosure := contractsv1.ContextFabricFactScopeUnexpandedLimitation
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(disclosure) {
		t.Fatal("the disclosure is not registered service-authored -- a result that displaces a caveat for it will fail validation")
	}
	for _, forbidden := range []string{
		"project", "team", "repository", "pull request", "review", "metric",
		string(FactScopePolicyProjectWorkItemRepository),
		string(FactScopePolicyProjectWorkItemPullRequest),
		string(FactScopePolicyProjectWorkItemPullRequestReview),
		"activity_proxy", "policy", "authorization", "traversal",
	} {
		if strings.Contains(strings.ToLower(disclosure), forbidden) {
			t.Fatalf("the disclosure names %q -- a reader cannot act on it, and operators have telemetry", forbidden)
		}
	}
}

// TestChaos4099_ANonDegradingOutcomeDoesNotDisclose pins the trigger's lower
// bound. attempted_empty is a genuine proof of absence -- the chain ran and
// ended -- and disclosing there would train readers to ignore the disclosure
// everywhere it matters.
func TestChaos4099_ANonDegradingOutcomeDoesNotDisclose(t *testing.T) {
	t.Parallel()

	for outcome, wantDisclosure := range map[FactScopeExpansionOutcome]bool{
		FactScopePolicyUnavailable:  true,
		FactScopeExpandedPartial:    true,
		FactScopeFailed:             true,
		FactScopeTargetKindMismatch: true,
		FactScopeAttemptedEmpty:     false,
	} {
		scope := &FactReadScope{Gaps: map[FactKind]FactScopeGap{FactMetrics: {Outcome: outcome}}}
		if got := scope.HasDisclosableGap(); got != wantDisclosure {
			t.Fatalf("HasDisclosableGap for %q = %v, want %v", outcome, got, wantDisclosure)
		}
		result := affirmationResult()
		applyFactScopeDisclosure(&result, scope)
		disclosed := len(result.Limitations) == 1 &&
			result.Limitations[0] == contractsv1.ContextFabricFactScopeUnexpandedLimitation
		if disclosed != wantDisclosure {
			t.Fatalf("outcome %q disclosed = %v, want %v", outcome, disclosed, wantDisclosure)
		}
	}
}

// TestChaos4099_TheDisclosureIsIdempotent pins that a second application
// neither duplicates the sentence nor inflates LimitationsDisplaced.
func TestChaos4099_TheDisclosureIsIdempotent(t *testing.T) {
	t.Parallel()

	scope := &FactReadScope{Gaps: map[FactKind]FactScopeGap{FactMetrics: {Outcome: FactScopePolicyUnavailable}}}
	result := affirmationResult()
	applyFactScopeDisclosure(&result, scope)
	first := append([]string(nil), result.Limitations...)
	firstDisplaced := result.LimitationsDisplaced

	applyFactScopeDisclosure(&result, scope)
	if !reflect.DeepEqual(result.Limitations, first) {
		t.Fatalf("limitations = %v, want unchanged %v", result.Limitations, first)
	}
	if result.LimitationsDisplaced != firstDisplaced {
		t.Fatalf("LimitationsDisplaced = %d, want unchanged %d", result.LimitationsDisplaced, firstDisplaced)
	}
}

// TestChaos4099_ANilScopeChangesNothing pins the compatibility guarantee
// every pre-existing caller depends on: a request that never met a resolver
// behaves byte-identically to before this ticket.
func TestChaos4099_ANilScopeChangesNothing(t *testing.T) {
	t.Parallel()

	var scope *FactReadScope
	if scope.derivedSubjectsFor(FactMetrics) != nil {
		t.Fatal("a nil scope must derive nothing")
	}
	if _, ok := scope.gapFor(FactMetrics); ok {
		t.Fatal("a nil scope must hold no gap")
	}
	if scope.HasDisclosableGap() || scope.HasActivityProxyDerivation() {
		t.Fatal("a nil scope must trigger no disclosure")
	}
	result := affirmationResult()
	applyFactScopeDisclosure(&result, nil)
	if len(result.Limitations) != 0 {
		t.Fatalf("limitations = %v, want none", result.Limitations)
	}
}

// ---------------------------------------------------------------------------
// The stage-2 port, exercised at stage 1
// ---------------------------------------------------------------------------

// recordingScopeExpander is a real FactScopeExpander -- not a struct double
// standing in for one -- so the resolver's own admission, target-kind and
// fail-closed logic runs against it exactly as it will against the graph
// implementation stage 2 wires.
type recordingScopeExpander struct {
	targets []SubjectRef
	// perCall overrides targets on a per-invocation basis, so a test can
	// give two origin groups DIFFERENT target sets. Identical sets cannot
	// distinguish the requirement-level cap from dedup (codex round 3).
	perCall  [][]SubjectRef
	counts   FactScopeExpansionCounts
	err      error
	calls    int
	requests []FactScopeExpansionRequest
}

func (e *recordingScopeExpander) ExpandFactScope(_ context.Context, request FactScopeExpansionRequest) (FactScopeExpansionResult, error) {
	e.calls++
	e.requests = append(e.requests, request)
	if e.err != nil {
		return FactScopeExpansionResult{Counts: e.counts}, e.err
	}
	targets := e.targets
	if len(e.perCall) > 0 {
		if e.calls-1 < len(e.perCall) {
			targets = e.perCall[e.calls-1]
		} else {
			targets = nil
		}
	}
	return FactScopeExpansionResult{Targets: targets, Counts: e.counts}, nil
}

// TestChaos4099_ADisabledPolicyNeverReachesTheExpander is the stage gate
// itself. Stage 1 must not traverse, whatever is wired: an expander that
// runs behind a disabled policy would mean the staged delivery never
// actually staged anything.
func TestChaos4099_ADisabledPolicyNeverReachesTheExpander(t *testing.T) {
	t.Parallel()

	expander := &recordingScopeExpander{targets: []SubjectRef{scopeRepo}}
	scope := NewFactReadScopeResolver(expander).Resolve(
		context.Background(),
		newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject}, caseSixtyRequirements(), TemporalCurrent)),
		scopeCapabilities(),
	)
	if expander.calls != 0 {
		t.Fatalf("expander ran %d time(s) -- every stage-1 policy is disabled", expander.calls)
	}
	if len(scope.DerivedSubjects) != 0 || len(scope.Derivations) != 0 {
		t.Fatalf("scope admitted subjects with every policy disabled: %+v", scope)
	}
	for _, event := range scope.Events {
		if event.Outcome != FactScopePolicyUnavailable {
			t.Fatalf("outcome = %q, want policy_unavailable", event.Outcome)
		}
	}
}

// TestChaos4099_AFailedTraversalAdmitsNothing pins the fail-closed rule for
// stage 2's error path. A subject set assembled from a half-finished
// authorization pass is not one anybody may read facts for, so a partial
// result before the error is discarded rather than used.
func TestChaos4099_AFailedTraversalAdmitsNothing(t *testing.T) {
	restore := factScopePolicies
	t.Cleanup(func() { factScopePolicies = restore })
	factScopePolicies = map[FactKind]map[SubjectKind]factScopePolicyRule{
		FactMetrics: {SubjectProject: {
			Policy: FactScopePolicyProjectWorkItemRepository, TargetKind: SubjectRepository,
			Basis: FactScopeBasisActivityProxy, Enabled: true,
		}},
	}
	expander := &recordingScopeExpander{
		targets: []SubjectRef{scopeRepo},
		counts:  FactScopeExpansionCounts{CandidateCount: 4, AuthorizationDroppedCount: 1},
		err:     context.DeadlineExceeded,
	}
	scope := NewFactReadScopeResolver(expander).Resolve(
		context.Background(),
		newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent)),
		scopeCapabilities(),
	)
	if len(scope.DerivedSubjects) != 0 || len(scope.Derivations) != 0 {
		t.Fatalf("a failed traversal admitted subjects: %+v", scope)
	}
	if len(scope.Events) != 1 {
		t.Fatalf("events = %+v, want one", scope.Events)
	}
	event := scope.Events[0]
	if event.Outcome != FactScopeFailed {
		t.Fatalf("outcome = %q, want failed", event.Outcome)
	}
	if event.FailureClass != FactScopeFailureTimeout {
		t.Fatalf("FailureClass = %q, want timeout -- a deadline and an unreachable backend demand opposite operator responses", event.FailureClass)
	}
	if event.AdmittedCount != 0 {
		t.Fatalf("AdmittedCount = %d, want 0", event.AdmittedCount)
	}
	// The diagnostic counts observed BEFORE the error still reach telemetry:
	// a failure with no counts is indistinguishable from one that never got
	// started, which is the diagnosis this stream exists to enable.
	if event.CandidateCount != 4 || event.AuthorizationDroppedCount != 1 {
		t.Fatalf("event = %+v, want the pre-failure counts preserved", event)
	}
	if gap, ok := scope.Gaps[FactMetrics]; !ok || gap.Outcome != FactScopeFailed {
		t.Fatalf("gaps = %+v, want a failed gap recorded", scope.Gaps)
	}
}

// TestChaos4099_AWrongKindTargetIsNeverHandedToAProvider is the last-line
// admission check. buildFactQuery rejects an unsupported subject kind by
// failing the WHOLE bundle, so a policy that produced the wrong kind would
// turn an expansion into a lost investigation. The resolver re-checks the
// kind itself rather than trusting the expander.
func TestChaos4099_AWrongKindTargetIsNeverHandedToAProvider(t *testing.T) {
	restore := factScopePolicies
	t.Cleanup(func() { factScopePolicies = restore })
	factScopePolicies = map[FactKind]map[SubjectKind]factScopePolicyRule{
		FactMetrics: {SubjectProject: {
			Policy: FactScopePolicyProjectWorkItemRepository, TargetKind: SubjectRepository,
			Basis: FactScopeBasisActivityProxy, Enabled: true,
		}},
	}
	expander := &recordingScopeExpander{
		// A work item, not a repository: the shape a miswired chain produces.
		targets: []SubjectRef{subject(SubjectWorkItem, "work_item:linear:ABC-1")},
		counts:  FactScopeExpansionCounts{CandidateCount: 1},
	}
	scope := NewFactReadScopeResolver(expander).Resolve(
		context.Background(),
		newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent)),
		scopeCapabilities(),
	)
	if len(scope.DerivedSubjects) != 0 {
		t.Fatalf("a wrong-kind target was admitted: %+v", scope.DerivedSubjects)
	}
	if got := scope.Events[0].Outcome; got != FactScopeTargetKindMismatch {
		t.Fatalf("outcome = %q, want target_kind_mismatch -- it must not hide inside attempted_empty", got)
	}
	if scope.Events[0].TargetKindMismatchCount != 1 {
		t.Fatalf("TargetKindMismatchCount = %d, want 1", scope.Events[0].TargetKindMismatchCount)
	}
	// It DEGRADES. The cause is a wiring error, but Coverage.Partial
	// describes the ANSWER, and this answer is exactly as short of evidence
	// as one whose policy was switched off. A loud telemetry event for the
	// operator is not a substitute for telling the reader.
	if !scope.HasDisclosableGap() {
		t.Fatal("a wrong-kind traversal yielded no facts; the reader is owed the same disclosure as any other gap")
	}
}

// TestChaos4099_TruncationIsReportedNotSwallowed pins ruling invariant 8's
// truncation half: a capped traversal degrades and says so, never silently
// returning a short list as if it were complete.
func TestChaos4099_TruncationIsReportedNotSwallowed(t *testing.T) {
	restore := factScopePolicies
	t.Cleanup(func() { factScopePolicies = restore })
	factScopePolicies = map[FactKind]map[SubjectKind]factScopePolicyRule{
		FactMetrics: {SubjectProject: {
			Policy: FactScopePolicyProjectWorkItemRepository, TargetKind: SubjectRepository,
			Basis: FactScopeBasisActivityProxy, Enabled: true,
		}},
	}
	expander := &recordingScopeExpander{
		targets: []SubjectRef{scopeRepo},
		counts:  FactScopeExpansionCounts{CandidateCount: 1, Truncated: true},
	}
	scope := NewFactReadScopeResolver(expander).Resolve(
		context.Background(),
		newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent)),
		scopeCapabilities(),
	)
	if got := scope.Events[0].Outcome; got != FactScopeExpandedPartial {
		t.Fatalf("outcome = %q, want expanded_partial", got)
	}
	if !scope.HasDisclosableGap() {
		t.Fatal("a truncated expansion must disclose -- the answer is genuinely incomplete")
	}
	// It still admits what it DID reach: a partial answer beats none.
	if len(scope.DerivedSubjects[FactMetrics]) != 1 {
		t.Fatalf("derived = %+v, want the reached target still admitted", scope.DerivedSubjects)
	}
	if !scope.HasActivityProxyDerivation() {
		t.Fatal("an activity-proxy derivation must be reported as one")
	}
	// The traversal is asked for limit+1's worth, so truncation is DETECTED
	// rather than inferred from a full page.
	if expander.requests[0].Limit != maxFactScopeTargets {
		t.Fatalf("limit = %d, want %d", expander.requests[0].Limit, maxFactScopeTargets)
	}
}

// ---------------------------------------------------------------------------
// SINK-LEVEL telemetry -- see chaos4085_telemetry_sink_test.go's header
// ---------------------------------------------------------------------------

// TestChaos4099_ProductionTelemetryEmitsAnExpansionEvent asserts the
// PRODUCTION sink's real output bytes.
//
// The compile-time proof is the load-bearing half: RecordFactScopeExpansion
// is declared on EngineTelemetry itself, so a sink that drops it cannot
// build. CHAOS-4085's event sat behind an optional interface nothing
// implemented and vanished silently; this ticket refused that shape, and the
// assertion states the dependency rather than leaving it implicit.
//
// EVERY field is asserted, including the zero counts. A field populated on
// the struct and never written by the sink is exactly the miss this file's
// sibling exists to prevent.
func TestChaos4099_ProductionTelemetryEmitsAnExpansionEvent(t *testing.T) {
	var _ EngineTelemetry = SlogEngineTelemetry{}

	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordFactScopeExpansion(
			context.Background(),
			storage.Principal{OrgID: "org_sink_test"},
			FactScopeExpansionEvent{
				RequirementKind: FactPullRequests,
				OriginKind:      SubjectProject,
				TargetKind:      SubjectPullRequest,
				Policy:          FactScopePolicyProjectWorkItemPullRequest,
				Basis:           FactScopeBasisActivityProxy,
				Outcome:         FactScopeExpandedPartial,
				OriginCount:     1, CandidateCount: 9, AdmittedCount: 5,
				AuthorizationDroppedCount: 2, TemporalDroppedCount: 1,
				MissingNextHopCount: 1, TargetKindMismatchCount: 0,
				Truncated: true, FailureClass: FactScopeFailureNone,
			},
		)
	})
	if len(records) != 1 {
		t.Fatalf("want exactly one log record, got %d", len(records))
	}
	record := records[0]
	for key, want := range map[string]any{
		"org_id":           "org_sink_test",
		"requirement_kind": string(FactPullRequests),
		"origin_kind":      string(SubjectProject),
		"target_kind":      string(SubjectPullRequest),
		"policy":           string(FactScopePolicyProjectWorkItemPullRequest),
		"basis":            string(FactScopeBasisActivityProxy),
		"outcome":          string(FactScopeExpandedPartial),
		"truncated":        true,
		"failure_class":    "",
	} {
		if got, ok := record[key]; !ok || got != want {
			t.Fatalf("record[%q] = %v (present=%v), want %v -- an operator greps for this key", key, got, ok, want)
		}
	}
	for key, want := range map[string]float64{
		"origin_count": 1, "candidate_count": 9, "admitted_count": 5,
		"authorization_dropped_count": 2, "temporal_dropped_count": 1,
		"missing_next_hop_count": 1, "target_kind_mismatch_count": 0,
	} {
		got, ok := record[key].(float64)
		if !ok || got != want {
			t.Fatalf("record[%q] = %v, want %v -- a zero count must still be written, or 'dropped nothing' and 'never counted' are indistinguishable", key, record[key], want)
		}
	}
}

// TestChaos4099_ExpansionTelemetryLeaksNoIdentityAndSplitsLevelByDegradation
// holds the content-safety line and pins the level split.
//
// The oracle is an ALLOW-LIST: a denylist only catches leaks someone thought
// of, and the whole point is that no field carrying identity exists here at
// all.
func TestChaos4099_ExpansionTelemetryLeaksNoIdentityAndSplitsLevelByDegradation(t *testing.T) {
	allowed := map[string]struct{}{}
	for _, key := range []string{
		"time", "level", "msg", "org_id", "requirement_kind", "origin_kind", "target_kind",
		"policy", "basis", "outcome", "origin_count", "candidate_count", "admitted_count",
		"authorization_dropped_count", "temporal_dropped_count", "missing_next_hop_count",
		"target_kind_mismatch_count", "truncated", "failure_class",
	} {
		allowed[key] = struct{}{}
	}
	for outcome, wantLevel := range map[FactScopeExpansionOutcome]string{
		FactScopePolicyUnavailable: "WARN",
		FactScopeExpandedPartial:   "WARN",
		FactScopeFailed:            "WARN",
		FactScopeExpanded:          "INFO",
		FactScopeAttemptedEmpty:    "INFO",
	} {
		records := captureSlogJSON(t, func(logger *slog.Logger) {
			NewSlogEngineTelemetry(logger).RecordFactScopeExpansion(
				context.Background(),
				storage.Principal{OrgID: "org_leak_test"},
				FactScopeExpansionEvent{
					RequirementKind: FactMetrics, OriginKind: SubjectProject,
					TargetKind: SubjectRepository, Policy: FactScopePolicyProjectWorkItemRepository,
					Basis: FactScopeBasisActivityProxy, Outcome: outcome, OriginCount: 1,
				},
			)
		})
		if len(records) != 1 {
			t.Fatalf("outcome %q: want one record, got %d", outcome, len(records))
		}
		for key := range records[0] {
			if _, ok := allowed[key]; !ok {
				t.Fatalf("outcome %q: unexpected log key %q -- every field here must be a closed enum or a count", outcome, key)
			}
		}
		if got := records[0]["level"]; got != wantLevel {
			t.Fatalf("outcome %q: level = %v, want %v -- a degraded answer is worth an operator's attention", outcome, got, wantLevel)
		}
	}
}

// TestChaos4099_EveryEventFieldReachesTheSink is the anti-drift guard the
// CHAOS-4085 header asks for by name: "if you add a field to a telemetry
// event, add its assertion here too". A struct field with no log key is a
// field no operator will ever see.
func TestChaos4099_EveryEventFieldReachesTheSink(t *testing.T) {
	// The mapping is declared, then checked against the struct's real field
	// set, so ADDING a field without a log key fails here rather than
	// shipping unread.
	fieldToKey := map[string]string{
		"RequirementKind": "requirement_kind", "OriginKind": "origin_kind",
		"TargetKind": "target_kind", "Policy": "policy", "Basis": "basis",
		"Outcome": "outcome", "OriginCount": "origin_count",
		"CandidateCount": "candidate_count", "AdmittedCount": "admitted_count",
		"AuthorizationDroppedCount": "authorization_dropped_count",
		"TemporalDroppedCount":      "temporal_dropped_count",
		"MissingNextHopCount":       "missing_next_hop_count",
		"TargetKindMismatchCount":   "target_kind_mismatch_count",
		"Truncated":                 "truncated", "FailureClass": "failure_class",
	}
	eventType := reflect.TypeOf(FactScopeExpansionEvent{})
	if eventType.NumField() != len(fieldToKey) {
		t.Fatalf("FactScopeExpansionEvent has %d fields, %d are mapped to log keys -- an unmapped field is one no operator sees", eventType.NumField(), len(fieldToKey))
	}
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordFactScopeExpansion(
			context.Background(), storage.Principal{OrgID: "org_1"},
			FactScopeExpansionEvent{Outcome: FactScopeExpanded},
		)
	})
	for i := 0; i < eventType.NumField(); i++ {
		name := eventType.Field(i).Name
		key, mapped := fieldToKey[name]
		if !mapped {
			t.Fatalf("FactScopeExpansionEvent.%s has no log key", name)
		}
		if _, present := records[0][key]; !present {
			t.Fatalf("the sink never wrote %q for field %s", key, name)
		}
	}
}

// ---------------------------------------------------------------------------
// End to end with a policy actually enabled (stage 2's shape, proven now)
// ---------------------------------------------------------------------------

// enableMetricsProjectPolicy switches on the metrics policy for the duration
// of one test. Stage 1 ships every policy dark, so without this no test could
// exercise the admission path at all -- and an admission path that first runs
// in the change that activates it is an admission path nobody reviewed.
func enableMetricsProjectPolicy(t *testing.T, limit int) {
	t.Helper()
	restore := factScopePolicies
	t.Cleanup(func() { factScopePolicies = restore })
	factScopePolicies = map[FactKind]map[SubjectKind]factScopePolicyRule{
		FactMetrics: {SubjectProject: {
			Policy: FactScopePolicyProjectWorkItemRepository, TargetKind: SubjectRepository,
			Basis: FactScopeBasisActivityProxy, Enabled: true, Limit: limit,
		}},
	}
}

// TestChaos4099_AnEnabledPolicyReachesTheProviderAndDisclosesTheProxy is the
// full-stack proof, and the one codex review named as the most important
// missing invariant.
//
// It runs the REAL registry, the REAL planner, the REAL scope-set gate and a
// REAL provider, with a policy switched on -- so it exercises every step
// stage 2 will depend on:
//
//   - the derived repository reaches the provider as a query subject at all;
//   - investigationScopeSubjectSet admits it, so buildFactQuery does not
//     reject it as "outside the discovered investigation set";
//   - mergeFactProviderResult keeps a fact whose Subject is the derived
//     repository, so the fact is not silently dropped after being read;
//   - CanonicalFact.Subject stays the exact repository (ruling invariant 5);
//   - the answer discloses the activity proxy (ruling invariant 6).
//
// Every one of those is a place a derived read can be planned and then
// thrown away, which would present as "expansion did nothing" with no error
// anywhere.
func TestChaos4099_AnEnabledPolicyReachesTheProviderAndDisclosesTheProxy(t *testing.T) {
	enableMetricsProjectPolicy(t, 0)

	observed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	metrics := &factProviderStub{
		capability: planCapability(FactMetrics, "metrics", SubjectRepository),
		result: FactProviderResult{
			State: SourceAvailable, Version: "metrics-v1", ObservedAt: &observed,
			Facts: []CanonicalFact{{
				Kind: FactMetrics, Subject: scopeRepo,
				Fields:      map[string]FactValue{"lead_time_days": NumberFactValue(3.5)},
				ObservedAt:  &observed,
				SourceState: SourceAvailable,
			}},
		},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{
		ScopeExpander: &recordingScopeExpander{targets: []SubjectRef{scopeRepo}},
	})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent))
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(metrics.queries) != 1 {
		t.Fatalf("provider queried %d time(s), want 1 -- the derived subject never reached it", len(metrics.queries))
	}
	if got := metrics.queries[0].Subjects; len(got) != 1 || got[0] != scopeRepo {
		t.Fatalf("query subjects = %+v, want exactly the derived repository", got)
	}
	if len(bundle.Facts) != 1 {
		t.Fatalf("facts = %+v, want the derived fact retained -- a fact read and then dropped by the scope gate is invisible", bundle.Facts)
	}
	// Ruling invariant 5: the fact's subject is the exact repository, NOT
	// the project it was derived from. That is what keeps the fact
	// re-verifiable against its own source.
	if bundle.Facts[0].Subject != scopeRepo {
		t.Fatalf("fact subject = %+v, want the exact repository", bundle.Facts[0].Subject)
	}
	// ...and the derivation is recorded in the parallel structure instead.
	if len(bundle.Scope.Derivations) != 1 {
		t.Fatalf("derivations = %+v, want one", bundle.Scope.Derivations)
	}
	derivation := bundle.Scope.Derivations[0]
	if derivation.Root != scopeProject || derivation.Target != scopeRepo {
		t.Fatalf("derivation = %+v, want project -> repository", derivation)
	}
	if derivation.Policy != FactScopePolicyProjectWorkItemRepository || derivation.Basis != FactScopeBasisActivityProxy {
		t.Fatalf("derivation = %+v, want the named policy on an activity-proxy basis", derivation)
	}
	// Ruling invariant 6: the proxy is disclosed on the ANSWER.
	result := scopeServableResult()
	applyFactScopeDisclosure(&result, bundle.Scope)
	if len(result.Limitations) != 1 || result.Limitations[0] != contractsv1.ContextFabricFactScopeActivityProxyLimitation {
		t.Fatalf("limitations = %v, want the activity-proxy disclosure -- a reader takes a proxy for a roster without it", result.Limitations)
	}
	if !result.Coverage.Partial {
		t.Fatal("Coverage.Partial = false on a proxy-derived answer")
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the disclosed result must be servable: %v", err)
	}
}

// TestChaos4099_BothDisclosuresCanFireOnOneAnswer pins that the two
// disclosures are independent. One investigation can easily expand metrics
// through the proxy while reviews hit a disabled policy, and a reader told
// only about the gap would take the metrics at face value.
func TestChaos4099_BothDisclosuresCanFireOnOneAnswer(t *testing.T) {
	t.Parallel()

	scope := &FactReadScope{
		Gaps: map[FactKind]FactScopeGap{FactReviews: {Outcome: FactScopePolicyUnavailable}},
		Derivations: []FactScopeDerivation{{
			Root: scopeProject, Target: scopeRepo,
			Policy: FactScopePolicyProjectWorkItemRepository, Basis: FactScopeBasisActivityProxy,
		}},
	}
	result := scopeServableResult()
	applyFactScopeDisclosure(&result, scope)
	if len(result.Limitations) != 2 {
		t.Fatalf("limitations = %v, want both disclosures -- they say opposite things and neither implies the other", result.Limitations)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("two disclosures must still validate: %v", err)
	}
}

// TestChaos4099_ADirectBasisDerivationDoesNotClaimAProxy is the
// false-positive pin for the proxy disclosure. A future ownership edge must
// not inherit a caveat that does not apply to it, or the sentence stops
// carrying information.
func TestChaos4099_ADirectBasisDerivationDoesNotClaimAProxy(t *testing.T) {
	t.Parallel()

	scope := &FactReadScope{
		Gaps: map[FactKind]FactScopeGap{},
		Derivations: []FactScopeDerivation{{
			Root: scopeProject, Target: scopeRepo, Basis: FactScopeBasisDirect,
		}},
	}
	if scope.HasActivityProxyDerivation() {
		t.Fatal("a direct-basis derivation is not an activity proxy")
	}
	result := affirmationResult()
	applyFactScopeDisclosure(&result, scope)
	if len(result.Limitations) != 0 {
		t.Fatalf("limitations = %v, want none", result.Limitations)
	}
}

// TestChaos4099_TheResolverEnforcesTheCapItselfFromTheOverflowRow is ruling
// invariant 8's truncation half, pinned against the party that OWNS it.
//
// The failure it forbids is subtle and silent: an expander that issued
// `LIMIT 200` rather than `LIMIT 201` returns exactly a full page with
// Truncated=false, and a full page is indistinguishable from a truncated one
// without the overflow row. Trusting the expander's flag there produces
// `expanded` with no disclosure over a scope that was actually cut -- which
// is the silent truncation the invariant exists to forbid. So the resolver
// detects overflow itself and enforces the cap, rather than believing a
// count it did not compute.
func TestChaos4099_TheResolverEnforcesTheCapItselfFromTheOverflowRow(t *testing.T) {
	const limit = 3
	enableMetricsProjectPolicy(t, limit)

	// limit+1 targets, and an expander that (incorrectly) reports no
	// truncation -- the exact miswiring described above.
	targets := make([]SubjectRef, 0, limit+1)
	for _, id := range []string{"repo:github:a", "repo:github:b", "repo:github:c", "repo:github:d"} {
		targets = append(targets, subject(SubjectRepository, id))
	}
	expander := &recordingScopeExpander{
		targets: targets,
		counts:  FactScopeExpansionCounts{CandidateCount: len(targets), Truncated: false},
	}
	scope := NewFactReadScopeResolver(expander).Resolve(
		context.Background(),
		newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent)),
		scopeCapabilities(),
	)
	event := scope.Events[0]
	if !event.Truncated {
		t.Fatal("the resolver believed an expander that under-reported truncation -- a full page is not proof of completeness")
	}
	if event.Outcome != FactScopeExpandedPartial {
		t.Fatalf("outcome = %q, want expanded_partial", event.Outcome)
	}
	if event.AdmittedCount != limit {
		t.Fatalf("AdmittedCount = %d, want the cap enforced at %d", event.AdmittedCount, limit)
	}
	if got := len(scope.DerivedSubjects[FactMetrics]); got != limit {
		t.Fatalf("derived = %d subjects, want the cap enforced at %d", got, limit)
	}
	if !scope.HasDisclosableGap() {
		t.Fatal("a capped scope must disclose")
	}
	if expander.requests[0].Limit != limit {
		t.Fatalf("request limit = %d, want the policy's own %d", expander.requests[0].Limit, limit)
	}
}

// TestChaos4099_AForgedScopeOnTheRequestIsIgnored is the authorization pin
// for the trust boundary codex review found.
//
// ReadFacts used to HONOR a pre-populated request.Scope as a test injection
// point. investigationScopeSubjectSet trusts every Derivations entry, so an
// in-process caller could hand over a forged derivation for a repository it
// may not read, name that repository in a requirement override, and have
// buildFactQuery's own scope check wave it through -- ID smuggling past
// authorization (ruling invariant 3), and a path to new fact reads while
// every policy is still disabled. The resolver now always runs and always
// wins.
func TestChaos4099_AForgedScopeOnTheRequestIsIgnored(t *testing.T) {
	t.Parallel()

	forged := subject(SubjectRepository, "repo:github:not-yours")
	request := scopeRequest([]SubjectRef{scopeProject},
		[]FactRequirement{{Kind: FactMetrics, Subjects: []SubjectRef{forged}}}, TemporalCurrent)
	request.Scope = &FactReadScope{
		DerivedSubjects: map[FactKind][]SubjectRef{FactMetrics: {forged}},
		Derivations: []FactScopeDerivation{{
			Root: scopeProject, Target: forged,
			Policy: FactScopePolicyProjectWorkItemRepository, Basis: FactScopeBasisActivityProxy,
		}},
		Gaps: map[FactKind]FactScopeGap{},
	}
	registry, stubs := scopeRegistry(t)
	_, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err == nil {
		t.Fatal("a forged derivation was accepted -- the resolver must overwrite an incoming scope, never honor it")
	}
	if !strings.Contains(err.Error(), "outside the discovered investigation set") {
		t.Fatalf("error = %v, want the smuggled subject rejected as out of scope", err)
	}
	if len(stubs[FactMetrics].queries) != 0 {
		t.Fatal("the provider was queried for a subject that entered scope through a forged derivation")
	}
}

// TestChaos4099_AForgedScopeCannotSmuggleAnExpansionOrigin closes the
// escalation of the forged-scope hole (codex round 3, High).
//
// The round-1 fix stopped a forged derivation at buildFactQuery. That holds
// only while the forged subject is one a capability DIRECTLY supports --
// buildFactQuery is the gate it walks into. Forge an out-of-investigation
// PROJECT instead and the registry never reaches that gate for it: the
// project becomes an expansion ORIGIN (Resolve takes a requirement's roots
// from requirement.Subjects), and an enabled policy derives a repository from
// it. The repository is then legitimately in the resolved scope and IS read.
//
// The subject that reaches the provider is authorized; the ORIGIN it hangs
// off never was. That is the ID smuggling invariants 3 and 4 forbid, and no
// amount of downstream scope checking catches it, because by the time the
// derived subject exists the unauthorized origin has already done its work.
//
// The fix drops the incoming scope BEFORE validation, so the override is
// rejected as out of scope and the expander is never consulted at all.
func TestChaos4099_AForgedScopeCannotSmuggleAnExpansionOrigin(t *testing.T) {
	restore := factScopePolicies
	t.Cleanup(func() { factScopePolicies = restore })
	// The project policy ACTIVATED, which is what stage 2 does. The hole is
	// dark today only because nothing traverses yet.
	factScopePolicies = map[FactKind]map[SubjectKind]factScopePolicyRule{
		FactMetrics: {SubjectProject: {
			Policy: FactScopePolicyProjectWorkItemRepository, TargetKind: SubjectRepository,
			Basis: FactScopeBasisActivityProxy, Enabled: true, Limit: 10,
		}},
	}

	// scopeProject is the investigation's only authorized root. The forged
	// project is a DIFFERENT one the caller may not read.
	forgedOrigin := subject(SubjectProject, "project:linear:NOT-YOURS")
	derivedTarget := subject(SubjectRepository, "repo:github:secret")
	request := scopeRequest([]SubjectRef{scopeProject},
		[]FactRequirement{{Kind: FactMetrics, Subjects: []SubjectRef{forgedOrigin}}}, TemporalCurrent)
	request.Scope = &FactReadScope{
		DerivedSubjects: map[FactKind][]SubjectRef{},
		Derivations: []FactScopeDerivation{{
			Root: scopeProject, Target: forgedOrigin,
			Policy: FactScopePolicyProjectWorkItemRepository, Basis: FactScopeBasisActivityProxy,
		}},
		Gaps: map[FactKind]FactScopeGap{},
	}

	expander := &recordingScopeExpander{targets: []SubjectRef{derivedTarget}}
	metrics := &factProviderStub{
		capability: planCapability(FactMetrics, "metrics", SubjectRepository),
		result:     FactProviderResult{State: SourceAvailable, Version: "metrics-v1"},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{metrics},
		FactRegistryOptions{ScopeExpander: expander})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}

	_, err = registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err == nil {
		t.Fatal("a forged expansion origin was accepted -- an override may name only an investigation root")
	}
	if !strings.Contains(err.Error(), "outside the discovered investigation set") {
		t.Fatalf("error = %v, want the smuggled origin rejected as out of scope", err)
	}
	// The sharpest assertion: the unauthorized project must never have been
	// TRAVERSED. A rejection that arrives after the expander already walked
	// the graph for it has already leaked the existence of its repositories.
	if expander.calls != 0 {
		t.Fatalf("the expander traversed %d time(s) from a forged origin -- authorization must precede traversal", expander.calls)
	}
	if len(metrics.queries) != 0 {
		t.Fatal("the provider was queried for a subject derived from a forged origin")
	}
}

// TestChaos4099_ACleanGapNeverEvictsADegradingOne is the THIRD instance of
// the round-2 masking class (codex round 3, Medium), one level up from the
// two fixed inside expand().
//
// A requirement can have several origin kinds and only one disclosure slot.
// The slot used to go to the first origin in sorted order, and `project`
// sorts before `team`. So a project chain that ran and genuinely found
// nothing (attempted_empty -- non-degrading, SourceNoData) took the slot and
// DISCARDED a team gap that was still owed a disclosure.
//
// The bundle then reported a clean no_data with no Coverage.Partial, and
// HasDisclosableGap reads the same map, so the answer's sentence vanished
// too. Exactly this ticket's own defect, arrived at from a third direction.
func TestChaos4099_ACleanGapNeverEvictsADegradingOne(t *testing.T) {
	restore := factScopePolicies
	t.Cleanup(func() { factScopePolicies = restore })
	// The project origin is ENABLED and will find nothing; the team origin
	// stays disabled and therefore degrades. Sorted order decides project
	// first, so the non-degrading outcome reaches the slot first.
	factScopePolicies = map[FactKind]map[SubjectKind]factScopePolicyRule{
		FactMetrics: {
			SubjectProject: {
				Policy: FactScopePolicyProjectWorkItemRepository, TargetKind: SubjectRepository,
				Basis: FactScopeBasisActivityProxy, Enabled: true, Limit: 10,
			},
			SubjectTeam: {
				Policy: FactScopePolicyNone, TargetKind: SubjectRepository,
				Basis: FactScopeBasisActivityProxy, Enabled: false, Limit: 10,
			},
		},
	}

	// No targets and no counts: the chain ran and genuinely ended.
	expander := &recordingScopeExpander{}
	scope := NewFactReadScopeResolver(expander).Resolve(
		context.Background(),
		newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject, scopeTeam},
			[]FactRequirement{{Kind: FactMetrics}}, TemporalCurrent)),
		scopeCapabilities(),
	)

	// Both origins were decided, and the events keep the per-origin detail.
	if len(scope.Events) != 2 {
		t.Fatalf("events = %+v, want one per origin kind", scope.Events)
	}
	var sawCleanProject, sawDegradingTeam bool
	for _, event := range scope.Events {
		if event.OriginKind == SubjectProject && event.Outcome == FactScopeAttemptedEmpty {
			sawCleanProject = true
		}
		if event.OriginKind == SubjectTeam && factScopeGapDegrades(event.Outcome) {
			sawDegradingTeam = true
		}
	}
	if !sawCleanProject || !sawDegradingTeam {
		t.Fatalf("events = %+v, want a clean project outcome and a degrading team outcome", scope.Events)
	}

	gap, disclosed := scope.gapFor(FactMetrics)
	if !disclosed {
		t.Fatal("no gap was recorded at all -- the degrading team outcome is owed a disclosure")
	}
	if !factScopeGapDegrades(gap.Outcome) {
		t.Fatalf("gap = %+v, want the DEGRADING outcome to win the slot, not the first in sorted order", gap)
	}
	if gap.OriginKind != SubjectTeam {
		t.Fatalf("gap origin = %q, want the team origin that actually degraded", gap.OriginKind)
	}
	// The consequence the reader sees: without this, both the coverage state
	// and the answer's sentence go quiet.
	if factScopeGapSourceState(gap.Outcome) == SourceNoData {
		t.Fatal("the gap reports a clean no_data -- a discarded degrading outcome is a false proof of absence")
	}
	if !scope.HasDisclosableGap() {
		t.Fatal("HasDisclosableGap is false -- the answer would carry no unexpanded limitation")
	}
}

// TestChaos4099_APartiallyLostExpansionDegradesTheBundle closes the last
// masking path (codex round 3, Medium).
//
// Only the "nothing supported at all" branch of the planner consulted the
// resolver's gap. A requirement that lost SOME targets but kept others took
// the ordinary read path, and the provider answered SourceAvailable over a
// subject set the resolver already KNEW was incomplete.
//
// target_kind_mismatch is the live shape, because the round-2 fix
// deliberately made it retain its valid survivors. The engine's answer-level
// disclosure still fired, but the BUNDLE -- what direct consumers and
// synthesis input read -- claimed complete coverage. A disclosure the fact
// bundle contradicts is worth less than no disclosure at all.
func TestChaos4099_APartiallyLostExpansionDegradesTheBundle(t *testing.T) {
	restore := factScopePolicies
	t.Cleanup(func() { factScopePolicies = restore })
	factScopePolicies = map[FactKind]map[SubjectKind]factScopePolicyRule{
		FactMetrics: {SubjectProject: {
			Policy: FactScopePolicyProjectWorkItemRepository, TargetKind: SubjectRepository,
			Basis: FactScopeBasisActivityProxy, Enabled: true, Limit: 10,
		}},
	}

	// One good repository and one wrong-kind target: the survivor is
	// admitted and read, the mismatch is recorded.
	goodRepo := subject(SubjectRepository, "repo:github:api")
	wrongKind := subject(SubjectWorkItem, "work_item:linear:TITAN-1")
	expander := &recordingScopeExpander{targets: []SubjectRef{goodRepo, wrongKind}}

	observed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	metrics := &factProviderStub{
		capability: planCapability(FactMetrics, "metrics", SubjectRepository),
		result: FactProviderResult{
			State: SourceAvailable, Version: "metrics-v1", ObservedAt: &observed,
			Facts: []CanonicalFact{{
				Kind: FactMetrics, Subject: goodRepo,
				Fields:      map[string]FactValue{"lead_time_days": NumberFactValue(3.5)},
				ObservedAt:  &observed,
				SourceState: SourceAvailable,
			}},
		},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{metrics},
		FactRegistryOptions{ScopeExpander: expander})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}

	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent))
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}

	// The survivor WAS read -- degrading must not mean discarding.
	if len(bundle.Facts) != 1 {
		t.Fatalf("facts = %+v, want the valid survivor still read", bundle.Facts)
	}
	if len(metrics.queries) != 1 {
		t.Fatalf("provider queried %d time(s), want the surviving repository read", len(metrics.queries))
	}
	// And the bundle says the scope was incomplete.
	if !bundle.Coverage.Partial {
		t.Fatal("bundle reports complete coverage over a scope the resolver knows lost targets")
	}
	var observation SourceObservation
	for _, source := range bundle.Coverage.Sources {
		if source.Source == "canonical_fact:"+string(FactMetrics) {
			observation = source
		}
	}
	if observation.State == SourceAvailable {
		t.Fatalf("coverage state = %q, want a degrading state for a knowingly incomplete read", observation.State)
	}
	if !factStateDegrades(observation.State) {
		t.Fatalf("coverage state = %q does not degrade -- the gap is invisible to every bundle consumer", observation.State)
	}
	if !strings.Contains(observation.Reason, string(FactScopeReasonUnexpanded)) {
		t.Fatalf("reason = %q, want the unexpanded note riding on the capability's own observation", observation.Reason)
	}
	if len(bundle.Coverage.DegradedReasons) == 0 {
		t.Fatal("no degraded reason was filed for a knowingly incomplete read")
	}
}

// TestChaos4099_TheCapAndDedupApplyPerRequirementNotPerOriginGroup pins the
// second-order bound.
//
// Both the cap and the target-kind filter are applied per (origin kind),
// because that is the unit an expander is called for -- but a requirement can
// have several origin kinds. Without a requirement-level pass, two origin
// groups each admit up to `limit` targets for ONE requirement (twice the
// declared cap), and a target reachable from both is admitted twice.
//
// The duplicate is the sharp edge: buildFactQuery rejects a query whose
// subjects repeat with "fact query subjects must be unique", and ReadFacts
// turns that into a WHOLE-bundle failure. A successful expansion would
// destroy the investigation it was meant to help.
func TestChaos4099_TheCapAndDedupApplyPerRequirementNotPerOriginGroup(t *testing.T) {
	const limit = 2
	restore := factScopePolicies
	t.Cleanup(func() { factScopePolicies = restore })
	// BOTH origin kinds enabled and aimed at the same target kind, so one
	// requirement genuinely has two expanding origin groups.
	rule := factScopePolicyRule{
		Policy: FactScopePolicyProjectWorkItemRepository, TargetKind: SubjectRepository,
		Basis: FactScopeBasisActivityProxy, Enabled: true, Limit: limit,
	}
	factScopePolicies = map[FactKind]map[SubjectKind]factScopePolicyRule{
		FactMetrics: {SubjectProject: rule, SubjectTeam: rule},
	}
	// OVERLAPPING BUT DISTINCT groups: [a,b] and [b,c].
	//
	// Codex round 3 (Low): both groups previously returned the identical
	// pair, so dedup alone held the union at 2 and deleting the
	// requirement-level cap guard still passed -- the mutation check was
	// vacuous. With [a,b] and [b,c] the deduplicated union is 3, which is
	// over the cap of 2, so ONLY the cap can bring it back down. Removing
	// the guard now admits 3 and fails.
	repoA := subject(SubjectRepository, "repo:github:a")
	repoB := subject(SubjectRepository, "repo:github:b")
	repoC := subject(SubjectRepository, "repo:github:c")
	shared := []SubjectRef{repoA, repoB}
	expander := &recordingScopeExpander{
		perCall: [][]SubjectRef{{repoA, repoB}, {repoB, repoC}},
		counts:  FactScopeExpansionCounts{CandidateCount: 2},
	}

	scope := NewFactReadScopeResolver(expander).Resolve(
		context.Background(),
		newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject, scopeTeam},
			[]FactRequirement{{Kind: FactMetrics}}, TemporalCurrent)),
		scopeCapabilities(),
	)
	derived := scope.DerivedSubjects[FactMetrics]
	if len(derived) != len(shared) {
		t.Fatalf("derived = %+v, want the union capped and deduplicated to %d", derived, len(shared))
	}
	// The cap, not dedup, is what holds this at 2: the deduplicated union of
	// [a,b] and [b,c] is 3. Naming the excluded subject makes the assertion
	// about the cap rather than about the count alone.
	for _, target := range derived {
		if canonicalFactSubjectKey(target) == canonicalFactSubjectKey(repoC) {
			t.Fatalf("derived %+v admitted a subject past the policy's cap of %d", derived, limit)
		}
	}
	keys := map[string]struct{}{}
	for _, target := range derived {
		key := canonicalFactSubjectKey(target)
		if _, duplicate := keys[key]; duplicate {
			t.Fatalf("derived subject %q appears twice -- buildFactQuery rejects that and fails the WHOLE bundle", key)
		}
		keys[key] = struct{}{}
	}
	if len(derived) > limit {
		t.Fatalf("derived %d subjects, want the policy's cap of %d honored across origin groups", len(derived), limit)
	}
	// The second group reached nothing NEW, and says so rather than claiming
	// it admitted two subjects it did not add.
	var totalAdmitted int
	for _, event := range scope.Events {
		totalAdmitted += event.AdmittedCount
	}
	if totalAdmitted != len(derived) {
		t.Fatalf("events claim %d admitted subjects but %d are in scope -- telemetry must report what the provider is actually asked", totalAdmitted, len(derived))
	}
	// Deterministic: the same inputs produce the same scope, every time.
	repeat := NewFactReadScopeResolver(&recordingScopeExpander{
		perCall: [][]SubjectRef{{repoA, repoB}, {repoB, repoC}},
		counts:  FactScopeExpansionCounts{CandidateCount: 2},
	}).Resolve(
		context.Background(),
		newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject, scopeTeam},
			[]FactRequirement{{Kind: FactMetrics}}, TemporalCurrent)),
		scopeCapabilities(),
	)
	if !reflect.DeepEqual(repeat.DerivedSubjects[FactMetrics], derived) {
		t.Fatalf("scope is not deterministic: %+v vs %+v", repeat.DerivedSubjects[FactMetrics], derived)
	}
}

// ---------------------------------------------------------------------------
// The outcome ladder's ordering (codex round-2 findings)
// ---------------------------------------------------------------------------

// TestChaos4099_ATruncatedTraversalThatAdmittedNothingIsNotAProofOfAbsence
// pins the ladder's most dangerous ordering bug, which shipped in the first
// pass.
//
// A traversal that hit a cap and admitted nothing -- every candidate on the
// first page dropped by authorization, with more pages behind it -- is the
// LEAST-evidence case of all. Checking "did we admit nothing?" before "were
// we truncated?" reported it as attempted_empty: non-degrading, no
// disclosure, logged at INFO. That is this ticket's own defect reintroduced
// inside its fix, on the one path where the system knows least.
func TestChaos4099_ATruncatedTraversalThatAdmittedNothingIsNotAProofOfAbsence(t *testing.T) {
	enableMetricsProjectPolicy(t, 0)

	expander := &recordingScopeExpander{
		targets: nil,
		counts: FactScopeExpansionCounts{
			CandidateCount: 40, AuthorizationDroppedCount: 40, Truncated: true,
		},
	}
	scope := NewFactReadScopeResolver(expander).Resolve(
		context.Background(),
		newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent)),
		scopeCapabilities(),
	)
	event := scope.Events[0]
	if event.Outcome == FactScopeAttemptedEmpty {
		t.Fatal("a truncated traversal that admitted nothing claimed the chain ran to completion -- it did not")
	}
	if event.Outcome != FactScopeExpandedPartial {
		t.Fatalf("outcome = %q, want expanded_partial", event.Outcome)
	}
	if !scope.HasDisclosableGap() {
		t.Fatal("the least-evidence case must be the loudest, not the quietest")
	}
	// The authorization drops stay telemetry-only (ruling invariant 9).
	if event.AuthorizationDroppedCount != 40 {
		t.Fatalf("AuthorizationDroppedCount = %d, want 40", event.AuthorizationDroppedCount)
	}
	result := scopeServableResult()
	applyFactScopeDisclosure(&result, &scope)
	for _, limitation := range result.Limitations {
		if strings.Contains(limitation, "40") || strings.Contains(strings.ToLower(limitation), "authoriz") {
			t.Fatalf("limitation %q leaks the authorization drop -- that is an existence side-channel", limitation)
		}
	}
}

// TestChaos4099_AMixedKindTraversalIsNotReportedAsComplete pins the ladder's
// other ordering bug. A chain returning one good repository and one wrong-kind
// work item used to be reported as a clean `expanded`: the bad candidate
// vanished with no gap, no disclosure and no degradation, and the only trace
// was a count nobody was alerted to. Whatever else the traversal got right,
// it produced something it could not use.
func TestChaos4099_AMixedKindTraversalIsNotReportedAsComplete(t *testing.T) {
	enableMetricsProjectPolicy(t, 0)

	expander := &recordingScopeExpander{
		targets: []SubjectRef{scopeRepo, subject(SubjectWorkItem, "work_item:linear:ABC-1")},
		counts:  FactScopeExpansionCounts{CandidateCount: 2},
	}
	scope := NewFactReadScopeResolver(expander).Resolve(
		context.Background(),
		newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent)),
		scopeCapabilities(),
	)
	event := scope.Events[0]
	if event.Outcome == FactScopeExpanded {
		t.Fatal("a traversal that produced an unusable target was reported as complete")
	}
	if event.Outcome != FactScopeTargetKindMismatch {
		t.Fatalf("outcome = %q, want target_kind_mismatch even though a valid target survived", event.Outcome)
	}
	// The valid target IS still admitted -- partial evidence beats none.
	if event.AdmittedCount != 1 || len(scope.DerivedSubjects[FactMetrics]) != 1 {
		t.Fatalf("event = %+v, derived = %+v, want the valid repository still admitted", event, scope.DerivedSubjects)
	}
	if !scope.HasDisclosableGap() {
		t.Fatal("a mixed-kind traversal must disclose")
	}
}

// TestChaos4099_TheExpandersOwnMismatchCountIsNotDoubleCounted pins the
// arithmetic contract between the two mismatch sources. An expander reports
// only targets it dropped ITSELF and never returns them; the resolver adds
// what its own defensive filter drops. Summing is correct exactly because
// the two are disjoint.
func TestChaos4099_TheExpandersOwnMismatchCountIsNotDoubleCounted(t *testing.T) {
	enableMetricsProjectPolicy(t, 0)

	expander := &recordingScopeExpander{
		// Two dropped by the expander (and NOT returned), one good target,
		// one wrong-kind target that slipped through to the resolver.
		targets: []SubjectRef{scopeRepo, subject(SubjectWorkItem, "work_item:linear:ABC-1")},
		counts:  FactScopeExpansionCounts{CandidateCount: 4, TargetKindMismatchCount: 2},
	}
	scope := NewFactReadScopeResolver(expander).Resolve(
		context.Background(),
		newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent)),
		scopeCapabilities(),
	)
	if got := scope.Events[0].TargetKindMismatchCount; got != 3 {
		t.Fatalf("TargetKindMismatchCount = %d, want 3 (2 dropped by the expander + 1 by the resolver's own filter)", got)
	}
}

// TestChaos4099_ExactlyTheCapIsNotTruncation is the boundary's other side.
// Reporting a scope that exactly filled its cap as partial would cry wolf on
// every well-sized traversal and train readers past the disclosure.
func TestChaos4099_ExactlyTheCapIsNotTruncation(t *testing.T) {
	const limit = 2
	enableMetricsProjectPolicy(t, limit)

	expander := &recordingScopeExpander{
		targets: []SubjectRef{
			subject(SubjectRepository, "repo:github:a"),
			subject(SubjectRepository, "repo:github:b"),
		},
		counts: FactScopeExpansionCounts{CandidateCount: limit},
	}
	scope := NewFactReadScopeResolver(expander).Resolve(
		context.Background(),
		newFactScopeResolveInput(scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: FactMetrics}}, TemporalCurrent)),
		scopeCapabilities(),
	)
	event := scope.Events[0]
	if event.Truncated {
		t.Fatal("exactly the cap is not truncation -- crying wolf here trains readers past the disclosure")
	}
	if event.Outcome != FactScopeExpanded {
		t.Fatalf("outcome = %q, want expanded", event.Outcome)
	}
	if scope.HasDisclosableGap() {
		t.Fatal("a complete traversal must not disclose a gap")
	}
	if event.AdmittedCount != limit {
		t.Fatalf("AdmittedCount = %d, want %d", event.AdmittedCount, limit)
	}
}

// ---------------------------------------------------------------------------
// Telemetry survives a failed read (codex round-2 finding 6)
// ---------------------------------------------------------------------------

// TestChaos4099_ScopeTelemetrySurvivesARejectedRequest pins that the two
// remaining early-return paths in ReadFacts carry the resolved scope out with
// their error.
//
// A fact read that resolved its scope and THEN failed is precisely the run an
// operator most needs the expansion decisions for. Returning a zero bundle
// there means the engine sees a nil Scope and emits nothing -- the signal
// disappears exactly when it matters.
func TestChaos4099_ScopeTelemetrySurvivesARejectedRequest(t *testing.T) {
	t.Parallel()

	registry, _ := scopeRegistry(t)
	// A disallowed parameter: rejected by the pre-pass that runs AFTER scope
	// resolution.
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeProject},
			[]FactRequirement{{Kind: FactMetrics, Parameters: map[string]string{"sql": "select *"}}}, TemporalCurrent))
	if err == nil {
		t.Fatal("a disallowed parameter must be rejected")
	}
	if bundle.Scope == nil {
		t.Fatal("the resolved scope was discarded with the error -- the engine emits nothing and the run is undiagnosable")
	}
	if len(bundle.Scope.Events) != 1 {
		t.Fatalf("events = %+v, want the expansion decision preserved", bundle.Scope.Events)
	}
}

// TestChaos4099_ScopeTelemetrySurvivesACancelledRead covers the second path.
func TestChaos4099_ScopeTelemetrySurvivesACancelledRead(t *testing.T) {
	t.Parallel()

	// metrics is answerable for the repository, so the read reaches a
	// provider; reviews is not, so a scope decision exists to preserve.
	blocking := &factProviderStub{
		capability: planCapability(FactMetrics, "metrics", SubjectRepository), wait: true,
	}
	reviews := &factProviderStub{
		capability: planCapability(FactReviews, "reviews", contractsv1.ContextFabricSubjectPullRequestReview),
		result:     FactProviderResult{State: SourceAvailable},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{blocking, reviews}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	bundle, err := registry.ReadFacts(ctx, storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{scopeRepo, scopeProject},
			[]FactRequirement{{Kind: FactMetrics}, {Kind: FactReviews, Subjects: []SubjectRef{scopeProject}}}, TemporalCurrent))
	if err == nil {
		t.Skip("the read completed before cancellation landed; the ordering under test did not occur")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if bundle.Scope == nil {
		t.Fatal("the resolved scope was discarded on cancellation")
	}
}

// ---------------------------------------------------------------------------
// The eligibility table's own admission rule
// ---------------------------------------------------------------------------

// TestChaos4099_EveryEligibilityRowCitesItsChain enforces the ruling's
// admission criterion: a row enters the table only when the traversal chain
// exists in prod code AND the row cites it.
//
// A row asserts that facts ARE reachable and that the pruner's proof of
// absence is therefore false. That assertion is only as good as the edges
// behind it, and an uncited row is someone's guess about reachability wearing
// the same clothes as a verified one -- which is how this table drifts back
// into the global widen option D was rejected in favour of.
func TestChaos4099_EveryEligibilityRowCitesItsChain(t *testing.T) {
	t.Parallel()

	for _, row := range factScopeEligibility {
		if strings.TrimSpace(row.Rule.Chain) == "" {
			t.Fatalf("%s has no chain citation -- no chain citation, no row", row.Requirement)
		}
		// A citation names edges and the producer that writes them, so it
		// can be checked against the code rather than taken on faith.
		if !strings.Contains(row.Rule.Chain, "->") || !strings.Contains(row.Rule.Chain, "devhealthsource/") {
			t.Fatalf("%s cites %q, want typed edges and the prod producer that writes them", row.Requirement, row.Rule.Chain)
		}
		// Codex round 3 (Low): the checks above are SYNTACTIC, so a citation
		// naming a producer that does not exist would pass them -- and a
		// fabricated chain is exactly the failure this test exists to
		// prevent, since a row's whole claim is that the pruner's proof of
		// absence is false. Resolve every cited `file: function` pair against
		// the tree and require the function to actually be declared there.
		for _, citation := range parseChainCitations(t, row.Rule.Chain) {
			source, err := os.ReadFile(citation.file)
			if err != nil {
				t.Fatalf("%s cites %q, which does not exist: %v", row.Requirement, citation.file, err)
			}
			for _, producer := range citation.producers {
				if !strings.Contains(string(source), "func "+producer+"(") {
					t.Fatalf("%s cites %s: %s, but %s declares no such function -- the chain is fabricated",
						row.Requirement, citation.file, producer, citation.file)
				}
			}
		}
		if len(row.Origins) == 0 {
			t.Fatalf("%s has no origin kinds", row.Requirement)
		}
		if row.Rule.TargetKind == "" {
			t.Fatalf("%s names no target kind", row.Requirement)
		}
		if row.Rule.Basis == "" {
			t.Fatalf("%s names no epistemic basis", row.Requirement)
		}
	}
}

// chainCitation is one `devhealthsource/<file>.go: fnA, fnB` clause lifted
// out of a chain string.
type chainCitation struct {
	file      string
	producers []string
}

// parseChainCitations pulls every parenthesised `file: producers` clause out
// of a chain citation and resolves the file against this package's directory.
// Chains are built by concatenating clauses with "; ", and each clause ends
// in "(<path>: <fn>[, <fn>...])".
func parseChainCitations(t *testing.T, chain string) []chainCitation {
	t.Helper()

	citations := []chainCitation{}
	for _, clause := range strings.Split(chain, "(")[1:] {
		body, _, found := strings.Cut(clause, ")")
		if !found {
			t.Fatalf("chain %q has an unterminated citation clause", chain)
		}
		path, functions, found := strings.Cut(body, ":")
		if !found {
			t.Fatalf("citation %q names no producer -- want `<file>: <function>`", body)
		}
		producers := []string{}
		for _, function := range strings.Split(functions, ",") {
			if trimmed := strings.TrimSpace(function); trimmed != "" {
				producers = append(producers, trimmed)
			}
		}
		if len(producers) == 0 {
			t.Fatalf("citation %q names no producer", body)
		}
		// Chains cite paths relative to the contextfabric package, which is
		// this test's own working directory.
		citations = append(citations, chainCitation{file: strings.TrimSpace(path), producers: producers})
	}
	if len(citations) == 0 {
		t.Fatalf("chain %q carries no checkable `(file: function)` citation", chain)
	}
	return citations
}

// TestChaos4099_OnlyTheThreeRuledPoliciesAreEverActivatable is the stage
// boundary, and the disclosure/activation split the ruling drew.
//
// Widening DISCLOSURE is honesty: SourcePruned asserts "proven nothing
// missing" and on a reachable chain that is false. Widening ACTIVATION is a
// product commitment about what a fact family MEANS for a subject that does
// not own it, and every one needs its own preconditions. A row that acquired
// a policy name without going through that is the failure this pins.
func TestChaos4099_OnlyTheThreeRuledPoliciesAreEverActivatable(t *testing.T) {
	t.Parallel()

	named := map[FactScopePolicy]struct{}{
		FactScopePolicyProjectWorkItemRepository:        {},
		FactScopePolicyProjectWorkItemPullRequest:       {},
		FactScopePolicyProjectWorkItemPullRequestReview: {},
	}
	for _, row := range factScopeEligibility {
		if row.Rule.Policy == FactScopePolicyNone {
			if row.Rule.Enabled {
				t.Fatalf("%s is enabled with no policy to name it", row.Requirement)
			}
			continue
		}
		if _, ok := named[row.Rule.Policy]; !ok {
			t.Fatalf("%s carries unratified policy %q -- activation scope is the 3 ruled policies only", row.Requirement, row.Rule.Policy)
		}
		// Stage 1: even the ratified three ship dark.
		if row.Rule.Enabled {
			t.Fatalf("%s ships enabled -- stage 1 activates nothing", row.Requirement)
		}
		// ...and only from a project origin.
		for _, origin := range row.Origins {
			if origin != SubjectProject {
				t.Fatalf("policy %q is declared for origin %q; the ruled activation scope is project only", row.Rule.Policy, origin)
			}
		}
	}
}

// TestChaos4099_TeamOriginNeverCarriesAPolicyName is CHAOS-4101's boundary.
// The team-attribution edge is an Ops-COMPUTED attribution
// (work_item_team_attributions, source enum spanning native_team through
// manual_fallback), so traversing it would launder an inference into fact
// scope. The gap is disclosed; the policy is CHAOS-4101's to name.
func TestChaos4099_TeamOriginNeverCarriesAPolicyName(t *testing.T) {
	t.Parallel()

	for _, row := range factScopeEligibility {
		for _, origin := range row.Origins {
			if origin == SubjectTeam && row.Rule.Policy != FactScopePolicyNone {
				t.Fatalf("%s names policy %q for a team origin -- that pre-empts CHAOS-4101's product ruling", row.Requirement, row.Rule.Policy)
			}
		}
	}
}

// TestChaos4099_ReachableWorkItemFamiliesNoLongerClaimAProofOfAbsence is the
// ruling's widened half, end to end.
//
// Work-item status is ONE typed hop from a project -- a shorter chain than
// the three named policies use -- so pruning it was the same defect this
// ticket exists to fix, on a more obviously reachable path.
func TestChaos4099_ReachableWorkItemFamiliesNoLongerClaimAProofOfAbsence(t *testing.T) {
	t.Parallel()

	families := []FactKind{
		FactStatus, FactWork, FactActualCompletion,
		FactBlockers, FactRequiredChildren, FactIdentity, FactMembership,
	}
	providers := make([]FactProvider, 0, len(families))
	requirements := make([]FactRequirement, 0, len(families))
	for _, kind := range families {
		providers = append(providers, &factProviderStub{
			capability: planCapability(kind, string(kind), SubjectWorkItem),
			result:     FactProviderResult{State: SourceAvailable},
		})
		requirements = append(requirements, FactRequirement{Kind: kind})
	}
	registry, err := NewFactCapabilityRegistry(providers, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	for _, origin := range []SubjectRef{scopeProject, scopeTeam} {
		bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
			scopeRequest([]SubjectRef{origin}, requirements, TemporalCurrent))
		if err != nil {
			t.Fatalf("origin %s: ReadFacts: %v", origin.Kind, err)
		}
		if len(bundle.Scope.Events) != len(families) {
			t.Fatalf("origin %s: events = %d, want one per family", origin.Kind, len(bundle.Scope.Events))
		}
		for _, observation := range bundle.Coverage.Sources {
			if observation.State == SourcePruned {
				t.Fatalf("origin %s: %s still claims a proof of absence one typed hop from its own work items",
					origin.Kind, observation.Source)
			}
		}
		if !bundle.Coverage.Partial {
			t.Fatalf("origin %s: Coverage.Partial = false over a reachable gap", origin.Kind)
		}
	}
}

// TestChaos4099_TeamScopedFamiliesFromAProjectStayPruned is the widening's
// upper bound, and the reason the table is a table rather than a rule.
//
// Reaching workload/investment/readiness from a project would run
// project <- work_item -OWNED_BY_TEAM-> team, through the same computed
// attribution CHAOS-4101 is holding back. Approaching it from the other
// direction does not make it stronger, so no chain is claimed and
// CHAOS-3783's proof of absence stands.
func TestChaos4099_TeamScopedFamiliesFromAProjectStayPruned(t *testing.T) {
	t.Parallel()

	for _, kind := range []FactKind{FactWorkload, FactInvestment, FactReadiness, FactOperationalDeficiencies} {
		provider := &factProviderStub{
			capability: planCapability(kind, string(kind), SubjectTeam),
			result:     FactProviderResult{State: SourceAvailable},
		}
		registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
		if err != nil {
			t.Fatalf("NewFactCapabilityRegistry: %v", err)
		}
		bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
			scopeRequest([]SubjectRef{scopeProject}, []FactRequirement{{Kind: kind}}, TemporalCurrent))
		if err != nil {
			t.Fatalf("%s: ReadFacts: %v", kind, err)
		}
		if got := bundle.Coverage.Sources[0].State; got != SourcePruned {
			t.Fatalf("%s state = %q, want pruned -- no chain to a team-scoped fact is claimed from a project", kind, got)
		}
		if bundle.Coverage.Partial {
			t.Fatalf("%s degraded the answer -- the widening must stop where the citations stop", kind)
		}
	}
}

// TestChaos4099_TheProxyDisclosureReachesTheAnswerThroughTheEngine is the
// production-wiring pin for the proxy half.
//
// The other proxy tests call applyFactScopeDisclosure directly, which proves
// the function is correct and proves NOTHING about whether Investigate calls
// it -- exactly the gap CHAOS-4085 shipped through. This one goes through
// Investigate, so deleting the engine's call fails here.
func TestChaos4099_TheProxyDisclosureReachesTheAnswerThroughTheEngine(t *testing.T) {
	enableMetricsProjectPolicy(t, 0)

	observed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	metrics := &factProviderStub{
		capability: planCapability(FactMetrics, "metrics", SubjectRepository),
		result: FactProviderResult{
			State: SourceAvailable, Version: "metrics-v1", ObservedAt: &observed,
			Facts: []CanonicalFact{{
				Kind: FactMetrics, Subject: scopeRepo,
				Fields:      map[string]FactValue{"lead_time_days": NumberFactValue(3.5)},
				ObservedAt:  &observed,
				SourceState: SourceAvailable,
			}},
		},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{
		ScopeExpander: &recordingScopeExpander{targets: []SubjectRef{scopeRepo}},
	})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry: %v", err)
	}
	telemetry := &recordingTelemetry{}
	engine, request := scopeEngineWithRegistry(t, telemetry, []SubjectRef{scopeProject}, registry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("a proxy-disclosed result must be servable: %v", err)
	}
	found := false
	for _, limitation := range result.Limitations {
		if limitation == contractsv1.ContextFabricFactScopeActivityProxyLimitation {
			found = true
		}
	}
	if !found {
		t.Fatalf("limitations = %v, want the activity-proxy disclosure to reach the ANSWER, not just the helper", result.Limitations)
	}
	if !result.Coverage.Partial {
		t.Fatal("Coverage.Partial = false on a proxy-derived answer")
	}
	// The committed subject is still the project: expansion granted a read
	// permission, never a second commit (ruling invariants 1 and 2).
	if len(result.SubjectResolution.Committed) != 1 || result.SubjectResolution.Committed[0] != scopeProject {
		t.Fatalf("Committed = %+v, want the project unchanged by a successful expansion", result.SubjectResolution.Committed)
	}
	// ...and the expansion was reported.
	if len(telemetry.factScopeExpansions) != 1 || telemetry.factScopeExpansions[0].Outcome != FactScopeExpanded {
		t.Fatalf("expansion events = %+v, want one expanded", telemetry.factScopeExpansions)
	}
	if telemetry.factScopeExpansions[0].Basis != FactScopeBasisActivityProxy {
		t.Fatalf("basis = %q, want activity_proxy", telemetry.factScopeExpansions[0].Basis)
	}
}
