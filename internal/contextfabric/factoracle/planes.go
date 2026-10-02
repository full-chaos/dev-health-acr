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
// answered with 500. It keeps the root field arguments (variables resolved)
// and the selection of the last request, so the caller can hold the request
// the runner sent against the case the reply was recorded for (boundRequest).
type ReplayListener struct {
	mu       sync.Mutex
	armed    *RecordedReply
	root     string
	orgID    string
	requests int
	failures []string

	lastArguments map[string]any
	lastSelection []string
}

// last returns the root field arguments and the selected leaf paths of the
// last request.
func (l *ReplayListener) last() (arguments map[string]any, selection []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastArguments, append([]string(nil), l.lastSelection...)
}

// selectionPaths lists the leaf fields of a selection as dotted paths.
func selectionPaths(prefix string, set ast.SelectionSet) []string {
	var out []string
	for _, selection := range set {
		field, ok := selection.(*ast.Field)
		if !ok {
			out = append(out, prefix+".(fragment)")
			continue
		}
		path := prefix + "." + field.Name
		if prefix == "" {
			path = field.Name
		}
		if len(field.SelectionSet) == 0 {
			out = append(out, path)
			continue
		}
		out = append(out, selectionPaths(path, field.SelectionSet)...)
	}
	return out
}

// flattenVariables lists the leaves of a variable value by path. A list of
// objects is walked item by item ("[*]" in the path, the item number in the
// key); any other list is one leaf.
func flattenVariables(prefix, key string, value any, out map[string]variableLeaf) {
	switch v := value.(type) {
	case map[string]any:
		for name, item := range v {
			path, itemKey := name, name
			if prefix != "" {
				path, itemKey = prefix+"."+name, key+"."+name
			}
			flattenVariables(path, itemKey, item, out)
		}
	case []any:
		objects := len(v) > 0
		for _, item := range v {
			if _, isObject := item.(map[string]any); !isObject {
				objects = false
			}
		}
		if !objects {
			out[key] = variableLeaf{Path: prefix, Value: value}
			return
		}
		for i, item := range v {
			flattenVariables(prefix+"[*]", fmt.Sprintf("%s[%d]", key, i), item, out)
		}
	default:
		out[key] = variableLeaf{Path: prefix, Value: value}
	}
}

type variableLeaf struct {
	// Path is the policy path of the leaf ("[*]" for a list item).
	Path  string
	Value any
}

// opsForm gives a client value the form acr sends to ops: a subject id loses
// its acr prefix where the policy says so.
func opsForm(rule directread.VariableRule, value any) any {
	if rule.Subject == nil || rule.Subject.AcrPrefix == "" {
		return value
	}
	switch v := value.(type) {
	case string:
		return strings.TrimPrefix(v, rule.Subject.AcrPrefix)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = opsForm(rule, item)
		}
		return out
	default:
		return value
	}
}

