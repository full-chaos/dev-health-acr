package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	serviceModelEntitlement        = "acr_service_model"
	serviceModelNotEntitledMessage = "Investigations on the service model are not enabled for this organization"
)

// serviceModelBasis is why a turn may, or may not, use the deployment model.
type serviceModelBasis string

const (
	serviceModelBasisWebAssertion   serviceModelBasis = "web_assertion"
	serviceModelBasisDeploymentOpen serviceModelBasis = "deployment_open"
	serviceModelBasisEntitlement    serviceModelBasis = "entitlement"
	serviceModelBasisOwnModel       serviceModelBasis = "own_model"
	serviceModelBasisClientModel    serviceModelBasis = "client_model"
	serviceModelBasisRefused        serviceModelBasis = "refused"
)

var (
	errServiceModelEntitlementUnavailable = errors.New("service model entitlement unavailable")
	errServiceModelOwnModelUnavailable    = errors.New("organization model configuration unavailable")
)

// serviceModelDecision decides whether an investigation may run on the
// deployment's own model. Web assertions (Ask Dev) are not decided here. A
// credential caller may when the deployment is open or the organization holds
// acr_service_model; otherwise, and also when that entitlement cannot be read,
// the deployment model is withheld, and a turn that needs a model at all runs
// only on the organization's own model configuration, or is refused (or, with
// the entitlement unreadable, answered as unavailable).
func (a *App) serviceModelDecision(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (serviceModelBasis, bool, error) {
	if principal.AuthenticationMethod != storage.AuthenticationMethodCredential {
		return serviceModelBasisWebAssertion, true, nil
	}
	if a.runtime.ServiceModelOpen {
		return serviceModelBasisDeploymentOpen, true, nil
	}
	entitled, featureErr := a.runtime.Entitlements.HasEntitlement(ctx, principal.OrgID, serviceModelEntitlement)
	if featureErr == nil && entitled {
		return serviceModelBasisEntitlement, true, nil
	}
	if featureErr != nil {
		a.logger.WarnContext(ctx, "context fabric service model entitlement unavailable; deployment model withheld", "request_id", contextfabric.SanitizeLogAttr(RequestID(ctx)))
	}
	if request.SuppliedInterpretation != nil && request.SynthesisMode == contextfabric.SynthesisModeClient {
		return serviceModelBasisClientModel, false, nil
	}
	own, err := a.orgHasOwnModel(ctx, principal)
	if err != nil {
		return "", false, err
	}
	if own {
		return serviceModelBasisOwnModel, false, nil
	}
	if featureErr != nil {
		return "", false, errServiceModelEntitlementUnavailable
	}
	return serviceModelBasisRefused, false, nil
}

// orgHasOwnModel reports whether the organization stored a model
// configuration. A stored configuration counts even when its credential cannot
// be used: that turn then fails on the organization's own model, never on the
// deployment's.
func (a *App) orgHasOwnModel(ctx context.Context, principal storage.Principal) (bool, error) {
	if a.runtime.OrgModelConfigs == nil {
		return false, nil
	}
	_, err := a.runtime.OrgModelConfigs.GetOrgModelConfig(ctx, principal)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, contextfabric.ErrOrgModelConfigNotFound):
		return false, nil
	default:
		return false, errServiceModelOwnModelUnavailable
	}
}

// gateServiceModel applies serviceModelDecision to one investigation request.
// It writes the refusal or the unavailability itself and returns false, or
// returns the context the engine runs under.
func (a *App) gateServiceModel(w http.ResponseWriter, r *http.Request, principal storage.Principal, request contextfabric.InvestigationRequest) (context.Context, bool) {
	ctx := r.Context()
	basis, allowed, err := a.serviceModelDecision(ctx, principal, request)
	switch {
	case errors.Is(err, errServiceModelEntitlementUnavailable):
		a.logger.WarnContext(ctx, "context fabric service model decision unavailable", "request_id", contextfabric.SanitizeLogAttr(RequestID(ctx)), "dependency", "entitlement")
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Entitlement service is temporarily unavailable", true, nil)
		return nil, false
	case err != nil:
		a.logger.WarnContext(ctx, "context fabric service model decision unavailable", "request_id", contextfabric.SanitizeLogAttr(RequestID(ctx)), "dependency", "org_model_config")
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "Organization model configuration is temporarily unavailable", true, nil)
		return nil, false
	}
	a.logger.InfoContext(ctx, "context fabric service model decision", "request_id", contextfabric.SanitizeLogAttr(RequestID(ctx)), "basis", string(basis), "deployment_model", allowed)
	if basis == serviceModelBasisRefused {
		a.recordReadAudit(ctx, principal, "entitlement_denied", "acr_entitlement", serviceModelEntitlement, "denied", nil)
		writeError(w, r, http.StatusForbidden, "feature_not_enabled", serviceModelNotEntitledMessage, false, map[string]any{"reason": contractsv1.ContextFabricServiceModelNotEntitledReason})
		return nil, false
	}
	if !allowed {
		ctx = contextfabric.WithPlatformModelWithheldBasis(ctx, string(basis))
	}
	return ctx, true
}
