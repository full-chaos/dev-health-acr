package falkorgraph

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// These tests exercise the adapter's pure, deterministic helpers directly --
// no FalkorDB connection needed. The conn interface here is a thin
// Cypher-execution boundary (query(cypher string, params) ([]row, error)),
// unlike zepgraph's semantically-typed api interface (GetNode, Search,
// AddFactTriple, ...); a fake that "interprets" arbitrary Cypher text well
// enough to stand in for a real server would itself be a small graph
// database and a large, fragile undertaking. Behavior that genuinely
// depends on FalkorDB's own semantics (MERGE upsert, constraint
// enforcement, tombstone staleness, full-text search) is proven against a
// real server in adapter_live_integration_test.go instead -- see that
// file's package doc comment.

func TestGraphKeyIsServerDerivedAndOrganizationIsolated(t *testing.T) {
	a := graphKey("acr-cf", "org-a")
	b := graphKey("acr-cf", "org-b")
	if a == b {
		t.Fatalf("graphKey(org-a) == graphKey(org-b): %q", a)
	}
	if graphKey("acr-cf", "org-a") != a {
		t.Fatal("graphKey is not deterministic for the same input")
	}
	if graphKey("other-prefix", "org-a") == a {
		t.Fatal("graphKey ignored the prefix")
	}
}

