package contractcheck

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const publishedSchemaDir = "contracts"

// RequiredBaselineOptions configures the required-field baseline check.
type RequiredBaselineOptions struct {
	Root string
	Out  io.Writer
}

// CheckRequiredAgainstTag fails when a published schema requires a field at an
// instance location where the same schema at the previous prod roll tag
// (prod-rev<N>, else v*) neither required nor declared it (locationFacts,
// newlyRequiredAt). A field is optional for one release first; it may become
// required in the next: a field the baseline declared optional there may be
// required now, and a field new since the baseline may not (CHAOS-12218),
// however the requirement is reached -- a $ref, a combinator branch, items,
// additionalProperties, or a baseline of true at any depth (CHAOS-12222).
// With no tag the check passes and says so.
//
// Declared means declared where the requirement applies. A field the baseline
// declared outside every branch may become required anywhere at its
// location. A field it declared only under a then, else, dependentSchemas
// entry or one $ref alternative of an anyOf or oneOf was sent only when that
// branch applied, so it may become required only inside that same branch
// (CHAOS-12331). A then or else is the same branch when its location, schema
// position and if (compared through the if's $refs) are the same; a
// dependentSchemas entry when its location, position and key are; a $ref
// alternative when its group's location, keyword and alternative count, its
// index and its $ref target are. A declaration under an inline alternative
// counts for nothing: an inline alternative is known only by its position,
// so a reorder could fake it.
//
// An anyOf or oneOf whose alternatives are all $refs pairs with the baseline
// group at its location with the same keyword, alternative count, enclosing
// branch and targets in order. It is covered when each alternative requires
// every field its baseline alternative required, and besides only fields
// that alternative declared (pairCovered). A group with an inline
// alternative keeps the earlier rules. An uncovered group always refuses
// (groupReport). A paired one names the fields its alternatives gained that
// their baseline branches never declared, and each field an alternative no
// longer requires on a line of its own. Another names the fields no
// baseline alternative there required, else the fields neither required nor
// declared outright there, else every field it requires, else the group
// itself. The last two close an existing silent pass: before CHAOS-12331 an
// uncovered oneOf over fields the baseline declared outright, or one whose
// alternatives require no field, was accepted with nothing reported.
//
// Assumption: acr is the only producer of these bodies, and it emits only the
// fields its schema declares. So a property name new since the baseline was
// never sent, even where the baseline admits extra keys by default (no
// additionalProperties), and a new optional object may require its own
// members.
//
// Known limits, all conservative: the check weighs required fields, not
// types, reachability or discriminators, so it refuses some changes no old
// body breaks: a required under allOf whose sibling branch admits no object,
// an else the baseline can no longer reach, a oneOf that gains an
// alternative disjoint by a const, a changed oneOf whose alternatives require
// no field, a tightening inside an inline alternative, and a tightening in a
// branch whose then moved or whose if changed, or beside a reordered,
// retargeted, added or removed alternative. None of these occurs in the
// release history (prod-rev49 to prod-rev67). A false refusal is loud; widen
// the rule only with a real schema that hits it.
//
// Two limits accept, by design. The check does not weigh values: a const,
// enum or type edit is not seen, so a $ref alternative whose discriminator
// changed still pairs by its target. And a declaration in an enclosing
// branch meets a requirement in a branch inside it, as a declaration outside
// every branch meets one anywhere at its location.
func CheckRequiredAgainstTag(options RequiredBaselineOptions) error {
	root, err := findRoot(options.Root)
	if err != nil {
		return err
	}
	out := options.Out
	if out == nil {
		out = io.Discard
	}
	tag, err := previousReleaseTag(root)
	if err != nil {
		return err
	}
	if tag == "" {
		fmt.Fprintln(out, "contractcheck required-baseline: NO RELEASE TAG found (prod-rev* or v* reachable from HEAD, not at HEAD); nothing compared, passing. Fetch tags in CI.")
		return nil
	}
	files, err := gitOutput(root, "ls-tree", "-r", "--name-only", tag, "--", publishedSchemaDir)
	if err != nil {
		return err
	}
	// Every published schema at the tag and at HEAD, by file name: a $ref
	// names another file by its base name (splitSchemaRef), so the facts of
	// one schema can come from another (context_fabric_common.v1).
	baseDocs, curDocs := newSchemaDocs(), newSchemaDocs()
	var names []string
	for _, name := range strings.Split(strings.TrimSpace(files), "\n") {
		if !strings.HasSuffix(name, ".schema.json") {
			continue
		}
		raw, err := gitOutput(root, "show", tag+":"+name)
		if err != nil {
			return err
		}
		var baseline any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&baseline); err != nil {
			return fmt.Errorf("%s at %s: %w", name, tag, err)
		}
		baseDocs.add(filepath.Base(name), baseline)
		names = append(names, name)
	}
	current, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(publishedSchemaDir), "*", "*", "*.schema.json"))
	if err != nil {
		return err
	}
	for _, path := range current {
		doc, err := decodeJSONFile(path)
		if err != nil {
			return err
		}
		curDocs.add(filepath.Base(path), doc)
	}
	var problems []string
	compared := 0
	for _, name := range names {
		file := filepath.Base(name)
		if _, ok := curDocs.byFile[file]; !ok {
			// Deleted or renamed since the tag: removal is not a required-field change.
			continue
		}
		baseFacts, err := collectLocationFacts(baseDocs, file)
		if err != nil {
			return fmt.Errorf("%s at %s: %w", name, tag, err)
		}
		curFacts, err := collectLocationFacts(curDocs, file)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		compared++
		for _, added := range newlyRequiredAt(baseFacts, curFacts) {
			if added.alternative > 0 {
				problems = append(problems, fmt.Sprintf("%s: the %s at instance location %q changed: alternative %d no longer requires %q; a paired alternative may only gain fields its baseline branch declared (CHAOS-12331)", name, added.keyword, displayLocation(added.location), added.alternative, added.field))
				continue
			}
			if added.field == "" {
				problems = append(problems, fmt.Sprintf("%s: the %s at instance location %q changed since %s in a way the check cannot weigh (its alternatives require no field), so it may refuse a body the baseline accepted; keep its alternatives as they were for one release", name, added.keyword, displayLocation(added.location), tag))
				continue
			}
			problems = append(problems, fmt.Sprintf("%s: field %q at instance location %q is required now but was neither required nor declared there at %s; make it optional for one release first", name, added.field, displayLocation(added.location), tag))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("newly required fields:\n  %s", strings.Join(problems, "\n  "))
	}
	fmt.Fprintf(out, "contractcheck required-baseline: %d schemas compared against %s, no required field the baseline did not declare\n", compared, tag)
	return nil
}

