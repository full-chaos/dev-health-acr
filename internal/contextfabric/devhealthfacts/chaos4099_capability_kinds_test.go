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
func TestChaos4099_RealProviderSubjectKindsStayUnwidened(t *testing.T) {
	t.Parallel()

	// The three families CHAOS-4099 routes through the scope resolver, with
	// the subject kind each is expected to answer for and nothing else.
	want := map[contextfabric.FactKind][]contextfabric.SubjectKind{
		contextfabric.FactMetrics:      {contextfabric.SubjectRepository},
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
// If this ever fails, CHAOS-4099's defect is either fixed by another route or
// its analysis was wrong -- and either way the scope resolver's disclosure
// would be claiming a gap that no longer exists. Better to fail here, loudly,
// than to disclose a limitation that is not true.
func TestChaos4099_NoCanonicalFactCapabilityAnswersForAProject(t *testing.T) {
	t.Parallel()

	for _, provider := range allProvidersForKindAudit() {
		capability := provider.Capability()
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
	}
}
