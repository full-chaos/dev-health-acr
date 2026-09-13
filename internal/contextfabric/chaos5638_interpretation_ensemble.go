package contextfabric

import (
	"context"
	"errors"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5638: the question family is decided by consensus over N interpret
// samples, not by one.
//
// THE CONDITION THIS SLICE EXISTS TO ANSWER WAS WRITTEN INTO THE CODE IT
// CHANGES. RuntimeQuestionInterpreter's own doc comment held N=1 open with
// "Add it when a corpus shows N=1 failing", on the strength of a labelled set
// of 12 cases x 2 replicates that measured 100% resolved-family stability.
// The 36-row x 3-replicate corpus of 2026-09-12 shows it failing:
//
//   - the resolved family FLIPS WITHIN a single replicate -- the same
//     question, the same turn chain -- on 12 (row, replicate) pairs;
//   - the family differs ACROSS replicates at turn 1 on 3 rows;
//   - interpretation.subject_terms is present in one replicate and absent in
//     another, on the same question, on 8 rows.
//
// Every one of those is a decision the engine routes on: the family gates
// which structure axes may be offered, the plan copies from it, and the
// carry propagates it to later turns. A field that disagrees with itself
// between two runs of the same question makes the whole chain unreproducible.
//
// WHAT THIS BUYS, BOUNDED HONESTLY. 15 rows show interpretation instability
// and 15 rows show a bucket that differs across replicates, but the two sets
// OVERLAP ON ONLY 6. Nine bucket-unstable rows have a completely stable
// interpretation -- same family, same terms, same claimed facts, same
// coverage -- and still land degraded versus partial; those are the
// model-authored terminal status, a different question, and consensus will
// not move them. So the bucket effect here is bounded at 6 rows and is not
// promised even there: a consensus picks the majority sample, it does not
// make the majority correct. What it buys outright is removing the
// interpretation as a source of run-to-run variance on all 15.
//
// WHAT IT COSTS, and why the ceiling already existed. Interpret runs on every
// turn 1, the most frequent call in the product, so N multiplies its token
// cost. The samples are independent and run CONCURRENTLY, so the wall cost is
// one sample's latency rather than N. QuestionFamilyEnsembleMax bounds N
// whatever a caller configures.
//
// CONCURRENCY, CHECKED RATHER THAN ASSUMED. Each sample makes its own model
// call, validates its own interpretation, resolves its own frame and persists
// its own receipt, so N samples touch the receipt sink and the telemetry sink
// in parallel. Both production implementations are safe for that by
// construction: pgmodelreceipts.Store
// holds a *sql.DB (safe for concurrent use by definition) and an id generator,
// and SlogEngineTelemetry holds only a *slog.Logger. A composition that wires
// a sink which is NOT safe would be a defect in that sink, not here -- the
// ModelReceiptSink contract is a persistence port and every port in this
// package is called from concurrent request handling already.
//
// WHY NOT ResolveQuestionFamilyEnsemble. That helper takes a sampler over
// sample indices and returns the samples it collected. It cannot carry each
// sample's own InterpretedQuestion and ModelExecutionReceipt back out, and
// this path needs both: the winning sample's QUESTION is what the caller
// receives, and the winning sample's RECEIPT is what stamps the gate, the
// frame and the shadow. Reconstructing that pairing from a returned sample
// slice would mean matching samples back to calls by value, which is exactly
// the field-wise fabrication selectWinningSample exists to prevent one level
// up. So this file runs the fan-out itself, keeps the three arrays index
// aligned by construction, and hands the resulting slice to the SAME
// ResolveQuestionFamily the N=1 path uses. The aggregation rule is shared;
// only the collection differs.

// ErrEnsembleRuntimeMissing is returned when a composition asks for N>1 and
// wired no sampled runtime to draw the samples from.
//
// LOUD, NEVER A SILENT DEGRADE TO N=1. This package has already paid for the
// other choice once: CommitAffirmationTelemetry was an optional interface,
// nothing in production implemented it, every call failed a type assertion,
// and the entire event disappeared while the tests stayed green. A consensus
// that quietly became a single sample would be the same failure with a worse
// blast radius -- the operator would read `model_consensus` in the design and
// `model` on the wire and have nothing to tell them which was true.
var ErrEnsembleRuntimeMissing = errors.New("context fabric interpretation ensemble requires a sampled model runtime")

// ErrEnsembleAllSamplesFellBack reports that every sample came back from the
// fallback provider, so none of them may vote and there is no error to
// surface either.
//
// ITS OWN SENTINEL because the operator response differs from every other
// failure here: nothing is broken in this service, the primary model provider
// is unreachable or refusing, and the fix is upstream. Folding it into a
// joined sample error would describe it as N independent failures, which is
// the opposite of what happened -- N successes that are not usable as votes.
var ErrEnsembleAllSamplesFellBack = errors.New("context fabric interpretation ensemble: every sample was served by the fallback provider and none may vote")

// SampledModelRuntime is the per-sample half of ModelRuntime: one interpret
// call identified by a sample index, so the runtime can derive a distinct
// decoding seed per sample.
//
// A SEPARATE, EXPLICITLY WIRED PORT rather than an optional method discovered
// by type assertion on Runtime -- see ErrEnsembleRuntimeMissing for the
// incident that rule comes from. Seed derivation stays inside the runtime
// implementation, which is the only place that knows how its decoder is
// seeded; this interface passes the index and nothing else.
type SampledModelRuntime interface {
	InterpretQuestionForSample(context.Context, storage.Principal, InvestigationRequest, int) (InterpretedQuestion, ModelExecutionReceipt, error)
}

// interpretedSample is one sample's complete result: the three values that
// must travel together or not at all.
type interpretedSample struct {
	question InterpretedQuestion
	receipt  ModelExecutionReceipt
	sample   FamilySample
}

// ensembleEnabled reports whether this interpreter is configured to sample.
// Both halves are required: a size with no runtime is a misconfiguration
// (ErrEnsembleRuntimeMissing), and a runtime with no size is simply the
// unconfigured default.
func (r RuntimeQuestionInterpreter) ensembleEnabled() bool {
	return r.EnsembleSize > 1
}

// interpretEnsemble runs BoundEnsembleSize(r.EnsembleSize) interpret samples
// concurrently and returns the winning sample's question with the consensus
// outcome.
//
// FAILED SAMPLES ARE DROPPED, NOT SUBSTITUTED, and the rule is
// ResolveQuestionFamilyEnsemble's own: a sampler error yields no sample, the
// strict-majority denominator is the number of samples that SUCCEEDED, and a
// zero-valued stand-in would inject a vote no model cast. If every sample
// fails, the caller gets the first error rather than an outcome resolved over
// nothing -- an interpretation that could not be produced is an error, not an
// unclassified family.
func (r RuntimeQuestionInterpreter) interpretEnsemble(ctx context.Context, principal storage.Principal, request InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	if r.SampledRuntime == nil {
		return InterpretedQuestion{}, QuestionFamilyOutcome{}, ErrEnsembleRuntimeMissing
	}
	size := BoundEnsembleSize(r.EnsembleSize)
	collected := make([]interpretedSample, size)
	failures := make([]error, size)
	ok := make([]bool, size)

	var wg sync.WaitGroup
	for index := 0; index < size; index++ {
		wg.Add(1)
		go func(sample int) {
			defer wg.Done()
			question, receipt, err := r.interpretOneSample(ctx, principal, request, func() (InterpretedQuestion, ModelExecutionReceipt, error) {
				return r.SampledRuntime.InterpretQuestionForSample(ctx, principal, request, sample)
			})
			if err != nil {
				failures[sample] = err
				return
			}
			collected[sample] = interpretedSample{
				question: question,
				receipt:  receipt,
				sample:   familySampleFrom(question, receipt),
			}
			ok[sample] = true
		}(index)
	}
	wg.Wait()

	// INDEX ORDER, never completion order. The winner is chosen by a total
	// key so ordering does not decide the outcome, but a reproducible order
	// makes two runs of the same question diffable -- the same reason
	// ResolveQuestionFamilyEnsemble preserves it.
	//
	// A FALLBACK-SERVED SAMPLE IS NOT A VOTE. genkitruntime's own port says so
	// on the method this calls: the sample index governs the PRIMARY attempt
	// only, the fallback entry point takes no index at all, and "a caller
	// doing an N-sample measurement MUST check FallbackUsed and exclude any
	// such sample". N fallback responses are therefore ONE answer counted N
	// times, and a majority among them is agreement that was never measured.
	// Counted and reported, never voted.
	succeeded := make([]interpretedSample, 0, size)
	samples := make([]FamilySample, 0, size)
	fallbackServed := 0
	failed := 0
	for index := range collected {
		if !ok[index] {
			failed++
			continue
		}
		if collected[index].receipt.FallbackUsed {
			fallbackServed++
			continue
		}
		succeeded = append(succeeded, collected[index])
		samples = append(samples, collected[index].sample)
	}

	// QUORUM: a strict majority of the samples that were REQUESTED, not of the
	// ones that happened to come back. Measuring the majority against the
	// survivors would let one surviving sample out of three be a unanimous
	// consensus of itself, which is the degrade this rule exists to name.
	quorum := size/2 + 1
	quorumMet := len(succeeded) >= quorum
	r.recordEnsembleComposition(ctx, principal, InterpretationEnsembleEvent{
		Requested:        size,
		PrimarySucceeded: len(succeeded),
		FallbackServed:   fallbackServed,
		Failed:           failed,
		QuorumMet:        quorumMet,
	})

	if len(succeeded) == 0 {
		// Nothing to resolve over. A joined error rather than an unclassified
		// family: an interpretation that could not be produced is not a
		// question the model declined to classify.
		if failed == 0 {
			// Every sample came back, and every one came back on the
			// fallback. There is no error to join, and returning nil would
			// hand the caller a zero-valued interpretation as though it
			// were real.
			return InterpretedQuestion{}, QuestionFamilyOutcome{}, ErrEnsembleAllSamplesFellBack
		}
		return InterpretedQuestion{}, QuestionFamilyOutcome{}, errors.Join(failures...)
	}

	// BELOW QUORUM THE TURN IS NOT A CONSENSUS and must not claim to be. It
	// takes the single-sample path over the first surviving primary sample,
	// which records source=model -- the honest label for one sample -- and the
	// Warn event above is what tells an operator it happened. Silently
	// resolving over the survivors would publish `model_consensus` for a vote
	// of one or two out of three.
	if !quorumMet {
		winner := succeeded[0]
		single := []FamilySample{winner.sample}
		return winner.question, r.finishFamilyResolution(ctx, principal, ResolveQuestionFamily(single), single, winner.receipt), nil
	}

	outcome := ResolveQuestionFamily(samples)
	// THE WINNING SAMPLE'S OWN QUESTION AND RECEIPT, or the first sample's
	// when the ensemble reached no majority.
	//
	// WinningSampleIndex is -1 exactly when there is no winner, and the
	// outcome then carries family=unclassified with
	// source=model_plurality_rejected -- a split the resolver REFUSED, which
	// is the design working. The caller still needs an interpretation to
	// return, and it must be one whole sample rather than a merge: the first
	// successful sample is the reproducible choice (index order is fixed
	// above), and the refused-plurality source on the wire is what tells an
	// operator the family behind it was not agreed.
	winner := succeeded[0]
	if outcome.WinningSampleIndex >= 0 && outcome.WinningSampleIndex < len(succeeded) {
		winner = succeeded[outcome.WinningSampleIndex]
	}
	return winner.question, r.finishFamilyResolution(ctx, principal, outcome, samples, winner.receipt), nil
}

// recordEnsembleComposition emits the one event that makes an ensemble turn's
// composition observable, when telemetry is wired.
//
// nil FamilyTelemetry still resolves the turn -- the same rule
// recordFamilyResolution already follows, and for the same reason: gating
// BEHAVIOUR on whether telemetry was configured would make a misconfigured
// composition answer differently from a correct one.
func (r RuntimeQuestionInterpreter) recordEnsembleComposition(ctx context.Context, principal storage.Principal, event InterpretationEnsembleEvent) {
	if r.FamilyTelemetry == nil {
		return
	}
	r.FamilyTelemetry.RecordInterpretationEnsemble(ctx, principal, event)
}
