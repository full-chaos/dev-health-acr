// Package modelprovider constructs the Context Fabric model runtime
// (contextfabric.ModelRuntime) from provider-shaped deployment
// configuration, and is the only place in this repository that builds a
// production genkit.Genkit instance.
//
// BYO LLM is the supported long-term shape (CHAOS-3770), so the
// configuration surface names a *provider*, not a vendor: a provider kind,
// an optional base URL, a model id, and a credential. The default values
// select OpenAI's hosted endpoint with gpt-5-nano, but nothing in this
// package's control flow is OpenAI-specific -- pointing
// ACR_CONTEXT_FABRIC_MODEL_BASE_URL at any OpenAI-compatible server (a
// customer's gateway, vLLM, Ollama, llama.cpp) and naming its model id is
// a pure configuration change, with no code change and no new plugin.
//
// The transport is Genkit's OpenAI-compatible plugin
// (github.com/firebase/genkit/go/plugins/compat_oai), used directly rather
// than through the higher-level plugins/compat_oai/openai wrapper on
// purpose. That wrapper hard-codes a list of OpenAI model ids that does not
// include gpt-5-nano, eagerly defines embedders this service never calls,
// and panics when no API key is present -- all three are wrong for BYO LLM.
// The compat layer underneath resolves *any* model id dynamically through
// its api.DynamicPlugin implementation, which is exactly the property BYO
// LLM needs.
package modelprovider

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	acrconfig "github.com/full-chaos/dev-health-acr/internal/config"
)

// Default provider selection. These encode the CHAOS-3770 decision (start
// on OpenAI + gpt-5-nano) as *defaults only*; every one of them is
// overridable by environment, and no other code path checks for them.
const (
	DefaultProvider = "openai"
	DefaultModel    = "gpt-5-nano"
	// DefaultBaseURL is applied explicitly, never left to the OpenAI SDK's
	// own default, because openai.NewClient reads OPENAI_BASE_URL and
	// OPENAI_API_KEY from the ambient process environment. Passing both
	// values explicitly on every construction means an ambient OPENAI_*
	// variable can neither redirect this service's traffic nor supply a
	// credential this configuration did not choose. See newClientOptions.
	DefaultBaseURL = "https://api.openai.com/v1/"
)

// DefaultTimeout, DefaultMaxAttempts, and DefaultMaxTransportRetries are
// exported so a caller building a Config for a surface ConfigFromEnv does
// not cover -- e.g. internal/contextfabric/modelruntimeresolver constructing
// a per-organization Config (CHAOS-3775), whose tuning knobs are inherited
// from the deployment surface rather than being part of the per-organization
// contract -- can reuse the same defaults without duplicating them.
const (
	DefaultTimeout             = 45 * time.Second
	DefaultMaxAttempts         = 2
	DefaultMaxTransportRetries = 2
)

const maximumProviderOrModelLength = 256

// Environment variable names, following the ACR_<COMPONENT>_ naming and the
// KEY / KEY_FILE secret convention used by internal/config.SecretValue and
// by the graph adapters' own ConfigFromEnv.
const (
	EnvProvider = "ACR_CONTEXT_FABRIC_MODEL_PROVIDER"
	EnvBaseURL  = "ACR_CONTEXT_FABRIC_MODEL_BASE_URL"
	EnvModel    = "ACR_CONTEXT_FABRIC_MODEL"
	// EnvFallbackModel names a second, usually stronger model on the same
	// provider. When set, it becomes genkitruntime.Config.Fallback: the
	// primary model is tried first and the fallback answers only when the
	// primary call fails or returns output that does not validate. It is
	// empty by default because a fallback is a second billable call, so an
	// operator opts into it explicitly rather than inheriting it.
	EnvFallbackModel = "ACR_CONTEXT_FABRIC_MODEL_FALLBACK"
	EnvAPIKey        = "ACR_CONTEXT_FABRIC_MODEL_API_KEY"
	EnvTimeout       = "ACR_CONTEXT_FABRIC_MODEL_TIMEOUT"
	EnvMaxAttempts   = "ACR_CONTEXT_FABRIC_MODEL_MAX_ATTEMPTS"
	// EnvMaxTransportRetries bounds the OpenAI-compatible SDK's own
	// in-client retry loop, which is a second retry layer underneath
	// genkitruntime's MaxAttempts. Both are bounded by genkitruntime's
	// per-attempt deadline, so this exists to make the total call budget
	// explicit rather than inherited: set it to 0 to make genkitruntime
	// the single retry owner (recommended for a local BYO server, where a
	// transport retry buys nothing).
	EnvMaxTransportRetries = "ACR_CONTEXT_FABRIC_MODEL_MAX_TRANSPORT_RETRIES"
	// EnvAllowInsecureBaseURL permits a plaintext http:// base URL. It
	// exists for a co-located BYO server reached over loopback or a
	// private network; it is false by default and must never be set for a
	// base URL that leaves the trust boundary, because the credential
	// travels as a bearer token on every request.
	EnvAllowInsecureBaseURL = "ACR_CONTEXT_FABRIC_MODEL_ALLOW_INSECURE_BASE_URL"
)

