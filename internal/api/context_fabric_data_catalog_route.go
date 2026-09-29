package api

import (
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// catalogScopeTokens are the scopes the caller section may name.
var catalogScopeTokens = []string{auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.ScopeEpisodeWrite, auth.ScopeContextAdmin, auth.ScopeDataRead}

// contextFabricDataCatalogHandler serves data_catalog (CHAOS-7072, S1a;
// design C.3). The catalogue is built for THIS caller: its grant class
// decides the operation list, its scopes decide availability, and the
// caller section carries scopes and class only -- never a repository name
// or a grant count. The sections query parameter (comma list) filters; an
// unknown section is 400 invalid_request.
func (a *App) contextFabricDataCatalogHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sections, err := directread.ParseCatalogSections(r.URL.Query().Get("sections"))
		if err != nil || len(r.URL.Query()["sections"]) > 1 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "data_catalog request is invalid", false, map[string]any{"reason": "unknown_section"})
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		scopes := []string{}
		for _, scope := range catalogScopeTokens {
			if auth.HasScope(principal.Permissions, scope) {
				scopes = append(scopes, scope)
			}
		}
		// The kinds come from the registry read_facts validates against.
		facts := a.directFacts()
		var factCapabilities []contextfabric.FactCapability
		if facts != nil {
			factCapabilities = a.runtime.DirectFactReader.Capabilities()
		}
		catalog := directread.BuildDataCatalog(a.dataCatalogue(), directread.CatalogCaller{
			PrincipalClass:     directread.ClassifyPrincipal(principal),
			Scopes:             scopes,
			DataRead:           auth.HasScope(principal.Permissions, auth.ScopeDataRead),
			OperationsServable: a.dataOperations() != nil,
			FactsServable:      facts != nil,
			FactCapabilities:   factCapabilities,
		}, sections)
		encoded, err := encodeBounded(catalog, contextFabricDataResponseBytes)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Direct data response exceeded service limits", false, nil)
			return
		}
		a.recordReadAudit(r.Context(), principal, "direct_catalog_read", "context_fabric_data_catalog", "catalog", "success", nil)
		writeEncodedJSON(w, http.StatusOK, encoded)
	}
}
