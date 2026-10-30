//go:build fixturegraph

package fixturegraph

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The investigation event stream on acr's own live stack (plan item I-S09):
// the real acr-api behind its TLS proxy, with ACR_CONTEXT_FABRIC_EVENT_STREAM_ENABLED on
// (scripts/e2e/fixture-graph.sh), driven over HTTP the way the web panel will drive it -- the
// MCP binary never asks for the stream or names a submission. Every turn is a client
// synthesis turn with a supplied interpretation, the venue's model-free path (the venue has
// no model provider), and its body is the one the MCP investigate_with_interpretation tool
// sends for the same question.

// apiClient trusts the venue's CA.
func apiClient(t *testing.T) *http.Client {
	t.Helper()
	pem, err := os.ReadFile(mustEnv(t, "FG_CA_FILE"))
	if err != nil {
		t.Fatalf("read FG_CA_FILE: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("FG_CA_FILE holds no certificate")
	}
	return &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// ownerTurnRequest is the body investigate_with_interpretation sends for "which team owns
// project P?" (internal/mcp/investigate_question.go hostedInvestigationRequest, and the
// sidecar's consumer stamp), so the HTTP turn and the MCP turn are the same request.
func ownerTurnRequest(t *testing.T, c *client, project string) contractsv1.ContextFabricInvestigationRequest {
	t.Helper()
	interpretation, err := json.Marshal(doc{
		"shape": "single_subject", "requested_judgment": "the team that owns the project",
		"subject_terms": []string{project}, "requested_subject_kind": "project",
		"time_context": doc{"axis": "current"}, "fact_requirements": []doc{{"kind": "identity"}},
		"clarification_needed": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	contract := c.interpretContract()
	return contractsv1.ContextFabricInvestigationRequest{
		SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema,
		RequestID:     "req_" + randomHex(t, 16),
		Question:      fmt.Sprintf("Which team owns the project %s?", project),
		ExpectedKinds: []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectProject},
		TimeContext: contractsv1.ContextFabricTimeContext{
			Axis:           contractsv1.ContextFabricTemporalCurrent,
			EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowTrailing365D},
		},
		Options: contractsv1.ContextFabricInvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100,
			// The venue's ACR_MAX_SERIALIZED_BYTES default, as the MCP tool caps a large budget.
			MaxSerializedBytes: 262144, AnswerBudgetBytes: 262144, AllowClarification: true,
		},
		Consumer: contractsv1.ContextFabricConsumerInfo{Name: "fixture-graph", Version: "0.0.1", Surface: "mcp"},
		SuppliedInterpretation: &contractsv1.ContextFabricSuppliedInterpretation{
			Output:             interpretation,
			ModelOutputVersion: str(contract, "model_output_version"),
			PromptVersion:      str(contract, "prompt_version"),
			SystemSHA256:       str(contract, "system_sha256"),
		},
		SynthesisMode: contractsv1.ContextFabricSynthesisModeClient,
	}
}

type apiAnswer struct {
	status      int
	contentType string
	retryAfter  string
	requestID   string
	raw         []byte
}

// postInvestigation sends one create-investigation request. stream asks for the event
// stream; submissionID, when set, names the submission.
func postInvestigation(t *testing.T, token string, body contractsv1.ContextFabricInvestigationRequest, stream bool, submissionID string) apiAnswer {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, mustEnv(t, "FG_API_URL")+"/api/v1/context-fabric/investigations", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	requestID := "req_" + randomHex(t, 16)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", requestID)
	if stream {
		request.Header.Set("Accept", contractsv1.ContextFabricInvestigationEventStreamMediaType)
	} else {
		request.Header.Set("Accept", "application/json")
	}
	if submissionID != "" {
		request.Header.Set(contractsv1.ContextFabricSubmissionIDHeader, submissionID)
	}
	response, err := apiClient(t).Do(request)
	if err != nil {
		t.Fatalf("POST investigations: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the response: %v", err)
	}
	return apiAnswer{status: response.StatusCode, contentType: response.Header.Get("Content-Type"), retryAfter: response.Header.Get("Retry-After"), requestID: requestID, raw: raw}
}

// streamOf reads an answer as the event stream with the contract's own strict reader and
// holds it to the contract's grammar.
func streamOf(t *testing.T, answer apiAnswer) []contractsv1.ContextFabricInvestigationEvent {
	t.Helper()
	if answer.status != http.StatusOK || answer.contentType != contractsv1.ContextFabricInvestigationEventStreamMediaType {
		t.Fatalf("want a 200 event stream, got %d %q: %.1500s", answer.status, answer.contentType, answer.raw)
	}
	events, err := contractsv1.DecodeContextFabricInvestigationEventFrames(bytes.NewReader(answer.raw), 8<<20)
	if err != nil {
		t.Fatalf("the body is not the writer's frames: %v\n%.3000s", err, answer.raw)
	}
	if err := contractsv1.ValidateContextFabricInvestigationEventStream(events); err != nil {
		t.Fatalf("the stream breaks the contract grammar: %v\n%.3000s", err, answer.raw)
	}
	return events
}

func decodeDoc(t *testing.T, raw []byte) doc {
	t.Helper()
	var d doc
	if err := json.Unmarshal(raw, &d); err != nil || d == nil {
		t.Fatalf("not one JSON object: %v: %.1500s", err, raw)
	}
	return d
}

// ownerProject is an active project with exactly one open owner, from the seeded rows.
func ownerProject(t *testing.T) string {
	t.Helper()
	rows := ch(t, fmt.Sprintf(`SELECT p.name FROM team_project_ownership AS o FINAL
INNER JOIN projects AS p FINAL ON p.org_id = o.org_id AND p.id = o.project_id
WHERE o.org_id = %[1]s AND o.valid_to IS NULL AND p.is_active = 1
AND o.project_id IN (SELECT project_id FROM team_project_ownership FINAL WHERE org_id = %[1]s AND valid_to IS NULL GROUP BY project_id HAVING uniqExact(team_id) = 1)
ORDER BY p.name LIMIT 1`, sqlStr(orgID(t))))
	if len(rows) != 1 {
		t.Fatalf("no active project with exactly one open owner: %v", rows)
	}
	return rows[0][0]
}

// answeredTurn is the owner turn that ends in an answer: when the first turn asks to confirm
// the subject kind, the follow-up names its receipt, as the MCP flow does. The returned body
// is deterministic, so it can be sent again on another path.
func answeredTurn(t *testing.T, c *client, token, project string) contractsv1.ContextFabricInvestigationRequest {
	t.Helper()
	body := ownerTurnRequest(t, c, project)
	first := postInvestigation(t, token, body, false, "")
	if first.status != http.StatusOK {
		t.Fatalf("the owner turn: %d %.1500s", first.status, first.raw)
	}
	d := decodeDoc(t, first.raw)
	if str(d, "status") != "clarification_required" {
		return body
	}
	for _, o := range list(d, "structure_needs", "kind_options") {
		if str(o, "kind") == "project" {
			rid := str(d, "result_id")
			body.ParentResultID = rid
			body.PriorKindReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: rid, ReceiptID: str(o, "receipt_id")}}
			return body
		}
	}
	t.Fatalf("the clarification offers no project kind: %.1500s", first.raw)
	return body
}

