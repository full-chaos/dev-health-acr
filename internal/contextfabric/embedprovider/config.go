// Package embedprovider constructs the Context Fabric embedder
// (contextfabric.Embedder) from provider-shaped deployment configuration
// (CHAOS-3778).
//
// It deliberately mirrors internal/contextfabric/modelprovider: the
// configuration surface names a PROVIDER, not a vendor -- a provider kind, a
// base URL, a model id, a dimension, and an optional credential. Pointing
// ACR_CONTEXT_FABRIC_EMBED_BASE_URL at any OpenAI-compatible embeddings server
// (LM Studio, Ollama, TEI, vLLM, a customer gateway, or OpenAI itself) and
// naming its model id is a pure configuration change. No endpoint is ever
// hardcoded and no result, contract, or test may depend on a specific
// embedder (TRD §19.4.2).
//
// Unlike modelprovider this package does NOT go through Genkit's compat_oai
// plugin. That wrapper eagerly defines embedders and hard-codes model ids
// (see modelprovider's own package doc for the full reasoning); the embeddings
// call needs none of it. The openai-go SDK's Embeddings service is used
// directly, with the same option plumbing and the SAME response-body
// sanitizer (modelprovider.SanitizeProviderErrorBody), so a provider error
// body can never be held as free text here either.
//
// The whole feature is OFF unless a base URL is configured. An unconfigured
// deployment constructs no embedder, makes no call, and leaves the lexical
// retrieval path exactly as it was.
package embedprovider

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	acrconfig "github.com/full-chaos/dev-health-acr/internal/config"
)

// Defaults. Every one is overridable; none names a vendor.
const (
	// DefaultSimilarityFloor is tau in
	// docs/design/context-fabric-vector-retrieval.md §4.2 -- the ABSOLUTE
	// cosine similarity below which a vector neighbor is DROPPED rather than
	// scored.
	//
	// This is the AC-3778-4 guard, the highest-severity acceptance bar in
	// CHAOS-3778 ("a true no-match control question still returns no match").
	// A k-nearest-neighbor query ALWAYS returns k rows when k rows exist --
	// it has no concept of "nothing is close enough" -- so without an
	// absolute floor a question about a subject that does not exist would
	// come back with k confident-looking neighbors. The floor is what turns
	// "the nearest k" into "the ones that are actually close".
	//
	// 0.55 is a starting value for a general-purpose sentence embedder, not a
	// law: it is tuned per embedder against the AC-3778-1 corpus, which is
	// exactly why it is configuration and not a constant in the retrieval
	// path.
	DefaultSimilarityFloor = 0.55
	// DefaultTimeout bounds ONE embeddings call. It is deliberately far below
	// the AC-3778-5 budget of 150 ms p95 for the whole retrieval stage,
	// because the failure mode being bounded is not a slow call but a COLD
	// one: a local server that has to load the model first was measured at
	// 9.3 s against 10-17 ms warm. A caller that exceeds this budget degrades
	// to lexical-only rather than blowing the stage budget (see
	// contextfabric.Embedder's callers).
	DefaultTimeout = 250 * time.Millisecond
	// DefaultMaxTransportRetries is 0: a retry against a local embedder buys
	// nothing but latency, and the read path fails open to lexical retrieval
	// anyway.
	DefaultMaxTransportRetries = 0
	// DefaultMaxBatch bounds how many texts go into one embeddings request at
	// projection time.
	DefaultMaxBatch = 64
	// DefaultMaxTextRunes bounds how much of one node's search text is
	// embedded, so a single large document cannot dominate a projection
	// batch's latency or the server's context window.
	DefaultMaxTextRunes = 2000
)

const maximumProviderOrModelLength = 256

