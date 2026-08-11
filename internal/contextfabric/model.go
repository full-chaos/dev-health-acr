package contextfabric

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	InvestigationRequestSchemaV1 = "context_fabric_investigation_request.v1"
	InvestigationResultSchemaV1  = "context_fabric_investigation_result.v1"
	ProjectionBatchSchemaV1      = "context_fabric_projection_batch.v1"
)

type InvestigationStatus string

const (
	InvestigationComplete              InvestigationStatus = "complete"
	InvestigationPartial               InvestigationStatus = "partial"
	InvestigationDegraded              InvestigationStatus = "degraded"
	InvestigationClarificationRequired InvestigationStatus = "clarification_required"
	InvestigationNoMatch               InvestigationStatus = "no_match"
)

type InvestigationShape string

const (
	ShapeSingleSubject    InvestigationShape = "single_subject"
	ShapeExplicitCohort   InvestigationShape = "explicit_cohort"
	ShapeDiscoveredCohort InvestigationShape = "discovered_cohort"
	ShapeOpen             InvestigationShape = "open"
)

type SubjectKind string

const (
	SubjectOrganization SubjectKind = "organization"
	SubjectTeam         SubjectKind = "team"
	SubjectProject      SubjectKind = "project"
	SubjectRepository   SubjectKind = "repository"
	SubjectWorkItem     SubjectKind = "work_item"
	SubjectPullRequest  SubjectKind = "pull_request"
	SubjectDeployment   SubjectKind = "deployment"
	SubjectIncident     SubjectKind = "incident"
	SubjectDocument     SubjectKind = "document"
	SubjectDecision     SubjectKind = "decision"
	SubjectEpisode      SubjectKind = "episode"
	SubjectMetric       SubjectKind = "metric"
)

type ResolutionState string

const (
	ResolutionCommitted  ResolutionState = "committed"
	ResolutionProposed   ResolutionState = "proposed"
	ResolutionAmbiguous  ResolutionState = "ambiguous"
	ResolutionUnresolved ResolutionState = "unresolved"
)

type TemporalAxis string

const (
	TemporalCurrent      TemporalAxis = "current"
	TemporalValidTime    TemporalAxis = "valid_time"
	TemporalObservedTime TemporalAxis = "observed_time"
	TemporalRange        TemporalAxis = "range"
)

type DriverStanding string

const (
	DriverPrincipal    DriverStanding = "principal"
	DriverContributing DriverStanding = "contributing"
	DriverSymptom      DriverStanding = "symptom"
	DriverContext      DriverStanding = "context"
	DriverWithheld     DriverStanding = "withheld"
)

type DerivationMethod string

const (
	DerivationCanonicalStructured     DerivationMethod = "canonical_structured"
	DerivationDeterministicProjection DerivationMethod = "deterministic_projection"
	DerivationGraphAssociated         DerivationMethod = "graph_associated"
	DerivationModelExtracted          DerivationMethod = "model_extracted"
	DerivationRuleInferred            DerivationMethod = "rule_inferred"
)

type EpistemicStatus string

const (
	EpistemicObserved       EpistemicStatus = "observed"
	EpistemicSourceAsserted EpistemicStatus = "source_asserted"
	EpistemicInferred       EpistemicStatus = "inferred"
	EpistemicDisputed       EpistemicStatus = "disputed"
	EpistemicSuperseded     EpistemicStatus = "superseded"
	EpistemicUnknown        EpistemicStatus = "unknown"
)

type SourceState string

const (
	SourceAvailable     SourceState = "available"
	SourceStale         SourceState = "stale"
	SourceUnavailable   SourceState = "unavailable"
	SourceUnconfigured  SourceState = "unconfigured"
	SourceUnauthorized  SourceState = "unauthorized"
	SourceNoData        SourceState = "no_data"
	SourceTruncated     SourceState = "truncated"
	SourceConflicted    SourceState = "conflicted"
	SourceNotApplicable SourceState = "not_applicable"
)

