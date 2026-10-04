package genkitruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
)

// The digest was taken from BuildSynthesisPrompt on the commit before the
// payload builders moved to synthesisprompt. A change to the model input
// bytes needs a new synthesis prompt version and a new digest here.
func TestSynthesisUserPayloadBytesEqualTheRecordedDigest(t *testing.T) {
	const (
		wantBytes  = 1284
		wantDigest = "512347128ad054367059f8715566b0306630fbc69d88ba1b0d8b8f2af3d2b1e0"
	)
	input := validSynthesisInput()
	preview, err := BuildSynthesisPrompt(input, DefaultExchangeMaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := synthesisprompt.UserPayload("", input, DefaultExchangeMaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string]string{"BuildSynthesisPrompt": preview, "synthesisprompt.UserPayload": string(leaf)} {
		sum := sha256.Sum256([]byte(payload))
		if got := hex.EncodeToString(sum[:]); len(payload) != wantBytes || got != wantDigest {
			t.Errorf("%s: %d bytes, sha256 %s; want %d bytes, sha256 %s", name, len(payload), got, wantBytes, wantDigest)
		}
	}
}
