package api

import "net/http"

// contextFabricDataSubjectsHandler serves S1a (find_subjects). Stub until that slice lands
// (CHAOS-7071 registers the route and its protection chain only).
func (a *App) contextFabricDataSubjectsHandler() http.HandlerFunc {
	return writeContextFabricDataNotImplemented
}
