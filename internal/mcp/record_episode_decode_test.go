package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDecodeRecordEpisodeRequestRejectsNonCanonicalJSONBeforeHostedCall(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"unknown field", `{"client_episode_id":"client_ep_01J0ACR001","idempotency_key":"idem_01J0ACR001","context_packet_id":"packet_01J0ACR001","goal":"g","repository":{"slug":"acme/widgets"},"scope":{},"started_at":"2026-07-11T12:00:00Z","ended_at":"2026-07-11T12:01:00Z","outcome":"succeeded","summary":"s","artifacts":{"files_touched":[],"artifact_uris":[],"tests_run":[]},"transcript":{"mode":"none"},"retention_class":"no_persist","unknown":true}`},
		{"duplicate field", `{"client_episode_id":"client_ep_01J0ACR001","idempotency_key":"idem_01J0ACR001","idempotency_key":"idem_01J0ACR002","context_packet_id":"packet_01J0ACR001","goal":"g","repository":{"slug":"acme/widgets"},"scope":{},"started_at":"2026-07-11T12:00:00Z","ended_at":"2026-07-11T12:01:00Z","outcome":"succeeded","summary":"s","artifacts":{"files_touched":[],"artifact_uris":[],"tests_run":[]},"transcript":{"mode":"none"},"retention_class":"no_persist"}`},
		{"trailing JSON", `{"client_episode_id":"client_ep_01J0ACR001","idempotency_key":"idem_01J0ACR001","context_packet_id":"packet_01J0ACR001","goal":"g","repository":{"slug":"acme/widgets"},"scope":{},"started_at":"2026-07-11T12:00:00Z","ended_at":"2026-07-11T12:01:00Z","outcome":"succeeded","summary":"s","artifacts":{"files_touched":[],"artifact_uris":[],"tests_run":[]},"transcript":{"mode":"none"},"retention_class":"no_persist"} {}`},
		{"missing scope", `{"client_episode_id":"client_ep_01J0ACR001","idempotency_key":"idem_01J0ACR001","context_packet_id":"packet_01J0ACR001","goal":"g","repository":{"slug":"acme/widgets"},"started_at":"2026-07-11T12:00:00Z","ended_at":"2026-07-11T12:01:00Z","outcome":"succeeded","summary":"s","artifacts":{"files_touched":[],"artifact_uris":[],"tests_run":[]},"transcript":{"mode":"none"},"retention_class":"no_persist"}`},
		{"null scope", `{"client_episode_id":"client_ep_01J0ACR001","idempotency_key":"idem_01J0ACR001","context_packet_id":"packet_01J0ACR001","goal":"g","repository":{"slug":"acme/widgets"},"scope":null,"started_at":"2026-07-11T12:00:00Z","ended_at":"2026-07-11T12:01:00Z","outcome":"succeeded","summary":"s","artifacts":{"files_touched":[],"artifact_uris":[],"tests_run":[]},"transcript":{"mode":"none"},"retention_class":"no_persist"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			payload := json.RawMessage(test.payload)

			// When
			_, err := decodeRecordEpisodeRequest(payload)

			// Then
			if err == nil {
				t.Fatal("expected invalid record_episode JSON to be rejected")
			}
		})
	}
}

func TestHandleRecordEpisodeRejectsInvalidJSONBeforeHostedCall(t *testing.T) {
	// Given
	fx := newFixtureServer(t)
	calls := 0
	fx.EpisodeHandler = func(w http.ResponseWriter, _ *http.Request) { calls++ }
	boot := newFixtureBootstrap(t, fx)
	boot.Config.EnableWriteback = true
	boot.Capabilities.Permissions.EpisodeWrite = true
	request := &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: []byte(`{"client_episode_id":"client_ep_01J0ACR001","idempotency_key":"idem_01J0ACR001","idempotency_key":"idem_01J0ACR002"}`)}}

	// When
	result, err := handleRecordEpisode(context.Background(), boot, request)

	// Then
	if err != nil || !result.IsError || calls != 0 {
		t.Fatalf("result=%#v err=%v calls=%d", result, err, calls)
	}
}

func TestDecodeRecordEpisodeRequestAllowsPresentEmptyScope(t *testing.T) {
	// Given
	payload := json.RawMessage(`{"client_episode_id":"client_ep_01J0ACR001","idempotency_key":"idem_01J0ACR001","context_packet_id":"packet_01J0ACR001","goal":"g","repository":{"slug":"acme/widgets"},"scope":{},"started_at":"2026-07-11T12:00:00Z","ended_at":"2026-07-11T12:01:00Z","outcome":"succeeded","summary":"s","artifacts":{"files_touched":[],"artifact_uris":[],"tests_run":[]},"transcript":{"mode":"none"},"retention_class":"no_persist"}`)

	// When
	request, err := decodeRecordEpisodeRequest(payload)

	// Then
	if err != nil || request.Scope == nil || request.Scope.Branch != "" || request.Scope.CommitSHA != "" {
		t.Fatalf("request=%#v err=%v", request, err)
	}
}
