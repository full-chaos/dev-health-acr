package mcp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
)

// TestHostedEndToEndLive drives a deployed hosted acr-mcp endpoint the way an
// external agent does: the go-sdk client over Streamable HTTP, one bearer per
// caller, protocol 2026-07-28. It shares the auth matrix's endpoint and bearer
// variables (docs/mcp-auth-matrix.md) and adds its own:
//
//	ACR_MCP_MATRIX_URL, ACR_MCP_MATRIX_BEARER_A, ACR_MCP_MATRIX_BEARER_C,
//	ACR_MCP_MATRIX_REPO_A                   required (as for the matrix)
//	ACR_MCP_MATRIX_E2E_REPO_C               a repository in C's grant (required); C's grant must not hold REPO_A
//	ACR_MCP_MATRIX_E2E_GRANT_A              comma-separated repositories in A's grant (default: REPO_A)
//	ACR_MCP_MATRIX_E2E_QUESTION             a question answerable on A's data (required)
//	ACR_MCP_MATRIX_E2E_CLARIFY_QUESTION     a question expected to need clarification (optional)
//	ACR_MCP_MATRIX_E2E_TOOLS_A, _TOOLS_C, _TOOLS_H   comma-separated expected catalogues
//	                                        (default: the four answer tools)
//	ACR_MCP_MATRIX_BEARER_H                 an optional third caller for the catalogue step
//	ACR_MCP_MATRIX_E2E_SERVER_REVISION      text the discovered serverInfo.version must carry
//	ACR_MCP_MATRIX_E2E_CONCURRENCY          requests per caller in the concurrency step (default 24)
//	ACR_MCP_MATRIX_E2E_BUDGET_BYTES         max_serialized_bytes of the include_full_result call (default 8192)
//	ACR_MCP_MATRIX_E2E_EXPECT_OMITTED=1     that budget must drop the full result
//	ACR_MCP_MATRIX_RESULT_A                 a stored result id owned by A, also read back (optional)
//	ACR_MCP_MATRIX_E2E_OUT                  directory for the captured transcripts (no bearer is written)
//
// Unset, it skips loudly and never passes; with ACR_MCP_MATRIX_REQUIRE_LIVE=1
// an unset endpoint fails.
func TestHostedEndToEndLive(t *testing.T) {
	env := acrmcp.MatrixEnvironmentForTest
	cfg, missing := hostedE2EConfigFromEnv(env)
	switch {
	case cfg == nil && env("ACR_MCP_MATRIX_REQUIRE_LIVE") == "1":
		t.Fatal("ACR_MCP_MATRIX_REQUIRE_LIVE=1 but ACR_MCP_MATRIX_URL is not set: the hosted end-to-end proof did not run")
	case cfg == nil:
		t.Skip("HOSTED END-TO-END NOT EXECUTED: ACR_MCP_MATRIX_URL is not set; nothing was proven against a deployment")
	case len(missing) > 0:
		t.Fatalf("ACR_MCP_MATRIX_URL is set but %s are not: the hosted end-to-end proof cannot run", strings.Join(missing, ", "))
	}
	runHostedE2E(t, cfg)
}

var hostedAnswerTools = []string{"context_for_task", "investigate_question", "investigate_with_interpretation", "investigation_result", "source_evidence"}

type hostedE2EConfig struct {
	url             string
	bearers         map[string]string
	tools           map[string][]string
	repoA, repoC    string
	grantA          []string
	question        string
	clarifyQuestion string
	serverRevision  string
	concurrency     int
	expectOmitted   bool
	budgetBytes     int
	storedResult    string
	// investigatedNotStored is set only by the in-process runner, whose
	// stand-in investigator answers without storing its results.
	investigatedNotStored bool
	outDir                string
	runID                 string
}

