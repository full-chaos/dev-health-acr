package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/logsanitize"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/version"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The hosted transport's telemetry vocabularies are declared once, in
// internal/contextfabric/eventspec, beside the events that carry them. The
// names below are aliases so this package's code and its callers read them
// without a second list.

// Transport names.
const (
	TransportSTDIO = eventspec.MCPTransportSTDIO
	TransportHTTP  = eventspec.MCPTransportHTTP
)

// TransportVocabulary lists every transport acr-mcp serves.
func TransportVocabulary() []string { return eventspec.MCPTransportVocabulary() }

// Auth outcomes of one hosted HTTP request.
const (
	HTTPAuthAdmitted                = eventspec.MCPHTTPAuthAdmitted
	HTTPAuthMissingBearer           = eventspec.MCPHTTPAuthMissingBearer
	HTTPAuthMalformedBearer         = eventspec.MCPHTTPAuthMalformedBearer
	HTTPAuthInvalidCredential       = eventspec.MCPHTTPAuthInvalidCredential
	HTTPAuthInsufficientScope       = eventspec.MCPHTTPAuthInsufficientScope
	HTTPAuthInsufficientEntitlement = eventspec.MCPHTTPAuthInsufficientEntitlement
	HTTPAuthRateLimited             = eventspec.MCPHTTPAuthRateLimited
	HTTPAuthUpstreamIncompatible    = eventspec.MCPHTTPAuthUpstreamIncompatible
	HTTPAuthUpstreamUnavailable     = eventspec.MCPHTTPAuthUpstreamUnavailable
)

// HTTPAuthOutcomeVocabulary lists every auth outcome, admitted first.
func HTTPAuthOutcomeVocabulary() []string { return eventspec.MCPHTTPAuthOutcomeVocabulary() }

// Result classes of one hosted HTTP request.
const (
	HTTPResultOK                = eventspec.MCPHTTPResultOK
	HTTPResultToolError         = eventspec.MCPHTTPResultToolError
	HTTPResultProtocolError     = eventspec.MCPHTTPResultProtocolError
	HTTPResultAuthDenied        = eventspec.MCPHTTPResultAuthDenied
	HTTPResultAuthUnavailable   = eventspec.MCPHTTPResultAuthUnavailable
	HTTPResultTransportRejected = eventspec.MCPHTTPResultTransportRejected
)

// HTTPResultClassVocabulary lists every result class.
func HTTPResultClassVocabulary() []string { return eventspec.MCPHTTPResultClassVocabulary() }

// Principal classes.
const (
	PrincipalClassNone   = eventspec.MCPPrincipalClassNone
	PrincipalClassBearer = eventspec.MCPPrincipalClassBearer
)

// PrincipalClassVocabulary lists every principal class.
func PrincipalClassVocabulary() []string { return eventspec.MCPPrincipalClassVocabulary() }

// Buckets for untrusted request values.
const (
	httpValueNone        = eventspec.MCPHTTPValueNone
	httpValueOther       = eventspec.MCPHTTPValueOther
	httpValueUnspecified = eventspec.MCPHTTPValueUnspecified
)

// HTTPMethodVocabulary lists every MCP method name the request line records.
func HTTPMethodVocabulary() []string { return eventspec.MCPHTTPMethodVocabulary() }

// HTTPToolVocabulary lists every tool name the request line records.
func HTTPToolVocabulary() []string { return eventspec.MCPHTTPToolVocabulary() }

// HTTPProtocolRevisionVocabulary lists every protocol revision value the
// request line records.
func HTTPProtocolRevisionVocabulary() []string { return eventspec.MCPHTTPProtocolRevisionVocabulary() }

// Readiness states and failure classes.
const (
	ReadinessReady    = eventspec.MCPReadinessReady
	ReadinessNotReady = eventspec.MCPReadinessNotReady

	ReadinessFailureNone        = eventspec.MCPReadinessFailureNone
	ReadinessFailureUnreachable = eventspec.MCPReadinessFailureUnreachable
	ReadinessFailureTimeout     = eventspec.MCPReadinessFailureTimeout
	ReadinessFailureNotLive     = eventspec.MCPReadinessFailureNotLive
)

