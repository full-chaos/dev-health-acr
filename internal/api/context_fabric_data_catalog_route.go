package api

import "net/http"

// contextFabricDataCatalogHandler serves S1a (data_catalog). Stub until that slice lands
// (CHAOS-7071 registers the route and its protection chain only).
func (a *App) contextFabricDataCatalogHandler() http.HandlerFunc {
	return writeContextFabricDataNotImplemented
}
