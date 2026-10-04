package contextfabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type (
	SynthesisMode   = contractsv1.ContextFabricSynthesisMode
	SynthesisSource = contractsv1.ContextFabricSynthesisSource
)

const (
	SynthesisModeServer   = contractsv1.ContextFabricSynthesisModeServer
	SynthesisModeClient   = contractsv1.ContextFabricSynthesisModeClient
	SynthesisSourceServer = contractsv1.ContextFabricSynthesisSourceServer
	SynthesisSourceClient = contractsv1.ContextFabricSynthesisSourceClient
)

// ClientSynthesisDecisionLogMessage is the msg of the client synthesis decision line.
const ClientSynthesisDecisionLogMessage = "context fabric client synthesis decision"

// ErrClientSynthesisUnavailable refuses a turn that asked to write its own
// answer when this deployment cannot hand the caller a synthesis input.
var ErrClientSynthesisUnavailable = errors.New("client synthesis is not available on this deployment")

// ClientSynthesisOutcome is the closed outcome of one client synthesis decision.
type ClientSynthesisOutcome string

const (
	ClientSynthesisServed        ClientSynthesisOutcome = "served"
	ClientSynthesisUnavailable   ClientSynthesisOutcome = "unavailable"
	ClientSynthesisInputTooLarge ClientSynthesisOutcome = "input_too_large"
)

// ClientSynthesisDecisionEvent is one client synthesis decision. Counts and
// closed labels only.
type ClientSynthesisDecisionEvent struct {
	Outcome          ClientSynthesisOutcome
	Status           InvestigationStatus
	BundleBytes      int
	MaxBytes         int
	Bounded          bool
	FactsRead        int
	FactsGiven       int
	CommitsRetracted int
}

// ClientSynthesisComposer is a Synthesizer that can serve a turn without
// calling a model: it composes the result from the facts and hands back the
// synthesis input the caller's model writes the answer from.
type ClientSynthesisComposer interface {
	ComposeForClientSynthesis(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, *contractsv1.ContextFabricSynthesisInput, error)
}

// clientSynthesisAvailability is implemented by a composer that can be wired
// without the means to build a synthesis input.
type clientSynthesisAvailability interface {
	ClientSynthesisAvailable() bool
}

// clientSynthesisMeasuredComposer is the composer's own form that also
// reports what the input was fitted to, for the decision line.
type clientSynthesisMeasuredComposer interface {
	composeClientSynthesisMeasured(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, *contractsv1.ContextFabricSynthesisInput, clientSynthesisMeasure, error)
}

type clientSynthesisMeasure struct {
	MaxBytes   int
	FactsRead  int
	FactsGiven int
	Bounded    bool
}

// ClientSynthesisAssembly is what a RuntimeAnswerSynthesizer needs to build a
// synthesis input. Encode is injected because the prompt package imports this one.
type ClientSynthesisAssembly struct {
	PromptVersion      string
	ModelOutputVersion string
	SystemSHA256       string
	Rules              []string
	MaxBytes           int
	Encode             func(orgID string, input SynthesisInput, maxBytes int) ([]byte, error)
	// ParseDraft turns the raw output a caller wrote into a draft. It makes
	// no model call. Nil refuses a request that carries a supplied synthesis.
	ParseDraft func(raw []byte) (SynthesisDraft, error)
}

func clientSynthesisRequested(request InvestigationRequest) bool {
	return request.SynthesisMode == SynthesisModeClient
}

// clientSynthesisWithoutDraft is a client synthesis turn that carries no
// draft: the one kind of turn whose answer is the fixed client text.
func clientSynthesisWithoutDraft(request InvestigationRequest) bool {
	return clientSynthesisRequested(request) && request.SuppliedSynthesis == nil
}

// suppliedSynthesisAvailability is the form of the availability interface
// that also names the synthesis contract a supplied synthesis must match.
type suppliedSynthesisAvailability interface {
	clientSynthesisAvailability
	suppliedSynthesisContract() (contractsv1.ContextFabricSynthesisContract, bool)
}

func (r RuntimeAnswerSynthesizer) suppliedSynthesisContract() (contractsv1.ContextFabricSynthesisContract, bool) {
	if !r.ClientSynthesisAvailable() || r.ClientSynthesis.ParseDraft == nil {
		return contractsv1.ContextFabricSynthesisContract{}, false
	}
	assembly := r.ClientSynthesis
	return contractsv1.ContextFabricSynthesisContract{
		ModelOutputVersion: assembly.ModelOutputVersion, PromptVersion: assembly.PromptVersion, SystemSHA256: assembly.SystemSHA256,
	}, true
}

