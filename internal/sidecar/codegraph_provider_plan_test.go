package sidecar

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeGraphProvider_Capabilities(t *testing.T) {
	// Given
	provider, _, _ := newFixtureCodeGraphProvider(t)

	// When
	capabilities, err := provider.Capabilities(context.Background())

	// Then
	require.NoError(t, err)
	require.True(t, capabilities.Available)
}

func TestCodeGraphProvider_ContextForTask(t *testing.T) {
	// Given
	provider, workspace, _ := newFixtureCodeGraphProvider(t)

	// When
	bundle, err := provider.ContextForTask(context.Background(), LocalContextRequest{TaskID: "CHAOS-3007", Goal: "inspect local evidence", MaxItems: 1, MaxOutputTokens: 125, Workspace: &workspace})

	// Then
	require.NoError(t, err)
	require.Len(t, bundle.Evidence, 1)
}

func TestCodeGraphProvider_ResolveEvidence(t *testing.T) {
	// Given
	provider, workspace, _ := newFixtureCodeGraphProvider(t)
	bundle, err := provider.ContextForTask(context.Background(), LocalContextRequest{TaskID: "CHAOS-3007", Goal: "inspect local evidence", MaxItems: 1, MaxOutputTokens: 125, Workspace: &workspace})
	require.NoError(t, err)

	// When
	evidence, resolveErr := provider.ResolveEvidence(context.Background(), bundle.Evidence[0].Locator)

	// Then
	require.NoError(t, resolveErr)
	require.Equal(t, bundle.Evidence[0].Locator, evidence.Locator)
}

func TestCodeGraphProvider_AdditiveFields(t *testing.T) {
	// Given
	payload := localStatusPayload(t, readCodeGraphFixtureAt(t, "additive", "status"))

	// When
	_, err := decodeCodeGraphStatus(payload)

	// Then
	require.NoError(t, err)
}

func TestCodeGraphProvider_Deterministic(t *testing.T) {
	// Given
	payload := []byte(readCodeGraphFixture(t, "query"))

	// When
	first, firstErr := decodeCodeGraphQuery(payload)
	second, secondErr := decodeCodeGraphQuery(payload)

	// Then
	require.NoError(t, firstErr)
	require.NoError(t, secondErr)
	require.Equal(t, first, second)
}

func TestCodeGraphProvider_MissingField(t *testing.T) {
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

func TestCodeGraphProvider_UnsupportedVersion(t *testing.T) {
	// Given
	payload := []byte(strings.Replace(string(localStatusPayload(t, readCodeGraphFixture(t, "status"))), `"1.2.0"`, `"2.0.0"`, 1))

	// When
	_, err := decodeCodeGraphStatus(payload)

	// Then
	require.ErrorIs(t, err, errCodeGraphDecode)
}

func TestCodeGraphProvider_AbsolutePath(t *testing.T) {
	// Given
	payload := []byte(strings.Replace(readCodeGraphFixture(t, "query"), `"acr/internal/contextpacket/assembler.go"`, `"/private/local.go"`, 1))

	// When
	_, err := decodeCodeGraphQuery(payload)

	// Then
	require.ErrorIs(t, err, errCodeGraphDecode)
}

func TestCodeGraphProvider_ControlCharacter(t *testing.T) {
	// Given
	payload := []byte(strings.Replace(readCodeGraphFixture(t, "query"), `"Assemble"`, `"Assem\u0001ble"`, 1))

	// When
	_, err := decodeCodeGraphQuery(payload)

	// Then
	require.ErrorIs(t, err, errCodeGraphDecode)
}

func TestCodeGraphProvider_Oversized(t *testing.T) {
	// Given
	payload := []byte(strings.Replace(readCodeGraphFixture(t, "query"), `"Assembler::Assemble"`, `"`+strings.Repeat("x", maxLocalTaskBytes+1)+`"`, 1))

	// When
	_, err := decodeCodeGraphQuery(payload)

	// Then
	require.ErrorIs(t, err, errCodeGraphDecode)
}

func TestCodeGraphProvider_ForbiddenRelationship(t *testing.T) {
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
