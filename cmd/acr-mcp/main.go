package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

type metadata struct {
	Service       string   `json:"service"`
	Version       string   `json:"version"`
	Commit        string   `json:"commit"`
	BuildDate     string   `json:"build_date"`
	Transport     string   `json:"transport"`
	EnabledTools  []string `json:"enabled_tools"`
	DisabledTools []string `json:"disabled_tools"`
	Status        string   `json:"status"`
}

type diagnostic struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type doctorReport struct {
	Service                  string           `json:"service"`
	Version                  string           `json:"version"`
	Commit                   string           `json:"commit"`
	BuildDate                string           `json:"build_date"`
	APIURLSet                bool             `json:"api_url_set"`
	APIURLValid              bool             `json:"api_url_valid"`
	CredentialSet            bool             `json:"credential_set"`
	CredentialSource         string           `json:"credential_source,omitempty"`
	CredentialShapeValid     bool             `json:"credential_shape_valid"`
	WriteEnabled             bool             `json:"write_enabled"`
	TranscriptCaptureEnabled bool             `json:"transcript_capture_enabled"`
	LogLevel                 string           `json:"log_level,omitempty"`
	Checks                   []diagnostic     `json:"checks"`
	Status                   string           `json:"status"`
	LiveCheck                *doctorLiveCheck `json:"live_check,omitempty"`
	LocalIndex               localIndexReport `json:"local_index"`
}

