package factoracle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/proxycapture"
)

// The live plane of the owner-share pair (CHAOS-12691) against a fake ops
// API on an httptest server: no venue, no container. The store is the
// hand-seeded one of owner_share_pair_test.go: repository A owned by X (0.7)
// and Y (0.3), B by X alone, C by nobody, Z an active team that owns
// nothing.

const (
	osLivePassword = "pw-Secret-9f3e"
	osLiveToken    = "tok-Secret-7c1a"
	osLiveEmail    = "oracle@example.test"
)

var osLiveWindow = Window{Start: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}

type opsFakeCall struct {
	at                 time.Time
	measure, level, id string
	variables          string
	// operation is the ops registry row the query text resolved to, by its
	// digest; legacy is that row's legacy mark.
	operation string
	legacy    bool
}

// opsRegisteredRow is one row of the vendored ops registry.
type opsRegisteredRow struct {
	operation string
	legacy    bool
}

// opsRegistryByDigest reads the vendored ops registry the way ops resolves a
// document (CHAOS-12879): by the hex sha256 of the trimmed text. It reads
// the file itself, not directread's catalogue, so the fake does not hold
// the plane's own lookup against itself.
var opsRegistryByDigest = sync.OnceValues(func() (map[string]opsRegisteredRow, error) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "mcp", "ops-catalogue", "registry.v1.json"))
	if err != nil {
		return nil, err
	}
	var file struct {
		Rows []struct {
			Operation string `json:"operation"`
			Document  string `json:"document"`
			Digest    string `json:"digest"`
			Legacy    bool   `json:"legacy"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}
	out := map[string]opsRegisteredRow{}
	for _, row := range file.Rows {
		if opsDocumentDigest(row.Document) != row.Digest {
			return nil, fmt.Errorf("registry row %s: the digest is not the text's", row.Operation)
		}
		out[row.Digest] = opsRegisteredRow{operation: row.Operation, legacy: row.Legacy}
	}
	return out, nil
})

func opsDocumentDigest(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])
}

// opsFake is the ops login and GraphQL routes. answer, when set, may
// replace the answer of a call (ok false keeps the consistent one).
type opsFake struct {
	mu          sync.Mutex
	loginStatus int
	limitOnce   bool
	logins      []time.Time
	calls       []opsFakeCall
	problems    []string
	answer      func(measure, level, id string) (status int, body any, ok bool)
}

func (f *opsFake) problem(format string, args ...any) {
	f.problems = append(f.problems, fmt.Sprintf(format, args...))
}

// osLiveValues are answers consistent with the shares: TEAM is the
// share-weighted sum of REPO, ORG holds 2 more than the repositories.
func osLiveValues(level, id string) map[string]float64 {
	switch level {
	case "ORG":
		out := map[string]float64{"feature_delivery": 2}
		for _, values := range osRepoAnswers {
			for key, value := range values {
				out[key] += value
			}
		}
		return out
	case "TEAM":
		switch id {
		case osTeamX:
			return osWeighted(map[string]float64{osRepoA: 0.7, osRepoB: 1})
		case osTeamY:
			return osWeighted(map[string]float64{osRepoA: 0.3})
		}
		return map[string]float64{}
	default:
		return osRepoAnswers[id]
	}
}

func osBreakdown(measure string, values map[string]float64) map[string]any {
	items := []any{}
	for _, key := range mapKeys(values) {
		items = append(items, map[string]any{"key": key, "value": values[key]})
	}
	return map[string]any{"data": map[string]any{"analytics": map[string]any{"breakdowns": []any{
		map[string]any{"dimension": "THEME", "measure": measure, "items": items}}}}}
}

func (f *opsFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
		f.logins = append(f.logins, time.Now())
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 3 ||
			body["email"] != osLiveEmail || body["password"] != osLivePassword || body["org_id"] != FixtureOrgID {
			f.problem("the login body is not {email, password, org_id} of the run")
		}
		if r.Header.Get("Authorization") != "" {
			f.problem("the login carries an Authorization header")
		}
		if f.loginStatus != 0 {
			// A hostile answer that echoes what was sent.
			w.WriteHeader(f.loginStatus)
			_ = json.NewEncoder(w).Encode(map[string]any{"detail": "bad password " + osLivePassword + " for " + osLiveEmail})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": osLiveToken, "refresh_token": "refresh-Secret", "token_type": "bearer", "expires_in": 3600})
	case r.Method == http.MethodPost && r.URL.Path == "/graphql":
		if r.Header.Get("Authorization") != "Bearer "+osLiveToken {
			f.problem("a GraphQL call does not carry the login's bearer")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.limitOnce {
			f.limitOnce = false
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.problem("a GraphQL body is not JSON")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Ops resolves the document by the digest of its text and refuses a
		// text it does not register (query-api operationForDocument,
		// refuseGraphQLEdgeUnregistered).
		registry, err := opsRegistryByDigest()
		if err != nil {
			f.problem("the vendored ops registry: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		row, registered := registry[opsDocumentDigest(body.Query)]
		if !registered {
			f.problem("a GraphQL query is not a registered ops document")
			http.NotFound(w, r)
			return
		}
		call := opsFakeCall{at: time.Now(), variables: canonicalJSON(body.Variables), operation: row.operation, legacy: row.legacy}
		if batch, ok := body.Variables["batch"].(map[string]any); ok {
			if breakdowns, ok := batch["breakdowns"].([]any); ok && len(breakdowns) == 1 {
				call.measure, _ = breakdowns[0].(map[string]any)["measure"].(string)
			}
			if filters, ok := batch["filters"].(map[string]any); ok {
				if scope, ok := filters["scope"].(map[string]any); ok {
					call.level, _ = scope["level"].(string)
					if ids, ok := scope["ids"].([]any); ok && len(ids) == 1 {
						call.id, _ = ids[0].(string)
					}
				}
			}
		}
		f.calls = append(f.calls, call)
		if f.answer != nil {
			if status, answer, ok := f.answer(call.measure, call.level, call.id); ok {
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(answer)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(osBreakdown(call.measure, osLiveValues(call.level, call.id)))
	default:
		f.problem("request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func osPasswordFile(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ops-password")
	if err := os.WriteFile(path, []byte(osLivePassword+"\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

type osLiveRig struct {
	fake  *opsFake
	plane *OpsGraphQLPlane
	logs  *[]string
}

func newOSLiveRig(t *testing.T, fake *opsFake) osLiveRig {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	logs := &[]string{}
	var mu sync.Mutex
	plane := &OpsGraphQLPlane{BaseURL: server.URL, Email: osLiveEmail, PasswordFile: osPasswordFile(t, 0o600), OrgID: FixtureOrgID,
		Logf: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			*logs = append(*logs, fmt.Sprintf(format, args...))
		}}
	return osLiveRig{fake: fake, plane: plane, logs: logs}
}

func osFixedNow(instants ...time.Time) func() time.Time {
	i := 0
	return func() time.Time {
		at := instants[min(i, len(instants)-1)]
		i++
		return at
	}
}

func osLiveRun(t *testing.T, rig osLiveRig, rows []Row) (*OwnerShareLiveReport, error) {
	t.Helper()
	store, err := NewStore(osExtract(rows...))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return runOwnerShare(ctx, rig.plane, store, osLiveWindow, []string{"COUNT", "ISSUE_SIZE"}, osFixedNow(osAsOf))
}

// osExpectedVariables is written out here, not built by ownerShareVariables,
// so the test does not hold the function against itself.
func osExpectedVariables(measure, level, id string) string {
	scope := map[string]any{"level": level}
	if id != "" {
		scope["ids"] = []any{id}
	}
	return canonicalJSON(map[string]any{"orgId": FixtureOrgID, "batch": map[string]any{
		"breakdowns": []any{map[string]any{"dimension": "THEME", "measure": measure,
			"dateRange": map[string]any{"startDate": "2026-09-01", "endDate": "2026-10-01"}, "topN": 50}},
		"useInvestment": true,
		"filters":       map[string]any{"scope": scope},
	}})
}

// osExpectedOperation is the ops registered document of each scope level:
// the operation acr's run_operation policy serves for that level
// (CHAOS-12879).
var osExpectedOperation = map[string]string{"ORG": "investmentBreakdown", "TEAM": "investmentBreakdownTeam", "REPO": "investmentBreakdownRepository"}

func TestOwnerShareLiveSendsOneCallPerScopeWithTheLoginBearer(t *testing.T) {
	rig := newOSLiveRig(t, &opsFake{})
	report, err := osLiveRun(t, rig, osReference())
	if err != nil {
		t.Fatal(err)
	}
	if len(rig.fake.problems) != 0 {
		t.Fatalf("the fake ops saw: %v", rig.fake.problems)
	}
	if len(report.Findings()) != 0 {
		t.Fatalf("control run has findings: %+v", report.Findings())
	}
	if len(rig.fake.logins) != 1 || len(rig.fake.calls) == 0 || rig.fake.calls[0].at.Before(rig.fake.logins[0]) {
		t.Fatalf("%d logins, the first call before the login: want one login before every call", len(rig.fake.logins))
	}
	want := map[string]bool{}
	for _, measure := range []string{"COUNT", "ISSUE_SIZE"} {
		want[measure+" ORG "] = true
		for _, team := range []string{osTeamX, osTeamY, osTeamZ} {
			want[measure+" TEAM "+team] = true
		}
		for _, repo := range []string{osRepoA, osRepoB, osRepoC} {
			want[measure+" REPO "+repo] = true
		}
	}
	got := map[string]bool{}
	for _, call := range rig.fake.calls {
		key := call.measure + " " + call.level + " " + call.id
		if got[key] {
			t.Fatalf("scope %s was asked twice", key)
		}
		got[key] = true
		if want := osExpectedOperation[call.level]; call.operation != want || call.legacy {
			t.Fatalf("scope %s sent the registered document %q (legacy %v), want the current %q", key, call.operation, call.legacy, want)
		}
		if call.variables != osExpectedVariables(call.measure, call.level, call.id) {
			t.Fatalf("scope %s sent %s, want %s", key, call.variables, osExpectedVariables(call.measure, call.level, call.id))
		}
	}
	if strings.Join(sortedKeys(got), "|") != strings.Join(sortedKeys(want), "|") {
		t.Fatalf("scopes asked %v, want %v", sortedKeys(got), sortedKeys(want))
	}
	if report.Calls != len(want) || report.Teams != 3 || report.OwningTeams != 2 || report.Repositories != 3 || report.OwnedRepositories != 2 ||
		!report.OwnershipAsOf.Equal(osAsOf) || !report.OwnershipCheckedAt.Equal(osAsOf) {
		t.Fatalf("report header %+v", report)
	}
	for _, m := range report.Measures {
		if m.Compared == 0 || m.Matches != m.Compared || m.OrgResidual["feature_delivery"] != 2 {
			t.Fatalf("measure %+v: want every compare a match and an ORG residual of 2", m)
		}
	}
}

// Each failed or unusable answer is a finding that names why, never a pass;
// no credential reaches a log line, an error, a finding or the report.
func TestOwnerShareLiveAFailedAnswerIsAFindingAndNoSecretLeaks(t *testing.T) {
	fake := &opsFake{limitOnce: true, answer: func(measure, level, id string) (int, any, bool) {
		switch {
		case measure == "COUNT" && level == "REPO" && id == osRepoC:
			return http.StatusUnauthorized, map[string]any{"detail": "Not authenticated"}, true
		case measure == "COUNT" && level == "TEAM" && id == osTeamZ:
			return http.StatusInternalServerError, map[string]any{}, true
		case measure == "ISSUE_SIZE" && level == "TEAM" && id == osTeamY:
			answer := osBreakdown(measure, osLiveValues(level, id))
			answer["errors"] = []any{map[string]any{"message": "denied for bearer " + osLiveToken, "extensions": map[string]any{"code": "FORBIDDEN"}}}
			return http.StatusOK, answer, true
		case measure == "ISSUE_SIZE" && level == "TEAM" && id == osTeamX:
			return http.StatusOK, osBreakdown("COUNT", osLiveValues(level, id)), true
		case measure == "ISSUE_SIZE" && level == "ORG":
			answer := osBreakdown(measure, map[string]float64{"feature_delivery": 1})
			answer["data"].(map[string]any)["analytics"].(map[string]any)["breakdowns"].([]any)[0].(map[string]any)["items"] =
				[]any{map[string]any{"key": "feature_delivery", "value": nil}}
			return http.StatusOK, answer, true
		}
		return 0, nil, false
	}}
	rig := newOSLiveRig(t, fake)
	report, err := osLiveRun(t, rig, osReference())
	if err != nil {
		t.Fatal(err)
	}
	type want struct{ key, rule, why string }
	for _, w := range []want{
		{"COUNT repository " + osRepoC, string(RuleRepoAnswerMissing), "HTTP 401"},
		{"COUNT team " + osTeamZ, string(RuleTeamAnswerMissing), "HTTP 500"},
		{"ISSUE_SIZE team " + osTeamY, string(RuleTeamAnswerMissing), "[FORBIDDEN] denied for bearer [token]"},
		{"ISSUE_SIZE team " + osTeamX, string(RuleTeamAnswerMissing), "echo"},
		{"ISSUE_SIZE", string(RuleOrgAnswerMissing), "not a number"},
	} {
		found := false
		for _, f := range report.Findings() {
			found = found || (f.Key == w.key && f.Path == w.rule && strings.Contains(f.Detail, w.why) && f.Pair == pairOwnerShare && f.Root == "analytics")
		}
		if !found {
			t.Errorf("no finding %+v among %+v", w, report.Findings())
		}
	}
	// The 429 was waited out and logged once.
	if report.RateLimited != 1 || len(*rig.logs) != 1 || !strings.Contains((*rig.logs)[0], "429 count 1") {
		t.Fatalf("rate limited %d, logs %v: want one wait, logged", report.RateLimited, *rig.logs)
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	surfaces := append([]string{string(encoded), report.Table(), report.Details()}, *rig.logs...)

	// A login refused with an answer that echoes the password, and a
	// password file others can read: both are errors that name neither.
	refused := newOSLiveRig(t, &opsFake{loginStatus: http.StatusUnauthorized})
	_, loginErr := osLiveRun(t, refused, osReference())
	if loginErr == nil || !strings.Contains(loginErr.Error(), "HTTP 401") || len(refused.fake.calls) != 0 {
		t.Fatalf("login refused: error %v and %d calls, want an error naming HTTP 401 and no call", loginErr, len(refused.fake.calls))
	}
	open := newOSLiveRig(t, &opsFake{})
	open.plane.PasswordFile = osPasswordFile(t, 0o644)
	_, modeErr := osLiveRun(t, open, osReference())
	if modeErr == nil || !strings.Contains(modeErr.Error(), "group or others") || len(open.fake.logins) != 0 {
		t.Fatalf("open password file: error %v and %d logins, want a refusal before any login", modeErr, len(open.fake.logins))
	}
	surfaces = append(surfaces, loginErr.Error(), modeErr.Error())
	surfaces = append(surfaces, *refused.logs...)
	for _, surface := range surfaces {
		for _, secret := range []string{osLivePassword, osLiveToken, "refresh-Secret", osLiveEmail} {
			if strings.Contains(surface, secret) {
				t.Fatalf("a log line, error or report holds a credential or the login: %q", surface)
			}
		}
	}
}

func TestOwnerShareLivePacesEveryCallTheLoginIncluded(t *testing.T) {
	rig := newOSLiveRig(t, &opsFake{})
	rig.plane.MinInterval = 300 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rig.plane.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.plane.InvestmentBreakdown(ctx, "COUNT", osLiveWindow, ScopeLevelOrg, ""); err != nil {
		t.Fatal(err)
	}
	if got := rig.fake.calls[0].at.Sub(rig.fake.logins[0]); got < 250*time.Millisecond {
		t.Fatalf("the login and the first call are %s apart, want about the 300ms pacing interval", got)
	}
}

// A run that cannot finish in its deadline at its pace is refused before the
// login: it would report a measurement that did not happen.
func TestOwnerShareLiveRefusesARunItsDeadlineCannotHold(t *testing.T) {
	rig := newOSLiveRig(t, &opsFake{})
	rig.plane.MinInterval = 1100 * time.Millisecond
	store, err := NewStore(osExtract(osReference()...))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = runOwnerShare(ctx, rig.plane, store, osLiveWindow, []string{"COUNT", "ISSUE_SIZE"}, osFixedNow(osAsOf))
	if err == nil || !strings.Contains(err.Error(), "the run needs 15 ops calls") || len(rig.fake.logins) != 0 {
		t.Fatalf("error %v and %d logins, want a refusal naming 15 calls and no login", err, len(rig.fake.logins))
	}
}

// Ops reads the owners at each call; the store reading is taken at the first
// and checked at the last. Owners that change in between refuse the run.
func TestOwnerShareLiveRefusesOwnersThatChangedDuringTheRun(t *testing.T) {
	rig := newOSLiveRig(t, &opsFake{})
	rows := osReference()
	rows[1]["valid_to"] = "2026-10-01 00:00:30.000"
	store, err := NewStore(osExtract(rows...))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = runOwnerShare(ctx, rig.plane, store, osLiveWindow, []string{"COUNT"}, osFixedNow(osAsOf, osAsOf.Add(time.Minute)))
	if err == nil || !strings.Contains(err.Error(), "the owners changed") {
		t.Fatalf("error %v, want the run refused: Y's row ended between the first and the last call", err)
	}
}

// Answers with no value anywhere compare nothing: not a measurement.
func TestOwnerShareLiveFailsARunThatComparedNothing(t *testing.T) {
	rig := newOSLiveRig(t, &opsFake{answer: func(measure, _, _ string) (int, any, bool) {
		return http.StatusOK, osBreakdown(measure, map[string]float64{}), true
	}})
	_, err := osLiveRun(t, rig, osReference())
	if err == nil || !strings.Contains(err.Error(), "compared nothing") {
		t.Fatalf("error %v, want the run to fail as no measurement", err)
	}
}

// A redirect is never followed: a 307 of the login would send the password
// again, one of a call the bearer.
func TestOwnerShareLiveNeverFollowsARedirect(t *testing.T) {
	var mu sync.Mutex
	elsewhere := 0
	redirectCalls := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/elsewhere":
			elsewhere++
		case r.URL.Path == "/api/v1/auth/login" && !redirectCalls:
			http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
		case r.URL.Path == "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": osLiveToken})
		default:
			http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
		}
	}))
	defer server.Close()
	plane := &OpsGraphQLPlane{BaseURL: server.URL, Email: osLiveEmail, PasswordFile: osPasswordFile(t, 0o600), OrgID: FixtureOrgID}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := plane.Login(ctx); err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("login error %v, want HTTP 307", err)
	}
	mu.Lock()
	redirectCalls = true
	mu.Unlock()
	if err := plane.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := plane.InvestmentBreakdown(ctx, "COUNT", osLiveWindow, ScopeLevelOrg, ""); err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("call error %v, want HTTP 307", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if elsewhere != 0 {
		t.Fatalf("the redirect target was reached %d times: a password or a bearer was sent again", elsewhere)
	}
}

func TestTheOpsGraphQLPlaneNeverDialsTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		dir, err := os.MkdirTemp("", "ops-password-")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(dir)
		file := filepath.Join(dir, "password")
		if err := os.WriteFile(file, []byte(proxycapture.Canary), 0o600); err != nil {
			panic(err)
		}
		plane := &OpsGraphQLPlane{BaseURL: strings.TrimSuffix(proxycapture.Target, "/"), Email: osLiveEmail, PasswordFile: file, OrgID: FixtureOrgID}
		proxycapture.MustFailWith(plane.Login(ctx), "failed in transport")
	}).AssertNothingReached(t)
}
