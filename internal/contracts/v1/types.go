package v1

import "time"

const (
	ContextPacketRequestSchema = "context_packet_request.v1"
	ContextPacketSchema        = "context_packet.v1"
	ContextPacketItemSchema    = "context_packet_item.v1"
	EvidenceRefSchema          = "evidence_ref.v1"
	ExpandedEvidenceSchema     = "expanded_evidence.v1"
	CapabilitiesSchema         = "capabilities.v1"
	AgentEpisodeCreateSchema   = "agent_episode_create.v1"
	AgentEpisodeSchema         = "agent_episode.v1"
	ClientCredentialSchema     = "acr_client_credential.v1"
	ErrorSchema                = "error.v1"
)

type ScopeResolution string

const (
	ScopeExactCommit    ScopeResolution = "exact_commit"
	ScopeBranchFiltered ScopeResolution = "branch_filtered"
	ScopeRepoFallback   ScopeResolution = "repo_fallback"
	ScopeUnresolved     ScopeResolution = "unresolved"
)

type PacketStatus string

const (
	PacketComplete PacketStatus = "complete"
	PacketPartial  PacketStatus = "partial"
	PacketDegraded PacketStatus = "degraded"
	PacketEmpty    PacketStatus = "empty"
)

type PacketCategory string

const (
	CategoryState    PacketCategory = "state"
	CategoryPressure PacketCategory = "pressure"
	CategoryCause    PacketCategory = "cause"
	CategoryEvidence PacketCategory = "evidence"
	CategoryAction   PacketCategory = "action"
)

type ClaimKind string

const (
	ClaimObserved       ClaimKind = "observed"
	ClaimInferred       ClaimKind = "inferred"
	ClaimRecommendation ClaimKind = "recommendation"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

type EvidenceAvailability string

const (
	EvidenceAvailable    EvidenceAvailability = "available"
	EvidenceStale        EvidenceAvailability = "stale"
	EvidenceRedacted     EvidenceAvailability = "redacted"
	EvidenceDeleted      EvidenceAvailability = "deleted"
	EvidenceUnauthorized EvidenceAvailability = "unauthorized"
)

type ContextPacketRequest struct {
	SchemaVersion string         `json:"schema_version"`
	RequestID     string         `json:"request_id"`
	Goal          string         `json:"goal"`
	Repository    RepositoryRef  `json:"repository"`
	Scope         RequestedScope `json:"scope"`
	Options       PacketOptions  `json:"options"`
	Client        ClientInfo     `json:"client"`
}

type RepositoryRef struct {
	Slug      string `json:"slug"`
	RepoID    string `json:"repo_id,omitempty"`
	RemoteURL string `json:"remote_url,omitempty"`
}

type RequestedScope struct {
	Branch         string     `json:"branch,omitempty"`
	CommitSHA      string     `json:"commit_sha,omitempty"`
	TaskRef        string     `json:"task_ref,omitempty"`
	Files          []string   `json:"files,omitempty"`
	AsOf           *time.Time `json:"as_of,omitempty"`
	TimeWindowDays int        `json:"time_window_days,omitempty"`
}

type PacketOptions struct {
	RequestedCategories  []PacketCategory `json:"requested_categories,omitempty"`
	MaxItems             int              `json:"max_items"`
	MaxOutputTokens      int              `json:"max_output_tokens"`
	MaxSerializedBytes   int              `json:"max_serialized_bytes"`
	IncludeDebug         bool             `json:"include_debug"`
	IncludeLowConfidence bool             `json:"include_low_confidence"`
}

type ClientInfo struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	SidecarVersion string `json:"sidecar_version,omitempty"`
}

type ContextPacket struct {
	SchemaVersion         string              `json:"schema_version"`
	ContextPacketID       string              `json:"context_packet_id"`
	RequestID             string              `json:"request_id"`
	GeneratedAt           time.Time           `json:"generated_at"`
	Status                PacketStatus        `json:"status"`
	Goal                  string              `json:"goal"`
	Repository            RepositoryRef       `json:"repository"`
	RequestedScope        RequestedScope      `json:"requested_scope"`
	ResolvedScope         ResolvedScope       `json:"resolved_scope"`
	QueryVersion          string              `json:"query_version"`
	RankingVersion        string              `json:"ranking_version"`
	Summary               string              `json:"summary"`
	Items                 []ContextPacketItem `json:"items"`
	RequiredChecks        []RequiredCheck     `json:"required_checks"`
	RecommendedNextSteps  []RecommendedStep   `json:"recommended_next_steps"`
	Freshness             Freshness           `json:"freshness"`
	Coverage              Coverage            `json:"coverage"`
	Budget                PacketBudget        `json:"budget"`
	Warnings              []string            `json:"warnings"`
	Compatibility         Compatibility       `json:"compatibility"`
	RetrievalDebugSummary string              `json:"retrieval_debug_summary,omitempty"`
}

