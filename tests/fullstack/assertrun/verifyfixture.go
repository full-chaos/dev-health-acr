package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// --- fixture-manifest.json / testdata/evaluation/v1/manifest.json shapes ---

type fixtureManifest struct {
	SchemaVersion     string                    `json:"schema_version"`
	FixtureVersion    string                    `json:"fixture_version"`
	SourceCorpus      fixtureSourceCorpus       `json:"source_corpus"`
	SeedFiles         []fixtureSeedFile         `json:"seed_files"`
	FixedIdentities   fixtureFixedIdentities    `json:"fixed_identities"`
	ExpectedRowCounts map[string]map[string]int `json:"expected_row_counts"`
	ScopeProbes       []fixtureScopeProbe       `json:"scope_probes"`
}

type fixtureSourceCorpus struct {
	Path          string                `json:"path"`
	ManifestPath  string                `json:"manifest_path"`
	CorpusVersion string                `json:"corpus_version"`
	ConsumedFiles []fixtureConsumedFile `json:"consumed_files"`
}

type fixtureConsumedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type fixtureSeedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type fixtureFixedIdentities struct {
	Repositories []fixtureRepository `json:"repositories"`
}

type fixtureRepository struct {
	Role   string `json:"role"`
	Slug   string `json:"slug"`
	RepoID string `json:"repo_id"`
}

type fixtureScopeProbe struct {
	Description string `json:"description"`
	Query       string `json:"query"`
	Expected    int    `json:"expected"`
}

type evaluationManifest struct {
	SchemaVersion string                `json:"schema_version"`
	CorpusVersion string                `json:"corpus_version"`
	Files         []fixtureConsumedFile `json:"files"`
}

// --- fixture-verification.json output shape ---

type corpusHashCheck struct {
	Path                         string `json:"path"`
	FixtureExpectedSHA256        string `json:"fixture_expected_sha256"`
	CorpusManifestExpectedSHA256 string `json:"corpus_manifest_expected_sha256"`
	ActualSHA256                 string `json:"actual_sha256"`
	OK                           bool   `json:"ok"`
	Message                      string `json:"message,omitempty"`
}

type seedHashCheck struct {
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256"`
	ActualSHA256   string `json:"actual_sha256"`
	OK             bool   `json:"ok"`
	Message        string `json:"message,omitempty"`
}

type probeCheck struct {
	Name        string `json:"name"`
	SQLRedacted string `json:"sql_redacted"`
	Expected    string `json:"expected"`
	Actual      string `json:"actual"`
	OK          bool   `json:"ok"`
	Message     string `json:"message,omitempty"`
}

type fixtureVerification struct {
	SchemaVersion  string            `json:"schema_version"`
	FixtureVersion string            `json:"fixture_version"`
	CorpusHashes   []corpusHashCheck `json:"corpus_hashes"`
	SeedHashes     []seedHashCheck   `json:"seed_hashes"`
	Probes         []probeCheck      `json:"probes"`
	// UnhandledPythonMigrationNotes is populated only when --migrations-dir is given: DDL in
	// a Python ops migration this tool's replay could not attribute to a table (see
	// pymigration.go). It never fails the run by itself -- see verify-seed-schema, the
	// dedicated static check, for why -- it is here so a live run's own
	// fixture-verification.json also discloses the gap rather than only a separate offline
	// report nobody may be looking at.
	UnhandledPythonMigrationNotes []string `json:"unhandled_python_migration_notes,omitempty"`
	OK                            bool     `json:"ok"`
}

func unhandledDDLStrings(notes []unhandledDDL) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, n.String())
	}
	return out
}

const fixtureVerificationSchema = "fullstack_fixture_verification.v1"

