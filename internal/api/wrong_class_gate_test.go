package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// The wrong-credential-class gate: every class of the ops credential-classes.json
// against every row of contracts/auth/v1/endpoint-profiles.acr.json, through the
// real route table (NewApp + Handler) with the real authenticator, web-assertion
// verifier and stores. A wrong class gets the answer its garbage twin gets. A
// protected baseline is the uniform refusal the authenticator writes
// ((*auth.Authenticator).MiddlewareFor, internal/auth/middleware.go: 401
// invalid_token "Missing or invalid ACR credential") and reaches no store. A live
// credential of each accepted class passes; its forged twin is refused.
//
// Inputs: ACR_CREDENTIAL_CLASSES and ACR_ENDPOINT_PROFILE_SCHEMA name the pinned
// ops files. With ACR_CONTRACT_GATE=required (the CI contract-gate job) a missing
// input fails; elsewhere the gate skips with its reason.

const (
	wcgService       = "dev-health-acr-api"
	wcgWebIssuer     = "https://web.example.test"
	wcgWebAudience   = "acr-api"
	wcgOAuthIssuer   = "https://acr.example.test"
	wcgOAuthResource = "https://mcp.example.test/mcp"
	wcgConsentURL    = "https://web.example.test/acr/authorize"
	wcgRepository    = "example-org/widget-service"
	wcgOrg           = "org_1"

	wcgClientCredential = "acr_client_credential"
	wcgWebAssertion     = "acr_web_assertion"
)

// wcgInput is what a row's request carries besides its credential. form rows
// take an application/x-www-form-urlencoded body, so a body-field class is sent
// there as a form field as well as JSON.
type wcgInput struct {
	query string
	body  string
	form  bool
}

var wcgInputs = map[string]wcgInput{
	"POST /api/v1/oauth/token [dev-health-acr-api]":    {form: true},
	"POST /authorize/first-party [dev-health-acr-api]": {form: true},
	"POST /token [dev-health-acr-api]":                 {form: true},
	"POST /revoke [dev-health-acr-api]":                {form: true},
	"POST /device_authorization [dev-health-acr-api]":  {form: true},
}

// wcgPublicCarrierAnswers are the public rows where the carrier itself, with a
// credential of no class, changes the answer from no credential at all. Every
// class in that carrier still gets the twin's answer; an entry that stops
// holding fails as stale.
var wcgPublicCarrierAnswers = map[string]string{
	"POST /token [dev-health-acr-api] baseline:authorization": "the token endpoint reads Authorization as RFC 6749 §2.3.1 client authentication " +
		"and refuses any non-Basic scheme invalid_request (tokenClientID, internal/api/oauth_routes.go:1156)",
}

type wcgReplays struct {
	mu    sync.Mutex
	seen  map[string]bool
	calls *atomic.Int64
}

func (r *wcgReplays) Observe(_ context.Context, issuer, jti string, _, _ time.Time) (bool, error) {
	r.calls.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	key := issuer + "\x00" + jti
	replay := r.seen[key]
	r.seen[key] = true
	return replay, nil
}

type wcgAudit struct {
	storage.AuditStore
	calls *atomic.Int64
}

func (a wcgAudit) Record(ctx context.Context, event storage.AuditEvent) error {
	a.calls.Add(1)
	return a.AuditStore.Record(ctx, event)
}

// wcgRig is one real App. Every store the auth layer or a handler reaches is
// counted: the credential store at the store, audit, web-assertion replay ids
// and the entitlement check.
type wcgRig struct {
	handler      http.Handler
	now          time.Time
	key          ed25519.PrivateKey
	foreign      ed25519.PrivateKey
	credentials  *storage.CredentialLifecycle
	lookups      atomic.Int64
	audits       atomic.Int64
	replays      atomic.Int64
	entitlements atomic.Int64
	jti          int
}

func (r *wcgRig) effects() int64 {
	return r.lookups.Load() + r.audits.Load() + r.replays.Load() + r.entitlements.Load()
}

