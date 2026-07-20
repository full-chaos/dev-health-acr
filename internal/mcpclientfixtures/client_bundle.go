package mcpclientfixtures

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
	Name           string               `json:"name"`
	Input          ClientScenarioInput  `json:"input"`
	ExpectedOutput ClientScenarioOutput `json:"expected_output"`
}
type ClientScenarioInput struct {
	Tool  string `json:"tool"`
	State string `json:"state"`
}
type ClientScenarioOutput struct {
	Kind    string `json:"kind"`
	Visible bool   `json:"visible"`
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
		return ClientBundle{}, invalidBundle(bundleDecodeClassification(raw))
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ClientBundle{}, invalidBundle("decode.trailing")
	}
	if err := bundle.Validate(); err != nil {
		return ClientBundle{}, err
	}
	return bundle, nil
}

func bundleDecodeClassification(raw []byte) string {
	for _, classifiedField := range []struct {
		field string
		class string
	}{
		{`"credential"`, "bundle.credentials"},
		{`"codegraph_command"`, "bundle.codegraph"},
		{`"client_specific_contract"`, "bundle.contract_fork"},
		{`"installer"`, "bundle.mutable_installer"},
		{`"package_path"`, "namespace.out_of_namespace"},
	} {
		if bytes.Contains(raw, []byte(classifiedField.field)) {
			return classifiedField.class
		}
	}
	return "decode"
}

func (b ClientBundle) Validate() error {
	if b.SchemaVersion != ClientBundleSchema {
		return invalidBundle("identity.schema")
	}
	if !semVerPattern.MatchString(b.BundleVersion) || !semVerPattern.MatchString(b.MinimumSidecarVersion) {
		return invalidBundle("identity.semver")
	}
	if b.Server.Command != "acr-mcp" {
		return invalidBundle("server.command")
	}
	if len(b.Server.Args) != 1 || b.Server.Args[0] != "serve" {
		return invalidBundle("server.args")
	}
	want := map[string]bool{"opencode": true, "claude-code": true, "codex": true, "cursor": true}
	if len(b.SupportedClients) != len(want) {
		return invalidBundle("supported_clients.missing")
	}
	for _, client := range b.SupportedClients {
		if !want[client] {
			return invalidBundle("supported_clients.missing")
		}
		delete(want, client)
	}
	if len(want) != 0 {
		return invalidBundle("supported_clients.missing")
	}
	if b.Workflow.ContextTool != "context_for_task" || b.Workflow.EvidenceTool != "source_evidence" {
		return invalidBundle("workflow.tools")
	}
	if b.Workflow.UnavailableState != "visible" || b.Workflow.IncompatibleState != "visible" || b.Workflow.UntrustedContent != "treat_as_untrusted" {
		return invalidBundle("workflow.safety")
	}
	if b.Workflow.WritebackEnabledByDefault {
		return invalidBundle("workflow.writeback_default")
	}
	if b.Workflow.PreplanEnabledByDefault {
		return invalidBundle("workflow.preplan_default")
	}
	if b.Ownership.Install != "client-owned" || b.Ownership.Update != "client-owned" || b.Ownership.Uninstall != "client-owned" {
		return invalidBundle("ownership")
	}
	if len(b.Scenarios) != len(clientScenarioExpectations) {
		return invalidBundle("scenarios")
	}
	seen := make(map[string]struct{}, len(b.Scenarios))
	for _, scenario := range b.Scenarios {
		if _, duplicate := seen[scenario.Name]; duplicate {
			return invalidBundle("scenarios")
		}
		seen[scenario.Name] = struct{}{}
		if expected, ok := clientScenarioExpectations[scenario.Name]; !ok || expected != scenario {
			return invalidBundle("scenarios")
		}
	}
	return nil
}

var clientScenarioExpectations = map[string]ClientScenario{
	"context":     {Name: "context", Input: ClientScenarioInput{Tool: "context_for_task", State: "available"}, ExpectedOutput: ClientScenarioOutput{Kind: "structured_context", Visible: true}},
	"evidence":    {Name: "evidence", Input: ClientScenarioInput{Tool: "source_evidence", State: "available"}, ExpectedOutput: ClientScenarioOutput{Kind: "structured_evidence", Visible: true}},
	"unavailable": {Name: "unavailable", Input: ClientScenarioInput{Tool: "context_for_task", State: "unavailable"}, ExpectedOutput: ClientScenarioOutput{Kind: "visible_degradation", Visible: true}},
}

func (b ClientBundle) ConformanceExpectations() map[string]ClientScenarioOutput {
	expectations := make(map[string]ClientScenarioOutput, len(b.Scenarios))
	for _, scenario := range b.Scenarios {
		expectations[scenario.Name] = scenario.ExpectedOutput
	}
	return expectations
}

func ValidateClientPackageRoots(root string, bundle ClientBundle) error {
	return validateClientPackageTree(root, bundle)
}
