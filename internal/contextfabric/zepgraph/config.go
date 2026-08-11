package zepgraph

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

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

type api interface {
	GetGraph(context.Context, string) (*zep.Graph, error)
	CreateGraph(context.Context, *zep.CreateGraphRequest) (*zep.Graph, error)
	DeleteGraph(context.Context, string) error
	AddFactTriple(context.Context, *zep.AddTripleRequest) (*zep.AddTripleResponse, error)
	Search(context.Context, *zep.GraphSearchQuery) (*zep.GraphSearchResults, error)
	GetNode(context.Context, string) (*zep.EntityNode, error)
	DeleteNode(context.Context, string) error
	GetNodeEdges(context.Context, string) ([]*zep.EntityEdge, error)
	DeleteEdge(context.Context, string) error
}

type sdkAPI struct {
	client *zepclient.Client
}

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
	return s.client.Graph.Create(ctx, request)
}

func (s *sdkAPI) DeleteGraph(ctx context.Context, graphID string) error {
	_, err := s.client.Graph.Delete(ctx, graphID)
	return err
}

func (s *sdkAPI) AddFactTriple(ctx context.Context, request *zep.AddTripleRequest) (*zep.AddTripleResponse, error) {
	return s.client.Graph.AddFactTriple(ctx, request)
}

func (s *sdkAPI) Search(ctx context.Context, request *zep.GraphSearchQuery) (*zep.GraphSearchResults, error) {
	return s.client.Graph.Search(ctx, request)
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