// boundRequest holds the request a runner sent to the listener against the
// case it was sent for: it selects exactly the leaves of the shape, every
// variable the client set arrives with the client's value, and a variable the
// client did not set is one acr itself sets (the caller's organization, or a
// forced value). A runner that drops, adds or changes a variable or a field
// would else be answered with the recorded reply of the case it was meant to
// send. arguments are the arguments of the root field the listener received,
// variables resolved: graphql_query rebuilds the query (it renames variables
// and may write a value into the query text), run_operation sends the
// registered document, and both must give the root field the same arguments.
//
// registered is true for run_operation: its request must select what the
// registered document selects (the document can select an output acr
// withholds from the answer; a client query cannot). For graphql_query the
// request must select the leaves of the shape, no more and no less.
func boundRequest(shape Shape, registered bool, client map[string]any, orgID string, arguments map[string]any, selection []string) []string {
	var problems []string
	// The arguments, renamed to the variables of the registered document: the
	// policy's variable rules and the client's variables use those names.
	upstream := map[string]any{}
	doc, err := parser.ParseQuery(&ast.Source{Input: shape.op.DocumentText})
	if err != nil || len(doc.Operations) != 1 || len(doc.Operations[0].SelectionSet) != 1 {
		return []string{"the registered document is not one root field"}
	}
	root, _ := doc.Operations[0].SelectionSet[0].(*ast.Field)
	if root == nil {
		return []string{"the registered document is not one root field"}
	}
	declared := map[string]bool{}
	for _, argument := range root.Arguments {
		declared[argument.Name] = true
		value, sent := arguments[argument.Name]
		if argument.Value.Kind == ast.Variable {
			if sent {
				upstream[argument.Value.Raw] = value
			}
			continue
		}
		// A value the registered document writes itself.
		literal, lerr := argument.Value.Value(nil)
		if lerr != nil || !sent || canonicalJSON(literal) != canonicalJSON(value) {
			problems = append(problems, "the argument "+argument.Name+" is not the value the registered document writes")
		}
	}
	for name := range arguments {
		if !declared[name] {
			problems = append(problems, "the request has the argument "+name+", which the registered document does not have")
		}
	}
	want := map[string]bool{}
	if registered {
		for _, path := range selectionPaths("", doc.Operations[0].SelectionSet) {
			want[path] = true
		}
	} else {
		for _, path := range shape.Paths {
			want[strings.ReplaceAll(path, "[*]", "")] = true
		}
	}
	got := map[string]bool{}
	for _, path := range selection {
		got[path] = true
	}
	for path := range want {
		if !got[path] {
			problems = append(problems, "the request does not select "+path)
		}
	}
	for path := range got {
		if !want[path] {
			problems = append(problems, "the request selects "+path+", which is not in the selection of the case")
		}
	}
	generic, err := decodeJSON([]byte(canonicalJSON(client)))
	if err != nil {
		return append(problems, "the client variables are not JSON")
	}
	sent, received := map[string]variableLeaf{}, map[string]variableLeaf{}
	flattenVariables("", "", generic, sent)
	flattenVariables("", "", upstream, received)
	for key, leaf := range sent {
		rule, ok := shape.op.Variable(leaf.Path)
		if !ok {
			problems = append(problems, "the client variable "+leaf.Path+" has no rule in the policy")
			continue
		}
		arrived, ok := received[key]
		if !ok {
			problems = append(problems, "the client variable "+key+" did not reach the listener")
			continue
		}
		if canonicalJSON(opsForm(rule, leaf.Value)) != canonicalJSON(arrived.Value) {
			problems = append(problems, "the client variable "+key+" reached the listener with another value")
		}
	}
	for key, leaf := range received {
		if _, set := sent[key]; set {
			continue
		}
		rule, ok := shape.op.Variable(leaf.Path)
		switch {
		case !ok:
			problems = append(problems, "the listener received the variable "+key+", which has no rule in the policy")
		case rule.Source == directread.SourceClient:
			problems = append(problems, "the listener received the client variable "+key+", which the client did not set")
		case rule.Source == directread.SourcePrincipalOrg && canonicalJSON(leaf.Value) != canonicalJSON(orgID):
			problems = append(problems, "the organization variable "+key+" is not the caller's organization")
		}
	}
	sort.Strings(problems)
	return problems
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
		Query     string          `json:"query"`
		Variables json.RawMessage `json:"variables"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || json.Unmarshal(raw, &body) != nil {
		l.fail(w, "request body is not a GraphQL request")
		return
	}
	l.lastArguments, l.lastSelection = nil, nil
	variables := map[string]any{}
	if len(body.Variables) > 0 && string(body.Variables) != "null" {
		decoded, verr := decodeJSON(body.Variables)
		object, isObject := decoded.(map[string]any)
		if verr != nil || !isObject {
			l.fail(w, "request variables are not a JSON object")
			return
		}
		variables = object
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
	arguments := map[string]any{}
	for _, argument := range field.Arguments {
		value, aerr := argument.Value.Value(variables)
		if aerr != nil {
			l.fail(w, "argument %s of root %s has no value", argument.Name, field.Name)
			return
		}
		arguments[argument.Name] = value
	}
	l.lastArguments = arguments
	l.lastSelection = selectionPaths("", doc.Operations[0].SelectionSet)
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
	upstream, selection := p.Listener.last()
	if problems := boundRequest(shape, false, variables, p.Principal.OrgID, upstream, selection); len(problems) > 0 {
		return nil, fmt.Errorf("case %s: the request graphql_query sent is not the request of the case: %s", key, strings.Join(problems, "; "))
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
	upstream, selection := p.Listener.last()
	if problems := boundRequest(shape, true, variables, p.Principal.OrgID, upstream, selection); len(problems) > 0 {
		return nil, fmt.Errorf("case %s: the request run_operation sent is not the request of the case: %s", key, strings.Join(problems, "; "))
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
		// The text of a tool error can quote a name, an address or a
		// credential of the venue: only its length is reported.
		return nil, limited, fmt.Errorf("venue call %s: no structured content (tool error: %t, rate limit: %t, %d characters of text withheld)", tool, envelope.Result.IsError, limited, len(text))
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
