package contextfabric

import (
	"fmt"
	"math"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	InvestigationRequestSchemaV1 = contractsv1.ContextFabricInvestigationRequestSchema
	InvestigationResultSchemaV1  = contractsv1.ContextFabricInvestigationResultSchema
	// InvestigationResultSchemaV2 (CHAOS-4042, sol-max ruling) is the
	// anchor membership-verify semantic major -- see
	// contractsv1.ContextFabricInvestigationResultSchemaV2's own doc
	// comment.
	InvestigationResultSchemaV2 = contractsv1.ContextFabricInvestigationResultSchemaV2
	ProjectionBatchSchemaV1     = contractsv1.ContextFabricProjectionBatchSchema
)

// ValidateResult (CHAOS-4042) dispatches a result about to be WRITTEN to
// the v1 or v2 write-path validator by its own SchemaVersion -- every
// storage adapter's Save must use this instead of calling result.Validate()
// directly, or a genuinely valid v2 result would be rejected before it
// could ever be persisted. See ValidateStoredResult for the read-path
// counterpart; both fail closed on an unrecognized SchemaVersion.
func ValidateResult(result InvestigationResult) error {
	switch result.SchemaVersion {
	case InvestigationResultSchemaV2:
		return result.ValidateV2()
	case InvestigationResultSchemaV1:
		return result.Validate()
	default:
		return fmt.Errorf("investigation result schema_version %q is not a recognized major", result.SchemaVersion)
	}
}

// ValidateStoredResult (CHAOS-4042) dispatches a read-back result to the
// v1 or v2 stored-validator by its OWN persisted SchemaVersion -- every
// storage adapter's read path (Get/FindReusable) must use this instead of
// calling result.ValidateStored() directly, or a genuinely valid v2 row
// would be rejected by the v1-only validator the instant this ticket's
// offer generation ever mints one. Any other SchemaVersion value fails
// closed (neither validator ever runs), matching this package's existing
// "unrecognized value fails loudly" discipline -- never a silent pass.
func ValidateStoredResult(result InvestigationResult) error {
	switch result.SchemaVersion {
	case InvestigationResultSchemaV2:
		return result.ValidateStoredV2()
	case InvestigationResultSchemaV1:
		return result.ValidateStored()
	default:
		return fmt.Errorf("investigation result schema_version %q is not a recognized major", result.SchemaVersion)
	}
}

type InvestigationStatus = contractsv1.ContextFabricInvestigationStatus

const (
	InvestigationComplete              = contractsv1.ContextFabricInvestigationComplete
	InvestigationPartial               = contractsv1.ContextFabricInvestigationPartial
	InvestigationDegraded              = contractsv1.ContextFabricInvestigationDegraded
	InvestigationClarificationRequired = contractsv1.ContextFabricInvestigationClarificationRequired
	InvestigationNoMatch               = contractsv1.ContextFabricInvestigationNoMatch
)

type InvestigationShape = contractsv1.ContextFabricInvestigationShape

const (
	ShapeSingleSubject    = contractsv1.ContextFabricShapeSingleSubject
	ShapeExplicitCohort   = contractsv1.ContextFabricShapeExplicitCohort
	ShapeDiscoveredCohort = contractsv1.ContextFabricShapeDiscoveredCohort
	ShapeOpen             = contractsv1.ContextFabricShapeOpen
)

type SubjectKind = contractsv1.ContextFabricSubjectKind

const (
	SubjectOrganization = contractsv1.ContextFabricSubjectOrganization
	SubjectTeam         = contractsv1.ContextFabricSubjectTeam
	SubjectProject      = contractsv1.ContextFabricSubjectProject
	SubjectRepository   = contractsv1.ContextFabricSubjectRepository
	SubjectWorkItem     = contractsv1.ContextFabricSubjectWorkItem
	SubjectPullRequest  = contractsv1.ContextFabricSubjectPullRequest
	SubjectDeployment   = contractsv1.ContextFabricSubjectDeployment
	SubjectIncident     = contractsv1.ContextFabricSubjectIncident
	SubjectDocument     = contractsv1.ContextFabricSubjectDocument
	SubjectDecision     = contractsv1.ContextFabricSubjectDecision
	SubjectEpisode      = contractsv1.ContextFabricSubjectEpisode
	SubjectMetric       = contractsv1.ContextFabricSubjectMetric
	// SubjectWorkItemRef (CHAOS-3898 §1.5) is the non-authoritative stub
	// kind for an unresolved work_item dependency/hierarchy target -- see
	// ContextFabricSubjectWorkItemRef's own doc comment.
	SubjectWorkItemRef = contractsv1.ContextFabricSubjectWorkItemRef
)

type ResolutionState = contractsv1.ContextFabricResolutionState

