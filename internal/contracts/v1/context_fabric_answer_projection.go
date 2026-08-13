package v1

import "time"

// ContextFabricAnswerProjectionSchema identifies the bounded consumer
// projection of a ContextFabricInvestigationResult (CHAOS-3746).
//
// The projection exists so that a bounded consumer -- an MCP agent today,
// any other narrow surface later -- receives the same judgment the hosted
// API produced, in a shape small enough to be useful, without any consumer
// growing its own summariser. It is produced by exactly one function,
// internal/contextfabric/answerprojection.Project, which both the API and
// MCP surfaces call. That single choke point is what makes API/MCP parity
// structural rather than a test convention.
//
// BINDING RULE: the projection SELECTS and DROPS. It never rewrites,
// re-ranks, re-judges, or re-words. DirectJudgment, CurrentState, and every
// retained Standing and Category are copied verbatim from the canonical
// result. A consumer's wording or truncation can therefore never change the
// underlying judgment (the CHAOS-3746 parity requirement).
//
// Every drop is declared in ProjectionBudget. Silent truncation is a defect:
// a caller that cannot see what was omitted cannot tell a short answer from
// a complete one.
//
// This contract is deliberately self-contained rather than composed from
// context_fabric_common.v1's $defs. Its sub-shapes are genuinely narrower
// than the canonical ones (ContextFabricProjectedDriver carries a fraction
// of ContextFabricDriverJudgment; ContextFabricProjectedCohort is not a
// ContextFabricCohort), and embedding all of context_fabric_common.v1 into
// an agent-facing MCP tool schema would ship graph-projection vocabulary no
// answer consumer uses. The one real hazard of a separate shape is enum
// drift, and that is closed by an explicit parity test binding every
// projected vocabulary back to the canonical constants -- see
// context_fabric_answer_projection_test.go.
const ContextFabricAnswerProjectionSchema = "context_fabric_answer_projection.v1"

// Projection bounds. Each is <= its canonical counterpart in
// ContextFabricInvestigationResult, so a projection can never be looser
// than the result it came from.
const (
	ContextFabricProjectedDriversMaxCount    = 50
	ContextFabricProjectedFactsMaxCount      = 250
	ContextFabricProjectedCohortMaxCount     = 250
	ContextFabricProjectedCandidatesMaxCount = 50
	ContextFabricProjectedSubjectsMaxCount   = 250
	ContextFabricProjectedEvidenceMaxCount   = 500
	ContextFabricProjectedReceiptsMaxCount   = 50
	ContextFabricProjectedCoverageMaxCount   = 100
	ContextFabricProjectedNarrativeMaxCount  = 100
	ContextFabricProjectedPressuresMaxCount  = 50
)

