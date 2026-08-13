package genkitruntime

import (
	"os"
	"path/filepath"
	"testing"
)

// The RENDERED prompt is what the model actually reads, and until now
// nothing pinned it (codex round-9 F6).
//
// Every other prompt test asserts a property: that a number appears, that it
// is derived, that its anchor names the right field. All of them are
// satisfiable by wording that is subtly wrong everywhere else -- a dropped
// negation, a mangled interpolation, an accidentally reordered vocabulary,
// a clause deleted in a refactor. Interpolation makes that MORE likely, not
// less: the prompt is now assembled at init time, so nobody reads the final
// text in review, they read a template.
//
// A byte-exact golden makes every change to what the model reads visible in
// the diff, and deliberate. It is not a bound check and does not replace
// one; it is the record of the artifact itself.
//
// Regenerate deliberately, never reflexively -- a golden updated without
// reading the diff proves nothing:
//
//	ACR_UPDATE_PROMPT_GOLDENS=1 go test ./internal/contextfabric/genkitruntime/ -run TestRenderedPromptsMatchTheirGoldens
func TestRenderedPromptsMatchTheirGoldens(t *testing.T) {
	for _, prompt := range []struct {
		name     string
		rendered string
	}{
		{"interpretation_system_prompt", interpretationSystemPrompt},
		{"synthesis_system_prompt", synthesisSystemPrompt},
	} {
		t.Run(prompt.name, func(t *testing.T) {
			path := filepath.Join("testdata", prompt.name+".golden")
			if os.Getenv("ACR_UPDATE_PROMPT_GOLDENS") == "1" {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(prompt.rendered), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("rewrote %s; read the diff before committing it", path)
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("no golden for %s (%v); generate it with ACR_UPDATE_PROMPT_GOLDENS=1 and read the result before committing", prompt.name, err)
			}
			if string(want) != prompt.rendered {
				t.Errorf("the rendered %s no longer matches its golden.\nIf the change is intended, regenerate with ACR_UPDATE_PROMPT_GOLDENS=1 and review the diff as a change to what the model is told.\n--- golden ---\n%s\n--- rendered ---\n%s",
					prompt.name, want, prompt.rendered)
			}
		})
	}
}

// TestPromptGoldensCarryNoTemplateDirectives catches the failure the golden
// itself cannot: a prompt that shipped with an unsubstituted verb, or with
// fmt's error markers baked in because an argument was missing or of the
// wrong type. Those render as ordinary text and a regenerated golden would
// happily record them.
func TestPromptGoldensCarryNoTemplateDirectives(t *testing.T) {
	for _, prompt := range []struct {
		name     string
		rendered string
	}{
		{"interpretation_system_prompt", interpretationSystemPrompt},
		{"synthesis_system_prompt", synthesisSystemPrompt},
	} {
		t.Run(prompt.name, func(t *testing.T) {
			for _, marker := range []string{"%!", "%d", "%s", "%v", "{N}", "MISSING", "EXTRA"} {
				if containsMarker(prompt.rendered, marker) {
					t.Errorf("the rendered prompt contains %q, so an interpolation did not resolve; the model would read it literally", marker)
				}
			}
		})
	}
}

func containsMarker(text, marker string) bool {
	for i := 0; i+len(marker) <= len(text); i++ {
		if text[i:i+len(marker)] == marker {
			return true
		}
	}
	return false
}