// ClientSynthesisAvailable reports whether the synthesizer can build a
// synthesis input.
func (r RuntimeAnswerSynthesizer) ClientSynthesisAvailable() bool {
	return r.ClientSynthesis != nil && r.ClientSynthesis.Encode != nil
}

type synthesisInputCollectorKey struct{}

type synthesisInputCollector struct {
	mu     sync.Mutex
	bundle *contractsv1.ContextFabricSynthesisInput
}

// WithSynthesisInputCollector installs the place Investigate delivers the
// synthesis input of a client synthesis turn. The returned func gives the
// delivered input, or nil when none was.
func WithSynthesisInputCollector(ctx context.Context) (context.Context, func() *contractsv1.ContextFabricSynthesisInput) {
	collector := &synthesisInputCollector{}
	return context.WithValue(ctx, synthesisInputCollectorKey{}, collector), func() *contractsv1.ContextFabricSynthesisInput {
		collector.mu.Lock()
		defer collector.mu.Unlock()
		return collector.bundle
	}
}

func synthesisInputCollectorFrom(ctx context.Context) *synthesisInputCollector {
	collector, _ := ctx.Value(synthesisInputCollectorKey{}).(*synthesisInputCollector)
	return collector
}

func deliverSynthesisInput(ctx context.Context, bundle *contractsv1.ContextFabricSynthesisInput) {
	collector := synthesisInputCollectorFrom(ctx)
	if collector == nil || bundle == nil {
		return
	}
	collector.mu.Lock()
	collector.bundle = bundle
	collector.mu.Unlock()
}

// checkClientSynthesis is the entry gate of a client synthesis turn: it
// fails closed unless the synthesis input can be built and delivered.
func (e *Engine) checkClientSynthesis(ctx context.Context) error {
	composer, ok := e.synthesizer.(ClientSynthesisComposer)
	if !ok {
		return ErrClientSynthesisUnavailable
	}
	if availability, ok := composer.(clientSynthesisAvailability); ok && !availability.ClientSynthesisAvailable() {
		return ErrClientSynthesisUnavailable
	}
	if synthesisInputCollectorFrom(ctx) == nil {
		return ErrClientSynthesisUnavailable
	}
	return nil
}

func (e *Engine) recordClientSynthesisDecision(ctx context.Context, principal storage.Principal, event ClientSynthesisDecisionEvent) {
	if e.telemetry != nil {
		e.telemetry.RecordClientSynthesisDecision(ctx, principal, event)
	}
}

// runClientSynthesis runs the client form of the synthesis step and
// reports the decision line for a pass that ends the turn.
func (e *Engine) runClientSynthesis(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, *contractsv1.ContextFabricSynthesisInput, clientSynthesisMeasure, error) {
	var (
		result  InvestigationResult
		bundle  *contractsv1.ContextFabricSynthesisInput
		measure clientSynthesisMeasure
		err     error
	)
	switch composer := e.synthesizer.(type) {
	case clientSynthesisMeasuredComposer:
		result, bundle, measure, err = composer.composeClientSynthesisMeasured(ctx, principal, input)
	case ClientSynthesisComposer:
		result, bundle, err = composer.ComposeForClientSynthesis(ctx, principal, input)
		measure = clientSynthesisMeasure{FactsRead: len(input.Facts.Facts), FactsGiven: len(input.Facts.Facts)}
		if bundle != nil {
			measure.Bounded = bundle.Bounded
		}
	default:
		err = ErrClientSynthesisUnavailable
	}
	writeBack := input.Request.SuppliedSynthesis != nil
	if err == nil && bundle == nil && !writeBack {
		err = ErrClientSynthesisUnavailable
	}
	if err != nil && writeBack {
		e.recordSuppliedSynthesisRefusal(ctx, principal, input.Request, err)
	} else if err != nil {
		event := ClientSynthesisDecisionEvent{MaxBytes: measure.MaxBytes, Bounded: measure.Bounded, FactsRead: measure.FactsRead, FactsGiven: measure.FactsGiven}
		switch {
		case errors.Is(err, ErrModelInputTooLarge):
			event.Outcome = ClientSynthesisInputTooLarge
		case errors.Is(err, ErrClientSynthesisUnavailable):
			event.Outcome = ClientSynthesisUnavailable
		}
		if event.Outcome != "" {
			e.recordClientSynthesisDecision(ctx, principal, event)
		}
	}
	return result, bundle, measure, err
}