// Config is the provider-shaped model runtime configuration. Its fields are
// deliberately vendor-neutral: Provider is a plugin namespace, not a brand
// check, and no field is consulted for an OpenAI-specific behavior anywhere
// in this package.
type Config struct {
	// Provider is the plugin namespace the model id is resolved under, and
	// is recorded verbatim as ModelExecutionReceipt.Provider. For a BYO
	// deployment set it to a stable name for the endpoint being used
	// (e.g. "vllm", "acme-gateway") so replay can tell receipts apart.
	Provider string
	// BaseURL is the OpenAI-compatible API root. Empty selects
	// DefaultBaseURL.
	BaseURL string
	// Model is the bare model id, without the provider namespace --
	// "gpt-5-nano", not "openai/gpt-5-nano". modelprovider adds the
	// namespace when it asks Genkit to resolve the model, and keeps the
	// bare id for receipts.
	Model string
	// FallbackModel is an optional second model on the same provider; see
	// EnvFallbackModel.
	FallbackModel string
	// APIKey is the bearer credential. It is optional only when BaseURL is
	// set (a local BYO server may not authenticate at all); reaching a
	// hosted provider on the default base URL always requires it.
	APIKey string
	// Timeout bounds one generation attempt, MaxAttempts bounds how many
	// attempts genkitruntime makes, and MaxTransportRetries bounds the
	// SDK's own retry loop within one attempt.
	Timeout             time.Duration
	MaxAttempts         int
	MaxTransportRetries int
	// AllowInsecureBaseURL permits a plaintext http:// BaseURL; see
	// EnvAllowInsecureBaseURL.
	AllowInsecureBaseURL bool
}

// validate enforces the bounds this package owns. genkitruntime.New
// re-validates Timeout, MaxAttempts and the provider/model strings on its
// own; the checks here exist so a misconfigured deployment fails at
// composition with a message naming the environment variable, rather than
// with genkitruntime's internal vocabulary.
func (c Config) validate() error {
	for name, value := range map[string]string{
		EnvProvider: c.Provider,
		EnvModel:    c.Model,
	} {
		if strings.TrimSpace(value) == "" || len(value) > maximumProviderOrModelLength {
			return fmt.Errorf("%s is required and must be at most %d bytes", name, maximumProviderOrModelLength)
		}
	}
	// The provider becomes a Genkit action namespace, and Genkit splits an
	// action key on "/" -- a provider containing a separator would silently
	// resolve a different model than the one configured.
	if strings.ContainsRune(c.Provider, '/') {
		return fmt.Errorf("%s must not contain a path separator", EnvProvider)
	}
	if err := validateModelID(EnvModel, c.Model); err != nil {
		return err
	}
	if c.FallbackModel != "" {
		if len(c.FallbackModel) > maximumProviderOrModelLength {
			return fmt.Errorf("%s must be at most %d bytes", EnvFallbackModel, maximumProviderOrModelLength)
		}
		if err := validateModelID(EnvFallbackModel, c.FallbackModel); err != nil {
			return err
		}
		if c.FallbackModel == c.Model {
			return fmt.Errorf("%s must name a different model than %s", EnvFallbackModel, EnvModel)
		}
	}
	if err := c.validateBaseURL(); err != nil {
		return err
	}
	if c.BaseURL == "" && strings.TrimSpace(c.APIKey) == "" {
		return fmt.Errorf("%s is required when %s is unset (the default provider endpoint authenticates every request)", EnvAPIKey, EnvBaseURL)
	}
	if c.Timeout < time.Second || c.Timeout > 2*time.Minute {
		return fmt.Errorf("%s must be between one second and two minutes", EnvTimeout)
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 3 {
		return fmt.Errorf("%s must be between one and three", EnvMaxAttempts)
	}
	if c.MaxTransportRetries < 0 || c.MaxTransportRetries > 5 {
		return fmt.Errorf("%s must be between zero and five", EnvMaxTransportRetries)
	}
	return nil
}

func (c Config) validateBaseURL() error {
	if c.BaseURL == "" {
		if c.AllowInsecureBaseURL {
			return fmt.Errorf("%s has no effect without %s", EnvAllowInsecureBaseURL, EnvBaseURL)
		}
		return nil
	}
	parsed, err := url.Parse(c.BaseURL)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute URL", EnvBaseURL)
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if !c.AllowInsecureBaseURL {
			return fmt.Errorf("%s must use https unless %s is true", EnvBaseURL, EnvAllowInsecureBaseURL)
		}
		return nil
	default:
		return fmt.Errorf("%s must use http or https", EnvBaseURL)
	}
}

