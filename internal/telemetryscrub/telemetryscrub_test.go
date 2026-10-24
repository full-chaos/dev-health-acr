package telemetryscrub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func record(t *testing.T, build func(*slog.Logger)) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	build(slog.New(Handler(slog.NewJSONHandler(&buf, nil))))
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", buf.String(), err)
	}
	return out
}

func TestProtectedKeysRedacted(t *testing.T) {
	for _, key := range []string{"token", "api_key", "Authorization", "client_secret", "password", "set-cookie", "session_id", "dsn", "signature", "bearer"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", key, "value-1") })
		if got[key] != Redacted {
			t.Errorf("key %q = %v, want %q", key, got[key], Redacted)
		}
	}
}

func TestBenignKeysKept(t *testing.T) {
	got := record(t, func(l *slog.Logger) {
		l.Info("m", "output_tokens", 5, "input_tokens", 6, "max_output_tokens", 7, "packet_tokens", 8, "org_id", "o1", "request_id", "r1")
	})
	for _, key := range []string{"output_tokens", "input_tokens", "max_output_tokens", "packet_tokens", "org_id", "request_id"} {
		if got[key] == Redacted || got[key] == nil {
			t.Errorf("key %q was redacted or lost: %v", key, got[key])
		}
	}
}

func TestValueShapesRedacted(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl"
	for _, v := range []string{
		"Bearer abcdefghijklmnop1234",
		jwt,
		"-----BEGIN PRIVATE KEY-----",
		"postgres://user:pa55@host/db",
		"ghp_abcdefghijklmnopqrstuvwxyz0123",
	} {
		got := record(t, func(l *slog.Logger) { l.Info("m", "note", "x "+v+" y") })
		s, _ := got["note"].(string)
		if !strings.Contains(s, Redacted) || strings.Contains(s, "pa55") || strings.Contains(s, "abcdefghijklmnop") || strings.Contains(s, jwt) || strings.Contains(s, "BEGIN") {
			t.Errorf("value %q not redacted: %q", v, s)
		}
	}
	got := record(t, func(l *slog.Logger) { l.Info("m", "note", "plain text 123") })
	if got["note"] != "plain text 123" {
		t.Errorf("plain value changed: %v", got["note"])
	}
}

func TestErrorValueLoggedAsClassOnly(t *testing.T) {
	secretErr := fmt.Errorf("wrap: %w", errors.New("dial db password=hunter2"))
	got := record(t, func(l *slog.Logger) { l.Error("m", "error", secretErr, "err", errors.New("hunter2")) })
	for _, k := range []string{"error", "err"} {
		s, _ := got[k].(string)
		if strings.Contains(s, "hunter2") || s == "" {
			t.Errorf("%s = %q", k, s)
		}
	}
	if got["error"] != "*fmt.wrapError" {
		t.Errorf("error class = %v", got["error"])
	}
}

func TestRemoteIPTruncated(t *testing.T) {
	cases := map[string]string{
		"203.0.113.77":                           "203.0.113.0/24",
		"2001:db8:1234:5678:9abc:def0:1234:5678": "2001:db8:1234::/48",
		"not-an-ip":                              Redacted,
	}
	for in, want := range cases {
		got := record(t, func(l *slog.Logger) { l.Warn("m", "remote_ip", in) })
		if got["remote_ip"] != want {
			t.Errorf("remote_ip %q = %v, want %q", in, got["remote_ip"], want)
		}
	}
}

func TestGroupsAndWithAttrsScrubbed(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(Handler(slog.NewJSONHandler(&buf, nil))).With("token", "t1").WithGroup("g")
	l.InfoContext(context.Background(), "m", "password", "p1", slog.Group("h", slog.String("secret", "s1"), slog.String("remote_ip", "10.1.2.3")))
	out := buf.String()
	for _, leak := range []string{"t1", "p1", "s1", "10.1.2.3"} {
		if strings.Contains(out, `"`+leak+`"`) || strings.Contains(out, leak+`"`) && leak != "t1" && leak != "p1" && leak != "s1" {
			t.Errorf("leak %q in %s", leak, out)
		}
	}
	if strings.Contains(out, `:"t1"`) || strings.Contains(out, `:"p1"`) || strings.Contains(out, `:"s1"`) {
		t.Errorf("leak in %s", out)
	}
	if !strings.Contains(out, "10.1.2.0/24") {
		t.Errorf("nested remote_ip not truncated: %s", out)
	}
}

