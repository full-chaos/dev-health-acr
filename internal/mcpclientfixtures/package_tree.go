package mcpclientfixtures

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var requiredClientNamespaces = map[string]struct{}{
	"opencode": {}, "claude-code": {}, "codex": {}, "cursor": {},
}

type clientPackageManifest struct {
	BundleVersion         string   `json:"bundle_version"`
	MinimumSidecarVersion string   `json:"minimum_sidecar_version"`
	Command               string   `json:"command"`
	Args                  []string `json:"args"`
	MCPCommands           []string `json:"mcp_commands"`
}

func validateClientPackageTree(root string, bundle ClientBundle) error {
	clientsRoot := filepath.Join(root, "clients")
	entries, err := os.ReadDir(clientsRoot)
	if err != nil {
		return invalidBundle("namespace.missing")
	}
	for _, entry := range entries {
		if entry.Name() == "conformance" {
			info, err := os.Lstat(filepath.Join(clientsRoot, entry.Name()))
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return invalidBundle("namespace.symlink")
			}
			continue
		}
		if _, ok := requiredClientNamespaces[entry.Name()]; !ok {
			return invalidBundle("namespace.out_of_namespace")
		}
		info, err := os.Lstat(filepath.Join(clientsRoot, entry.Name()))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return invalidBundle("package.symlink")
		}
	}
	for _, client := range bundle.SupportedClients {
		packagePath := filepath.Join(clientsRoot, client)
		info, err := os.Lstat(packagePath)
		if err != nil || !info.IsDir() {
			return invalidBundle("package.missing")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return invalidBundle("package.symlink")
		}
		if err := validateClientPackage(packagePath, bundle); err != nil {
			return err
		}
	}
	return nil
}

func validateClientPackage(packagePath string, bundle ClientBundle) error {
	manifest, err := loadClientPackageManifest(filepath.Join(packagePath, "package.v1.json"))
	if err != nil {
		return err
	}
	if manifest.BundleVersion != bundle.BundleVersion || manifest.MinimumSidecarVersion != bundle.MinimumSidecarVersion {
		return invalidBundle("package.version")
	}
	if manifest.Command != "acr-mcp" {
		return invalidBundle("package.command")
	}
	if len(manifest.Args) != 1 || manifest.Args[0] != "serve" {
		return invalidBundle("package.args")
	}
	if !equalStrings(manifest.MCPCommands, []string{"context_for_task", "source_evidence"}) {
		return invalidBundle("package.mcp_commands")
	}
	return filepath.WalkDir(packagePath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return invalidBundle("package.unreadable")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return invalidBundle("package.symlink")
		}
		if entry.IsDir() || filepath.Base(path) == "package.v1.json" {
			return nil
		}
		if filepath.Base(path) == "client-bundle.v1.json" {
			return invalidBundle("package.contract_fork")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return invalidBundle("package.unreadable")
		}
		relativePath, err := filepath.Rel(packagePath, path)
		if err != nil {
			return invalidBundle("package.unreadable")
		}
		return validateClientPackageContents(filepath.Base(packagePath), filepath.ToSlash(relativePath), string(raw))
	})
}

func loadClientPackageManifest(path string) (clientPackageManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return clientPackageManifest{}, invalidBundle("package.manifest_missing")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest clientPackageManifest
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return clientPackageManifest{}, invalidBundle("package.manifest_decode")
	}
	return manifest, nil
}

func validateClientPackageContents(clientName, relativePath, contents string) error {
	if clientName == "opencode" && relativePath == "config/opencode.json" {
		withoutSchema, err := withoutCanonicalOpenCodeSchema(contents)
		if err != nil {
			return invalidBundle("package.direct_api")
		}
		contents = withoutSchema
	}
	for _, forbidden := range []struct {
		needle string
		class  string
	}{
		{"http://", "package.direct_api"},
		{"https://", "package.direct_api"},
		{"ACR_API_TOKEN", "package.credentials"},
		{"credential", "package.credentials"},
		{"codegraph", "package.codegraph"},
		{"client-bundle.v1.json", "package.contract_fork"},
		{"record_episode", "package.mcp_commands"},
		{`"writeback_enabled_by_default":true`, "package.writeback_default"},
		{`"writeback_enabled_by_default": true`, "package.writeback_default"},
		{`writeback_enabled_by_default = true`, "package.writeback_default"},
		{`"preplan_enabled_by_default":true`, "package.preplan_default"},
		{`"preplan_enabled_by_default": true`, "package.preplan_default"},
		{`preplan_enabled_by_default = true`, "package.preplan_default"},
		{"curl ", "package.mutable_installer"},
		{"/latest", "package.mutable_installer"},
		{"/main", "package.mutable_installer"},
	} {
		if strings.Contains(strings.ToLower(contents), strings.ToLower(forbidden.needle)) {
			return invalidBundle(forbidden.class)
		}
	}
	return nil
}

func withoutCanonicalOpenCodeSchema(contents string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(contents))
	var config map[string]json.RawMessage
	if err := decoder.Decode(&config); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return "", invalidBundle("package.direct_api")
	}
	var schema string
	if rawSchema, ok := config["$schema"]; !ok || json.Unmarshal(rawSchema, &schema) != nil || schema != "https://opencode.ai/config.json" {
		return "", invalidBundle("package.direct_api")
	}
	delete(config, "$schema")
	remaining, err := json.Marshal(config)
	if err != nil {
		return "", invalidBundle("package.direct_api")
	}
	return string(remaining), nil
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
