package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestAModelTimeoutNamesTheModelTimeoutNotTheRequestBudget(t *testing.T) {
	cases := []struct {
		name           string
		err            error
		wantClassified string
		wantDeadline   string
		wantNamesKnob  bool
	}{
		{"model deadline", fmt.Errorf("%w: %w", cf.ErrModelTimeout, context.DeadlineExceeded), "model_deadline_exceeded", "model", true},
		{"any other deadline keeps the investigation message", context.DeadlineExceeded, "deadline_exceeded", "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logs := &bytes.Buffer{}
			app, token := teamSynthesisApp(t, scriptedSynthesisModel{err: testCase.err}, nil, teamExpander{targets: 3}, logs)

			response := postTeamQuestion(t, app, token)

			if response.Code != http.StatusGatewayTimeout {
				t.Fatalf("status = %d, want 504 body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Error struct {
					Code      string         `json:"code"`
					Message   string         `json:"message"`
					Retryable bool           `json:"retryable"`
					Details   map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != "upstream_unavailable" || !body.Error.Retryable {
				t.Fatalf("code=%q retryable=%v, want upstream_unavailable/true", body.Error.Code, body.Error.Retryable)
			}
			if got := strings.Contains(body.Error.Message, "ACR_CONTEXT_FABRIC_MODEL_TIMEOUT"); got != testCase.wantNamesKnob {
				t.Fatalf("message %q names the model knob = %v, want %v", body.Error.Message, got, testCase.wantNamesKnob)
			}
			deadline, present := body.Error.Details["deadline"]
			if testCase.wantDeadline == "" && present {
				t.Fatalf("details.deadline = %v, want absent (the request context did not expire)", deadline)
			}
			if testCase.wantDeadline != "" && deadline != testCase.wantDeadline {
				t.Fatalf("details.deadline = %v, want %q", deadline, testCase.wantDeadline)
			}
			entry := decodeFailureLog(t, logs.String())
			if got := entry["failure_classification"]; got != testCase.wantClassified {
				t.Fatalf("failure_classification = %v, want %q", got, testCase.wantClassified)
			}
		})
	}
}
