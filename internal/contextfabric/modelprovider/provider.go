package modelprovider

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-go/llm"
	"github.com/openai/openai-go/option"
	"go.opentelemetry.io/otel"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// New builds the Context Fabric model runtime for cfg. On success the
// returned contextfabric.ModelRuntime is always non-nil, so a caller may
// assign it straight into an interface field without the typed-nil trap.
//
// ctx is used for the construction log line only; it is not retained for
// generation, which is bounded per attempt by Config.Timeout inside
// genkitruntime.
func New(ctx context.Context, cfg Config) (contextfabric.ModelRuntime, error) {
	// Deliberately NOT a direct `return newPrimaryRuntime(ctx, cfg)`: that
	// forwards a *genkitruntime.Runtime nil, on the error path, straight
	// into an interface-typed return value -- Go's classic typed-nil trap.
	// A nil *genkitruntime.Runtime wrapped in the contextfabric.ModelRuntime
	// interface is a NON-nil interface value (it carries the concrete
	// type, just a nil pointer inside it), so every existing "err != nil
	// implies runtime == nil" caller (composition callers included) would
	// see a non-nil ModelRuntime alongside the error. Converting to nil
	// explicitly on the error path keeps New's existing contract byte for
	// byte identical to before this function's NewGenkitRuntime split.
	runtime, err := newPrimaryRuntime(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return runtime, nil
}

// NewGenkitRuntime is New's concrete-type counterpart (CHAOS-4631). It
// builds the SAME primary runtime New does -- identical validation,
// identical shared-client construction, identical fallback wiring -- but
// returns the concrete *genkitruntime.Runtime instead of the
// contextfabric.ModelRuntime interface.
//
// The concrete type is required only because InterpretQuestionForSample
// (genkitruntime's CHAOS-4631 measurement entry point, which takes a
// sample index) is deliberately NOT part of contextfabric.ModelRuntime --
// adding it there would widen an interface every production ModelRuntime
// caller (RuntimeQuestionInterpreter, the fallback chain) depends on, for
// a method only a measurement harness ever calls. NewGenkitRuntime exists
// so that harness (cmd/acr-interpret-seed-bench) can reach
// InterpretQuestionForSample while still going through the SAME
// environment-driven construction path production composition
// (internal/runtime/hosted) uses via New -- never a second, hand-rolled
// client setup that could drift from it.
func NewGenkitRuntime(ctx context.Context, cfg Config) (*genkitruntime.Runtime, error) {
	return newPrimaryRuntime(ctx, cfg)
}

func newPrimaryRuntime(ctx context.Context, cfg Config) (*genkitruntime.Runtime, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	suppressModelTelemetryExport()
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	var fallback contextfabric.ModelRuntime
	if cfg.FallbackModel != "" {
		clients, err := newLLMClients(cfg, cfg.FallbackModel)
		if err != nil {
			return nil, fmt.Errorf("initialize fallback model runtime: %w", err)
		}
		fallbackRuntime, err := genkitruntime.NewWithLLM(fallbackRuntimeConfig(cfg), clients)
		if err != nil {
			return nil, fmt.Errorf("initialize fallback model runtime: %w", err)
		}
		fallback = fallbackRuntime
	}
	clients, err := newLLMClients(cfg, cfg.Model, cfg.PhrasingModel)
	if err != nil {
		return nil, fmt.Errorf("initialize model runtime: %w", err)
	}
	// CHAOS-4171 PR2: the phrasing model (empty PhrasingModel defaults
	// inside genkitruntime to the primary Model) is wired ONLY onto the
	// primary runtime -- offer phrasing has no fallback chain of its own
	// (ratified design: a failure degrades to structural, never to a second
	// model), so the fallback runtime built above never needs it.
	primary, err := genkitruntime.NewWithLLM(runtimeConfigWithPhrasing(cfg, cfg.Model, fallback), clients)
	if err != nil {
		return nil, fmt.Errorf("initialize model runtime: %w", err)
	}
	logger.InfoContext(ctx, modelClientsBuiltMessage,
		"provider", contextfabric.SanitizeLogAttr(cfg.Provider), "llm_kind", contextfabric.SanitizeLogAttr(string(kindFor(cfg.Provider))),
		"model", contextfabric.SanitizeLogAttr(cfg.Model), "fallback_model", contextfabric.SanitizeLogAttr(cfg.FallbackModel),
		"phrasing_model", contextfabric.SanitizeLogAttr(cfg.PhrasingModel), "temperature", contextfabric.SanitizeLogAttr("omitted"),
		"output_cap", contextfabric.SanitizeLogAttr("omitted"), "max_output_tokens", cfg.MaxOutputTokens)
	return primary, nil
}

// modelClientsBuiltMessage is the one line that records which shared-client
// kind and models a model runtime was built with.
const modelClientsBuiltMessage = "context fabric model clients built"

// kindFor picks the shared dev-health-go client a Provider namespace is
// served by. The namespaces that name a provider API the package has a
// native client for get that client; every other namespace ("openai", a BYO
// gateway name) gets the OpenAI-compatible Chat Completions client, the API
// the Genkit compat_oai plugin spoke before it. Provider is still recorded
// verbatim on receipts.
func kindFor(provider string) llm.Kind {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case string(llm.KindAnthropic):
		return llm.KindAnthropic
	case string(llm.KindGemini):
		return llm.KindGemini
	case string(llm.KindQwen):
		return llm.KindQwen
	}
	return llm.KindLocal
}

