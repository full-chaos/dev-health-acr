package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/diagnostics"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

const (
	doctorUsageLine      = "Usage: acr-mcp doctor [--offline] [--bundle <path>] [--live]"
	diagnosticsUsageLine = "Usage: acr-mcp diagnostics --output <path> [--live]"
)

var errDoctorArgsInvalid = errors.New("doctor arguments are invalid")

type doctorArgs struct {
	offline      bool
	live         bool
	bundle       bool
	bundleOutput string
	bundleLive   bool
}

// parseDiagnosticsArgs parses the arguments following the `diagnostics`
// command: an explicit --output <path> is required (there is intentionally
// no default destination, so a bundle is never written somewhere the
// caller did not ask for), and --live is an optional opt-in to include a
// real, sanitized hosted-API capabilities check alongside the static
// report.
func parseDiagnosticsArgs(args []string) (output string, live bool, err error) {
	seenOutput := false
	seenLive := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--live":
			if seenLive {
				return "", false, errors.New("diagnostics arguments are invalid")
			}
			seenLive = true
			live = true
		case args[i] == "--output":
			if seenOutput {
				return "", false, errors.New("diagnostics arguments are invalid")
			}
			if i+1 >= len(args) {
				return "", false, errors.New("--output requires a path argument")
			}
			seenOutput = true
			i++
			output = args[i]
		case strings.HasPrefix(args[i], "--output="):
			if seenOutput {
				return "", false, errors.New("diagnostics arguments are invalid")
			}
			output = strings.TrimPrefix(args[i], "--output=")
			seenOutput = true
		default:
			return "", false, errors.New("diagnostics arguments are invalid")
		}
	}
	if strings.TrimSpace(output) == "" {
		return "", false, errors.New("diagnostics requires an explicit --output <path>")
	}
	return output, live, nil
}

func diagnosticsHelpRequested(args []string) bool {
	return len(args) == 1 && isHelpArg(args[0])
}

func printDoctorUsage() {
	fmt.Fprintln(os.Stdout, doctorUsageLine)
}

func printDiagnosticsUsage() {
	fmt.Fprintln(os.Stdout, diagnosticsUsageLine)
}

func isHelpArg(arg string) bool {
	return arg == "--help" || arg == "-h" || arg == "help"
}

func parseDoctorArgs(args []string) (doctorArgs, error) {
	parsed := doctorArgs{live: true}
	seenLive := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--offline":
			if parsed.offline || seenLive || parsed.bundle {
				return doctorArgs{}, errDoctorArgsInvalid
			}
			parsed.offline = true
			parsed.live = false
		case args[i] == "--live":
			if parsed.offline || seenLive {
				return doctorArgs{}, errDoctorArgsInvalid
			}
			seenLive = true
			parsed.live = true
			if parsed.bundle {
				parsed.bundleLive = true
			}
		case args[i] == "--bundle":
			if parsed.offline || parsed.bundle {
				return doctorArgs{}, errDoctorArgsInvalid
			}
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return doctorArgs{}, errDoctorArgsInvalid
			}
			i++
			parsed.bundle = true
			parsed.bundleOutput = args[i]
			if seenLive {
				parsed.bundleLive = true
			}
		case strings.HasPrefix(args[i], "--bundle="):
			if parsed.offline || parsed.bundle {
				return doctorArgs{}, errDoctorArgsInvalid
			}
			output := strings.TrimPrefix(args[i], "--bundle=")
			if strings.TrimSpace(output) == "" {
				return doctorArgs{}, errDoctorArgsInvalid
			}
			parsed.bundle = true
			parsed.bundleOutput = output
			if seenLive {
				parsed.bundleLive = true
			}
		default:
			return doctorArgs{}, errDoctorArgsInvalid
		}
	}
	if parsed.bundle && seenLive {
		parsed.bundleLive = true
	}
	return parsed, nil
}

func doctorHelpRequested(args []string) bool {
	return len(args) == 1 && isHelpArg(args[0])
}