// Environment variable names, following the ACR_<COMPONENT>_ naming and the
// KEY / KEY_FILE secret convention used by internal/config.SecretValue and by
// modelprovider and the graph adapters.
const (
	EnvProvider        = "ACR_CONTEXT_FABRIC_EMBED_PROVIDER"
	EnvBaseURL         = "ACR_CONTEXT_FABRIC_EMBED_BASE_URL"
	EnvModel           = "ACR_CONTEXT_FABRIC_EMBED_MODEL"
	EnvDimension       = "ACR_CONTEXT_FABRIC_EMBED_DIMENSION"
	EnvAPIKey          = "ACR_CONTEXT_FABRIC_EMBED_API_KEY"
	EnvSimilarityFloor = "ACR_CONTEXT_FABRIC_EMBED_SIMILARITY_FLOOR"
	EnvTimeout         = "ACR_CONTEXT_FABRIC_EMBED_TIMEOUT"
	EnvMaxBatch        = "ACR_CONTEXT_FABRIC_EMBED_MAX_BATCH"
	EnvMaxTextRunes    = "ACR_CONTEXT_FABRIC_EMBED_MAX_TEXT_RUNES"
	// EnvExpectResponseModel names the model id the SERVER reports, when it
	// legitimately differs from the id we send. It RETARGETS the
	// response-model check; it cannot disable it. Leave it unset unless a
	// provider is known to rename its own id (some hosted providers append a
	// revision suffix to the id they echo back).
	EnvExpectResponseModel = "ACR_CONTEXT_FABRIC_EMBED_EXPECT_RESPONSE_MODEL"
	// EnvMaxTransportRetries bounds the SDK's own in-client retry loop.
	EnvMaxTransportRetries = "ACR_CONTEXT_FABRIC_EMBED_MAX_TRANSPORT_RETRIES"
	// EnvAllowInsecureBaseURL permits a plaintext http:// base URL. It exists
	// for a co-located embedder reached over loopback or a private network
	// (the development LM Studio endpoint is exactly this case); it is false
	// by default and must never be set for a base URL that leaves the trust
	// boundary, because the credential travels as a bearer token.
	EnvAllowInsecureBaseURL = "ACR_CONTEXT_FABRIC_EMBED_ALLOW_INSECURE_BASE_URL"
)

// Config is the provider-shaped embedder configuration.
type Config struct {
	// Provider is a stable name for the endpoint being used, recorded
	// verbatim in EmbedderIdentity so a rebuild can tell vectors apart. It is
	// never checked for a specific vendor.
	Provider string
	// BaseURL is the OpenAI-compatible API root, e.g.
	// "http://localhost:1234/v1/". There is NO default: an unset base URL
	// means the feature is off, not that some vendor's endpoint is implied.
	BaseURL string
	// Model is the bare embedding model id.
	Model string
	// Dimension is the vector width this deployment expects. It must match
	// what the server returns AND what the graph's vector index was built
	// with; a mismatch on either side is a hard error rather than a silently
	// degraded similarity (AC-3778-7).
	Dimension int
	// APIKey is an optional bearer credential. A loopback embedder needs
	// none; the shape accommodates one so a hosted embedder is a
	// configuration change only.
	APIKey string
	// SimilarityFloor is tau; see DefaultSimilarityFloor.
	SimilarityFloor float64
	// Timeout bounds one embeddings call.
	Timeout time.Duration
	// ExpectResponseModel is the model id the server is expected to report in
	// its response, when that legitimately differs from Model. Empty means
	// "the server must report exactly Model". See EnvExpectResponseModel.
	ExpectResponseModel string
	// MaxBatch bounds texts per request; MaxTextRunes bounds runes per text.
	MaxBatch     int
	MaxTextRunes int
	// MaxTransportRetries bounds the SDK's in-client retry loop.
	MaxTransportRetries int
	// AllowInsecureBaseURL permits a plaintext http:// base URL.
	AllowInsecureBaseURL bool
}

var (
	// ErrNotConfigured reports that no embedder is configured. It is not a
	// failure: it is how a deployment says "vector retrieval is off".
	ErrNotConfigured = errors.New("context fabric embedder is not configured")
	// ErrDimensionMismatch reports that the server returned a vector of a
	// different width than the configuration declared.
	ErrDimensionMismatch = errors.New("context fabric embedder returned an unexpected vector dimension")
	// ErrResponseShape reports a response that does not pair one vector with
	// each input, in order.
	ErrResponseShape = errors.New("context fabric embedder returned a malformed response")
	// ErrModelIdentityMismatch reports that the server answered with a
	// DIFFERENT model than the one this deployment configured, or did not say
	// which model it used.
	//
	// This is not a hypothetical. Reproduced repeatedly against LM Studio with
	// more than one embedding model loaded: /v1/embeddings SILENTLY IGNORES
	// the request's `model` field and serves whichever model it prefers, with
	// no error -- observed returning 768-dimension nomic vectors for requests
	// explicitly naming a 1024-dimension qwen3-embedding model.
	//
	// The dimension check catches that particular pair, but ONLY because the
	// widths differ. Two same-width models (embeddinggemma at 768 and nomic at
	// 768) would sail straight through it, and the result is silent mixed-
	// vector corruption: a graph whose vectors were produced by two different
	// models, whose cosine similarities are meaningless against each other,
	// with every node stamped with the identity of the model we ASKED for.
	// Nothing downstream could detect that, and no rebuild would fix it
	// without first fixing the server.
	ErrModelIdentityMismatch = errors.New("context fabric embedder served a different model than configured")
)

// Configured reports whether a base URL is set at all, so a deployment that
// has not opted into vector retrieval never constructs the client and never
// fails closed over a dependency it did not choose. Mirrors
// falkorgraph.Configured and modelprovider's own posture.
func Configured(lookup func(string) (string, bool)) bool {
	value, ok := lookup(EnvBaseURL)
	return ok && strings.TrimSpace(value) != ""
}

