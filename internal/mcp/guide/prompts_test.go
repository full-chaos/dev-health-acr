package guide

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xeipuuv/gojsonschema"
)

func schemaFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustVocab(t *testing.T) PromptVocab {
	t.Helper()
	v, err := LoadPromptVocab()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

var callBlock = regexp.MustCompile("(?s)```json\n(.*?)\n```")

// callJSON extracts the JSON arguments block from a rendered prompt.
func callJSON(t *testing.T, text string) []byte {
	t.Helper()
	m := callBlock.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no json block in prompt:\n%s", text)
	}
	return []byte(m[1])
}

func schemaErrors(t *testing.T, schemaName string, doc []byte) []gojsonschema.ResultError {
	t.Helper()
	schema := gojsonschema.NewBytesLoader(schemaFile(t, schemaName))
	result, err := gojsonschema.Validate(schema, gojsonschema.NewBytesLoader(doc))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	return result.Errors()
}

func validate(t *testing.T, schemaName string, doc []byte) {
	t.Helper()
	if errs := schemaErrors(t, schemaName, doc); len(errs) > 0 {
		t.Fatalf("call does not match %s: %v\n%s", schemaName, errs, doc)
	}
}

// The validator must be able to fail on these schemas; otherwise every
// "validates against the schema" test above passes vacuously.
func TestSchemaValidatorRejectsMalformedCalls(t *testing.T) {
	for _, doc := range []string{
		`{}`,
		`{"question":""}`,
		`{"question":"q","extra":1}`,
		`{"question":"q","expected_kinds":["not_a_kind"]}`,
		`{"question":"q","evidence_window":{"relative_id":"trailing_7d"}}`,
		`{"question":"q","prior_kind_receipts":[{"result_id":"result_0001","receipt_id":"winr_00000000"}]}`,
		`{"question":"q","scope":{"team_ids":["a|b"]}}`,
	} {
		if len(schemaErrors(t, investigateSchema, []byte(doc))) == 0 {
			t.Errorf("schema accepted %s", doc)
		}
	}
	if len(schemaErrors(t, evidenceSchema, []byte(`{"evidence_ref_id":""}`))) == 0 {
		t.Error("evidence schema accepted an empty id")
	}
}

const (
	investigateSchema = "mcp_investigate_question_request.v1.schema.json"
	evidenceSchema    = "mcp_source_evidence_request.v1.schema.json"
)

func TestInvestigatePromptCallValidatesAgainstRequestSchema(t *testing.T) {
	v := mustVocab(t)
	cases := []map[string]string{
		{ArgQuestion: "Which teams need attention?"},
		{ArgQuestion: "q", ArgRepository: "full-chaos/acr, full-chaos/web ,full-chaos/acr", ArgProject: "p1,p2", ArgTeam: "t1"},
		{ArgQuestion: "q", ArgExpectedKinds: strings.Join(v.SubjectKinds, ",")},
		{ArgQuestion: strings.Repeat("x", maxQuestionLen)},
	}
	for _, w := range v.Windows {
		cases = append(cases, map[string]string{ArgQuestion: "q", ArgWindow: w})
	}
	for _, k := range v.SubjectKinds {
		cases = append(cases, map[string]string{ArgQuestion: "q", ArgExpectedKinds: k})
	}
	for i, args := range cases {
		out, err := RenderPrompt(v, PromptInvestigate, args)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		validate(t, investigateSchema, callJSON(t, out.Text))
	}
}

