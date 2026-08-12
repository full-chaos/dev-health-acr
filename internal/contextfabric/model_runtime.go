package contextfabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	ErrModelUnavailable = errors.New("context fabric model runtime unavailable")
	ErrModelOutput      = errors.New("context fabric model output invalid")
	// ErrModelRateLimited signals the configured provider/model rejected the
	// call because a rate or quota limit was exceeded. It is a distinct
	// classification from ErrModelUnavailable so callers can apply different
	// backoff or alerting policy to throttling versus outages.
	ErrModelRateLimited = errors.New("context fabric model runtime rate limited")
)

type ModelOperation string

const (
	ModelOperationInterpret  ModelOperation = "interpret"
	ModelOperationSynthesize ModelOperation = "synthesize"
)

type ModelUsage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	TotalTokens  int `json:"total_tokens,omitempty"`
}

type ModelExecutionReceipt struct {
	Operation        ModelOperation `json:"operation"`
	Provider         string         `json:"provider"`
	Model            string         `json:"model"`
	ModelVersion     string         `json:"model_version"`
	PromptVersion    string         `json:"prompt_version"`
	SchemaVersion    string         `json:"schema_version"`
	EvaluatorVersion string         `json:"evaluator_version"`
	StartedAt        time.Time      `json:"started_at"`
	CompletedAt      time.Time      `json:"completed_at"`
	Attempts         int            `json:"attempts"`
	InputDigest      string         `json:"input_digest"`
	OutputDigest     string         `json:"output_digest,omitempty"`
	Usage            ModelUsage     `json:"usage,omitempty"`
	FallbackUsed     bool           `json:"fallback_used"`
	Outcome          string         `json:"outcome"`
}

func (r ModelExecutionReceipt) Validate() error {
	if r.Operation != ModelOperationInterpret && r.Operation != ModelOperationSynthesize {
		return fmt.Errorf("model receipt operation is invalid")
	}
	for name, value := range map[string]string{
		"provider": r.Provider, "model": r.Model, "model_version": r.ModelVersion,
		"prompt_version": r.PromptVersion, "schema_version": r.SchemaVersion,
		"evaluator_version": r.EvaluatorVersion, "input_digest": r.InputDigest,
		"outcome": r.Outcome,
	} {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return fmt.Errorf("model receipt %s is invalid", name)
		}
	}
	if r.StartedAt.IsZero() || r.CompletedAt.IsZero() || r.CompletedAt.Before(r.StartedAt) || r.Attempts < 1 || r.Attempts > 8 {
		return fmt.Errorf("model receipt timing or attempts are invalid")
	}
	if len(r.InputDigest) != 64 || (r.OutputDigest != "" && len(r.OutputDigest) != 64) {
		return fmt.Errorf("model receipt digest is invalid")
	}
	if r.Usage.InputTokens < 0 || r.Usage.OutputTokens < 0 || r.Usage.TotalTokens < 0 {
		return fmt.Errorf("model receipt usage is invalid")
	}
	return nil
}

// ModelReceiptSink durably records every model execution receipt
// (success, fallback, invalid_output, rate_limited, or unavailable). It is
// also the defined evaluator seam for CHAOS-3756: an evaluator consumes
// EvaluatorVersion-keyed receipts from this sink asynchronously, outside the
// synchronous investigation path, rather than calling back into the model
// in-band. See ADR 0008's "Evaluator seam" section.
type ModelReceiptSink interface {
	RecordModelExecution(context.Context, storage.Principal, ModelExecutionReceipt) error
}

type SynthesisDraft struct {
	Status              InvestigationStatus `json:"status"`
	DirectJudgment      string              `json:"direct_judgment"`
	CurrentState        string              `json:"current_state"`
	StrongestPressures  []string            `json:"strongest_pressures"`
	Drivers             []DriverJudgment    `json:"drivers"`
	RemainingWork       []Finding           `json:"remaining_work"`
	ReadinessGaps       []Finding           `json:"readiness_gaps"`
	Conflicts           []Finding           `json:"conflicts"`
	Limitations         []string            `json:"limitations"`
	EvidenceRefIDs      []string            `json:"evidence_ref_ids"`
	DeterministicAnswer string              `json:"deterministic_answer"`
	Warnings            []string            `json:"warnings"`
}

