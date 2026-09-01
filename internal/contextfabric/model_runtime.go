package contextfabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	// ClaimIndex is the zero-based index into SynthesisDraft.ClaimedFacts
	// the violated Bound is attributable to, or -1 when Bound is not
	// claim-scoped (an interpretation bound, or a driver/finding bound).
	// Set only by ClassifySynthesisRejection (via claimIndexForBound in
	// bound_diagnosis.go), CHAOS-4355 follow-up, so a server-side log line
	// can name WHICH claim tripped a rejection without exposing any
	// model-authored content.
	ClaimIndex int
	err        error
}

func (e *ModelBoundViolation) Error() string { return e.err.Error() }
func (e *ModelBoundViolation) Unwrap() error { return e.err }

// NewModelBoundViolation constructs a *ModelBoundViolation wrapping err with
// the given registry bound name. Exported so a caller that already knows
// the violated bound -- or a test simulating one -- can attach it without
// reaching into an unexported field. ClaimIndex defaults to -1 (not
// claim-scoped); ClassifySynthesisRejection overwrites it after
// construction when the diagnosed bound came from the claims loop.
func NewModelBoundViolation(bound string, err error) *ModelBoundViolation {
	return &ModelBoundViolation{Bound: bound, ClaimIndex: -1, err: err}
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
	// ModelOperationPhraseOffers (CHAOS-4171 PR2) is the SECOND bounded
	// model call's own receipt operation -- always a call distinct from,
	// and running strictly after, ModelOperationInterpret produced the
	// interpretation composeStructureNeeds built its offers from. See
	// chaos4171_offer_phrasing.go.
	ModelOperationPhraseOffers ModelOperation = "phrase_offers"
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
	// QuestionFamily/GroupKind/ScopeAnchorTerm and their sanitize-outcome
	// flags (CHAOS-4632, SHADOW ONLY -- see
	// chaos4632_question_family_vocab.go's package-level note) are the
	// model's own family pick and the two NEW structure signals the §4.2
	// precedence table keys on, captured HERE and deliberately NOT on
	// InterpretedQuestion.
	//
	// WHY HERE AND NOT ON THE INTERPRETATION. InterpretedQuestion is a
	// type ALIAS to contractsv1.ContextFabricInterpretedQuestion
	// (model.go:299), so adding a field to it IS a wire-contract widening
	// -- and ask-dev validates with additionalProperties:false under
	// strictSchema:true and fails closed to acr_contract_violation, which
	// makes an "additive" v1 field a BREAKING change for the deployed
	// consumer (CHAOS-4623; lane-rig-refresh proved it by breaking the
	// shared rig with #336's render_shape field). The design's §9-S2 gate
	// cell therefore requires the cheaper falsification FIRST: capture
	// receipt-only, measure labelled semantic correctness including
	// NEGATIVE cases, and only then move a contract. This is the exact
	// sequence CHAOS-3900 W0 -> W1 followed for WindowClass above.
	//
	// GroupKind is closed against the ContextFabricSubjectKind registry.
	// ScopeAnchorTerm is a FREE STRING and is NOT closed against anything
	// -- it is a retrieval pointer, never a value; nothing branches on its
	// text. It is bounded and truncation is reported rather than silent.
	// It is never logged (see chaos4632_question_family_telemetry.go).
	//
	// omitempty throughout, for the identical asymmetry-avoidance reason
	// WindowClass's own comment above gives: every receipt written before
	// these fields existed, and every non-interpret receipt, has them
	// absent rather than present-and-empty.
	QuestionFamily             QuestionFamily `json:"question_family,omitempty"`
	QuestionFamilyUnrecognized bool           `json:"question_family_unrecognized,omitempty"`
	GroupKind                  SubjectKind    `json:"group_kind,omitempty"`
	GroupKindUnrecognized      bool           `json:"group_kind_unrecognized,omitempty"`
	ScopeAnchorTerm            string         `json:"scope_anchor_term,omitempty"`
	ScopeAnchorTermTruncated   bool           `json:"scope_anchor_term_truncated,omitempty"`
	// ScopeAnchorKind and RequestedSubjectKind are the two halves of
	// §4.2 row 2's ASYMMETRY test ("ScopeAnchorTerm set AND the question
	// asks about a different kind than the anchor's"). Both are closed
	// against the ContextFabricSubjectKind registry.
	//
	// They are on the receipt, and not merely passed through in memory,
	// because the labelled semantic-correctness measurement this slice
	// exists to make has to SCORE them: "was the emitted anchor kind the
	// right kind" and "was the emitted requested kind the right kind" are
	// two of the labels, and a signal that is never durably captured
	// cannot be scored after the fact -- it would have to be re-derived
	// by re-running, which is exactly the artifact-diagnosability rule
	// AGENTS.md forbids relying on.
	ScopeAnchorKind      SubjectKind `json:"scope_anchor_kind,omitempty"`
	RequestedSubjectKind SubjectKind `json:"requested_subject_kind,omitempty"`
	// The matching unrecognized flags. Every out-of-vocabulary emission
	// must be COUNTABLE, because the gating measurement scores false
	// emission: without these, a model inventing kind names is recorded
	// identically to one correctly emitting nothing.
	ScopeAnchorKindUnrecognized      bool `json:"scope_anchor_kind_unrecognized,omitempty"`
	RequestedSubjectKindUnrecognized bool `json:"requested_subject_kind_unrecognized,omitempty"`
}

