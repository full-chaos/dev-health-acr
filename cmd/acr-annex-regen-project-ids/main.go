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
// is therefore the SAME FalkorDB graph graphrank itself resolves against --
// but that graph is EPHEMERAL, torn down within seconds of the trial
// process that seeded it exiting, so this tool cannot query it live either.
//
// Usage:
//
//	acr-annex-regen-project-ids -annex <path> -provider raw_id=provider[,raw_id=provider...] -verified-by "<evidence>" -probe-evidence <path>[,<path>...] [-check]
//
// -check validates only: reports every stale-scheme project id found (and,
// with -provider/-probe-evidence, previews whether each mapped one would
// pass) and exits 1, writing nothing. -verified-by/-probe-evidence are not
// required for -check.
//
// The default mode regenerates the annex in place (after which -check
// exits 0). Every stale id found MUST have a -provider mapping or the tool
// refuses to write anything at all -- a partial regeneration would leave
// the annex in a WORSE state than leaving it alone (some ids fixed, some
// still silently wrong, with no signal distinguishing the two without
// re-running this tool).
//
// -verified-by and -probe-evidence are BOTH required to write (codex
// adversarial review, HIGH, confirmed across two rounds: an unverified or
// wrong -provider value otherwise passes every other guard in this tool
// silently -- schema-shape checks, the round-trip identity.Segments
// self-check -- producing a well-formed but WRONG v2 id that reads no
// scheme mismatch yet still cannot exact-match the pool, the same
// false-absent failure this tool exists to fix):
//
//   - -verified-by is a short human-readable description of the evidence
//     (e.g. "idx 57/60/33 forced-trace probes, CHAOS-4348 ticket comment
//     2026-08-26"), recorded verbatim in the annex's own
//     provenance.chaos4348_id_regeneration block -- an in-place edit to a
//     chris-signed artifact (provenance.signoff.status=="APPROVED") is
//     never silent.
//   - -probe-evidence is the MACHINE check: comma-separated paths to
//     two-turn trial report artifacts (ACR_TEST_TRIAL_FORCE_TRACE_INDICES
//     probe output, e.g. .remember/trial-results/gen-trial-*.json). Every
//     derived v2 id must appear as a project-kind "corroboration" trace
//     event in at least one of them, or the tool refuses to write --
//     -verified-by's free text is a falsifiable claim, not a substitute
//     for this.
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
	probeEvidence := flag.String("probe-evidence", "", "required (unless -check): comma-separated paths to two-turn trial report JSON artifacts (ACR_TEST_TRIAL_FORCE_TRACE_INDICES probe output) -- every derived v2 id MUST appear as a project-kind \"corroboration\" stage canonical_id in at least one of these, machine-checked, or the tool refuses to write")
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

	staleIDs, err := findStaleProjectIDs(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acr-annex-regen-project-ids: scan annex: %v\n", err)
		os.Exit(1)
	}
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

	// corroboratedProjectIDs (codex adversarial review, HIGH, confirmed):
	// -verified-by/the round-trip check above prove this tool's OWN
	// encoding is self-consistent, never that the CHOSEN provider is
	// factually correct -- a well-formed but wrong mapping (e.g. "gitlab"
	// typed for a linear raw id) would pass both silently. This is the
	// actual machine check: every derived id must appear as a REAL
	// project-kind "corroboration" trace event in a real probe artifact,
	// not merely be well-formed. Loaded even for -check (so -check can
	// preview whether a candidate mapping would pass), required to WRITE.
	var corroboratedProjectIDs map[string]bool
	if strings.TrimSpace(*probeEvidence) != "" {
		corroboratedProjectIDs, err = loadCorroboratedProjectIDs(strings.Split(*probeEvidence, ","))
		if err != nil {
			fmt.Fprintf(os.Stderr, "acr-annex-regen-project-ids: -probe-evidence: %v\n", err)
			os.Exit(1)
		}
	}

	replacements := make(map[string]string, len(sortedRaw))
	var unmapped []string
	var unverified []string
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
		// Round-trip self-check: identity.Segments is Derive's own
		// documented exact inverse -- decoding newID must reproduce
		// EXACTLY [provider, rawID]. Catches an encoding-level bug in
		// this tool itself, never a well-formed wrong provider -- that is
		// corroboratedProjectIDs' job, below.
		decoded, segOK := identity.Segments(identity.KindProject, newID)
		if !segOK || len(decoded) != 2 || decoded[0] != provider || decoded[1] != rawID {
			fmt.Fprintf(os.Stderr, "acr-annex-regen-project-ids: INTERNAL: round-trip check failed for %s -> %s (decoded %v) -- refusing to write a self-inconsistent id\n", staleID, newID, decoded)
			os.Exit(1)
		}
		if corroboratedProjectIDs != nil && !corroboratedProjectIDs[newID] {
			unverified = append(unverified, staleID)
			continue
		}
		replacements[staleID] = newID
	}

	if *checkOnly {
		fmt.Printf("acr-annex-regen-project-ids: %d stale-scheme project id(s):\n", len(sortedRaw))
		for _, id := range sortedRaw {
			switch {
			case replacements[id] != "":
				fmt.Printf("  %s (%d occurrence(s)) -> %s [corroborated]\n", id, occurrences[id], replacements[id])
			case contains(unverified, id):
				fmt.Printf("  %s (%d occurrence(s)) -- provider mapping given but NOT corroborated by -probe-evidence\n", id, occurrences[id])
			default:
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

	if corroboratedProjectIDs == nil {
		fmt.Fprintln(os.Stderr, "acr-annex-regen-project-ids: -probe-evidence is required to write (comma-separated trial-report artifact paths; every derived id must be machine-corroborated, not merely well-formed -- see the package doc comment)")
		os.Exit(2)
	}
	// unverified is NEVER bypassable by -allow-unmapped: that flag exists
	// for a raw id with no live subject at all (a known, investigated
	// residual, still stale-scheme), not for a provider guess this run
	// could not corroborate -- those are different failure shapes and
	// conflating them would let a wrong-but-plausible provider slip
	// through under the same escape hatch meant for "no data exists".
	if len(unverified) > 0 {
		fmt.Fprintf(os.Stderr, "acr-annex-regen-project-ids: refusing to write: %d -provider mapping(s) produced an id NOT corroborated by -probe-evidence (a well-formed but wrong provider is indistinguishable from a correct one without this check):\n", len(unverified))
		for _, id := range unverified {
			fmt.Fprintf(os.Stderr, "  %s\n", id)
		}
		os.Exit(1)
	}

	updated := string(raw)
	for _, staleID := range sortedRaw {
		newID, ok := replacements[staleID]
		if !ok {
			continue
		}
		updated = strings.ReplaceAll(updated, `"`+staleID+`"`, `"`+newID+`"`)
	}

	updated, err = injectRegenerationProvenance(updated, sortedRaw, replacements, occurrences, unmapped, *verifiedBy, *probeEvidence)
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

// findStaleProjectIDs scans the RAW JSON TEXT of the annex's "cases" object
// ONLY (codex adversarial review, MEDIUM, confirmed: an earlier version
// scanned the WHOLE document, including this tool's own injected
// provenance.chaos4348_id_regeneration.id_mappings block -- which records
// each OLD stale id as a map KEY by design, permanently re-triggering
// -check on every subsequent run even after every real case was already
// fixed) for every distinct `"project:<raw>"` string literal and how many
// times it occurs. Scoped to "cases", but still text-level WITHIN that
// scope, not further schema-aware: the annex's project ids appear in three
// unrelated JSON shapes there (oracles.anchor.positive_key,
// oracles.anchor.negatives[], committable_negative_designations[].value),
// and matching the literal string form once, uniformly, is what makes the
// in-place strings.ReplaceAll above safe -- it can never miss an
// occurrence a schema-aware walk forgot to visit, and it can never touch a
// same-looking substring inside an unrelated field (question/corpus text
// is never present in this file -- ids and structural labels only, the
// corpus-safety rule this whole trial harness already follows).
func findStaleProjectIDs(annexJSON []byte) (map[string]int, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(annexJSON, &top); err != nil {
		return nil, fmt.Errorf("parse annex: %w", err)
	}
	cases, ok := top["cases"]
	if !ok {
		return nil, fmt.Errorf("annex has no top-level \"cases\" object")
	}
	quoted := regexp.MustCompile(`"project:[^"]+"`)
	found := make(map[string]int)
	for _, m := range quoted.FindAllString(string(cases), -1) {
		id := strings.Trim(m, `"`)
		found[id]++
	}
	return found, nil
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
	ProbeEvidence      []string          `json:"probe_evidence"`
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
func injectRegenerationProvenance(annexJSON string, staleIDs []string, replacements map[string]string, occurrences map[string]int, unmapped []string, verifiedBy, probeEvidence string) (string, error) {
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
		ProbeEvidence:      strings.Split(probeEvidence, ","),
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

// probeArtifactShape is the minimal slice of a two-turn trial report
// (internal/runtime/hosted/chaos3742_two_turn_confirmation_test.go's
// twoTurnReport/twoTurnCaseResult, mirrored in
// cmd/acr-trial-merge-two-turn/main.go) this tool needs: just enough of
// each row's turn1_trace_events to read a graphrank.ResolutionTraceEvent's
// Stage and Subject.{kind,canonical_id} -- deliberately NOT importing the
// hosted package's own (test-only, internal) types, since this is a
// separate `package main` reading the artifact as plain JSON, the same
// arm's-length relationship cmd/acr-trial-merge-two-turn already has to
// its own producer.
type probeArtifactShape struct {
	Results []struct {
		Turn1TraceEvents []struct {
			Stage   string `json:"Stage"`
			Subject struct {
				Kind        string `json:"kind"`
				CanonicalID string `json:"canonical_id"`
			} `json:"Subject"`
		} `json:"turn1_trace_events"`
	} `json:"results"`
}

// loadCorroboratedProjectIDs reads one or more two-turn trial report
// artifacts (forced-trace probe output, ACR_TEST_TRIAL_FORCE_TRACE_INDICES)
// and returns the SET of every project-kind canonical_id that reached the
// "corroboration" trace stage in any of them -- graphrank's own proof that
// a subject actually entered the real candidate pool under that exact id
// (see internal/contextfabric/graphrank/resolve.go). This is the machine
// check -verified-by's free text cannot be: a derived v2 id not in this
// set was never actually seen live, regardless of how plausible the
// -provider mapping that produced it looks.
func loadCorroboratedProjectIDs(paths []string) (map[string]bool, error) {
	ids := make(map[string]bool)
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read probe artifact %s: %w", path, err)
		}
		var artifact probeArtifactShape
		if err := json.Unmarshal(raw, &artifact); err != nil {
			return nil, fmt.Errorf("parse probe artifact %s: %w", path, err)
		}
		found := 0
		for _, result := range artifact.Results {
			for _, event := range result.Turn1TraceEvents {
				if event.Stage == "corroboration" && event.Subject.Kind == "project" && event.Subject.CanonicalID != "" {
					ids[event.Subject.CanonicalID] = true
					found++
				}
			}
		}
		if found == 0 {
			return nil, fmt.Errorf("probe artifact %s has zero project-kind corroboration events -- not usable as verification evidence (did ACR_TEST_TRIAL_FORCE_TRACE_INDICES actually cover this case?)", path)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("-probe-evidence given but no project-kind corroboration events found across any file")
	}
	return ids, nil
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
