package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type EngineOptions struct {
	ServiceVersion string
	Now            func() time.Time
	NewResultID    func() string
}

type EngineDependencies struct {
	Interpreter QuestionInterpreter
	Graph       GraphReader
	Facts       CanonicalFactReader
	Synthesizer AnswerSynthesizer
	Results     InvestigationResultStore
	// Telemetry is optional. When set, Engine reports content-safe
	// operational counters through it -- see EngineTelemetry.
	Telemetry EngineTelemetry
}

// EngineTelemetry receives content-safe operational counters from Engine.
// Implementations must record only counts and fixed classifications --
// never question text, subject labels, canonical IDs, result IDs, or any
// other investigation content -- so a signal is diagnosable without
// becoming a new disclosure surface.
type EngineTelemetry interface {
	// RecordPriorSubjectReceiptsSkipped reports how many of one
	// Investigate call's PriorSubjectReceipts did not end up bound to a
	// resolved subject -- whether because the referenced prior result
	// could not be loaded, no candidate in it matched the receipt, or the
	// resolved subject did not survive current authorization/graph
	// resolution. Investigate never errors or otherwise surfaces this to
	// the caller (a stale, foreign, or now-unauthorized receipt degrades
	// silently), so this count is the only operator-visible signal that it
	// happened.
	RecordPriorSubjectReceiptsSkipped(ctx context.Context, principal storage.Principal, skipped int)
}

// Engine coordinates one open-ended investigation. It deliberately composes
// capabilities rather than matching the question against a route/plan table.
type Engine struct {
	interpreter    QuestionInterpreter
	graph          GraphReader
	facts          CanonicalFactReader
	synthesizer    AnswerSynthesizer
	results        InvestigationResultStore
	telemetry      EngineTelemetry
	serviceVersion string
	now            func() time.Time
	newResultID    func() string
}

