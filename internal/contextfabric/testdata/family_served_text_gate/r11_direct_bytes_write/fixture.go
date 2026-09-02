// Package r11bytes is codex round 1 P1 (second), re-executed by the lane.
package r11bytes

import (
	"fmt"
	"net/http"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Handler formats family-derived prose and writes the bytes straight to
// the HTTP response, never touching encoding/json.
func Handler(w http.ResponseWriter, family contractsv1.ContextFabricQuestionFamily) {
	prose := fmt.Sprintf("The selected family is %s, so ask about one subject.", family)
	_, _ = w.Write([]byte(prose))
}
