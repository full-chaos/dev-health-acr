package v1

import "time"

const (
	ContextFabricInvestigationRequestSchema = "context_fabric_investigation_request.v1"
	ContextFabricInvestigationResultSchema  = "context_fabric_investigation_result.v1"
	ContextFabricProjectionBatchSchema      = "context_fabric_projection_batch.v1"
)

type ContextFabricInvestigationStatus string

const (
	ContextFabricInvestigationComplete              ContextFabricInvestigationStatus = "complete"
	ContextFabricInvestigationPartial               ContextFabricInvestigationStatus = "partial"
	ContextFabricInvestigationDegraded              ContextFabricInvestigationStatus = "degraded"
	ContextFabricInvestigationClarificationRequired ContextFabricInvestigationStatus = "clarification_required"
	ContextFabricInvestigationNoMatch               ContextFabricInvestigationStatus = "no_match"
)

type ContextFabricInvestigationShape string

const (
	ContextFabricShapeSingleSubject    ContextFabricInvestigationShape = "single_subject"
	ContextFabricShapeExplicitCohort   ContextFabricInvestigationShape = "explicit_cohort"
	ContextFabricShapeDiscoveredCohort ContextFabricInvestigationShape = "discovered_cohort"
	ContextFabricShapeOpen             ContextFabricInvestigationShape = "open"
)

type ContextFabricSubjectKind string

const (
	ContextFabricSubjectOrganization ContextFabricSubjectKind = "organization"
	ContextFabricSubjectTeam         ContextFabricSubjectKind = "team"
	ContextFabricSubjectProject      ContextFabricSubjectKind = "project"
	ContextFabricSubjectRepository   ContextFabricSubjectKind = "repository"
	ContextFabricSubjectWorkItem     ContextFabricSubjectKind = "work_item"
	ContextFabricSubjectPullRequest  ContextFabricSubjectKind = "pull_request"
	ContextFabricSubjectDeployment   ContextFabricSubjectKind = "deployment"
	ContextFabricSubjectIncident     ContextFabricSubjectKind = "incident"
	ContextFabricSubjectDocument     ContextFabricSubjectKind = "document"
	ContextFabricSubjectDecision     ContextFabricSubjectKind = "decision"
	ContextFabricSubjectEpisode      ContextFabricSubjectKind = "episode"
	ContextFabricSubjectMetric       ContextFabricSubjectKind = "metric"
)

type ContextFabricResolutionState string

const (
	ContextFabricResolutionCommitted  ContextFabricResolutionState = "committed"
	ContextFabricResolutionProposed   ContextFabricResolutionState = "proposed"
	ContextFabricResolutionAmbiguous  ContextFabricResolutionState = "ambiguous"
	ContextFabricResolutionUnresolved ContextFabricResolutionState = "unresolved"
)

type ContextFabricTemporalAxis string

const (
	ContextFabricTemporalCurrent      ContextFabricTemporalAxis = "current"
	ContextFabricTemporalValidTime    ContextFabricTemporalAxis = "valid_time"
	ContextFabricTemporalObservedTime ContextFabricTemporalAxis = "observed_time"
	ContextFabricTemporalRange        ContextFabricTemporalAxis = "range"
)

type ContextFabricDriverStanding string

const (
	ContextFabricDriverPrincipal    ContextFabricDriverStanding = "principal"
	ContextFabricDriverContributing ContextFabricDriverStanding = "contributing"
	ContextFabricDriverSymptom      ContextFabricDriverStanding = "symptom"
	ContextFabricDriverContext      ContextFabricDriverStanding = "context"
	ContextFabricDriverWithheld     ContextFabricDriverStanding = "withheld"
)

type ContextFabricDerivationMethod string

const (
	ContextFabricDerivationCanonicalStructured     ContextFabricDerivationMethod = "canonical_structured"
	ContextFabricDerivationDeterministicProjection ContextFabricDerivationMethod = "deterministic_projection"
	ContextFabricDerivationGraphAssociated         ContextFabricDerivationMethod = "graph_associated"
	ContextFabricDerivationModelExtracted          ContextFabricDerivationMethod = "model_extracted"
	ContextFabricDerivationRuleInferred            ContextFabricDerivationMethod = "rule_inferred"
)

