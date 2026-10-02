//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/experiments/intent-model-training/internal/interpreq"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Loopback harness: a fake OpenAI-compatible server, fixture seal, session,
// capture input, profile and approval. No network, no sealed data.

const testKey = "sk-test-LOOPBACK-0123456789abcdefXYZ"

const okAnswer = `{"shape":"open","requested_judgment":"recorder","time_context":{"axis":"current"},"fact_requirements":[],"clarification_needed":false}`

// rejectedAnswer parses as JSON but the production validator refuses it.
const rejectedAnswer = `{"shape":"open","requested_judgment":"x","time_context":{"axis":"current"},"fact_requirements":[{"kind":"not_a_real_kind"}],"clarification_needed":false}`

var fixtureRequests = map[string]string{
	"row-a": `{"question":"What is blocking the Kestrel project?","time_context":{"axis":"current"}}`,
	"row-b": `{"question":"Why is the Harbor Lights team slower than last quarter?","conversation":[{"turn_id":"t1","role":"user","content":"How is the Harbor Lights team doing?","created_at":"2026-09-01T10:00:00Z"}],"time_context":{"axis":"current"},"prior_subject_receipts":[{"result_id":"res_0000demo01","receipt_id":"rcpt_0000demo01"}]}`,
}

type scripted struct {
	status  int
	body    string
	delay   time.Duration
	headers map[string]string
}

type fakeServer struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	script   []scripted
	fallback scripted
	bodies   [][]byte
	times    []time.Time
	onFirst  func()
}

