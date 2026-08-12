package modelprovider

import (
	"context"
	"fmt"

	"github.com/firebase/genkit/go/core/api"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/compat_oai"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/openai/openai-go/option"
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
	}
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