// ReadinessStateVocabulary lists every readiness state.
func ReadinessStateVocabulary() []string { return eventspec.MCPReadinessStateVocabulary() }

// ReadinessFailureVocabulary lists every readiness failure class.
func ReadinessFailureVocabulary() []string { return eventspec.MCPReadinessFailureVocabulary() }

// Log messages of the three lines the hosted transport emits.
const (
	HTTPRequestLogMessage   = eventspec.MCPHTTPRequestLogMessage
	HTTPServingLogMessage   = eventspec.MCPHTTPServingLogMessage
	HTTPReadinessLogMessage = eventspec.MCPHTTPReadinessLogMessage
)

// HealthPath and ReadyPath are the probe routes. They sit outside the MCP
// base path, carry no caller data, and need no credential.
const (
	HealthPath = "/healthz"
	ReadyPath  = "/readyz"
)

// requestIDHeader carries the correlation id in and out.
const requestIDHeader = "X-Request-ID"

// HTTPHandlerOptions configures NewHTTPHandler.
type HTTPHandlerOptions struct {
	// BasePath is the exact path the MCP endpoint answers on, e.g. "/mcp".
	BasePath string
	// Identity is the build identity reported as the server revision.
	Identity version.Info
	// MaxRequestBodyBytes bounds every MCP request body.
	MaxRequestBodyBytes int64
	// ResolveTimeout bounds the per-request credential decision (the hosted
	// capabilities call made with the caller's bearer).
	ResolveTimeout time.Duration
	// Now is the clock latency is measured with. Nil means time.Now.
	Now func() time.Time
}

// HTTPHandler is the hosted, stateless Streamable HTTP MCP endpoint plus its
// probe routes. It holds no session and no caller state between requests:
// every request is authenticated on its own bearer, gets its own caller
// context and its own server, and leaves nothing behind.
type HTTPHandler struct {
	cfg       *ProcessConfig
	opts      HTTPHandlerOptions
	logger    *slog.Logger
	mcp       http.Handler
	mux       *http.ServeMux
	now       func() time.Time
	refKey    []byte
	inFlight  atomic.Int64
	readiness readinessTracker
}

// ErrHTTPOptionsInvalid reports an unusable handler configuration.
var ErrHTTPOptionsInvalid = errors.New("mcp: invalid hosted HTTP transport options")

