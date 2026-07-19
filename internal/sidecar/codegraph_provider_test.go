package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const codeGraphCommandsPerTask = 8

func TestCodeGraphProvider_ContextForTask_returnsDeterministicBoundedFixtureEvidence(t *testing.T) {
	// Given
	provider, workspace, commandLog := newFixtureCodeGraphProvider(t)
	request := LocalContextRequest{
		TaskID:              "CHAOS-3007",
		Task:                "consume the local index safely",
		RequestedCategories: []string{"maintenance"},
		MaxItems:            5,
		MaxOutputTokens:     1000,
		Workspace:           &workspace,
	}
	// When
	first, err := provider.ContextForTask(context.Background(), request)
	second, repeatedErr := provider.ContextForTask(context.Background(), request)

	// Then
	require.NoError(t, err)
	require.NoError(t, repeatedErr)
	require.Equal(t, first, second)
	require.Equal(t, "codegraph", first.ProviderID)
	require.Equal(t, "1.2.0", first.ProviderVersion)
	require.Equal(t, codeGraphJSONQueryVersion, first.QueryVersion)
	require.NotEmpty(t, first.Evidence)
	require.LessOrEqual(t, len(first.Evidence), request.MaxItems)
	require.NotContains(t, strings.Join(localEvidenceLocators(first.Evidence), "\n"), workspace.Root)

	resolved, resolveErr := provider.ResolveEvidence(context.Background(), first.Evidence[0].Locator)
	require.NoError(t, resolveErr)
	require.Equal(t, first.Evidence[0], resolved)
	_, missingErr := provider.ResolveEvidence(context.Background(), "missing")
	require.ErrorIs(t, missingErr, ErrLocalEvidenceNotFound)

	commands, readErr := os.ReadFile(commandLog)
	require.NoError(t, readErr)
	lines := strings.Split(strings.TrimSpace(string(commands)), "\n")
	require.LessOrEqual(t, len(lines)/2, codeGraphCommandsPerTask)
	require.Equal(t, []string{
		"status --json",
		"query --json consume the local index safely maintenance --limit 5",
		"affected --json acr/internal/contextpacket/assembler.go --depth 2",
		"callers --json Assemble --limit 5",
		"callees --json Assemble --limit 5",
		"impact --json Assemble --depth 2",
		"status --json",
		"query --json consume the local index safely maintenance --limit 5",
		"affected --json acr/internal/contextpacket/assembler.go --depth 2",
		"callers --json Assemble --limit 5",
		"callees --json Assemble --limit 5",
		"impact --json Assemble --depth 2",
	}, lines)
	for _, line := range lines {
		require.NotContains(t, line, "explore")
		require.NotContains(t, line, "sqlite")
	}
}

func newFixtureCodeGraphProvider(t *testing.T) (*CodeGraphLocalIndexProvider, LocalWorkspace, string) {
	t.Helper()
	root := t.TempDir()
	canonicalRoot, err := canonicalCodeGraphRoot(root)
	require.NoError(t, err)
	fixtureDir := filepath.Join(root, "fixtures")
	require.NoError(t, os.Mkdir(fixtureDir, 0o700))
	for _, name := range []string{"status", "query", "callers", "callees", "impact", "affected", "files"} {
		fixture := readCodeGraphFixture(t, name)
		if name == "status" {
			fixture = strings.ReplaceAll(fixture, "<local-only:absolute-project-path>", canonicalRoot)
			fixture = strings.ReplaceAll(fixture, "<local-only:absolute-index-path>", filepath.Join(canonicalRoot, ".codegraph", "codegraph.db"))
		}
		require.NoError(t, os.WriteFile(filepath.Join(fixtureDir, name+".json"), []byte(fixture), 0o600))
	}
	commandLog := filepath.Join(root, "commands.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(commandLog) + "\nfixture_dir=${0%/*}/fixtures\ncase \"$1\" in status|query|callers|callees|impact|affected|files) /bin/cat \"$fixture_dir/$1.json\" ;; *) exit 1 ;; esac\n"
	executable := filepath.Join(root, "codegraph")
	require.NoError(t, os.WriteFile(executable, []byte(script), 0o700))
	runner := CodeGraphRunner{
		Config:            LocalIndexConfig{Executable: executable, Timeout: time.Second, MaxItems: 5, MaxOutputTokens: 1000},
		resolveExecutable: func(string) (string, error) { return executable, nil },
	}
	workspace := LocalWorkspace{
		RepositorySlug: "full-chaos/dev-health-acr",
		Root:           canonicalRoot,
		Branch:         "feat/chaos-3007",
		CommitSHA:      "0123456789abcdef0123456789abcdef01234567",
		TargetFiles:    []string{"acr/internal/contextpacket/assembler.go"},
	}
	return NewCodeGraphLocalIndexProvider(runner, workspace), workspace, commandLog
}

func readCodeGraphFixture(t *testing.T, name string) string {
	t.Helper()
	_, sourceFile, _, found := runtime.Caller(0)
	require.True(t, found)
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), "..", "..", "testdata", "codegraph", "v1.2.0", name+".json"))
	require.NoError(t, err)
	return string(contents)
}

func shellQuote(value string) string {
	return strconv.Quote(value)
}

func localEvidenceLocators(evidence []LocalExpandedEvidence) []string {
	locators := make([]string, len(evidence))
	for index := range evidence {
		locators[index] = evidence[index].Locator
	}
	return locators
}

func TestCodeGraphProvider_ResolveEvidence_returnsNotFoundForMalformedLocator(t *testing.T) {
	// Given
	provider, _, _ := newFixtureCodeGraphProvider(t)

	// When
	_, err := provider.ResolveEvidence(context.Background(), "../unsafe")

	// Then
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrLocalEvidenceNotFound))
}
