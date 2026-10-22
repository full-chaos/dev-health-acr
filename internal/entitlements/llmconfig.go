package entitlements

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/full-chaos/dev-health-go/llm"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// llmConfigSchemaVersion is the schema of ops' internal org llm-config route
// (GET /api/v1/internal/acr/orgs/{org_id}/llm-config).
const llmConfigSchemaVersion = "acr_llm_config.v1"

// llmConfigCacheTTL bounds how long one org's answer is reused: the route is
// read on every model call, and a changed BYO setting must still reach the
// next turn quickly.
const llmConfigCacheTTL = 15 * time.Second

// ErrLLMConfigUnusable is a BYO configuration ops holds but acr cannot use:
// a credential without the llm_config:read scope (401/403), a stored config
// ops cannot decrypt (409), or a body that does not match the request. It
// fails the organization's model calls closed; it is never "no BYO", so the
// platform model is never used in its place.
var ErrLLMConfigUnusable = errors.New("Dev Health BYO LLM configuration is unusable")

// ErrLLMConfigGateClosed is a BYO configuration ops holds but will not serve
// because the org's byo_llm gate is closed (a lapsed tier, an override off).
// It is neither "no BYO" nor "unusable": the turn is refused with its own
// reason and never runs on the platform model.
var ErrLLMConfigGateClosed = errors.New("Dev Health BYO LLM configuration is behind a closed byo_llm gate")

// ErrLLMConfigNoInvestigateConfig is an org whose saved BYO keys are for other
// purposes only: none resolves for investigations. The org is a BYO org, so the
// turn is refused with its own reason and never runs on the platform model.
var ErrLLMConfigNoInvestigateConfig = errors.New("Dev Health BYO LLM configuration has no investigate-capable config")

// ErrLLMConfigTurnedOff is an org whose admin turned its saved BYO configuration
// off without deleting it. The org has no usable own model: the gate treats it
// as an org with no configuration, so only the platform-admin grant draws the
// platform model and every other turn is refused with its own reason.
var ErrLLMConfigTurnedOff = errors.New("Dev Health BYO LLM configuration is turned off by the organization")

// llmConfigReasonHeader carries why ops answered the marked no-config 404.
const (
	llmConfigMarkerHeader        = "X-Org-Llm-Config"
	llmConfigMarkerNone          = "none"
	llmConfigReasonHeader        = "X-Org-Llm-Config-Reason"
	llmConfigGateClosedReason    = "byo_llm_gate_closed"
	llmConfigNoInvestigateReason = "byo_no_investigate_config"
	llmConfigTurnedOffReason     = "byo_llm_turned_off"
)

// OrgLLMConfig is one org's BYO LLM configuration as ops serves it. APIKey is
// the plaintext credential: it never reaches a log line, an error or a
// printed value (String, GoString, LogValue and MarshalJSON redact it).
type OrgLLMConfig struct {
	Provider string
	Model    string
	BaseURL  string
	APIKey   llm.Secret
	// PromptCache is the org's opt-in to the Anthropic prompt cache; ops omits
	// it for an org that has not opted in.
	PromptCache bool
	// Version changes when the org's stored settings change (ops: the
	// newest updated_at, RFC 3339 with nanoseconds).
	Version string
}

func (c OrgLLMConfig) view() map[string]any {
	return map[string]any{"provider": c.Provider, "model": c.Model, "base_url": c.BaseURL, "api_key": "[REDACTED]", "prompt_cache": c.PromptCache, "version": c.Version}
}

func (c OrgLLMConfig) String() string {
	return fmt.Sprintf("OrgLLMConfig{Provider:%q Model:%q BaseURL:%q APIKey:[REDACTED] PromptCache:%t Version:%q}", c.Provider, c.Model, c.BaseURL, c.PromptCache, c.Version)
}

func (c OrgLLMConfig) GoString() string { return c.String() }

func (c OrgLLMConfig) LogValue() slog.Value {
	return slog.GroupValue(slog.String("provider", c.Provider), slog.String("model", c.Model),
		slog.String("base_url", c.BaseURL), slog.String("api_key", "[REDACTED]"), slog.Bool("prompt_cache", c.PromptCache), slog.String("version", c.Version))
}

func (c OrgLLMConfig) MarshalJSON() ([]byte, error) { return json.Marshal(c.view()) }