func TestEnabledDelegates(t *testing.T) {
	h := Handler(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if h.Enabled(context.Background(), slog.LevelInfo) || !h.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("Enabled does not delegate")
	}
}

func TestErrorClass(t *testing.T) {
	if got := ErrorClass(nil); got != "" {
		t.Errorf("nil = %q", got)
	}
	if got := ErrorClass(errors.New("x password=1")); got != "*errors.errorString" {
		t.Errorf("got %q", got)
	}
}

func raw(t *testing.T, build func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	build(slog.New(Handler(slog.NewJSONHandler(&buf, nil))))
	return buf.String()
}

func TestGroupNamesAndWithGroupArePartOfTheKey(t *testing.T) {
	out := raw(t, func(l *slog.Logger) {
		l.Info("m", slog.Group("api_secret", "value", "group-secret"))
		l.With(slog.Group("token", "v", "with-group-token")).Info("m")
		l.WithGroup("client").With("address", "203.0.113.77").Info("m")
		l.WithGroup("auth_token").Info("m", "plain", "wg-secret")
		l.Info("m", slog.Group("client", "address", "203.0.113.78"))
	})
	for _, leak := range []string{"group-secret", "with-group-token", "203.0.113.77", "203.0.113.78", "wg-secret"} {
		if strings.Contains(out, leak) {
			t.Errorf("leak %q in %s", leak, out)
		}
	}
}

type probeStringer struct{}

func (probeStringer) String() string { return "stringer-secret" }

type probeStruct struct {
	Name     string
	Password string
	Err      error
	hidden   string
}

func TestOpaqueAnyValuesAreWalkedNotFormatted(t *testing.T) {
	out := raw(t, func(l *slog.Logger) {
		l.Info("m",
			"struct", probeStruct{Name: "n", Password: "struct-pw", Err: errors.New("struct raw error"), hidden: "hid"},
			"map", map[string]any{"password": "map-secret", "attempt": 3},
			"stringer", probeStringer{},
			"ptr", &probeStruct{Name: "p"},
			"nilerr", error(nil),
			"list", []any{errors.New("list raw error"), "ok"},
		)
	})
	for _, leak := range []string{"struct-pw", "struct raw error", "map-secret", "stringer-secret", "list raw error", "hid"} {
		if strings.Contains(out, leak) {
			t.Errorf("leak %q in %s", leak, out)
		}
	}
	if !strings.Contains(out, `"attempt":3`) {
		t.Errorf("plain map shape lost: %s", out)
	}
}

func TestSeparatorVariantsOfProtectedKeys(t *testing.T) {
	fragments := []string{"token", "secret", "password", "passwd", "credential", "authorization", "bearer", "cookie", "api_key", "signature", "dsn", "jwt", "session", "private_key"}
	for _, f := range fragments {
		variants := []string{f, strings.ToUpper(f), strings.ReplaceAll(f, "_", "-"), strings.ReplaceAll(f, "_", "."), strings.ReplaceAll(f, "_", ""), "x." + f, f + "_value", "Svc" + strings.ToUpper(f[:1]) + f[1:]}
		for _, key := range variants {
			got := record(t, func(l *slog.Logger) { l.Info("m", key, "variant-secret") })
			if got[key] != Redacted {
				t.Errorf("key %q = %v", key, got[key])
			}
		}
	}
	for _, key := range []string{"windsnow", "handsnap", "org_id", "request_id", "output_tokens"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", key, "ok") })
		if key != "output_tokens" && got[key] != "ok" {
			t.Errorf("key %q = %v, want kept", key, got[key])
		}
	}
}

func TestPEMBlockRedactedWhole(t *testing.T) {
	for _, v := range []string{
		"a -----BEGIN PRIVATE KEY-----\nMIIE-body-secret-material\n-----END PRIVATE KEY----- z",
		"a -----BEGIN RSA PRIVATE KEY-----\nMIIE-body-secret-material",
	} {
		out := raw(t, func(l *slog.Logger) { l.Info("m", "note", v) })
		if strings.Contains(out, "MIIE") || strings.Contains(out, "END PRIVATE") {
			t.Errorf("pem material in %s", out)
		}
	}
}