// encodeWithinInputBound builds the synthesis input and, when it does not fit
// the bound, reduces the facts and builds it again. It returns the input the
// bytes were built from, so a later change can check a caller's draft
// against the same facts.
func (r RuntimeAnswerSynthesizer) encodeWithinInputBound(ctx context.Context, principal storage.Principal, input SynthesisInput, encode func(string, SynthesisInput, int) ([]byte, error), maxBytes int) (given SynthesisInput, encoded []byte, bounded bool, err error) {
	given = input
	var event SynthesisInputBoundEvent
	ranking := newFactRanking(input)
	for {
		encoded, err = encode(principal.OrgID, given, maxBytes)
		var overflow *ModelInputOverflow
		if !errors.As(err, &overflow) {
			if event.Passes > 0 {
				event.Outcome = SynthesisInputBoundFitted
				r.recordSynthesisInputBound(ctx, principal, event, input, given)
			}
			return given, encoded, event.Passes > 0, err
		}
		if event.Passes == 0 {
			event.InputBytes, event.MaxInputBytes = overflow.Bytes, overflow.MaxBytes
		}
		reducedFacts, reduced, selection := boundSynthesisFacts(given.Facts.Facts, ranking, overflow)
		if selection == SynthesisInputSelectionRelevance || event.Selection == "" {
			event.Selection = selection
		}
		if !reduced || event.Passes == maxSynthesisInputBoundPasses {
			event.Outcome = SynthesisInputBoundExhausted
			r.recordSynthesisInputBound(ctx, principal, event, input, given)
			return given, nil, false, err
		}
		event.Passes++
		given.Facts.Facts = reducedFacts
	}
}

// clientSynthesisDraft is the draft of a turn the caller writes: no model
// authored field and no warning. It is partial when anything was read.
func clientSynthesisDraft(given SynthesisInput) SynthesisDraft {
	status := InvestigationNoMatch
	if len(given.Facts.Facts) > 0 || len(given.Graph.Paths) > 0 {
		status = InvestigationPartial
	}
	return SynthesisDraft{
		Status:             status,
		StrongestPressures: []string{},
		Drivers:            []DriverJudgment{},
		RemainingWork:      []Finding{},
		ReadinessGaps:      []Finding{},
		Conflicts:          []Finding{},
		Limitations:        make([]string, 0),
		EvidenceRefIDs:     []string{},
		ClaimedFacts:       []ClaimedFact{},
		Warnings:           []string{},
	}
}

// ComposeForClientSynthesis implements ClientSynthesisComposer.
func (r RuntimeAnswerSynthesizer) ComposeForClientSynthesis(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, *contractsv1.ContextFabricSynthesisInput, error) {
	result, bundle, _, err := r.composeClientSynthesisMeasured(ctx, principal, input)
	return result, bundle, err
}

func (r RuntimeAnswerSynthesizer) composeClientSynthesisMeasured(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, *contractsv1.ContextFabricSynthesisInput, clientSynthesisMeasure, error) {
	measure := clientSynthesisMeasure{FactsRead: len(input.Facts.Facts)}
	if !r.ClientSynthesisAvailable() {
		return InvestigationResult{}, nil, measure, ErrClientSynthesisUnavailable
	}
	assembly := r.ClientSynthesis
	measure.MaxBytes = assembly.MaxBytes
	given, encoded, bounded, err := r.encodeWithinInputBound(ctx, principal, input, assembly.Encode, assembly.MaxBytes)
	measure.FactsGiven, measure.Bounded = len(given.Facts.Facts), bounded
	if err != nil {
		return InvestigationResult{}, nil, measure, err
	}
	sum := sha256.Sum256(encoded)
	inputSHA := hex.EncodeToString(sum[:])
	if supplied := input.Request.SuppliedSynthesis; supplied != nil {
		result, err := r.composeSuppliedSynthesis(ctx, principal, given, encoded, bounded, inputSHA, supplied)
		return result, nil, measure, err
	}
	draft := clientSynthesisDraft(given)
	result, err := r.composeSynthesisResult(ctx, principal, given, draft, ModelExecutionReceipt{}, bounded, false)
	if err != nil {
		return InvestigationResult{}, nil, measure, err
	}
	result.Versions.SynthesisVersion = SynthesisVersionNotSynthesized
	result.Versions.ModelIdentity = unwiredVersion
	result.Versions.SynthesisSource = SynthesisSourceClient
	stampClientInterpretationVersion(ctx, &result.Versions)
	if draft.Status == InvestigationPartial {
		result.DirectJudgment = contractsv1.ContextFabricClientSynthesisAnswer
		result.CurrentState = contractsv1.ContextFabricClientSynthesisAnswer
		result.DeterministicAnswer = contractsv1.ContextFabricClientSynthesisAnswer
	}
	bundle, err := assembly.newInputBundle(encoded, inputSHA, bounded)
	if err != nil {
		return InvestigationResult{}, nil, measure, err
	}
	return result, bundle, measure, nil
}