func completion(content string) string {
	c, _ := json.Marshal(content)
	return `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-5.6-luna","system_fingerprint":"fp_test","choices":[{"index":0,"message":{"role":"assistant","content":` + string(c) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{t: t, fallback: scripted{status: 200, body: completion(okAnswer)}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		if len(f.bodies) == 0 && f.onFirst != nil {
			f.onFirst()
		}
		f.bodies = append(f.bodies, body)
		f.times = append(f.times, time.Now())
		step := f.fallback
		if len(f.script) > 0 {
			step, f.script = f.script[0], f.script[1:]
		}
		f.mu.Unlock()
		if step.delay > 0 {
			time.Sleep(step.delay)
		}
		for k, v := range step.headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(step.status)
		_, _ = w.Write([]byte(step.body))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeServer) push(steps ...scripted) {
	f.mu.Lock()
	f.script = append(f.script, steps...)
	f.mu.Unlock()
}

func (f *fakeServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func (f *fakeServer) allow() allowlist {
	u, _ := url.Parse(f.server.URL)
	return allowlist{Provider: "openai", BaseURL: f.server.URL + "/v1/", Model: "gpt-5.6-luna", Insecure: true, Scheme: "http", Host: u.Host, Path: "/v1/chat/completions"}
}

func (f *fakeServer) env() map[string]string {
	return map[string]string{
		modelprovider.EnvProvider:             "openai",
		modelprovider.EnvBaseURL:              f.server.URL + "/v1/",
		modelprovider.EnvModel:                "gpt-5.6-luna",
		modelprovider.EnvAPIKey:               testKey,
		modelprovider.EnvAllowInsecureBaseURL: "true",
	}
}

func lookupOf(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func experimentDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..")
}

func goldenPath() string {
	return filepath.Join(experimentDir(), "gocapture", "testdata", "expected_envelope.golden.json")
}

var systemSHAOnce sync.Once
var systemSHA string

func helperSystemSHA(t *testing.T) string {
	t.Helper()
	systemSHAOnce.Do(func() {
		out, err := exec.Command(builtHelper, "system-message").Output()
		if err != nil {
			return
		}
		var v struct {
			SHA256 string `json:"sha256"`
		}
		if json.Unmarshal(out, &v) == nil {
			systemSHA = v.SHA256
		}
	})
	if systemSHA == "" {
		t.Fatal("the helper built for this test run gave no system-message: a missing measurement fails, it never skips")
	}
	return systemSHA
}

// testPaths mirrors the §5 layout for fixture writing (plain file I/O).
type testPaths struct {
	Root     string
	Openings string
	Seals    string
	Ledger   string
}

func newTestPaths(root string) testPaths {
	return testPaths{
		Root:     root,
		Openings: filepath.Join(root, "review", openingsName),
		Seals:    filepath.Join(root, "review", sealsName),
		Ledger:   filepath.Join(root, "heldout", ledgerName),
	}
}

func (p testPaths) input(set string) string {
	return filepath.Join(p.Root, "heldout", set, inputName)
}

func (p testPaths) runDir(set, runID string) string {
	return filepath.Join(p.Root, "heldout", set, "captures", runID)
}

func readJSONLines(path string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return splitJSONLines(data), nil
}

type fixture struct {
	t          *testing.T
	root       string
	paths      testPaths
	server     *fakeServer
	sessionID  string
	sealDigest string
	profile    string
	env        map[string]string
	profileMap map[string]any
	membership []membershipRow
}

func writePrivate(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(path, 0o600)
}

func jsonLines(t *testing.T, values ...any) []byte {
	var buf bytes.Buffer
	for _, v := range values {
		line, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func defaultProfile(allow allowlist, pin string) map[string]any {
	return map[string]any{
		"schema": profileSchema, "deployment_id": "fixture", "deployed_source_revision": pin, "image_digest": "sha256:fixture",
		"config_snapshot_at": "2026-09-30T00:00:00Z", "source_revision_relation": "equal_to_pin", "relation_evidence": "",
		"acr_request_timeout": "30s", "model_timeout": "5s", "model_max_attempts": 1, "model_max_transport_retries": 0,
		"synthesis_max_resynthesis_attempts": 3, "provider": allow.Provider, "base_url_resolved": allow.BaseURL, "model": allow.Model,
		"fallback_model": "", "allow_insecure_base_url": allow.Insecure, "phrasing_model": "",
		"org_model_resolution":        map[string]any{"enabled": false, "capture_org_override": false},
		"credential_source_confirmed": true,
		"sources": map[string]string{"acr_request_timeout": "fixture", "model_timeout": "fixture", "model_max_attempts": "fixture",
			"model_max_transport_retries": "fixture", "synthesis_max_resynthesis_attempts": "fixture", "provider": "fixture",
			"base_url_resolved": "fixture", "model": "fixture"},
	}
}

const testPin = "c201cb307fb945019a695d1959078e00c770e699"

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	for _, d := range []string{dataRoot, filepath.Join(dataRoot, "review"), filepath.Join(dataRoot, "heldout"), filepath.Join(dataRoot, "heldout", "H1")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(d, 0o700)
	}
	f := &fixture{t: t, root: root, paths: newTestPaths(dataRoot), server: newFakeServer(t), sessionID: "sess-1", sealDigest: strings.Repeat("ab", 32)}
	f.env = f.server.env()
	sysSHA := helperSystemSHA(t)
	ids := make([]string, 0, len(fixtureRequests))
	for id := range fixtureRequests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var inputRows []any
	for _, id := range ids {
		rendered, err := interpreq.Render([]byte(fixtureRequests[id]))
		if err != nil {
			t.Fatal(err)
		}
		f.membership = append(f.membership, membershipRow{ExampleID: "heldout:" + id, InputSHA256: rendered.InputSHA256, RequestSHA256: rendered.RequestSHA256, RowID: id})
		inputRows = append(inputRows, map[string]any{"kind": "row", "row_id": id, "example_id": "heldout:" + id, "request": json.RawMessage(fixtureRequests[id]),
			"request_sha256": rendered.RequestSHA256, "input_sha256": rendered.InputSHA256})
	}
	md, err := membershipDigest(f.membership)
	if err != nil {
		t.Fatal(err)
	}
	manifest := strings.Repeat("cd", 32)
	writePrivate(t, f.paths.Seals, jsonLines(t, map[string]any{"kind": "seal", "schema": "intent-training.seal.v1", "set": "H1", "set_version": 1,
		"seal_digest": f.sealDigest, "membership_digest": md, "membership": f.membership, "expected_system_message_sha256": sysSHA,
		"evaluation_manifest_sha256": manifest, "sealed_at": "2026-09-30T00:00:00Z"}))
	writePrivate(t, f.paths.Openings, jsonLines(t, map[string]any{"kind": "evaluation_session", "session_id": f.sessionID, "set": "H1",
		"seal_digest": f.sealDigest, "membership_digest": md, "evaluation_manifest_sha256": manifest, "phase": "H1-decision",
		"authorized_by": "human:chris", "allowed_readers": []string{"capture-input", "capture", "predict", "evaluate"}, "opened_at": "2026-09-30T00:00:00Z"}))
	header := map[string]any{"kind": "header", "schema": "intent-training.capture-input.v1", "set": "H1", "seal_digest": f.sealDigest,
		"membership_digest": md, "session_id": f.sessionID, "expected_system_message_sha256": sysSHA, "row_count": len(ids)}
	writePrivate(t, f.paths.input("H1"), jsonLines(t, append([]any{header}, inputRows...)...))
	f.profileMap = defaultProfile(f.server.allow(), testPin)
	f.writeProfile()
	if err := approve(f.paths.Root, "appr-test", 720, "fixture approval", "human:chris", ""); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) writeProfile() {
	f.profile = filepath.Join(f.root, "profile.json")
	data, _ := json.Marshal(f.profileMap)
	writePrivate(f.t, f.profile, data)
}

func (f *fixture) config(runID string) runConfig {
	allow := f.server.allow()
	return runConfig{
		DataRoot: f.paths.Root, SessionID: f.sessionID, ApprovalID: "appr-test", RunID: runID, RunCap: 720,
		SourcePin: testPin, ProfilePath: f.profile, GoldenPath: goldenPath(), MinInterval: 0,
		allow: &allow, lookup: lookupOf(f.env), build: &buildIdentity{BinarySHA256: "test-binary", ManifestSHA256: "test-manifest"},
		next: &http.Transport{},
	}
}

func (f *fixture) run(cfg runConfig) (runResult, error) {
	return capture(context.Background(), cfg)
}

func (f *fixture) ledger() *approvalLedger {
	l, err := f.tryLedger()
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(l.close)
	return l
}

func (f *fixture) tryLedger() (*approvalLedger, error) {
	root, err := openRoot(f.paths.Root)
	if err != nil {
		return nil, err
	}
	defer root.close()
	dir, err := root.walk(false, "heldout")
	if err != nil {
		return nil, err
	}
	defer dir.close()
	return openLedger(dir, "appr-test", false)
}

func (f *fixture) runDir(runID string) string { return f.paths.runDir("H1", runID) }

func (f *fixture) artifacts(runID string) map[string]artifact {
	dir := filepath.Join(f.runDir(runID), "raw")
	entries, _ := os.ReadDir(dir)
	out := map[string]artifact{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			f.t.Fatal(err)
		}
		a, err := verifyArtifactBytes(data)
		if err != nil {
			f.t.Fatalf("artifact %s: %v", e.Name(), err)
		}
		out[e.Name()] = a
	}
	return out
}

func (f *fixture) responses(runID string) []responseRow {
	data, err := os.ReadFile(filepath.Join(f.runDir(runID), "responses.jsonl"))
	if err != nil {
		f.t.Fatal(err)
	}
	var rows []responseRow
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var r responseRow
		if err := json.Unmarshal(line, &r); err != nil {
			f.t.Fatal(err)
		}
		rows = append(rows, r)
	}
	return rows
}

// recordEnvelope runs the PRODUCTION runtime over loopback once with a
// plain recording transport and returns the request body it sent.
func recordEnvelope(t *testing.T) []byte {
	t.Helper()
	server := newFakeServer(t)
	rec := &recorder{next: &http.Transport{}}
	restore := installDefaultTransport(rec)
	defer restore()
	cfg, err := modelprovider.ConfigFromEnv(lookupOf(server.env()))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxAttempts, cfg.MaxTransportRetries, cfg.Timeout, cfg.MaxSynthesisResynthesisAttempts = 1, 0, 5*time.Second, 1
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	rt, err := modelprovider.NewGenkitRuntime(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := interpreq.Render([]byte(fixtureRequests["row-b"]))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := rt.InterpretQuestion(context.Background(), storage.Principal{OrgID: "golden", Subject: "golden"}, rendered.Decoded.Request); err != nil {
		t.Fatalf("production interpret over loopback: %v", err)
	}
	if len(rec.bodies) != 1 {
		t.Fatalf("want one request, got %d", len(rec.bodies))
	}
	return rec.bodies[0]
}

type recorder struct {
	next   http.RoundTripper
	bodies [][]byte
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readAndRestoreRequest(req)
	if err != nil {
		return nil, err
	}
	r.bodies = append(r.bodies, body)
	return r.next.RoundTrip(req)
}

func goldenFromBody(t *testing.T, body []byte) []byte {
	t.Helper()
	d, err := observedDescriptor(body)
	if err != nil {
		t.Fatal(err)
	}
	g := envelopeGolden{Schema: goldenSchema, Top: map[string]json.RawMessage{}}
	for k, v := range d.Top {
		if k != "model" {
			g.Top[k] = v
		}
	}
	for _, m := range d.Messages {
		g.Messages = append(g.Messages, messageShape{ContentKind: m.ContentKind, Keys: m.Keys, Parts: m.Parts, Role: m.Role})
	}
	out, err := json.MarshalIndent(g, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func mustEqual(t *testing.T, name string, got, want any) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func zeroInterpreted() contextfabric.InterpretedQuestion { return contextfabric.InterpretedQuestion{} }
func zeroReceipt() contextfabric.ModelExecutionReceipt   { return contextfabric.ModelExecutionReceipt{} }