func runVerifyFixture(args []string) int {
	fs := flag.NewFlagSet("verify-fixture", flag.ContinueOnError)
	manifestPath := fs.String("manifest", "", "path to testdata/fullstack/v1/fixture-manifest.json")
	corpusDir := fs.String("corpus", "", "path to testdata/evaluation/v1 (the canonical corpus)")
	seedDir := fs.String("seed-dir", "", "path to testdata/fullstack/v1/seed/clickhouse")
	orgID := fs.String("org-id", "", "the isolated organization UUID substituted for __ORG_ID__")
	database := fs.String("database", "", "the ClickHouse database name substituted for __DATABASE__ (optional)")
	out := fs.String("out", "", "path to write fixture-verification.json")
	probeCommandRaw := fs.String("probe-command", "", "shell-quoted probe command prefix; the (substituted) SQL is appended as the final argument")
	probeCommandFile := fs.String("probe-command-file", "", "file holding the NUL-separated probe argv; preferred, because the caller's compose wrapper is a shell function and cannot be expressed as a quoted string")
	migrationsDir := fs.String("migrations-dir", "", "optional path to ops/src/dev_health_ops/migrations/clickhouse; when given, unattributable Python migration DDL is disclosed in fixture-verification.json rather than only in verify-seed-schema's own report")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	for name, value := range map[string]string{"--manifest": *manifestPath, "--corpus": *corpusDir, "--seed-dir": *seedDir, "--org-id": *orgID, "--out": *out} {
		if value == "" {
			fmt.Fprintf(os.Stderr, "[assertrun] FAIL fixture: %s is required\n", name)
			return 2
		}
	}
	if *probeCommandRaw == "" && *probeCommandFile == "" {
		fmt.Fprintf(os.Stderr, "[assertrun] FAIL fixture: --probe-command or --probe-command-file is required\n")
		return 2
	}

	manifest, err := loadFixtureManifest(*manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[assertrun] FAIL fixture manifest: %s\n", redact(err.Error()))
		return 1
	}
	corpusManifestPath := filepath.Join(*corpusDir, "manifest.json")
	corpusManifest, err := loadEvaluationManifest(corpusManifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[assertrun] FAIL fixture corpus manifest: %s\n", redact(err.Error()))
		return 1
	}
	probeWords, err := resolveProbeWords(*probeCommandFile, *probeCommandRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[assertrun] FAIL fixture probe-command: %s\n", redact(err.Error()))
		return 1
	}
	if len(probeWords) == 0 {
		fmt.Fprintf(os.Stderr, "[assertrun] FAIL fixture probe-command: expanded to zero words\n")
		return 1
	}

	report := fixtureVerification{
		SchemaVersion:  fixtureVerificationSchema,
		FixtureVersion: manifest.FixtureVersion,
		OK:             true,
	}

	report.CorpusHashes = verifyCorpusHashes(manifest, corpusManifest, *corpusDir)
	seedHashes, err := verifySeedHashes(manifest, *seedDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[assertrun] FAIL fixture seed-dir: %s\n", redact(err.Error()))
		return 1
	}
	report.SeedHashes = seedHashes
	report.Probes = runFixtureProbes(manifest, *orgID, *database, probeWords)

	if *migrationsDir != "" {
		if _, unhandled, err := replayMigrationsDir(*migrationsDir); err != nil {
			fmt.Fprintf(os.Stderr, "[assertrun] WARN fixture migrations-dir: %s\n", redact(err.Error()))
		} else {
			report.UnhandledPythonMigrationNotes = unhandledDDLStrings(unhandled)
			for _, note := range unhandled {
				fmt.Fprintf(os.Stderr, "[assertrun] WARN fixture: %s\n", redact(note.String()))
			}
		}
	}

	for _, c := range report.CorpusHashes {
		if !c.OK {
			report.OK = false
		}
	}
	for _, s := range report.SeedHashes {
		if !s.OK {
			report.OK = false
		}
	}
	for _, p := range report.Probes {
		if !p.OK {
			report.OK = false
		}
	}

	if err := writeFixtureVerification(*out, report); err != nil {
		fmt.Fprintf(os.Stderr, "[assertrun] FAIL fixture: %s\n", redact(err.Error()))
		return 1
	}

	if !report.OK {
		for _, c := range report.CorpusHashes {
			if !c.OK {
				fmt.Fprintf(os.Stderr, "[assertrun] FAIL fixture corpus_hash %s: %s\n", c.Path, redact(c.Message))
			}
		}
		for _, s := range report.SeedHashes {
			if !s.OK {
				fmt.Fprintf(os.Stderr, "[assertrun] FAIL fixture seed_hash %s: %s\n", s.Path, redact(s.Message))
			}
		}
		for _, p := range report.Probes {
			if !p.OK {
				fmt.Fprintf(os.Stderr, "[assertrun] FAIL fixture probe %s: %s\n", p.Name, redact(p.Message))
			}
		}
		return 1
	}
	return 0
}

