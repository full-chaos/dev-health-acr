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
// Declared means declared where the requirement applies. Every rule below is
// scoped by branch paths: the then, else, dependentSchemas entries and anyOf
// or oneOf alternatives a fact sits under, each named by its instance
// location, keyword, the schema position of its node and its branch (the if,
// compared through its $refs; the key; the alternative count, index and $ref
// target). A branch path is a conjunction of conditions on the instance, so a
// rule that held under a subset of the conditions holds wherever the current
// path holds (CHAOS-12340).
//
// So a baseline declaration meets a requirement whose path holds every
// condition of the declaration's path: one outside every branch meets any,
// and one made only under a then, else, dependentSchemas entry or $ref
// alternative meets only a requirement inside that branch, since the field
// was sent only when the branch applied (CHAOS-12331). A baseline requirement
// meets one the same way, and only when it waited on no trigger the current
// one does not wait on. A declaration under an inline alternative counts for
// nothing: an inline alternative is known only by its position, so a reorder
// could fake it. A property is forbidden, so not declared, wherever every
// condition of the forbid holds: where some subschema's schema for it refuses
// every value (false, a not of true or {}, an allOf with such a member, an
// anyOf or oneOf of only such members, or a $ref to one), or where a
// subschema whose additionalProperties or unevaluatedProperties refuses every
// value neither declares it nor matches it by a pattern (for
// unevaluatedProperties, counting what its in-place subschemas declare).
//
// An anyOf or oneOf whose alternatives are all $refs pairs with the baseline
// group at its location with the same keyword, schema position, alternative
// count, enclosing branch (exactly) and targets in order. It is covered when
// each alternative requires every field its baseline alternative required,
// and besides only fields that alternative declared (pairCovered). Any other
// coverage by a baseline group (unchanged alternatives, or an anyOf that only
// widens) counts only a baseline group whose path conditions the current
// group's path holds, and an alternative needing only fields the baseline
// required counts only requirements made wherever the group applies: a group
// under a then never applied to bodies outside it. An uncovered group always
// refuses (groupReport). A paired one names the fields its alternatives
// gained that their baseline branches never declared, and each field an
// alternative no longer requires on a line of its own. Another names the
// fields no baseline alternative there required (with no baseline group
// there, the fields neither required nor declared outright there). When no
// field is newly required, the refusal names the group: its alternative
// counts at the baseline and now, or the group alone when its alternatives
// require no field. Naming the group closes an existing silent pass: before
// CHAOS-12331 an uncovered oneOf over fields the baseline declared outright,
// or one whose alternatives require no field, was accepted with nothing
// reported.
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
// no field, a tightening inside an inline alternative, a requirement moved
// between two if blocks alike but at different schema positions, a paired
// group moved into a new branch, and a tightening in a branch whose then
// moved or whose if changed, or beside a reordered, retargeted, added or
// removed alternative. None of these occurs in the release history
// (prod-rev49 to prod-rev68). A false refusal is loud; widen the rule only
// with a real schema that hits it.
//
// Three limits accept, by design. The check does not weigh values: a const,
// enum or type edit is not seen, so a $ref alternative whose discriminator
// changed still pairs by its target. A declaration in an enclosing branch
// meets a requirement in a branch inside it, as a declaration outside every
// branch meets one anywhere at its location. And an inline alternative is
// known only by its position: a baseline requirement or group nested inside
// inline alternative i still counts for whatever alternative sits at index i
// after a reorder (CHAOS-12340 leaves this open).
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
			if added.changed {
				problems = append(problems, fmt.Sprintf("%s: the %s at instance location %q changed its alternatives (%d at %s, %d now); no field is newly required, but a body the baseline accepted may no longer meet it; keep its alternatives as they were for one release (CHAOS-12331)", name, added.keyword, displayLocation(added.location), added.was, tag, added.now))
				continue
			}
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
	// declaredIn is the branch paths each declaration was made under, and
	// forbidden the branch paths some subschema forbade the field under (its
	// schema refuses every value). closed holds, per location, each
	// subschema whose additionalProperties or unevaluatedProperties refuses
	// every value: it forbids every name it does not admit (CHAOS-12340). A
	// forbid holds under its own branch and every branch inside it, so a
	// declaration there counts for nothing (settleDeclarations).
	declaredIn map[string]map[string]map[string]bool
	forbidden  map[string]map[string]map[string]bool
	closed     map[string][]closedForbid
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
	// (directDeclared, less what a forbid holding wherever the group applies
	// forbids there), targets each
	// alternative's $ref target ("" for an inline alternative,
	// alternativeTarget), pointer the schema position of the node holding
	// the group, and branch the branch path the group sits under. They pair
	// the group with the baseline's (pairedGroup).
	declared [][]string
	targets  []string
	pointer  string
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
// "" is unconditional. Each element names its condition by absolute place:
// the instance location, the keyword, the schema position (file#pointer) of
// the node holding the if, dependentSchemas, anyOf or oneOf, and the branch.
// Each begins with branchSep and quotes its parts, so no part holds a
// separator and a path splits into whole elements (branchWithin). A branch
// path is a conjunction of conditions on the instance, so a rule that held
// under a subset of the conditions holds wherever the current path holds
// (CHAOS-12340). The element of an inline alternative begins with
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