type ContextFabricEpistemicStatus string

const (
	ContextFabricEpistemicObserved       ContextFabricEpistemicStatus = "observed"
	ContextFabricEpistemicSourceAsserted ContextFabricEpistemicStatus = "source_asserted"
	ContextFabricEpistemicInferred       ContextFabricEpistemicStatus = "inferred"
	ContextFabricEpistemicDisputed       ContextFabricEpistemicStatus = "disputed"
	ContextFabricEpistemicSuperseded     ContextFabricEpistemicStatus = "superseded"
	ContextFabricEpistemicUnknown        ContextFabricEpistemicStatus = "unknown"
)

type ContextFabricSourceState string

const (
	ContextFabricSourceAvailable     ContextFabricSourceState = "available"
	ContextFabricSourceStale         ContextFabricSourceState = "stale"
	ContextFabricSourceUnavailable   ContextFabricSourceState = "unavailable"
	ContextFabricSourceUnconfigured  ContextFabricSourceState = "unconfigured"
	ContextFabricSourceUnauthorized  ContextFabricSourceState = "unauthorized"
	ContextFabricSourceNoData        ContextFabricSourceState = "no_data"
	ContextFabricSourceTruncated     ContextFabricSourceState = "truncated"
	ContextFabricSourceConflicted    ContextFabricSourceState = "conflicted"
	ContextFabricSourceNotApplicable ContextFabricSourceState = "not_applicable"
)

type ContextFabricConversationRole string

const (
	ContextFabricConversationUser      ContextFabricConversationRole = "user"
	ContextFabricConversationAssistant ContextFabricConversationRole = "assistant"
)

type ContextFabricFactKind string

const (
	ContextFabricFactIdentity                ContextFabricFactKind = "identity"
	ContextFabricFactMembership              ContextFabricFactKind = "membership"
	ContextFabricFactStatus                  ContextFabricFactKind = "status"
	ContextFabricFactActualCompletion        ContextFabricFactKind = "actual_completion"
	ContextFabricFactWork                    ContextFabricFactKind = "work"
	ContextFabricFactBlockers                ContextFabricFactKind = "blockers"
	ContextFabricFactRequiredChildren        ContextFabricFactKind = "required_children"
	ContextFabricFactPullRequests            ContextFabricFactKind = "pull_requests"
	ContextFabricFactReviews                 ContextFabricFactKind = "reviews"
	ContextFabricFactContinuousIntegration   ContextFabricFactKind = "continuous_integration"
	ContextFabricFactDeployments             ContextFabricFactKind = "deployments"
	ContextFabricFactIncidents               ContextFabricFactKind = "incidents"
	ContextFabricFactMetrics                 ContextFabricFactKind = "metrics"
	ContextFabricFactHealth                  ContextFabricFactKind = "health"
	ContextFabricFactWorkload                ContextFabricFactKind = "workload"
	ContextFabricFactInvestment              ContextFabricFactKind = "investment"
	ContextFabricFactReadiness               ContextFabricFactKind = "readiness"
	ContextFabricFactOperationalDeficiencies ContextFabricFactKind = "operational_deficiencies"
	ContextFabricFactSourceHealth            ContextFabricFactKind = "source_health"
	ContextFabricFactEvidence                ContextFabricFactKind = "evidence"
)

type ContextFabricInvestigationRequest struct {
	SchemaVersion        string                             `json:"schema_version"`
	RequestID            string                             `json:"request_id"`
	Question             string                             `json:"question"`
	Conversation         []ContextFabricConversationTurn    `json:"conversation,omitempty"`
	PriorSubjectReceipts []ContextFabricBoundSubjectReceipt `json:"prior_subject_receipts,omitempty"`
	RequestedScope       ContextFabricRequestedScope        `json:"requested_scope,omitempty"`
	TimeContext          ContextFabricTimeContext           `json:"time_context"`
	Options              ContextFabricInvestigationOptions  `json:"options"`
	Consumer             ContextFabricConsumerInfo          `json:"consumer"`
}

