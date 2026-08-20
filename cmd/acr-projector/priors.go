package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgstructurepriors"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/structurepriorcuration"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
)

const priorsUsage = `Usage: acr-projector priors <curate|flip|rollback|revoke> [flags]

CHAOS-3977 P5 (pivot-intent design brief §3.2/§3.3, DP8(a)): the Bridge
prior store's operator surface. Every write here is a HUMAN-RATIFIED
operation -- ratified_by/revoked_by is required and never inferred; nothing
in this codebase's own production composition calls any of these paths
automatically.

  curate --org ID                     derive candidate priors from CHAOS-3927
                                       P4's own captured selections and
                                       publish them as a NEW, inactive
                                       version (never activates it)
  flip --org ID --version N --by WHO  CAS the org's active-version pointer to
                                       N (must already exist, e.g. from
                                       curate) -- the human-ratified promotion
  rollback --org ID --by WHO          CAS the org's active-version pointer
                                       back to its previous value
  revoke --org ID --entry ID --by WHO kill one entry, in every version that
                                       ever proposes it, present or future
`

func priorsCommand(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Print(priorsUsage)
		return err
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "curate":
		return priorsCurate(rest)
	case "flip":
		return priorsFlip(rest)
	case "rollback":
		return priorsRollback(rest)
	case "revoke":
		return priorsRevoke(rest)
	default:
		return fmt.Errorf("unknown priors subcommand %q; use curate, flip, rollback, or revoke", sub)
	}
}