// alternativeElement names one alternative: its group's location, keyword,
// schema position and alternative count, its index, and its $ref target, so
// two groups at one location never share an element. An inline
// alternative's element begins with inlineMark: a declaration under it
// never meets a requirement (declaredUnder).
func alternativeElement(location, keyword, pointer string, count, index int, target string) string {
	if target == "" {
		return inlineMark + branchParts(location, keyword, pointer, strconv.Itoa(count), strconv.Itoa(index))
	}
	return branchSep + branchParts(location, keyword, pointer, strconv.Itoa(count), strconv.Itoa(index), target)
}

// branchWithin reports whether every element of outer is also an element of
// path: wherever path holds, outer holds. "" is within every path.
func branchWithin(path, outer string) bool {
	if outer == "" {
		return true
	}
	have := map[string]bool{}
	for _, element := range strings.Split(path, branchSep)[1:] {
		have[element] = true
	}
	for _, element := range strings.Split(outer, branchSep)[1:] {
		if !have[element] {
			return false
		}
	}
	return true
}

// declaredUnder reports whether a field was declared at a location under a
// branch path that holds wherever path holds: one whose every condition is a
// condition of path.
// A declaration under an inline alternative never counts: a reorder could
// move it to another alternative unseen.
func (f *locationFacts) declaredUnder(location, field, path string) bool {
	for declared := range f.declaredIn[location][field] {
		if strings.Contains(declared, inlineMark) {
			continue
		}
		if branchWithin(path, declared) {
			return true
		}
	}
	return false
}

func collectLocationFacts(docs schemaDocs, file string) (*locationFacts, error) {
	facts := &locationFacts{required: map[string]map[string][]requiredOccurrence{}, declared: map[string]map[string]bool{}, declaredAlways: map[string]map[string]bool{}, declaredIn: map[string]map[string]map[string]bool{}, forbidden: map[string]map[string]map[string]bool{}, closed: map[string][]closedForbid{}, shaped: map[string]bool{}, seen: map[string]bool{}, extraKeys: map[string]bool{}, patterns: map[string][]string{}}
	if err := facts.walk(docs, schemaScope{file: file}, docs.byFile[file], file+"#", "", map[string]bool{}, 0, nil, notInAlternative, ""); err != nil {
		return facts, err
	}
	facts.settleDeclarations()
	return facts, nil
}

// settleDeclarations drops each declaration a forbid holds over (one at the
// same location whose path conditions the declaration's path holds), then
// fills declared and declaredAlways from what is left, and drops from each
// group's declared fields what a forbid holding wherever the group applies
// forbids there.
// It runs after the walk, because a forbid may sit in a part walked after
// the declaration it cancels.
func (f *locationFacts) settleDeclarations() {
	for location, fields := range f.declaredIn {
		for field, paths := range fields {
			for path := range paths {
				if f.forbiddenOver(location, field, path) {
					delete(paths, path)
				}
			}
			if len(paths) == 0 {
				delete(fields, field)
				continue
			}
			addFact(f.declared, location, field)
			if paths[""] {
				addFact(f.declaredAlways, location, field)
			}
		}
	}
	for i := range f.groups {
		group := &f.groups[i]
		for j, fields := range group.declared {
			var kept []string
			for _, field := range fields {
				if !f.forbiddenOver(group.location, field, group.branch) {
					kept = append(kept, field)
				}
			}
			group.declared[j] = kept
		}
	}
}