const (
	ResolutionCommitted  = contractsv1.ContextFabricResolutionCommitted
	ResolutionProposed   = contractsv1.ContextFabricResolutionProposed
	ResolutionAmbiguous  = contractsv1.ContextFabricResolutionAmbiguous
	ResolutionUnresolved = contractsv1.ContextFabricResolutionUnresolved
)

// MatchMechanism names HOW a subject candidate was proposed (CHAOS-3778 /
// AC-3778-6). The enum is closed -- see the contract type's doc comment for
// why graphrank's corroboration band depends on that.
type MatchMechanism = contractsv1.ContextFabricSubjectMatchMechanism

const (
	MatchExact           = contractsv1.ContextFabricMatchExact
	MatchAlias           = contractsv1.ContextFabricMatchAlias
	MatchProviderKey     = contractsv1.ContextFabricMatchProviderKey
	MatchLexical         = contractsv1.ContextFabricMatchLexical
	MatchVector          = contractsv1.ContextFabricMatchVector
	MatchTraversalParent = contractsv1.ContextFabricMatchTraversalParent
)

type TemporalAxis = contractsv1.ContextFabricTemporalAxis

const (
	TemporalCurrent      = contractsv1.ContextFabricTemporalCurrent
	TemporalValidTime    = contractsv1.ContextFabricTemporalValidTime
	TemporalObservedTime = contractsv1.ContextFabricTemporalObservedTime
	TemporalRange        = contractsv1.ContextFabricTemporalRange
)

const (
	GrainInstant = contractsv1.ContextFabricGrainInstant
	GrainDay     = contractsv1.ContextFabricGrainDay
	GrainNone    = contractsv1.ContextFabricGrainNone
)

type DriverStanding = contractsv1.ContextFabricDriverStanding

const (
	DriverPrincipal    = contractsv1.ContextFabricDriverPrincipal
	DriverContributing = contractsv1.ContextFabricDriverContributing
	DriverSymptom      = contractsv1.ContextFabricDriverSymptom
	DriverContext      = contractsv1.ContextFabricDriverContext
	DriverWithheld     = contractsv1.ContextFabricDriverWithheld
)

type DerivationMethod = contractsv1.ContextFabricDerivationMethod

const (
	DerivationCanonicalStructured     = contractsv1.ContextFabricDerivationCanonicalStructured
	DerivationDeterministicProjection = contractsv1.ContextFabricDerivationDeterministicProjection
	DerivationGraphAssociated         = contractsv1.ContextFabricDerivationGraphAssociated
	DerivationModelExtracted          = contractsv1.ContextFabricDerivationModelExtracted
	DerivationRuleInferred            = contractsv1.ContextFabricDerivationRuleInferred
)

type EpistemicStatus = contractsv1.ContextFabricEpistemicStatus

const (
	EpistemicObserved       = contractsv1.ContextFabricEpistemicObserved
	EpistemicSourceAsserted = contractsv1.ContextFabricEpistemicSourceAsserted
	EpistemicInferred       = contractsv1.ContextFabricEpistemicInferred
	EpistemicDisputed       = contractsv1.ContextFabricEpistemicDisputed
	EpistemicSuperseded     = contractsv1.ContextFabricEpistemicSuperseded
	EpistemicUnknown        = contractsv1.ContextFabricEpistemicUnknown
)

type SourceState = contractsv1.ContextFabricSourceState

const (
	SourceAvailable     = contractsv1.ContextFabricSourceAvailable
	SourceStale         = contractsv1.ContextFabricSourceStale
	SourceUnavailable   = contractsv1.ContextFabricSourceUnavailable
	SourceUnconfigured  = contractsv1.ContextFabricSourceUnconfigured
	SourceUnauthorized  = contractsv1.ContextFabricSourceUnauthorized
	SourceNoData        = contractsv1.ContextFabricSourceNoData
	SourceTruncated     = contractsv1.ContextFabricSourceTruncated
	SourceConflicted    = contractsv1.ContextFabricSourceConflicted
	SourceNotApplicable = contractsv1.ContextFabricSourceNotApplicable
	SourcePruned        = contractsv1.ContextFabricSourcePruned
)

type ConversationRole = contractsv1.ContextFabricConversationRole

const (
	ConversationUser      = contractsv1.ContextFabricConversationUser
	ConversationAssistant = contractsv1.ContextFabricConversationAssistant
)

type FactKind = contractsv1.ContextFabricFactKind