func hostedE2EConfigFromEnv(getenv func(string) string) (*hostedE2EConfig, []string) {
	endpointURL := getenv("ACR_MCP_MATRIX_URL")
	if endpointURL == "" {
		return nil, nil
	}
	var missing []string
	need := func(name string) string {
		v := getenv(name)
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}
	cfg := &hostedE2EConfig{
		url: endpointURL,
		bearers: map[string]string{
			"A": need("ACR_MCP_MATRIX_BEARER_A"),
			"C": need("ACR_MCP_MATRIX_BEARER_C"),
			"H": getenv("ACR_MCP_MATRIX_BEARER_H"),
		},
		tools:           map[string][]string{},
		repoA:           need("ACR_MCP_MATRIX_REPO_A"),
		repoC:           need("ACR_MCP_MATRIX_E2E_REPO_C"),
		question:        need("ACR_MCP_MATRIX_E2E_QUESTION"),
		clarifyQuestion: getenv("ACR_MCP_MATRIX_E2E_CLARIFY_QUESTION"),
		serverRevision:  getenv("ACR_MCP_MATRIX_E2E_SERVER_REVISION"),
		concurrency:     24,
		expectOmitted:   getenv("ACR_MCP_MATRIX_E2E_EXPECT_OMITTED") == "1",
		budgetBytes:     8192,
		storedResult:    getenv("ACR_MCP_MATRIX_RESULT_A"),
		outDir:          getenv("ACR_MCP_MATRIX_E2E_OUT"),
		runID:           randomToken(4),
	}
	for _, caller := range []string{"A", "C", "H"} {
		cfg.tools[caller] = hostedAnswerTools
		if list := getenv("ACR_MCP_MATRIX_E2E_TOOLS_" + caller); list != "" {
			cfg.tools[caller] = nil
			for _, name := range strings.Split(list, ",") {
				if name = strings.TrimSpace(name); name != "" {
					cfg.tools[caller] = append(cfg.tools[caller], name)
				}
			}
			slices.Sort(cfg.tools[caller])
		}
	}
	cfg.grantA = []string{cfg.repoA}
	if list := getenv("ACR_MCP_MATRIX_E2E_GRANT_A"); list != "" {
		cfg.grantA = nil
		for _, repo := range strings.Split(list, ",") {
			if repo = strings.TrimSpace(repo); repo != "" {
				cfg.grantA = append(cfg.grantA, repo)
			}
		}
	}
	if !slices.Contains(cfg.grantA, cfg.repoA) || cfg.repoA == cfg.repoC {
		missing = append(missing, "ACR_MCP_MATRIX_E2E_GRANT_A (must contain ACR_MCP_MATRIX_REPO_A, which must differ from ACR_MCP_MATRIX_E2E_REPO_C)")
	}
	if n := getenv("ACR_MCP_MATRIX_E2E_BUDGET_BYTES"); n != "" {
		if _, err := fmt.Sscanf(n, "%d", &cfg.budgetBytes); err != nil || cfg.budgetBytes < 8192 || cfg.budgetBytes > 1048576 {
			missing = append(missing, "ACR_MCP_MATRIX_E2E_BUDGET_BYTES (an integer from 8192 to 1048576)")
		}
	}
	if n := getenv("ACR_MCP_MATRIX_E2E_CONCURRENCY"); n != "" {
		var parsed int
		if _, err := fmt.Sscanf(n, "%d", &parsed); err != nil || parsed < 1 {
			missing = append(missing, "ACR_MCP_MATRIX_E2E_CONCURRENCY (a positive integer)")
		}
		cfg.concurrency = parsed
	}
	return cfg, missing
}

func randomToken(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// exchange is one HTTP request/response pair as the client saw it. The
// bearer is never recorded.
type exchange struct {
	Method          string          `json:"method"`
	RequestID       string          `json:"request_id"`
	EchoedRequestID string          `json:"echoed_request_id"`
	Status          int             `json:"status"`
	ProtocolHeader  string          `json:"protocol_header"`
	Request         json.RawMessage `json:"request,omitempty"`
	Response        string          `json:"response"`
}

// recordingTransport adds the caller's bearer and a traceable correlation id
// to every request and records every exchange.
type recordingTransport struct {
	bearer string
	prefix string
	mu     sync.Mutex
	seq    int
	log    []exchange
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	r.mu.Lock()
	r.seq++
	id := fmt.Sprintf("%s-%03d", r.prefix, r.seq)
	r.mu.Unlock()
	if r.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+r.bearer)
	}
	req.Header.Set("X-Request-ID", id)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	var envelope struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &envelope)
	x := exchange{
		Method: envelope.Method, RequestID: id, EchoedRequestID: resp.Header.Get("X-Request-ID"),
		Status: resp.StatusCode, ProtocolHeader: req.Header.Get("Mcp-Protocol-Version"), Response: string(raw),
	}
	if json.Valid(body) {
		x.Request = body
	}
	r.mu.Lock()
	r.log = append(r.log, x)
	r.mu.Unlock()
	return resp, nil
}

