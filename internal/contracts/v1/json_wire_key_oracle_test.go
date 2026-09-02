package v1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// This file derives the set of JSON keys a contract type emits by ASKING
// encoding/json, instead of modelling what it would do.
//
// The earlier version of this anchor re-implemented encoding/json's field
// resolution by hand. Two consecutive review rounds each found defects in
// that hand-written model, and none of them were in the surrounding logic:
//
//   round 1  promotion from an embedded struct whose TYPE is unexported
//   round 1  promotion from an anonymous field whose tag NAME is empty
//   round 2  tag-name normalization (Go TRUNCATES `bad'name` to `bad`)
//   round 2  embedded type ALIASES, which go/types reports as *types.Alias
//
// Four defects in one hand-written primitive is the signal that the
// primitive is being re-derived by hand instead of taken from the
// reference. The remedy is not a fifth patch: it is to delete the model.
//
// So the key set is now obtained by building a fully-populated value of the
// type, marshalling it, and reading the top-level keys back out. Promotion,
// unexported embedded structs, empty and invalid tag names, aliases, depth
// and tag conflict resolution, and any future change to encoding/json are
// all correct here BY CONSTRUCTION, because none of them are modelled any
// more. The reference implementation is the oracle.
//
// Every field is populated with a non-zero value, recursively, so that
// `omitempty` cannot hide a key that the type really does emit.
//
// What this file does NOT decide: the wire TYPE of a value. That is
// deliberately out of scope here and tracked separately; the `,string`
// guard in the parity test fails closed on the one tag option that changes
// a type without changing a key.

// oracleString is the sentinel every populated string field receives. It is
// deliberately not empty (so `omitempty` keeps the key) and deliberately
// recognisable in a failure dump.
const oracleString = "x"

// maxPopulateDepth bounds recursion so a self-referential contract type
// cannot hang the oracle. Contract documents are trees; if this bound is
// ever reached the type is reported rather than silently truncated.
const maxPopulateDepth = 12

// populationIssue records a field the oracle could not populate. These are
// reported rather than ignored: a field left at its zero value with
// `omitempty` would silently drop a key from the oracle's answer, which
// would read as "the schema publishes a property Go never emits".
type populationIssue struct {
	path   string
	reason string
}

// populator fills a value with non-zero data so that nothing is omitted.
type populator struct {
	issues []populationIssue
}