// normalizedRun is one answer body with the run's own identity removed: its result_id and
// request_id, wherever they appear, and every generated_at. What remains is what the run
// decided and served.
func normalizedRun(t *testing.T, raw []byte) any {
	t.Helper()
	d := decodeDoc(t, raw)
	resultID, requestID := str(d, "result_id"), str(d, "request_id")
	if resultID == "" || requestID == "" {
		t.Fatalf("the answer names no result_id or request_id: %.800s", raw)
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, item := range x {
				if k == "generated_at" {
					continue
				}
				out[k] = walk(item)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, item := range x {
				out[i] = walk(item)
			}
			return out
		case string:
			switch x {
			case resultID:
				return "<result_id>"
			case requestID:
				return "<request_id>"
			}
			return x
		default:
			return x
		}
	}
	return walk(d)
}

// differingPaths names up to ten paths at which two decoded JSON values differ.
func differingPaths(a, b any, path string, out *[]string) {
	if len(*out) >= 10 || reflect.DeepEqual(a, b) {
		return
	}
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			differingPaths(am[k], bm[k], path+"."+k, out)
		}
		return
	}
	as, aok := a.([]any)
	bs, bok := b.([]any)
	if aok && bok && len(as) == len(bs) {
		for i := range as {
			differingPaths(as[i], bs[i], fmt.Sprintf("%s[%d]", path, i), out)
		}
		return
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	*out = append(*out, fmt.Sprintf("%s: %.200s != %.200s", path, ja, jb))
}