type ConversationRole string

const (
	ConversationUser      ConversationRole = "user"
	ConversationAssistant ConversationRole = "assistant"
)

type FactKind string

const (
	FactIdentity                FactKind = "identity"
	FactMembership              FactKind = "membership"
	FactStatus                  FactKind = "status"
	FactActualCompletion        FactKind = "actual_completion"
	FactWork                    FactKind = "work"
	FactBlockers                FactKind = "blockers"
	FactRequiredChildren        FactKind = "required_children"
	FactPullRequests            FactKind = "pull_requests"
	FactReviews                 FactKind = "reviews"
	FactContinuousIntegration   FactKind = "continuous_integration"
	FactDeployments             FactKind = "deployments"
	FactIncidents               FactKind = "incidents"
	FactMetrics                 FactKind = "metrics"
	FactHealth                  FactKind = "health"
	FactWorkload                FactKind = "workload"
	FactInvestment              FactKind = "investment"
	FactReadiness               FactKind = "readiness"
	FactOperationalDeficiencies FactKind = "operational_deficiencies"
	FactSourceHealth            FactKind = "source_health"
	FactEvidence                FactKind = "evidence"
)

type InvestigationRequest struct {
	SchemaVersion        string                `json:"schema_version"`
	RequestID            string                `json:"request_id"`
	Question             string                `json:"question"`
	Conversation         []ConversationTurn    `json:"conversation,omitempty"`
	PriorSubjectReceipts []BoundSubjectReceipt `json:"prior_subject_receipts,omitempty"`
	RequestedScope       RequestedScope        `json:"requested_scope,omitempty"`
	TimeContext          TimeContext           `json:"time_context"`
	Options              InvestigationOptions  `json:"options"`
	Consumer             ConsumerInfo          `json:"consumer"`
}

type ConversationTurn struct {
	TurnID    string           `json:"turn_id"`
	Role      ConversationRole `json:"role"`
	Content   string           `json:"content"`
	CreatedAt time.Time        `json:"created_at"`
}

type BoundSubjectReceipt struct {
	ResultID  string `json:"result_id"`
	ReceiptID string `json:"receipt_id"`
}

type RequestedScope struct {
	RepositorySlugs []string      `json:"repository_slugs,omitempty"`
	ProjectIDs      []string      `json:"project_ids,omitempty"`
	TeamIDs         []string      `json:"team_ids,omitempty"`
	SubjectHints    []SubjectHint `json:"subject_hints,omitempty"`
}

type SubjectHint struct {
	Kind   SubjectKind `json:"kind"`
	ID     string      `json:"id,omitempty"`
	Label  string      `json:"label,omitempty"`
	Source string      `json:"source"`
}

type TimeContext struct {
	Axis  TemporalAxis `json:"axis"`
	AsOf  *time.Time   `json:"as_of,omitempty"`
	Start *time.Time   `json:"start,omitempty"`
	End   *time.Time   `json:"end,omitempty"`
}

type InvestigationOptions struct {
	MaxSubjectCandidates int  `json:"max_subject_candidates"`
	MaxCohortMembers     int  `json:"max_cohort_members"`
	MaxRelationshipPaths int  `json:"max_relationship_paths"`
	MaxDrivers           int  `json:"max_drivers"`
	MaxEvidenceRefs      int  `json:"max_evidence_refs"`
	MaxSerializedBytes   int  `json:"max_serialized_bytes"`
	AllowClarification   bool `json:"allow_clarification"`
	IncludeDebug         bool `json:"include_debug"`
}

type ConsumerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Surface string `json:"surface"`
}

type SubjectRef struct {
	Kind        SubjectKind `json:"kind"`
	CanonicalID string      `json:"canonical_id"`
	Label       string      `json:"label"`
}