// ContextFabricAnswerProjection is the bounded consumer projection of one
// ContextFabricInvestigationResult. ResultID identifies the canonical
// result it projects: a consumer that needs anything this shape dropped
// fetches the full result by that ID rather than asking for a fatter
// answer.
//
// Every identifier here -- ResultID, RequestID, ReceiptID, DriverID,
// ClaimID, EvidenceRefID -- is frozen and opaque. No consumer parses one,
// and no structure is implied or documented.
type ContextFabricAnswerProjection struct {
	SchemaVersion string                           `json:"schema_version"`
	ResultID      string                           `json:"result_id"`
	RequestID     string                           `json:"request_id"`
	GeneratedAt   time.Time                        `json:"generated_at"`
	Status        ContextFabricInvestigationStatus `json:"status"`
	Question      string                           `json:"question"`
	// Reused mirrors the canonical result's CHAOS-3782 flag: true when the
	// answer came from the immutable result store rather than a fresh
	// investigation. A bounded consumer that could not tell a reused answer
	// from a freshly computed one would lose a real diagnostic signal, and
	// would also misread result_id and generated_at, which then name the
	// REUSED result rather than this request.
	Reused bool `json:"reused"`
	// DirectJudgment and CurrentState are copied verbatim. They are the
	// answer. A projection that reworded either would be answering a
	// different question than the API did.
	DirectJudgment     string   `json:"direct_judgment"`
	CurrentState       string   `json:"current_state"`
	StrongestPressures []string `json:"strongest_pressures"`
	// CommittedSubjects mirrors SubjectResolution.Committed exactly. The
	// differential parity check compares this across surfaces, so it is
	// never truncated -- a surface that resolved a different set of
	// subjects answered a different question.
	CommittedSubjects []ContextFabricSubjectRef `json:"committed_subjects"`
	// Clarification is present only when Status is
	// clarification_required. It carries the candidates a caller chooses
	// between, each with the ReceiptID that binds the choice on the next
	// turn.
	Clarification    *ContextFabricProjectedClarification `json:"clarification,omitempty"`
	Cohort           *ContextFabricProjectedCohort        `json:"cohort,omitempty"`
	PrincipalDrivers []ContextFabricProjectedDriver       `json:"principal_drivers"`
	// KeyFacts carries only the claimed facts that retained drivers cite.
	// It is the value-level evidence a consumer needs to see that the
	// judgment rests on canonical observations, not prose.
	KeyFacts        []ContextFabricProjectedFact     `json:"key_facts"`
	CoverageSummary []ContextFabricProjectedCoverage `json:"coverage_summary"`
	// CoveragePartial and Limitations are never dropped. A bounded
	// consumer must always be able to see that an answer was incomplete.
	CoveragePartial bool     `json:"coverage_partial"`
	Limitations     []string `json:"limitations"`
	Warnings        []string `json:"warnings"`
	// EvidenceRefIDs carries only refs that retained content cites. Every
	// one expands through the existing evidence boundary; this contract
	// adds no second retrieval path.
	EvidenceRefIDs []string `json:"evidence_ref_ids"`
	// SubjectReceipts lets a caller bind the next turn to the subjects
	// this turn resolved, by echoing them as
	// ContextFabricInvestigationRequest.PriorSubjectReceipts.
	SubjectReceipts  []ContextFabricBoundSubjectReceipt `json:"subject_receipts"`
	Versions         ContextFabricVersionSet            `json:"versions"`
	ProjectionBudget ContextFabricProjectionBudget      `json:"projection_budget"`
}

// ContextFabricProjectedClarification carries the ambiguity a caller must
// resolve. It is the projected form of the candidate list plus the
// canonical clarification prompt.
type ContextFabricProjectedClarification struct {
	Prompt     string                            `json:"prompt,omitempty"`
	Candidates []ContextFabricProjectedCandidate `json:"candidates"`
}

// ContextFabricProjectedCandidate is one ambiguity option. ReceiptID is the
// handle a caller returns to commit this choice.
type ContextFabricProjectedCandidate struct {
	ReceiptID    string                       `json:"receipt_id"`
	Subject      ContextFabricSubjectRef      `json:"subject"`
	State        ContextFabricResolutionState `json:"state"`
	Confidence   float64                      `json:"confidence"`
	MatchReasons []string                     `json:"match_reasons"`
}

// ContextFabricProjectedCohort summarises a subjectless cohort answer. Total
// is the canonical member count before projection, so a caller always sees
// the true size even when Members is truncated.
type ContextFabricProjectedCohort struct {
	Kind      ContextFabricSubjectKind             `json:"kind"`
	Total     int                                  `json:"total"`
	Rationale string                               `json:"rationale"`
	Complete  bool                                 `json:"complete"`
	Members   []ContextFabricProjectedCohortMember `json:"members"`
}

// ContextFabricProjectedCohortMember keeps the canonical Rank so a consumer
// can never reorder a cohort into a different judgment.
type ContextFabricProjectedCohortMember struct {
	Subject          ContextFabricSubjectRef `json:"subject"`
	Rank             int                     `json:"rank"`
	InclusionReasons []string                `json:"inclusion_reasons"`
	EvidenceRefIDs   []string                `json:"evidence_ref_ids,omitempty"`
}

// ContextFabricProjectedDriver is one driver judgment, narrowed to what a
// bounded consumer needs to state and defend it. Standing and Category are
// copied verbatim: driver standing is the judgment, and a projection that
// promoted or demoted one would change the answer.
type ContextFabricProjectedDriver struct {
	DriverID       string                      `json:"driver_id"`
	Standing       ContextFabricDriverStanding `json:"standing"`
	Category       string                      `json:"category"`
	Title          string                      `json:"title"`
	Summary        string                      `json:"summary"`
	Qualification  string                      `json:"qualification,omitempty"`
	Confidence     float64                     `json:"confidence"`
	EvidenceRefIDs []string                    `json:"evidence_ref_ids"`
	ClaimedFactIDs []string                    `json:"claimed_fact_ids,omitempty"`
}

