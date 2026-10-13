package contextfabric

// WithoutScopeExpansion returns a registry over the SAME providers and
// declarations whose scope resolver has NO expander: every expansion policy
// fails closed to policy_unavailable (reason expander_unwired) and the gap is
// disclosed in coverage, never read. The policy table is kept (not emptied):
// an empty table would turn a disclosed gap into a silent prune.
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

// DirectReadFactSource is the fact source the direct read tools read
// through: this registry itself, scope expander included. Design C.5 lets the
// direct path expand when a test proves each derived subject is authorized:
// the expander authorizes every hop against the caller's principal, and the
// direct reader serves a derived fact only when its subject is a target the
// resolver derived from a gate-admitted root (directread.guardDerivedFacts).
// Sharing the registry shares its policies, per-policy limits and work-item
// admission slots with the investigate path.
func (r *FactCapabilityRegistry) DirectReadFactSource() CanonicalFactReader {
	if r == nil {
		return nil
	}
	return r
}

// DirectReadFactSourcer is implemented by a fact reader that can hand the
// direct path its fact source.
type DirectReadFactSourcer interface {
	DirectReadFactSource() CanonicalFactReader
}