type ResolvedScope struct {
	RepoID          string          `json:"repo_id"`
	RepoSlug        string          `json:"repo_slug"`
	Branch          string          `json:"branch,omitempty"`
	CommitSHA       string          `json:"commit_sha,omitempty"`
	Resolution      ScopeResolution `json:"resolution"`
	FallbackReasons []string        `json:"fallback_reasons"`
}

type ContextPacketItem struct {
	SchemaVersion   string          `json:"schema_version"`
	PacketItemID    string          `json:"packet_item_id"`
	Category        PacketCategory  `json:"category"`
	ClaimKind       ClaimKind       `json:"claim_kind"`
	Title           string          `json:"title"`
	Summary         string          `json:"summary"`
	WhyIncluded     string          `json:"why_included"`
	RuleID          string          `json:"rule_id"`
	Confidence      float64         `json:"confidence"`
	Severity        Severity        `json:"severity"`
	Rank            int             `json:"rank"`
	ValidityScope   ValidityScope   `json:"validity_scope"`
	Flags           ItemFlags       `json:"flags"`
	RelatedEntities []RelatedEntity `json:"related_entities"`
	EvidenceRefIDs  []string        `json:"evidence_ref_ids"`
}

type ValidityScope struct {
	Branch    string     `json:"branch,omitempty"`
	CommitSHA string     `json:"commit_sha,omitempty"`
	ValidFrom *time.Time `json:"valid_from,omitempty"`
	ValidTo   *time.Time `json:"valid_to,omitempty"`
}

type ItemFlags struct {
	Stale            bool `json:"stale"`
	Uncertain        bool `json:"uncertain"`
	Conflicting      bool `json:"conflicting"`
	UntrustedContent bool `json:"untrusted_content"`
}

type RelatedEntity struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Label string `json:"label"`
	URL   string `json:"url,omitempty"`
}

type RequiredCheck struct {
	CheckID string `json:"check_id"`
	Label   string `json:"label"`
	Reason  string `json:"reason"`
	RuleID  string `json:"rule_id"`
}

type RecommendedStep struct {
	StepID string `json:"step_id"`
	Label  string `json:"label"`
	Reason string `json:"reason"`
	RuleID string `json:"rule_id"`
}

type Freshness struct {
	AsOf              time.Time         `json:"as_of"`
	StaleAfterSeconds int               `json:"stale_after_seconds"`
	Watermarks        []SourceWatermark `json:"watermarks"`
}

type SourceWatermark struct {
	Source         string     `json:"source"`
	LastIngestedAt *time.Time `json:"last_ingested_at,omitempty"`
	Status         string     `json:"status"`
}

type Coverage struct {
	SourcesConsidered  []string            `json:"sources_considered"`
	SourcesAvailable   []string            `json:"sources_available"`
	SourcesUnavailable []UnavailableSource `json:"sources_unavailable"`
	Partial            bool                `json:"partial"`
	DegradedReasons    []string            `json:"degraded_reasons"`
}

type UnavailableSource struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

type PacketBudget struct {
	MaxItems           int  `json:"max_items"`
	ItemsUsed          int  `json:"items_used"`
	MaxOutputTokens    int  `json:"max_output_tokens"`
	EstimatedTokens    int  `json:"estimated_tokens"`
	MaxSerializedBytes int  `json:"max_serialized_bytes"`
	SerializedBytes    int  `json:"serialized_bytes"`
	Truncated          bool `json:"truncated"`
}

type Compatibility struct {
	ServiceVersion          string   `json:"service_version"`
	MinimumSidecarVersion   string   `json:"minimum_sidecar_version"`
	SupportedSchemaVersions []string `json:"supported_schema_versions"`
}

type EvidenceRef struct {
	SchemaVersion string               `json:"schema_version"`
	EvidenceRefID string               `json:"evidence_ref_id"`
	Source        EvidenceSource       `json:"source"`
	Provenance    string               `json:"provenance"`
	Confidence    float64              `json:"confidence"`
	Citation      string               `json:"citation"`
	ObservedAt    time.Time            `json:"observed_at"`
	EventAt       *time.Time           `json:"event_at,omitempty"`
	SourceVersion string               `json:"source_version,omitempty"`
	SnapshotHash  string               `json:"snapshot_hash,omitempty"`
	ContentDigest string               `json:"content_digest,omitempty"`
	Availability  EvidenceAvailability `json:"availability"`
	Metadata      map[string]any       `json:"metadata,omitempty"`
}

