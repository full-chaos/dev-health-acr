package credentiallifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

const (
	testOrgUUID = "9f1c2b3a-0000-4000-8000-000000000001"
	testOrgWire = "org_9f1c2b3a-0000-4000-8000-000000000001"
	secretPref  = "fcacr_SECRETPREFIX"
)

var refPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func vendoredSchema(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	raw, err := os.ReadFile(repopath.Path(t, "contracts", "auth", "v1", "ops-vendored", "jsonschema", "acr-credential-lifecycle-event.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// clientRules are the three reject_by_client rules a schema cannot state.
func clientRules(t *testing.T, body []byte) {
	t.Helper()
	var event struct {
		AggregateID    string `json:"aggregate_id"`
		EventType      string `json:"event_type"`
		IdempotencyKey string `json:"idempotency_key"`
		Payload        struct {
			Revision  int    `json:"revision"`
			ActorType string `json:"actor_type"`
			ActorRef  string `json:"actor_ref"`
			Change    struct {
				Parent string `json:"rotation_parent_id"`
			} `json:"change"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatal(err)
	}
	if want := "acr_credential:" + event.AggregateID + ":" + itoa(event.Payload.Revision); event.IdempotencyKey != want {
		t.Errorf("idempotency_key = %q, want %q", event.IdempotencyKey, want)
	}
	if event.Payload.ActorType == "credential" {
		switch event.EventType {
		case "acr_credential.revoked":
			if event.Payload.ActorRef != event.AggregateID {
				t.Errorf("credential actor on revoked: ref %q != aggregate %q", event.Payload.ActorRef, event.AggregateID)
			}
		case "acr_credential.rotated":
			if event.Payload.ActorRef != event.Payload.Change.Parent {
				t.Errorf("credential actor on rotated: ref %q != parent %q", event.Payload.ActorRef, event.Payload.Change.Parent)
			}
		}
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func assertContractValid(t *testing.T, resolved *jsonschema.Resolved, row Row) {
	t.Helper()
	var document any
	if err := json.Unmarshal(row.Payload, &document); err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(document); err != nil {
		t.Fatalf("payload refused by the vendored schema: %v\n%s", err, row.Payload)
	}
	clientRules(t, row.Payload)
}

func legacyCredential(id string) contractsv1.ClientCredential {
	expires := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	return contractsv1.ClientCredential{
		CredentialID: id, OrgID: testOrgUUID, Name: "a secret-looking name", TokenPrefix: secretPref,
		Scopes: []string{"evidence:read", "context:read"}, CreatedAt: time.Date(2026, 9, 21, 1, 2, 3, 456789000, time.UTC),
		ExpiresAt: &expires,
		Provenance: contractsv1.CredentialProvenance{
			ClientClass: "native_device", ApprovingUserID: "web-user-1",
		},
	}
}

func TestOrgWireIDPinsTheLowerCaseUUIDText(t *testing.T) {
	for in, want := range map[string]string{
		testOrgUUID:                            testOrgWire,
		strings.ToUpper(testOrgUUID):           testOrgWire,
		"00000000-0000-0000-0000-000000000000": "org_00000000-0000-0000-0000-000000000000",
	} {
		got, err := OrgWireID(in)
		if err != nil || got != want {
			t.Errorf("OrgWireID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "not-a-uuid", testOrgUUID + "0", "org_" + testOrgUUID, "{" + testOrgUUID + "}", strings.ReplaceAll(testOrgUUID, "-", "")} {
		if got, err := OrgWireID(bad); err == nil {
			t.Errorf("OrgWireID(%q) = %q, want an error", bad, got)
		}
	}
}

func TestWireRefEncodesWhatDoesNotFit(t *testing.T) {
	digest := func(s string) string { sum := sha256.Sum256([]byte(s)); return "sha256_" + hex.EncodeToString(sum[:]) }
	long := strings.Repeat("a", 256)
	for name, tc := range map[string]struct{ in, want string }{
		"fits":               {"cred_abcdefghij", "cred_abcdefghij"},
		"fits at 128":        {strings.Repeat("b", 128), strings.Repeat("b", 128)},
		"129 is encoded":     {strings.Repeat("b", 129), digest(strings.Repeat("b", 129))},
		"256 char web sub":   {long, digest(long)},
		"at sign and space":  {"a@b c", digest("a@b c")},
		"colon":              {"web:user", digest("web:user")},
		"already prefixed":   {"sha256_" + strings.Repeat("0", 64), "sha256_" + strings.Repeat("0", 64)},
		"unicode":            {"é", digest("é")},
		"leading space kept": {" a", digest(" a")},
	} {
		got, err := WireRef(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%s: WireRef(%q) = %q, %v; want %q", name, tc.in, got, err, tc.want)
		}
		if err == nil && !refPattern.MatchString(got) {
			t.Errorf("%s: %q does not fit the wire pattern", name, got)
		}
		again, _ := WireRef(tc.in)
		if again != got {
			t.Errorf("%s: not stable across calls", name)
		}
	}
	if got, err := WireRef(""); err == nil {
		t.Errorf("WireRef(\"\") = %q, want an error: an empty ref is never encoded", got)
	}
	if len(digest("x")) != 71 {
		t.Fatal("encoded form must be 71 chars")
	}
}

func TestClassOfIsTotal(t *testing.T) {
	workload := "binding-1"
	for name, tc := range map[string]struct {
		cred contractsv1.ClientCredential
		want ClientClass
	}{
		"stored class passes through":    {contractsv1.ClientCredential{Provenance: contractsv1.CredentialProvenance{ClientClass: "browser"}}, ClientClassBrowser},
		"stored operator stays":          {contractsv1.ClientCredential{Provenance: contractsv1.CredentialProvenance{ClientClass: "operator"}}, ClientClassOperator},
		"null, workload binding":         {contractsv1.ClientCredential{WorkloadBindingID: &workload}, ClientClassWorkload},
		"null, approving user, client":   {contractsv1.ClientCredential{Provenance: contractsv1.CredentialProvenance{ApprovingUserID: "u", ClientID: "c"}}, ClientClassOAuthUser},
		"null, approving user, resource": {contractsv1.ClientCredential{Resource: "r", Provenance: contractsv1.CredentialProvenance{ApprovingUserID: "u"}}, ClientClassOAuthUser},
		"null, approving user only":      {contractsv1.ClientCredential{Provenance: contractsv1.CredentialProvenance{ApprovingUserID: "u"}}, ClientClassNativeDevice},
		"null, nothing":                  {contractsv1.ClientCredential{}, ClientClassOrgBearer},
		"unknown stored value":           {contractsv1.ClientCredential{Provenance: contractsv1.CredentialProvenance{ClientClass: "bogus"}}, ClientClassOrgBearer},
	} {
		if got := ClassOf(tc.cred); got != tc.want || !got.Valid() {
			t.Errorf("%s: ClassOf = %q, want %q", name, got, tc.want)
		}
	}
}

func TestEveryMappedEventIsValidAgainstTheVendoredContract(t *testing.T) {
	resolved := vendoredSchema(t)
	at := time.Date(2026, 11, 1, 5, 6, 7, 800000000, time.UTC)
	workload := "binding-9"
	oauth := legacyCredential("cred_oauth000000001")
	oauth.Provenance = contractsv1.CredentialProvenance{ClientClass: "oauth_user", ApprovingUserID: "web:user@example", ClientID: "client-1"}
	browser := legacyCredential("cred_browser0000001")
	browser.Provenance = contractsv1.CredentialProvenance{ClientClass: "browser", ApprovingUserID: "web-user-1", SessionID: "s", FamilyID: "f"}
	bearer := legacyCredential("cred_bearer00000001")
	bearer.Provenance = contractsv1.CredentialProvenance{ClientClass: "org_bearer"}
	bearer.ExpiresAt = nil
	wl := legacyCredential("cred_workload0000001")
	wl.WorkloadBindingID = &workload
	wl.Provenance = contractsv1.CredentialProvenance{ClientClass: "workload"}
	native := legacyCredential("cred_native00000001")

	cases := map[string]Facts{
		"issued by cli user":         {Kind: KindIssued, Credential: bearer, At: at, Actor: Actor{ActorUser, "ops@example.com"}},
		"issued by workload system":  {Kind: KindIssued, Credential: wl, At: at, Actor: Actor{Type: ActorSystem}},
		"issued browser by refresh":  {Kind: KindIssued, Credential: browser, At: at, Actor: Actor{Type: ActorSystem}},
		"device redeemed native":     {Kind: KindDeviceRedeemed, Credential: native, At: at, Actor: Actor{ActorUser, "web-user-1"}},
		"device redeemed oauth":      {Kind: KindDeviceRedeemed, Credential: oauth, At: at, Actor: Actor{ActorUser, "web:user@example"}},
		"device redeemed browser":    {Kind: KindDeviceRedeemed, Credential: browser, At: at, Actor: Actor{ActorUser, "web-user-1"}},
		"rotated by admin":           {Kind: KindRotated, Credential: native, ParentID: "cred_parent0000000001", At: at, Actor: Actor{ActorUser, "admin-1"}},
		"rotated by self":            {Kind: KindRotated, Credential: native, ParentID: "cred_parent0000000001", At: at, Actor: Actor{Type: ActorCredential}},
		"revoked by user":            {Kind: KindRevoked, Revision: 2, Credential: native, At: at, Actor: Actor{ActorUser, "admin-1"}},
		"revoked by operator":        {Kind: KindRevoked, Revision: 2, Credential: native, At: at, Actor: Actor{ActorOperator, "op-1"}},
		"revoked by self":            {Kind: KindRevoked, Revision: 2, Credential: native, At: at, Actor: Actor{Type: ActorCredential}},
		"revoked by system sweep":    {Kind: KindRevoked, Revision: 2, Credential: native, At: at, Actor: Actor{Type: ActorSystem}},
		"revoked at revision 7":      {Kind: KindRevoked, Revision: 7, Credential: native, At: at, Actor: Actor{ActorUser, "x y"}},
		"issued with 256-char owner": {Kind: KindIssued, Credential: withOwner(native, strings.Repeat("o", 256)), At: at, Actor: Actor{ActorUser, "a"}},
	}
	for name, facts := range cases {
		row, err := BuildRow(facts)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		assertContractValid(t, resolved, row)
		if row.OrgID != testOrgUUID || row.AggregateID != facts.Credential.CredentialID {
			t.Errorf("%s: row keys %q %q", name, row.OrgID, row.AggregateID)
		}
		var wire struct {
			Payload struct {
				Org string `json:"organization_id"`
			} `json:"payload"`
		}
		_ = json.Unmarshal(row.Payload, &wire)
		if wire.Payload.Org != testOrgWire {
			t.Errorf("%s: organization_id = %q", name, wire.Payload.Org)
		}
	}
}

func withOwner(c contractsv1.ClientCredential, owner string) contractsv1.ClientCredential {
	c.Provenance.ApprovingUserID = owner
	return c
}

func TestMapperRefusesWhatTheContractRefuses(t *testing.T) {
	at := time.Now()
	native := legacyCredential("cred_native00000001")
	browser := legacyCredential("cred_browser0000001")
	browser.Provenance.ClientClass = "browser"
	noScopes := native
	noScopes.Scopes = nil
	for name, facts := range map[string]Facts{
		"user without a ref":          {Kind: KindIssued, Credential: native, At: at, Actor: Actor{Type: ActorUser}},
		"system with a ref":           {Kind: KindIssued, Credential: native, At: at, Actor: Actor{ActorSystem, "x"}},
		"issued by a credential":      {Kind: KindIssued, Credential: native, At: at, Actor: Actor{Type: ActorCredential}},
		"redeemed by a credential":    {Kind: KindDeviceRedeemed, Credential: native, At: at, Actor: Actor{Type: ActorCredential}},
		"rotated browser":             {Kind: KindRotated, Credential: browser, ParentID: "cred_p", At: at, Actor: Actor{ActorUser, "a"}},
		"rotated without a parent":    {Kind: KindRotated, Credential: native, At: at, Actor: Actor{ActorUser, "a"}},
		"revoked at revision 1":       {Kind: KindRevoked, Revision: 1, Credential: native, At: at, Actor: Actor{ActorUser, "a"}},
		"issued with no scopes":       {Kind: KindIssued, Credential: noScopes, At: at, Actor: Actor{ActorUser, "a"}},
		"unknown kind":                {Kind: "expired", Credential: native, At: at, Actor: Actor{Type: ActorSystem}},
		"unknown actor type":          {Kind: KindIssued, Credential: native, At: at, Actor: Actor{"robot", "a"}},
		"bad org":                     {Kind: KindIssued, Credential: func() contractsv1.ClientCredential { c := native; c.OrgID = "x"; return c }(), At: at, Actor: Actor{ActorUser, "a"}},
		"aggregate id out of pattern": {Kind: KindIssued, Credential: func() contractsv1.ClientCredential { c := native; c.CredentialID = "a b"; return c }(), At: at, Actor: Actor{ActorUser, "a"}},
		"zero time":                   {Kind: KindIssued, Credential: native, Actor: Actor{ActorUser, "a"}},
	} {
		if row, err := BuildRow(facts); err == nil {
			t.Errorf("%s: built %s, want an error", name, row.Payload)
		}
	}
}

func TestNoSecretOrHashReachesARow(t *testing.T) {
	cred := legacyCredential("cred_native00000001")
	row, err := BuildRow(Facts{Kind: KindIssued, Credential: cred, At: time.Now(), Actor: Actor{ActorUser, "admin"}})
	if err != nil {
		t.Fatal(err)
	}
	blob := string(row.Payload) + row.AggregateID + row.EventType + row.OrgID
	for _, forbidden := range []string{secretPref, "SECRETPREFIX", cred.Name, "token_prefix", "token_hash", "fingerprint", "display_prefix"} {
		if strings.Contains(blob, forbidden) {
			t.Errorf("row carries %q", forbidden)
		}
	}
}

type fakeSink struct {
	rows       []Row
	failInsert int
	inserts    int
	maxErr     error
}

func (s *fakeSink) MaxRevision(_ context.Context, id string) (int, error) {
	if s.maxErr != nil {
		return 0, s.maxErr
	}
	max := 0
	for _, r := range s.rows {
		if r.AggregateID == id && r.Revision > max {
			max = r.Revision
		}
	}
	return max, nil
}

func (s *fakeSink) Insert(_ context.Context, r Row) error {
	s.inserts++
	if s.failInsert != 0 && s.inserts == s.failInsert {
		return errors.New("insert failed")
	}
	for _, e := range s.rows {
		if e.AggregateID == r.AggregateID && e.Revision == r.Revision {
			return ErrRevisionConflict
		}
	}
	s.rows = append(s.rows, r)
	return nil
}

func revisions(s *fakeSink, id string) []int {
	var out []int
	for _, r := range s.rows {
		if r.AggregateID == id {
			out = append(out, r.Revision)
		}
	}
	return out
}

func TestRevokeOfALegacyCredentialWritesFirstTouchThenTheEvent(t *testing.T) {
	resolved := vendoredSchema(t)
	sink := &fakeSink{}
	cred := legacyCredential("cred_legacy000000001")
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if err := Append(context.Background(), sink, Facts{Kind: KindRevoked, Credential: cred, At: at, Actor: Actor{ActorUser, "admin"}}); err != nil {
		t.Fatal(err)
	}
	if got := revisions(sink, cred.CredentialID); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("revisions = %v, want [1 2]", got)
	}
	first := sink.rows[0]
	assertContractValid(t, resolved, first)
	var wire struct {
		EventType string `json:"event_type"`
		CreatedAt string `json:"created_at"`
		Payload   struct {
			ActorType string `json:"actor_type"`
			ActorRef  string `json:"actor_ref"`
			Change    struct {
				Class string `json:"acr_class"`
				Owner string `json:"owner_subject"`
			} `json:"change"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(first.Payload, &wire)
	if wire.EventType != "acr_credential.issued" || wire.Payload.ActorType != "system" || wire.Payload.ActorRef != "" ||
		wire.Payload.Change.Class != "native_device" || wire.Payload.Change.Owner != "web-user-1" || wire.CreatedAt != "2026-09-21T01:02:03.456789Z" {
		t.Errorf("first-touch event = %s", first.Payload)
	}
	if !first.CreatedAt.Equal(cred.CreatedAt) {
		t.Errorf("first-touch row time %v, want the credential's %v", first.CreatedAt, cred.CreatedAt)
	}
	assertContractValid(t, resolved, sink.rows[1])
	if err := Append(context.Background(), sink, Facts{Kind: KindRevoked, Credential: cred, At: at, Actor: Actor{ActorUser, "admin"}}); err != nil {
		t.Fatal(err)
	}
	if got := revisions(sink, cred.CredentialID); len(got) != 3 || got[2] != 3 {
		t.Fatalf("a later event after first touch must be rev 3, got %v", got)
	}
}

func TestEnsureIssuedWritesOneRevisionOneAndNeverTwo(t *testing.T) {
	sink := &fakeSink{}
	cred := legacyCredential("cred_legacy000000002")
	for i := 0; i < 3; i++ {
		if err := EnsureIssued(context.Background(), sink, cred); err != nil {
			t.Fatal(err)
		}
	}
	if got := revisions(sink, cred.CredentialID); len(got) != 1 || got[0] != 1 {
		t.Fatalf("revisions = %v, want [1]", got)
	}
}

func TestNewCredentialEventsAreRevisionOneWithoutFirstTouch(t *testing.T) {
	sink := &fakeSink{}
	cred := legacyCredential("cred_new00000000001")
	if err := Append(context.Background(), sink, Facts{Kind: KindIssued, Credential: cred, At: time.Now(), Actor: Actor{ActorUser, "admin"}}); err != nil {
		t.Fatal(err)
	}
	if got := revisions(sink, cred.CredentialID); len(got) != 1 || got[0] != 1 {
		t.Fatalf("revisions = %v, want [1]", got)
	}
}

func TestRotateOfALegacyParentWritesTheParentFirstThenRotatedThenRevoked(t *testing.T) {
	sink := &fakeSink{}
	parent := legacyCredential("cred_parent0000000001")
	child := legacyCredential("cred_child00000000001")
	at := time.Now()
	ctx := context.Background()
	if err := EnsureIssued(ctx, sink, parent); err != nil {
		t.Fatal(err)
	}
	if err := Append(ctx, sink, Facts{Kind: KindRotated, Credential: child, ParentID: parent.CredentialID, At: at, Actor: Actor{ActorUser, "admin"}}); err != nil {
		t.Fatal(err)
	}
	if err := Append(ctx, sink, Facts{Kind: KindRevoked, Credential: parent, At: at, Actor: Actor{ActorUser, "admin"}}); err != nil {
		t.Fatal(err)
	}
	if got := revisions(sink, parent.CredentialID); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("parent revisions = %v, want [1 2]", got)
	}
	if got := revisions(sink, child.CredentialID); len(got) != 1 || got[0] != 1 {
		t.Fatalf("child revisions = %v, want [1]", got)
	}
	if sink.rows[0].AggregateID != parent.CredentialID || !strings.Contains(string(sink.rows[1].Payload), parent.CredentialID) {
		t.Fatalf("the parent's rev 1 must precede the rotated event that names it")
	}
}

func TestSinkFailuresSurfaceAndAreNeverSwallowed(t *testing.T) {
	cred := legacyCredential("cred_fail0000000001")
	facts := Facts{Kind: KindRevoked, Credential: cred, At: time.Now(), Actor: Actor{ActorUser, "admin"}}
	for name, sink := range map[string]*fakeSink{
		"first insert fails":  {failInsert: 1},
		"second insert fails": {failInsert: 2},
		"max read fails":      {maxErr: errors.New("read failed")},
	} {
		if err := Append(context.Background(), sink, facts); err == nil {
			t.Errorf("%s: Append returned nil", name)
		}
	}
	dup := &fakeSink{}
	_ = EnsureIssued(context.Background(), dup, cred)
	if err := dup.Insert(context.Background(), dup.rows[0]); !errors.Is(err, ErrRevisionConflict) {
		t.Errorf("a duplicate (aggregate, revision) must conflict, got %v", err)
	}
}

func TestLedgerEnforcesTheRevisionKey(t *testing.T) {
	ledger := NewLedger()
	cred := legacyCredential("cred_ledger000000001")
	if err := Append(context.Background(), ledger, Facts{Kind: KindRevoked, Credential: cred, At: time.Now(), Actor: Actor{ActorUser, "a"}}); err != nil {
		t.Fatal(err)
	}
	rows := ledger.Rows()
	if len(rows) != 2 || rows[0].Revision != 1 || rows[1].Revision != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if err := ledger.Insert(context.Background(), rows[1]); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("duplicate insert = %v, want ErrRevisionConflict", err)
	}
	rows[0].Revision = 99
	if ledger.Rows()[0].Revision != 1 {
		t.Fatal("Rows must return a copy")
	}
}

func TestTheSchemaOracleIsNotVacuous(t *testing.T) {
	resolved := vendoredSchema(t)
	row, err := BuildRow(Facts{Kind: KindIssued, Credential: legacyCredential("cred_oracle0000001"), At: time.Now(), Actor: Actor{ActorUser, "admin"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"token prefix member": func(m map[string]any) {
			m["payload"].(map[string]any)["change"].(map[string]any)["token_prefix"] = secretPref
		},
		"bad org": func(m map[string]any) { m["payload"].(map[string]any)["organization_id"] = "org_" },
		"seventh class": func(m map[string]any) {
			m["payload"].(map[string]any)["change"].(map[string]any)["acr_class"] = "other"
		},
		"system with ref": func(m map[string]any) { m["payload"].(map[string]any)["actor_type"] = "system" },
	} {
		var doc map[string]any
		if err := json.Unmarshal(row.Payload, &doc); err != nil {
			t.Fatal(err)
		}
		mutate(doc)
		if err := resolved.Validate(doc); err == nil {
			t.Errorf("%s: the vendored schema accepted a mutated payload", name)
		}
	}
}

func TestActorsFollowTheInputThatTheAuditRowReads(t *testing.T) {
	for name, tc := range map[string]struct {
		got  Actor
		want Actor
	}{
		"cli issue":             {CreateActor(CreateInput{ActorID: "ops"}), Actor{ActorUser, "ops"}},
		"workload issue":        {CreateActor(CreateInput{ActorID: "binding", IssuanceProvenance: IssuanceProvenanceWorkloadExchange}), Actor{Type: ActorSystem}},
		"device redeem":         {CreateActor(CreateInput{ActorID: "u1", ApprovingUserID: "u1", IssuanceProvenance: IssuanceProvenanceDeviceAuthorization}), Actor{ActorUser, "u1"}},
		"refresh grant":         {CreateActor(CreateInput{ActorID: "oauth_refresh_grant", ApprovingUserID: "u1", IssuanceProvenance: IssuanceProvenanceDeviceAuthorization}), Actor{Type: ActorSystem}},
		"admin rotate":          {RotationActor(RotationInput{ActorID: "admin"}), Actor{ActorUser, "admin"}},
		"self rotate":           {RotationActor(RotationInput{ActorID: "cred_a", SelfCredential: true}), Actor{Type: ActorCredential}},
		"admin revoke":          {RevocationActor(RevocationInput{ActorID: "admin"}), Actor{ActorUser, "admin"}},
		"operator revoke":       {RevocationActor(RevocationInput{ActorID: "op", ActorType: "operator"}), Actor{ActorOperator, "op"}},
		"system revoke":         {RevocationActor(RevocationInput{ActorID: "device-credential-ack", ActorType: "system"}), Actor{Type: ActorSystem}},
		"self revoke":           {RevocationActor(RevocationInput{ActorID: "cred_a", SelfCredential: true}), Actor{Type: ActorCredential}},
		"unknown type is user":  {RevocationActor(RevocationInput{ActorID: "x", ActorType: "weird"}), Actor{ActorUser, "x"}},
		"rollback by successor": {RollbackActor(RotationRollbackInput{ActorID: "cred_s", SuccessorCredentialID: "cred_s"}), Actor{Type: ActorCredential}},
		"rollback by a user":    {RollbackActor(RotationRollbackInput{ActorID: "admin", SuccessorCredentialID: "cred_s"}), Actor{ActorUser, "admin"}},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, tc.got, tc.want)
		}
	}
	if CreateKind(CreateInput{IssuanceProvenance: IssuanceProvenanceDeviceAuthorization}) != KindDeviceRedeemed ||
		CreateKind(CreateInput{}) != KindIssued || CreateKind(CreateInput{IssuanceProvenance: IssuanceProvenanceWorkloadExchange}) != KindIssued {
		t.Error("CreateKind")
	}
}

func TestSelfMarkerNeedsTheCredentialsOwnId(t *testing.T) {
	if _, err := normalizeRevocation(RevocationInput{OrgID: testOrgUUID, CredentialID: "cred_aaaaaaaaaaaaaaaaaaaaa", ActorID: "someone", SelfCredential: true}); err == nil {
		t.Error("a self revocation naming another actor must be refused")
	}
	if _, err := normalizeRevocation(RevocationInput{OrgID: testOrgUUID, CredentialID: "cred_aaaaaaaaaaaaaaaaaaaaa", ActorID: "cred_aaaaaaaaaaaaaaaaaaaaa", SelfCredential: true}); err != nil {
		t.Errorf("a self revocation by the credential itself: %v", err)
	}
	if _, err := normalizeRevocation(RevocationInput{OrgID: testOrgUUID, CredentialID: "cred_aaaaaaaaaaaaaaaaaaaaa", ActorID: "someone"}); err != nil {
		t.Errorf("an admin revocation: %v", err)
	}
}
