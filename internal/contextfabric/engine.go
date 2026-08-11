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
}

// Engine coordinates one open-ended investigation. It deliberately composes
// capabilities rather than matching the question against a route/plan table.
type Engine struct {
	interpreter    QuestionInterpreter
	graph          GraphReader
	facts          CanonicalFactReader
	synthesizer    AnswerSynthesizer
	results        InvestigationResultStore
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
		synthesizer: dependencies.Synthesizer, results: dependencies.Results,
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
	if err := ctx.Err(); err != nil {
		return InvestigationResult{}, err
	}

	interpretation, err := e.interpreter.Interpret(ctx, principal, request)
	if err != nil {
		return InvestigationResult{}, fmt.Errorf("interpret question: %w", err)
	}
	resolution, err := e.graph.ResolveSubjects(ctx, principal, request, interpretation)
	if err != nil {
		return InvestigationResult{}, fmt.Errorf("resolve subjects: %w", err)
	}
	graphContext, err := e.graph.DiscoverContext(ctx, principal, GraphDiscoveryRequest{
		Request: request, Interpretation: interpretation, Resolution: resolution,
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
