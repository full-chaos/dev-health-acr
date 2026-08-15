package devhealthsource

import "testing"

// TestTicketKeyAlias pins the EXACT first-colon parse rule the embed-text
// spec (§2, review R2) requires unit tests for: linear-prefixed,
// jira-prefixed, extra-colon, and colon-free ids.
func TestTicketKeyAlias(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		workItemID string
		want       string
	}{
		{name: "linear prefixed", workItemID: "linear:CHAOS-100", want: "CHAOS-100"},
		{name: "jira prefixed", workItemID: "jira:ABC-1", want: "ABC-1"},
		{name: "extra colons keep the remainder intact", workItemID: "linear:CHAOS-100:extra", want: "CHAOS-100:extra"},
		{name: "colon-free id derives no alias", workItemID: "CHAOS-100", want: ""},
		{name: "empty id derives no alias", workItemID: "", want: ""},
		{name: "leading colon derives the verbatim remainder", workItemID: ":CHAOS-100", want: "CHAOS-100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ticketKeyAlias(tc.workItemID); got != tc.want {
				t.Fatalf("ticketKeyAlias(%q) = %q, want %q", tc.workItemID, got, tc.want)
			}
		})
	}
}

// TestJoinedSortedListIsOrderInsensitive pins the §2 determinism rule: an
// unordered source array must never produce two different texts for the
// same row -- deduplicated, sorted, element-capped, per-element rune-capped.
func TestJoinedSortedListIsOrderInsensitive(t *testing.T) {
	t.Parallel()
	a := joinedSortedList([]string{"infra", "bug", "  bug ", "api"}, 10, 40, ", ")
	b := joinedSortedList([]string{"api", "infra", "bug"}, 10, 40, ", ")
	if a != b {
		t.Fatalf("order/duplicate variance changed the joined text: %q vs %q", a, b)
	}
	if a != "api, bug, infra" {
		t.Fatalf("joined text = %q, want sorted deduplicated join", a)
	}
	if got := joinedSortedList([]string{"c", "b", "a"}, 2, 40, ", "); got != "a, b" {
		t.Fatalf("element cap applied after sort = %q, want %q", got, "a, b")
	}
	if got := joinedSortedList([]string{"abcdef"}, 10, 3, " "); got != "abc" {
		t.Fatalf("per-element rune cap = %q, want %q", got, "abc")
	}
	if got := joinedSortedList(nil, 10, 40, ", "); got != "" {
		t.Fatalf("empty input = %q, want empty", got)
	}
}

// TestParsedRepoTags pins the repos.tags handling: a JSON-rendered string
// array parses to a sorted space-joined value; anything else yields ""
// rather than leaking raw JSON into search text.
func TestParsedRepoTags(t *testing.T) {
	t.Parallel()
	if got := parsedRepoTags(`["github","Go"]`); got != "Go github" {
		t.Fatalf("parsedRepoTags = %q, want %q", got, "Go github")
	}
	for _, malformed := range []string{"", "   ", "not-json", `{"a":1}`, `[1,2]`} {
		if got := parsedRepoTags(malformed); got != "" {
			t.Fatalf("parsedRepoTags(%q) = %q, want empty", malformed, got)
		}
	}
}
