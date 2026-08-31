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
	// Temporal is CHAOS-3781's temporal label, copied verbatim, and like
	// Limitations it is never dropped. Nil means the answer is about
	// current state.
	//
	// The projection carries no Interpretation, so before this field a
	// bounded consumer had no way at all to tell a historical answer from
	// a current one: the same drivers, the same current_state, the same
	// judgment, with nothing marking which time they spoke for. That is
	// the H6 defect AC-3781-2 closed on the canonical result, reappearing
	// one layer out on the only surface an agent actually reads.
	//
	// It is the canonical ContextFabricTemporalLabel, not a projected
	// variant of one. Every other sub-shape here is genuinely narrower
	// than its canonical counterpart; this one is not, and a narrowed copy
	// would only create a second place for the axis-shape rules to drift.
	Temporal *ContextFabricTemporalLabel `json:"temporal,omitempty"`
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
	// EffectiveEvidenceWindow and WindowClarification (CHAOS-3900 W2,
	// design brief §4) mirror the canonical result's own fields of the
	// same name verbatim -- the MCP surface is the ONE bounded consumer
	// this whole window-disclosure mechanism exists to reach, so the
	// projection carries them unclamped rather than a narrower, second
	// copy. Both join the Limitations/StructureNeeds "never dropped"
	// discipline (§2.1's own never-truncated pin, extended to windows):
	// the projection budget may drop or bound OTHER content, never these.
	EffectiveEvidenceWindow *ContextFabricEffectiveEvidenceWindow `json:"effective_evidence_window,omitempty"`
	WindowClarification     *ContextFabricWindowClarification     `json:"window_clarification,omitempty"`
	// StructureNeeds and ConfirmedStructure (CHAOS-3972 P3, design brief
	// §2.1/§2.3) mirror the canonical result's own fields verbatim -- the
	// MCP investigate_question response is the P3 disclosure/accept
	// surface this whole block exists to reach. Never dropped, matching
	// the design brief's own never-truncated pin.
	StructureNeeds     *ContextFabricStructureNeeds           `json:"structure_needs,omitempty"`
	ConfirmedStructure []ContextFabricConfirmedStructureEntry `json:"confirmed_structure,omitempty"`
	// PriorSubjectReceiptDispositions (CHAOS-3478/CHAOS-3813) mirrors the
	// canonical result's SubjectResolution.PriorSubjectReceiptDispositions
	// verbatim, joining ConfirmedStructure's own never-dropped discipline
	// immediately above -- the MCP investigate_question response is the
	// bounded consumer surface this whole disclosure mechanism exists to
	// reach (codex round-1 finding: a projection that omitted it left the
	// DEFAULT answer surface silently reproducing the exact CHAOS-3813
	// drop this field exists to close, even though the canonical result
	// carried the disclosure correctly).
	PriorSubjectReceiptDispositions []ContextFabricPriorSubjectReceiptEntry `json:"prior_subject_receipt_dispositions,omitempty"`
	// RenderShapes (CHAOS-4415 slice 1) mirrors the canonical result's
	// own field verbatim -- the SAME ContextFabricRenderShape type, not a
	// narrowed copy, for the same reason Temporal is the canonical label
	// here: a shape is already minimal, and a second variant would only
	// create a second place for the selection vocabulary to drift.
	//
	// A shape whose point sources the projection no longer carries (its
	// cohort member or claimed fact was cut by the budget) is DROPPED and
	// counted in ProjectionBudget.RenderShapesOmitted. Shipping it would
	// be a chart the reader cannot check against anything in the document
	// it arrived in, which is precisely what this contract exists to
	// prevent.
	RenderShapes []ContextFabricRenderShape `json:"render_shapes,omitempty"`
	// Completeness (CHAOS-4413) mirrors the canonical result's field
	// verbatim -- ClaimedFactsCount/RowsCount are the UN-CLAMPED totals from
	// the canonical result, deliberately not recomputed from this
	// projection's own budget-clamped KeyFacts: a consumer counting KeyFacts
	// would learn how much of the answer THIS bounded read kept, never how
	// much the investigation actually produced. See
	// ContextFabricAnswerCompleteness's own doc comment.
	Completeness ContextFabricAnswerCompleteness `json:"completeness"`
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
	Kind      ContextFabricSubjectKind `json:"kind"`
	Total     int                      `json:"total"`
	Rationale string                   `json:"rationale"`
	Complete  bool                     `json:"complete"`
	// Truncated (CHAOS-4636) is the cohort's own truncation flag, which the
	// projected cohort did not carry at all before this slice -- so per-group
	// truncation had no projected representation and a reader could not tell
	// a complete grouped answer from a partial one.
	Truncated bool                                 `json:"truncated,omitempty"`
	Members   []ContextFabricProjectedCohortMember `json:"members"`
	// Groups (CHAOS-4636) is the group axis, projected. Absent on every flat
	// cohort. Members are named by canonical id into Members above, exactly
	// as on the canonical cohort, so the projection carries one member list
	// rather than two that could disagree.
	//
	// The projection clamp is GROUP-AWARE because of this field: it
	// allocates its member budget across groups before truncating within
	// them. The pre-CHAOS-4636 clamp kept the LEADING MaxCohortMembers of
	// the flat list, which for a grouped answer could return every project
	// of team A and none of team B -- silently, with the caller unable to
	// tell that a whole group had vanished.
	Groups []ContextFabricProjectedCohortGroup `json:"groups,omitempty"`
	// RankingTable (CHAOS-4398 PR3, design doc §4a) is the Rows-panel
	// rendering of the cohort's ranking: one row per surviving member, in
	// AttentionRank order, columns for rank/score/data_completeness/window
	// and the member's top drivers -- built from data ContextFabricCohortMember/
	// ContextFabricCohortMemberDriver already carry, never re-derived or
	// re-judged here. Reuses ContextFabricClaimedFactRow (the same row type
	// ContextFabricProjectedFact.Rows already uses) rather than a bespoke
	// struct, so a Rows-rendering consumer has ONE row shape to handle.
	// Absent (nil) when RankCohort never ran for this cohort (RankingComputed
	// false on every member) -- the same "not computed" distinction
	// RankingComputed itself makes on the canonical member.
	RankingTable []ContextFabricClaimedFactRow `json:"ranking_table,omitempty"`
}