func (r ModelExecutionReceipt) Validate() error {
	if r.Operation != ModelOperationInterpret && r.Operation != ModelOperationSynthesize && r.Operation != ModelOperationPhraseOffers {
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
	// CHAOS-3900 W0: the closed-vocabulary guarantee on WindowClass/
	// WindowConfidence must live HERE, at the receipt's own persistence
	// boundary (pgmodelreceipts.Store.RecordModelExecution calls Validate()
	// before json.Marshal, store.go) -- not only inside
	// genkitruntime.sanitizeWindowOutput, which is a property of today's
	// ONE ModelRuntime implementation, not of ModelExecutionReceipt itself.
	// Without this, a future ModelRuntime that assigns
	// WindowClass(rawModelString) directly, skipping sanitization, could
	// land question-derived free text in a durable artifact -- exactly
	// the class of bound this repo's other closed-vocab receipt/contract
	// fields are enforced against at their own persistence boundary, not
	// merely by convention in the one caller that happens to sanitize
	// today. Empty stays legal (genuinely "unset").
	if r.WindowClass != "" && !ValidWindowClass(r.WindowClass) {
		return fmt.Errorf("model receipt window_class is invalid")
	}
	if r.WindowConfidence != "" && !ValidWindowConfidence(r.WindowConfidence) {
		return fmt.Errorf("model receipt window_confidence is invalid")
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
	// CoverageDisclosures (CHAOS-4690 Commit F, design §4.1) is the
	// LENIENTLY-decoded model-authored coverage_disclosures subdocument --
	// the ONE new model-authorable surface this commit adds. It is nil
	// whenever the model omitted the field (or sent JSON null) AND
	// whenever CoverageDisclosuresUndecodable is true (see that field's
	// own doc comment) -- so nil alone never distinguishes "nothing
	// offered" from "offered something we could not parse"; that is
	// exactly what CoverageDisclosuresUndecodable is for. Nothing here is
	// guard-verified yet -- applyCoverageDisclosures (below) is the only
	// consumer, and it checks ref closure, uniqueness, bounds, and the
	// digit ban against the SAME merged result.Coverage.Details the
	// caller is about to serve before applying a single entry.
	CoverageDisclosures []CoverageDisclosure `json:"-"`
	// CoverageDisclosuresUndecodable is true when the model's raw
	// coverage_disclosures value was present (non-empty, non-null) but
	// failed to unmarshal into []CoverageDisclosure -- e.g.
	// `"coverage_disclosures":[{"detail_id":"cov-01","text":17}]` (design
	// §4.1's r2 F1 scenario: schema validation cannot catch this because
	// the field is deliberately unconstrained, genkitruntime.synthesisOutput's
	// own doc comment explains why). This is a LENIENT decode failure,
	// never an error: the whole synthesis answer must still be served,
	// Label-only, with telemetry outcome discarded_undecodable
	// distinguishing it from the ordinary "nothing offered" absent
	// outcome (CoverageDisclosures nil, this false).
	CoverageDisclosuresUndecodable bool `json:"-"`
}

// CoverageDisclosure is one model-authored, UNGUARDED phrasing for one
// coverage detail -- the raw entry shape of the synthesis model's
// coverage_disclosures subdocument (CHAOS-4690 Commit F, design §4.1).
// Membership (DetailID must name a real detail on the result's own merged
// Coverage.Details), uniqueness, bounds, and the digit ban are all
// enforced by applyCoverageDisclosures, never here -- this type carries
// exactly what the model said, nothing more.
type CoverageDisclosure struct {
	// DetailID/Text carry explicit json tags (snake_case) because this
	// type is unmarshalled directly off the model's raw JSON
	// (genkitruntime.parseCoverageDisclosures) -- Go's default,
	// tag-less field matching would never match "detail_id"/"text"
	// against DetailID/Text.
	DetailID string `json:"detail_id"`
	Text     string `json:"text"`
}

func (d SynthesisDraft) ValidateAgainst(input SynthesisInput) error {
	switch d.Status {
	case InvestigationComplete, InvestigationPartial, InvestigationDegraded, InvestigationClarificationRequired, InvestigationNoMatch:
	default:
		return rejectSynthesis(RejectionReasonStatusInvalid, "synthesis draft status is invalid")
	}
	if (d.Status == InvestigationComplete || d.Status == InvestigationPartial) && strings.TrimSpace(d.DirectJudgment) == "" {
		return rejectSynthesis(RejectionReasonDirectJudgmentMissing, "answer-capable synthesis requires a direct judgment")
	}
	if strings.TrimSpace(d.DeterministicAnswer) == "" {
		return rejectSynthesis(RejectionReasonDeterministicAnswerMissing, "deterministic answer is required")
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
	// CHAOS-4522: the cohort's OWN per-member evidence. synthesisSubjects
	// has admitted input.Graph.Cohort.Members[].Subject since CHAOS-4398,
	// and synthesisInputFromDomain SHOWS the model the whole Cohort --
	// including each member's EvidenceRefIDs -- but this closure was never
	// widened to match, so a member's evidence ref was displayed to the
	// model and then rejected as "unknown evidence" when the model cited
	// it. On the live three-team discovered_cohort answer that is not
	// hypothetical: the cohort ranks team:gh:ops-team, whose only evidence
	// ref is acr:v1:team:gh:ops-team, and no canonical fact exists for that
	// member, so nothing else in this closure could ever supply it. Every
	// attempt at that answer died here. These refs are engine-minted from
	// the cohort ACR itself built, exactly like the path/fact/candidate
	// refs above -- admitting them widens nothing the model could forge.
	if input.Graph.Cohort != nil {
		for _, member := range input.Graph.Cohort.Members {
			for _, evidenceRefID := range member.EvidenceRefIDs {
				allowedEvidence[evidenceRefID] = struct{}{}
			}
		}
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
			return rejectSynthesis(RejectionReasonEvidenceUnknown, "synthesis references unknown evidence %q", evidenceRefID)
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
			return rejectSynthesis(RejectionReasonClaimInvalid, "claimed_facts: %w", err)
		}
		// CHAOS-4347 codex round-3, still true under CHAOS-4355: Rows
		// (ContextFabricClaimedFact.Rows) is a producer-facing
		// renderable-table capability, and the value-level closure two
		// lines below only compares the scalar Value against the
		// canonical fact -- so a model attaching a Rows array to an
		// otherwise-valid, closure-passing scalar claim would sail
		// through completely unchecked, fabricated table content riding
		// on a real citation. CHAOS-4355 routes Rows into a claim, but
		// ONLY by the engine copying them verbatim from the canonical
		// fact the claim cites, in attachCanonicalRows, AFTER this
		// validation passes -- so a model-authored claim setting Rows
		// itself still fails closed here, unconditionally, the same
		// "retract rather than trust an unverified assertion" posture
		// CHAOS-4085's commit-affirmation gate already applies to
		// everything else in this function.
		if len(claim.Rows) > 0 {
			return rejectSynthesis(RejectionReasonClaimRowsModelAuthored, "claimed fact %q sets rows directly -- rows are attached from the cited canonical fact, never model-authored", claim.ClaimID)
		}
		if _, exists := claimedByID[claim.ClaimID]; exists {
			return rejectSynthesis(RejectionReasonClaimIDDuplicate, "claimed fact IDs must be unique")
		}
		claimedByID[claim.ClaimID] = claim
		if _, ok := allowedSubjects[subjectKeyForModel(claim.Subject)]; !ok {
			return rejectSynthesis(RejectionReasonClaimSubjectOutOfScope, "claimed fact references subject outside the investigation")
		}
		if err := requireBoundLabel("claimed fact", claim.Subject, canonicalLabels); err != nil {
			return rejectSynthesis(RejectionReasonClaimSubjectLabelMismatch, "%w", err)
		}
		switch _, outcome := groundClaim(input.Facts.Facts, claim); outcome {
		case claimGrounded:
		case claimNoCanonicalFact:
			return rejectSynthesisClaim(RejectionReasonClaimNoCanonicalFact, 0, "claimed fact %s/%s has no canonical observation to ground it", claim.Kind, claim.Field)
		case claimFieldUnobserved:
			return rejectSynthesisClaim(RejectionReasonClaimFieldUnobserved, canonicalFactGroupSize(input.Facts.Facts, claim), "claimed fact field %q was not canonically observed", claim.Field)
		default:
			return rejectSynthesisClaim(RejectionReasonClaimValueContradicts, canonicalFactGroupSize(input.Facts.Facts, claim), "claimed fact %q contradicts the canonical value observed for %s.%s", claim.ClaimID, claim.Kind, claim.Field)
		}
	}
	for _, driver := range d.Drivers {
		if err := driver.Validate(); err != nil {
			return rejectSynthesis(RejectionReasonDriverInvalid, "driver: %w", err)
		}
		for _, subject := range driver.AffectedSubjects {
			if _, ok := allowedSubjects[subjectKeyForModel(subject)]; !ok {
				return rejectSynthesis(RejectionReasonDriverSubjectOutOfScope, "driver references subject outside the investigation")
			}
			if err := requireBoundLabel("driver", subject, canonicalLabels); err != nil {
				return rejectSynthesis(RejectionReasonDriverSubjectLabelMismatch, "%w", err)
			}
		}
		for _, pathID := range driver.PathIDs {
			if _, ok := allowedPaths[pathID]; !ok {
				return rejectSynthesis(RejectionReasonDriverPathUnknown, "driver references unknown path %q", pathID)
			}
		}
		for _, evidenceRefID := range driver.EvidenceRefIDs {
			if _, ok := allowedEvidence[evidenceRefID]; !ok {
				return rejectSynthesis(RejectionReasonDriverEvidenceUnknown, "driver references unknown evidence %q", evidenceRefID)
			}
		}
		if err := requireGroundedClaims("driver", driver.Category, driver.AffectedSubjects, allowedSubjects, driver.ClaimedFactIDs, claimedByID); err != nil {
			return rejectSynthesis(RejectionReasonDriverClaimUngrounded, "%w", err)
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
				return rejectSynthesis(RejectionReasonFindingInvalid, "%s: %w", name, err)
			}
			for _, subject := range finding.Subjects {
				if _, ok := allowedSubjects[subjectKeyForModel(subject)]; !ok {
					return rejectSynthesis(RejectionReasonFindingSubjectOutOfScope, "%s references subject outside the investigation", name)
				}
				if err := requireBoundLabel(name, subject, canonicalLabels); err != nil {
					return rejectSynthesis(RejectionReasonFindingSubjectLabelMismatch, "%w", err)
				}
			}
			for _, evidenceRefID := range finding.EvidenceRefIDs {
				if _, ok := allowedEvidence[evidenceRefID]; !ok {
					return rejectSynthesis(RejectionReasonFindingEvidenceUnknown, "%s references unknown evidence %q", name, evidenceRefID)
				}
			}
			if err := requireGroundedClaims(name, finding.Kind, finding.Subjects, allowedSubjects, finding.ClaimedFactIDs, claimedByID); err != nil {
				return rejectSynthesis(RejectionReasonFindingClaimUngrounded, "%w", err)
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

// StripModelAuthoredClaimedFactTableContent returns a copy of claims with
// any model-authored table content -- Rows AND the CHAOS-4637 Table
// declaration -- cleared, plus the count of claims that carried some.
//
// SynthesisDraft.ValidateAgainst unconditionally rejects a claim with
// non-empty Rows. Both Rows and Table are attached server-side, from the
// SAME canonical fact the claim cites, only after validation
// (attachCanonicalRows below), so model-authored table content is a benign
// hallucination to discard, never a reason to reject an otherwise-valid,
// closure-passing answer (CHAOS-4355 follow-up). Exported so both this
// package's RuntimeAnswerSynthesizer.Synthesize and genkitruntime.Runtime's
// own production ValidateAgainst call site (the actual live rejection
// source) can apply the identical strip before validating.
//
// TABLE WAS ADDED HERE BY CHAOS-4637, and the reason is a class worth
// naming (codex round 2, P2, EXECUTED): the model-output DTO is DERIVED
// FROM THIS STRUCT by schema inference, so adding a field to ClaimedFact
// silently widens what a model is allowed to return. A model could then
// author a syntactically valid Table beside a correct scalar claim and NO
// rows, and the wire validator would refuse the whole document --
// "declared table describes rows the fact does not carry" -- turning a good
// answer into a failed one. Observed verbatim on the tip before this fix:
//
//	stripped=0 table_nil=false
//	validate=table: declared table describes rows the fact does not carry
//
// The rule this makes explicit for the next field: ANY field added to a
// model-output DTO becomes model-authorable, and must be either grounded
// or stripped. There is no third option, and "the model would not do that"
// is not one of them.
func StripModelAuthoredClaimedFactTableContent(claims []ClaimedFact) (cleaned []ClaimedFact, strippedCount int) {
	cleaned = make([]ClaimedFact, len(claims))
	for i, claim := range claims {
		// Counted ONCE per claim, not once per field: the telemetry
		// dimension counts CLAIMS that carried hallucinated table content,
		// and a claim carrying both is one such claim.
		if len(claim.Rows) > 0 || claim.Table != nil {
			claim.Rows = nil
			claim.Table = nil
			strippedCount++
		}
		cleaned[i] = claim
	}
	return cleaned, strippedCount
}

// claimGroundingOutcome names how far groundClaim got before it stopped --
// the three distinct failure modes CHAOS-3755's value-level closure has
// always had, now separated so each carries its OWN closed-vocabulary
// rejection reason (CHAOS-4522) instead of three anonymous fmt.Errorf
// returns.
type claimGroundingOutcome int

const (
	// claimNoCanonicalFact: no canonical fact of this claim's Kind was
	// observed for this claim's Subject at all.
	claimNoCanonicalFact claimGroundingOutcome = iota
	// claimFieldUnobserved: facts of that Kind/Subject exist, but NONE of
	// them carries the claimed Field.
	claimFieldUnobserved
	// claimValueContradicts: some fact carries the Field, but no fact
	// carries it with the claimed Value.
	claimValueContradicts
	// claimGrounded: a canonical fact of this Kind/Subject observed this
	// Field with exactly this Value.
	claimGrounded
)

// groundClaim is the value-level evidence closure CHAOS-3755 introduced,
// widened by CHAOS-4522 to close over EVERY canonical fact sharing the
// claim's (Kind, Subject) rather than only the FIRST one.
//
// The first-match-wins lookup this replaces silently assumed at most one
// fact per (Kind, Subject). That assumption is FALSE, and the ranking layer
// already says so in code: cohort_ranking.go's findFact documents that
// "readiness/workload/deficiency aggregate across every fact of their kind
// ... because those producers can legitimately emit several". A live
// discovered_cohort answer for three teams carries 40 team-subject facts,
// of which 17 are readiness|team:CHAOS -- one row per work scope/day. The
// FIRST of those 17 happens not to carry estimate_coverage_ratio, so every
// claim about the readiness coverage gap -- one of the four signals the v2
// cohort ranking is built on -- was rejected as "not canonically observed"
// while the value sat in fact #2 of the same group. That is the whole of
// CHAOS-4522's deterministic 422.
//
// The grounding guarantee is UNCHANGED in strength: a claim is admitted iff
// some canonical fact the model was actually shown observed that field with
// exactly that value, by the same factValueEqualsScalar struct equality.
// What is removed is the arbitrary tiebreak -- slice order was never a
// semantic rule, and "the first fact of this kind" is not something a claim
// can address, because ClaimedFact carries no fact identity to address it
// with.
//
// The matched fact is returned so attachCanonicalRows can attach Rows from
// the SAME fact that grounded the claim (never a different member of the
// group), keeping a claim's scalar and its table describing one observation.
func groundClaim(facts []CanonicalFact, claim ClaimedFact) (CanonicalFact, claimGroundingOutcome) {
	fact, outcome, _ := groundClaimAt(facts, claim)
	return fact, outcome
}

// groundClaimAt is groundClaim plus the ZERO-BASED position, within the
// claim's own (Kind, Subject) group, of the fact that grounded it -- or -1
// when nothing did (codex R3 finding 2).
//
// That position, not the group's size, is what says whether the CHAOS-4522
// closure actually decided the outcome. A claim sitting on a 17-fact group
// whose FIRST member already carried the matching value would have been
// admitted by the old first-match-wins lookup too; reporting "multi-fact
// grounding rescued this answer" for it would be a false claim about which
// branch decided, which is exactly what this telemetry exists to prevent.
// Only a match at position > 0 was impossible before.
func groundClaimAt(facts []CanonicalFact, claim ClaimedFact) (CanonicalFact, claimGroundingOutcome, int) {
	key := subjectKeyForModel(claim.Subject)
	outcome := claimNoCanonicalFact
	var fieldMatch CanonicalFact
	position := 0
	for _, fact := range facts {
		if fact.Kind != claim.Kind || subjectKeyForModel(fact.Subject) != key {
			continue
		}
		if outcome == claimNoCanonicalFact {
			outcome = claimFieldUnobserved
		}
		observed, present := fact.Fields[claim.Field]
		if present {
			if outcome == claimFieldUnobserved {
				outcome = claimValueContradicts
				fieldMatch = fact
			}
			if factValueEqualsScalar(observed, claim.Value) {
				return fact, claimGrounded, position
			}
		}
		position++
	}
	return fieldMatch, outcome, -1
}

// claimSourceFact returns the canonical fact a claim's Rows must come from
// (CHAOS-4522): the fact groundClaim actually admitted the claim against
// when there is one, so a claim's scalar Value and its attached table
// always describe the SAME observation rather than two different rows of
// the same (Kind, Subject) group.
//
// The fallback -- the first fact of the group when no fact grounds the
// claim's value -- is exactly the pre-CHAOS-4522 behavior, kept for two
// reasons. In production it is unreachable: attachCanonicalRows only ever
// runs on claims ValidateAgainst has already admitted, so groundClaim
// returns claimGrounded for every one of them. And a helper whose row
// attachment silently depends on a validation the caller may not have run
// is exactly the hidden coupling this repo's "a measurement that did not
// happen must FAIL, loudly" rule warns about -- so the ungrounded path
// keeps its old, explicit behavior instead of quietly attaching nothing.
func claimSourceFact(facts []CanonicalFact, claim ClaimedFact) (CanonicalFact, bool) {
	if fact, outcome := groundClaim(facts, claim); outcome == claimGrounded {
		return fact, true
	}
	key := subjectKeyForModel(claim.Subject)
	for _, fact := range facts {
		if fact.Kind == claim.Kind && subjectKeyForModel(fact.Subject) == key {
			return fact, true
		}
	}
	return CanonicalFact{}, false
}

// canonicalFactGroupSize counts how many canonical facts share a claim's
// (Kind, Subject) -- the ambiguity groundClaim closes over.
//
// Two callers, deliberately different in scope (codex R2 findings 1 and 4).
// On a REJECTION it is called for the one rejecting claim only, because
// ValidateAgainst short-circuits and a maximum would describe a claim that
// was never evaluated. On SUCCESS every claim was evaluated by definition,
// so MaxClaimFactGroupSize below takes the maximum across all of them --
// there is no short-circuit to misrepresent, and without it the
// outcome-changing "this claim grounded against a LATER fact of its group"
// path would leave no trace in a successful run's own artifacts.
// MaxClaimFactGroupSize is the largest (Kind, Subject) group any admitted
// claim was grounded against -- pure CARDINALITY, context only.
//
// It deliberately does NOT mean "the CHAOS-4522 closure decided this
// answer" (codex R3 finding 2). A claim on a 17-fact group whose FIRST
// member already carried the matching value would have been admitted by the
// old first-match-wins lookup too. ClaimsGroundedBeyondFirstGroupMember
// below is the signal that actually names the deciding branch; this one
// only says how much ambiguity was in play.
//
// Taking a maximum is sound on the SUCCESS path and not on the rejection
// path: a draft that passed validation had every one of its claims
// evaluated, so no claim here was skipped by a short-circuit.
func MaxClaimFactGroupSize(facts []CanonicalFact, claims []ClaimedFact) int {
	maximum := 0
	for _, claim := range claims {
		if size := canonicalFactGroupSize(facts, claim); size > maximum {
			maximum = size
		}
	}
	return maximum
}

// ClaimsGroundedBeyondFirstGroupMember counts the admitted claims that were
// grounded against a fact OTHER than the first of their (Kind, Subject)
// group (codex R3 finding 2). It is the honest "the widened closure decided
// this outcome" signal: every one of these claims would have been REJECTED
// by the pre-CHAOS-4522 first-match-wins lookup, and a count of 0 means the
// closure changed nothing about this answer however large its fact groups
// were.
func ClaimsGroundedBeyondFirstGroupMember(facts []CanonicalFact, claims []ClaimedFact) int {
	count := 0
	for _, claim := range claims {
		if _, outcome, position := groundClaimAt(facts, claim); outcome == claimGrounded && position > 0 {
			count++
		}
	}
	return count
}

func canonicalFactGroupSize(facts []CanonicalFact, claim ClaimedFact) int {
	key := subjectKeyForModel(claim.Subject)
	size := 0
	for _, fact := range facts {
		if fact.Kind == claim.Kind && subjectKeyForModel(fact.Subject) == key {
			size++
		}
	}
	return size
}

// attachCanonicalRows is the ONLY place a ClaimedFact.Rows is ever set
// (CHAOS-4355). It runs on claims that have already passed
// SynthesisDraft.ValidateAgainst, which rejects any draft claim carrying a
// non-empty Rows of its own -- so every claim entering here starts with
// Rows nil. For each claim, it looks up the SAME canonical fact
// ValidateAgainst grounded Value against (identical Kind+Subject lookup)
// and, if that fact carries a renderable table on any of its OWN fields
// (e.g. a project rollup's team_breakdown), copies it onto the claim
// verbatim -- never derived, reworded, or recomputed by either the model or
// this function. A claim whose canonical fact carries no Rows-shaped field
// is left with Rows nil, byte-identical to pre-CHAOS-4355 behavior.
//
// rowsCount is the total number of rows attached across every claim
// (CHAOS-4355's projected_rows_count telemetry dimension); truncated
// reports whether any single claim lost table content relative to what
// its canonical fact actually carried -- either an unambiguous table
// capped to fit ContextFabricClaimedFactMaxRows, or (canonicalFieldRows)
// no table attached at all because the fact carried more than one
// Rows-shaped field and which one a claim means is ambiguous. This is the
// fact-plan-adjacent "dropped by cap/pruning" signal the
// ticket asks for. Both are reported once per successful Synthesize call
// by the caller (see RuntimeAnswerSynthesizer.Telemetry's own doc
// comment for when that is), zero/false included, so a quiet run is as
// visible as a busy one.
//
// byKind (CHAOS-4418) is the SAME rowsCount total, broken down per
// FactKind the model actually claimed something about this call --
// EVERY such kind gets an entry, including a kind that claimed but
// attached zero rows (initialized before the loop below, never added
// lazily only on a nonzero hit), mirroring RecordFactScopeExpansion's own
// "every field is logged, including the zero-valued counts" rule
// (telemetry.go): a kind's absence from this map must mean "the model
// never claimed this kind at all" and never "it claimed it but happened
// to attach zero rows that run" -- those are different facts, and a
// reader diagnosing why a repository-subject's metrics/health Rows count
// is/was 0 needs to tell them apart.
func attachCanonicalRows(claims []ClaimedFact, facts []CanonicalFact) (result []ClaimedFact, rowsCount int, byKind map[FactKind]int, truncated bool) {
	byKind = make(map[FactKind]int, len(claims))
	for i := range claims {
		if _, seen := byKind[claims[i].Kind]; !seen {
			byKind[claims[i].Kind] = 0
		}
		// CHAOS-4522: the SAME fact groundClaim admitted the claim
		// against, never merely the first of its (Kind, Subject) group --
		// a claim's scalar and its table must describe one observation.
		canonical, ok := claimSourceFact(facts, claims[i])
		if !ok {
			continue
		}
		rows, wasTruncated := canonicalFieldRows(canonical)
		// Union BEFORE the empty-rows early exit (codex CHAOS-4355 R3 P2
		// finding): canonicalFieldRows returns (nil, true) for its
		// fail-closed ambiguous-fields case, and that drop must still
		// reach the caller even though there is no table to attach --
		// dropping it here would silently contradict this function's own
		// "reported unconditionally" promise for the exact case the
		// promise exists to cover.
		truncated = truncated || wasTruncated
		if len(rows) == 0 {
			continue
		}
		claims[i].Rows = rows
		// CHAOS-4637: the DECLARATION travels with the rows, off the
		// SAME field selection (canonicalRowsField), so the claim can
		// never end up declaring one field's shape over another field's
		// rows. Nil when the chosen field carries no declared Table --
		// every pre-CHAOS-4633 producer -- which is the wire saying
		// "undeclared", and undeclared is never charted.
		claims[i].Table = canonicalFieldTable(canonical)
		rowsCount += len(rows)
		byKind[claims[i].Kind] += len(rows)
	}
	return claims, rowsCount, byKind, truncated
}

// canonicalFieldRows returns the canonical fact's ONE Rows-shaped field,
// converted verbatim -- and NOTHING when the fact carries more than one
// (CHAOS-4355 codex R2 P1 finding, sharpening R1's first attempt: picking
// an arbitrary field by sort order is deterministic, but a claim carries no
// row-field identity to say WHICH table it means, so presenting one
// lexically-chosen table risks presenting the WRONG canonical table as if
// it were authoritative -- worse than a claim's Rows simply staying nil).
// A fact could in principle carry more than one renderable table (e.g. a
// future producer alongside MetricsProvider's project rollup, which emits
// exactly one, team_breakdown); until a claim can name which field it
// means, this fails closed rather than guesses, the identical "retract
// rather than trust an unverified assertion" posture the rest of this file
// already applies elsewhere -- and reports the drop via the second return
// value so it is counted, not silent. The same return value also covers a
// cap: a single unambiguous table is truncated to fit
// ContextFabricClaimedFactMaxRows -- the exact bound
// ContextFabricClaimedFact.Validate() already enforces, so a canonical
// fact's own table can never make an otherwise-valid claim fail contract
// validation -- and a cap that actually binds is reported rather than
// silently dropping the tail.
func canonicalFieldRows(fact CanonicalFact) (rows []contractsv1.ContextFabricClaimedFactRow, truncated bool) {
	rowsField, ambiguous, ok := canonicalRowsField(fact)
	if !ok {
		return nil, ambiguous
	}
	source := fact.Fields[rowsField].Rows
	rows = make([]contractsv1.ContextFabricClaimedFactRow, 0, len(source))
	for _, row := range source {
		rows = append(rows, convertFactValueRow(row))
	}
	if len(rows) > contractsv1.ContextFabricClaimedFactMaxRows {
		return rows[:contractsv1.ContextFabricClaimedFactMaxRows], true
	}
	return rows, false
}

// canonicalRowsField is the ONE place a fact's Rows-shaped field is chosen.
// canonicalFieldRows and canonicalFieldTable both go through it so the rows
// a claim serves and the declaration describing them can never come from
// two different fields -- a divergence that would be invisible on the wire
// and would make the declaration a lie rather than a guard.
//
// ok=false with ambiguous=true is the CHAOS-4355 fail-closed case the
// caller must still report; ok=false with ambiguous=false simply means the
// fact carries no table at all.
func canonicalRowsField(fact CanonicalFact) (field string, ambiguous bool, ok bool) {
	var rowsField string
	rowsFieldCount := 0
	for key, value := range fact.Fields {
		if len(value.Rows) > 0 {
			rowsFieldCount++
			rowsField = key
		}
	}
	switch rowsFieldCount {
	case 0:
		return "", false, false
	case 1:
		return rowsField, false, true
	default:
		// CHAOS-4645 ruling: the ambiguity CHAOS-4355 fails closed on is no
		// longer unresolvable now that CHAOS-4633 gives every table an
		// explicit, registry-asserted Shape. Multiple Rows-shaped fields
		// resolve DETERMINISTICALLY, never by heuristic or field-name
		// convention, when the declared shapes pick out exactly one
		// non-time_series field: that field is the pre-CHAOS-4645 legacy
		// table (a breakdown, a ranking, or -- for a fact this ticket never
		// touched -- a field with no Table declaration at all, which is
		// unambiguously "not a new time_series" too). Serving it keeps the
		// wire byte-for-byte what it was before CHAOS-4645 added a SECOND,
		// additive table nothing downstream reads yet (that migration is
		// P2, by design -- see FactTable's own doc comment on the
		// migration phases). Two non-time_series tables, or two
		// time_series tables, give no way to prefer one over the other and
		// still fail closed exactly as CHAOS-4355 established.
		if field, unique := uniqueNonTimeSeriesRowsField(fact); unique {
			return field, false, true
		}
		return "", true, false
	}
}

// canonicalFieldTable is CHAOS-4637's declaration hop: the wire declaration
// of the SAME field canonicalRowsField chose, or nil when that field
// carries none.
//
// Nil is not a degraded state that wants a fallback -- it is the statement
// "this table is undeclared", and CHAOS-4627 rules that an undeclared table
// is never charted. Inferring a shape here from the rows would reinstate
// exactly the geometry inference this slice deletes.
func canonicalFieldTable(fact CanonicalFact) *contractsv1.ContextFabricClaimedFactTable {
	field, _, ok := canonicalRowsField(fact)
	if !ok {
		return nil
	}
	declared := fact.Fields[field].Table
	if declared == nil {
		return nil
	}
	return &contractsv1.ContextFabricClaimedFactTable{
		Field:        field,
		Shape:        contractsv1.ContextFabricFactTableShape(declared.Shape),
		Key:          append([]string(nil), declared.Key...),
		Measures:     append([]string(nil), declared.Measures...),
		Observations: append([]string(nil), declared.Observations...),
		OrderBy:      declared.OrderBy,
	}
}

// uniqueNonTimeSeriesRowsField is canonicalFieldRows' CHAOS-4645
// disambiguation: among a fact's Rows-shaped fields, the ones that are NOT
// declared time_series (a nil Table -- every field this repository minted
// before CHAOS-4633 -- or a declared Table whose Shape is breakdown/
// ranking). Reports ok=true only when EXACTLY ONE such field exists; two or
// zero is exactly as ambiguous as canonicalFieldRows' own multi-field case,
// and it is the caller's job to fail closed on that, not this function's.
func uniqueNonTimeSeriesRowsField(fact CanonicalFact) (field string, ok bool) {
	found := ""
	count := 0
	for key, value := range fact.Fields {
		if len(value.Rows) == 0 {
			continue
		}
		if value.Table != nil && value.Table.Shape == FactTableTimeSeries {
			continue
		}
		count++
		found = key
	}
	return found, count == 1
}

func convertFactValueRow(row FactValueRow) contractsv1.ContextFabricClaimedFactRow {
	fields := make(map[string]contractsv1.ContextFabricScalarValue, len(row.Fields))
	for key, value := range row.Fields {
		fields[key] = convertFactValueScalar(value)
	}
	return contractsv1.ContextFabricClaimedFactRow{Fields: fields}
}

// convertFactValueScalar converts one row cell. Row cells are LEAF values
// by construction (FactValue.validate rejects Rows-within-Rows), so every
// variant here has a direct ContextFabricScalarValue counterpart -- the two
// types share the identical String/Integer/Number/Boolean/Null shape.
func convertFactValueScalar(value FactValue) contractsv1.ContextFabricScalarValue {
	return contractsv1.ContextFabricScalarValue{
		String: value.String, Integer: value.Integer, Number: value.Number, Boolean: value.Boolean, Null: value.Null,
	}
}

type ModelRuntime interface {
	InterpretQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error)
	SynthesizeAnswer(context.Context, storage.Principal, SynthesisInput) (SynthesisDraft, ModelExecutionReceipt, error)
}

type RuntimeQuestionInterpreter struct {
	Runtime ModelRuntime
	Sink    ModelReceiptSink
	// FamilyTelemetry (CHAOS-4632, SHADOW ONLY) receives one
	// QuestionFamilyResolutionEvent per Interpret call that produced a
	// usable interpretation.
	//
	// AN EXPLICIT, WIRED FIELD -- never a type assertion on Runtime or on
	// some optional interface. CHAOS-4085's whole lesson (see
	// chaos4085_telemetry_sink_test.go's header) is that
	// CommitAffirmationTelemetry was optional, nothing in production
	// implemented it, every retraction failed a type assertion, and the
	// entire event disappeared while tests passed. A nil here means an
	// operator sees nothing, which is why open.go wires it and
	// TestSlogEngineTelemetryLogsQuestionFamilyResolution asserts the
	// PRODUCTION sink's own bytes rather than a struct field.
	FamilyTelemetry QuestionFamilyTelemetry
}

// THE ENSEMBLE SIZE IS DELIBERATELY NOT A FIELD HERE, and the absence is
// the honest statement rather than an oversight.
//
// This interpreter runs at N=1: the single interpret call Interpret
// already makes is the only sample, the §4.2 precedence table decides on
// its own, and ResolveQuestionFamily records source=model -- the design's
// own degrade path (§4.1: "the resolver falls back to N=1 plus the
// precedence table, recording source = model rather than model_consensus
// -- a visibly weaker guarantee rather than an invisible cost"). No extra
// model call is made, which is what makes "zero behaviour change" a
// PROVABLE property of the merged default rather than an assertion: no
// extra latency, no extra tokens, and nothing gated on the outcome either
// way.
//
// N>1 needs a per-sample interpret call, which now EXISTS: S1
// (CHAOS-4631, merged d00080fd) shipped
// genkitruntime.Runtime.InterpretQuestionForSample, and
// ResolveQuestionFamilyEnsemble takes a sampler over sample INDICES that
// calls it. What is still absent is a reason to turn it on. The measured
// result on the labelled set (12 cases x 2 replicates, kiac/dh_0830 real
// data) was 100% resolved-family stability AT N=1 -- the model's own
// family hint was only 83.3% stable and it did not matter, because the
// precedence table reads structure signals above Shape and treats that
// hint as a hint. So the ensemble currently buys nothing measurable, and
// its price would be paid on every turn 1, the product's most frequent
// call.
//
// This type therefore still has no ensemble-size field. A configuration
// knob wired to a mechanism that demonstrably improves nothing is a knob
// that invites spending for no gain -- the same reasoning that kept it out
// while the call site was missing, now resting on a measurement instead of
// an absence. Add it when a corpus shows N=1 failing.

func (r RuntimeQuestionInterpreter) Interpret(ctx context.Context, principal storage.Principal, request InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	if r.Runtime == nil {
		return InterpretedQuestion{}, QuestionFamilyOutcome{}, ErrModelUnavailable
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
			return InterpretedQuestion{}, QuestionFamilyOutcome{}, sinkErr
		}
		err = errors.Join(err, sinkErr)
	}
	if err != nil {
		return InterpretedQuestion{}, QuestionFamilyOutcome{}, err
	}
	// CHAOS-4632/CHAOS-4634: resolve the question family from the signals
	// this interpretation actually produced, report it, and -- as of S4 --
	// RETURN it too, so the caller (Engine) can gate offer composition on
	// it. S2 shipped this same resolution shadow-only (telemetered,
	// discarded); S4 is the slice where the family first affects an
	// answer, so it must reach past this function's own return.
	//
	// Placed AFTER every error return, so the event fires exactly when an
	// interpretation was produced -- which makes the denominator
	// "investigations that reached interpretation", the same denominator
	// every other per-interpretation signal already has. An answer served
	// from the reuse store never reaches here, and it never reaches
	// Interpret at all, so the two agree.
	//
	// `question` itself is returned unchanged whatever the family resolves
	// to -- the family is a SEPARATE return value, never folded onto the
	// wire InterpretedQuestion.
	outcome := r.recordFamilyResolution(ctx, principal, question, receipt)
	return question, outcome, nil
}

// recordFamilyResolution builds the §4.2 sample from one interpretation's
// receipt capture, resolves the family, emits the §4.3 event, and returns
// the resolved outcome for the caller to gate on (CHAOS-4634).
//
// FIRES ON EVERY INTERPRETATION, including the ones that resolve to
// unclassified -- the denominator has to be countable, or "the resolver
// never classifies anything" and "the resolver never ran" become the same
// observation. That is the lesson lane-4579 wrote up in its §4 and codex
// confirmed by mutation in its finding 5.
//
// r.FamilyTelemetry == nil (a composition that never wired it) still
// resolves and returns the outcome -- CHAOS-4634 gating must not silently
// go dark just because telemetry was never configured; only the EVENT
// emission is telemetry-gated, exactly as every other EngineTelemetry
// call in this package is.
func (r RuntimeQuestionInterpreter) recordFamilyResolution(ctx context.Context, principal storage.Principal, interpreted InterpretedQuestion, receipt ModelExecutionReceipt) QuestionFamilyOutcome {
	samples := []FamilySample{familySampleFrom(interpreted, receipt)}
	outcome := ResolveQuestionFamily(samples)
	if r.FamilyTelemetry != nil {
		r.FamilyTelemetry.RecordQuestionFamilyResolution(ctx, principal, QuestionFamilyResolutionEventFrom(outcome, samples))
	}
	return outcome
}

// familySampleFrom projects ONE interpretation into the precedence table's
// input.
//
// The Shape and terms come from the InterpretedQuestion this very call
// produced, and the four new signals from the SAME call's sanitized
// receipt capture -- never from a re-interpretation and never from two
// different calls. A sample must be exactly what ONE model call produced,
// or the consensus is aggregating over a combination no model proposed,
// which is the same field-wise fabrication selectWinningSample exists to
// prevent one level up.
func familySampleFrom(interpreted InterpretedQuestion, receipt ModelExecutionReceipt) FamilySample {
	return FamilySample{
		Shape:                   interpreted.Shape,
		SubjectTerms:            interpreted.SubjectTerms,
		ComparisonTerms:         interpreted.ComparisonTerms,
		GroupKind:               receipt.GroupKind,
		ScopeAnchorTerm:         receipt.ScopeAnchorTerm,
		ScopeAnchorKind:         receipt.ScopeAnchorKind,
		RequestedKind:           receipt.RequestedSubjectKind,
		ModelFamily:             receipt.QuestionFamily,
		ModelFamilyUnrecognized: receipt.QuestionFamilyUnrecognized,
	}
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
	// Telemetry is OPTIONAL (nil-safe, same convention as Sink) -- CHAOS-4355's
	// RecordProjectedRowsCount is reported here, once per Synthesize call
	// that reaches claim assembly (i.e. draft.ValidateAgainst already
	// passed), count=0 included whenever no cited canonical fact carried a
	// renderable table -- so "no producer emitted rows" stays
	// distinguishable from "nobody is counting". A call that returns
	// earlier (Runtime nil, a rejected draft, a receipt-sink failure)
	// reports nothing here: there are no claims to report rows for, and
	// that failure is already the receipt sink's own Outcome to record
	// (codex CHAOS-4355 R1 P2 finding -- this is a documentation fix, not
	// a behavior change: "unconditional" previously overclaimed coverage
	// this method never actually had). A nil Telemetry (every test that
	// builds a synthesizer by hand) behaves exactly as before this field
	// existed.
	Telemetry EngineTelemetry
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
		// CHAOS-4355 follow-up (tolerance): a model that still authors
		// ClaimedFact.Rows despite CHAOS-4364's model-facing facts no
		// longer showing it any Rows-shaped field (a bare hallucination,
		// not a value it could have copied) must not have its WHOLE
		// otherwise-valid answer rejected for it -- Rows are attached
		// server-side from the SAME canonical fact the claim cites, in
		// attachCanonicalRows below, so a model-authored Rows array is
		// pure noise to discard, never signal to trust or reject on. This
		// is defense-in-depth alongside the identical strip
		// genkitruntime.Runtime.SynthesizeAnswer already applies before
		// its OWN ValidateAgainst call (the actual production rejection
		// site) -- a draft reaching here has normally already been
		// stripped, so stripped is normally 0; this still runs for any
		// ModelRuntime implementation that does not strip on its own.
		var stripped int
		draft.ClaimedFacts, stripped = StripModelAuthoredClaimedFactTableContent(draft.ClaimedFacts)
		if stripped > 0 && r.Telemetry != nil {
			r.Telemetry.RecordModelRowsStripped(ctx, principal, stripped)
		}
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
	// CHAOS-4355: attachCanonicalRows is the ONLY place a ClaimedFact.Rows
	// is ever set. ValidateAgainst has just rejected any draft claim that
	// carried a non-empty Rows of its own, so every claim reaching this
	// point starts with Rows nil -- what follows copies rows verbatim from
	// the canonical fact each claim cites, never from the model, closing
	// the routing gap the CHAOS-4347 rejection above left open.
	claimedFacts, rowsCount, rowsByKind, rowsTruncated := attachCanonicalRows(cloneSlice(draft.ClaimedFacts), input.Facts.Facts)
	if r.Telemetry != nil {
		r.Telemetry.RecordProjectedRowsCount(ctx, principal, rowsCount, rowsTruncated)
		// CHAOS-4418: the SAME total, broken down per FactKind -- see
		// attachCanonicalRows' own doc comment for why a claimed-but-zero
		// kind must still appear (never omitted alongside a kind never
		// claimed at all).
		r.Telemetry.RecordProjectedRowsByFactKind(ctx, principal, rowsByKind)
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
		ClaimedFacts:       claimedFacts,
		Coverage:           MergeCoverage(principal.OrgID, input.Graph.Coverage, input.Facts.Coverage),
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
	// CHAOS-4690 Commit F (design §4.2): the coverage-disclosure guard
	// runs AFTER result is fully composed -- result.Coverage carries the
	// SAME merged, ordinal-DetailID-minted details the caller is about to
	// serve, which is exactly what ref closure must check against (never
	// input.Graph.Coverage/input.Facts.Coverage, the unmerged halves).
	// Never rejects the answer: every branch below only decides whether
	// Phrasing gets applied, never whether Synthesize returns an error.
	outcome, violation := classifyCoverageDisclosures(draft, &result)
	if violation != "" {
		// Content-safe by construction, same discipline as
		// mergeCoverageDetails' own fail-open WARN above: org id and the
		// closed violation class only, never the model's own detail_id or
		// text (r2 F5's "content-safe by construction" rule applied to
		// this guard too).
		slog.Default().Warn("context_fabric: coverage disclosure guard discarded the model's whole disclosure set",
			"org_id", principal.OrgID, "violation", string(violation))
	}
	if r.Telemetry != nil {
		phrased, total := coverageDisclosurePhrasedCount(result.Coverage.Details)
		r.Telemetry.RecordCoverageDisclosurePhrasing(ctx, principal, outcome, phrased, total)
	}
	return result, nil
}

// CoverageDisclosureOutcome is the closed vocabulary
// EngineTelemetry.RecordCoverageDisclosurePhrasing reports (CHAOS-4690
// Commit F, design §4.2).
type CoverageDisclosureOutcome string

const (
	// CoverageDisclosurePhrased: every one of result.Coverage.Details
	// received a guard-verified Phrasing from this call's disclosure set.
	CoverageDisclosurePhrased CoverageDisclosureOutcome = "phrased"
	// CoverageDisclosurePartialAbsent: the guard applied a non-empty,
	// fully-valid disclosure set, but it named FEWER detail_ids than
	// result.Coverage.Details carries -- the model exercised its ruled
	// latitude to phrase SOME entries and leave others Label-only, which
	// is legitimate (design §4.1: "the model MAY write one plain-language
	// sentence" per entry, never must), not a guard failure.
	CoverageDisclosurePartialAbsent CoverageDisclosureOutcome = "partial_absent"
	// CoverageDisclosureRejectedByGuard: the model returned a
	// well-formed, decodable disclosure set, but at least one entry
	// violated ref closure, uniqueness, the text bound, or the digit ban
	// -- the WHOLE set was discarded, every detail ships Label-only.
	CoverageDisclosureRejectedByGuard CoverageDisclosureOutcome = "rejected_by_guard"
	// CoverageDisclosureDiscardedUndecodable: the model's raw
	// coverage_disclosures value was present but could not be unmarshalled
	// into []CoverageDisclosure (SynthesisDraft.CoverageDisclosuresUndecodable) --
	// distinct from CoverageDisclosureAbsent so an operator can tell
	// "the model tried and we could not read it" from "the model offered
	// nothing this call".
	CoverageDisclosureDiscardedUndecodable CoverageDisclosureOutcome = "discarded_undecodable"
	// CoverageDisclosureAbsent: the model omitted coverage_disclosures (or
	// sent an empty/null value) -- the ordinary, expected shape on a call
	// with nothing degrading to phrase, or a model that chose not to
	// phrase anything this turn.
	CoverageDisclosureAbsent CoverageDisclosureOutcome = "absent"
)

// CoverageDisclosureViolation is the closed vocabulary the guard-rejection
// WARN log line's "violation" attribute carries (CHAOS-4690 Commit F,
// design §4.2) -- content-safe by construction, same discipline as every
// other closed-enum-only log line in this file.
type CoverageDisclosureViolation string

const (
	// CoverageDisclosureViolationUnknownDetailID: a disclosure's detail_id
	// names no entry on result.Coverage.Details -- the ref-closure clause
	// ("a disclosure must be traceable to the structured reason it
	// phrases", design §4.2).
	CoverageDisclosureViolationUnknownDetailID CoverageDisclosureViolation = "unknown_detail_id"
	// CoverageDisclosureViolationDuplicateDetailID: the same detail_id
	// appears more than once in one disclosure set.
	CoverageDisclosureViolationDuplicateDetailID CoverageDisclosureViolation = "duplicate_detail_id"
	// CoverageDisclosureViolationTextBound: a disclosure's Text is empty
	// after trimming, carries leading/trailing whitespace, or exceeds
	// contractsv1.ContextFabricCoverageDetailPhrasingMaxLength runes.
	CoverageDisclosureViolationTextBound CoverageDisclosureViolation = "text_bound"
	// CoverageDisclosureViolationDigitsForbidden: a disclosure's Text
	// carries a Unicode digit rune anywhere -- quantities are the
	// deterministic Label's job alone (design §4.1/r1 F5).
	CoverageDisclosureViolationDigitsForbidden CoverageDisclosureViolation = "digits_forbidden"
	// CoverageDisclosureViolationParseFailed: the raw subdocument itself
	// could not be unmarshalled (SynthesisDraft.CoverageDisclosuresUndecodable) --
	// reported through the SAME closed vocabulary as the guard's own
	// clauses so one log line shape covers both discard reasons.
	CoverageDisclosureViolationParseFailed CoverageDisclosureViolation = "parse_failed"
)

// classifyCoverageDisclosures is Synthesize's own dispatcher: it picks the
// discarded_undecodable outcome directly off SynthesisDraft's decode-layer
// flag (never re-derived here -- the parse either succeeded or it did not,
// and only genkitruntime's lenient parser can know which), and otherwise
// defers to applyCoverageDisclosures for the guard-clause classification
// against the ALREADY-composed result.
func classifyCoverageDisclosures(draft SynthesisDraft, result *InvestigationResult) (CoverageDisclosureOutcome, CoverageDisclosureViolation) {
	if draft.CoverageDisclosuresUndecodable {
		return CoverageDisclosureDiscardedUndecodable, CoverageDisclosureViolationParseFailed
	}
	return applyCoverageDisclosures(result, draft.CoverageDisclosures)
}

// applyCoverageDisclosures is the CHAOS-4690 Commit F guard (design §4.2).
// It runs against result.Coverage.Details -- the SAME merged, ordinal
// DetailID-minted set the caller is about to serve -- and enforces, in
// order:
//
//  1. every disclosure's DetailID names a detail on result.Coverage.Details
//     (ref closure: "a disclosure must be traceable to the structured
//     reason it phrases");
//  2. no DetailID repeats within the set;
//  3. Text is non-empty after trimming, trims to itself (no
//     leading/trailing whitespace), and is at most
//     contractsv1.ContextFabricCoverageDetailPhrasingMaxLength runes
//     (mirrors ContextFabricCoverageDetail.Validate's own Phrasing bound,
//     validate_context_fabric_result.go / context_fabric_coverage_detail.go);
//  4. Text carries no Unicode digit rune anywhere -- quantities are the
//     deterministic Label's job, never the model's.
//
// ANY violation on ANY entry discards the WHOLE set -- the same
// OfferPhraser discipline classifyOfferPhrasingDraft applies
// (chaos4171_offer_phrasing.go): a partially-trusted disclosure set is
// treated exactly like an untrusted one, never applied piecemeal. On a
// discard (or an empty/absent set), result is left completely unmodified;
// on success, survivors are written into their matching detail's Phrasing
// field, in place. Pure and side-effect-free beyond that in-place write --
// no logging, no telemetry -- so each clause is testable in isolation.
func applyCoverageDisclosures(result *InvestigationResult, disclosures []CoverageDisclosure) (CoverageDisclosureOutcome, CoverageDisclosureViolation) {
	if len(disclosures) == 0 {
		return CoverageDisclosureAbsent, ""
	}
	indexByDetailID := make(map[string]int, len(result.Coverage.Details))
	for i, detail := range result.Coverage.Details {
		indexByDetailID[detail.DetailID] = i
	}
	seen := make(map[string]struct{}, len(disclosures))
	targets := make([]int, 0, len(disclosures))
	for _, disclosure := range disclosures {
		idx, ok := indexByDetailID[disclosure.DetailID]
		if !ok {
			return CoverageDisclosureRejectedByGuard, CoverageDisclosureViolationUnknownDetailID
		}
		if _, duplicate := seen[disclosure.DetailID]; duplicate {
			return CoverageDisclosureRejectedByGuard, CoverageDisclosureViolationDuplicateDetailID
		}
		seen[disclosure.DetailID] = struct{}{}
		trimmed := strings.TrimSpace(disclosure.Text)
		if trimmed == "" || trimmed != disclosure.Text || utf8.RuneCountInString(disclosure.Text) > contractsv1.ContextFabricCoverageDetailPhrasingMaxLength {
			return CoverageDisclosureRejectedByGuard, CoverageDisclosureViolationTextBound
		}
		for _, r := range disclosure.Text {
			if unicode.IsDigit(r) {
				return CoverageDisclosureRejectedByGuard, CoverageDisclosureViolationDigitsForbidden
			}
		}
		targets = append(targets, idx)
	}
	for i, disclosure := range disclosures {
		result.Coverage.Details[targets[i]].Phrasing = disclosure.Text
	}
	if len(disclosures) < len(result.Coverage.Details) {
		return CoverageDisclosurePartialAbsent, ""
	}
	return CoverageDisclosurePhrased, ""
}

// coverageDisclosurePhrasedCount reports, from the result's own final
// Coverage.Details -- never from the model's disclosure count -- how many
// details actually carry a Phrasing and how many details exist in total.
// Reading it back off the result (rather than threading a count through
// applyCoverageDisclosures' return) means the reported number is always
// what the served answer actually contains, on every outcome including the
// three that apply nothing (rejected_by_guard, discarded_undecodable,
// absent), where phrased is always 0.
func coverageDisclosurePhrasedCount(details []CoverageDetail) (phrased, total int) {
	total = len(details)
	for _, detail := range details {
		if detail.Phrasing != "" {
			phrased++
		}
	}
	return phrased, total
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
	return composeDirectJudgmentFrom(draft.Status, draft.Drivers, resolution)
}

// composeDirectJudgmentFrom is composeDirectJudgment's body, taking the
// three values it actually reads instead of the draft that carries them.
//
// Split out for CHAOS-4098, which must RE-render this field after the
// engine overrides a synthesized status (see applySynthesisStatusOverride):
// the engine holds an already-composed InvestigationResult, never the
// SynthesisDraft, and the result's Drivers are the draft's Drivers copied
// verbatim (Synthesize above), so the same rendering is reachable from
// both sides. A wrapper rather than a copy: one definition of what this
// field says means an override cannot render it differently from the
// original composition, which is the whole point of recomposing rather
// than patching the leading sentence.
func composeDirectJudgmentFrom(status InvestigationStatus, drivers []DriverJudgment, resolution SubjectResolution) string {
	var b strings.Builder
	b.WriteString(statusSentence(status, resolution))
	if titles := principalDriverTitles(drivers); len(titles) > 0 {
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
// already-validated structured fields only (Status, principal Drivers) --
// see the call site's comment for why. It never reads
// draft.DirectJudgment/CurrentState/DeterministicAnswer (unvalidated
// model prose).
//
// CHAOS-4699: this used to also append a "Canonical facts: k=v; k=v." raw
// sentence list built from ClaimedFacts. Removed under the standing
// language principle (chris 2026-08-31 13:35, CHAOS-4690): deterministic
// lead prose composes the status floor only; the facts already live in
// their own structured fields (ClaimedFacts) and in composeCurrentState's
// one canonical statement -- restating them here was pure duplication in
// internal-ish k=v vocabulary, not new information. The cohort path's
// equivalent duplication (CHAOS-4580/CHAOS-4636) was removed the same way.
// ClaimedFacts is still validated against canonical facts by
// ValidateAgainst before this ever runs, so nothing here can diverge from
// a canonical value -- there's just no longer a reason to restate it in
// this field too.
func composeDeterministicAnswer(draft SynthesisDraft, resolution SubjectResolution) string {
	return composeDeterministicAnswerFrom(draft.Status, draft.Drivers, draft.ClaimedFacts, resolution)
}

// composeDeterministicAnswerFrom is composeDeterministicAnswer's body.
// Same split, same reason, as composeDirectJudgmentFrom above -- see that
// function's doc comment.
//
// claims is accepted but deliberately unread (see composeDeterministicAnswer's
// doc comment) -- kept as a parameter rather than dropped so every existing
// call site (the CHAOS-4098 status-override recompose, tests deriving an
// expected value from this same renderer) keeps calling the shared
// renderer with the same arguments composeDeterministicAnswer itself
// passes, instead of each caller needing to know which fields this
// renderer currently reads.
func composeDeterministicAnswerFrom(status InvestigationStatus, drivers []DriverJudgment, _ []ClaimedFact, resolution SubjectResolution) string {
	var b strings.Builder
	b.WriteString(statusSentence(status, resolution))
	if titles := principalDriverTitles(drivers); len(titles) > 0 {
		b.WriteString(" Principal driver(s): ")
		b.WriteString(strings.Join(titles, "; "))
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

// MergeCoverage is the SINGLE pure coverage normalizer (CHAOS-4690 §3.4),
// shared by BOTH merge call sites: this package's own Synthesize (above)
// and genkitruntime's synthesis-input composition
// (genkitruntime.synthesisInputFromDomain / BuildSynthesisPrompt) route the
// SAME two coverage groups through this one function, so their merged
// Sources/DegradedReasons/Details and minted DetailIDs can never
// independently drift between the result the caller stores and the input
// the model actually saw.
//
// orgID is used ONLY for the fail-open reconcile WARN log below -- never
// for merge semantics, which are a pure function of groups alone. Callers
// with no authenticated principal in scope (genkitruntime.BuildSynthesisPrompt's
// prompt-preview path) pass "".
func MergeCoverage(orgID string, groups ...Coverage) Coverage {
	bySource := make(map[string]SourceObservation)
	degraded := make(map[string]struct{})
	partial := false
	var allDetails []CoverageDetail
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
		allDetails = append(allDetails, group.Details...)
	}
	sources := make([]SourceObservation, 0, len(bySource))
	for _, source := range bySource {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Source < sources[j].Source })
	// CHAOS-4690 item 4: every merged source chip carries its display
	// label, stamped from the contracts registry AFTER the priority merge
	// above -- the label describes whichever observation actually won.
	for i := range sources {
		sources[i].Label = contractsv1.ContextFabricSourceObservationLabel(sources[i].Source)
		sources[i].StateLabel = contractsv1.ContextFabricSourceStateLabel(sources[i].State)
	}
	// reasons: EXACTLY today's computation (dedupe map + sort.Strings) --
	// the golden anchor this ticket must never move. Computed independently
	// of allDetails below, so a defect on the structured half can never
	// touch the field every existing consumer already parses.
	reasons := make([]string, 0, len(degraded))
	for reason := range degraded {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)

	return Coverage{
		Sources: sources, Partial: partial || len(reasons) > 0, DegradedReasons: reasons,
		Details: mergeCoverageDetails(allDetails, reasons, orgID),
	}
}

// mergeCoverageDetails is MergeCoverage's own Details half (design §3.4):
// concatenate, sort/dedupe each half separately, RECONCILE the degrading
// half's derived Raw sequence against the ALREADY-COMPUTED reasons
// (MergeCoverage's own golden-anchor computation above, never recomputed
// here), and mint final ordinal ids over the reconciled order.
//
// Fail-open, never fail-closed: a mismatch (a group carried a
// DegradedReasons entry with no paired detail -- a legacy test double, or a
// future producer bug) drops the WHOLE Details array rather than ship a
// structured surface the write-path validator would 500 the investigation
// over (validateCoverageDetails' own 1:1 pairing requirement). The legacy
// degraded_reasons[] computed by the caller is completely unaffected
// either way -- this function can only ever narrow Details, never widen or
// alter reasons.
func mergeCoverageDetails(all []CoverageDetail, reasons []string, orgID string) []CoverageDetail {
	var degrading, nonDegrading []CoverageDetail
	for _, d := range all {
		if d.Degrading {
			degrading = append(degrading, d)
		} else {
			nonDegrading = append(nonDegrading, d)
		}
	}
	sort.SliceStable(degrading, func(i, j int) bool { return degrading[i].Raw < degrading[j].Raw })
	degrading = dedupeCoverageDetailsByRaw(degrading)
	sort.SliceStable(nonDegrading, func(i, j int) bool {
		if nonDegrading[i].Source != nonDegrading[j].Source {
			return nonDegrading[i].Source < nonDegrading[j].Source
		}
		if nonDegrading[i].Code != nonDegrading[j].Code {
			return nonDegrading[i].Code < nonDegrading[j].Code
		}
		return nonDegrading[i].Raw < nonDegrading[j].Raw
	})
	nonDegrading = dedupeCoverageDetailsBySourceCodeRaw(nonDegrading)

	derived := make([]string, 0, len(degrading))
	for _, d := range degrading {
		derived = append(derived, d.Raw)
	}
	if !equalStringSlices(derived, reasons) {
		// Content-safe by construction: org id and two counts, never a
		// segment/reason string (engine.go:251's telemetry contract applies
		// equally to a diagnostic log line -- this fires from a runtime
		// package too, genkitruntime, which has no access to the engine's
		// own EngineTelemetry sink).
		slog.Default().Warn("context_fabric: coverage detail derivation did not reconcile with degraded_reasons, dropping structured details",
			"org_id", orgID, "degrading_details", len(derived), "degraded_reasons", len(reasons))
		return nil
	}

	merged := make([]CoverageDetail, 0, len(degrading)+len(nonDegrading))
	merged = append(merged, degrading...)
	merged = append(merged, nonDegrading...)
	if len(merged) == 0 {
		return nil
	}
	for i := range merged {
		merged[i].DetailID = fmt.Sprintf("cov-%02d", i+1)
	}
	return merged
}

func dedupeCoverageDetailsByRaw(details []CoverageDetail) []CoverageDetail {
	if len(details) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(details))
	out := make([]CoverageDetail, 0, len(details))
	for _, d := range details {
		if _, ok := seen[d.Raw]; ok {
			continue
		}
		seen[d.Raw] = struct{}{}
		out = append(out, d)
	}
	return out
}

func dedupeCoverageDetailsBySourceCodeRaw(details []CoverageDetail) []CoverageDetail {
	if len(details) == 0 {
		return nil
	}
	type key struct{ source, code, raw string }
	seen := make(map[key]struct{}, len(details))
	out := make([]CoverageDetail, 0, len(details))
	for _, d := range details {
		k := key{d.Source, string(d.Code), d.Raw}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, d)
	}
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
