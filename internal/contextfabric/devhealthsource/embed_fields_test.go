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

// TestRepositoryBareNameAlias pins CHAOS-3884 Part A's bare-name derivation
// rule: the last "/"-delimited segment of an org-qualified slug, "" for an
// unqualified slug (no redundant self-alias) or a non-ASCII-alphabet
// result (the identity_norm_v1 premise-monitoring exclusion).
func TestRepositoryBareNameAlias(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		slug string
		want string
	}{
		{name: "org-qualified slug", slug: "full-chaos/dev-health-acr", want: "dev-health-acr"},
		{name: "gitlab dotted owner", slug: "full.chaos/dev-health-ops", want: "dev-health-ops"},
		{name: "unqualified slug derives no redundant self-alias", slug: "dev-health-acr", want: ""},
		{name: "empty slug derives no alias", slug: "", want: ""},
		{name: "trailing slash derives no alias", slug: "full-chaos/", want: ""},
		{name: "non-ASCII bare name is excluded (premise monitoring)", slug: "full-chaos/dév-health", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := repositoryBareNameAlias(tc.slug); got != tc.want {
				t.Fatalf("repositoryBareNameAlias(%q) = %q, want %q", tc.slug, got, tc.want)
			}
		})
	}
}

// TestRepositoryProviderAlias pins CHAOS-3884 Part A's provider-variant
// derivation rule: "<provider>:<slug>", "" when provider is unset or the
// composed value fails the ASCII-alphabet premise-monitoring gate.
func TestRepositoryProviderAlias(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		provider, slug string
		want           string
	}{
		{name: "github", provider: "github", slug: "full-chaos/dev-health-acr", want: "github:full-chaos/dev-health-acr"},
		{name: "gitlab", provider: "gitlab", slug: "full.chaos/dev-health-ops", want: "gitlab:full.chaos/dev-health-ops"},
		{name: "unset provider derives no alias", provider: "", slug: "full-chaos/dev-health-acr", want: ""},
		{name: "non-ASCII slug is excluded (premise monitoring)", provider: "github", slug: "full-chaos/dév-health", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := repositoryProviderAlias(tc.provider, tc.slug); got != tc.want {
				t.Fatalf("repositoryProviderAlias(%q, %q) = %q, want %q", tc.provider, tc.slug, got, tc.want)
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