// ContextFabricProjectedCohortGroup is one group of a projected grouped
// cohort. It mirrors ContextFabricCohortGroup: the group subject, its members
// by canonical id, its OWN completeness, and the size it had before any
// narrowing.
type ContextFabricProjectedCohortGroup struct {
	Subject            ContextFabricSubjectRef `json:"subject"`
	MemberCanonicalIDs []string                `json:"member_canonical_ids"`
	Complete           bool                    `json:"complete"`
	Truncated          bool                    `json:"truncated"`
	Total              int                     `json:"total"`
}

// ContextFabricProjectedCohortMember keeps the canonical Rank so a consumer
// can never reorder a cohort into a different judgment.
type ContextFabricProjectedCohortMember struct {
	Subject          ContextFabricSubjectRef `json:"subject"`
	Rank             int                     `json:"rank"`
	InclusionReasons []string                `json:"inclusion_reasons"`
	EvidenceRefIDs   []string                `json:"evidence_ref_ids,omitempty"`
	// RankingComputed/AttentionRank/Score/RankingBasis/DataCompleteness/
	// Outcome/MissingSignals (CHAOS-4398 PR3, design doc §4a/§8) mirror the
	// SAME fields on the canonical ContextFabricCohortMember verbatim --
	// copied, never recomputed or renarrated here. See that type's own doc
	// comments for what each means; the presence/pairing invariants are
	// identical (RankingComputed false implies every other field here is
	// absent/zero too). Additive v1: a pre-PR1 projection simply omits all
	// seven.
	RankingComputed  bool                                `json:"ranking_computed,omitempty"`
	AttentionRank    int                                 `json:"attention_rank,omitempty"`
	Score            *float64                            `json:"score,omitempty"`
	RankingBasis     []string                            `json:"ranking_basis,omitempty"`
	DataCompleteness ContextFabricCohortDataCompleteness `json:"data_completeness,omitempty"`
	Outcome          ContextFabricCohortMemberOutcome    `json:"outcome,omitempty"`
	MissingSignals   []string                            `json:"missing_signals,omitempty"`
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
	// AffectedSubjects (CHAOS-4398 PR3, design doc §4a) names which cohort
	// member(s) this driver judgment is about -- copied verbatim from the
	// canonical ContextFabricDriverJudgment.AffectedSubjects. Without this,
	// a cohort-answer driver (unlike a single-subject answer's, where the
	// subject is implicit) cannot be tied back to which team it explains.
	// Additive v1: omitempty, absent for every pre-PR3 driver.
	AffectedSubjects []ContextFabricSubjectRef `json:"affected_subjects,omitempty"`
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
	// Rows mirrors ContextFabricClaimedFact.Rows (CHAOS-4347): the
	// renderable table a claim carried, passed through unchanged so a
	// consumer of the answer surface -- not just the canonical result --
	// can render it. Reuses ContextFabricClaimedFactRow directly rather
	// than a second, identically-shaped type: the two documents
	// (context_fabric_common.v1 / context_fabric_answer_projection.v1)
	// each keep their own JSON Schema $defs copy (ProjectedFact is a
	// self-contained document by the same convention every other
	// projected shape here already follows), but nothing requires the Go
	// side to duplicate the type just because the wire schema duplicates
	// the definition.
	Rows []ContextFabricClaimedFactRow `json:"rows,omitempty"`
	// Table mirrors ContextFabricClaimedFact.Table (CHAOS-4637 /
	// CHAOS-4627), passed through unchanged. The answer surface is where
	// Ask Dev reads a fact from, so a declaration that stopped at the
	// canonical result would leave the consumer half of CHAOS-4627
	// exactly as unable to say what a row table is as before.
	Table *ContextFabricClaimedFactTable `json:"table,omitempty"`
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
	// CohortGroupsOmitted (CHAOS-4636) counts groups the clamp could not
	// keep a single member of. It is tracked separately from
	// CohortMembersOmitted for the same reason WithheldDriversOmitted is
	// tracked separately from DriversOmitted: a reader who cannot see that a
	// whole GROUP is missing reads a per-group answer as covering every
	// group. Decision D2 (member-first) makes this rare by construction --
	// every group survives with at least one member for as long as the
	// budget admits any -- which is exactly why a non-zero value here is
	// worth alerting on rather than expecting.
	CohortGroupsOmitted int `json:"cohort_groups_omitted"`
	FactsOmitted        int `json:"facts_omitted"`
	CandidatesOmitted   int `json:"candidates_omitted"`
	EvidenceRefsOmitted int `json:"evidence_refs_omitted"`
	// LimitationsOmitted, WarningsOmitted, and CoverageOmitted exist
	// because the canonical result bounds these arrays at 250 while the
	// projection bounds them at 100 (see the Max*Count constants above).
	// Copying them wholesale therefore made a valid canonical result
	// produce an INVALID projection, and silently cutting them would have
	// hidden exactly the caveats a reader needs most: a shortened
	// limitations list reads as more confident than the investigation was.
	// LimitationsOmitted also carries the engine's own displacement
	// (ContextFabricInvestigationResult.LimitationsDisplaced), not only
	// what this projection cut: the field means "limitations this
	// investigation produced that you are not reading", and where the
	// loss happened does not change what the reader is missing.
	LimitationsOmitted int `json:"limitations_omitted"`
	WarningsOmitted    int `json:"warnings_omitted"`
	CoverageOmitted    int `json:"coverage_omitted"`
	// ValuesClamped counts individual values the projection SHORTENED
	// rather than dropped. Clamping was silent (codex round-5 R5-3): an
	// 8000-rune legacy judgment became 4000 runes with truncated=false and
	// no count, so a consumer could not tell it was reading a cut value.
	// Shortening a value is a form of omission and is disclosed like one.
	// ReasonsOmitted counts inclusion reasons and match reasons dropped
	// from members and candidates that were themselves RETAINED. Folding
	// these into the member and candidate counters made the projection
	// claim members were dropped when they were not -- a wrong statement
	// on the wire, not merely an imprecise one (codex round-7 F4).
	ReasonsOmitted int `json:"reasons_omitted"`
	ValuesClamped  int `json:"values_clamped"`
	// RenderShapesOmitted (CHAOS-4415) counts render shapes the
	// projection dropped because a number they plot is no longer present
	// in the projected document -- the citing shape is dropped, never one
	// of its points, the same "drop the citing item, never its
	// references" rule projectDrivers already follows for evidence.
	RenderShapesOmitted int  `json:"render_shapes_omitted"`
	FullResultOmitted   bool `json:"full_result_omitted"`
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
