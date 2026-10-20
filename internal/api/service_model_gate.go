package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	serviceModelEntitlement          = "acr_service_model"
	platformModelFallbackEntitlement = "platform_model_fallback"
	byoLLMGateResource               = "byo_llm"
	serviceModelNotEntitledMessage   = "Investigations on the service model are not enabled for this organization"
	ownModelGateClosedMessage        = "This organization's own model is saved but not enabled for its plan"
	aiFeaturesTurnedOffMessage       = "ACR AI features are turned off for this organization"
	noInvestigateConfigMessage       = "This organization's saved model keys are for other uses only; none is set for investigations"
)

// serviceModelBasis is why a turn may, or may not, use the deployment model.
type serviceModelBasis string

const (
	serviceModelBasisDeploymentOpen        serviceModelBasis = "deployment_open"
	serviceModelBasisPlatformModelFallback serviceModelBasis = "platform_model_fallback"
	serviceModelBasisOwnModel              serviceModelBasis = "own_model"
	serviceModelBasisOwnModelGateClosed    serviceModelBasis = "own_model_gate_closed"
	serviceModelBasisAIFeaturesTurnedOff   serviceModelBasis = "ai_features_turned_off"
	serviceModelBasisNoInvestigateConfig   serviceModelBasis = "byo_no_investigate_config"
	serviceModelBasisClientModel           serviceModelBasis = "client_model"
	serviceModelBasisRefused               serviceModelBasis = "refused"
)

var (
	errServiceModelEntitlementUnavailable = errors.New("service model entitlement unavailable")
	errServiceModelOwnModelUnavailable    = errors.New("organization model configuration unavailable")
)

// ownModelState is what the organization model configuration says.
type ownModelState int

const (
	ownModelNone ownModelState = iota
	ownModelSaved
	ownModelGateClosed
	ownModelNoInvestigateConfig
)