func TestMessageIsScrubbed(t *testing.T) {
	out := raw(t, func(l *slog.Logger) { l.Info("failed with Bearer abcdefghijklmnop1234") })
	if strings.Contains(out, "abcdefghijklmnop") {
		t.Errorf("message leak: %s", out)
	}
}

func TestDefaultLoggerCanBeScrubbed(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	InstallDefault(&buf, slog.LevelInfo)
	slog.Info("m", "token", "default-raw-secret", "remote_ip", "203.0.113.77")
	if strings.Contains(buf.String(), "default-raw-secret") || strings.Contains(buf.String(), "203.0.113.77") {
		t.Errorf("package-level call leaked: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "203.0.113.0/24") {
		t.Errorf("default logger is not the scrubber: %s", buf.String())
	}
}

func TestScrubIsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(Handler(Handler(slog.NewJSONHandler(&buf, nil))))
	l.Info("m", "remote_ip", "203.0.113.77", "error", errors.New("x"))
	if !strings.Contains(buf.String(), `"remote_ip":"203.0.113.0/24"`) || strings.Contains(buf.String(), "REDACTED") {
		t.Errorf("double scrub changed output: %s", buf.String())
	}
}

func TestCleanRecordPassesThroughUnchanged(t *testing.T) {
	var got slog.Record
	h := Handler(captureHandler{rec: &got})
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "plain", 0)
	r.AddAttrs(slog.String("org_id", "o1"), slog.Int("n", 1))
	if err := h.Handle(context.Background(), r); err != nil || got.NumAttrs() != 2 {
		t.Fatalf("err=%v attrs=%d", err, got.NumAttrs())
	}
	values := map[string]string{}
	got.Attrs(func(a slog.Attr) bool { values[a.Key] = a.Value.String(); return true })
	if values["org_id"] != "o1" || values["n"] != "1" {
		t.Fatalf("values changed: %v", values)
	}
}

type captureHandler struct{ rec *slog.Record }

func (captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c captureHandler) Handle(_ context.Context, r slog.Record) error {
	*c.rec = r
	return nil
}
func (c captureHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c captureHandler) WithGroup(string) slog.Handler      { return c }

func BenchmarkHandleCleanRecord(b *testing.B) {
	h := Handler(captureHandler{rec: new(slog.Record)})
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "plain message", 0)
	r.AddAttrs(slog.String("org_id", "o1"), slog.String("request_id", "r1"), slog.Int("n", 1))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = h.Handle(context.Background(), r)
	}
}

func TestKeyClass(t *testing.T) {
	cases := []struct {
		key        string
		prot, netw bool
	}{
		{"api.key", true, false}, {"Authorization", true, false}, {"output_tokens", false, false},
		{"org_id", false, false}, {"remote_ip", false, true}, {"client.address", false, true}, {"handsnap", false, false},
	}
	for _, c := range cases {
		if p, n := KeyClass(c.key); p != c.prot || n != c.netw {
			t.Errorf("KeyClass(%q) = %v,%v want %v,%v", c.key, p, n, c.prot, c.netw)
		}
	}
}

func TestScrubText(t *testing.T) {
	if got := ScrubText("a Bearer abcdefghijklmnop1234 b"); strings.Contains(got, "abcdefghijklmnop") || !strings.Contains(got, Redacted) {
		t.Errorf("got %q", got)
	}
	if got := ScrubText("plain text"); got != "plain text" {
		t.Errorf("got %q", got)
	}
	if got := ScrubText("-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----"); strings.Contains(got, "MIIE") {
		t.Errorf("got %q", got)
	}
}

func TestNetworkGroupNamesTaintChildren(t *testing.T) {
	out := raw(t, func(l *slog.Logger) {
		l.Info("m", slog.Group("client_ip", "v4", "203.0.113.77"))
		l.WithGroup("remote_ip").Info("v", "addr", "198.51.100.9")
		l.With(slog.Group("remote_ip", slog.Group("inner", "x", "192.0.2.55"))).Info("m")
	})
	for _, leak := range []string{"203.0.113.77", "198.51.100.9", "192.0.2.55"} {
		if strings.Contains(out, leak) {
			t.Errorf("leak %q in %s", leak, out)
		}
	}
	if !strings.Contains(out, "203.0.113.0/24") {
		t.Errorf("prefix missing: %s", out)
	}
}

