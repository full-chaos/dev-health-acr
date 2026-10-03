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
	InterpretSuppliedQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error)
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
