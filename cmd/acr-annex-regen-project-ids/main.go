// Command acr-annex-regen-project-ids regenerates a two-turn trial oracle
// annex's stale pre-v2 "project:<raw>" anchor ids (predating the
// CHAOS-3898/3916 identity migration) to identity.Derive's current
// "project.v2:<provider>:<raw>" canonical form (CHAOS-4348 measurement-layer
// fix).
//
// The oracle annex compares subject ids by EXACT STRING against what
// graphrank actually resolves; a stale-scheme id can never match, which is
// why Run F measured project in-pool 0/20 despite CHAOS-4348's retrieval
// fix (PR #299, 34785cb9) actually working -- proven live via forced-trace
// probes idx 57 and 60 (both anchors reached "corroboration" under their
// live project.v2 id; ticket comment, 2026-08-26).
//
// This tool does NOT guess the provider from the stale id's own text. The
// old scheme's raw suffix happens to embed a provider-looking segment for
// SOME providers (gitlab's composite "<org-uuid>:<provider>:<external_id>",
// idx 57) but not others (linear's raw id is a bare UUID with no such
// segment, idx 60) -- guessing would silently mis-derive a wrong v2 id for
// any provider whose raw id format doesn't happen to embed one.
//
// It also does NOT query the `projects` table directly: the kiac trial
// Postgres (ACR_TEST_TRIAL_POSTGRES_DSN) only carries context-fabric's own
// projection-checkpoint tables, not a seeded `projects` table -- confirmed
// live (\dt on the trial database returns no such relation). The real
// source of truth for "what provider does this raw project id belong to"
// is therefore the SAME FalkorDB graph graphrank itself resolves against:
// every -provider mapping this tool accepts must be independently verified
// against a live forced-trace probe (ACR_TEST_TRIAL_FORCE_TRACE_INDICES)
// that shows the raw id's OWN v2 canonical_id in a real trace event
// (corroboration/identity_gate/ranked_cut) -- see the PR body for the
// probe evidence backing each mapping this tool was actually run with.
//
// Usage:
//
//	acr-annex-regen-project-ids -annex <path> -provider raw_id=provider[,raw_id=provider...] -verified-by "<evidence>" [-check]
//
// -check validates only: reports every stale-scheme project id found (and,
// with -provider, previews the regenerated id for each mapped one) and
// exits 1, writing nothing. -verified-by is not required for -check.
//
// The default mode regenerates the annex in place (after which -check
// exits 0). Every stale id found MUST have a -provider mapping or the tool
// refuses to write anything at all -- a partial regeneration would leave
// the annex in a WORSE state than leaving it alone (some ids fixed, some
// still silently wrong, with no signal distinguishing the two without
// re-running this tool).
//
// -verified-by is REQUIRED to write: a short description of the live
// evidence backing EVERY -provider mapping (e.g. "idx 57/60/33
// forced-trace probes, CHAOS-4348 ticket comment 2026-08-26"). This tool
// cannot itself verify a mapping against the live graph (see above -- the
// graph this data comes from is ephemeral, torn down within seconds of
// the trial process that seeded it exiting) -- codex adversarial review
// (HIGH, confirmed): an unverified or wrong -provider value passes every
// other guard in this tool silently, producing a well-formed but WRONG
// v2 id that reads no scheme mismatch yet still cannot exact-match the
// pool -- the same false-absent failure this tool exists to fix.
// -verified-by does not itself PROVE correctness, but it forces the
// operator to record, in the artifact's own provenance block, a
// falsifiable claim about how each mapping was checked -- never a bare
// assertion with no trail. It is stored in the annex's
// provenance.chaos4348_id_regeneration block below, alongside the exact
// before/after ids and a timestamp, so an approver signing off on the
// resulting annex can see exactly what changed, when, and on what basis
// -- an in-place edit to a chris-signed artifact is never silent.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// staleProjectID matches the pre-v2 "project:<raw>" scheme -- a literal
// "project:" prefix NOT followed by ".v2:" (identity.Derive's own
// kind+".v2:" prefix, which this tool must never touch: re-running it
// against an already-regenerated annex is a no-op, not a double-encode --
// findStaleProjectIDs' own regexp requires the character right after
// "project" to be ':', which "project.v2:" never is).
var staleProjectID = regexp.MustCompile(`^project:(.+)$`)

