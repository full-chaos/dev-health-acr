package v1

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

// Ask Dev answer feedback (CHAOS-11950). The goldens are encoder output of
// production Go values; regenerate with
//
//	ACR_WRITE_THREAD_GOLDENS=1 go test ./internal/contracts/v1 -run TestResultFeedbackGoldens
//
// and review the diff.

const (
	feedbackSchemaFile        = "context_fabric_result_feedback.v1.schema.json"
	feedbackRequestSchemaFile = "context_fabric_result_feedback_request.v1.schema.json"
)

var feedbackGoldenAt = time.Date(2026, 10, 30, 21, 0, 0, 0, time.UTC)

func goldenResultFeedback() ContextFabricResultFeedback {
	at := feedbackGoldenAt
	return ContextFabricResultFeedback{
		SchemaVersion: ContextFabricResultFeedbackSchema,
		ResultID:      "result_11950_feedback_golden",
		Correct:       ContextFabricResultFeedbackYes,
		Useful:        ContextFabricResultFeedbackNo,
		UpdatedAt:     &at,
	}
}

func goldenResultFeedbackUnset() ContextFabricResultFeedback {
	return ContextFabricResultFeedback{
		SchemaVersion: ContextFabricResultFeedbackSchema,
		ResultID:      "result_11950_feedback_golden",
		Correct:       ContextFabricResultFeedbackUnset,
		Useful:        ContextFabricResultFeedbackUnset,
	}
}

func goldenResultFeedbackRequest() ContextFabricResultFeedbackRequest {
	return ContextFabricResultFeedbackRequest{
		SchemaVersion: ContextFabricResultFeedbackRequestSchema,
		Correct:       ContextFabricResultFeedbackYes,
		Useful:        ContextFabricResultFeedbackUnset,
	}
}

func TestResultFeedbackGoldens(t *testing.T) {
	for name, tc := range map[string]struct {
		value    interface{ Validate() error }
		schema   string
		parse    func([]byte) (any, error)
		original any
	}{
		"context_fabric_result_feedback.v1.json":         {goldenResultFeedback(), feedbackSchemaFile, func(b []byte) (any, error) { return ParseContextFabricResultFeedback(b) }, goldenResultFeedback()},
		"context_fabric_result_feedback_unset.v1.json":   {goldenResultFeedbackUnset(), feedbackSchemaFile, func(b []byte) (any, error) { return ParseContextFabricResultFeedback(b) }, goldenResultFeedbackUnset()},
		"context_fabric_result_feedback_request.v1.json": {goldenResultFeedbackRequest(), feedbackRequestSchemaFile, func(b []byte) (any, error) { return ParseContextFabricResultFeedbackRequest(b) }, goldenResultFeedbackRequest()},
	} {
		t.Run(name, func(t *testing.T) {
			if err := tc.value.Validate(); err != nil {
				t.Fatalf("the golden value does not validate: %v", err)
			}
			data, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if err := contractcheck.ValidateSerializedStrict("", tc.schema, data); err != nil {
				t.Fatalf("schema rejects the encoder's output: %v", err)
			}
			decoded, err := tc.parse(data)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			again, err := json.Marshal(decoded)
			if err != nil || !bytes.Equal(again, data) {
				t.Fatalf("round trip changed the value (err %v)", err)
			}
			compareOrWriteThreadGolden(t, name, append(data, '\n'))
		})
	}
}

