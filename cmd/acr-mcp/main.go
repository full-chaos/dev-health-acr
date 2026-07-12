package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

var version = "dev"

type metadata struct {
	Service       string   `json:"service"`
	Version       string   `json:"version"`
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
	Service              string           `json:"service"`
	Version              string           `json:"version"`
	APIURLSet            bool             `json:"api_url_set"`
	APIURLValid          bool             `json:"api_url_valid"`
	CredentialSet        bool             `json:"credential_set"`
	CredentialSource     string           `json:"credential_source,omitempty"`
	CredentialShapeValid bool             `json:"credential_shape_valid"`
	WriteEnabled         bool             `json:"write_enabled"`
	LogLevel             string           `json:"log_level,omitempty"`
	Checks               []diagnostic     `json:"checks"`
	Status               string           `json:"status"`
	LiveCheck            *doctorLiveCheck `json:"live_check,omitempty"`
}

// doctorLiveCheck is populated only by `acr-mcp doctor --live`, never by
// the default `acr-mcp doctor`: it reports the actual, live entitlement,
// scope, and enabled-tool availability a real hosted capabilities
// handshake returned for the currently configured credential, rather than
// only the static local configuration runDoctor already reports without
// touching the network.
type doctorLiveCheck struct {
	Reachable           bool     `json:"reachable"`
	Detail              string   `json:"detail,omitempty"`
	AgentContextRuntime bool     `json:"agent_context_runtime"`
	ContextReadScope    bool     `json:"context_read_scope"`
	EvidenceReadScope   bool     `json:"evidence_read_scope"`
	EnabledTools        []string `json:"enabled_tools,omitempty"`
}

func main() {
	command := "metadata"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	switch command {
	case "version", "--version", "-version":
		fmt.Println(version)
	case "doctor":
		if len(os.Args) > 2 && os.Args[2] == "--live" {
			printJSON(runDoctorLive())
		} else {
			printJSON(runDoctor())
		}
	case "metadata":
		printJSON(metadata{
			Service:       "dev-health-acr-mcp",
			Version:       version,
			Transport:     "stdio",
			EnabledTools:  []string{"context_for_task", "source_evidence"},
			DisabledTools: []string{"record_episode"},
			Status:        "read-only",
		})
	case "serve":
		os.Exit(runServe())
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q; use version, doctor, metadata, or serve\n", command)
		os.Exit(2)
	}
}

func runDoctor() doctorReport {
	apiURLSet := strings.TrimSpace(os.Getenv(sidecar.APIURLEnvironment)) != ""
	writeEnabled := strings.EqualFold(strings.TrimSpace(os.Getenv("ACR_ENABLE_WRITEBACK")), "true")
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
	// A credential that failed LoadCredential's own shape check (rather
	// than simply being unconfigured) is still "set" from the operator's
	// point of view -- they configured something, it is just malformed --
	// so doctor keeps reporting it as configured-but-invalid instead of
	// collapsing it into "not configured". credential is the zero value in
	// this case, so nothing about the rejected value is ever echoed.
	credentialShapeInvalid := errors.Is(credentialErr, sidecar.ErrCredentialShapeInvalid)
	credentialSet := credentialErr == nil || credentialShapeInvalid
	credentialShapeValid := credentialErr == nil && auth.IsTokenShapeValid(credential.Token)
	if !credentialSet {
		checks = append(checks, diagnostic{Name: "credential", Status: "warning", Detail: "ACR API credential is not configured"})
	} else if !credentialShapeValid {
		checks = append(checks, diagnostic{Name: "credential", Status: "error", Detail: "ACR API credential is configured but malformed"})
	} else {
		checks = append(checks, diagnostic{Name: "credential", Status: "ok", Detail: "ACR API credential is configured and redacted via " + credential.Source})
	}

	status := "ok"
	if !apiURLSet || !credentialSet {
		status = "incomplete_configuration"
	}
	if apiURLSet && !apiURLValid {
		status = "invalid_configuration"
	}
	if credentialSet && !credentialShapeValid {
		status = "invalid_configuration"
	}
	report := doctorReport{
		Service:              "dev-health-acr-mcp",
		Version:              version,
		APIURLSet:            apiURLSet,
		APIURLValid:          apiURLValid,
		CredentialSet:        credentialSet,
		CredentialSource:     credential.Source,
		CredentialShapeValid: credentialShapeValid,
		WriteEnabled:         writeEnabled,
		Checks:               checks,
		Status:               status,
	}
	if configErr == nil {
		report.LogLevel = cfg.LogLevel.String()
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
	boot, err := acrmcp.NewBootstrap(context.Background(), version)
	if err != nil {
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
		Reachable:           true,
		AgentContextRuntime: boot.Capabilities.Entitlements.AgentContextRuntime,
		ContextReadScope:    boot.Capabilities.Permissions.ContextRead,
		EvidenceReadScope:   boot.Capabilities.Permissions.EvidenceRead,
		EnabledTools:        boot.Capabilities.EnabledTools,
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
	if err := acrmcp.Serve(ctx, os.Stderr, version); err != nil {
		return 1
	}
	return 0
}
