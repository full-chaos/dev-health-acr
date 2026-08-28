package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

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
	mux.Handle("POST "+ContextFabricInvestigationsPath, a.ContextFabricInvestigationHandler(a.investigator()))
	mux.Handle("GET "+ContextFabricInvestigationResultPath, a.ContextFabricInvestigationResultHandler(a.investigationResults()))
	mux.Handle("GET "+ContextFabricOrgModelConfigPath, a.ContextFabricOrgModelConfigGetHandler(a.orgModelConfigs()))
	mux.Handle("PUT "+ContextFabricOrgModelConfigPath, a.ContextFabricOrgModelConfigPutHandler(a.orgModelConfigs(), a.reuseInvalidator()))
	mux.Handle("DELETE "+ContextFabricOrgModelConfigPath, a.ContextFabricOrgModelConfigDeleteHandler(a.orgModelConfigs(), a.orgModelRuntimeEvictor(), a.reuseInvalidator()))
	mux.Handle("POST /api/v1/oauth/device_authorization", a.deviceRuntimeHandler(http.HandlerFunc(a.handleDeviceAuthorization)))
	mux.Handle("POST /api/v1/oauth/token", http.HandlerFunc(a.handleDeviceToken))
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
		// CodeQL go/log-injection (CHAOS-4355 response-bound follow-up,
		// alert #54): reject any caller-supplied header carrying a control
		// character (CR/LF included) BEFORE it can reach any log line,
		// rather than relying solely on observability.parseRequestID's
		// stricter req_+32-hex format check a few lines below to catch it
		// indirectly. Functionally the two guards agree today (a value
		// with an embedded control character can never match that strict
		// format either -- confirmed with a live repro sending
		// "evil\nFAKE_LOG_LINE=injected", which the pre-existing fallback
		// already replaced with a clean generated ID), but a scanner
		// tracing this function alone has no way to see that -- and
		// neither does a future reader who only reads THIS function. This
		// makes the guard explicit at the one point untrusted input enters
		// the request-ID pipeline, per CWE-117's own remediation ("remove
		// line breaks from user input").
		if requestID == "" || len(requestID) > 128 || !isSafeRequestIDHeaderValue(requestID) {
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

// isSafeRequestIDHeaderValue rejects any control character (CR/LF
// included), mirroring internal/limits/policy.go's validIdentifier check
// for the same class of caller-supplied identifier. A legitimate
// correlation ID needs no control characters; the request ID this
// function admits is echoed in the response header, threaded through
// every downstream log line and audit record via RequestID(ctx), and
// (CHAOS-4355) now also disclosed in Context Fabric response-budget error
// details -- it must never be able to forge a log entry (CWE-117).
func isSafeRequestIDHeaderValue(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
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
		// CHAOS-4330: `status` is whatever the handler decided BEFORE the
		// body write that can still fail (e.g. http.Server.WriteTimeout
		// firing mid-write on a slow investigation) -- it is not proof the
		// client actually received a complete response. Every field below
		// stays exactly as before; this only ADDS the one signal that lets
		// a genuinely-failed write be told apart from a real success, both
		// in this log line and in the observability snapshot, without
		// changing what `status` itself reports.
		fields := []any{
			"request_id", RequestID(r.Context()),
			"operation", requestOperation(r),
			"status", wrapped.status,
			"bytes", wrapped.bytes,
			"duration_ms", a.now().Sub(started).Milliseconds(),
		}
		level := slog.LevelInfo
		if wrapped.writeErr != nil {
			level = slog.LevelWarn
			// classifyWriteError, never wrapped.writeErr.Error() itself
			// (codex review, CHAOS-4330): this repo's own observability
			// rule (docs/observability.md) forbids raw error text as a
			// log/metric attribute -- a *net.OpError from a failed Write
			// can carry the remote address or other incidental transport
			// detail, the same class of leak that rule exists to prevent.
			fields = append(fields, "write_error", classifyWriteError(wrapped.writeErr))
		}
		a.logger.Log(r.Context(), level, "request completed", fields...)
		a.observability.ObserveRequest(r.Context(), requestObservation(requestOperation(r), wrapped.status, denialForError(wrapped.denialCode), a.now().Sub(started), wrapped.writeErr != nil))
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
