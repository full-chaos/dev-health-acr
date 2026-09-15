package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

// The invalid arm is an explicit faulty-store boundary injection. Production
// PG decoding validates stored results; this test does not claim such a row
// can pass that decoder. It certifies the actual projection-validation failure
// path's logging, independently of the logger's successful projection path.
func TestAnswerDisplayInfoRequiresValidatedProjection(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		name := "valid"
		if invalid {
			name = "invalid"
		}
		t.Run(name, func(t *testing.T) {
			result := validContextFabricInvestigationResult()
			if err := result.Validate(); err != nil {
				t.Fatal(err)
			}
			if invalid {
				result.RequestID = ""
			}
			projection := answerprojection.Project(result, answerprojection.Budget{})
			if (projection.Validate() != nil) != invalid {
				t.Fatal("fixture did not reach the requested projection-validation state")
			}
			app, token := newContextFabricTestAppWithResults(t, nil, legacyResultStore{result: result})
			file, err := os.CreateTemp(t.TempDir(), "display-validation-*.jsonl")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			app.logger = slog.New(slog.NewJSONHandler(file, &slog.HandlerOptions{Level: slog.LevelInfo}))
			request := investigationResultRequest(t, token, result.ResultID)
			query := request.URL.Query()
			query.Set("view", "projection")
			request.URL.RawQuery = query.Encode()
			recorder := httptest.NewRecorder()
			app.InstrumentedHandler(app.Handler()).ServeHTTP(recorder, request)
			wantCode, wantDisplays, wantFailures := http.StatusOK, 1, 0
			if invalid {
				wantCode, wantDisplays, wantFailures = http.StatusInternalServerError, 0, 1
			}
			if recorder.Code != wantCode {
				t.Fatalf("HTTP%d want%d: %s", recorder.Code, wantCode, recorder.Body.String())
			}
			if err := file.Sync(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(file.Name())
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := certify.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(parsed.LinesWithMsg(eventspec.AnswerDisplay.Msg)); got != wantDisplays {
				t.Errorf("HTTP%d successful-display Info count=%d want%d", recorder.Code, got, wantDisplays)
			}
			if got := len(parsed.LinesWithMsg("context fabric projection failed contract validation")); got != wantFailures {
				t.Errorf("projection failure diagnostic count=%d want%d", got, wantFailures)
			}
			if !invalid {
				_, err = certify.Certify(parsed, certify.Assertion{Event: eventspec.AnswerDisplay, Want: map[string]any{"request_id": recorder.Header().Get("X-Request-ID"), "surface": "result_by_id", "markdown_rendered": false}})
				if err != nil {
					t.Fatal(err)
				}
			}
			if dir := os.Getenv("WORK_ITEM_COMBINED_PROOF_DIR"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, "display-validation-"+name+".jsonl"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