type ContextFabricConversationTurn struct {
	TurnID    string                        `json:"turn_id"`
	Role      ContextFabricConversationRole `json:"role"`
	Content   string                        `json:"content"`
	CreatedAt time.Time                     `json:"created_at"`
}

type ContextFabricBoundSubjectReceipt struct {
	ResultID  string `json:"result_id"`
	ReceiptID string `json:"receipt_id"`
}

type ContextFabricRequestedScope struct {
	RepositorySlugs []string                   `json:"repository_slugs,omitempty"`
	ProjectIDs      []string                   `json:"project_ids,omitempty"`
	TeamIDs         []string                   `json:"team_ids,omitempty"`
	SubjectHints    []ContextFabricSubjectHint `json:"subject_hints,omitempty"`
}

type ContextFabricSubjectHint struct {
	Kind   ContextFabricSubjectKind `json:"kind"`
	ID     string                   `json:"id,omitempty"`
	Label  string                   `json:"label,omitempty"`
	Source string                   `json:"source"`
}

type ContextFabricTimeContext struct {
	Axis  ContextFabricTemporalAxis `json:"axis"`
	AsOf  *time.Time                `json:"as_of,omitempty"`
	Start *time.Time                `json:"start,omitempty"`
	End   *time.Time                `json:"end,omitempty"`
}

type ContextFabricInvestigationOptions struct {
	MaxSubjectCandidates int  `json:"max_subject_candidates"`
	MaxCohortMembers     int  `json:"max_cohort_members"`
	MaxRelationshipPaths int  `json:"max_relationship_paths"`
	MaxDrivers           int  `json:"max_drivers"`
	MaxEvidenceRefs      int  `json:"max_evidence_refs"`
	MaxSerializedBytes   int  `json:"max_serialized_bytes"`
	AllowClarification   bool `json:"allow_clarification"`
	IncludeDebug         bool `json:"include_debug"`
}

type ContextFabricConsumerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Surface string `json:"surface"`
}

type ContextFabricSubjectRef struct {
	Kind        ContextFabricSubjectKind `json:"kind"`
	CanonicalID string                   `json:"canonical_id"`
	Label       string                   `json:"label"`
}

type ContextFabricSubjectCandidate struct {
	ReceiptID      string                       `json:"receipt_id"`
	Subject        ContextFabricSubjectRef      `json:"subject"`
	State          ContextFabricResolutionState `json:"state"`
	MatchedTerms   []string                     `json:"matched_terms,omitempty"`
	MatchReasons   []string                     `json:"match_reasons"`
	Confidence     float64                      `json:"confidence"`
	EvidenceRefIDs []string                     `json:"evidence_ref_ids,omitempty"`
}

type ContextFabricSubjectResolution struct {
	Candidates          []ContextFabricSubjectCandidate `json:"candidates"`
	Committed           []ContextFabricSubjectRef       `json:"committed"`
	ClarificationPrompt string                          `json:"clarification_prompt,omitempty"`
}

type ContextFabricCohort struct {
	Kind       ContextFabricSubjectKind       `json:"kind"`
	Members    []ContextFabricCohortMember    `json:"members"`
	Exclusions []ContextFabricCohortExclusion `json:"exclusions,omitempty"`
	Rationale  string                         `json:"rationale"`
	Complete   bool                           `json:"complete"`
	Truncated  bool                           `json:"truncated"`
}

type ContextFabricCohortMember struct {
	Subject          ContextFabricSubjectRef `json:"subject"`
	Rank             int                     `json:"rank"`
	InclusionReasons []string                `json:"inclusion_reasons"`
	EvidenceRefIDs   []string                `json:"evidence_ref_ids,omitempty"`
}

type ContextFabricCohortExclusion struct {
	Subject ContextFabricSubjectRef `json:"subject"`
	Reason  string                  `json:"reason"`
}

type ContextFabricRelationshipPath struct {
	PathID         string                          `json:"path_id"`
	Nodes          []ContextFabricSubjectRef       `json:"nodes"`
	Edges          []ContextFabricRelationshipEdge `json:"edges"`
	WhyRelevant    string                          `json:"why_relevant"`
	EvidenceRefIDs []string                        `json:"evidence_ref_ids"`
	Truncated      bool                            `json:"truncated"`
}

