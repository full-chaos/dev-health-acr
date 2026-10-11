package v1

// RateLimitDetailsLimitKey is the error.details key of a 429 rate_limited
// answer that names which limit refused the request. The token is closed: a
// consumer that sees a value outside this vocabulary treats it as absent.
const RateLimitDetailsLimitKey = "limit"

// RateLimitRequestBudget is the limit token of a 429 answered from the
// organization's request budget. The caller's credential was already decided;
// the budget, not an authentication attempt limit, refused the request.
const RateLimitRequestBudget = "request_budget"

// RateLimitCapabilitiesBudget is the limit token of a 429 answered from the
// capabilities handshake's own budget. Every MCP request resolves its caller
// through that handshake; the budget is separate from the context budget and
// from the credential-lifecycle budget. The credential was already decided.
const RateLimitCapabilitiesBudget = "capabilities_budget"

// IsRateLimitBudgetRefusal reports whether limit names a budget that refused
// an already-authenticated caller (not an authentication-attempt limit).
func IsRateLimitBudgetRefusal(limit string) bool {
	return limit == RateLimitRequestBudget || limit == RateLimitCapabilitiesBudget
}

// RateLimitLimits lists every member of the closed vocabulary. It is the one
// list IsRateLimitLimit and the published schema's enum are held equal to.
func RateLimitLimits() []string { return []string{RateLimitRequestBudget, RateLimitCapabilitiesBudget} }

// IsRateLimitLimit reports whether limit is a member of the closed vocabulary.
func IsRateLimitLimit(limit string) bool {
	for _, member := range RateLimitLimits() {
		if limit == member {
			return true
		}
	}
	return false
}