const (
	FactIdentity                = contractsv1.ContextFabricFactIdentity
	FactMembership              = contractsv1.ContextFabricFactMembership
	FactStatus                  = contractsv1.ContextFabricFactStatus
	FactActualCompletion        = contractsv1.ContextFabricFactActualCompletion
	FactWork                    = contractsv1.ContextFabricFactWork
	FactBlockers                = contractsv1.ContextFabricFactBlockers
	FactRequiredChildren        = contractsv1.ContextFabricFactRequiredChildren
	FactPullRequests            = contractsv1.ContextFabricFactPullRequests
	FactReviews                 = contractsv1.ContextFabricFactReviews
	FactContinuousIntegration   = contractsv1.ContextFabricFactContinuousIntegration
	FactDeployments             = contractsv1.ContextFabricFactDeployments
	FactIncidents               = contractsv1.ContextFabricFactIncidents
	FactMetrics                 = contractsv1.ContextFabricFactMetrics
	FactHealth                  = contractsv1.ContextFabricFactHealth
	FactWorkload                = contractsv1.ContextFabricFactWorkload
	FactInvestment              = contractsv1.ContextFabricFactInvestment
	FactReadiness               = contractsv1.ContextFabricFactReadiness
	FactOperationalDeficiencies = contractsv1.ContextFabricFactOperationalDeficiencies
	FactSourceHealth            = contractsv1.ContextFabricFactSourceHealth
	FactEvidence                = contractsv1.ContextFabricFactEvidence
	FactFlow                    = contractsv1.ContextFabricFactFlow
	FactLandscape               = contractsv1.ContextFabricFactLandscape
)

type InvestigationRequest = contractsv1.ContextFabricInvestigationRequest
type ConversationTurn = contractsv1.ContextFabricConversationTurn
type BoundSubjectReceipt = contractsv1.ContextFabricBoundSubjectReceipt
type RequestedScope = contractsv1.ContextFabricRequestedScope
type SubjectHint = contractsv1.ContextFabricSubjectHint
type TimeContext = contractsv1.ContextFabricTimeContext
type TemporalLabel = contractsv1.ContextFabricTemporalLabel
type TemporalGrain = contractsv1.ContextFabricTemporalGrain

// CHAOS-3900 W1: the evidence-window wire shapes, same alias convention as
// every sibling type in this file.
type RequestedEvidenceWindow = contractsv1.ContextFabricRequestedEvidenceWindow
type EffectiveEvidenceWindow = contractsv1.ContextFabricEffectiveEvidenceWindow
type WindowOption = contractsv1.ContextFabricWindowOption
type WindowClarification = contractsv1.ContextFabricWindowClarification

// CHAOS-3900 P1: the StructureNeeds wire shapes, same alias convention.
type StructureNeeds = contractsv1.ContextFabricStructureNeeds
type KindOption = contractsv1.ContextFabricKindOption
type AnchorOption = contractsv1.ContextFabricAnchorOption
type HandleOption = contractsv1.ContextFabricHandleOption
type CandidateOption = contractsv1.ContextFabricCandidateOption
type AcceptedGrammar = contractsv1.ContextFabricAcceptedGrammar
type ConfirmedStructureEntry = contractsv1.ContextFabricConfirmedStructureEntry
type StructureOfferSnapshotEntry = contractsv1.ContextFabricStructureOfferSnapshotEntry
type StructureNeedKind = contractsv1.ContextFabricStructureNeedKind
type StructureOfferSource = contractsv1.ContextFabricStructureOfferSource
type StructureSource = contractsv1.ContextFabricStructureSource
type StructureProvenance = contractsv1.ContextFabricStructureProvenance
type StructureDisposition = contractsv1.ContextFabricStructureDisposition

type InvestigationOptions = contractsv1.ContextFabricInvestigationOptions
type ConsumerInfo = contractsv1.ContextFabricConsumerInfo
type SubjectRef = contractsv1.ContextFabricSubjectRef
type SubjectCandidate = contractsv1.ContextFabricSubjectCandidate
type SubjectResolution = contractsv1.ContextFabricSubjectResolution
type Cohort = contractsv1.ContextFabricCohort
type CohortMember = contractsv1.ContextFabricCohortMember
type CohortExclusion = contractsv1.ContextFabricCohortExclusion

// CHAOS-4398: RankCohort's own additive contract types, same alias
// convention as every sibling type in this file.
type CohortDataCompleteness = contractsv1.ContextFabricCohortDataCompleteness

const (
	CohortDataComplete = contractsv1.ContextFabricCohortDataComplete
	CohortDataPartial  = contractsv1.ContextFabricCohortDataPartial
	CohortDataDegraded = contractsv1.ContextFabricCohortDataDegraded
)

// CHAOS-4398 PR2: CohortMember.Drivers' own additive contract type.
type CohortMemberDriver = contractsv1.ContextFabricCohortMemberDriver
type CohortMemberDriverWindow = contractsv1.ContextFabricCohortMemberDriverWindow

const (
	DriverWindowCurrent        = contractsv1.ContextFabricCohortMemberDriverWindowCurrent
	DriverWindowCurrentVsPrior = contractsv1.ContextFabricCohortMemberDriverWindowCurrentVsPrior
)

// CHAOS-4398 PR3: CohortMember.Outcome's own additive contract type
// (design doc §8).
type CohortMemberOutcome = contractsv1.ContextFabricCohortMemberOutcome

