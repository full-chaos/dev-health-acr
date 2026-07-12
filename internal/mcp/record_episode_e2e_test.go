package mcp

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCommandTransportE2ERecordEpisodeCreateReplayAndNoPersist(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-spawning E2E test in -short mode")
	}

	// Given
	server := recordEpisodeE2EServer(t)
	t.Cleanup(server.Close)
	caPath := recordEpisodeE2ECAPath(t, server)
	binPath := buildACRMCPBinary(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binPath, "serve")
	command.Env = append(os.Environ(),
		"ACR_API_URL="+server.URL,
		"ACR_API_CA_BUNDLE="+caPath,
		"ACR_API_TOKEN="+fixtureToken(0xE3),
		"ACR_SIDECAR_VERSION=1.0.0",
		"ACR_ENABLE_WRITEBACK=true",
		"ACR_ENABLE_TRANSCRIPT_CAPTURE=true",
	)
	var stderr fixtureStderrBuffer
	command.Stderr = &stderr
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "e2e-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatalf("connect over CommandTransport failed: %v\nstderr: %s", err, stderr.String())
	}
	defer session.Close()

	// When
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := callRecordEpisode(t, session, recordEpisodeArguments("summary-secret", "transcript-secret", "default_90d"))
	second := callRecordEpisode(t, session, recordEpisodeArguments("summary-secret", "transcript-secret", "default_90d"))
	noPersist := callRecordEpisode(t, session, recordEpisodeArguments("summary-secret", "transcript-secret", "no_persist"))

	// Then
	if len(tools.Tools) != 3 || first.Duplicate == nil || *first.Duplicate || first.Scope.Branch != "main" || first.TranscriptDisposition != "redacted" || second.Duplicate == nil || !*second.Duplicate || noPersist.Status != "no_persist" || noPersist.Scope.Branch != "main" || noPersist.TranscriptDisposition != "accepted" {
		t.Fatalf("unexpected real-binary writeback results: tools=%d first=%#v second=%#v no_persist=%#v", len(tools.Tools), first, second, noPersist)
	}
	for _, receipt := range []contractsv1.MCPRecordEpisodeResponse{first, second, noPersist} {
		if rendered := string(receiptJSON(t, receipt)); strings.Contains(rendered, "summary-secret") || strings.Contains(rendered, "transcript-secret") {
			t.Fatal("real-binary receipt leaked submitted text")
		}
	}
}

func recordEpisodeE2EServer(t *testing.T) *httptest.Server {
	t.Helper()
	calls := 0
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent-context/capabilities":
			caps := validCapabilitiesFixture()
			caps.Permissions.EpisodeWrite = true
			caps.SupportedSchemaVersions = append(caps.SupportedSchemaVersions, writebackSchemaVersions...)
			caps.EnabledTools = append(caps.EnabledTools, toolRecordEpisode)
			writeJSONFixture(t, w, http.StatusOK, caps)
		case "/api/v1/agent-context/episodes":
			var create contractsv1.AgentEpisodeCreate
			if err := json.NewDecoder(r.Body).Decode(&create); err != nil {
				t.Fatal(err)
			}
			if create.RetentionClass == "no_persist" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			calls++
			recorded := validAgentEpisodeFixture(create, calls == 2)
			recorded.SchemaVersion = contractsv1.AgentEpisodeSchema
			writeJSONFixture(t, w, http.StatusCreated, recorded)
		default:
			http.NotFound(w, r)
		}
	}))
}

func recordEpisodeE2ECAPath(t *testing.T, server *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "record-episode-ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(path, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
