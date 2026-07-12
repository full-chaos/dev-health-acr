package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/episode"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func (a *App) handleEpisode(w http.ResponseWriter, r *http.Request) {
	if a.runtime.Episodes == nil {
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Episode recording is temporarily unavailable", true, nil)
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
	principal.ProductEntitlements = append(append([]string(nil), principal.ProductEntitlements...), agentContextRuntimeEntitlement)
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
	default:
		a.logger.ErrorContext(r.Context(), "episode recording dependency failed", "request_id", RequestID(r.Context()), "failure_class", "episode_creator")
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Episode recording is temporarily unavailable", true, nil)
	}
}
