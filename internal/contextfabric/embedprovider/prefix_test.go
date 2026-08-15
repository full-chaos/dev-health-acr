package embedprovider

import "testing"

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
