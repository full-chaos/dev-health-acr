package v1

import (
	"encoding/json"
	"go/types"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

// THE WIRE-PRESENCE CHECK IS THE SCHEMA'S REQUIRED LIST.
//
// Every Go client of the hosted API refuses a response whose wire-required key
// is absent or null through one walker (internal/sidecar, RequiredFieldsPresent).
// That walker derives "required" from this package's struct tags: a field
// without `,omitempty` is required. This test proves that derivation equals
// the PUBLISHED schema's `required` list for every schema object an
// investigation result reaches, so the walker is the schema's required list,
// not a second hand-kept one. A key added to either side without the other
// fails here.
//
// Exactly one pair differs, and the difference is the contract's own legacy
// rule rather than drift: the schema requires `completeness.state`, the Go tag
// omits it when empty, and ValidateStored accepts a stored result written
// before the field existed (empty state, no outcomes). The API re-serves that
// row without the key, so a presence check that demanded it would refuse a
// result the contract accepts. The exemption is pinned by an executed cell
// below: if the legacy rule ever goes away, the cell fails and the exemption
// must go with it.
func TestTheWirePresenceCheckRequiresExactlyTheSchemasRequiredKeysForAResult(t *testing.T) {
	t.Parallel()
	root := moduleRootForParity(t)
	schemas := loadCanonicalSchemas(t, root)
	pkg := loadContractsPackage(t, root)
	runtimeTypes := runtimeTypeIndex()
	bindings, _ := resolveBindings(t, schemas, pkg.Types.Scope())

	reachable := reachableStructNames(reflect.TypeOf(ContextFabricInvestigationResult{}))
	resultDocuments := map[string]bool{
		"context_fabric_investigation_result.v1.schema.json": true,
		"context_fabric_investigation_result.v2.schema.json": true,
		"context_fabric_common.v1.schema.json":               true,
	}
	legacyExemptions := map[string]map[string]bool{
		"ContextFabricAnswerCompleteness": {"state": true},
	}

	checked, exemptionsUsed := 0, 0
	for _, b := range bindings {
		if !resultDocuments[b.document] || !reachable[b.named.Obj().Name()] {
			continue
		}
		if _, isStruct := b.named.Underlying().(*types.Struct); !isStruct {
			continue
		}
		node, _, ok := schemas.objectNode(b.document, b.node)
		if !ok {
			t.Errorf("%s: unresolvable schema node", b.label)
			continue
		}
		rt, ok := runtimeTypes[b.named.Obj().Name()]
		if !ok {
			t.Errorf("%s: Go type %s is not in the runtime index, so this pair is unchecked", b.label, b.named.Obj().Name())
			continue
		}
		schemaRequired := map[string]bool{}
		if list, ok := node["required"].([]any); ok {
			for _, key := range list {
				schemaRequired[key.(string)] = true
			}
		}
		walkerRequired := nonOmitemptyWireKeys(rt)
		var walkerOnly, schemaOnly []string
		for key := range walkerRequired {
			if !schemaRequired[key] {
				walkerOnly = append(walkerOnly, key)
			}
		}
		for key := range schemaRequired {
			if walkerRequired[key] {
				continue
			}
			if legacyExemptions[b.named.Obj().Name()][key] {
				exemptionsUsed++
				continue
			}
			schemaOnly = append(schemaOnly, key)
		}
		sort.Strings(walkerOnly)
		sort.Strings(schemaOnly)
		for _, key := range walkerOnly {
			t.Errorf("%s: Go type %s requires wire key %q (no omitempty) but the published schema does not -- the presence check would refuse a document the schema accepts", b.label, b.named.Obj().Name(), key)
		}
		for _, key := range schemaOnly {
			t.Errorf("%s: the published schema requires %q but Go type %s marks it omitempty -- the presence check would accept a document the schema refuses", b.label, key, b.named.Obj().Name())
		}
		checked++
	}
	if checked < 20 {
		t.Fatalf("only %d schema objects reachable from a result were compared; the reachability walk is not reaching the result tree", checked)
	}
	if exemptionsUsed == 0 {
		t.Error("the completeness.state legacy exemption matched nothing; remove it")
	}
	t.Logf("PRESENCE PARITY: %d schema objects reachable from an investigation result compared; %d legacy exemption(s) used", checked, exemptionsUsed)

	// THE EXEMPTION'S REASON, EXECUTED. A stored result written before the
	// completeness state existed: empty state, no outcomes.
	legacy := validContextFabricContractResult()
	legacy.GeneratedAt = time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	legacy.Completeness.State = ""
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy row: %v", err)
	}
	var wire map[string]map[string]any
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	wire = map[string]map[string]any{}
	var completeness map[string]any
	_ = json.Unmarshal(raw["completeness"], &completeness)
	wire["completeness"] = completeness
	_, stateOnWire := wire["completeness"]["state"]
	storedErr := legacy.ValidateStored()
	schemaErr := contractcheck.ValidateSerialized("", "context_fabric_investigation_result.v1.schema.json", encoded)
	t.Logf("PRESENCE LEGACY CELL: state key on the wire=%t ValidateStored=%v schema=%v", stateOnWire, storedErr == nil, schemaErr == nil)
	if stateOnWire || storedErr != nil || schemaErr == nil {
		t.Errorf("the completeness.state exemption's reason no longer holds (state on wire=%t, ValidateStored error=%v, schema accepted=%t); remove the exemption", stateOnWire, storedErr, schemaErr == nil)
	}
}

// nonOmitemptyWireKeys is the presence walker's notion of "required" for one
// struct level: every exported, json-tagged field without `,omitempty`.
func nonOmitemptyWireKeys(rt reflect.Type) map[string]bool {
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	keys := map[string]bool{}
	for i := range rt.NumField() {
		field := rt.Field(i)
		if field.PkgPath != "" {
			continue
		}
		tag, ok := field.Tag.Lookup("json")
		if tag == "-" {
			continue
		}
		name, omitempty := field.Name, false
		if ok && tag != "" {
			parts := strings.Split(tag, ",")
			if parts[0] != "" {
				name = parts[0]
			}
			for _, option := range parts[1:] {
				if option == "omitempty" {
					omitempty = true
				}
			}
		}
		if !omitempty {
			keys[name] = true
		}
	}
	return keys
}

// reachableStructNames collects every named struct type an investigation
// result can carry, at any depth, so the parity above covers the whole tree.
func reachableStructNames(root reflect.Type) map[string]bool {
	seen := map[string]bool{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || t.PkgPath() == "time" {
			return
		}
		if seen[t.Name()] {
			return
		}
		seen[t.Name()] = true
		for i := range t.NumField() {
			walk(t.Field(i).Type)
		}
	}
	walk(root)
	return seen
}