// TestTheInvestigationStreamOnTheLiveStack: the stream request is answered as the
// context_fabric_investigation_event.v1 stream -- accepted first with the reserved result id,
// the engine's stages, exactly one terminal -- and the terminal result carries the reserved
// id and this request's id.
func TestTheInvestigationStreamOnTheLiveStack(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	token := readTokenFile(t, "FG_ORG_TOKEN_FILE")
	body := answeredTurn(t, c, token, ownerProject(t))
	answer := postInvestigation(t, token, body, true, "")
	events := streamOf(t, answer)
	accepted, terminal := events[0], events[len(events)-1]
	if accepted.Type != contractsv1.ContextFabricInvestigationEventAccepted || accepted.ReservedResultID == "" || accepted.RequestID != answer.requestID {
		t.Fatalf("first event %+v, want accepted for this request with a reserved id", accepted)
	}
	stages := 0
	for _, event := range events {
		if event.Type == contractsv1.ContextFabricInvestigationEventStage {
			stages++
		}
	}
	if stages == 0 {
		t.Fatalf("the stream shows no stage event:\n%.3000s", answer.raw)
	}
	if terminal.Type != contractsv1.ContextFabricInvestigationEventResult {
		t.Fatalf("terminal %+v, want the result:\n%.3000s", terminal, answer.raw)
	}
	result := decodeDoc(t, terminal.Result)
	if str(result, "result_id") != accepted.ReservedResultID || str(result, "request_id") != answer.requestID {
		t.Fatalf("the result names result_id %q and request_id %q, want the reserved %q and %q", str(result, "result_id"), str(result, "request_id"), accepted.ReservedResultID, answer.requestID)
	}
	t.Logf("stream: %d events, %d stage events, result status %s", len(events), stages, str(result, "status"))
	requireNoRunningSubmission(t)
}

// TestJSONAndTheStreamServeTheSameLiveAnswer: the same request sent once as JSON and once as
// the stream is answered the same: the stream's result is the JSON body once each run's own
// identity (result_id, request_id, generated_at) is set aside.
//
// The turn asks for no clarification, so it names no receipt: a follow-up turn is not
// repeatable (its receipt is superseded by the first answer, and the second run vetoes it as
// stale), but this first turn is, and both paths run it.
func TestJSONAndTheStreamServeTheSameLiveAnswer(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	token := readTokenFile(t, "FG_ORG_TOKEN_FILE")
	body := ownerTurnRequest(t, c, ownerProject(t))
	body.Options.AllowClarification = false
	jsonAnswer := postInvestigation(t, token, body, false, "")
	if jsonAnswer.status != http.StatusOK || !strings.HasPrefix(jsonAnswer.contentType, "application/json") {
		t.Fatalf("the JSON answer: %d %q %.1500s", jsonAnswer.status, jsonAnswer.contentType, jsonAnswer.raw)
	}
	events := streamOf(t, postInvestigation(t, token, body, true, ""))
	terminal := events[len(events)-1]
	if terminal.Type != contractsv1.ContextFabricInvestigationEventResult {
		t.Fatalf("the stream's terminal is %s, the JSON answer is a 200", terminal.Type)
	}
	want, got := normalizedRun(t, jsonAnswer.raw), normalizedRun(t, terminal.Result)
	if !reflect.DeepEqual(want, got) {
		var paths []string
		differingPaths(want, got, "$", &paths)
		t.Fatalf("the stream's result differs from the JSON answer at:\n%s", strings.Join(paths, "\n"))
	}
}

