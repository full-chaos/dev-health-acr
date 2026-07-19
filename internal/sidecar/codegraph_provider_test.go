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

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/stretchr/testify/require"
)

const codeGraphCommandsPerTask = 8

func TestCodeGraphProvider_ContextForTask_returnsDeterministicBoundedFixtureEvidence(t *testing.T) {
	// Given
	provider, workspace, commandLog := newFixtureCodeGraphProvider(t)
	request := LocalContextRequest{
		TaskID:              "CHAOS-3007",
		Goal:                "consume the local index safely",
		RequestedCategories: []contractsv1.PacketCategory{contractsv1.CategoryEvidence},
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
	require.NotContains(t, strings.Join(localEvidenceLocators(first.Evidence), "\n"), workspace.GitRoot)

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
		"query --json consume the local index safely evidence --limit 5",
		"affected --json --stdin --depth 2",
		"callers --json Assemble --limit 5",
		"callees --json Assemble --limit 5",
		"impact --json Assemble --depth 2",
		"status --json",
		"query --json consume the local index safely evidence --limit 5",
		"affected --json --stdin --depth 2",
		"callers --json Assemble --limit 5",
		"callees --json Assemble --limit 5",
		"impact --json Assemble --depth 2",
	}, lines)
	for _, line := range lines {
		require.NotContains(t, line, "explore")
		require.NotContains(t, line, "sqlite")
	}
}

