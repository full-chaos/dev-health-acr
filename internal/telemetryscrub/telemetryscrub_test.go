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
		{"https://h/p?q=foo%20Bearer%20abcdefghijklmnop1234567890", "abcdefghijklmnop1234567890"},
		{"Authorization%3A%20Bearer%20abcdefghijklmnop1234567890", "abcdefghijklmnop1234567890"},
		{"line%0ABearer%20ab12", "ab12"},
		{"x%09Basic%20UjU%3D", "UjU"},
		{"Bearer%C2%A0abcdefghijklmnop1234567890", "abcdefghijklmnop1234567890"},
		{"Bearer%E2%80%A8abcdefghijklmnop1234567890", "abcdefghijklmnop1234567890"},
		{"Bearer%25C2%25A0abcdefghijklmnop1234567890", "abcdefghijklmnop1234567890"},
		{"foo%2520Bearer%2520ab12", "ab12"},
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

func TestSchemeSeparatorsAcceptEveryRawSpaceAndItsEncodings(t *testing.T) {
	raw := []rune{' ', '\t', '\n', '\v', '\f', '\r', 0x85, 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000}
	for r := rune(0x2000); r <= 0x200A; r++ {
		raw = append(raw, r)
	}
	for _, r := range raw {
		var enc strings.Builder
		for _, b := range []byte(string(r)) {
			fmt.Fprintf(&enc, "%%%02X", b)
		}
		forms := []string{string(r), enc.String(), strings.ToLower(enc.String()), strings.ReplaceAll(enc.String(), "%", "%25")}
		for _, sep := range forms {
			for _, in := range []string{"Bearer" + sep + "ab12", "q=x" + sep + "Bearer" + sep + "ab12", "Basic" + sep + "UjU=", "k=x" + sep + "Basic" + sep + "UjU="} {
				if got := ScrubText(in); strings.Contains(got, "ab12") || strings.Contains(got, "UjU") {
					t.Errorf("U+%04X %q: ScrubText(%q) = %q", r, sep, in, got)
				}
			}
		}
	}
}

func TestEncodedBoundaryKeepsThePrefixAndRedactsOnlyTheCredential(t *testing.T) {
	cases := map[string]string{
		"https://h/p?q=foo%20Bearer%20abcdefghijklmnop1234567890": "https://h/p?q=foo%20" + Redacted,
		"line%0ABearer%20ab12": "line%0A" + Redacted,
		"x%09Basic%20UjU%3D":   "x%09" + Redacted,
	}
	for in, want := range cases {
		if got := ScrubText(in); got != want {
			t.Errorf("ScrubText(%q) = %q want %q", in, got, want)
		}
	}
	for _, in := range []string{"foo%20Bearer%20of%20news", "xBearer%20ab12", "unBearer ab12"} {
		if got := ScrubText(in); got != in {
			t.Errorf("clean text changed: %q -> %q", in, got)
		}
	}
}

var typedBenignCases = []struct {
	key  string
	kind string
}{
	{"authorization_dropped_count", "int"}, {"repo_less_authorization_dropped_count", "int"},
	{"authorization_grant_exact_selectors", "int"}, {"authorization_grant_owner_selectors", "int"},
	{"authorization_batches", "int"}, {"authorization_batch_size", "int"},
	{"device_authorizations", "int"}, {"device_authorizations_remaining", "int"},
	{"pass", "int"}, {"evaluated_pass", "int"},
	{"authorization_grant_measured", "bool"}, {"authorization_grant_organization_wide", "bool"},
	{"dev_health_entitlement_token_file_configured", "bool"}, {"coverage_bypass", "bool"},
	{"authorization_reason", "string"},
}

func sampleOfKind(kind string) any {
	switch kind {
	case "int":
		return 7
	case "bool":
		return true
	}
	return "subjects_admitted"
}