// validateModelID rejects a model id that would change which action Genkit
// resolves. A leading "/" or an empty segment would make the namespaced key
// ambiguous; an embedded "/" is allowed on purpose, because many
// OpenAI-compatible servers use ids like "meta-llama/Llama-3-8B-Instruct"
// and Genkit rejoins everything after the provider segment.
func validateModelID(name, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed != value {
		return fmt.Errorf("%s must not have leading or trailing whitespace", name)
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasSuffix(trimmed, "/") || strings.Contains(trimmed, "//") {
		return fmt.Errorf("%s must not contain an empty path segment", name)
	}
	return nil
}

// Configured reports whether the environment selects a model provider at
// all. It is the composition gate: when it returns false the caller must
// leave contextfabric.ModelRuntime nil, which keeps the investigation
// endpoint's clean per-request 503 (CHAOS-3755) exactly as it is today.
//
// ANY nonblank ACR_CONTEXT_FABRIC_MODEL* variable counts as opting in --
// not just a credential or base URL (CHAOS-3770 F5). An operator who sets
// only ACR_CONTEXT_FABRIC_MODEL, or only a tuning variable like
// ACR_CONTEXT_FABRIC_MODEL_TIMEOUT, has unambiguously expressed intent to
// configure a provider; treating that as "unconfigured" would silently
// discard their setting and degrade to a clean per-request 503 instead of
// the startup failure AC-3770-2 requires for a mis-specified configuration.
// Once any of these variables is set, ConfigFromEnv+validate own deciding
// whether the resulting configuration is actually usable (e.g. a model
// name with no credential and no base URL still fails validate() with a
// message naming EnvAPIKey) -- Configured only decides whether to attempt
// that parse at all.
//
// Ambient OPENAI_* variables are deliberately NOT consulted: opting this
// service into a paid provider is an ACR configuration decision, never
// something inherited from whatever happened to be exported in the
// process environment.
func Configured(lookup func(string) (string, bool)) bool {
	for _, key := range []string{
		EnvProvider, EnvBaseURL, EnvModel, EnvFallbackModel,
		EnvAPIKey, EnvAPIKey + "_FILE",
		EnvTimeout, EnvMaxAttempts, EnvMaxTransportRetries, EnvAllowInsecureBaseURL,
	} {
		if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// ConfigFromEnv builds a Config from the process environment. Callers own
// deciding when to call it -- see Configured -- because a deployment that
// did not opt into a model provider must never fail closed over it.
func ConfigFromEnv(lookup func(string) (string, bool)) (Config, error) {
	apiKey, err := acrconfig.SecretValue(lookup, EnvAPIKey)
	if err != nil {
		return Config{}, fmt.Errorf("context fabric model credential: %w", err)
	}
	cfg := Config{
		Provider:      envString(lookup, EnvProvider, DefaultProvider),
		BaseURL:       envString(lookup, EnvBaseURL, ""),
		Model:         envString(lookup, EnvModel, DefaultModel),
		FallbackModel: envString(lookup, EnvFallbackModel, ""),
		APIKey:        apiKey,
	}
	if cfg.Timeout, err = envDuration(lookup, EnvTimeout, DefaultTimeout); err != nil {
		return Config{}, err
	}
	if cfg.MaxAttempts, err = envInt(lookup, EnvMaxAttempts, DefaultMaxAttempts); err != nil {
		return Config{}, err
	}
	if cfg.MaxTransportRetries, err = envInt(lookup, EnvMaxTransportRetries, DefaultMaxTransportRetries); err != nil {
		return Config{}, err
	}
	if cfg.AllowInsecureBaseURL, err = envBool(lookup, EnvAllowInsecureBaseURL, false); err != nil {
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
