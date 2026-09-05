package contextfabric

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The FINAL budget assertion: the one point that measures the document the
// route will actually serialize, against the EFFECTIVE budget, at every fresh
// result exit.
//
// Two separate defects make this necessary, and they are different defects.
//
// 1. THE DECISIVE PATH MEASURED THE WRONG DOCUMENT. fitAssembledResult measures
// (engine.go), and only afterwards does the engine re-stamp AnswerPlan and run
// applyCoverageDisplayLabels, which writes coverage source labels, coverage
// detail labels and the whole EvidenceRefLabels map. Those writes add BYTES and
// no items, so the measured document was smaller than the served one. This is
// the defect four successive revisions of the minimal-answer-floor
// specification died on; the accompanying test executes it rather than arguing
// it (label composition adds 324 bytes on the fixture, and the engine accepts a
// fit at a budget the served document then exceeds).
//
// 2. FOUR OF THE FIVE FRESH-RESULT EXITS WERE NEVER MEASURED AT ALL.
// fitAssembledResult sits on the decisive path only. unresolved.go's
// terminalResult, window.go's windowVetoResult and
// windowConfirmationRequiredResult, and structure.go's structureVetoResult each
// return a served document with no measurement, no narrowing event and no
// continuation -- the route then refuses it carrying none of the diagnosis the
// measured path emits.
//
// WHY THE GUARD IS PER-EXIT RATHER THAN PER-SITE. Those five exits have been
// swept twice before: CHAOS-4413 added the Completeness stamp to all five, and
// CHAOS-4690 added the display-label stamp to all five. Both comments are still
// in the tree beside each exit. The enumeration already existed and the budget
// stage was simply never added to it, so placing this assertion on the decisive
// path alone would repeat the omission a third time -- and would leave the
// claim "the final document is measured" true only of one path.

// BudgetAssertStage names WHICH fresh-result exit asserted. It is CLOSED
// because it reaches telemetry, where it is the field that turns "an assertion
// fired" into "which exit served an unmeasured document" -- the difference
// between a countable defect and an operator re-reading source.
//
// It is deliberately NOT the same vocabulary as
// ContextFabricPlanNarrowingStage: that names a narrowing STAGE within the
// decisive path's budget pipeline, and four of these exits have no such
// pipeline. Reusing it would force four members that mean "not that pipeline".
type BudgetAssertStage string

const (
	// BudgetAssertDecisive is engine.go's own path -- the only exit that was
	// measured before this change, and measured too early.
	BudgetAssertDecisive BudgetAssertStage = "decisive"
	// BudgetAssertSubjectlessTerminal is unresolved.go's terminalResult: the
	// clarification/no-match exit. It charges SubjectResolution.Candidates,
	// which the contract permits up to 50 -- above a default 30-item ceiling
	// on that term alone.
	BudgetAssertSubjectlessTerminal BudgetAssertStage = "subjectless_terminal"
	// BudgetAssertWindowVeto is window.go's windowVetoResult.
	BudgetAssertWindowVeto BudgetAssertStage = "window_veto"
	// BudgetAssertWindowConfirmationRequired is window.go's
	// windowConfirmationRequiredResult.
	BudgetAssertWindowConfirmationRequired BudgetAssertStage = "window_confirmation_required"
	// BudgetAssertStructureVeto is structure.go's structureVetoResult.
	//
	// The Save-race terminal (structureSupersessionVetoResult) reports under
	// THIS label and deliberately gets no member of its own: it is a thin
	// wrapper that delegates wholly to structureVetoResult, so it genuinely
	// IS this code path. Minting a second token for it would put a member in
	// the vocabulary that no producer can emit -- and a closed vocabulary
	// wider than its producer is not a closed vocabulary (the rule #378
	// established when it narrowed its own narrowing-basis enum to the two
	// orders its sole producer can return).
	BudgetAssertStructureVeto BudgetAssertStage = "structure_veto"
	// BudgetAssertReuse is the reuse serve. A stored row is re-validated
	// against the CURRENT effective budget, which is chris's promise of
	// record in terms: "reuse and stored reads are re-validated against the
	// current budget and refuse if they no longer fit." It persists nothing
	// -- the stored document is left exactly as it was; only the decision to
	// serve it is made here. The REMEDY for a reuse hit that no longer fits
	// (budget-keyed reuse, or re-investigation) is a separate open decision,
	// floor paper C2, ticketed.
	BudgetAssertReuse BudgetAssertStage = "reuse"
)

// BudgetAssertStageCount is the vocabulary size, so a test that must cover
// every member fails to compile rather than silently covering fewer when a
// member is added.
const BudgetAssertStageCount = 6