// TestASameKeyRetryOfAStreamedSubmissionRunsNothing: a streamed submission's accepted names
// the submission and the id its record reserved; a JSON retry of the key replays that result
// (the same result_id and generated_at: nothing ran again), a stream retry of the key is
// answered the same way before any stream starts, and the key with another body is refused.
func TestASameKeyRetryOfAStreamedSubmissionRunsNothing(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	token := readTokenFile(t, "FG_ORG_TOKEN_FILE")
	body := answeredTurn(t, c, token, ownerProject(t))
	key := "fg-stream-" + randomHex(t, 8)
	events := streamOf(t, postInvestigation(t, token, body, true, key))
	accepted, terminal := events[0], events[len(events)-1]
	if accepted.SubmissionID != key || accepted.ReservedResultID == "" {
		t.Fatalf("accepted %+v, want the submission %q and its reserved id", accepted, key)
	}
	if terminal.Type != contractsv1.ContextFabricInvestigationEventResult {
		t.Fatalf("terminal %+v, want the result", terminal)
	}
	first := decodeDoc(t, terminal.Result)
	if str(first, "result_id") != accepted.ReservedResultID {
		t.Fatalf("the answer's result_id %q, want the submission's reserved %q", str(first, "result_id"), accepted.ReservedResultID)
	}
	for _, retry := range []struct {
		name   string
		stream bool
	}{{"JSON retry", false}, {"stream retry", true}} {
		answer := postInvestigation(t, token, body, retry.stream, key)
		if answer.status != http.StatusOK || !strings.HasPrefix(answer.contentType, "application/json") {
			t.Fatalf("%s: %d %q, want the replayed JSON answer before any stream: %.1500s", retry.name, answer.status, answer.contentType, answer.raw)
		}
		replayed := decodeDoc(t, answer.raw)
		if str(replayed, "result_id") != str(first, "result_id") || str(replayed, "generated_at") != str(first, "generated_at") {
			t.Fatalf("%s: result_id %q generated_at %q, want the first run's %q %q (a replay runs nothing)", retry.name, str(replayed, "result_id"), str(replayed, "generated_at"), str(first, "result_id"), str(first, "generated_at"))
		}
	}
	other := body
	other.Question = body.Question + " Please be brief."
	mismatch := postInvestigation(t, token, other, true, key)
	if mismatch.status != http.StatusConflict || !strings.Contains(string(mismatch.raw), `"`+contractsv1.ErrorCodeSubmissionMismatch+`"`) {
		t.Fatalf("the key with another body: %d %.800s, want 409 %s", mismatch.status, mismatch.raw, contractsv1.ErrorCodeSubmissionMismatch)
	}
	if state := pgScalar(t, fmt.Sprintf(`SELECT state FROM acr.context_fabric_investigation_submissions WHERE org_id = %s AND submission_id = %s`, sqlStr(orgID(t)), sqlStr(key))); state != "done" {
		t.Fatalf("the streamed submission's record is %q after its terminal, want done", state)
	}
	requireNoRunningSubmission(t)
}

// pg runs one SQL script, in one session, against acr's Postgres and returns its
// tab-separated rows.
func pg(t testing.TB, sql string) [][]string {
	t.Helper()
	cmd := exec.Command(mustEnv(t, "FG_PG_QUERY"))
	cmd.Stdin = strings.NewReader(sql)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("postgres query failed: %v\n%s\nSQL: %s", err, stderr.String(), sql)
	}
	var rows [][]string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			rows = append(rows, strings.Split(line, "\t"))
		}
	}
	return rows
}

func pgScalar(t testing.TB, sql string) string {
	t.Helper()
	rows := pg(t, sql)
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("want one scalar from %q, got %v", sql, rows)
	}
	return rows[0][0]
}

const submissionsTable = "acr.context_fabric_investigation_submissions"