func (r *recordingTransport) exchanges() []exchange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.log)
}

type hostedCaller struct {
	name      string
	session   *mcpsdk.ClientSession
	transport *recordingTransport
}

func (cfg *hostedE2EConfig) connect(t *testing.T, caller, step string) *hostedCaller {
	t.Helper()
	rt := &recordingTransport{bearer: cfg.bearers[caller], prefix: fmt.Sprintf("e2e-%s-%s-%s", cfg.runID, step, caller)}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "acr-hosted-e2e", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var session *mcpsdk.ClientSession
	err := retryTooMany(t, "caller "+caller+" connect", func() (err error) {
		session, err = client.Connect(ctx, &mcpsdk.StreamableClientTransport{
			Endpoint: cfg.url, HTTPClient: &http.Client{Transport: rt, Timeout: 5 * time.Minute},
			DisableStandaloneSSE: true, MaxRetries: -1,
		}, &mcpsdk.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
		return err
	})
	if err != nil {
		t.Fatalf("caller %s: connect: %v", caller, err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &hostedCaller{name: caller, session: session, transport: rt}
}

func (cfg *hostedE2EConfig) record(t *testing.T, name string, v any) {
	t.Helper()
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	for caller, bearer := range cfg.bearers {
		if bearer != "" && bytes.Contains(encoded, []byte(bearer)) {
			t.Fatalf("transcript %s carries caller %s's bearer; refusing to write it", name, caller)
		}
	}
	if cfg.outDir == "" {
		return
	}
	if err := os.MkdirAll(cfg.outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.outDir, name+".json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (c *hostedCaller) call(t *testing.T, tool string, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for attempt := 1; ; attempt++ {
		var res *mcpsdk.CallToolResult
		err := retryTooMany(t, "caller "+c.name+" "+tool, func() (err error) {
			res, err = c.session.CallTool(ctx, &mcpsdk.CallToolParams{Name: tool, Arguments: args})
			return err
		})
		if err != nil {
			t.Fatalf("caller %s: %s: %v", c.name, tool, err)
		}
		if !rateLimited(res) || attempt == rateLimitAttempts {
			return res
		}
		t.Logf("E2E-RETRY caller %s %s: rate_limited (attempt %d), waiting %s", c.name, tool, attempt, rateLimitBackoff)
		select {
		case <-ctx.Done():
			return res
		case <-time.After(rateLimitBackoff):
		}
	}
}

// A rate-limited answer decides nothing about identity or scope, so it is
// retried; every retry is logged. The deployment's own per-credential and
// per-organization request budget is not relaxed for the proof.
const (
	rateLimitAttempts = 10
	rateLimitBackoff  = 40 * time.Second
)

// retryTooMany retries fn while the endpoint answers HTTP 429, logging every
// retry.
func retryTooMany(t *testing.T, label string, fn func() error) error {
	t.Helper()
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil || !strings.Contains(err.Error(), "Too Many Requests") || attempt == 2*rateLimitAttempts {
			return err
		}
		t.Logf("E2E-RETRY %s: HTTP 429 (attempt %d), waiting %s", label, attempt, rateLimitBackoff)
		time.Sleep(rateLimitBackoff)
	}
}

func rateLimited(res *mcpsdk.CallToolResult) bool {
	return res != nil && res.IsError && strings.Contains(resultText(res), "rate_limited")
}

// investigate asks one question and answers every clarification the service
// asks for, up to three follow-ups. Each follow-up carries, for every kind of
// choice the latest answer offered, the receipt of its first option, all
// bound to that latest answer: a receipt names an offer of the answer it came
// from, and a newer answer supersedes it. It returns every turn, oldest first.
func (c *hostedCaller) investigate(t *testing.T, args map[string]any) []*mcpsdk.CallToolResult {
	t.Helper()
	var turns []*mcpsdk.CallToolResult
	turn := maps.Clone(args)
	for range 4 {
		res := c.call(t, "investigate_question", turn)
		turns = append(turns, res)
		if res.IsError {
			return turns
		}
		answer, _ := structured(t, res)["structured"].(map[string]any)
		if answer["status"] != "clarification_required" {
			return turns
		}
		id, _ := answer["result_id"].(string)
		receipts := firstReceipts(answer)
		if len(receipts) == 0 {
			return turns
		}
		t.Logf("E2E-EVIDENCE clarification turn %d: result_id=%s missing=%v -> %v", len(turns), id, field(answer, "structure_needs", "missing"), receipts)
		turn = maps.Clone(args)
		turn["parent_result_id"] = id
		for reqField, receipt := range receipts {
			turn[reqField] = []any{map[string]any{"result_id": id, "receipt_id": receipt}}
		}
	}
	return turns
}

func resultText(res *mcpsdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// structured decodes a tool result's structured content into a generic map.
func structured(t *testing.T, res *mcpsdk.CallToolResult) map[string]any {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		t.Fatalf("structured content is not an object: %s", truncate(string(raw), 400))
	}
	return m
}

func field(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[p]
	}
	return cur
}

func stringsAt(m map[string]any, path ...string) []string {
	items, _ := field(m, path...).([]any)
	var out []string
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func runHostedE2E(t *testing.T, cfg *hostedE2EConfig) {
	t.Logf("E2E run id %s against %s", cfg.runID, cfg.url)
	var resultID, evidenceRef, investigationRef string

	t.Run("01_discover_2026_07_28", func(t *testing.T) {
		a := cfg.connect(t, "A", "discover")
		init := a.session.InitializeResult()
		if init == nil || init.ProtocolVersion != "2026-07-28" {
			t.Fatalf("negotiated %+v, want protocol 2026-07-28", init)
		}
		log := a.transport.exchanges()
		// Only the attempt that connected counts: an attempt the endpoint
		// refused with HTTP 429 is retried from the start.
		for i := len(log) - 1; i >= 0; i-- {
			if log[i].Status == http.StatusTooManyRequests {
				log = log[i+1:]
				break
			}
		}
		if len(log) == 0 || log[0].Method != "server/discover" {
			t.Fatalf("first request was not server/discover: %+v", log)
		}
		for _, x := range log {
			if x.Method == "initialize" {
				t.Fatalf("client fell back to the legacy initialize handshake: %s", truncate(x.Response, 400))
			}
		}
		if log[0].Status != http.StatusOK {
			t.Fatalf("server/discover HTTP %d", log[0].Status)
		}
		if cfg.serverRevision != "" && (init.ServerInfo == nil || !strings.Contains(init.ServerInfo.Version, cfg.serverRevision)) {
			t.Fatalf("serverInfo %+v does not carry revision %q", init.ServerInfo, cfg.serverRevision)
		}
		t.Logf("E2E-EVIDENCE discover: protocol=%s server=%s/%s request_id=%s echoed=%s",
			init.ProtocolVersion, init.ServerInfo.Name, init.ServerInfo.Version, log[0].RequestID, log[0].EchoedRequestID)
		cfg.record(t, "01_discover", log[0])
	})

	t.Run("02_catalogue_per_credential", func(t *testing.T) {
		catalogues := map[string][]string{}
		for _, caller := range []string{"A", "C", "H"} {
			if cfg.bearers[caller] == "" {
				t.Logf("E2E-EVIDENCE catalogue %s: NOT EXECUTED (no bearer configured)", caller)
				continue
			}
			c := cfg.connect(t, caller, "catalogue")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			var tools *mcpsdk.ListToolsResult
			err := retryTooMany(t, caller+" tools/list", func() (err error) {
				tools, err = c.session.ListTools(ctx, nil)
				return err
			})
			cancel()
			if err != nil {
				t.Fatalf("%s tools/list: %v", caller, err)
			}
			var names []string
			for _, tool := range tools.Tools {
				names = append(names, tool.Name)
			}
			slices.Sort(names)
			catalogues[caller] = names
			want := slices.Clone(cfg.tools[caller])
			slices.Sort(want)
			if !slices.Equal(names, want) {
				t.Errorf("%s tools/list = %v, want %v", caller, names, want)
			}
			t.Logf("E2E-EVIDENCE catalogue %s: tools=%v", caller, names)
		}
		cfg.record(t, "02_catalogues", catalogues)

		a := cfg.connect(t, "A", "guide")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		var resources *mcpsdk.ListResourcesResult
		err := retryTooMany(t, "resources/list", func() (err error) {
			resources, err = a.session.ListResources(ctx, nil)
			return err
		})
		if err != nil {
			t.Fatalf("resources/list: %v", err)
		}
		var uris []string
		for _, r := range resources.Resources {
			uris = append(uris, r.URI)
		}
		guides := 0
		for _, u := range uris {
			if strings.HasPrefix(u, "acr://guide/") {
				guides++
			}
		}
		if guides == 0 {
			t.Errorf("resources/list carries no acr://guide/* resource: %v", uris)
		}
		var prompts *mcpsdk.ListPromptsResult
		err = retryTooMany(t, "prompts/list", func() (err error) {
			prompts, err = a.session.ListPrompts(ctx, nil)
			return err
		})
		if err != nil {
			t.Fatalf("prompts/list: %v", err)
		}
		var promptNames []string
		for _, p := range prompts.Prompts {
			promptNames = append(promptNames, p.Name)
		}
		for _, want := range []string{"investigate", "continue_investigation", "expand_evidence", "interpret_question", "synthesize_answer"} {
			if !slices.Contains(promptNames, want) {
				t.Errorf("prompts/list lacks %q: %v", want, promptNames)
			}
		}
		t.Logf("E2E-EVIDENCE guide A: resources=%v prompts=%v", uris, promptNames)
		cfg.record(t, "02_guide", map[string]any{"resources": uris, "prompts": promptNames})
	})

	t.Run("03_context_for_task", func(t *testing.T) {
		a := cfg.connect(t, "A", "context")
		res := a.call(t, "context_for_task", map[string]any{
			"goal":       "Understand the current state of recent work in this repository before changing it.",
			"repository": map[string]any{"slug": cfg.repoA},
		})
		if res.IsError {
			t.Fatalf("context_for_task with repository %s refused: %s", cfg.repoA, truncate(resultText(res), 600))
		}
		m := structured(t, res)
		refs := collectEvidenceRefs(m)
		if len(refs) == 0 {
			t.Errorf("context packet for %s carries no evidence reference", cfg.repoA)
		} else if evidenceRef == "" {
			evidenceRef = refs[0]
		}
		t.Logf("E2E-EVIDENCE context_for_task %s: ok, keys=%v evidence_refs=%d", cfg.repoA, keysOf(m), len(refs))
		cfg.record(t, "03_context_ok", res)

		refused := a.call(t, "context_for_task", map[string]any{"goal": "Understand the current state of this repository."})
		text := resultText(refused)
		if !refused.IsError || !strings.Contains(text, "repository.slug") {
			t.Fatalf("context_for_task without repository: isError=%v text=%s, want a typed refusal naming repository.slug", refused.IsError, truncate(text, 600))
		}
		t.Logf("E2E-EVIDENCE context_for_task no repository: isError=true text=%s", truncate(text, 300))
		cfg.record(t, "03_context_refused", refused)
	})

	t.Run("04_investigate_question", func(t *testing.T) {
		a := cfg.connect(t, "A", "investigate")
		turns := a.investigate(t, map[string]any{"question": cfg.question})
		for i, turn := range turns {
			cfg.record(t, fmt.Sprintf("04_investigate_turn%d", i+1), turn)
		}
		res := turns[len(turns)-1]
		if res.IsError {
			t.Fatalf("investigate_question refused: %s", truncate(resultText(res), 800))
		}
		m := structured(t, res)
		answer, _ := m["structured"].(map[string]any)
		if answer == nil {
			t.Fatalf("no structured answer: keys=%v", keysOf(m))
		}
		for _, key := range []string{"result_id", "status", "completeness", "coverage_summary", "coverage_partial", "projection_budget", "evidence_ref_ids"} {
			if _, ok := answer[key]; !ok {
				t.Errorf("answer lacks contract field %q", key)
			}
		}
		if answer["status"] == "clarification_required" {
			t.Errorf("still clarification_required after %d turns", len(turns))
		}
		if field(m, "untrusted_content", "untrusted") != true {
			t.Errorf("untrusted_content.untrusted = %v, want true", field(m, "untrusted_content", "untrusted"))
		}
		resultID, _ = answer["result_id"].(string)
		if resultID == "" {
			t.Fatal("answer carries no result_id")
		}
		refs := stringsAt(answer, "evidence_ref_ids")
		if len(refs) > 0 {
			investigationRef = refs[0]
		}
		t.Logf("E2E-EVIDENCE investigate_question: turns=%d result_id=%s status=%v completeness=%v coverage_partial=%v evidence_refs=%d warnings=%d limitations=%d",
			len(turns), resultID, answer["status"], field(answer, "completeness", "state"), answer["coverage_partial"], len(refs),
			lenOf(answer["warnings"]), lenOf(answer["limitations"]))

		if cfg.clarifyQuestion == "" {
			if len(turns) == 1 {
				t.Log("E2E-EVIDENCE clarification path: NOT EXECUTED (the question needed no clarification and ACR_MCP_MATRIX_E2E_CLARIFY_QUESTION is unset)")
			}
			return
		}
		clarify := a.investigate(t, map[string]any{"question": cfg.clarifyQuestion, "allow_clarification": true})
		for i, turn := range clarify {
			cfg.record(t, fmt.Sprintf("04_clarify_turn%d", i+1), turn)
		}
		first, _ := structured(t, clarify[0])["structured"].(map[string]any)
		if first["status"] != "clarification_required" || len(clarify) < 2 {
			t.Fatalf("clarification question: first status %v over %d turns, want clarification_required then a follow-up", first["status"], len(clarify))
		}
		last := clarify[len(clarify)-1]
		if last.IsError {
			t.Fatalf("clarification follow-up refused: %s", truncate(resultText(last), 800))
		}
		lm, _ := structured(t, last)["structured"].(map[string]any)
		t.Logf("E2E-EVIDENCE clarification question: %d turns, final status=%v result_id=%v", len(clarify), lm["status"], lm["result_id"])
	})

	t.Run("05_investigation_result_by_id", func(t *testing.T) {
		a := cfg.connect(t, "A", "result")
		smallTurns := a.investigate(t, map[string]any{
			"question":            cfg.question,
			"include_full_result": true,
			"budget":              map[string]any{"max_serialized_bytes": cfg.budgetBytes},
		})
		small := smallTurns[len(smallTurns)-1]
		if small.IsError {
			t.Fatalf("budgeted investigate_question refused: %s", truncate(resultText(small), 800))
		}
		sm := structured(t, small)
		answer, _ := sm["structured"].(map[string]any)
		omitted := field(answer, "projection_budget", "full_result_omitted") == true
		switch {
		case omitted && sm["full_result"] != nil:
			t.Fatalf("%d-byte budget: full_result_omitted=true but the full result is attached", cfg.budgetBytes)
		case !omitted && sm["full_result"] == nil:
			t.Fatal("include_full_result=true: no full result and no full_result_omitted declaration")
		case !omitted && cfg.expectOmitted:
			t.Fatalf("%d-byte budget: the full result fit, so the omission path was not exercised (ACR_MCP_MATRIX_E2E_EXPECT_OMITTED=1)", cfg.budgetBytes)
		case !omitted:
			t.Logf("E2E-EVIDENCE budget: FULL-RESULT OMISSION NOT EXERCISED: the full result fit within %d bytes", cfg.budgetBytes)
		}
		id, _ := answer["result_id"].(string)
		cfg.record(t, "05_budgeted", small)
		ids := []string{cfg.storedResult}
		if !cfg.investigatedNotStored {
			ids = append(ids, id, resultID)
		}
		read := 0
		for _, rid := range ids {
			if rid == "" {
				continue
			}
			read++
			res := a.call(t, "investigation_result", map[string]any{"result_id": rid})
			if res.IsError {
				t.Fatalf("investigation_result %s refused: %s", rid, truncate(resultText(res), 600))
			}
			m := structured(t, res)
			got, _ := field(m, "structured", "result_id").(string)
			if got != rid {
				t.Fatalf("investigation_result %s returned result_id %q", rid, got)
			}
			t.Logf("E2E-EVIDENCE investigation_result %s: ok status=%v", rid, field(m, "structured", "status"))
			cfg.record(t, "05_result_"+rid, res)
		}
		if read == 0 {
			t.Fatal("no result id to read back")
		}
		t.Logf("E2E-EVIDENCE budget: result_id=%s full_result_omitted=%v truncated=%v", id, omitted, field(answer, "projection_budget", "truncated"))
	})

	expand := func(t *testing.T, name, ref string) {
		t.Helper()
		a := cfg.connect(t, "A", name)
		res := a.call(t, "source_evidence", map[string]any{"evidence_ref_id": ref})
		cfg.record(t, "06_"+name, res)
		if res.IsError {
			t.Fatalf("source_evidence %s refused: %s", ref, truncate(resultText(res), 600))
		}
		m := structured(t, res)
		if field(m, "rendered_markdown", "untrusted") != true {
			t.Errorf("rendered_markdown.untrusted = %v, want true", field(m, "rendered_markdown", "untrusted"))
		}
		t.Logf("E2E-EVIDENCE source_evidence %s: ok untrusted=%v truncated=%v", ref,
			field(m, "rendered_markdown", "untrusted"), field(m, "rendered_markdown", "truncated"))
	}
	t.Run("06_source_evidence_context_ref", func(t *testing.T) {
		if evidenceRef == "" {
			t.Fatal("context_for_task returned no evidence reference; source_evidence cannot be exercised")
		}
		expand(t, "source_evidence_context_ref", evidenceRef)
	})
	// The tool contract names investigate_question's evidence_ref_ids as
	// expandable too, so a refusal here is a failure, not a skip.
	t.Run("06_source_evidence_investigation_ref", func(t *testing.T) {
		if investigationRef == "" {
			t.Log("E2E-EVIDENCE source_evidence investigation ref: NOT EXECUTED (the answer carried no evidence_ref_ids)")
			return
		}
		expand(t, "source_evidence_investigation_ref", investigationRef)
	})
	cfg.record(t, "ids", map[string]string{"result_a": resultID, "evidence_a": evidenceRef, "investigation_ref_a": investigationRef})

	t.Run("08_concurrent_callers_no_identity_bleed", func(t *testing.T) {
		callers := map[string]*hostedCaller{"A": cfg.connect(t, "A", "conc"), "C": cfg.connect(t, "C", "conc")}
		repos := map[string][2]string{"A": {cfg.repoA, cfg.repoC}, "C": {cfg.repoC, cfg.repoA}}
		grants := map[string][]string{"A": cfg.grantA, "C": {cfg.repoC}}
		type outcome struct {
			caller, repo string
			allowed      bool
			retries      int
			text         string
		}
		var mu sync.Mutex
		var outcomes []outcome
		var wg sync.WaitGroup
		start := make(chan struct{})
		// Four in flight per caller: the two callers' requests interleave on
		// the one workload while staying near the deployment's request budget.
		sem := map[string]chan struct{}{"A": make(chan struct{}, 4), "C": make(chan struct{}, 4)}
		for name, c := range callers {
			for i := range cfg.concurrency {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					repo := repos[name][i%2]
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
					defer cancel()
					sem[name] <- struct{}{}
					defer func() { <-sem[name] }()
					o := outcome{caller: name, repo: repo}
					for attempt := 1; attempt <= 2*rateLimitAttempts; attempt++ {
						res, err := c.session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "context_for_task", Arguments: map[string]any{
							"goal": "Summarize recent work.", "repository": map[string]any{"slug": repo},
						}})
						if err != nil {
							o.allowed, o.text = false, "transport: "+err.Error()
							if !strings.Contains(err.Error(), "Too Many Requests") {
								break
							}
						} else {
							o.allowed, o.text = !res.IsError, resultText(res)
							if !rateLimited(res) {
								break
							}
						}
						o.retries++
						time.Sleep(rateLimitBackoff)
					}
					mu.Lock()
					outcomes = append(outcomes, o)
					mu.Unlock()
				}()
			}
		}
		close(start)
		wg.Wait()
		counts := map[string]int{}
		retries := 0
		for _, o := range outcomes {
			wantAllowed := slices.Contains(grants[o.caller], o.repo)
			if o.allowed != wantAllowed {
				t.Errorf("caller %s on %s: allowed=%v, want %v (%s)", o.caller, o.repo, o.allowed, wantAllowed, truncate(o.text, 200))
			}
			if !o.allowed && !strings.Contains(o.text, "repo_forbidden") {
				t.Errorf("caller %s on %s refused without repo_forbidden: %s", o.caller, o.repo, truncate(o.text, 200))
			}
			counts[fmt.Sprintf("%s:%s:allowed=%v", o.caller, o.repo, o.allowed)]++
			retries += o.retries
		}
		echoMismatch := 0
		for _, c := range callers {
			for _, x := range c.transport.exchanges() {
				if x.EchoedRequestID != x.RequestID {
					echoMismatch++
				}
			}
		}
		if echoMismatch > 0 {
			t.Errorf("%d responses echoed another request's correlation id", echoMismatch)
		}
		if len(outcomes) != 2*cfg.concurrency {
			t.Fatalf("%d outcomes, want %d", len(outcomes), 2*cfg.concurrency)
		}
		t.Logf("E2E-EVIDENCE concurrency: %d requests per caller, interleaved; outcomes=%v; rate-limit retries=%d; correlation echo mismatches=0; request id prefixes e2e-%s-conc-{A,C}",
			cfg.concurrency, counts, retries, cfg.runID)
		cfg.record(t, "08_concurrency", counts)
	})
}

