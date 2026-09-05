package contextfabric

import (
	"context"
	"errors"
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The MEASURED ATTEMPT: one object carrying everything known about one
// assembled result -- the grants it was written against, the occurrence-level
// ledger of what it actually charged, the serialized measurement, the capacity
// verdict and the per-group quota exposure -- built by ONE constructor on EVERY
// terminal arm.
//
// WHY ONE OBJECT, and why it is built even when the answer FITS. Three review
// rounds found the same class from three directions: a quota written at several
// sites and read at none; a quota dropped on both refusal arms; a quota read
// from an empty value on the arm where the retry succeeded. The common cause is
// not carelessness at any one of those sites. It is that the exposure lived on
// an OPTIONAL narrowing attempt, so the arms that did not narrow had nothing to
// carry, and the majority arm -- the answer that fits -- never computed one at
// all. The number meant to PREVENT an overrun was only produced after one had
// already happened.
//
// So the measurement is not a side effect of narrowing. Every arm that decides
// anything about an assembled result builds one of these first, and the event
// constructor takes the whole object rather than three scalars a branch could
// forget to set. An uninitialised measurement is INVALID by construction:
// Availability's zero value is not a member of its own vocabulary.

// ItemQuotaAvailability is the CLOSED vocabulary of what a quota exposure MEANS
// on this attempt.
//
// It exists because zero is four different statements and this seam has been
// bitten by all of them: "the quota is zero and every item breaches it", "there
// is no ceiling at all", "there is a ceiling but no group axis to report", and
// "the account did not reconcile, so no quota statement is possible". A reader
// given a bare 0 cannot act on any of them.
type ItemQuotaAvailability string

const (
	// ItemQuotaUnset is the ZERO VALUE and is NOT a member of the
	// vocabulary. An attempt carrying it was never measured, and every
	// consumer refuses it rather than emitting three zeros that read as a
	// measurement.
	ItemQuotaUnset ItemQuotaAvailability = ""
	// ItemQuotaBounded: a positive per-group allowance is in force and was
	// measured against the served document.
	ItemQuotaBounded ItemQuotaAvailability = "bounded"
	// ItemQuotaBoundedZero: a group axis exists and the allowance is ZERO,
	// so every group-naming item breaches it. A real, measured quota --
	// round 2 found the exposure skipping exactly this case as though it
	// were absent.
	ItemQuotaBoundedZero ItemQuotaAvailability = "bounded_zero"
	// ItemQuotaUnbounded: no item ceiling is in force, so there is no quota
	// to report. Distinct from a quota of zero, which forbids everything.
	ItemQuotaUnbounded ItemQuotaAvailability = "unbounded"
	// ItemQuotaUnavailable: a ceiling is in force but the answer has no
	// group axis, so a per-group quota does not exist for it. The answer is
	// still bounded globally.
	ItemQuotaUnavailable ItemQuotaAvailability = "unavailable"
	// ItemQuotaAccountingDisagreement: the ledger did not reconcile with the
	// result, so no quota statement can be made about it at all. The
	// consequence is a typed accounting error, never a budget refusal.
	ItemQuotaAccountingDisagreement ItemQuotaAvailability = "accounting_disagreement"
)

var itemQuotaAvailabilities = [5]ItemQuotaAvailability{
	ItemQuotaBounded,
	ItemQuotaBoundedZero,
	ItemQuotaUnbounded,
	ItemQuotaUnavailable,
	ItemQuotaAccountingDisagreement,
}

// ItemQuotaAvailabilityCount is the closed vocabulary's size. The unset zero
// value is deliberately NOT counted: it is the absence of a measurement, not a
// kind of one.
const ItemQuotaAvailabilityCount = len(itemQuotaAvailabilities)

// ItemQuotaAvailabilityVocabulary returns it in published order.
func ItemQuotaAvailabilityVocabulary() [ItemQuotaAvailabilityCount]ItemQuotaAvailability {
	return itemQuotaAvailabilities
}

// ValidItemQuotaAvailability reports membership. The unset value is not a
// member, which is what makes an unmeasured attempt refusable.
func ValidItemQuotaAvailability(value ItemQuotaAvailability) bool {
	for _, member := range itemQuotaAvailabilities {
		if member == value {
			return true
		}
	}
	return false
}

// MeasuredAttempt is one measured assembled result.
type MeasuredAttempt struct {
	// Allocation is the grant set this attempt was written against. Carried
	// so a consumer cannot pair a measurement with a quota from a different
	// revision of the plan -- "selecting the wrong attempt" is the residual
	// this binding narrows.
	Allocation ItemAllocation
	// Ledger is the occurrence-level account of the document measured.
	Ledger contractsv1.ContextFabricItemLedger
	// Measurement is the item counts and serialized size of that same
	// document, from the same call.
	Measurement ResponseMeasurement
	// Overrun names which ceiling axis this document exceeded, from the
	// shipped vocabulary.
	Overrun contractsv1.ContextFabricBudgetOverrun
	// Capacity is the ledger-backed verdict: a fit here is CERTIFIED, and a
	// document whose account did not reconcile gets no capacity statement.
	Capacity contractsv1.ContextFabricCapacityVerdict
	// Availability says what the quota numbers below MEAN.
	Availability ItemQuotaAvailability
	// GroupAllowance is the published per-group allowance, from the
	// allocator's own method.
	GroupAllowance int
	// GroupsGranted is how many groups that allowance was granted across.
	GroupsGranted int
	// GroupsMeasured is how many groups the MEASURED DOCUMENT declares.
	//
	// It is separate from GroupsGranted on purpose: a retry narrows the
	// cohort, so an attempt whose measured group count differs from the
	// granted one is measuring a document the grants were not written for.
	// Publishing one number for both would hide exactly that.
	GroupsMeasured int
	// GroupsOverAllowance is how many measured groups used more than the
	// allowance. Zero is a real answer ("every group fitted") whenever
	// Availability is bounded or bounded_zero.
	GroupsOverAllowance int
}

// Valid reports whether this attempt was actually measured. The zero value is
// not, and no consumer may emit from it.
func (m MeasuredAttempt) Valid() bool { return ValidItemQuotaAvailability(m.Availability) }

// Reconciled reports whether the ledger accounted for the document.
func (m MeasuredAttempt) Reconciled() bool { return m.Ledger.Reconciled() }

// CertifiedFit reports whether this attempt is a certified fit -- a reconciled
// ledger inside a positive ceiling. It is NOT the negation of Overrun: an
// unbounded answer and an unreconciled one are both "not overrun" and neither
// is certified.
func (m MeasuredAttempt) CertifiedFit() bool {
	return m.Capacity == contractsv1.ContextFabricCapacityCertifiedFit
}

// MeasureAttempt builds the one measurement of one assembled result.
//
// It is PURE with respect to the answer: it counts, reconciles and compares,
// and it never truncates, discloses, mutates coverage or refuses. What to do
// about an overdraw or a disagreement is S7c's decision, and the engine's final
// assertion re-runs this on the finished document because a late composer can
// change what was measured here.
//
// The error is the MARSHAL error and nothing else, exactly as
// MeasureContextFabricResponse's is: a result that cannot be serialized is a
// server defect, and conflating it with an over-budget answer would let a
// serialization bug present as "your question was too big".
func MeasureAttempt(allocation ItemAllocation, result InvestigationResult, budget ResponseBudget) (MeasuredAttempt, error) {
	measurement, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		return MeasuredAttempt{}, err
	}
	ledger := contractsv1.ReconcileContextFabricResultItems(result)
	_, capacity := contractsv1.CertifyContextFabricCapacity(ledger, budget)

	attempt := MeasuredAttempt{
		Allocation:     allocation,
		Ledger:         ledger,
		Measurement:    measurement,
		Overrun:        measurement.Overrun(budget),
		Capacity:       capacity,
		GroupAllowance: allocation.GroupAllowance(),
		GroupsGranted:  allocation.Groups,
	}

	incidence := ledger.GroupIncidenceCounts()
	attempt.GroupsMeasured = len(incidence)

	switch {
	case !ledger.Reconciled():
		// No quota statement is possible about a document whose account
		// does not add up. Reporting zeros here is what "silently
		// substitute zero exposure" means, and it is forbidden.
		attempt.Availability = ItemQuotaAccountingDisagreement
		return attempt, nil
	case !allocation.InForce():
		attempt.Availability = ItemQuotaUnbounded
		return attempt, nil
	case allocation.Groups <= 0:
		attempt.Availability = ItemQuotaUnavailable
		return attempt, nil
	case attempt.GroupAllowance == 0:
		attempt.Availability = ItemQuotaBoundedZero
	default:
		attempt.Availability = ItemQuotaBounded
	}
	// MEASURED PER GROUP, under the rule the allocator DECLARED. Comparing a
	// SUM of incidences against an aggregate capacity is shared-pool
	// arithmetic: eighteen drivers each naming both of two groups measured
	// 18 <= 18 and reported zero over quota, while under every_group each
	// group carried all eighteen. Each group is compared to its own
	// allowance, and nothing is summed.
	for _, used := range incidence {
		if used > attempt.GroupAllowance {
			attempt.GroupsOverAllowance++
		}
	}
	return attempt, nil
}