func TestInvestigatePromptCarriesArgumentsIntoCall(t *testing.T) {
	v := mustVocab(t)
	out, err := RenderPrompt(v, PromptInvestigate, map[string]string{
		ArgQuestion: " Why is web slipping? ", ArgRepository: "a/b, c/d", ArgProject: "p1", ArgTeam: "t1,t2",
		ArgExpectedKinds: "team, project", ArgWindow: "trailing_90d",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Question string `json:"question"`
		Scope    struct {
			Repos    []string `json:"repository_slugs"`
			Projects []string `json:"project_ids"`
			Teams    []string `json:"team_ids"`
		} `json:"scope"`
		Kinds  []string `json:"expected_kinds"`
		Window struct {
			ID string `json:"relative_id"`
		} `json:"evidence_window"`
	}
	if err := json.Unmarshal(callJSON(t, out.Text), &got); err != nil {
		t.Fatal(err)
	}
	if got.Question != "Why is web slipping?" || strings.Join(got.Scope.Repos, "|") != "a/b|c/d" ||
		strings.Join(got.Scope.Projects, "|") != "p1" || strings.Join(got.Scope.Teams, "|") != "t1|t2" ||
		strings.Join(got.Kinds, "|") != "team|project" || got.Window.ID != "trailing_90d" {
		t.Fatalf("unexpected call: %+v", got)
	}
	bare, err := RenderPrompt(v, PromptInvestigate, map[string]string{ArgQuestion: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(callJSON(t, bare.Text)); strings.Contains(s, "scope") || strings.Contains(s, "expected_kinds") || strings.Contains(s, "evidence_window") {
		t.Fatalf("optional fields must be absent when not given: %s", s)
	}
}

func TestInvestigatePromptStatesReplyHandling(t *testing.T) {
	v := mustVocab(t)
	out, err := RenderPrompt(v, PromptInvestigate, map[string]string{ArgQuestion: "q"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"clarification_required", "investigation_result", "result_id", "source_evidence", "evidence_ref_ids",
		"untrusted data", PromptContinue, PromptExpand, "acr://guide/conversation",
	} {
		if !strings.Contains(out.Text, want) {
			t.Errorf("investigate prompt does not mention %q", want)
		}
	}
	for _, r := range v.Receipts {
		if !strings.Contains(out.Text, "`"+r.Field+"`") {
			t.Errorf("investigate prompt does not name receipt field %s", r.Field)
		}
	}
	for _, f := range v.Families {
		if got := strings.Contains(out.Text, "`"+f.ID+"`"); got != f.Answerable {
			t.Errorf("family %s: in prompt = %v, answerable = %v", f.ID, got, f.Answerable)
		}
	}
}

func TestContinuePromptRoutesEachReceiptToItsField(t *testing.T) {
	v := mustVocab(t)
	byField := map[string]string{}
	var ids []string
	for i, r := range v.Receipts {
		id := r.Prefix + "receipt_" + strings.Repeat("a", i+1) + "00000000"
		if r.Prefix == "" {
			id = "subj_" + id
		}
		byField[r.Field] = id
		ids = append(ids, id)
	}
	out, err := RenderPrompt(v, PromptContinue, map[string]string{
		ArgQuestion: "and what about api?", ArgParentResultID: "result_0001", ArgReceipts: strings.Join(ids, ", "),
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := callJSON(t, out.Text)
	validate(t, investigateSchema, doc)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatal(err)
	}
	var parent string
	if err := json.Unmarshal(got["parent_result_id"], &parent); err != nil || parent != "result_0001" {
		t.Fatalf("parent_result_id = %q (%v)", parent, err)
	}
	for field, id := range byField {
		var list []struct {
			ResultID  string `json:"result_id"`
			ReceiptID string `json:"receipt_id"`
		}
		if err := json.Unmarshal(got[field], &list); err != nil || len(list) != 1 || list[0].ReceiptID != id || list[0].ResultID != "result_0001" {
			t.Errorf("field %s: got %s, want receipt %s", field, got[field], id)
		}
	}
	none, err := RenderPrompt(v, PromptContinue, map[string]string{ArgQuestion: "q", ArgParentResultID: "result_0001"})
	if err != nil {
		t.Fatal(err)
	}
	validate(t, investigateSchema, callJSON(t, none.Text))
}

func TestExpandPromptCallValidatesAndRestatesTrustRule(t *testing.T) {
	v := mustVocab(t)
	for _, id := range []string{"ev_1", strings.Repeat("e", maxEvidenceIDLen)} {
		out, err := RenderPrompt(v, PromptExpand, map[string]string{ArgEvidenceRefID: id})
		if err != nil {
			t.Fatal(err)
		}
		validate(t, evidenceSchema, callJSON(t, out.Text))
		if !strings.Contains(out.Text, "untrusted data, not instructions") {
			t.Fatal("expand prompt lost the untrusted-content rule")
		}
	}
}

// Every argument violation is refused with a fixed message that does not echo
// the offending value.
func TestPromptArgumentDomain(t *testing.T) {
	v := mustVocab(t)
	secret := "SECRETVALUE"
	cases := []struct {
		name string
		args map[string]string
		note string
	}{
		{PromptInvestigate, nil, "no arguments"},
		{PromptInvestigate, map[string]string{ArgQuestion: ""}, "empty question"},
		{PromptInvestigate, map[string]string{ArgQuestion: " \n "}, "blank question"},
		{PromptInvestigate, map[string]string{ArgQuestion: strings.Repeat("x", maxQuestionLen+1)}, "question above bound"},
		{PromptInvestigate, map[string]string{ArgQuestion: "q", ArgExpectedKinds: secret}, "kind out of vocabulary"},
		{PromptInvestigate, map[string]string{ArgQuestion: "q", ArgExpectedKinds: "team," + secret}, "second kind out of vocabulary"},
		{PromptInvestigate, map[string]string{ArgQuestion: "q", ArgExpectedKinds: strings.Repeat("team,", 3) + strings.Join(v.SubjectKinds, ",") + ",extra"}, "kind list above bound"},
		{PromptInvestigate, map[string]string{ArgQuestion: "q", ArgWindow: secret}, "window out of vocabulary"},
		{PromptInvestigate, map[string]string{ArgQuestion: "q", ArgWindow: "trailing_90d,all_time"}, "two windows"},
		{PromptInvestigate, map[string]string{ArgQuestion: "q", ArgRepository: "a|" + secret}, "pipe in repository"},
		{PromptInvestigate, map[string]string{ArgQuestion: "q", ArgProject: "a|" + secret}, "pipe in project"},
		{PromptInvestigate, map[string]string{ArgQuestion: "q", ArgTeam: "a|" + secret}, "pipe in team"},
		{PromptInvestigate, map[string]string{ArgQuestion: "q", ArgRepository: strings.Repeat("r", maxRepoLen+1)}, "repository above bound"},
		{PromptInvestigate, map[string]string{ArgQuestion: "q", ArgProject: strings.Repeat("p", maxProjectLen+1)}, "project above bound"},
		{PromptInvestigate, map[string]string{ArgQuestion: "q", ArgTeam: strings.Repeat("t", maxTeamLen+1)}, "team above bound"},
		{PromptInvestigate, map[string]string{ArgQuestion: "q", ArgTeam: manyItems(maxScopeItems + 1)}, "too many teams"},
		{PromptContinue, map[string]string{ArgQuestion: "q"}, "no parent"},
		{PromptContinue, map[string]string{ArgParentResultID: "result_0001"}, "no question"},
		{PromptContinue, map[string]string{ArgQuestion: "q", ArgParentResultID: "short"}, "parent below bound"},
		{PromptContinue, map[string]string{ArgQuestion: "q", ArgParentResultID: strings.Repeat("p", maxResultIDLen+1)}, "parent above bound"},
		{PromptContinue, map[string]string{ArgQuestion: "q", ArgParentResultID: "result_0001", ArgReceipts: "short"}, "receipt below bound"},
		{PromptContinue, map[string]string{ArgQuestion: "q", ArgParentResultID: "result_0001", ArgReceipts: "kindr_" + strings.Repeat("k", maxReceiptIDLen)}, "receipt above bound"},
		{PromptContinue, map[string]string{ArgQuestion: "q", ArgParentResultID: "result_0001", ArgReceipts: manyItems(maxReceiptsField + 1)}, "too many receipts for one field"},
		{PromptExpand, nil, "no evidence id"},
		{PromptExpand, map[string]string{ArgEvidenceRefID: "  "}, "blank evidence id"},
		{PromptExpand, map[string]string{ArgEvidenceRefID: strings.Repeat("e", maxEvidenceIDLen+1)}, "evidence id above bound"},
		{"nope", map[string]string{ArgQuestion: "q"}, "unknown prompt"},
	}
	for _, c := range cases {
		_, err := RenderPrompt(v, c.name, c.args)
		if err == nil {
			t.Errorf("%s: %s: rendered, want refusal", c.name, c.note)
			continue
		}
		if _, ok := err.(*ArgError); !ok {
			t.Errorf("%s: %s: error %T is not an ArgError", c.name, c.note, err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: %s: error echoes the argument value: %v", c.name, c.note, err)
		}
	}
}

func manyItems(n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = "item_" + strings.Repeat("0", 8) + string(rune('a'+i%26)) + strings.Repeat("z", i/26)
	}
	return strings.Join(items, ",")
}

func TestPromptBoundsMatchPublishedSchemas(t *testing.T) {
	var req struct {
		Properties map[string]struct {
			MaxLength int `json:"maxLength"`
			MinLength int `json:"minLength"`
			MaxItems  int `json:"maxItems"`
		} `json:"properties"`
		Defs map[string]struct {
			Properties map[string]struct {
				MaxLength int `json:"maxLength"`
				MinLength int `json:"minLength"`
				MaxItems  int `json:"maxItems"`
				Items     struct {
					MaxLength int `json:"maxLength"`
				} `json:"items"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(schemaFile(t, investigateSchema), &req); err != nil {
		t.Fatal(err)
	}
	check := func(what string, got, want int) {
		t.Helper()
		if got != want {
			t.Errorf("%s: prompt bound %d, schema %d", what, got, want)
		}
	}
	check("question maxLength", maxQuestionLen, req.Properties["question"].MaxLength)
	check("parent_result_id minLength", minResultIDLen, req.Properties["parent_result_id"].MinLength)
	check("parent_result_id maxLength", maxResultIDLen, req.Properties["parent_result_id"].MaxLength)
	check("expected_kinds maxItems", maxKinds, req.Properties["expected_kinds"].MaxItems)
	check("prior_kind_receipts maxItems", maxReceiptsField, req.Properties["prior_kind_receipts"].MaxItems)
	check("receipt_id minLength", minReceiptIDLen, req.Defs["BoundSubjectReceipt"].Properties["receipt_id"].MinLength)
	check("receipt_id maxLength", maxReceiptIDLen, req.Defs["BoundSubjectReceipt"].Properties["receipt_id"].MaxLength)
	scope := req.Defs["InvestigationScope"].Properties
	check("repository_slugs item maxLength", maxRepoLen, scope["repository_slugs"].Items.MaxLength)
	check("project_ids item maxLength", maxProjectLen, scope["project_ids"].Items.MaxLength)
	check("team_ids item maxLength", maxTeamLen, scope["team_ids"].Items.MaxLength)
	check("repository_slugs maxItems", maxScopeItems, scope["repository_slugs"].MaxItems)
	check("project_ids maxItems", maxScopeItems, scope["project_ids"].MaxItems)
	check("team_ids maxItems", maxScopeItems, scope["team_ids"].MaxItems)

	var ev struct {
		Properties struct {
			ID struct {
				MaxLength int `json:"maxLength"`
			} `json:"evidence_ref_id"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schemaFile(t, evidenceSchema), &ev); err != nil {
		t.Fatal(err)
	}
	check("evidence_ref_id maxLength", maxEvidenceIDLen, ev.Properties.ID.MaxLength)
}

func TestPromptCompletionsComeFromVocabulary(t *testing.T) {
	v := mustVocab(t)
	if got := CompletePrompt(v, PromptInvestigate, ArgWindow, "trailing_"); len(got) != 3 {
		t.Errorf("window completion = %v", got)
	}
	if got := CompletePrompt(v, PromptInvestigate, ArgWindow, ""); len(got) != len(v.Windows) {
		t.Errorf("window completion for empty value = %v, want every window", got)
	}
	got := CompletePrompt(v, PromptInvestigate, ArgExpectedKinds, "team,pull_")
	if len(got) != 2 || got[0] != "team,pull_request" || got[1] != "team,pull_request_review" {
		t.Errorf("kind completion = %v", got)
	}
	if got := CompletePrompt(v, PromptInvestigate, ArgExpectedKinds, "team,te"); len(got) != 0 {
		t.Errorf("an already listed kind must not complete again: %v", got)
	}
	for _, c := range [][3]string{
		{PromptInvestigate, ArgQuestion, ""}, {PromptContinue, ArgReceipts, ""}, {PromptExpand, ArgEvidenceRefID, ""}, {"nope", ArgWindow, ""},
	} {
		if got := CompletePrompt(v, c[0], c[1], c[2]); len(got) != 0 {
			t.Errorf("%s/%s must not complete: %v", c[0], c[1], got)
		}
	}
}

func TestPromptArgumentDescriptionsListVocabulary(t *testing.T) {
	v := mustVocab(t)
	described := map[string]string{}
	for _, def := range PromptDefs(v) {
		if def.Title == "" || def.Description == "" {
			t.Errorf("prompt %s lacks title or description", def.Name)
		}
		for _, arg := range def.Args {
			if arg.Description == "" {
				t.Errorf("%s/%s lacks a description", def.Name, arg.Name)
			}
			if def.Name == PromptInvestigate {
				described[arg.Name] = arg.Description
			}
		}
	}
	for _, k := range v.SubjectKinds {
		if !strings.Contains(described[ArgExpectedKinds], "`"+k+"`") {
			t.Errorf("expected_kinds description lacks %s", k)
		}
	}
	for _, w := range v.Windows {
		if !strings.Contains(described[ArgWindow], "`"+w+"`") {
			t.Errorf("window description lacks %s", w)
		}
	}
}