type SubjectCandidate struct {
	ReceiptID      string          `json:"receipt_id"`
	Subject        SubjectRef      `json:"subject"`
	State          ResolutionState `json:"state"`
	MatchedTerms   []string        `json:"matched_terms,omitempty"`
	MatchReasons   []string        `json:"match_reasons"`
	Confidence     float64         `json:"confidence"`
	EvidenceRefIDs []string        `json:"evidence_ref_ids,omitempty"`
}

type SubjectResolution struct {
	Candidates          []SubjectCandidate `json:"candidates"`
	Committed           []SubjectRef       `json:"committed"`
	ClarificationPrompt string             `json:"clarification_prompt,omitempty"`
}

type Cohort struct {
	Kind       SubjectKind       `json:"kind"`
	Members    []CohortMember    `json:"members"`
	Exclusions []CohortExclusion `json:"exclusions,omitempty"`
	Rationale  string            `json:"rationale"`
	Complete   bool              `json:"complete"`
	Truncated  bool              `json:"truncated"`
}

type CohortMember struct {
	Subject          SubjectRef `json:"subject"`
	Rank             int        `json:"rank"`
	InclusionReasons []string   `json:"inclusion_reasons"`
	EvidenceRefIDs   []string   `json:"evidence_ref_ids,omitempty"`
}

type CohortExclusion struct {
	Subject SubjectRef `json:"subject"`
	Reason  string     `json:"reason"`
}

type RelationshipPath struct {
	PathID         string             `json:"path_id"`
	Nodes          []SubjectRef       `json:"nodes"`
	Edges          []RelationshipEdge `json:"edges"`
	WhyRelevant    string             `json:"why_relevant"`
	EvidenceRefIDs []string           `json:"evidence_ref_ids"`
	Truncated      bool               `json:"truncated"`
}

type RelationshipEdge struct {
	Type            string           `json:"type"`
	From            SubjectRef       `json:"from"`
	To              SubjectRef       `json:"to"`
	Derivation      DerivationMethod `json:"derivation"`
	EpistemicStatus EpistemicStatus  `json:"epistemic_status"`
	ObservedAt      *time.Time       `json:"observed_at,omitempty"`
	ValidFrom       *time.Time       `json:"valid_from,omitempty"`
	ValidTo         *time.Time       `json:"valid_to,omitempty"`
	EvidenceRefIDs  []string         `json:"evidence_ref_ids"`
}

type DriverJudgment struct {
	DriverID         string           `json:"driver_id"`
	Standing         DriverStanding   `json:"standing"`
	Category         string           `json:"category"`
	Title            string           `json:"title"`
	Summary          string           `json:"summary"`
	AffectedSubjects []SubjectRef     `json:"affected_subjects"`
	PathIDs          []string         `json:"path_ids,omitempty"`
	EvidenceRefIDs   []string         `json:"evidence_ref_ids"`
	Derivation       DerivationMethod `json:"derivation"`
	EpistemicStatus  EpistemicStatus  `json:"epistemic_status"`
	Confidence       float64          `json:"confidence"`
	Qualification    string           `json:"qualification,omitempty"`
	Current          bool             `json:"current"`
}

type Finding struct {
	FindingID      string       `json:"finding_id"`
	Kind           string       `json:"kind"`
	Summary        string       `json:"summary"`
	Subjects       []SubjectRef `json:"subjects,omitempty"`
	EvidenceRefIDs []string     `json:"evidence_ref_ids"`
}

type SourceObservation struct {
	Source     string      `json:"source"`
	State      SourceState `json:"state"`
	ObservedAt *time.Time  `json:"observed_at,omitempty"`
	Watermark  string      `json:"watermark,omitempty"`
	Reason     string      `json:"reason,omitempty"`
}

type Coverage struct {
	Sources         []SourceObservation `json:"sources"`
	Partial         bool                `json:"partial"`
	DegradedReasons []string            `json:"degraded_reasons,omitempty"`
}

