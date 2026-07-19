package sidecar

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeGraphProvider_Capabilities_reportsPinnedVersionFromStatus(t *testing.T) {
	// Given
	provider, _, _ := newFixtureCodeGraphProvider(t)

	// When
	capabilities, err := provider.Capabilities(t.Context())

	// Then
	require.NoError(t, err)
	require.Equal(t, LocalIndexCapabilities{ProviderID: codeGraphProviderID, ProviderVersion: "1.2.0", Available: true, MaxItems: 5, MaxOutputTokens: 1000}, capabilities)
}

func TestCodeGraphProvider_AdditiveFields_acceptsUnknownStatusField(t *testing.T) {
	// Given
	payload := localStatusPayload(t, readCodeGraphFixtureAt(t, "additive", "status"))

	// When
	status, err := decodeCodeGraphStatus(payload)

	// Then
	require.NoError(t, err)
	require.Equal(t, "1.2.0", status.Version)
}

func TestCodeGraphProvider_MissingField_rejectsRequiredStatusField(t *testing.T) {
	// Given
	object := decodeJSONObject(t, localStatusPayload(t, readCodeGraphFixture(t, "status")))
	delete(object, "fileCount")
	payload, err := json.Marshal(object)
	require.NoError(t, err)

	// When
	_, decodeErr := decodeCodeGraphStatus(payload)

	// Then
	require.ErrorIs(t, decodeErr, errCodeGraphDecode)
}

func TestCodeGraphProvider_UnsupportedVersion_rejectsStatus(t *testing.T) {
	// Given
	payload := []byte(strings.Replace(string(localStatusPayload(t, readCodeGraphFixture(t, "status"))), `"1.2.0"`, `"2.0.0"`, 1))

	// When
	_, err := decodeCodeGraphStatus(payload)

	// Then
	require.ErrorIs(t, err, errCodeGraphDecode)
}

func TestCodeGraphProvider_AbsolutePath_rejectsNodeProvenance(t *testing.T) {
	// Given
	payload := []byte(strings.Replace(readCodeGraphFixture(t, "query"), `"acr/internal/contextpacket/assembler.go"`, `"/private/local.go"`, 1))

	// When
	_, err := decodeCodeGraphQuery(payload)

	// Then
	require.ErrorIs(t, err, errCodeGraphDecode)
}

func TestCodeGraphProvider_ControlCharacter_rejectsNode(t *testing.T) {
	// Given
	payload := []byte(strings.Replace(readCodeGraphFixture(t, "query"), `"Assemble"`, `"Assem\u0001ble"`, 1))

	// When
	_, err := decodeCodeGraphQuery(payload)

	// Then
	require.ErrorIs(t, err, errCodeGraphDecode)
}

func TestCodeGraphProvider_Oversized_rejectsNode(t *testing.T) {
	// Given
	payload := []byte(strings.Replace(readCodeGraphFixture(t, "query"), `"Assembler::Assemble"`, `"`+strings.Repeat("x", maxLocalTaskBytes+1)+`"`, 1))

	// When
	_, err := decodeCodeGraphQuery(payload)

	// Then
	require.ErrorIs(t, err, errCodeGraphDecode)
}

func TestCodeGraphProvider_ForbiddenRelationship_rejectsDuplicateCaller(t *testing.T) {
	// Given
	object := decodeJSONObject(t, []byte(readCodeGraphFixture(t, "callers")))
	var callers []json.RawMessage
	require.NoError(t, json.Unmarshal(object["callers"], &callers))
	callers = append(callers, callers[0])
	value, err := json.Marshal(callers)
	require.NoError(t, err)
	object["callers"] = value
	payload, err := json.Marshal(object)
	require.NoError(t, err)

	// When
	_, decodeErr := decodeCodeGraphRelations(payload, "callers")

	// Then
	require.ErrorIs(t, decodeErr, errCodeGraphDecode)
}

func TestCodeGraphProvider_ResolveEvidence_returnsNotFoundForUnknownOrMalformedLocator(t *testing.T) {
	// Given
	provider, _, _ := newFixtureCodeGraphProvider(t)

	// When
	_, unknownErr := provider.ResolveEvidence(t.Context(), "unknown")
	_, malformedErr := provider.ResolveEvidence(t.Context(), "../unsafe")

	// Then
	require.ErrorIs(t, unknownErr, ErrLocalEvidenceNotFound)
	require.ErrorIs(t, malformedErr, ErrLocalEvidenceNotFound)
}

