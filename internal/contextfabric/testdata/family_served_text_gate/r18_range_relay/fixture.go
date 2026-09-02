package r18range

import (
	"encoding/json"
	"fmt"
	"net/http"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type Answer struct {
	Text string `json:"text"`
}

func Serve(w http.ResponseWriter, family contractsv1.ContextFabricQuestionFamily) {
	table := map[string]string{
		"k": fmt.Sprintf("A %s question needs one subject.", family),
	}
	var answer Answer
	for _, text := range table {
		answer.Text = text
	}
	b, err := json.Marshal(answer)
	if err != nil {
		return
	}
	_, _ = w.Write(b)
}
