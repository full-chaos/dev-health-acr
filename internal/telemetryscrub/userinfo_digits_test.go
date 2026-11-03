package telemetryscrub

import (
	"strings"
	"testing"
)

func TestDigitsOnlyPasswordBeforeSlashRedacted(t *testing.T) {
	for _, c := range []struct{ in, leak string }{
		{"postgres://user:12345/ab@h.example/db", "12345"},
		{"redis://u:99999/x9@h.example:6379/0", "99999"},
		{"postgres://user:12345/ab@h.example/db", "ab@"},
	} {
		got := ScrubText(c.in)
		if strings.Contains(got, c.leak) || !strings.Contains(got, Redacted+"@h.example") {
			t.Errorf("userinfo leaked: %q -> %q", c.in, got)
		}
	}
	for _, v := range []string{"https://host:8080/users/@alice", "https://example.org/users/@alice/profile", "http://h:80/a/b", "https://host:8080/a/b?x=1"} {
		if got := ScrubText(v); got != v {
			t.Errorf("ordinary url changed: %q -> %q", v, got)
		}
	}
}
