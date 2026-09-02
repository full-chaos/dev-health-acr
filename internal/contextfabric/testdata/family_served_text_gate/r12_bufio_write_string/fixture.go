// Package r12bufio is codex round 2 P1, re-executed by the lane.
package r12bufio

import (
	"bufio"
	"fmt"
	"net/http"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Handler writes family-derived prose through bufio.Writer.WriteString --
// a writer method that is not named exactly "Write".
func Handler(w http.ResponseWriter, family contractsv1.ContextFabricQuestionFamily) {
	prose := fmt.Sprintf("The selected family is %s, so ask about one subject.", family)
	bw := bufio.NewWriter(w)
	_, _ = bw.WriteString(prose)
	_ = bw.Flush()
}