// BudgetAssertStageVocabulary returns every member. Returned as a sized array
// rather than a slice for the same reason the count above is exported: a new
// exit must break the build of anything enumerating them.
func BudgetAssertStageVocabulary() [BudgetAssertStageCount]BudgetAssertStage {
	return [BudgetAssertStageCount]BudgetAssertStage{
		BudgetAssertDecisive,
		BudgetAssertSubjectlessTerminal,
		BudgetAssertWindowVeto,
		BudgetAssertWindowConfirmationRequired,
		BudgetAssertStructureVeto,
		BudgetAssertReuse,
	}
}

// ValidBudgetAssertStage reports membership.
func ValidBudgetAssertStage(value BudgetAssertStage) bool {
	for _, member := range BudgetAssertStageVocabulary() {
		if member == value {
			return true
		}
	}
	return false
}

// BudgetAssertionEvent is CLOSED ENUMS AND COUNTS ONLY -- no question text, no
// subject identifier, no label -- the same discipline PlanNarrowingEvent holds,
// for the same reason: this stream is corpus-safe and is read as a dashboard.
type BudgetAssertionEvent struct {
	// Stage names the exit that asserted.
	Stage BudgetAssertStage
	// Fits is the ordinary case and is recorded, not just the failure. An
	// assertion that only emitted on overrun would make "how often does the
	// final document fit" -- the denominator for every rate an operator
	// would want -- unanswerable from the artifacts, which is exactly the
	// finding a prior round made against the stage-3 fit.
	Fits bool
	// Overrun names which axis failed, or "fits".
	Overrun contractsv1.ContextFabricBudgetOverrun
	// MeasuredItems and MeasuredBytesPostLabel are the FINAL document's, so
	// the decisive path's earlier measurement and this one can be compared
	// in production and the byte delta that label composition adds is
	// observable rather than inferred.
	MeasuredItems          int
	MeasuredBytesPostLabel int64
	// The ceilings this was measured against.
	MaxItems           int
	MaxSerializedBytes int64
	// LedgerStatus is the FINISHED document's own accounting verdict, and
	// LedgerDebits the number of charged occurrences it accounted for.
	// Recorded on a fit as well as a failure, so "how often does the account
	// reconcile" has a denominator.
	LedgerStatus contractsv1.ContextFabricLedgerStatus
	LedgerDebits int
	// Capacity is the ledger-backed verdict, and CertifiedFit reports
	// whether a real certificate was issued.
	//
	// It is NOT the negation of Overrun and the two are deliberately both
	// here: an unbounded answer and an unreconciled one are each "not
	// overrun", and neither is certified. A dashboard reading only Fits
	// cannot tell "we checked it against a ceiling" from "there was no
	// ceiling to check".
	Capacity     contractsv1.ContextFabricCapacityVerdict
	CertifiedFit bool
}

// assertFitsBudget measures result -- which must be the FINAL document, after
// every composer and immediately before Validate -- against the effective
// budget, records the outcome either way, and returns an AnswerBudgetRefusal
// when it does not fit.
//
// The refusal is the SAME sentinel the decisive path's planned refusal already
// uses, so the route classifies it as the designed outcome it is and no caller
// sees a new status. What changes is that it now carries a measurement taken on
// the served document, and telemetry naming which exit produced it.
//
// A zero on either budget axis means unbounded on that axis
// (ContextFabricResponseBudget's own doc comment), so an engine composed
// without ceilings behaves exactly as it did before this change.
func (e *Engine) assertFitsBudget(ctx context.Context, principal storage.Principal, stage BudgetAssertStage, result InvestigationResult, budget ResponseBudget) error {
	if budget.MaxItems <= 0 && budget.MaxSerializedBytes <= 0 {
		return nil
	}
	measurement, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		// A result that cannot be marshaled is a server defect, not an
		// over-budget answer. Conflating the two would let a serialization
		// bug present to the caller as "your question was too big" -- the
		// same distinction fitAssembledResult already makes.
		return stageError(StageValidation, err)
	}
	// RECONCILE THE FINISHED DOCUMENT, here, after every late writer.
	//
	// Stage three measured an earlier shape: the plan is stamped, the
	// requirement gap-fill runs, completeness is re-derived and the display
	// labels are composed AFTER it. An account reconciled against that
	// earlier shape says nothing about the document the route will serialize,
	// and "a late composer changes the result while the consumer holds an
	// earlier, perfectly balanced ledger" is the residual this seam was
	// warned about by name. The ledger is therefore re-derived from the
	// document in hand, at the only point where nothing further can write.
	ledger := contractsv1.ReconcileContextFabricResultItems(result)
	if !ledger.Reconciled() {
		accounting := ItemAccountingError{
			Stage:        string(stage),
			Status:       ledger.Status,
			Disagreement: ledger.Disagreement,
			Debits:       ledger.Total(),
			Budgeted:     ledger.Counts.Budgeted(),
		}
		if e.telemetry != nil {
			e.telemetry.RecordItemAccounting(ctx, principal, ItemAccountingEvent{
				Stage:        string(stage),
				Status:       ledger.Status,
				Disagreement: ledger.Disagreement,
				Debits:       ledger.Total(),
				Budgeted:     ledger.Counts.Budgeted(),
				MaxItems:     budget.MaxItems,
			})
		}
		return stageError(StageValidation, accounting)
	}
	overrun := measurement.Overrun(budget)
	event := BudgetAssertionEvent{
		Stage:                  stage,
		Fits:                   overrun == contractsv1.ContextFabricBudgetFits,
		Overrun:                overrun,
		MeasuredItems:          measurement.Items.Budgeted(),
		MeasuredBytesPostLabel: measurement.Bytes,
		MaxItems:               budget.MaxItems,
		MaxSerializedBytes:     budget.MaxSerializedBytes,
		// The finished document's own account, on the line that describes
		// the finished document. Recorded on a FIT as well as an overrun,
		// for the same reason Fits itself is: an event that only fired on
		// failure leaves "how often does the account reconcile"
		// unanswerable from the artifacts.
		LedgerStatus:  ledger.Status,
		LedgerDebits:  ledger.Total(),
		CertifiedFit:  false,
	}
	certificate, capacity := contractsv1.CertifyContextFabricCapacity(ledger, budget)
	event.CertifiedFit = certificate.Certified()
	event.Capacity = capacity
	if e.telemetry != nil {
		e.telemetry.RecordBudgetAssertion(ctx, principal, event)
	}
	if event.Fits {
		return nil
	}
	return AnswerBudgetRefusal{
		Overrun:            overrun,
		MeasuredItems:      measurement.Items.Budgeted(),
		MeasuredBytes:      measurement.Bytes,
		MaxItems:           budget.MaxItems,
		MaxSerializedBytes: budget.MaxSerializedBytes,
		// RetryAttempted is false and is not a placeholder: no retry is
		// possible at this point on ANY exit. The decisive path's retry
		// already ran and its outcome is on its own narrowing event; the
		// other four exits have no synthesis to re-run.
		RetryAttempted: false,
	}
}

