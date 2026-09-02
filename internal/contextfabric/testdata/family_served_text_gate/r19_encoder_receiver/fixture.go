package r19enc

import (
	"encoding/json"
	"fmt"
	"net/http"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type Answer struct {
	Text string `json:"text"`
}

// Serve uses json.NewEncoder(w).Encode(v) -- production's writeJSON shape.
func Serve(w http.ResponseWriter, family contractsv1.ContextFabricQuestionFamily) {
	answer := Answer{Text: fmt.Sprintf("A %s question needs one subject.", family)}
	_ = json.NewEncoder(w).Encode(answer)
}
