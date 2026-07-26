package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/stretchr/testify/require"
)

func TestService_PrepareCreate_reusesCreateInvariants_withoutPersistingPlaintext(t *testing.T) {
	// Given
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	credentials := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, credentials, audit, now)
	request := CreateCredentialRequest{
		OrgID:            "11111111-1111-1111-1111-111111111111",
		Name:             "device login",
		RepositoryScopes: []string{"Full-Chaos/Dev-Health-ACR"},
		CreatedBy:        "user_1",
		ExpiresAt:        pointerToTime(now.Add(30 * 24 * time.Hour)),
	}

	// When
	prepared, err := service.PrepareCreate(request)

	// Then
	require.NoError(t, err)
	storedBeforeCommit, err := credentials.List(ctx, request.OrgID)
	require.NoError(t, err)
	require.Empty(t, storedBeforeCommit)
	input := prepared.StorageInput()
	expectedToken := deterministicPreparedToken()
	require.Equal(t, []string{"full-chaos/dev-health-acr"}, input.RepositoryScopes)
	require.Equal(t, []string{ScopeContextRead, ScopeEvidenceRead}, input.Scopes)
	require.Equal(t, HashToken(expectedToken), input.TokenHash)
	require.NotContains(t, input.TokenPrefix, expectedToken)

	stored, err := credentials.CreateCredential(ctx, input)
	require.NoError(t, err)
	issued, err := prepared.Complete(stored)
	require.NoError(t, err)
	require.Equal(t, expectedToken, issued.Token)
	require.True(t, IsTokenShapeValid(issued.Token))
}

func TestPreparedCredential_reflectionAndSerializationCannotExposePlaintext(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	credentials := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, credentials, audit, now)
	prepared, err := service.PrepareCreate(CreateCredentialRequest{
		OrgID: "11111111-1111-1111-1111-111111111111", Name: "device login",
		RepositoryScopes: []string{"full-chaos/dev-health-acr"}, CreatedBy: "user_1",
	})
	require.NoError(t, err)
	expectedToken := deterministicPreparedToken()

	// When
	jsonValue, err := json.Marshal(prepared)
	require.NoError(t, err)
	var logOutput bytes.Buffer
	slog.New(slog.NewJSONHandler(&logOutput, nil)).Info("prepared credential", slog.Any("credential", prepared))
	reflectedValues := make([]string, 0)
	collectReflectedStrings(reflect.ValueOf(prepared), &reflectedValues)
	representations := append(reflectedValues,
		string(jsonValue),
		fmt.Sprint(prepared),
		fmt.Sprintf("%+v", prepared),
		fmt.Sprintf("%#v", prepared),
		logOutput.String(),
	)

	// Then
	unissued, completionErr := prepared.Complete(contractsv1.ClientCredential{})
	require.ErrorIs(t, completionErr, ErrInvalidCredential)
	require.Empty(t, unissued.Token)
	require.JSONEq(t, `{"redacted":true}`, string(jsonValue))
	require.Equal(t, preparedCredentialRedacted, fmt.Sprint(prepared))
	require.Equal(t, preparedCredentialRedacted, fmt.Sprintf("%+v", prepared))
	require.Equal(t, preparedCredentialRedacted, fmt.Sprintf("%#v", prepared))
	require.Contains(t, logOutput.String(), preparedCredentialRedacted)
	for _, representation := range representations {
		require.NotContains(t, representation, expectedToken)
	}
}

func collectReflectedStrings(value reflect.Value, result *[]string) {
	if !value.IsValid() {
		return
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !value.IsNil() {
			collectReflectedStrings(value.Elem(), result)
		}
	case reflect.Struct:
		if value.Type() == reflect.TypeFor[time.Time]() {
			return
		}
		for index := range value.NumField() {
			collectReflectedStrings(value.Field(index), result)
		}
	case reflect.Array, reflect.Slice:
		for index := range value.Len() {
			collectReflectedStrings(value.Index(index), result)
		}
	case reflect.String:
		*result = append(*result, value.String())
	}
}

func deterministicPreparedToken() string {
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, tokenSecretBytes))
}

func pointerToTime(value time.Time) *time.Time { return &value }
