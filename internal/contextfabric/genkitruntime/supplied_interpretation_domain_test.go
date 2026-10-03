package genkitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// domainBaseline is an output that sets every property the schema declares
// where the validators allow it, so each schema path has a value to mutate.
func domainBaseline() interpretationOutput {
	output := richInterpretationOutput()
	output.ComparisonTerms = []string{"Project Beta"}
	output.RequestedJudgmentKind = "performance"
	output.FactRequirements = []factRequirementOutput{{Kind: "status", Parameters: map[string]string{"window": "30d"}}, {Kind: "readiness"}}
	output.QuestionFrame = &questionFrameOutput{
		Goals: []string{"compare"}, Temporal: "current", Emphasis: []string{"risk"}, Dimensions: []string{"status"},
		SubjectExpression: &subjectExpressionOutput{
			Kind: "explicit_set", Terms: []string{"Project Alpha"}, AnchorTerms: []string{"Project Alpha"},
			MemberKind: "work_item", MemberQualifier: "status", MemberQualifierValue: "open", GroupKind: "team",
			Operands: []subjectOperandOutput{{
				Kind: "children_of_scope", Terms: []string{"Project Alpha"}, AnchorTerms: []string{"Project Alpha"},
				MemberKind: "work_item", MemberQualifier: "status", MemberQualifierValue: "open",
			}},
		},
	}
	return output
}

// schemaPaths lists every property path the schema declares, with the JSON
// type the schema gives it. "[]" is the first element of an array.
func schemaPaths(node map[string]any, prefix []string, out map[string]string) {
	switch node["type"] {
	case "object":
		properties, _ := node["properties"].(map[string]any)
		for name, child := range properties {
			childNode := child.(map[string]any)
			path := append(append([]string(nil), prefix...), name)
			out[strings.Join(path, ".")], _ = childNode["type"].(string)
			schemaPaths(childNode, path, out)
		}
	case "array":
		items, _ := node["items"].(map[string]any)
		path := append(append([]string(nil), prefix...), "[]")
		out[strings.Join(path, ".")], _ = items["type"].(string)
		schemaPaths(items, path, out)
	}
}

type domainMutation struct {
	name   string
	remove bool
	value  any
}

func domainMutations() []domainMutation {
	return []domainMutation{
		{name: "canonical"},
		{name: "absent", remove: true},
		{name: "null", value: nil},
		{name: "zero", value: 0},
		{name: "fractional", value: 1.5},
		{name: "true", value: true},
		{name: "empty string", value: ""},
		{name: "blank string", value: "  "},
		{name: "out of vocabulary", value: "PLANTED_OUT_OF_VOCABULARY"},
		{name: "long string", value: strings.Repeat("x", 9000)},
		{name: "empty array", value: []any{}},
		{name: "array of empty string", value: []any{""}},
		{name: "duplicate array", value: []any{"dup", "dup"}},
		{name: "array of number", value: []any{1}},
		{name: "empty object", value: map[string]any{}},
		{name: "object with unknown key", value: map[string]any{"PLANTED_KEY": 1}},
	}
}

