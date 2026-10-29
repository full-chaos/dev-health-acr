package contextfabric

import (
	"context"
	"fmt"
	"time"
)

// Shared Ask Dev admission limits (CHAOS-11914, plan §2.9, INV-5). Each limit
// names its scope and what it counts:
//
//   - running runs per principal: SHARED across replicas; counts the
//     principal's submission records in state running whose run is still
//     inside its budget (expires_at in the future) and whose reserved result
//     is not stored.
//   - running runs per organization: SHARED, the same count over the
//     organization.
//   - monthly platform-model runs: SHARED; a content-free aggregate per
//     (organization, calendar month UTC) that counts every ADMITTED run on
//     the deployment's own model. A run on the organization's own model counts
//     to the two concurrency limits only. Written in the admission
//     transaction; nothing decrements it, and deleting submission records
//     or results leaves it unchanged. Only the organization purge deletes
//     an organization's rows (orgpurge purgeTables).
//
// What is never counted: a repeat of an admitted key (a replay, an
// in-progress answer, any same-digest or mismatched repeat), a status read,
// a request without X-ACR-Submission-ID, and a refused admission.
//
// The request rate limit (limits.Manager) is unchanged and PER REPLICA: with
// N replicas the effective ceiling is N times the configured one.

// Default and maximum limits (plan §2.9; operator maximum 5,000 runs a month).
const (
	DefaultMaxRunningPerPrincipal      = 2
	DefaultMaxRunningPerOrg            = 4
	DefaultMonthlyPlatformRunAllowance = 1000
	MaxMonthlyPlatformRunAllowance     = 5000
	MaxRunningPerPrincipalLimit        = 16
	MaxRunningPerOrgLimit              = 64
)

// SubmissionLimits are the shared admission limits. A zero field is the
// default, never "unlimited": a composition that forgets a limit gets the
// plan's default.
type SubmissionLimits struct {
	MaxRunningPerPrincipal      int
	MaxRunningPerOrg            int
	MonthlyPlatformRunAllowance int64
}

// Effective is the limits with every zero field set to its default.
func (l SubmissionLimits) Effective() SubmissionLimits {
	if l.MaxRunningPerPrincipal <= 0 {
		l.MaxRunningPerPrincipal = DefaultMaxRunningPerPrincipal
	}
	if l.MaxRunningPerOrg <= 0 {
		l.MaxRunningPerOrg = DefaultMaxRunningPerOrg
	}
	if l.MonthlyPlatformRunAllowance <= 0 {
		l.MonthlyPlatformRunAllowance = DefaultMonthlyPlatformRunAllowance
	}
	return l
}

// Validate refuses limits outside the operator bounds.
func (l SubmissionLimits) Validate() error {
	e := l.Effective()
	switch {
	case e.MaxRunningPerPrincipal > MaxRunningPerPrincipalLimit:
		return fmt.Errorf("Ask Dev running runs per principal must be between 1 and %d", MaxRunningPerPrincipalLimit)
	case e.MaxRunningPerOrg > MaxRunningPerOrgLimit:
		return fmt.Errorf("Ask Dev running runs per organization must be between 1 and %d", MaxRunningPerOrgLimit)
	case e.MaxRunningPerOrg < e.MaxRunningPerPrincipal:
		return fmt.Errorf("Ask Dev running runs per organization must be at least the per-principal limit")
	case e.MonthlyPlatformRunAllowance > MaxMonthlyPlatformRunAllowance:
		return fmt.Errorf("Ask Dev monthly platform run allowance must be between 1 and %d", MaxMonthlyPlatformRunAllowance)
	}
	return nil
}

// SubmissionRefusal is the closed reason an admission was refused.
type SubmissionRefusal string

const (
	SubmissionRefusedPrincipalConcurrency SubmissionRefusal = "principal"
	SubmissionRefusedOrgConcurrency       SubmissionRefusal = "org"
	SubmissionRefusedAllowanceExhausted   SubmissionRefusal = "allowance_exhausted"
)

// SubmissionRefusalVocabulary is the closed set of refusals.
func SubmissionRefusalVocabulary() [3]SubmissionRefusal {
	return [3]SubmissionRefusal{SubmissionRefusedPrincipalConcurrency, SubmissionRefusedOrgConcurrency, SubmissionRefusedAllowanceExhausted}
}

// SubmissionRefusedError is an admission the shared limits refused. Nothing
// was written: no record, no aggregate increment.
type SubmissionRefusedError struct {
	Refusal SubmissionRefusal
	// Count is what was counted against Limit when it was refused: running
	// runs for a concurrency refusal, the month's platform runs for the
	// allowance.
	Count int64
	Limit int64
	// RetryAfter is when a retry can be admitted: a short poll for
	// concurrency, the start of the next UTC month for the allowance.
	RetryAfter time.Duration
}

func (e *SubmissionRefusedError) Error() string {
	return fmt.Sprintf("context fabric: submission refused (%s: %d of %d)", e.Refusal, e.Count, e.Limit)
}

// SubmissionConcurrencyRetryAfter is the Retry-After of a concurrency
// refusal: the status poll cadence.
const SubmissionConcurrencyRetryAfter = 3 * time.Second

// UsageMonth is the calendar month (UTC) an admission at now counts in.
func UsageMonth(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// AllowanceRetryAfter is the time from now to the start of the next month.
func AllowanceRetryAfter(now time.Time) time.Duration {
	return UsageMonth(now).AddDate(0, 1, 0).Sub(now.UTC())
}

// UsageMonthly is one organization's content-free usage aggregate for one
// month: counts only, no question, answer, principal or result id.
type UsageMonthly struct {
	OrgID        string
	Month        time.Time
	PlatformRuns int64
	// CostMicroUSD is nil: ACR records token counts per model call
	// (ModelExecutionReceipt.Usage) but no price, so it meters no cost.
	CostMicroUSD *int64
}

// UsageReader reads the aggregate (tests and operators). found is false when
// the organization admitted no platform run that month.
type UsageReader interface {
	ReadUsageMonthly(ctx context.Context, orgID string, month time.Time) (usage UsageMonthly, found bool, err error)
}
