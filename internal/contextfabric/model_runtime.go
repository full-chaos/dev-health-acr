package contextfabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
	// ErrInterpretationRejected classifies an InterpretQuestion failure
	// caused by ACR's OWN model-facing bound validation
	// (InterpretedQuestion.Validate, contracts/v1) rejecting the model's
	// structured interpretation -- as opposed to a provider/transport/
	// schema-level failure (genkitruntime.classifyModelError), which stays
	// a bare ErrModelOutput. It always wraps ErrModelOutput too (see
	// RuntimeQuestionInterpreter.Interpret), so every existing
	// errors.Is(err, ErrModelOutput) caller (e.g. receiptOutcomeForError)
	// is unaffected; this is a strictly more specific classification a
	// caller can check FIRST to tell the two apart (CHAOS-3784: the
	// investigations route previously reported the identical opaque
	// response for both).
	ErrInterpretationRejected = errors.New("context fabric interpretation rejected")
	// ErrSynthesisRejected is ErrInterpretationRejected's synthesis-side
	// counterpart: SynthesisDraft.ValidateAgainst rejected the model's
	// synthesis draft, whether for a length/count bound or a claim-
	// binding/grounding rule.
	ErrSynthesisRejected = errors.New("context fabric synthesis rejected")
)

// ModelBoundViolation carries the specific contracts/v1
// ContextFabricModelFacingBounds entry a rejected interpretation or
// synthesis draft violated, when the rejection is attributable to one
// (contractsv1.DiagnoseContextFabric*Bound). It is never constructed for a
// business-rule rejection (an invalid enum, a missing claim binding) --
// those carry ErrInterpretationRejected/ErrSynthesisRejected alone, with no
// ModelBoundViolation in the chain, since there is no single bound name to
// report. Bound is always one of the fixed, ACR-owned registry names; it
// never contains model output.
type ModelBoundViolation struct {
	Bound string
	err   error
}

func (e *ModelBoundViolation) Error() string { return e.err.Error() }
func (e *ModelBoundViolation) Unwrap() error { return e.err }

// NewModelBoundViolation constructs a *ModelBoundViolation wrapping err with
// the given registry bound name. Exported so a caller that already knows
// the violated bound -- or a test simulating one -- can attach it without
// reaching into an unexported field.
func NewModelBoundViolation(bound string, err error) *ModelBoundViolation {
	return &ModelBoundViolation{Bound: bound, err: err}
}

