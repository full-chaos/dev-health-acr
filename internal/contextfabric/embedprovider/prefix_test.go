package embedprovider

import (
	"strings"
	"testing"
)

// The no-prefix default: an Embedder built without naming a PrefixFamily
// (the zero value) must not alter document or query text at all.
func TestNoPrefixFamilyConfiguredAppliesNoPrefix(t *testing.T) {
	cfg := testConfig("http://localhost:1")
	embedder, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := embedder.DocumentPrefix(); got != "" {
		t.Fatalf("DocumentPrefix() = %q, want empty", got)
	}
	if got := embedder.QueryPrefix(); got != "" {
		t.Fatalf("QueryPrefix() = %q, want empty", got)
	}
	if got := embedder.ApplyDocumentPrefix("the auth work"); got != "the auth work" {
		t.Fatalf("ApplyDocumentPrefix() = %q, want unchanged text", got)
	}
	if got := embedder.ApplyQueryPrefix("who owns auth"); got != "who owns auth" {
		t.Fatalf("ApplyQueryPrefix() = %q, want unchanged text", got)
	}
	if got := embedder.PrefixTagComponent(); got != "pnone" {
		t.Fatalf("PrefixTagComponent() = %q, want %q", got, "pnone")
	}
}

// PrefixFamilyNone named explicitly must behave identically to the zero
// value -- "none" and "" are the same configuration.
func TestExplicitPrefixFamilyNoneMatchesTheZeroValue(t *testing.T) {
	cfg := testConfig("http://localhost:1")
	cfg.PrefixFamily = PrefixFamilyNone
	embedder, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := embedder.DocumentPrefix(); got != "" {
		t.Fatalf("DocumentPrefix() = %q, want empty", got)
	}
	if got := embedder.QueryPrefix(); got != "" {
		t.Fatalf("QueryPrefix() = %q, want empty", got)
	}
}

// The document side: nomic's search_document prefix must be applied to
// SUBJECT text destined for storage embedding, and only to the document
// side.
func TestNomicPrefixFamilyAppliesTheDocumentPrefix(t *testing.T) {
	cfg := testConfig("http://localhost:1")
	cfg.PrefixFamily = PrefixFamilyNomic
	embedder, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const text = "PR #52 fix the auth redirect loop"
	got := embedder.ApplyDocumentPrefix(text)
	want := "search_document: " + text
	if got != want {
		t.Fatalf("ApplyDocumentPrefix() = %q, want %q", got, want)
	}
}

// The query side: nomic's search_query prefix must be applied to QUESTION
// text destined for search embedding, and it must differ from the document
// prefix -- the whole point of an asymmetric contract.
func TestNomicPrefixFamilyAppliesTheQueryPrefix(t *testing.T) {
	cfg := testConfig("http://localhost:1")
	cfg.PrefixFamily = PrefixFamilyNomic
	embedder, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const text = "who fixed the auth redirect loop"
	got := embedder.ApplyQueryPrefix(text)
	want := "search_query: " + text
	if got != want {
		t.Fatalf("ApplyQueryPrefix() = %q, want %q", got, want)
	}
	if embedder.DocumentPrefix() == embedder.QueryPrefix() {
		t.Fatal("document and query prefixes must differ for an asymmetric family")
	}
}

func TestPrefixTagComponentNamesTheConfiguredFamily(t *testing.T) {
	cfg := testConfig("http://localhost:1")
	cfg.PrefixFamily = PrefixFamilyNomic
	embedder, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := embedder.PrefixTagComponent(); got != "pnomic" {
		t.Fatalf("PrefixTagComponent() = %q, want %q", got, "pnomic")
	}
}

// Explicit, closed-vocabulary config, fail closed: an unrecognized family
// must reject construction rather than silently fall back to no prefix.
func TestUnrecognizedPrefixFamilyFailsValidation(t *testing.T) {
	cfg := testConfig("http://localhost:1")
	cfg.PrefixFamily = PrefixFamily("nomic-embed-text-v1.5") // a model id, not a family
	if _, err := New(cfg); err == nil {
		t.Fatal("New must reject an unrecognized prefix family, got nil error")
	}
}

