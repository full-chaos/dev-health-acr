package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

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