// runningSubmissions counts the org's submission records still running.
func runningSubmissions(t testing.TB) int {
	t.Helper()
	n, err := strconv.Atoi(pgScalar(t, fmt.Sprintf(`SELECT count(*) FROM %s WHERE org_id = %s AND state = 'running'`, submissionsTable, sqlStr(orgID(t)))))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// requireNoRunningSubmission: once a run's terminal event is written, no record of the org is
// left running -- a record left running would hold a concurrency slot no run fills.
func requireNoRunningSubmission(t *testing.T) {
	t.Helper()
	if n := runningSubmissions(t); n != 0 {
		t.Fatalf("%d submission records are still running after the terminal: %v", n, pg(t, fmt.Sprintf(`SELECT submission_id, state FROM %s WHERE org_id = %s AND state = 'running'`, submissionsTable, sqlStr(orgID(t)))))
	}
}

// plantHeldRuns adds n running records for the principal whose real record is templateKey,
// each a copy of that record (every column, the owner included) with its own key and reserved
// id, admitted now and expiring in ten minutes. They are the runs another replica holds in
// flight: written outside this acr-api, read by its admission from the shared table.
func plantHeldRuns(t *testing.T, templateKey string, n int) []string {
	t.Helper()
	var keys []string
	var script strings.Builder
	fmt.Fprintf(&script, "CREATE TEMP TABLE planted AS SELECT * FROM %s WHERE org_id = %s AND submission_id = %s;\n", submissionsTable, sqlStr(orgID(t)), sqlStr(templateKey))
	for i := 0; i < n; i++ {
		key := "fg-held-" + randomHex(t, 8)
		keys = append(keys, key)
		fmt.Fprintf(&script, "UPDATE planted SET submission_id = %s, reserved_result_id = %s, state = 'running', result_id = NULL, admitted_at = now(), updated_at = now(), expires_at = now() + interval '10 minutes';\nINSERT INTO %s SELECT * FROM planted;\n",
			sqlStr(key), sqlStr("result_fgheld"+randomHex(t, 16)), submissionsTable)
	}
	fmt.Fprintf(&script, "SELECT count(*) FROM %s WHERE org_id = %s AND submission_id IN (%s) AND state = 'running';\n", submissionsTable, sqlStr(orgID(t)), sqlList(keys))
	if got := pg(t, script.String()); len(got) != 1 || got[0][0] != strconv.Itoa(n) {
		t.Fatalf("planted %v running records, want %d (is the template %q in the table?)", got, n, templateKey)
	}
	return keys
}

func sqlList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = sqlStr(v)
	}
	return strings.Join(quoted, ", ")
}

// templateSubmission makes one real submission for token, so its record carries the
// principal's owner identity, and returns its key.
func templateSubmission(t *testing.T, token string, body contractsv1.ContextFabricInvestigationRequest) string {
	t.Helper()
	key := "fg-template-" + randomHex(t, 8)
	answer := postInvestigation(t, token, body, false, key)
	if answer.status == http.StatusTooManyRequests {
		t.Fatalf("the template submission was refused: %.800s", answer.raw)
	}
	if n := pgScalar(t, fmt.Sprintf(`SELECT count(*) FROM %s WHERE org_id = %s AND submission_id = %s`, submissionsTable, sqlStr(orgID(t)), sqlStr(key))); n != "1" {
		t.Fatalf("the template submission left %s records, want 1 (status %d: %.500s)", n, answer.status, answer.raw)
	}
	return key
}

// expectConcurrencyRefusal: a new key is refused 429 concurrency_limited for limit, with a
// Retry-After, before anything is admitted -- no record is written for it.
func expectConcurrencyRefusal(t *testing.T, answer apiAnswer, key, limit string) {
	t.Helper()
	if answer.status != http.StatusTooManyRequests {
		t.Fatalf("want 429 %s, got %d: %.800s", limit, answer.status, answer.raw)
	}
	envelope := decodeDoc(t, answer.raw)
	if code := str(envelope, "error", "code"); code != contractsv1.ErrorCodeConcurrencyLimited {
		t.Fatalf("error code %q, want %s: %.800s", code, contractsv1.ErrorCodeConcurrencyLimited, answer.raw)
	}
	if got := str(envelope, "error", "details", contractsv1.ContextFabricConcurrencyLimitDetailsKey); got != limit {
		t.Fatalf("details.%s %q, want %q: %.800s", contractsv1.ContextFabricConcurrencyLimitDetailsKey, got, limit, answer.raw)
	}
	if seconds, err := strconv.Atoi(answer.retryAfter); err != nil || seconds < 1 {
		t.Fatalf("Retry-After %q, want whole seconds", answer.retryAfter)
	}
	if n := pgScalar(t, fmt.Sprintf(`SELECT count(*) FROM %s WHERE org_id = %s AND submission_id = %s`, submissionsTable, sqlStr(orgID(t)), sqlStr(key))); n != "0" {
		t.Fatalf("the refused key left %s records, want 0", n)
	}
}