func main() {
	annexPath := flag.String("annex", "", "path to the oracle annex JSON file (required)")
	providerFlag := flag.String("provider", "", "comma-separated raw_id=provider mappings, one per stale project id (e.g. 70d529e0-...:gitlab:71133891=gitlab)")
	verifiedBy := flag.String("verified-by", "", "required (unless -check): describes the live evidence backing EVERY -provider mapping above (e.g. a forced-trace probe case index and ticket comment); recorded verbatim in the annex's own provenance block")
	checkOnly := flag.Bool("check", false, "validate only: report stale ids and exit 1 without writing")
	allowUnmapped := flag.Bool("allow-unmapped", false, "regenerate every stale id that HAS a -provider mapping and leave any unmapped id untouched (loudly reported), instead of refusing to write at all -- use only when an unmapped id is a KNOWN, separately-tracked residual (e.g. a decoy that no longer resolves against the live trial dataset), never to silently skip one you have not investigated")
	flag.Parse()

	if *annexPath == "" {
		fmt.Fprintln(os.Stderr, "acr-annex-regen-project-ids: -annex is required")
		os.Exit(2)
	}

	raw, err := os.ReadFile(*annexPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acr-annex-regen-project-ids: read annex: %v\n", err)
		os.Exit(1)
	}

	staleIDs := findStaleProjectIDs(string(raw))
	if len(staleIDs) == 0 {
		fmt.Println("acr-annex-regen-project-ids: no stale-scheme project ids found -- nothing to do")
		return
	}

	sortedRaw := make([]string, 0, len(staleIDs))
	occurrences := make(map[string]int, len(staleIDs))
	for id, count := range staleIDs {
		sortedRaw = append(sortedRaw, id)
		occurrences[id] = count
	}
	sort.Strings(sortedRaw)

	providers, err := parseProviderFlag(*providerFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acr-annex-regen-project-ids: -provider: %v\n", err)
		os.Exit(2)
	}

	replacements := make(map[string]string, len(sortedRaw))
	var unmapped []string
	for _, staleID := range sortedRaw {
		match := staleProjectID.FindStringSubmatch(staleID)
		if match == nil {
			continue
		}
		rawID := match[1]
		provider, ok := providers[rawID]
		if !ok {
			unmapped = append(unmapped, staleID)
			continue
		}
		newID, omitted, err := identity.Derive(identity.KindProject, []string{provider, rawID}, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "acr-annex-regen-project-ids: derive v2 id for %s: %v\n", staleID, err)
			os.Exit(1)
		}
		if omitted {
			fmt.Fprintf(os.Stderr, "acr-annex-regen-project-ids: %s exceeds identity.MaxNaturalKeyBytes -- refusing to mint an id that could never appear live\n", staleID)
			os.Exit(1)
		}
		// Round-trip self-check (codex adversarial review, HIGH, partial
		// mitigation): identity.Segments is Derive's own documented exact
		// inverse -- decoding newID must reproduce EXACTLY [provider,
		// rawID]. This catches an encoding-level bug in this tool itself
		// (or a future identity.Derive/Segments drift), but it can NOT
		// catch a well-formed WRONG provider (e.g. "gitlab" typed instead
		// of "linear") -- that risk is why -verified-by is required below,
		// not a substitute for it.
		decoded, segOK := identity.Segments(identity.KindProject, newID)
		if !segOK || len(decoded) != 2 || decoded[0] != provider || decoded[1] != rawID {
			fmt.Fprintf(os.Stderr, "acr-annex-regen-project-ids: INTERNAL: round-trip check failed for %s -> %s (decoded %v) -- refusing to write a self-inconsistent id\n", staleID, newID, decoded)
			os.Exit(1)
		}
		replacements[staleID] = newID
	}

	if *checkOnly {
		fmt.Printf("acr-annex-regen-project-ids: %d stale-scheme project id(s):\n", len(sortedRaw))
		for _, id := range sortedRaw {
			if newID, ok := replacements[id]; ok {
				fmt.Printf("  %s (%d occurrence(s)) -> %s\n", id, occurrences[id], newID)
			} else {
				fmt.Printf("  %s (%d occurrence(s)) -- NO -provider mapping\n", id, occurrences[id])
			}
		}
		os.Exit(1)
	}

	if len(unmapped) > 0 && !*allowUnmapped {
		fmt.Fprintf(os.Stderr, "acr-annex-regen-project-ids: refusing to write: %d stale id(s) have no -provider mapping (a partial regeneration would leave the annex silently half-fixed; pass -allow-unmapped once you have separately confirmed each one is a known, tracked residual):\n", len(unmapped))
		for _, id := range unmapped {
			fmt.Fprintf(os.Stderr, "  %s\n", id)
		}
		os.Exit(1)
	}

	if strings.TrimSpace(*verifiedBy) == "" {
		fmt.Fprintln(os.Stderr, "acr-annex-regen-project-ids: -verified-by is required to write (describe the live evidence backing every -provider mapping -- this tool cannot itself verify a provider against the live graph; see the package doc comment)")
		os.Exit(2)
	}

	updated := string(raw)
	for _, staleID := range sortedRaw {
		newID, ok := replacements[staleID]
		if !ok {
			continue
		}
		updated = strings.ReplaceAll(updated, `"`+staleID+`"`, `"`+newID+`"`)
	}

	updated, err = injectRegenerationProvenance(updated, sortedRaw, replacements, occurrences, unmapped, *verifiedBy)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acr-annex-regen-project-ids: record provenance: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*annexPath, []byte(updated), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "acr-annex-regen-project-ids: write annex: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("acr-annex-regen-project-ids: regenerated %d distinct project id(s) in %s:\n", len(replacements), *annexPath)
	for _, staleID := range sortedRaw {
		newID, ok := replacements[staleID]
		if !ok {
			continue
		}
		fmt.Printf("  %s (%d occurrence(s)) -> %s\n", staleID, occurrences[staleID], newID)
	}
	if len(unmapped) > 0 {
		fmt.Printf("acr-annex-regen-project-ids: WARNING -- %d stale id(s) left UNCHANGED (ran with -allow-unmapped):\n", len(unmapped))
		for _, id := range unmapped {
			fmt.Printf("  %s (still stale scheme -- will keep reading as a scheme mismatch)\n", id)
		}
	}
}

