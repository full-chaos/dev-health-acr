package directread

// QueryClient is acr-api's side of the internal call to the ops query
// service (CHAOS-7036 design E.6, BUILD ENTRY S1a "Internal call"):
//
//	POST <internal service URL>/query
//	{"query": <vendored document text>, "variables": {...}}
//
// with the four internal identity headers built from the principal and
// nothing else that names a caller. The header names are the ops contract
// in ops internal/queryapi/internalidentity/internalidentity.go:28-33 (at
// ops 9dffd5f77); all four must be present exactly once (:71-98) and the
// two flags must be exactly "true" or "false" (:100-108). The role is a
// pass-through string there (claims.Role = values[HeaderRole], :94); the ops
// side has no closed role vocabulary in that file, so acr sends the least
// role it knows, "viewer" (privileged roles in ops are admin, owner and
// operator: ops internal/queryapi/datahealth/datahealth.go:78).
//
// Rules this client keeps, each pinned by a test:
//
//   - NO Authorization header. ops refuses headers plus a bearer
//     (server/internal_auth.go:72-78), and acr never forwards a caller's bearer.
//   - No client header is ever forwarded: the request is built from
//     QueryCall alone, never from an inbound *http.Request.
//   - Superuser and impersonation are the constant "false".
//   - The request body is refused before sending when it exceeds 16 KiB,
//     the ops body limit (ops server/query_route.go:3110).
//   - 404 is CallOperationUnavailable (not registered OR routing row off;
//     acr cannot tell which) and is never retried on another path.
//   - An error never carries upstream body text, the URL or a transport
//     message: QueryError holds a closed class only.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/observability"
)

// Internal identity header names (ops internalidentity.go:29-34).
const (
	HeaderInternalOrgID               = "X-DH-Internal-Org-Id"
	HeaderInternalRole                = "X-DH-Internal-Role"
	HeaderInternalSuperuser           = "X-DH-Internal-Superuser"
	HeaderInternalImpersonationActive = "X-DH-Internal-Impersonation-Active"
	HeaderRequestID                   = "X-Request-Id"
)

// Constant identity values. Superuser and impersonation are never derived
// from anything: they are false in code.
const (
	InternalRoleLeast         = "viewer"
	internalSuperuserValue    = "false"
	internalImpersonatingFlag = "false"
	queryClientUserAgent      = "acr-api-data-operations"
)

// Size limits of the internal call.
const (
	// MaxQueryRequestBytes is the ops /query body limit (16 KiB).
	MaxQueryRequestBytes = 16 * 1024
	// MaxQueryResponseBytes bounds how much of an answer acr reads. The
	// serialized data is then measured against the caller's max_bytes.
	MaxQueryResponseBytes = 4 * 1024 * 1024
)

// QueryErrorClass is the closed vocabulary of internal-call failures.
type QueryErrorClass string

const (
	// QueryErrorRequestInvalid: acr refused to send (no org, body over the
	// ops limit, variables that do not encode).
	QueryErrorRequestInvalid QueryErrorClass = "request_invalid"
	// QueryErrorNotFound: the service answered 404.
	QueryErrorNotFound QueryErrorClass = "operation_unavailable"
	// QueryErrorTimeout: the deadline passed.
	QueryErrorTimeout QueryErrorClass = "timeout"
	// QueryErrorCanceled: the caller's context was canceled.
	QueryErrorCanceled QueryErrorClass = "canceled"
	// QueryErrorTransport: the request did not complete (dial, reset).
	QueryErrorTransport QueryErrorClass = "transport"
	// QueryErrorHTTPStatus: a non-2xx answer other than 404.
	QueryErrorHTTPStatus QueryErrorClass = "http_status"
	// QueryErrorResponseTooLarge: the answer exceeded MaxQueryResponseBytes.
	QueryErrorResponseTooLarge QueryErrorClass = "response_too_large"
)

// QueryErrorClassVocabulary is the closed set of internal-call failures.
func QueryErrorClassVocabulary() [7]QueryErrorClass {
	return [7]QueryErrorClass{QueryErrorRequestInvalid, QueryErrorNotFound, QueryErrorTimeout, QueryErrorCanceled, QueryErrorTransport, QueryErrorHTTPStatus, QueryErrorResponseTooLarge}
}