func TestTypedBenignKeysKeepTheirOneKind(t *testing.T) {
	for _, c := range typedBenignCases {
		want := sampleOfKind(c.kind)
		got := record(t, func(l *slog.Logger) { l.Info("m", c.key, want) })
		if fmt.Sprint(got[c.key]) != fmt.Sprint(want) {
			t.Errorf("%s=%v want kept %v", c.key, got[c.key], want)
		}
		if BenignKind(c.key) != c.kind {
			t.Errorf("BenignKind(%q) = %q want %q", c.key, BenignKind(c.key), c.kind)
		}
	}
}

func TestTypedBenignKeysRedactEveryOtherKind(t *testing.T) {
	for _, c := range typedBenignCases {
		for _, other := range []string{"int", "bool", "string"} {
			if other == c.kind {
				continue
			}
			v := sampleOfKind(other)
			if other == "string" {
				v = "leaky-value"
			}
			got := record(t, func(l *slog.Logger) { l.Info("m", c.key, v) })
			if got[c.key] != Redacted {
				t.Errorf("%s with %s value = %v want redacted", c.key, other, got[c.key])
			}
		}
		for _, v := range []any{1.5, time.Second} {
			got := record(t, func(l *slog.Logger) { l.Info("m", c.key, v) })
			if got[c.key] != Redacted {
				t.Errorf("%s with %T value = %v want redacted", c.key, v, got[c.key])
			}
		}
	}
}

func TestTypedBenignNearVariantsStayRedacted(t *testing.T) {
	for _, c := range typedBenignCases {
		for _, k := range []string{c.key + "_token", c.key + "_secret", c.key + "_password", c.key + "s_token", "session_" + c.key, "authorization_" + c.key + "_x"} {
			got := record(t, func(l *slog.Logger) { l.Info("m", k, sampleOfKind(c.kind)) })
			if got[k] != Redacted {
				t.Errorf("near variant %q = %v want redacted", k, got[k])
			}
		}
	}
	for _, k := range []string{"authorization_batches_token", "authorization_dropped_count_password", "evaluated_pass_token", "pass_phrase", "request_pass", "pass_token", "authorization", "authorization_header", "credential", "session_id", "otp_count"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", k, 3) })
		if got[k] != Redacted {
			t.Errorf("%q = %v want redacted", k, got[k])
		}
	}
}

func TestTypedBenignKeysNeedNoGroup(t *testing.T) {
	got := record(t, func(l *slog.Logger) { l.WithGroup("g").Info("m", "authorization_batches", 3) })
	if g, _ := got["g"].(map[string]any); g["authorization_batches"] != Redacted {
		t.Errorf("under WithGroup: %v", got["g"])
	}
	got = record(t, func(l *slog.Logger) { l.Info("m", slog.Group("credentials", slog.Int("authorization_batches", 3))) })
	if got["credentials"] != Redacted {
		t.Errorf("under protected group: %v", got["credentials"])
	}
	got = record(t, func(l *slog.Logger) { l.Info("m", slog.Group("span", slog.Int("authorization_batches", 3))) })
	if g, _ := got["span"].(map[string]any); g["authorization_batches"] != Redacted {
		t.Errorf("under clean group: %v", got["span"])
	}
	got = record(t, func(l *slog.Logger) { l.Info("m", "o", map[string]any{"pass": 3, "authorization_batches": 4}) })
	if o, _ := got["o"].(map[string]any); o["pass"] != Redacted || o["authorization_batches"] != Redacted {
		t.Errorf("opaque map: %v", got["o"])
	}
}

func TestTypedBenignStringStillShapeScrubbed(t *testing.T) {
	got := record(t, func(l *slog.Logger) { l.Info("m", "authorization_reason", secretShapedReason) })
	if got["authorization_reason"] == secretShapedReason {
		t.Errorf("a registered value with a secret shape reached the log: %v", got["authorization_reason"])
	}
	if s, _ := got["authorization_reason"].(string); strings.Contains(s, "abcdefghijklmnop") {
		t.Errorf("shape leaked: %q", s)
	}
}