func collectEvidenceRefs(m map[string]any) []string {
	var out []string
	var walk func(v any, key string)
	walk = func(v any, key string) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				walk(vv, k)
			}
		case []any:
			for _, vv := range x {
				walk(vv, key)
			}
		case string:
			if key == "evidence_ref_ids" || key == "evidence_ref_id" {
				if !slices.Contains(out, x) {
					out = append(out, x)
				}
			}
		}
	}
	walk(m, "")
	slices.Sort(out)
	return out
}

// firstReceipts picks, for every kind of choice a clarification offers, its
// first option, keyed by the request field that confirms it.
func firstReceipts(answer map[string]any) map[string]string {
	fields := []struct{ options, request string }{
		{"kind_options", "prior_kind_receipts"},
		{"anchor_options", "prior_anchor_receipts"},
		{"candidate_options", "prior_candidate_receipts"},
		{"handle_options", "prior_handle_receipts"},
		{"window_options", "prior_window_receipts"},
	}
	out := map[string]string{}
	for _, f := range fields {
		options, _ := field(answer, "structure_needs", f.options).([]any)
		for _, o := range options {
			if id, _ := field2(o, "receipt_id").(string); id != "" {
				out[f.request] = id
				break
			}
		}
	}
	return out
}

func field2(v any, path ...string) any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return field(m, path...)
}

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func lenOf(v any) int {
	items, _ := v.([]any)
	return len(items)
}