func (c Config) validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return ErrNotConfigured
	}
	parsed, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || parsed.Host == "" {
		return errors.New("embedder base URL must be an absolute URL")
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !c.AllowInsecureBaseURL {
			return errors.New("embedder base URL must use https unless " + EnvAllowInsecureBaseURL + " is set")
		}
	default:
		return errors.New("embedder base URL must use http or https")
	}
	if !stringBounded(c.Provider) {
		return errors.New("embedder provider is required and must be bounded")
	}
	if !stringBounded(c.Model) {
		return errors.New("embedder model is required and must be bounded")
	}
	if strings.TrimSpace(c.ExpectResponseModel) != "" && !stringBounded(c.ExpectResponseModel) {
		return errors.New("embedder expected response model must be bounded")
	}
	// The upper bound is generous rather than tight: it exists to catch a
	// misconfiguration (a token count pasted into the dimension field), not
	// to encode an opinion about which embedders are acceptable.
	if c.Dimension < 8 || c.Dimension > 8192 {
		return errors.New("embedder dimension must be between eight and eight thousand one hundred ninety-two")
	}
	// A floor of 0 would disable the AC-3778-4 no-match guard entirely, and a
	// floor of 1 would demand an exact vector identity no real paraphrase
	// reaches. Both are configuration mistakes, not tuning choices.
	if c.SimilarityFloor <= 0 || c.SimilarityFloor >= 1 {
		return errors.New("embedder similarity floor must be between zero and one, exclusive")
	}
	if c.Timeout < 10*time.Millisecond || c.Timeout > time.Minute {
		return errors.New("embedder timeout must be between ten milliseconds and one minute")
	}
	if c.MaxBatch < 1 || c.MaxBatch > 512 {
		return errors.New("embedder max batch must be between one and five hundred twelve")
	}
	if c.MaxTextRunes < 32 || c.MaxTextRunes > 32768 {
		return errors.New("embedder max text runes must be between thirty-two and thirty-two thousand seven hundred sixty-eight")
	}
	if c.MaxTransportRetries < 0 || c.MaxTransportRetries > 5 {
		return errors.New("embedder max transport retries must be between zero and five")
	}
	return nil
}

func stringBounded(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && len(trimmed) <= maximumProviderOrModelLength
}

// ConfigFromEnv builds a Config from environment lookups. It returns
// ErrNotConfigured when no base URL is set, which every caller must treat as
// "vector retrieval is off", never as a startup failure.
func ConfigFromEnv(lookup func(string) (string, bool)) (Config, error) {
	if !Configured(lookup) {
		return Config{}, ErrNotConfigured
	}
	apiKey, err := acrconfig.SecretValue(lookup, EnvAPIKey)
	if err != nil {
		return Config{}, err
	}
	dimension, err := envInt(lookup, EnvDimension, 0)
	if err != nil {
		return Config{}, err
	}
	floor, err := envFloat(lookup, EnvSimilarityFloor, DefaultSimilarityFloor)
	if err != nil {
		return Config{}, err
	}
	timeout, err := envDuration(lookup, EnvTimeout, DefaultTimeout)
	if err != nil {
		return Config{}, err
	}
	maxBatch, err := envInt(lookup, EnvMaxBatch, DefaultMaxBatch)
	if err != nil {
		return Config{}, err
	}
	maxTextRunes, err := envInt(lookup, EnvMaxTextRunes, DefaultMaxTextRunes)
	if err != nil {
		return Config{}, err
	}
	retries, err := envInt(lookup, EnvMaxTransportRetries, DefaultMaxTransportRetries)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Provider:             envString(lookup, EnvProvider, ""),
		BaseURL:              envString(lookup, EnvBaseURL, ""),
		Model:                envString(lookup, EnvModel, ""),
		ExpectResponseModel:  envString(lookup, EnvExpectResponseModel, ""),
		Dimension:            dimension,
		APIKey:               apiKey,
		SimilarityFloor:      floor,
		Timeout:              timeout,
		MaxBatch:             maxBatch,
		MaxTextRunes:         maxTextRunes,
		MaxTransportRetries:  retries,
		AllowInsecureBaseURL: envBool(lookup, EnvAllowInsecureBaseURL, false),
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

func envBool(lookup func(string) (string, bool), key string, fallback bool) bool {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return fallback
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

func envFloat(lookup func(string) (string, bool), key string, fallback float64) (float64, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number", key)
	}
	return parsed, nil
}

func envDuration(lookup func(string) (string, bool), key string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid Go duration", key)
	}
	return parsed, nil
}
