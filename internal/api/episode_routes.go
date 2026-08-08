package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/episode"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ErrEpisodeWritebackNotEnabledForOrg is returned by an EpisodeCreator (for
// example CHAOS-3565's cohort decorator, internal/runtime/hosted) for an
// org writeback is not enabled for. It lives here rather than in the
// runtime package that raises it because writeEpisodeError needs to match
// it by identity without internal/api importing internal/runtime/hosted
// (which already imports internal/api for the EpisodeCreator interface --
// importing back would cycle). It intentionally carries no detail
// distinguishing "the feature is scoped to a cohort and you're not in it"
// from "the feature is off entirely": both map to the exact same response
// this handler already gives a nil a.runtime.Episodes, so a denied org sees
// exactly what it would see if writeback were disabled outright.
var ErrEpisodeWritebackNotEnabledForOrg = errors.New("episode writeback is not enabled for this organization")

// withAgentContextRuntimeEntitlement returns principal with
// agentContextRuntimeEntitlement appended to ProductEntitlements. This is
// the only place that field is ever populated: requireEntitlement (see
// protectedRuntimeHandler, runtime.go) verifies the org's entitlement via
// the real entitlement provider but never writes that fact back into the
// principal, so every handler behind it that calls into internal/episode
// (whose authorizeWrite/authorizeRead independently re-check
// ProductEntitlements as defense-in-depth) must translate "middleware
// already confirmed this" into the marker that check expects -- review
// finding B1 was exactly this translation missing from both read handlers
// (only the write handler did it), which made every authorized
// episode:read call fail with ErrEntitlementRequired in production. Builds
// a fresh slice rather than mutating principal.ProductEntitlements in
// place, since PrincipalFromContext's backing array may be shared.
func withAgentContextRuntimeEntitlement(principal storage.Principal) storage.Principal {
	principal.ProductEntitlements = append(append([]string(nil), principal.ProductEntitlements...), agentContextRuntimeEntitlement)
	return principal
}

func (a *App) handleEpisode(w http.ResponseWriter, r *http.Request) {
	if a.runtime.Episodes == nil {
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Episode recording is temporarily unavailable", false, nil)
		return
	}
	var create contractsv1.AgentEpisodeCreate
	if err := decodeJSONBody(w, r, a.config.MaxRequestBodyBytes, &create); err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, r, status, "invalid_request", "Episode request is invalid", false, nil)
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || utf8.RuneCountInString(keys[0]) < 8 || utf8.RuneCountInString(keys[0]) > 256 || keys[0] != create.IdempotencyKey || create.Validate() != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Episode request is invalid", false, nil)
		return
	}
	slug, err := auth.NormalizeRepositorySlug(create.Repository.Slug)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Episode request is invalid", false, nil)
		return
	}
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	if err := auth.AuthorizeRepository(principal, slug); err != nil {
		a.recordReadAudit(r.Context(), principal, "repository_denied", "repository", slug, "denied", nil)
		writeError(w, r, http.StatusForbidden, "repo_forbidden", "Credential is not authorized for this repository", false, nil)
		return
	}
	create.Repository = contractsv1.RepositoryRef{Slug: slug}
	principal = withAgentContextRuntimeEntitlement(principal)
	episode, duplicate, err := a.runtime.Episodes.Create(r.Context(), principal, create)
	if err != nil {
		a.writeEpisodeError(w, r, principal, create, err)
		return
	}
	episode.Duplicate = duplicate
	if err := episode.Validate(); err != nil {
		a.logger.ErrorContext(r.Context(), "episode creator returned invalid output", "request_id", RequestID(r.Context()), "failure_class", "episode_output")
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Episode response is invalid", false, nil)
		return
	}
	encoded, err := encodeBounded(episode, a.config.MaxEvidenceResponseBytes)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Episode response exceeded service limits", false, nil)
		return
	}
	if err := CompleteUsage(r.Context(), limits.ResourceUsage{Items: 1, Tokens: int64((len(encoded) + 3) / 4), Bytes: int64(len(encoded))}); err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Episode response exceeded service limits", false, nil)
		return
	}
	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
	}
	a.recordReadAudit(r.Context(), principal, "episode_recorded", "agent_episode", episode.EpisodeID, "success", map[string]any{"request_bytes": r.ContentLength, "response_bytes": len(encoded), "duplicate": duplicate})
	writeEncodedJSON(w, status, encoded)
}

