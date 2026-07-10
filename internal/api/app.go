package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type contextKey string

const requestIDContextKey contextKey = "request_id"

type ReadinessCheck interface {
	Name() string
	Check(context.Context) error
}

type CheckFunc struct {
	CheckName string
	Fn        func(context.Context) error
}

func (c CheckFunc) Name() string { return c.CheckName }
func (c CheckFunc) Check(ctx context.Context) error {
	if c.Fn == nil {
		return nil
	}
	return c.Fn(ctx)
}

type CapabilitiesProvider interface {
	Capabilities(context.Context, *http.Request) (contractsv1.Capabilities, error)
}

type StaticCapabilitiesProvider struct {
	Value contractsv1.Capabilities
	Now   func() time.Time
}

func (p StaticCapabilitiesProvider) Capabilities(_ context.Context, _ *http.Request) (contractsv1.Capabilities, error) {
	value := p.Value
	if p.Now != nil {
		value.GeneratedAt = p.Now().UTC()
	} else {
		value.GeneratedAt = time.Now().UTC()
	}
	return value, nil
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /readyz", a.handleReady)
	mux.HandleFunc("GET /api/v1/agent-context/capabilities", a.handleCapabilities)

	var handler http.Handler = mux
	handler = a.accessLogMiddleware(handler)
	handler = a.timeoutMiddleware(handler)
	handler = a.recoveryMiddleware(handler)
	handler = a.requestIDMiddleware(handler)
	return handler
}

type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Version string `json:"version"`
}

type readinessCheckResponse struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type readinessResponse struct {
	Status  string                   `json:"status"`
	Service string                   `json:"service"`
	Version string                   `json:"version"`
	Checks  []readinessCheckResponse `json:"checks"`
}

func (a *App) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status:  "ok",
		Service: a.config.ServiceName,
		Version: a.config.ServiceVersion,
	})
}

func (a *App) handleReady(w http.ResponseWriter, r *http.Request) {
	response := readinessResponse{
		Status:  "ready",
		Service: a.config.ServiceName,
		Version: a.config.ServiceVersion,
		Checks:  make([]readinessCheckResponse, 0, len(a.readinessChecks)),
	}
	status := http.StatusOK
	for _, check := range a.readinessChecks {
		checkStatus := "ready"
		if err := check.Check(r.Context()); err != nil {
			checkStatus = "not_ready"
			response.Status = "not_ready"
			status = http.StatusServiceUnavailable
			a.logger.WarnContext(r.Context(), "readiness check failed",
				"request_id", RequestID(r.Context()),
				"check", check.Name(),
				"error", err,
			)
		}
		response.Checks = append(response.Checks, readinessCheckResponse{Name: check.Name(), Status: checkStatus})
	}
	writeJSON(w, status, response)
}

func (a *App) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	capabilities, err := a.capabilities.Capabilities(r.Context(), r)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "capabilities resolution failed",
			"request_id", RequestID(r.Context()),
			"error", err,
		)
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Capabilities are temporarily unavailable", true, nil)
		return
	}
	writeJSON(w, http.StatusOK, capabilities)
}

func (a *App) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" || len(requestID) > 128 {
			requestID = a.requestID()
		}
		w.Header().Set("X-Request-ID", requestID)
		r.Header.Set("X-Request-ID", requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *App) timeoutMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), a.config.RequestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *App) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.ErrorContext(r.Context(), "request panic recovered",
					"request_id", RequestID(r.Context()),
					"panic", fmt.Sprint(recovered),
					"stack", string(debug.Stack()),
				)
				writeError(w, r, http.StatusInternalServerError, "internal_error", "Internal server error", false, nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (a *App) accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := a.now()
		wrapped := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(wrapped, r)
		a.logger.InfoContext(r.Context(), "request completed",
			"request_id", RequestID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrapped.status,
			"bytes", wrapped.bytes,
			"duration_ms", a.now().Sub(started).Milliseconds(),
		)
	})
}

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey).(string)
	return value
}

var fallbackRequestIDCounter atomic.Uint64

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "req_" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("req_fallback_%d_%d", time.Now().UnixNano(), fallbackRequestIDCounter.Add(1))
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, retryable bool, details map[string]any) {
	writeJSON(w, status, contractsv1.ErrorEnvelope{
		SchemaVersion: contractsv1.ErrorSchema,
		RequestID:     RequestID(r.Context()),
		Error: contractsv1.ErrorDetail{
			Code:       code,
			Message:    message,
			HTTPStatus: status,
			Retryable:  retryable,
			Details:    details,
		},
	})
}
