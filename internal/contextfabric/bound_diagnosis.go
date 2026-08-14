package contextfabric

import (
	"fmt"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ClassifyInterpretationRejection wraps cause -- already produced by
// question.Validate() failing -- into ErrInterpretationRejected (which also
// wraps ErrModelOutput, so every existing errors.Is(err, ErrModelOutput)
// caller is unaffected), attaching a ModelBoundViolation when question's
// rejection is attributable to a specific contracts/v1 model-facing bound
// (see contractsv1.DiagnoseContextFabricInterpretedQuestionBound).
//
// question must be the ACTUAL (possibly invalid) value Validate() was
// called against, not a zero value -- CHAOS-3784 round-2 finding F1:
// genkitruntime.Runtime.InterpretQuestion's own toDomain() call is the
// production ModelRuntime's OWN Validate() call site (it self-validates
// before RuntimeQuestionInterpreter.Interpret ever sees the result), so
// this function must be reachable from there too, not only from
// RuntimeQuestionInterpreter.Interpret's defensive re-validation for a
// ModelRuntime that does not self-validate.
func ClassifyInterpretationRejection(question InterpretedQuestion, cause error) error {
	// CHAOS-3811: %w on the cause too, not %v. The rendered message is
	// identical either way; what changes is that a sentinel the
	// contracts/v1 validator attached to cause still answers errors.Is at
	// the route instead of being flattened into prose here.
	wrapped := fmt.Errorf("%w: %w: %w", ErrInterpretationRejected, ErrModelOutput, cause)
	bound, diagnosed := contractsv1.DiagnoseContextFabricInterpretedQuestionBound(question)
	return withBoundViolation(wrapped, bound, diagnosed)
}

// ClassifySynthesisRejection is ClassifyInterpretationRejection's
// synthesis-side counterpart: draft must be the actual (possibly invalid)
// value ValidateAgainst was called against, and input the SAME
// SynthesisInput it was validated against -- diagnoseSynthesisDraftBound
// needs it to rebuild the identical grounding context (allowedSubjects/
// allowedPaths/allowedEvidence/canonicalLabels) ValidateAgainst itself
// uses (CHAOS-3784 round-4). genkitruntime.Runtime.SynthesizeAnswer calls
// draft.ValidateAgainst(input) itself, so this must be reachable from
// there too, not only from RuntimeAnswerSynthesizer.Synthesize's
// defensive re-validation.
func ClassifySynthesisRejection(draft SynthesisDraft, input SynthesisInput, cause error) error {
	wrapped := fmt.Errorf("%w: %w: %w", ErrSynthesisRejected, ErrModelOutput, cause)
	bound, diagnosed := diagnoseSynthesisDraftBound(draft, input)
	return withBoundViolation(wrapped, bound, diagnosed)
}

// diagnoseSynthesisDraftBound is a literal, statement-by-statement MIRROR
// of SynthesisDraft.ValidateAgainst's own left-to-right, short-circuit
// control flow (model_runtime.go) -- not an independent scan of d for any
// diagnosable bound. CHAOS-3784 round-4: violated_bound must be SOUND
// before it is complete. Scanning independently let an EARLIER,
// non-diagnosable rejection (an invalid status, a missing
// deterministic_answer, an unknown top-level evidence ref, a claim/driver/
// finding that fails structural OR grounding checks before a LATER
// draft entry's genuine bound violation) be masked by that later,
// unrelated bound -- reporting a name for something ValidateAgainst never
// actually rejected on. This function instead returns at the FIRST
// statement that fails, exactly where ValidateAgainst itself would
// return, naming a bound only when that first failure is one.
//
// This duplicates ValidateAgainst's control flow (deliberately -- see
// CHAOS-3784 round-4's discussion of why extracting a single shared
// ordered-check table was not attempted: ValidateAgainst's grounding
// checks close over allowedSubjects/allowedPaths/allowedEvidence built
// from `input`, and are woven through business-rule and structural checks
// throughout; splitting that into a shared, order-preserving table is a
// bigger and riskier change than this ticket's route-level classification
// scope). A change to ValidateAgainst's statement order must be mirrored
// here; the regression tests alongside this file
// (TestDiagnoseSynthesisDraftBound*) pin the two exact scenarios CHAOS-3784
// round-4 found (an earlier non-bound rejection masking a later bound) so
// drift is caught, not merely single calls exercised.
//
// Top-level synthesis collection caps (drivers.max_count,
// remaining_work.max_count, and siblings in
// contractsv1.ContextFabricModelFacingBounds) are still deliberately NOT
// diagnosable here, unchanged from earlier rounds: ValidateAgainst itself
// never checks them -- only ContextFabricInvestigationResult.Validate()
// does, later and against the already-composed InvestigationResult,
// classified ErrInvalidResult (see engine.go), not ErrSynthesisRejected. A
// synthesis draft that violates one of those top-level caps therefore
// never reaches this function with a non-nil ValidateAgainst error in the
// first place; distinguishing THAT class of rejection is a separate,
// pre-existing gap (a violation there is misattributed to ACR as a 500,
// not to the model) outside CHAOS-3784's narrow scope (CHAOS-3790).
func diagnoseSynthesisDraftBound(d SynthesisDraft, input SynthesisInput) (bound string, ok bool) {
	switch d.Status {
	case InvestigationComplete, InvestigationPartial, InvestigationDegraded, InvestigationClarificationRequired, InvestigationNoMatch:
	default:
		return "", false
	}
	if (d.Status == InvestigationComplete || d.Status == InvestigationPartial) && strings.TrimSpace(d.DirectJudgment) == "" {
		return "", false
	}
	if strings.TrimSpace(d.DeterministicAnswer) == "" {
		return "", false
	}

	allowedSubjects := synthesisSubjects(input)
	canonicalLabels := canonicalSubjectLabels(input)
	allowedPaths := make(map[string]struct{}, len(input.Graph.Paths))
	allowedEvidence := make(map[string]struct{})
	for _, path := range input.Graph.Paths {
		allowedPaths[path.PathID] = struct{}{}
		for _, evidenceRefID := range path.EvidenceRefIDs {
			allowedEvidence[evidenceRefID] = struct{}{}
		}
		for _, edge := range path.Edges {
			for _, evidenceRefID := range edge.EvidenceRefIDs {
				allowedEvidence[evidenceRefID] = struct{}{}
			}
		}
	}
	for _, evidenceRefID := range input.Graph.EvidenceRefIDs {
		allowedEvidence[evidenceRefID] = struct{}{}
	}
	for _, fact := range input.Facts.Facts {
		for _, evidenceRefID := range fact.EvidenceRefIDs {
			allowedEvidence[evidenceRefID] = struct{}{}
		}
	}
	for _, candidate := range input.Graph.Resolution.Candidates {
		for _, evidenceRefID := range candidate.EvidenceRefIDs {
			allowedEvidence[evidenceRefID] = struct{}{}
		}
	}
	for _, evidenceRefID := range d.EvidenceRefIDs {
		if _, exists := allowedEvidence[evidenceRefID]; !exists {
			return "", false
		}
	}

	claimedByID := make(map[string]ClaimedFact, len(d.ClaimedFacts))
	for _, claim := range d.ClaimedFacts {
		if err := claim.Validate(); err != nil {
			return contractsv1.DiagnoseContextFabricClaimedFactBound(claim)
		}
		if _, exists := claimedByID[claim.ClaimID]; exists {
			return "", false
		}
		claimedByID[claim.ClaimID] = claim
		if _, exists := allowedSubjects[subjectKeyForModel(claim.Subject)]; !exists {
			return "", false
		}
		if err := requireBoundLabel("claimed fact", claim.Subject, canonicalLabels); err != nil {
			return "", false
		}
		canonical, exists := lookupCanonicalFact(input.Facts.Facts, claim.Kind, claim.Subject)
		if !exists {
			return "", false
		}
		observed, present := canonical.Fields[claim.Field]
		if !present {
			return "", false
		}
		if !factValueEqualsScalar(observed, claim.Value) {
			return "", false
		}
	}

	for _, driver := range d.Drivers {
		if err := driver.Validate(); err != nil {
			return contractsv1.DiagnoseContextFabricDriverJudgmentBound(driver)
		}
		for _, subject := range driver.AffectedSubjects {
			if _, exists := allowedSubjects[subjectKeyForModel(subject)]; !exists {
				return "", false
			}
			if err := requireBoundLabel("driver", subject, canonicalLabels); err != nil {
				return "", false
			}
		}
		for _, pathID := range driver.PathIDs {
			if _, exists := allowedPaths[pathID]; !exists {
				return "", false
			}
		}
		for _, evidenceRefID := range driver.EvidenceRefIDs {
			if _, exists := allowedEvidence[evidenceRefID]; !exists {
				return "", false
			}
		}
		if err := requireGroundedClaims("driver", driver.Category, driver.AffectedSubjects, allowedSubjects, driver.ClaimedFactIDs, claimedByID); err != nil {
			return "", false
		}
	}

	// Fixed slice, matching ValidateAgainst's own fixed order (CHAOS-3784
	// round-3 R3-2) -- NOT a map, whose iteration order is randomized per
	// range.
	for _, section := range []struct {
		name     string
		findings []Finding
	}{
		{"remaining_work", d.RemainingWork},
		{"readiness_gaps", d.ReadinessGaps},
		{"conflicts", d.Conflicts},
	} {
		for _, finding := range section.findings {
			if err := finding.Validate(); err != nil {
				return contractsv1.DiagnoseContextFabricFindingBound(finding)
			}
			for _, subject := range finding.Subjects {
				if _, exists := allowedSubjects[subjectKeyForModel(subject)]; !exists {
					return "", false
				}
				if err := requireBoundLabel(section.name, subject, canonicalLabels); err != nil {
					return "", false
				}
			}
			for _, evidenceRefID := range finding.EvidenceRefIDs {
				if _, exists := allowedEvidence[evidenceRefID]; !exists {
					return "", false
				}
			}
			if err := requireGroundedClaims(section.name, finding.Kind, finding.Subjects, allowedSubjects, finding.ClaimedFactIDs, claimedByID); err != nil {
				return "", false
			}
		}
	}

	return "", false
}
