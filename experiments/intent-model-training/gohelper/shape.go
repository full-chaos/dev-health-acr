package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The model-facing output shape of genkitruntime.interpretationOutput and
// its nested structs, in declaration order, with each field's omitempty
// flag. The type is unexported, so this table cannot be derived at run
// time; shape_test.go derives the same table from runtime.go's AST and
// from the exported schema and fails on any difference, so a production
// change cannot silently drift from it.
type fieldKind int

const (
	kindString fieldKind = iota
	kindBool
	kindStringList
	kindTimestamp
	kindStringMap
	kindObject
	kindObjectList
)

type fieldSpec struct {
	Name      string
	OmitEmpty bool
	Kind      fieldKind
	Child     *objectSpec
}

type objectSpec struct {
	GoType string
	Fields []fieldSpec
}

var operandSpec = &objectSpec{GoType: "subjectOperandOutput", Fields: []fieldSpec{
	{Name: "kind", OmitEmpty: true, Kind: kindString},
	{Name: "terms", OmitEmpty: true, Kind: kindStringList},
	{Name: "anchor_terms", OmitEmpty: true, Kind: kindStringList},
	{Name: "member_kind", OmitEmpty: true, Kind: kindString},
	{Name: "member_qualifier", OmitEmpty: true, Kind: kindString},
}}

var subjectExpressionSpec = &objectSpec{GoType: "subjectExpressionOutput", Fields: []fieldSpec{
	{Name: "kind", OmitEmpty: true, Kind: kindString},
	{Name: "terms", OmitEmpty: true, Kind: kindStringList},
	{Name: "anchor_terms", OmitEmpty: true, Kind: kindStringList},
	{Name: "member_kind", OmitEmpty: true, Kind: kindString},
	{Name: "member_qualifier", OmitEmpty: true, Kind: kindString},
	{Name: "group_kind", OmitEmpty: true, Kind: kindString},
	{Name: "operands", OmitEmpty: true, Kind: kindObjectList, Child: operandSpec},
}}

var questionFrameSpec = &objectSpec{GoType: "questionFrameOutput", Fields: []fieldSpec{
	{Name: "goals", OmitEmpty: true, Kind: kindStringList},
	{Name: "subject_expression", OmitEmpty: true, Kind: kindObject, Child: subjectExpressionSpec},
	{Name: "temporal", OmitEmpty: true, Kind: kindString},
	{Name: "emphasis", OmitEmpty: true, Kind: kindStringList},
	{Name: "dimensions", OmitEmpty: true, Kind: kindStringList},
}}

var timeContextSpec = &objectSpec{GoType: "outputTimeContext", Fields: []fieldSpec{
	{Name: "axis", Kind: kindString},
	{Name: "as_of", OmitEmpty: true, Kind: kindTimestamp},
	{Name: "start", OmitEmpty: true, Kind: kindTimestamp},
	{Name: "end", OmitEmpty: true, Kind: kindTimestamp},
}}

var factRequirementSpec = &objectSpec{GoType: "factRequirementOutput", Fields: []fieldSpec{
	{Name: "kind", Kind: kindString},
	{Name: "parameters", OmitEmpty: true, Kind: kindStringMap},
}}

var outputSpec = &objectSpec{GoType: "interpretationOutput", Fields: []fieldSpec{
	{Name: "shape", Kind: kindString},
	{Name: "requested_judgment", Kind: kindString},
	{Name: "subject_terms", OmitEmpty: true, Kind: kindStringList},
	{Name: "comparison_terms", OmitEmpty: true, Kind: kindStringList},
	{Name: "time_context", Kind: kindObject, Child: timeContextSpec},
	{Name: "fact_requirements", Kind: kindObjectList, Child: factRequirementSpec},
	{Name: "clarification_needed", Kind: kindBool},
	{Name: "clarification_reason", OmitEmpty: true, Kind: kindString},
	{Name: "window_class", OmitEmpty: true, Kind: kindString},
	{Name: "window_confidence", OmitEmpty: true, Kind: kindString},
	{Name: "requested_judgment_kind", OmitEmpty: true, Kind: kindString},
	{Name: "question_family", OmitEmpty: true, Kind: kindString},
	{Name: "group_kind", OmitEmpty: true, Kind: kindString},
	{Name: "scope_anchor_term", OmitEmpty: true, Kind: kindString},
	{Name: "scope_anchor_kind", OmitEmpty: true, Kind: kindString},
	{Name: "requested_subject_kind", OmitEmpty: true, Kind: kindString},
	{Name: "question_frame", OmitEmpty: true, Kind: kindObject, Child: questionFrameSpec},
}}

