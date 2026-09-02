// Package r12copy is codex round 2 P1, re-executed by the lane.
package r12copy

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Handler wraps family-derived prose in a strings.Reader and io.Copy's it
// to the response. The value arriving at the boundary is a *strings.Reader,
// not text.
func Handler(w http.ResponseWriter, family contractsv1.ContextFabricQuestionFamily) {
	prose := fmt.Sprintf("The selected family is %s, so ask about one subject.", family)
	_, _ = io.Copy(w, strings.NewReader(prose))
}
