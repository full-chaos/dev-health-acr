// Command checkendpointprofiles is the CHAOS-3273 L3 CI gate for
// dev-health-acr: enforces guardrail G-1 ("a route without a registered
// profile fails CI and may not ship") against
// contracts/auth/v1/endpoint-profiles.acr.json.
//
// Modelled on dev-health-ops's ci/check_endpoint_profiles.py, which is in
// turn modelled on ci/check_transitional_inventory.py (CUT-01): independent
// re-discovery from source, a bidirectional discovery/inventory parity
// check, closed-vocabulary validation, and anchor existence + content-drift
// validation. Discovery is delegated to the SIBLING ci/discover_acr_routes.go
// (lane auth-cp/L1's discovery half) by invoking it as a subprocess (`go run
// ci/discover_acr_routes.go`) rather than duplicating its parsing logic --
// it lives in package main in ci/ and cannot be imported directly, and this
// repo has no existing convention for splitting a CI script into an
// importable package, so subprocess invocation is the least-invasive reuse
// path that does not touch L1's already-committed file.
//
// CROSS-REPO INPUTS: the endpoint-profile schema
// (contracts/auth/v1/endpoint-profile.schema.json), the credential-class
// closed vocabulary (contracts/auth/v1/credential-classes.json) and that
// vocabulary's own schema (contracts/auth/v1/credential-classes.schema.json)
// are OWNED BY OPS (docs/endpoint-profiles.md: "Shared schema (owned by ops,
// reused as is)") and are NOT vendored into this repo. This gate takes their
// paths as REQUIRED flags rather than defaulting to a same-repo path that
// would not exist in a real acr-only checkout. .github/workflows/ci.yml
// supplies them from a sparse checkout of the ops repo at the commit named in
// ci/ops-contract.pin.
//
// KNOWN LIMITS -- each carries a ticket, because a documented limitation with
// no ticket reads as an excuse and one with a ticket reads as a known gap
// someone owns:
//   - Discovery re-derives registrations from SOURCE TEXT, not from a served
//     router object, so a registration it cannot resolve is not cross-checked.
//     It fails closed (UNRESOLVED REGISTRATION) rather than skipping.
//     CHAOS-4761.
//   - Multi-mount route collapse in the discovery half: CHAOS-4760.
//   - Anchor CONTENT verification is a name match, not a proof that the named
//     line is the validator.
//   - Every anchor (primary_validator, reachable_validators[], issued_credential[])
//     carries an explicit marker (the anchor's `note` field: a short, exact,
//     literal substring of the real call/declaration -- or the first
//     backticked span of a longer note) that this gate finds by TEXT
//     (CHAOS-7128, extended to all three kinds by CHAOS-7245): the declared
//     line is an advisory hint and is not compared, and the triviality and
//     function-identity checks run at the marker's own site rather than at the
//     declared line. One invariant per (file, marker):
//     no line carries it twice, and its total occurrences equal the number
//     of distinct sites the rows declare (rows citing one shared definition
//     line are one site). More = AMBIGUOUS ANCHOR MARKER; none/fewer =
//     ANCHOR MARKER NOT FOUND. Still a TEXT match, not proof the text is
//     executable code.
//   - Two rows whose primary_validator anchors point at the SAME source line
//     (the model-config PUT/DELETE rows share one dispatch line) necessarily
//     share one marker too. If both anchors drift onto the same wrong line at
//     once, the marker re-location cannot tell them apart: CHAOS-5652.
//   - The inventory's declared source_commit and credential_class_source are
//     NOT verified by this gate: CHAOS-4765.
//   - CHAOS-7128: rows address a surface by SYMBOL, not line. A row is matched
//     to a discovered registration by (source.file, method, route); a
//     primary_validator anchor is located by its `note` marker. The
//     source.line / anchor.line values remain in the file only because the
//     ops-owned schema requires them, and are advisory hints: a moved line
//     passes (CHAOS-7245: nothing reads the file's content at a declared
//     line), a renamed/removed route (PHANTOM ROW + UNOWNED SURFACE) or a
//     marker that no longer exists (ANCHOR MARKER NOT FOUND) fails loudly.
//     Two registrations on one line are therefore both profilable
//     (CHAOS-4774's limitation no longer applies to source rows).
//   - The inventory's declared source_commit and credential_class_source are
//     NOT verified by this gate: CHAOS-4765.
//   - ★ THE BIGGEST ONE, and the one most likely to be misread: this gate does
//     NOT reconcile a row's SECURITY CLAIM with the code. It checks that a row
//     exists for every surface, that the row is well-formed and internally
//     consistent, and that its anchors resolve to content matching what the row
//     says. It does not check that a route this inventory calls "protected"
//     really is, nor that a "public" one really is. A guarded route relabelled
//     public, or left claiming primary_validator: null, passes today.
//     CHAOS-4780. Guardrail G-1 ("an unregistered route fails CI") is enforced;
//     "the inventory's security claims are true" is not.
//   - Two checks in scripts/ci/test-workflow-contract.sh assert a guard is
//     PRESENT rather than that its property HOLDS: CHAOS-4781.
//
// Usage:
//
//	go run ci/checkendpointprofiles/main.go \
//	    -root PATH \
//	    -inventory contracts/auth/v1/endpoint-profiles.acr.json \
//	    -schema /path/to/endpoint-profile.schema.json \
//	    -credential-classes /path/to/credential-classes.json \
//	    -credential-classes-schema /path/to/credential-classes.schema.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	// LIBRARY CONSTRAINT (decision-sheet B12, commit 8ced6f749; recorded
	// here too because a sheet entry is not where someone swapping a
	// dependency looks): the schema declares Draft 2020-12
	// ("$schema": "https://json-schema.org/draft/2020-12/schema"). This
	// repo's go.mod has THREE JSON Schema packages -- picking the wrong
	// one is a live mistake, not a hypothetical:
	//   - github.com/google/jsonschema-go (this one): doc.go states it
	//     validates "the draft 2020-12 and draft-07 specifications" and
	//     that other drafts are "not supported." Correct.
	//   - github.com/xeipuuv/gojsonschema: NEVER use here. Its draft.go:30-33
	//     caps out at Draft 7 -- against a 2020-12 schema it would silently
	//     accept constructs it cannot interpret, producing a green gate
	//     that checks LESS than it claims. The exact failure class this
	//     whole file exists to close.
	//   - github.com/invopop/jsonschema: a schema GENERATOR, not a
	//     validator -- wrong tool entirely.
	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

// --- discovery report shape (mirrors ci/discover_acr_routes.go's Report) --

type discoveredRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	// Service is the deployed app whose mux discovery found this route on.
	// See discover_acr_routes.go's deployedService for why discovery can
	// state this at all.
	Service string `json:"service"`
}

type discoverReport struct {
	Routes     []discoveredRoute `json:"routes"`
	Unresolved []string          `json:"unresolved"`
}

type surfaceKey struct {
	File string
	Line int
}

// routeKey is a REST surface's identity (CHAOS-7128): the registering file
// plus the mux pattern's method and path. A row is matched to a discovered
// surface by this key, never by line number, so an unrelated edit that
// shifts a registration does not invalidate its row, while a renamed or
// removed route (a changed method or path) fails loudly as PHANTOM ROW +
// UNOWNED SURFACE. The rows' source.line / anchor.line values stay in the
// file because the ops-owned schema requires them, but they are advisory
// hints only.
// markerKey identifies a primary_validator marker: the anchor's file and its
// literal `note` text (CHAOS-7128).
type markerKey struct {
	Path string
	Note string
}

type routeKey struct {
	File   string
	Method string
	Path   string
}

func runDiscovery(root, discovererPath string) (*discoverReport, error) {
	tmp, err := os.CreateTemp("", "acr-discover-*.json")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	// discover_acr_routes.go's relPath() reports each route's file relative
	// to the PROCESS'S CURRENT WORKING DIRECTORY (os.Getwd()), matching its
	// documented "go run ci/discover_acr_routes.go" invocation from the repo
	// root -- not relative to -root. Running it with cmd.Dir unset would
	// inherit THIS process's cwd (e.g. ci/checkendpointprofiles when driven
	// by `go test`, or wherever `go run` for this gate itself was launched
	// from), producing paths like "../../internal/api/app.go" that never
	// match the inventory's repo-relative anchors. Set Dir = root so the
	// subprocess's cwd IS root, exactly matching the discoverer's own
	// documented usage, for both the real tree and a tmp_path fixture root.
	cmd := exec.Command("go", "run", discovererPath, "-root", root, "-out", tmpPath)
	cmd.Dir = root
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("running discover_acr_routes: %w", err)
	}

	raw, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, err
	}
	var report discoverReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("parsing discovery report: %w", err)
	}
	return &report, nil
}

// --- generic JSON helpers --------------------------------------------------

func loadJSON(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return out, nil
}

func asObject(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asArray(v any) []any {
	a, _ := v.([]any)
	return a
}

// asCanonicalPath reads a repo-relative path field and canonicalizes it
// (filepath.ToSlash(filepath.Clean)) so `internal/api/./app.go` and
// `internal/api/app.go` are ONE key for site counting, marker occurrences
// and route ownership (r3 P1). Cleaning never hides an escape: ".." prefixes
// and absolute paths survive Clean and are still rejected by
// anchorPathWithinRoot / checkAnchorExists.
func asCanonicalPath(v any) (string, bool) {
	s, ok := asString(v)
	if !ok || s == "" {
		return s, ok
	}
	return filepath.ToSlash(filepath.Clean(s)), true
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// NOTE ON LIVE VOCABULARY (merge-gate round on 896ca76e): this file used to
// carry a schemaEnum() helper that read a field's enum out of the schema, and
// a test named for the gate's live-vocabulary property that called only that
// helper. The helper had NO call path from check() -- it was dead code, and
// the test that appeared to prove the property proved nothing about the gate.
// Both are gone. The property is real and is delivered by validateAgainstSchema
// below: full Draft 2020-12 validation reads every enum out of the schema
// document by construction, so a schema-level vocabulary addition is accepted
// with zero checker change. That is proven at GATE level, through check(), by
// TestGateReadsIssuedCredentialDirectionAndExposureReachabilityLive.

// validateAgainstSchema validates one loaded JSON document against one
// Draft 2020-12 schema file, appending each reported violation to errs under
// the given label. ONE code path for both documents the gate validates: the
// merge-gate round found the credential-class document going unvalidated
// precisely because it had no path through here, and two parallel
// implementations would let that happen again.
//
// A failure to read, parse or resolve the SCHEMA ITSELF is returned as an
// error rather than appended: the gate must fail loudly when it cannot
// validate, never report a clean run it did not perform.
func validateAgainstSchema(schemaPath string, document any, label string, errs *[]string) error {
	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("reading schema %s: %w", schemaPath, err)
	}
	var schemaTyped jsonschema.Schema
	if err := json.Unmarshal(schemaBytes, &schemaTyped); err != nil {
		return fmt.Errorf("parsing schema %s: %w", schemaPath, err)
	}
	resolved, err := schemaTyped.Resolve(&jsonschema.ResolveOptions{})
	if err != nil {
		return fmt.Errorf("resolving schema %s: %w", schemaPath, err)
	}
	if err := resolved.Validate(document); err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				*errs = append(*errs, label+": "+line)
			}
		}
	}
	return nil
}

// credentialClassVocabulary reduces the ops-owned credential-class document
// to the set of ids the inventory may reference, AND reports ids declared
// more than once.
//
// Merge-gate finding (CHAOS-3273, terra round on 896ca76e; the same class
// ops fixed): the previous version collapsed classes into a set and returned
// only that, so two CONFLICTING definitions of one class_id -- different
// issuer, different validator, different lifecycle authority -- both
// survived as a single set member and the gate returned no errors. A closed
// vocabulary that can hold one id twice with two meanings is not closed.
//
// This cannot be expressed in JSON Schema: uniqueness of a field ACROSS
// objects in an array has no keyword (uniqueItems compares whole items, so
// two entries differing in any other field are already "unique"). It is a
// checker rule by necessity, not by preference.
func credentialClassVocabulary(credentialClasses map[string]any) (map[string]bool, []string) {
	classes := asArray(credentialClasses["classes"])
	out := map[string]bool{}
	counts := map[string]int{}
	var order []string
	for _, c := range classes {
		obj := asObject(c)
		if id, ok := asString(obj["class_id"]); ok {
			if counts[id] == 0 {
				order = append(order, id)
			}
			counts[id]++
			out[id] = true
		}
	}
	var errs []string
	for _, id := range order {
		if counts[id] > 1 {
			errs = append(errs, fmt.Sprintf(
				"DUPLICATE CREDENTIAL CLASS: class_id %q is declared %d times in the credential-class document -- "+
					"a closed vocabulary cannot hold one id with two definitions (JSON Schema cannot express cross-object id uniqueness)",
				id, counts[id],
			))
		}
	}
	return out, errs
}

