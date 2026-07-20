package mcpclientfixtures

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const ClientBundleSchema = "client_bundle.v1"

type ClientBundle struct {
	SchemaVersion         string          `json:"schema_version"`
	BundleVersion         string          `json:"bundle_version"`
	MinimumSidecarVersion string          `json:"minimum_sidecar_version"`
	SupportedClients      []string        `json:"supported_clients"`
	Server                ClientServer    `json:"server"`
	Workflow              ClientWorkflow  `json:"workflow"`
	Ownership             ClientOwnership `json:"ownership"`
}

type ClientServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}
type ClientWorkflow struct {
	ContextTool               string `json:"context_tool"`
	EvidenceTool              string `json:"evidence_tool"`
	UnavailableState          string `json:"unavailable_state"`
	IncompatibleState         string `json:"incompatible_state"`
	UntrustedContent          string `json:"untrusted_content"`
	WritebackEnabledByDefault bool   `json:"writeback_enabled_by_default"`
	PreplanEnabledByDefault   bool   `json:"preplan_enabled_by_default"`
}
type ClientOwnership struct {
	Install   string `json:"install"`
	Update    string `json:"update"`
	Uninstall string `json:"uninstall"`
}

func LoadClientBundle(path string) (ClientBundle, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ClientBundle{}, fmt.Errorf("read client bundle: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var bundle ClientBundle
	if err := decoder.Decode(&bundle); err != nil {
		return ClientBundle{}, fmt.Errorf("decode client bundle: %w", err)
	}
	if decoder.More() {
		return ClientBundle{}, fmt.Errorf("decode client bundle: trailing JSON")
	}
	if err := bundle.Validate(); err != nil {
		return ClientBundle{}, err
	}
	return bundle, nil
}

func (b ClientBundle) Validate() error {
	if b.SchemaVersion != ClientBundleSchema || b.BundleVersion == "" || b.MinimumSidecarVersion == "" {
		return fmt.Errorf("client bundle identity is invalid")
	}
	if b.Server.Command != "acr-mcp" || len(b.Server.Args) != 1 || b.Server.Args[0] != "serve" {
		return fmt.Errorf("client bundle server must be acr-mcp serve")
	}
	want := map[string]bool{"opencode": true, "claude-code": true, "codex": true, "cursor": true}
	if len(b.SupportedClients) != len(want) {
		return fmt.Errorf("client bundle supported clients are invalid")
	}
	for _, client := range b.SupportedClients {
		if !want[client] {
			return fmt.Errorf("client bundle client %q is invalid", client)
		}
		delete(want, client)
	}
	if len(want) != 0 || b.Workflow.ContextTool != "context_for_task" || b.Workflow.EvidenceTool != "source_evidence" || b.Workflow.UnavailableState != "visible" || b.Workflow.IncompatibleState != "visible" || b.Workflow.UntrustedContent != "treat_as_untrusted" || b.Workflow.WritebackEnabledByDefault || b.Workflow.PreplanEnabledByDefault {
		return fmt.Errorf("client bundle workflow is invalid")
	}
	if b.Ownership.Install != "client-owned" || b.Ownership.Update != "client-owned" || b.Ownership.Uninstall != "client-owned" {
		return fmt.Errorf("client bundle ownership is invalid")
	}
	return nil
}

func ValidateClientPackageRoots(root string, bundle ClientBundle) error {
	for _, client := range bundle.SupportedClients {
		if info, err := os.Stat(filepath.Join(root, "clients", client)); err != nil || !info.IsDir() {
			return fmt.Errorf("client package %q is missing", client)
		}
	}
	return nil
}