// newLLMClients builds one shared-package client per distinct non-empty
// model, all on cfg's endpoint and credential. The configured model is
// always passed explicitly, so the package default never applies.
//
// Temperature and the output cap are omitted, as the Genkit plugin omitted
// them: GPT-5 models refuse any temperature but their default with a 400,
// and a fixed cap is a 400 on a model whose context window is not above it. The HTTP client
// times out with the per-attempt bound; the attempt's own context deadline
// still governs first.
func newLLMClients(cfg Config, models ...string) (genkitruntime.LLMClients, error) {
	kind := kindFor(cfg.Provider)
	baseURL := cfg.BaseURL
	if baseURL == "" && kind == llm.KindLocal {
		// A native kind with no BaseURL uses its provider's public endpoint.
		baseURL = DefaultBaseURL
	}
	clients := genkitruntime.LLMClients{}
	for _, model := range models {
		if model == "" {
			continue
		}
		if _, ok := clients[model]; ok {
			continue
		}
		client, err := llm.New(llm.Config{
			Kind:        kind,
			APIKey:      cfg.APIKey,
			BaseURL:     baseURL,
			Model:       model,
			HTTPClient:  newModelHTTPClient(cfg),
			Temperature: llm.TemperatureOmit,
			OutputCap:   llm.OutputCapOmit,
		})
		if err != nil {
			return nil, err
		}
		clients[model] = client
	}
	return clients, nil
}

// newModelHTTPClient is the HTTP client of every model call. A
// credential-free BYO endpoint gets no Authorization header at all: the
// shared client would otherwise send its own placeholder bearer, and an
// endpoint configured without a credential has never been sent one.
func newModelHTTPClient(cfg Config) *http.Client {
	if cfg.APIKey.Configured() {
		return &http.Client{Timeout: cfg.Timeout}
	}
	// A custom RoundTripper is used by the shared client as it is, so this
	// one drops the ambient proxy itself, as the client does for its own.
	direct := http.DefaultTransport.(*http.Transport).Clone()
	direct.Proxy = nil
	return &http.Client{Timeout: cfg.Timeout, Transport: withoutAuthorization{next: direct}}
}

type withoutAuthorization struct{ next http.RoundTripper }

func (t withoutAuthorization) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Del("Authorization")
	return t.next.RoundTrip(request)
}