// handleGetEpisode serves a single episode by its server-assigned
// episode_id. Cross-tenant access and deletion both surface as the same
// 404 a missing ID gets (see storage.EpisodeStore.GetByEpisodeID).
func (a *App) handleGetEpisode(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	principal = withAgentContextRuntimeEntitlement(principal)
	if a.runtime.EpisodeReader == nil {
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Episode reads are temporarily unavailable", true, nil)
		return
	}
	episodeID := r.PathValue("episode_id")
	if strings.TrimSpace(episodeID) != episodeID || len([]rune(episodeID)) < 8 || len([]rune(episodeID)) > 256 {
		a.writeEpisodeReadNotFound(w, r, principal)
		return
	}
	found, err := a.runtime.EpisodeReader.GetByID(r.Context(), principal, episodeID)
	if err != nil {
		a.writeEpisodeReadError(w, r, principal, err)
		return
	}
	if err := found.Validate(); err != nil {
		a.logger.ErrorContext(r.Context(), "episode reader returned invalid output", "request_id", RequestID(r.Context()), "failure_class", "episode_output")
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Episode response is invalid", false, nil)
		return
	}
	encoded, err := encodeBounded(found, a.config.MaxEvidenceResponseBytes)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Episode response exceeded service limits", false, nil)
		return
	}
	if err := CompleteUsage(r.Context(), limits.ResourceUsage{Items: 1, Tokens: int64((len(encoded) + 3) / 4), Bytes: int64(len(encoded))}); err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Episode response exceeded service limits", false, nil)
		return
	}
	a.recordReadAudit(r.Context(), principal, "episode_read", "agent_episode", found.EpisodeID, "success", nil)
	writeEncodedJSON(w, http.StatusOK, encoded)
}

// handleListEpisodes serves the caller's episodes, newest first, optionally
// filtered to one repository. The response is a bare JSON array of
// agent_episode.v1 objects -- each element already carries its own
// schema_version, so no separate collection-level contract is needed.
func (a *App) handleListEpisodes(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	principal = withAgentContextRuntimeEntitlement(principal)
	if a.runtime.EpisodeReader == nil {
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Episode reads are temporarily unavailable", true, nil)
		return
	}
	repositorySlug := strings.TrimSpace(r.URL.Query().Get("repository"))
	if repositorySlug != "" {
		slug, err := auth.NormalizeRepositorySlug(repositorySlug)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "repository is invalid", false, nil)
			return
		}
		repositorySlug = slug
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, convErr := strconv.Atoi(raw)
		// A limit string that overflows Go's int range (e.g. sixty digits)
		// is not a syntax error -- strconv.Atoi still parses the sign and
		// digits, it just can't represent the magnitude, and returns
		// math.MaxInt/MinInt with strconv.ErrRange. The OpenAPI contract
		// (L8) promises any value above the service maximum is silently
		// clamped downstream, never rejected -- true for limit=10000, so it
		// must also be true for an overflowing positive value, which is
		// just an even larger instance of the same case. A negative
		// overflow is still semantically invalid the same way limit=-1 is,
		// so it stays rejected.
		var numErr *strconv.NumError
		if convErr != nil && errors.As(convErr, &numErr) && errors.Is(numErr.Err, strconv.ErrRange) && parsed >= 0 {
			convErr = nil
		}
		if convErr != nil || parsed < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit is invalid", false, nil)
			return
		}
		limit = parsed
	}
	episodes, err := a.runtime.EpisodeReader.List(r.Context(), principal, repositorySlug, limit)
	if err != nil {
		a.writeEpisodeReadError(w, r, principal, err)
		return
	}
	if episodes == nil {
		episodes = []contractsv1.AgentEpisode{}
	}
	for _, episode := range episodes {
		if err := episode.Validate(); err != nil {
			a.logger.ErrorContext(r.Context(), "episode reader returned invalid output", "request_id", RequestID(r.Context()), "failure_class", "episode_output")
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Episode list response is invalid", false, nil)
			return
		}
	}
	encoded, err := encodeBounded(episodes, a.config.MaxEvidenceResponseBytes)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Episode list response exceeded service limits", false, nil)
		return
	}
	if err := CompleteUsage(r.Context(), limits.ResourceUsage{Items: int64(len(episodes)), Tokens: int64((len(encoded) + 3) / 4), Bytes: int64(len(encoded))}); err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Episode list response exceeded service limits", false, nil)
		return
	}
	a.recordReadAudit(r.Context(), principal, "episode_list", "agent_episode", "list", "success", map[string]any{"count": len(episodes)})
	writeEncodedJSON(w, http.StatusOK, encoded)
}

func (a *App) writeEpisodeReadNotFound(w http.ResponseWriter, r *http.Request, principal storage.Principal) {
	a.recordReadAudit(r.Context(), principal, "episode_read_denied", "agent_episode", "unavailable", "denied", nil)
	writeError(w, r, http.StatusNotFound, "not_found", "Episode was not found", false, nil)
}

