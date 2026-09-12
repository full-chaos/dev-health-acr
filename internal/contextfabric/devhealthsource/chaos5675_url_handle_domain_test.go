package devhealthsource

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// THE PARSER'S WHOLE INPUT DOMAIN, executed in one pass. projectURLHandle
// reads a provider-supplied string and turns it into a retrieval handle, so
// every shape that string can take is a cell: absent, empty, whitespace, not a
// URL, no path, a trailing slash, a segment that is only the id, and each
// boundary of the twelve-hex suffix rule on both sides.
func TestProjectURLHandleOverItsWholeInputDomain(t *testing.T) {
	t.Parallel()
	long512 := strings.Repeat("a", 512)
	long513 := strings.Repeat("a", 513)
	for _, testCase := range []struct {
		cell     string
		provider string
		in       string
		want     string
	}{
		{"absent", "linear", "", ""},
		{"whitespace only", "linear", "   ", ""},
		{"a bare slash", "linear", "/", ""},
		{"no path at all", "linear", "https://linear.app", ""},
		{"host with a trailing slash", "linear", "https://linear.app/", ""},
		{"not a URL, no separator", "linear", "chaos-draw", ""},
		// A SCHEME WITHOUT A HOST is the only shape the host check alone
		// rejects: the scheme check passes it and the path is non-empty.
		{"a scheme with no host: file", "linear", "file:///srv/projects/chaos-draw", ""},
		{"a scheme with no host: mailto", "linear", "mailto:someone@example.com", ""},
		{"a scheme with no host and one segment", "linear", "file:///chaos-draw", ""},

		{"linear: slug and hex id", "linear", "https://linear.app/fullchaos/project/chaos-draw-0d9bd4168c10", "chaos-draw"},
		{"linear: long multi-word slug", "linear", "https://linear.app/fullchaos/project/sync-observability-ux-coverage-gaps-and-usable-config-operations-c40da9b04fba", "sync-observability-ux-coverage-gaps-and-usable-config-operations"},
		{"linear: trailing slash", "linear", "https://linear.app/fullchaos/project/chaos-draw-0d9bd4168c10/", "chaos-draw"},
		{"linear: surrounding whitespace", "linear", "  https://linear.app/fullchaos/project/chaos-draw-0d9bd4168c10  ", "chaos-draw"},
		{"gitlab: no id suffix", "gitlab", "https://gitlab.com/full.chaos/chaos-ops", "chaos-ops"},
		{"jira-shaped: last segment is the key", "jira", "https://fullchaos.atlassian.net/browse/BILL", "BILL"},

		// THE STRIP IS SCOPED TO THE PROVIDER WHOSE SUFFIX IS AN ID.
		{"gitlab: a name that ends in twelve hex keeps it", "gitlab", "https://gitlab.com/g/release-0123456789ab", "release-0123456789ab"},
		{"jira: a name that ends in twelve hex keeps it", "jira", "https://x.atlassian.net/browse/release-0123456789ab", "release-0123456789ab"},
		{"linear: a name ending in twelve hex, plus the id, keeps the name whole", "linear", "https://linear.app/fc/project/release-0123456789ab-0d9bd4168c10", "release-0123456789ab"},
		{"an unknown provider does not strip", "", "https://x.dev/p/slug-0d9bd4168c10", "slug-0d9bd4168c10"},

		// The id-suffix rule's boundaries, both sides, on the provider it applies to.
		{"linear: eleven hex, kept whole", "linear", "https://x.dev/p/slug-0d9bd4168c1", "slug-0d9bd4168c1"},
		{"linear: thirteen hex, kept whole", "linear", "https://x.dev/p/slug-0d9bd4168c10a", "slug-0d9bd4168c10a"},
		{"linear: twelve chars but not hex", "linear", "https://x.dev/p/slug-0d9bd4168cZZ", "slug-0d9bd4168cZZ"},
		{"linear: twelve hex in uppercase is not the shape", "linear", "https://x.dev/p/slug-0D9BD4168C10", "slug-0D9BD4168C10"},
		{"linear: twelve hex with no hyphen before it", "linear", "https://x.dev/p/slug0d9bd4168c10", "slug0d9bd4168c10"},
		{"linear: the segment is ONLY a hyphen and an id", "linear", "https://x.dev/p/-0d9bd4168c10", ""},
		{"linear: the segment is only an id, no hyphen", "linear", "https://x.dev/p/0d9bd4168c10", "0d9bd4168c10"},

		// SCREENED AGAINST THE ALIAS CONTRACT: every value it would reject is
		// dropped, because one inadmissible alias fails the whole entity.
		{"exactly the per-value bound is admitted", "gitlab", "https://x.dev/p/" + long512, long512},
		{"one past the per-value bound is dropped", "gitlab", "https://x.dev/p/" + long513, ""},
		{"a leading space decoded from %20 is dropped", "gitlab", "https://x.dev/p/%20name", ""},
		{"a trailing space decoded from %20 is dropped", "gitlab", "https://x.dev/p/name%20", ""},
		{"a separator decoded from %7C is dropped", "gitlab", "https://x.dev/p/a%7Cb", ""},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			if got := projectURLHandle(testCase.provider, testCase.in); got != testCase.want {
				t.Fatalf("projectURLHandle(%q, %q) = %q, want %q", testCase.provider, testCase.in, got, testCase.want)
			}
		})
	}
}

// A handle shaped like the HOST is shared by every project on the provider, so
// one term would match all of them.
func TestTheHandleIsNeverAFragmentOfTheHost(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"https://linear.app", "linear.app", "", "   "} {
		if got := projectURLHandle("linear", raw); got != "" {
			t.Fatalf("projectURLHandle(%q) = %q, want empty", raw, got)
		}
	}
}

// EVERY HANDLE THE PRODUCER EMITS IS ONE THE CONTRACT ADMITS. Swept over the
// whole domain above rather than asserted per cell, so a future cell whose
// expectation is set wrongly still cannot publish a value the validator would
// reject: an emitted handle that fails the contract fails the entity's whole
// projection.
func TestEveryEmittedHandleSatisfiesTheAliasContract(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"https://linear.app/fc/project/chaos-draw-0d9bd4168c10",
		"https://x.dev/p/" + strings.Repeat("b", 512),
		"https://x.dev/p/" + strings.Repeat("b", 513),
		"https://x.dev/p/%20name", "https://x.dev/p/name%20", "https://x.dev/p/a%7Cb",
		"https://x.dev/p/%09tab", "https://x.dev/p/line%0Abreak",
	} {
		for _, provider := range []string{"linear", "gitlab", "jira", ""} {
			handle := projectURLHandle(provider, raw)
			if handle != "" && !contractsv1.ValidContextFabricEntityAlias(handle) {
				t.Errorf("projectURLHandle(%q, %q) emitted %q, which the alias contract rejects", provider, raw, handle)
			}
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