// forbiddenOver reports whether some subschema forbids a field at a location
// under a branch path whose every condition is a condition of path: by name,
// or as a closed subschema that does not admit it.
func (f *locationFacts) forbiddenOver(location, field, path string) bool {
	for forbid := range f.forbidden[location][field] {
		if branchWithin(path, forbid) {
			return true
		}
	}
	for _, closed := range f.closed[location] {
		if branchWithin(path, closed.branch) && !closed.set.admits(field) {
			return true
		}
	}
	return false
}

// requiredOver reports whether the baseline required a field at a location
// wherever a current occurrence of it applies: under a branch path whose
// every condition is a condition of the occurrence's path, and waiting on no
// trigger the occurrence does not wait on too (CHAOS-12340).
func (f *locationFacts) requiredOver(location, field string, occurrence requiredOccurrence) bool {
	for _, old := range f.required[location][field] {
		if branchWithin(occurrence.branch, old.branch) && triggersWithin(old.triggers, occurrence.triggers) {
			return true
		}
	}
	return false
}

// triggersWithin reports whether every trigger is also one of of.
func triggersWithin(triggers, of []trigger) bool {
	set := make(map[trigger]bool, len(of))
	for _, t := range of {
		set[t] = true
	}
	for _, t := range triggers {
		if !set[t] {
			return false
		}
	}
	return true
}

// closedForbid is a closed subschema met at a location under a branch path.
type closedForbid struct {
	branch string
	set    closedSet
}

// closedSet is what a subschema whose additionalProperties or
// unevaluatedProperties refuses every value admits at its location: the
// names it declares and the names its patterns match, and for
// unevaluatedProperties also what its in-place subschemas declare and match.
// It forbids every other name.
type closedSet struct {
	names    map[string]bool
	patterns []string
}

func (c closedSet) admits(name string) bool {
	return c.names[name] || matchesAPattern(c.patterns, name)
}

// closedOf is the closed set a subschema imposes, if it is closed; scope is
// the subschema's own. An unevaluatedProperties false closes nothing when an
// in-place additionalProperties, or an inner unevaluatedProperties, admits
// values: that subschema evaluates every name.
func closedOf(docs schemaDocs, scope schemaScope, schema map[string]any) (closedSet, bool) {
	set := closedSet{names: map[string]bool{}}
	if sub, ok := schema["additionalProperties"]; ok && refusesEveryValue(docs, scope, sub, 0) {
		set.addOwn(schema)
		return set, true
	}
	if sub, ok := schema["unevaluatedProperties"]; ok && refusesEveryValue(docs, scope, sub, 0) {
		if set.addInPlace(docs, scope, schema, map[string]bool{}, 0, true) {
			return closedSet{}, false
		}
		return set, true
	}
	return closedSet{}, false
}

// addOwn adds the names a subschema's properties declare and the patterns of
// its patternProperties.
func (c *closedSet) addOwn(schema map[string]any) {
	if properties, ok := schema["properties"].(map[string]any); ok {
		for name := range properties {
			c.names[name] = true
		}
	}
	if patterns, ok := schema["patternProperties"].(map[string]any); ok {
		for pattern := range patterns {
			c.patterns = append(c.patterns, pattern)
		}
	}
}