func stampClientInterpretationVersion(ctx context.Context, versions *VersionSet) {
	versions.InterpretationVersion = unwiredVersion
	if stamp, ok := interpretationStampFrom(ctx); ok && stamp.InterpretationVersion != "" {
		versions.InterpretationVersion = stamp.InterpretationVersion
	}
}

func (a *ClientSynthesisAssembly) newInputBundle(encoded []byte, inputSHA string, bounded bool) (*contractsv1.ContextFabricSynthesisInput, error) {
	bundle := &contractsv1.ContextFabricSynthesisInput{
		Contract: contractsv1.ContextFabricSynthesisContract{
			ModelOutputVersion: a.ModelOutputVersion,
			PromptVersion:      a.PromptVersion,
			SystemSHA256:       a.SystemSHA256,
		},
		Input:       encoded,
		InputSHA256: inputSHA,
		Bounded:     bounded,
		Rules:       append([]string(nil), a.Rules...),
	}
	if err := bundle.Validate(); err != nil {
		return nil, fmt.Errorf("client synthesis input: %w", err)
	}
	return bundle, nil
}

// SynthesisInputChanged refuses a supplied synthesis that was written from an
// input this turn no longer builds. Input is the input to write from now.
type SynthesisInputChanged struct {
	Input *contractsv1.ContextFabricSynthesisInput
}

func (e *SynthesisInputChanged) Error() string {
	return "supplied synthesis was written from an input that has changed"
}

// SynthesisContractMismatch refuses a supplied synthesis that was written
// under a contract this service does not run, or that does not name its
// input. Current holds only service-owned values.
type SynthesisContractMismatch struct {
	Mismatch []string
	Current  contractsv1.ContextFabricSynthesisContract
}

func (e *SynthesisContractMismatch) Error() string {
	return "supplied synthesis contract mismatch: " + strings.Join(e.Mismatch, ",")
}

// ErrSuppliedInterpretationRequired refuses a supplied synthesis that comes
// without a supplied interpretation.
var ErrSuppliedInterpretationRequired = errors.New("a supplied synthesis requires a supplied interpretation")

// groundingEvaluatorVersion is the evaluator version the service's own model
// path records on a synthesis receipt.
const (
	groundingEvaluatorVersion     = "context-fabric-grounding.v1"
	suppliedSynthesisModelVersion = "n/a"
)

func declaredSynthesisModel(supplied *contractsv1.ContextFabricSuppliedSynthesis) string {
	if supplied.ClientModel == "" {
		return contractsv1.ContextFabricClientModelUndeclared
	}
	return supplied.ClientModel
}