const (
	CohortOutcomeQualified            = contractsv1.ContextFabricCohortOutcomeQualified
	CohortOutcomeProvisional          = contractsv1.ContextFabricCohortOutcomeProvisional
	CohortOutcomeInsufficientEvidence = contractsv1.ContextFabricCohortOutcomeInsufficientEvidence
	CohortOutcomeNotApplicable        = contractsv1.ContextFabricCohortOutcomeNotApplicable
)

type RelationshipPath = contractsv1.ContextFabricRelationshipPath
type RelationshipEdge = contractsv1.ContextFabricRelationshipEdge
type RelationshipType = contractsv1.ContextFabricRelationshipType
type DriverJudgment = contractsv1.ContextFabricDriverJudgment
type Finding = contractsv1.ContextFabricFinding
type ClaimedFact = contractsv1.ContextFabricClaimedFact
type ClaimedFactRow = contractsv1.ContextFabricClaimedFactRow
type SourceObservation = contractsv1.ContextFabricSourceObservation
type Coverage = contractsv1.ContextFabricCoverage

// CoverageDetail (CHAOS-4690) is one structured coverage reason -- the
// wire type, aliased so producers (fact_registry, falkorgraph) mint the
// exact object the contract validates.
type CoverageDetail = contractsv1.ContextFabricCoverageDetail
type AnswerCompleteness = contractsv1.ContextFabricAnswerCompleteness
type TerminalReason = contractsv1.ContextFabricTerminalReason
type VersionSet = contractsv1.ContextFabricVersionSet
type InterpretedQuestion = contractsv1.ContextFabricInterpretedQuestion
type FactRequirement = contractsv1.ContextFabricFactRequirement
type InvestigationResult = contractsv1.ContextFabricInvestigationResult
type ProjectionBatch = contractsv1.ContextFabricProjectionBatch
type AuthorizationScope = contractsv1.ContextFabricAuthorizationScope
type ScalarValue = contractsv1.ContextFabricScalarValue
type EntityProjection = contractsv1.ContextFabricEntityProjection
type RelationshipProjection = contractsv1.ContextFabricRelationshipProjection
type ContentProjection = contractsv1.ContextFabricContentProjection
type EpisodeProjection = contractsv1.ContextFabricEpisodeProjection
type ProjectionTombstone = contractsv1.ContextFabricProjectionTombstone
type ProjectionReceipt = contractsv1.ContextFabricProjectionReceipt
type ProjectionWatermark = contractsv1.ContextFabricProjectionWatermark
type ProjectionCheckpoint = contractsv1.ContextFabricProjectionCheckpoint

type GraphContext struct {
	Resolution       SubjectResolution  `json:"resolution"`
	Cohort           *Cohort            `json:"cohort,omitempty"`
	Paths            []RelationshipPath `json:"paths"`
	DriverCandidates []DriverJudgment   `json:"driver_candidates"`
	EvidenceRefIDs   []string           `json:"evidence_ref_ids"`
	FactRequirements []FactRequirement  `json:"fact_requirements"`
	Coverage         Coverage           `json:"coverage"`
}

type CanonicalFactRequest struct {
	Question     InterpretedQuestion `json:"question"`
	Subjects     []SubjectRef        `json:"subjects"`
	Cohort       *Cohort             `json:"cohort,omitempty"`
	Requirements []FactRequirement   `json:"requirements"`
	// Scope is the FactReadScopeResolver's verdict for this request
	// (CHAOS-4099): which derived subjects each requirement may additionally
	// be READ for, and which requirements could not be reached at all.
	//
	// ENGINE-OWNED AND OPTIONAL. A nil Scope means "no expansion was
	// resolved", which is exactly what every pre-existing caller means and
	// what every test that builds a request by hand means -- so the planner
	// and the registry behave byte-identically to before when it is absent.
	// It is deliberately NOT part of Subjects: Subjects are the
	// investigation's ROOT subjects (the committed resolution or the
	// cohort), and merging derived read targets into them would make a
	// fact-read permission indistinguishable from a resolution commitment,
	// which is the one thing ruling invariants 1 and 2 forbid.
	//
	// It carries `json:"-"`: this struct's tags exist for debug/trace
	// rendering, and scope is engine bookkeeping about how a read was
	// planned, never part of what a fact request MEANS to a provider.
	Scope *FactReadScope `json:"-"`
}

// FactValueRow is one row of a table-shaped FactValue (CHAOS-4347): a flat
// map of column name to a LEAF FactValue. A row field must never itself
// carry Rows -- FactValue.Validate enforces that, so a renderable fact's
// nesting is bounded by construction, not by convention.
type FactValueRow struct {
	Fields map[string]FactValue `json:"fields"`
}

