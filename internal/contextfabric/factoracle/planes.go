package factoracle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// FactsRequest is the read_facts request the oracle sends.
type FactsRequest struct {
	Kinds    []string       `json:"kinds"`
	Subjects []FactsSubject `json:"subjects"`
	Window   *FactsWindow   `json:"window,omitempty"`
	Tables   string         `json:"tables,omitempty"`
	MaxBytes int            `json:"max_bytes,omitempty"`
}

// FactsSubject is a canonical subject.
type FactsSubject struct {
	Kind        string `json:"kind"`
	CanonicalID string `json:"canonical_id"`
}

// FactsWindow is a range window.
type FactsWindow struct {
	Mode  string    `json:"mode"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Planes are the two tools the oracle calls. Both return the tool's JSON
// answer, so the live and the recorded mode are read by the same code.
type Planes interface {
	GraphQL(ctx context.Context, shape Shape, variables map[string]any) (json.RawMessage, error)
	// Operation runs run_operation for the operation of shape, which is the
	// operation's full selection.
	Operation(ctx context.Context, shape Shape, variables map[string]any) (json.RawMessage, error)
	Facts(ctx context.Context, request FactsRequest) (json.RawMessage, error)
}

// OperationAnswer is the part of the run_operation answer the oracle reads.
type OperationAnswer struct {
	Call      string `json:"call"`
	Result    string `json:"result"`
	Operation string `json:"operation"`
	Refusal   *struct {
		Code string `json:"code"`
	} `json:"refusal"`
	Errors []struct {
		Class string `json:"class"`
	} `json:"errors"`
	Data json.RawMessage `json:"data"`
	Page struct {
		ReturnedBytes int `json:"returned_bytes"`
		MaxBytes      int `json:"max_bytes"`
	} `json:"page"`
}

// OperationKey names one run_operation call.
func OperationKey(shape Shape, variables map[string]any) string {
	sum := sha256.Sum256([]byte(canonicalJSON(variables)))
	return "run_operation/" + shape.Operation + "#" + hex.EncodeToString(sum[:8])
}

// GraphQLAnswer is the part of the graphql_query answer the oracle reads.
type GraphQLAnswer struct {
	Call         string `json:"call"`
	Completeness string `json:"completeness"`
	Result       string `json:"result"`
	Refusal      *struct {
		Code   string `json:"code"`
		Reason string `json:"reason"`
		Path   string `json:"path"`
	} `json:"refusal"`
	Errors []struct {
		Class string `json:"class"`
	} `json:"errors"`
	RootFields []struct {
		Key       string `json:"key"`
		Field     string `json:"field"`
		Operation string `json:"operation"`
	} `json:"root_fields"`
	Data json.RawMessage `json:"data"`
	Page struct {
		ReturnedBytes int `json:"returned_bytes"`
		MaxBytes      int `json:"max_bytes"`
	} `json:"page"`
	Source struct {
		SchemaDigest string `json:"schema_digest"`
	} `json:"source"`
}

// FactsAnswer is the part of the read_facts answer the oracle reads. Field
// and table values keep their JSON form (json.Number for numbers).
type FactsAnswer struct {
	Status   string       `json:"status"`
	Facts    []ServedFact `json:"facts"`
	Coverage []struct {
		Kind    string       `json:"kind"`
		Subject FactsSubject `json:"subject"`
		Outcome string       `json:"outcome"`
	} `json:"coverage"`
	Truncation *struct {
		TruncatedBy string `json:"truncated_by"`
	} `json:"truncation"`
	Versions struct {
		Kinds map[string]string `json:"kinds"`
	} `json:"versions"`
}

// ServedFact is one fact of a read_facts answer.
type ServedFact struct {
	Kind    string                 `json:"kind"`
	Subject FactsSubject           `json:"subject"`
	Fields  map[string]any         `json:"fields"`
	Tables  map[string]ServedTable `json:"tables"`
}

// ServedTable is one table of a fact.
type ServedTable struct {
	Columns     []string `json:"columns"`
	Rows        [][]any  `json:"rows"`
	TruncatedBy string   `json:"truncated_by"`
	RowsOmitted int      `json:"rows_omitted"`
}

func decodeNumbered(raw []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(into)
}

// canonicalJSON renders a value with sorted object keys and with every list
// of strings sorted, so a key does not depend on the order of an id list.
func canonicalJSON(value any) string {
	var normalize func(v any) any
	normalize = func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			out := make(map[string]any, len(t))
			for k, item := range t {
				out[k] = normalize(item)
			}
			return out
		case []any:
			out := make([]any, len(t))
			allStrings := true
			for i, item := range t {
				out[i] = normalize(item)
				if _, ok := out[i].(string); !ok {
					allStrings = false
				}
			}
			if allStrings {
				sort.Slice(out, func(i, j int) bool { return out[i].(string) < out[j].(string) })
			}
			return out
		case []string:
			out := make([]any, len(t))
			for i, item := range t {
				out[i] = item
			}
			return normalize(out)
		default:
			return v
		}
	}
	// A round trip through JSON gives every value its generic form first.
	encoded, err := json.Marshal(value)
	if err != nil {
		return "!" + err.Error()
	}
	generic, err := decodeJSON(encoded)
	if err != nil {
		return "!" + err.Error()
	}
	out, err := json.Marshal(normalize(generic))
	if err != nil {
		return "!" + err.Error()
	}
	return string(out)
}

// CaseKey names one ops call: the shape and its variables.
func CaseKey(shape Shape, variables map[string]any) string {
	sum := sha256.Sum256([]byte(canonicalJSON(variables)))
	return shape.ID() + "#" + hex.EncodeToString(sum[:8])
}

// RecordedReply is what the real listener answered for one case, as acr
// served it (acr removes no allowed path and cuts no payload), or the fact
// that the root was not enabled on the listener.
type RecordedReply struct {
	// Status is the listener's HTTP status: 200, or 404 for a root that is
	// not enabled.
	Status int `json:"status"`
	// Reason is the listener's refusal reason for a 404.
	Reason string `json:"reason,omitempty"`
	// Data is the "data" object of the reply.
	Data json.RawMessage `json:"data,omitempty"`
	// Call, Result and Operation are what acr answered on the venue.
	Call      string `json:"call"`
	Result    string `json:"result,omitempty"`
	Operation string `json:"operation,omitempty"`
}

// Recording is the recorded replies of one capture, by case key.
type Recording struct {
	Replies map[string]RecordedReply `json:"replies"`
}

// ReplayListener stands in for the ops query service in the recorded mode:
// the MCP listener graphql_query calls and the registered-document route
// run_operation calls (both are POST /query with the internal headers). It
// answers one armed case at a time with the reply the real listener gave; a
// request with no armed case, or for another root field, is a failure and is
// answered with 500.
type ReplayListener struct {
	mu       sync.Mutex
	armed    *RecordedReply
	root     string
	orgID    string
	requests int
	failures []string
}

// NewReplayListener makes a listener that accepts requests for orgID only.
func NewReplayListener(orgID string) *ReplayListener { return &ReplayListener{orgID: orgID} }

func (l *ReplayListener) arm(root string, reply RecordedReply) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.armed, l.root = &reply, root
}

func (l *ReplayListener) disarm() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.armed, l.root = nil, ""
}

// Requests is the number of requests the listener received.
func (l *ReplayListener) Requests() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.requests
}

// Failures lists every request the listener could not answer from a record.
func (l *ReplayListener) Failures() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.failures...)
}

func (l *ReplayListener) fail(w http.ResponseWriter, format string, args ...any) {
	l.failures = append(l.failures, fmt.Sprintf(format, args...))
	http.Error(w, "replay listener: no record", http.StatusInternalServerError)
}

// ServeHTTP implements http.Handler.
func (l *ReplayListener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests++
	if r.Method != http.MethodPost || r.URL.Path != directread.GraphQLListenerPath {
		l.fail(w, "request is %s %s, want POST %s", r.Method, r.URL.Path, directread.GraphQLListenerPath)
		return
	}
	if r.Header.Get("Authorization") != "" {
		l.fail(w, "request carries an Authorization header")
		return
	}
	if got := r.Header.Get(directread.HeaderInternalOrgID); got != l.orgID {
		l.fail(w, "org header is not the fixture organization")
		return
	}
	var body struct {
		Query string `json:"query"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || json.Unmarshal(raw, &body) != nil {
		l.fail(w, "request body is not a GraphQL request")
		return
	}
	doc, perr := parser.ParseQuery(&ast.Source{Input: body.Query})
	if perr != nil || len(doc.Operations) != 1 || len(doc.Operations[0].SelectionSet) != 1 {
		l.fail(w, "request is not one operation with one root field")
		return
	}
	field, ok := doc.Operations[0].SelectionSet[0].(*ast.Field)
	if !ok {
		l.fail(w, "root selection is not a field")
		return
	}
	if l.armed == nil || field.Name != l.root {
		l.fail(w, "no reply is armed for root %s", field.Name)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if l.armed.Status == http.StatusNotFound {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{
			"message":    "not found",
			"extensions": map[string]any{"code": directread.MCPRefusedCode, "reason": l.armed.Reason},
		}}})
		return
	}
	if l.armed.Status != http.StatusOK {
		l.fail(w, "armed reply has status %d", l.armed.Status)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": l.armed.Data})
}

