package zepgraph

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	acrconfig "github.com/full-chaos/dev-health-acr/internal/config"
	zep "github.com/getzep/zep-go/v3"
	zepclient "github.com/getzep/zep-go/v3/client"
	zepcore "github.com/getzep/zep-go/v3/core"
	zepoption "github.com/getzep/zep-go/v3/option"
)

const (
	SDKModule  = "github.com/getzep/zep-go/v3"
	SDKVersion = "v3.22.0"
)

var (
	ErrNotFound     = errors.New("context fabric graph record not found")
	ErrUnauthorized = errors.New("context fabric graph request unauthorized")
	ErrRateLimited  = errors.New("context fabric graph request rate limited")
)

type Config struct {
	BaseURL        string
	APIKey         string
	GraphPrefix    string
	RequestTimeout time.Duration
	MaxAttempts    uint
	MaxResults     int
	AllowInsecure  bool
}

func (c Config) validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("zep base URL is required")
	}
	parsed, err := url.Parse(c.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("zep base URL must be an absolute URL")
	}
	if parsed.Scheme != "https" && !c.AllowInsecure {
		return errors.New("zep base URL must use https")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return errors.New("zep API key is required")
	}
	if c.RequestTimeout < time.Second || c.RequestTimeout > 2*time.Minute {
		return errors.New("zep request timeout must be between one second and two minutes")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 5 {
		return errors.New("zep max attempts must be between one and five")
	}
	if c.MaxResults < 1 || c.MaxResults > 50 {
		return errors.New("zep max results must be between one and fifty")
	}
	if strings.TrimSpace(c.GraphPrefix) == "" || len(c.GraphPrefix) > 32 {
		return errors.New("zep graph prefix is required and must be bounded")
	}
	return nil
}

// Environment variable names for ConfigFromEnv, matching the ACR_<COMPONENT>_
// naming and KEY / KEY_FILE secret convention used by internal/config. These
// are not read anywhere else in this repository yet: Context Fabric graph
// composition into the hosted runtime bundle is Reset 1 scope. Compose and
// Helm document these names (see docs/adr/0007) so the Reset 1 composition
// wiring and the deployment configuration agree without renegotiation.
const (
	EnvBaseURL        = "ACR_CONTEXT_FABRIC_ZEP_BASE_URL"
	EnvAPIKey         = "ACR_CONTEXT_FABRIC_ZEP_API_KEY"
	EnvGraphPrefix    = "ACR_CONTEXT_FABRIC_ZEP_GRAPH_PREFIX"
	EnvRequestTimeout = "ACR_CONTEXT_FABRIC_ZEP_REQUEST_TIMEOUT"
	EnvMaxAttempts    = "ACR_CONTEXT_FABRIC_ZEP_MAX_ATTEMPTS"
	EnvMaxResults     = "ACR_CONTEXT_FABRIC_ZEP_MAX_RESULTS"
	EnvAllowInsecure  = "ACR_CONTEXT_FABRIC_ZEP_ALLOW_INSECURE"
)

// Configured reports whether the environment selects the Zep adapter at all.
// Context Fabric's graph dependency is optional at the deployment level: an
// unset base URL means the caller should not construct a zepgraph.Adapter.
func Configured(lookup func(string) (string, bool)) bool {
	value, ok := lookup(EnvBaseURL)
	return ok && strings.TrimSpace(value) != ""
}