// withBoundViolation wraps err in a *ModelBoundViolation carrying bound when
// diagnosed is true, or returns err unchanged otherwise.
func withBoundViolation(err error, bound string, diagnosed bool) error {
	if !diagnosed {
		return err
	}
	return NewModelBoundViolation(bound, err)
}

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
	// RequestID correlates this receipt back to the originating
	// InvestigationRequest (CHAOS-3889, audit MED item "ModelExecutionReceipt
	// has no request_id column"). Optional and best-effort: a genkitruntime
	// caller stamps it from InvestigationRequest.RequestID /
	// SynthesisInput.Request.RequestID when building the receipt, but a
	// receipt built before this field existed, or by a ModelRuntime that
	// never received one, is still a valid receipt with RequestID empty --
	// Validate() below only bounds it when present, never requires it.
	RequestID string `json:"request_id,omitempty"`
	// WindowClass/WindowConfidence/WindowClassUnrecognized (CHAOS-3900 W0,
	// SHADOW ONLY -- see chaos3900_window_vocab.go's package doc comment):
	// the sanitized closed-vocabulary window classification an Interpret
	// call produced, captured here rather than on InterpretedQuestion so
	// this stays off the published wire contract for a slice that ships no
	// consumer of it. Omitempty: every receipt built before this field
	// existed, and every Operation=synthesize receipt (this classification
	// exists only for interpretation), has it absent, not a present zero
	// value -- the same asymmetry-avoidance ModelIdentity's own doc comment
	// on this struct already explains.
	WindowClass             WindowClass      `json:"window_class,omitempty"`
	WindowConfidence        WindowConfidence `json:"window_confidence,omitempty"`
	WindowClassUnrecognized bool             `json:"window_class_unrecognized,omitempty"`
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
	if len(r.RequestID) > 256 {
		return fmt.Errorf("model receipt request_id is invalid")
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
	ClaimedFacts        []ClaimedFact       `json:"claimed_facts"`
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
	// canonicalLabels binds every subject the model can legally reference
	// to the ONE label the investigation input actually carries for that
	// canonical ID (graph resolution, cohort, paths, and canonical facts
	// are the sources of truth). A model-supplied SubjectRef whose
	// CanonicalID is in-bounds but whose Label diverges from that is a
	// forged/rewritten label -- e.g. presenting a project under a
	// different name than the one the graph actually resolved -- and must
	// be rejected the same as an out-of-bounds subject (CHAOS-3755
	// adversarial review finding H3).
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
		if _, ok := allowedEvidence[evidenceRefID]; !ok {
			return fmt.Errorf("synthesis references unknown evidence %q", evidenceRefID)
		}
	}
	// Value-level evidence closure (CHAOS-3755 must-do): structural
	// grounding above only proves a citation exists, not that its claimed
	// value agrees with the canonical fact it's supposedly grounded in --
	// a synthesis draft claiming "release-ready" against a canonical
	// release_ready=false fact would pass every check above unchanged.
	// ClaimedFacts closes that gap deterministically: every claim must
	// resolve to an actually-observed canonical fact field, by exact
	// struct equality, not wording.
	claimedByID := make(map[string]ClaimedFact, len(d.ClaimedFacts))
	for _, claim := range d.ClaimedFacts {
		if err := claim.Validate(); err != nil {
			return fmt.Errorf("claimed_facts: %w", err)
		}
		if _, exists := claimedByID[claim.ClaimID]; exists {
			return fmt.Errorf("claimed fact IDs must be unique")
		}
		claimedByID[claim.ClaimID] = claim
		if _, ok := allowedSubjects[subjectKeyForModel(claim.Subject)]; !ok {
			return fmt.Errorf("claimed fact references subject outside the investigation")
		}
		if err := requireBoundLabel("claimed fact", claim.Subject, canonicalLabels); err != nil {
			return err
		}
		canonical, ok := lookupCanonicalFact(input.Facts.Facts, claim.Kind, claim.Subject)
		if !ok {
			return fmt.Errorf("claimed fact %s/%s has no canonical observation to ground it", claim.Kind, claim.Field)
		}
		observed, present := canonical.Fields[claim.Field]
		if !present {
			return fmt.Errorf("claimed fact field %q was not canonically observed", claim.Field)
		}
		if !factValueEqualsScalar(observed, claim.Value) {
			return fmt.Errorf("claimed fact %q contradicts the canonical value observed for %s.%s", claim.ClaimID, claim.Kind, claim.Field)
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
			if err := requireBoundLabel("driver", subject, canonicalLabels); err != nil {
				return err
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
		if err := requireGroundedClaims("driver", driver.Category, driver.AffectedSubjects, allowedSubjects, driver.ClaimedFactIDs, claimedByID); err != nil {
			return err
		}
	}
	// Fixed slice, NOT a map: a map's iteration order is randomized per
	// run, so returning the first-rejected section's error from a map
	// range would make WHICH violation surfaces (when more than one
	// section has one) nondeterministic across runs -- and
	// diagnoseSynthesisDraftBound (bound_diagnosis.go) diagnoses in a
	// fixed order, so a random validation order could report a
	// violated_bound naming a section ValidateAgainst did NOT actually
	// reject (CHAOS-3784 round-3 R3-2). This order (remaining_work,
	// readiness_gaps, conflicts) matches diagnoseSynthesisDraftBound's
	// exactly, so the two can never disagree.
	for _, section := range []struct {
		name     string
		findings []Finding
	}{
		{"remaining_work", d.RemainingWork},
		{"readiness_gaps", d.ReadinessGaps},
		{"conflicts", d.Conflicts},
	} {
		name, findings := section.name, section.findings
		for _, finding := range findings {
			if err := finding.Validate(); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			for _, subject := range finding.Subjects {
				if _, ok := allowedSubjects[subjectKeyForModel(subject)]; !ok {
					return fmt.Errorf("%s references subject outside the investigation", name)
				}
				if err := requireBoundLabel(name, subject, canonicalLabels); err != nil {
					return err
				}
			}
			for _, evidenceRefID := range finding.EvidenceRefIDs {
				if _, ok := allowedEvidence[evidenceRefID]; !ok {
					return fmt.Errorf("%s references unknown evidence %q", name, evidenceRefID)
				}
			}
			if err := requireGroundedClaims(name, finding.Kind, finding.Subjects, allowedSubjects, finding.ClaimedFactIDs, claimedByID); err != nil {
				return err
			}
		}
	}
	return nil
}

// requireGroundedClaims checks that every ID in claimedFactIDs resolves
// inside claimed, that its claim is about a subject the citing driver/
// finding actually named (scopeSubjects -- or, when a finding declares no
// subjects of its own, any subject already established elsewhere in the
// investigation, via fallbackAllowed), and -- when category names a
// canonical-fact-shaped judgment per
// ContextFabricDriverCategoryRequiresClaimedFact -- that at least one
// referenced claim's Kind matches.
//
// The subject-scoping check (CHAOS-3755 adversarial review finding H1)
// matters independently of the value-equality check ValidateAgainst
// already does on each ClaimedFact: a driver whose AffectedSubjects is
// [project B] citing a perfectly true, canonically-grounded claim about
// project A is still a false public assertion -- it presents A's data as
// if it were about B. Binding every reference to the citing driver/
// finding's own subjects closes that.
//
// Catching all of this here (at model-output validation time, classified
// ErrModelOutput -> 502) rather than only later at
// InvestigationResult.Validate() (classified ErrInvalidResult -> 500)
// lets the route correctly attribute a closure-violating draft to the
// model, not to ACR.
func requireGroundedClaims(name, category string, scopeSubjects []SubjectRef, fallbackAllowed map[string]struct{}, claimedFactIDs []string, claimed map[string]ClaimedFact) error {
	scoped := fallbackAllowed
	if len(scopeSubjects) > 0 {
		scoped = make(map[string]struct{}, len(scopeSubjects))
		for _, subject := range scopeSubjects {
			scoped[subjectKeyForModel(subject)] = struct{}{}
		}
	}
	requiredKind, required := contractsv1.ContextFabricDriverCategoryRequiresClaimedFact(contractsv1.ContextFabricDriverCategory(category))
	matchedKind := false
	for _, claimID := range claimedFactIDs {
		claim, ok := claimed[claimID]
		if !ok {
			return fmt.Errorf("%s references unknown claimed fact %q", name, claimID)
		}
		if _, ok := scoped[subjectKeyForModel(claim.Subject)]; !ok {
			return fmt.Errorf("%s claimed fact %q is about a subject outside its own affected subjects", name, claimID)
		}
		if required && claim.Kind == requiredKind {
			matchedKind = true
		}
	}
	if required && !matchedKind {
		return fmt.Errorf("%s category %q requires a claimed fact of kind %q", name, category, requiredKind)
	}
	return nil
}

// canonicalSubjectLabels binds every canonical ID the investigation input
// actually named to the ONE label that ID carries in the input: graph
// resolution (candidates and committed subjects), the discovered cohort,
// relationship path nodes, and canonical fact subjects are all sources of
// truth, in that order, first-write-wins (an investigation's own inputs
// are expected to already agree; this is not a conflict-resolution
// policy). See requireBoundLabel.
func canonicalSubjectLabels(input SynthesisInput) map[string]string {
	labels := make(map[string]string)
	set := func(subject SubjectRef) {
		key := subjectKeyForModel(subject)
		if _, exists := labels[key]; !exists {
			labels[key] = subject.Label
		}
	}
	for _, candidate := range input.Graph.Resolution.Candidates {
		set(candidate.Subject)
	}
	for _, subject := range input.Graph.Resolution.Committed {
		set(subject)
	}
	if input.Graph.Cohort != nil {
		for _, member := range input.Graph.Cohort.Members {
			set(member.Subject)
		}
	}
	for _, path := range input.Graph.Paths {
		for _, node := range path.Nodes {
			set(node)
		}
	}
	for _, fact := range input.Facts.Facts {
		set(fact.Subject)
	}
	return labels
}

// requireBoundLabel rejects a model-supplied subject whose Label diverges
// from the one canonicalSubjectLabels recorded for its CanonicalID+Kind --
// a forged/rewritten label presenting a real, in-bounds subject under a
// different name (CHAOS-3755 adversarial review finding H3). A subject
// with no entry in labels is not this check's concern: that's an
// out-of-bounds reference, already rejected by the caller's own
// allowedSubjects membership check before this runs.
func requireBoundLabel(name string, subject SubjectRef, labels map[string]string) error {
	if want, ok := labels[subjectKeyForModel(subject)]; ok && want != subject.Label {
		return fmt.Errorf("%s references subject %q with a label that does not match the investigation input", name, subject.CanonicalID)
	}
	return nil
}

func factValueEqualsScalar(fv FactValue, sv ScalarValue) bool {
	switch {
	case fv.String != nil:
		return sv.String != nil && *sv.String == *fv.String
	case fv.Integer != nil:
		return sv.Integer != nil && *sv.Integer == *fv.Integer
	case fv.Number != nil:
		return sv.Number != nil && *sv.Number == *fv.Number
	case fv.Boolean != nil:
		return sv.Boolean != nil && *sv.Boolean == *fv.Boolean
	case fv.Null:
		return sv.Null
	default:
		return false
	}
}

func lookupCanonicalFact(facts []CanonicalFact, kind FactKind, subject SubjectRef) (CanonicalFact, bool) {
	key := subjectKeyForModel(subject)
	for _, fact := range facts {
		if fact.Kind == kind && subjectKeyForModel(fact.Subject) == key {
			return fact, true
		}
	}
	return CanonicalFact{}, false
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
	if err == nil {
		if validateErr := question.Validate(); validateErr != nil {
			receipt.Outcome = "invalid_output"
			err = ClassifyInterpretationRejection(question, validateErr)
		} else if receipt.Outcome == "pending_validation" {
			receipt.Outcome = "success"
		}
	}
	if sinkErr := recordModelReceipt(ctx, principal, r.Sink, receipt); sinkErr != nil {
		// A sink failure is never silently dropped, even when a domain
		// validation error already occurred: losing the receipt for a
		// rejected draft is itself worth surfacing, not just the
		// rejection. errors.Join preserves errors.Is(err, ErrModelOutput)
		// for callers that only check the classification.
		if err == nil {
			return InterpretedQuestion{}, sinkErr
		}
		err = errors.Join(err, sinkErr)
	}
	if err != nil {
		return InterpretedQuestion{}, err
	}
	return question, nil
}

type RuntimeAnswerSynthesizerOptions struct {
	ServiceVersion string
	// Backend and BackendVersion land verbatim in the public
	// InvestigationResult.Versions -- callers must set Backend to the
	// vendor-neutral capability class (e.g. "graph"), never a specific
	// vendor or product name, and must treat BackendVersion as an opaque
	// token. See ContextFabricVersionSet's field docs.
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

// StaticResultVersions implements ResultVersionProvider (CHAOS-3810), so a
// terminal result Engine composes without a model call still reports the
// backend, projection, and query versions this synthesizer would have
// stamped. The receipt-derived fields (interpretation/synthesis versions,
// model identity) are deliberately left empty: no model ran, and Engine
// fills them with its own "unwired" placeholder rather than borrowing a
// value from a call that never happened.
func (r RuntimeAnswerSynthesizer) StaticResultVersions() VersionSet {
	return VersionSet{
		ServiceVersion:          strings.TrimSpace(r.Options.ServiceVersion),
		ContractVersion:         InvestigationResultSchemaV1,
		Backend:                 strings.TrimSpace(r.Options.Backend),
		BackendVersion:          strings.TrimSpace(r.Options.BackendVersion),
		ProjectionVersion:       strings.TrimSpace(r.Options.ProjectionVersion),
		QueryVersion:            strings.TrimSpace(r.Options.QueryVersion),
		CanonicalServiceVersion: strings.TrimSpace(r.Options.CanonicalServiceVersion),
	}
}

func (r RuntimeAnswerSynthesizer) Synthesize(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
	if r.Runtime == nil {
		return InvestigationResult{}, ErrModelUnavailable
	}
	draft, receipt, err := r.Runtime.SynthesizeAnswer(ctx, principal, input)
	if err == nil {
		if validateErr := draft.ValidateAgainst(input); validateErr != nil {
			receipt.Outcome = "invalid_output"
			err = ClassifySynthesisRejection(draft, input, validateErr)
		} else if receipt.Outcome == "pending_validation" {
			receipt.Outcome = "success"
		}
	}
	if sinkErr := recordModelReceipt(ctx, principal, r.Sink, receipt); sinkErr != nil {
		// See the matching comment in RuntimeQuestionInterpreter.Interpret:
		// a sink failure is never silently dropped, even when a domain
		// validation error already occurred.
		if err == nil {
			return InvestigationResult{}, sinkErr
		}
		err = errors.Join(err, sinkErr)
	}
	if err != nil {
		return InvestigationResult{}, err
	}
	result := InvestigationResult{
		Status: draft.Status,
		// DirectJudgment and CurrentState are server-composed too, for the
		// same reason DeterministicAnswer is (see its comment below):
		// draft.DirectJudgment/draft.CurrentState (whatever prose the
		// model produced) are deliberately discarded. The contract has no
		// separate field labeled for raw model inference, so that prose
		// is dropped entirely rather than smuggled into a field a
		// consumer would reasonably read as validated (CHAOS-3755
		// adversarial review finding H2 -- prose could otherwise assert
		// the opposite of an already-validated claim, e.g. "on track"
		// prose next to a principal driver whose claimed fact says
		// release_ready=false).
		DirectJudgment:     composeDirectJudgment(draft, input.Graph.Resolution),
		CurrentState:       composeCurrentState(draft),
		StrongestPressures: cloneSlice(draft.StrongestPressures),
		Drivers:            cloneSlice(draft.Drivers),
		RemainingWork:      cloneSlice(draft.RemainingWork),
		ReadinessGaps:      cloneSlice(draft.ReadinessGaps),
		Paths:              cloneSlice(input.Graph.Paths),
		Conflicts:          cloneSlice(draft.Conflicts),
		Limitations:        cloneSlice(draft.Limitations),
		EvidenceRefIDs:     cloneSlice(draft.EvidenceRefIDs),
		ClaimedFacts:       cloneSlice(draft.ClaimedFacts),
		Coverage:           mergeCoverage(input.Graph.Coverage, input.Facts.Coverage),
		// DeterministicAnswer is server-composed, not model-authored: it is
		// a pure function of the already-validated Status, Drivers, and
		// ClaimedFacts, computed after ValidateAgainst has passed. That is
		// the only reading of "deterministic" that can't itself introduce
		// a fresh, unchecked claim -- draft.DeterministicAnswer (whatever
		// prose the model produced) is deliberately discarded here.
		DeterministicAnswer: composeDeterministicAnswer(draft, input.Graph.Resolution),
		Warnings:            cloneSlice(draft.Warnings),
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
			// ModelIdentity (CHAOS-3782) names the provider and model that
			// produced THIS synthesis -- receipt.Provider/receipt.Model,
			// not receipt.ModelVersion (already captured above inside
			// SynthesisVersion). Answer reuse binds on this so a model
			// swap (even one that coincidentally shares a model_version
			// string) never reuses a result the new model never produced.
			ModelIdentity: nonEmptyVersion(modelIdentity(receipt.Provider, receipt.Model), "unwired"),
		},
	}
	return result, nil
}

// composeDirectJudgment/composeCurrentState/composeDeterministicAnswer's
// max lengths mirror ContextFabricInvestigationResult.Validate()'s Go-level
// bounds exactly (validate_context_fabric_result.go). A composer must never
// let a large number of drivers/claims grow a rendered field past its
// contract bound: that would turn a perfectly valid investigation into an
// ErrInvalidResult/500 at the very last step, for a reason entirely of
// ACR's own making (CHAOS-3755 adversarial review finding M4) -- so every
// composer truncates itself, at a sentence boundary with an explicit
// elision marker, rather than ever producing an oversized field.
// Derived from the contract registry, never copied. These composers used to
// hold their own 8000/16000 while writes accepted 4000/12000, so a valid
// synthesis could compose a field the validator then rejected (codex
// round-7 F2). One source of truth removes the possibility.
const (
	directJudgmentMaxLength      = contractsv1.ContextFabricDirectJudgmentMaxLength
	currentStateMaxLength        = contractsv1.ContextFabricCurrentStateMaxLength
	deterministicAnswerMaxLength = contractsv1.ContextFabricDeterministicAnswerMaxLength
)

// truncateAtSentenceBoundary shortens text to at most maxRunes runes,
// preferring to cut at the last '.' or ';' within budget so the elision
// marker doesn't land mid-sentence, and always appends an explicit marker
// so truncation is visible to a reader/consumer rather than silent.
func truncateAtSentenceBoundary(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	const marker = " […truncated]"
	markerRunes := []rune(marker)
	budget := maxRunes - len(markerRunes)
	if budget < 0 {
		budget = 0
	}
	truncated := runes[:budget]
	for i := len(truncated) - 1; i >= 0; i-- {
		if truncated[i] == '.' || truncated[i] == ';' {
			truncated = truncated[:i+1]
			break
		}
	}
	return strings.TrimSpace(string(truncated)) + marker
}

// composeDirectJudgment renders DirectJudgment server-side, for the same
// reason composeDeterministicAnswer exists: draft.DirectJudgment (model
// prose) is never read. Distinct from composeDeterministicAnswer in
// content, not just name -- this is the short judgment sentence alone,
// without the trailing claimed-fact restatement DeterministicAnswer adds.
func composeDirectJudgment(draft SynthesisDraft, resolution SubjectResolution) string {
	var b strings.Builder
	b.WriteString(statusSentence(draft.Status, resolution))
	if titles := principalDriverTitles(draft.Drivers); len(titles) > 0 {
		b.WriteString(" Principal driver: ")
		b.WriteString(strings.Join(titles, "; "))
		b.WriteString(".")
	}
	return truncateAtSentenceBoundary(strings.TrimSpace(b.String()), directJudgmentMaxLength)
}

// composeCurrentState renders CurrentState server-side from validated
// ClaimedFacts only -- the "current" observed values a claim restates. See
// composeDeterministicAnswer's doc comment for why model prose
// (draft.CurrentState) is never read here.
func composeCurrentState(draft SynthesisDraft) string {
	claims := claimedFactSentences(draft.ClaimedFacts)
	if len(claims) == 0 {
		return "No canonical facts were observed to describe the current state."
	}
	return truncateAtSentenceBoundary("Current observed values: "+strings.Join(claims, "; ")+".", currentStateMaxLength)
}

// composeDeterministicAnswer renders DeterministicAnswer server-side from
// already-validated structured fields only (Status, principal Drivers,
// ClaimedFacts) -- see the call site's comment for why. It never reads
// draft.DirectJudgment/CurrentState/DeterministicAnswer (unvalidated
// model prose) and cannot itself diverge from a canonical fact value
// because every value it renders came from ClaimedFacts, which
// ValidateAgainst already proved equal to the canonical fact bundle.
func composeDeterministicAnswer(draft SynthesisDraft, resolution SubjectResolution) string {
	var b strings.Builder
	b.WriteString(statusSentence(draft.Status, resolution))
	if titles := principalDriverTitles(draft.Drivers); len(titles) > 0 {
		b.WriteString(" Principal driver(s): ")
		b.WriteString(strings.Join(titles, "; "))
		b.WriteString(".")
	}
	if claims := claimedFactSentences(draft.ClaimedFacts); len(claims) > 0 {
		b.WriteString(" Canonical facts: ")
		b.WriteString(strings.Join(claims, "; "))
		b.WriteString(".")
	}
	return truncateAtSentenceBoundary(strings.TrimSpace(b.String()), deterministicAnswerMaxLength)
}

// statusSentence renders the one-sentence status prose every composed
// answer field opens with.
//
// It takes the RESOLUTION, not a derived flag, because no_match is not one
// state (CHAOS-3810 codex rounds 1-2). Three separate defects came from
// rendering one fixed sentence for it:
//
//   - round-1 P2: "no subject could be resolved" was rendered while
//     candidates sat in the same payload;
//   - round-2 F1: the replacement said "more than one authorized subject
//     matched", which is false for a single uncommitted candidate -- one
//     weak candidate that misses the 0.72 gate reaches exactly this path;
//   - round-2 F2: with a COMMITTED subject the absence sentence is false
//     twice over. no_match with committed subjects is contract-legal and
//     shipped: the acceptance corpus's own no-data case
//     (TestAcceptanceNoDataProducesNoMatchNotAnError) commits a subject,
//     reads facts that return no rows, and takes no_match with the
//     limitation "No canonical data was observed for this subject." No
//     validator couples the status to subject_resolution (the enum check in
//     validate_context_fabric_helpers.go and ValidateAgainst are the only
//     constraints), and the JSON Schema's enum carries no coupling either.
//     So no_match means "nothing to answer with", NOT "no subject was
//     found", and the prose has to say which one happened.
//
// Passing the resolution rather than a bool is deliberate: every derived
// flag so far has been the thing that went stale when a new case appeared.
//
// It states no CAUSE in any branch. The engine's terminal path knows the
// cause (the caller disallowed clarification) and says so in its own
// limitation; a model-authored no_match may have any number of reasons, and
// asserting one here would be a guess.
func statusSentence(status InvestigationStatus, resolution SubjectResolution) string {
	switch status {
	case InvestigationComplete:
		return "This investigation is complete."
	case InvestigationPartial:
		return "This investigation is partial: some canonical or graph coverage was unavailable."
	case InvestigationDegraded:
		return "This investigation is degraded: coverage was limited."
	case InvestigationClarificationRequired:
		return "Clarification is required before this question can be answered."
	case InvestigationNoMatch:
		switch {
		case len(resolution.Committed) > 0:
			// Count-NEUTRAL, deliberately. Committing more than one subject
			// is routine rather than exotic: FinalizeExactResolution commits
			// EVERY resolved caller hint, so two subject hints produce two
			// committed subjects, and the phase-3 fast path does the same for
			// multiple receipt-derived hints. A singular "the subject" would
			// have been the round-2 F1 plurality defect reintroduced on the
			// branch that fixed F2 -- and a fourth pair of singular/plural
			// constants would only grow a constant census that is already
			// long enough to be its own hazard.
			return "This question's subjects were resolved, but no canonical data was found to answer it."
		case len(resolution.Candidates) == 1:
			return "One authorized subject matched this question but could not be confirmed as its subject; that candidate is listed in this result."
		case len(resolution.Candidates) > 1:
			return "No single subject could be confirmed for this question: more than one authorized subject matched, and the matching candidates are listed in this result."
		default:
			return "No investigation subject could be resolved for this question."
		}
	default:
		return "This investigation could not produce a complete answer."
	}
}

func principalDriverTitles(drivers []DriverJudgment) []string {
	filtered := make([]DriverJudgment, 0, len(drivers))
	for _, driver := range drivers {
		if driver.Standing == DriverPrincipal {
			filtered = append(filtered, driver)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].DriverID < filtered[j].DriverID })
	titles := make([]string, 0, len(filtered))
	for _, driver := range filtered {
		if title := strings.TrimSpace(driver.Title); title != "" {
			titles = append(titles, title)
		}
	}
	return titles
}