func (p *populator) populate(v reflect.Value, path string, depth int, seen map[reflect.Type]int) {
	if depth > maxPopulateDepth {
		p.issues = append(p.issues, populationIssue{path: path, reason: fmt.Sprintf("recursion depth %d exceeded (self-referential contract type?)", maxPopulateDepth)})
		return
	}
	t := v.Type()

	// time.Time is a struct with unexported state and its own MarshalJSON.
	// Writing into its fields would corrupt it; it gets a real value.
	if t == reflect.TypeOf(time.Time{}) {
		if v.CanSet() {
			v.Set(reflect.ValueOf(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)))
		}
		return
	}

	switch t.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			if !v.CanSet() {
				p.issues = append(p.issues, populationIssue{path: path, reason: "unsettable nil pointer"})
				return
			}
			v.Set(reflect.New(t.Elem()))
		}
		p.populate(v.Elem(), path, depth+1, seen)
	case reflect.Struct:
		// A type that is its own descendant would recurse forever. Allow a
		// small number of repeats so nested-but-finite shapes still fill.
		if seen[t] > 2 {
			return
		}
		seen[t]++
		for i := 0; i < t.NumField(); i++ {
			field := v.Field(i)
			name := t.Field(i).Name
			// reflect cannot Set an unexported field, but an unexported
			// EMBEDDED struct still promotes its exported fields onto the
			// wire -- exactly the round-1 case. reflect.NewAt on the
			// addressable field yields a settable view so those promoted
			// fields get populated too. Without this the promoted keys
			// would be zero and `omitempty` would hide them, making the
			// oracle wrong in the same direction the old model was.
			if !field.CanSet() {
				if !field.CanAddr() {
					p.issues = append(p.issues, populationIssue{path: path + "." + name, reason: "unexported and unaddressable"})
					continue
				}
				field = reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
			}
			p.populate(field, path+"."+name, depth+1, seen)
		}
		seen[t]--
	case reflect.Slice:
		if !v.CanSet() {
			return
		}
		elem := reflect.New(t.Elem()).Elem()
		p.populate(elem, path+"[0]", depth+1, seen)
		v.Set(reflect.Append(reflect.MakeSlice(t, 0, 1), elem))
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			p.populate(v.Index(i), fmt.Sprintf("%s[%d]", path, i), depth+1, seen)
		}
	case reflect.Map:
		if !v.CanSet() {
			return
		}
		m := reflect.MakeMap(t)
		key := reflect.New(t.Key()).Elem()
		p.populate(key, path+".<key>", depth+1, seen)
		val := reflect.New(t.Elem()).Elem()
		p.populate(val, path+".<value>", depth+1, seen)
		m.SetMapIndex(key, val)
		v.Set(m)
	case reflect.String:
		if v.CanSet() {
			v.SetString(oracleString)
		}
	case reflect.Bool:
		if v.CanSet() {
			v.SetBool(true)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if v.CanSet() {
			v.SetInt(1)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if v.CanSet() {
			v.SetUint(1)
		}
	case reflect.Float32, reflect.Float64:
		if v.CanSet() {
			v.SetFloat(1.5)
		}
	case reflect.Interface:
		// An EMPTY interface (`any`) accepts any concrete value, so it gets
		// the marker string -- that is enough to keep an `omitempty` key
		// present, which is all the oracle needs from it. `map[string]any`
		// metadata bags are the common shape here.
		//
		// A NON-empty interface cannot be satisfied generically; that one is
		// reported rather than silently left nil, because a nil interface
		// under `omitempty` would drop a key and read as "the schema
		// publishes a property Go never emits".
		if t.NumMethod() == 0 {
			if v.CanSet() {
				v.Set(reflect.ValueOf(oracleString))
			}
			return
		}
		p.issues = append(p.issues, populationIssue{path: path, reason: "non-empty interface (" + t.String() + "): no concrete type to populate generically"})
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		p.issues = append(p.issues, populationIssue{path: path, reason: "type cannot appear on the wire: " + t.Kind().String()})
	}
}