// groupCountOf is how many group entities a cohort carries, zero for a nil
// cohort or one with no group axis.
func groupCountOf(cohort *Cohort) int {
	if cohort == nil {
		return 0
	}
	return len(cohort.Groups)
}

// measureAssembledAttempt is the ONE way a decision-making arm obtains a
// measured attempt, and the one place an accounting disagreement is raised.
//
// EVERY arm calls this, including the arm where the answer FITS. That is not
// symmetry for its own sake: the quota used to be computed only inside the
// narrowing path, so the majority path -- an answer that fits -- emitted three
// zeros that meant "never computed" and were read as "measured none".
//
// A disagreement raises a TYPED error and emits its own line before returning.
// It never degrades to zero exposure, never manufactures an adjusting debit,
// and never becomes a budget refusal: the caller's question was not too big,
// the server could not account for its own answer.
func (e *Engine) measureAssembledAttempt(
	ctx context.Context,
	principal storage.Principal,
	stage string,
	allocation ItemAllocation,
	result InvestigationResult,
	budget ResponseBudget,
) (MeasuredAttempt, error) {
	attempt, err := MeasureAttempt(allocation, result, budget)
	if err != nil {
		return MeasuredAttempt{}, stageError(StageValidation, fmt.Errorf("measure %s: %w", stage, err))
	}
	if accounting := itemAccountingErrorFor(stage, attempt); accounting != nil {
		// Telemetry FIRST, on the failure path, so the defect is
		// diagnosable from the run's own artifacts rather than by
		// re-running with instrumentation added.
		if e.telemetry != nil {
			e.telemetry.RecordItemAccounting(ctx, principal, ItemAccountingEvent{
				Stage:        stage,
				Status:       attempt.Ledger.Status,
				Disagreement: attempt.Ledger.Disagreement,
				Debits:       attempt.Ledger.Total(),
				Budgeted:     attempt.Ledger.Counts.Budgeted(),
				MaxItems:     budget.MaxItems,
			})
		}
		return MeasuredAttempt{}, stageError(StageValidation, accounting)
	}
	return attempt, nil
}

