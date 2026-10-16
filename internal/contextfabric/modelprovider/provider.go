package modelprovider

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
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
	if err := rejectDevGenkitEnvironment(); err != nil {
		return nil, err
	}
	suppressGenkitTelemetryExport()
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
		"provider", contextfabric.SanitizeLogAttr(cfg.Provider), "llm_kind", contextfabric.SanitizeLogAttr(string(llmKind)),
		"model", contextfabric.SanitizeLogAttr(cfg.Model), "fallback_model", contextfabric.SanitizeLogAttr(cfg.FallbackModel),
		"phrasing_model", contextfabric.SanitizeLogAttr(cfg.PhrasingModel), "temperature", contextfabric.SanitizeLogAttr("omitted"),
		"output_cap", contextfabric.SanitizeLogAttr("omitted"), "max_output_tokens", cfg.MaxOutputTokens)
	return primary, nil
}

// modelClientsBuiltMessage is the one line that records which shared-client
// kind and models a model runtime was built with.
const modelClientsBuiltMessage = "context fabric model clients built"

// llmKind is the shared dev-health-go client every Provider namespace is
// served by: the OpenAI-compatible Chat Completions client, the API the
// Genkit compat_oai plugin spoke before it. Provider stays a namespace
// recorded verbatim on receipts; it does not pick the wire API.
const llmKind = llm.KindLocal

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
	baseURL := cfg.BaseURL
	if baseURL == "" {
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
			Kind:        llmKind,
			APIKey:      llm.NewSecret(cfg.APIKey),
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
	if cfg.APIKey != "" {
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

// suppressGenkitTelemetryExport prevents Genkit's own tracing AND metrics
// packages from ever exporting generation content off this process
// (CHAOS-3770 F1, hardened in codex round 2).
//
// TRACES: every Genkit generate call runs inside tracing.RunInNewSpan
// (core/tracing/tracing.go), which unconditionally records the full
// request -- including the system prompt and the encoded question -- as
// the "genkit:input" span attribute, and the model's response as
// "genkit:output" (spanMetadata.attributes(), set via a deferred
// span.SetAttributes on every span, success or failure). That alone is
// harmless: an attribute on an unexported span goes nowhere. What makes it
// a live risk is tracing.TracerProvider()'s lazy, package-global
// initialization: the FIRST call anywhere in the process to Genkit's
// Tracer() checks the ambient GENKIT_TELEMETRY_SERVER environment
// variable and, if it is non-empty, wires up an HTTP exporter
// (WriteTelemetryImmediate) that ships every finished span -- prompt and
// response content included -- to that URL SYNCHRONOUSLY, in the same
// goroutine as span.End(). tracing.TracerProvider() only consults
// GENKIT_TELEMETRY_SERVER when otel.GetTracerProvider() does not already
// return a *sdktrace.TracerProvider:
//
//	func TracerProvider() *sdktrace.TracerProvider {
//		if tp := otel.GetTracerProvider(); tp != nil {
//			if sdkTP, ok := tp.(*sdktrace.TracerProvider); ok {
//				return sdkTP
//			}
//		}
//		providerInitOnce.Do(func() { ... reads GENKIT_TELEMETRY_SERVER ... })
//		...
//	}
//
// So registering our own bare *sdktrace.TracerProvider -- with no span
// processors, meaning every span it creates is simply discarded when it
// ends -- before Genkit ever calls Tracer() makes that check succeed and
// the env-var-gated branch dead code for this process.
//
// METRICS: internal/metrics/metrics.go's WriteActionFailure/WriteFlowFailure
// (invoked from core/action.go's recordActionMetrics on every failed
// generate call -- reachable from genkitruntime's own generate path, not
// only Genkit's dev-only registered-action path) record
// attribute.String("errorMessage", err.Error()) on a counter obtained from
// the GLOBAL otel.Meter("genkit") -- i.e. otel.GetMeterProvider(). err.Error()
// can carry a raw provider response body (see retryable's doc comment in
// genkitruntime/runtime.go for why). Unlike TracerProvider(), Genkit's
// metrics path has NO "already registered, skip" check of its own -- it
// just calls otel.Meter("genkit") fresh on every failure, so nothing in
// Genkit itself gates this on an env var the way tracing does. The only
// lever available from here is the same one: register our own no-op
// *metric.MeterProvider (metric/noop) so genkit/action metrics are
// recorded into a sink that discards them, since OTel's default global
// MeterProvider (what genkit would otherwise get) is ALSO a no-op --
// this only matters if something else in the binary later installs a
// real one (see the LAST-WRITER-WINS note below).
//
// LAST-WRITER-WINS (codex round 2, point d): both otel.SetTracerProvider
// and otel.SetMeterProvider are unconditional global assignments -- ANY
// later call anywhere in the process, by ANY package, silently overrides
// what this function set, for every subsequent Genkit span/metric,
// including ones already suppressed here. This function cannot and does
// not defend against that; it can only ensure IT wins the race for
// whichever caller constructs the first genkit.Genkit instance. That is a
// real, durable guarantee for THIS codebase specifically -- confirmed by
// inspection, not assumed -- because modelprovider is the only package in
// this repository that imports otel's Set{Tracer,Meter}Provider at all,
// and the only Genkit plugin ACR uses (compat_oai) never touches
// OpenTelemetry itself (only Genkit's own plugins/googlecloud does, and
// ACR does not import it). ACR's real service export (internal/otelexport)
// was built against this comment: its providers are never installed
// globally, only handed explicitly to its own HTTP handler, log handler and
// tick tracer, so Genkit -- which reads only the globals set here -- never
// reaches them. A bare otel.Set{Tracer,Meter}Provider call added elsewhere
// in this repository would silently re-expose Genkit's prompt/response
// content to whatever exporter it configures;
// internal/otelexport.TestNoGlobalTelemetryInstallOutsideModelProvider fails
// on one, and TestGenkitTelemetryNeverExported (this package) proves the
// isolation end to end. This function also
// necessarily overwrites any tracer/meter provider a HOST process embedding
// this code as a library had already configured for its OWN purposes --
// acceptable because ACR ships as a self-contained service (cmd/acr-api),
// never as an embedded library, but worth this explicit note for anyone
// who changes that.
//
// Called on every initGenkit invocation rather than gated by a sync.Once
// of its own: otel.Set{Tracer,Meter}Provider are cheap, idempotent pointer
// swaps, and always winning this race -- regardless of which caller
// happens to construct the first genkit.Genkit instance in the process --
// is what makes the guarantee independent of call order.
func suppressGenkitTelemetryExport() {
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetMeterProvider(metricnoop.NewMeterProvider())
}

// rejectDevGenkitEnvironment fails composition if the ambient GENKIT_ENV
// variable would put Genkit into its dev mode (CHAOS-3770 F1 residual,
// codex round 2, point c). genkit.Init starts a local reflection server
// (HTTP or WebSocket, depending on GENKIT_REFLECTION_V2_SERVER) only when
// api.CurrentEnvironment() == api.EnvironmentDev, i.e. only when
// os.Getenv("GENKIT_ENV") == "dev" -- unset defaults to prod, and ACR
// itself never sets this variable, so by default no such server ever
// binds. That server's handleNotify endpoint lets a caller register a NEW
// telemetry exporter URL at runtime (configureTelemetry), independent of
// GENKIT_TELEMETRY_SERVER and independent of suppressGenkitTelemetryExport
// above (it operates via genkit's own tracing.WriteTelemetryImmediate/
// WriteTelemetryRealtime, not through the global TracerProvider check this
// package preempts) -- so if it ever started, it would reopen exactly the
// export path this package exists to close. Rather than merely relying on
// "ACR itself never sets GENKIT_ENV" (true today, but an ambient variable
// this package does not control, same class of risk as
// GENKIT_TELEMETRY_SERVER), this fails composition outright the moment it
// would matter, consistent with newClientOptions' treatment of ambient
// OPENAI_* variables: an ambient setting must never be able to change this
// service's behavior in a security-relevant way without an explicit ACR
// configuration decision.
func rejectDevGenkitEnvironment() error {
	if value := os.Getenv("GENKIT_ENV"); value == "dev" {
		return fmt.Errorf("GENKIT_ENV=dev is not permitted: it starts Genkit's local reflection server, whose handleNotify endpoint can register a telemetry exporter for prompt/response content at runtime, independent of this package's own telemetry suppression")
	}
	return nil
}