func TestTypedBenignWithAttrsAndRecordAttrs(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(Handler(slog.NewJSONHandler(&buf, nil))).With("pass", 2, "coverage_bypass", true, "pass_x", "v")
	l.Info("m", "evaluated_pass", 5)
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["pass"] != float64(2) || out["coverage_bypass"] != true || out["evaluated_pass"] != float64(5) {
		t.Errorf("got %v", out)
	}
}

var testReasons = []string{"subjects_admitted", "subject_denied", "grant_mismatch"}

const secretShapedReason = "ghp_abcdefghijklmnopqrstuvwx"

func init() {
	RegisterClosedValues("authorization_reason", append(append([]string{}, testReasons...), secretShapedReason))
}

func TestClosedValueKeyRedactsEveryValueOutsideTheSet(t *testing.T) {
	for _, v := range testReasons {
		got := record(t, func(l *slog.Logger) { l.Info("m", "authorization_reason", v) })
		if got["authorization_reason"] != v {
			t.Errorf("%q = %v want kept", v, got["authorization_reason"])
		}
	}
	for _, v := range []string{"", "subjects_admitted ", "Subjects_Admitted", "subjects_admitted2", "Bearer abcdefgh1", "pq: password authentication failed for user x", "Authorization: Bearer abc", "x-api-key: k123", "plain text"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", "authorization_reason", v) })
		if got["authorization_reason"] != Redacted {
			t.Errorf("%q = %v want redacted", v, got["authorization_reason"])
		}
		got = record(t, func(l *slog.Logger) { l.With("authorization_reason", v).Info("m") })
		if got["authorization_reason"] != Redacted {
			t.Errorf("With %q = %v want redacted", v, got["authorization_reason"])
		}
	}
}

func TestClosedValueRegistrationNeedsAStringTypedBenignKey(t *testing.T) {
	for _, key := range []string{"authorization_batches", "unlisted_key", "token"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("RegisterClosedValues(%q) did not panic", key)
				}
			}()
			RegisterClosedValues(key, []string{"a"})
		}()
	}
}

func TestCompactEqualSpellingsAreOneKeyForOneKindOnly(t *testing.T) {
	for _, k := range []string{"AuthorizationBatches", "authorization-batches", "authorization.batches", "AUTHORIZATION_BATCHES", "authorization batches"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", k, 3) })
		if got[k] != float64(3) {
			t.Errorf("%q int = %v want kept", k, got[k])
		}
		got = record(t, func(l *slog.Logger) { l.Info("m", k, "x") })
		if got[k] != Redacted {
			t.Errorf("%q string = %v want redacted", k, got[k])
		}
		got = record(t, func(l *slog.Logger) { l.WithGroup("g").Info("m", k, 3) })
		if g, _ := got["g"].(map[string]any); g[k] != Redacted {
			t.Errorf("%q under group = %v want redacted", k, g[k])
		}
	}
	// A key that is protected by a rule beyond its compact letters stays protected even though those
	// letters equal an exempt key: the joined path of a group and the key is not the exempt key.
	for _, group := range []string{"api", "span", "x"} {
		got := record(t, func(l *slog.Logger) { l.WithGroup(group).Info("m", "pass", 1) })
		if g, _ := got[group].(map[string]any); g["pass"] != Redacted {
			t.Errorf("group %q pass = %v want redacted", group, g["pass"])
		}
	}
}

func registerPanics(key string, values []string) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	RegisterClosedValues(key, values)
	return ""
}