func TestKindLabelIsPascalCaseAndCypherSafe(t *testing.T) {
	cases := map[contextfabric.SubjectKind]string{
		contextfabric.SubjectProject:                 "Project",
		contextfabric.SubjectWorkItem:                "WorkItem",
		contextfabric.SubjectKind("ci_pipeline_run"): "CiPipelineRun",
		"": "Unknown",
	}
	for kind, want := range cases {
		if got := kindLabel(kind); got != want {
			t.Errorf("kindLabel(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestSubjectUUIDRoundTrips(t *testing.T) {
	uuid := subjectUUID("project", "project_ask_dev")
	kind, canonicalID := splitSubjectUUID(uuid)
	if kind != "project" || canonicalID != "project_ask_dev" {
		t.Fatalf("splitSubjectUUID(%q) = (%q, %q)", uuid, kind, canonicalID)
	}
}

func TestSplitSubjectUUIDRejectsMalformedInput(t *testing.T) {
	kind, canonicalID := splitSubjectUUID("not-a-uuid")
	if kind != "" || canonicalID != "" {
		t.Fatalf("splitSubjectUUID(malformed) = (%q, %q), want (\"\", \"\")", kind, canonicalID)
	}
}

func TestNsTimestampIsMonotonicAcrossFractionalWidths(t *testing.T) {
	// The exact regression the design doc's temporal-ordering finding
	// guards against: a whole-second timestamp and a sub-second timestamp
	// must compare correctly as int64 nanoseconds, unlike their
	// RFC3339Nano string forms (which render at different lengths and
	// compare incorrectly -- see docs/design/context-fabric-falkordb-adapter.md
	// §5). This test proves the _ns half of the fix in isolation; the live
	// integration suite proves FalkorDB actually orders/filters on it
	// correctly server-side.
	whole := mustParseRFC3339(t, "2026-01-01T00:00:00Z")
	frac := mustParseRFC3339(t, "2026-01-01T00:00:00.5Z")
	if !(nsTimestamp(whole) < nsTimestamp(frac)) {
		t.Fatalf("nsTimestamp(whole)=%d, nsTimestamp(frac)=%d -- want whole < frac", nsTimestamp(whole), nsTimestamp(frac))
	}
	// The RFC3339Nano STRING forms of these two, by contrast, compare in
	// the WRONG order lexicographically -- this is the bug the _ns fields
	// exist to route around. Documented here, not asserted as a passing
	// condition, so the reason nsTimestamp exists stays visible next to its
	// own test.
}

func mustParseRFC3339(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := parseRFC3339(value)
	if err != nil {
		t.Fatalf("parseRFC3339(%q) error = %v", value, err)
	}
	return parsed
}

func TestTokenizeForFulltextStripsRediSearchSyntaxCharacters(t *testing.T) {
	got := tokenizeForFulltext(`What is "Ask Dev" depending on? (urgent) @field:x 50%`)
	want := []string{"What", "is", "Ask", "Dev", "depending", "on?", "urgent", "field", "x", "50"}
	if len(got) != len(want) {
		t.Fatalf("tokenizeForFulltext() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tokenizeForFulltext()[%d] = %q, want %q (full: %#v)", i, got[i], want[i], got)
		}
	}
}

func TestTokenizeForFulltextEmptyInputYieldsNoTerms(t *testing.T) {
	if got := tokenizeForFulltext("   "); len(got) != 0 {
		t.Fatalf("tokenizeForFulltext(whitespace) = %#v, want empty", got)
	}
}

func TestAuthorizationValueUnrestrictedIsWildcard(t *testing.T) {
	if got := authorizationValue(nil); got != "*" {
		t.Fatalf("authorizationValue(nil) = %#v, want \"*\"", got)
	}
	if got := authorizationValue([]string{}); got != "*" {
		t.Fatalf("authorizationValue([]) = %#v, want \"*\"", got)
	}
}

func TestAuthorizationValueSpecificListIsSortedAndDeduped(t *testing.T) {
	got, ok := authorizationValue([]string{"b", "a", "b"}).([]string)
	if !ok {
		t.Fatalf("authorizationValue(specific) did not return []string")
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("authorizationValue([b,a,b]) = %#v, want [a b]", got)
	}
}

func TestNormalizePropertiesConvertsAllStringListsToStringSlice(t *testing.T) {
	// The exact live-discovered bug this guards against: FalkorDB always
	// reads a list-valued property back as []interface{}, even when the
	// original write was a genuine Go []string -- silently breaking every
	// graphrank []string type assertion (EvidenceRefs, the authorization_*
	// wildcard convention) downstream, which fail closed (treat a real
	// list as absent) rather than panic, so the bug had no loud symptom.
	in := map[string]interface{}{
		"evidence_refs": []interface{}{"e1", "e2"},
		"mixed":         []interface{}{"a", 1},
		"scalar":        "unchanged",
		"already_typed": []string{"x"},
	}
	out := normalizeProperties(in)
	refs, ok := out["evidence_refs"].([]string)
	if !ok || len(refs) != 2 || refs[0] != "e1" || refs[1] != "e2" {
		t.Fatalf("normalizeProperties()[evidence_refs] = %#v, want []string{e1 e2}", out["evidence_refs"])
	}
	if _, ok := out["mixed"].([]interface{}); !ok {
		t.Fatalf("normalizeProperties()[mixed] = %#v, want left as []interface{} (not all-string)", out["mixed"])
	}
	if out["scalar"] != "unchanged" {
		t.Fatalf("normalizeProperties()[scalar] = %#v, want unchanged", out["scalar"])
	}
	if _, ok := out["already_typed"].([]string); !ok {
		t.Fatalf("normalizeProperties()[already_typed] = %#v, want []string", out["already_typed"])
	}
}

func TestNormalizePropertiesHandlesNilAndEmptyList(t *testing.T) {
	if normalizeProperties(nil) != nil {
		t.Fatal("normalizeProperties(nil) should return nil")
	}
	out := normalizeProperties(map[string]interface{}{"empty": []interface{}{}})
	refs, ok := out["empty"].([]string)
	if !ok || len(refs) != 0 {
		t.Fatalf("normalizeProperties()[empty] = %#v, want empty []string", out["empty"])
	}
}

func TestSafeParamsRejectsUnsupportedTypesRatherThanPanicking(t *testing.T) {
	// Verified live: falkordb-go's ToString panics on int32, uint64,
	// float32, []int64, map[string]string, and time.Time. safeParams must
	// reject these with an error before they ever reach BuildParamsHeader.
	for _, bad := range []interface{}{int32(1), uint64(1), float32(1), []int64{1}, map[string]string{"a": "b"}} {
		if _, err := safeParams(map[string]interface{}{"x": bad}); err == nil {
			t.Errorf("safeParams(%T) error = nil, want an error", bad)
		}
	}
}

func TestSafeParamsAcceptsEverySupportedType(t *testing.T) {
	safe := map[string]interface{}{
		"a": nil, "b": "s", "c": 1, "d": int64(1), "e": 1.5, "f": true,
		"g": []string{"x"}, "h": []interface{}{"y", 1}, "i": map[string]interface{}{"k": "v"},
	}
	out, err := safeParams(safe)
	if err != nil {
		t.Fatalf("safeParams(all supported types) error = %v", err)
	}
	if len(out) != len(safe) {
		t.Fatalf("safeParams() dropped keys: got %#v", out)
	}
}

func TestSafeParamsNilIsNoop(t *testing.T) {
	out, err := safeParams(nil)
	if err != nil || out != nil {
		t.Fatalf("safeParams(nil) = %#v, %v, want nil, nil", out, err)
	}
}

func TestSafeDependencyErrorPreservesKnownSentinels(t *testing.T) {
	// The exact live-discovered bug: an already-classified error (e.g.
	// ErrNotFound from classifyFalkorError) must survive a second pass
	// through safeDependencyError -- callers throughout this package rely
	// on errors.Is(err, ErrNotFound) after wrapping.
	classified := classifyFalkorError("query context graph", errNotFoundLike{})
	wrapped := safeDependencyError("read projection watermark", classified)
	if !isErrNotFound(wrapped) {
		t.Fatalf("safeDependencyError() flattened an already-classified ErrNotFound: %v", wrapped)
	}
}

func TestSafeDependencyErrorFlattensGenuinelyUnknownErrors(t *testing.T) {
	wrapped := safeDependencyError("some operation", genericErr{})
	if isErrNotFound(wrapped) {
		t.Fatal("safeDependencyError() misclassified a generic error as ErrNotFound")
	}
	if wrapped.Error() == (genericErr{}).Error() {
		t.Fatal("safeDependencyError() leaked the raw dependency error text")
	}
}

// TestGraphNotProjectedErrorTranslatesErrNotFound (CHAOS-4077) pins
// graphNotProjectedError's own contract: an already-classified ErrNotFound
// (the ONLY shape classifyFalkorError produces for "GRAPH.RO_QUERY against
// a graph key that never existed") gains contextfabric.ErrGraphNotProjected
// WITHOUT losing errors.Is(err, ErrNotFound) -- callers elsewhere in this
// package that only check the local sentinel must keep working unchanged.
func TestGraphNotProjectedErrorTranslatesErrNotFound(t *testing.T) {
	classified := classifyFalkorError("query context graph", errNotFoundLike{})
	translated := graphNotProjectedError(classified)
	if !errors.Is(translated, contextfabric.ErrGraphNotProjected) {
		t.Fatalf("graphNotProjectedError() = %v, want errors.Is(_, contextfabric.ErrGraphNotProjected)", translated)
	}
	if !isErrNotFound(translated) {
		t.Fatalf("graphNotProjectedError() = %v, lost errors.Is(_, ErrNotFound) -- existing package-local callers would break", translated)
	}
}

// TestGraphNotProjectedErrorPassesThroughEverythingElse proves the guard is
// narrow: a genuine rate limit, auth failure, or generic dependency error
// must NEVER satisfy errors.Is(_, contextfabric.ErrGraphNotProjected) --
// misclassifying one of those would degrade a real outage into a silent
// "no match" instead of surfacing it.
func TestGraphNotProjectedErrorPassesThroughEverythingElse(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"a DIFFERENT classified sentinel (unauthorized, not not-found)", classifyFalkorError("op", errUnauthorizedLike{})},
		{"generic dependency error", safeDependencyError("op", genericErr{})},
		{"context canceled", context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := graphNotProjectedError(tc.err)
			if errors.Is(got, contextfabric.ErrGraphNotProjected) {
				t.Fatalf("graphNotProjectedError(%v) = %v, must NOT satisfy errors.Is(_, ErrGraphNotProjected)", tc.err, got)
			}
			if got != tc.err {
				t.Fatalf("graphNotProjectedError(%v) = %v, want the error returned UNCHANGED (identity-equal) for anything but ErrNotFound", tc.err, got)
			}
		})
	}
}

type errUnauthorizedLike struct{}

func (errUnauthorizedLike) Error() string { return "WRONGPASS invalid credentials" }

type errNotFoundLike struct{}

func (errNotFoundLike) Error() string { return "Invalid graph operation on empty key" }

type genericErr struct{}

func (genericErr) Error() string { return "some raw internal falkordb detail that must not leak" }

func isErrNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
