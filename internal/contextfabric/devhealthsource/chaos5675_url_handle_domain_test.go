package devhealthsource

import "testing"

// THE PARSER'S WHOLE INPUT DOMAIN, executed in one pass. projectURLHandle
// reads a provider-supplied string and turns it into a retrieval handle, so
// every shape that string can take is a cell: absent, empty, whitespace, not a
// URL, no path, a trailing slash, a segment that is only the id, and each
// boundary of the twelve-hex suffix rule on both sides.
func TestProjectURLHandleOverItsWholeInputDomain(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		cell string
		in   string
		want string
	}{
		{"absent", "", ""},
		{"whitespace only", "   ", ""},
		{"a bare slash", "/", ""},
		{"no path at all", "https://linear.app", ""},
		{"host with a trailing slash", "https://linear.app/", ""},
		{"not a URL, no separator", "chaos-draw", ""},
		// A SCHEME WITHOUT A HOST is the only shape the host check alone
		// rejects: the scheme check passes it and the path is non-empty, so
		// without that check these publish a handle taken from a local path.
		{"a scheme with no host: file", "file:///srv/projects/chaos-draw", ""},
		{"a scheme with no host: mailto", "mailto:someone@example.com", ""},
		{"a scheme with no host and one segment", "file:///chaos-draw", ""},

		{"linear: slug and hex id", "https://linear.app/fullchaos/project/chaos-draw-0d9bd4168c10", "chaos-draw"},
		{"linear: long multi-word slug", "https://linear.app/fullchaos/project/sync-observability-ux-coverage-gaps-and-usable-config-operations-c40da9b04fba", "sync-observability-ux-coverage-gaps-and-usable-config-operations"},
		{"linear: trailing slash", "https://linear.app/fullchaos/project/chaos-draw-0d9bd4168c10/", "chaos-draw"},
		{"linear: surrounding whitespace", "  https://linear.app/fullchaos/project/chaos-draw-0d9bd4168c10  ", "chaos-draw"},

		{"gitlab: no id suffix", "https://gitlab.com/full.chaos/chaos-ops", "chaos-ops"},
		{"jira-shaped: last segment is the key", "https://fullchaos.atlassian.net/browse/BILL", "BILL"},

		{"eleven hex: one short, kept whole", "https://x.dev/p/slug-0d9bd4168c1", "slug-0d9bd4168c1"},
		{"thirteen hex: one long, kept whole", "https://x.dev/p/slug-0d9bd4168c10a", "slug-0d9bd4168c10a"},
		{"twelve chars but not hex", "https://x.dev/p/slug-0d9bd4168cZZ", "slug-0d9bd4168cZZ"},
		{"twelve hex, uppercase: not the shape", "https://x.dev/p/slug-0D9BD4168C10", "slug-0D9BD4168C10"},
		{"twelve hex with no hyphen before it", "https://x.dev/p/slug0d9bd4168c10", "slug0d9bd4168c10"},
		{"the segment is ONLY a hyphen and an id", "https://x.dev/p/-0d9bd4168c10", ""},
		{"the segment is only an id, no hyphen", "https://x.dev/p/0d9bd4168c10", "0d9bd4168c10"},

		{"a name that genuinely ends in twelve hex", "https://x.dev/p/release-0123456789ab", "release"},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			if got := projectURLHandle(testCase.in); got != testCase.want {
				t.Fatalf("projectURLHandle(%q) = %q, want %q", testCase.in, got, testCase.want)
			}
		})
	}
}

// A handle shaped like the HOST is shared by every project on the provider, so
// one term would match all of them.
func TestTheHandleIsNeverAFragmentOfTheHost(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"https://linear.app", "linear.app", "", "   "} {
		if got := projectURLHandle(raw); got != "" {
			t.Fatalf("projectURLHandle(%q) = %q, want empty", raw, got)
		}
	}
}

// distinctNonEmpty is the guard that keeps an absent handle out of the list and
// a handle equal to the key from appearing twice; the producer relies on both.
func TestTheAliasListDropsAnAbsentOrDuplicateHandle(t *testing.T) {
	t.Parallel()
	if got := distinctNonEmpty("BILL", ""); len(got) != 1 || got[0] != "BILL" {
		t.Fatalf("aliases = %v, want the key alone when the URL yields no handle", got)
	}
	if got := distinctNonEmpty("", ""); got != nil {
		t.Fatalf("aliases = %v, want nil when a project has neither a key nor a handle", got)
	}
	if got := distinctNonEmpty("chaos-ops", "chaos-ops"); len(got) != 1 {
		t.Fatalf("aliases = %v, want one entry when the key and the handle are the same spelling", got)
	}
}
