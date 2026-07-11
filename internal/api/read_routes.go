package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func (a *App) handleContextPacket(w http.ResponseWriter, r *http.Request) {
	var request contractsv1.ContextPacketRequest
	if err := decodeJSONBody(w, r, a.config.MaxRequestBodyBytes, &request); err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, r, status, "invalid_request", "Context packet request is invalid", false, nil)
		return
	}
	if err := request.Validate(); err != nil || !a.requestWithinLimits(request) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Context packet request is invalid", false, nil)
		return
	}
	slug, err := auth.NormalizeRepositorySlug(request.Repository.Slug)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Context packet request is invalid", false, nil)
		return
	}
	request.RequestID = RequestID(r.Context())
	request.Repository = contractsv1.RepositoryRef{Slug: slug}
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
	if !a.allowReadEntitlement(w, r, principal) {
		return
	}
	packet, err := a.runtime.Assembler.Assemble(r.Context(), principal, request)
	if err != nil {
		a.writeReadDependencyError(w, r, err, "context_packet_assembly")
		return
	}
	maximumBytes := min(int64(a.config.MaxSerializedBytes), int64(request.Options.MaxSerializedBytes))
	encoded, err := encodeBounded(packet, maximumBytes)
	if err != nil || !packetWithinRequestLimits(packet, request) {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Context packet response exceeded service limits", false, nil)
		return
	}
	usage := limits.ResourceUsage{Items: int64(len(packet.Items)), Tokens: int64(packet.Budget.EstimatedTokens), Bytes: int64(len(encoded))}
	if err := CompleteUsage(r.Context(), usage); err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Context packet response exceeded service limits", false, nil)
		return
	}
	a.recordReadAudit(r.Context(), principal, "context_packet_generated", "context_packet", packet.ContextPacketID, "success", map[string]any{"packet_status": packet.Status})
	writeEncodedJSON(w, http.StatusOK, encoded)
}

func (a *App) handleEvidence(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	referenceID := r.PathValue("evidence_ref_id")
	if strings.TrimSpace(referenceID) != referenceID || len([]rune(referenceID)) < 8 || len([]rune(referenceID)) > 256 {
		a.writeEvidenceNotFound(w, r, principal)
		return
	}
	expanded, err := a.runtime.Evidence.ResolveEvidence(r.Context(), principal, referenceID)
	if err != nil {
		a.writeEvidenceError(w, r, principal, err)
		return
	}
	if expanded.Availability == contractsv1.EvidenceDeleted || expanded.Availability == contractsv1.EvidenceUnauthorized {
		a.writeEvidenceNotFound(w, r, principal)
		return
	}
	encoded, err := encodeBounded(expanded, a.config.MaxEvidenceResponseBytes)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Evidence response exceeded service limits", false, nil)
		return
	}
	usage := limits.ResourceUsage{Items: 1, Tokens: int64((len(encoded) + 3) / 4), Bytes: int64(len(encoded))}
	if err := CompleteUsage(r.Context(), usage); err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Evidence response exceeded service limits", false, nil)
		return
	}
	a.recordReadAudit(r.Context(), principal, "evidence_expanded", "evidence", "expanded", "success", nil)
	writeEncodedJSON(w, http.StatusOK, encoded)
}

func (a *App) requestWithinLimits(request contractsv1.ContextPacketRequest) bool {
	return request.Options.MaxItems <= a.config.MaxItems && request.Options.MaxOutputTokens <= a.config.MaxOutputTokens && request.Options.MaxSerializedBytes <= a.config.MaxSerializedBytes
}

func packetWithinRequestLimits(packet contractsv1.ContextPacket, request contractsv1.ContextPacketRequest) bool {
	return len(packet.Items) <= request.Options.MaxItems &&
		packet.Budget.ItemsUsed >= 0 && packet.Budget.ItemsUsed <= request.Options.MaxItems &&
		packet.Budget.EstimatedTokens >= 0 && packet.Budget.EstimatedTokens <= request.Options.MaxOutputTokens &&
		packet.Budget.SerializedBytes >= 0 && packet.Budget.SerializedBytes <= request.Options.MaxSerializedBytes
}

func (a *App) allowReadEntitlement(w http.ResponseWriter, r *http.Request, principal storage.Principal) bool {
	enabled, err := a.runtime.Entitlements.HasEntitlement(r.Context(), principal.OrgID, agentContextRuntimeEntitlement)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Entitlement service is temporarily unavailable", true, nil)
		return false
	}
	if enabled {
		return true
	}
	a.recordReadAudit(r.Context(), principal, "entitlement_denied", "acr_entitlement", agentContextRuntimeEntitlement, "denied", nil)
	writeError(w, r, http.StatusForbidden, "feature_not_enabled", "Agent Context Runtime is not enabled for this organization", false, nil)
	return false
}

func (a *App) writeEvidenceError(w http.ResponseWriter, r *http.Request, principal storage.Principal, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		a.writeEvidenceNotFound(w, r, principal)
		return
	}
	a.writeReadDependencyError(w, r, err, "evidence_resolution")
}

func (a *App) writeEvidenceNotFound(w http.ResponseWriter, r *http.Request, principal storage.Principal) {
	a.recordReadAudit(r.Context(), principal, "evidence_denied", "evidence", "unavailable", "denied", nil)
	writeError(w, r, http.StatusNotFound, "not_found", "Evidence was not found", false, nil)
}

func (a *App) writeReadDependencyError(w http.ResponseWriter, r *http.Request, err error, failureClass string) {
	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		writeError(w, r, http.StatusGatewayTimeout, "upstream_unavailable", "The read operation timed out", true, nil)
		return
	}
	a.logger.ErrorContext(r.Context(), "hosted read dependency failed", "request_id", RequestID(r.Context()), "failure_class", failureClass)
	writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "The read operation is temporarily unavailable", true, nil)
}
