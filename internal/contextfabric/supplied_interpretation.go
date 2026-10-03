package contextfabric

import (
	"context"
	"errors"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type (
	SuppliedInterpretation        = contractsv1.ContextFabricSuppliedInterpretation
	InterpretationContract        = contractsv1.ContextFabricInterpretationContract
	InterpretationContractRefusal = contractsv1.ContextFabricInterpretationContractRefusal
	InterpretationSource          = contractsv1.ContextFabricInterpretationSource
)

const (
	InterpretationSourceServer = contractsv1.ContextFabricInterpretationSourceServer
	InterpretationSourceClient = contractsv1.ContextFabricInterpretationSourceClient
)

// SuppliedInterpretationRuntime turns the interpretation a caller ran on its
// own model into an interpreted question and a receipt. It makes no model
// call. The supplied output is untrusted: an implementation validates it as
// it validates a model output.
type SuppliedInterpretationRuntime interface {
	// CheckSuppliedContract refuses, with a
	// *SuppliedInterpretationContractMismatch, a supplied interpretation
	// whose declared contract is not the service's own. It evaluates nothing
	// else of the request.
	CheckSuppliedContract(context.Context, storage.Principal, InvestigationRequest) error
	InterpretSuppliedQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error)
}

// SuppliedInterpretationGate is implemented by a QuestionInterpreter that
// takes supplied interpretations. The engine calls it at the start of every
// turn that carries one, before any exit that could serve a result, so a
// turn that ends before the interpret step is refused like any other.
type SuppliedInterpretationGate interface {
	CheckSuppliedInterpretation(context.Context, storage.Principal, InvestigationRequest) error
}

// ErrSuppliedInterpretationUnsupported is returned when a request carries a
// supplied interpretation and no SuppliedInterpretationRuntime is wired.
var ErrSuppliedInterpretationUnsupported = errors.New("supplied interpretations are not supported by this deployment")

// SuppliedInterpretationContractMismatch refuses a supplied interpretation
// that was made under a contract this service does not run. Refusal carries
// only service-owned values.
type SuppliedInterpretationContractMismatch struct {
	Refusal InterpretationContractRefusal
}

func (e *SuppliedInterpretationContractMismatch) Error() string {
	return "supplied interpretation contract mismatch: " + strings.Join(e.Refusal.Mismatch, ",")
}

// CheckSuppliedInterpretation refuses a supplied interpretation this
// interpreter would refuse for its contract, and every supplied
// interpretation when no supplied runtime is wired.
func (r RuntimeQuestionInterpreter) CheckSuppliedInterpretation(ctx context.Context, principal storage.Principal, request InvestigationRequest) error {
	if r.Supplied == nil {
		return ErrSuppliedInterpretationUnsupported
	}
	return r.Supplied.CheckSuppliedContract(ctx, principal, request)
}

// checkSuppliedInterpretation is the engine's entry gate for a request that
// carries a supplied interpretation. An interpreter that cannot check one
// cannot take one.
func (e *Engine) checkSuppliedInterpretation(ctx context.Context, principal storage.Principal, request InvestigationRequest) error {
	gate, ok := e.interpreter.(SuppliedInterpretationGate)
	if !ok {
		return ErrSuppliedInterpretationUnsupported
	}
	return gate.CheckSuppliedInterpretation(ctx, principal, request)
}

// interpretSupplied is Interpret for a request that carries its own
// interpretation. It enters interpretOneSample exactly as a model call does,
// so validation, the frame gate, the receipt sink and the family resolution
// run unchanged; only the source of the interpretation differs.
func (r RuntimeQuestionInterpreter) interpretSupplied(ctx context.Context, principal storage.Principal, request InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	if r.Supplied == nil {
		return InterpretedQuestion{}, QuestionFamilyOutcome{}, ErrSuppliedInterpretationUnsupported
	}
	question, receipt, err := r.interpretOneSample(ctx, principal, request, func() (InterpretedQuestion, ModelExecutionReceipt, error) {
		return r.Supplied.InterpretSuppliedQuestion(ctx, principal, request)
	})
	if err != nil {
		return InterpretedQuestion{}, QuestionFamilyOutcome{}, err
	}
	outcome := r.recordFamilyResolution(ctx, principal, question, receipt)
	outcome.Interpretation.Source = InterpretationSourceClient
	return question, outcome, nil
}
