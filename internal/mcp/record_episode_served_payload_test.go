package mcp

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// The golden holds the bytes record_episode served before the value sets
// became exported constants.
func TestRecordEpisodeServedVocabularyBytesAreUnchanged(t *testing.T) {
	created := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	var out []json.RawMessage
	add := func(response contractsv1.MCPRecordEpisodeResponse, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, encoded)
	}
	request := func(mode string) contractsv1.MCPRecordEpisodeRequest {
		return contractsv1.MCPRecordEpisodeRequest{
			ClientEpisodeID: "client-episode-1", IdempotencyKey: "idempotency-key-1",
			Scope: &contractsv1.EpisodeScope{}, Transcript: contractsv1.TranscriptRef{Mode: mode},
		}
	}
	add(recordEpisodeResponse(request("none"), sidecar.RecordEpisodeResult{NoPersist: true}))
	for _, state := range []string{"active", "redacted", "purged_tombstone"} {
		for _, mode := range []string{"none", "full", "redacted_summary"} {
			episode := &contractsv1.AgentEpisode{EpisodeID: "episode-12345", CreatedAt: created, RedactionState: state}
			episode.Transcript.Mode = mode
			response, err := recordEpisodeResponse(request(mode), sidecar.RecordEpisodeResult{Episode: episode})
			if err != nil && state != "purged_tombstone" {
				t.Fatalf("%s/%s: %v", state, mode, err)
			}
			add(response, nil)
		}
	}
	got, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const golden = "testdata/record_episode_served_vocabulary.golden.json"
	if *servedGolden {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("served record_episode bytes changed:\n%s\nwant:\n%s", got, want)
	}
}

var servedGolden = flag.Bool("write-served-golden", false, "rewrite the served-vocabulary golden")