// ItemAccountingEvent is the operator-facing record of a disagreement. Counts
// and closed values only -- no subject, no group name, no prose -- the same
// corpus-safe posture every other event in this package keeps.
type ItemAccountingEvent struct {
	Stage        string
	Status       contractsv1.ContextFabricLedgerStatus
	Disagreement string
	Debits       int
	Budgeted     int
	MaxItems     int
}

// ErrItemAccounting is the sentinel for an answer whose item account does not
// reconcile with the answer itself.
//
// It UNWRAPS to ErrInvalidResult, so the route classifies it exactly where
// every other server-side result defect is classified -- an internal error,
// not retryable -- and NEVER as the budget refusal that renders a 413. That
// distinction is the whole point: an accounting defect must not diagnose the
// caller's question as oversized. The engine already draws the same line for a
// result that cannot be marshaled.
var ErrItemAccounting = errors.New("context fabric item accounting does not reconcile with the result")

// ItemAccountingError is the typed form, carrying what an operator needs to
// find the defect without re-running with instrumentation added.
type ItemAccountingError struct {
	// Stage names where the disagreement was observed.
	Stage string
	// Status is the reconciler's own closed verdict.
	Status contractsv1.ContextFabricLedgerStatus
	// Disagreement names the collection or bucket that disagreed.
	Disagreement string
	// Debits and Budgeted are the two numbers that should have been equal.
	Debits   int
	Budgeted int
}

func (e ItemAccountingError) Error() string {
	return fmt.Sprintf("%s: stage %s, status %s, disagreement %q, %d debits against %d budgeted items",
		ErrItemAccounting.Error(), e.Stage, e.Status, e.Disagreement, e.Debits, e.Budgeted)
}

// Unwrap yields ErrInvalidResult so existing classification applies unchanged.
// It deliberately does NOT yield ErrAnswerExceedsBudget: a caller must never be
// told their question was too large because the server could not account for
// its own answer.
func (e ItemAccountingError) Unwrap() error { return ErrInvalidResult }

// Is reports the sentinel too, so `errors.Is(err, ErrItemAccounting)` works for
// alerting while `errors.Is(err, ErrInvalidResult)` keeps the route's existing
// classification. Two questions, two answers, one error.
func (e ItemAccountingError) Is(target error) bool {
	return target == ErrItemAccounting || target == ErrInvalidResult
}

// itemAccountingErrorFor builds the typed error for an attempt that did not
// reconcile, and returns nil for one that did.
//
// A CONSTRUCTOR rather than a literal at each site, for the same reason the
// measurement has one: five arms decide on an assembled result, and a sixth
// will be added. Every one of them raises through here.
func itemAccountingErrorFor(stage string, attempt MeasuredAttempt) error {
	if attempt.Reconciled() {
		return nil
	}
	return ItemAccountingError{
		Stage:        stage,
		Status:       attempt.Ledger.Status,
		Disagreement: attempt.Ledger.Disagreement,
		Debits:       attempt.Ledger.Total(),
		Budgeted:     attempt.Ledger.Counts.Budgeted(),
	}
}