// fallbackRuntimeConfig is the fallback leg's runtime config: the plain
// runtimeConfig (no phrasing model, no re-synthesis bound) held to a single
// synthesis draw, because it answers once after the primary has spent its own.
func fallbackRuntimeConfig(cfg Config) genkitruntime.Config {
	config := runtimeConfig(cfg, cfg.FallbackModel, nil)
	config.SingleDraw = true
	return config
}

func runtimeConfig(cfg Config, model string, fallback contextfabric.ModelRuntime) genkitruntime.Config {
	return genkitruntime.Config{
		Provider: cfg.Provider,
		// Model is the bare id: it lands unqualified in every
		// ModelExecutionReceipt (the receipt carries Provider separately),
		// and it is the key of the model's shared-package client.
		Model:           model,
		Timeout:         cfg.Timeout,
		MaxAttempts:     cfg.MaxAttempts,
		Fallback:        fallback,
		MaxOutputTokens: cfg.MaxOutputTokens,
		Telemetry:       cfg.Telemetry,
		// CHAOS-5380: copied straight through, unconditionally, exactly
		// like Telemetry above it -- a nil here keeps genkitruntime's own
		// documented slog.Default() fallback rather than a
		// package-internal default substituted at this layer.
		Logger: cfg.Logger,
	}
}

// runtimeConfigWithPhrasing extends runtimeConfig with the offer-phrasing
// model fields (CHAOS-4171 PR2) -- a separate function rather than folding
// PhrasingModel into runtimeConfig itself, because the fallback runtime
// (built via the plain runtimeConfig above) must never carry it.
func runtimeConfigWithPhrasing(cfg Config, model string, fallback contextfabric.ModelRuntime) genkitruntime.Config {
	config := runtimeConfig(cfg, model, fallback)
	if cfg.PhrasingModel != "" {
		config.PhrasingModel = cfg.PhrasingModel
	}
	// CHAOS-5655: wired here, not in the shared runtimeConfig above, because
	// this function builds ONLY the primary runtime (the fallback runtime is
	// built through the plain runtimeConfig call in newPrimaryRuntime) --
	// see MaxSynthesisResynthesisAttempts's own doc comment on Config for why
	// the fallback leg must not inherit it.
	config.MaxSynthesisResynthesisAttempts = cfg.MaxSynthesisResynthesisAttempts
	return config
}

// sanitizeProviderErrorBody is an OpenAI SDK request middleware
// (CHAOS-3770 F1(b) residual, codex round 3) that replaces a non-2xx
// provider response body with a fixed, content-free shape before the SDK
// itself ever reads it.
//
// Without this, the openai-go SDK constructs an *openai.Error
// (internal/apierror.Error) directly from the response body for every
// non-2xx status, and that type's Error() method formats as
// "%s %q: %d %s %s" with the RAW BODY as the last component -- so
// whatever the provider returned (which can echo request content, e.g. a
// gateway's "rejected prompt: ..." message) becomes part of the error's
// own string representation. That string then flows, unmodified, into
// every consumer of the error: retryable/classifyModelError in
// genkitruntime (already provider-content-safe on its own merits, per
// their own doc comments), Genkit's internal/metrics errorMessage
// attribute (F1(a)), and -- what round 3 of this review actually caught
// -- core/action.go's Action.Run, which logs the raw err verbatim at
// Debug level on every action failure, INCLUDING the model action itself
// (ai/generate.go:882's Model.Generate calls m.Action.Run directly).
// Muting Genkit's logger only helps for log levels ACR chooses; the
// provider's own response body is content this service must never hold
// as free text at all, at any log level, so it is sanitized here, once,
// at the one place ACR constructs the transport that ever sees it.
//
// This intentionally preserves the HTTP status code (both in the
// response's own StatusCode field, untouched, and spelled out in the
// replacement message) and discards everything else. That is enough for
// every existing consumer that classifies on status: shouldRetry (the
// SDK's own retry decision, internal/requestconfig) reads only
// res.StatusCode and response headers, never the body, so retry behavior
// is unaffected; classifyModelError's substring fallback for rate
// limiting keys off the literal digits "429", which the replacement
// message still contains for a 429 response
// (TestSanitizedProviderErrorStillClassifiesIdenticallyThroughRetryableAndTaxonomy
// pins this against every case
// TestNew_classifiesRecordedProviderFailures already covers). It
// deliberately does NOT try to preserve or reconstruct a provider error
// "code" or "type" field -- inventing plausible-looking values for those
// would be worse than omitting them, and nothing in this codebase
// classifies on them.
// SanitizeProviderErrorBody exports sanitizeProviderErrorBody for the sibling
// provider packages that build their own OpenAI-compatible transport --
// currently internal/contextfabric/embedprovider (CHAOS-3778).
//
// It is shared rather than copied on purpose. The reasoning below is
// security-critical and non-obvious, and two copies would drift: the next
// person to discover a new status class that leaks a provider body would fix
// one of them. Any package in this repository that constructs a transport
// which can receive a provider response body must install THIS middleware, not
// its own equivalent.
func SanitizeProviderErrorBody(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	return sanitizeProviderErrorBody(req, next)
}

