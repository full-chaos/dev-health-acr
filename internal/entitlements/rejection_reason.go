package entitlements

import "errors"

const (
	reasonTransport    = "transport"
	reasonStatus       = "status"
	reasonSchema       = "schema"
	reasonUnknownField = "unknown_field"
	reasonDecode       = "decode"
	// The two inventory limits (ops CHAOS-12224, CHAOS-12267): a typed access
	// limitation, not an outage.
	reasonProjectScopeOverLimit    = "project_scope_over_limit"
	reasonRepositoryScopeOverLimit = "repository_scope_over_limit"
)

var errUnknownField = errors.New("unknown response field")

type rejection struct{ reason string }

func (r *rejection) Error() string { return errUnavailable.Error() }

func (r *rejection) Is(target error) bool { return target == errUnavailable }

func rejected(reason string) error { return &rejection{reason: reason} }

// RejectionReason names why a lookup was rejected, or "" when err is not a rejection.
func RejectionReason(err error) string {
	var r *rejection
	if errors.As(err, &r) {
		return r.reason
	}
	return ""
}

// AccessScopeOverLimit reports whether err is the access read refusing an
// inventory above its limit. It is still a rejection (it grants nothing), but
// a retry cannot clear it, so it is a limitation of the organization's access,
// not an outage. RejectionReason names which inventory.
func AccessScopeOverLimit(err error) bool {
	switch RejectionReason(err) {
	case reasonProjectScopeOverLimit, reasonRepositoryScopeOverLimit:
		return true
	}
	return false
}