// LocalPlanes runs both tools in process: the real graphql_query runner over
// the replay listener and the real read_facts reader over a seeded store.
type LocalPlanes struct {
	Runner     *directread.GraphQLRunner
	Operations *directread.OperationRunner
	Facts_     *directread.FactsReader
	Principal  storage.Principal
	Listener   *ReplayListener
	Recording  *Recording
	seq        int
	used       map[string]bool
}

// Unused lists the recorded replies no call of the run asked for.
func (p *LocalPlanes) Unused() []string {
	var out []string
	for key := range p.Recording.Replies {
		if !p.used[key] {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func (p *LocalPlanes) requestContext(ctx context.Context) context.Context {
	p.seq++
	return observability.WithRequestID(ctx, fmt.Sprintf("req_%032x", p.seq))
}

// GraphQL implements Planes.
func (p *LocalPlanes) GraphQL(ctx context.Context, shape Shape, variables map[string]any) (json.RawMessage, error) {
	key := CaseKey(shape, variables)
	reply, ok := p.Recording.Replies[key]
	if !ok {
		return nil, fmt.Errorf("no recorded reply for case %s", key)
	}
	if p.used == nil {
		p.used = map[string]bool{}
	}
	p.used[key] = true
	query, sent, err := shape.Query(variables)
	if err != nil {
		return nil, err
	}
	encodedVars, err := json.Marshal(sent)
	if err != nil {
		return nil, err
	}
	p.Listener.arm(shape.Root, reply)
	defer p.Listener.disarm()
	before := p.Listener.Requests()
	response, err := p.Runner.Run(p.requestContext(ctx), p.Principal, directread.GraphQLRequest{Query: query, Variables: encodedVars})
	if err != nil {
		return nil, err
	}
	if got := p.Listener.Requests() - before; got != 1 {
		return nil, fmt.Errorf("case %s: the runner sent %d listener requests, want 1 (call %s)", key, got, response.Call)
	}
	return json.Marshal(response)
}

// Operation implements Planes.
func (p *LocalPlanes) Operation(ctx context.Context, shape Shape, variables map[string]any) (json.RawMessage, error) {
	key := OperationKey(shape, variables)
	reply, ok := p.Recording.Replies[key]
	if !ok {
		return nil, fmt.Errorf("no recorded reply for case %s", key)
	}
	if p.used == nil {
		p.used = map[string]bool{}
	}
	p.used[key] = true
	encodedVars, err := json.Marshal(variables)
	if err != nil {
		return nil, err
	}
	p.Listener.arm(shape.Root, reply)
	defer p.Listener.disarm()
	before := p.Listener.Requests()
	response, err := p.Operations.Run(p.requestContext(ctx), p.Principal, directread.OperationRequest{Operation: shape.Operation, Variables: encodedVars})
	if err != nil {
		return nil, err
	}
	if got := p.Listener.Requests() - before; got != 1 {
		return nil, fmt.Errorf("case %s: the runner sent %d upstream requests, want 1 (call %s)", key, got, response.Call)
	}
	return json.Marshal(response)
}

// Facts implements Planes.
func (p *LocalPlanes) Facts(ctx context.Context, request FactsRequest) (json.RawMessage, error) {
	read := directread.FactsRequest{Kinds: request.Kinds, MaxBytes: request.MaxBytes, Tables: request.Tables}
	for _, s := range request.Subjects {
		read.Subjects = append(read.Subjects, directread.RequestSubject{Kind: s.Kind, CanonicalID: s.CanonicalID})
	}
	if request.Window != nil {
		start, end := request.Window.Start, request.Window.End
		read.Window = &directread.RequestWindow{Mode: request.Window.Mode, Start: &start, End: &end}
	}
	response, err := p.Facts_.Read(p.requestContext(ctx), p.Principal, read)
	if err != nil {
		return nil, err
	}
	return json.Marshal(response)
}

// VenuePlanes calls both tools on a hosted MCP server.
type VenuePlanes struct {
	// URL is the MCP endpoint.
	URL string
	// TokenFile holds the bearer credential. It is read at every call and is
	// never logged.
	TokenFile string
	Client    *http.Client
}

// venueRetries bounds how often one call waits out a rate limit answer.
const venueRetries = 8

// call runs one tool. A 429 is the venue's rate limit: the call waits the
// time the server names in Retry-After and tries again; the limit itself is
// never worked around.
func (p *VenuePlanes) call(ctx context.Context, tool string, arguments any) (json.RawMessage, error) {
	for attempt := 0; ; attempt++ {
		raw, status, retryAfter, err := p.post(ctx, tool, arguments)
		if err != nil {
			return nil, err
		}
		if status == http.StatusTooManyRequests && attempt < venueRetries {
			wait := 5 * time.Second
			if seconds, perr := strconv.Atoi(strings.TrimSpace(retryAfter)); perr == nil && seconds > 0 {
				wait = time.Duration(min(seconds, 60)) * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("venue call %s: rate limited and the run deadline passed", tool)
			case <-time.After(wait):
			}
			continue
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("venue call %s: HTTP %d", tool, status)
		}
		content, limited, err := venueStructuredContent(tool, raw)
		if limited && attempt < venueRetries {
			// The API behind the MCP server answered its own rate limit; the
			// server hands that on as a tool error.
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("venue call %s: rate limited and the run deadline passed", tool)
			case <-time.After(5 * time.Second):
			}
			continue
		}
		return content, err
	}
}

func (p *VenuePlanes) post(ctx context.Context, tool string, arguments any) (raw []byte, status int, retryAfter string, err error) {
	token, err := os.ReadFile(p.TokenFile)
	if err != nil {
		return nil, 0, "", fmt.Errorf("venue credential file is not readable")
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": arguments},
	})
	if err != nil {
		return nil, 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(body))
	if err != nil {
		return nil, 0, "", fmt.Errorf("venue request could not be built")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 150 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, "", fmt.Errorf("venue call %s failed in transport", tool)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, "", fmt.Errorf("venue call %s: answer could not be read", tool)
	}
	return raw, resp.StatusCode, resp.Header.Get("Retry-After"), nil
}

// venueStructuredContent reads the structured content of a tool answer.
// limited is true for a tool error that is the rate limit of the API.
func venueStructuredContent(tool string, raw []byte) (content json.RawMessage, limited bool, err error) {
	payload := raw
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "data: "); ok {
			payload = []byte(rest)
		}
	}
	var envelope struct {
		Result struct {
			StructuredContent json.RawMessage `json:"structuredContent"`
			IsError           bool            `json:"isError"`
			Content           []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, false, fmt.Errorf("venue call %s: answer is not JSON-RPC", tool)
	}
	if envelope.Error != nil {
		return nil, false, fmt.Errorf("venue call %s: JSON-RPC error %d", tool, envelope.Error.Code)
	}
	if len(envelope.Result.StructuredContent) == 0 {
		text := ""
		if len(envelope.Result.Content) > 0 {
			text = strings.ToLower(envelope.Result.Content[0].Text)
		}
		limited = envelope.Result.IsError && (strings.Contains(text, "rate") || strings.Contains(text, "429") || strings.Contains(text, "too many"))
		if len(text) > 120 {
			text = text[:120]
		}
		return nil, limited, fmt.Errorf("venue call %s: no structured content (tool error: %t): %s", tool, envelope.Result.IsError, text)
	}
	return envelope.Result.StructuredContent, false, nil
}

// GraphQL implements Planes.
func (p *VenuePlanes) GraphQL(ctx context.Context, shape Shape, variables map[string]any) (json.RawMessage, error) {
	query, sent, err := shape.Query(variables)
	if err != nil {
		return nil, err
	}
	arguments := map[string]any{"query": query}
	if len(sent) > 0 {
		arguments["variables"] = sent
	}
	return p.call(ctx, "graphql_query", arguments)
}

// Operation implements Planes.
func (p *VenuePlanes) Operation(ctx context.Context, shape Shape, variables map[string]any) (json.RawMessage, error) {
	arguments := map[string]any{"operation": shape.Operation}
	if len(variables) > 0 {
		arguments["variables"] = variables
	}
	return p.call(ctx, "run_operation", arguments)
}

// Facts implements Planes.
func (p *VenuePlanes) Facts(ctx context.Context, request FactsRequest) (json.RawMessage, error) {
	return p.call(ctx, "read_facts", request)
}
