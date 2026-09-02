// Package r13writerto is codex round 4 P1 (ARGUED by the reviewer,
// re-executed by the lane): a static method whose RECEIVER carries the
// payload and whose first explicit PARAMETER is the writer.
package r13writerto

import (
	"fmt"
	"net/http"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Handler writes family-derived prose via io.WriterTo: the data flows OUT
// of the receiver INTO the writer, the reverse of every other boundary
// shape in the corpus.
func Handler(w http.ResponseWriter, family contractsv1.ContextFabricQuestionFamily) {
	prose := fmt.Sprintf("The selected family is %s, so ask about one subject.", family)
	_, _ = strings.NewReader(prose).WriteTo(w)
}