// composeSuppliedSynthesis checks a caller's draft against the input this turn
// rebuilt and composes the served result from it. It makes no model call.
func (r RuntimeAnswerSynthesizer) composeSuppliedSynthesis(ctx context.Context, principal storage.Principal, given SynthesisInput, encoded []byte, bounded bool, inputSHA string, supplied *contractsv1.ContextFabricSuppliedSynthesis) (InvestigationResult, error) {
	assembly := r.ClientSynthesis
	if assembly.ParseDraft == nil {
		return InvestigationResult{}, ErrClientSynthesisUnavailable
	}
	if inputSHA != supplied.InputSHA256 {
		bundle, err := assembly.newInputBundle(encoded, inputSHA, bounded)
		if err != nil {
			return InvestigationResult{}, err
		}
		return InvestigationResult{}, &SynthesisInputChanged{Input: bundle}
	}
	at := time.Now().UTC()
	receipt := ModelExecutionReceipt{
		Operation: ModelOperationSynthesize,
		Provider:  contractsv1.ContextFabricClientSuppliedProvider, Model: declaredSynthesisModel(supplied), ModelVersion: suppliedSynthesisModelVersion,
		PromptVersion: supplied.PromptVersion, SchemaVersion: supplied.ModelOutputVersion, EvaluatorVersion: groundingEvaluatorVersion,
		StartedAt: at, CompletedAt: at, Attempts: 1,
		InputDigest:  DigestModelValue(encoded),
		OutputDigest: DigestModelValue(supplied.Output),
		RequestID:    given.Request.RequestID,
	}
	draft, err := assembly.ParseDraft(supplied.Output)
	if err != nil {
		err = asRejectedSuppliedDraft(err)
	} else {
		draft, err = r.vetSynthesisDraft(ctx, principal, given, draft)
	}
	receipt.Outcome = "success"
	if err != nil {
		receipt.Outcome = "invalid_output"
	}
	if sinkErr := recordModelReceipt(ctx, principal, r.Sink, receipt); sinkErr != nil {
		return InvestigationResult{}, errors.Join(err, sinkErr)
	}
	if err != nil {
		return InvestigationResult{}, err
	}
	result, err := r.composeSynthesisResult(ctx, principal, given, draft, receipt, bounded, false)
	if err != nil {
		return InvestigationResult{}, err
	}
	result.Versions.SynthesisSource = SynthesisSourceClient
	result.Versions.SynthesisVersion = assembly.PromptVersion
	stampClientInterpretationVersion(ctx, &result.Versions)
	return result, nil
}

// asRejectedSuppliedDraft makes a draft the caller's parser refused a
// rejected draft: it carries the closed reason and the rejection sentinels.
func asRejectedSuppliedDraft(err error) error {
	if errors.Is(err, ErrSynthesisRejected) {
		return err
	}
	reason := SynthesisRejectionReasonOf(err)
	if reason == RejectionReasonUnclassified {
		reason = RejectionReasonOutputSchemaMismatch
	}
	return NewSynthesisRejection(reason, fmt.Errorf("%w: %w: %w", ErrSynthesisRejected, ErrModelOutput, err))
}

// SuppliedSynthesisOutcome is the closed outcome of one supplied-synthesis decision.
type SuppliedSynthesisOutcome string

const (
	SuppliedSynthesisServed                 SuppliedSynthesisOutcome = "served"
	SuppliedSynthesisContractMismatch       SuppliedSynthesisOutcome = "contract_mismatch"
	SuppliedSynthesisInterpretationRequired SuppliedSynthesisOutcome = "interpretation_required"
	SuppliedSynthesisInputChangedOutcome    SuppliedSynthesisOutcome = "input_changed"
	SuppliedSynthesisRejected               SuppliedSynthesisOutcome = "rejected"
	SuppliedSynthesisUnavailable            SuppliedSynthesisOutcome = "unavailable"
)

// SuppliedSynthesisDecisionLogMessage is the msg of the supplied synthesis decision line.
const SuppliedSynthesisDecisionLogMessage = "context fabric supplied synthesis decision"

// SuppliedSynthesisDecisionEvent is one supplied synthesis decision. Closed
// labels, field names of the contract and a byte count only: nothing the
// caller wrote.
type SuppliedSynthesisDecisionEvent struct {
	Outcome         SuppliedSynthesisOutcome
	Mismatch        []string
	RejectionReason SynthesisRejectionReason
	ClientModel     string
	OutputBytes     int
}

func (e *Engine) recordSuppliedSynthesisDecision(ctx context.Context, principal storage.Principal, event SuppliedSynthesisDecisionEvent) {
	if e.telemetry != nil {
		e.telemetry.RecordSuppliedSynthesisDecision(ctx, principal, event)
	}
}

func suppliedSynthesisEvent(request InvestigationRequest, outcome SuppliedSynthesisOutcome) SuppliedSynthesisDecisionEvent {
	supplied := request.SuppliedSynthesis
	return SuppliedSynthesisDecisionEvent{Outcome: outcome, ClientModel: declaredSynthesisModel(supplied), OutputBytes: len(supplied.Output)}
}

