package api

import "net/http"

// contextFabricDataOperationsHandler serves S1a (run_operation). Stub until that slice lands
// (CHAOS-7071 registers the route and its protection chain only).
func (a *App) contextFabricDataOperationsHandler() http.HandlerFunc {
	return writeContextFabricDataNotImplemented
}