func (s *objectSpec) field(name string) (fieldSpec, bool) {
	for _, f := range s.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return fieldSpec{}, false
}

// shapeFindings are the strict-mode observations production would accept
// silently (encoding/json semantics) or that make a target ambiguous.
type shapeFindings struct {
	UnknownFields   []string
	CaseVariantKeys []string
	NullPaths       []string
	UntrimmedPaths  []string
}

// inspectShape walks the ordered tree against the output shape. Keys are
// matched exactly; a key that equals a known field only case-insensitively
// is reported separately, because encoding/json would still bind it.
func inspectShape(root *node) shapeFindings {
	var findings shapeFindings
	inspectObject(root, outputSpec, "$", &findings)
	return findings
}

func inspectObject(n *node, spec *objectSpec, path string, findings *shapeFindings) {
	if n.Kind == nodeNull {
		findings.NullPaths = append(findings.NullPaths, path)
		return
	}
	if n.Kind != nodeObject {
		return
	}
	for _, m := range n.Members {
		childPathValue := childPath(path, m.Key)
		f, known := spec.field(m.Key)
		if !known {
			if variant := caseVariantOf(spec, m.Key); variant != "" {
				findings.CaseVariantKeys = append(findings.CaseVariantKeys, childPathValue)
			} else {
				findings.UnknownFields = append(findings.UnknownFields, childPathValue)
			}
			continue
		}
		inspectValue(m.Value, f, childPathValue, findings)
	}
}

func inspectValue(n *node, f fieldSpec, path string, findings *shapeFindings) {
	if n.Kind == nodeNull {
		findings.NullPaths = append(findings.NullPaths, path)
		return
	}
	switch f.Kind {
	case kindString, kindTimestamp:
		checkTrimmed(n, path, findings)
	case kindStringList:
		for i, item := range n.Items {
			itemPath := path + "[" + itoa(i) + "]"
			if item.Kind == nodeNull {
				findings.NullPaths = append(findings.NullPaths, itemPath)
				continue
			}
			checkTrimmed(item, itemPath, findings)
		}
	case kindStringMap:
		for _, m := range n.Members {
			valuePath := childPath(path, m.Key)
			if m.Value.Kind == nodeNull {
				findings.NullPaths = append(findings.NullPaths, valuePath)
				continue
			}
			checkTrimmed(m.Value, valuePath, findings)
		}
	case kindObject:
		inspectObject(n, f.Child, path, findings)
	case kindObjectList:
		for i, item := range n.Items {
			inspectObject(item, f.Child, path+"["+itoa(i)+"]", findings)
		}
	}
}

func checkTrimmed(n *node, path string, findings *shapeFindings) {
	if n.Kind == nodeString && strings.TrimSpace(n.String) != n.String {
		findings.UntrimmedPaths = append(findings.UntrimmedPaths, path)
	}
}

func caseVariantOf(spec *objectSpec, key string) string {
	for _, f := range spec.Fields {
		if strings.EqualFold(f.Name, key) {
			return f.Name
		}
	}
	return ""
}

func itoa(i int) string {
	return strconv.Itoa(i)
}

