package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

// Environment names of the serve contract. Every one has a matching
// `acr-mcp serve` flag; a flag given on the command line wins.
const (
	TransportEnvironment             = "ACR_MCP_TRANSPORT"
	HTTPListenEnvironment            = "ACR_MCP_HTTP_LISTEN"
	HTTPBasePathEnvironment          = "ACR_MCP_HTTP_BASE_PATH"
	HTTPReadHeaderTimeoutEnvironment = "ACR_MCP_HTTP_READ_HEADER_TIMEOUT"
	HTTPReadTimeoutEnvironment       = "ACR_MCP_HTTP_READ_TIMEOUT"
	HTTPWriteTimeoutEnvironment      = "ACR_MCP_HTTP_WRITE_TIMEOUT"
	HTTPIdleTimeoutEnvironment       = "ACR_MCP_HTTP_IDLE_TIMEOUT"
	HTTPShutdownTimeoutEnvironment   = "ACR_MCP_HTTP_SHUTDOWN_TIMEOUT"
	HTTPMaxBodyBytesEnvironment      = "ACR_MCP_HTTP_MAX_BODY_BYTES"
	// ResourceURLEnvironment is this endpoint's public URL, its OAuth
	// protected resource identifier (RFC 9728, RFC 8707).
	ResourceURLEnvironment = "ACR_MCP_RESOURCE_URL"
	// AuthorizationServerEnvironment is the issuer of the authorization
	// server that mints credentials for this resource (the acr-api public
	// origin).
	AuthorizationServerEnvironment = "ACR_MCP_AUTHORIZATION_SERVER"
)

// Defaults of the serve contract.
const (
	DefaultHTTPListen            = ":8081"
	DefaultHTTPBasePath          = "/mcp"
	DefaultHTTPReadHeaderTimeout = 10 * time.Second
	DefaultHTTPReadTimeout       = 30 * time.Second
	DefaultHTTPWriteTimeout      = 300 * time.Second
	DefaultHTTPIdleTimeout       = 120 * time.Second
	DefaultHTTPShutdownTimeout   = 30 * time.Second
	DefaultHTTPMaxBodyBytes      = int64(1 << 20)

	maxHTTPTimeout      = time.Hour
	maxHTTPMaxBodyBytes = int64(16 << 20)
)

// ServeOptions is the resolved `acr-mcp serve` configuration.
type ServeOptions struct {
	Transport         string
	Listen            string
	BasePath          string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxBodyBytes      int64
	// ResourceURL and AuthorizationServer are set together or not at all.
	// Set, the endpoint advertises OAuth discovery (protected resource
	// metadata and a resource_metadata challenge) and forwards ResourceURL
	// on every hosted API call so a credential bound to another resource is
	// refused.
	ResourceURL         string
	AuthorizationServer string
}

// ErrServeOptionInvalid reports a serve setting that is out of range or
// unparseable. Its message names the setting, never the value.
type ErrServeOptionInvalid struct{ Setting string }

func (e *ErrServeOptionInvalid) Error() string {
	return "acr-mcp: serve setting " + e.Setting + " is invalid"
}

// DefaultServeOptions is the contract's defaults, STDIO transport.
func DefaultServeOptions() ServeOptions {
	return ServeOptions{
		Transport:         TransportSTDIO,
		Listen:            DefaultHTTPListen,
		BasePath:          DefaultHTTPBasePath,
		ReadHeaderTimeout: DefaultHTTPReadHeaderTimeout,
		ReadTimeout:       DefaultHTTPReadTimeout,
		WriteTimeout:      DefaultHTTPWriteTimeout,
		IdleTimeout:       DefaultHTTPIdleTimeout,
		ShutdownTimeout:   DefaultHTTPShutdownTimeout,
		MaxBodyBytes:      DefaultHTTPMaxBodyBytes,
	}
}