// The Go validator and the published schema judge every mutation the same.
func TestResultFeedbackValidatorAndSchemaAgree(t *testing.T) {
	zero := time.Time{}
	feedback := map[string]struct {
		mutate func(*ContextFabricResultFeedback)
		valid  bool
	}{
		"golden": {func(*ContextFabricResultFeedback) {}, true},
		"both no": {func(f *ContextFabricResultFeedback) {
			f.Correct, f.Useful = ContextFabricResultFeedbackNo, ContextFabricResultFeedbackNo
		}, true},
		"set, then both cleared": {func(f *ContextFabricResultFeedback) {
			f.Correct, f.Useful = ContextFabricResultFeedbackUnset, ContextFabricResultFeedbackUnset
		}, true},
		"never set, both unset":    {func(f *ContextFabricResultFeedback) { *f = goldenResultFeedbackUnset() }, true},
		"never set, correct given": {func(f *ContextFabricResultFeedback) { f.UpdatedAt = nil }, false},
		"never set, useful given": {func(f *ContextFabricResultFeedback) {
			*f = goldenResultFeedbackUnset()
			f.Useful = ContextFabricResultFeedbackYes
		}, false},
		"unknown value":              {func(f *ContextFabricResultFeedback) { f.Correct = "maybe" }, false},
		"empty value":                {func(f *ContextFabricResultFeedback) { f.Useful = "" }, false},
		"short result id":            {func(f *ContextFabricResultFeedback) { f.ResultID = "r_1" }, false},
		"long result id":             {func(f *ContextFabricResultFeedback) { f.ResultID = strings.Repeat("r", 257) }, false},
		"result id at the bound":     {func(f *ContextFabricResultFeedback) { f.ResultID = strings.Repeat("r", 256) }, true},
		"wrong schema version":       {func(f *ContextFabricResultFeedback) { f.SchemaVersion = ContextFabricResultFeedbackRequestSchema }, false},
		"zero updated_at is refused": {func(f *ContextFabricResultFeedback) { f.UpdatedAt = &zero }, false},
	}
	for name, tc := range feedback {
		t.Run("feedback/"+name, func(t *testing.T) {
			value := goldenResultFeedback()
			tc.mutate(&value)
			goErr := value.Validate()
			if (goErr == nil) != tc.valid {
				t.Fatalf("Go validator: valid=%v, want %v (%v)", goErr == nil, tc.valid, goErr)
			}
			if name == "zero updated_at is refused" {
				// The zero time encodes as a valid date-time; only Go
				// refuses it, as the thread validator does.
				return
			}
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			schemaErr := contractcheck.ValidateSerializedStrict("", feedbackSchemaFile, data)
			if (schemaErr == nil) != tc.valid {
				t.Fatalf("schema: valid=%v, want %v (%v)", schemaErr == nil, tc.valid, schemaErr)
			}
		})
	}
	request := map[string]struct {
		mutate func(*ContextFabricResultFeedbackRequest)
		valid  bool
	}{
		"golden":               {func(*ContextFabricResultFeedbackRequest) {}, true},
		"both unset":           {func(r *ContextFabricResultFeedbackRequest) { r.Correct = ContextFabricResultFeedbackUnset }, true},
		"unknown value":        {func(r *ContextFabricResultFeedbackRequest) { r.Useful = "YES" }, false},
		"empty value":          {func(r *ContextFabricResultFeedbackRequest) { r.Correct = "" }, false},
		"wrong schema version": {func(r *ContextFabricResultFeedbackRequest) { r.SchemaVersion = ContextFabricResultFeedbackSchema }, false},
	}
	for name, tc := range request {
		t.Run("request/"+name, func(t *testing.T) {
			value := goldenResultFeedbackRequest()
			tc.mutate(&value)
			goErr := value.Validate()
			if (goErr == nil) != tc.valid {
				t.Fatalf("Go validator: valid=%v, want %v (%v)", goErr == nil, tc.valid, goErr)
			}
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			schemaErr := contractcheck.ValidateSerializedStrict("", feedbackRequestSchemaFile, data)
			if (schemaErr == nil) != tc.valid {
				t.Fatalf("schema: valid=%v, want %v (%v)", schemaErr == nil, tc.valid, schemaErr)
			}
		})
	}
}

// The parse refuses what the schema refuses and a Go value cannot show: a
// missing flag, a null, a key twice, an unknown key, trailing content.
func TestParseContextFabricResultFeedbackRequestRefusesWhatTheSchemaRefuses(t *testing.T) {
	for name, body := range map[string]string{
		"missing useful":     `{"schema_version":"context_fabric_result_feedback_request.v1","correct":"yes"}`,
		"null flag":          `{"schema_version":"context_fabric_result_feedback_request.v1","correct":null,"useful":"no"}`,
		"key twice":          `{"schema_version":"context_fabric_result_feedback_request.v1","correct":"yes","correct":"no","useful":"no"}`,
		"unknown key":        `{"schema_version":"context_fabric_result_feedback_request.v1","correct":"yes","useful":"no","comment":"x"}`,
		"trailing content":   `{"schema_version":"context_fabric_result_feedback_request.v1","correct":"yes","useful":"no"} {}`,
		"key in other case":  `{"schema_version":"context_fabric_result_feedback_request.v1","Correct":"yes","useful":"no"}`,
		"boolean flag":       `{"schema_version":"context_fabric_result_feedback_request.v1","correct":true,"useful":"no"}`,
		"not an object":      `[]`,
		"missing everything": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseContextFabricResultFeedbackRequest([]byte(body)); err == nil {
				t.Fatalf("parse accepted %s", body)
			}
			if err := contractcheck.ValidateSerializedStrict("", feedbackRequestSchemaFile, []byte(body)); err == nil && name != "key twice" && name != "trailing content" {
				t.Fatalf("schema accepted %s", body)
			}
		})
	}
	if _, err := ParseContextFabricResultFeedbackRequest([]byte(`{"schema_version":"context_fabric_result_feedback_request.v1","correct":"yes","useful":"no"}`)); err != nil {
		t.Fatalf("parse refused a valid body: %v", err)
	}
}
