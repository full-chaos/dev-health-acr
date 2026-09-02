// Package r14served is the ENFORCED-TIER PROOF, and it exists because
// narrowing the enforced claim to encoder-reachable field stores left that
// tier with no fixture coverage at all: every other fixture stands in for
// a served field without being encoder-reachable itself, so all sixteen
// landed in the reported tier and the tier that actually FAILS the build
// could have been broken with the whole corpus still green.
//
// This fixture closes that. It is also the production shape the ticket is
// really about -- prose derived from the family, stored on a wire struct,
// and encoded -- which is why it should arguably have been here from the
// start rather than only after the tiering changed.
package r14served

import (
	"encoding/json"
	"fmt"
	"net/http"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Answer is encoder-reachable: Serve marshals it, so the served-type
// derivation reaches it and its fields by the ordinary rule, with no name
// written down anywhere.
type Answer struct {
	DirectJudgment string `json:"direct_judgment"`
}

// Serve derives prose from the family, stores it on the wire struct, and
// encodes it.
func Serve(w http.ResponseWriter, family contractsv1.ContextFabricQuestionFamily) {
	answer := Answer{
		DirectJudgment: fmt.Sprintf("A %s question needs one subject.", family),
	}
	encoded, err := json.Marshal(answer)
	if err != nil {
		return
	}
	_, _ = w.Write(encoded)
}