// ServeOptionsFromEnvironment applies the ACR_MCP_* environment over the
// defaults. lookup is os.LookupEnv in production.
func ServeOptionsFromEnvironment(lookup func(string) (string, bool)) (ServeOptions, error) {
	opts := DefaultServeOptions()
	if lookup == nil {
		lookup = os.LookupEnv
	}
	set := func(name string, apply func(string) error) error {
		value, ok := lookup(name)
		if !ok || strings.TrimSpace(value) == "" {
			return nil
		}
		if err := apply(strings.TrimSpace(value)); err != nil {
			return &ErrServeOptionInvalid{Setting: name}
		}
		return nil
	}
	durations := []struct {
		name   string
		target *time.Duration
	}{
		{HTTPReadHeaderTimeoutEnvironment, &opts.ReadHeaderTimeout},
		{HTTPReadTimeoutEnvironment, &opts.ReadTimeout},
		{HTTPWriteTimeoutEnvironment, &opts.WriteTimeout},
		{HTTPIdleTimeoutEnvironment, &opts.IdleTimeout},
		{HTTPShutdownTimeoutEnvironment, &opts.ShutdownTimeout},
	}
	steps := []error{
		set(TransportEnvironment, func(v string) error { opts.Transport = v; return nil }),
		set(HTTPListenEnvironment, func(v string) error { opts.Listen = v; return nil }),
		set(HTTPBasePathEnvironment, func(v string) error { opts.BasePath = v; return nil }),
		set(ResourceURLEnvironment, func(v string) error { opts.ResourceURL = v; return nil }),
		set(AuthorizationServerEnvironment, func(v string) error { opts.AuthorizationServer = v; return nil }),
		set(HTTPMaxBodyBytesEnvironment, func(v string) error {
			n, err := strconv.ParseInt(v, 10, 64)
			opts.MaxBodyBytes = n
			return err
		}),
	}
	for _, d := range durations {
		target := d.target
		steps = append(steps, set(d.name, func(v string) error {
			parsed, err := time.ParseDuration(v)
			*target = parsed
			return err
		}))
	}
	if err := firstServeOptionError(steps); err != nil {
		return ServeOptions{}, err
	}
	return opts, nil
}

func firstServeOptionError(errs []error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Validate checks every setting is in range. The STDIO transport ignores the
// HTTP settings, but they are still validated so a bad value is reported
// before a deployment switches transport.
func (o ServeOptions) Validate() error {
	if o.Transport != TransportSTDIO && o.Transport != TransportHTTP {
		return &ErrServeOptionInvalid{Setting: TransportEnvironment}
	}
	if _, port, err := net.SplitHostPort(o.Listen); err != nil || port == "" {
		return &ErrServeOptionInvalid{Setting: HTTPListenEnvironment}
	} else if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return &ErrServeOptionInvalid{Setting: HTTPListenEnvironment}
	}
	if !validBasePath(o.BasePath) {
		return &ErrServeOptionInvalid{Setting: HTTPBasePathEnvironment}
	}
	for _, d := range []struct {
		name  string
		value time.Duration
	}{
		{HTTPReadHeaderTimeoutEnvironment, o.ReadHeaderTimeout},
		{HTTPReadTimeoutEnvironment, o.ReadTimeout},
		{HTTPWriteTimeoutEnvironment, o.WriteTimeout},
		{HTTPIdleTimeoutEnvironment, o.IdleTimeout},
		{HTTPShutdownTimeoutEnvironment, o.ShutdownTimeout},
	} {
		if d.value <= 0 || d.value > maxHTTPTimeout {
			return &ErrServeOptionInvalid{Setting: d.name}
		}
	}
	if o.MaxBodyBytes <= 0 || o.MaxBodyBytes > maxHTTPMaxBodyBytes {
		return &ErrServeOptionInvalid{Setting: HTTPMaxBodyBytesEnvironment}
	}
	return validateOAuthDiscovery(o)
}

// ServeHTTPTransport runs the hosted Streamable HTTP server until ctx is
// cancelled, then shuts down gracefully within opts.ShutdownTimeout. It needs
// ACR_API_URL (and the other ACR_API_* settings) but no process credential:
// every hosted call carries the calling client's own bearer.
func ServeHTTPTransport(ctx context.Context, diagnostics io.Writer, identity version.Info, opts ServeOptions) error {
	if err := opts.Validate(); err != nil {
		fmt.Fprintf(diagnostics, "acr-mcp: startup failed: %s\n", err.Error())
		return err
	}
	sidecarCfg, err := sidecar.LoadConfig()
	if err != nil {
		wrapped := fmt.Errorf("configuration: %s", sidecar.DescribeConfigError(err))
		fmt.Fprintf(diagnostics, "acr-mcp: startup failed: %s\n", wrapped.Error())
		return wrapped
	}
	cfg, err := newServeProcessConfig(sidecarCfg, identity, diagnostics, opts)
	if err != nil {
		fmt.Fprintf(diagnostics, "acr-mcp: startup failed: %s\n", classify(err).Error())
		return err
	}
	listener, err := net.Listen("tcp", opts.Listen)
	if err != nil {
		fmt.Fprintf(diagnostics, "acr-mcp: startup failed: the listen address could not be bound\n")
		return err
	}
	return serveHTTPOn(ctx, listener, cfg, identity, opts)
}

