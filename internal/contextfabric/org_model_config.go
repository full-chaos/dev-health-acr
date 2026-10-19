package contextfabric

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/full-chaos/dev-health-go/llm"
	"log/slog"
	"time"
)

// ResolvedOrgModelConfig is the plaintext-credential shape
// OrgModelConfigResolver returns. It exists ONLY for constructing a
// per-organization contextfabric.ModelRuntime (see
// internal/contextfabric/modelruntimeresolver) and must never be
// marshaled, logged, or returned across an API boundary.
//
// Credential is deliberately excluded from every incidental serialization
// path a caller might reach for by habit (team-lead review requirement 5:
// "never in a struct that gets logged/serialized") -- String, LogValue, and
// MarshalJSON below all redact it, so a stray %v/%s format verb, a
// log/slog call passed this value directly, or an accidental json.Marshal
// cannot leak it even if the credential-only-in-the-runtime-construction-
// path discipline is violated somewhere else. json:"-" additionally removes
// the field from the default encoding/json path entirely; MarshalJSON exists
// so that even a caller using a custom marshaler still gets the redacted
// shape rather than a marshal error or a bypass.
//
// Generation (Codex round-1 findings F3/F4) is the cache key
// internal/contextfabric/modelruntimeresolver uses to decide whether a
// cached constructed runtime is still valid: a table-wide monotonic
// sequence value from the store, NOT a wall-clock timestamp. UpdatedAt
// alone cannot serve that role -- two upserts landing in the same
// clock_timestamp() tick (or a system clock stepping backward) would be
// indistinguishable, silently pinning a stale runtime/credential. Generation
// is also guaranteed never to repeat across a DELETE followed by a fresh
// write for the same organization, which an org_id-scoped counter (reset on
// each new row) would not guarantee.
type ResolvedOrgModelConfig struct {
	Provider      string
	BaseURL       string
	Model         string
	FallbackModel string
	Credential    llm.Secret `json:"-"`
	Generation    int64
	UpdatedAt     time.Time
}

// String redacts Credential -- see the type's doc comment.
func (r ResolvedOrgModelConfig) String() string {
	return fmt.Sprintf("ResolvedOrgModelConfig{Provider:%q BaseURL:%q Model:%q FallbackModel:%q Credential:%s Generation:%d UpdatedAt:%s}",
		r.Provider, r.BaseURL, r.Model, r.FallbackModel, redactedCredentialPlaceholder, r.Generation, r.UpdatedAt)
}

// GoString redacts Credential for the %#v format verb -- see the type's doc
// comment. Without this, %#v bypasses String/LogValue/MarshalJSON entirely
// (Codex round-1 finding F7: fmt's GoStringer interface is a distinct seam
// from Stringer, and Go's default %#v rendering of a struct prints every
// exported field verbatim, Credential included).
func (r ResolvedOrgModelConfig) GoString() string {
	return fmt.Sprintf("contextfabric.ResolvedOrgModelConfig{Provider:%q, BaseURL:%q, Model:%q, FallbackModel:%q, Credential:%q, Generation:%d, UpdatedAt:%#v}",
		r.Provider, r.BaseURL, r.Model, r.FallbackModel, redactedCredentialPlaceholder, r.Generation, r.UpdatedAt)
}

// LogValue redacts Credential for log/slog -- see the type's doc comment.
func (r ResolvedOrgModelConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("provider", SanitizeLogAttr(r.Provider)),
		slog.String("base_url", SanitizeLogAttr(r.BaseURL)),
		slog.String("model", SanitizeLogAttr(r.Model)),
		slog.String("fallback_model", SanitizeLogAttr(r.FallbackModel)),
		slog.String("credential", redactedCredentialPlaceholder),
		slog.Int64("generation", r.Generation),
		slog.Time("updated_at", r.UpdatedAt),
	)
}

// MarshalJSON redacts Credential -- see the type's doc comment. This is
// belt-and-suspenders alongside the Credential field's own json:"-" tag:
// that tag already removes it from the default encoding/json path, and this
// method keeps the redacted shape even for a caller that reaches for a
// custom marshaler.
func (r ResolvedOrgModelConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Provider      string    `json:"provider"`
		BaseURL       string    `json:"base_url,omitempty"`
		Model         string    `json:"model"`
		FallbackModel string    `json:"fallback_model,omitempty"`
		Credential    string    `json:"credential"`
		Generation    int64     `json:"generation"`
		UpdatedAt     time.Time `json:"updated_at"`
	}{r.Provider, r.BaseURL, r.Model, r.FallbackModel, redactedCredentialPlaceholder, r.Generation, r.UpdatedAt})
}

const redactedCredentialPlaceholder = "[REDACTED]"

// OrgModelConfigResolver resolves an organization's decrypted BYO LLM
// configuration. It returns (zero, false, nil) when the organization has no
// configuration -- the caller falls through to the deployment default. A
// non-nil error means the organization DOES have a configuration but it
// could not be read (e.g. a credential that fails to decrypt because its
// encryption key was retired); the caller must treat that as unavailable
// for this organization specifically, and must never fall back to the
// deployment credential (AC-3775-3's explicit prohibition).
type OrgModelConfigResolver interface {
	ResolveOrgModelConfig(context.Context, string) (ResolvedOrgModelConfig, bool, error)
}