// gapsMention reports whether row.gaps contains an entry mentioning needle
// (case-sensitive substring, matching the ops checker's convention).
func gapsMention(row map[string]any, needle string) bool {
	gaps := asArray(row["gaps"])
	for _, g := range gaps {
		if s, ok := asString(g); ok && containsFold(s, needle) {
			return true
		}
	}
	return false
}

func containsFold(haystack, needle string) bool {
	hl := toLower(haystack)
	nl := toLower(needle)
	return indexOf(hl, nl) >= 0
}

func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func indexOf(haystack, needle string) int {
	n := len(needle)
	if n == 0 {
		return 0
	}
	for i := 0; i+n <= len(haystack); i++ {
		if haystack[i:i+n] == needle {
			return i
		}
	}
	return -1
}

// --- the gate ---------------------------------------------------------------

// Required-field / top-level-shape / enum vocabulary rules formerly lived
// here as hand-rolled package vars (topLevelRequired, rowRequired). They
// are now enforced by real Draft 2020-12 validation against the schema
// itself (see check()) -- keeping a parallel hand-rolled list invites
// exactly the drift Codex found: a schema-declared rule this file never
// re-derived.

func check(root, inventoryPath, schemaPath, credentialClassesPath, credentialClassesSchemaPath, discovererPath string) ([]string, error) {
	var errs []string

	inventory, err := loadJSON(inventoryPath)
	if err != nil {
		return nil, fmt.Errorf("loading inventory: %w", err)
	}
	credentialClasses, err := loadJSON(credentialClassesPath)
	if err != nil {
		return nil, fmt.Errorf("loading credential classes: %w", err)
	}
	classVocab, vocabErrs := credentialClassVocabulary(credentialClasses)
	errs = append(errs, vocabErrs...)

	// Merge-gate finding (CHAOS-3273, terra round on 896ca76e): the
	// credential-class document was loaded only to harvest ids, and never
	// validated against credential-classes.schema.json -- the schema that
	// makes each entry carry an issuer, a validator, a lifecycle authority
	// and an allowed route set. Every class reduced to bare {"class_id":
	// "..."} therefore passed. The inventory's closed vocabulary was being
	// enforced against a document whose own contents nothing checked, which
	// makes "closed vocabulary" a claim about ids only.
	if err := validateAgainstSchema(credentialClassesSchemaPath, credentialClasses, "CREDENTIAL CLASS SCHEMA VIOLATION", &errs); err != nil {
		return nil, err
	}

	report, err := runDiscovery(root, discovererPath)
	if err != nil {
		return nil, err
	}

	// CHAOS-7128: a row addresses a surface by (file, method, route), so two
	// registrations written on ONE line are both profilable and a moved line
	// never invalidates a row. Only a same method+route registered twice in
	// one file is a collision (DUPLICATE REGISTRATION below).
	discoveredKeys := map[routeKey]discoveredRoute{}
	for _, r := range report.Routes {
		key := routeKey{r.File, r.Method, r.Path}
		if _, clash := discoveredKeys[key]; clash {
			errs = append(errs, fmt.Sprintf(
				"DUPLICATE REGISTRATION: %s registers %s %s more than once -- a row addresses a surface by "+
					"method+route, so the duplicate cannot be profiled separately",
				r.File, r.Method, r.Path,
			))
			continue
		}
		discoveredKeys[key] = r
	}

	// Codex-verified gap (round 1): discovery's Unresolved field -- lines
	// matching mux.Handle(...)/mux.HandleFunc(...) whose pattern expression
	// could not be resolved to a literal, so they never became a Route at
	// all -- was ignored here. A dynamically-expressed registration this
	// discoverer cannot parse would be entirely invisible to the
	// unowned-surface check below: not a missing row, an INVISIBLE route.
	// Fail closed instead: "cannot resolve" must never mean "cannot see".
	for _, u := range report.Unresolved {
		errs = append(errs, fmt.Sprintf(
			"UNRESOLVED REGISTRATION: discovery could not resolve a route pattern (%s) -- "+
				"resolve it in discover_acr_routes.go or the gate cannot guard it (guardrail G-1)",
			u,
		))
	}

	// FULL Draft 2020-12 JSON Schema validation over the whole document
	// (top-level shape, every row's required fields, every field's declared
	// type/enum, including nested $defs like issuedCredential.direction and
	// exposure.reachability) -- real structural validation via
	// github.com/google/jsonschema-go (already a direct dependency; see
	// tests/fullstack/assertrun/schema.go for the same pattern in this
	// repo), never a hand-rolled re-derivation of the schema's own rules.
	// Codex-verified gap (round 1): the hand-rolled version only checked
	// TOP-LEVEL shape plus a handful of per-field enums (two of which --
	// issued_credential.direction, exposure.reachability -- were hardcoded
	// Go maps instead of read from the schema); nothing checked row-level
	// field TYPES at all, so a row with an obviously wrong type anywhere
	// passed silently.
	if err := validateAgainstSchema(schemaPath, inventory, "JSON SCHEMA VIOLATION", &errs); err != nil {
		return nil, err
	}

	rowsRaw, _ := inventory["rows"].([]any)

	idsSeen := map[string]int{}
	rowKeys := map[routeKey]bool{}
	// Every (file, method, route) claimed as a row's `source`, mapped to every
	// row id that claims it -- a discovered surface must be owned by EXACTLY one
	// row. Codex-verified gap (round 1): two rows with DIFFERENT ids both
	// anchored at the same surface (possibly with conflicting
	// classifications) previously returned OK -- worse than a missing row,
	// since both look registered. Not expressible as a JSON Schema
	// constraint; it's a cross-row uniqueness rule.
	surfaceOwners := map[routeKey][]string{}

	// Per (file, marker): the DISTINCT declared lines across every row's
	// anchors of every kind (primary_validator, reachable_validators,
	// issued_credential) -- the number of sites the rows claim (rows citing
	// one shared definition line are one site). checkAnchorMarker requires
	// the marker's occurrence count in the file to equal it; the lines
	// themselves are only counted and ranked, never compared with the file.
	declaredSites := map[markerKey]map[int]bool{}
	for _, raw := range rowsRaw {
		for _, a := range rowAnchorObjects(asObject(raw)) {
			path, _ := asCanonicalPath(a["path"])
			lineF, _ := a["line"].(float64)
			if path == "" || lineF < 1 {
				continue
			}
			note, _ := asString(a["note"])
			if marker := anchorMarker(note); marker != "" {
				mk := markerKey{path, marker}
				if declaredSites[mk] == nil {
					declaredSites[mk] = map[int]bool{}
				}
				declaredSites[mk][int(lineF)] = true
			}
		}
	}

	for idx, raw := range rowsRaw {
		row := asObject(raw)
		id, _ := asString(row["id"])
		if id == "" {
			id = fmt.Sprintf("<row %d, no id>", idx)
		}

		// 3. duplicate id.
		if rid, ok := asString(row["id"]); ok {
			if firstIdx, seen := idsSeen[rid]; seen {
				errs = append(errs, fmt.Sprintf("DUPLICATE ID: %q used by more than one row (first seen at index %d, again at %d)", rid, firstIdx, idx))
			} else {
				idsSeen[rid] = idx
			}
		}

		classification, _ := asString(row["classification"])
		if classification == "public" {
			rationale, _ := asString(row["public_rationale"])
			if rationale == "" {
				errs = append(errs, fmt.Sprintf("MISSING public_rationale: row %q is classification=public but public_rationale is null/empty", id))
			}
		}
		if classification == "protected" {
			if len(asArray(row["accepted_credential_classes"])) == 0 {
				errs = append(errs, fmt.Sprintf("EMPTY accepted_credential_classes: row %q is classification=protected but lists no accepted credential class", id))
			}
		}

		for _, c := range asArray(row["accepted_credential_classes"]) {
			cls, _ := asString(c)
			if !classVocab[cls] {
				errs = append(errs, fmt.Sprintf("UNKNOWN accepted_credential_class: row %q claims %q, not in credential-classes.json's closed vocabulary", id, cls))
			}
		}

		// issued_credential: four-valued (absent / null / [] / [entries]).
		if icRaw, present := row["issued_credential"]; present {
			switch ic := icRaw.(type) {
			case nil:
				if !gapsMention(row, "issued_credential") {
					errs = append(errs, fmt.Sprintf("UNSTATED NULL: row %q has issued_credential=null (undetermined) with no gaps entry explaining it", id))
				}
			case []any:
				for entryIdx, entryRaw := range ic {
					entry := asObject(entryRaw)
					if entry == nil {
						continue // reported by the full schema validation above
					}
					classID, _ := asString(entry["class_id"])
					if !classVocab[classID] {
						errs = append(errs, fmt.Sprintf("UNKNOWN issued_credential class_id: row %q entry %d claims %q", id, entryIdx, classID))
					}
					anchorRaw, hasAnchor := entry["anchor"]
					if !hasAnchor || anchorRaw == nil {
						if !gapsMention(row, "issued_credential") {
							errs = append(errs, fmt.Sprintf("UNSTATED NULL: row %q issued_credential entry %d has anchor=null with no gaps entry explaining it", id, entryIdx))
						}
					} else {
						anchorObj := asObject(anchorRaw)
						checkAnchorExists(root, id, anchorObj, &errs, "issued_credential anchor")
						site := checkAnchorMarker(root, id, "issued_credential", anchorObj, declaredSites, &errs)
						checkIssuedCredentialAnchorIdentity(root, id, entryIdx, anchorObj, entry, site, &errs)
					}
				}
			}
			// default (not a list, not nil): reported by the full schema
			// validation above.
		}

		// exposure: absent / null / object, reachability=unknown gated.
		if expRaw, present := row["exposure"]; present {
			switch exp := expRaw.(type) {
			case nil:
				if !gapsMention(row, "exposure") {
					errs = append(errs, fmt.Sprintf("UNSTATED NULL: row %q has exposure=null (undetermined) with no gaps entry explaining it", id))
				}
			case map[string]any:
				reachability, _ := asString(exp["reachability"])
				src, _ := asString(exp["source"])
				if src == "" {
					errs = append(errs, fmt.Sprintf("MISSING exposure.source: row %q has an exposure claim with no source artifact cited", id))
				}
				if reachability == "unknown" && !gapsMention(row, "exposure") {
					errs = append(errs, fmt.Sprintf("UNSTATED NULL: row %q has exposure.reachability='unknown' with no gaps entry explaining it", id))
				}
			}
			// default: reported by the full schema validation above.
		}

		// primary_validator.anchor null. NOTE: this is about anchor being
		// null while primary_validator itself is a present object (an
		// unresolved anchor on a validator the row DOES claim to have) --
		// distinct from primary_validator itself being null, which is
		// schema-legal for a genuinely public route with no validator at
		// all (falls through the pv != nil guard below). Codex-verified
		// gap (round 1): this used to be scoped to classification ==
		// "protected" only, so a PUBLIC row could set an anchor object
		// with anchor=null and gaps=[] and pass -- but the schema's anchor
		// rule ("null MUST be paired with a gaps entry") is unconditional
		// on classification.
		if pv := asObject(row["primary_validator"]); pv != nil {
			anchorRaw, hasAnchor := pv["anchor"]
			if !hasAnchor || anchorRaw == nil {
				if !gapsMention(row, "primary_validator") {
					errs = append(errs, fmt.Sprintf("UNSTATED NULL: row %q has primary_validator.anchor=null and no gaps entry explaining it", id))
				}
			} else {
				checkAnchorExists(root, id, asObject(anchorRaw), &errs, "primary_validator anchor")
				checkAnchorMarker(root, id, "primary_validator", asObject(anchorRaw), declaredSites, &errs)
			}
		}

		// reachable_validators anchors.
		for rvIdx, rvRaw := range asArray(row["reachable_validators"]) {
			rv := asObject(rvRaw)
			anchorRaw, hasAnchor := rv["anchor"]
			if !hasAnchor || anchorRaw == nil {
				if !gapsMention(row, "reachable_validator") {
					errs = append(errs, fmt.Sprintf("UNSTATED NULL: row %q reachable_validators[%d] has anchor=null and no gaps entry explaining it", id, rvIdx))
				}
			} else {
				rvLabel := fmt.Sprintf("reachable_validators[%d]", rvIdx)
				checkAnchorExists(root, id, asObject(anchorRaw), &errs, rvLabel+" anchor")
				checkAnchorMarker(root, id, rvLabel, asObject(anchorRaw), declaredSites, &errs)
			}
		}

		// source anchor.
		src := asObject(row["source"])
		if src != nil {
			file, _ := asCanonicalPath(src["file"])
			rowMethod, _ := asString(row["method"])
			rowRoute, _ := asString(row["route"])
			if file != "" && rowMethod != "" && rowRoute != "" {
				key := routeKey{file, rowMethod, rowRoute}
				rowKeys[key] = true
				surfaceOwners[key] = append(surfaceOwners[key], id)
			}
		}
	}

	// 3b. duplicate surface ownership: two DIFFERENT ids both claiming the
	// same discovered (file, line) surface.
	for key, owners := range surfaceOwners {
		if len(owners) > 1 {
			sorted := append([]string(nil), owners...)
			sort.Strings(sorted)
			errs = append(errs, fmt.Sprintf(
				"DUPLICATE SURFACE OWNERSHIP: rows %v all claim %s %s in %s -- exactly one row may own a discovered surface",
				sorted, key.Method, key.Path, key.File,
			))
		}
	}

	// 1 & 2. bidirectional surface/row parity.
	sortedDiscoveredKeys := make([]routeKey, 0, len(discoveredKeys))
	for k := range discoveredKeys {
		sortedDiscoveredKeys = append(sortedDiscoveredKeys, k)
	}
	sort.Slice(sortedDiscoveredKeys, func(i, j int) bool {
		if sortedDiscoveredKeys[i].File != sortedDiscoveredKeys[j].File {
			return sortedDiscoveredKeys[i].File < sortedDiscoveredKeys[j].File
		}
		if sortedDiscoveredKeys[i].Path != sortedDiscoveredKeys[j].Path {
			return sortedDiscoveredKeys[i].Path < sortedDiscoveredKeys[j].Path
		}
		return sortedDiscoveredKeys[i].Method < sortedDiscoveredKeys[j].Method
	})
	for _, key := range sortedDiscoveredKeys {
		if !rowKeys[key] {
			errs = append(errs, fmt.Sprintf("UNOWNED SURFACE: rest route %s %s in %s (discovered at line %d) has no row in %s. Add an owning row (guardrail G-1).", key.Method, key.Path, key.File, discoveredKeys[key].Line, filepath.Base(inventoryPath)))
		}
	}

	for idx, raw := range rowsRaw {
		row := asObject(raw)
		id, _ := asString(row["id"])
		if id == "" {
			id = fmt.Sprintf("<row %d, no id>", idx)
		}
		src := asObject(row["source"])
		if src == nil {
			continue
		}
		file, _ := asCanonicalPath(src["file"])
		method, _ := asString(row["method"])
		route, _ := asString(row["route"])
		surface, ok := discoveredKeys[routeKey{file, method, route}]
		if !ok {
			errs = append(errs, fmt.Sprintf("PHANTOM ROW: row %q claims %s %s in %s which independent discovery did not find (renamed or removed route -- re-key or remove the row)", id, method, route, file))
			continue
		}
		line := surface.Line // discovered line, for messages only

		// 5. content/anchor drift: matched row vs discovered surface.
		if sk, _ := asString(row["surface_kind"]); sk != "rest" {
			errs = append(errs, fmt.Sprintf("STALE ANCHOR: row %q claims surface_kind=%q but %s:%d is a REST route (content drift)", id, sk, file, line))
		}

		// Merge-gate finding (CHAOS-3273, terra round on 896ca76e; same
		// class ops fixed in 4bab8745b): `service` was validated against
		// the schema's closed enum but never compared with what discovery
		// actually found, so relabelling a row to a DIFFERENT VALID value
		// returned OK. That is not a cosmetic mislabel. `service` selects
		// which deployed app -- and therefore which middleware stack --
		// the row's whole security analysis applies to, so a row attributed
		// to the wrong app silently invalidates its own reasoning while
		// still satisfying guardrail G-1.
		//
		// Only compared when discovery states a service: an older
		// discoverer report (or a future one that walks a source it cannot
		// attribute) leaves it empty, and comparing against "" would
		// manufacture a failure on every row rather than checking anything.
		if surface.Service != "" {
			if svc, _ := asString(row["service"]); svc != surface.Service {
				errs = append(errs, fmt.Sprintf(
					"SERVICE MISMATCH: row %q claims service=%q but %s:%d is registered on the %q mux "+
						"(the row's reachable-validator and middleware reasoning is attributed to the wrong deployed app)",
					id, svc, file, line, surface.Service,
				))
			}
		}
	}

	sort.Strings(errs)
	return errs, nil
}

