package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type retireKey struct {
	kid         string
	public      ed25519.PublicKey
	retireAfter string
}

func writeRetireJWKS(t *testing.T, path string, keys ...retireKey) string {
	t.Helper()
	members := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		member := map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": key.kid, "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(key.public)}
		if key.retireAfter != "" {
			member["retire_after"] = key.retireAfter
		}
		members = append(members, member)
	}
	encoded, err := json.Marshal(map[string]any{"keys": members})
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		path = filepath.Join(t.TempDir(), "web-assertions.jwks.json")
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type countingReplays struct{ observed int }

func (c *countingReplays) Observe(context.Context, string, string, time.Time, time.Time) (bool, error) {
	c.observed++
	return false, nil
}

type retireRig struct {
	t          *testing.T
	verifier   *WebAssertionVerifier
	path       string
	clock      time.Time
	oldPublic  ed25519.PublicKey
	oldPrivate ed25519.PrivateKey
	newPublic  ed25519.PublicKey
	newPrivate ed25519.PrivateKey
	replays    *countingReplays
	logs       *recordingHandler
	serial     int
}

func newRetireRig(t *testing.T, t0 time.Time, oldRetireAfter string) *retireRig {
	t.Helper()
	rig := &retireRig{t: t, clock: t0, replays: &countingReplays{}, logs: &recordingHandler{}}
	var err error
	if rig.oldPublic, rig.oldPrivate, err = ed25519.GenerateKey(rand.Reader); err != nil {
		t.Fatal(err)
	}
	if rig.newPublic, rig.newPrivate, err = ed25519.GenerateKey(rand.Reader); err != nil {
		t.Fatal(err)
	}
	rig.path = writeRetireJWKS(t, "", retireKey{"old", rig.oldPublic, oldRetireAfter}, retireKey{"new", rig.newPublic, ""})
	rig.verifier, err = NewWebAssertionVerifier(WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: rig.path,
		Now: func() time.Time { return rig.clock }, Replays: rig.replays, Logger: slog.New(rig.logs),
	})
	if err != nil {
		t.Fatal(err)
	}
	return rig
}