// wireKeysOf returns the TOP-LEVEL JSON keys the type actually emits, as
// reported by encoding/json itself.
func wireKeysOf(rt reflect.Type) (map[string]bool, []populationIssue, error) {
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	value := reflect.New(rt).Elem()
	p := &populator{}
	p.populate(value, rt.Name(), 0, map[reflect.Type]int{})

	encoded, err := json.Marshal(value.Interface())
	if err != nil {
		return nil, p.issues, fmt.Errorf("marshal populated %s: %w", rt.Name(), err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &document); err != nil {
		return nil, p.issues, fmt.Errorf("%s does not marshal to a JSON object (custom MarshalJSON?): %w", rt.Name(), err)
	}
	keys := make(map[string]bool, len(document))
	for key := range document {
		keys[key] = true
	}
	return keys, p.issues, nil
}

// visibleFieldsByWireKey maps a wire key to the struct field behind it,
// using reflect.VisibleFields -- the standard library's OWN promotion and
// shadowing logic, not a reimplementation of it.
//
// This is used ONLY to associate a key with a Go type for the enum and
// `,string` checks. It never decides which keys exist; wireKeysOf does that.
// A key with no association is reported by the caller rather than skipped
// silently.
func visibleFieldsByWireKey(rt reflect.Type) map[string]reflect.StructField {
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	out := map[string]reflect.StructField{}
	if rt.Kind() != reflect.Struct {
		return out
	}
	for _, field := range reflect.VisibleFields(rt) {
		if field.Anonymous {
			continue
		}
		if !field.IsExported() {
			continue
		}
		tag, hasTag := field.Tag.Lookup("json")
		name, _, _ := strings.Cut(tag, ",")
		if hasTag && name == "-" && !strings.Contains(tag, "-,") {
			continue
		}
		key := field.Name
		if name != "" && name != "-" {
			key = name
		}
		if _, exists := out[key]; exists {
			continue
		}
		out[key] = field
	}
	return out
}

// ---------------------------------------------------------------------------
// Oracle rot guards
// ---------------------------------------------------------------------------

// The oracle's correctness rests on encoding/json's behaviour, which can
// move under a Go upgrade. These fixtures pin the exact constructions the
// two review rounds found, and assert what the ORACLE answers for each.
//
// A Go release that changes any of these makes this test fail by name --
// which is the point. The alternative is that the anchor silently starts
// modelling a different encoder than the one the service ships with, and
// nothing anywhere says so.

type oracleUnexportedEmbed struct {
	Promoted string `json:"promoted_from_unexported"`
}

type OracleTagOnlyEmbed struct {
	TagOnly string `json:"promoted_from_tag_only"`
}

type OracleAliasTarget struct {
	Aliased string `json:"promoted_via_alias"`
}

// OracleAlias is a type ALIAS, which go/types reports as *types.Alias and
// the old hand-written model failed to recognise as promotable.
type OracleAlias = OracleAliasTarget

type oracleFixture struct {
	Plain string `json:"plain"`
	oracleUnexportedEmbed
	OracleTagOnlyEmbed `json:",omitempty"`
	OracleAlias
	Ignored     string `json:"-"`
	LiteralDash string `json:"-,"`
	Untagged    string
	InvalidTag  string `json:"bad'name"`
}

// TestWireKeyOracleMatchesEncodingJSON pins the oracle against the four
// constructions the review rounds produced, plus the tag forms the parity
// rules depend on.
func TestWireKeyOracleMatchesEncodingJSON(t *testing.T) {
	keys, issues, err := wireKeysOf(reflect.TypeOf(oracleFixture{}))
	if err != nil {
		t.Fatalf("oracle failed on its own fixture: %v", err)
	}
	for _, issue := range issues {
		t.Errorf("oracle could not populate %s: %s", issue.path, issue.reason)
	}

	// Every expectation here was MEASURED against encoding/json on go1.27,
	// not read off the specification.
	want := map[string]string{
		"plain":                    "an ordinary tagged field",
		"promoted_from_unexported": "promoted from an embedded struct whose TYPE is unexported -- round 1 finding (a)",
		"promoted_from_tag_only":   "promoted from an anonymous field whose tag has an EMPTY NAME -- round 1 finding (b)",
		"promoted_via_alias":       "promoted through a type ALIAS -- round 2 finding; go/types reports *types.Alias",
		"-":                        `json:"-," names a field literally "-", unlike json:"-" which drops it`,
		"Untagged":                 "an exported field with no tag emits under its GO FIELD NAME",
		"bad":                      `an invalid tag name is TRUNCATED at the first invalid character ("bad'name" -> "bad"), it is not discarded`,
	}
	for key, why := range want {
		if !keys[key] {
			t.Errorf("oracle did not report wire key %q, which encoding/json does emit: %s", key, why)
		}
	}
	for key := range keys {
		if _, expected := want[key]; !expected {
			t.Errorf("oracle reported unexpected wire key %q -- encoding/json's behaviour has moved, or the fixture changed", key)
		}
	}
	if keys["Ignored"] || keys["-,"] || keys["bad'name"] {
		t.Errorf(`oracle kept a key it must not: json:"-" must drop the field, and an invalid tag name must not survive untruncated. got keys: %v`, sortedKeys(keys))
	}
}

// TestWireKeyOracleFillsOmitemptyFields proves the populator defeats
// `omitempty`. Without this the oracle would under-report keys, and an
// under-reporting oracle turns every optional field into a false "the
// schema publishes a property Go never emits".
func TestWireKeyOracleFillsOmitemptyFields(t *testing.T) {
	type nested struct {
		Deep string `json:"deep,omitempty"`
	}
	type omitemptyFixture struct {
		Str    string            `json:"str,omitempty"`
		Num    int               `json:"num,omitempty"`
		Flag   bool              `json:"flag,omitempty"`
		Slice  []string          `json:"slice,omitempty"`
		Map    map[string]string `json:"map,omitempty"`
		Ptr    *nested           `json:"ptr,omitempty"`
		Struct nested            `json:"struct,omitempty"`
		Time   *time.Time        `json:"time,omitempty"`
	}
	keys, issues, err := wireKeysOf(reflect.TypeOf(omitemptyFixture{}))
	if err != nil {
		t.Fatalf("oracle failed: %v", err)
	}
	for _, issue := range issues {
		t.Errorf("oracle could not populate %s: %s", issue.path, issue.reason)
	}
	for _, key := range []string{"str", "num", "flag", "slice", "map", "ptr", "struct", "time"} {
		if !keys[key] {
			t.Errorf("oracle lost `omitempty` key %q -- the populator left it zero, so the oracle under-reports what the producer emits", key)
		}
	}
}

func sortedKeys(keys map[string]bool) []string {
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Runtime type index
// ---------------------------------------------------------------------------

// contractRootExemplars is one zero value per canonical document root.
//
// Only the ROOTS are listed. Every other contract type is DERIVED by
// walking these with reflect, so a nested type never needs a hand-written
// entry and cannot be forgotten. That keeps the hand-maintained surface at
// the size of the document set, which is closed and slow-moving, rather
// than the size of the type graph.
//
// The names here are cross-checked against schemaRootTypes: an exemplar
// with no document, or a document whose root type is not reachable here,
// fails TestRuntimeTypeIndexCoversEveryBoundType.
var contractRootExemplars = []any{
	AgentEpisode{}, AgentEpisodeCreate{}, Capabilities{}, ClientCredential{},
	ContextFabricAnswerProjection{}, ContextFabricInvestigationRequest{},
	ContextFabricInvestigationResult{}, ContextFabricOrgModelConfig{},
	ContextFabricOrgModelConfigWriteRequest{}, ContextFabricProjectionBatch{},
	ContextPacket{}, ContextPacketItem{}, ContextPacketRequest{},
	CredentialRevokeRequest{}, CredentialRevokeResponse{},
	CredentialRotateRequest{}, CredentialRotateResponse{},
	DeviceApprovalPreviewRequest{}, DeviceApprovalPreviewResponse{},
	DeviceApprovalRequest{}, DeviceApprovalResponse{},
	DeviceAuthorizationRequest{}, DeviceAuthorizationResponse{},
	DeviceTokenRequest{}, DeviceTokenResponse{},
	ErrorEnvelope{}, EvidenceRef{}, ExpandedEvidence{},
	MCPContextForTaskRequest{}, MCPContextForTaskResponse{},
	MCPInvestigateQuestionRequest{}, MCPInvestigateQuestionResponse{},
	MCPInvestigationResultRequest{}, MCPInvestigationResultResponse{},
	MCPRecordEpisodeRequest{}, MCPRecordEpisodeResponse{},
	MCPSourceEvidenceRequest{}, MCPSourceEvidenceResponse{},
	OAuthDeviceErrorResponse{}, OAuthTokenExchangeErrorResponse{},
	TokenExchangeResponse{},
}

// contractNonRootExemplars are published contract types that are NOT
// reachable from any document root through the Go type graph, listed so the
// oracle can still build a value for them.
//
// This is an exemplar list, not an exemption list: these types are anchored
// exactly like every other, they simply cannot be discovered by walking.
// Each entry states why it is unreachable, and an entry that BECOMES
// reachable is harmless (the walk finds it first).
var contractNonRootExemplars = []any{
	// CHAOS-4042's membership-verify anchor option. The v2 investigation
	// result reuses the v1 Go type on the wire and this type converts into
	// it via ToV1Wire, so nothing holds it as a field -- its own doc comment
	// says "nothing yet constructs a ContextFabricAnchorOptionV2". It is
	// still a published $def with a real producer type, so it stays
	// anchored rather than exempted.
	ContextFabricAnchorOptionV2{},
}

// runtimeTypeIndex maps a Go type NAME to its reflect.Type, by walking the
// document roots through every field, slice element, map value and pointer.
func runtimeTypeIndex() map[string]reflect.Type {
	index := map[string]reflect.Type{}
	var walk func(t reflect.Type, depth int)
	walk = func(t reflect.Type, depth int) {
		if depth > 24 {
			return
		}
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			t = t.Elem()
		}
		if t.Kind() == reflect.Map {
			walk(t.Key(), depth+1)
			t = t.Elem()
			for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
				t = t.Elem()
			}
		}
		if name := t.Name(); name != "" && t.PkgPath() != "" {
			if _, seen := index[name]; seen {
				return
			}
			index[name] = t
		}
		if t.Kind() != reflect.Struct || t == reflect.TypeOf(time.Time{}) {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			walk(t.Field(i).Type, depth+1)
		}
	}
	for _, exemplar := range contractRootExemplars {
		walk(reflect.TypeOf(exemplar), 0)
	}
	for _, exemplar := range contractNonRootExemplars {
		walk(reflect.TypeOf(exemplar), 0)
	}
	return index
}