// NewHTTPHandler builds the hosted endpoint for a ProcessConfig made by
// NewHTTPProcessConfig.
func NewHTTPHandler(cfg *ProcessConfig, opts HTTPHandlerOptions) (*HTTPHandler, error) {
	if cfg == nil || cfg.hosted == nil || cfg.Transport() != TransportHTTP {
		return nil, ErrProcessConfigMissing
	}
	if !validBasePath(opts.BasePath) || opts.MaxRequestBodyBytes <= 0 || opts.ResolveTimeout <= 0 {
		return nil, ErrHTTPOptionsInvalid
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	h := &HTTPHandler{
		cfg:    cfg,
		opts:   opts,
		logger: cfg.Diagnostics(),
		now:    opts.Now,
		refKey: key,
	}
	h.mcp = mcpsdk.NewStreamableHTTPHandler(serverFromRequest, &mcpsdk.StreamableHTTPOptions{
		Stateless:           true,
		Logger:              h.logger,
		MaxRequestBodyBytes: opts.MaxRequestBodyBytes,
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+HealthPath, h.serveHealth)
	mux.HandleFunc("GET "+ReadyPath, h.serveReady)
	pattern := opts.BasePath
	if pattern == "/" {
		pattern = "/{$}"
	}
	mux.Handle(pattern, http.HandlerFunc(h.serveMCP))
	h.mux = mux
	return h, nil
}

// validBasePath accepts an absolute, clean path with no query, fragment,
// wildcard or trailing slash, and refuses the probe routes.
func validBasePath(path string) bool {
	if path == "" || path[0] != '/' || len(path) > 128 || path == HealthPath || path == ReadyPath {
		return false
	}
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		return false
	}
	for _, r := range path {
		isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !isAlnum && !strings.ContainsRune("/-_.", r) {
			return false
		}
	}
	return !strings.Contains(path, "//") && !strings.Contains(path, "/./") && !strings.Contains(path, "/../") && !strings.HasSuffix(path, "/..") && !strings.HasSuffix(path, "/.")
}

// ServeHTTP routes a request to the MCP endpoint or a probe.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// InFlight reports how many MCP requests are being served at this moment.
func (h *HTTPHandler) InFlight() int64 { return h.inFlight.Load() }

type healthBody struct {
	Status   string `json:"status"`
	InFlight int64  `json:"in_flight"`
}

func (h *HTTPHandler) serveHealth(w http.ResponseWriter, _ *http.Request) {
	writeProbe(w, http.StatusOK, healthBody{Status: "ok", InFlight: h.InFlight()})
}

// serveReady answers ready only when the hosted API's liveness route answers
// with the process configuration alone. It sends no credential and carries
// no caller data, and it logs a line only when the state changes.
func (h *HTTPHandler) serveReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.opts.ResolveTimeout)
	defer cancel()
	failure := readinessFailureClass(h.cfg.hosted.Reachable(ctx))
	state := ReadinessReady
	status := http.StatusOK
	if failure != ReadinessFailureNone {
		state, status = ReadinessNotReady, http.StatusServiceUnavailable
	}
	if sequence, previous, changed := h.readiness.observe(state); changed {
		h.logger.InfoContext(r.Context(), HTTPReadinessLogMessage,
			"sequence", sequence,
			"transport", TransportHTTP,
			"state", state,
			"previous_state", previous,
			"failure_class", failure,
		)
	}
	writeProbe(w, status, healthBody{Status: state, InFlight: h.InFlight()})
}

func readinessFailureClass(err error) string {
	switch {
	case err == nil:
		return ReadinessFailureNone
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return ReadinessFailureTimeout
	case errors.Is(err, sidecar.ErrHostedNotLive):
		return ReadinessFailureNotLive
	default:
		return ReadinessFailureUnreachable
	}
}

// readinessTracker remembers the last readiness state so the readiness line
// records transitions, not every probe.
type readinessTracker struct {
	mu       sync.Mutex
	last     string
	sequence int
}

func (t *readinessTracker) observe(state string) (sequence int, previous string, changed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	previous = t.last
	if previous == "" {
		previous = "unknown"
	}
	if t.last == state {
		return t.sequence, previous, false
	}
	t.last = state
	t.sequence++
	return t.sequence, previous, true
}

func writeProbe(w http.ResponseWriter, status int, body healthBody) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// requestRecord accumulates what one MCP request did, for its one line.
type requestRecord struct {
	mu          sync.Mutex
	method      string
	tool        string
	protocol    string
	protocolSet bool
	toolError   bool
	rpcError    bool
	methodsRun  int
}

type requestServerKey struct{}

// serverFromRequest is the SDK's getServer hook. The server was built for
// this request's authenticated caller before the SDK handler ran, so the hook
// only hands it back; it never resolves an identity itself and returns nil
// (a 400 from the SDK) for a request that reached it without one.
func serverFromRequest(r *http.Request) *mcpsdk.Server {
	server, _ := r.Context().Value(requestServerKey{}).(*mcpsdk.Server)
	return server
}

// recordMiddleware records the method, tool, negotiated revision and outcome
// of every MCP method the request ran, in bounded closed vocabularies.
func recordMiddleware(record *requestRecord) mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			result, err := next(ctx, method, req)
			record.observe(method, req, result, err)
			return result, err
		}
	}
}