// maxFactValueRows and maxFactValueRowFields bound a renderable table the
// same way CanonicalFact.Fields already bounds a fact's own field count
// (64): a producer emitting an unbounded rollup is a producer bug, and the
// bound catches it at Validate rather than at a downstream renderer.
const (
	maxFactValueRows      = 64
	maxFactValueRowFields = 32
)

// MaxFactValueRows is maxFactValueRows, exported (CHAOS-4364) so a
// FactProvider building a Rows table (e.g. a project's per-team rollup) can
// cap and truncate BEFORE constructing the CanonicalFact, rather than
// discovering the bound only when Validate rejects the whole fact. A
// producer that silently drops the excess without disclosing it is exactly
// the "silent truncation reads as covered everything" defect this
// package's own conventions forbid -- see devhealthfacts' capped-rows
// helper for the disclosure pattern.
const MaxFactValueRows = maxFactValueRows

type FactValue struct {
	String  *string  `json:"string,omitempty"`
	Integer *int64   `json:"integer,omitempty"`
	Number  *float64 `json:"number,omitempty"`
	Boolean *bool    `json:"boolean,omitempty"`
	Null    bool     `json:"null,omitempty"`
	// Rows carries a small, renderable table (CHAOS-4347) for a fact whose
	// evidence is genuinely a set of rows -- e.g. a project's per-team
	// metrics rollup, where summing counts across teams is sound but
	// averaging their rates is not (a rate is not additive across
	// populations of different sizes). Additive: every pre-existing
	// FactValue caller sets exactly one of the scalar variants and leaves
	// Rows nil, so this changes nothing for them.
	Rows []FactValueRow `json:"rows,omitempty"`
	// Table declares WHAT Rows is (CHAOS-4633, design doc §5.1) -- the
	// missing piece a consumer used to have to infer: is this a dated
	// series, a per-entity breakdown, or an ordered ranking; what
	// (composite) tuple of row columns identifies a row; which columns are
	// measurements. nil for every pre-CHAOS-4633 caller and for any
	// FactValue that is not tabular -- purely additive.
	//
	// P1 (CHAOS-4633, this ticket) is DUAL-WRITE ONLY: TableFactValue is
	// the sole constructor, and it builds Table.Rows and the sibling Rows
	// field from the SAME slice value, so the two can never diverge --
	// "keep Rows identical" is a property of construction, not a
	// convention a caller has to remember. No reader in this repository
	// consults Table yet (model_runtime.go, answerprojection/project.go and
	// render_shapes.go all still read Rows only); that migration is P2, a
	// later ticket.
	Table *FactTable `json:"table,omitempty"`
}

func StringFactValue(value string) FactValue  { return FactValue{String: &value} }
func IntegerFactValue(value int64) FactValue  { return FactValue{Integer: &value} }
func NumberFactValue(value float64) FactValue { return FactValue{Number: &value} }
func BooleanFactValue(value bool) FactValue   { return FactValue{Boolean: &value} }
func NullFactValue() FactValue                { return FactValue{Null: true} }

// RowsFactValue builds a table-shaped FactValue (CHAOS-4347) -- a
// renderable series/table for facts whose evidence is a set of rows rather
// than one number. See FactValue.Rows for why this exists instead of an
// averaged scalar.
//
// Deprecated: a producer that also knows its table's shape/key/measures
// should call TableFactValue instead, which sets Rows AND the CHAOS-4633
// declaration together. RowsFactValue survives for any caller that
// genuinely has no declaration to make (a leaf ClaimedFact.Rows, or a test
// fixture) and stays the correct choice there.
func RowsFactValue(rows []FactValueRow) FactValue { return FactValue{Rows: rows} }

// TableFactValue builds a declared table-shaped FactValue (CHAOS-4633, P1
// dual-write): table.Rows becomes BOTH FactValue.Rows and FactValue.Table's
// own Rows, off the same slice header, so the two fields the design's P1
// row requires stay "populated identically" by construction -- there is no
// second place a caller could pass a different row set for one than the
// other.
func TableFactValue(table FactTable) FactValue {
	return FactValue{Rows: table.Rows, Table: &table}
}

// FactTableShape is the closed vocabulary a declared FactTable's Shape may
// take (CHAOS-4633, design doc §5.1). A fourth shape is a new major
// decision, not a value silently accepted here.
type FactTableShape string

const (
	// FactTableTimeSeries: one entity, indexed by an instant. Key is
	// exactly one column, and that column parses as an instant on every
	// row -- CHAOS-4616's fix stated as a property of the declaration.
	FactTableTimeSeries FactTableShape = "time_series"
	// FactTableBreakdown: many entities, one observation each.
	FactTableBreakdown FactTableShape = "breakdown"
	// FactTableRanking: many entities, ordered by a measure named in
	// OrderBy.
	FactTableRanking FactTableShape = "ranking"
)

func validFactTableShape(shape FactTableShape) bool {
	switch shape {
	case FactTableTimeSeries, FactTableBreakdown, FactTableRanking:
		return true
	default:
		return false
	}
}