// ProbeHostedLiveness checks the hosted API's liveness route with the process
// configuration alone, exactly as /readyz does. It needs no credential.
func ProbeHostedLiveness(ctx context.Context, identity version.Info) error {
	sidecarCfg, err := sidecar.LoadConfig()
	if err != nil {
		return fmt.Errorf("configuration: %s", sidecar.DescribeConfigError(err))
	}
	cfg, err := NewHTTPProcessConfig(sidecarCfg, identity, io.Discard)
	if err != nil {
		return err
	}
	return cfg.hosted.Reachable(ctx)
}

// serveHTTPOn serves on an already-bound listener. It owns the listener.
func serveHTTPOn(ctx context.Context, listener net.Listener, cfg *ProcessConfig, identity version.Info, opts ServeOptions) error {
	logger := cfg.Diagnostics()
	handler, err := NewHTTPHandler(cfg, serveHandlerOptions(cfg, identity, opts))
	if err != nil {
		_ = listener.Close()
		logger.ErrorContext(ctx, "acr-mcp http startup failed", "failure_class", "handler_options")
		return err
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		ReadTimeout:       opts.ReadTimeout,
		WriteTimeout:      opts.WriteTimeout,
		IdleTimeout:       opts.IdleTimeout,
		MaxHeaderBytes:    64 << 10,
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	logger.InfoContext(ctx, HTTPServingLogMessage,
		"listen_address", listener.Addr().String(),
		"transport", TransportHTTP,
		"base_path", opts.BasePath,
		"server_version", identity.Version,
		"server_commit", identity.Commit,
		"protocol_revisions", ProtocolRevisions(TransportHTTP),
		"max_body_bytes", opts.MaxBodyBytes,
	)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		logger.ErrorContext(ctx, "acr-mcp http serve exited", "failure_class", "serve_error")
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opts.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.ErrorContext(ctx, "acr-mcp http shutdown incomplete", "failure_class", "shutdown_timeout", "in_flight", handler.InFlight())
		_ = server.Close()
		return err
	}
	<-serveErr
	return nil
}

// newServeProcessConfig builds the hosted process configuration for a serve
// command: the sidecar configuration plus the resource identifier every
// hosted API call forwards.
func newServeProcessConfig(sidecarCfg sidecar.Config, identity version.Info, diagnostics io.Writer, opts ServeOptions) (*ProcessConfig, error) {
	sidecarCfg.Resource = opts.ResourceURL
	return NewHTTPProcessConfig(sidecarCfg, identity, diagnostics)
}

// serveHandlerOptions maps serve options onto the handler's options.
func serveHandlerOptions(cfg *ProcessConfig, identity version.Info, opts ServeOptions) HTTPHandlerOptions {
	return HTTPHandlerOptions{
		BasePath:            opts.BasePath,
		Identity:            identity,
		MaxRequestBodyBytes: opts.MaxBodyBytes,
		ResolveTimeout:      cfg.Config.Timeout,
		ResourceURL:         opts.ResourceURL,
		AuthorizationServer: opts.AuthorizationServer,
	}
}

// NewServeHTTPHandler builds the endpoint exactly as ServeHTTPTransport does,
// without binding a listener.
func NewServeHTTPHandler(sidecarCfg sidecar.Config, identity version.Info, diagnostics io.Writer, opts ServeOptions) (*HTTPHandler, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	cfg, err := newServeProcessConfig(sidecarCfg, identity, diagnostics, opts)
	if err != nil {
		return nil, err
	}
	return NewHTTPHandler(cfg, serveHandlerOptions(cfg, identity, opts))
}
