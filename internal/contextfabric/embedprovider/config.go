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
//
// Full pathway diagram (env resolution -> projector write path -> per-epoch
// FalkorDB storage -> KNN read path, including the stored-vector
// invalidation fence and the epoch build-aside/swap hop): CHAOS-4133,
// docs/design/context-fabric-vector-retrieval.md §8.
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
	// DefaultBatchTimeout bounds ONE embeddings call issued from the
	// PROJECTION (write) path, where a single request carries up to
	// MaxBatch texts rather than DefaultTimeout's one (CHAOS-3828).
	// DefaultTimeout is sized for a single query embedding on the read
	// path's hot path; reusing it for a MaxBatch-sized request means every
	// write-side call times out on anything but a very fast warm local
	// model (measured ~360ms for 64 texts against a local nomic model,
	// comfortably over the 250ms read-side budget) -- and
	// embedProjectionBatch's failure handling then clears every target's
	// vector on that timeout, indistinguishable from a genuine embedder
	// outage. 5 seconds is sized for a warm batch call against a slower or
	// remote endpoint while still bounding one projection tick; a cold
	// model load is the ProbeTimeout-class 9.3s case this does NOT need to
	// cover, because acr-projector's own bootstrap already waits for the
	// index/model to be ready before ticking.
	DefaultBatchTimeout = 5 * time.Second
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
	// MinimumMaxTextRunes is the validation FLOOR for MaxTextRunes
	// (CHAOS-3833, embed-text spec §0 (c)): 2,000 conservatively covers
	// the largest COMPLETE per-kind template (~1,860 runes, pull_request),
	// so at every configuration that passes validation the embed-side
	// truncation can only ever touch UNBOUNDED compositions (episodes) --
	// never a templated kind. That is what makes the lexical/vector
	// byte-identity claim UNCONDITIONAL: below this floor, lexical would
	// index text the vector arm silently truncated away, reopening the
	// exact divergence the shared composition exists to close. An
	// unconditional invariant is checked once, here, at config load; a
	// cap-scoped claim would have to be re-derived at every reasoning
	// site and WILL be forgotten. A deployment that had lowered the env
	// below this fails validation loudly at startup (runbook-documented;
	// strictly better than silently amputating the highest-value text).
	// Test seams that need tiny caps construct the truncation directly
	// rather than through config validation. 2,000 runes sits comfortably
	// inside every supported embedding model's context window.
	MinimumMaxTextRunes = 2000
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
	// EnvBatchTimeout is the PROJECTION (write) path's own per-call timeout
	// (CHAOS-3828), independent of EnvTimeout. See DefaultBatchTimeout for
	// why the read-side default cannot be reused for a MaxBatch-sized
	// request.
	EnvBatchTimeout = "ACR_CONTEXT_FABRIC_EMBED_BATCH_TIMEOUT"
	EnvMaxBatch     = "ACR_CONTEXT_FABRIC_EMBED_MAX_BATCH"
	EnvMaxTextRunes = "ACR_CONTEXT_FABRIC_EMBED_MAX_TEXT_RUNES"
	// EnvPrefixFamily selects the asymmetric task-prefix pair this
	// deployment's model requires (CHAOS-3836; spec §6 T6). Unset means
	// PrefixFamilyNone. See the embedprovider package doc comment in
	// prefix.go for why this is a closed-vocabulary setting rather than
	// inferred from EnvModel.
	EnvPrefixFamily = "ACR_CONTEXT_FABRIC_EMBED_PREFIX_FAMILY"
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
	// EnvProviderLocality declares where embedded text ends up (CHAOS-3833,
	// embed-text spec §3): "local" (same trust zone -- the text never
	// leaves the deployment) or "remote" (a new reader outside the graph's
	// authorization scope). UNSET means "remote", so free-text bodies
	// default OFF until an operator affirmatively declares the endpoint
	// local. This is an EXPLICIT, fail-closed configuration decision --
	// NEVER inferred from URL shape: a loopback URL can front an ssh
	// tunnel or provider gateway, and a non-loopback URL can be a
	// same-host container address. URL heuristics lie in both directions.
	// Any value other than "local", "remote", or unset is a configuration
	// error, not a default.
	EnvProviderLocality = "ACR_CONTEXT_FABRIC_EMBED_PROVIDER_LOCALITY"
	// EnvIncludeBodies overrides the locality-derived body default
	// (CHAOS-3833, spec §3): free-text bodies (PR body head, incident
	// description head) join the composed search text only when this
	// resolves true. Explicitly setting it true with a remote/unset
	// locality is the documented tenant opt-in for transmitting body text
	// to a remote provider; setting it false keeps bodies out even for a
	// local endpoint. It is SEMANTIC config: the effective value joins the
	// composition tag, so a flip moves the stamped identity, fails stored
	// vectors closed to lexical, and invalidates answer reuse until the
	// prescribed rebuild.
	EnvIncludeBodies = "ACR_CONTEXT_FABRIC_EMBED_INCLUDE_BODIES"
	// EnvAllowNoCredential is the explicit opt-in for a configured embedder
	// (BaseURL set) with no credential at all (CHAOS-4192). Mirrors
	// EnvAllowInsecureBaseURL's exact posture: false by default, an
	// EXPLICIT declaration required to relax it, never inferred from the
	// base URL's shape (loopback or otherwise -- see EnvProviderLocality's
	// own doc comment on why URL heuristics are rejected throughout this
	// package).
	//
	// CHAOS-4192: acr-projector ran a full rebuild against
	// ACR_CONTEXT_FABRIC_EMBED_BASE_URL pointed at a real hosted endpoint
	// with EnvAPIKey resolving BLANK (a shell/env-file substitution defect,
	// not an intentional no-auth choice) -- Configured()/validate() had no
	// opinion on that combination, so the embedder was constructed
	// successfully and every projection batch's Embed() call failed with an
	// unauthenticated-request error. vector_projection.go's per-batch
	// failure handling correctly treats an Embed() failure as CLEAR the
	// stale vector and continue (the right call for a genuinely transient
	// failure, so one bad batch cannot stall projection forever) -- but a
	// blank credential is not transient, so that per-batch design silently
	// wiped every organization's graph vectors, one batch at a time, each
	// one logging only a Warn easy to miss in a busy rebuild.
	//
	// A local no-auth embedder (LM Studio, Ollama, TEI on loopback) is a
	// real, documented, supported deployment shape (see this package's own
	// doc comment and docs/design/context-fabric-vector-retrieval.md) --
	// requiring a credential unconditionally would break it. This flag is
	// the explicit way an operator declares "this endpoint genuinely needs
	// no credential", so a blank credential elsewhere is caught at startup
	// (ConfigFromEnv/validate, before any batch runs and before any vector
	// is ever cleared) rather than discovered mid-rebuild via scrolling
	// per-batch logs.
	EnvAllowNoCredential = "ACR_CONTEXT_FABRIC_EMBED_ALLOW_NO_CREDENTIAL"
)