func checkAnchorExists(root, rowID string, anchor map[string]any, errs *[]string, label string) {
	if anchor == nil {
		*errs = append(*errs, fmt.Sprintf("SCHEMA VIOLATION: row %q %s must be an object or null", rowID, label))
		return
	}
	path, _ := asString(anchor["path"])
	lineF, _ := anchor["line"].(float64)
	if path == "" {
		*errs = append(*errs, fmt.Sprintf("STALE ANCHOR: row %q %s has no path", rowID, label))
		return
	}
	// Merge-gate round 2 (CHAOS-3273, EXECUTED): the path was joined to root
	// with no containment check, so a row could name
	// "../../../../../../etc/hosts" as its authentication validator and the
	// gate returned OK. The schema's anchor path is defined as relative to
	// the owning repository; an anchor that leaves the repository is not a
	// stale claim about this codebase, it is a claim about a different one.
	if !anchorPathWithinRoot(root, path) {
		*errs = append(*errs, fmt.Sprintf(
			"ANCHOR ESCAPES REPO: row %q %s references %q, which is absolute or resolves outside the repository root -- "+
				"anchor paths are repo-relative by schema, and a validator anchored outside this repo cannot be verified by this gate",
			rowID, label, path,
		))
		return
	}
	full := filepath.Join(root, path)
	if info, err := os.Stat(full); err != nil || !info.Mode().IsRegular() {
		*errs = append(*errs, fmt.Sprintf("STALE ANCHOR: row %q %s references missing file %s", rowID, label, path))
		return
	}
	// CHAOS-7245: this check is deliberately blind to the FILE'S CONTENT at the
	// declared line. `line` / `line_end` are advisory hints (the ops schema
	// requires them), so an unrelated edit that shifts the anchored symbol
	// must not fail them: bounds against the file length and the triviality
	// of the declared line both did, and each unrelated 4-line insertion above
	// protectedRuntimeHandler failed ten rows as TRIVIAL ANCHOR. What the
	// anchor points AT is decided by its marker, at the marker's own site --
	// checkAnchorMarker. What is left here is the part of a hint that is wrong
	// whatever the file says: not a positive line, or a reversed range.
	line := int(lineF)
	if line < 1 {
		*errs = append(*errs, fmt.Sprintf("STALE ANCHOR: row %q %s in %s has line=%d, which is not a line number", rowID, label, path, line))
		return
	}
	// Merge-gate round 2 (EXECUTED): only the START line was validated, and
	// the issued-credential path silently CLAMPED a reversed range. The
	// shipped inventory already carried two of them (line=158 line_end=157,
	// line=185 line_end=184) -- both produced by a re-anchoring edit that
	// bumped `line` and left `line_end` behind. A range the gate silently
	// repairs is a range nobody is told is wrong.
	if raw, ok := anchor["line_end"]; ok && raw != nil {
		endF, isNum := raw.(float64)
		switch {
		case !isNum:
			*errs = append(*errs, fmt.Sprintf("SCHEMA VIOLATION: row %q %s line_end must be a number", rowID, label))
		case int(endF) < line:
			*errs = append(*errs, fmt.Sprintf(
				"INVALID ANCHOR RANGE: row %q %s has line_end=%d before line=%d in %s -- "+
					"a reversed range is silently clamped by readers, so it reads as verified while describing nothing",
				rowID, label, int(endF), line, path,
			))
		}
	}
}