// loadFixtureManifest requires every section this tool depends on to actually contain
// entries. A manifest that parses as valid JSON but has, say, an empty scope_probes array
// would otherwise silently produce an empty (therefore vacuously passing) probe check slice
// -- Codex finding 6.
func loadFixtureManifest(path string) (fixtureManifest, error) {
	var m fixtureManifest
	if _, err := readJSONFile(path, &m); err != nil {
		return fixtureManifest{}, err
	}
	if m.FixtureVersion == "" {
		return fixtureManifest{}, fmt.Errorf("%s: missing fixture_version", path)
	}
	if len(m.SourceCorpus.ConsumedFiles) == 0 {
		return fixtureManifest{}, fmt.Errorf("%s: source_corpus.consumed_files is empty -- corpus hashes would go unverified", path)
	}
	if len(m.SeedFiles) == 0 {
		return fixtureManifest{}, fmt.Errorf("%s: seed_files is empty -- seed hashes would go unverified", path)
	}
	if len(m.ScopeProbes) == 0 {
		return fixtureManifest{}, fmt.Errorf("%s: scope_probes is empty -- no scope-isolation probes would run", path)
	}
	if len(m.ExpectedRowCounts) == 0 {
		return fixtureManifest{}, fmt.Errorf("%s: expected_row_counts is empty -- no row-count probes would run", path)
	}
	return m, nil
}

func loadEvaluationManifest(path string) (evaluationManifest, error) {
	var m evaluationManifest
	if _, err := readJSONFile(path, &m); err != nil {
		return evaluationManifest{}, err
	}
	return m, nil
}

func verifyCorpusHashes(manifest fixtureManifest, corpusManifest evaluationManifest, corpusDir string) []corpusHashCheck {
	corpusExpected := make(map[string]string, len(corpusManifest.Files))
	for _, f := range corpusManifest.Files {
		corpusExpected[f.Path] = f.SHA256
	}

	var checks []corpusHashCheck
	for _, consumed := range manifest.SourceCorpus.ConsumedFiles {
		check := corpusHashCheck{
			Path:                         consumed.Path,
			FixtureExpectedSHA256:        consumed.SHA256,
			CorpusManifestExpectedSHA256: corpusExpected[consumed.Path],
		}
		actual, err := sha256File(filepath.Join(corpusDir, consumed.Path))
		if err != nil {
			check.Message = err.Error()
			checks = append(checks, check)
			continue
		}
		check.ActualSHA256 = actual
		switch {
		case check.CorpusManifestExpectedSHA256 == "":
			check.Message = fmt.Sprintf("%s is not listed in %s/manifest.json", consumed.Path, corpusDir)
		case actual != consumed.SHA256:
			check.Message = fmt.Sprintf("actual sha256 does not match fixture-manifest.json's recorded hash for %s", consumed.Path)
		case actual != check.CorpusManifestExpectedSHA256:
			check.Message = fmt.Sprintf("actual sha256 does not match testdata/evaluation/v1/manifest.json's recorded hash for %s", consumed.Path)
		default:
			check.OK = true
		}
		checks = append(checks, check)
	}
	return checks
}

// verifySeedHashes hashes every manifest-listed seed file AND requires an exact bijection
// between those listings and the *.sql files actually present in seedDir. Without the
// bijection, scripts/e2e/fullstack-opencode.sh's seed_fullstack_evidence (which globs
// seed/clickhouse/*.sql and executes every file it finds) could run a file this tool never
// hashed or otherwise checked at all -- an unverified statement executing against the live
// database (Codex finding 6).
func verifySeedHashes(manifest fixtureManifest, seedDir string) ([]seedHashCheck, error) {
	var checks []seedHashCheck
	for _, seed := range manifest.SeedFiles {
		check := seedHashCheck{Path: seed.Path, ExpectedSHA256: seed.SHA256}
		actual, err := sha256File(filepath.Join(seedDir, filepath.Base(seed.Path)))
		if err != nil {
			check.Message = err.Error()
			checks = append(checks, check)
			continue
		}
		check.ActualSHA256 = actual
		check.OK = actual == seed.SHA256
		if !check.OK {
			check.Message = fmt.Sprintf("actual sha256 does not match fixture-manifest.json's recorded hash for %s", seed.Path)
		}
		checks = append(checks, check)
	}

	entries, err := os.ReadDir(seedDir)
	if err != nil {
		return nil, fmt.Errorf("read seed dir %s: %w", seedDir, err)
	}
	onDisk := newStringSet()
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			onDisk.add(e.Name())
		}
	}
	manifestListed := newStringSet()
	for _, seed := range manifest.SeedFiles {
		manifestListed.add(filepath.Base(seed.Path))
	}
	unlisted := manifestListed.missing(onDisk.sorted())      // on disk, not in the manifest
	missingOnDisk := onDisk.missing(manifestListed.sorted()) // in the manifest, not on disk
	bijection := seedHashCheck{Path: "<seed_files bijection: every *.sql on disk vs. every manifest-listed seed file>"}
	switch {
	case len(unlisted) == 0 && len(missingOnDisk) == 0:
		bijection.OK = true
	default:
		var parts []string
		if len(unlisted) > 0 {
			parts = append(parts, "present in "+seedDir+" but NOT listed in fixture-manifest.json (would run unverified): "+joinIDs(unlisted))
		}
		if len(missingOnDisk) > 0 {
			parts = append(parts, "listed in fixture-manifest.json but missing from "+seedDir+": "+joinIDs(missingOnDisk))
		}
		bijection.Message = strings.Join(parts, "; ")
	}
	checks = append(checks, bijection)

	return checks, nil
}