// The live runner, in CI, against acr-mcp in front of a real in-process
// acr-api: the harness itself is exercised on every change, so a live run
// that fails points at the deployment, not at the harness.
func TestHostedEndToEndRunnerAgainstAnInProcessEndpoint(t *testing.T) {
	target, _, _, callers := inProcessTarget(t)
	env := map[string]string{
		"ACR_MCP_MATRIX_URL":          target.url,
		"ACR_MCP_MATRIX_BEARER_A":     callers.a.token,
		"ACR_MCP_MATRIX_BEARER_C":     callers.c.token,
		"ACR_MCP_MATRIX_BEARER_H":     callers.h.token,
		"ACR_MCP_MATRIX_REPO_A":       repoWidget,
		"ACR_MCP_MATRIX_E2E_REPO_C":   repoOther,
		"ACR_MCP_MATRIX_E2E_QUESTION": "which work needs attention?",
		"ACR_MCP_MATRIX_E2E_TOOLS_H":  strings.Join(toolsWriteSet, ","),
		"ACR_MCP_MATRIX_RESULT_A":     resultOwnedByA,
	}
	cfg, missing := hostedE2EConfigFromEnv(mapGetenv(env))
	if cfg == nil || len(missing) != 0 {
		t.Fatalf("config %v missing %v", cfg, missing)
	}
	cfg.investigatedNotStored = true
	runHostedE2E(t, cfg)
}