// anchorMarker returns the literal source text an anchor is located by: the
// first backtick-quoted span of its `note` when it has one (the rest of the
// note is prose for the reader), otherwise the whole note. A note with no
// backticks is therefore its own marker, which is what every primary_validator
// anchor has always done.
func anchorMarker(note string) string {
	if open := strings.IndexByte(note, '`'); open >= 0 {
		if n := strings.IndexByte(note[open+1:], '`'); n > 0 {
			return note[open+1 : open+1+n]
		}
	}
	return note
}

// rowAnchorObjects returns every anchor object a row carries, of every kind
// the gate locates by marker: primary_validator, reachable_validators[] and
// issued_credential[]. A null or absent anchor is skipped (the "null needs a
// gaps entry" rules report it elsewhere).
func rowAnchorObjects(row map[string]any) []map[string]any {
	var out []map[string]any
	add := func(v any) {
		if a := asObject(v); a != nil {
			out = append(out, a)
		}
	}
	if pv := asObject(row["primary_validator"]); pv != nil {
		add(pv["anchor"])
	}
	for _, rv := range asArray(row["reachable_validators"]) {
		if o := asObject(rv); o != nil {
			add(o["anchor"])
		}
	}
	for _, ic := range asArray(row["issued_credential"]) {
		if o := asObject(ic); o != nil {
			add(o["anchor"])
		}
	}
	return out
}

