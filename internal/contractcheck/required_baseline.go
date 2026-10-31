package contractcheck

import (
	"bytes"
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
	byID   map[string]string
}

func newSchemaDocs() schemaDocs {
	return schemaDocs{byFile: map[string]any{}, byID: map[string]string{}}
}

func (d schemaDocs) add(file string, doc any) {
	d.byFile[file] = doc
	if schema, ok := doc.(map[string]any); ok {
		if id, ok := schema["$id"].(string); ok {
			d.byID[strings.SplitN(id, "#", 2)[0]] = file
		}
	}
}

// resolve returns the file and JSON pointer a $ref in file names.
func (d schemaDocs) resolve(file, ref string) (string, string) {
	target, fragment := splitSchemaRef(file, ref)
	if strings.HasPrefix(ref, "#") {
		return target, fragment
	}
	if schema, ok := d.byFile[file].(map[string]any); ok {
		if id, ok := schema["$id"].(string); ok {
			base, errBase := url.Parse(id)
			relative, errRef := url.Parse(strings.SplitN(ref, "#", 2)[0])
			if errBase == nil && errRef == nil {
				if byID, ok := d.byID[base.ResolveReference(relative).String()]; ok {
					return byID, fragment
				}
			}
		}
	}
	return target, fragment
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
// so the comparison can tell a requirement no old body can trigger.
type locationFacts struct {
	required map[string]map[string][][]trigger // occurrences of each required field, each with its triggers
	declared map[string]map[string]bool        // fields some applicable subschema declares in properties (false excluded)
	shaped   map[string]bool                   // some applicable subschema has a properties map
	seen     map[string]bool                   // some subschema other than false applies
	groups   []alternativeGroup                // anyOf and oneOf branches' own requirements
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
	triggers     []trigger
	// exclusive marks a oneOf: a body must meet exactly one alternative,
	// so an old body meeting two fails it. A oneOf is covered only when no
	// old body reaches it or a baseline oneOf there has the same
	// alternatives (codex #1817 r1).
	exclusive bool
}

// maxLocationDepth bounds the walk. A recursive $ref stops on its own stack,
// so only a schema this deeply nested reaches the bound, and that is an error
// (a silent stop would be a pass on requirements never read).
const maxLocationDepth = 64

// trigger is a field that must be present at a location for a conditional
// requirement to apply.
type trigger struct{ location, field string }

func collectLocationFacts(docs schemaDocs, file string) (*locationFacts, error) {
	facts := &locationFacts{required: map[string]map[string][][]trigger{}, declared: map[string]map[string]bool{}, shaped: map[string]bool{}, seen: map[string]bool{}, extraKeys: map[string]bool{}, patterns: map[string][]string{}}
	return facts, facts.walk(docs, file, docs.byFile[file], "", map[string]bool{}, 0, nil, notInAlternative)
}

// notInAlternative is the alternativeAt of a walk outside any anyOf or oneOf
// branch. It is no location: "" is the body's own.
const notInAlternative = "\x00"

func (f *locationFacts) addRequired(location, field string, triggers []trigger) {
	if f.required[location] == nil {
		f.required[location] = map[string][][]trigger{}
	}
	f.required[location][field] = append(f.required[location][field], triggers)
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
// and counts as a requirement with its triggers.
func (f *locationFacts) walk(docs schemaDocs, file string, node any, location string, refs map[string]bool, depth int, triggers []trigger, alternativeAt string) error {
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
		if raw, ok := schema["$ref"]; ok {
			ref, ok := raw.(string)
			if !ok {
				return fmt.Errorf("$ref at %s must be a string", displayLocation(location))
			}
			target, fragment := docs.resolve(file, ref)
			key := target + "#" + fragment
			if !refs[key] {
				doc, ok := docs.byFile[target]
				if !ok {
					return fmt.Errorf("unresolved $ref %q", ref)
				}
				resolved, err := resolveJSONPointer(doc, fragment)
				if err != nil {
					return fmt.Errorf("unresolved $ref %q: %w", ref, err)
				}
				inner := make(map[string]bool, len(refs)+1)
				for k := range refs {
					inner[k] = true
				}
				inner[key] = true
				if err := f.walk(docs, target, resolved, location, inner, depth+1, triggers, alternativeAt); err != nil {
					return err
				}
			}
		}
		// required and dependentRequired constrain objects only: on a schema
		// whose type admits no object they have no effect.
		if location != alternativeAt && admitsObject(schema) {
			for _, field := range stringList(schema["required"]) {
				f.addRequired(location, field, triggers)
			}
		}
		if dependent, ok := schema["dependentRequired"].(map[string]any); ok && admitsObject(schema) {
			for key, fields := range dependent {
				for _, field := range stringList(fields) {
					f.addRequired(location, field, withTriggers(triggers, location, key))
				}
			}
		}
		if properties, ok := schema["properties"].(map[string]any); ok {
			f.shaped[location] = true
			for name, sub := range properties {
				if refusesEveryValue(docs, file, sub, 0) {
					continue // forbidden there, never optional
				}
				addFact(f.declared, location, name)
				if err := f.walk(docs, file, sub, childLocation(location, name), refs, depth+1, triggers, alternativeAt); err != nil {
					return err
				}
			}
		}
		if patterns, ok := schema["patternProperties"].(map[string]any); ok {
			for pattern, sub := range patterns {
				f.patterns[location] = append(f.patterns[location], pattern)
				if err := f.walk(docs, file, sub, location+"/~{"+pattern+"}", refs, depth+1, triggers, alternativeAt); err != nil {
					return err
				}
			}
		}
		for _, key := range []string{"additionalProperties", "unevaluatedProperties"} {
			if sub, ok := schema[key]; ok {
				if allowed, isBool := sub.(bool); !isBool || allowed {
					f.extraKeys[location] = true
				}
				if err := f.walk(docs, file, sub, location+"/~{*}", refs, depth+1, triggers, alternativeAt); err != nil {
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
					if err := f.walk(docs, file, item, location+"/["+strconv.Itoa(index)+"]", refs, depth+1, triggers, alternativeAt); err != nil {
						return err
					}
				}
			default:
				if err := f.walk(docs, file, sub, location+"/[]", refs, depth+1, triggers, alternativeAt); err != nil {
					return err
				}
			}
		}
		if branches, ok := schema["allOf"].([]any); ok {
			for _, branch := range branches {
				if err := f.walk(docs, file, branch, location, refs, depth+1, triggers, alternativeAt); err != nil {
					return err
				}
			}
		}
		for _, key := range []string{"anyOf", "oneOf"} {
			branches, ok := schema[key].([]any)
			if !ok {
				continue
			}
			group := alternativeGroup{location: location, triggers: triggers, exclusive: key == "oneOf"}
			for _, branch := range branches {
				group.alternatives = append(group.alternatives, directRequired(docs, file, branch, 0))
				if err := f.walk(docs, file, branch, location, refs, depth+1, triggers, location); err != nil {
					return err
				}
			}
			f.groups = append(f.groups, group)
		}
		// then applies only when if matches: a body must carry every field
		// the if requires. else applies whenever if fails, so it has none.
		if branch, ok := schema["then"]; ok {
			var condition []string
			if when, ok := schema["if"].(map[string]any); ok {
				condition = stringList(when["required"])
			}
			if err := f.walk(docs, file, branch, location, refs, depth+1, withTriggers(triggers, location, condition...), notInAlternative); err != nil {
				return err
			}
		}
		if branch, ok := schema["else"]; ok {
			if err := f.walk(docs, file, branch, location, refs, depth+1, triggers, notInAlternative); err != nil {
				return err
			}
		}
		if dependent, ok := schema["dependentSchemas"].(map[string]any); ok {
			for key, branch := range dependent {
				if err := f.walk(docs, file, branch, location, refs, depth+1, withTriggers(triggers, location, key), notInAlternative); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// directRequired is what a branch itself requires at its own location,
// through its $ref and allOf members.
func directRequired(docs schemaDocs, file string, node any, depth int) []string {
	schema, ok := node.(map[string]any)
	if !ok || depth > maxLocationDepth {
		return nil
	}
	fields := stringList(schema["required"])
	if !admitsObject(schema) {
		fields = nil
	}
	if ref, ok := schema["$ref"].(string); ok {
		target, fragment := docs.resolve(file, ref)
		if doc, ok := docs.byFile[target]; ok {
			if resolved, err := resolveJSONPointer(doc, fragment); err == nil {
				fields = append(fields, directRequired(docs, target, resolved, depth+1)...)
			}
		}
	}
	if members, ok := schema["allOf"].([]any); ok {
		for _, member := range members {
			fields = append(fields, directRequired(docs, file, member, depth+1)...)
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

type addedRequired struct{ location, field string }

// newlyRequiredAt reports each field current requires at an instance location
// where the baseline neither required nor declared it, unless the location
// lies under a property name new since the baseline: old producers never send
// that property, so a new optional object may require its own members. The
// exemption needs a named property under a baseline object that declares its
// shape (has properties); a new item, additional property or pattern
// property, or a property under a baseline of true, is a new constraint on
// values old producers could already send.
func newlyRequiredAt(baseline, current *locationFacts) []addedRequired {
	var found []addedRequired
	for location, fields := range current.required {
		for field, occurrences := range fields {
			if len(baseline.required[location][field]) > 0 || baseline.declared[location][field] {
				continue
			}
			if underNewProperty(baseline, location) {
				continue
			}
			if noOldBodyTriggers(baseline, occurrences) {
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
		for _, alternative := range group.alternatives {
			for _, field := range alternative {
				added := addedRequired{location: group.location, field: field}
				if reported[added] || len(baseline.required[group.location][field]) > 0 || baseline.declared[group.location][field] {
					continue
				}
				reported[added] = true
				found = append(found, added)
			}
		}
	}
	return found
}

// groupCovered reports whether every body the baseline accepted still meets
// one alternative of an anyOf or oneOf, as far as required fields go. It
// holds when the group never applies to an old body (a new property, or a
// trigger no old body carries); when one alternative needs nothing, or only
// fields the baseline required unconditionally there; or when a baseline
// group at the same location had alternatives each at least as strict as one
// of the current group's (adding an alternative widens).
func groupCovered(baseline *locationFacts, group alternativeGroup) bool {
	if underNewProperty(baseline, group.location) || (len(group.triggers) > 0 && noOldBodyTriggers(baseline, [][]trigger{group.triggers})) {
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
func refusesEveryValue(docs schemaDocs, file string, node any, depth int) bool {
	switch schema := node.(type) {
	case bool:
		return !schema
	case map[string]any:
		ref, ok := schema["$ref"].(string)
		if !ok || depth > maxLocationDepth {
			return false
		}
		target, fragment := docs.resolve(file, ref)
		doc, ok := docs.byFile[target]
		if !ok {
			return false
		}
		resolved, err := resolveJSONPointer(doc, fragment)
		return err == nil && refusesEveryValue(docs, target, resolved, depth+1)
	}
	return false
}

func requiredUnconditionally(baseline *locationFacts, location, field string) bool {
	for _, triggers := range baseline.required[location][field] {
		if len(triggers) == 0 {
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
func noOldBodyTriggers(baseline *locationFacts, occurrences [][]trigger) bool {
	for _, triggers := range occurrences {
		blocked := false
		for _, t := range triggers {
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