// configBounds is the presence/bounds-only subset of sidecar.Config this
// command surfaces: never the configured API URL, proxy URL, or CA bundle
// path -- only whether each optional setting is present, plus numeric and
// boolean bounds that carry no secret or location information.
func loadConfigBounds() *diagnostics.ConfigBounds {
	cfg, err := sidecar.LoadConfig()
	if err != nil {
		return nil
	}
	return &diagnostics.ConfigBounds{
		TimeoutSeconds:        cfg.Timeout.Seconds(),
		MaxResponseBytes:      cfg.MaxResponseBytes,
		MaxRequestBodyBytes:   cfg.MaxRequestBodyBytes,
		AllowInsecureLoopback: cfg.AllowInsecureLoopback,
		ProxyConfigured:       cfg.ProxyURL != nil,
		CABundleConfigured:    strings.TrimSpace(cfg.CACertPath) != "",
	}
}

// toDiagnosticsInput narrows a doctorReport (and, when present, its
// LiveCheck) down to exactly the fields diagnostics.Input can carry. This
// explicit field-by-field construction -- rather than passing doctorReport
// through directly -- is deliberate: a future field added to doctorReport
// does not reach the bundle unless diagnostics.Input grows a matching
// field too, reviewed on its own merits.
func toDiagnosticsInput(report doctorReport) diagnostics.Input {
	checks := make([]diagnostics.CheckResult, len(report.Checks))
	for i, c := range report.Checks {
		checks[i] = diagnostics.CheckResult{Name: c.Name, Status: c.Status, Detail: c.Detail}
	}
	input := diagnostics.Input{
		Identity: diagnostics.Identity{
			Service:   "dev-health-acr-mcp",
			Version:   report.Version,
			Commit:    report.Commit,
			BuildDate: report.BuildDate,
			GOOS:      runtime.GOOS,
			GOARCH:    runtime.GOARCH,
		},
		Static: diagnostics.StaticReport{
			APIURLSet:                report.APIURLSet,
			APIURLValid:              report.APIURLValid,
			CredentialSet:            report.CredentialSet,
			CredentialSource:         report.CredentialSource,
			CredentialShapeValid:     report.CredentialShapeValid,
			WriteEnabled:             report.WriteEnabled,
			TranscriptCaptureEnabled: report.TranscriptCaptureEnabled,
			LogLevel:                 report.LogLevel,
			Status:                   report.Status,
			Bounds:                   loadConfigBounds(),
			Checks:                   checks,
		},
	}
	if report.LiveCheck != nil {
		input.Live = &diagnostics.LiveReport{
			Reachable:                report.LiveCheck.Reachable,
			Detail:                   report.LiveCheck.Detail,
			AgentContextRuntime:      report.LiveCheck.AgentContextRuntime,
			ContextReadScope:         report.LiveCheck.ContextReadScope,
			EvidenceReadScope:        report.LiveCheck.EvidenceReadScope,
			EpisodeWriteScope:        report.LiveCheck.EpisodeWriteScope,
			RecordEpisodeActive:      report.LiveCheck.RecordEpisodeActive,
			TranscriptCaptureEnabled: report.LiveCheck.TranscriptCaptureEnabled,
			EnabledTools:             append([]string(nil), report.LiveCheck.EnabledTools...),
		}
	}
	return input
}

// runDiagnosticsBundle builds and writes a diagnostic bundle to outputPath,
// optionally including a live hosted-API check, and returns a process exit
// code. Every error message here is either a fixed sentence or a value
// this package's own contract already guarantees is safe to print (the
// caller-supplied outputPath itself is not a secret).
func runDiagnosticsBundle(outputPath string, live bool) int {
	data, err := buildDiagnosticsBundle(live)
	if err != nil {
		fmt.Fprintln(os.Stderr, "diagnostics: failed to build bundle:", err)
		return 1
	}
	if err := diagnostics.WriteBundle(outputPath, data); err != nil {
		fmt.Fprintln(os.Stderr, "diagnostics: failed to write bundle:", err)
		return 1
	}
	fmt.Printf("diagnostics bundle written to %s\n", outputPath)
	return 0
}

// buildDiagnosticsBundle runs the static (and, when live is true, live)
// doctor checks and renders them into a built archive's bytes, without
// writing anything to disk. It is split out from runDiagnosticsBundle so
// tests can inspect the exact bytes a real invocation would write --
// including the canary tests that scan every byte for a leaked secret --
// without needing a filesystem destination.
func buildDiagnosticsBundle(live bool) ([]byte, error) {
	var report doctorReport
	if live {
		report = runDoctorLive()
	} else {
		report = runDoctor()
	}
	return diagnostics.Build(toDiagnosticsInput(report), time.Now())
}