type dbConn struct {
	DBPass   string `json:"password"`
	Alias    string `json:"api_key"`
	PeerAddr string `json:"remote_ip"`
	Host     string
}

func TestOpaqueWalkAppliesNetworkAndTagRules(t *testing.T) {
	out := raw(t, func(l *slog.Logger) {
		l.Info("m",
			"conn", dbConn{DBPass: "tag-pw", Alias: "tag-key", PeerAddr: "203.0.113.99", Host: "h1"},
			"peers", map[string]any{"remote_ip": "198.51.100.77", "n": 1},
		)
	})
	for _, leak := range []string{"tag-pw", "tag-key", "203.0.113.99", "198.51.100.77"} {
		if strings.Contains(out, leak) {
			t.Errorf("leak %q in %s", leak, out)
		}
	}
	if !strings.Contains(out, `"Host":"h1"`) || !strings.Contains(out, "203.0.113.0/24") {
		t.Errorf("ordinary field lost or prefix missing: %s", out)
	}
}

func TestExtraFragmentsAndFalsePositives(t *testing.T) {
	for _, key := range []string{"pwd", "db_pwd", "pass", "DBPass", "dbpass", "passphrase", "signing_key", "signingKey", "encryption_key", "aws_access_key_id", "auth", "basic_auth", "proxy.auth"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", key, "frag-secret") })
		if got[key] != Redacted {
			t.Errorf("key %q = %v", key, got[key])
		}
	}
	for _, key := range []string{"author", "passenger", "bypass", "compass", "auth_outcome", "authority", "pass_outcome"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", key, "keep") })
		if got[key] != "keep" {
			t.Errorf("key %q = %v, want kept", key, got[key])
		}
	}
}

func TestValueShapesCaseAndUserinfo(t *testing.T) {
	for _, v := range []string{
		"x beaRer abcdefghijklmnop1234",
		"redis://:PW1@host:6379/0",
		"https://user:pa/ss1@h.example/x",
		"https://u:p@ssPW1@h.example",
		"Authorization: Basic dXNlcjpwYXNzd29yZA==",
		"key AKIAABCDEFGHIJKLMNOP end",
		"key ASIAABCDEFGHIJKLMNOP end",
		"dial failed password=hunter2 host=x",
		"cfg SECRET=s3cr3t;",
		"Token: abc123xyz",
	} {
		for _, got := range []string{ScrubText(v), func() string {
			o := record(t, func(l *slog.Logger) { l.Info("m", "note", v) })
			s, _ := o["note"].(string)
			return s
		}()} {
			for _, leak := range []string{"PW1", "dXNlcjpwYXNz", "AKIAABCDEFGHIJKLMNOP", "ASIAABCDEFGHIJKLMNOP", "hunter2", "s3cr3t", "abc123xyz", "abcdefghijklmnop", "pa/ss1"} {
				if strings.Contains(got, leak) {
					t.Errorf("value %q leaks %q: %q", v, leak, got)
				}
			}
			if !strings.Contains(got, Redacted) {
				t.Errorf("value %q not redacted: %q", v, got)
			}
		}
	}
	if got := ScrubText("see https://example.org/a/b now"); got != "see https://example.org/a/b now" {
		t.Errorf("plain url changed: %q", got)
	}
}

func TestByteArraysElided(t *testing.T) {
	out := raw(t, func(l *slog.Logger) { l.Info("m", "key", [4]byte{1, 2, 3, 4}, "id", [16]byte{9}) })
	if strings.Contains(out, "[1,2,3,4]") || strings.Count(out, "[bytes]") != 2 {
		t.Errorf("byte arrays not elided: %s", out)
	}
}

