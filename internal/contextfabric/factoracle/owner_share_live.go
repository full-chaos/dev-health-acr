package factoracle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/outboundhttp"
)

// The live plane of the owner-share pair (CHAOS-12691).
//
// The O4 live mode (VenuePlanes) reaches ops through the hosted acr MCP
// server, whose run_operation policy refuses the TEAM and REPO scopes of the
// investment operations (cmd/operationpolicy investmentDecl, K14-A) until
// this pair has passed. So this plane calls the ops PUBLIC GraphQL API
// directly, with a normal user login (lead ruling on CHAOS-12691). Only the
// o4venue-tagged TestVenueOwnerShareLive uses it: never a pull request gate.

// opsInvestmentBreakdownOperation names, per scope level, the ops
// registered document the plane sends: the operation acr's run_operation
// policy serves for that level. Ops resolves a document by the digest of its
// exact text and refuses any other text as unregistered, so the plane sends
// the registered text itself, read from the vendored catalogue
// (contracts/mcp/ops-catalogue/registry.v1.json), never a text of its own
// (CHAOS-12879). The pair reads only the breakdowns of the answer.
var opsInvestmentBreakdownOperation = map[string]string{
	ScopeLevelOrg:  "investmentBreakdown",
	ScopeLevelTeam: "investmentBreakdownTeam",
	ScopeLevelRepo: "investmentBreakdownRepository",
}

// opsInvestmentBreakdownDocument is the registered text of the scope level's
// document. directread.LoadCatalogue holds every text to its registry digest.
func opsInvestmentBreakdownDocument(level string) (string, error) {
	name, ok := opsInvestmentBreakdownOperation[level]
	if !ok {
		return "", fmt.Errorf("ops GraphQL: no registered document for scope level %q", level)
	}
	catalogue, err := directread.DefaultCatalogue()
	if err != nil {
		return "", fmt.Errorf("ops GraphQL: the vendored catalogue: %w", err)
	}
	op, refusal := catalogue.Lookup(name)
	if refusal != nil {
		return "", fmt.Errorf("ops GraphQL: the vendored catalogue has no %s: %s", name, refusal.Reason)
	}
	return op.DocumentText, nil
}

// The scope levels the pair reads (ops ScopeLevelInput).
const (
	ScopeLevelOrg  = "ORG"
	ScopeLevelTeam = "TEAM"
	ScopeLevelRepo = "REPO"
)

const (
	// opsBreakdownTopN is the topN of every breakdown. THEME has five keys,
	// so an answer with topN items may be cut and is not an answer.
	opsBreakdownTopN = 50
	// opsCallTimeout caps one HTTP call, a retry included as its own call.
	opsCallTimeout = 25 * time.Second
	// opsLoginPath and opsGraphQLPath are the ops public routes.
	opsLoginPath   = "/api/v1/auth/login"
	opsGraphQLPath = "/graphql"
)

// RuleOrgAnswerMissing: the organization has no ops ORG answer.
const RuleOrgAnswerMissing OwnerShareRule = "org_answer_missing"

// OpsGraphQLPlane calls the ops public GraphQL API as one user.
//
// The password is read from PasswordFile at login, in process, and is held
// only while the login request is built. It never reaches an argument
// vector, a log line, an error or a report. The access token is held in
// memory; it is sent only as the bearer of the GraphQL calls and is never
// logged. A redirect is never followed: a redirected login would send the
// password again, a redirected call the bearer.
type OpsGraphQLPlane struct {
	// BaseURL is the ops API base, for example http://api.localhost:3000.
	BaseURL string
	// Email is the login of the user.
	Email string
	// PasswordFile holds the password. It must be readable by its owner
	// only (0600).
	PasswordFile string
	// OrgID is the organization of the login and of every call.
	OrgID string
	// DialAddr, when set, is the host:port every connection dials in place
	// of the host of BaseURL; the request keeps the host of BaseURL. It is
	// for a name the Go resolver does not resolve (api.localhost on macOS).
	DialAddr string
	// MinInterval is the least time between two calls, retries and the
	// login included. Zero is no pacing.
	MinInterval time.Duration
	// Logf receives every Retry-After wait and nothing else: never a body,
	// a credential, an email or an address.
	Logf func(format string, args ...any)

	mu         sync.Mutex
	httpClient *http.Client
	token      string
	last       time.Time
	calls      int
	limited    int
	waited     time.Duration
}