// checkAnchorMarker requires every anchor -- primary_validator,
// reachable_validators[] and issued_credential[] alike -- to carry its own
// machine-checkable identity marker (anchor.note, already a schema-legal
// free-string field; see anchorMarker for which part of the note is the
// marker) and locates the anchor by that TEXT, never by the declared line
// number. It returns the source line the anchor resolves to, or 0 when it does
// not resolve (the reason is in errs, or reported by checkAnchorExists).
//
// This is the CHAOS-5652 fix for a real, committed defect isTrivialAnchorLine's
// own denylist approach cannot catch by construction: a re-anchoring edit
// (CHAOS-5637, #519) shifted the real `return a.protectedRuntimeHandler(...)`
// call for the "GET /api/v1/context-fabric/investigations/{result_id}" row from
// line 185 to its new home at line 228 -- and line 185, at the time, held
// `Items: int64(itemCounts.Total()),`, a plausible-looking resource-usage
// struct field literal, not a comment, not blank, not in any denylist shape. A
// denylist can only rule out shapes someone already thought of; a marker the
// anchor's own author commits to, and this gate independently re-locates on
// every run, is a POSITIVE check instead -- it does not matter whether the
// wrong line looks trivial or looks like ordinary business logic, only whether
// the marker text is actually there.
//
// CHAOS-7245 extends that from primary_validator to every anchor kind, and moves
// the remaining content checks off the declared line and onto the marker's
// site. Measured on this tree at 1a1ef01e: inserting four comment lines at the
// top of any single anchored file failed the gate for 10 of the 13 anchored
// files (TRIVIAL ANCHOR, and ANCHOR CONTENT MISMATCH for issued_credential),
// because reachable_validators and issued_credential anchors had no marker and
// their content checks read whatever the declared line then held. The
// per-kind precision discipline that once kept these two kinds off the marker
// path (checkIssuedCredentialAnchorIdentity's measurement: matching row PROSE
// against code produces false misses) does not apply to an exact-substring
// marker, which is a different and much stronger claim than matching prose.
func checkAnchorMarker(root, rowID, label string, anchor map[string]any, declaredSites map[markerKey]map[int]bool, errs *[]string) int {
	if anchor == nil {
		return 0 // reported elsewhere (checkAnchorExists)
	}
	path, _ := asCanonicalPath(anchor["path"])
	lineF, _ := anchor["line"].(float64)
	line := int(lineF)
	if path == "" || line < 1 || !anchorPathWithinRoot(root, path) {
		return 0 // reported elsewhere
	}
	note, _ := asString(anchor["note"])
	marker := anchorMarker(note)
	if strings.TrimSpace(marker) == "" {
		*errs = append(*errs, fmt.Sprintf(
			"MISSING ANCHOR MARKER: row %q %s anchor (%s:%d) has no note -- "+
				"every anchor must name a short, exact, literal substring "+
				"(a call expression, a function declaration; in a longer note, the first `backticked` span) "+
				"this gate finds by text, so a future edit that renames or removes the symbol fails loudly "+
				"and one that merely moves it does not",
			rowID, label, path, line,
		))
		return 0
	}
	raw, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		return 0 // reported elsewhere (checkAnchorExists)
	}
	// ONE INVARIANT (CHAOS-7128, r1/r2 P1 re-found twice as per-branch
	// discounting): the declared line is an advisory hint; the marker TEXT is
	// the anchor. For each (file, marker):
	//   - no single line may carry the marker more than once (one line cannot
	//     say which copy is the validator);
	//   - total occurrences must EQUAL the number of distinct sites the rows
	//     declare for it (rows citing one shared definition line are one site,
	//     which is how nine rows can share protectedRuntimeHandler's one
	//     definition). More = AMBIGUOUS ANCHOR MARKER; none or fewer =
	//     ANCHOR MARKER NOT FOUND.
	declared := declaredSites[markerKey{path, marker}]
	sites := len(declared)
	if sites < 1 {
		sites = 1
	}
	lines := strings.Split(string(raw), "\n")
	total := 0
	var at []int
	for i, l := range lines {
		n := strings.Count(l, marker)
		if n == 0 {
			continue
		}
		total += n
		at = append(at, i+1)
		if n > 1 {
			*errs = append(*errs, fmt.Sprintf(
				"AMBIGUOUS ANCHOR MARKER: row %q %s marker %q appears %d times on line %d of %s -- "+
					"one line cannot say which copy is the real validator. Use a longer, more specific marker",
				rowID, label, marker, n, i+1, path))
			return 0
		}
	}
	switch {
	case total > sites:
		*errs = append(*errs, fmt.Sprintf(
			"AMBIGUOUS ANCHOR MARKER: row %q %s marker %q appears %d times (lines %v) in %s but the rows declare %d site(s) for it -- "+
				"a substring match cannot tell which occurrence is the real validator. Use a longer, more specific marker",
			rowID, label, marker, total, at, path, sites))
		return 0
	case total < sites:
		*errs = append(*errs, fmt.Sprintf(
			"ANCHOR MARKER NOT FOUND: row %q %s anchor's marker %q appears %d time(s) in %s but the rows declare %d site(s) for it -- "+
				"the marked symbol was renamed or removed",
			rowID, label, marker, total, path, sites))
		return 0
	}

	// The marker's own site is what the content checks look at. A marker that
	// resolves to a placeholder/no-op line describes nothing (a bare `})`, a
	// comment); the declared line is not consulted.
	for _, l := range at {
		if isTrivialAnchorLine(lines[l-1]) {
			*errs = append(*errs, fmt.Sprintf(
				"TRIVIAL ANCHOR: row %q %s marker %q resolves to %s:%d, which is a placeholder/no-op line, not a real validator or mint site",
				rowID, label, marker, path, l))
			return 0
		}
	}
	// This anchor's own site among the marker's: the k-th smallest declared
	// line for the marker is the k-th smallest located line. A pure shift moves
	// every site and every declared hint alike, so the order is preserved; only
	// a real reordering of the sites changes the pairing.
	rank := 0
	for d := range declared {
		if d < line {
			rank++
		}
	}
	if rank >= len(at) {
		rank = 0
	}
	return at[rank]
}

