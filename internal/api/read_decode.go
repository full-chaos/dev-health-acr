package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/limits"
)

var errTrailingJSON = errors.New("request body must contain exactly one JSON value")

func decodeJSONBody(w http.ResponseWriter, r *http.Request, maximum int64, target any) error {
	reader := http.MaxBytesReader(w, r.Body, maximum)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errTrailingJSON
	}
	return nil
}

func encodeBounded(value any, maximum int64) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if int64(len(encoded)) > maximum {
		return nil, limits.ErrResourceBudgetExceeded
	}
	return encoded, nil
}

func writeEncodedJSON(w http.ResponseWriter, status int, encoded []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

const (
	bodyReasonMalformedJSON         = "malformed_json"
	bodyReasonSchemaViolated        = "schema_violation"
	bodyReasonUnknownField          = "unknown_field"
	bodyReasonTrailingJSON          = "trailing_json"
	bodyReasonTooLarge              = "body_too_large"
	bodyReasonInvalidIdempotencyKey = "invalid_idempotency_key"
	bodyDetailField                 = "field"
	bodyFieldMaxLength              = 64
)

// invalidBodyDetails classifies an error from decodeJSONBody into the closed
// details.reason vocabulary, and names the failing field when the decoder
// reports one. The field is kept only when it has a closed identifier shape;
// no decoder message, Go type name or request value is carried.
func invalidBodyDetails(err error) map[string]any {
	if err == nil {
		return schemaViolationDetails()
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return map[string]any{"reason": bodyReasonTooLarge}
	case errors.Is(err, errTrailingJSON):
		return map[string]any{"reason": bodyReasonTrailingJSON}
	case errors.As(err, &syntaxErr), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return map[string]any{"reason": bodyReasonMalformedJSON}
	case errors.As(err, &typeErr):
		details := map[string]any{"reason": bodyReasonSchemaViolated}
		if field := safeBodyField(typeErr.Field); field != "" {
			details[bodyDetailField] = field
		}
		return details
	}
	if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		details := map[string]any{"reason": bodyReasonUnknownField}
		if unquoted, quoteErr := strconv.Unquote(name); quoteErr == nil {
			if field := safeBodyField(unquoted); field != "" {
				details[bodyDetailField] = field
			}
		}
		return details
	}
	return map[string]any{"reason": bodyReasonMalformedJSON}
}

// schemaViolationDetails is the details of a body that decoded but failed its
// shape or bounds check.
func bodyFieldDetails(field string) map[string]any {
	return map[string]any{"reason": bodyReasonSchemaViolated, bodyDetailField: field}
}

func schemaViolationDetails() map[string]any {
	return map[string]any{"reason": bodyReasonSchemaViolated}
}

func safeBodyField(field string) string {
	if field == "" || len(field) > bodyFieldMaxLength {
		return ""
	}
	for _, r := range field {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '.' {
			return ""
		}
	}
	return field
}
