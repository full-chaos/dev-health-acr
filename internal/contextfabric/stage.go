package contextfabric

import "errors"

// InvestigationStage names the phase of Engine.Investigate a failure came
// from. CHAOS-3811: a failed investigation used to reach the route with
// nothing but a message an operator was not allowed to log, so every failure
// -- a graph outage, a rejected fact request, an invalid synthesized result
// -- looked identical from outside: failure_class=context_fabric_investigation
// and nothing else.
//
// It is a CLOSED enum on purpose. The stage is emitted in structured logs, so
// it must be a fixed, content-free classification -- never a provider name, a
// driver message, an org identifier, or any part of the question. Adding a
// value is a deliberate edit here, which is what keeps the log surface
// bounded by construction rather than by reviewer vigilance.
type InvestigationStage string

const (
	StageInterpretation InvestigationStage = "interpretation"
	StageResolution     InvestigationStage = "resolution"
	StageGraph          InvestigationStage = "graph"
	StageFactRead       InvestigationStage = "fact_read"
	StageSynthesis      InvestigationStage = "synthesis"
	StageValidation     InvestigationStage = "validation"
	// StagePersistence covers the immutable result store write, the one
	// step that happens after a result is already valid. It is separate
	// from StageValidation because the two have opposite meanings for an
	// operator: validation failing is an ACR-side defect in what was
	// produced, persistence failing is a dependency that was unavailable
	// to accept it.
	StagePersistence InvestigationStage = "persistence"
	// StageUnknown is what a reader reports for an error carrying no stage
	// at all (e.g. a wire-level refusal raised before the pipeline starts).
	// It is never attached by StageError -- it exists so a consumer of
	// FailureStage always has a bounded value to emit.
	StageUnknown InvestigationStage = "unknown"
)

// ValidInvestigationStage reports whether stage is a member of the closed
// enum above. Used by anything that emits the value, so an out-of-band string
// can never reach a log or metric label.
func ValidInvestigationStage(stage InvestigationStage) bool {
	switch stage {
	case StageInterpretation, StageResolution, StageGraph, StageFactRead,
		StageSynthesis, StageValidation, StagePersistence, StageUnknown:
		return true
	default:
		return false
	}
}

// StageError attaches an InvestigationStage to an error WITHOUT replacing or
// flattening it: Unwrap returns the cause, so every sentinel already in the
// chain (ErrUnavailable, ErrModelOutput, ErrNoInvestigationSubjects, a
// contracts/v1 validation sentinel, ...) still answers errors.Is/errors.As at
// the route exactly as it did before the stage was added.
type StageError struct {
	Stage InvestigationStage
	Err   error
}

func (e *StageError) Error() string {
	if e == nil || e.Err == nil {
		return string(e.Stage)
	}
	return string(e.Stage) + ": " + e.Err.Error()
}

func (e *StageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// stageError tags err with stage. A nil error stays nil, and an unrecognized
// stage is dropped rather than propagated -- the enum is the authority.
func stageError(stage InvestigationStage, err error) error {
	if err == nil {
		return nil
	}
	if !ValidInvestigationStage(stage) || stage == StageUnknown {
		return err
	}
	return &StageError{Stage: stage, Err: err}
}

// FailureStage reports the stage an investigation failure was tagged with.
// It returns the OUTERMOST tag, which is the stage that actually failed: an
// inner stage's error is never re-tagged on the way out, so nesting does not
// occur today, and outermost is the right answer if it ever does.
//
// Returns StageUnknown, false when err carries no stage -- a caller should
// emit the returned value either way rather than inventing its own default.
func FailureStage(err error) (InvestigationStage, bool) {
	var staged *StageError
	if errors.As(err, &staged) && staged != nil && ValidInvestigationStage(staged.Stage) {
		return staged.Stage, true
	}
	return StageUnknown, false
}
