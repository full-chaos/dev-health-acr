package contextfabric

// CHAOS-6182: the backend-neutral shape an organization-discovery adapter
// answers with. It lives here, beside every other port model, because both
// the scheduler (projectionrun) and the ClickHouse adapter
// (devhealthsource) need it and neither may import the other.
//
// A discovery answer is deliberately NOT just a list of ids. An adapter
// that silently drops an organization it judged ineligible is
// indistinguishable from one that never saw it, and "why is this tenant not
// being projected" is the question an operator will actually ask. Every
// exclusion is therefore returned, with a closed reason, and the caller
// publishes it.

// OrgSkipReason is the closed vocabulary of why a discovered organization
// was not admitted to the projected set.
type OrgSkipReason string

const (
	// OrgSkipReasonNoRepo: the organization appears in canonical data
	// (work items, pull requests) but owns no repository row. Repository
	// ownership is how this platform scopes a graph at all, so there is
	// nothing to build a graph around yet.
	OrgSkipReasonNoRepo OrgSkipReason = "no_repo"
	// OrgSkipReasonInactive: the organization owns repositories, but
	// nothing in its canonical data has moved inside the configured
	// activity window. This is what keeps a long-abandoned or throwaway
	// tenant from being given a graph, and it is REVERSIBLE by
	// construction -- one new row inside the window makes it eligible on
	// the next tick, with no operator action.
	OrgSkipReasonInactive OrgSkipReason = "inactive"
	// OrgSkipReasonDenied: the organization is named in the deployment's
	// discovery deny list. An operator decision, never a data one.
	OrgSkipReasonDenied OrgSkipReason = "denied"
)

// OrgSkipReasonVocabulary is the ONE list of admissible OrgSkipReason
// values. Exported so a certifying test reads the producer's own
// vocabulary rather than keeping a second copy that can drift from it.
func OrgSkipReasonVocabulary() [3]OrgSkipReason {
	return [3]OrgSkipReason{OrgSkipReasonNoRepo, OrgSkipReasonInactive, OrgSkipReasonDenied}
}

// SkippedOrg names one organization discovery saw and did not admit.
type SkippedOrg struct {
	OrgID  string
	Reason OrgSkipReason
}

// OrgDiscoveryResult is one enumeration's whole answer: the organizations
// to project, and every organization seen but not admitted.
type OrgDiscoveryResult struct {
	// OrgIDs are the eligible organizations, in the adapter's own order.
	// The caller trims, de-duplicates and sorts, so an adapter need not.
	OrgIDs []string
	// Skipped is every organization this enumeration saw and excluded.
	// Empty is a real answer ("everything seen was eligible"), distinct
	// from an adapter that does not report exclusions -- which is why the
	// admitted and excluded sets are returned together rather than the
	// caller being left to infer the difference.
	Skipped []SkippedOrg
}