// anchorPathWithinRoot reports whether a repo-relative anchor path stays
// inside root once cleaned. Absolute paths are rejected outright; so is any
// path whose cleaned form starts with "..".
func anchorPathWithinRoot(root, path string) bool {
	if filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(root, filepath.Join(root, path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// goFuncDeclRE matches a Go function or method declaration line and
// captures its name -- `func Name(` or `func (recv T) Name(`.
var goFuncDeclRE = regexp.MustCompile(`^\s*func\s+(?:\([^)]*\)\s*)?(\w+)\s*\(`)

// funcNameNear returns the name of the function/method declared AT or
// within [line, lineEnd] (an anchor may point at the def line itself, or
// at a line inside a multi-line range the row's line_end covers), falling
// back to the nearest ENCLOSING declaration found by walking backward from
// line -- the same "what does this specific line actually belong to"
// question the source anchor's content-drift check already answers for
// method+path, applied here to mint-site anchors instead. Returns "" if no
// declaration can be found at all.
func funcNameNear(lines []string, line, lineEnd int) string {
	if lineEnd < line {
		lineEnd = line
	}
	if lineEnd > len(lines) {
		lineEnd = len(lines)
	}
	for i := line - 1; i < lineEnd; i++ {
		if m := goFuncDeclRE.FindStringSubmatch(lines[i]); m != nil {
			return m[1]
		}
	}
	for i := line - 1; i >= 0; i-- {
		if m := goFuncDeclRE.FindStringSubmatch(lines[i]); m != nil {
			return m[1]
		}
	}
	return ""
}

// mentionsWord reports whether name appears in text as a whole word
// (case-insensitive) -- a substring match alone would let "Issue" match
// inside "reissued" or similar.
func mentionsWord(text, name string) bool {
	if name == "" {
		return false
	}
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `\b`)
	return re.MatchString(text)
}

// checkIssuedCredentialAnchorIdentity cross-checks an issued_credential
// anchor's CONTENT against what the row itself claims (anchor.note,
// issuer), rather than only verifying the anchor exists and is in bounds.
// Coordinator ruling (2026-09-01): a denylist of trivial line shapes
// cannot establish an anchor is meaningful, only rule out the shapes
// someone thought of -- proven true by a real committed bug this same
// review found (see trivialAnchorLines' doc comment). Scoped to
// issued_credential specifically (not primary_validator/
// reachable_validators). Codex round 3 checked one acr example
// (context_fabric_model_config_routes.go:98, whose enclosing func is
// ContextFabricOrgModelConfigGetHandler while its description names only
// protectedRuntimeHandler) and judged the scoping justified in acr too;
// a full measurement across acr's own real inventory (funcNameNear +
// mentionsWord, this file's own shipped functions, run against every
// primary_validator/reachable_validators anchor) confirms it more
// strongly than that one example: primary_validator misses 5/16 (31.3%),
// reachable_validators misses 7/7 (100%) -- acr's descriptions
// consistently narrate the single-dispatch MIDDLEWARE guarantee
// ("Authenticator.MiddlewareFor is a SINGLE dispatch point...") rather
// than naming the specific authenticateWebAssertion function the anchor
// points at, the same behavioral-summary-vs-literal-citation gap ops's
// own measurement found (ops: primary_validator 2.1% (7/340) miss,
// reachable_validators 41.6% (297/714) miss -- see ops's identically-named
// check for the reproducible numbers). issued_credential notes, by
// contrast, consistently name the actual mint function (verified against
// all 3 real acr entries before this check was written) because they are
// written as narrow "this is the mint site" citations, not prose
// summaries -- making a real, precision check possible here specifically.
// Reports (not silently passes) when no function/method name can be
// established at all, and when one is found but named nowhere in the
// row's own text -- "say so in the message rather than passing."
//
// CHAOS-7245: `site` is the line the anchor's MARKER resolved to
// (checkAnchorMarker), not the declared line, so a pure shift of the file
// cannot make this read the wrong function. site == 0 means the marker did not
// resolve, which is reported there.
func checkIssuedCredentialAnchorIdentity(root, rowID string, entryIdx int, anchor, entry map[string]any, site int, errs *[]string) {
	if anchor == nil || site < 1 {
		return // reported elsewhere (checkAnchorExists / checkAnchorMarker / schema validation)
	}
	path, _ := asString(anchor["path"])
	if path == "" {
		return // reported elsewhere
	}
	raw, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		return // reported elsewhere (checkAnchorExists)
	}
	lines := strings.Split(string(raw), "\n")
	if site > len(lines) {
		return // unreachable: the site was found in this same file
	}
	name := funcNameNear(lines, site, site)
	note, _ := asString(anchor["note"])
	issuer, _ := asString(entry["issuer"])
	haystack := note + " " + issuer
	if name == "" {
		*errs = append(*errs, fmt.Sprintf(
			"ANCHOR CONTENT UNVERIFIED: row %q issued_credential entry %d anchors %s:%d, "+
				"but no function/method declaration could be found there or nearby -- cannot confirm this is the mint site",
			rowID, entryIdx, path, site,
		))
		return
	}
	if !mentionsWord(haystack, name) {
		*errs = append(*errs, fmt.Sprintf(
			"ANCHOR CONTENT MISMATCH: row %q issued_credential entry %d anchors %s:%d "+
				"(function %q), but neither anchor.note nor issuer names it -- re-anchor to the real mint site or update the note",
			rowID, entryIdx, path, site, name,
		))
	}
}

// Lines an anchor pointing at a real validator/mint site should never
// collapse to -- an obviously-trivial or placeholder body, not the actual
// check/creation logic. This is a DENYLIST of known-trivial shapes, not a
// positive "looks like real code" test -- verified against the real 16-row
// inventory, a positive test (require a call or assignment on the line)
// produced hundreds of false positives, because real anchors routinely
// point at a func/method DECLARATION line, not a line that itself does
// work. NOT a claim that a non-denylisted line IS the correct site (needs
// real semantic understanding this checker does not attempt). Coordinator
// (2026-09-01) found this class of bug for real: the committed
// `POST /api/v1/context-fabric/investigations` row anchored its
// primary_validator at context_fabric_routes.go:156, which is `})` -- the
// close of the handler literal, one line off the actual
// `return a.protectedRuntimeHandler(...)` at line 157. Fixed in the data;
// `})`/`}),` etc. added here so the SHAPE is caught structurally too. A
// genuinely blank line is deliberately NOT included: several real
// reachable_validators anchors point a couple of lines into a doc comment
// near the real declaration, accepted pre-existing imprecision.
var trivialAnchorLines = map[string]bool{
	"{": true, "}": true, "pass": true, "return": true,
	"return {}": true, "return nil": true, "return true": true, "return false": true,
	"break": true, "continue": true, "else": true, "else {": true,
	"default:": true, "fallthrough": true,
	"})": true, "}),": true, "),": true, ")": true,
}

func isTrivialAnchorLine(line string) bool {
	stripped := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(line), ";"))
	if trivialAnchorLines[stripped] {
		return true
	}
	return strings.HasPrefix(stripped, "//") || strings.HasPrefix(stripped, "#") ||
		strings.HasPrefix(stripped, "package ")
}