// ContextFabricProjectedFact is a claimed fact a retained driver cites,
// carried at value level so a consumer can show the canonical observation
// behind a judgment rather than only prose about it.
type ContextFabricProjectedFact struct {
	ClaimID string                   `json:"claim_id"`
	Kind    ContextFabricFactKind    `json:"kind"`
	Subject ContextFabricSubjectRef  `json:"subject"`
	Field   string                   `json:"field"`
	Value   ContextFabricScalarValue `json:"value"`
}

// ContextFabricProjectedCoverage reports one source's state. Reason is
// carried when present, because "why is this source missing" is exactly
// what a caller needs to judge a partial answer.
type ContextFabricProjectedCoverage struct {
	Source string                   `json:"source"`
	State  ContextFabricSourceState `json:"state"`
	Reason string                   `json:"reason,omitempty"`
}

// ContextFabricProjectionBudget declares what the projection dropped. It is
// the honesty mechanism of this contract: Truncated is true whenever any
// omitted count is non-zero, and the per-field counts say where.
//
// WithheldDriversOmitted is tracked separately from DriversOmitted. A
// withheld driver is one the engine deliberately declined to stand behind,
// and a consumer that cannot see that some were withheld would read a
// filtered answer as a complete one.
//
// FullResultOmitted records that a caller asked for the full canonical
// result alongside this projection and it did not fit the byte budget. The
// projection is still complete and honest on its own terms; only the
// requested extra copy was dropped. Failing this way -- projection plus
// result_id, never truncated JSON -- keeps every emitted document valid.
type ContextFabricProjectionBudget struct {
	Truncated              bool `json:"truncated"`
	DriversOmitted         int  `json:"drivers_omitted"`
	WithheldDriversOmitted int  `json:"withheld_drivers_omitted"`
	CohortMembersOmitted   int  `json:"cohort_members_omitted"`
	FactsOmitted           int  `json:"facts_omitted"`
	CandidatesOmitted      int  `json:"candidates_omitted"`
	EvidenceRefsOmitted    int  `json:"evidence_refs_omitted"`
	// LimitationsOmitted, WarningsOmitted, and CoverageOmitted exist
	// because the canonical result bounds these arrays at 250 while the
	// projection bounds them at 100 (see the Max*Count constants above).
	// Copying them wholesale therefore made a valid canonical result
	// produce an INVALID projection, and silently cutting them would have
	// hidden exactly the caveats a reader needs most: a shortened
	// limitations list reads as more confident than the investigation was.
	LimitationsOmitted int `json:"limitations_omitted"`
	WarningsOmitted    int `json:"warnings_omitted"`
	CoverageOmitted    int `json:"coverage_omitted"`
	// ValuesClamped counts individual values the projection SHORTENED
	// rather than dropped. Clamping was silent (codex round-5 R5-3): an
	// 8000-rune legacy judgment became 4000 runes with truncated=false and
	// no count, so a consumer could not tell it was reading a cut value.
	// Shortening a value is a form of omission and is disclosed like one.
	ValuesClamped     int  `json:"values_clamped"`
	FullResultOmitted bool `json:"full_result_omitted"`
}

// Projection field-length bounds.
//
// These exist because reads of persisted data accept the HISTORICAL maxima
// (see ValidateStored): a stored result can legitimately carry text longer
// than the projection schema admits. The projection is a VIEW, so it clamps
// such values rather than refusing to render them or emitting a
// schema-invalid document -- the canonical view still serves the untouched
// original.
const (
	ContextFabricProjectedJudgmentMaxLength            = 4000
	ContextFabricProjectedNarrativeMaxLength           = 2000
	ContextFabricProjectedDriverTitleMaxLength         = 512
	ContextFabricProjectedDriverSummaryMaxLength       = 4000
	ContextFabricProjectedDriverQualificationMaxLength = 2000
	ContextFabricProjectedCohortRationaleMaxLength     = 4000
	ContextFabricProjectedInclusionReasonsMaxCount     = 32
	ContextFabricProjectedInclusionReasonMaxLength     = 1000
	ContextFabricProjectedMatchReasonsMaxCount         = 100
	ContextFabricProjectedMatchReasonMaxLength         = 1024
	ContextFabricProjectedCoverageSourceMaxLength      = 128
	ContextFabricProjectedCoverageReasonMaxLength      = 2000
	ContextFabricProjectedClarificationPromptMaxLength = 2000
)