// Round-1 review P1: a prefix must never push the combined text past
// MaxTextRunes, because Embed's own TruncateRunes(text, MaxTextRunes) would
// then silently cut retrieval-bearing runes off the TAIL to make room for a
// prefix it does not know about. At exactly MaxTextRunes of input, the
// prefixed result must (a) be no longer than MaxTextRunes, and (b) already
// be a fixed point of TruncateRunes at MaxTextRunes -- i.e. Embed's own
// second truncation is provably a no-op.
func TestApplyDocumentPrefixNeverExceedsMaxTextRunesAfterPrefixing(t *testing.T) {
	cfg := testConfig("http://localhost:1")
	cfg.PrefixFamily = PrefixFamilyNomic
	cfg.MaxTextRunes = 64
	embedder, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	input := strings.Repeat("x", cfg.MaxTextRunes) // exactly at the budget
	got := embedder.ApplyDocumentPrefix(input)
	if n := len([]rune(got)); n > cfg.MaxTextRunes {
		t.Fatalf("prefixed result has %d runes, want <= %d", n, cfg.MaxTextRunes)
	}
	if !strings.HasPrefix(got, embedder.DocumentPrefix()) {
		t.Fatalf("result %q lost its document prefix", got)
	}
	// Embed's internal truncation must be a provable no-op on this text.
	if again := TruncateRunes(got, cfg.MaxTextRunes); again != got {
		t.Fatalf("Embed's own truncation is NOT a no-op: got %q, retruncated %q", got, again)
	}
}

// Same guarantee on the query side, with a DIFFERENT-length prefix
// ("search_query: " vs "search_document: ") -- both arms' OUTPUT must be
// bounded by the same MaxTextRunes ceiling even though they reserve
// different amounts of budget for the underlying text.
func TestApplyQueryPrefixNeverExceedsMaxTextRunesAfterPrefixing(t *testing.T) {
	cfg := testConfig("http://localhost:1")
	cfg.PrefixFamily = PrefixFamilyNomic
	cfg.MaxTextRunes = 64
	embedder, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	input := strings.Repeat("y", cfg.MaxTextRunes)
	got := embedder.ApplyQueryPrefix(input)
	if n := len([]rune(got)); n > cfg.MaxTextRunes {
		t.Fatalf("prefixed result has %d runes, want <= %d", n, cfg.MaxTextRunes)
	}
	if !strings.HasPrefix(got, embedder.QueryPrefix()) {
		t.Fatalf("result %q lost its query prefix", got)
	}
	if again := TruncateRunes(got, cfg.MaxTextRunes); again != got {
		t.Fatalf("Embed's own truncation is NOT a no-op: got %q, retruncated %q", got, again)
	}
}

// Both arms' effective ceiling is IDENTICAL (MaxTextRunes) despite the
// document and query prefixes differing in length -- the asymmetry lives
// entirely in how much underlying TEXT each side keeps, never in the total
// each side may produce.
func TestDocumentAndQueryPrefixesShareTheSameOutputCeilingDespiteDifferingLengths(t *testing.T) {
	cfg := testConfig("http://localhost:1")
	cfg.PrefixFamily = PrefixFamilyNomic
	cfg.MaxTextRunes = 64
	embedder, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(embedder.DocumentPrefix()) == len(embedder.QueryPrefix()) {
		t.Fatal("fixture assumption violated: this test requires prefixes of different lengths")
	}
	input := strings.Repeat("z", cfg.MaxTextRunes*2) // comfortably over budget on both sides
	docOut := embedder.ApplyDocumentPrefix(input)
	queryOut := embedder.ApplyQueryPrefix(input)
	if len([]rune(docOut)) != cfg.MaxTextRunes {
		t.Fatalf("document output = %d runes, want exactly %d", len([]rune(docOut)), cfg.MaxTextRunes)
	}
	if len([]rune(queryOut)) != cfg.MaxTextRunes {
		t.Fatalf("query output = %d runes, want exactly %d", len([]rune(queryOut)), cfg.MaxTextRunes)
	}
	// The retained TEXT differs in length between the two arms -- that is
	// the expected, budgeted consequence of the prefixes' differing
	// lengths, not a bug.
	docText := strings.TrimPrefix(docOut, embedder.DocumentPrefix())
	queryText := strings.TrimPrefix(queryOut, embedder.QueryPrefix())
	if len(docText) == len(queryText) {
		t.Fatal("expected the retained text lengths to differ, matching the prefixes' differing lengths")
	}
}