func (d SynthesisDraft) ValidateAgainst(input SynthesisInput) error {
	switch d.Status {
	case InvestigationComplete, InvestigationPartial, InvestigationDegraded, InvestigationClarificationRequired, InvestigationNoMatch:
	default:
		return fmt.Errorf("synthesis draft status is invalid")
	}
	if (d.Status == InvestigationComplete || d.Status == InvestigationPartial) && strings.TrimSpace(d.DirectJudgment) == "" {
		return fmt.Errorf("answer-capable synthesis requires a direct judgment")
	}
	if strings.TrimSpace(d.DeterministicAnswer) == "" {
		return fmt.Errorf("deterministic answer is required")
	}
	allowedSubjects := synthesisSubjects(input)
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
		if _, ok := allowedEvidence[evidenceRefID]; !ok {
			return fmt.Errorf("synthesis references unknown evidence %q", evidenceRefID)
		}
	}
	for _, driver := range d.Drivers {
		if err := driver.Validate(); err != nil {
			return fmt.Errorf("driver: %w", err)
		}
		for _, subject := range driver.AffectedSubjects {
			if _, ok := allowedSubjects[subjectKeyForModel(subject)]; !ok {
				return fmt.Errorf("driver references subject outside the investigation")
			}
		}
		for _, pathID := range driver.PathIDs {
			if _, ok := allowedPaths[pathID]; !ok {
				return fmt.Errorf("driver references unknown path %q", pathID)
			}
		}
		for _, evidenceRefID := range driver.EvidenceRefIDs {
			if _, ok := allowedEvidence[evidenceRefID]; !ok {
				return fmt.Errorf("driver references unknown evidence %q", evidenceRefID)
			}
		}
	}
	for name, findings := range map[string][]Finding{
		"remaining_work": d.RemainingWork,
		"readiness_gaps": d.ReadinessGaps,
		"conflicts":      d.Conflicts,
	} {
		for _, finding := range findings {
			if err := finding.Validate(); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			for _, subject := range finding.Subjects {
				if _, ok := allowedSubjects[subjectKeyForModel(subject)]; !ok {
					return fmt.Errorf("%s references subject outside the investigation", name)
				}
			}
			for _, evidenceRefID := range finding.EvidenceRefIDs {
				if _, ok := allowedEvidence[evidenceRefID]; !ok {
					return fmt.Errorf("%s references unknown evidence %q", name, evidenceRefID)
				}
			}
		}
	}
	return nil
}

type ModelRuntime interface {
	InterpretQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error)
	SynthesizeAnswer(context.Context, storage.Principal, SynthesisInput) (SynthesisDraft, ModelExecutionReceipt, error)
}

type RuntimeQuestionInterpreter struct {
	Runtime ModelRuntime
	Sink    ModelReceiptSink
}

func (r RuntimeQuestionInterpreter) Interpret(ctx context.Context, principal storage.Principal, request InvestigationRequest) (InterpretedQuestion, error) {
	if r.Runtime == nil {
		return InterpretedQuestion{}, ErrModelUnavailable
	}
	question, receipt, err := r.Runtime.InterpretQuestion(ctx, principal, request)
	if sinkErr := recordModelReceipt(ctx, principal, r.Sink, receipt); sinkErr != nil && err == nil {
		return InterpretedQuestion{}, sinkErr
	}
	if err != nil {
		return InterpretedQuestion{}, err
	}
	if err := question.Validate(); err != nil {
		return InterpretedQuestion{}, fmt.Errorf("%w: %v", ErrModelOutput, err)
	}
	return question, nil
}

type RuntimeAnswerSynthesizerOptions struct {
	ServiceVersion          string
	Backend                 string
	BackendVersion          string
	ProjectionVersion       string
	QueryVersion            string
	CanonicalServiceVersion string
}

type RuntimeAnswerSynthesizer struct {
	Runtime ModelRuntime
	Sink    ModelReceiptSink
	Options RuntimeAnswerSynthesizerOptions
}