// writeEpisodeReadError classifies internal/episode's authorizeRead errors
// explicitly, mirroring writeEpisodeError's pattern for the write path
// (review finding H2): these are permanent, expected auth refusals -- a
// credential structurally missing repository scope, the episode:read
// scope, or the runtime entitlement -- not transient dependency failures,
// so they must not fall into writeReadDependencyError's generic
// ERROR-logged, retryable:true default. In practice these should never
// trigger post-B1-fix (protectedRuntimeHandler's middleware already
// enforces scope and entitlement before the handler runs), but authorizeRead
// re-checks both as defense-in-depth, so this handler must classify
// whatever it can actually return. The four causes that collapse into the
// same not-found (cross-tenant access, unauthorized repository scope on an
// existing episode, redaction, retention expiry) are untouched -- those are
// per-record outcomes from the store, never surfaced here as anything but
// storage.ErrNotFound.
func (a *App) writeEpisodeReadError(w http.ResponseWriter, r *http.Request, principal storage.Principal, err error) {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		a.writeEpisodeReadNotFound(w, r, principal)
	case errors.Is(err, auth.ErrRepositoryForbidden):
		a.recordReadAudit(r.Context(), principal, "episode_read_denied", "agent_episode", "unavailable", "denied", nil)
		writeError(w, r, http.StatusForbidden, "repo_forbidden", "Credential is not authorized for this repository", false, nil)
	case errors.Is(err, auth.ErrInsufficientScope):
		a.recordReadAudit(r.Context(), principal, "episode_read_denied", "agent_episode", "unavailable", "denied", nil)
		writeError(w, r, http.StatusForbidden, "insufficient_scope", "Credential is missing the required scope", false, map[string]any{"required_scope": auth.ScopeEpisodeRead})
	case errors.Is(err, episode.ErrEntitlementRequired):
		a.recordReadAudit(r.Context(), principal, "episode_read_denied", "agent_episode", "unavailable", "denied", nil)
		writeError(w, r, http.StatusForbidden, "feature_not_enabled", "Agent Context Runtime is not enabled for this organization", false, nil)
	default:
		a.writeReadDependencyError(w, r, err, "episode_read")
	}
}

func (a *App) writeEpisodeError(w http.ResponseWriter, r *http.Request, principal storage.Principal, create contractsv1.AgentEpisodeCreate, err error) {
	switch {
	case errors.Is(err, episode.ErrNoPersistAccepted):
		if usageErr := CompleteUsage(r.Context(), limits.ResourceUsage{Items: 1, Tokens: 0, Bytes: 0}); usageErr != nil {
			writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Episode response exceeded service limits", false, nil)
			return
		}
		a.recordReadAudit(r.Context(), principal, "episode_recorded", "agent_episode", create.ClientEpisodeID, "success", map[string]any{"request_bytes": r.ContentLength, "response_bytes": 0, "no_persist": true})
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, storage.ErrNotFound):
		a.recordReadAudit(r.Context(), principal, "episode_not_found", "context_packet", "unavailable", "denied", nil)
		writeError(w, r, http.StatusNotFound, "not_found", "Context packet was not found", false, nil)
	case errors.Is(err, storage.ErrConflict):
		writeError(w, r, http.StatusConflict, "invalid_request", "Episode conflicts with an existing record", false, nil)
	case strings.HasPrefix(err.Error(), "invalid episode:"):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Episode request is invalid", false, nil)
	case errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled):
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Episode recording was canceled", true, nil)
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded):
		writeError(w, r, http.StatusGatewayTimeout, "upstream_unavailable", "Episode recording timed out", true, nil)
	case errors.Is(err, ErrEpisodeWritebackNotEnabledForOrg):
		// A permanent, expected denial (writeback is off for this org, by
		// config) -- not a transient dependency failure, so no ERROR log and
		// no retryable:true (retrying the identical request cannot succeed
		// until the org is added to the cohort). The response body is
		// byte-identical to the nil a.runtime.Episodes case above: neither
		// leaks whether a cohort exists that this org merely isn't in.
		a.recordReadAudit(r.Context(), principal, "episode_write_denied", "agent_episode", "unavailable", "denied", nil)
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Episode recording is temporarily unavailable", false, nil)
	default:
		a.logger.ErrorContext(r.Context(), "episode recording dependency failed", "request_id", RequestID(r.Context()), "failure_class", "episode_creator")
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Episode recording is temporarily unavailable", true, nil)
	}
}