// ---------------------------------------------------------------------------
// Fail-closed scans
// ---------------------------------------------------------------------------

// stringOptionFields reports every field in a type's own struct graph that
// carries the `,string` tag option, as a "Path.Field" list.
//
// This walks the STRUCT GRAPH directly and does not consult any key-to-field
// association, because the association is not encoding/json's own field
// selection and a review round evaded the previous guard through exactly
// that gap: an invalid tag name (`json:"oracle_total'broken,string"`) whose
// key Go truncates, so the association missed the field and the guard never
// fired while the emitted value was a string against an integer contract.
//
// A guard on a wire-TYPE property must not depend on knowing which KEY the
// field ends up under. Any `,string` anywhere in the graph is reported.
func stringOptionFields(rt reflect.Type) []string {
	var found []string
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type, path string, depth int)
	walk = func(t reflect.Type, path string, depth int) {
		if depth > 16 {
			return
		}
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			t = t.Elem()
		}
		if t.Kind() == reflect.Map {
			t = t.Elem()
			for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
				t = t.Elem()
			}
		}
		if t.Kind() != reflect.Struct || t == reflect.TypeOf(time.Time{}) {
			return
		}
		if seen[t] {
			return
		}
		seen[t] = true
		defer delete(seen, t)
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if tag, ok := field.Tag.Lookup("json"); ok {
				_, options, _ := strings.Cut(tag, ",")
				for _, option := range strings.Split(options, ",") {
					if option == "string" {
						found = append(found, path+"."+field.Name)
					}
				}
			}
			walk(field.Type, path+"."+field.Name, depth+1)
		}
	}
	walk(rt, rt.Name(), 0)
	sort.Strings(found)
	return found
}

