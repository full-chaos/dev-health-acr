package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestDoctorLocalIndexClassifiesTypedCapabilityFailures(t *testing.T) {
	for _, test := range []struct {
		name, status, wantCode       string
		indexChecked, versionChecked bool
	}{
		{"missing", `{"initialized":false,"version":"1.2.0"}`, "local_index_missing", true, true},
		{"incompatible", typedStatus("2.0.0"), "local_index_incompatible_version", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, info := typedDoctorFixture(t, test.status, "[]")
			withDoctorLocalProbe(t, config, info, sidecar.NewWorkspaceLocalIndexProvider(config, mustDoctorSnapshot(t, info)))
			report := probeLocalIndex()
			if report.ErrorCode != test.wantCode || report.Available || report.QueryChecked || report.IndexReadable || report.IndexChecked != test.indexChecked || report.VersionChecked != test.versionChecked {
				t.Fatalf("unexpected capability report: %#v", report)
			}
		})
	}
}

func TestDoctorLocalIndexClassifiesTypedQueryTimeout(t *testing.T) {
	config, info := typedDoctorFixture(t, typedStatus("1.2.0"), "sleep 2")
	config.Timeout = time.Second
	withDoctorLocalProbe(t, config, info, sidecar.NewWorkspaceLocalIndexProvider(config, mustDoctorSnapshot(t, info)))
	report := probeLocalIndex()
	if report.ErrorCode != "local_index_timeout" || report.Available || !report.QueryChecked || report.QuerySucceeded || !report.IndexReadable || !report.VersionChecked || !report.VersionCompatible || report.ProviderVersion != "1.2.0" {
		t.Fatalf("unexpected query timeout report: %#v", report)
	}
}

func typedDoctorFixture(t *testing.T, status, query string) (sidecar.LocalIndexConfig, sidecar.WorkspaceInfo) {
	t.Helper()
	root := t.TempDir()
	runDoctorGit(t, root, "init", "-q")
	runDoctorGit(t, root, "config", "user.email", "doctor@example.test")
	runDoctorGit(t, root, "config", "user.name", "doctor")
	if err := os.WriteFile(filepath.Join(root, "repo.go"), []byte("package repo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDoctorGit(t, root, "add", "repo.go")
	runDoctorGit(t, root, "commit", "-qm", "init")
	runDoctorGit(t, root, "remote", "add", "origin", "https://github.com/full-chaos/acr.git")
	if err := os.Mkdir(filepath.Join(root, ".codegraph"), 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "codegraph")
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	body := "#!/usr/bin/env bash\nset -eu\ncase \"$1\" in\nstatus) cat <<'JSON'\n" + strings.ReplaceAll(status, "ROOT", canonicalRoot) + "\nJSON\n;;\nquery) " + query + ";;\nesac\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return sidecar.LocalIndexConfig{Provider: sidecar.LocalIndexProviderCodeGraph, Executable: script, Timeout: time.Second, MaxItems: 5, MaxOutputTokens: 1000, MaxSerializedBytes: 65536, StalePolicy: sidecar.LocalIndexStaleGraceful}, validDoctorWorkspace(root)
}

func typedStatus(version string) string {
	return `{"initialized":true,"version":"` + version + `","projectPath":"ROOT","indexPath":"ROOT/.codegraph","lastIndexed":"2026-07-19T12:34:19Z","fileCount":1,"nodeCount":1,"edgeCount":1,"dbSizeBytes":1,"backend":"node-sqlite","journalMode":"wal","nodesByKind":{},"languages":["go"],"pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null,"index":{"builtWithVersion":"` + version + `","builtWithExtractionVersion":24,"currentExtractionVersion":24,"reindexRecommended":false}}`
}

func mustDoctorSnapshot(t *testing.T, info sidecar.WorkspaceInfo) sidecar.LocalWorkspaceSnapshot {
	t.Helper()
	snapshot, err := sidecar.NewLocalWorkspaceSnapshot(info, info.Remote.Slug(), false)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func runDoctorGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}
