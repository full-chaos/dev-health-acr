package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// runVerifySeedSchema is an offline, static check: it replays the ops ClickHouse migration
// directory (CREATE/ALTER/DROP TABLE only) into an effective schema and verifies every
// INSERT INTO statement in the fixture seed SQL references a table/column that actually
// exists in that schema, with the correct VALUES tuple arity. It exists because the seed
// once referenced an org_id column on six tables that never had one -- a mistake that would
// otherwise only surface after a ~20 minute Compose stack boot; this catches it in
// milliseconds and needs neither Docker nor ClickHouse.
func runVerifySeedSchema(args []string) int {
	fs := flag.NewFlagSet("verify-seed-schema", flag.ContinueOnError)
	seedDir := fs.String("seed-dir", "", "path to testdata/fullstack/v1/seed/clickhouse")
	migrationsDir := fs.String("migrations-dir", "", "path to ops/src/dev_health_ops/migrations/clickhouse")
	out := fs.String("out", "", "optional path to write a JSON report")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *seedDir == "" || *migrationsDir == "" {
		fmt.Fprintln(os.Stderr, "[assertrun] FAIL verify-seed-schema: --seed-dir and --migrations-dir are required")
		return 2
	}

	schema, unhandled, err := replayMigrationsDir(*migrationsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[assertrun] FAIL verify-seed-schema: %s\n", redact(err.Error()))
		return 1
	}

	seedFiles, err := seedSQLFiles(*seedDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[assertrun] FAIL verify-seed-schema: %s\n", redact(err.Error()))
		return 1
	}
	if len(seedFiles) == 0 {
		fmt.Fprintf(os.Stderr, "[assertrun] FAIL verify-seed-schema: no seed .sql files found in %s\n", *seedDir)
		return 1
	}

	var allIssues []seedIssue
	touched := newStringSet()
	for _, path := range seedFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[assertrun] FAIL verify-seed-schema: %s\n", redact(err.Error()))
			return 1
		}
		issues, err := verifySeedAgainstSchema(schema, string(data), filepath.Base(path))
		if err != nil {
			fmt.Fprintf(os.Stderr, "[assertrun] FAIL verify-seed-schema: %s\n", redact(err.Error()))
			return 1
		}
		allIssues = append(allIssues, issues...)
		touched.add(seedTouchedTables(string(data)).sorted()...)
	}

	// Unhandled DDL fails closed -- not just a warning -- when it touches a table the seed
	// actually writes to, or when the affected table could not even be determined at all
	// (Table == ""): either way this replay's picture of that table's columns may be wrong,
	// which is exactly the condition that let a bad seed pass before (Codex finding 12). A
	// note about a table the seed never inserts into stays a warning: it cannot affect this
	// run's verdict either way.
	var blocking []unhandledDDL
	for _, note := range unhandled {
		if note.Table == "" || touched.has(note.Table) {
			blocking = append(blocking, note)
			fmt.Fprintf(os.Stderr, "[assertrun] FAIL verify-seed-schema: unattributed DDL touches a seeded table: %s\n", redact(note.String()))
		} else {
			fmt.Fprintf(os.Stderr, "[assertrun] WARN verify-seed-schema: %s\n", redact(note.String()))
		}
	}

	if *out != "" {
		if err := writeSeedSchemaReport(*out, allIssues, unhandled, blocking); err != nil {
			fmt.Fprintf(os.Stderr, "[assertrun] FAIL verify-seed-schema: %s\n", redact(err.Error()))
			return 1
		}
	}

	if len(allIssues) > 0 {
		for _, issue := range allIssues {
			fmt.Fprintf(os.Stderr, "[assertrun] FAIL verify-seed-schema: %s\n", redact(issue.String()))
		}
	}
	if len(allIssues) > 0 || len(blocking) > 0 {
		return 1
	}
	return 0
}

func seedSQLFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read seed dir %s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

func writeSeedSchemaReport(path string, issues []seedIssue, unhandled, blocking []unhandledDDL) error {
	type reportIssue struct {
		File    string `json:"file"`
		Table   string `json:"table"`
		Column  string `json:"column,omitempty"`
		Tuple   int    `json:"tuple,omitempty"`
		Wanted  int    `json:"wanted_arity,omitempty"`
		Got     int    `json:"got_arity,omitempty"`
		Message string `json:"message"`
	}
	type reportUnhandled struct {
		File     string `json:"file"`
		Table    string `json:"table,omitempty"`
		Message  string `json:"message"`
		Blocking bool   `json:"blocking"`
	}
	blockingSet := map[unhandledDDL]bool{}
	for _, b := range blocking {
		blockingSet[b] = true
	}
	report := struct {
		SchemaVersion string            `json:"schema_version"`
		OK            bool              `json:"ok"`
		Issues        []reportIssue     `json:"issues"`
		Unhandled     []reportUnhandled `json:"unhandled_migration_ddl,omitempty"`
	}{SchemaVersion: "fullstack_seed_schema_verification.v1", OK: len(issues) == 0 && len(blocking) == 0}
	for _, i := range issues {
		report.Issues = append(report.Issues, reportIssue{
			File: i.File, Table: i.Table, Column: i.Column, Tuple: i.Tuple, Wanted: i.Wanted, Got: i.Got, Message: i.String(),
		})
	}
	for _, u := range unhandled {
		report.Unhandled = append(report.Unhandled, reportUnhandled{
			File: u.File, Table: u.Table, Message: u.Message, Blocking: blockingSet[u],
		})
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode seed schema report: %w", err)
	}
	data = redactBytes(data)
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