// substituteProbeSQL mirrors the seeder's __ORG_ID__/__DATABASE__ textual substitution
// (scripts/e2e/fullstack-opencode.sh:seed_fullstack_evidence uses sed on those literal
// tokens), generalized to also handle ClickHouse's own {org_id:String}/{database:String}
// query-parameter syntax, which is what fixture-manifest.json's scope_probes queries
// actually use. --probe-command ends in "--query" with no --param_* flags, so the only way
// to bind these values is to substitute them directly into the SQL text as quoted string
// literals before it is appended as the final argument.
func substituteProbeSQL(sql, orgID, database string) string {
	sql = strings.ReplaceAll(sql, "__ORG_ID__", orgID)
	sql = strings.ReplaceAll(sql, "__DATABASE__", database)
	sql = strings.ReplaceAll(sql, "{org_id:String}", "'"+orgID+"'")
	sql = strings.ReplaceAll(sql, "{database:String}", "'"+database+"'")
	return sql
}

func runFixtureProbes(manifest fixtureManifest, orgID, database string, probeWords []string) []probeCheck {
	var checks []probeCheck

	for _, probe := range manifest.ScopeProbes {
		sql := substituteProbeSQL(probe.Query, orgID, database)
		checks = append(checks, executeProbe(probe.Description, sql, strconv.Itoa(probe.Expected), probeWords))
	}

	repoIDBySlug := map[string]string{}
	for _, repo := range manifest.FixedIdentities.Repositories {
		repoIDBySlug[repo.Slug] = repo.RepoID
	}

	slugs := make([]string, 0, len(manifest.ExpectedRowCounts))
	for slug := range manifest.ExpectedRowCounts {
		if slug != "totals" {
			slugs = append(slugs, slug)
		}
	}
	sort.Strings(slugs)

	totalActual := map[string]int{}
	orgScopedExpected := map[string]int{}
	orgScopedShape := map[string]tableShape{}
	for _, slug := range slugs {
		repoID, ok := repoIDBySlug[slug]
		if !ok {
			checks = append(checks, probeCheck{
				Name:    fmt.Sprintf("expected_row_counts.%s", slug),
				OK:      false,
				Message: fmt.Sprintf("no fixed_identities.repositories entry for %s to derive repo_id from", slug),
			})
			continue
		}
		tables := make([]string, 0, len(manifest.ExpectedRowCounts[slug]))
		for table := range manifest.ExpectedRowCounts[slug] {
			tables = append(tables, table)
		}
		sort.Strings(tables)
		for _, table := range tables {
			expected := manifest.ExpectedRowCounts[slug][table]
			name := fmt.Sprintf("expected_row_counts.%s.%s", slug, table)
			shape, err := describeTable(table, probeWords)
			if err != nil {
				checks = append(checks, probeCheck{Name: name, OK: false, Message: redact(err.Error())})
				continue
			}
			// A table with no repo_id cannot be counted per repository. The manifest still
			// lists it under each one, so accumulate the expectations and check the
			// org-scoped count once, after the loop.
			if table != "repos" && !shape.HasRepoID {
				orgScopedExpected[table] += expected
				orgScopedShape[table] = shape
				continue
			}
			sql := rowCountSQL(table, slug, repoID, orgID, shape)
			check := executeProbe(name, sql, strconv.Itoa(expected), probeWords)
			check = withCountingContext(check, shape)
			checks = append(checks, check)
			if check.OK {
				if n, err := strconv.Atoi(check.Actual); err == nil {
					totalActual[table] += n
				}
			}
		}
	}

	orgScopedTables := make([]string, 0, len(orgScopedExpected))
	for table := range orgScopedExpected {
		orgScopedTables = append(orgScopedTables, table)
	}
	sort.Strings(orgScopedTables)
	for _, table := range orgScopedTables {
		name := fmt.Sprintf("expected_row_counts.%s (org-scoped: no repo_id column)", table)
		sql := rowCountSQL(table, "", "", orgID, orgScopedShape[table])
		check := executeProbe(name, sql, strconv.Itoa(orgScopedExpected[table]), probeWords)
		check = withCountingContext(check, orgScopedShape[table])
		checks = append(checks, check)
		if check.OK {
			if n, err := strconv.Atoi(check.Actual); err == nil {
				totalActual[table] += n
			}
		}
	}

	// "totals" has no single repo_id to probe against; verify it arithmetically instead of
	// issuing a cross-repository SQL query the manifest never specified.
	if totals, ok := manifest.ExpectedRowCounts["totals"]; ok {
		tables := make([]string, 0, len(totals))
		for table := range totals {
			tables = append(tables, table)
		}
		sort.Strings(tables)
		for _, table := range tables {
			expected := totals[table]
			actual, computed := totalActual[table]
			check := probeCheck{
				Name:     fmt.Sprintf("expected_row_counts.totals.%s", table),
				Expected: strconv.Itoa(expected),
			}
			if !computed {
				check.Message = fmt.Sprintf("no per-repository probes for %s succeeded to sum", table)
			} else {
				check.Actual = strconv.Itoa(actual)
				check.OK = actual == expected
				if !check.OK {
					check.Message = fmt.Sprintf("sum of per-repository %s counts does not match fixture-manifest.json's totals", table)
				}
			}
			checks = append(checks, check)
		}
	}

	return checks
}

