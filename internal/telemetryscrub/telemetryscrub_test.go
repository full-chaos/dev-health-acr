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
