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
	"log/slog"
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
// Restated here rather than imported so this package's tests do not depend
// on devhealthfacts; the values are pinned against the real ones by
// TestChaos4099_ProviderSubjectKindsAreUnchanged.
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
	for _, event := range bundle.Scope.Events {
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

	for _, outcome := range []FactScopeExpansionOutcome{
		FactScopeNotNeeded, FactScopePolicyUnavailable, FactScopeAttemptedEmpty,
		FactScopeTargetKindMismatch, FactScopeExpanded, FactScopeExpandedPartial, FactScopeFailed,
	} {
		state := factScopeGapSourceState(outcome)
		if state == SourcePruned {
			t.Fatalf("outcome %q maps to SourcePruned -- that vocabulary asserts a proof this one does not hold", outcome)
		}
		if !validFactSourceState(state) && state != SourcePruned {
			t.Fatalf("outcome %q maps to %q, which no provider may return either", outcome, state)
		}
	}
	// And the degrading split is exactly the outcomes that mean "the system
	// did not, or could not, look".
	for outcome, wantDegrades := range map[FactScopeExpansionOutcome]bool{
		FactScopePolicyUnavailable:  true,
		FactScopeExpandedPartial:    true,
		FactScopeFailed:             true,
		FactScopeAttemptedEmpty:     false,
		FactScopeTargetKindMismatch: false,
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

// TestChaos4099_ProviderSubjectKindsAreUnchanged is acceptance point 3, and
// it is the reason option A was rejected: widening each capability's
// SupportedSubjectKinds would have put the same join in N places with no
// central policy. The declared kinds are the planner's ONE signal, and this
// ticket does not touch them.
func TestChaos4099_ProviderSubjectKindsAreUnchanged(t *testing.T) {
	t.Parallel()

	for kind, capability := range scopeCapabilities() {
		if supportsSubjectKind(capability.SupportedSubjectKinds, SubjectProject) {
			t.Fatalf("%s now declares project support -- scope expansion must not reach into provider capabilities", kind)
		}
		if supportsSubjectKind(capability.SupportedSubjectKinds, SubjectTeam) {
			t.Fatalf("%s now declares team support -- same reason", kind)
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
	interpretation := InterpretedQuestion{
		Shape: ShapeSingleSubject, RequestedJudgment: "release_readiness_and_drivers",
		TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: caseSixtyRequirements(),
	}
	// The REAL registry, over real FactProvider implementations, so the
	// planner, the scope resolver, the coverage merge and the bundle all run
	// for real. A CanonicalFactReader double here would let the whole
	// mechanism under test be skipped.
	registry, _ := scopeRegistry(t)
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
		FactScopeAttemptedEmpty:     false,
		FactScopeTargetKindMismatch: false,
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
	targets  []SubjectRef
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
	return FactScopeExpansionResult{Targets: e.targets, Counts: e.counts}, nil
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