// --- DISCLOSURE-HOLD reporting -- REPORT ONLY, never a check() error. -----
//
// Other lanes mark content that documents a currently-unfixed weakness with
// the literal string "DISCLOSURE-HOLD" (in a gaps entry, or elsewhere in a
// row's prose) so it can be found and withheld from a public push. A held
// row is a CORRECT row -- the marker records that publishing it is gated on
// a fix landing, not that the row is wrong. Failing on it would pressure
// someone into deleting the finding just to get green, so this never
// touches the errs slice; main() prints it as a separate, always-on report
// line regardless of pass/fail.

const disclosureHoldMarker = "DISCLOSURE-HOLD"

func rowContainsMarker(v any, marker string) bool {
	switch val := v.(type) {
	case string:
		return strings.Contains(val, marker)
	case map[string]any:
		for _, item := range val {
			if rowContainsMarker(item, marker) {
				return true
			}
		}
	case []any:
		for _, item := range val {
			if rowContainsMarker(item, marker) {
				return true
			}
		}
	}
	return false
}

// findDisclosureHoldRows returns every row id (sorted, for stable output)
// whose content -- recursively, any string field, gaps entries and any
// other prose alike -- contains the literal DISCLOSURE-HOLD marker.
func findDisclosureHoldRows(rows []any) []string {
	var held []string
	for _, raw := range rows {
		row := asObject(raw)
		if rowContainsMarker(row, disclosureHoldMarker) {
			id, _ := asString(row["id"])
			if id == "" {
				id = "<no id>"
			}
			held = append(held, id)
		}
	}
	sort.Strings(held)
	return held
}

func main() {
	root := flag.String("root", ".", "repository root")
	inventory := flag.String("inventory", "contracts/auth/v1/endpoint-profiles.acr.json", "inventory JSON path (relative to root)")
	schema := flag.String("schema", "", "endpoint-profile.schema.json path (owned by ops; must be supplied, see file doc comment)")
	credentialClasses := flag.String("credential-classes", "", "credential-classes.json path (owned by ops; must be supplied, see file doc comment)")
	credentialClassesSchema := flag.String("credential-classes-schema", "", "credential-classes.schema.json path (owned by ops; must be supplied, see file doc comment)")
	discoverer := flag.String("discoverer", "", "path to discover_acr_routes.go (default: <root>/ci/discover_acr_routes.go)")
	flag.Parse()

	if *schema == "" || *credentialClasses == "" || *credentialClassesSchema == "" {
		fmt.Fprintln(os.Stderr, "checkendpointprofiles: -schema, -credential-classes and -credential-classes-schema are required (ops-owned files, not vendored into this repo -- see main.go doc comment)")
		os.Exit(2)
	}

	rootAbs, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkendpointprofiles:", err)
		os.Exit(1)
	}
	inventoryPath := *inventory
	if !filepath.IsAbs(inventoryPath) {
		inventoryPath = filepath.Join(rootAbs, inventoryPath)
	}
	discovererPath := *discoverer
	if discovererPath == "" {
		discovererPath = filepath.Join(rootAbs, "ci", "discover_acr_routes.go")
	}

	errs, err := check(rootAbs, inventoryPath, *schema, *credentialClasses, *credentialClassesSchema, discovererPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkendpointprofiles:", err)
		os.Exit(1)
	}

	// DISCLOSURE-HOLD: report only, printed unconditionally (before the
	// pass/fail outcome so it isn't lost in a long failure listing), never
	// folded into errs -- see findDisclosureHoldRows's doc comment.
	inventoryJSON, err := loadJSON(inventoryPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkendpointprofiles:", err)
		os.Exit(1)
	}
	held := findDisclosureHoldRows(asArray(inventoryJSON["rows"]))
	if len(held) > 0 {
		fmt.Printf("DISCLOSURE-HOLD: %d row(s) marked: %s\n", len(held), strings.Join(held, ", "))
	} else {
		fmt.Println("DISCLOSURE-HOLD: 0 rows marked")
	}

	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d endpoint-profile violation(s):\n", len(errs))
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  - %s\n", e)
		}
		os.Exit(1)
	}
	fmt.Println("OK: acr endpoint-profile inventory is consistent with discovery.")
}
