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
	slog.SetDefault(slog.New(Handler(slog.NewJSONHandler(&buf, nil))))
	slog.Info("m", "token", "default-raw-secret")
	if strings.Contains(buf.String(), "default-raw-secret") {
		t.Errorf("package-level call leaked: %s", buf.String())
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