func TestNumbersAndFlagsUnderProtectedKeysAreRedacted(t *testing.T) {
	keys := []string{"token", "access_token", "session_id", "authorization", "credential_id", "otp", "pin", "mfa_code", "passcode", "verification_code", "auth_code", "otp_code", "access_key", "authKey", "auth_key"}
	for _, k := range keys {
		got := record(t, func(l *slog.Logger) { l.Info("m", k, 123456, "force", "Bearer abcdefghijklmnop1234") })
		if got[k] != Redacted {
			t.Errorf("number under %q = %v", k, got[k])
		}
		mapped := raw(t, func(l *slog.Logger) { l.Info("m", "payload", map[string]any{k: 987654}) })
		if strings.Contains(mapped, "987654") {
			t.Errorf("number under map key %q leaked: %s", k, mapped)
		}
	}
	for _, k := range []string{"status_code", "error_code", "exit_code", "estimated_tokens", "output_tokens", "pass_pages", "spinner", "mapping"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", k, 7, "force", "Bearer abcdefghijklmnop1234") })
		if got[k] != float64(7) {
			t.Errorf("clean key %q = %v", k, got[k])
		}
	}
}

func TestKeyValuePairsInText(t *testing.T) {
	for _, v := range []string{
		"GET /x?access_token=AT123456&b=1",
		"client_secret=CS123456 ok",
		"dial db_password=DBPW123 host=h",
		`{"api_key":"JSONKEY123","n":1}`,
		"login pass=PASS123 now",
		"cfg 'otp': '445566'",
		"x session_id=SID123",
		"Authorization: Bearer TOKTOKTOK1",
	} {
		for _, got := range []string{ScrubText(v), func() string {
			o := record(t, func(l *slog.Logger) { l.Info("m", "note", v) })
			s, _ := o["note"].(string)
			return s
		}(), func() string { o := record(t, func(l *slog.Logger) { l.Info(v) }); s, _ := o["msg"].(string); return s }()} {
			for _, leak := range []string{"AT123456", "CS123456", "DBPW123", "JSONKEY123", "PASS123", "445566", "SID123", "TOKTOKTOK1"} {
				if strings.Contains(got, leak) {
					t.Errorf("%q leaks %q: %q", v, leak, got)
				}
			}
		}
	}
	for _, v := range []string{"status_code=200 path=/a", "time 12:30:45 ok", "error: failed", `{"n":1,"name":"x"}`} {
		if got := ScrubText(v); got != v {
			t.Errorf("clean text changed: %q -> %q", v, got)
		}
	}
}

func TestBasicAuthWithTab(t *testing.T) {
	out := ScrubText("Authorization:\tBasic\tdXNlcjpwYXNzd29yZA==")
	if strings.Contains(out, "dXNlcjpwYXNz") {
		t.Errorf("tab separated basic leaked: %q", out)
	}
	got := record(t, func(l *slog.Logger) { l.Info("m", "note", "x Basic\tdXNlcjpwYXNzd29yZA==") })
	if s, _ := got["note"].(string); strings.Contains(s, "dXNlcjpwYXNz") {
		t.Errorf("attr leaked: %q", s)
	}
}

func TestNestedMapPathsAreJoined(t *testing.T) {
	out := raw(t, func(l *slog.Logger) {
		l.Info("m", "peer", map[string]any{"client": map[string]any{"ip": "203.0.113.55"}}, slog.Any("x", map[string]any{"remote": map[string]any{"addr": "198.51.100.5"}}))
		l.Info("m", "auth", map[string]any{"deep": map[string]any{"v": "deep-secret"}})
	})
	for _, leak := range []string{"203.0.113.55", "198.51.100.5", "deep-secret"} {
		if strings.Contains(out, leak) {
			t.Errorf("leak %q in %s", leak, out)
		}
	}
}

func TestURLQueryParametersAreScrubbed(t *testing.T) {
	urls := []string{
		"https://h/cb?access_token=SEKRET4&x=1",
		"https://h/cb?x=1&client_secret=SEKRET5",
		"https://u:SEKRET6@h/cb?access_token=SEKRET7&y=2",
	}
	leaks := []string{"SEKRET4", "SEKRET5", "SEKRET6", "SEKRET7"}
	for _, u := range urls {
		carriers := map[string]string{
			"text": ScrubText(u),
			"value": func() string {
				o := record(t, func(l *slog.Logger) { l.Info("m", "note", u) })
				s, _ := o["note"].(string)
				return s
			}(),
			"msg":    func() string { o := record(t, func(l *slog.Logger) { l.Info(u) }); s, _ := o["msg"].(string); return s }(),
			"map":    raw(t, func(l *slog.Logger) { l.Info("m", "payload", map[string]any{"url": u}) }),
			"nested": raw(t, func(l *slog.Logger) { l.Info("m", "payload", map[string]any{"a": map[string]any{"b": []any{u}}}) }),
		}
		for name, got := range carriers {
			for _, leak := range leaks {
				if strings.Contains(got, leak) {
					t.Errorf("%s carrier leaks %s for %q: %s", name, leak, u, got)
				}
			}
		}
	}
	if got := ScrubText("https://h/cb?x=1&y=2"); got != "https://h/cb?x=1&y=2" {
		t.Errorf("clean url changed: %q", got)
	}
}

