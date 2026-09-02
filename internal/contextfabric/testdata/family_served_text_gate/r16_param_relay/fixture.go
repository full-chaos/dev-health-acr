package r16param

import (
	"encoding/json"
	"fmt"
	"net/http"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type Answer struct {
	Text string `json:"text"`
}

func put(a *Answer, s string) { a.Text = s }

func Serve(w http.ResponseWriter, family contractsv1.ContextFabricQuestionFamily) {
	var answer Answer
	put(&answer, fmt.Sprintf("A %s question needs one subject.", family))
	b, err := json.Marshal(answer)
	if err != nil {
		return
	}
	_, _ = w.Write(b)
}