type VersionSet struct {
	ServiceVersion          string `json:"service_version"`
	ContractVersion         string `json:"contract_version"`
	Backend                 string `json:"backend"`
	BackendVersion          string `json:"backend_version,omitempty"`
	ProjectionVersion       string `json:"projection_version"`
	QueryVersion            string `json:"query_version"`
	InterpretationVersion   string `json:"interpretation_version"`
	SynthesisVersion        string `json:"synthesis_version"`
	CanonicalServiceVersion string `json:"canonical_service_version"`
}

type InterpretedQuestion struct {
	Shape               InvestigationShape `json:"shape"`
	RequestedJudgment   string             `json:"requested_judgment"`
	SubjectTerms        []string           `json:"subject_terms,omitempty"`
	ComparisonTerms     []string           `json:"comparison_terms,omitempty"`
	TimeContext         TimeContext        `json:"time_context"`
	FactRequirements    []FactRequirement  `json:"fact_requirements"`
	ClarificationNeeded bool               `json:"clarification_needed"`
	ClarificationReason string             `json:"clarification_reason,omitempty"`
}

type FactRequirement struct {
	Kind       FactKind          `json:"kind"`
	Subjects   []SubjectRef      `json:"subjects,omitempty"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

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

type CanonicalFact struct {
	Kind           FactKind    `json:"kind"`
	Subject        SubjectRef  `json:"subject"`
	Value          any         `json:"value"`
	ObservedAt     *time.Time  `json:"observed_at,omitempty"`
	EventAt        *time.Time  `json:"event_at,omitempty"`
	EvidenceRefIDs []string    `json:"evidence_ref_ids"`
	SourceState    SourceState `json:"source_state"`
}

type CanonicalFactBundle struct {
	Facts    []CanonicalFact `json:"facts"`
	Coverage Coverage        `json:"coverage"`
	Version  string          `json:"version"`
}

type SynthesisInput struct {
	Request        InvestigationRequest `json:"request"`
	Interpretation InterpretedQuestion  `json:"interpretation"`
	Graph          GraphContext         `json:"graph"`
	Facts          CanonicalFactBundle  `json:"facts"`
}

type InvestigationResult struct {
	SchemaVersion       string              `json:"schema_version"`
	ResultID            string              `json:"result_id"`
	RequestID           string              `json:"request_id"`
	GeneratedAt         time.Time           `json:"generated_at"`
	Status              InvestigationStatus `json:"status"`
	Question            string              `json:"question"`
	Interpretation      InterpretedQuestion `json:"interpretation"`
	SubjectResolution   SubjectResolution   `json:"subject_resolution"`
	Cohort              *Cohort             `json:"cohort,omitempty"`
	DirectJudgment      string              `json:"direct_judgment"`
	CurrentState        string              `json:"current_state"`
	StrongestPressures  []string            `json:"strongest_pressures"`
	Drivers             []DriverJudgment    `json:"drivers"`
	RemainingWork       []Finding           `json:"remaining_work"`
	ReadinessGaps       []Finding           `json:"readiness_gaps"`
	Paths               []RelationshipPath  `json:"paths"`
	Conflicts           []Finding           `json:"conflicts"`
	Limitations         []string            `json:"limitations"`
	EvidenceRefIDs      []string            `json:"evidence_ref_ids"`
	Coverage            Coverage            `json:"coverage"`
	Versions            VersionSet          `json:"versions"`
	DeterministicAnswer string              `json:"deterministic_answer"`
	Warnings            []string            `json:"warnings"`
}

type ProjectionBatch struct {
	SchemaVersion       string                   `json:"schema_version"`
	BatchID             string                   `json:"batch_id"`
	OrgID               string                   `json:"org_id"`
	Source              string                   `json:"source"`
	SourceVersion       string                   `json:"source_version"`
	Cursor              string                   `json:"cursor"`
	NextCursor          string                   `json:"next_cursor"`
	GeneratedAt         time.Time                `json:"generated_at"`
	FullSnapshot        bool                     `json:"full_snapshot"`
	CompleteEnumeration bool                     `json:"complete_enumeration"`
	Entities            []EntityProjection       `json:"entities"`
	Relationships       []RelationshipProjection `json:"relationships"`
	Contents            []ContentProjection      `json:"contents"`
	Episodes            []EpisodeProjection      `json:"episodes"`
	Tombstones          []ProjectionTombstone    `json:"tombstones"`
}

type AuthorizationScope struct {
	RepositorySlugs []string `json:"repository_slugs,omitempty"`
	ProjectIDs      []string `json:"project_ids,omitempty"`
	TeamIDs         []string `json:"team_ids,omitempty"`
}

type EntityProjection struct {
	Subject        SubjectRef         `json:"subject"`
	Aliases        []string           `json:"aliases,omitempty"`
	PreviousNames  []string           `json:"previous_names,omitempty"`
	ProviderIDs    map[string]string  `json:"provider_ids,omitempty"`
	Properties     map[string]any     `json:"properties,omitempty"`
	Authorization  AuthorizationScope `json:"authorization"`
	EvidenceRefIDs []string           `json:"evidence_ref_ids"`
	ObservedAt     time.Time          `json:"observed_at"`
	ValidFrom      *time.Time         `json:"valid_from,omitempty"`
	ValidTo        *time.Time         `json:"valid_to,omitempty"`
	SourceVersion  string             `json:"source_version"`
}

type RelationshipProjection struct {
	RelationshipID  string             `json:"relationship_id"`
	Type            string             `json:"type"`
	From            SubjectRef         `json:"from"`
	To              SubjectRef         `json:"to"`
	Properties      map[string]any     `json:"properties,omitempty"`
	Derivation      DerivationMethod   `json:"derivation"`
	EpistemicStatus EpistemicStatus    `json:"epistemic_status"`
	Authorization   AuthorizationScope `json:"authorization"`
	EvidenceRefIDs  []string           `json:"evidence_ref_ids"`
	ObservedAt      time.Time          `json:"observed_at"`
	ValidFrom       *time.Time         `json:"valid_from,omitempty"`
	ValidTo         *time.Time         `json:"valid_to,omitempty"`
	SourceVersion   string             `json:"source_version"`
}

type ContentProjection struct {
	ContentID      string             `json:"content_id"`
	Subject        SubjectRef         `json:"subject"`
	Title          string             `json:"title"`
	Body           string             `json:"body"`
	ContentDigest  string             `json:"content_digest"`
	Authorization  AuthorizationScope `json:"authorization"`
	EvidenceRefIDs []string           `json:"evidence_ref_ids"`
	ObservedAt     time.Time          `json:"observed_at"`
	SourceVersion  string             `json:"source_version"`
	Untrusted      bool               `json:"untrusted"`
}

type EpisodeProjection struct {
	EpisodeID      string             `json:"episode_id"`
	Subject        SubjectRef         `json:"subject"`
	Goal           string             `json:"goal"`
	Outcome        string             `json:"outcome"`
	Summary        string             `json:"summary"`
	Authorization  AuthorizationScope `json:"authorization"`
	EvidenceRefIDs []string           `json:"evidence_ref_ids"`
	StartedAt      time.Time          `json:"started_at"`
	EndedAt        time.Time          `json:"ended_at"`
	SourceVersion  string             `json:"source_version"`
}

type ProjectionTombstone struct {
	Kind          string    `json:"kind"`
	CanonicalID   string    `json:"canonical_id"`
	Reason        string    `json:"reason"`
	EffectiveAt   time.Time `json:"effective_at"`
	SourceVersion string    `json:"source_version"`
}

type ProjectionReceipt struct {
	BatchID           string    `json:"batch_id"`
	AppliedAt         time.Time `json:"applied_at"`
	BackendWatermark  string    `json:"backend_watermark"`
	EntitiesApplied   int       `json:"entities_applied"`
	EdgesApplied      int       `json:"edges_applied"`
	ContentsApplied   int       `json:"contents_applied"`
	EpisodesApplied   int       `json:"episodes_applied"`
	TombstonesApplied int       `json:"tombstones_applied"`
}

type ProjectionWatermark struct {
	OrgID            string    `json:"org_id"`
	Source           string    `json:"source"`
	Cursor           string    `json:"cursor"`
	SourceVersion    string    `json:"source_version"`
	ProjectedAt      time.Time `json:"projected_at"`
	BackendWatermark string    `json:"backend_watermark"`
}

type ProjectionCheckpoint struct {
	OrgID            string    `json:"org_id"`
	Source           string    `json:"source"`
	Cursor           string    `json:"cursor"`
	SourceVersion    string    `json:"source_version"`
	BackendWatermark string    `json:"backend_watermark"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (r InvestigationRequest) Validate() error {
	if r.SchemaVersion != InvestigationRequestSchemaV1 {
		return fmt.Errorf("schema_version must be %q", InvestigationRequestSchemaV1)
	}
	if !boundedString(r.RequestID, 8, 256) || !boundedString(strings.TrimSpace(r.Question), 1, 8000) {
		return fmt.Errorf("request_id or question violates bounds")
	}
	if len(r.Conversation) > 50 || len(r.PriorSubjectReceipts) > 20 {
		return fmt.Errorf("conversation or prior subject receipts exceed bounds")
	}
	for _, turn := range r.Conversation {
		if err := turn.Validate(); err != nil {
			return fmt.Errorf("conversation: %w", err)
		}
	}
	for _, receipt := range r.PriorSubjectReceipts {
		if !boundedString(receipt.ResultID, 8, 256) || !boundedString(receipt.ReceiptID, 8, 256) {
			return fmt.Errorf("prior subject receipt violates bounds")
		}
	}
	if err := r.RequestedScope.Validate(); err != nil {
		return fmt.Errorf("requested_scope: %w", err)
	}
	if err := r.TimeContext.Validate(); err != nil {
		return fmt.Errorf("time_context: %w", err)
	}
	if err := r.Options.Validate(); err != nil {
		return fmt.Errorf("options: %w", err)
	}
	if !boundedString(r.Consumer.Name, 1, 200) || !boundedString(r.Consumer.Version, 1, 200) || !boundedString(r.Consumer.Surface, 1, 200) {
		return fmt.Errorf("consumer metadata violates bounds")
	}
	return nil
}

func (t ConversationTurn) Validate() error {
	if !boundedString(t.TurnID, 1, 256) || !boundedString(strings.TrimSpace(t.Content), 1, 12000) || t.CreatedAt.IsZero() {
		return fmt.Errorf("turn identity, content, or timestamp violates bounds")
	}
	switch t.Role {
	case ConversationUser, ConversationAssistant:
		return nil
	default:
		return fmt.Errorf("role is invalid")
	}
}

func (s RequestedScope) Validate() error {
	if len(s.RepositorySlugs) > 200 || len(s.ProjectIDs) > 200 || len(s.TeamIDs) > 200 || len(s.SubjectHints) > 50 {
		return fmt.Errorf("scope exceeds bounds")
	}
	if !uniqueNonEmpty(s.RepositorySlugs, 512) || !uniqueNonEmpty(s.ProjectIDs, 256) || !uniqueNonEmpty(s.TeamIDs, 256) {
		return fmt.Errorf("scope identifiers violate bounds")
	}
	for _, hint := range s.SubjectHints {
		if hint.Kind == "" || !boundedString(hint.ID, 0, 256) || !boundedString(hint.Label, 0, 512) || !boundedString(hint.Source, 1, 64) || (hint.ID == "" && strings.TrimSpace(hint.Label) == "") {
			return fmt.Errorf("subject hint violates bounds")
		}
	}
	return nil
}

func (t TimeContext) Validate() error {
	switch t.Axis {
	case TemporalCurrent:
		if t.AsOf != nil || t.Start != nil || t.End != nil {
			return fmt.Errorf("current time context cannot include explicit timestamps")
		}
	case TemporalValidTime, TemporalObservedTime:
		if t.AsOf == nil || t.Start != nil || t.End != nil {
			return fmt.Errorf("point-in-time context requires only as_of")
		}
	case TemporalRange:
		if t.Start == nil || t.End == nil || t.AsOf != nil || t.End.Before(*t.Start) {
			return fmt.Errorf("range context requires an ordered start and end")
		}
	default:
		return fmt.Errorf("time axis is invalid")
	}
	return nil
}

func (o InvestigationOptions) Validate() error {
	if o.MaxSubjectCandidates < 1 || o.MaxSubjectCandidates > 50 ||
		o.MaxCohortMembers < 1 || o.MaxCohortMembers > 250 ||
		o.MaxRelationshipPaths < 1 || o.MaxRelationshipPaths > 250 ||
		o.MaxDrivers < 1 || o.MaxDrivers > 50 ||
		o.MaxEvidenceRefs < 1 || o.MaxEvidenceRefs > 500 ||
		o.MaxSerializedBytes < 8192 || o.MaxSerializedBytes > 1<<20 {
		return fmt.Errorf("investigation options violate bounds")
	}
	return nil
}

func (r InvestigationResult) Validate() error {
	if r.SchemaVersion != InvestigationResultSchemaV1 || !boundedString(r.ResultID, 8, 256) || !boundedString(r.RequestID, 8, 256) || r.GeneratedAt.IsZero() || !boundedString(strings.TrimSpace(r.Question), 1, 8000) {
		return fmt.Errorf("result identity violates bounds")
	}
	switch r.Status {
	case InvestigationComplete, InvestigationPartial, InvestigationDegraded, InvestigationClarificationRequired, InvestigationNoMatch:
	default:
		return fmt.Errorf("result status is invalid")
	}
	if (r.Status == InvestigationComplete || r.Status == InvestigationPartial) && strings.TrimSpace(r.DirectJudgment) == "" {
		return fmt.Errorf("answer-capable results require a direct judgment")
	}
	if strings.TrimSpace(r.DeterministicAnswer) == "" {
		return fmt.Errorf("deterministic answer is required")
	}
	for _, driver := range r.Drivers {
		if driver.Standing != DriverWithheld && len(driver.EvidenceRefIDs) == 0 && len(driver.PathIDs) == 0 {
			return fmt.Errorf("non-withheld driver %q lacks evidence closure", driver.DriverID)
		}
	}
	return nil
}

func (b ProjectionBatch) Validate() error {
	if b.SchemaVersion != ProjectionBatchSchemaV1 || !boundedString(b.BatchID, 8, 256) || !boundedString(b.OrgID, 1, 256) || !boundedString(b.Source, 1, 128) || !boundedString(b.SourceVersion, 1, 256) || b.GeneratedAt.IsZero() {
		return fmt.Errorf("projection batch identity violates bounds")
	}
	if b.FullSnapshot && !b.CompleteEnumeration {
		return fmt.Errorf("full snapshot requires a complete enumeration proof")
	}
	if len(b.Entities)+len(b.Relationships)+len(b.Contents)+len(b.Episodes)+len(b.Tombstones) == 0 {
		return fmt.Errorf("projection batch is empty")
	}
	return nil
}

func boundedString(value string, minimum, maximum int) bool {
	length := utf8.RuneCountInString(value)
	return length >= minimum && length <= maximum
}

func uniqueNonEmpty(values []string, maximum int) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != value || !boundedString(value, 1, maximum) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}
