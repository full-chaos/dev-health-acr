package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var version = "dev"

func main() {
	listen := flag.String("listen", ":8080", "HTTP listen address")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	server := &http.Server{
		Addr:              *listen,
		Handler:           newMux(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("acr-api version=%s listening=%s mode=contract-bootstrap", version, *listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Println(err)
		os.Exit(1)
	}
}

func newMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "mode": "contract-bootstrap"})
	})
	mux.HandleFunc("GET /api/v1/agent-context/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		// Bootstrap-only shape. Production middleware replaces the false
		// entitlement/permission values with authenticated organization state.
		writeJSON(w, http.StatusOK, contractsv1.Capabilities{
			SchemaVersion:         contractsv1.CapabilitiesSchema,
			Service:               "dev-health-acr",
			ServiceVersion:        version,
			MinimumSidecarVersion: "0.1.0",
			SupportedSchemaVersions: []string{
				contractsv1.ContextPacketRequestSchema,
				contractsv1.ContextPacketSchema,
				contractsv1.ContextPacketItemSchema,
				contractsv1.EvidenceRefSchema,
				contractsv1.ExpandedEvidenceSchema,
				contractsv1.CapabilitiesSchema,
				contractsv1.AgentEpisodeCreateSchema,
				contractsv1.AgentEpisodeSchema,
				contractsv1.ErrorSchema,
			},
			EnabledTools: []string{},
			Entitlements: contractsv1.CapabilityEntitlements{
				AgentContextRuntime: false,
			},
			Permissions: contractsv1.CapabilityPermissions{},
			Limits: contractsv1.CapabilityLimits{
				MaxItems:           30,
				MaxOutputTokens:    4000,
				MaxSerializedBytes: 262144,
				RequestsPerMinute:  60,
			},
			GeneratedAt: time.Now().UTC(),
		})
	})
	return requestIDMiddleware(mux)
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID != "" {
			w.Header().Set("X-Request-ID", requestID)
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("encode response: %v", err)
	}
}