// canonicalize renders the target in one deterministic form: fields in
// production declaration order, production omitempty semantics applied
// (an omitted field and its empty value decode identically), timestamps
// in UTC RFC 3339, string content unchanged, no insignificant whitespace.
// It is only called on a tree with no duplicate, unknown or case-variant
// keys, so no value is lost.
func canonicalize(root *node) (string, error) {
	var buf bytes.Buffer
	if err := writeObject(&buf, root, outputSpec); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func writeObject(buf *bytes.Buffer, n *node, spec *objectSpec) error {
	if n.Kind != nodeObject {
		return errors.New("canonicalize: expected an object")
	}
	buf.WriteByte('{')
	first := true
	for _, f := range spec.Fields {
		value, ok := n.lookup(f.Name)
		if !ok || omitted(value, f) {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		writeString(buf, f.Name)
		buf.WriteByte(':')
		if err := writeField(buf, value, f); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// omitted mirrors encoding/json's omitempty for the field kinds this shape
// uses. A null is treated as absent: decoding null leaves the Go zero value.
func omitted(n *node, f fieldSpec) bool {
	if n.Kind == nodeNull {
		return f.OmitEmpty || f.Kind == kindObject || f.Kind == kindTimestamp
	}
	if !f.OmitEmpty {
		return false
	}
	switch f.Kind {
	case kindString:
		return n.Kind == nodeString && n.String == ""
	case kindStringList, kindObjectList:
		return n.Kind == nodeArray && len(n.Items) == 0
	case kindStringMap:
		return n.Kind == nodeObject && len(n.Members) == 0
	}
	return false
}

func writeField(buf *bytes.Buffer, n *node, f fieldSpec) error {
	switch f.Kind {
	case kindString:
		if n.Kind == nodeNull {
			buf.WriteString(`""`)
			return nil
		}
		if n.Kind != nodeString {
			return errors.New("canonicalize: expected a string")
		}
		writeString(buf, n.String)
	case kindBool:
		if n.Kind == nodeNull {
			buf.WriteString("false")
			return nil
		}
		if n.Kind != nodeBool {
			return errors.New("canonicalize: expected a boolean")
		}
		if n.Bool {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case kindTimestamp:
		if n.Kind != nodeString {
			return errors.New("canonicalize: expected a timestamp string")
		}
		parsed, err := time.Parse(time.RFC3339Nano, n.String)
		if err != nil {
			return err
		}
		writeString(buf, parsed.UTC().Format(time.RFC3339Nano))
	case kindStringList:
		if n.Kind == nodeNull {
			buf.WriteString("[]")
			return nil
		}
		if n.Kind != nodeArray {
			return errors.New("canonicalize: expected an array")
		}
		buf.WriteByte('[')
		for i, item := range n.Items {
			if i > 0 {
				buf.WriteByte(',')
			}
			if item.Kind != nodeString {
				return errors.New("canonicalize: expected a string item")
			}
			writeString(buf, item.String)
		}
		buf.WriteByte(']')
	case kindStringMap:
		if n.Kind != nodeObject {
			return errors.New("canonicalize: expected an object map")
		}
		keys := make([]string, 0, len(n.Members))
		values := make(map[string]string, len(n.Members))
		for _, m := range n.Members {
			if m.Value.Kind != nodeString {
				return errors.New("canonicalize: expected a string map value")
			}
			if _, exists := values[m.Key]; !exists {
				keys = append(keys, m.Key)
			}
			values[m.Key] = m.Value.String
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, key)
			buf.WriteByte(':')
			writeString(buf, values[key])
		}
		buf.WriteByte('}')
	case kindObject:
		return writeObject(buf, n, f.Child)
	case kindObjectList:
		if n.Kind == nodeNull {
			buf.WriteString("[]")
			return nil
		}
		if n.Kind != nodeArray {
			return errors.New("canonicalize: expected an array of objects")
		}
		buf.WriteByte('[')
		for i, item := range n.Items {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeObject(buf, item, f.Child); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	}
	return nil
}

func writeString(buf *bytes.Buffer, value string) {
	var tmp bytes.Buffer
	encoder := json.NewEncoder(&tmp)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	buf.Write(bytes.TrimRight(tmp.Bytes(), "\n"))
}
