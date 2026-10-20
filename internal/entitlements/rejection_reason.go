package entitlements

import "errors"

const (
	reasonTransport    = "transport"
	reasonStatus       = "status"
	reasonSchema       = "schema"
	reasonUnknownField = "unknown_field"
	reasonDecode       = "decode"
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