func TestCodeGraphProvider_Deterministic_decodesAllCanonicalFixtures(t *testing.T) {
	// Given
	statusPayload := localStatusPayload(t, readCodeGraphFixture(t, "status"))

	// When
	firstStatus, firstStatusErr := decodeCodeGraphStatus(statusPayload)
	secondStatus, secondStatusErr := decodeCodeGraphStatus(statusPayload)
	firstQuery, firstQueryErr := decodeCodeGraphQuery([]byte(readCodeGraphFixture(t, "query")))
	secondQuery, secondQueryErr := decodeCodeGraphQuery([]byte(readCodeGraphFixture(t, "query")))
	callers, callersErr := decodeCodeGraphRelations([]byte(readCodeGraphFixture(t, "callers")), "callers")
	callees, calleesErr := decodeCodeGraphRelations([]byte(readCodeGraphFixture(t, "callees")), "callees")
	impact, impactErr := decodeCodeGraphImpact([]byte(readCodeGraphFixture(t, "impact")))
	affected, affectedErr := decodeCodeGraphAffected([]byte(readCodeGraphFixture(t, "affected")))
	files, filesErr := decodeCodeGraphFiles([]byte(readCodeGraphFixture(t, "files")))

	// Then
	require.NoError(t, firstStatusErr)
	require.NoError(t, secondStatusErr)
	require.Equal(t, firstStatus, secondStatus)
	require.NoError(t, firstQueryErr)
	require.NoError(t, secondQueryErr)
	require.Equal(t, firstQuery, secondQuery)
	require.NoError(t, callersErr)
	require.NoError(t, calleesErr)
	require.NoError(t, impactErr)
	require.NoError(t, affectedErr)
	require.NoError(t, filesErr)
	require.NotEmpty(t, callers)
	require.NotEmpty(t, callees)
	require.NotEmpty(t, impact)
	require.NotEmpty(t, affected.ChangedFiles)
	require.NotEmpty(t, files)
}

func TestCodeGraphProvider_ContextForTask_rejectsTruncatedOrMismatchedWorkspace(t *testing.T) {
	// Given
	provider, workspace, _ := newFixtureCodeGraphProvider(t)
	truncatedWorkspace := workspace
	truncatedWorkspace.ChangedFilesState = LocalChangedFilesTruncated
	mismatchedWorkspace := workspace
	mismatchedWorkspace.Repository.Slug = "other/repository"
	alteredFilesWorkspace := workspace
	alteredFilesWorkspace.ChangedFiles = []string{"internal/sidecar/local_index.go"}

	// When
	_, truncatedErr := provider.ContextForTask(t.Context(), LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe local context", MaxItems: 1, MaxOutputTokens: 125, Workspace: &truncatedWorkspace})
	_, mismatchErr := provider.ContextForTask(t.Context(), LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe local context", MaxItems: 1, MaxOutputTokens: 125, Workspace: &mismatchedWorkspace})
	_, alteredFilesErr := provider.ContextForTask(t.Context(), LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe local context", MaxItems: 1, MaxOutputTokens: 125, Workspace: &alteredFilesWorkspace})

	// Then
	require.ErrorIs(t, truncatedErr, ErrInvalidLocalContextRequest)
	require.ErrorIs(t, mismatchErr, ErrInvalidLocalContextRequest)
	require.ErrorIs(t, alteredFilesErr, ErrInvalidLocalContextRequest)
}

func localStatusPayload(t *testing.T, fixture string) []byte {
	t.Helper()
	root, err := canonicalCodeGraphRoot(t.TempDir())
	require.NoError(t, err)
	fixture = strings.ReplaceAll(fixture, "<local-only:absolute-project-path>", root)
	fixture = strings.ReplaceAll(fixture, "<local-only:absolute-index-path>", root+"/.codegraph")
	return []byte(fixture)
}

func decodeJSONObject(t *testing.T, payload []byte) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(payload, &object))
	return object
}

func readCodeGraphFixtureAt(t *testing.T, directory, name string) string {
	t.Helper()
	_, sourceFile, _, found := runtime.Caller(0)
	require.True(t, found)
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), "..", "..", "testdata", "codegraph", "v1.2.0", directory, name+".json"))
	require.NoError(t, err)
	return string(contents)
}