func quotedOrgID(orgID string) string { return "'" + orgID + "'" }

// withCountingContext annotates a FAILED row-count probe with the engine the count was taken
// against. A passing probe is left exactly as it was: the engine is already disclosed by the
// probe's own recorded SQL (FINAL or no FINAL), and only the failure needs to explain itself.
//
// This exists because of a real, and initially opaque, failure: ops migration 087 converted
// file_complexity_snapshots from MergeTree to ReplacingMergeTree(computed_at), describeTable
// (correctly) started appending FINAL, and the count silently changed meaning from "rows the
// seed inserted" to "distinct sorting-key tuples" -- 4 became 3. The message said only that
// the probe "did not match the manifest's expected value", which reads like a broken seed
// and is what made an ops-side engine change look like an acr-side regression.
func withCountingContext(check probeCheck, shape tableShape) probeCheck {
	if check.OK || check.Message == "" {
		return check
	}
	check.Message += countingContext(shape)
	return check
}

func executeProbe(name, sql, expected string, probeWords []string) probeCheck {
	check := probeCheck{Name: name, SQLRedacted: redact(sql), Expected: expected}
	actual, err := runProbeCommand(probeWords, sql)
	if err != nil {
		check.Message = err.Error()
		return check
	}
	check.Actual = actual
	check.OK = actual == expected
	if !check.OK {
		check.Message = "probe result did not match the manifest's expected value"
	}
	return check
}

func runProbeCommand(probeWords []string, sql string) (string, error) {
	args := append(append([]string{}, probeWords[1:]...), sql)
	cmd := exec.Command(probeWords[0], args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("probe command failed: %w: %s", err, redact(strings.TrimSpace(stderr.String())))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func writeFixtureVerification(path string, report fixtureVerification) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode fixture-verification.json: %w", err)
	}
	data = redactBytes(data)
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// resolveProbeWords prefers the NUL-separated argv file. The caller's compose wrapper is a
// shell function, so the older shell-quoted form cannot express it: handing this tool the
// literal word "compose" produces "executable file not found in $PATH".
func resolveProbeWords(argvFile, quoted string) ([]string, error) {
	if argvFile != "" {
		raw, err := os.ReadFile(argvFile)
		if err != nil {
			return nil, fmt.Errorf("read probe argv: %w", err)
		}
		words := []string{}
		for _, part := range strings.Split(string(raw), "\x00") {
			if part != "" {
				words = append(words, part)
			}
		}
		if len(words) == 0 {
			return nil, fmt.Errorf("probe argv file held no arguments")
		}
		return words, nil
	}
	return splitShellWords(quoted)
}