type valuerErr struct{}

func (valuerErr) Error() string        { return "valuer-error-secret" }
func (valuerErr) LogValue() slog.Value { return slog.StringValue("logvaluer-error-secret") }

func TestR2JoinedPathsLogValuerErrorsPrefixesAndEncodedKeys(t *testing.T) {
	out := raw(t, func(l *slog.Logger) {
		l.Info("m", slog.Group("api", "key", "group-path-secret"))
		l.Info("m", "payload", map[string]any{"api": map[string]any{"key": "map-path-secret"}})
		l.Info("m", "custom_error", valuerErr{})
		l.Info("m", "remote_ip", "203.0.113.77/32", "peer_ip", "x")
		l.Info("m", "note", "https://example.test/cb?access%5Ftoken=QUERY-SECRET-1&api%5Fkey=QUERY-SECRET-2&ok=1")
	})
	for _, leak := range []string{"group-path-secret", "map-path-secret", "logvaluer-error-secret", "valuer-error-secret", "203.0.113.77", "QUERY-SECRET-1", "QUERY-SECRET-2"} {
		if strings.Contains(out, leak) {
			t.Errorf("leak %q in %s", leak, out)
		}
	}
	if !strings.Contains(out, "203.0.113.0/24") || !strings.Contains(out, "telemetryscrub.valuerErr") {
		t.Errorf("expected prefix and error class: %s", out)
	}
}

func TestR2FalsePositivesStayClean(t *testing.T) {
	for _, v := range []string{"https://example.org/users/@alice/profile", "https://host:8080/users/@alice", "see https://example.org/a"} {
		if got := ScrubText(v); got != v {
			t.Errorf("ordinary url changed: %q -> %q", v, got)
		}
	}
	for _, v := range []string{"redis://:PW1@host:6379/0", "https://user:pa/ss1@h.example/x", "https://u:p@ssPW1@h.example"} {
		if got := ScrubText(v); strings.Contains(got, "PW1") || strings.Contains(got, "pa/ss1") {
			t.Errorf("userinfo leaked: %q -> %q", v, got)
		}
	}
	for _, k := range []string{"secretary", "secretaries", "Secretary", "SECRETARIES"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", k, "keep") })
		if got[k] != "keep" {
			t.Errorf("%q = %v", k, got[k])
		}
	}
	for _, k := range []string{"secret", "api_secret", "clientSecret", "secrets_ref"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", k, "v") })
		if got[k] != Redacted {
			t.Errorf("%q = %v", k, got[k])
		}
	}
}

func TestR2ExactBenignKeysOnly(t *testing.T) {
	for _, k := range []string{"secret_args", "secretArray", "secret_arn", "secretary_token", "refresh_tokens", "auth_tokens", "api_tokens", "access_tokens", "session_tokens", "tokens_secret"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", k, "leaky-value") })
		if got[k] != Redacted {
			t.Errorf("%q = %v, want redacted", k, got[k])
		}
	}
	for _, k := range []string{"estimated_tokens", "output_tokens", "input_tokens", "total_tokens", "prompt_tokens", "completion_tokens", "cached_input_tokens", "reasoning_output_tokens", "max_tokens", "total_estimated_tokens", "secretary", "secretaries"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", k, 42) })
		if got[k] != float64(42) {
			t.Errorf("%q = %v, want kept", k, got[k])
		}
	}
}