// finalizeServed is THE point at which a served document becomes final and is
// measured. There is exactly one of these, and every serving path goes through
// it -- which is the property that matters, not the number of call sites.
//
// WHY ONE SITE, and why the previous shape was wrong. The first version of this
// guard put an assertion at the tail of each exit function. That was defeated
// immediately: `stampAnswerPlan` runs in the CALLER, so the plan object was
// added to the document AFTER the callee had measured it -- the same
// "something writes after the measurement" defeat that has now beaten this
// program SEVEN times (four revisions of the minimal-answer-floor
// specification, CHAOS-4636's own round-1 finding, this guard's
// label-composition predecessor, and then the plan stamp itself).
//
// The order here is the whole point and it is not negotiable:
//
//	stamp every late writer  ->  measure  ->  (caller persists)
//
// Persistence deliberately follows the caller's own call to this function
// rather than being passed in as a closure: each serving path keys its Save
// differently (time-axis keys, watermarks, epochs), and lifting that into a
// shared helper would move five different key derivations into one place for no
// gain. What this function guarantees is that no path can persist or serve a
// document that was not measured in its final form, because the measurement
// happens here and the Save statements sit below the call.
//
// plan is nil on the paths that legitimately have none -- the three
// pre-interpret veto/gate exits run before an AnswerPlan exists. A nil plan
// stamps nothing; it is not a missing value.
func (e *Engine) finalizeServed(ctx context.Context, principal storage.Principal, stage BudgetAssertStage, result InvestigationResult, plan *AnswerPlan, budget ResponseBudget) (InvestigationResult, error) {
	if plan != nil {
		result = stampAnswerPlan(result, *plan)
	}
	// Re-derive completeness HERE, after the plan is on the result and before
	// the budget is asserted.
	//
	// The veto exits call ComputeAnswerCompleteness themselves, but they call
	// it BEFORE this function stamps the plan. Anything the derivation reads
	// off the plan is therefore invisible to them -- which is how those exits
	// came to serve, and save, a plan describing requirement rows that no
	// outcome row accounted for. Re-deriving here is what makes this function
	// the place the two published arrays are reconciled, rather than four
	// places that each have to remember.
	//
	// BEFORE assertFitsBudget, deliberately: rows added by the re-derivation
	// are bytes in the served document. Measuring first and completing the
	// account afterwards would assert a budget against a document smaller
	// than the one the route marshals -- the exact defect this function's
	// header says it exists to prevent.
	//
	// Safe to run on the paths that already derived it: the derivation is
	// pure and idempotent, so a second pass over an already-complete set
	// returns the same set.
	result.Completeness = ComputeAnswerCompleteness(result)
	if err := e.assertFitsBudget(ctx, principal, stage, result, budget); err != nil {
		return InvestigationResult{}, err
	}
	return result, nil
}
