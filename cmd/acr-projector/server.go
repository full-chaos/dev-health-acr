package main

import (
	"encoding/json"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/api"
)

type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Version string `json:"version"`
}

type readinessCheckResponse struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type readinessResponse struct {
	Status  string                   `json:"status"`
	Service string                   `json:"service"`
	Version string                   `json:"version"`
	Enabled bool                     `json:"projection_enabled"`
	Checks  []readinessCheckResponse `json:"checks"`
}

// readinessHandler serves GET /healthz and GET /readyz for cmd/acr-projector,
// mirroring internal/api.App's response shape so operators see one
// consistent readiness contract across both ACR binaries. When Checks is
// empty (projection disabled, see openRuntime), /readyz reports ready with
// projection_enabled=false rather than failing: a deliberately disabled
// projector is a healthy state, not an outage.
func readinessHandler(serviceVersion string, checks []api.ReadinessCheck) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Service: "acr-projector", Version: serviceVersion})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		response := readinessResponse{
			Status: "ready", Service: "acr-projector", Version: serviceVersion,
			Enabled: len(checks) > 0, Checks: make([]readinessCheckResponse, 0, len(checks)),
		}
		status := http.StatusOK
		for _, check := range checks {
			checkStatus := "ready"
			if err := check.Check(r.Context()); err != nil {
				checkStatus = "not_ready"
				response.Status = "not_ready"
				status = http.StatusServiceUnavailable
			}
			response.Checks = append(response.Checks, readinessCheckResponse{Name: check.Name(), Status: checkStatus})
		}
		writeJSON(w, status, response)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