func (rec *requestRecord) observe(method string, req mcpsdk.Request, result mcpsdk.Result, err error) {
	tool := httpValueNone
	var meta map[string]any
	if params := req.GetParams(); params != nil && !isNilValue(params) {
		meta = params.GetMeta()
		if call, ok := params.(*mcpsdk.CallToolParamsRaw); ok {
			tool = bucket(call.Name, HTTPToolVocabulary())
		}
	}
	protocol := ""
	if value, ok := meta[mcpsdk.MetaKeyProtocolVersion].(string); ok {
		protocol = value
	} else if session, ok := req.GetSession().(*mcpsdk.ServerSession); ok && session != nil {
		if params := session.InitializeParams(); params != nil {
			protocol = params.ProtocolVersion
		}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.methodsRun++
	if rec.methodsRun == 1 {
		rec.method = bucket(method, HTTPMethodVocabulary())
		rec.tool = tool
	}
	if protocol != "" && !rec.protocolSet {
		rec.protocol = bucket(protocol, HTTPProtocolRevisionVocabulary())
		rec.protocolSet = true
	}
	if err != nil {
		rec.rpcError = true
		return
	}
	if call, ok := result.(*mcpsdk.CallToolResult); ok && call != nil && call.IsError {
		rec.toolError = true
	}
}

func isNilValue(value any) bool {
	v := reflect.ValueOf(value)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// bucket maps an untrusted value onto a closed vocabulary. It returns the
// vocabulary's own member, never the caller's string, so what reaches a log
// line is always a value this package declared.
func bucket(value string, vocabulary []string) string {
	if value == httpValueNone || value == httpValueOther || value == httpValueUnspecified {
		return httpValueOther
	}
	for _, member := range vocabulary {
		if member == value {
			return member
		}
	}
	return httpValueOther
}

// serveMCP authenticates one request on its own bearer, builds that caller's
// server, runs the stateless SDK handler, and emits the request line. Every
// refusal happens before the SDK handler is invoked.
func (h *HTTPHandler) serveMCP(w http.ResponseWriter, r *http.Request) {
	started := h.now()
	inFlight := h.inFlight.Add(1)
	defer h.inFlight.Add(-1)

	requestID := inboundRequestID(r.Header.Get(requestIDHeader))
	if requestID == "" {
		requestID = newRequestID()
	}
	w.Header().Set(requestIDHeader, requestID)
	recorder := &statusRecorder{ResponseWriter: w}
	record := &requestRecord{method: httpValueNone, tool: httpValueNone}

	line := requestLine{
		requestID:      requestID,
		principalClass: PrincipalClassNone,
		authOutcome:    HTTPAuthAdmitted,
		inFlight:       inFlight,
	}
	defer func() {
		line.status = recorder.statusCode()
		line.latency = h.now().Sub(started)
		h.emitRequestLine(r.Context(), line, record, r.Header.Get("Mcp-Protocol-Version"), r.Header.Get("Mcp-Method"))
	}()

	bearer, presented := bearerFromRequest(r)
	if presented && auth.IsTokenShapeValid(bearer) {
		line.principalClass = PrincipalClassBearer
		line.principalRef = h.principalRef(bearer)
	}
	switch {
	case !presented:
		line.authOutcome = HTTPAuthMissingBearer
	case !auth.IsTokenShapeValid(bearer):
		line.authOutcome = HTTPAuthMalformedBearer
	}
	if line.authOutcome != HTTPAuthAdmitted {
		writeAuthRefusal(recorder, line.authOutcome, 0)
		return
	}

	resolveCtx, cancel := context.WithTimeout(r.Context(), h.opts.ResolveTimeout)
	caller, err := ResolveCaller(resolveCtx, h.cfg, CallerCredential{Bearer: bearer})
	cancel()
	if err != nil {
		outcome, retryAfter := classifyResolveFailure(err)
		line.authOutcome = outcome
		writeAuthRefusal(recorder, outcome, retryAfter)
		return
	}

	server := NewServerForCaller(h.cfg, caller, h.opts.Identity.Version)
	server.AddReceivingMiddleware(recordMiddleware(record))
	h.mcp.ServeHTTP(recorder, r.WithContext(context.WithValue(r.Context(), requestServerKey{}, server)))
}

// requestLine is the per-request telemetry state gathered by serveMCP.
type requestLine struct {
	requestID      string
	principalClass string
	principalRef   string
	authOutcome    string
	status         int
	latency        time.Duration
	inFlight       int64
}

// emitRequestLine writes the one line every MCP request produces, on every
// path: refused before the SDK, rejected by the transport, or served.
//
// When no MCP method ran (a refusal, or a request the SDK rejected before
// dispatch), method and protocol_revision are read from the request's
// Mcp-Method and Mcp-Protocol-Version headers, bucketed like every other
// caller-sent value.
func (h *HTTPHandler) emitRequestLine(ctx context.Context, line requestLine, record *requestRecord, protocolHeader, methodHeader string) {
	record.mu.Lock()
	method, tool, protocolSet, protocol := record.method, record.tool, record.protocolSet, record.protocol
	toolError, rpcError, methodsRun := record.toolError, record.rpcError, record.methodsRun
	record.mu.Unlock()
	if methodsRun == 0 && methodHeader != "" {
		method = bucket(methodHeader, HTTPMethodVocabulary())
	}
	if !protocolSet {
		protocol = httpValueUnspecified
		if protocolHeader != "" {
			protocol = bucket(protocolHeader, HTTPProtocolRevisionVocabulary())
		}
	}
	result := HTTPResultOK
	switch {
	case line.authOutcome == HTTPAuthUpstreamUnavailable || line.authOutcome == HTTPAuthUpstreamIncompatible:
		result = HTTPResultAuthUnavailable
	case line.authOutcome != HTTPAuthAdmitted:
		result = HTTPResultAuthDenied
	case rpcError:
		result = HTTPResultProtocolError
	case toolError:
		result = HTTPResultToolError
	case methodsRun == 0 && line.status/100 != 2:
		result = HTTPResultTransportRejected
	case line.status >= 400:
		result = HTTPResultProtocolError
	}
	args := []any{
		"request_id", logsanitize.SanitizeLogAttr(line.requestID),
		"transport", TransportHTTP,
		"server_version", h.opts.Identity.Version,
		"server_commit", h.opts.Identity.Commit,
		"protocol_revision", protocol,
		"method", method,
		"tool", tool,
		"principal_class", line.principalClass,
	}
	if line.principalClass == PrincipalClassBearer {
		args = append(args, "principal_ref", line.principalRef)
	}
	args = append(args,
		"auth_outcome", line.authOutcome,
		"result_class", result,
		"status", line.status,
		"latency_ms", line.latency.Milliseconds(),
		"in_flight", line.inFlight,
	)
	h.logger.InfoContext(ctx, HTTPRequestLogMessage, args...)
}

// principalRef is an opaque, per-process keyed digest of the bearer. It lets
// one process's lines be correlated per credential without writing the
// credential, its store lookup hash, or anything a reader could reverse.
func (h *HTTPHandler) principalRef(bearer string) string {
	mac := hmac.New(sha256.New, h.refKey)
	mac.Write([]byte(bearer))
	return hex.EncodeToString(mac.Sum(nil)[:8])
}

// bearerFromRequest returns the bearer of the one Authorization header the
// request carries. More than one Authorization header, or a scheme other than
// Bearer, is reported as presented but malformed (empty token).
func bearerFromRequest(r *http.Request) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) == 0 {
		return "", false
	}
	if len(values) > 1 {
		return "", true
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", true
	}
	return token, true
}