func newWcgRig(t *testing.T) *wcgRig {
	t.Helper()
	rig := &wcgRig{now: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return rig.now }
	memoryAudit := memory.NewAuditStore()
	credentials, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: memoryAudit, Now: clock, Observe: func(string) { rig.lookups.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	rig.credentials = credentials
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, rig.foreign, err = ed25519.GenerateKey(rand.Reader); err != nil {
		t.Fatal(err)
	}
	rig.key = private
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{Issuer: wcgWebIssuer, Audience: wcgWebAudience, JWKSPath: writeAPIJWKS(t, public), Now: clock,
		Replays: &wcgReplays{seen: map[string]bool{}, calls: &rig.replays}})
	if err != nil {
		t.Fatal(err)
	}
	quota := func() (time.Duration, int, limits.ResourceBudget) {
		return time.Minute, 100, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}
	}
	window, perOrg, budget := quota()
	manager, err := limits.NewManager(limits.Options{Now: clock, PerOrgConcurrency: 4, Policies: limits.PolicySet{
		Auth:         limits.AuthPolicy{Window: window, PerOrgLimit: perOrg},
		Context:      limits.ContextPolicy{Window: window, PerOrgLimit: perOrg, Resources: budget},
		Evidence:     limits.EvidencePolicy{Window: window, PerOrgLimit: perOrg, Resources: budget},
		Episode:      limits.EpisodePolicy{Window: window, PerOrgLimit: perOrg, Resources: budget},
		Data:         limits.DataPolicy{Window: window, PerOrgLimit: perOrg, Resources: budget},
		Capabilities: limits.CapabilitiesPolicy{Window: window, PerOrgLimit: perOrg},
	}})
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities: StaticCapabilitiesProvider{Now: clock, Value: hostedCapabilities()}, Limits: manager, Now: clock, WebAssertions: verifier,
		Runtime: &RuntimeDependencies{
			Credentials: credentials, Audit: wcgAudit{AuditStore: memoryAudit, calls: &rig.audits},
			Entitlements: EntitlementFunc(func(context.Context, string, string) (bool, error) {
				rig.entitlements.Add(1)
				return true, nil
			}),
			Assembler: noopAssembler{}, Evidence: noopEvidenceStore{},
			DeviceAuthorizations: devices, DeviceVerificationURL: "https://web.example.test/acr/device",
			DeviceAuthorizationLimiter: NewDeviceAuthorizationLimiter(ClockFunc(clock)),
			ReadinessChecks:            []ReadinessCheck{CheckFunc{CheckName: "postgres"}},
			DataStoreChecks:            []ReadinessCheck{CheckFunc{CheckName: "clickhouse"}},
			OAuth:                      &OAuthRuntime{Issuer: wcgOAuthIssuer, Resources: []string{wcgOAuthResource}, Store: memory.NewOAuthStore(clock), ConsentURL: wcgConsentURL},
		},
	}, slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err != nil {
		t.Fatal(err)
	}
	rig.handler = app.Handler()
	return rig
}