// BodiesIncluded resolves the §3 body gate from the environment: the
// explicit EnvIncludeBodies wins when set; otherwise locality decides
// (local => on, remote/unset => off). It is deliberately independent of
// Configured/ConfigFromEnv because the ONE shared composition needs the
// gate's value even in a deployment with no embedder at all -- both
// retrieval arms must always index the identical text, so the gate cannot
// vary with whether an embedder happens to be constructed.
//
// Fail closed on garbage: an unparseable locality or gate value is an
// error, never a silent default -- a deployment that thinks it declared
// its endpoint local must not silently run with bodies off (or, worse,
// the reverse).
func BodiesIncluded(lookup func(string) (string, bool)) (bool, error) {
	locality := strings.ToLower(strings.TrimSpace(envString(lookup, EnvProviderLocality, "remote")))
	var localityLocal bool
	switch locality {
	case "local":
		localityLocal = true
	case "remote":
		localityLocal = false
	default:
		return false, errors.New(EnvProviderLocality + ` must be "local" or "remote"`)
	}
	value, ok := lookup(EnvIncludeBodies)
	if !ok || strings.TrimSpace(value) == "" {
		return localityLocal, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, errors.New(EnvIncludeBodies + " must be a boolean")
	}
	return parsed, nil
}

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
	// Timeout bounds one READ-path embeddings call (a single query text).
	Timeout time.Duration
	// BatchTimeout bounds one WRITE/PROJECTION-path embeddings call (up to
	// MaxBatch texts). Kept separate from Timeout so a batch sized for the
	// write path is not silently bounded by a budget sized for the read
	// path's single-text hot path (CHAOS-3828). embedChunk applies this to
	// any chunk issued under a ctx marked by WithBatchCall (CHAOS-4259
	// codex R1 finding 1), never by inferring from text count -- a
	// single-target write batch still needs it. Every read-path caller in
	// this repository leaves ctx unmarked, so this never affects the read
	// path.
	BatchTimeout time.Duration
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
	// AllowNoCredential is the explicit opt-in for a blank APIKey; see
	// EnvAllowNoCredential (CHAOS-4192).
	AllowNoCredential bool
	// PrefixFamily selects the asymmetric task-prefix pair applied to text
	// before it is embedded (CHAOS-3836). The zero value is treated as
	// PrefixFamilyNone everywhere this is read (validate, New), so a
	// hand-built Config that never mentions prefixes stays valid and
	// prefix-free -- only an explicitly set, UNRECOGNIZED family is a
	// configuration error.
	PrefixFamily PrefixFamily
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
	if c.BatchTimeout < 10*time.Millisecond || c.BatchTimeout > time.Minute {
		return errors.New("embedder batch timeout must be between ten milliseconds and one minute")
	}
	if c.MaxBatch < 1 || c.MaxBatch > 512 {
		return errors.New("embedder max batch must be between one and five hundred twelve")
	}
	// Floor raised 32 -> 2000 by CHAOS-3833 -- see MinimumMaxTextRunes for
	// why this is the byte-identity invariant's load-bearing bound. The
	// 32768 upper bound is unchanged.
	if c.MaxTextRunes < MinimumMaxTextRunes || c.MaxTextRunes > 32768 {
		return errors.New("embedder max text runes must be between two thousand and thirty-two thousand seven hundred sixty-eight")
	}
	if c.MaxTransportRetries < 0 || c.MaxTransportRetries > 5 {
		return errors.New("embedder max transport retries must be between zero and five")
	}
	if !validPrefixFamily(c.resolvedPrefixFamily()) {
		return prefixFamilyError(c.PrefixFamily)
	}
	// CHAOS-4192: a configured-looking embedder (BaseURL set) with a BLANK
	// credential is caught HERE, at startup, before any batch ever runs --
	// not discovered mid-rebuild via a per-batch "embedded:0, cleared:N"
	// log easy to miss in a busy rebuild. See EnvAllowNoCredential's doc
	// comment for the incident and why this is an explicit opt-in rather
	// than inferred from AllowInsecureBaseURL/URL shape (a loopback server
	// can still require a credential, and a non-loopback one can still be
	// genuinely open).
	if strings.TrimSpace(c.APIKey) == "" && !c.AllowNoCredential {
		return fmt.Errorf(
			"%s is configured but %s resolved empty -- either provide a credential or set %s=true to declare this endpoint genuinely needs none (e.g. a loopback LM Studio/Ollama/TEI server); a blank credential on a real endpoint fails every embed call, and this package's per-batch failure handling clears vectors rather than stalling (CHAOS-4192: this silently wiped a graph's vectors, one rebuild batch at a time, before this check existed)",
			EnvBaseURL, EnvAPIKey, EnvAllowNoCredential,
		)
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
	batchTimeout, err := envDuration(lookup, EnvBatchTimeout, DefaultBatchTimeout)
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
		BatchTimeout:         batchTimeout,
		MaxBatch:             maxBatch,
		MaxTextRunes:         maxTextRunes,
		MaxTransportRetries:  retries,
		AllowInsecureBaseURL: envBool(lookup, EnvAllowInsecureBaseURL, false),
		AllowNoCredential:    envBool(lookup, EnvAllowNoCredential, false),
		PrefixFamily:         PrefixFamily(envString(lookup, EnvPrefixFamily, string(PrefixFamilyNone))),
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