type ContextFabricRelationshipEdge struct {
	Type            string                        `json:"type"`
	From            ContextFabricSubjectRef       `json:"from"`
	To              ContextFabricSubjectRef       `json:"to"`
	Derivation      ContextFabricDerivationMethod `json:"derivation"`
	EpistemicStatus ContextFabricEpistemicStatus  `json:"epistemic_status"`
	ObservedAt      *time.Time                    `json:"observed_at,omitempty"`
	ValidFrom       *time.Time                    `json:"valid_from,omitempty"`
	ValidTo         *time.Time                    `json:"valid_to,omitempty"`
	EvidenceRefIDs  []string                      `json:"evidence_ref_ids"`
}

type ContextFabricDriverJudgment struct {
	DriverID         string                        `json:"driver_id"`
	Standing         ContextFabricDriverStanding   `json:"standing"`
	Category         string                        `json:"category"`
	Title            string                        `json:"title"`
	Summary          string                        `json:"summary"`
	AffectedSubjects []ContextFabricSubjectRef     `json:"affected_subjects"`
	PathIDs          []string                      `json:"path_ids,omitempty"`
	EvidenceRefIDs   []string                      `json:"evidence_ref_ids"`
	Derivation       ContextFabricDerivationMethod `json:"derivation"`
	EpistemicStatus  ContextFabricEpistemicStatus  `json:"epistemic_status"`
	Confidence       float64                       `json:"confidence"`
	Qualification    string                        `json:"qualification,omitempty"`
	Current          bool                          `json:"current"`
}

type ContextFabricFinding struct {
	FindingID      string                    `json:"finding_id"`
	Kind           string                    `json:"kind"`
	Summary        string                    `json:"summary"`
	Subjects       []ContextFabricSubjectRef `json:"subjects,omitempty"`
	EvidenceRefIDs []string                  `json:"evidence_ref_ids"`
}

type ContextFabricSourceObservation struct {
	Source     string                   `json:"source"`
	State      ContextFabricSourceState `json:"state"`
	ObservedAt *time.Time               `json:"observed_at,omitempty"`
	Watermark  string                   `json:"watermark,omitempty"`
	Reason     string                   `json:"reason,omitempty"`
}

type ContextFabricCoverage struct {
	Sources         []ContextFabricSourceObservation `json:"sources"`
	Partial         bool                             `json:"partial"`
	DegradedReasons []string                         `json:"degraded_reasons,omitempty"`
}

