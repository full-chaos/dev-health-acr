package devhealthfacts

// CHAOS-4099 acceptance point 3, pinned against the REAL providers.
//
// WHY IT LIVES HERE. The equivalent guard in internal/contextfabric can only
// assert against capabilities its own tests restate, which makes it a
// tautology: it proves the restatement is consistent with itself, not that
// production still declares what the ticket assumed. devhealthfacts is where
// the real declarations are, and it already imports contextfabric, so this is
// the only package that can compare them.
//
// WHAT IT PROTECTS. Option A -- widening each capability's
// SupportedSubjectKinds and inlining the joins -- was explicitly REJECTED in
// the ruling: it puts the same traversal in N places with no central policy
// and no disclosure. FactReadScopeResolver exists so that widening is never
// necessary. A provider that quietly gains project or team support would
// reintroduce option A one file at a time, and would do it invisibly: the
// scope resolver would simply stop seeing a gap, the disclosure would stop
// firing, and the facts would arrive with no statement about the proxy they
// came through.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestChaos4099_RealProviderSubjectKindsStayUnwidened is acceptance point 3.
//
// CHAOS-4347 (ruling 2026-08-26 17:52) deliberately widens FactMetrics'
// pin below to include SubjectTeam and SubjectProject. That is NOT the
// rejected "Option A" this test exists to catch: Option A was widening a
// capability's SupportedSubjectKinds while inlining project/team->repository
// PROXY joins per-provider, with no central policy and no disclosure --
// exactly what FactReadScopeResolver was built to replace. MetricsProvider's
// widening is a REAL table join instead (team_metrics_daily directly for
// team; team_project_ownership -> team_metrics_daily, summed and disclosed
// via rollup_basis/team_breakdown, for project) -- see metrics.go's package
// doc comment. FactPullRequests and FactReviews have no such source and
// stay exactly as CHAOS-4099 left them, so this test keeps pinning them.
func TestChaos4099_RealProviderSubjectKindsStayUnwidened(t *testing.T) {
	t.Parallel()

	// The families CHAOS-4099 routes through the scope resolver, with the
	// subject kind each is expected to answer for and nothing else.
	// FactMetrics (CHAOS-4347) is the deliberate exception -- see the test's
	// own doc comment above.
	want := map[contextfabric.FactKind][]contextfabric.SubjectKind{
		contextfabric.FactMetrics:      {contextfabric.SubjectRepository, contextfabric.SubjectTeam, contextfabric.SubjectProject},
		contextfabric.FactPullRequests: {contextfabric.SubjectPullRequest},
		contextfabric.FactReviews:      {contractsv1.ContextFabricSubjectPullRequestReview},
	}
	got := map[contextfabric.FactKind][]contextfabric.SubjectKind{
		contextfabric.FactMetrics:      (&MetricsProvider{}).Capability().SupportedSubjectKinds,
		contextfabric.FactPullRequests: (&PullRequestsProvider{}).Capability().SupportedSubjectKinds,
		contextfabric.FactReviews:      (&ReviewsProvider{}).Capability().SupportedSubjectKinds,
	}
	for kind, expected := range want {
		actual := got[kind]
		if len(actual) != len(expected) {
			t.Fatalf("%s declares %v, want exactly %v -- widening a capability is the REJECTED option A", kind, actual, expected)
		}
		for i := range expected {
			if actual[i] != expected[i] {
				t.Fatalf("%s declares %v, want exactly %v", kind, actual, expected)
			}
		}
	}
}

// TestChaos4099_NoCanonicalFactCapabilityAnswersForAProject is the premise
// the whole ticket rests on, asserted rather than assumed.
//
// If this ever fails for a capability OTHER than FactMetrics, CHAOS-4099's
// defect is either fixed by another route or its analysis was wrong -- and
// either way the scope resolver's disclosure would be claiming a gap that
// no longer exists. Better to fail here, loudly, than to disclose a
// limitation that is not true.
//
// FactMetrics is excluded (CHAOS-4347, ruling 2026-08-26 17:52): it now
// answers for a project directly, by a real team_project_ownership join,
// not by the proxy route CHAOS-4099's disclosure describes -- see
// metrics.go's package doc comment and
// TestChaos4099_RealProviderSubjectKindsStayUnwidened's updated doc
// comment above. Every OTHER capability's premise is unchanged and this
// test still proves it.
//
// FactHealth, FactWorkload, FactInvestment, and FactReadiness are ALSO
// excluded (CHAOS-4363): each now answers for a project directly, by the
// same real-join pattern (team_project_ownership, and for FactHealth also
// team_repo_ownership one hop further) -- see health.go/workload.go/
// investment.go/readiness.go's own package doc comments. This is still not
// the rejected Option A: every join is a genuine ownership traversal with a
// disclosed rollup_basis, never an inlined proxy with no central policy.
//
// FactFlow and FactLandscape are ALSO excluded (CHAOS-4364, same reasoning):
// flow.go/landscape.go both roll up to project via a real
// team_project_ownership join.
func TestChaos4099_NoCanonicalFactCapabilityAnswersForAProject(t *testing.T) {
	t.Parallel()

	realProjectJoinKinds := map[contextfabric.FactKind]bool{
		contextfabric.FactMetrics:    true,
		contextfabric.FactHealth:     true,
		contextfabric.FactWorkload:   true,
		contextfabric.FactInvestment: true,
		contextfabric.FactReadiness:  true,
		contextfabric.FactFlow:       true,
		contextfabric.FactLandscape:  true,
	}
	for _, provider := range allProvidersForKindAudit() {
		capability := provider.Capability()
		if realProjectJoinKinds[capability.Kind] {
			continue
		}
		for _, kind := range capability.SupportedSubjectKinds {
			if kind == contextfabric.SubjectProject {
				t.Fatalf("capability %s now answers for a project directly -- CHAOS-4099's premise, and its disclosure, no longer hold", capability.Kind)
			}
		}
	}
}

// allProvidersForKindAudit lists every canonical-fact provider by
// construction. Listed explicitly rather than discovered so that ADDING a
// provider is a deliberate act that shows up in this file's diff -- a new
// provider that answered for a project is exactly what the test above exists
// to catch, and a discovery mechanism that silently skipped it would defeat
// the purpose.
func allProvidersForKindAudit() []contextfabric.FactProvider {
	return []contextfabric.FactProvider{
		&MetricsProvider{}, &PullRequestsProvider{}, &ReviewsProvider{},
		&HealthProvider{}, &WorkloadProvider{}, &InvestmentProvider{},
		&ReadinessProvider{}, &OperationalDeficienciesProvider{},
		&IdentityProvider{}, &MembershipProvider{},
		&StatusProvider{}, &WorkProvider{}, &ActualCompletionProvider{},
		&BlockersProvider{}, &RequiredChildrenProvider{},
		&IncidentsProvider{}, &DeploymentsProvider{},
		&ContinuousIntegrationProvider{}, &SourceHealthProvider{},
		// CHAOS-4364 (codex R2 P2): FlowProvider/LandscapeProvider were
		// missing here, so TestChaos4099_NoCanonicalFactCapabilityAnswersForAProject
		// never actually exercised either -- the exact silent-gap this
		// list's own doc comment exists to prevent.
		&FlowProvider{}, &LandscapeProvider{},
	}
}