func (r *retireRig) verify(kid string) (token string, err error) {
	r.t.Helper()
	private := r.newPrivate
	if kid == "old" {
		private = r.oldPrivate
	}
	r.serial++
	body := []byte(`{}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/context-packets", bytes.NewReader(body))
	claims := webAssertionClaims(r.clock, request, body)
	claims["jti"] = fmt.Sprintf("assertion_%d", r.serial)
	token = signTestWebAssertion(r.t, private, kid, claims)
	request.Header.Set(WebAssertionHeader, token)
	_, err = r.verifier.Verify(request)
	return token, err
}

func (r *retireRig) mustAccept(kid string) {
	r.t.Helper()
	if _, err := r.verify(kid); err != nil {
		r.t.Fatalf("kid %q at %s refused: %v", kid, r.clock.Format(time.RFC3339), err)
	}
}

func (r *retireRig) mustRefuse(kid string) {
	r.t.Helper()
	if _, err := r.verify(kid); err != ErrInvalidWebAssertion {
		r.t.Fatalf("kid %q at %s: err = %v, want the bare ErrInvalidWebAssertion", kid, r.clock.Format(time.RFC3339), err)
	}
}

var retireT0 = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func TestWebAssertionKeyRetire_R1BothKidsAcceptedInsideTheOverlap(t *testing.T) {
	rig := newRetireRig(t, retireT0, retireT0.Add(time.Hour).Format(time.RFC3339))
	rig.clock = retireT0.Add(59*time.Minute + 59*time.Second)
	rig.mustAccept("old")
	rig.mustAccept("new")
}

func TestWebAssertionKeyRetire_R2OldKidRefusedAtAndAfterRetireAfter(t *testing.T) {
	rig := newRetireRig(t, retireT0, retireT0.Add(time.Hour).Format(time.RFC3339))
	for _, offset := range []time.Duration{time.Hour, time.Hour + time.Second, 3 * time.Hour} {
		rig.clock = retireT0.Add(offset)
		rig.mustRefuse("old")
		rig.mustAccept("new")
	}
}

func TestWebAssertionKeyRetire_R2RefusedKidFailsLikeAnUnknownKid(t *testing.T) {
	rig := newRetireRig(t, retireT0, retireT0.Add(time.Hour).Format(time.RFC3339))
	rig.clock = retireT0.Add(2 * time.Hour)
	_, retiredErr := rig.verify("old")
	rig.oldPrivate = rig.newPrivate
	_, unknownErr := rig.verify("ghost")
	if retiredErr == nil || retiredErr != unknownErr || !errors.Is(retiredErr, ErrInvalidWebAssertion) {
		t.Fatalf("retired kid err = %v, unknown kid err = %v", retiredErr, unknownErr)
	}
}

func TestWebAssertionKeyRetire_R3CeilingIsExactlyTwentyFourHours(t *testing.T) {
	atCeiling := newRetireRig(t, retireT0, retireT0.Add(24*time.Hour).Format(time.RFC3339))
	atCeiling.mustAccept("old")
	atCeiling.mustAccept("new")
	overCeiling := newRetireRig(t, retireT0, retireT0.Add(24*time.Hour+time.Second).Format(time.RFC3339))
	overCeiling.mustRefuse("old")
	overCeiling.mustAccept("new")
}

func TestWebAssertionKeyRetire_R4RotationInPlaceIsPickedUpOnTheNextVerify(t *testing.T) {
	rig := newRetireRig(t, retireT0, "")
	rig.mustAccept("old")
	writeRetireJWKS(t, rig.path,
		retireKey{"old", rig.oldPublic, retireT0.Add(time.Hour).Format(time.RFC3339)},
		retireKey{"new", rig.newPublic, ""})
	rig.clock = retireT0.Add(30 * time.Minute)
	rig.mustAccept("old")
	rig.clock = retireT0.Add(time.Hour)
	rig.mustRefuse("old")
	rig.mustAccept("new")
	writeRetireJWKS(t, rig.path, retireKey{"new", rig.newPublic, ""})
	rig.mustRefuse("old")
	rig.mustAccept("new")
}

func TestWebAssertionKeyRetire_R5MalformedMemberRefusesTheWholeDocument(t *testing.T) {
	rig := newRetireRig(t, retireT0, "")
	writeRetireJWKS(t, rig.path,
		retireKey{"old", rig.oldPublic, "tomorrow"},
		retireKey{"new", rig.newPublic, ""})
	rig.mustRefuse("old")
	rig.mustRefuse("new")
	_, err := NewWebAssertionVerifier(WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: rig.path, Now: func() time.Time { return retireT0 },
	})
	if err != ErrInvalidWebAssertion {
		t.Fatalf("NewWebAssertionVerifier err = %v, want ErrInvalidWebAssertion", err)
	}
}

func TestWebAssertionKeyRetire_R5LeapSecondMemberRefusesTheWholeDocument(t *testing.T) {
	rig := newRetireRig(t, retireT0, "")
	writeRetireJWKS(t, rig.path,
		retireKey{"old", rig.oldPublic, "2026-12-31T23:59:60Z"},
		retireKey{"new", rig.newPublic, ""})
	rig.mustRefuse("new")
}

func TestWebAssertionKeyRetire_R6RefusedKidLogsClassKidAndRetireAfterOnly(t *testing.T) {
	retireAfter := retireT0.Add(time.Hour)
	rig := newRetireRig(t, retireT0, retireAfter.Format(time.RFC3339))
	rig.clock = retireT0.Add(2 * time.Hour)
	token, err := rig.verify("old")
	if err != ErrInvalidWebAssertion {
		t.Fatalf("err = %v", err)
	}
	if len(rig.logs.records) != 1 {
		t.Fatalf("log records = %d, want 1", len(rig.logs.records))
	}
	record := rig.logs.records[0]
	if record.Level != slog.LevelWarn {
		t.Fatalf("level = %v, want WARN", record.Level)
	}
	attrs := map[string]string{}
	record.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.String(); return true })
	if len(attrs) != 3 || attrs["class"] != "web_assertion_key_retired" || attrs["kid"] != "old" || attrs["retire_after"] != retireAfter.Format(time.RFC3339) {
		t.Fatalf("log attrs = %#v", attrs)
	}
	line := record.Message
	for _, value := range attrs {
		line += " " + value
	}
	for _, secret := range []string{
		base64.RawURLEncoding.EncodeToString(rig.oldPublic), base64.RawURLEncoding.EncodeToString(rig.oldPrivate),
		base64.StdEncoding.EncodeToString(rig.oldPublic), token,
	} {
		if strings.Contains(line, secret) {
			t.Fatal("the log line carries key material or the token")
		}
	}
}

func TestWebAssertionKeyRetire_R6CeilingRefusalLogsTheSameClass(t *testing.T) {
	rig := newRetireRig(t, retireT0, retireT0.Add(25*time.Hour).Format(time.RFC3339))
	rig.mustRefuse("old")
	if len(rig.logs.records) != 1 {
		t.Fatalf("log records = %d, want 1", len(rig.logs.records))
	}
	attrs := map[string]string{}
	rig.logs.records[0].Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.String(); return true })
	if attrs["class"] != "web_assertion_key_retired" || attrs["kid"] != "old" {
		t.Fatalf("log attrs = %#v", attrs)
	}
}

func TestWebAssertionKeyRetire_NoLogForAnAcceptedKid(t *testing.T) {
	rig := newRetireRig(t, retireT0, retireT0.Add(time.Hour).Format(time.RFC3339))
	rig.mustAccept("old")
	rig.mustAccept("new")
	if len(rig.logs.records) != 0 {
		t.Fatalf("log records = %d, want 0", len(rig.logs.records))
	}
}

func TestWebAssertionKeyRetire_R7OverlapCeilingIsTwentyFourHours(t *testing.T) {
	if webAssertionOverlapCeiling != 24*time.Hour {
		t.Fatalf("webAssertionOverlapCeiling = %v, want 24h (ops AssertionOverlapCeiling)", webAssertionOverlapCeiling)
	}
}

func TestWebAssertionKeyRetire_RefusedKidDoesNotReachTheReplayStore(t *testing.T) {
	rig := newRetireRig(t, retireT0, retireT0.Add(time.Hour).Format(time.RFC3339))
	rig.clock = retireT0.Add(time.Hour)
	rig.mustRefuse("old")
	if rig.replays.observed != 0 {
		t.Fatalf("replay store observed %d times for a refused kid", rig.replays.observed)
	}
	rig.mustAccept("new")
	if rig.replays.observed != 1 {
		t.Fatalf("replay store observed %d times, want 1 for the accepted kid", rig.replays.observed)
	}
}

func TestWebAssertionKeyRetire_NoClockSkewOnRetireAfter(t *testing.T) {
	rig := newRetireRig(t, retireT0, retireT0.Add(time.Hour).Format(time.RFC3339))
	rig.clock = retireT0.Add(time.Hour - time.Nanosecond)
	rig.mustAccept("old")
	rig.clock = retireT0.Add(time.Hour)
	rig.mustRefuse("old")
}