// QueryError is the only error a QueryClient returns. Its text is its class:
// never a URL, a host, a transport message or upstream body text.
type QueryError struct {
	Class QueryErrorClass
	// StatusCode is the HTTP status for QueryErrorNotFound and
	// QueryErrorHTTPStatus, else 0.
	StatusCode int
	// ReadBudget is set, on the MCP listener client only, when a non-2xx
	// answer carries the listener's typed read-budget refusal
	// (errors[].extensions.code MCP_READ_BUDGET_EXCEEDED, CHAOS-7085/7091):
	// the closed reason (ReadBudgetReasonVocabulary), never upstream text.
	ReadBudget ReadBudgetReason
	// ListenerRefusal is set, on the MCP listener client only, when a
	// non-2xx answer carries the listener's typed refusal
	// (errors[].extensions.code MCP_REFUSED, ops PR #3425): the closed class
	// of its reason (ListenerRefusalClassVocabulary), never upstream text.
	ListenerRefusal ListenerRefusalClass
	// ListenerReason is the listener's refusal reason when it is one of
	// the closed MCPListenerRefusalReasons, else "unknown" (logging only).
	ListenerReason string
}

// mcpListenerReasons is the MCP listener's closed refusal vocabulary (ops
// PR #3425 at c33b9f280, extensions.reason under code MCP_REFUSED), each
// with acr's class. carrier: the identity carrier or the org it names was
// refused (the 401 set, elevated_claim, and the two org-argument reasons,
// since acr sets every org value itself). precheck: acr validates exactly
// this before the wire, so the listener refusing it means acr's own check
// failed (logged as acr_precheck_gap). The rest are listener_refused.
var mcpListenerReasons = map[string]struct {
	class    ListenerRefusalClass
	precheck bool
}{
	// 401
	"authorization_header": {ListenerRefusalCarrier, false},
	"no_carrier":           {ListenerRefusalCarrier, false},
	"missing_header":       {ListenerRefusalCarrier, false},
	"duplicate_header":     {ListenerRefusalCarrier, false},
	"not_a_boolean":        {ListenerRefusalCarrier, false},
	"invalid_org":          {ListenerRefusalCarrier, false},
	// 403
	"elevated_claim":         {ListenerRefusalCarrier, false},
	"invalid_org_argument":   {ListenerRefusalCarrier, true},
	"org_mismatch":           {ListenerRefusalCarrier, true},
	"root_field_not_allowed": {ListenerRefusalQuery, true},
	// 405, 413, 415
	"method_not_allowed": {ListenerRefusalQuery, false},
	"not_a_query":        {ListenerRefusalQuery, true},
	"body_too_large":     {ListenerRefusalQuery, true},
	"content_type":       {ListenerRefusalQuery, false},
	// 400
	"bad_body":                {ListenerRefusalQuery, false},
	"unknown_body_field":      {ListenerRefusalQuery, false},
	"invalid_document":        {ListenerRefusalQuery, true},
	"operation_count":         {ListenerRefusalQuery, true},
	"operation_name_mismatch": {ListenerRefusalQuery, false},
	"introspection":           {ListenerRefusalQuery, true},
	"depth_limit":             {ListenerRefusalQuery, true},
	"alias_limit":             {ListenerRefusalQuery, true},
	"invalid_variables":       {ListenerRefusalQuery, true},
	"complexity_limit":        {ListenerRefusalQuery, true},
}