// classifyResolveFailure maps a caller-resolution failure to an auth outcome.
// Unknown, expired and revoked credentials are all "invalid_credential": the
// hosted API answers them identically, and so does this endpoint.
func classifyResolveFailure(err error) (string, time.Duration) {
	var apiErr *sidecar.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.HTTPStatus == http.StatusUnauthorized:
			return HTTPAuthInvalidCredential, 0
		case apiErr.HTTPStatus == http.StatusForbidden:
			return HTTPAuthInsufficientScope, 0
		case apiErr.HTTPStatus == http.StatusTooManyRequests:
			return HTTPAuthRateLimited, apiErr.RetryAfter
		case apiErr.HTTPStatus == http.StatusUpgradeRequired:
			return HTTPAuthUpstreamIncompatible, 0
		}
		return HTTPAuthUpstreamUnavailable, 0
	}
	var compat *compatError
	if errors.As(err, &compat) {
		if compat.category == "entitlement" {
			return HTTPAuthInsufficientEntitlement, 0
		}
		return HTTPAuthUpstreamIncompatible, 0
	}
	if errors.Is(err, ErrCallerCredentialInvalid) {
		return HTTPAuthMalformedBearer, 0
	}
	return HTTPAuthUpstreamUnavailable, 0
}

// authRefusal is the fixed response for each refused outcome. Bodies carry
// only the outcome name and a fixed sentence.
var authRefusal = map[string]struct {
	status      int
	challenge   string
	description string
}{
	HTTPAuthMissingBearer:           {http.StatusUnauthorized, `Bearer`, "an ACR API bearer credential is required"},
	HTTPAuthMalformedBearer:         {http.StatusUnauthorized, `Bearer error="invalid_token"`, "the credential is not an ACR API bearer credential"},
	HTTPAuthInvalidCredential:       {http.StatusUnauthorized, `Bearer error="invalid_token"`, "the credential is unknown, expired or revoked"},
	HTTPAuthInsufficientScope:       {http.StatusForbidden, `Bearer error="insufficient_scope"`, "the credential lacks a required scope"},
	HTTPAuthInsufficientEntitlement: {http.StatusForbidden, `Bearer error="insufficient_scope"`, "the credential's organization or scopes do not enable the agent context runtime tools"},
	HTTPAuthRateLimited:             {http.StatusTooManyRequests, "", "too many authentication attempts"},
	HTTPAuthUpstreamIncompatible:    {http.StatusBadGateway, "", "the hosted API is incompatible with this server"},
	HTTPAuthUpstreamUnavailable:     {http.StatusServiceUnavailable, "", "the hosted API could not decide the credential"},
}

type refusalBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func writeAuthRefusal(w http.ResponseWriter, outcome string, retryAfter time.Duration) {
	refusal, ok := authRefusal[outcome]
	if !ok {
		refusal = authRefusal[HTTPAuthUpstreamUnavailable]
		outcome = HTTPAuthUpstreamUnavailable
	}
	if refusal.challenge != "" {
		w.Header().Set("WWW-Authenticate", refusal.challenge)
	}
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int((retryAfter+time.Second-1)/time.Second))))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(refusal.status)
	_ = json.NewEncoder(w).Encode(refusalBody{Error: outcome, ErrorDescription: refusal.description})
}

// inboundRequestID accepts a caller's correlation id only when it is a short
// token of safe characters; anything else is replaced, never logged.
func inboundRequestID(value string) string {
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, r := range value {
		isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !isAlnum && !strings.ContainsRune("-_.:", r) {
			return ""
		}
	}
	return value
}

func newRequestID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "mcp_unavailable"
	}
	return "mcp_" + hex.EncodeToString(buf)
}

// statusRecorder captures the response status while keeping the SDK's
// streaming (Flush) and response-controller access intact.
type statusRecorder struct {
	http.ResponseWriter
	mu     sync.Mutex
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.mu.Lock()
	if s.status == 0 {
		s.status = code
	}
	s.mu.Unlock()
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.mu.Lock()
	if s.status == 0 {
		s.status = http.StatusOK
	}
	s.mu.Unlock()
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusRecorder) statusCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == 0 {
		return http.StatusOK
	}
	return s.status
}