func jsonTypeOf(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case int, float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

// mutatePath applies one mutation at path in doc. It reports false when the
// path's parent does not exist in doc, so the cell has nothing to mutate.
func mutatePath(doc any, path []string, mutation domainMutation) bool {
	for index, segment := range path {
		last := index == len(path)-1
		if segment == "[]" {
			array, ok := doc.([]any)
			if !ok || len(array) == 0 {
				return false
			}
			if last {
				if mutation.remove {
					return false
				}
				array[0] = mutation.value
				return true
			}
			doc = array[0]
			continue
		}
		object, ok := doc.(map[string]any)
		if !ok {
			return false
		}
		if last {
			if mutation.remove {
				delete(object, segment)
			} else {
				object[segment] = mutation.value
			}
			return true
		}
		doc = object[segment]
	}
	return false
}

// comparableReceipt blanks the receipt fields that legitimately differ
// between the two paths: who ran the model, when, at what cost, and the
// digest of the exact bytes.
func comparableReceipt(receipt contextfabric.ModelExecutionReceipt) contextfabric.ModelExecutionReceipt {
	receipt.Provider, receipt.Model, receipt.ModelVersion = "", "", ""
	receipt.StartedAt, receipt.CompletedAt = receipt.StartedAt.Truncate(0), receipt.StartedAt.Truncate(0)
	receipt.Usage = contextfabric.ModelUsage{}
	receipt.OutputDigest = ""
	return receipt
}

// TestSuppliedInterpretationDomainNeverAcceptsWhatTheModelPathWouldNot sweeps
// the input domain of the supplied output: every property the schema
// declares, crossed with every mutation shape. For each cell it runs the
// supplied path and the model path on the same bytes and holds three rules:
// an accepted supplied output is one the model path accepts with an identical
// interpretation and receipt; a refused one is a typed rejection; and a value
// whose JSON type is not the type the schema declares is always refused.
func TestSuppliedInterpretationDomainNeverAcceptsWhatTheModelPathWouldNot(t *testing.T) {
	t.Parallel()
	document, err := InterpretationOutputSchema()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(document, &schema); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	schemaPaths(schema, nil, paths)
	var names []string
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	supplied := mustSuppliedInterpreter(t, quiet)
	principal := storage.Principal{OrgID: "org_1"}
	baseline := mustMarshalOutput(t, domainBaseline())

	type tally struct{ cells, accepted, refused, strictOnly, wrongType int }
	byMutation := map[string]*tally{}
	total := tally{}
	for _, name := range names {
		for _, mutation := range domainMutations() {
			var doc any
			if err := json.Unmarshal(baseline, &doc); err != nil {
				t.Fatal(err)
			}
			if mutation.name != "canonical" && !mutatePath(doc, strings.Split(name, "."), mutation) {
				continue
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			cell := fmt.Sprintf("%s <- %s", name, mutation.name)
			count := byMutation[mutation.name]
			if count == nil {
				count = &tally{}
				byMutation[mutation.name] = count
			}
			count.cells++
			total.cells++

			gotQuestion, gotReceipt, suppliedErr := supplied.InterpretSuppliedQuestion(context.Background(), principal, suppliedRequestFor(supplied, raw))

			var decoded interpretationOutput
			modelErr := json.Unmarshal(raw, &decoded)
			var wantQuestion contextfabric.InterpretedQuestion
			var wantReceipt contextfabric.ModelExecutionReceipt
			if modelErr == nil {
				model := mustRuntime(t, &generatorStub{interpretation: decoded}, Config{Logger: quiet})
				wantQuestion, wantReceipt, modelErr = model.InterpretQuestion(context.Background(), principal, validRequest())
			}

			wrongType := !mutation.remove && mutation.name != "canonical" && jsonTypeOf(mutation.value) != paths[name]
			if wrongType {
				count.wrongType++
				total.wrongType++
				if suppliedErr == nil {
					t.Errorf("%s: accepted a %s where the schema declares %s", cell, jsonTypeOf(mutation.value), paths[name])
				}
			}
			if suppliedErr != nil {
				count.refused++
				total.refused++
				if !errors.Is(suppliedErr, contextfabric.ErrInterpretationRejected) || gotReceipt.Outcome != "invalid_output" {
					t.Errorf("%s: refused with %v and receipt outcome %q, want a typed rejection with an invalid_output receipt", cell, suppliedErr, gotReceipt.Outcome)
				}
				if modelErr == nil {
					count.strictOnly++
					total.strictOnly++
				}
				continue
			}
			count.accepted++
			total.accepted++
			if modelErr != nil {
				t.Errorf("%s: the supplied path accepted an output the model path rejects: %v", cell, modelErr)
				continue
			}
			if !reflect.DeepEqual(gotQuestion, wantQuestion) {
				t.Errorf("%s: interpreted question differs\nsupplied: %#v\nmodel:    %#v", cell, gotQuestion, wantQuestion)
			}
			if got, want := comparableReceipt(gotReceipt), comparableReceipt(wantReceipt); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: receipt differs beyond the model identity\nsupplied: %#v\nmodel:    %#v", cell, got, want)
			}
		}
	}

	var mutationNames []string
	for name := range byMutation {
		mutationNames = append(mutationNames, name)
	}
	sort.Strings(mutationNames)
	t.Logf("schema paths = %d, cells = %d, accepted = %d, refused = %d, refused only by the supplied path = %d, wrong-type cells = %d",
		len(names), total.cells, total.accepted, total.refused, total.strictOnly, total.wrongType)
	for _, name := range mutationNames {
		count := byMutation[name]
		t.Logf("%-24s cells=%-3d accepted=%-3d refused=%-3d supplied-only-refusals=%-3d wrong-type=%d", name, count.cells, count.accepted, count.refused, count.strictOnly, count.wrongType)
	}

	// The sweep must have measured something on every axis it claims.
	if len(names) < 30 || total.cells < 400 {
		t.Fatalf("schema paths = %d, cells = %d: the sweep enumerated too little of the schema to mean anything", len(names), total.cells)
	}
	if canonical := byMutation["canonical"]; canonical == nil || canonical.accepted != canonical.cells || canonical.cells != len(names) {
		t.Fatalf("canonical cells = %#v, want the baseline accepted once per schema path (%d)", byMutation["canonical"], len(names))
	}
	if total.accepted <= byMutation["canonical"].cells {
		t.Fatalf("accepted = %d: no mutated output was accepted, so the equality rule was never exercised on a mutation", total.accepted)
	}
	if total.wrongType < 200 || total.strictOnly == 0 {
		t.Fatalf("wrong-type cells = %d, supplied-only refusals = %d: the strictness rule was not exercised", total.wrongType, total.strictOnly)
	}
}