// schemaDocs is a set of published schema documents by file base name, with
// each document's $id, so a $ref resolves against the referencing document's
// $id as JSON Schema does, falling back to the file name (splitSchemaRef).
type schemaDocs struct {
	byFile map[string]any
	byID   map[string]idTarget
}

// idTarget is where a schema resource with an $id sits: its file, its JSON
// pointer there, and the base URI its $id resolves against.
type idTarget struct{ file, pointer, parentBase string }

// schemaScope is where a subschema sits: its published file, and the base URI
// of its nearest enclosing $id ("" when none). A $ref resolves against that
// base (JSON Schema 2020-12 §8.2), so a relative or "#..." ref inside a
// nested resource names that resource (codex #1817 r2).
type schemaScope struct{ file, base string }

func newSchemaDocs() schemaDocs {
	return schemaDocs{byFile: map[string]any{}, byID: map[string]idTarget{}}
}

func (d schemaDocs) add(file string, doc any) {
	d.byFile[file] = doc
	d.indexIDs(file, doc, "", "")
}

// instanceKeywords hold instance data, not subschemas: an $id inside them
// names no schema resource (codex #1817 r3).
var instanceKeywords = map[string]bool{"const": true, "enum": true, "default": true, "examples": true}

// schemaMapKeywords map names to subschemas: a name there is not a keyword.
var schemaMapKeywords = map[string]bool{"properties": true, "patternProperties": true, "$defs": true, "definitions": true, "dependentSchemas": true}

func pointerToken(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}

// indexIDs records the $id of every schema resource in a document, at any
// depth, by its absolute URI; the first claimant of a URI keeps it.
func (d schemaDocs) indexIDs(file string, node any, pointer, base string) {
	switch n := node.(type) {
	case map[string]any:
		if id, ok := n["$id"].(string); ok {
			abs := resolveURI(base, id)
			if _, taken := d.byID[abs]; !taken {
				d.byID[abs] = idTarget{file: file, pointer: pointer, parentBase: base}
			}
			base = abs
		}
		for key, child := range n {
			at := pointer + "/" + pointerToken(key)
			switch {
			case instanceKeywords[key]:
			case schemaMapKeywords[key]:
				if named, ok := child.(map[string]any); ok {
					for name, sub := range named {
						d.indexIDs(file, sub, at+"/"+pointerToken(name), base)
					}
				}
			default:
				d.indexIDs(file, child, at, base)
			}
		}
	case []any:
		for index, child := range n {
			d.indexIDs(file, child, pointer+"/"+strconv.Itoa(index), base)
		}
	}
}

// enter is the scope of a schema's own subschemas: its $id, when it has one,
// sets their base URI.
func (s schemaScope) enter(schema map[string]any) schemaScope {
	if id, ok := schema["$id"].(string); ok {
		s.base = resolveURI(s.base, id)
	}
	return s
}

