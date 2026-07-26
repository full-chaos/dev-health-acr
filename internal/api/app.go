package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/observability"
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

func (a *App) InstrumentedHandler(next http.Handler) http.Handler {
	handler := a.recoveryMiddleware(next)
	handler = a.timeoutMiddleware(handler)
	handler = a.accessLogMiddleware(handler)
	handler = a.requestIDMiddleware(handler)
	return handler
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /readyz", a.handleReady)
	mux.Handle("GET /api/v1/agent-context/capabilities", a.protectedRuntimeHandler(limits.RequestClassAuth, auth.ScopeContextRead, false, true, http.HandlerFunc(a.handleCapabilities)))
	mux.Handle("POST /api/v1/agent-context/context-packets", a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextRead, false, true, http.HandlerFunc(a.handleContextPacket)))
	mux.Handle("GET /api/v1/agent-context/evidence/{evidence_ref_id}", a.protectedRuntimeHandler(limits.RequestClassEvidence, auth.ScopeEvidenceRead, true, true, http.HandlerFunc(a.handleEvidence)))
	mux.Handle("POST /api/v1/agent-context/episodes", a.protectedRuntimeHandler(limits.RequestClassEpisode, auth.ScopeEpisodeWrite, true, false, http.HandlerFunc(a.handleEpisode)))
	mux.Handle("POST /api/v1/oauth/device_authorization", a.deviceRuntimeHandler(http.HandlerFunc(a.handleDeviceAuthorization)))
	mux.Handle("POST /api/v1/oauth/token", a.deviceRuntimeHandler(http.HandlerFunc(a.handleDeviceToken)))
	mux.Handle("POST /api/v1/oauth/device_approval", a.deviceRuntimeHandler(a.deviceApprovalHandler(http.HandlerFunc(a.handleDeviceApproval))))
	mux.Handle("POST /api/v1/auth/credentials/self/rotate", a.selfLifecycleHandler(http.HandlerFunc(a.handleRotateSelfCredential)))
	mux.Handle("POST /api/v1/auth/credentials/self/revoke", a.selfLifecycleHandler(http.HandlerFunc(a.handleRevokeSelfCredential)))
	return a.InstrumentedHandler(mux)
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
				"failure_class", "readiness_check",
			)
		}
		response.Checks = append(response.Checks, readinessCheckResponse{Name: check.Name(), Status: checkStatus})
	}
	writeJSON(w, status, response)
}

func (a *App) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" || len(requestID) > 128 {
			requestID = a.requestID()
		}
		telemetryContext := observability.WithRequestID(r.Context(), requestID)
		if _, ok := observability.RequestIDFromContext(telemetryContext); !ok {
			requestID = newRequestID()
			telemetryContext = observability.WithRequestID(r.Context(), requestID)
		}
		w.Header().Set("X-Request-ID", requestID)
		r.Header.Set("X-Request-ID", requestID)
		ctx := context.WithValue(telemetryContext, requestIDContextKey, requestID)
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
					"status", http.StatusInternalServerError,
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
			"operation", requestOperation(r),
			"status", wrapped.status,
			"bytes", wrapped.bytes,
			"duration_ms", a.now().Sub(started).Milliseconds(),
		)
		a.observability.ObserveRequest(r.Context(), requestObservation(requestOperation(r), wrapped.status, denialForError(wrapped.denialCode), a.now().Sub(started)))
	})
}

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey).(string)
	return value
}

var fallbackRequestIDCounter atomic.Uint64

func newRequestID() string {
	return newRequestIDFrom(rand.Reader, time.Now(), fallbackRequestIDCounter.Add(1))
}

func newRequestIDFrom(reader io.Reader, now time.Time, sequence uint64) string {
	var raw [16]byte
	if _, err := io.ReadFull(reader, raw[:]); err == nil {
		return "req_" + hex.EncodeToString(raw[:])
	}
	fallback := sha256.Sum256(fmt.Appendf(nil, "%d:%d", now.UnixNano(), sequence))
	return "req_" + hex.EncodeToString(fallback[:16])
}