func TestShortSchemeCredentialsRedacted(t *testing.T) {
	cases := []struct{ in, leak string }{
		{"Authorization: Bearer abc", "abc"},
		{"authorization=Bearer a1", "a1"},
		{"proxy-authorization: Bearer x9", "x9"},
		{"msg token=Bearer ab12", "ab12"},
		{"Authorization: Basic abc", "abc"},
		{"Authorization:\tBasic\tUjU=", "UjU="},
		{"sent Bearer ab12 now", "ab12"},
		{"sent Bearer a.b-c now", "a.b-c"},
		{"sent Bearer abcdefgh now", "abcdefgh"},
		{"Basic\tUjU=", "UjU="},
		{"sent Basic ab+1 now", "ab+1"},
		{"sent Basic abcdefgh now", "abcdefgh"},
		{"Bearer Bearer ab12", "ab12"},
		{"Basic Basic YWJjMTIz", "YWJjMTIz"},
		{"Bearer Basic ab12", "ab12"},
		{"Authorization: Bearer Bearer ab12", "ab12"},
		{"Authorization: Basic Basic YWJjMTIz", "YWJjMTIz"},
		{"Authorization: Bearer \"ab12\"", "ab12"},
		{"authorization=Bearer 'ab12'", "ab12"},
		{"Authorization: Basic \"UjU=\"", "UjU="},
		{"Bearer\vab12", "ab12"},
		{"Basic\vUjU=", "UjU="},
		{"Bearer\u00a0ab12", "ab12"},
		{"Bearer\u2003ab12", "ab12"},
		{"Basic\u3000UjU=", "UjU="},
		{"Authorization:\vBearer\vab12", "ab12"},
		{"Authorization: Bearer\u00a0ab12", "ab12"},
		{"Authorization: Bearer Bearer abcd", "abcd"},
		{"Authorization: Basic Basic abcdefg", "abcdefg"},
		{"Authorization: Bearer Bearer Bearer abcd", "abcd"},
		{"Authorization: Bearer Bearer \"abcd\"", "abcd"},
		{"Authorization: Bearer 'ab'abc12", "abc12"},
		{"Authorization: [REDACTED]abc12", "abc12"},
		{"https://host/path?q=Bearer%20ab12", "ab12"},
		{"https://host/path?q=Basic%20UjU%3D", "UjU"},
		{"Bearer%09ab12", "ab12"},
		{"Bearer%2520ab12", "ab12"},
		{"x=Bearer%0Aab12", "ab12"},
		{"q=Bearer+ab12", "ab12"},
	}
	for _, c := range cases {
		if got := ScrubText(c.in); strings.Contains(got, c.leak) || !strings.Contains(got, Redacted) {
			t.Errorf("ScrubText(%q) = %q", c.in, got)
		}
		rec := record(t, func(l *slog.Logger) { l.Info(c.in, "note", c.in) })
		note, ok := rec["note"].(string)
		if !ok || strings.Contains(note, c.leak) {
			t.Errorf("attr for %q = %v", c.in, rec["note"])
		}
		msg, ok := rec["msg"].(string)
		if !ok || strings.Contains(msg, c.leak) {
			t.Errorf("message for %q = %v", c.in, rec["msg"])
		}
	}
}

func TestShortSchemeFloorAndControls(t *testing.T) {
	for _, v := range []string{
		"Bearer of news", "the bearer token", "bearer bonds", "Bearer Smith", "Bearer of the ring",
		"Bearer abcdefg", "Bearer%20of%20news", "Basic%20abcd", "Basic\tabcd", "Basic abcdefg", "Bearer a1b", "basic train", "author: Bearer",
	} {
		if got := ScrubText(v); got != v {
			t.Errorf("clean text changed: %q -> %q", v, got)
		}
	}
	for _, v := range []string{"Bearer a1b2", "Bearer abcdefgh", "Basic ab=c", "Basic abcdefgh"} {
		if got := ScrubText(v); got != Redacted {
			t.Errorf("ScrubText(%q) = %q", v, got)
		}
	}
}

func TestSchemeRedactionIsIdempotent(t *testing.T) {
	for _, in := range []string{"Authorization: Bearer ab12", "Authorization: Bearer Bearer ab12", "sent Bearer ab12 now", "Authorization: Basic UjU="} {
		once := ScrubText(in)
		if twice := ScrubText(once); twice != once {
			t.Errorf("%q: once %q twice %q", in, once, twice)
		}
	}
	if got := ScrubText("Authorization: Bearer ab12"); got != "Authorization: "+Redacted {
		t.Errorf("got %q", got)
	}
}