// ConfigFromEnv builds a Config from the process environment. It uses the
// same KEY / KEY_FILE secret convention as internal/config.SecretValue so the
// API key may be supplied directly (development) or via a mounted secret
// file (Compose/Kubernetes). Callers own deciding when to call this — see
// Configured — because an unset deployment should never fail closed over a
// dependency it did not opt into.
func ConfigFromEnv(lookup func(string) (string, bool)) (Config, error) {
	apiKey, err := acrconfig.SecretValue(lookup, EnvAPIKey)
	if err != nil {
		return Config{}, fmt.Errorf("zep API key: %w", err)
	}
	cfg := Config{
		BaseURL:     envString(lookup, EnvBaseURL, ""),
		APIKey:      apiKey,
		GraphPrefix: envString(lookup, EnvGraphPrefix, "acr-cf"),
	}
	if cfg.RequestTimeout, err = envDuration(lookup, EnvRequestTimeout, 30*time.Second); err != nil {
		return Config{}, err
	}
	maxAttempts, err := envUint(lookup, EnvMaxAttempts, 3)
	if err != nil {
		return Config{}, err
	}
	cfg.MaxAttempts = maxAttempts
	if cfg.MaxResults, err = envInt(lookup, EnvMaxResults, 25); err != nil {
		return Config{}, err
	}
	if cfg.AllowInsecure, err = envBool(lookup, EnvAllowInsecure, false); err != nil {
		return Config{}, err
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func envString(lookup func(string) (string, bool), key, fallback string) string {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func envDuration(lookup func(string) (string, bool), key string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration", key)
	}
	return parsed, nil
}

func envInt(lookup func(string) (string, bool), key string, fallback int) (int, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return parsed, nil
}

func envUint(lookup func(string) (string, bool), key string, fallback uint) (uint, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 8)
	if err != nil {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	return uint(parsed), nil
}

func envBool(lookup func(string) (string, bool), key string, fallback bool) (bool, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return parsed, nil
}

type api interface {
	GetGraph(context.Context, string) (*zep.Graph, error)
	CreateGraph(context.Context, *zep.CreateGraphRequest) (*zep.Graph, error)
	DeleteGraph(context.Context, string) error
	AddFactTriple(context.Context, *zep.AddTripleRequest) (*zep.AddTripleResponse, error)
	Search(context.Context, *zep.GraphSearchQuery) (*zep.GraphSearchResults, error)
	GetNode(context.Context, string) (*zep.EntityNode, error)
	DeleteNode(context.Context, string) error
	GetNodeEdges(context.Context, string) ([]*zep.EntityEdge, error)
	GetEdge(context.Context, string) (*zep.EntityEdge, error)
	DeleteEdge(context.Context, string) error
}

type sdkAPI struct {
	client *zepclient.Client
}

// suppressBodyRetry overrides the client's configured MaxAttempts to 1 for
// the body-bearing calls below (CreateGraph, AddFactTriple, Search). The
// pinned SDK version never rewinds http.Request.Body before a retry, so its
// internal retrier resends a request whose body reader is already drained
// -- an empty/truncated body on any retried attempt, not a genuine retry.
// That makes the client-level retry setting nondeterministic specifically
// for these three calls. Bodyless reads and deletes (GetGraph, DeleteGraph,
// GetNode, DeleteNode, GetNodeEdges, DeleteEdge) are unaffected and keep the
// configured bounded retries.
var suppressBodyRetry = zepoption.WithMaxAttempts(1)

func newSDKAPI(config Config) (api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	httpClient := &http.Client{Timeout: config.RequestTimeout}
	client := zepclient.NewClient(
		zepoption.WithBaseURL(strings.TrimRight(config.BaseURL, "/")),
		zepoption.WithAPIKey(config.APIKey),
		zepoption.WithHTTPClient(httpClient),
		zepoption.WithMaxAttempts(config.MaxAttempts),
	)
	return &sdkAPI{client: client}, nil
}

func (s *sdkAPI) GetGraph(ctx context.Context, graphID string) (*zep.Graph, error) {
	return s.client.Graph.Get(ctx, graphID)
}

func (s *sdkAPI) CreateGraph(ctx context.Context, request *zep.CreateGraphRequest) (*zep.Graph, error) {
	return s.client.Graph.Create(ctx, request, suppressBodyRetry)
}

func (s *sdkAPI) DeleteGraph(ctx context.Context, graphID string) error {
	_, err := s.client.Graph.Delete(ctx, graphID)
	return err
}

func (s *sdkAPI) AddFactTriple(ctx context.Context, request *zep.AddTripleRequest) (*zep.AddTripleResponse, error) {
	return s.client.Graph.AddFactTriple(ctx, request, suppressBodyRetry)
}

func (s *sdkAPI) Search(ctx context.Context, request *zep.GraphSearchQuery) (*zep.GraphSearchResults, error) {
	return s.client.Graph.Search(ctx, request, suppressBodyRetry)
}

func (s *sdkAPI) GetNode(ctx context.Context, nodeUUID string) (*zep.EntityNode, error) {
	return s.client.Graph.Node.Get(ctx, nodeUUID)
}

func (s *sdkAPI) DeleteNode(ctx context.Context, nodeUUID string) error {
	_, err := s.client.Graph.Node.Delete(ctx, nodeUUID)
	return err
}

func (s *sdkAPI) GetNodeEdges(ctx context.Context, nodeUUID string) ([]*zep.EntityEdge, error) {
	return s.client.Graph.Node.GetEdges(ctx, nodeUUID)
}

func (s *sdkAPI) GetEdge(ctx context.Context, edgeUUID string) (*zep.EntityEdge, error) {
	return s.client.Graph.Edge.Get(ctx, edgeUUID)
}

func (s *sdkAPI) DeleteEdge(ctx context.Context, edgeUUID string) error {
	_, err := s.client.Graph.Edge.Delete(ctx, edgeUUID)
	return err
}

func safeDependencyError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	status := zepStatusCode(err)
	switch status {
	case http.StatusNotFound:
		return fmt.Errorf("%s: %w", operation, ErrNotFound)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s: %w", operation, ErrUnauthorized)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%s: %w", operation, ErrRateLimited)
	default:
		return fmt.Errorf("%s: graph dependency unavailable", operation)
	}
}

func zepStatusCode(err error) int {
	if err == nil {
		return 0
	}
	var notFound *zep.NotFoundError
	if errors.As(err, &notFound) {
		return http.StatusNotFound
	}
	var badRequest *zep.BadRequestError
	if errors.As(err, &badRequest) {
		return http.StatusBadRequest
	}
	var forbidden *zep.ForbiddenError
	if errors.As(err, &forbidden) {
		return http.StatusForbidden
	}
	var conflict *zep.ConflictError
	if errors.As(err, &conflict) {
		return http.StatusConflict
	}
	var internal *zep.InternalServerError
	if errors.As(err, &internal) {
		return http.StatusInternalServerError
	}
	var apiError *zepcore.APIError
	if errors.As(err, &apiError) {
		return apiError.StatusCode
	}
	return 0
}