// MCPListenerRefusalReasons lists the listener's MCP_REFUSED reasons acr
// knows (404 root_field_not_enabled and off_mcp_listener are read from the
// status as operation_unavailable; the 422 ceilings are the read budget).
func MCPListenerRefusalReasons() []string {
	out := make([]string, 0, len(mcpListenerReasons))
	for r := range mcpListenerReasons {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// ListenerRefusalIsPrecheckGap reports whether a known listener reason is
// one acr checks itself before the wire.
func ListenerRefusalIsPrecheckGap(reason string) bool {
	return mcpListenerReasons[reason].precheck
}

// MCPRefusedCode is the extensions.code of every other MCP listener refusal.
const MCPRefusedCode = "MCP_REFUSED"

// ListenerRefusalClass is the closed class of an MCP listener refusal.
type ListenerRefusalClass string

const (
	// ListenerRefusalCarrier: the identity carrier was refused
	// (invalid_org, no_carrier, authorization_header, elevated_claim). acr
	// never sends such a request, so it is an acr defect or a tampered hop.
	ListenerRefusalCarrier ListenerRefusalClass = "carrier_refused"
	// ListenerRefusalQuery: the listener refused the query itself (a cap,
	// the allowlist, the document, the org arguments). acr refuses all of
	// these before the wire, so it is a drift between acr and the listener.
	ListenerRefusalQuery ListenerRefusalClass = "listener_refused"
)

// ListenerRefusalClassVocabulary is the closed set of listener refusal
// classes.
func ListenerRefusalClassVocabulary() [2]ListenerRefusalClass {
	return [2]ListenerRefusalClass{ListenerRefusalCarrier, ListenerRefusalQuery}
}

// listenerRefusalOf classifies an MCP_REFUSED answer by its reason with the
// closed map; an unknown reason is the query class, reported as "unknown".
// A body without MCP_REFUSED yields "", "".
func listenerRefusalOf(body []byte) (ListenerRefusalClass, string) {
	var answer struct {
		Errors []struct {
			Extensions struct {
				Code   string `json:"code"`
				Reason string `json:"reason"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &answer) != nil {
		return "", ""
	}
	for _, e := range answer.Errors {
		if e.Extensions.Code != MCPRefusedCode {
			continue
		}
		if known, ok := mcpListenerReasons[e.Extensions.Reason]; ok {
			return known.class, e.Extensions.Reason
		}
		return ListenerRefusalQuery, "unknown"
	}
	return "", ""
}

// Closed reasons of a listener 404 (ops mcp_route.go): a root the routing
// rows do not enable, a path other than POST /query, or anything else.
const (
	ListenerNotFoundRootNotEnabled = "root_field_not_enabled"
	ListenerNotFoundOffListener    = "off_mcp_listener"
	ListenerNotFoundUnknown        = "unknown"
)

// listenerNotFoundReasonOf reads a 404 body for the listener's MCP_REFUSED
// reason and keeps it only when it is one of the two known values; any other
// body is "unknown". Nothing else of the body is kept.
func listenerNotFoundReasonOf(body []byte) string {
	var answer struct {
		Errors []struct {
			Extensions struct {
				Code   string `json:"code"`
				Reason string `json:"reason"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &answer) != nil {
		return ListenerNotFoundUnknown
	}
	for _, e := range answer.Errors {
		if e.Extensions.Code != MCPRefusedCode {
			continue
		}
		switch e.Extensions.Reason {
		case ListenerNotFoundRootNotEnabled, ListenerNotFoundOffListener:
			return e.Extensions.Reason
		}
	}
	return ListenerNotFoundUnknown
}

// MCPReadBudgetExceededCode is the extensions.code GWC's MCP listener sets
// when a query breaches its ClickHouse bytes-read or time ceiling.
const MCPReadBudgetExceededCode = "MCP_READ_BUDGET_EXCEEDED"

// ReadBudgetReason is the closed sub-value of a read-budget refusal.
type ReadBudgetReason string

const (
	ReadBudgetBytes   ReadBudgetReason = "bytes_ceiling"
	ReadBudgetRows    ReadBudgetReason = "rows_ceiling"
	ReadBudgetTime    ReadBudgetReason = "time_ceiling"
	ReadBudgetUnknown ReadBudgetReason = "unknown"
)

// ReadBudgetReasonVocabulary is the closed set of read-budget reasons.
func ReadBudgetReasonVocabulary() [4]ReadBudgetReason {
	return [4]ReadBudgetReason{ReadBudgetBytes, ReadBudgetRows, ReadBudgetTime, ReadBudgetUnknown}
}

// ReadBudgetOf reads a GraphQL error list for the listener's read-budget
// code and returns its closed reason (extensions.reason bytes_ceiling or
// time_ceiling; anything else is unknown), or "" when the code is absent.
// Nothing else of the body is kept.
func ReadBudgetOf(body []byte) ReadBudgetReason {
	var answer struct {
		Errors []struct {
			Extensions struct {
				Code   string `json:"code"`
				Reason string `json:"reason"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &answer) != nil {
		return ""
	}
	for _, e := range answer.Errors {
		if e.Extensions.Code != MCPReadBudgetExceededCode {
			continue
		}
		switch ReadBudgetReason(e.Extensions.Reason) {
		case ReadBudgetBytes, ReadBudgetRows, ReadBudgetTime:
			return ReadBudgetReason(e.Extensions.Reason)
		default:
			return ReadBudgetUnknown
		}
	}
	return ""
}

func (e *QueryError) Error() string { return "query service call failed: " + string(e.Class) }

// QueryErrorClassOf returns the class of err, or "" when err is not a
// QueryError.
func QueryErrorClassOf(err error) QueryErrorClass {
	var qe *QueryError
	if errors.As(err, &qe) {
		return qe.Class
	}
	return ""
}

// QueryCall is one internal call. OrgID comes from the principal only.
type QueryCall struct {
	OrgID     string
	Document  string
	Variables map[string]any
}

// QueryResult is the raw GraphQL answer body (status 2xx), at most
// MaxQueryResponseBytes long.
type QueryResult struct {
	Body       []byte
	StatusCode int
}

// QueryClient calls the internal ops query service.
type QueryClient interface {
	Execute(ctx context.Context, call QueryCall) (QueryResult, error)
}

// HTTPQueryClient is the production QueryClient.
type HTTPQueryClient struct {
	endpoint  string
	userAgent string
	// readsTypedRefusals: the MCP listener client reads a bounded error
	// body to recognise the read-budget refusal.
	readsTypedRefusals bool
	timeout            time.Duration
	client             *http.Client
}

// ErrQueryClientConfig is returned by NewHTTPQueryClient for an unusable
// base URL or timeout. It never echoes the URL.
var ErrQueryClientConfig = errors.New("query service client: invalid configuration")

// NewHTTPQueryClient builds the client for baseURL (config.DataQueryURL)
// with the per-call deadline timeout (config.DataQueryTimeout). The
// transport keeps connections alive and bounds every phase; it uses no
// proxy from the environment and follows no redirect.
func NewHTTPQueryClient(baseURL string, timeout time.Duration) (*HTTPQueryClient, error) {
	return newHTTPQueryClient(baseURL, timeout, queryEndpointPath, queryClientUserAgent)
}

// NewHTTPQueryClientWithPath is NewHTTPQueryClient posting to path (config.DataQueryPath) instead of the default "/query". An empty path is the default;
// a path that is not an absolute plain path (plainQueryPath) is refused with ErrQueryClientConfig. Nothing else about the client changes.
func NewHTTPQueryClientWithPath(baseURL string, timeout time.Duration, path string) (*HTTPQueryClient, error) {
	if path == "" {
		path = queryEndpointPath
	}
	if !plainQueryPath(path) {
		return nil, ErrQueryClientConfig
	}
	return newHTTPQueryClient(baseURL, timeout, path, queryClientUserAgent)
}

// plainQueryPath is the rule of config.ValidateDataQueryPath (ACR_DATA_QUERY_PATH), repeated here so this package does not import config: an absolute
// path of plain segments (letters, digits, . _ ~ -), none empty, "." or "..", at most 200 characters. internal/runtime/hosted pins that the two agree.
func plainQueryPath(path string) bool {
	if len(path) > 200 || path == "" || path[0] != '/' {
		return false
	}
	for _, segment := range strings.Split(path[1:], "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for i := 0; i < len(segment); i++ {
			c := segment[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '~' || c == '-') {
				return false
			}
		}
	}
	return true
}

// Endpoint paths of the two internal listeners.
const (
	// queryEndpointPath is the registered-document route (run_operation).
	queryEndpointPath = "/query"
	// GraphQLListenerPath is the route of GWC's MCP listener (CHAOS-7085:
	// its own port, 8092 in the prod Service, POST /query only).
	GraphQLListenerPath    = "/query"
	graphqlClientUserAgent = "acr-api-graphql-query"
)

// NewHTTPGraphQLClient builds the graphql_query client for GWC's MCP
// listener at baseURL (config.DataGraphQLURL). Every rule of
// NewHTTPQueryClient holds: the four identity headers built from the call
// alone, no Authorization header, superuser and impersonation "false", the
// 16 KiB body limit, no proxy, no redirect.
func NewHTTPGraphQLClient(baseURL string, timeout time.Duration) (*HTTPQueryClient, error) {
	c, err := newHTTPQueryClient(baseURL, timeout, GraphQLListenerPath, graphqlClientUserAgent)
	if err != nil {
		return nil, err
	}
	c.readsTypedRefusals = true
	return c, nil
}

// NewHTTPGraphQLClientWithHTTP is NewHTTPGraphQLClient over a
// caller-supplied *http.Client. The redirect rule is applied to a copy.
func NewHTTPGraphQLClientWithHTTP(baseURL string, timeout time.Duration, httpClient *http.Client) (*HTTPQueryClient, error) {
	c, err := NewHTTPGraphQLClient(baseURL, timeout)
	if err != nil {
		return nil, err
	}
	if httpClient != nil {
		copied := *httpClient
		copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		c.client = &copied
	}
	return c, nil
}

func newHTTPQueryClient(baseURL string, timeout time.Duration, path, userAgent string) (*HTTPQueryClient, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrQueryClientConfig
	}
	if timeout <= 0 {
		return nil, ErrQueryClientConfig
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
		// No Accept-Encoding is added, so the wire header set is exactly
		// what Execute writes.
		DisableCompression: true,
	}
	return &HTTPQueryClient{
		endpoint:  u.String(),
		userAgent: userAgent,
		timeout:   timeout,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// NewHTTPQueryClientWithHTTP is NewHTTPQueryClient over a caller-supplied
// *http.Client (tests use an httptest server's client). The redirect rule is
// applied to a copy.
func NewHTTPQueryClientWithHTTP(baseURL string, timeout time.Duration, httpClient *http.Client) (*HTTPQueryClient, error) {
	c, err := NewHTTPQueryClient(baseURL, timeout)
	if err != nil {
		return nil, err
	}
	if httpClient != nil {
		copied := *httpClient
		copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		c.client = &copied
	}
	return c, nil
}

type queryBody struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// EncodeQueryBody is the exact body Execute sends.
func EncodeQueryBody(call QueryCall) ([]byte, error) {
	vars := call.Variables
	if vars == nil {
		vars = map[string]any{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(queryBody{Query: call.Document, Variables: vars}); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Execute sends one call. Every failure is a *QueryError.
func (c *HTTPQueryClient) Execute(ctx context.Context, call QueryCall) (QueryResult, error) {
	if c == nil || c.client == nil {
		return QueryResult{}, &QueryError{Class: QueryErrorTransport}
	}
	if strings.TrimSpace(call.OrgID) == "" || strings.TrimSpace(call.Document) == "" {
		return QueryResult{}, &QueryError{Class: QueryErrorRequestInvalid}
	}
	body, err := EncodeQueryBody(call)
	if err != nil || len(body) > MaxQueryRequestBytes {
		return QueryResult{}, &QueryError{Class: QueryErrorRequestInvalid}
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return QueryResult{}, &QueryError{Class: QueryErrorRequestInvalid}
	}
	// The header set is built here and nowhere else. Header values are set
	// with direct map writes so their canonical names are exactly the ops
	// constants.
	req.Header = http.Header{}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set(HeaderInternalOrgID, call.OrgID)
	req.Header.Set(HeaderInternalRole, InternalRoleLeast)
	req.Header.Set(HeaderInternalSuperuser, internalSuperuserValue)
	req.Header.Set(HeaderInternalImpersonationActive, internalImpersonatingFlag)
	if requestID, ok := observability.RequestIDFromContext(ctx); ok && string(requestID) != "" {
		req.Header.Set(HeaderRequestID, string(requestID))
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return QueryResult{}, &QueryError{Class: classifyTransportError(ctx, err)}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		qerr := &QueryError{Class: QueryErrorNotFound, StatusCode: resp.StatusCode}
		if c.readsTypedRefusals {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			qerr.ListenerReason = listenerNotFoundReasonOf(body)
		}
		drain(resp.Body)
		return QueryResult{}, qerr
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		qerr := &QueryError{Class: QueryErrorHTTPStatus, StatusCode: resp.StatusCode}
		if c.readsTypedRefusals {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			qerr.ReadBudget = ReadBudgetOf(body)
			qerr.ListenerRefusal, qerr.ListenerReason = listenerRefusalOf(body)
		}
		drain(resp.Body)
		return QueryResult{}, qerr
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxQueryResponseBytes+1))
	if err != nil {
		return QueryResult{}, &QueryError{Class: classifyTransportError(ctx, err)}
	}
	if len(data) > MaxQueryResponseBytes {
		return QueryResult{}, &QueryError{Class: QueryErrorResponseTooLarge}
	}
	return QueryResult{Body: data, StatusCode: resp.StatusCode}, nil
}

func drain(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64*1024))
}

func classifyTransportError(ctx context.Context, err error) QueryErrorClass {
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return QueryErrorTimeout
	case errors.As(err, &netErr) && netErr.Timeout():
		return QueryErrorTimeout
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return QueryErrorCanceled
	default:
		return QueryErrorTransport
	}
}
