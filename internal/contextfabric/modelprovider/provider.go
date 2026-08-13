package modelprovider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/firebase/genkit/go/core/api"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/compat_oai"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/openai/openai-go/option"
	"go.opentelemetry.io/otel"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// New builds the Context Fabric model runtime for cfg. On success the
// returned contextfabric.ModelRuntime is always non-nil, so a caller may
// assign it straight into an interface field without the typed-nil trap.
//
// ctx bounds plugin initialization only; it is not retained for
// generation, which is bounded per attempt by Config.Timeout inside
// genkitruntime.
func New(ctx context.Context, cfg Config) (contextfabric.ModelRuntime, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if err := rejectDevGenkitEnvironment(); err != nil {
		return nil, err
	}
	instance, err := initGenkit(ctx, &compat_oai.OpenAICompatible{
		Provider: cfg.Provider,
		Opts:     newClientOptions(cfg),
	})
	if err != nil {
		return nil, err
	}
	var fallback contextfabric.ModelRuntime
	if cfg.FallbackModel != "" {
		fallbackRuntime, err := genkitruntime.New(runtimeConfig(instance, cfg, cfg.FallbackModel, nil))
		if err != nil {
			return nil, fmt.Errorf("initialize fallback model runtime: %w", err)
		}
		fallback = fallbackRuntime
	}
	primary, err := genkitruntime.New(runtimeConfig(instance, cfg, cfg.Model, fallback))
	if err != nil {
		return nil, fmt.Errorf("initialize model runtime: %w", err)
	}
	return primary, nil
}

func runtimeConfig(instance *genkit.Genkit, cfg Config, model string, fallback contextfabric.ModelRuntime) genkitruntime.Config {
	return genkitruntime.Config{
		Genkit:   instance,
		Provider: cfg.Provider,
		// Model stays the bare id so it lands unqualified in every
		// ModelExecutionReceipt (the receipt already carries Provider
		// separately); ModelRef carries the namespaced action name Genkit
		// resolves against.
		Model:       model,
		ModelRef:    api.NewName(cfg.Provider, model),
		Timeout:     cfg.Timeout,
		MaxAttempts: cfg.MaxAttempts,
		Fallback:    fallback,
	}
}

// newClientOptions builds the OpenAI-compatible client options.
//
// Both the credential and the base URL are ALWAYS passed explicitly, even
// when the credential is empty or the base URL is the provider default.
// openai.NewClient seeds itself from the ambient process environment
// (OPENAI_API_KEY, OPENAI_BASE_URL, OPENAI_ORG_ID, ...) and applies
// caller-supplied options afterwards, so passing both explicitly is what
// guarantees that (a) an ambient OPENAI_BASE_URL cannot redirect this
// service's traffic, and (b) an ambient OPENAI_API_KEY is never sent to a
// BYO endpoint this configuration pointed at. Do not "simplify" either
// option away on the grounds that the SDK has a sensible default -- the
// point is precisely that the SDK's default is environment-derived.
func newClientOptions(cfg Config) []option.RequestOption {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	credential := option.WithAPIKey(cfg.APIKey)
	if cfg.APIKey == "" {
		// A credential-free BYO endpoint should receive no Authorization
		// header at all, not an empty bearer -- but the header must still
		// be actively removed, because the SDK will have populated it from
		// an ambient OPENAI_API_KEY.
		credential = option.WithHeaderDel("authorization")
	}
	return []option.RequestOption{
		credential,
		option.WithBaseURL(baseURL),
		option.WithMaxRetries(cfg.MaxTransportRetries),
		option.WithMiddleware(sanitizeProviderErrorBody),
	}
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
func sanitizeProviderErrorBody(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if err != nil || resp == nil || resp.StatusCode < 400 {
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
// ACR does not import it). If a future change adds real OpenTelemetry
// export to ACR (e.g. for legitimate service observability), it MUST
// either route through this same construction point or be reviewed
// against this comment -- a bare otel.Set{Tracer,Meter}Provider call added
// elsewhere in this repository silently re-exposes Genkit's prompt/
// response content to whatever exporter it configures. This function also
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

// initGenkit wraps genkit.Init, which reports every failure by panicking
// (an unrecoverable plugin error, an invalid option, an unloadable prompt
// directory) because it has no error return. Hosted composition must fail
// closed with an error it can annotate and log, and must not take the
// process down mid-startup, so the panic is converted here.
//
// Only the panic value's type and text reach the returned error, and only
// from genkit's own construction path -- no credential is in scope at this
// point beyond the option values themselves, which genkit never formats
// into a panic message.
func initGenkit(ctx context.Context, plugin api.Plugin) (instance *genkit.Genkit, err error) {
	suppressGenkitTelemetryExport()
	defer func() {
		if recovered := recover(); recovered != nil {
			instance = nil
			err = fmt.Errorf("initialize model provider plugin %q: %v", plugin.Name(), recovered)
		}
	}()
	instance = genkit.Init(ctx, genkit.WithPlugins(plugin))
	if instance == nil {
		return nil, fmt.Errorf("initialize model provider plugin %q: genkit returned no instance", plugin.Name())
	}
	return instance, nil
}
