package genkitruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/xeipuuv/gojsonschema"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretprompt"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	suppliedModelVersion         = "n/a"
	suppliedSchemaErrorUndecoded = "undecodable"
)

var errSuppliedOutputSchema = errors.New("supplied interpretation does not match the model output schema")

// SuppliedInterpreterConfig names the interpretation contract this service
// runs. Empty fields take the same defaults New gives a Runtime.
type SuppliedInterpreterConfig struct {
	InterpretationPromptVersion string
	SchemaVersion               string
	EvaluatorVersion            string
	MaxInputBytes               int
	Logger                      *slog.Logger
}

// SuppliedInterpreter is the contextfabric.SuppliedInterpretationRuntime of
// this package. It takes the interpretation a caller ran on its own model
// through the decode, validation and sanitizers a model output goes through,
// and it has no generator: it cannot make a model call.
type SuppliedInterpreter struct {
	config   SuppliedInterpreterConfig
	contract contextfabric.InterpretationContract
	schema   *gojsonschema.Schema
	now      func() time.Time
}

func NewSuppliedInterpreter(config SuppliedInterpreterConfig) (*SuppliedInterpreter, error) {
	if strings.TrimSpace(config.InterpretationPromptVersion) == "" {
		config.InterpretationPromptVersion = DefaultInterpretationPromptVersion
	}
	if strings.TrimSpace(config.SchemaVersion) == "" {
		config.SchemaVersion = DefaultSchemaVersion
	}
	if strings.TrimSpace(config.EvaluatorVersion) == "" {
		config.EvaluatorVersion = defaultEvaluatorVersion
	}
	if config.MaxInputBytes == 0 {
		config.MaxInputBytes = DefaultExchangeMaxInputBytes
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	document, err := InterpretationOutputSchema()
	if err != nil {
		return nil, fmt.Errorf("interpretation output schema: %w", err)
	}
	schema, err := gojsonschema.NewSchema(gojsonschema.NewBytesLoader(document))
	if err != nil {
		return nil, fmt.Errorf("interpretation output schema: %w", err)
	}
	return &SuppliedInterpreter{
		config: config,
		contract: contextfabric.InterpretationContract{
			ModelOutputVersion: config.SchemaVersion,
			PromptVersion:      config.InterpretationPromptVersion,
			SystemSHA256:       InterpretationSystemPromptSHA256(),
		},
		schema: schema,
		now:    time.Now,
	}, nil
}

// InterpretationSystemPromptSHA256 is the lowercase hex sha256 of the system
// message InterpretQuestion sends.
func InterpretationSystemPromptSHA256() string {
	sum := sha256.Sum256([]byte(interpretprompt.System()))
	return hex.EncodeToString(sum[:])
}

// Contract is the interpretation contract a supplied interpretation must
// have been made under.
func (s *SuppliedInterpreter) Contract() contextfabric.InterpretationContract { return s.contract }

func (s *SuppliedInterpreter) InterpretSuppliedQuestion(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	// Every return before the output is evaluated is a refused request; the
	// two later outcomes are read off the receipt.
	var receipt contextfabric.ModelExecutionReceipt
	decision := suppliedDecision{outcome: eventspec.SuppliedInterpretationOutcomeRequestInvalid}
	defer func() {
		if receipt.Operation != "" {
			decision.outcome = receipt.Outcome
		}
		s.logDecision(ctx, principal.OrgID, request.RequestID, decision)
	}()

	if err := request.Validate(); err != nil {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, fmt.Errorf("interpretation request: %w", err)
	}
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, errors.New("authenticated organization is required")
	}
	supplied := request.SuppliedInterpretation
	if supplied == nil {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, errors.New("request carries no supplied interpretation")
	}
	model := supplied.ClientModel
	if model == "" {
		model = contractsv1.ContextFabricClientModelUndeclared
	}
	decision.clientModel = model
	if mismatch := s.contractMismatch(*supplied); len(mismatch) > 0 {
		decision.outcome = eventspec.SuppliedInterpretationOutcomeContractMismatch
		decision.contractMismatch = mismatch
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, &contextfabric.SuppliedInterpretationContractMismatch{
			Refusal: contextfabric.InterpretationContractRefusal{Mismatch: mismatch, Current: s.contract},
		}
	}
	prompt, err := BuildInterpretationPrompt(request, s.config.MaxInputBytes)
	if err != nil {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, err
	}
	at := s.now().UTC()
	receipt = contextfabric.ModelExecutionReceipt{
		Operation: contextfabric.ModelOperationInterpret,
		Provider:  contractsv1.ContextFabricClientSuppliedProvider, Model: model, ModelVersion: suppliedModelVersion,
		PromptVersion: s.config.InterpretationPromptVersion, SchemaVersion: s.config.SchemaVersion,
		EvaluatorVersion: s.config.EvaluatorVersion,
		StartedAt:        at, CompletedAt: at, Attempts: 1,
		InputDigest:  contextfabric.DigestModelValue([]byte(prompt)),
		OutputDigest: contextfabric.DigestModelValue(supplied.Output),
		RequestID:    request.RequestID,
	}

	output, schemaErrorType, err := s.decode(supplied.Output)
	if err != nil {
		decision.schemaErrorType = schemaErrorType
		rejection := contextfabric.NewInterpretationRejection(contextfabric.InterpretationRejectionUnclassified,
			fmt.Errorf("%w: %w: %w", contextfabric.ErrInterpretationRejected, contextfabric.ErrModelOutput, err))
		return s.reject(&receipt, &decision, rejection)
	}
	interpreted, err := output.toDomain(request.TimeContext)
	if err != nil {
		// The schema lists the fact kinds, so an out-of-vocabulary kind never
		// reaches this validator: there is no raw kind to carry.
		return s.reject(&receipt, &decision, contextfabric.ClassifyInterpretationRejection(interpreted, err, nil))
	}
	receipt.Outcome = "success"
	applyInterpretationCaptures(&receipt, output)
	return interpreted, receipt, nil
}