// issue stores a real fcacr_ client credential and returns its token.
func (r *wcgRig) issue(t *testing.T) string {
	t.Helper()
	service, err := auth.NewService(r.credentials, auth.ServiceOptions{Now: func() time.Time { return r.now }})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Create(context.Background(), auth.CreateCredentialRequest{
		OrgID: wcgOrg, Name: "wrong-class gate", RepositoryScopes: []string{wcgRepository}, CreatedBy: "wrong_class_gate",
		Scopes: []string{auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.ScopeEpisodeWrite, auth.ScopeDataRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Token
}

func (r *wcgRig) claims(method, path string, body []byte) map[string]any {
	r.jti++
	digest := sha256.Sum256(body)
	return map[string]any{
		"iss": wcgWebIssuer, "aud": wcgWebAudience, "sub": "user_123", "org_id": wcgOrg,
		"repository_scopes": []string{wcgRepository},
		"permissions":       []string{auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.WebAssertionPermissionCredentialIssue},
		"iat":               r.now.Unix(), "nbf": r.now.Unix(), "exp": r.now.Add(30 * time.Second).Unix(), "jti": fmt.Sprintf("wrong_class_%d", r.jti),
		"method": method, "path": path, "body_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
}

func wcgSigningInput(t *testing.T, header, claims map[string]any) string {
	t.Helper()
	h, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	c, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
}

// assertion is a web assertion bound to the request, signed by key: the
// verifier's own key makes a live one, the rig's foreign key a forged one.
func (r *wcgRig) assertion(t *testing.T, key ed25519.PrivateKey, method, path string, body []byte) string {
	t.Helper()
	input := wcgSigningInput(t, map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": "current"}, r.claims(method, path, body))
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
}

// minter signs a Bearer-class JWT with the claims of a live web assertion for
// the request: an EdDSA token with the verifier's own key, an HS256 token with
// an HMAC secret. Another alg has no rule and fails the carrier.
func (r *wcgRig) minter(t *testing.T, method, path string, body []byte) wcgMint {
	return func(alg, typ string) string {
		header := map[string]any{"alg": alg}
		if typ != "" {
			header["typ"] = typ
		}
		switch alg {
		case "EdDSA":
			header["kid"] = "current"
			input := wcgSigningInput(t, header, r.claims(method, path, body))
			return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(r.key, []byte(input)))
		case "HS256":
			input := wcgSigningInput(t, header, r.claims(method, path, body))
			mac := hmac.New(sha256.New, []byte("wrong-class-gate-hs256-secret"))
			mac.Write([]byte(input))
			return input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		}
		return ""
	}
}

// wcgSpec is one cell's credential: a class through its carrier, a fixed
// baseline carrier, or a live or forged credential of an acr class.
type wcgSpec struct {
	class    *wcgClass
	baseline *wcgCarrier
	form     bool
	live     string
	forged   string
}

type wcgCell struct {
	answer  wcgAnswer
	effects int64
	lookups int64
	carrier wcgCarrier
}

func wcgBody(input wcgInput, carrier wcgCarrier, form bool) ([]byte, string) {
	if carrier.BodyField == "" {
		switch {
		case input.form:
			return []byte(input.body), "application/x-www-form-urlencoded"
		case input.body != "":
			return []byte(input.body), "application/json"
		}
		return nil, ""
	}
	if form {
		values, _ := url.ParseQuery(input.body)
		values.Set(carrier.BodyField, carrier.Value)
		return []byte(values.Encode()), "application/x-www-form-urlencoded"
	}
	fields := map[string]any{}
	if input.body != "" && !input.form {
		_ = json.Unmarshal([]byte(input.body), &fields)
	}
	fields[carrier.BodyField] = carrier.Value
	raw, _ := json.Marshal(fields)
	return raw, "application/json"
}

func wcgServe(t *testing.T, row wcgRow, spec wcgSpec) wcgCell {
	t.Helper()
	rig := newWcgRig(t)
	input := wcgInputs[row.ID]
	path := row.path()
	var carrier wcgCarrier
	switch {
	case spec.class != nil:
		base, _ := wcgBody(input, wcgCarrier{}, false)
		var ok bool
		var err error
		carrier, ok, err = wcgCarrierFor(*spec.class, rig.minter(t, row.Method, path, base))
		if err != nil || !ok {
			t.Fatalf("class %s: carrier ok %v err %v", spec.class.ID, ok, err)
		}
	case spec.baseline != nil:
		carrier = *spec.baseline
	}
	body, contentType := wcgBody(input, carrier, spec.form)
	switch spec.live + spec.forged {
	case "":
	case wcgClientCredential:
		token := auth.TokenPrefix + wcgFiller
		if spec.live != "" {
			token = rig.issue(t)
		}
		carrier = wcgCarrier{Header: wcgAuthorization, Value: "Bearer " + token}
	case wcgWebAssertion:
		key := rig.foreign
		if spec.live != "" {
			key = rig.key
		}
		carrier = wcgCarrier{Header: auth.WebAssertionHeader, Value: rig.assertion(t, key, row.Method, path, body)}
	default:
		t.Fatalf("no live or forged credential rule for class %s%s", spec.live, spec.forged)
	}
	request := httptest.NewRequest(row.Method, path+input.query, bytes.NewReader(body))
	request.RemoteAddr = "192.0.2.10:4000"
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if carrier.Header != "" {
		request.Header.Set(carrier.Header, carrier.Value)
	}
	before, lookups := rig.effects(), rig.lookups.Load()
	recorder := httptest.NewRecorder()
	rig.handler.ServeHTTP(recorder, request)
	return wcgCell{answer: wcgCapture(recorder), effects: rig.effects() - before, lookups: rig.lookups.Load() - lookups, carrier: carrier}
}

// wcgUniformRefusal checks an answer is byte for byte the authenticator's
// refusal of a missing or invalid credential.
func wcgUniformRefusal(answer wcgAnswer) error {
	var want bytes.Buffer
	_ = json.NewEncoder(&want).Encode(contractsv1.ErrorEnvelope{
		SchemaVersion: contractsv1.ErrorSchema, RequestID: answer.requestID,
		Error: contractsv1.ErrorDetail{Code: "invalid_token", Message: "Missing or invalid ACR credential", HTTPStatus: http.StatusUnauthorized},
	})
	if answer.Status != http.StatusUnauthorized || answer.raw != want.String() || answer.Header != "Content-Type: application/json" || answer.requestID == "" {
		return fmt.Errorf("not the uniform refusal: %s", answer)
	}
	return nil
}

func wcgServed(answer wcgAnswer) error {
	if (answer.Status == http.StatusNotFound && answer.Body == "404 page not found\n") || (answer.Status == http.StatusMethodNotAllowed && strings.HasPrefix(answer.Body, "Method Not Allowed")) {
		return fmt.Errorf("the route table does not serve the row: %s", answer)
	}
	return nil
}

func wcgContractInputs(t *testing.T) (classes, schema []byte) {
	t.Helper()
	required := os.Getenv("ACR_CONTRACT_GATE") == "required"
	var problems []string
	read := func(name string) []byte {
		path := os.Getenv(name)
		if path == "" {
			problems = append(problems, name+" is unset")
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s=%q: %v", name, path, err))
			return nil
		}
		return raw
	}
	classes = read("ACR_CREDENTIAL_CLASSES")
	schema = read("ACR_ENDPOINT_PROFILE_SCHEMA")
	if len(problems) == 0 {
		return classes, schema
	}
	if required {
		t.Fatalf("WRONG_CLASS_GATE input missing in the contract-gate step (%s): a measurement that did not happen must fail, not skip", strings.Join(problems, "; "))
	}
	t.Skipf("WRONG_CLASS_GATE_SKIPPED: the pinned ops credential-classes.json and endpoint-profile.schema.json are not given (%s); "+
		"the contract-gate CI step sets ACR_CREDENTIAL_CLASSES, ACR_ENDPOINT_PROFILE_SCHEMA and ACR_CONTRACT_GATE=required", strings.Join(problems, "; "))
	return nil, nil
}

type wcgDriven struct {
	class wcgClass
	shape wcgCarrier
}

func TestWrongClassGate(t *testing.T) {
	classesRaw, schemaRaw := wcgContractInputs(t)
	vocabulary, err := wcgVocabulary(schemaRaw)
	if err != nil {
		t.Fatal(err)
	}
	classes, err := wcgLoadClasses(classesRaw, vocabulary)
	if err != nil {
		t.Fatal(err)
	}
	profilesRaw, err := os.ReadFile(repopath.Path(t, "contracts", "auth", "v1", "endpoint-profiles.acr.json"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := wcgLoadRows(profilesRaw, wcgService)
	if err != nil {
		t.Fatal(err)
	}
	var registry, inventory struct {
		Classes []json.RawMessage `json:"classes"`
		Rows    []json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(classesRaw, &registry); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(profilesRaw, &inventory); err != nil {
		t.Fatal(err)
	}

	shapeMint := func(alg, typ string) string { return "h." + alg + typ + ".s" }
	var driven []wcgDriven
	census := wcgCensus{Service: wcgService, Rows: len(inventory.Rows), Classes: len(registry.Classes)}
	for _, class := range classes {
		shape, ok, err := wcgCarrierFor(class, shapeMint)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			if class.notDrivenReason() == "" {
				t.Fatalf("class %s is not driven and gives no reason", class.ID)
			}
			census.NotDriven = append(census.NotDriven, class.ID)
			continue
		}
		driven = append(driven, wcgDriven{class: class, shape: shape})
	}
	if len(driven) == 0 {
		t.Fatal("no class has a carrier: the matrix would drive nothing")
	}
	ids := map[string]bool{}
	for _, row := range rows {
		ids[row.ID] = true
	}
	for id := range wcgInputs {
		if !ids[id] {
			t.Fatalf("input fixture %q names no inventory row", id)
		}
	}
	for entry := range wcgPublicCarrierAnswers {
		id, _, _ := strings.Cut(entry, "] ")
		if !ids[id+"]"] {
			t.Fatalf("public carrier entry %q names no inventory row", entry)
		}
	}

	drivenRows := map[string]bool{}
	for _, row := range rows {
		t.Run(row.ID, func(t *testing.T) {
			problems, wrong, accept := wcgDriveRow(t, row, driven)
			census.WrongCells += wrong
			census.AcceptCells += accept
			for _, problem := range problems {
				t.Error(problem)
			}
			drivenRows[row.ID] = true
		})
	}
	census.Driven = len(drivenRows)
	t.Log(census.line())

	if len(rows) != len(inventory.Rows) {
		t.Fatalf("rows driven from %d of %d inventory rows", len(rows), len(inventory.Rows))
	}
	if len(classes) != len(registry.Classes) {
		t.Fatalf("classes loaded %d of %d registry classes", len(classes), len(registry.Classes))
	}
	var notDriven []string
	for _, row := range rows {
		if !drivenRows[row.ID] {
			notDriven = append(notDriven, row.ID)
		}
	}
	if len(notDriven) != 0 || census.Driven != len(inventory.Rows) {
		t.Fatalf("rows not driven: %v", notDriven)
	}
	if census.WrongCells == 0 || census.AcceptCells == 0 {
		t.Fatalf("wrong cells %d, accept cells %d: the matrix measured nothing", census.WrongCells, census.AcceptCells)
	}
}

func wcgDriveRow(t *testing.T, row wcgRow, driven []wcgDriven) (problems []string, wrongCells, acceptCells int) {
	t.Helper()
	input := wcgInputs[row.ID]
	none := wcgServe(t, row, wcgSpec{baseline: &wcgCarrier{Class: "baseline:none"}})
	if err := wcgServed(none.answer); err != nil {
		return []string{err.Error()}, 0, 0
	}
	baselines := map[string]wcgCell{"baseline:none": none}
	baselineFor := func(carrier wcgCarrier, form bool) (string, wcgCell) {
		twin := carrier.Baseline()
		key := twin.Class
		if form {
			key += ":form"
		}
		if _, ok := baselines[key]; !ok {
			baselines[key] = wcgServe(t, row, wcgSpec{baseline: &twin, form: form})
		}
		return key, baselines[key]
	}
	compare := func(name string, cell wcgCell, key string, reference wcgCell) {
		wrongCells++
		if !cell.answer.same(reference.answer) {
			problems = append(problems, fmt.Sprintf("class %s: %s != %s %s", name, cell.answer, key, reference.answer))
		}
	}

	for i := range driven {
		class := driven[i].class
		if row.accepts(class.ID) {
			continue
		}
		transports := []bool{false}
		if driven[i].shape.BodyField != "" && input.form {
			transports = append(transports, true)
		}
		for _, form := range transports {
			name := class.ID
			if form {
				name += " (form)"
			}
			cell := wcgServe(t, row, wcgSpec{class: &class, form: form})
			key, reference := baselineFor(driven[i].shape, form)
			compare(name, cell, key, reference)
		}
	}
	// A live credential of an acr class the row does not accept is a wrong
	// class too, the strongest one: it would pass a row that took it.
	for _, live := range []struct {
		class string
		shape wcgCarrier
	}{
		{wcgClientCredential, wcgCarrier{Header: wcgAuthorization}},
		{wcgWebAssertion, wcgCarrier{Header: auth.WebAssertionHeader}},
	} {
		if row.accepts(live.class) {
			continue
		}
		cell := wcgServe(t, row, wcgSpec{live: live.class})
		key, reference := baselineFor(live.shape, false)
		compare("live "+live.class, cell, key, reference)
	}

	if !row.protected() {
		for key, base := range baselines {
			reason, known := wcgPublicCarrierAnswers[row.ID+" "+key]
			switch differs := !base.answer.same(none.answer); {
			case differs && !known:
				problems = append(problems, fmt.Sprintf("public row: %s %s != baseline:none %s", key, base.answer, none.answer))
			case !differs && known:
				problems = append(problems, fmt.Sprintf("public row: stale carrier entry %q (%s): %s now answers as baseline:none", row.ID+" "+key, reason, key))
			}
		}
		return problems, wrongCells, acceptCells
	}
	baselineFor(wcgCarrier{Header: wcgAuthorization}, false)
	for key, base := range baselines {
		if err := wcgUniformRefusal(base.answer); err != nil {
			problems = append(problems, fmt.Sprintf("protected %s: %v", key, err))
		}
		if base.effects != 0 {
			problems = append(problems, fmt.Sprintf("protected %s reached a store or handler (%d calls)", key, base.effects))
		}
	}
	if len(row.Accepted) == 0 {
		problems = append(problems, "a protected row accepts no class")
	}
	for _, class := range row.Accepted {
		if class != wcgClientCredential && class != wcgWebAssertion {
			problems = append(problems, fmt.Sprintf("accepted class %s has no live credential rule", class))
			continue
		}
		acceptCells++
		live := wcgServe(t, row, wcgSpec{live: class})
		if live.answer.Status == http.StatusUnauthorized || wcgUniformRefusal(live.answer) == nil {
			problems = append(problems, fmt.Sprintf("accept %s: a live credential got the refusal %s", class, live.answer))
		}
		if class == wcgClientCredential && live.lookups == 0 {
			problems = append(problems, fmt.Sprintf("accept %s: the credential store was never read", class))
		}
		forged := wcgServe(t, row, wcgSpec{forged: class})
		if err := wcgUniformRefusal(forged.answer); err != nil {
			problems = append(problems, fmt.Sprintf("forged %s: %v", class, err))
		}
	}
	return problems, wrongCells, acceptCells
}
