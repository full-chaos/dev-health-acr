package r17chan

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
	ch := make(chan string, 1)
	ch <- fmt.Sprintf("A %s question needs one subject.", family)
	text := <-ch
	answer := Answer{Text: text}
	b, err := json.Marshal(answer)
	if err != nil {
		return
	}
	_, _ = w.Write(b)
}
