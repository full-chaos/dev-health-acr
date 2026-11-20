package contextfabric

import "errors"

// DependencyCause returns the driver cause class a dependency adapter attached
// to err (falkorgraph: eof, connection_reset, connection_refused, broken_pipe,
// connection_aborted, loading, timeout, pool_timeout, other). ok is false when
// no adapter attached one. The value is a fixed token, never error text.
func DependencyCause(err error) (cause string, ok bool) {
	var carrier interface{ DependencyCause() string }
	if errors.As(err, &carrier) {
		return carrier.DependencyCause(), true
	}
	return "", false
}
