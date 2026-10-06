package v1

// InvalidRequestReasons is the closed vocabulary of error.details.reason
// values the hosted API sends with an invalid_request (CHAOS-7167). It is
// declared once here; the sidecar surfaces a reason to an MCP client only
// when it is a member, and a drift test in internal/api pins every
// constant the routes emit against this list. A value outside it is never
// echoed: hosted text could carry a subject, id or name.
var invalidRequestReasons = map[string]struct{}{
	"scope_required":          {}, // find_subjects handle mode, repository-restricted credential (CHAOS-7160)
	"invalid_find_request":    {}, // find_subjects request rejected
	"malformed_request":       {}, // direct data request body/shape
	"unknown_section":         {}, // data_catalog section
	"invalid_request":         {}, // read_facts / read_relationships request shape
	"invalid_cursor":          {}, // read_relationships cursor
	"expired_cursor":          {}, // read_relationships cursor
	"denied_or_not_found":     {}, // read_facts / read_relationships subject
	"malformed_json":          {}, // body is not valid JSON
	"schema_violation":        {}, // body decoded but a field has the wrong type or fails validation
	"unknown_field":           {}, // body names a field the request does not declare
	"trailing_json":           {}, // body holds more than one JSON value
	"body_too_large":          {}, // body exceeds the request byte limit
	"invalid_idempotency_key": {}, // Idempotency-Key header missing, duplicated, out of bounds or not equal to the body key

	ContextFabricSuppliedSynthesisReasonInputChanged:           {}, // supplied synthesis written from another input
	ContextFabricSuppliedSynthesisReasonInterpretationRequired: {}, // supplied synthesis without a supplied interpretation
}

// IsInvalidRequestReason reports whether reason is a member of the closed
// invalid_request reason vocabulary.
func IsInvalidRequestReason(reason string) bool {
	_, ok := invalidRequestReasons[reason]
	return ok
}