type ContextFabricVersionSet struct {
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

type ContextFabricInterpretedQuestion struct {
	Shape               ContextFabricInvestigationShape `json:"shape"`
	RequestedJudgment   string                          `json:"requested_judgment"`
	SubjectTerms        []string                        `json:"subject_terms,omitempty"`
	ComparisonTerms     []string                        `json:"comparison_terms,omitempty"`
	TimeContext         ContextFabricTimeContext        `json:"time_context"`
	FactRequirements    []ContextFabricFactRequirement  `json:"fact_requirements"`
	ClarificationNeeded bool                            `json:"clarification_needed"`
	ClarificationReason string                          `json:"clarification_reason,omitempty"`
}

type ContextFabricFactRequirement struct {
	Kind       ContextFabricFactKind     `json:"kind"`
	Subjects   []ContextFabricSubjectRef `json:"subjects,omitempty"`
	Parameters map[string]string         `json:"parameters,omitempty"`
}

type ContextFabricInvestigationResult struct {
	SchemaVersion       string                           `json:"schema_version"`
	ResultID            string                           `json:"result_id"`
	RequestID           string                           `json:"request_id"`
	GeneratedAt         time.Time                        `json:"generated_at"`
	Status              ContextFabricInvestigationStatus `json:"status"`
	Question            string                           `json:"question"`
	Interpretation      ContextFabricInterpretedQuestion `json:"interpretation"`
	SubjectResolution   ContextFabricSubjectResolution   `json:"subject_resolution"`
	Cohort              *ContextFabricCohort             `json:"cohort,omitempty"`
	DirectJudgment      string                           `json:"direct_judgment"`
	CurrentState        string                           `json:"current_state"`
	StrongestPressures  []string                         `json:"strongest_pressures"`
	Drivers             []ContextFabricDriverJudgment    `json:"drivers"`
	RemainingWork       []ContextFabricFinding           `json:"remaining_work"`
	ReadinessGaps       []ContextFabricFinding           `json:"readiness_gaps"`
	Paths               []ContextFabricRelationshipPath  `json:"paths"`
	Conflicts           []ContextFabricFinding           `json:"conflicts"`
	Limitations         []string                         `json:"limitations"`
	EvidenceRefIDs      []string                         `json:"evidence_ref_ids"`
	Coverage            ContextFabricCoverage            `json:"coverage"`
	Versions            ContextFabricVersionSet          `json:"versions"`
	DeterministicAnswer string                           `json:"deterministic_answer"`
	Warnings            []string                         `json:"warnings"`
}

// ContextFabricScalarValue is the only free-form value admitted by the public
// projection-control contract. Nested objects and arrays remain disallowed.
type ContextFabricScalarValue struct {
	String  *string  `json:"string,omitempty"`
	Integer *int64   `json:"integer,omitempty"`
	Number  *float64 `json:"number,omitempty"`
	Boolean *bool    `json:"boolean,omitempty"`
	Null    bool     `json:"null,omitempty"`
}

type ContextFabricProjectionBatch struct {
	SchemaVersion       string                                `json:"schema_version"`
	BatchID             string                                `json:"batch_id"`
	OrgID               string                                `json:"org_id"`
	Source              string                                `json:"source"`
	SourceVersion       string                                `json:"source_version"`
	Cursor              string                                `json:"cursor"`
	NextCursor          string                                `json:"next_cursor"`
	GeneratedAt         time.Time                             `json:"generated_at"`
	FullSnapshot        bool                                  `json:"full_snapshot"`
	CompleteEnumeration bool                                  `json:"complete_enumeration"`
	Entities            []ContextFabricEntityProjection       `json:"entities"`
	Relationships       []ContextFabricRelationshipProjection `json:"relationships"`
	Contents            []ContextFabricContentProjection      `json:"contents"`
	Episodes            []ContextFabricEpisodeProjection      `json:"episodes"`
	Tombstones          []ContextFabricProjectionTombstone    `json:"tombstones"`
}

type ContextFabricAuthorizationScope struct {
	RepositorySlugs []string `json:"repository_slugs,omitempty"`
	ProjectIDs      []string `json:"project_ids,omitempty"`
	TeamIDs         []string `json:"team_ids,omitempty"`
}

type ContextFabricEntityProjection struct {
	Subject        ContextFabricSubjectRef             `json:"subject"`
	Aliases        []string                            `json:"aliases,omitempty"`
	PreviousNames  []string                            `json:"previous_names,omitempty"`
	ProviderIDs    map[string]string                   `json:"provider_ids,omitempty"`
	Properties     map[string]ContextFabricScalarValue `json:"properties,omitempty"`
	Authorization  ContextFabricAuthorizationScope     `json:"authorization"`
	EvidenceRefIDs []string                            `json:"evidence_ref_ids"`
	ObservedAt     time.Time                           `json:"observed_at"`
	ValidFrom      *time.Time                          `json:"valid_from,omitempty"`
	ValidTo        *time.Time                          `json:"valid_to,omitempty"`
	SourceVersion  string                              `json:"source_version"`
}

type ContextFabricRelationshipProjection struct {
	RelationshipID  string                              `json:"relationship_id"`
	Type            string                              `json:"type"`
	From            ContextFabricSubjectRef             `json:"from"`
	To              ContextFabricSubjectRef             `json:"to"`
	Properties      map[string]ContextFabricScalarValue `json:"properties,omitempty"`
	Derivation      ContextFabricDerivationMethod       `json:"derivation"`
	EpistemicStatus ContextFabricEpistemicStatus        `json:"epistemic_status"`
	Authorization   ContextFabricAuthorizationScope     `json:"authorization"`
	EvidenceRefIDs  []string                            `json:"evidence_ref_ids"`
	ObservedAt      time.Time                           `json:"observed_at"`
	ValidFrom       *time.Time                          `json:"valid_from,omitempty"`
	ValidTo         *time.Time                          `json:"valid_to,omitempty"`
	SourceVersion   string                              `json:"source_version"`
}

type ContextFabricContentProjection struct {
	ContentID      string                          `json:"content_id"`
	Subject        ContextFabricSubjectRef         `json:"subject"`
	Title          string                          `json:"title"`
	Body           string                          `json:"body"`
	ContentDigest  string                          `json:"content_digest"`
	Authorization  ContextFabricAuthorizationScope `json:"authorization"`
	EvidenceRefIDs []string                        `json:"evidence_ref_ids"`
	ObservedAt     time.Time                       `json:"observed_at"`
	SourceVersion  string                          `json:"source_version"`
	Untrusted      bool                            `json:"untrusted"`
}

type ContextFabricEpisodeProjection struct {
	EpisodeID      string                          `json:"episode_id"`
	Subject        ContextFabricSubjectRef         `json:"subject"`
	Goal           string                          `json:"goal"`
	Outcome        string                          `json:"outcome"`
	Summary        string                          `json:"summary"`
	Authorization  ContextFabricAuthorizationScope `json:"authorization"`
	EvidenceRefIDs []string                        `json:"evidence_ref_ids"`
	StartedAt      time.Time                       `json:"started_at"`
	EndedAt        time.Time                       `json:"ended_at"`
	SourceVersion  string                          `json:"source_version"`
}

type ContextFabricProjectionTombstone struct {
	Kind          string    `json:"kind"`
	CanonicalID   string    `json:"canonical_id"`
	Reason        string    `json:"reason"`
	EffectiveAt   time.Time `json:"effective_at"`
	SourceVersion string    `json:"source_version"`
}

type ContextFabricProjectionReceipt struct {
	BatchID           string    `json:"batch_id"`
	AppliedAt         time.Time `json:"applied_at"`
	BackendWatermark  string    `json:"backend_watermark"`
	EntitiesApplied   int       `json:"entities_applied"`
	EdgesApplied      int       `json:"edges_applied"`
	ContentsApplied   int       `json:"contents_applied"`
	EpisodesApplied   int       `json:"episodes_applied"`
	TombstonesApplied int       `json:"tombstones_applied"`
}

type ContextFabricProjectionWatermark struct {
	OrgID            string    `json:"org_id"`
	Source           string    `json:"source"`
	Cursor           string    `json:"cursor"`
	SourceVersion    string    `json:"source_version"`
	ProjectedAt      time.Time `json:"projected_at"`
	BackendWatermark string    `json:"backend_watermark"`
}

type ContextFabricProjectionCheckpoint struct {
	OrgID            string    `json:"org_id"`
	Source           string    `json:"source"`
	Cursor           string    `json:"cursor"`
	SourceVersion    string    `json:"source_version"`
	BackendWatermark string    `json:"backend_watermark"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type ContextFabricCapabilities struct {
	Enabled                        bool                          `json:"enabled"`
	SupportsOpenQuestions          bool                          `json:"supports_open_questions"`
	SupportsPriorSubjectReceipts   bool                          `json:"supports_prior_subject_receipts"`
	SupportedRequestSchemaVersions []string                      `json:"supported_request_schema_versions"`
	SupportedResultSchemaVersions  []string                      `json:"supported_result_schema_versions"`
	Limits                         ContextFabricCapabilityLimits `json:"limits"`
}

type ContextFabricCapabilityLimits struct {
	MaxSubjectCandidates int `json:"max_subject_candidates"`
	MaxCohortMembers     int `json:"max_cohort_members"`
	MaxRelationshipPaths int `json:"max_relationship_paths"`
	MaxDrivers           int `json:"max_drivers"`
	MaxEvidenceRefs      int `json:"max_evidence_refs"`
	MaxSerializedBytes   int `json:"max_serialized_bytes"`
}