// maxFactTableKeyColumns and maxFactTableMeasures bound a declaration the
// same defensive way maxFactValueRows/maxFactValueRowFields already bound a
// table's row count and per-row field count -- a producer bug that tries to
// declare an unbounded key/measure list fails Validate rather than reaching
// a downstream renderer.
const (
	maxFactTableKeyColumns = 8
	maxFactTableMeasures   = 32
)

// FactTable declares what a tabular FactValue.Rows IS (CHAOS-4633, design
// doc §5.1): its Shape, the COMPOSITE Key that identifies a row (never a
// single-column axis -- flow.go's scope_breakdown legitimately needs
// [provider, work_scope_id], because two different providers can share one
// work_scope_id string), which columns MEASURE something, and -- for a
// ranking table only -- which Measure the rows are ordered by.
//
// Key names ROW COLUMNS ONLY. Row identity is relative to the fact's own
// Subject (CanonicalFact.Subject), which must never be duplicated into Key:
// flow.go's team scope_breakdown declares Key: [provider, work_scope_id],
// never team_id, because toFactValueRow never emits a team_id column for
// the SQL to partition scope_breakdown could satisfy.
type FactTable struct {
	Shape    FactTableShape `json:"shape"`
	Key      []string       `json:"key"`
	Measures []string       `json:"measures"`
	// OrderBy names a member of Measures. Ranking shape only; every other
	// shape must leave it empty.
	OrderBy string `json:"order_by,omitempty"`
	// Grain is the temporal precision this ONE table was computed at --
	// the same closed TemporalGrain vocabulary FactProviderResult.Grain
	// already reports at the whole-result level, now recorded per table
	// too, since a producer can emit more than one table shape at
	// different grains.
	Grain TemporalGrain  `json:"grain,omitempty"`
	Rows  []FactValueRow `json:"rows"`
}

// factTableInstantLayouts are the layouts a time_series Key column is
// accepted to parse under -- every date/instant string this package's
// producers actually emit: a bare ClickHouse Date ("2026-08-15", every
// *_daily table's `day`/`as_of_day` column, rendered via toString()) or a
// full RFC3339 instant. Never a caller-configurable list -- widening it is
// a code change, not data.
var factTableInstantLayouts = []string{time.RFC3339, time.RFC3339Nano, "2006-01-02"}

func parsesAsFactTableInstant(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	for _, layout := range factTableInstantLayouts {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

// Validate makes the wrong states unrepresentable (CHAOS-4633, design doc
// §5.1's own list): every row column is declared into exactly one of Key or
// Measures; the Key tuple is distinct across rows; a time_series table's
// Key is exactly one column that parses as an instant on every row; a
// ranking table's OrderBy names a Measure and the rows are actually in that
// order. A producer that cannot satisfy its own declaration fails here, in
// its own test, rather than at a renderer three stages later.
func (t FactTable) Validate() error {
	if !validFactTableShape(t.Shape) {
		return fmt.Errorf("fact table shape is invalid")
	}
	if len(t.Key) == 0 || len(t.Key) > maxFactTableKeyColumns {
		return fmt.Errorf("fact table key must declare between 1 and %d columns", maxFactTableKeyColumns)
	}
	if len(t.Measures) > maxFactTableMeasures {
		return fmt.Errorf("fact table measures exceed bounds")
	}
	columns := make(map[string]bool, len(t.Key)+len(t.Measures))
	for _, name := range t.Key {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("fact table key column name must not be blank")
		}
		if columns[name] {
			return fmt.Errorf("fact table column %q is declared more than once", name)
		}
		columns[name] = true
	}
	for _, name := range t.Measures {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("fact table measure column name must not be blank")
		}
		if columns[name] {
			return fmt.Errorf("fact table column %q is declared in both key and measures", name)
		}
		columns[name] = true
	}
	if t.Shape == FactTableRanking {
		if t.OrderBy == "" || !containsString(t.Measures, t.OrderBy) {
			return fmt.Errorf("fact table ranking order_by must name a declared measure")
		}
	} else if t.OrderBy != "" {
		return fmt.Errorf("fact table order_by is only valid for the ranking shape")
	}
	if t.Shape == FactTableTimeSeries && len(t.Key) != 1 {
		return fmt.Errorf("fact table time_series key must be exactly one column; a second identity column makes this a breakdown, not a time_series")
	}
	if len(t.Rows) == 0 {
		return fmt.Errorf("fact table must declare at least one row")
	}
	if len(t.Rows) > maxFactValueRows {
		return fmt.Errorf("fact table rows exceed bounds")
	}
	seenKeys := make(map[string]bool, len(t.Rows))
	var previousOrderValue float64
	for rowIndex, row := range t.Rows {
		keyParts := make([]string, 0, len(t.Key))
		for _, name := range t.Key {
			cell, ok := row.Fields[name]
			if !ok {
				return fmt.Errorf("fact table row %d is missing declared key column %q", rowIndex, name)
			}
			keyParts = append(keyParts, factTableCellIdentity(cell))
		}
		for name := range row.Fields {
			if !columns[name] {
				// The Fable F3 rule, verbatim, so the fix is obvious at the
				// producer rather than rediscovered downstream: a column
				// constant across the WHOLE table (a unit, a single-provider
				// series' "provider") is context about the table, not a
				// row's identity or a measurement of it, and belongs in a
				// sibling scalar field on the fact -- never in Key,
				// Measures, or the row itself.
				return fmt.Errorf("fact table row %d has column %q not declared in key or measures; a column constant across the whole table belongs in a sibling scalar field on the fact, not in the table's rows", rowIndex, name)
			}
		}
		if t.Shape == FactTableTimeSeries {
			instantCell, ok := row.Fields[t.Key[0]]
			if !ok || instantCell.String == nil || !parsesAsFactTableInstant(*instantCell.String) {
				return fmt.Errorf("fact table row %d time_series key %q does not parse as an instant", rowIndex, t.Key[0])
			}
		}
		keyIdentity := strings.Join(keyParts, "\x1f")
		if seenKeys[keyIdentity] {
			return fmt.Errorf("fact table key is not distinct across rows")
		}
		seenKeys[keyIdentity] = true
		if t.Shape == FactTableRanking {
			cell, ok := row.Fields[t.OrderBy]
			var orderValue float64
			switch {
			case ok && cell.Integer != nil:
				orderValue = float64(*cell.Integer)
			case ok && cell.Number != nil:
				orderValue = *cell.Number
			default:
				return fmt.Errorf("fact table row %d ranking order_by %q must be a numeric measure", rowIndex, t.OrderBy)
			}
			// Ranking order is fixed as DESCENDING (highest value first) --
			// the sense every ranking consumer in this codebase already
			// uses (RankCohort's AttentionRank is highest-attention-first),
			// so a producer cannot declare "ranking" and mean either
			// direction depending on what its own data happens to show.
			if rowIndex > 0 && orderValue > previousOrderValue {
				return fmt.Errorf("fact table rows are not in descending order_by order")
			}
			previousOrderValue = orderValue
		}
	}
	return nil
}

