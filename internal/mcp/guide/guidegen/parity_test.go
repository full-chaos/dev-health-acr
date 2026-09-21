package guidegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/mcp/guide"
)

func embeddedFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, res := range guide.Resources() {
		text, err := guide.Text(res.URI)
		if err != nil {
			t.Fatal(err)
		}
		files[res.File] = text
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 embedded guide files, got %d", len(files))
	}
	vocab, err := guide.PromptVocabText()
	if err != nil {
		t.Fatal(err)
	}
	files[FilePromptVocab] = vocab
	return files
}

func sameFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for name, text := range a {
		if b[name] != text {
			return false
		}
	}
	return true
}

func TestEmbeddedGuideMatchesRegistries(t *testing.T) {
	in := FromRegistries()
	built, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if !sameFiles(built, embeddedFiles(t)) {
		t.Fatal("embedded guide content differs from the registries; run `go generate ./internal/mcp/guide`")
	}
}

func TestRegistryInputsAreNonEmptyAndAppearInGuide(t *testing.T) {
	in := FromRegistries()
	counts := map[string]int{
		"families": len(in.Families), "subject kinds": len(in.SubjectKinds), "cohort kinds": len(in.ServableCohortKinds),
		"grammars": len(in.Grammars), "windows": len(in.Windows), "statuses": len(in.Statuses),
	}
	for name, n := range counts {
		if n == 0 {
			t.Fatalf("registry %s is empty", name)
		}
	}
	files := embeddedFiles(t)
	must := func(file, id string) {
		t.Helper()
		if !strings.Contains(files[file], "`"+id+"`") {
			t.Errorf("%s does not name %q", file, id)
		}
	}
	for _, f := range in.Families {
		must(FileQuestions, f.ID)
	}
	for _, id := range in.SubjectKinds {
		must(FileVocabulary, id)
	}
	for _, g := range in.Grammars {
		must(FileVocabulary, g.PatternID)
	}
	for _, id := range in.Windows {
		must(FileVocabulary, id)
	}
	for _, id := range in.Statuses {
		must(FileVocabulary, id)
	}
}

func TestCohortServableRowsMatchRegistry(t *testing.T) {
	servable := map[string]bool{}
	for _, kind := range contextfabric.ServableCohortKindsForAudit() {
		servable[string(kind)] = true
	}
	if len(servable) == 0 {
		t.Fatal("no servable cohort kinds")
	}
	vocabulary := embeddedFiles(t)[FileVocabulary]
	rows := 0
	for _, line := range strings.Split(vocabulary, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 4 || !strings.HasPrefix(strings.TrimSpace(cells[1]), "`") {
			continue
		}
		kind := strings.Trim(strings.TrimSpace(cells[1]), "`")
		if _, isKind := subjectKindTexts[kind]; !isKind {
			continue
		}
		rows++
		if got, want := strings.TrimSpace(cells[2]), yesNo(servable[kind]); got != want {
			t.Errorf("kind %s: guide says %q, registry says %q", kind, got, want)
		}
	}
	if rows != len(subjectKindTexts) {
		t.Fatalf("expected %d kind rows, found %d", len(subjectKindTexts), rows)
	}
}

