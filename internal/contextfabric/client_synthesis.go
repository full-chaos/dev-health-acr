package contextfabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"

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
}

func clientSynthesisRequested(request InvestigationRequest) bool {
	return request.SynthesisMode == SynthesisModeClient
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
	if err == nil && bundle == nil {
		err = ErrClientSynthesisUnavailable
	}
	if err != nil {
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
	draft := clientSynthesisDraft(given)
	result, err := r.composeSynthesisResult(ctx, principal, given, draft, ModelExecutionReceipt{}, bounded, false)
	if err != nil {
		return InvestigationResult{}, nil, measure, err
	}
	result.Versions.SynthesisVersion = SynthesisVersionNotSynthesized
	result.Versions.ModelIdentity = unwiredVersion
	result.Versions.SynthesisSource = SynthesisSourceClient
	result.Versions.InterpretationVersion = unwiredVersion
	if stamp, ok := interpretationStampFrom(ctx); ok && stamp.InterpretationVersion != "" {
		result.Versions.InterpretationVersion = stamp.InterpretationVersion
	}
	if draft.Status == InvestigationPartial {
		result.DirectJudgment = contractsv1.ContextFabricClientSynthesisAnswer
		result.CurrentState = contractsv1.ContextFabricClientSynthesisAnswer
		result.DeterministicAnswer = contractsv1.ContextFabricClientSynthesisAnswer
	}
	sum := sha256.Sum256(encoded)
	bundle := &contractsv1.ContextFabricSynthesisInput{
		Contract: contractsv1.ContextFabricSynthesisContract{
			ModelOutputVersion: assembly.ModelOutputVersion,
			PromptVersion:      assembly.PromptVersion,
			SystemSHA256:       assembly.SystemSHA256,
		},
		Input:       encoded,
		InputSHA256: hex.EncodeToString(sum[:]),
		Bounded:     bounded,
		Rules:       append([]string(nil), assembly.Rules...),
	}
	if err := bundle.Validate(); err != nil {
		return InvestigationResult{}, nil, measure, fmt.Errorf("client synthesis input: %w", err)
	}
	return result, bundle, measure, nil
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