// Round-1 review P2: applying the same prefix twice must be a no-op, not
// "search_document: search_document: text".
func TestApplyDocumentPrefixIsIdempotent(t *testing.T) {
	cfg := testConfig("http://localhost:1")
	cfg.PrefixFamily = PrefixFamilyNomic
	embedder, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	once := embedder.ApplyDocumentPrefix("the auth work")
	twice := embedder.ApplyDocumentPrefix(once)
	if twice != once {
		t.Fatalf("a second application changed the text: once=%q twice=%q", once, twice)
	}
	if strings.Count(twice, embedder.DocumentPrefix()) != 1 {
		t.Fatalf("prefix appears %d times, want exactly 1: %q", strings.Count(twice, embedder.DocumentPrefix()), twice)
	}
}

func TestApplyQueryPrefixIsIdempotent(t *testing.T) {
	cfg := testConfig("http://localhost:1")
	cfg.PrefixFamily = PrefixFamilyNomic
	embedder, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	once := embedder.ApplyQueryPrefix("who fixed the auth redirect loop")
	twice := embedder.ApplyQueryPrefix(once)
	if twice != once {
		t.Fatalf("a second application changed the text: once=%q twice=%q", once, twice)
	}
	if strings.Count(twice, embedder.QueryPrefix()) != 1 {
		t.Fatalf("prefix appears %d times, want exactly 1: %q", strings.Count(twice, embedder.QueryPrefix()), twice)
	}
}

// The no-prefix family is trivially idempotent (nothing is ever prepended),
// covered for completeness alongside the nomic idempotency tests above.
func TestApplyPrefixWithNoFamilyConfiguredIsIdempotent(t *testing.T) {
	embedder, err := New(testConfig("http://localhost:1"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const text = "the auth work"
	if got := embedder.ApplyDocumentPrefix(embedder.ApplyDocumentPrefix(text)); got != text {
		t.Fatalf("ApplyDocumentPrefix twice = %q, want %q", got, text)
	}
	if got := embedder.ApplyQueryPrefix(embedder.ApplyQueryPrefix(text)); got != text {
		t.Fatalf("ApplyQueryPrefix twice = %q, want %q", got, text)
	}
}

// ConfigFromEnv: unset means PrefixFamilyNone, and a set value round-trips.
func TestConfigFromEnvReadsThePrefixFamily(t *testing.T) {
	base := map[string]string{
		EnvProvider:             "lmstudio",
		EnvBaseURL:              "http://localhost:1234/v1/",
		EnvModel:                "probe-embed",
		EnvDimension:            "8",
		EnvAllowInsecureBaseURL: "true",
	}
	lookupWith := func(overrides map[string]string) func(string) (string, bool) {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		for k, v := range overrides {
			env[k] = v
		}
		return func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		}
	}

	cfg, err := ConfigFromEnv(lookupWith(nil))
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.PrefixFamily != PrefixFamilyNone {
		t.Fatalf("unset %s should default to %q, got %q", EnvPrefixFamily, PrefixFamilyNone, cfg.PrefixFamily)
	}

	cfg, err = ConfigFromEnv(lookupWith(map[string]string{EnvPrefixFamily: "nomic"}))
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.PrefixFamily != PrefixFamilyNomic {
		t.Fatalf("%s=nomic should set PrefixFamilyNomic, got %q", EnvPrefixFamily, cfg.PrefixFamily)
	}

	if _, err := ConfigFromEnv(lookupWith(map[string]string{EnvPrefixFamily: "e5"})); err == nil {
		t.Fatal("an unrecognized family from the environment must fail closed, got nil error")
	}
}
