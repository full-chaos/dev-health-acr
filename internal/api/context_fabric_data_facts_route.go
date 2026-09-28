package api

import "net/http"

// contextFabricDataFactsHandler serves S2 (read_facts). Stub until that slice lands
// (CHAOS-7071 registers the route and its protection chain only).
func (a *App) contextFabricDataFactsHandler() http.HandlerFunc {
	return writeContextFabricDataNotImplemented
}
