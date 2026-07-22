// Package diagnostics builds a secrets-free diagnostic bundle for the ACR
// MCP sidecar: a small deterministic tar archive containing a schema-
// versioned manifest, the static (network-free) doctor report, an optional
// sanitized live capabilities check, and a human-readable interpretation
// README. Every type in this package is a caller-assembled, already-
// sanitized value -- this package never reads environment variables,
// files, or the network itself, and never accepts a raw URL, credential,
// file path, HTTP header, or response body. Callers (see cmd/acr-mcp) are
// responsible for narrowing their own richer internal report types down to
// exactly these fields before calling Build.
package diagnostics

// Identity carries build and runtime identity fields safe to publish
// verbatim: semantic version, commit, build date, and the compiling
// platform's GOOS/GOARCH. None of these fields can carry a credential,
// host, or filesystem path.
type Identity struct {
	Service   string `json:"service"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

// CheckResult mirrors one entry of the static doctor report's Checks list.
// Detail is expected to already be sanitized by the caller (acr-mcp doctor
// has exhaustive canary tests proving its own Detail strings never embed a
// token, userinfo secret, or configured path/URL); this package treats it
// as opaque text and never inspects or rewrites it.
type CheckResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// ConfigBounds reports only the presence and numeric/boolean bounds of
// optional sidecar configuration -- never the configured URL, host, or
// filesystem path itself. A nil *ConfigBounds means the local
// configuration could not be loaded (for example, an invalid ACR_API_URL),
// so no bounds are available to report.
type ConfigBounds struct {
	TimeoutSeconds        float64 `json:"timeout_seconds"`
	MaxResponseBytes      int64   `json:"max_response_bytes"`
	MaxRequestBodyBytes   int64   `json:"max_request_body_bytes"`
	AllowInsecureLoopback bool    `json:"allow_insecure_loopback"`
	ProxyConfigured       bool    `json:"proxy_configured"`
	CABundleConfigured    bool    `json:"ca_bundle_configured"`
}

// StaticReport mirrors the network-free `acr-mcp doctor --offline`
// report's safe subset: presence/validity flags, the credential source
// enum ("environment", "keyring", or "file" -- never a path), and
// bounded, non-secret config values. It never carries the configured API
// URL, any userinfo, the bearer credential value, or a CA bundle/token file path.
type StaticReport struct {
	APIURLSet                bool             `json:"api_url_set"`
	APIURLValid              bool             `json:"api_url_valid"`
	CredentialSet            bool             `json:"credential_set"`
	CredentialSource         string           `json:"credential_source,omitempty"`
	CredentialShapeValid     bool             `json:"credential_shape_valid"`
	WriteEnabled             bool             `json:"write_enabled"`
	TranscriptCaptureEnabled bool             `json:"transcript_capture_enabled"`
	LogLevel                 string           `json:"log_level,omitempty"`
	Status                   string           `json:"status"`
	Bounds                   *ConfigBounds    `json:"bounds,omitempty"`
	Checks                   []CheckResult    `json:"checks"`
	LocalIndex               LocalIndexReport `json:"local_index"`
}

type LocalIndexReport struct {
	ProviderMode                string `json:"provider_mode"`
	ConfigValid                 bool   `json:"config_valid"`
	WorkspaceDiscovered         bool   `json:"workspace_discovered"`
	RepositoryIdentityAvailable bool   `json:"repository_identity_available"`
	WorkspaceScopeValid         bool   `json:"workspace_scope_valid"`
	IndexChecked                bool   `json:"index_checked"`
	IndexReadable               bool   `json:"index_readable"`
	Available                   bool   `json:"available"`
	ProviderVersion             string `json:"provider_version,omitempty"`
	VersionChecked              bool   `json:"version_checked"`
	VersionCompatible           bool   `json:"version_compatible"`
	Status                      string `json:"status"`
	Freshness                   string `json:"freshness"`
	MaxItems                    int    `json:"max_items"`
	MaxOutputTokens             int    `json:"max_output_tokens"`
	WorktreeMismatchChecked     bool   `json:"worktree_mismatch_checked"`
	WorktreeMismatchDetected    bool   `json:"worktree_mismatch_detected"`
	QueryChecked                bool   `json:"query_checked"`
	QuerySucceeded              bool   `json:"query_succeeded"`
	ResultCount                 int    `json:"result_count"`
	IndexedCommitStatus         string `json:"indexed_commit_status,omitempty"`
	ErrorCode                   string `json:"error_code,omitempty"`
}

// LiveReport mirrors the sanitized `acr-mcp doctor --live` result: booleans
// and enabled-tool names only. It never carries the hosted API host, any
// HTTP header, or any response body -- only the already-sanitized
// entitlement/scope/reachability booleans and the fixed detail sentence
// doctor's own canary tests already prove leak-free.
type LiveReport struct {
	Reachable                bool     `json:"reachable"`
	Detail                   string   `json:"detail,omitempty"`
	AgentContextRuntime      bool     `json:"agent_context_runtime"`
	ContextReadScope         bool     `json:"context_read_scope"`
	EvidenceReadScope        bool     `json:"evidence_read_scope"`
	EpisodeWriteScope        bool     `json:"episode_write_scope"`
	RecordEpisodeActive      bool     `json:"record_episode_active"`
	TranscriptCaptureEnabled bool     `json:"transcript_capture_enabled"`
	EnabledTools             []string `json:"enabled_tools,omitempty"`
}

// Input is the fully-sanitized data a caller assembles before calling
// Build. Assembling it explicitly field-by-field (rather than accepting a
// caller's own richer report type) is a deliberate second line of defense:
// even if a future field were added upstream, it does not reach the bundle
// unless this package's types grow a matching field too.
type Input struct {
	Identity Identity
	Static   StaticReport
	Live     *LiveReport
}