// customMarshalerTypes reports every type in a type's own struct graph that
// implements json.Marshaler, excluding time.Time which the populator models
// explicitly.
//
// The oracle observes ONE synthetic value. For an ordinary struct that is
// exhaustive -- encoding/json emits a key per eligible field regardless of
// value, and the populator ensures none is omitted. For a type with a custom
// MarshalJSON it is NOT: the marshaller can emit different key sets for
// different values, so a single sample proves nothing about the key space. A
// review round demonstrated a marshaller emitting {"a"} for the populated
// value and {"a","hidden"} for the zero value, with the anchor green.
//
// There are ZERO such types in this package today (measured). The scan
// exists so the first one fails closed instead of being silently sampled.
func customMarshalerTypes(rt reflect.Type) []string {
	marshaler := reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	timeType := reflect.TypeOf(time.Time{})
	var found []string
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type, path string, depth int)
	walk = func(t reflect.Type, path string, depth int) {
		if depth > 16 {
			return
		}
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			t = t.Elem()
		}
		if t.Kind() == reflect.Map {
			t = t.Elem()
			for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
				t = t.Elem()
			}
		}
		if seen[t] {
			return
		}
		seen[t] = true
		if t != timeType && t.PkgPath() != "" {
			if t.Implements(marshaler) || reflect.PointerTo(t).Implements(marshaler) {
				found = append(found, path+" ("+t.Name()+")")
			}
		}
		if t.Kind() != reflect.Struct || t == timeType {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			walk(t.Field(i).Type, path+"."+t.Field(i).Name, depth+1)
		}
	}
	walk(rt, rt.Name(), 0)
	sort.Strings(found)
	return found
}