// findStaleProjectIDs scans the RAW JSON TEXT (not a parsed structure) for
// every distinct `"project:<raw>"` string literal and how many times it
// occurs -- deliberately text-level, not schema-aware: the annex's project
// ids appear in three unrelated JSON shapes (oracles.anchor.positive_key,
// oracles.anchor.negatives[], committable_negative_designations[].value),
// and matching the literal string form once, uniformly, is what makes the
// in-place strings.ReplaceAll above safe -- it can never miss an
// occurrence a schema-aware walk forgot to visit, and it can never touch a
// same-looking substring inside an unrelated field (question/corpus text
// is never present in this file -- ids and structural labels only, the
// corpus-safety rule this whole trial harness already follows).
func findStaleProjectIDs(text string) map[string]int {
	quoted := regexp.MustCompile(`"project:[^"]+"`)
	found := make(map[string]int)
	for _, m := range quoted.FindAllString(text, -1) {
		id := strings.Trim(m, `"`)
		found[id]++
	}
	return found
}

// parseProviderFlag parses "raw_id=provider[,raw_id=provider...]". A raw id
// can itself contain '=' only if a provider's real id format does (none
// observed to date); '=' is therefore split on the FIRST occurrence, never
// on every occurrence, so a future such id degrades to "wrong provider"
// (caught by the caller re-deriving and comparing against a probe) rather
// than a silent parse corruption.
func parseProviderFlag(flagValue string) (map[string]string, error) {
	providers := make(map[string]string)
	if flagValue == "" {
		return providers, nil
	}
	for _, pair := range strings.Split(flagValue, ",") {
		rawID, provider, ok := strings.Cut(pair, "=")
		if !ok || rawID == "" || provider == "" {
			return nil, fmt.Errorf("malformed mapping %q, want raw_id=provider", pair)
		}
		providers[rawID] = provider
	}
	return providers, nil
}