// openPriorsDB opens a Postgres-only connection -- CHAOS-3977 P5's own
// store needs no ClickHouse or graph backend, unlike openRuntime's full
// stack (rebuild/rollback, main.go), so this is a deliberately lighter
// composition, not a reuse of that heavier path.
func openPriorsDB(ctx context.Context) (*sql.DB, error) {
	cfg, err := config.LoadProjector()
	if err != nil {
		return nil, fmt.Errorf("configuration: %w", err)
	}
	if strings.TrimSpace(cfg.PostgresDSN) == "" {
		return nil, errors.New("acr-projector priors requires ACR_POSTGRES_DSN")
	}
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{
		DSN: cfg.PostgresDSN, PoolerAdminDSN: cfg.PostgresPoolerAdminDSN,
		MaxOpenConns: cfg.PostgresMaxOpenConns, MaxIdleConns: cfg.PostgresMaxIdleConns, MaxIdleConnsSet: cfg.PostgresMaxIdleConnsConfigured,
		ConnMaxLifetime: cfg.PostgresConnMaxLifetime, ConnMaxIdleTime: cfg.PostgresConnMaxIdleTime, PingTimeout: cfg.PostgresPingTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	return db, nil
}

// frozenQuestionHashesFromEnv reads the operator-supplied frozen-corpus
// exclusion set (CHAOS-3977 P5's own HARD REQUIREMENT: frozen-corpus
// QuestionHashes never enter curation output). ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES
// is a comma-separated list of the corpus's own SHA-256 QuestionHash
// digests -- HASHES ONLY, never question text (the one-way hash reveals
// nothing about the withheld corpus material it names; see
// structurepriorcuration's own doc comment for the mechanism this feeds).
// Unset/empty means "no known frozen questions in this environment" --
// correct for any deployment that never replays the frozen corpus against
// the same Postgres curation reads from; an operator running curation
// somewhere the corpus replay COULD have landed rows must populate this
// from the corpus's own hash manifest first.
//
// Fails LOUDLY (codex adversarial review, medium finding) rather than
// silently on a malformed entry: a value that is not exactly 64 lowercase
// hex characters (QuestionHash's own shape, answer_reuse.go's
// hex.EncodeToString(sha256...)) can never match a real captured hash, so
// accepting it silently would look like exclusion is configured when it
// is actually a no-op for that entry.
func frozenQuestionHashesFromEnv() (map[string]bool, error) {
	raw := strings.TrimSpace(os.Getenv("ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES"))
	out := map[string]bool{}
	if raw == "" {
		return out, nil
	}
	for _, h := range strings.Split(raw, ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if !isLowercaseSHA256Hex(h) {
			return nil, fmt.Errorf("acr-projector priors: ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES entry %q is not a 64-character lowercase hex SHA-256 digest", h)
		}
		out[h] = true
	}
	return out, nil
}

func isLowercaseSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func priorsCurate(args []string) error {
	flags := flag.NewFlagSet("acr-projector priors curate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	org := flags.String("org", "", "organization ID to curate (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*org) == "" {
		return errors.New("acr-projector priors curate requires --org")
	}
	ctx := context.Background()
	db, err := openPriorsDB(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	store, err := pgstructurepriors.NewStore(db)
	if err != nil {
		return err
	}

	frozenHashes, err := frozenQuestionHashesFromEnv()
	if err != nil {
		return err
	}
	if len(frozenHashes) == 0 {
		// codex adversarial review, medium finding: make the omission
		// VISIBLE rather than silent -- an operator running curation
		// somewhere the frozen corpus replay could have landed rows, with
		// this variable unset, would otherwise get no signal that
		// exclusion is a no-op for this run.
		fmt.Fprintln(os.Stderr, "WARNING: ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES is unset or empty -- if this environment could contain frozen-corpus replay data, curation will NOT exclude it. Set the variable to the corpus's own QuestionHash manifest first if in doubt.")
	}

	events, err := structurepriorcuration.ReadSelections(ctx, db, *org)
	if err != nil {
		return err
	}
	entries := structurepriorcuration.Curate(events, frozenHashes)
	if len(entries) == 0 {
		_, err := fmt.Printf("org %s: curation found nothing promotable (0 candidate selections met the promotion rule)\n", *org)
		return err
	}
	version, err := store.PublishVersion(ctx, *org, entries, "", contextfabric.CurationRuleVersionV1)
	if err != nil {
		return fmt.Errorf("publish version: %w", err)
	}
	_, err = fmt.Printf("org %s: published version %d with %d entries (NOT active -- run `priors flip --org %s --version %d --by <you>` to promote it)\n", *org, version, len(entries), *org, version)
	return err
}

func priorsFlip(args []string) error {
	flags := flag.NewFlagSet("acr-projector priors flip", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	org := flags.String("org", "", "organization ID (required)")
	version := flags.Int64("version", 0, "target version, from `priors curate` (required)")
	expected := flags.Int64("expect-current", -1, "the CURRENT active version you observed (omit or -1 for none)")
	by := flags.String("by", "", "your identity -- DP8(a) requires a human-ratified actor (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*org) == "" || *version <= 0 || strings.TrimSpace(*by) == "" {
		return errors.New("acr-projector priors flip requires --org, --version, and --by")
	}
	ctx := context.Background()
	db, err := openPriorsDB(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	store, err := pgstructurepriors.NewStore(db)
	if err != nil {
		return err
	}

	var expectedPtr *int64
	if *expected >= 0 {
		expectedPtr = expected
	}
	target := *version
	if err := store.FlipActiveVersion(ctx, *org, expectedPtr, &target, *by); err != nil {
		return fmt.Errorf("flip: %w", err)
	}
	_, err = fmt.Printf("org %s: active version is now %d (ratified by %s)\n", *org, target, *by)
	return err
}

func priorsRollback(args []string) error {
	flags := flag.NewFlagSet("acr-projector priors rollback", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	org := flags.String("org", "", "organization ID (required)")
	by := flags.String("by", "", "your identity -- DP8(a) requires a human-ratified actor (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*org) == "" || strings.TrimSpace(*by) == "" {
		return errors.New("acr-projector priors rollback requires --org and --by")
	}
	ctx := context.Background()
	db, err := openPriorsDB(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	store, err := pgstructurepriors.NewStore(db)
	if err != nil {
		return err
	}

	if err := store.RollbackActiveVersion(ctx, *org, *by); err != nil {
		return fmt.Errorf("rollback: %w", err)
	}
	_, err = fmt.Printf("org %s: active version rolled back (ratified by %s)\n", *org, *by)
	return err
}

func priorsRevoke(args []string) error {
	flags := flag.NewFlagSet("acr-projector priors revoke", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	org := flags.String("org", "", "organization ID (required)")
	entry := flags.String("entry", "", "entry ID to revoke, in EVERY version (required)")
	by := flags.String("by", "", "your identity -- required for accountability (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*org) == "" || strings.TrimSpace(*entry) == "" || strings.TrimSpace(*by) == "" {
		return errors.New("acr-projector priors revoke requires --org, --entry, and --by")
	}
	ctx := context.Background()
	db, err := openPriorsDB(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	store, err := pgstructurepriors.NewStore(db)
	if err != nil {
		return err
	}

	if err := store.RevokeEntry(ctx, *org, *entry, *by); err != nil {
		return fmt.Errorf("revoke: %w", err)
	}
	_, err = fmt.Printf("org %s: entry %s revoked (by %s)\n", *org, *entry, *by)
	return err
}
