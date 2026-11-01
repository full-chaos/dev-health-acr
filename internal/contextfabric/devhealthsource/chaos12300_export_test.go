package devhealthsource

import "log/slog"

// SwapMembershipOutcomeLoggerForTest points the membership outcome line at l
// and returns the restore. Not safe beside a parallel test that projects work
// items; its callers do not call t.Parallel.
func SwapMembershipOutcomeLoggerForTest(l *slog.Logger) (restore func()) {
	previous := membershipOutcomeLogger
	membershipOutcomeLogger = func() *slog.Logger { return l }
	return func() { membershipOutcomeLogger = previous }
}