// chaos4348IDRegeneration is the audit-trail block this tool injects into
// the annex's own provenance object -- codex adversarial review (MEDIUM,
// confirmed): an in-place edit to a chris-signed artifact (provenance.
// signoff.status=="APPROVED") that leaves no trace of the edit lets the
// harness keep treating the file as approved with no record of what
// actually changed. This does NOT re-run a sign-off cycle (the entities
// named are unchanged -- only their id ENCODING is corrected to match the
// identity.v2 migration every other kind's ids already reflect); it makes
// the change visible to whoever reads the artifact next, with enough
// detail to independently re-check it.
type chaos4348IDRegeneration struct {
	Tool               string            `json:"tool"`
	Ticket             string            `json:"ticket"`
	AppliedAtUTC       string            `json:"applied_at_utc"`
	VerifiedBy         string            `json:"verified_by"`
	IDMappings         map[string]string `json:"id_mappings"`
	OccurrencesUpdated map[string]int    `json:"occurrences_updated"`
	// UnresolvedStaleIDs (empty unless -allow-unmapped was used) names any
	// stale id this run deliberately left untouched -- never silently: a
	// reader of this provenance block sees at a glance that the migration
	// was not exhaustive, and why (this tool's own stderr/stdout at run
	// time carries the "why", not repeated here to avoid the two drifting
	// apart).
	UnresolvedStaleIDs []string `json:"unresolved_stale_ids,omitempty"`
}

// injectRegenerationProvenance adds a "chaos4348_id_regeneration" key to
// the annex's provenance object, recording exactly what this run changed.
// Deliberately narrow in what it re-marshals: "cases" (the bulk of the
// file, already correctly updated in place by the caller's string
// replacement) is carried through as an OPAQUE json.RawMessage, untouched
// byte-for-byte -- only the much smaller "provenance" object is decoded
// and re-encoded, so a 65-case annex's formatting/ordering is not
// perturbed by a JSON round-trip outside the one block that needs one.
func injectRegenerationProvenance(annexJSON string, staleIDs []string, replacements map[string]string, occurrences map[string]int, unmapped []string, verifiedBy string) (string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(annexJSON), &top); err != nil {
		return "", fmt.Errorf("parse annex for provenance update: %w", err)
	}
	provenanceRaw, ok := top["provenance"]
	if !ok {
		return "", fmt.Errorf("annex has no top-level \"provenance\" object")
	}
	var provenance map[string]json.RawMessage
	if err := json.Unmarshal(provenanceRaw, &provenance); err != nil {
		return "", fmt.Errorf("parse annex provenance: %w", err)
	}

	mappings := make(map[string]string, len(replacements))
	updatedCounts := make(map[string]int, len(replacements))
	for _, id := range staleIDs {
		newID, ok := replacements[id]
		if !ok {
			continue
		}
		mappings[id] = newID
		updatedCounts[id] = occurrences[id]
	}
	record := chaos4348IDRegeneration{
		Tool:               "cmd/acr-annex-regen-project-ids",
		Ticket:             "CHAOS-4348",
		AppliedAtUTC:       time.Now().UTC().Format(time.RFC3339),
		VerifiedBy:         verifiedBy,
		IDMappings:         mappings,
		OccurrencesUpdated: updatedCounts,
		UnresolvedStaleIDs: unmapped,
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("marshal regeneration record: %w", err)
	}
	provenance["chaos4348_id_regeneration"] = recordJSON

	newProvenance, err := json.MarshalIndent(provenance, "  ", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal updated provenance: %w", err)
	}
	top["provenance"] = newProvenance

	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal updated annex: %w", err)
	}
	return string(out) + "\n", nil
}
