package directread

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTemporalScalarReasonForms(t *testing.T) {
	for _, tc := range []struct{ typ, in, want string }{
		{"DateTime", "2026-09-08T00:00:00Z", ""},
		{"DateTime", "2026-09-08", "got a date"},
		{"DateTime", "soon", "not a timestamp"},
		{"Date", "2026-09-08", ""},
		{"Date", "2026-09-08T00:00:00Z", "got a timestamp"},
		{"Date", "soon", "not a date"},
		{"String", "anything", ""},
		{"ID", "2026-09-08", ""},
	} {
		got := temporalScalarReason("input.x", tc.typ, tc.in)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) || (got != "" && !strings.HasPrefix(got, "input.x: "+tc.typ)) {
			t.Fatalf("%s %q: %q", tc.typ, tc.in, got)
		}
	}
}

func TestUpstreamGraphQLEntriesBounds(t *testing.T) {
	var raw []json.RawMessage
	for i := 0; i < 7; i++ {
		raw = append(raw, json.RawMessage(`{"message":"`+strings.Repeat("é", 600)+`","path":["a",1,"b"]}`))
	}
	got := upstreamGraphQLEntries(raw)
	if len(got) != 5 {
		t.Fatalf("entries %d", len(got))
	}
	if n := len([]rune(got[0].Message)); n != 512 {
		t.Fatalf("message runes %d", n)
	}
	if got[0].Path != "a.1.b" || got[0].Class != UpstreamGraphQLErrors {
		t.Fatalf("%+v", got[0])
	}
}
