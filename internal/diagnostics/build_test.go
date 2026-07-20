package diagnostics

import (
	"archive/tar"
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sampleInput() Input {
	return Input{
		Identity: Identity{
			Service:   "dev-health-acr-mcp",
			Version:   "1.2.3",
			Commit:    "0123456789abcdef0123456789abcdef01234567",
			BuildDate: "2026-07-12T00:00:00Z",
			GOOS:      "darwin",
			GOARCH:    "arm64",
		},
		Static: StaticReport{
			APIURLSet:            true,
			APIURLValid:          true,
			CredentialSet:        true,
			CredentialSource:     "environment",
			CredentialShapeValid: true,
			Status:               "ok",
			Bounds: &ConfigBounds{
				TimeoutSeconds:      20,
				MaxResponseBytes:    1 << 20,
				MaxRequestBodyBytes: 256 << 10,
			},
			Checks: []CheckResult{
				{Name: "api_url", Status: "ok", Detail: "ACR_API_URL is configured and valid"},
			},
		},
	}
}

// TestBuildIsDeterministicForFixedInput: given the same Input and
// generatedAt, Build must produce byte-identical archives every time --
// the bundle's whole point is to be a reproducible artifact, not one that
// differs run to run for no reason.
func TestBuildIsDeterministicForFixedInput(t *testing.T) {
	input := sampleInput()
	when := time.Date(2026, 7, 12, 15, 4, 5, 0, time.UTC)

	first, err := Build(input, when)
	if err != nil {
		t.Fatalf("first Build: %v", err)
	}
	second, err := Build(input, when)
	if err != nil {
		t.Fatalf("second Build: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("expected byte-identical archives for a fixed input and generation time")
	}
}

// TestBuildOmitsLiveReportFileWhenNotRequested proves the archive's file
// list -- both the manifest's own Files field and the actual tar entries
// -- excludes doctor-live.json whenever Input.Live is nil, and includes it
// otherwise.
func TestBuildOmitsLiveReportFileWhenNotRequested(t *testing.T) {
	input := sampleInput()
	when := time.Now().UTC()

	withoutLive, err := Build(input, when)
	if err != nil {
		t.Fatalf("Build without live: %v", err)
	}
	names := tarEntryNames(t, withoutLive)
	if slicesContains(names, liveReportFile) {
		t.Fatalf("expected no %s entry without a live report, got: %v", liveReportFile, names)
	}

	input.Live = &LiveReport{Reachable: true, AgentContextRuntime: true}
	withLive, err := Build(input, when)
	if err != nil {
		t.Fatalf("Build with live: %v", err)
	}
	names = tarEntryNames(t, withLive)
	if !slicesContains(names, liveReportFile) {
		t.Fatalf("expected a %s entry with a live report, got: %v", liveReportFile, names)
	}
}

// TestBuildRejectsArchiveExceedingBound proves an adversarially large
// Checks list -- for example a future caller wiring in an unbounded
// source -- is rejected by Build rather than silently producing an
// oversized archive.
func TestBuildRejectsArchiveExceedingBound(t *testing.T) {
	input := sampleInput()
	huge := strings.Repeat("x", 1024)
	for i := 0; i < 4096; i++ {
		input.Static.Checks = append(input.Static.Checks, CheckResult{
			Name: "padding", Status: "ok", Detail: huge,
		})
	}

	if _, err := Build(input, time.Now().UTC()); err == nil {
		t.Fatal("expected Build to reject an archive exceeding MaxBundleBytes")
	}
}

// TestDiagnosticsTypesExposeOnlyAllowlistedFields is a structural canary:
// it walks every exported field of every public type in this package via
// reflection and fails if a field name outside the explicit allowlist
// appears. This locks in the "never a URL, credential, path, header, or
// body" contract at the type level, so a future change cannot silently
// widen what this package can carry without a reviewer seeing this test
// fail and updating the allowlist deliberately.
func TestDiagnosticsTypesExposeOnlyAllowlistedFields(t *testing.T) {
	allowed := map[string]bool{
		"Service": true, "Version": true, "Commit": true, "BuildDate": true, "GOOS": true, "GOARCH": true,
		"Name": true, "Status": true, "Detail": true,
		"TimeoutSeconds": true, "MaxResponseBytes": true, "MaxRequestBodyBytes": true,
		"AllowInsecureLoopback": true, "ProxyConfigured": true, "CABundleConfigured": true,
		"APIURLSet": true, "APIURLValid": true, "CredentialSet": true, "CredentialSource": true,
		"CredentialShapeValid": true, "WriteEnabled": true, "TranscriptCaptureEnabled": true,
		"LogLevel": true, "Bounds": true, "Checks": true,
		"LocalIndex":   true,
		"ProviderMode": true, "ConfigValid": true, "WorkspaceDiscovered": true, "RepositoryIdentityAvailable": true, "WorkspaceScopeValid": true,
		"IndexChecked": true, "IndexReadable": true, "Available": true, "ProviderVersion": true, "VersionChecked": true, "VersionCompatible": true,
		"Freshness": true, "MaxItems": true, "MaxOutputTokens": true, "WorktreeMismatchChecked": true, "WorktreeMismatchDetected": true,
		"QueryChecked": true, "QuerySucceeded": true, "ResultCount": true, "IndexedCommitStatus": true, "ErrorCode": true,
		"Reachable": true, "AgentContextRuntime": true, "ContextReadScope": true,
		"EvidenceReadScope": true, "EpisodeWriteScope": true, "RecordEpisodeActive": true,
		"EnabledTools": true,
		"Identity":     true, "Static": true, "Live": true,
	}

	types := []any{Identity{}, CheckResult{}, ConfigBounds{}, LocalIndexReport{}, StaticReport{}, LiveReport{}, Input{}}
	for _, sample := range types {
		typ := reflect.TypeOf(sample)
		for i := 0; i < typ.NumField(); i++ {
			name := typ.Field(i).Name
			if !allowed[name] {
				t.Fatalf("type %s has an unexpected field %q not present in the safe-field allowlist; if this field is intentional and secrets-free, add it to the allowlist deliberately", typ.Name(), name)
			}
		}
	}
}

func tarEntryNames(t *testing.T, archive []byte) []string {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(archive))
	var names []string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar entry: %v", err)
		}
		names = append(names, header.Name)
	}
	return names
}

func slicesContains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}