// addInPlace adds what a subschema and its in-place subschemas ($ref, allOf,
// anyOf, oneOf, if, then, else, dependentSchemas) declare and match. It
// reports true when one of them evaluates every name: an
// additionalProperties, or an inner unevaluatedProperties, that admits
// values. top marks the closed subschema itself, whose scope is entered.
func (c *closedSet) addInPlace(docs schemaDocs, scope schemaScope, node any, refs map[string]bool, depth int, top bool) bool {
	schema, ok := node.(map[string]any)
	if !ok || depth > maxLocationDepth {
		return false
	}
	if !top {
		scope = scope.enter(schema)
	}
	c.addOwn(schema)
	if sub, ok := schema["additionalProperties"]; ok && !refusesEveryValue(docs, scope, sub, 0) {
		return true
	}
	if sub, ok := schema["unevaluatedProperties"]; ok && !top && !refusesEveryValue(docs, scope, sub, 0) {
		return true
	}
	if ref, ok := schema["$ref"].(string); ok {
		if resolved, target, key, err := docs.resolve(scope, ref); err == nil && !refs[key] {
			inner := make(map[string]bool, len(refs)+1)
			for k := range refs {
				inner[k] = true
			}
			inner[key] = true
			if c.addInPlace(docs, target, resolved, inner, depth+1, false) {
				return true
			}
		}
	}
	var subs []any
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		if members, ok := schema[key].([]any); ok {
			subs = append(subs, members...)
		}
	}
	for _, key := range []string{"if", "then", "else"} {
		if sub, ok := schema[key]; ok {
			subs = append(subs, sub)
		}
	}
	if dependent, ok := schema["dependentSchemas"].(map[string]any); ok {
		for _, sub := range dependent {
			subs = append(subs, sub)
		}
	}
	for _, sub := range subs {
		if c.addInPlace(docs, scope, sub, refs, depth+1, false) {
			return true
		}
	}
	return false
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

// addDeclared and addForbidden record a declaration and a forbid with their
// branch paths; settleDeclarations derives declared and declaredAlways.
func (f *locationFacts) addDeclared(location, field, branch string) {
	addPath(f.declaredIn, location, field, branch)
}

func (f *locationFacts) addForbidden(location, field, branch string) {
	addPath(f.forbidden, location, field, branch)
}

