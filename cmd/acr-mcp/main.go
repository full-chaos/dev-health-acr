package main

import (
	"encoding/json"
	"fmt"
	"os"
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
	Service       string       `json:"service"`
	Version       string       `json:"version"`
	APIURLSet     bool         `json:"api_url_set"`
	CredentialSet bool         `json:"credential_set"`
	WriteEnabled  bool         `json:"write_enabled"`
	Checks        []diagnostic `json:"checks"`
	Status        string       `json:"status"`
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
		printJSON(runDoctor())
	case "metadata":
		printJSON(metadata{
			Service:       "dev-health-acr-mcp",
			Version:       version,
			Transport:     "stdio",
			EnabledTools:  []string{"context_for_task", "source_evidence"},
			DisabledTools: []string{"record_episode"},
			Status:        "contract-bootstrap",
		})
	case "serve":
		fmt.Fprintln(os.Stderr, "MCP transport is intentionally not wired in the contract bootstrap; implement under CHAOS-2908")
		os.Exit(3)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q; use version, doctor, metadata, or serve\n", command)
		os.Exit(2)
	}
}

func runDoctor() doctorReport {
	apiURLSet := os.Getenv("ACR_API_URL") != ""
	credentialSet := os.Getenv("ACR_API_TOKEN") != ""
	writeEnabled := os.Getenv("ACR_ENABLE_WRITEBACK") == "true"
	checks := []diagnostic{
		{Name: "binary", Status: "ok", Detail: "acr-mcp is executable"},
		{Name: "transport", Status: "ok", Detail: "STDIO is the SVS MCP transport"},
	}
	if apiURLSet {
		checks = append(checks, diagnostic{Name: "api_url", Status: "ok", Detail: "ACR_API_URL is configured"})
	} else {
		checks = append(checks, diagnostic{Name: "api_url", Status: "warning", Detail: "ACR_API_URL is not configured"})
	}
	if credentialSet {
		checks = append(checks, diagnostic{Name: "credential", Status: "ok", Detail: "ACR_API_TOKEN is configured and redacted"})
	} else {
		checks = append(checks, diagnostic{Name: "credential", Status: "warning", Detail: "ACR_API_TOKEN is not configured"})
	}
	status := "ok"
	if !apiURLSet || !credentialSet {
		status = "incomplete_configuration"
	}
	return doctorReport{
		Service:       "dev-health-acr-mcp",
		Version:       version,
		APIURLSet:     apiURLSet,
		CredentialSet: credentialSet,
		WriteEnabled:  writeEnabled,
		Checks:        checks,
		Status:        status,
	}
}

func printJSON(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
