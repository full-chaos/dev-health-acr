package main

import (
	"fmt"
	"os"

	versioninfo "github.com/full-chaos/dev-health-acr/internal/version"
)

const rootUsageLine = "Usage: acr-mcp [version|doctor|diagnostics|metadata|serve]"

func runCLI(args []string) int {
	if len(args) == 0 {
		printJSON(currentMetadata())
		return 0
	}

	command := args[0]
	commandArgs := args[1:]
	switch command {
	case "help", "-h", "--help":
		if len(commandArgs) > 0 {
			return rejectRootArgs()
		}
		fmt.Fprintln(os.Stdout, rootUsageLine)
		return 0
	case "version", "--version", "-version":
		if len(commandArgs) > 0 {
			return rejectRootArgs()
		}
		info := versioninfo.Current()
		fmt.Printf("%s commit=%s built=%s\n", info.Version, info.Commit, info.Date)
		return 0
	case "doctor":
		return runDoctorCommand(commandArgs)
	case "diagnostics":
		return runDiagnosticsCommand(commandArgs)
	case "metadata":
		if len(commandArgs) > 0 {
			return rejectRootArgs()
		}
		printJSON(currentMetadata())
		return 0
	case "serve":
		if len(commandArgs) > 0 {
			return rejectRootArgs()
		}
		return runServe()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q; use version, doctor, diagnostics, metadata, or serve\n", command)
		return 2
	}
}

func runDoctorCommand(args []string) int {
	if doctorHelpRequested(args) {
		printDoctorUsage()
		return 0
	}
	parsed, err := parseDoctorArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "doctor: invalid arguments")
		printDoctorUsage()
		return 2
	}
	if parsed.bundle {
		return runDiagnosticsBundle(parsed.bundleOutput, parsed.bundleLive)
	}
	if parsed.offline {
		printJSON(runDoctor())
		return 0
	}
	if parsed.live {
		printJSON(runDoctorLive())
		return 0
	}
	printJSON(runDoctor())
	return 0
}

func runDiagnosticsCommand(args []string) int {
	if diagnosticsHelpRequested(args) {
		printDiagnosticsUsage()
		return 0
	}
	outputPath, live, err := parseDiagnosticsArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "diagnostics: invalid arguments")
		printDiagnosticsUsage()
		return 2
	}
	return runDiagnosticsBundle(outputPath, live)
}

func rejectRootArgs() int {
	fmt.Fprintln(os.Stderr, rootUsageLine)
	return 2
}
