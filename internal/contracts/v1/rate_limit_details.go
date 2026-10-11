package v1

// RateLimitDetailsLimitKey is the error.details key of a 429 rate_limited
// answer that names which limit refused the request. The token is closed: a
// consumer that sees a value outside this vocabulary treats it as absent.
const RateLimitDetailsLimitKey = "limit"

// RateLimitRequestBudget is the limit token of a 429 answered from the
// organization's request budget. The caller's credential was already decided;
// the budget, not an authentication attempt limit, refused the request.
const RateLimitRequestBudget = "request_budget"

// IsRateLimitLimit reports whether limit is a member of the closed vocabulary.
func IsRateLimitLimit(limit string) bool { return limit == RateLimitRequestBudget }