func NewEngine(dependencies EngineDependencies, options EngineOptions) (*Engine, error) {
	if dependencies.Interpreter == nil || dependencies.Graph == nil || dependencies.Facts == nil || dependencies.Synthesizer == nil {
		return nil, errors.New("context fabric engine requires interpreter, graph, facts, and synthesizer")
	}
	if strings.TrimSpace(options.ServiceVersion) == "" {
		return nil, errors.New("context fabric engine service version is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewResultID == nil {
		return nil, errors.New("context fabric engine result ID generator is required")
	}
	return &Engine{
		interpreter: dependencies.Interpreter, graph: dependencies.Graph, facts: dependencies.Facts,
		synthesizer: dependencies.Synthesizer, results: dependencies.Results, telemetry: dependencies.Telemetry,
		serviceVersion: options.ServiceVersion, now: options.Now, newResultID: options.NewResultID,
	}, nil
}

func (e *Engine) Investigate(ctx context.Context, principal storage.Principal, request InvestigationRequest) (InvestigationResult, error) {
	if err := request.Validate(); err != nil {
		return InvestigationResult{}, fmt.Errorf("investigation request: %w", err)
	}
	if strings.TrimSpace(principal.OrgID) == "" {
		return InvestigationResult{}, errors.New("authenticated organization is required")
	}
	// Refuse historical/point-in-time questions before doing any work.
	// Every canonical fact source behind this engine reads current state
	// only, so continuing would answer the caller's question with data
	// that does not correspond to the time they asked about. See
	// ErrUnsupportedTimeAxis.
	//
	// This is the FIRST of two checks. It rejects what the caller asked
	// for on the wire; the second (below, after Interpret) rejects what
	// the question was understood to mean. Both are required -- see the
	// second check's comment for why this one alone is not enough.
	if err := requireCurrentTimeAxis(request.TimeContext.Axis); err != nil {
		return InvestigationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return InvestigationResult{}, err
	}

	interpretation, err := e.interpreter.Interpret(ctx, principal, request)
	if err != nil {
		return InvestigationResult{}, fmt.Errorf("interpret question: %w", err)
	}
	// Re-check the axis on the INTERPRETED question, not just the wire
	// request (CHAOS-3755 codex delta review, P2).
	//
	// Interpretation may legitimately change the axis: a caller can send
	// axis=current while the question itself is historical ("what was the
	// status last month"), and a QuestionInterpreter is expected to
	// recognize that and set valid_time. The wire-level check above
	// cannot see this -- it ran before the question was understood -- so
	// on its own it lets an interpreted-historical investigation run the
	// graph, the fact reads, and synthesis, and answer with current data.
	// That is the exact false-historical-answer this refusal exists to
	// prevent, reached by a different door.
	//
	// The invariant belongs HERE rather than in any QuestionInterpreter
	// implementation: clamping a model's axis inside the runtime adapter
	// would silently rewrite the question into one the caller never
	// asked, and the next interpreter implementation would reopen the
	// hole. The engine owns what it can honestly answer.
	//
	// Placed before prior-receipt expansion and every capability call, so
	// a refused investigation does no graph or fact work at all.
	if err := requireCurrentTimeAxis(interpretation.TimeContext.Axis); err != nil {
		return InvestigationResult{}, err
	}
	// Prior-result receipts (PriorSubjectReceipts) name a subject already
	// committed or proposed in an earlier InvestigationResult -- e.g. a
	// conversational follow-up ("what about it") binding back to the
	// subject a prior turn resolved. A receipt is a one-way identifier
	// (ReceiptID), not itself a resolvable subject: only the Engine holds
	// the InvestigationResultStore needed to look one up, so expansion
	// happens here rather than inside GraphReader. The expanded request
	// feeds the exact-hint path GraphReader already has (SubjectHint), so
	// every resolved receipt is independently re-authorized before it can
	// become a candidate -- a stale, foreign, or now-unauthorized receipt
	// is skipped, never trusted outright, and never treated as an error.
	graphRequest := request
	var priorHints []SubjectHint
	if e.results != nil && len(request.PriorSubjectReceipts) > 0 {
		priorHints = e.resolvePriorSubjectHints(ctx, principal, request.PriorSubjectReceipts)
		// The v1 contract bounds RequestedScope.SubjectHints at 50
		// (ContextFabricRequestedScope.Validate). request.Validate()
		// already proved the caller's own hints are within that bound,
		// but Engine's own expansion must not push the combined total
		// back out of it -- drop excess receipt-derived hints (never the
		// caller's own explicit hints), and let the existing skip
		// telemetry in recordPriorSubjectReceiptSkips below count the
		// drop exactly like any other unresolved receipt.
		const maxSubjectHints = 50
		if available := maxSubjectHints - len(request.RequestedScope.SubjectHints); len(priorHints) > available {
			if available < 0 {
				available = 0
			}
			priorHints = priorHints[:available]
		}
		if len(priorHints) > 0 {
			graphRequest.RequestedScope.SubjectHints = append(
				append([]SubjectHint(nil), request.RequestedScope.SubjectHints...), priorHints...,
			)
		}
	}
	resolution, err := e.graph.ResolveSubjects(ctx, principal, graphRequest, interpretation)
	if err != nil {
		return InvestigationResult{}, fmt.Errorf("resolve subjects: %w", err)
	}
	if len(request.PriorSubjectReceipts) > 0 {
		e.recordPriorSubjectReceiptSkips(ctx, principal, len(request.PriorSubjectReceipts), priorHints, resolution)
	}
	graphContext, err := e.graph.DiscoverContext(ctx, principal, GraphDiscoveryRequest{
		Request: graphRequest, Interpretation: interpretation, Resolution: resolution,
	})
	if err != nil {
		return InvestigationResult{}, fmt.Errorf("discover graph context: %w", err)
	}
	graphContext.Resolution = resolution

	factRequest := CanonicalFactRequest{
		Question:     interpretation,
		Subjects:     investigationSubjects(resolution, graphContext.Cohort),
		Cohort:       graphContext.Cohort,
		Requirements: mergeFactRequirements(interpretation.FactRequirements, graphContext.FactRequirements),
	}
	facts, err := e.facts.ReadFacts(ctx, principal, factRequest)
	if err != nil {
		return InvestigationResult{}, fmt.Errorf("read canonical facts: %w", err)
	}

	result, err := e.synthesizer.Synthesize(ctx, principal, SynthesisInput{
		Request: request, Interpretation: interpretation, Graph: graphContext, Facts: facts,
	})
	if err != nil {
		return InvestigationResult{}, fmt.Errorf("synthesize investigation: %w", err)
	}
	result.SchemaVersion = InvestigationResultSchemaV1
	result.ResultID = e.newResultID()
	result.RequestID = request.RequestID
	result.GeneratedAt = e.now().UTC()
	result.Question = request.Question
	result.Interpretation = interpretation
	result.SubjectResolution = resolution
	if result.Cohort == nil {
		result.Cohort = graphContext.Cohort
	}
	if strings.TrimSpace(result.Versions.ServiceVersion) == "" {
		result.Versions.ServiceVersion = e.serviceVersion
	}
	if strings.TrimSpace(result.Versions.ContractVersion) == "" {
		result.Versions.ContractVersion = InvestigationResultSchemaV1
	}
	if strings.TrimSpace(result.Versions.CanonicalServiceVersion) == "" {
		result.Versions.CanonicalServiceVersion = facts.Version
	}
	if err := result.Validate(); err != nil {
		return InvestigationResult{}, fmt.Errorf("%w: %v", ErrInvalidResult, err)
	}
	if e.results != nil {
		if err := e.results.Save(ctx, principal, result); err != nil {
			return InvestigationResult{}, fmt.Errorf("save investigation result: %w", err)
		}
	}
	return result, nil
}

// resolvePriorSubjectHints expands PriorSubjectReceipts into SubjectHints by
// loading each referenced prior InvestigationResult (deduplicated per
// ResultID) and matching ReceiptID against that result's
// SubjectResolution.Candidates. A receipt that fails to load (not found,
// unauthorized, unavailable) or has no matching candidate is silently
// skipped: an unresolvable prior-turn reference must degrade to "not bound"
// rather than fail the whole investigation or fall back to an unauthorized
// guess.
func (e *Engine) resolvePriorSubjectHints(ctx context.Context, principal storage.Principal, receipts []BoundSubjectReceipt) []SubjectHint {
	hints := make([]SubjectHint, 0, len(receipts))
	loaded := make(map[string]InvestigationResult, len(receipts))
	for _, receipt := range receipts {
		if ctx.Err() != nil {
			return hints
		}
		resultID := strings.TrimSpace(receipt.ResultID)
		receiptID := strings.TrimSpace(receipt.ReceiptID)
		if resultID == "" || receiptID == "" {
			continue
		}
		prior, ok := loaded[resultID]
		if !ok {
			fetched, err := e.results.Get(ctx, principal, resultID)
			if err != nil {
				continue
			}
			prior = fetched
			loaded[resultID] = prior
		}
		for _, candidate := range prior.SubjectResolution.Candidates {
			if candidate.ReceiptID != receiptID {
				continue
			}
			hints = append(hints, SubjectHint{
				Kind: candidate.Subject.Kind, ID: candidate.Subject.CanonicalID,
				Label: candidate.Subject.Label, Source: "prior_subject_receipt",
			})
			break
		}
	}
	return hints
}

// recordPriorSubjectReceiptSkips counts every PriorSubjectReceipt that did
// not end up bound to a resolved subject on this Investigate call and
// reports only that count (never receipt, result, or subject content)
// through the optional EngineTelemetry hook. A receipt counts as skipped
// both when resolvePriorSubjectHints already dropped it (unloadable prior
// result, no matching candidate) and when it became a hint but the subject
// did not survive graph resolution -- e.g. GraphReader's exact-hint
// authorization check silently rejected it, the same path an ordinary
// client-supplied SubjectHint goes through. Either way Investigate itself
// never errors or otherwise surfaces the skip, so this is the only
// operator-visible signal.
func (e *Engine) recordPriorSubjectReceiptSkips(ctx context.Context, principal storage.Principal, receiptCount int, priorHints []SubjectHint, resolution SubjectResolution) {
	if e.telemetry == nil {
		return
	}
	resolved := make(map[string]struct{}, len(resolution.Candidates)+len(resolution.Committed))
	for _, candidate := range resolution.Candidates {
		resolved[subjectKeyForModel(candidate.Subject)] = struct{}{}
	}
	for _, subject := range resolution.Committed {
		resolved[subjectKeyForModel(subject)] = struct{}{}
	}
	survived := 0
	for _, hint := range priorHints {
		if _, ok := resolved[string(hint.Kind)+"\x00"+hint.ID]; ok {
			survived++
		}
	}
	if skipped := receiptCount - survived; skipped > 0 {
		e.telemetry.RecordPriorSubjectReceiptsSkipped(ctx, principal, skipped)
	}
}

// requireCurrentTimeAxis is the single definition of what this engine can
// honestly answer, shared by the wire-request check and the
// post-interpretation check so the two can never diverge. Any axis other
// than current is refused with ErrUnsupportedTimeAxis, which the route
// maps to a non-retryable 400.
func requireCurrentTimeAxis(axis TemporalAxis) error {
	if axis == TemporalCurrent {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrUnsupportedTimeAxis, axis)
}

func investigationSubjects(resolution SubjectResolution, cohort *Cohort) []SubjectRef {
	seen := make(map[string]struct{})
	result := make([]SubjectRef, 0, len(resolution.Committed))
	appendSubject := func(subject SubjectRef) {
		key := string(subject.Kind) + "\x00" + subject.CanonicalID
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		result = append(result, subject)
	}
	for _, subject := range resolution.Committed {
		appendSubject(subject)
	}
	if cohort != nil {
		for _, member := range cohort.Members {
			appendSubject(member.Subject)
		}
	}
	return result
}

func mergeFactRequirements(groups ...[]FactRequirement) []FactRequirement {
	result := make([]FactRequirement, 0)
	seen := make(map[FactKind]struct{})
	for _, group := range groups {
		for _, requirement := range group {
			if _, exists := seen[requirement.Kind]; exists {
				continue
			}
			seen[requirement.Kind] = struct{}{}
			result = append(result, requirement)
		}
	}
	return result
}