// serviceModelDecision decides which model an investigation may run on, for
// every caller (credential and web assertion alike):
//   - the caller's own interpretation and synthesis: decided on the same rule
//     (a saved configuration, a closed gate and the feature switch apply to it
//     too, because acr's own phrasing still binds a model); with no
//     configuration and no grant it is served with the deployment model
//     withheld;
//   - a saved configuration behind a closed byo_llm gate: refused with its own
//     reason, whatever the flags;
//   - saved BYO keys for other purposes only (none resolves for
//     investigations): a BYO org, refused with its own reason, whatever the
//     flags; never the platform model;
//   - a saved organization model configuration: the organization's own model
//     when acr_service_model (ops serves the effective value: the org's
//     override, else ON for a saved configuration) is ON, with the deployment
//     model withheld; refused as turned off when it is OFF, and answered as
//     unavailable when it cannot be read;
//   - no configuration: the deployment model only when the deployment is open
//     or the organization holds both acr_service_model (AI features on) and
//     platform_model_fallback (the platform-admin grant); otherwise refused,
//     or answered as unavailable when the entitlement cannot be read.
func (a *App) serviceModelDecision(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (serviceModelBasis, bool, error) {
	basis, granted, featureErr := a.deploymentModelGrant(ctx, principal)
	if errors.Is(featureErr, entitlements.ErrOrganizationUnknown) {
		return "", false, entitlements.ErrOrganizationUnknown
	}
	if featureErr != nil {
		a.logger.WarnContext(ctx, "context fabric service model entitlement unavailable; deployment model withheld", "request_id", contextfabric.SanitizeLogAttr(RequestID(ctx)), "reason", entitlements.RejectionReason(featureErr))
	}
	callerModel := request.SuppliedInterpretation != nil && request.SynthesisMode == contextfabric.SynthesisModeClient
	state, err := a.orgOwnModel(ctx, principal)
	if err != nil {
		return "", false, err
	}
	switch {
	case state == ownModelGateClosed:
		return serviceModelBasisOwnModelGateClosed, false, nil
	case state == ownModelNoInvestigateConfig:
		return serviceModelBasisNoInvestigateConfig, false, nil
	case state == ownModelSaved:
		on, err := a.serviceModelOn(ctx, principal)
		switch {
		case err != nil:
			return "", false, fmt.Errorf("%w: %w", errServiceModelEntitlementUnavailable, err)
		case !on:
			return serviceModelBasisAIFeaturesTurnedOff, false, nil
		default:
			return serviceModelBasisOwnModel, false, nil
		}
	case granted:
		return basis, true, nil
	case callerModel:
		return serviceModelBasisClientModel, false, nil
	case featureErr != nil:
		return "", false, fmt.Errorf("%w: %w", errServiceModelEntitlementUnavailable, featureErr)
	default:
		return serviceModelBasisRefused, false, nil
	}
}

// deploymentModelGrant reports whether the organization may draw the
// deployment's own model when it has no model configuration of its own: the
// deployment is open (every organization holds both flags), or the
// organization holds acr_service_model AND platform_model_fallback. The error
// is the entitlement read failure, if any.
func (a *App) deploymentModelGrant(ctx context.Context, principal storage.Principal) (serviceModelBasis, bool, error) {
	if a.runtime.ServiceModelOpen {
		return serviceModelBasisDeploymentOpen, true, nil
	}
	featureOn, err := a.runtime.Entitlements.HasEntitlement(ctx, principal.OrgID, serviceModelEntitlement)
	if err != nil {
		return "", false, err
	}
	fallback, err := a.runtime.Entitlements.HasEntitlement(ctx, principal.OrgID, platformModelFallbackEntitlement)
	if err != nil {
		return "", false, err
	}
	if featureOn && fallback {
		return serviceModelBasisPlatformModelFallback, true, nil
	}
	return "", false, nil
}

// serviceModelOn reports whether ACR AI features are on for the organization:
// the deployment is open, or ops serves acr_service_model ON (the effective
// value: the org's explicit override, else ON for a saved BYO configuration,
// else the plan or admin state).
func (a *App) serviceModelOn(ctx context.Context, principal storage.Principal) (bool, error) {
	if a.runtime.ServiceModelOpen {
		return true, nil
	}
	return a.runtime.Entitlements.HasEntitlement(ctx, principal.OrgID, serviceModelEntitlement)
}

// missingPlatformModelGrant names the flag a refused organization lacks, for
// the audit row: acr_service_model first, then platform_model_fallback.
func (a *App) missingPlatformModelGrant(ctx context.Context, principal storage.Principal) string {
	on, err := a.runtime.Entitlements.HasEntitlement(ctx, principal.OrgID, serviceModelEntitlement)
	if err != nil {
		a.logger.WarnContext(ctx, "context fabric service model audit label unavailable", "request_id", contextfabric.SanitizeLogAttr(RequestID(ctx)), "reason", entitlements.RejectionReason(err))
		return serviceModelEntitlement
	}
	if on {
		return platformModelFallbackEntitlement
	}
	return serviceModelEntitlement
}

// serviceModelListable reports whether investigate_question can serve this
// caller: the organization has a saved model configuration with
// acr_service_model ON (or behind a closed byo_llm gate, so a call answers its
// reason), or the deployment model is granted. A read failure keeps the tool listed, because the call itself
// still answers the typed refusal or unavailability.
func (a *App) serviceModelListable(ctx context.Context, principal storage.Principal) bool {
	listed, reason := true, "granted"
	var entitlementReason string
	state, ownErr := a.orgOwnModel(ctx, principal)
	switch {
	case ownErr != nil:
		reason = "model_config_unreadable"
	case state == ownModelGateClosed:
		reason = "own_model_gate_closed"
	case state == ownModelNoInvestigateConfig:
		reason = "byo_no_investigate_config"
	case state == ownModelSaved:
		on, err := a.serviceModelOn(ctx, principal)
		switch {
		case err != nil:
			reason, entitlementReason = "entitlement_unreadable", entitlements.RejectionReason(err)
		case !on:
			listed, reason = false, "ai_features_turned_off"
		default:
			reason = "own_model"
		}
	default:
		if _, granted, err := a.deploymentModelGrant(ctx, principal); err != nil {
			reason, entitlementReason = "entitlement_unreadable", entitlements.RejectionReason(err)
		} else if !granted {
			listed, reason = false, "no_service_model"
		}
	}
	attrs := []any{"request_id", contextfabric.SanitizeLogAttr(RequestID(ctx)), "investigate_question_listed", listed, "reason", reason}
	if entitlementReason != "" {
		attrs = append(attrs, "entitlement_reason", entitlementReason)
	}
	a.logger.InfoContext(ctx, "context fabric service model listing decision", attrs...)
	return listed
}

// orgOwnModel reports what the organization's BYO model configuration says. A
// stored configuration counts as saved even when its credential cannot be
// used: that turn then fails on the organization's own model, never on the
// deployment's.
func (a *App) orgOwnModel(ctx context.Context, principal storage.Principal) (ownModelState, error) {
	if a.runtime.OrgModelConfigs == nil {
		return ownModelNone, nil
	}
	_, found, err := a.runtime.OrgModelConfigs.ResolveOrgModelConfig(ctx, principal.OrgID)
	switch {
	case errors.Is(err, entitlements.ErrLLMConfigGateClosed):
		return ownModelGateClosed, nil
	case errors.Is(err, entitlements.ErrLLMConfigNoInvestigateConfig):
		return ownModelNoInvestigateConfig, nil
	case errors.Is(err, entitlements.ErrLLMConfigUnusable):
		return ownModelSaved, nil
	case err != nil:
		return ownModelNone, errServiceModelOwnModelUnavailable
	case found:
		return ownModelSaved, nil
	default:
		return ownModelNone, nil
	}
}

// gateServiceModel applies serviceModelDecision to one investigation request.
// It writes the refusal or the unavailability itself and returns false, or
// returns the context the engine runs under.
func (a *App) gateServiceModel(w http.ResponseWriter, r *http.Request, principal storage.Principal, request contextfabric.InvestigationRequest) (context.Context, bool) {
	ctx := r.Context()
	basis, allowed, err := a.serviceModelDecision(ctx, principal, request)
	switch {
	case errors.Is(err, entitlements.ErrOrganizationUnknown):
		a.writeEntitlementLookupError(w, r, err)
		return nil, false
	case errors.Is(err, errServiceModelEntitlementUnavailable):
		a.logger.WarnContext(ctx, "context fabric service model decision unavailable", "request_id", contextfabric.SanitizeLogAttr(RequestID(ctx)), "dependency", "entitlement", "reason", entitlements.RejectionReason(err))
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Entitlement service is temporarily unavailable", true, nil)
		return nil, false
	case err != nil:
		a.logger.WarnContext(ctx, "context fabric service model decision unavailable", "request_id", contextfabric.SanitizeLogAttr(RequestID(ctx)), "dependency", "org_model_config")
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "Organization model configuration is temporarily unavailable", true, nil)
		return nil, false
	}
	a.logger.InfoContext(ctx, "context fabric service model decision", "request_id", contextfabric.SanitizeLogAttr(RequestID(ctx)), "basis", string(basis), "deployment_model", allowed)
	switch basis {
	case serviceModelBasisRefused:
		a.recordReadAudit(ctx, principal, "entitlement_denied", "acr_entitlement", a.missingPlatformModelGrant(ctx, principal), "denied", nil)
		writeError(w, r, http.StatusForbidden, "feature_not_enabled", serviceModelNotEntitledMessage, false, map[string]any{"reason": contractsv1.ContextFabricServiceModelNotEntitledReason})
		return nil, false
	case serviceModelBasisOwnModelGateClosed:
		a.recordReadAudit(ctx, principal, "entitlement_denied", "acr_entitlement", byoLLMGateResource, "denied", nil)
		writeError(w, r, http.StatusForbidden, "feature_not_enabled", ownModelGateClosedMessage, false, map[string]any{"reason": contractsv1.ContextFabricOwnModelGateClosedReason})
		return nil, false
	case serviceModelBasisNoInvestigateConfig:
		a.recordReadAudit(ctx, principal, "entitlement_denied", "acr_entitlement", byoLLMGateResource, "denied", nil)
		writeError(w, r, http.StatusForbidden, "feature_not_enabled", noInvestigateConfigMessage, false, map[string]any{"reason": contractsv1.ContextFabricNoInvestigateConfigReason})
		return nil, false
	case serviceModelBasisAIFeaturesTurnedOff:
		a.recordReadAudit(ctx, principal, "entitlement_denied", "acr_entitlement", serviceModelEntitlement, "denied", nil)
		writeError(w, r, http.StatusForbidden, "feature_not_enabled", aiFeaturesTurnedOffMessage, false, map[string]any{"reason": contractsv1.ContextFabricAIFeaturesTurnedOffReason})
		return nil, false
	}
	if !allowed {
		ctx = contextfabric.WithPlatformModelWithheldBasis(ctx, string(basis))
	}
	if basis == serviceModelBasisPlatformModelFallback || basis == serviceModelBasisDeploymentOpen {
		// The basis binds the turn: it runs on the deployment model for every
		// call, whatever the organization configuration says by then.
		ctx = contextfabric.WithDeploymentModelBound(ctx)
	}
	return ctx, true
}
