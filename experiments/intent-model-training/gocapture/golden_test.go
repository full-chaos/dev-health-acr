//go:build unix

package main

import (
	"bytes"
	"os"
	"testing"
)

// T16: the golden envelope regenerated from the production runtime over
// loopback equals the checked-in file. GOCAPTURE_WRITE_GOLDEN=1 rewrites it.
func TestGoldenEnvelopeMatchesProductionClient(t *testing.T) {
	regenerated := goldenFromBody(t, recordEnvelope(t))
	if os.Getenv("GOCAPTURE_WRITE_GOLDEN") == "1" {
		writePrivate(t, goldenPath(), regenerated)
	}
	checked, err := os.ReadFile(goldenPath())
	if err != nil {
		t.Fatalf("golden missing: %v", err)
	}
	if !bytes.Equal(checked, regenerated) {
		t.Fatalf("golden is stale:\nchecked-in: %s\nproduction: %s", checked, regenerated)
	}
	if _, err := loadGolden(goldenPath()); err != nil {
		t.Fatal(err)
	}
}