func addPath(set map[string]map[string]map[string]bool, location, field, branch string) {
	if set[location] == nil {
		set[location] = map[string]map[string]bool{}
	}
	if set[location][field] == nil {
		set[location][field] = map[string]bool{}
	}
	set[location][field][branch] = true
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
		if set, ok := closedOf(docs, scope, schema); ok {
			f.closed[location] = append(f.closed[location], closedForbid{branch: branch, set: set})
		}
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
					f.addForbidden(location, name, branch) // forbidden there, never optional
					continue
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
			group := alternativeGroup{location: location, pointer: pointer, branch: branch, triggers: triggers, exclusive: key == "oneOf"}
			for index, alternative := range branches {
				target := alternativeTarget(docs, scope, alternative)
				group.alternatives = append(group.alternatives, directRequired(docs, scope, alternative, 0))
				group.declared = append(group.declared, directDeclared(docs, scope, alternative, 0))
				group.targets = append(group.targets, target)
				if err := f.walk(docs, scope, alternative, pointer+"/"+key+"/"+strconv.Itoa(index), location, refs, depth+1, triggers, location, branch+alternativeElement(location, key, pointer, len(branches), index, target)); err != nil {
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
// requirements. A property that any of those parts forbids (its schema
// refuses every value, or a closed part does not admit it) is not declared,
// even where another part declares it.
func directDeclared(docs schemaDocs, scope schemaScope, node any, depth int) []string {
	own := directProperties(docs, scope, node, depth)
	var out []string
	for _, name := range missingFrom(own.declared, own.forbidden) {
		admitted := true
		for _, closed := range own.closed {
			if !closed.admits(name) {
				admitted = false
				break
			}
		}
		if admitted {
			out = append(out, name)
		}
	}
	return out
}

// branchProperties is what a branch's own parts declare, forbid by name, and
// close at its location.
type branchProperties struct {
	declared, forbidden []string
	closed              []closedSet
}

// directProperties is what a branch's own parts declare and forbid at its
// location, through its $ref and allOf members, with the closed sets those
// parts impose.
func directProperties(docs schemaDocs, scope schemaScope, node any, depth int) branchProperties {
	var own branchProperties
	schema, ok := node.(map[string]any)
	if !ok || depth > maxLocationDepth {
		return own
	}
	scope = scope.enter(schema)
	if properties, ok := schema["properties"].(map[string]any); ok {
		for name, sub := range properties {
			if refusesEveryValue(docs, scope, sub, 0) {
				own.forbidden = append(own.forbidden, name)
			} else {
				own.declared = append(own.declared, name)
			}
		}
	}
	if set, ok := closedOf(docs, scope, schema); ok {
		own.closed = append(own.closed, set)
	}
	var parts []branchProperties
	if ref, ok := schema["$ref"].(string); ok {
		if resolved, target, _, err := docs.resolve(scope, ref); err == nil {
			parts = append(parts, directProperties(docs, target, resolved, depth+1))
		}
	}
	if members, ok := schema["allOf"].([]any); ok {
		for _, member := range members {
			parts = append(parts, directProperties(docs, scope, member, depth+1))
		}
	}
	for _, part := range parts {
		own.declared = append(own.declared, part.declared...)
		own.forbidden = append(own.forbidden, part.forbidden...)
		own.closed = append(own.closed, part.closed...)
	}
	return own
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
// there (keyword) no longer requires; with changed, an anyOf or oneOf whose
// alternatives changed (was at the baseline, now) with no field newly
// required to name; or, with no field, an anyOf or oneOf whose alternatives
// require no field to name.
type addedRequired struct {
	location, field, keyword string
	alternative              int
	changed                  bool
	was, now                 int
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
// anyOf or oneOf alternative) is met by a baseline declaration made under a
// path every condition of which the requirement's path holds (declaredUnder);
// one made outside every branch only by a declaration outside every branch
// (codex #1817 r2). A
// baseline requirement meets it the same way, and only when it waited on no
// trigger the current one does not wait on (requiredOver, CHAOS-12340).
func newlyRequiredAt(baseline, current *locationFacts) []addedRequired {
	var found []addedRequired
	for location, fields := range current.required {
		for field, occurrences := range fields {
			if baseline.declaredAlways[location][field] {
				continue
			}
			var unmet []requiredOccurrence
			for _, occurrence := range occurrences {
				if baseline.requiredOver(location, field, occurrence) {
					continue
				}
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
// no new field, so it is not reported as one. Another group names the fields
// no baseline alternative at its location required, or, where the baseline
// had no group there, the fields neither required nor declared outright
// there. When that names no field, the refusal names the group: its
// alternative counts, or, when its alternatives require no field at all, the
// group alone.
func groupReport(baseline *locationFacts, group alternativeGroup) []addedRequired {
	keyword := "anyOf"
	if group.exclusive {
		keyword = "oneOf"
	}
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
	}
	var fields []string
	atLocation := false
	var required []string
	for _, old := range baseline.groups {
		if old.location != group.location || !branchWithin(group.branch, old.branch) {
			continue
		}
		atLocation = true
		for _, alternative := range old.alternatives {
			required = append(required, alternative...)
		}
	}
	if atLocation {
		for _, alternative := range group.alternatives {
			fields = append(fields, missingFrom(alternative, required)...)
		}
	} else {
		for _, alternative := range group.alternatives {
			for _, field := range alternative {
				if len(baseline.required[group.location][field]) == 0 && !baseline.declaredAlways[group.location][field] {
					fields = append(fields, field)
				}
			}
		}
	}
	if len(fields) > 0 {
		out := make([]addedRequired, len(fields))
		for i, field := range fields {
			out[i] = addedRequired{location: group.location, field: field}
		}
		return out
	}
	anyField := false
	for _, alternative := range group.alternatives {
		if len(alternative) > 0 {
			anyField = true
		}
	}
	if !anyField {
		return []addedRequired{{location: group.location, keyword: keyword}}
	}
	return []addedRequired{{location: group.location, keyword: keyword, changed: true, was: baselineCount(baseline, group), now: len(group.alternatives)}}
}

// baselineCount is the number of alternatives of the baseline group a
// changed group replaces: the one at its location with its keyword and
// schema position, else the first there with its keyword, else none (0).
func baselineCount(baseline *locationFacts, group alternativeGroup) int {
	count := 0
	found := false
	for _, old := range baseline.groups {
		if old.location != group.location || old.exclusive != group.exclusive || !branchWithin(group.branch, old.branch) {
			continue
		}
		if old.pointer == group.pointer {
			return len(old.alternatives)
		}
		if !found {
			count, found = len(old.alternatives), true
		}
	}
	return count
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
// location, with the same keyword, schema position, enclosing branch path and
// number of alternatives, every alternative on both sides a $ref, and the
// same targets in order. An inline alternative is known only by its
// position, so a group holding one never pairs: it keeps the other coverage
// rules.
func pairedGroup(baseline *locationFacts, group alternativeGroup) *alternativeGroup {
	for i := range baseline.groups {
		old := &baseline.groups[i]
		if old.location != group.location || old.exclusive != group.exclusive || old.pointer != group.pointer || old.branch != group.branch {
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
// baseline required wherever the group applies (requiredForGroup); or when a
// baseline group at the
// same location had alternatives each at least as strict as one of the
// current group's (adding an alternative widens). A baseline group counts
// only when the current group's path holds every condition of its path: one
// under a then never applied to bodies outside it (CHAOS-12340).
func groupCovered(baseline *locationFacts, group alternativeGroup) bool {
	if underNewProperty(baseline, group.location) || (len(group.triggers) > 0 && noOldBodyTriggers(baseline, []requiredOccurrence{{triggers: group.triggers}})) {
		return true
	}
	if old := pairedGroup(baseline, group); old != nil && pairCovered(*old, group) {
		return true
	}
	if group.exclusive {
		for _, old := range baseline.groups {
			if old.exclusive && old.location == group.location && branchWithin(group.branch, old.branch) && sameAlternatives(old.alternatives, group.alternatives) {
				return true
			}
		}
		return false
	}
	for _, alternative := range group.alternatives {
		met := true
		for _, field := range alternative {
			if !requiredForGroup(baseline, group, field) {
				met = false
				break
			}
		}
		if met {
			return true
		}
	}
	for _, old := range baseline.groups {
		if old.location != group.location || !branchWithin(group.branch, old.branch) {
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

// refusesEveryValue reports whether no value can meet a schema: false; a not
// of a schema that accepts every value (true or {}); an allOf one of whose
// members refuses every value; an anyOf or oneOf all of whose members do;
// or a $ref to such a schema (CHAOS-12340).
func refusesEveryValue(docs schemaDocs, scope schemaScope, node any, depth int) bool {
	if depth > maxLocationDepth {
		return false
	}
	switch schema := node.(type) {
	case bool:
		return !schema
	case map[string]any:
		scope = scope.enter(schema)
		if acceptsEveryValue(schema["not"]) {
			return true
		}
		if members, ok := schema["allOf"].([]any); ok {
			for _, member := range members {
				if refusesEveryValue(docs, scope, member, depth+1) {
					return true
				}
			}
		}
		for _, key := range []string{"anyOf", "oneOf"} {
			members, ok := schema[key].([]any)
			if !ok || len(members) == 0 {
				continue
			}
			every := true
			for _, member := range members {
				if !refusesEveryValue(docs, scope, member, depth+1) {
					every = false
					break
				}
			}
			if every {
				return true
			}
		}
		if ref, ok := schema["$ref"].(string); ok {
			resolved, target, _, err := docs.resolve(scope, ref)
			if err == nil && refusesEveryValue(docs, target, resolved, depth+1) {
				return true
			}
		}
	}
	return false
}

// acceptsEveryValue reports whether a schema is true or {}.
func acceptsEveryValue(node any) bool {
	switch schema := node.(type) {
	case bool:
		return schema
	case map[string]any:
		return len(schema) == 0
	}
	return false
}

// requiredForGroup reports whether the baseline required a field at a
// group's location of every body the group applies to: under branches all
// of which the group's path holds, waiting on no trigger the group does not
// wait on. For a group outside every branch that means unconditionally: no
// trigger, no branch (CHAOS-12340).
func requiredForGroup(baseline *locationFacts, group alternativeGroup, field string) bool {
	for _, occurrence := range baseline.required[group.location][field] {
		if branchWithin(group.branch, occurrence.branch) && triggersWithin(occurrence.triggers, group.triggers) {
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