func (s *SuppliedInterpreter) reject(receipt *contextfabric.ModelExecutionReceipt, decision *suppliedDecision, rejection error) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	reason := contextfabric.InterpretationRejectionReasonOf(rejection)
	receipt.Outcome = "invalid_output"
	receipt.InterpretationRejectionReason = reason
	decision.rejectionReason = string(reason)
	return contextfabric.InterpretedQuestion{}, *receipt, rejection
}

func (s *SuppliedInterpreter) contractMismatch(supplied contextfabric.SuppliedInterpretation) []string {
	var mismatch []string
	if supplied.ModelOutputVersion != s.contract.ModelOutputVersion {
		mismatch = append(mismatch, contractsv1.ContextFabricInterpretationContractFieldModelOutputVersion)
	}
	if supplied.PromptVersion != s.contract.PromptVersion {
		mismatch = append(mismatch, contractsv1.ContextFabricInterpretationContractFieldPromptVersion)
	}
	if supplied.SystemSHA256 != "" && supplied.SystemSHA256 != s.contract.SystemSHA256 {
		mismatch = append(mismatch, contractsv1.ContextFabricInterpretationContractFieldSystemSHA256)
	}
	return mismatch
}

// decode checks raw against the model output schema and decodes it with
// unknown fields refused. The returned error text is fixed: it never carries
// a value or a key the caller wrote. schemaErrorType is the validator's own
// name for the first failed rule.
func (s *SuppliedInterpreter) decode(raw []byte) (interpretationOutput, string, error) {
	result, err := s.schema.Validate(gojsonschema.NewBytesLoader(raw))
	if err != nil {
		return interpretationOutput{}, suppliedSchemaErrorUndecoded, errSuppliedOutputSchema
	}
	if !result.Valid() {
		schemaErrorType := suppliedSchemaErrorUndecoded
		if failures := result.Errors(); len(failures) > 0 {
			schemaErrorType = failures[0].Type()
		}
		return interpretationOutput{}, schemaErrorType, errSuppliedOutputSchema
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var output interpretationOutput
	if err := decoder.Decode(&output); err != nil {
		return interpretationOutput{}, suppliedSchemaErrorUndecoded, errSuppliedOutputSchema
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return interpretationOutput{}, suppliedSchemaErrorUndecoded, errSuppliedOutputSchema
	}
	return output, "", nil
}

type suppliedDecision struct {
	outcome          string
	clientModel      string
	contractMismatch []string
	schemaErrorType  string
	rejectionReason  string
}

// logDecision emits the declared decision line of one supplied
// interpretation. The model path's own decision line names
// interpretation_source=server; this one names client.
func (s *SuppliedInterpreter) logDecision(ctx context.Context, orgID, requestID string, decision suppliedDecision) {
	mismatch := decision.contractMismatch
	if mismatch == nil {
		mismatch = []string{}
	}
	fields := eventspec.NewSuppliedInterpretationDecisionFields(
		requestID, decisionOrgIDHash(orgID), string(contextfabric.InterpretationSourceClient), decision.outcome,
		decision.clientModel, s.contract.PromptVersion, s.contract.ModelOutputVersion,
		mismatch, decision.schemaErrorType, decision.rejectionReason,
	)
	s.config.Logger.InfoContext(ctx, eventspec.SuppliedInterpretationDecision.Msg, fields.SlogArgs()...)
}
