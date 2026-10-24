package v1

// DeadlineDetailsKey is the error.details key of a 504 upstream_unavailable
// answer that names which deadline ended the investigation. The token is
// closed: a consumer that sees a value outside this vocabulary treats it as
// absent.
const DeadlineDetailsKey = "deadline"

// DeadlineModel is the token of a 504 caused by one model attempt reaching
// its own per-attempt timeout (ACR_CONTEXT_FABRIC_MODEL_TIMEOUT) while the
// request deadline had not passed.
const DeadlineModel = "model"

// DeadlineRequest is the token of a 504 caused by the request deadline
// (ACR_REQUEST_TIMEOUT, or the caller's shorter wait budget) passing.
const DeadlineRequest = "request"

// Deadlines lists every member of the closed vocabulary. The published
// schema's enum is held equal to it.
func Deadlines() []string { return []string{DeadlineModel, DeadlineRequest} }