// Stats is the number of GraphQL calls, of rate-limit answers and the time
// spent waiting on them.
func (p *OpsGraphQLPlane) Stats() (calls, limited int, waited time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.limited, p.waited
}

func (p *OpsGraphQLPlane) client() *http.Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.httpClient != nil {
		return p.httpClient
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	transport := outboundhttp.Transport()
	if p.DialAddr != "" {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		address := p.DialAddr
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		}
	}
	p.httpClient = &http.Client{Transport: transport, Timeout: opsCallTimeout, CheckRedirect: noRedirect}
	return p.httpClient
}

// pace holds a call until MinInterval has passed since the last one.
func (p *OpsGraphQLPlane) pace(ctx context.Context) error {
	if p.MinInterval <= 0 {
		return nil
	}
	p.mu.Lock()
	next := p.last.Add(p.MinInterval)
	if now := time.Now(); next.Before(now) {
		next = now
	}
	p.last = next
	p.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Until(next)):
		return nil
	}
}

func (p *OpsGraphQLPlane) noteLimited(what string, attempt int, wait time.Duration) {
	p.mu.Lock()
	p.limited++
	p.waited += wait
	count := p.limited
	p.mu.Unlock()
	if p.Logf != nil {
		p.Logf("ops %s rate limited (attempt %d, 429 count %d): waiting %s", what, attempt+1, count, wait)
	}
}

// transportReason names a transport failure without its text: the text of a
// url.Error quotes the address.
func transportReason(err error) string {
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the call timed out"
	case errors.As(err, &dns):
		return "the host name does not resolve (ACR_O4_OPS_DIAL_ADDR dials an address in its place)"
	case errors.As(err, &op) && op.Op == "dial":
		return "the dial failed"
	default:
		return "a transport error"
	}
}

// post sends one request. Its errors name no body, credential or address.
func (p *OpsGraphQLPlane) post(ctx context.Context, what, path string, body []byte, token string) (raw []byte, status int, retryAfter string, err error) {
	if err := p.pace(ctx); err != nil {
		return nil, 0, "", fmt.Errorf("ops %s: the run deadline passed while pacing", what)
	}
	callCtx, cancel := context.WithTimeout(ctx, opsCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, "", fmt.Errorf("ops %s: the request could not be built (is the API URL a URL?)", what)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, 0, "", fmt.Errorf("ops %s failed in transport: %s", what, transportReason(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, "", fmt.Errorf("ops %s: the answer could not be read", what)
	}
	return raw, resp.StatusCode, resp.Header.Get("Retry-After"), nil
}

// do sends one request and waits out a rate limit: a 429 waits the time the
// server names in Retry-After (5 s when it names none, 60 s at most) and
// tries again, at most venueRetries times. The limit is never worked around.
func (p *OpsGraphQLPlane) do(ctx context.Context, what, path string, body []byte, token string) ([]byte, int, error) {
	for attempt := 0; ; attempt++ {
		raw, status, retryAfter, err := p.post(ctx, what, path, body, token)
		if err != nil {
			return nil, 0, err
		}
		if status != http.StatusTooManyRequests || attempt >= venueRetries {
			return raw, status, nil
		}
		wait := 5 * time.Second
		if seconds, perr := strconv.Atoi(strings.TrimSpace(retryAfter)); perr == nil && seconds > 0 {
			wait = time.Duration(min(seconds, 60)) * time.Second
		}
		p.noteLimited(what, attempt, wait)
		select {
		case <-ctx.Done():
			return nil, 0, fmt.Errorf("ops %s: rate limited and the run deadline passed", what)
		case <-time.After(wait):
		}
	}
}

// readOwnerOnlyFile reads a secret file that only its owner may read.
func readOwnerOnlyFile(path, name string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("the %s file is not readable", name)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("the %s file is readable by group or others; make it 0600", name)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("the %s file is not readable", name)
	}
	value := strings.TrimRight(string(raw), "\r\n")
	if value == "" {
		return "", fmt.Errorf("the %s file is empty", name)
	}
	return value, nil
}

// Login signs the user in and keeps the access token. Its errors name the
// HTTP status at most: a login answer can echo what was sent.
func (p *OpsGraphQLPlane) Login(ctx context.Context) error {
	if p.BaseURL == "" || p.Email == "" || p.PasswordFile == "" || p.OrgID == "" {
		return errors.New("ops login: the API URL, the email, the password file and the organization are all required")
	}
	body, err := func() ([]byte, error) {
		password, err := readOwnerOnlyFile(p.PasswordFile, "ops password")
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(map[string]string{"email": p.Email, "password": password, "org_id": p.OrgID})
		if err != nil {
			return nil, errors.New("ops login: the request could not be encoded")
		}
		return encoded, nil
	}()
	if err != nil {
		return err
	}
	raw, status, err := p.do(ctx, "login", opsLoginPath, body, "")
	clear(body)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("ops login: HTTP %d", status)
	}
	var answer struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(raw, &answer) != nil || strings.TrimSpace(answer.AccessToken) == "" {
		return errors.New("ops login: the answer has no access_token")
	}
	p.mu.Lock()
	p.token = answer.AccessToken
	p.mu.Unlock()
	return nil
}