// recordSuppliedSynthesisRefusal writes the decision line of a write-back
// that ended without a result. An error outside these two outcomes writes none:
// the entry gate already refused an unavailable deployment.
func (e *Engine) recordSuppliedSynthesisRefusal(ctx context.Context, principal storage.Principal, request InvestigationRequest, err error) {
	var changed *SynthesisInputChanged
	switch {
	case errors.As(err, &changed):
		e.recordSuppliedSynthesisDecision(ctx, principal, suppliedSynthesisEvent(request, SuppliedSynthesisInputChangedOutcome))
	case errors.Is(err, ErrSynthesisRejected):
		event := suppliedSynthesisEvent(request, SuppliedSynthesisRejected)
		event.RejectionReason = SynthesisRejectionReasonOf(err)
		e.recordSuppliedSynthesisDecision(ctx, principal, event)
	}
}

// checkSuppliedSynthesis is the engine's entry gate for a request that
// carries a supplied synthesis, above every read and every save.
func (e *Engine) checkSuppliedSynthesis(ctx context.Context, principal storage.Principal, request InvestigationRequest) error {
	refuse := func(outcome SuppliedSynthesisOutcome, mismatch []string, err error) error {
		event := suppliedSynthesisEvent(request, outcome)
		event.Mismatch = mismatch
		e.recordSuppliedSynthesisDecision(ctx, principal, event)
		return err
	}
	if err := e.checkClientSynthesis(ctx); err != nil {
		return refuse(SuppliedSynthesisUnavailable, nil, err)
	}
	composer, ok := e.synthesizer.(suppliedSynthesisAvailability)
	if !ok {
		return refuse(SuppliedSynthesisUnavailable, nil, ErrClientSynthesisUnavailable)
	}
	current, ok := composer.suppliedSynthesisContract()
	if !ok {
		return refuse(SuppliedSynthesisUnavailable, nil, ErrClientSynthesisUnavailable)
	}
	if request.SuppliedInterpretation == nil {
		return refuse(SuppliedSynthesisInterpretationRequired, nil, ErrSuppliedInterpretationRequired)
	}
	supplied := request.SuppliedSynthesis
	var mismatch []string
	if supplied.ModelOutputVersion != current.ModelOutputVersion {
		mismatch = append(mismatch, contractsv1.ContextFabricSynthesisContractFieldModelOutputVersion)
	}
	if supplied.PromptVersion != current.PromptVersion {
		mismatch = append(mismatch, contractsv1.ContextFabricSynthesisContractFieldPromptVersion)
	}
	if supplied.SystemSHA256 != current.SystemSHA256 {
		mismatch = append(mismatch, contractsv1.ContextFabricSynthesisContractFieldSystemSHA256)
	}
	if supplied.InputSHA256 == "" {
		mismatch = append(mismatch, contractsv1.ContextFabricSynthesisContractFieldInputSHA256)
	}
	if len(mismatch) > 0 {
		return refuse(SuppliedSynthesisContractMismatch, mismatch, &SynthesisContractMismatch{Mismatch: mismatch, Current: current})
	}
	return nil
}

// RecordSuppliedSynthesisDecision implements EngineTelemetry.
func (t SlogEngineTelemetry) RecordSuppliedSynthesisDecision(ctx context.Context, principal storage.Principal, event SuppliedSynthesisDecisionEvent) {
	args := append([]any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"outcome", SanitizeLogAttr(string(event.Outcome)),
		"contract_mismatch", SanitizeLogStrings(nonNilStrings(event.Mismatch)),
		"rejection_reason", SanitizeLogAttr(string(event.RejectionReason)),
		"client_model", SanitizeLogAttr(event.ClientModel),
		"output_bytes", event.OutputBytes,
	}, requestIDLogAttrs(ctx)...)
	t.logger.Log(ctx, slog.LevelInfo, SuppliedSynthesisDecisionLogMessage, args...)
}

// RecordClientSynthesisDecision implements EngineTelemetry.
func (t SlogEngineTelemetry) RecordClientSynthesisDecision(ctx context.Context, principal storage.Principal, event ClientSynthesisDecisionEvent) {
	args := append([]any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"outcome", SanitizeLogAttr(string(event.Outcome)),
		"status", SanitizeLogAttr(string(event.Status)),
		"bundle_bytes", event.BundleBytes,
		"max_bytes", event.MaxBytes,
		"bounded", event.Bounded,
		"facts_read", event.FactsRead,
		"facts_given", event.FactsGiven,
		"commits_retracted", event.CommitsRetracted,
	}, requestIDLogAttrs(ctx)...)
	t.logger.Log(ctx, slog.LevelInfo, ClientSynthesisDecisionLogMessage, args...)
}