func (r RuntimeAnswerSynthesizer) Synthesize(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
	if r.Runtime == nil {
		return InvestigationResult{}, ErrModelUnavailable
	}
	draft, receipt, err := r.Runtime.SynthesizeAnswer(ctx, principal, input)
	if sinkErr := recordModelReceipt(ctx, principal, r.Sink, receipt); sinkErr != nil && err == nil {
		return InvestigationResult{}, sinkErr
	}
	if err != nil {
		return InvestigationResult{}, err
	}
	if err := draft.ValidateAgainst(input); err != nil {
		return InvestigationResult{}, fmt.Errorf("%w: %v", ErrModelOutput, err)
	}
	result := InvestigationResult{
		Status:              draft.Status,
		DirectJudgment:      strings.TrimSpace(draft.DirectJudgment),
		CurrentState:        strings.TrimSpace(draft.CurrentState),
		StrongestPressures:  append([]string(nil), draft.StrongestPressures...),
		Drivers:             append([]DriverJudgment(nil), draft.Drivers...),
		RemainingWork:       append([]Finding(nil), draft.RemainingWork...),
		ReadinessGaps:       append([]Finding(nil), draft.ReadinessGaps...),
		Paths:               append([]RelationshipPath(nil), input.Graph.Paths...),
		Conflicts:           append([]Finding(nil), draft.Conflicts...),
		Limitations:         append([]string(nil), draft.Limitations...),
		EvidenceRefIDs:      append([]string(nil), draft.EvidenceRefIDs...),
		Coverage:            mergeCoverage(input.Graph.Coverage, input.Facts.Coverage),
		DeterministicAnswer: strings.TrimSpace(draft.DeterministicAnswer),
		Warnings:            append([]string(nil), draft.Warnings...),
		Versions: VersionSet{
			ServiceVersion:          nonEmptyVersion(r.Options.ServiceVersion, "unwired"),
			ContractVersion:         InvestigationResultSchemaV1,
			Backend:                 nonEmptyVersion(r.Options.Backend, "unwired"),
			BackendVersion:          strings.TrimSpace(r.Options.BackendVersion),
			ProjectionVersion:       nonEmptyVersion(r.Options.ProjectionVersion, "unwired"),
			QueryVersion:            nonEmptyVersion(r.Options.QueryVersion, "unwired"),
			InterpretationVersion:   receipt.SchemaVersion,
			SynthesisVersion:        receipt.PromptVersion + "+" + receipt.ModelVersion,
			CanonicalServiceVersion: nonEmptyVersion(r.Options.CanonicalServiceVersion, input.Facts.Version),
		},
	}
	return result, nil
}

func recordModelReceipt(ctx context.Context, principal storage.Principal, sink ModelReceiptSink, receipt ModelExecutionReceipt) error {
	if receipt.Operation == "" {
		return nil
	}
	if err := receipt.Validate(); err != nil {
		return fmt.Errorf("model receipt: %w", err)
	}
	if sink == nil {
		return nil
	}
	if err := sink.RecordModelExecution(ctx, principal, receipt); err != nil {
		return fmt.Errorf("record model receipt: %w", err)
	}
	return nil
}

func mergeCoverage(groups ...Coverage) Coverage {
	bySource := make(map[string]SourceObservation)
	degraded := make(map[string]struct{})
	partial := false
	for _, group := range groups {
		partial = partial || group.Partial
		for _, source := range group.Sources {
			if current, ok := bySource[source.Source]; !ok || sourceStatePriority(source.State) > sourceStatePriority(current.State) {
				bySource[source.Source] = source
			}
		}
		for _, reason := range group.DegradedReasons {
			degraded[reason] = struct{}{}
		}
	}
	sources := make([]SourceObservation, 0, len(bySource))
	for _, source := range bySource {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Source < sources[j].Source })
	reasons := make([]string, 0, len(degraded))
	for reason := range degraded {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	return Coverage{Sources: sources, Partial: partial || len(reasons) > 0, DegradedReasons: reasons}
}

func sourceStatePriority(state SourceState) int {
	switch state {
	case SourceUnauthorized:
		return 9
	case SourceUnavailable:
		return 8
	case SourceConflicted:
		return 7
	case SourceTruncated:
		return 6
	case SourceStale:
		return 5
	case SourceUnconfigured:
		return 4
	case SourceNoData:
		return 3
	case SourceNotApplicable:
		return 2
	case SourceAvailable:
		return 1
	default:
		return 0
	}
}

func synthesisSubjects(input SynthesisInput) map[string]struct{} {
	result := make(map[string]struct{})
	for _, subject := range input.Graph.Resolution.Committed {
		result[subjectKeyForModel(subject)] = struct{}{}
	}
	if input.Graph.Cohort != nil {
		for _, member := range input.Graph.Cohort.Members {
			result[subjectKeyForModel(member.Subject)] = struct{}{}
		}
	}
	for _, fact := range input.Facts.Facts {
		result[subjectKeyForModel(fact.Subject)] = struct{}{}
	}
	for _, path := range input.Graph.Paths {
		for _, subject := range path.Nodes {
			result[subjectKeyForModel(subject)] = struct{}{}
		}
	}
	return result
}

func subjectKeyForModel(subject SubjectRef) string {
	return string(subject.Kind) + "\x00" + subject.CanonicalID
}

func nonEmptyVersion(primary, fallback string) string {
	if value := strings.TrimSpace(primary); value != "" {
		return value
	}
	if value := strings.TrimSpace(fallback); value != "" {
		return value
	}
	return "unwired"
}

func DigestModelValue(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
