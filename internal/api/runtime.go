package api

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

const agentContextRuntimeEntitlement = "agent_context_runtime"

type EntitlementProvider interface {
	HasEntitlement(context.Context, string, string) (bool, error)
}

type EntitlementFunc func(context.Context, string, string) (bool, error)

func (f EntitlementFunc) HasEntitlement(ctx context.Context, orgID, entitlement string) (bool, error) {
	return f(ctx, orgID, entitlement)
}

type ContextPacketAssembler interface {
	Assemble(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (contractsv1.ContextPacket, error)
}

type EpisodeCreator interface {
	Create(context.Context, storage.Principal, contractsv1.AgentEpisodeCreate) (contractsv1.AgentEpisode, bool, error)
}

// StoredResultAuthorizer is the stored-result authorization decision the
// retrieval route consults (contextfabric.StoredResultGate).
type StoredResultAuthorizer interface {
	Authorize(context.Context, storage.Principal, contextfabric.StoredInvestigationResult, contextfabric.StoredResultSurface) contextfabric.StoredResultAuthorization
}

type RuntimeDependencies struct {
	Credentials                *storage.CredentialLifecycle
	DeviceAuthorizations       storage.DeviceAuthorizationStore
	DeviceVerificationURL      string
	DeviceAuthorizationLimiter DeviceAuthorizationLimiter
	Audit                      storage.AuditStore
	Entitlements               EntitlementProvider
	Assembler                  ContextPacketAssembler
	Evidence                   storage.EvidenceStore
	Episodes                   EpisodeCreator
	// Investigator is optional -- context-fabric composition never fails
	// closed over an unconfigured optional dependency (same convention as
	// falkorgraph.Configured for the graph backend). When nil, the
	// investigations route is not registered at all (see Handler()),
	// mirroring how Episodes being nil leaves the episode route
	// unregistered.
	Investigator contextfabric.Investigator
	// InvestigationResults is optional (CHAOS-3746) and exposes the SAME
	// immutable result store the engine already writes through, so a
	// consumer can re-read a result by ID for replay, diagnostics, or
	// deeper inspection after receiving a bounded projection. It is
	// read-only at this boundary: the retrieval route never writes, and
	// composition keeps ownership of the Save path.
	//
	// It is wired independently of Investigator rather than reached
	// through it. Retrieval is a genuinely different capability from
	// investigation -- reading a stored answer costs no graph, fact, or
	// model work -- and folding it into the Investigator port would widen
	// a domain interface for one consumer's convenience.
	InvestigationResults contextfabric.InvestigationResultStore
	// StoredResultGate decides, live on every read, whether a stored result
	// may be served to the calling principal. It is the SAME gate the engine
	// decides its own prior-result reads with. When InvestigationResults is
	// configured and this is nil, the retrieval route fails closed as
	// unavailable rather than serve an undecided result.
	StoredResultGate StoredResultAuthorizer
	// DirectReadGate and DirectFactReader are the shared base of the direct
	// data tools (CHAOS-7071): the mandatory subject gate every direct read
	// passes, and the only path from a direct tool to the fact registry.
	// Both are nil when Context Fabric is not composed; a direct data
	// handler then fails closed as unavailable, never reads ungated.
	DirectReadGate   *directread.SubjectGate
	DirectFactReader *directread.FactReader
	// DataCatalogue, DataOperations and DataSubjects are the S1a direct
	// data tools (CHAOS-7072), each optional and independent -- same
	// convention as Investigator: an absent one never fails composition.
	//
	// DataCatalogue is the loaded operation policy artifact; nil when it
	// was not loaded (data_catalog then lists no operation and says
	// data_query_not_configured). DataOperations is the run_operation
	// runner; nil unless the internal query service URL is configured AND
	// the catalogue loaded (design E.5): the operations route then answers
	// feature_not_enabled with reason data_query_not_configured.
	// DataSubjects is the find_subjects lookup; nil when no graph is
	// composed: the subjects route then answers 503.
	DataCatalogue  *directread.Catalogue
	DataOperations DataOperationRunner
	DataSubjects   DataSubjectFinder
	// DataGraphQL is the graphql_query runner (CHAOS-7075); nil unless
	// ACR_DATA_GRAPHQL_URL is configured and the root policy derived: the
	// graphql route then answers feature_not_enabled with reason
	// data_graphql_not_configured.
	DataGraphQL DataGraphQLRunner
	// DirectRelationships serves read_relationships (CHAOS-7074) over the
	// same gate. Nil when the graph cannot serve bounded edge pages; the
	// route then fails closed as unavailable.
	DirectRelationships *directread.RelationshipsReader
	// OrgModelConfigs is optional (CHAOS-3775) -- same convention as
	// Investigator. When nil (no ACR_CONTEXT_FABRIC_CREDENTIAL_ENCRYPTION_KEYS
	// configured), the model-config routes stay registered, authorized, and
	// audited, and answer a clean 503 for every request, exactly like the
	// investigations route does with a nil Investigator.
	OrgModelConfigs contextfabric.OrgModelConfigStore
	// OrgModelRuntimeEvictor is optional and may be nil even when
	// OrgModelConfigs is non-nil (e.g. investigations disabled, so no
	// modelruntimeresolver.Resolver was ever constructed to evict from --
	// see internal/runtime/hosted's buildContextFabricInvestigator). When
	// nil, the DELETE model-config handler skips eviction entirely: there
	// is no cached runtime anywhere to purge if no resolver exists.
	OrgModelRuntimeEvictor contextfabric.OrgModelRuntimeEvictor
	// ReuseInvalidator is optional (CHAOS-3786, codex round-1 P1(b)) and
	// may be nil even when OrgModelConfigs is non-nil (e.g. answer reuse
	// itself is disabled -- ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE unset
	// -- so no reuse-capable investigation-result store was ever
	// composed). When non-nil, the model-config PUT and DELETE handlers
	// call InvalidateOrganizationReuse(principal.OrgID) after a
	// successful write: a chain change (primary or fallback model) must
	// invalidate reuse for that organization going forward, because a
	// stored candidate's chain-membership match is authorized by the
	// CURRENT chain, and a reconfiguration changes what that chain
	// vouches for. When nil, both handlers skip invalidation entirely --
	// there is no reuse-capable store to invalidate against.
	ReuseInvalidator contextfabric.ReuseInvalidator
	// ReadinessChecks gates /readyz -- the k8s readinessProbe target, which
	// pulls the WHOLE pod from every Service's endpoints (including the
	// OAuth/authorization-server routes) the moment any one check fails. It
	// therefore carries only the dependencies EVERY surface on this pod
	// needs: postgres and entitlement. ClickHouse does not belong here (see
	// DataStoreChecks) -- CHAOS-6745: an OAuth login has no ClickHouse
	// dependency, so a ClickHouse outage must never make the pod
	// unroutable for it.
	ReadinessChecks []ReadinessCheck
	// DataStoreChecks (CHAOS-6745) are checked live, per request, only by
	// the handlers that actually read ClickHouse (agent-context
	// context-packets/evidence, context-fabric investigations) -- never by
	// /readyz. A failing check here degrades those specific calls to a
	// typed, retryable "store unavailable" error; every other route on the
	// pod is unaffected.
	DataStoreChecks []ReadinessCheck
	// WorkloadTokenExchange is optional (CHAOS-4013): nil means no
	// Kubernetes TokenReview integration is configured for this
	// deployment, and the RFC 8693 grant on POST /api/v1/oauth/token
	// degrades to a clean 503 (see handleTokenExchange) -- the same
	// "unconfigured optional dependency never fails closed" convention as
	// Investigator/OrgModelConfigs above. The pre-existing device-code
	// grant on that same endpoint is entirely unaffected either way.
	WorkloadTokenExchange WorkloadTokenExchanger
	// OAuth is optional (nil: no OAuth routes); see OAuthRuntime.
	OAuth *OAuthRuntime
}

func (r *RuntimeDependencies) validate() error {
	if r == nil || r.Credentials == nil || storage.IsNil(r.DeviceAuthorizations) || storage.IsNil(r.Audit) || storage.IsNil(r.Entitlements) || storage.IsNil(r.Assembler) || storage.IsNil(r.Evidence) || storage.IsNil(r.DeviceAuthorizationLimiter) {
		return errors.New("hosted read runtime dependencies must be configured together")
	}
	verificationURL, err := url.ParseRequestURI(r.DeviceVerificationURL)
	if err != nil || !verificationURL.IsAbs() || verificationURL.Host == "" {
		return errors.New("hosted device authorization runtime requires an absolute verification URL")
	}
	if r.Episodes != nil && storage.IsNil(r.Episodes) {
		return errors.New("hosted episode runtime must not be typed nil")
	}
	if r.Investigator != nil && storage.IsNil(r.Investigator) {
		return errors.New("hosted context fabric investigator must not be typed nil")
	}
	if r.InvestigationResults != nil && storage.IsNil(r.InvestigationResults) {
		return errors.New("hosted context fabric investigation result store must not be typed nil")
	}
	if r.DataOperations != nil && storage.IsNil(r.DataOperations) {
		return errors.New("hosted data operation runner must not be typed nil")
	}
	if r.DataSubjects != nil && storage.IsNil(r.DataSubjects) {
		return errors.New("hosted data subject lookup must not be typed nil")
	}
	if r.DataOperations != nil && r.DataCatalogue == nil {
		return errors.New("hosted data operation runner requires its loaded catalogue")
	}
	if r.WorkloadTokenExchange != nil && storage.IsNil(r.WorkloadTokenExchange) {
		return errors.New("hosted workload token exchange must not be typed nil")
	}
	// CHAOS-6745: /readyz (r.ReadinessChecks) gates pod-level routing for
	// EVERY surface on this pod, so it carries only what every surface
	// needs -- postgres and entitlement, never clickhouse. ClickHouse is
	// validated separately, below, as r.DataStoreChecks.
	if len(r.ReadinessChecks) < 2 {
		return errors.New("hosted read runtime requires postgres and entitlement readiness checks")
	}
	required := map[string]bool{"postgres": false, "entitlement": false}
	for _, check := range r.ReadinessChecks {
		if storage.IsNil(check) {
			return errors.New("hosted read runtime readiness checks must not be nil")
		}
		name := strings.TrimSpace(check.Name())
		if name == "" {
			return errors.New("hosted read runtime readiness checks require a name")
		}
		if name == "clickhouse" {
			return errors.New("hosted read runtime must not gate /readyz on clickhouse -- see DataStoreChecks")
		}
		if seen, ok := required[name]; ok {
			if seen {
				return errors.New("hosted read runtime readiness checks must not repeat postgres or entitlement")
			}
			required[name] = true
		}
	}
	for _, name := range []string{"postgres", "entitlement"} {
		if !required[name] {
			return fmt.Errorf("hosted read runtime requires a %s readiness check", name)
		}
	}
	if len(r.DataStoreChecks) != 1 || storage.IsNil(r.DataStoreChecks[0]) || strings.TrimSpace(r.DataStoreChecks[0].Name()) != "clickhouse" {
		return errors.New("hosted read runtime requires exactly one clickhouse data-store check")
	}
	return nil
}

func (a *App) protectedRuntimeHandler(class limits.RequestClass, scope string, entitlement, allowWebAssertions bool, next http.Handler) http.Handler {
	if a.runtime == nil || a.authenticator == nil {
		return http.HandlerFunc(a.handleRuntimeUnavailable)
	}
	handler := next
	if entitlement {
		handler = a.requireEntitlement(agentContextRuntimeEntitlement, handler)
	}
	handler = a.requireClientVersion(handler)
	handler = LimitMiddleware(a.limits, class, handler)
	handler = a.authenticator.RequireScope(scope, handler)
	return a.authenticator.MiddlewareFor(allowWebAssertions, handler)
}

func (a *App) unauthenticatedRuntimeHandler(next http.Handler) http.Handler {
	if a.runtime == nil {
		return http.HandlerFunc(a.handleRuntimeUnavailable)
	}
	return next
}

func (a *App) requireClientVersion(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerRequest := r.Clone(r.Context())
		providerRequest.Header = r.Header.Clone()
		providerRequest.Header.Del("Authorization")
		providerRequest.Body = nil
		capabilities, err := a.capabilities.Capabilities(r.Context(), providerRequest)
		if err != nil {
			a.logger.ErrorContext(r.Context(), "capabilities resolution failed", "request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "failure_class", "capabilities_provider")
			writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Capabilities are temporarily unavailable", true, nil)
			return
		}
		clientVersion := strings.TrimSpace(r.Header.Get("X-ACR-Client-Version"))
		if clientVersion == "" || !clientVersionCompatible(clientVersion, capabilities.MinimumSidecarVersion) || revokedClientVersion(clientVersion, a.config.RevokedClientVersions) {
			writeError(w, r, http.StatusUpgradeRequired, "version_mismatch", "ACR client version is not supported", false, map[string]any{"minimum_client_version": capabilities.MinimumSidecarVersion})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func revokedClientVersion(clientVersion string, revokedVersions []string) bool {
	return slices.ContainsFunc(revokedVersions, func(revoked string) bool {
		return version.Exact(clientVersion, revoked)
	})
}

func (a *App) requireEntitlement(entitlement string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		enabled, err := a.runtime.Entitlements.HasEntitlement(r.Context(), principal.OrgID, entitlement)
		if err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Entitlement service is temporarily unavailable", true, nil)
			return
		}
		if !enabled {
			a.recordReadAudit(r.Context(), principal, "entitlement_denied", "acr_entitlement", entitlement, "denied", nil)
			writeError(w, r, http.StatusForbidden, "feature_not_enabled", "Agent Context Runtime is not enabled for this organization", false, nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleRuntimeUnavailable(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Hosted read runtime is temporarily unavailable", true, nil)
}

func (a *App) recordReadAudit(ctx context.Context, principal storage.Principal, action, resourceType, resourceID, status string, metadata map[string]any) {
	if a.runtime == nil || a.runtime.Audit == nil || strings.TrimSpace(principal.OrgID) == "" {
		return
	}
	actorType, actorID := principal.AuditActor()
	if status == "success" && a.usageTelemetry != nil {
		a.usageTelemetry.Enqueue(auth.UsageRecord{
			OrgID: principal.OrgID, ActorType: actorType, ActorID: actorID, Action: action,
			ResourceType: resourceType, ResourceID: resourceID, RequestID: RequestID(ctx), Metadata: cloneAuditMetadata(metadata), UsedAt: a.now().UTC(),
		})
		return
	}
	if status != "denied" {
		return
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := a.runtime.Audit.Record(auditCtx, storage.AuditEvent{
		OrgID: principal.OrgID, ActorType: actorType, ActorID: actorID,
		Action: action, ResourceType: resourceType, ResourceID: resourceID, Status: "denied",
		RequestID: RequestID(ctx), Metadata: metadata, CreatedAt: a.now().UTC(),
	}); err != nil {
		a.logger.WarnContext(ctx, "credential denial audit delivery failed", "failure_class", "denial_audit_delivery")
	}
}

func cloneAuditMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	copy := make(map[string]any, len(metadata))
	maps.Copy(copy, metadata)
	return copy
}