func claimedFactSentences(claims []ClaimedFact) []string {
	sorted := append([]ClaimedFact(nil), claims...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ClaimID < sorted[j].ClaimID })
	sentences := make([]string, 0, len(sorted))
	for _, claim := range sorted {
		sentences = append(sentences, fmt.Sprintf("%s.%s=%s for %s", claim.Kind, claim.Field, scalarValueString(claim.Value), claim.Subject.Label))
	}
	return sentences
}

func scalarValueString(v ScalarValue) string {
	switch {
	case v.String != nil:
		return *v.String
	case v.Integer != nil:
		return strconv.FormatInt(*v.Integer, 10)
	case v.Number != nil:
		return strconv.FormatFloat(*v.Number, 'g', -1, 64)
	case v.Boolean != nil:
		return strconv.FormatBool(*v.Boolean)
	case v.Null:
		return "null"
	default:
		return ""
	}
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

// cloneSlice returns an independent copy of values. Unlike
// append([]T(nil), values...), it always returns a non-nil slice -- even
// when values is empty or nil -- because the public InvestigationResult
// validator rejects a nil collection outright (see
// internal/contracts/v1/validate_context_fabric_result.go), and an ordinary
// draft with no conflicts, limitations, or warnings must still validate.
func cloneSlice[T any](values []T) []T {
	cloned := make([]T, len(values))
	copy(cloned, values)
	return cloned
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

// modelIdentity combines a receipt's provider and model into the single
// opaque token VersionSet.ModelIdentity carries (CHAOS-3782). Either half
// missing yields an empty string, which nonEmptyVersion then falls back to
// "unwired" for -- matching every other VersionSet field's convention.
func modelIdentity(provider, model string) string {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return ""
	}
	return provider + "/" + model
}

func DigestModelValue(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