// resolveURI resolves ref against base, without a fragment.
func resolveURI(base, ref string) string {
	ref = strings.SplitN(ref, "#", 2)[0]
	if base == "" {
		return ref
	}
	if ref == "" {
		return base
	}
	b, errBase := url.Parse(base)
	r, errRef := url.Parse(ref)
	if errBase != nil || errRef != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

// resolve finds what a $ref in scope names: the node, the scope to walk it in,
// and a key naming it for recursion. The ref resolves against the scope's base
// URI to a published $id at any depth; failing that, it names a file by its
// base name, with a fragment from that file's root (splitSchemaRef).
func (d schemaDocs) resolve(scope schemaScope, ref string) (any, schemaScope, string, error) {
	fragment := ""
	if parts := strings.SplitN(ref, "#", 2); len(parts) == 2 {
		fragment = parts[1]
	}
	if scope.base != "" {
		if target, ok := d.byID[resolveURI(scope.base, ref)]; ok {
			if root, err := resolveJSONPointer(d.byFile[target.file], target.pointer); err == nil {
				node, inner, err := descend(root, schemaScope{file: target.file, base: target.parentBase}, fragment)
				if err != nil {
					return nil, scope, "", fmt.Errorf("unresolved $ref %q: %w", ref, err)
				}
				return node, inner, target.file + "#" + target.pointer + fragment, nil
			}
		}
	}
	file, fragment := splitSchemaRef(scope.file, ref)
	doc, ok := d.byFile[file]
	if !ok {
		return nil, scope, "", fmt.Errorf("unresolved $ref %q", ref)
	}
	node, inner, err := descend(doc, schemaScope{file: file}, fragment)
	if err != nil {
		return nil, scope, "", fmt.Errorf("unresolved $ref %q: %w", ref, err)
	}
	return node, inner, file + "#" + fragment, nil
}

// descend follows a JSON pointer from node, applying the $id of each schema
// it passes through, so the scope it returns is the one the target sits in.
func descend(node any, scope schemaScope, fragment string) (any, schemaScope, error) {
	if fragment == "" {
		return node, scope, nil
	}
	if !strings.HasPrefix(fragment, "/") {
		return nil, scope, fmt.Errorf("unsupported fragment %q", fragment)
	}
	current := node
	for _, token := range strings.Split(strings.TrimPrefix(fragment, "/"), "/") {
		if schema, ok := current.(map[string]any); ok {
			scope = scope.enter(schema)
		}
		next, err := resolveJSONPointer(current, "/"+token)
		if err != nil {
			return nil, scope, err
		}
		current = next
	}
	return current, scope, nil
}

// locationFacts is what one published schema says about each instance
// location, a JSON pointer over the instance: "" is the body, "/name" a
// property, "/[]" any array item, "/~{*}" any additional property and
// "/~{<pattern>}" a pattern property. Every subschema that applies to a
// location contributes: the node itself, its $ref target (local or in another
// published file), and its allOf, anyOf, oneOf, then, else and
// dependentSchemas branches. An if branch is a condition, not a constraint,
// and a not branch constrains nothing it names, so neither is read; $defs are
// read only through a $ref (CHAOS-12222).
//
// A requirement under a then branch applies only to bodies that carry every
// field its if requires; one under dependentRequired or dependentSchemas only
// to bodies that carry the key. Each occurrence keeps those trigger fields,
// so the comparison can tell a requirement no old body can trigger, and the
// branch path it was made under (branchSep), so it can tell which baseline
// declarations held where it applies.
type locationFacts struct {
	required map[string]map[string][]requiredOccurrence // occurrences of each required field
	declared map[string]map[string]bool                 // fields some applicable subschema declares in properties (false excluded)
	// declaredAlways is the part of declared that no condition or
	// alternative holds: not under then, else, dependentSchemas or one anyOf
	// or oneOf branch (branch path ""). Such a field may become required
	// anywhere at its location. A field declared under a branch is sent
	// only when the branch applies (codex #1817 r2), so it may become
	// required only under that same branch (declaredIn). declared, with
	// the conditional ones, still says which fields an old body may carry
	// (noOldBodyTriggers).
	declaredAlways map[string]map[string]bool
	// declaredIn is the branch paths each declaration was made under.
	declaredIn map[string]map[string]map[string]bool
	shaped     map[string]bool    // some applicable subschema has a properties map
	seen       map[string]bool    // some subschema other than false applies
	groups     []alternativeGroup // anyOf and oneOf branches' own requirements
	// Extra keys an applicable subschema admits beside its properties: an
	// additionalProperties or unevaluatedProperties other than false, and
	// patternProperties. Old bodies may carry such a key, so a property
	// named like one is not new (underNewProperty).
	extraKeys map[string]bool
	patterns  map[string][]string
}

// alternativeGroup is an anyOf or oneOf at a location: a body must meet one
// alternative, each being the fields its branch itself requires there. Its
// requirements are not single required fields, so the comparison weighs the
// group as a whole: adding an alternative widens, it never tightens.
type alternativeGroup struct {
	location     string
	alternatives [][]string
	// declared is each alternative's own declared fields there
	// (directDeclared), targets each alternative's $ref target ("" for an
	// inline alternative, alternativeTarget), and branch the branch path the
	// group sits under. They pair the group with the baseline's
	// (pairedGroup).
	declared [][]string
	targets  []string
	branch   string
	triggers []trigger
	// exclusive marks a oneOf: a body must meet exactly one alternative,
	// so an old body meeting two fails it. A oneOf is covered only when no
	// old body reaches it, a baseline oneOf there has the same
	// alternatives (codex #1817 r1), or it pairs with the baseline's and
	// each alternative only gained fields its baseline alternative
	// declared (pairCovered).
	exclusive bool
}

// maxLocationDepth bounds the walk. A recursive $ref stops on its own stack,
// so only a schema this deeply nested reaches the bound, and that is an error
// (a silent stop would be a pass on requirements never read).
const maxLocationDepth = 64

// trigger is a field that must be present at a location for a conditional
// requirement to apply.
type trigger struct{ location, field string }

// requiredOccurrence is one requirement of a field at a location: the
// triggers it waits on and the branch path it was made under.
type requiredOccurrence struct {
	triggers []trigger
	branch   string
}

// A branch path names the branches a walk is under, outermost first: a then,
// an else, a dependentSchemas entry, or one alternative of an anyOf or oneOf.
// "" is unconditional. Each element begins with branchSep and quotes its
// parts, so no part holds a separator: a run of a path's leading whole
// elements is a prefix of it ending at a branchSep or at its end
// (underBranch). The element of an inline alternative begins with
// inlineMark, which no quoted part starts with.
const (
	branchSep  = "\x1e"
	partSep    = "\x1f"
	inlineMark = branchSep + "!"
)

func branchParts(parts ...string) string {
	quoted := make([]string, len(parts))
	for i, part := range parts {
		quoted[i] = strconv.Quote(part)
	}
	return strings.Join(quoted, partSep)
}

// conditionElement names a then, an else or a dependentSchemas entry: its
// instance location, keyword, the schema position of the node holding it, and
// its condition: a digest of the if (conditionDigest), or the
// dependentSchemas key. Two thens at one location, a then whose if changed,
// and swapped allOf members holding if and then never share an element.
func conditionElement(location, keyword, pointer, condition string) string {
	return branchSep + branchParts(location, keyword, pointer, condition)
}

// conditionDigest is a canonical digest of an if: its JSON, keys sorted, with
// each $ref replaced by the target it resolves to and that target's own
// canonical form, so an if that changes through a $ref changes too.
func conditionDigest(docs schemaDocs, scope schemaScope, node any) (string, error) {
	raw, err := json.Marshal(expandRefs(docs, scope, node, map[string]bool{}, 0))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// expandRefs copies a schema with each $ref replaced by its resolved target
// key and, unless the target is already being expanded, the target's own
// expansion. Instance data (const, enum, default, examples) is copied as it
// is; a ref that does not resolve stays as written.
func expandRefs(docs schemaDocs, scope schemaScope, node any, refs map[string]bool, depth int) any {
	if depth > maxLocationDepth {
		return node
	}
	switch n := node.(type) {
	case map[string]any:
		scope = scope.enter(n)
		out := make(map[string]any, len(n))
		for key, child := range n {
			switch {
			case instanceKeywords[key]:
				out[key] = child
			case schemaMapKeywords[key]:
				named, ok := child.(map[string]any)
				if !ok {
					out[key] = child
					continue
				}
				expanded := make(map[string]any, len(named))
				for name, sub := range named {
					expanded[name] = expandRefs(docs, scope, sub, refs, depth+1)
				}
				out[key] = expanded
			case key == "$ref":
				ref, ok := child.(string)
				if !ok {
					out[key] = child
					continue
				}
				resolved, target, resolvedKey, err := docs.resolve(scope, ref)
				if err != nil {
					out[key] = child
					continue
				}
				if refs[resolvedKey] {
					out[key] = map[string]any{"target": resolvedKey}
					continue
				}
				inner := make(map[string]bool, len(refs)+1)
				for k := range refs {
					inner[k] = true
				}
				inner[resolvedKey] = true
				out[key] = map[string]any{"target": resolvedKey, "schema": expandRefs(docs, target, resolved, inner, depth+1)}
			default:
				out[key] = expandRefs(docs, scope, child, refs, depth+1)
			}
		}
		return out
	case []any:
		out := make([]any, len(n))
		for i, child := range n {
			out[i] = expandRefs(docs, scope, child, refs, depth+1)
		}
		return out
	}
	return node
}

// alternativeTarget is the target an alternative's $ref resolves to, or ""
// for an inline alternative. Only a $ref alternative has an identity a
// reorder cannot fake: an inline one is known only by its position.
func alternativeTarget(docs schemaDocs, scope schemaScope, node any) string {
	if schema, ok := node.(map[string]any); ok {
		if ref, ok := schema["$ref"].(string); ok {
			if _, _, key, err := docs.resolve(scope.enter(schema), ref); err == nil {
				return key
			}
		}
	}
	return ""
}

// alternativeElement names one alternative: its group's location, keyword and
// alternative count, its index, and its $ref target. An inline alternative's
// element begins with inlineMark: a declaration under it never meets a
// requirement (declaredUnder).
func alternativeElement(location, keyword string, count, index int, target string) string {
	if target == "" {
		return inlineMark + branchParts(location, keyword, strconv.Itoa(count), strconv.Itoa(index))
	}
	return branchSep + branchParts(location, keyword, strconv.Itoa(count), strconv.Itoa(index), target)
}

// underBranch reports whether a path lies under prefix: prefix is the path
// or a run of its leading whole elements. "" is a prefix of every path.
func underBranch(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+branchSep)
}

// declaredUnder reports whether a field was declared at a location under a
// branch path that holds wherever path holds: path itself or a prefix of it.
// A declaration under an inline alternative never counts: a reorder could
// move it to another alternative unseen.
func (f *locationFacts) declaredUnder(location, field, path string) bool {
	for prefix := range f.declaredIn[location][field] {
		if strings.Contains(prefix, inlineMark) {
			continue
		}
		if underBranch(path, prefix) {
			return true
		}
	}
	return false
}

func collectLocationFacts(docs schemaDocs, file string) (*locationFacts, error) {
	facts := &locationFacts{required: map[string]map[string][]requiredOccurrence{}, declared: map[string]map[string]bool{}, declaredAlways: map[string]map[string]bool{}, declaredIn: map[string]map[string]map[string]bool{}, shaped: map[string]bool{}, seen: map[string]bool{}, extraKeys: map[string]bool{}, patterns: map[string][]string{}}
	return facts, facts.walk(docs, schemaScope{file: file}, docs.byFile[file], file+"#", "", map[string]bool{}, 0, nil, notInAlternative, "")
}

// notInAlternative is the alternativeAt of a walk outside any anyOf or oneOf
// branch. It is no location: "" is the body's own.
const notInAlternative = "\x00"

func (f *locationFacts) addRequired(location, field string, triggers []trigger, branch string) {
	if f.required[location] == nil {
		f.required[location] = map[string][]requiredOccurrence{}
	}
	f.required[location][field] = append(f.required[location][field], requiredOccurrence{triggers: triggers, branch: branch})
}

func (f *locationFacts) addDeclared(location, field, branch string) {
	addFact(f.declared, location, field)
	if branch == "" {
		addFact(f.declaredAlways, location, field)
	}
	if f.declaredIn[location] == nil {
		f.declaredIn[location] = map[string]map[string]bool{}
	}
	if f.declaredIn[location][field] == nil {
		f.declaredIn[location][field] = map[string]bool{}
	}
	f.declaredIn[location][field][branch] = true
}

func withTriggers(triggers []trigger, location string, fields ...string) []trigger {
	out := append([]trigger{}, triggers...)
	for _, field := range fields {
		out = append(out, trigger{location: location, field: field})
	}
	return out
}

func addFact(set map[string]map[string]bool, location, field string) {
	if set[location] == nil {
		set[location] = map[string]bool{}
	}
	set[location][field] = true
}

func childLocation(location, name string) string {
	return location + "/" + strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}

// alternativeAt is the location of the anyOf or oneOf branch being walked:
// a requirement there belongs to the branch's alternative, not to the body.
// It covers the branch's own required, $ref and allOf (directRequired); a
// then, else or dependentSchemas inside the branch is a condition of its own
// and counts as a requirement with its triggers. pointer is the node's schema
// position (file#pointer). branch is the branch path of the walk: each then,
// else, dependentSchemas entry or anyOf or oneOf alternative appends its
// element, and every declaration and requirement records it.
func (f *locationFacts) walk(docs schemaDocs, scope schemaScope, node any, pointer, location string, refs map[string]bool, depth int, triggers []trigger, alternativeAt, branch string) error {
	if depth > maxLocationDepth {
		return fmt.Errorf("schema nests deeper than %d steps at %s; the check cannot weigh it", maxLocationDepth, displayLocation(location))
	}
	switch schema := node.(type) {
	case bool:
		// true accepts any value and declares nothing; false allows none.
		if schema {
			f.seen[location] = true
		}
		return nil
	case map[string]any:
		f.seen[location] = true
		scope = scope.enter(schema)
		if raw, ok := schema["$ref"]; ok {
			ref, ok := raw.(string)
			if !ok {
				return fmt.Errorf("$ref at %s must be a string", displayLocation(location))
			}
			resolved, target, key, err := docs.resolve(scope, ref)
			if err != nil {
				return err
			}
			if !refs[key] {
				inner := make(map[string]bool, len(refs)+1)
				for k := range refs {
					inner[k] = true
				}
				inner[key] = true
				if err := f.walk(docs, target, resolved, key, location, inner, depth+1, triggers, alternativeAt, branch); err != nil {
					return err
				}
			}
		}
		// required and dependentRequired constrain objects only: on a schema
		// whose type admits no object they have no effect.
		if location != alternativeAt && admitsObject(schema) {
			for _, field := range stringList(schema["required"]) {
				f.addRequired(location, field, triggers, branch)
			}
		}
		if dependent, ok := schema["dependentRequired"].(map[string]any); ok && admitsObject(schema) {
			for key, fields := range dependent {
				for _, field := range stringList(fields) {
					f.addRequired(location, field, withTriggers(triggers, location, key), branch)
				}
			}
		}
		if properties, ok := schema["properties"].(map[string]any); ok {
			f.shaped[location] = true
			for name, sub := range properties {
				if refusesEveryValue(docs, scope, sub, 0) {
					continue // forbidden there, never optional
				}
				f.addDeclared(location, name, branch)
				if err := f.walk(docs, scope, sub, pointer+"/properties/"+pointerToken(name), childLocation(location, name), refs, depth+1, triggers, alternativeAt, branch); err != nil {
					return err
				}
			}
		}
		if patterns, ok := schema["patternProperties"].(map[string]any); ok {
			for pattern, sub := range patterns {
				f.patterns[location] = append(f.patterns[location], pattern)
				if err := f.walk(docs, scope, sub, pointer+"/patternProperties/"+pointerToken(pattern), location+"/~{"+pattern+"}", refs, depth+1, triggers, alternativeAt, branch); err != nil {
					return err
				}
			}
		}
		for _, key := range []string{"additionalProperties", "unevaluatedProperties"} {
			if sub, ok := schema[key]; ok {
				if allowed, isBool := sub.(bool); !isBool || allowed {
					f.extraKeys[location] = true
				}
				if err := f.walk(docs, scope, sub, pointer+"/"+key, location+"/~{*}", refs, depth+1, triggers, alternativeAt, branch); err != nil {
					return err
				}
			}
		}
		// prefixItems (and the older array form of items) constrain one
		// position each: "/[0]", "/[1]"; items, contains and
		// unevaluatedItems any item: "/[]".
		for _, key := range []string{"items", "prefixItems", "contains", "unevaluatedItems"} {
			switch sub := schema[key].(type) {
			case nil:
			case []any:
				for index, item := range sub {
					if err := f.walk(docs, scope, item, pointer+"/"+key+"/"+strconv.Itoa(index), location+"/["+strconv.Itoa(index)+"]", refs, depth+1, triggers, alternativeAt, branch); err != nil {
						return err
					}
				}
			default:
				if err := f.walk(docs, scope, sub, pointer+"/"+key, location+"/[]", refs, depth+1, triggers, alternativeAt, branch); err != nil {
					return err
				}
			}
		}
		if branches, ok := schema["allOf"].([]any); ok {
			for index, member := range branches {
				if err := f.walk(docs, scope, member, pointer+"/allOf/"+strconv.Itoa(index), location, refs, depth+1, triggers, alternativeAt, branch); err != nil {
					return err
				}
			}
		}
		for _, key := range []string{"anyOf", "oneOf"} {
			branches, ok := schema[key].([]any)
			if !ok {
				continue
			}
			group := alternativeGroup{location: location, branch: branch, triggers: triggers, exclusive: key == "oneOf"}
			for index, alternative := range branches {
				target := alternativeTarget(docs, scope, alternative)
				group.alternatives = append(group.alternatives, directRequired(docs, scope, alternative, 0))
				group.declared = append(group.declared, directDeclared(docs, scope, alternative, 0))
				group.targets = append(group.targets, target)
				if err := f.walk(docs, scope, alternative, pointer+"/"+key+"/"+strconv.Itoa(index), location, refs, depth+1, triggers, location, branch+alternativeElement(location, key, len(branches), index, target)); err != nil {
					return err
				}
			}
			f.groups = append(f.groups, group)
		}
		// then applies only when if matches: a body must carry every field
		// the if requires. else applies whenever if fails, so it has none.
		_, hasThen := schema["then"]
		_, hasElse := schema["else"]
		ifDigest := ""
		if hasThen || hasElse {
			digest, err := conditionDigest(docs, scope, schema["if"])
			if err != nil {
				return fmt.Errorf("if at %s: %w", displayLocation(location), err)
			}
			ifDigest = digest
		}
		if sub, ok := schema["then"]; ok {
			var condition []string
			if when, ok := schema["if"].(map[string]any); ok {
				condition = stringList(when["required"])
			}
			if err := f.walk(docs, scope, sub, pointer+"/then", location, refs, depth+1, withTriggers(triggers, location, condition...), notInAlternative, branch+conditionElement(location, "then", pointer, ifDigest)); err != nil {
				return err
			}
		}
		if sub, ok := schema["else"]; ok {
			if err := f.walk(docs, scope, sub, pointer+"/else", location, refs, depth+1, triggers, notInAlternative, branch+conditionElement(location, "else", pointer, ifDigest)); err != nil {
				return err
			}
		}
		if dependent, ok := schema["dependentSchemas"].(map[string]any); ok {
			for key, sub := range dependent {
				if err := f.walk(docs, scope, sub, pointer+"/dependentSchemas/"+pointerToken(key), location, refs, depth+1, withTriggers(triggers, location, key), notInAlternative, branch+conditionElement(location, "dependentSchemas", pointer, key)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// directRequired is what a branch itself requires at its own location,
// through its $ref and allOf members.
func directRequired(docs schemaDocs, scope schemaScope, node any, depth int) []string {
	schema, ok := node.(map[string]any)
	if !ok || depth > maxLocationDepth {
		return nil
	}
	scope = scope.enter(schema)
	fields := stringList(schema["required"])
	if !admitsObject(schema) {
		fields = nil
	}
	if ref, ok := schema["$ref"].(string); ok {
		if resolved, target, _, err := docs.resolve(scope, ref); err == nil {
			fields = append(fields, directRequired(docs, target, resolved, depth+1)...)
		}
	}
	if members, ok := schema["allOf"].([]any); ok {
		for _, member := range members {
			fields = append(fields, directRequired(docs, scope, member, depth+1)...)
		}
	}
	return fields
}

// directDeclared is what a branch itself declares in properties at its own
// location, through its $ref and allOf members, as directRequired reads its
// requirements. A property whose schema refuses every value is not declared.
func directDeclared(docs schemaDocs, scope schemaScope, node any, depth int) []string {
	schema, ok := node.(map[string]any)
	if !ok || depth > maxLocationDepth {
		return nil
	}
	scope = scope.enter(schema)
	var fields []string
	if properties, ok := schema["properties"].(map[string]any); ok {
		for name, sub := range properties {
			if !refusesEveryValue(docs, scope, sub, 0) {
				fields = append(fields, name)
			}
		}
	}
	if ref, ok := schema["$ref"].(string); ok {
		if resolved, target, _, err := docs.resolve(scope, ref); err == nil {
			fields = append(fields, directDeclared(docs, target, resolved, depth+1)...)
		}
	}
	if members, ok := schema["allOf"].([]any); ok {
		for _, member := range members {
			fields = append(fields, directDeclared(docs, scope, member, depth+1)...)
		}
	}
	return fields
}

func stringList(raw any) []string {
	list, _ := raw.([]any)
	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// addedRequired is one refusal: a field newly required at a location; with
// alternative (1-based), a field that alternative of a paired anyOf or oneOf
// there (keyword) no longer requires; or, with no field, an anyOf or oneOf
// whose alternatives require no field to name.
type addedRequired struct {
	location, field, keyword string
	alternative              int
}

// newlyRequiredAt reports each field current requires at an instance location
// where the baseline neither required nor declared it, unless the location
// lies under a property name new since the baseline: old producers never send
// that property, so a new optional object may require its own members. The
// exemption needs a named property under a baseline object that declares its
// shape (has properties); a new item, additional property or pattern
// property, or a property under a baseline of true, is a new constraint on
// values old producers could already send.
//
// A requirement made under a branch (a then, else, dependentSchemas entry or
// anyOf or oneOf alternative) is met by a baseline declaration made under the
// same branch or one enclosing it (declaredUnder); one made outside every
// branch only by a declaration outside every branch (codex #1817 r2).
func newlyRequiredAt(baseline, current *locationFacts) []addedRequired {
	var found []addedRequired
	for location, fields := range current.required {
		for field, occurrences := range fields {
			if len(baseline.required[location][field]) > 0 || baseline.declaredAlways[location][field] {
				continue
			}
			var unmet []requiredOccurrence
			for _, occurrence := range occurrences {
				if !baseline.declaredUnder(location, field, occurrence.branch) {
					unmet = append(unmet, occurrence)
				}
			}
			if len(unmet) == 0 {
				continue
			}
			if underNewProperty(baseline, location) {
				continue
			}
			if noOldBodyTriggers(baseline, unmet) {
				continue
			}
			found = append(found, addedRequired{location: location, field: field})
		}
	}
	reported := map[addedRequired]bool{}
	for _, f := range found {
		reported[f] = true
	}
	for _, group := range current.groups {
		if groupCovered(baseline, group) {
			continue
		}
		for _, added := range groupReport(baseline, group) {
			if reported[added] {
				continue
			}
			reported[added] = true
			found = append(found, added)
		}
	}
	return found
}

// groupReport is what an uncovered group is refused for, never nothing. A
// group paired with the baseline's names the fields its alternatives gained
// that their baseline branches never declared, and, each on a line of its
// own, every field an alternative no longer requires: a lost requirement is
// no new field, so it is not reported as one. Another group where the
// baseline had groups names the fields no baseline alternative there
// required. When that names nothing, it names the fields neither required
// nor declared outright there; then every field its alternatives require;
// then the group itself.
func groupReport(baseline *locationFacts, group alternativeGroup) []addedRequired {
	keyword := "anyOf"
	if group.exclusive {
		keyword = "oneOf"
	}
	var fields []string
	if old := pairedGroup(baseline, group); old != nil {
		var out []addedRequired
		for i, alternative := range group.alternatives {
			allowed := append(append([]string{}, old.alternatives[i]...), old.declared[i]...)
			for _, field := range missingFrom(alternative, allowed) {
				out = append(out, addedRequired{location: group.location, field: field})
			}
		}
		for i, alternative := range group.alternatives {
			for _, field := range missingFrom(old.alternatives[i], alternative) {
				out = append(out, addedRequired{location: group.location, field: field, keyword: keyword, alternative: i + 1})
			}
		}
		if len(out) > 0 {
			return out
		}
	} else {
		var required []string
		paired := false
		for _, old := range baseline.groups {
			if old.location != group.location {
				continue
			}
			paired = true
			for _, alternative := range old.alternatives {
				required = append(required, alternative...)
			}
		}
		if paired {
			for _, alternative := range group.alternatives {
				fields = append(fields, missingFrom(alternative, required)...)
			}
		}
	}
	if len(fields) == 0 {
		for _, alternative := range group.alternatives {
			for _, field := range alternative {
				if len(baseline.required[group.location][field]) == 0 && !baseline.declaredAlways[group.location][field] {
					fields = append(fields, field)
				}
			}
		}
	}
	if len(fields) == 0 {
		for _, alternative := range group.alternatives {
			fields = append(fields, alternative...)
		}
	}
	if len(fields) == 0 {
		return []addedRequired{{location: group.location, keyword: keyword}}
	}
	out := make([]addedRequired, len(fields))
	for i, field := range fields {
		out[i] = addedRequired{location: group.location, field: field}
	}
	return out
}

// missingFrom is the fields not in of.
func missingFrom(fields, of []string) []string {
	set := map[string]bool{}
	for _, field := range of {
		set[field] = true
	}
	var out []string
	for _, field := range fields {
		if !set[field] {
			out = append(out, field)
		}
	}
	return out
}

// pairedGroup is the baseline group a current group continues: at the same
// location, with the same keyword, enclosing branch path and number of
// alternatives, every alternative on both sides a $ref, and the same targets
// in order. An inline alternative is known only by its position, so a group
// holding one never pairs: it keeps the other coverage rules.
func pairedGroup(baseline *locationFacts, group alternativeGroup) *alternativeGroup {
	for i := range baseline.groups {
		old := &baseline.groups[i]
		if old.location != group.location || old.exclusive != group.exclusive || old.branch != group.branch {
			continue
		}
		if len(old.alternatives) != len(group.alternatives) {
			continue
		}
		same := true
		for j, target := range group.targets {
			if target == "" || old.targets[j] != target {
				same = false
				break
			}
		}
		if same {
			return old
		}
	}
	return nil
}

// pairCovered reports whether every alternative of a paired group still
// requires each field its baseline alternative required, and requires besides
// only fields that alternative declared. A body the baseline alternative
// accepted carries those, as a body carries a field declared outright (acr
// emits what it declares). A lost requirement could let an old body meet two
// alternatives of a oneOf.
func pairCovered(old, group alternativeGroup) bool {
	for i, alternative := range group.alternatives {
		if len(missingFrom(old.alternatives[i], alternative)) > 0 {
			return false
		}
		allowed := append(append([]string{}, old.alternatives[i]...), old.declared[i]...)
		if len(missingFrom(alternative, allowed)) > 0 {
			return false
		}
	}
	return true
}

// groupCovered reports whether every body the baseline accepted still meets
// one alternative of an anyOf or oneOf, as far as required fields go. It
// holds when the group never applies to an old body (a new property, or a
// trigger no old body carries); when it pairs with the baseline group and
// each alternative only gained fields its baseline alternative declared
// (pairCovered); when one alternative needs nothing, or only fields the
// baseline required unconditionally there; or when a baseline group at the
// same location had alternatives each at least as strict as one of the
// current group's (adding an alternative widens).
func groupCovered(baseline *locationFacts, group alternativeGroup) bool {
	if underNewProperty(baseline, group.location) || (len(group.triggers) > 0 && noOldBodyTriggers(baseline, []requiredOccurrence{{triggers: group.triggers}})) {
		return true
	}
	if old := pairedGroup(baseline, group); old != nil && pairCovered(*old, group) {
		return true
	}
	if group.exclusive {
		for _, old := range baseline.groups {
			if old.exclusive && old.location == group.location && sameAlternatives(old.alternatives, group.alternatives) {
				return true
			}
		}
		return false
	}
	for _, alternative := range group.alternatives {
		met := true
		for _, field := range alternative {
			if !requiredUnconditionally(baseline, group.location, field) {
				met = false
				break
			}
		}
		if met {
			return true
		}
	}
	for _, old := range baseline.groups {
		if old.location != group.location {
			continue
		}
		if old.exclusive {
			continue
		}
		every := true
		for _, oldAlternative := range old.alternatives {
			if !someSubset(group.alternatives, oldAlternative) {
				every = false
				break
			}
		}
		if every {
			return true
		}
	}
	return false
}

func sameAlternatives(a, b [][]string) bool {
	key := func(alternatives [][]string) string {
		parts := make([]string, 0, len(alternatives))
		for _, alternative := range alternatives {
			fields := append([]string{}, alternative...)
			sort.Strings(fields)
			parts = append(parts, strings.Join(fields, "\x00"))
		}
		sort.Strings(parts)
		return strings.Join(parts, "\x01")
	}
	return key(a) == key(b)
}

// admitsObject reports whether a schema's type allows an object (no type
// allows anything).
func admitsObject(schema map[string]any) bool {
	switch kind := schema["type"].(type) {
	case nil:
		return true
	case string:
		return kind == "object"
	case []any:
		for _, k := range kind {
			if k == "object" {
				return true
			}
		}
		return false
	}
	return true
}

// refusesEveryValue reports whether a schema is false, directly or through
// its $ref.
func refusesEveryValue(docs schemaDocs, scope schemaScope, node any, depth int) bool {
	switch schema := node.(type) {
	case bool:
		return !schema
	case map[string]any:
		ref, ok := schema["$ref"].(string)
		if !ok || depth > maxLocationDepth {
			return false
		}
		resolved, target, _, err := docs.resolve(scope.enter(schema), ref)
		return err == nil && refusesEveryValue(docs, target, resolved, depth+1)
	}
	return false
}

func requiredUnconditionally(baseline *locationFacts, location, field string) bool {
	for _, occurrence := range baseline.required[location][field] {
		if len(occurrence.triggers) == 0 {
			return true
		}
	}
	return false
}

// someSubset reports whether one of alternatives requires only fields of of.
func someSubset(alternatives [][]string, of []string) bool {
	set := map[string]bool{}
	for _, field := range of {
		set[field] = true
	}
	for _, alternative := range alternatives {
		subset := true
		for _, field := range alternative {
			if !set[field] {
				subset = false
				break
			}
		}
		if subset {
			return true
		}
	}
	return false
}

// noOldBodyTriggers reports whether every occurrence of a requirement waits
// on a trigger field that the baseline neither required nor declared at its
// location: an old body never carries that field, so the requirement never
// applies to it. An unconditional occurrence always applies.
func noOldBodyTriggers(baseline *locationFacts, occurrences []requiredOccurrence) bool {
	for _, occurrence := range occurrences {
		blocked := false
		for _, t := range occurrence.triggers {
			if len(baseline.required[t.location][t.field]) == 0 && !baseline.declared[t.location][t.field] {
				blocked = true
				break
			}
		}
		if !blocked {
			return false
		}
	}
	return true
}

func underNewProperty(baseline *locationFacts, location string) bool {
	if location == "" {
		return false
	}
	parent := ""
	for _, segment := range strings.Split(strings.TrimPrefix(location, "/"), "/") {
		next := parent + "/" + segment
		if baseline.seen[next] {
			parent = next
			continue
		}
		if strings.HasPrefix(segment, "[") || strings.HasPrefix(segment, "~{") {
			return false
		}
		if baseline.extraKeys[parent] || matchesAPattern(baseline.patterns[parent], unescapeSegment(segment)) {
			return false // an old body may carry it as an extra key
		}
		return baseline.shaped[parent]
	}
	return false
}

func unescapeSegment(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
}

// matchesAPattern reports whether name matches one of the patternProperties
// patterns. A pattern Go cannot compile counts as a match: no exemption is
// better than a wrong one.
func matchesAPattern(patterns []string, name string) bool {
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil || re.MatchString(name) {
			return true
		}
	}
	return false
}

func displayLocation(location string) string {
	if location == "" {
		return "/"
	}
	return location
}

// previousReleaseTag returns the baseline tag reachable from HEAD that does
// not point at HEAD's commit: the prod-rev<N> tag with the highest N, else the
// newest v* tag, else "".
func previousReleaseTag(root string) (string, error) {
	head, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	head = strings.TrimSpace(head)
	prodTags, err := gitOutput(root, "tag", "--merged", "HEAD", "--list", "prod-rev*")
	if err != nil {
		return "", err
	}
	type revTag struct {
		name string
		n    int
	}
	var revs []revTag
	for _, tag := range strings.Fields(prodTags) {
		n, err := strconv.Atoi(strings.TrimPrefix(tag, "prod-rev"))
		if err != nil || n < 0 {
			continue
		}
		revs = append(revs, revTag{tag, n})
	}
	sort.Slice(revs, func(i, j int) bool { return revs[i].n > revs[j].n })
	var ordered []string
	for _, r := range revs {
		ordered = append(ordered, r.name)
	}
	versionTags, err := gitOutput(root, "tag", "--merged", "HEAD", "--list", "v*", "--sort=-creatordate")
	if err != nil {
		return "", err
	}
	ordered = append(ordered, strings.Fields(versionTags)...)
	for _, tag := range ordered {
		commit, err := gitOutput(root, "rev-parse", tag+"^{commit}")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(commit) != head {
			return tag, nil
		}
	}
	return "", nil
}

func gitOutput(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