func TestUnreachableFamiliesAreMarkedNotAnswerable(t *testing.T) {
	unreachable := contextfabric.UnreachableQuestionFamilies()
	if len(unreachable) == 0 {
		t.Fatal("no unreachable families")
	}
	sections := strings.Split(embeddedFiles(t)[FileQuestions], "\n## ")
	seen := 0
	for _, section := range sections[1:] {
		id := strings.Trim(strings.SplitN(section, "\n", 2)[0], "`")
		want := slices.Contains(unreachable, contextfabric.QuestionFamily(id))
		if got := strings.Contains(section, "Not answerable today"); got != want && id != "Other tools" {
			t.Errorf("family %s: not-answerable marker = %v, registry unreachable = %v", id, got, want)
		}
		if strings.Contains(section, "Tool: `investigate_question`") == want && id != "Other tools" {
			t.Errorf("family %s: tool line disagrees with reachability", id)
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("no family sections found")
	}
}

func TestRenderKindsSplitByProducerAgainstRegistry(t *testing.T) {
	unproduced := map[string]bool{}
	for _, kind := range contextfabric.DeclaredUnproducedRenderKinds() {
		unproduced[string(kind)] = true
	}
	if len(unproduced) == 0 {
		t.Fatal("no declared-unproduced render kinds in the registry")
	}
	in := FromRegistries()
	used := map[string]bool{}
	for _, f := range in.Families {
		for _, k := range f.RenderKinds {
			used[k] = true
		}
	}
	if len(used) == 0 {
		t.Fatal("no render kinds in the family table")
	}
	files := embeddedFiles(t)
	// A kind the registry declares unproduced never appears on a "May render as" line.
	for _, line := range strings.Split(files[FileQuestions], "\n") {
		if !strings.HasPrefix(line, "- May render as:") {
			continue
		}
		for kind := range unproduced {
			if strings.Contains(line, "`"+kind+"`") {
				t.Errorf("guide offers unproduced render kind %s: %s", kind, line)
			}
		}
	}
	// Vocabulary: each used kind sits under exactly the heading its registry status implies.
	text := files[FileVocabulary]
	section := text[strings.Index(text, "## Render kinds"):]
	split := strings.Index(section, "Declared by a family, no producer today:")
	if split < 0 {
		t.Fatal("vocabulary has no declared-unproduced list")
	}
	producedPart, declaredPart := section[:split], section[split:]
	for kind := range renderKindTexts {
		inProduced := strings.Contains(producedPart, "\n- `"+kind+"`:")
		inDeclared := strings.Contains(declaredPart, "\n- `"+kind+"`:")
		switch {
		case !used[kind] && (inProduced || inDeclared):
			t.Errorf("render kind %s is listed but no family names it", kind)
		case used[kind] && unproduced[kind] && !(inDeclared && !inProduced):
			t.Errorf("render kind %s is declared unproduced but is not listed as such", kind)
		case used[kind] && !unproduced[kind] && !(inProduced && !inDeclared):
			t.Errorf("render kind %s has a producer but is not listed as produced", kind)
		}
	}
}

// Every authored handle example is executed through the real grammar.
func TestHandleExamplesBindThroughRealGrammar(t *testing.T) {
	patterns := graphrank.HandleGrammarPatterns()
	if len(patterns) == 0 {
		t.Fatal("no handle patterns")
	}
	for _, pattern := range patterns {
		example, ok := grammarExamples[pattern.ID]
		if !ok {
			t.Fatalf("pattern %s has no example", pattern.ID)
		}
		var hit *graphrank.BoundHandle
		for _, bound := range graphrank.BindHandles(example.Question) {
			if bound.Grammar == pattern.ID {
				b := bound
				hit = &b
			}
		}
		if hit == nil {
			t.Fatalf("example %q does not bind pattern %s", example.Question, pattern.ID)
		}
		if hit.Value != example.Value || hit.Kind != pattern.Kind {
			t.Errorf("pattern %s bound value %q kind %q; guide says %q kind %q", pattern.ID, hit.Value, hit.Kind, example.Value, pattern.Kind)
		}
	}
}

func repoFile(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestReceiptTableMatchesRequestSchema(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Items struct {
				Ref string `json:"$ref"`
			} `json:"items"`
		} `json:"properties"`
		Defs map[string]struct {
			Properties map[string]struct {
				Pattern string `json:"pattern"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(repoFile(t, "contracts/jsonschema/v1/mcp_investigate_question_request.v1.schema.json"), &schema); err != nil {
		t.Fatal(err)
	}
	var fields []string
	for name := range schema.Properties {
		if strings.HasPrefix(name, "prior_") && strings.HasSuffix(name, "_receipts") {
			fields = append(fields, name)
		}
	}
	if len(fields) == 0 {
		t.Fatal("no prior_*_receipts fields in schema")
	}
	documented := map[string]string{}
	for _, r := range Receipts {
		documented[r.Field] = r.Prefix
	}
	if len(documented) != len(fields) {
		t.Fatalf("guide documents %d receipt fields, schema has %d", len(documented), len(fields))
	}
	for _, field := range fields {
		prefix, ok := documented[field]
		if !ok {
			t.Errorf("schema field %s is not in the guide", field)
			continue
		}
		def := strings.TrimPrefix(schema.Properties[field].Items.Ref, "#/$defs/")
		pattern := schema.Defs[def].Properties["receipt_id"].Pattern
		if want := "^" + prefix; prefix != "" && pattern != want || prefix == "" && pattern != "" {
			t.Errorf("field %s: guide prefix %q, schema pattern %q", field, prefix, pattern)
		}
	}
}

func TestStatusAndWindowVocabulariesMatchPublishedSchemas(t *testing.T) {
	var projection struct {
		Properties struct {
			Status struct {
				Enum []string `json:"enum"`
			} `json:"status"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(repoFile(t, "contracts/jsonschema/v1/context_fabric_answer_projection.v1.schema.json"), &projection); err != nil {
		t.Fatal(err)
	}
	in := FromRegistries()
	if len(projection.Properties.Status.Enum) == 0 || !slices.Equal(projection.Properties.Status.Enum, in.Statuses) {
		t.Errorf("status enum %v != guide statuses %v", projection.Properties.Status.Enum, in.Statuses)
	}
	var request struct {
		Defs struct {
			Window struct {
				Enum []string `json:"enum"`
			} `json:"RelativeWindowID"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(repoFile(t, "contracts/jsonschema/v1/mcp_investigate_question_request.v1.schema.json"), &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Defs.Window.Enum) == 0 || !slices.Equal(request.Defs.Window.Enum, in.Windows) {
		t.Errorf("window enum %v != guide windows %v", request.Defs.Window.Enum, in.Windows)
	}
}

func TestConversationGuideNamesRealTools(t *testing.T) {
	var manifest struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(repoFile(t, "contracts/mcp/tools.v1.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range manifest.Tools {
		names[tool.Name] = true
	}
	if len(names) == 0 {
		t.Fatal("no tools in manifest")
	}
	files := embeddedFiles(t)
	for _, tool := range []string{"investigate_question", "investigation_result", "source_evidence", "context_for_task"} {
		if !names[tool] {
			t.Errorf("guide names tool %s, absent from the tool manifest", tool)
		}
		if !strings.Contains(files[FileQuestions]+files[FileConversation], "`"+tool+"`") {
			t.Errorf("guide does not name tool %s", tool)
		}
	}
}

// Planted-defect controls: dropping any registry entry must break parity with
// the embedded content, either as a Build error or as a content difference.
func TestParityDetectsRemovedRegistryEntries(t *testing.T) {
	embedded := embeddedFiles(t)
	drop := func(in Inputs) map[string]Inputs {
		clone := func() Inputs {
			c := in
			c.Families = slices.Clone(in.Families)
			c.SubjectKinds = slices.Clone(in.SubjectKinds)
			c.ServableCohortKinds = slices.Clone(in.ServableCohortKinds)
			c.Grammars = slices.Clone(in.Grammars)
			c.Windows = slices.Clone(in.Windows)
			c.Statuses = slices.Clone(in.Statuses)
			c.UnproducedRenderKinds = slices.Clone(in.UnproducedRenderKinds)
			return c
		}
		cases := map[string]Inputs{}
		c := clone()
		c.Families = c.Families[1:]
		cases["family"] = c
		c = clone()
		c.SubjectKinds = c.SubjectKinds[:len(c.SubjectKinds)-1]
		cases["subject kind"] = c
		c = clone()
		c.ServableCohortKinds = c.ServableCohortKinds[1:]
		cases["cohort kind"] = c
		c = clone()
		c.Grammars = c.Grammars[1:]
		cases["grammar"] = c
		c = clone()
		c.Windows = c.Windows[1:]
		cases["window"] = c
		c = clone()
		c.Statuses = c.Statuses[1:]
		cases["status"] = c
		c = clone()
		c.Families[0].Unreachable = !c.Families[0].Unreachable
		cases["reachability flip"] = c
		c = clone()
		c.UnproducedRenderKinds = slices.DeleteFunc(c.UnproducedRenderKinds, func(k string) bool { return k == "table" })
		cases["render kind became produced"] = c
		return cases
	}(FromRegistries())
	if len(drop) == 0 {
		t.Fatal("no planted cases")
	}
	for name, in := range drop {
		built, err := Build(in)
		if err == nil && sameFiles(built, embedded) {
			t.Errorf("planted defect %q went undetected", name)
		}
	}
}