func sanitizeProviderErrorBody(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	// A 2xx allowlist, not a >=400 threshold (CHAOS-3770 F1(b) round-4
	// residual): the OpenAI SDK's own success/error branch treats ANY
	// status below 400 as success, so a 3xx response -- one without a
	// Location header, since net/http.Client only follows a redirect when
	// one is present -- was reaching the SDK, and Genkit, completely
	// unsanitized under the old resp.StatusCode < 400 check. That is worse
	// than an unsanitized log line: Action.Run's err is nil for a 3xx
	// exactly as it would be for a genuine 200, so a well-formed
	// completion body on a redirect response (from a misconfigured proxy
	// in front of a BYO endpoint, say) was silently ACCEPTED as legitimate
	// model output, not merely logged
	// (TestNew_neverTreatsA3xxResponseAsASuccessfulCompletion). Only
	// 200-299 is ever a genuine provider success; everything else --
	// 1xx, 3xx, 4xx, 5xx alike -- gets the same sanitized, choices-free
	// body, which the SDK/compat_oai then deterministically reject.
	if err != nil || resp == nil || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return resp, err
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	sanitized := fmt.Sprintf(
		`{"error":{"message":"provider response redacted by ACR (status %d %s)","type":"acr_sanitized_error","param":null,"code":null}}`,
		resp.StatusCode, strings.ReplaceAll(http.StatusText(resp.StatusCode), `"`, ""),
	)
	resp.Body = io.NopCloser(strings.NewReader(sanitized))
	resp.ContentLength = int64(len(sanitized))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(sanitized)))
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
}

// suppressModelTelemetryExport keeps the model call's own tracing and metrics
// out of the service's OTLP export, and out of any exporter an ambient
// environment could attach.
//
// The shared dev-health-go llm client records a span per attempt on the
// GLOBAL tracer provider. Registering a bare *sdktrace.TracerProvider with no
// span processors makes those spans local and discards them when they end; a
// no-op meter provider does the same for metrics. ACR's real service export
// (internal/otelexport) is never installed globally, only handed explicitly
// to its own HTTP handler, log handler and tick tracer, so the model call
// never reaches it. A bare otel.Set{Tracer,Meter}Provider call added
// elsewhere in this repository would break that isolation;
// internal/otelexport.TestNoGlobalTelemetryInstallOutsideModelProvider fails
// on one and TestModelCallTelemetryNeverExported proves it end to end.
//
// otel.Set{Tracer,Meter}Provider are cheap, idempotent pointer swaps, so this
// runs on every runtime construction rather than behind a sync.Once. It
// overwrites any provider a host process embedding this code as a library
// had configured; ACR ships as a self-contained service, never a library.
func suppressModelTelemetryExport() {
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetMeterProvider(metricnoop.NewMeterProvider())
}