// doctorLiveCheck is populated by plain `acr-mcp doctor` (live is its
// default mode) and by the explicit `acr-mcp doctor --live` alias --
// never by `acr-mcp doctor --offline`. It reports the actual, live
// entitlement, scope, and enabled-tool availability a real hosted
// capabilities handshake returned for the currently configured
// credential, rather than only the static local configuration runDoctor
// already reports without touching the network.
type doctorLiveCheck struct {
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

func main() {
	os.Exit(runCLI(os.Args[1:]))
}

func currentMetadata() metadata {
	info := version.Current()
	return metadata{
		Service:       "dev-health-acr-mcp",
		Version:       info.Version,
		Commit:        info.Commit,
		BuildDate:     info.Date,
		Transport:     "stdio",
		EnabledTools:  []string{"context_for_task", "source_evidence"},
		DisabledTools: []string{"record_episode"},
		Status:        "read-only",
	}
}

func runDoctor() doctorReport {
	info := version.Current()
	apiURLSet := strings.TrimSpace(os.Getenv(sidecar.APIURLEnvironment)) != ""
	checks := []diagnostic{
		{Name: "binary", Status: "ok", Detail: "acr-mcp is executable"},
		{Name: "transport", Status: "ok", Detail: "STDIO is the SVS MCP transport"},
	}

	// sidecar.LoadConfig applies the same network-free invariants the real
	// `serve` path relies on: ACR_API_URL must be a well-formed, origin-only
	// URL (no userinfo/path/query/fragment, including a bare "?" with no
	// query text) and HTTPS unless an explicit loopback fixture opts into
	// plain HTTP, plus bounded ranges for the optional timeout/size/proxy
	// settings and, when ACR_API_CA_BUNDLE is set, a bounded local read
	// (never following a symlink, never blocking on a FIFO) plus a PEM
	// validity check with the same size ceiling NewClient will apply. None
	// of this touches the network, so calling it here distinguishes an
	// absent ACR_API_URL from a malformed one (wrong scheme, embedded
	// credentials, stray path/query, an unusable CA bundle, and so on)
	// instead of reporting "ok" for any nonblank value. Every error
	// sidecar.LoadConfig/Config.Validate can return is a *sidecar.ConfigError
	// (see config.go's ConfigError type and describeFileError handling)
	// whose Error() text is a fixed, path- and value-free description --
	// never the raw configured URL, any userinfo it carried, or the CA
	// bundle path. This surface still goes through
	// sidecar.DescribeConfigError rather than calling configErr.Error()
	// directly, so a future config-parsing code path that forgets to
	// return a *ConfigError degrades to a fixed, generic description
	// here instead of leaking whatever that error happens to contain.
	cfg, configErr := sidecar.LoadConfig()
	apiURLValid := apiURLSet && configErr == nil
	switch {
	case !apiURLSet:
		checks = append(checks, diagnostic{Name: "api_url", Status: "warning", Detail: "ACR_API_URL is not configured"})
	case configErr != nil:
		checks = append(checks, diagnostic{Name: "api_url", Status: "error", Detail: "ACR_API_URL is configured but the sidecar configuration is invalid: " + sidecar.DescribeConfigError(configErr)})
	default:
		checks = append(checks, diagnostic{Name: "api_url", Status: "ok", Detail: "ACR_API_URL is configured and valid"})
	}

	credential, credentialErr := sidecar.LoadCredential()
	// A credential that failed LoadCredential's own shape check is still
	// "set" from the operator's point of view -- they configured something,
	// it is just malformed. Lifecycle contention and every other operational
	// load failure are neither missing nor malformed: doctor cannot safely
	// determine whether a credential is present while the load boundary is
	// unavailable. Keep those states distinct, and render only fixed detail
	// strings so paths, configured values, and underlying error text can
	// never reach the report.
	credentialMissing := errors.Is(credentialErr, sidecar.ErrCredentialMissing)
	credentialShapeInvalid := errors.Is(credentialErr, sidecar.ErrCredentialShapeInvalid)
	credentialLifecycleBusy := errors.Is(credentialErr, sidecar.ErrCredentialLifecycleBusy)
	credentialUnavailable := credentialErr != nil && !credentialMissing && !credentialShapeInvalid
	credentialSet := credentialErr == nil || credentialShapeInvalid
	credentialShapeValid := credentialErr == nil && auth.IsTokenShapeValid(credential.Token)
	switch {
	case credentialMissing:
		checks = append(checks, diagnostic{Name: "credential", Status: "warning", Detail: "ACR API credential is not configured"})
	case credentialShapeInvalid:
		checks = append(checks, diagnostic{Name: "credential", Status: "error", Detail: "ACR API credential is configured but malformed"})
	case credentialLifecycleBusy:
		checks = append(checks, diagnostic{Name: "credential", Status: "error", Detail: "ACR API credential could not be checked because another credential lifecycle operation is active"})
	case credentialUnavailable:
		checks = append(checks, diagnostic{Name: "credential", Status: "error", Detail: "ACR API credential could not be checked safely"})
	default:
		checks = append(checks, diagnostic{Name: "credential", Status: "ok", Detail: "ACR API credential is configured and redacted via " + credential.Source})
	}

	status := "ok"
	if !apiURLSet || credentialMissing {
		status = "incomplete_configuration"
	}
	if apiURLSet && !apiURLValid {
		status = "invalid_configuration"
	}
	if credentialShapeInvalid {
		status = "invalid_configuration"
	}
	if credentialUnavailable {
		status = "credential_unavailable"
	}
	localIndex := probeLocalIndex()
	switch {
	case !localIndex.ConfigValid:
		checks = append(checks, diagnostic{Name: "local_index", Status: "warning", Detail: "local-index configuration is invalid"})
	case !localIndex.WorkspaceDiscovered:
		checks = append(checks, diagnostic{Name: "local_index", Status: "warning", Detail: "local-index workspace is unavailable"})
	case !localIndex.Available:
		checks = append(checks, diagnostic{Name: "local_index", Status: "warning", Detail: "local index is unavailable"})
	default:
		checks = append(checks, diagnostic{Name: "local_index", Status: "ok", Detail: "local index is available"})
	}
	report := doctorReport{
		Service:              "dev-health-acr-mcp",
		Version:              info.Version,
		Commit:               info.Commit,
		BuildDate:            info.Date,
		APIURLSet:            apiURLSet,
		APIURLValid:          apiURLValid,
		CredentialSet:        credentialSet,
		CredentialSource:     credential.Source,
		CredentialShapeValid: credentialShapeValid,
		Checks:               checks,
		Status:               status,
		LocalIndex:           localIndex,
	}
	if configErr == nil {
		report.LogLevel = cfg.LogLevel.String()
		report.WriteEnabled = cfg.EnableWriteback
		report.TranscriptCaptureEnabled = cfg.EnableTranscriptCapture
	}
	return report
}

// runDoctorLive extends runDoctor's static, network-free report with a
// real, bounded capabilities handshake against the hosted API -- the
// same acrmcp.NewBootstrap path `serve` uses -- so `acr-mcp doctor --live`
// can display the actual, live entitlement/scope/tool availability for
// the currently configured credential, not only static local
// configuration. It only attempts the network call when the static
// checks already report a valid API URL and credential shape (otherwise
// LiveCheck reports unreachable without ever touching the network), and
// every value it surfaces is either a fixed sentence or a field already
// engineered safe to print verbatim (acrmcp.NewBootstrap's own error
// contract; see its doc comment).
func runDoctorLive() doctorReport {
	report := runDoctor()
	if !report.APIURLValid || !report.CredentialShapeValid {
		report.LiveCheck = &doctorLiveCheck{Reachable: false, Detail: "local configuration is not valid; live check skipped"}
		return report
	}
	probe, err := acrmcp.ProbeCapabilities(context.Background(), version.Current())
	if err != nil {
		if minimum, ok := acrmcp.VersionMismatchMinimum(err); ok {
			report.LiveCheck = &doctorLiveCheck{Reachable: true, Detail: "the installed ACR client is unsupported; update acr-mcp to version " + minimum + " or later"}
			report.Status = "live_check_incompatible"
			return report
		}
		// A bootstrap failure here covers both a real network/TLS/connection
		// problem reaching the hosted API and a hosted-side rejection
		// (incompatibility, credential, entitlement): every case means the
		// live check itself did not succeed, so the top-level status must
		// say so too -- report.Status must never stay "ok" (set above by
		// runDoctor purely from static local configuration) while
		// LiveCheck.Reachable is false, or a caller reading only the
		// top-level status would miss a real, live-verified outage.
		report.LiveCheck = &doctorLiveCheck{Reachable: false, Detail: err.Error()}
		report.Status = "live_check_unreachable"
		return report
	}
	report.LiveCheck = &doctorLiveCheck{
		Reachable:                true,
		AgentContextRuntime:      probe.Capabilities.Entitlements.AgentContextRuntime,
		ContextReadScope:         probe.Capabilities.Permissions.ContextRead,
		EvidenceReadScope:        probe.Capabilities.Permissions.EvidenceRead,
		EpisodeWriteScope:        probe.Capabilities.Permissions.EpisodeWrite,
		RecordEpisodeActive:      probe.Config.EnableWriteback && probe.Capabilities.Entitlements.AgentContextRuntime && probe.Capabilities.Permissions.EpisodeWrite && slices.Contains(probe.Capabilities.EnabledTools, "record_episode"),
		TranscriptCaptureEnabled: probe.Config.EnableTranscriptCapture,
		EnabledTools:             probe.Capabilities.EnabledTools,
	}
	if err := probe.CheckCompatibility(); err != nil {
		report.Status = "live_check_incompatible"
		report.LiveCheck.Detail = err.Error()
	}
	return report
}

func printJSON(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runServe bootstraps and runs the STDIO MCP server, returning a process
// exit code. Startup failures (invalid config, missing/invalid credential,
// hosted API incompatibility) are reported as a single sanitized line on
// stderr by acrmcp.Serve; stdout is reserved exclusively for MCP JSON-RPC
// traffic and is never written to directly here.
func runServe() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := acrmcp.ServeWithIdentity(ctx, os.Stderr, version.Current()); err != nil {
		return 1
	}
	return 0
}