type llmConfigResponse struct {
	SchemaVersion string `json:"schema_version"`
	OrgID         string `json:"org_id"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	BaseURL       string `json:"base_url"`
	APIKey        string `json:"api_key"`
	PromptCache   bool   `json:"prompt_cache"`
	Version       string `json:"version"`
}

type llmConfigEntry struct {
	config  OrgLLMConfig
	found   bool
	refusal error
	expires time.Time
}

type llmConfigCache struct {
	mu      sync.Mutex
	entries map[string]llmConfigEntry
}

func (c *llmConfigCache) get(orgID string, now time.Time) (llmConfigEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[orgID]
	if !ok || !now.Before(entry.expires) {
		delete(c.entries, orgID)
		return llmConfigEntry{}, false
	}
	return entry, true
}

func (c *llmConfigCache) put(orgID string, entry llmConfigEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]llmConfigEntry{}
	}
	c.entries[orgID] = entry
}

// OrgLLMConfig returns orgID's BYO LLM configuration from ops. found=false with
// no error is "no BYO configured": a 404 that carries X-Org-Llm-Config: none and
// no reason header. A 404 with the marker and the reason byo_llm_gate_closed is
// ErrLLMConfigGateClosed (a saved configuration behind a closed byo_llm gate); with
// byo_no_investigate_config it is ErrLLMConfigNoInvestigateConfig (saved keys for
// other purposes only); with byo_llm_turned_off it is ErrLLMConfigTurnedOff (a saved
// configuration an org admin turned off). A 409 (a saved configuration whose stored key is
// unreadable) is ErrLLMConfigUnusable. Everything else is unavailable (errUnavailable),
// because the source cannot be read or has broken its contract: ops unreachable, a
// 5xx, a 401 or 403 (the credential or its scope is refused), a 404 without the
// marker or with an unknown reason, and a 200 whose body does not decode or names
// another schema, organization, provider, model or version. Answers (found, not
// found, gate closed, no investigate config or turned off) are reused for llmConfigCacheTTL;
// errors are never cached.
func (c *Client) OrgLLMConfig(ctx context.Context, orgID string) (OrgLLMConfig, bool, error) {
	if strings.TrimSpace(orgID) == "" {
		return OrgLLMConfig{}, false, errors.New("unsupported llm config request")
	}
	now := c.now()
	if entry, ok := c.llmConfigs.get(orgID, now); ok {
		if entry.refusal != nil {
			return OrgLLMConfig{}, false, entry.refusal
		}
		return entry.config, entry.found, nil
	}
	config, found, err := c.fetchLLMConfig(ctx, orgID)
	if errors.Is(err, ErrLLMConfigGateClosed) || errors.Is(err, ErrLLMConfigNoInvestigateConfig) || errors.Is(err, ErrLLMConfigTurnedOff) {
		c.llmConfigs.put(orgID, llmConfigEntry{refusal: err, expires: now.Add(llmConfigCacheTTL)})
		return OrgLLMConfig{}, false, err
	}
	if err != nil {
		return OrgLLMConfig{}, false, err
	}
	c.llmConfigs.put(orgID, llmConfigEntry{config: config, found: found, expires: now.Add(llmConfigCacheTTL)})
	return config, found, nil
}

func (c *Client) fetchLLMConfig(ctx context.Context, orgID string) (OrgLLMConfig, bool, error) {
	requestContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, llmConfigURL(c.baseURL, orgID), nil)
	if err != nil {
		return OrgLLMConfig{}, false, errUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return OrgLLMConfig{}, false, errUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		if response.Header.Get(llmConfigMarkerHeader) != llmConfigMarkerNone {
			return OrgLLMConfig{}, false, errUnavailable
		}
		switch response.Header.Get(llmConfigReasonHeader) {
		case llmConfigGateClosedReason:
			return OrgLLMConfig{}, false, ErrLLMConfigGateClosed
		case llmConfigNoInvestigateReason:
			return OrgLLMConfig{}, false, ErrLLMConfigNoInvestigateConfig
		case llmConfigTurnedOffReason:
			return OrgLLMConfig{}, false, ErrLLMConfigTurnedOff
		case "":
			return OrgLLMConfig{}, false, nil
		}
		return OrgLLMConfig{}, false, errUnavailable
	case http.StatusUnauthorized, http.StatusForbidden:
		return OrgLLMConfig{}, false, errUnavailable
	case http.StatusConflict:
		return OrgLLMConfig{}, false, fmt.Errorf("%w: ops answered %d", ErrLLMConfigUnusable, response.StatusCode)
	default:
		return OrgLLMConfig{}, false, errUnavailable
	}
	payload, err := readBounded(response.Body, c.maxBody)
	if err != nil {
		return OrgLLMConfig{}, false, errUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var parsed llmConfigResponse
	if err := decoder.Decode(&parsed); err != nil {
		return OrgLLMConfig{}, false, errUnavailable
	}
	switch {
	case parsed.SchemaVersion != llmConfigSchemaVersion:
		return OrgLLMConfig{}, false, errUnavailable
	case parsed.OrgID != orgID:
		return OrgLLMConfig{}, false, errUnavailable
	case strings.TrimSpace(parsed.Provider) == "" || strings.TrimSpace(parsed.Model) == "":
		return OrgLLMConfig{}, false, errUnavailable
	case strings.TrimSpace(parsed.Version) == "":
		return OrgLLMConfig{}, false, errUnavailable
	}
	return OrgLLMConfig{
		Provider: strings.ToLower(strings.TrimSpace(parsed.Provider)), Model: strings.TrimSpace(parsed.Model),
		BaseURL: strings.TrimSpace(parsed.BaseURL), APIKey: llm.NewSecret(parsed.APIKey), PromptCache: parsed.PromptCache, Version: parsed.Version,
	}, true, nil
}

func llmConfigURL(baseURL *url.URL, orgID string) string {
	copy := *baseURL
	copy.Path = "/api/v1/internal/acr/orgs/" + url.PathEscape(orgID) + "/llm-config"
	return copy.String()
}