type EvidenceSource struct {
	System       string `json:"system"`
	EntityType   string `json:"entity_type"`
	EntityID     string `json:"entity_id"`
	DisplayLabel string `json:"display_label"`
	SafeURI      string `json:"safe_uri,omitempty"`
}

type ExpandedEvidence struct {
	SchemaVersion   string               `json:"schema_version"`
	Evidence        EvidenceRef          `json:"evidence"`
	ResolvedAt      time.Time            `json:"resolved_at"`
	Availability    EvidenceAvailability `json:"availability"`
	Excerpt         string               `json:"excerpt,omitempty"`
	Structured      map[string]any       `json:"structured_fields"`
	RedactionReason string               `json:"redaction_reason,omitempty"`
}

type Capabilities struct {
	SchemaVersion           string                 `json:"schema_version"`
	Service                 string                 `json:"service"`
	ServiceVersion          string                 `json:"service_version"`
	MinimumSidecarVersion   string                 `json:"minimum_sidecar_version"`
	SupportedSchemaVersions []string               `json:"supported_schema_versions"`
	EnabledTools            []string               `json:"enabled_tools"`
	Entitlements            CapabilityEntitlements `json:"entitlements"`
	Permissions             CapabilityPermissions  `json:"permissions"`
	Limits                  CapabilityLimits       `json:"limits"`
	GeneratedAt             time.Time              `json:"generated_at"`
}

type CapabilityEntitlements struct {
	AgentContextRuntime bool `json:"agent_context_runtime"`
}

type CapabilityPermissions struct {
	ContextRead  bool `json:"context_read"`
	EvidenceRead bool `json:"evidence_read"`
	EpisodeWrite bool `json:"episode_write"`
}

type CapabilityLimits struct {
	MaxItems           int `json:"max_items"`
	MaxOutputTokens    int `json:"max_output_tokens"`
	MaxSerializedBytes int `json:"max_serialized_bytes"`
	RequestsPerMinute  int `json:"requests_per_minute"`
}

type AgentEpisodeCreate struct {
	SchemaVersion   string           `json:"schema_version"`
	ClientEpisodeID string           `json:"client_episode_id"`
	IdempotencyKey  string           `json:"idempotency_key"`
	ContextPacketID string           `json:"context_packet_id"`
	Goal            string           `json:"goal"`
	TaskRef         string           `json:"task_ref,omitempty"`
	Repository      RepositoryRef    `json:"repository"`
	Scope           EpisodeScope     `json:"scope"`
	Client          EpisodeClient    `json:"client"`
	StartedAt       time.Time        `json:"started_at"`
	EndedAt         time.Time        `json:"ended_at"`
	Outcome         string           `json:"outcome"`
	Summary         string           `json:"summary"`
	Artifacts       EpisodeArtifacts `json:"artifacts"`
	Transcript      TranscriptRef    `json:"transcript"`
	RetentionClass  string           `json:"retention_class"`
}

type EpisodeScope struct {
	Branch    string `json:"branch,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
}

type EpisodeClient struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	SidecarVersion string `json:"sidecar_version"`
	AgentName      string `json:"agent_name,omitempty"`
	Model          string `json:"model,omitempty"`
}

type EpisodeArtifacts struct {
	FilesTouched []string `json:"files_touched"`
	ArtifactURIs []string `json:"artifact_uris"`
	TestsRun     []string `json:"tests_run"`
}

type TranscriptRef struct {
	Mode            string `json:"mode"`
	OpaqueRef       string `json:"opaque_ref,omitempty"`
	RedactedSummary string `json:"redacted_summary,omitempty"`
}

type AgentEpisode struct {
	AgentEpisodeCreate
	EpisodeID      string    `json:"episode_id"`
	CreatedAt      time.Time `json:"created_at"`
	RedactionState string    `json:"redaction_state"`
}

type ClientCredential struct {
	SchemaVersion    string     `json:"schema_version"`
	CredentialID     string     `json:"credential_id"`
	Name             string     `json:"name"`
	TokenPrefix      string     `json:"token_prefix"`
	OrgID            string     `json:"org_id"`
	RepositoryScopes []string   `json:"repository_scopes"`
	Scopes           []string   `json:"scopes"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        *time.Time `json:"expires_at"`
	RevokedAt        *time.Time `json:"revoked_at"`
	LastUsedAt       *time.Time `json:"last_used_at"`
}

type ErrorEnvelope struct {
	SchemaVersion string      `json:"schema_version"`
	RequestID     string      `json:"request_id"`
	Error         ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	HTTPStatus int            `json:"http_status"`
	Retryable  bool           `json:"retryable"`
	Details    map[string]any `json:"details,omitempty"`
}