// ownerShareVariables are the variables of one call: the THEME breakdown
// under measure over the window, the investment source on, and the scope.
// ORG sends no ids; TEAM and REPO send the one id.
func ownerShareVariables(orgID, measure string, w Window, level, id string) map[string]any {
	scope := map[string]any{"level": level}
	if level != ScopeLevelOrg {
		scope["ids"] = []any{id}
	}
	return map[string]any{
		"orgId": orgID,
		"batch": map[string]any{
			"breakdowns": []any{map[string]any{
				"dimension": "THEME", "measure": measure,
				"dateRange": map[string]any{"startDate": w.startDate(), "endDate": w.endDate()},
				"topN":      opsBreakdownTopN,
			}},
			"useInvestment": true,
			"filters":       map[string]any{"scope": scope},
		},
	}
}

// graphQLErrorSummary lists the errors of an answer: the code and the
// message, the token removed, each cut to 160 characters.
func graphQLErrorSummary(errs []map[string]any, token string) string {
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		code := "no code"
		if extensions, ok := e["extensions"].(map[string]any); ok {
			if c, ok := extensions["code"].(string); ok && c != "" {
				code = c
			}
		}
		message, _ := e["message"].(string)
		if token != "" {
			message = strings.ReplaceAll(message, token, "[token]")
		}
		if r := []rune(message); len(r) > 160 {
			message = string(r[:160]) + "..."
		}
		parts = append(parts, "["+code+"] "+message)
	}
	return strings.Join(parts, "; ")
}

