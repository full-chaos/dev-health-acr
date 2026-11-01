package v1

// The per-call access read's refusals (CHAOS-12092, Ask Dev direct-browser
// design §3.1-3.2): every protected call by a user-class credential (browser,
// OAuth user, native device) re-reads, live, the user's membership, session,
// impersonation, flags and repository inventory.
const (
	// ErrorCodeAccessDenied: 403, the user is not a member of the
	// organization, is impersonating, the browser credential's session is not
	// live, or Dev Health does not know the organization or the user. One
	// code for every reason, so the answer says nothing about which exists.
	ErrorCodeAccessDenied = "access_denied"
	// ErrorCodeNoRepository: 409, the effective repository scope (the
	// credential's grant within the organization's live inventory) is empty.
	// A typed deny-all: the call is refused before any handler.
	ErrorCodeNoRepository = "no_repository"
	// ErrorCodeBudgetExhausted: 503, retryable, an investigation create whose
	// wait budget left after the access read is below
	// ContextFabricWaitBudgetMinSeconds. Refused before admission: no
	// submission row and no model attempt.
	ErrorCodeBudgetExhausted = "budget_exhausted"

	ContextFabricAccessDeniedMessage    = "Access to this organization is not available for this credential"
	ContextFabricNoRepositoryMessage    = "No repository is in scope for this credential"
	ContextFabricBudgetExhaustedMessage = "The request budget left is below the minimum for an investigation"
	// ContextFabricAccessInventoryOverLimitMessage is the 503 of an inventory
	// above its limit (CHAOS-12301): upstream_unavailable, not retryable. Dev
	// Health refuses such an inventory rather than truncate it, and the reason
	// is in the log line and the audit event, never on the wire.
	ContextFabricAccessInventoryOverLimitMessage = "This organization's inventory is above the supported access limit"
)