func TestClosedValuesRegisterOnceAndOnlyCodeShapedValues(t *testing.T) {
	if msg := registerPanics("authorization_reason", []string{"another_code"}); !strings.Contains(msg, "already registered") {
		t.Errorf("second registration: %q", msg)
	}
	if msg := registerPanics("AuthorizationReason", testReasons); !strings.Contains(msg, "already registered") {
		t.Errorf("second registration, other spelling: %q", msg)
	}
	for _, bad := range []string{"has space", "has:colon", "a/b", "", "UPPER", strings.Repeat("a", 65), "tab\there", "ünï"} {
		if msg := registerPanics("authorization_reason", []string{"ok_code", bad}); !strings.Contains(msg, "code shape") {
			t.Errorf("value %q: %q", bad, msg)
		}
	}
	if msg := registerPanics("authorization_reason", nil); !strings.Contains(msg, "no closed values") {
		t.Errorf("empty set: %q", msg)
	}
	for _, v := range []string{"another_code", "has space", "x"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", "authorization_reason", v) })
		if got["authorization_reason"] != Redacted {
			t.Errorf("%q = %v after rejected registrations, want redacted", v, got["authorization_reason"])
		}
	}
	if got := record(t, func(l *slog.Logger) { l.Info("m", "authorization_reason", "grant_mismatch") }); got["authorization_reason"] != "grant_mismatch" {
		t.Errorf("registered value changed: %v", got["authorization_reason"])
	}
	if !closedValueShape.MatchString(strings.Repeat("a", 64)) || !closedValueShape.MatchString("a1_b2") {
		t.Error("shape rejects a valid code")
	}
}

type uintValuer uint64

func (u uintValuer) LogValue() slog.Value { return slog.Uint64Value(uint64(u)) }

func TestIntExemptionNeverCoversUnsignedValues(t *testing.T) {
	for _, c := range typedBenignCases {
		if c.kind != "int" {
			continue
		}
		k := c.key
		for name, v := range map[string]any{"uint": uint(41), "uint64": uint64(42), "uint8": uint8(1), "uintptr": uintptr(2), "Uint64Value": slog.Uint64Value(43), "uint valuer": uintValuer(44)} {
			got := record(t, func(l *slog.Logger) { l.Info("m", k, v) })
			if got[k] != Redacted {
				t.Errorf("%s with %s = %v want redacted", k, name, got[k])
			}
		}
		if got := record(t, func(l *slog.Logger) { l.Info("m", k, 5) }); got[k] != float64(5) {
			t.Errorf("%s with int = %v want kept", k, got[k])
		}
		if got := record(t, func(l *slog.Logger) { l.Info("m", k, int64(6)) }); got[k] != float64(6) {
			t.Errorf("%s with int64 = %v want kept", k, got[k])
		}
	}
}

func TestUnnamedGroupsCountAsGroups(t *testing.T) {
	cases := map[string]func(*slog.Logger){
		"empty group": func(l *slog.Logger) {
			l.Info("m", slog.Group("", slog.Int("authorization_batches", 3), slog.Bool("coverage_bypass", true)))
		},
		"nested empty":     func(l *slog.Logger) { l.Info("m", slog.Group("", slog.Group("", slog.Int("pass", 3)))) },
		"With empty group": func(l *slog.Logger) { l.With(slog.Group("", slog.Int("evaluated_pass", 3))).Info("m") },
		"empty then named": func(l *slog.Logger) { l.Info("m", slog.Group("", slog.Group("span", slog.Int("pass", 3)))) },
		"empty group string": func(l *slog.Logger) {
			l.Info("m", slog.Group("", slog.String("authorization_reason", "subjects_admitted")))
		},
	}
	for name, build := range cases {
		got := record(t, build)
		for _, k := range []string{"authorization_batches", "coverage_bypass", "pass", "evaluated_pass", "authorization_reason"} {
			if v, ok := got[k]; ok && v != Redacted {
				t.Errorf("%s: %s = %v want redacted", name, k, v)
			}
		}
		if g, ok := got["span"].(map[string]any); ok && g["pass"] != Redacted {
			t.Errorf("%s: span.pass = %v want redacted", name, g["pass"])
		}
	}
	if got := record(t, func(l *slog.Logger) { l.Info("m", "pass", 3) }); got["pass"] != float64(3) {
		t.Errorf("ungrouped pass = %v want kept", got["pass"])
	}
}
