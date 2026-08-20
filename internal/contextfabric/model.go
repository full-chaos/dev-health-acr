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
	ProjectionBatchSchemaV1      = contractsv1.ContextFabricProjectionBatchSchema
)

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
type RelationshipPath = contractsv1.ContextFabricRelationshipPath
type RelationshipEdge = contractsv1.ContextFabricRelationshipEdge
type RelationshipType = contractsv1.ContextFabricRelationshipType
type DriverJudgment = contractsv1.ContextFabricDriverJudgment
type Finding = contractsv1.ContextFabricFinding
type ClaimedFact = contractsv1.ContextFabricClaimedFact
type SourceObservation = contractsv1.ContextFabricSourceObservation
type Coverage = contractsv1.ContextFabricCoverage
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
}

type FactValue struct {
	String  *string  `json:"string,omitempty"`
	Integer *int64   `json:"integer,omitempty"`
	Number  *float64 `json:"number,omitempty"`
	Boolean *bool    `json:"boolean,omitempty"`
	Null    bool     `json:"null,omitempty"`
}

func StringFactValue(value string) FactValue  { return FactValue{String: &value} }
func IntegerFactValue(value int64) FactValue  { return FactValue{Integer: &value} }
func NumberFactValue(value float64) FactValue { return FactValue{Number: &value} }
func BooleanFactValue(value bool) FactValue   { return FactValue{Boolean: &value} }
func NullFactValue() FactValue                { return FactValue{Null: true} }

func (v FactValue) Validate() error {
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
	if set != 1 {
		return fmt.Errorf("fact value must contain exactly one typed value")
	}
	return nil
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
}

type SynthesisInput struct {
	Request        InvestigationRequest `json:"request"`
	Interpretation InterpretedQuestion  `json:"interpretation"`
	Graph          GraphContext         `json:"graph"`
	Facts          CanonicalFactBundle  `json:"facts"`
}