// InvestmentBreakdown runs the THEME breakdown of one scope under one
// measure and returns theme -> value. An error is why there is no answer:
// an HTTP status, the GraphQL errors, or an answer that is not one THEME
// breakdown under the measure. A GraphQL error with partial data is no
// answer.
func (p *OpsGraphQLPlane) InvestmentBreakdown(ctx context.Context, measure string, w Window, level, id string) (map[string]float64, error) {
	document, err := opsInvestmentBreakdownDocument(level)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	token := p.token
	p.calls++
	p.mu.Unlock()
	if token == "" {
		return nil, errors.New("ops GraphQL: not logged in")
	}
	body, err := json.Marshal(map[string]any{"query": document, "variables": ownerShareVariables(p.OrgID, measure, w, level, id)})
	if err != nil {
		return nil, errors.New("ops GraphQL: the request could not be encoded")
	}
	raw, status, err := p.do(ctx, "GraphQL", opsGraphQLPath, body, token)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("ops GraphQL: HTTP %d", status)
	}
	var envelope struct {
		Data *struct {
			Analytics *struct {
				Breakdowns []struct {
					Dimension string `json:"dimension"`
					Measure   string `json:"measure"`
					Items     []struct {
						Key   string `json:"key"`
						Value any    `json:"value"`
					} `json:"items"`
				} `json:"breakdowns"`
			} `json:"analytics"`
		} `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	if err := decodeNumbered(raw, &envelope); err != nil {
		return nil, errors.New("ops GraphQL: the answer is not a GraphQL answer")
	}
	if len(envelope.Errors) > 0 {
		return nil, fmt.Errorf("ops GraphQL: %d errors: %s", len(envelope.Errors), graphQLErrorSummary(envelope.Errors, token))
	}
	if envelope.Data == nil || envelope.Data.Analytics == nil {
		return nil, errors.New("ops GraphQL: the answer has no analytics")
	}
	breakdowns := envelope.Data.Analytics.Breakdowns
	if len(breakdowns) != 1 || breakdowns[0].Dimension != "THEME" || breakdowns[0].Measure != measure {
		return nil, fmt.Errorf("ops GraphQL: echo: the answer is not one THEME breakdown under %s", measure)
	}
	if len(breakdowns[0].Items) >= opsBreakdownTopN {
		return nil, fmt.Errorf("ops GraphQL: the answer holds %d items, the topN; it may be cut", len(breakdowns[0].Items))
	}
	out := map[string]float64{}
	for _, item := range breakdowns[0].Items {
		if _, seen := out[item.Key]; seen {
			return nil, fmt.Errorf("ops GraphQL: the key %s is in the answer twice", item.Key)
		}
		leaf, lerr := typedLeaf(LeafFloat, item.Value)
		if lerr != nil || leaf.T == LeafNull {
			return nil, fmt.Errorf("ops GraphQL: the value of %s is not a number", item.Key)
		}
		out[item.Key], _ = leafFloat(leaf)
	}
	return out, nil
}

// OwnerShareLiveReport is the verdict of one live run of the owner-share
// pair. Its findings use the report form of the oracle (Finding).
type OwnerShareLiveReport struct {
	Pair   string `json:"pair"`
	Window Window `json:"window"`
	// OwnershipAsOf is the instant the store reading of the owners is
	// taken at, the start of the ops calls: ops reads ownership as of its
	// own now(). OwnershipCheckedAt is the end of the calls; the owners at
	// both instants are the same, or the run is refused.
	OwnershipAsOf      time.Time `json:"ownership_as_of"`
	OwnershipCheckedAt time.Time `json:"ownership_checked_at"`
	// OwnershipSource names where the store rows were read
	// (OwnershipSourceClickHouse, OwnershipSourceExtractFile);
	// OwnershipExtract records the extract file when it was the source.
	OwnershipSource  string                  `json:"ownership_source,omitempty"`
	OwnershipExtract *OwnershipExtractRecord `json:"ownership_extract,omitempty"`
	// Teams counts the teams asked (the owning teams and the active ones),
	// Repositories the repositories asked.
	Teams             int `json:"teams"`
	OwningTeams       int `json:"owning_teams"`
	Repositories      int `json:"repositories"`
	OwnedRepositories int `json:"owned_repositories"`
	// Calls counts the GraphQL calls; RateLimited the 429 answers and
	// RateLimitWait the time spent on them.
	Calls         int                       `json:"calls"`
	RateLimited   int                       `json:"rate_limited"`
	RateLimitWait string                    `json:"rate_limit_wait"`
	Measures      []OwnerShareMeasureReport `json:"measures"`
}

// OwnerShareMeasureReport is the verdict of one measure.
type OwnerShareMeasureReport struct {
	Measure  string    `json:"measure"`
	Compared int       `json:"compared"`
	Matches  int       `json:"matches"`
	Findings []Finding `json:"findings"`
	// Notes name what the run could not measure; they are not findings.
	Notes []string `json:"notes,omitempty"`
	// OrgResidual is the ops ORG answer minus the sum of the ops REPO
	// answers, per theme: effort that reaches no repository, or that ops
	// counts in more than one. It is recorded, not compared.
	OrgResidual map[string]float64 `json:"org_residual,omitempty"`
}

// Findings lists every finding of the run.
func (r *OwnerShareLiveReport) Findings() []Finding {
	var out []Finding
	for _, m := range r.Measures {
		out = append(out, m.Findings...)
	}
	return out
}

// Table renders the per-measure outcome as a Markdown table.
func (r *OwnerShareLiveReport) Table() string {
	var b strings.Builder
	fmt.Fprintf(&b, "owner-share pair: window %s to %s, owners as of %s (checked at %s), %d teams (%d owning), %d repositories (%d owned), %d calls, %d rate limited (%s)\n",
		r.Window.startDate(), r.Window.endDate(), r.OwnershipAsOf.Format(time.RFC3339), r.OwnershipCheckedAt.Format(time.RFC3339),
		r.Teams, r.OwningTeams, r.Repositories, r.OwnedRepositories, r.Calls, r.RateLimited, r.RateLimitWait)
	if e := r.OwnershipExtract; e != nil {
		fmt.Fprintf(&b, "owners read once from the extract snapshot taken at %s (%s old at the last call, bound %s), sha256 %s, rows: repos %d, teams %d, team_repo_ownership %d\n",
			e.SnapshotAt.Format(time.RFC3339), e.AgeAtLastCall, e.MaxAge, e.SHA256, e.Rows[tableRepos], e.Rows[tableTeams], e.Rows[tableTeamRepoOwnership])
	}
	b.WriteString("| measure | compared | matches | findings | notes |\n|---|---|---|---|---|\n")
	for _, m := range r.Measures {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d |\n", m.Measure, m.Compared, m.Matches, len(m.Findings), len(m.Notes))
	}
	return b.String()
}

// Details lists the findings, the notes and the ORG residual.
func (r *OwnerShareLiveReport) Details() string {
	var b strings.Builder
	for _, m := range r.Measures {
		for _, f := range m.Findings {
			fmt.Fprintf(&b, "FINDING %s %s [%s] %s: %s\n", f.Root, f.Pair, f.Key, f.Path, f.Detail)
		}
		for _, note := range m.Notes {
			fmt.Fprintf(&b, "NOTE %s: %s\n", m.Measure, note)
		}
		if len(m.OrgResidual) > 0 {
			fmt.Fprintf(&b, "ORG-RESIDUAL %s (ops ORG minus the sum of the ops REPO answers): %s\n", m.Measure, formatThemes(m.OrgResidual))
		}
	}
	return b.String()
}

// liveFinding is an owner-share finding in the report form, keyed by its
// measure.
func liveFinding(measure string, f OwnerShareFinding) Finding {
	out := f.Finding()
	out.Root = "analytics"
	out.Key = strings.TrimSpace(measure + " " + out.Key)
	return out
}

// scopeAnswers are the ops answers of one measure.
type scopeAnswers struct {
	org              map[string]float64
	orgErr           error
	team, repo       map[string]map[string]float64
	teamErr, repoErr map[string]error
}

// runOwnerShare is the live owner-share pair over one store reading: one
// ORG, one TEAM per team and one REPO per repository call for every
// measure, then CompareOwnerShares. The owners are read as of the start of
// the calls and again at their end; a run whose owners changed in between is
// refused. A failed call is a finding, never a pass; a measure that compares
// nothing is not a measurement and fails the run.
func runOwnerShare(ctx context.Context, plane *OpsGraphQLPlane, store *Store, w Window, measures []string, now func() time.Time) (*OwnerShareLiveReport, error) {
	if err := w.Validate(); err != nil {
		return nil, err
	}
	if len(measures) == 0 {
		return nil, errors.New("the owner-share pair has no measure")
	}
	for _, measure := range measures {
		if !ownerShareMeasures[measure] {
			return nil, fmt.Errorf("measure %q is refused: the owner-share pair compares additive measures only (COUNT, ISSUE_SIZE)", measure)
		}
	}
	asOf := now().UTC()
	reading, err := store.OwnerShares(asOf)
	if err != nil {
		return nil, err
	}
	owning := reading.OwningTeams()
	teamSet := map[string]bool{}
	for _, team := range append(append([]string(nil), owning...), store.TeamIDs()...) {
		teamSet[team] = true
	}
	repoSet := map[string]bool{}
	for _, repo := range store.RepositoryIDs() {
		repoSet[repo] = true
	}
	for repo := range reading.Owners {
		repoSet[repo] = true
	}
	teams, repos := sortedKeys(teamSet), sortedKeys(repoSet)
	planned := len(measures) * (1 + len(teams) + len(repos))
	if deadline, ok := ctx.Deadline(); ok && plane.MinInterval > 0 {
		need := time.Duration(planned+1) * plane.MinInterval
		if left := time.Until(deadline); left < need {
			return nil, fmt.Errorf("the run needs %d ops calls at one per %s (%s); the deadline leaves %s", planned+1, plane.MinInterval, need, left.Round(time.Second))
		}
	}
	if err := plane.Login(ctx); err != nil {
		return nil, err
	}

	byMeasure := map[string]*scopeAnswers{}
	for _, measure := range measures {
		a := &scopeAnswers{team: map[string]map[string]float64{}, repo: map[string]map[string]float64{},
			teamErr: map[string]error{}, repoErr: map[string]error{}}
		a.org, a.orgErr = plane.InvestmentBreakdown(ctx, measure, w, ScopeLevelOrg, "")
		for _, team := range teams {
			if values, err := plane.InvestmentBreakdown(ctx, measure, w, ScopeLevelTeam, team); err != nil {
				a.teamErr[team] = err
			} else {
				a.team[team] = values
			}
		}
		for _, repo := range repos {
			if values, err := plane.InvestmentBreakdown(ctx, measure, w, ScopeLevelRepo, repo); err != nil {
				a.repoErr[repo] = err
			} else {
				a.repo[repo] = values
			}
		}
		byMeasure[measure] = a
	}
	if ctx.Err() != nil {
		return nil, errors.New("the run deadline passed during the ops calls; no verdict is reported")
	}
	checkedAt := now().UTC()
	after, err := store.OwnerShares(checkedAt)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(reading.Owners, after.Owners) || !reflect.DeepEqual(reading.Clamped, after.Clamped) {
		return nil, fmt.Errorf("the owners changed between the first and the last ops call (%s to %s): an ownership row started or ended during the run; no verdict is reported, run again",
			asOf.Format(time.RFC3339), checkedAt.Format(time.RFC3339))
	}

	calls, limited, waited := plane.Stats()
	report := &OwnerShareLiveReport{Pair: pairOwnerShare, Window: w, OwnershipAsOf: asOf, OwnershipCheckedAt: checkedAt,
		Teams: len(teams), OwningTeams: len(owning), Repositories: len(repos), OwnedRepositories: len(reading.Owners),
		Calls: calls, RateLimited: limited, RateLimitWait: waited.String()}
	for _, measure := range measures {
		a := byMeasure[measure]
		verdict, err := CompareOwnerShares(OwnerShareInput{Measure: measure, Team: a.team, Repo: a.repo, Shares: reading, Repositories: repos})
		if err != nil {
			return nil, err
		}
		mr := OwnerShareMeasureReport{Measure: measure, Compared: verdict.Compared, Matches: verdict.Matches, Notes: verdict.Notes}
		reported := map[string]bool{}
		for _, f := range verdict.Findings {
			switch f.Rule {
			case RuleTeamAnswerMissing:
				reported["team\x00"+f.Team] = true
				if err := a.teamErr[f.Team]; err != nil {
					f.Detail += ": " + err.Error()
				}
			case RuleRepoAnswerMissing:
				reported["repo\x00"+f.Repo] = true
				if err := a.repoErr[f.Repo]; err != nil {
					f.Detail += ": " + err.Error()
				}
			}
			mr.Findings = append(mr.Findings, liveFinding(measure, f))
		}
		// A team that owns nothing is asked too (its answer must be empty);
		// the comparator does not know it was asked.
		for _, team := range mapKeys(a.teamErr) {
			if !reported["team\x00"+team] {
				mr.Compared++
				mr.Findings = append(mr.Findings, liveFinding(measure, OwnerShareFinding{Rule: RuleTeamAnswerMissing, Team: team,
					Detail: "the team has no ops TEAM answer; it is not a zero: " + a.teamErr[team].Error()}))
			}
		}
		for _, repo := range mapKeys(a.repoErr) {
			if !reported["repo\x00"+repo] {
				mr.Compared++
				mr.Findings = append(mr.Findings, liveFinding(measure, OwnerShareFinding{Rule: RuleRepoAnswerMissing, Repo: repo,
					Detail: "the repository has no ops REPO answer; it is not a zero: " + a.repoErr[repo].Error()}))
			}
		}
		if a.orgErr != nil {
			mr.Compared++
			mr.Findings = append(mr.Findings, liveFinding(measure, OwnerShareFinding{Rule: RuleOrgAnswerMissing,
				Detail: "the organization has no ops ORG answer; it is not a zero: " + a.orgErr.Error()}))
		} else if len(a.repoErr) == 0 {
			sum := map[string]float64{}
			for _, values := range a.repo {
				for key, value := range values {
					sum[key] += value
				}
			}
			mr.OrgResidual = map[string]float64{}
			for _, key := range themeKeys(a.org, sum) {
				mr.OrgResidual[key] = a.org[key] - sum[key]
			}
		}
		if mr.Compared == 0 {
			return nil, fmt.Errorf("measure %s compared nothing: no answer of the run holds a value; the run is not a measurement", measure)
		}
		report.Measures = append(report.Measures, mr)
	}
	return report, nil
}

// OwnerShareLiveConfig names the live run of the owner-share pair.
type OwnerShareLiveConfig struct {
	// Ops is the ops plane; its OrgID is the venue organization.
	Ops *OpsGraphQLPlane
	// ClickHouseDSNFile and ClickHouseAddr name the venue store the owners
	// are read from (VenueConfig).
	ClickHouseDSNFile string
	ClickHouseAddr    string
	// OwnershipExtract, in place of ClickHouseDSNFile, names the operator's
	// ownership extract file (ACR_O4_OWNERSHIP_EXTRACT, the output of
	// OwnershipExtractSQLFile): the owners are read from it and the venue
	// ClickHouse is not opened. OwnershipExtractMaxAge bounds the age of its
	// snapshot; zero is DefaultOwnershipExtractMaxAge.
	OwnershipExtract       string
	OwnershipExtractMaxAge time.Duration
	OrgID                  string
	Window                 Window
	// Measures default to COUNT and ISSUE_SIZE.
	Measures []string
}

// RunOwnerShareLive runs the owner-share pair on the venue: the ops answers
// through the ops public GraphQL API, the owners read from one of two
// sources. With ClickHouseDSNFile they are read with SELECT statements from
// the venue ClickHouse; the store must not change while the run reads it: the
// extract is read before and after the calls, and a run over a store that
// moved is refused (the error names the tables). With OwnershipExtract they
// are read once from the operator's extract file (runOwnerShareFromExtract).
func RunOwnerShareLive(ctx context.Context, cfg OwnerShareLiveConfig) (*OwnerShareLiveReport, error) {
	return runOwnerShareLive(ctx, cfg, time.Now)
}

func runOwnerShareLive(ctx context.Context, cfg OwnerShareLiveConfig, now func() time.Time) (*OwnerShareLiveReport, error) {
	if cfg.Ops == nil || cfg.OrgID == "" {
		return nil, errors.New("owner-share venue configuration is incomplete")
	}
	switch {
	case cfg.ClickHouseDSNFile != "" && cfg.OwnershipExtract != "":
		return nil, errors.New("name one source of the owners: the ownership extract file (ACR_O4_OWNERSHIP_EXTRACT) or the venue ClickHouse (ACR_O4_CLICKHOUSE_DSN_FILE), not both")
	case cfg.ClickHouseDSNFile == "" && cfg.OwnershipExtract == "":
		return nil, errors.New("owner-share venue configuration is incomplete: name the ownership extract file (ACR_O4_OWNERSHIP_EXTRACT) or the venue ClickHouse (ACR_O4_CLICKHOUSE_DSN_FILE)")
	}
	if cfg.Ops.OrgID != cfg.OrgID {
		return nil, errors.New("the ops plane and the store name different organizations")
	}
	measures := cfg.Measures
	if len(measures) == 0 {
		measures = mapKeys(ownerShareMeasures)
	}
	if cfg.OwnershipExtract != "" {
		return runOwnerShareFromExtract(ctx, cfg, measures, now)
	}
	conn, err := openVenueClickHouse(VenueConfig{ClickHouseDSNFile: cfg.ClickHouseDSNFile, ClickHouseAddr: cfg.ClickHouseAddr})
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	db := clickHouseQuerier{conn: conn}
	before, err := CaptureExtract(ctx, db, cfg.OrgID, cfg.Window)
	if err != nil {
		return nil, err
	}
	store, err := NewStore(before)
	if err != nil {
		return nil, err
	}
	report, err := runOwnerShare(ctx, cfg.Ops, store, cfg.Window, measures, now)
	if err != nil {
		return nil, err
	}
	after, err := CaptureExtract(ctx, db, cfg.OrgID, cfg.Window)
	if err != nil {
		return nil, err
	}
	if moved := movedTables(before, after); len(moved) > 0 {
		sort.Strings(moved)
		return nil, fmt.Errorf("the venue store changed during the run (%s); no verdict is reported: run again when no job writes these tables", strings.Join(moved, ", "))
	}
	report.OwnershipSource = OwnershipSourceClickHouse
	return report, nil
}
