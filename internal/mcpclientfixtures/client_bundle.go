package mcpclientfixtures

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const ClientBundleSchema = "client_bundle.v1"

var ErrInvalidClientBundle = errors.New("mcpclientfixtures: invalid client bundle")
var semVerPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type ClientBundleError struct{ Field string }

func (e *ClientBundleError) Error() string        { return "invalid client bundle field: " + e.Field }
func (e *ClientBundleError) Is(target error) bool { return target == ErrInvalidClientBundle }
func invalidBundle(field string) error            { return &ClientBundleError{Field: field} }

type ClientBundle struct {
	SchemaVersion         string           `json:"schema_version"`
	BundleVersion         string           `json:"bundle_version"`
	MinimumSidecarVersion string           `json:"minimum_sidecar_version"`
	SupportedClients      []string         `json:"supported_clients"`
	Server                ClientServer     `json:"server"`
	Workflow              ClientWorkflow   `json:"workflow"`
	Ownership             ClientOwnership  `json:"ownership"`
	Scenarios             []ClientScenario `json:"scenarios"`
}
type ClientScenario struct {
	Name           string `json:"name"`
	Input          string `json:"input"`
	ExpectedOutput string `json:"expected_output"`
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
		return ClientBundle{}, invalidBundle("decode")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ClientBundle{}, invalidBundle("decode")
	}
	if err := bundle.Validate(); err != nil {
		return ClientBundle{}, err
	}
	return bundle, nil
}

func (b ClientBundle) Validate() error {
	if b.SchemaVersion != ClientBundleSchema || !semVerPattern.MatchString(b.BundleVersion) || !semVerPattern.MatchString(b.MinimumSidecarVersion) {
		return invalidBundle("identity")
	}
	if b.Server.Command != "acr-mcp" || len(b.Server.Args) != 1 || b.Server.Args[0] != "serve" {
		return invalidBundle("server")
	}
	want := map[string]bool{"opencode": true, "claude-code": true, "codex": true, "cursor": true}
	if len(b.SupportedClients) != len(want) {
		return invalidBundle("supported_clients")
	}
	for _, client := range b.SupportedClients {
		if !want[client] {
			return invalidBundle("supported_clients")
		}
		delete(want, client)
	}
	if len(want) != 0 || b.Workflow.ContextTool != "context_for_task" || b.Workflow.EvidenceTool != "source_evidence" || b.Workflow.UnavailableState != "visible" || b.Workflow.IncompatibleState != "visible" || b.Workflow.UntrustedContent != "treat_as_untrusted" || b.Workflow.WritebackEnabledByDefault || b.Workflow.PreplanEnabledByDefault {
		return invalidBundle("workflow")
	}
	if b.Ownership.Install != "client-owned" || b.Ownership.Update != "client-owned" || b.Ownership.Uninstall != "client-owned" {
		return invalidBundle("ownership")
	}
	if len(b.Scenarios) < 3 {
		return invalidBundle("scenarios")
	}
	for _, scenario := range b.Scenarios {
		if scenario.Name == "" || scenario.Input == "" || scenario.ExpectedOutput == "" {
			return invalidBundle("scenarios")
		}
	}
	return nil
}

func ValidateClientPackageRoots(root string, bundle ClientBundle) error {
	allowed := map[string]bool{"conformance": true, "opencode": true, "claude-code": true, "codex": true, "cursor": true}
	entries, err := os.ReadDir(filepath.Join(root, "clients"))
	if err != nil {
		return fmt.Errorf("read client roots: %w", err)
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return invalidBundle("clients namespace")
		}
	}
	for _, client := range bundle.SupportedClients {
		packagePath := filepath.Join(root, "clients", client)
		if info, err := os.Stat(packagePath); err != nil || !info.IsDir() {
			return invalidBundle("client package")
		}
		raw, err := os.ReadFile(filepath.Join(packagePath, "package.v1.json"))
		if err != nil {
			return invalidBundle("client package")
		}
		var manifest struct {
			BundleVersion         string   `json:"bundle_version"`
			MinimumSidecarVersion string   `json:"minimum_sidecar_version"`
			Command               string   `json:"command"`
			Args                  []string `json:"args"`
		}
		if err := json.Unmarshal(raw, &manifest); err != nil || manifest.BundleVersion != bundle.BundleVersion || manifest.MinimumSidecarVersion != bundle.MinimumSidecarVersion || manifest.Command != "acr-mcp" || len(manifest.Args) != 1 || manifest.Args[0] != "serve" {
			return invalidBundle("client package")
		}
	}
	return nil
}