// factTableCellIdentity renders a FactValue cell into a comparable string
// for Key-distinctness checking. Cells participating in a Key are always
// scalar leaves (Validate already rejects nested Rows), so this only ever
// needs the scalar variants; an unset/null cell renders as a fixed sentinel
// distinct from any real value.
func factTableCellIdentity(cell FactValue) string {
	switch {
	case cell.String != nil:
		return "s:" + *cell.String
	case cell.Integer != nil:
		return fmt.Sprintf("i:%d", *cell.Integer)
	case cell.Number != nil:
		return fmt.Sprintf("n:%v", *cell.Number)
	case cell.Boolean != nil:
		return fmt.Sprintf("b:%t", *cell.Boolean)
	default:
		return "null"
	}
}

// containsString is declared in fact_registry.go; reused here.

func (v FactValue) Validate() error {
	return v.validate(true)
}

// validate is Validate's recursive worker. allowRows is false when
// validating a row's own leaf field, which is what makes Rows-within-Rows a
// validation error instead of a convention nobody checks.
func (v FactValue) validate(allowRows bool) error {
	set := 0
	if v.String != nil {
		set++
		if len([]rune(*v.String)) > 16_000 {
			return fmt.Errorf("fact string exceeds bounds")
		}
	}
	if v.Integer != nil {
		set++
	}
	if v.Number != nil {
		set++
		if math.IsNaN(*v.Number) || math.IsInf(*v.Number, 0) {
			return fmt.Errorf("fact number must be finite")
		}
	}
	if v.Boolean != nil {
		set++
	}
	if v.Null {
		set++
	}
	if len(v.Rows) > 0 {
		if !allowRows {
			return fmt.Errorf("fact value rows must not nest")
		}
		set++
		if len(v.Rows) > maxFactValueRows {
			return fmt.Errorf("fact value rows exceed bounds")
		}
		for _, row := range v.Rows {
			if len(row.Fields) == 0 || len(row.Fields) > maxFactValueRowFields {
				return fmt.Errorf("fact value row field count is invalid")
			}
			for key, cell := range row.Fields {
				if strings.TrimSpace(key) != key || len(key) == 0 || len(key) > 128 {
					return fmt.Errorf("fact value row field key is invalid")
				}
				if err := cell.validate(false); err != nil {
					return fmt.Errorf("fact value row field %q: %w", key, err)
				}
			}
		}
	}
	if set != 1 {
		return fmt.Errorf("fact value must contain exactly one typed value")
	}
	if v.Table != nil {
		if !allowRows {
			return fmt.Errorf("fact value table must not nest")
		}
		if len(v.Rows) == 0 {
			return fmt.Errorf("fact value declares a table with no rows")
		}
		// P1's "keep Rows identical" requirement, checked structurally
		// rather than trusted: TableFactValue already builds both from one
		// slice, so this only ever fires for a caller that constructed
		// FactValue by hand with a divergent pair.
		if len(v.Rows) != len(v.Table.Rows) {
			return fmt.Errorf("fact value rows and table rows must be identical (row count differs)")
		}
		for i := range v.Rows {
			if !factValueRowsEqual(v.Rows[i], v.Table.Rows[i]) {
				return fmt.Errorf("fact value rows and table rows must be identical (row %d differs)", i)
			}
		}
		if err := v.Table.Validate(); err != nil {
			return fmt.Errorf("fact value table: %w", err)
		}
	}
	return nil
}