func newFixtureCodeGraphProvider(t *testing.T) (*CodeGraphLocalIndexProvider, LocalWorkspaceSnapshot, string) {
	t.Helper()
	root := t.TempDir()
	canonicalRoot, err := canonicalCodeGraphRoot(root)
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(canonicalRoot, ".codegraph"), 0o700))
	fixtureDir := filepath.Join(root, "fixtures")
	require.NoError(t, os.Mkdir(fixtureDir, 0o700))
	for _, name := range []string{"status", "query", "callers", "callees", "impact", "affected", "files"} {
		fixture := readCodeGraphFixture(t, name)
		if name == "status" {
			fixture = strings.ReplaceAll(fixture, "<local-only:absolute-project-path>", canonicalRoot)
			fixture = strings.ReplaceAll(fixture, "<local-only:absolute-index-path>", filepath.Join(canonicalRoot, ".codegraph"))
		}
		require.NoError(t, os.WriteFile(filepath.Join(fixtureDir, name+".json"), []byte(fixture), 0o600))
	}
	commandLog := filepath.Join(root, "commands.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(commandLog) + "\nfixture_dir=${0%/*}/fixtures\ncase \"$1\" in status|query|callers|callees|impact|affected|files) /bin/cat \"$fixture_dir/$1.json\" ;; *) exit 1 ;; esac\n"
	executable := filepath.Join(root, "codegraph")
	require.NoError(t, os.WriteFile(executable, []byte(script), 0o700))
	runner := CodeGraphRunner{
		Config:            LocalIndexConfig{Executable: executable, Timeout: 3 * time.Second, MaxItems: 5, MaxOutputTokens: 1000},
		resolveExecutable: func(string) (string, error) { return executable, nil },
	}
	workspace := LocalWorkspaceSnapshot{
		Repository:        LocalRepositoryIdentity{Host: "github.com", Slug: "full-chaos/dev-health-acr"},
		GitRoot:           canonicalRoot,
		Branch:            "feat/chaos-3007",
		CommitSHA:         "0123456789abcdef0123456789abcdef01234567",
		ChangedFiles:      []string{"acr/internal/contextpacket/assembler.go"},
		ChangedFilesState: LocalChangedFilesComplete,
	}
	return NewCodeGraphLocalIndexProvider(runner, workspace), workspace, commandLog
}

func TestCodeGraphProvider_Capabilities_acceptsOnlyCanonicalIndexDirectory(t *testing.T) {
	// Given
	provider, workspace, _ := newFixtureCodeGraphProvider(t)
	cases := []struct {
		name      string
		indexPath string
		available bool
		symlink   bool
	}{
		{name: "canonical index directory", indexPath: filepath.Join(workspace.GitRoot, ".codegraph"), available: true},
		{name: "database beneath index directory", indexPath: filepath.Join(workspace.GitRoot, ".codegraph", "codegraph.db")},
		{name: "sibling index directory", indexPath: filepath.Join(filepath.Dir(workspace.GitRoot), ".codegraph")},
		{name: "ancestor index directory", indexPath: filepath.Join(filepath.Dir(filepath.Dir(workspace.GitRoot)), ".codegraph")},
		{name: "traversal index directory", indexPath: workspace.GitRoot + "/.codegraph/../.codegraph"},
		{name: "symlink escape", indexPath: filepath.Join(workspace.GitRoot, ".codegraph"), symlink: true},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			// Given
			if test.symlink {
				require.NoError(t, os.Remove(test.indexPath))
				require.NoError(t, os.Symlink(t.TempDir(), test.indexPath))
			}
			status := strings.ReplaceAll(readCodeGraphFixture(t, "status"), "<local-only:absolute-project-path>", workspace.GitRoot)
			status = strings.ReplaceAll(status, "<local-only:absolute-index-path>", test.indexPath)
			fixturePath := filepath.Join(t.TempDir(), "status.json")
			require.NoError(t, os.WriteFile(fixturePath, []byte(status), 0o600))
			executable := filepath.Join(t.TempDir(), "codegraph")
			require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\n/bin/cat "+shellQuote(fixturePath)+"\n"), 0o700))
			provider.runner = CodeGraphRunner{
				Config:            LocalIndexConfig{Executable: executable, Timeout: 3 * time.Second},
				resolveExecutable: func(string) (string, error) { return executable, nil },
			}

			// When
			capabilities, err := provider.Capabilities(t.Context())

			// Then
			require.NoError(t, err)
			require.Equal(t, test.available, capabilities.Available)
		})
	}
}

func TestCodeGraphProvider_Capabilities_rejectsExactIndexPathSymlinkOutsideRoot(t *testing.T) {
	// Given
	provider, workspace, _ := newFixtureCodeGraphProvider(t)
	index := filepath.Join(workspace.GitRoot, ".codegraph")
	require.NoError(t, os.Remove(index))
	require.NoError(t, os.Symlink(t.TempDir(), index))

	// When
	capabilities, err := provider.Capabilities(t.Context())

	// Then
	require.NoError(t, err)
	require.False(t, capabilities.Available)
}

func TestCodeGraphProvider_ContextForTask_preservesCandidateTimeout(t *testing.T) {
	// Given
	provider, workspace, _ := newFixtureCodeGraphProvider(t)
	status := strings.ReplaceAll(readCodeGraphFixture(t, "status"), "<local-only:absolute-project-path>", workspace.GitRoot)
	status = strings.ReplaceAll(status, "<local-only:absolute-index-path>", filepath.Join(workspace.GitRoot, ".codegraph"))
	statusPath := filepath.Join(t.TempDir(), "status.json")
	require.NoError(t, os.WriteFile(statusPath, []byte(status), 0o600))
	executable := filepath.Join(t.TempDir(), "codegraph")
	script := "#!/bin/sh\nif [ \"$1\" = status ]; then /bin/cat " + shellQuote(statusPath) + "; else sleep 10; fi\n"
	require.NoError(t, os.WriteFile(executable, []byte(script), 0o700))
	provider.runner = CodeGraphRunner{Config: LocalIndexConfig{Executable: executable, Timeout: 100 * time.Millisecond}, resolveExecutable: func(string) (string, error) { return executable, nil }}
	request := LocalContextRequest{TaskID: "CHAOS-3007", Goal: "inspect local evidence", MaxItems: 1, MaxOutputTokens: 125, Workspace: &workspace}

	// When
	_, err := provider.ContextForTask(t.Context(), request)

	// Then
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, ErrLocalIndexUnavailable)
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

func TestBuildCodeGraphEvidence_usesCommandQueryIDs(t *testing.T) {
	// Given
	candidates := []codeGraphCandidate{
		{Command: codeGraphCommandQuery, Type: "definition", Locator: "node:one", Title: "definition: one"},
		{Command: codeGraphCommandCallers, Type: "caller", Locator: "caller:one", Title: "caller: one"},
		{Command: codeGraphCommandCallees, Type: "callee", Locator: "callee:one", Title: "callee: one"},
		{Command: codeGraphCommandImpact, Type: "impact", Locator: "impact:one", Title: "impact: one"},
		{Command: codeGraphCommandAffected, Type: "affected", Locator: "affected:one", Title: "affected: one"},
		{Command: codeGraphCommandFiles, Type: "file", Locator: "file:one", Title: "file: one"},
	}

	// When
	evidence, err := buildCodeGraphEvidence(candidates, len(candidates), 1000)

	// Then
	require.NoError(t, err)
	require.Equal(t, []string{"query", "callers", "callees", "impact", "affected", "files"}, []string{evidence[0].QueryID, evidence[1].QueryID, evidence[2].QueryID, evidence[3].QueryID, evidence[4].QueryID, evidence[5].QueryID})
}

func TestBuildCodeGraphEvidence_rejectsUnknownCommand(t *testing.T) {
	// Given
	candidates := []codeGraphCandidate{{Command: codeGraphCommand("unknown"), Type: "definition", Locator: "node:one", Title: "definition: one"}}

	// When
	_, err := buildCodeGraphEvidence(candidates, 1, 1000)

	// Then
	require.ErrorIs(t, err, errCodeGraphDecode)
}

func TestCodeGraphProvider_preservesStatusTimeout(t *testing.T) {
	// Given
	provider, workspace, _ := newFixtureCodeGraphProvider(t)
	executable := filepath.Join(t.TempDir(), "codegraph")
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nsleep 10\n"), 0o700))
	provider.runner = CodeGraphRunner{
		Config:            LocalIndexConfig{Executable: executable, Timeout: 100 * time.Millisecond},
		resolveExecutable: func(string) (string, error) { return executable, nil },
	}
	request := LocalContextRequest{TaskID: "CHAOS-3007", Goal: "inspect local evidence", MaxItems: 1, MaxOutputTokens: 125, Workspace: &workspace}

	// When
	_, capabilitiesErr := provider.Capabilities(t.Context())
	_, contextErr := provider.ContextForTask(t.Context(), request)

	// Then
	require.ErrorIs(t, capabilitiesErr, context.DeadlineExceeded)
	require.ErrorIs(t, contextErr, context.DeadlineExceeded)
}
