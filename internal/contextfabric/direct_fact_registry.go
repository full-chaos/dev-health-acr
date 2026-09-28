package contextfabric

// WithoutScopeExpansion returns a registry over the SAME providers and
// declarations whose scope resolver has NO expander (CHAOS-7073).
//
// The direct read tools use it. ReadFacts can add derived subjects through
// the scope expander (a project expands to repositories, pull requests,
// work items). The expander checks each derived subject against the grant,
// but no test proves that on the direct path, and the design of record
// (CHAOS-7036 C.5) rules: "prove by test that each derived subject is
// authorized, or switch the expander off". Off: every expansion policy then
// fails closed to policy_unavailable (reason expander_unwired) and the gap is
// disclosed in coverage, never read.
//
// The policy table is kept (not emptied): an empty table would turn a
// disclosed gap into a silent prune. The engine's registry is not changed.
func (r *FactCapabilityRegistry) WithoutScopeExpansion() *FactCapabilityRegistry {
	if r == nil {
		return nil
	}
	direct := *r
	var policies map[FactKind]map[SubjectKind]factScopePolicyRule
	if r.scopeResolver != nil {
		policies = r.scopeResolver.policies
	}
	direct.scopeResolver = NewFactReadScopeResolverWithPolicies(nil, policies)
	return &direct
}