// TestBothConcurrencyBoundariesAgainstPlantedHeldRuns: the shared concurrency ceilings
// (2 running per principal, 4 per org: the acr-api defaults), counted from the submission
// table. No run in this venue stays in flight for long (every turn is a fast client synthesis
// turn and the venue has no model), so the runs held in flight are PLANTED: running records
// copied from each principal's real record, written outside acr-api the way another replica's
// runs are. Real held runs are proved live on kiac (I-48). The admission that counts them and
// refuses is the real one, over the stream path.
func TestBothConcurrencyBoundariesAgainstPlantedHeldRuns(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	tokenA, tokenB, tokenC := readTokenFile(t, "FG_ORG_TOKEN_FILE"), readTokenFile(t, "FG_SCOPED_TOKEN_FILE"), readTokenFile(t, "FG_THIRD_TOKEN_FILE")
	body := ownerTurnRequest(t, c, ownerProject(t))
	if n := runningSubmissions(t); n != 0 {
		t.Fatalf("%d records already running before the case: it would not start from an empty org", n)
	}
	templateA, templateB := templateSubmission(t, tokenA, body), templateSubmission(t, tokenB, body)
	var planted []string
	t.Cleanup(func() {
		if len(planted) > 0 {
			// Only the planted runs still held: the one marked done keeps its result id,
			// which an abandoned record may not carry.
			pg(t, fmt.Sprintf(`UPDATE %s SET state = 'abandoned', updated_at = now() WHERE org_id = %s AND submission_id IN (%s) AND state = 'running'`, submissionsTable, sqlStr(orgID(t)), sqlList(planted)))
		}
	})

	// The principal boundary: A holds its 2, the org has room for 2 more. A's third run is
	// refused for the principal.
	planted = append(planted, plantHeldRuns(t, templateA, 2)...)
	key := "fg-third-run-" + randomHex(t, 8)
	expectConcurrencyRefusal(t, postInvestigation(t, tokenA, body, true, key), key, "principal")

	// The org boundary: B holds 2 as well, so the org holds 4. C runs nothing, and its first
	// run -- the org's fifth -- is refused for the org.
	planted = append(planted, plantHeldRuns(t, templateB, 2)...)
	key = "fg-fifth-run-" + randomHex(t, 8)
	expectConcurrencyRefusal(t, postInvestigation(t, tokenC, body, true, key), key, "org")

	// And from the other side: one held run ends, and C's next run is admitted, streamed and
	// finished, and its record does not stay running.
	pg(t, fmt.Sprintf(`UPDATE %s SET state = 'done', result_id = reserved_result_id, updated_at = now() WHERE org_id = %s AND submission_id = %s`, submissionsTable, sqlStr(orgID(t)), sqlStr(planted[0])))
	key = "fg-admitted-run-" + randomHex(t, 8)
	events := streamOf(t, postInvestigation(t, tokenC, body, true, key))
	if accepted := events[0]; accepted.SubmissionID != key {
		t.Fatalf("accepted %+v, want the admitted submission %q", accepted, key)
	}
	if state := pgScalar(t, fmt.Sprintf(`SELECT state FROM %s WHERE org_id = %s AND submission_id = %s`, submissionsTable, sqlStr(orgID(t)), sqlStr(key))); state == "running" {
		t.Fatalf("the admitted run's record is still running after its terminal")
	}
}