// factValueRowsEqual reports whether two rows carry the same field set and
// values, by comparing each cell's factTableCellIdentity -- the same
// scalar-leaf comparison Key-distinctness uses, valid here because a row's
// fields are always scalar leaves (Validate already rejects nested Rows).
func factValueRowsEqual(a, b FactValueRow) bool {
	if len(a.Fields) != len(b.Fields) {
		return false
	}
	for name, cell := range a.Fields {
		other, ok := b.Fields[name]
		if !ok || factTableCellIdentity(cell) != factTableCellIdentity(other) {
			return false
		}
	}
	return true
}

type CanonicalFact struct {
	Kind           FactKind             `json:"kind"`
	Subject        SubjectRef           `json:"subject"`
	Fields         map[string]FactValue `json:"fields"`
	ObservedAt     *time.Time           `json:"observed_at,omitempty"`
	EventAt        *time.Time           `json:"event_at,omitempty"`
	EvidenceRefIDs []string             `json:"evidence_ref_ids"`
	SourceState    SourceState          `json:"source_state"`
	Source         string               `json:"source"`
	SourceVersion  string               `json:"source_version"`
}

func (f CanonicalFact) Validate(requireEvidence bool) error {
	if f.Kind == "" || f.Subject.Validate() != nil || f.Fields == nil || len(f.Fields) > 64 || f.SourceState == "" || strings.TrimSpace(f.Source) == "" || strings.TrimSpace(f.SourceVersion) == "" {
		return fmt.Errorf("canonical fact identity or source metadata is invalid")
	}
	if f.ObservedAt != nil && f.ObservedAt.IsZero() || f.EventAt != nil && f.EventAt.IsZero() {
		return fmt.Errorf("canonical fact timestamp is invalid")
	}
	for key, value := range f.Fields {
		if strings.TrimSpace(key) != key || len(key) == 0 || len(key) > 128 {
			return fmt.Errorf("canonical fact field key is invalid")
		}
		if err := value.Validate(); err != nil {
			return fmt.Errorf("canonical fact field %q: %w", key, err)
		}
	}
	if requireEvidence && len(f.EvidenceRefIDs) == 0 {
		return fmt.Errorf("canonical fact requires evidence")
	}
	return nil
}

type CanonicalFactBundle struct {
	Facts      []CanonicalFact     `json:"facts"`
	Coverage   Coverage            `json:"coverage"`
	Version    string              `json:"version"`
	Versions   map[FactKind]string `json:"versions"`
	Watermarks map[FactKind]string `json:"watermarks"`
	// TemporalGrain is the COARSEST grain among the providers that
	// actually contributed facts (CHAOS-3781 round-1 F1) -- an answer is
	// only as precise as its least precise source. Empty when no provider
	// answered on a temporal axis. See FactProviderResult.Grain.
	TemporalGrain TemporalGrain `json:"temporal_grain,omitempty"`
	// Scope is the FactReadScopeResolver's verdict for the read that
	// produced this bundle (CHAOS-4099), or nil when no resolver ran.
	//
	// It rides on the BUNDLE rather than being recomputed by the engine
	// because the registry is the only layer that holds the capability index
	// the resolver needs, and a second, independently-derived scope on the
	// engine side is the drift class investigationScopeSubjectSet's own doc
	// comment records this package already paid for once. The engine reads
	// it for exactly two things it alone can do: emit the expansion
	// telemetry, and compose the answer-facing disclosure.
	//
	// `json:"-"`, like CanonicalFactRequest.Scope and for the same reason:
	// this is engine bookkeeping about how a read was planned, never part of
	// the evidence a bundle carries.
	Scope *FactReadScope `json:"-"`
}

type SynthesisInput struct {
	Request        InvestigationRequest `json:"request"`
	Interpretation InterpretedQuestion  `json:"interpretation"`
	Graph          GraphContext         `json:"graph"`
	Facts          CanonicalFactBundle  `json:"facts"`
}
