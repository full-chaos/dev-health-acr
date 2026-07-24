// Command modeloracle is the deterministic OpenAI-compatible model used by the Context
// Fabric full-stack acceptance gate (CHAOS-3065).
//
// It implements only the surface the pinned OpenCode version needs and returns a fixed turn
// sequence: request context_for_task, inspect the live response, request source_evidence for
// a reference that response actually returned, then emit the strict agent result.
//
// It is NOT a mock ACR or a mock MCP server. Every packet and evidence value it reports is
// read back from the live stack, so the gate still fails when the real read path breaks. Its
// only job is to remove probabilistic language generation from a pass/fail decision.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const modelID = "context-fabric-oracle"

func main() {
	if err := run(); err != nil {
		log.Fatalf("modeloracle: %v", err)
	}
}

func run() error {
	var (
		planPath     = flag.String("plan", "", "path to the fullstack_model_plan.v1 document")
		host         = flag.String("host", "127.0.0.1", "loopback address to bind")
		port         = flag.Int("port", 0, "port to bind; 0 selects a free port")
		portFile     = flag.String("port-file", "", "file to write the bound port to")
		logDir       = flag.String("log-dir", "", "directory for request/observation artifacts")
		readyTimeout = flag.Duration("idle-timeout", 10*time.Minute, "shut down after this much inactivity")
	)
	flag.Parse()

	if *planPath == "" || *logDir == "" {
		return errors.New("--plan and --log-dir are required")
	}
	plan, err := loadPlan(*planPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*logDir, 0o700); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	srv := &server{plan: plan, logDir: *logDir, activity: make(chan struct{}, 1)}

	listener, err := net.Listen("tcp", net.JoinHostPort(*host, fmt.Sprint(*port)))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	bound := listener.Addr().(*net.TCPAddr).Port
	if *portFile != "" {
		if err := os.WriteFile(*portFile, []byte(fmt.Sprint(bound)), 0o600); err != nil {
			return fmt.Errorf("write port file: %w", err)
		}
	}
	log.Printf("modeloracle listening on %s:%d for task %s", *host, bound, plan.TaskID)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", srv.handleModels)
	mux.HandleFunc("/v1/chat/completions", srv.handleChatCompletions)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)

	errc := make(chan error, 1)
	go func() { errc <- httpServer.Serve(listener) }()

	idle := time.NewTimer(*readyTimeout)
	defer idle.Stop()
	for {
		select {
		case <-srv.activity:
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(*readyTimeout)
		case <-shutdown:
			return srv.finish(httpServer)
		case <-idle.C:
			log.Printf("modeloracle: idle timeout reached")
			return srv.finish(httpServer)
		case err := <-errc:
			if errors.Is(err, http.ErrServerClosed) {
				return srv.writeObservations()
			}
			return err
		}
	}
}

type server struct {
	plan     Plan
	logDir   string
	activity chan struct{}

	mu        sync.Mutex
	requests  int
	observed  Observation
	toolNames map[string]string
	finalSent bool
}

func (s *server) finish(httpServer *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	return s.writeObservations()
}

// writeObservations records exactly what the scripted model saw, so the assertion tool can
// prove the final answer transcribed the live stack instead of the plan.
func (s *server) writeObservations() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload := map[string]any{
		"schema_version":  "fullstack_model_observations.v1",
		"task_id":         s.plan.TaskID,
		"requests":        s.requests,
		"final_sent":      s.finalSent,
		"tool_names":      s.toolNames,
		"observed":        s.observed,
		"injected_fault":  string(s.plan.Fault),
		"model_id":        modelID,
		"invented_ref_id": inventedEvidenceRefIDIfUsed(s.plan.Fault),
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.logDir, "model-observations.json"), append(encoded, '\n'), 0o600)
}

func inventedEvidenceRefIDIfUsed(fault Fault) string {
	if fault == FaultInventEvidence {
		return inventedEvidenceRefID
	}
	return ""
}

func (s *server) touch() {
	select {
	case s.activity <- struct{}{}:
	default:
	}
}

func (s *server) handleModels(w http.ResponseWriter, _ *http.Request) {
	s.touch()
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data": []map[string]any{{
			"id":       modelID,
			"object":   "model",
			"created":  0,
			"owned_by": "context-fabric-acceptance",
		}},
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
