package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgstructurepriors"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/structurepriorcuration"
	"github.com/full-chaos/dev-health-acr/internal/runtime/hosted"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
  rollback --org ID --expect-current N --by WHO
                                       CAS the org's active-version pointer
                                       back to its previous value; N is the
                                       CURRENT active version you observed
  revoke --org ID --entry ID --by WHO kill one entry, in every version that
                                       ever proposes it, present or future

Every command holds the organization's projection lock until its write has
committed. While a projector pass or a purge holds that lock the command
writes nothing and says the organization is locked: run it again. A purged
organization is refused until it returns to discovery.
`

func priorsCommand(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Print(priorsUsage)
		return err
	}
	return runPriors(args[0], args[1:], openPriors)
}

func runPriors(sub string, rest []string, open priorsOpener) error {
	switch sub {
	case "curate":
		return priorsCurate(rest, open)
	case "flip":
		return priorsFlip(rest, open)
	case "rollback":
		return priorsRollback(rest, open)
	case "revoke":
		return priorsRevoke(rest, open)
	default:
		return fmt.Errorf("unknown priors subcommand %q; use curate, flip, rollback, or revoke", sub)
	}
}

// priorsOpener opens the database of one priors command and the guard its
// write runs under.
type priorsOpener func(ctx context.Context) (*sql.DB, priorsOrgGuard, error)

func openPriors(ctx context.Context) (*sql.DB, priorsOrgGuard, error) {
	db, err := openPriorsDB(ctx)
	if err != nil {
		return nil, priorsOrgGuard{}, err
	}
	locker, err := projectionrun.NewPostgresOrgLocker(db)
	if err != nil {
		_ = db.Close()
		return nil, priorsOrgGuard{}, err
	}
	markers, err := pgprojection.NewOrgPurgeMarkerStore(db)
	if err != nil {
		_ = db.Close()
		return nil, priorsOrgGuard{}, err
	}
	return db, priorsOrgGuard{Locker: locker, Markers: markers, Logger: slog.Default()}, nil
}

// priorsPurgeMarkers reads the organization's purge marker.
type priorsPurgeMarkers interface {
	IsPurged(ctx context.Context, orgID string) (bool, error)
}

// Outcomes of one priors command, as its Info line reports them.
const (
	priorsOutcomeWritten   = "written"
	priorsOutcomeUnchanged = "unchanged"
	priorsOutcomePurged    = "purged"
	priorsOutcomeLocked    = "locked"
	priorsOutcomeFailed    = "failed"
)

// priorsOrgGuard runs one priors write in the purge's lock order: the
// organization lock, then the purge marker read, then the write, whose own
// transaction takes the write fence lock through the trigger of migration
// 0052. The organization lock is held from before the marker read until the
// write has committed or failed. The purge takes the same lock before it
// deletes anything, so it can never run between the marker read and the write.
type priorsOrgGuard struct {
	Locker  projectionrun.OrgLocker
	Markers priorsPurgeMarkers
	Logger  *slog.Logger
}

// run returns an error that wraps projectionrun.ErrOrgLocked when another
// projector pass or a purge holds the organization lock, and one that wraps
// storage.ErrOrgPurged when the organization was purged. In both cases
// nothing was written. write reports whether it wrote a row.
func (g priorsOrgGuard) run(ctx context.Context, command, orgID string, write func(context.Context) (bool, error)) (err error) {
	logger := g.Logger
	if logger == nil {
		logger = slog.Default()
	}
	outcome := priorsOutcomeFailed
	defer func() {
		logger.InfoContext(ctx, "acr-projector priors command finished", "command", command, "org_id", contextfabric.SanitizeLogAttr(orgID), "outcome", outcome)
	}()
	if g.Locker == nil || g.Markers == nil {
		return errors.New("acr-projector priors: organization guard is not configured")
	}

	unlock, err := g.Locker.Lock(ctx, orgID)
	if errors.Is(err, projectionrun.ErrOrgLocked) {
		outcome = priorsOutcomeLocked
		logger.WarnContext(ctx, "acr-projector priors refused: organization is locked by a projector pass or a purge", "command", command, "org_id", contextfabric.SanitizeLogAttr(orgID), "failure_class", "org_locked")
		return fmt.Errorf("acr-projector priors %s: organization %s is locked by a projector pass or a purge; nothing was written, run the command again: %w", command, orgID, err)
	}
	if err != nil {
		return fmt.Errorf("acr-projector priors %s: take the organization lock: %w", command, err)
	}
	defer func() {
		if unlockErr := unlock(); unlockErr != nil {
			logger.WarnContext(ctx, "acr-projector priors: releasing the organization lock failed", "command", command, "failure_class", contextfabric.SanitizeLogAttr(projectionrun.ClassifyFailure(unlockErr)))
		}
	}()

	purged, err := g.Markers.IsPurged(ctx, orgID)
	if err != nil {
		return fmt.Errorf("acr-projector priors %s: read the purge marker: %w", command, err)
	}
	written := false
	if !purged {
		written, err = write(ctx)
	}
	if purged || errors.Is(err, storage.ErrOrgPurged) {
		outcome = priorsOutcomePurged
		logger.WarnContext(ctx, "acr-projector priors refused: organization was purged", "command", command, "org_id", contextfabric.SanitizeLogAttr(orgID), "failure_class", "org_purged")
		return fmt.Errorf("acr-projector priors %s: organization %s was purged; nothing is written for it until it returns to discovery: %w", command, orgID, storage.ErrOrgPurged)
	}
	if err != nil {
		return err
	}
	outcome = priorsOutcomeUnchanged
	if written {
		outcome = priorsOutcomeWritten
	}
	return nil
}

// openPriorsDB opens a Postgres-only connection -- CHAOS-3977 P5's own
// store needs no ClickHouse or graph backend, unlike openRuntime's full
// stack (rebuild/rollback, main.go), so this is a deliberately lighter
// composition, not a reuse of that heavier path.
func openPriorsDB(ctx context.Context) (*sql.DB, error) {
	// r2 P1 finding: priors is deliberately Postgres-only (see this
	// function's own doc comment above) -- config.LoadProjector() requires
	// the FULL backing-store set (Postgres AND ClickHouse) that
	// serve/rebuild/rollback actually need, which would refuse a
	// legitimately-configured Postgres-only priors deployment for a store
	// this command never opens.
	cfg, err := config.LoadProjectorPriors()
	if err != nil {
		return nil, fmt.Errorf("configuration: %w", err)
	}
	if strings.TrimSpace(cfg.PostgresDSN) == "" {
		return nil, errors.New("acr-projector priors requires ACR_POSTGRES_DSN")
	}
	db, err := hosted.OpenPostgresWithRetry(ctx, runtimepostgres.Config{
		DSN: cfg.PostgresDSN, PoolerAdminDSN: cfg.PostgresPoolerAdminDSN,
		MaxOpenConns: cfg.PostgresMaxOpenConns, MaxIdleConns: cfg.PostgresMaxIdleConns, MaxIdleConnsSet: cfg.PostgresMaxIdleConnsConfigured,
		ConnMaxLifetime: cfg.PostgresConnMaxLifetime, ConnMaxIdleTime: cfg.PostgresConnMaxIdleTime, PingTimeout: cfg.PostgresPingTimeout,
	}, cfg.PostgresStartupAttempts, cfg.PostgresStartupBackoff, slog.Default())
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

// latestCapturedAt returns the maximum CapturedAt across events, RFC3339
// nano-formatted, or "" for an empty slice -- PublishVersion's own
// created_from_watermark. See the field's own doc comment
// (structurepriorcuration.SelectionEvent) for what this does and does not
// prove.
func latestCapturedAt(events []structurepriorcuration.SelectionEvent) string {
	var max time.Time
	for _, e := range events {
		if e.CapturedAt.After(max) {
			max = e.CapturedAt
		}
	}
	if max.IsZero() {
		return ""
	}
	return max.UTC().Format(time.RFC3339Nano)
}

// requireFrozenQuestionHashes is priorsCurate's own fail-closed gate
// (codex adversarial review round 2, high finding, repro-confirmed and
// fixed): a warning an automated/scripted run can never see fails exactly
// the deployment mistake it exists to catch. Refuses (a hard error, not a
// warning) whenever ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES
// is unset/empty AND the operator has not passed --attest-no-frozen-corpus
// -- the explicit, logged, DP10-style human decision this HARD REQUIREMENT
// needs, never an inferred default. Factored out of priorsCurate so it is
// testable without a database (the check itself performs no I/O).
func requireFrozenQuestionHashes(attestNoFrozenCorpus bool) (map[string]bool, error) {
	frozenHashes, err := frozenQuestionHashesFromEnv()
	if err != nil {
		return nil, err
	}
	if len(frozenHashes) == 0 && !attestNoFrozenCorpus {
		return nil, errors.New("acr-projector priors curate: ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES is unset or empty -- refusing to publish an unguarded prior version. If this environment genuinely cannot contain frozen-corpus replay data, re-run with --attest-no-frozen-corpus; otherwise set the variable to the corpus's own QuestionHash manifest first")
	}
	if len(frozenHashes) == 0 {
		fmt.Fprintln(os.Stderr, "ATTESTED: proceeding with NO frozen-corpus exclusion, per --attest-no-frozen-corpus.")
	}
	return frozenHashes, nil
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

func priorsCurate(args []string, open priorsOpener) error {
	flags := flag.NewFlagSet("acr-projector priors curate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	org := flags.String("org", "", "organization ID to curate (required)")
	// attestNoFrozenCorpus (codex adversarial review round 2, high finding,
	// repro-confirmed and fixed): curation used to WARN and proceed when
	// ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES was unset
	// or empty -- a warning an automated/scripted run can never see fails
	// exactly the deployment mistake it exists to catch. The HARD
	// REQUIREMENT (frozen-corpus QuestionHashes never enter curation
	// output) is now enforced by REFUSING to run at all with an empty
	// exclusion set, unless the operator explicitly attests there is none
	// to exclude in this environment -- an explicit, logged, DP10-style
	// human decision, never an inferred default.
	attestNoFrozenCorpus := flags.Bool("attest-no-frozen-corpus", false, "required when ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES is unset/empty -- an explicit attestation that this environment cannot contain frozen-corpus replay data")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*org) == "" {
		return errors.New("acr-projector priors curate requires --org")
	}
	// Checked BEFORE any database connection is opened: cheap, and fails
	// fast on the deployment mistake this gate exists to catch rather than
	// spending a Postgres round trip first.
	frozenHashes, err := requireFrozenQuestionHashes(*attestNoFrozenCorpus)
	if err != nil {
		return err
	}

	ctx := context.Background()
	db, guard, err := open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	store, err := pgstructurepriors.NewStore(db)
	if err != nil {
		return err
	}
	return guard.run(ctx, "curate", *org, func(ctx context.Context) (bool, error) {
		return curateOrg(ctx, db, store, *org, frozenHashes)
	})
}

func curateOrg(ctx context.Context, db *sql.DB, store *pgstructurepriors.Store, org string, frozenHashes map[string]bool) (bool, error) {
	events, err := structurepriorcuration.ReadSelections(ctx, db, org)
	if err != nil {
		return false, err
	}
	entries := structurepriorcuration.Curate(events, frozenHashes)
	if len(entries) == 0 {
		_, err := fmt.Printf("org %s: curation found nothing promotable (0 candidate selections met the promotion rule)\n", org)
		return false, err
	}
	// watermark (codex adversarial review round 2, medium finding, repro-
	// confirmed and fixed): v1 still reads the org's FULL selection
	// history every run (structurepriorcuration's own doc comment covers
	// why an incremental cursor is a named v2 follow-on, not implemented
	// here) -- but nothing stopped this from stamping a MEANINGFUL
	// watermark for what it actually read, and an always-empty one
	// defeated the design brief's own "created_from event-log watermark"
	// reproducibility/audit purpose for every run, not just future
	// incremental ones. latestCapturedAt is the high-water mark of the
	// events THIS run actually curated from -- a real, checkable value,
	// even though it does not yet gate what a future run reads.
	watermark := latestCapturedAt(events)
	version, err := store.PublishVersion(ctx, org, entries, watermark, contextfabric.CurationRuleVersionV1)
	if err != nil {
		return false, fmt.Errorf("publish version: %w", err)
	}
	_, err = fmt.Printf("org %s: published version %d with %d entries, watermark=%s (NOT active -- run `priors flip --org %s --version %d --by <you>` to promote it)\n", org, version, len(entries), watermark, org, version)
	return true, err
}

func priorsFlip(args []string, open priorsOpener) error {
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
	db, guard, err := open(ctx)
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
	return guard.run(ctx, "flip", *org, func(ctx context.Context) (bool, error) {
		if err := store.FlipActiveVersion(ctx, *org, expectedPtr, &target, *by); err != nil {
			return false, fmt.Errorf("flip: %w", err)
		}
		_, err := fmt.Printf("org %s: active version is now %d (ratified by %s)\n", *org, target, *by)
		return true, err
	})
}

func priorsRollback(args []string, open priorsOpener) error {
	flags := flag.NewFlagSet("acr-projector priors rollback", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	org := flags.String("org", "", "organization ID (required)")
	expectCurrent := flags.Int64("expect-current", -1, "the CURRENT active version you observed (required) -- guards against a concurrent flip retargeting this rollback, or a retry silently reversing it")
	by := flags.String("by", "", "your identity -- DP8(a) requires a human-ratified actor (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*org) == "" || strings.TrimSpace(*by) == "" || *expectCurrent < 0 {
		return errors.New("acr-projector priors rollback requires --org, --expect-current, and --by")
	}
	ctx := context.Background()
	db, guard, err := open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	store, err := pgstructurepriors.NewStore(db)
	if err != nil {
		return err
	}

	expected := *expectCurrent
	return guard.run(ctx, "rollback", *org, func(ctx context.Context) (bool, error) {
		if err := store.RollbackActiveVersion(ctx, *org, &expected, *by); err != nil {
			return false, fmt.Errorf("rollback: %w", err)
		}
		_, err := fmt.Printf("org %s: active version rolled back from %d (ratified by %s)\n", *org, expected, *by)
		return true, err
	})
}

func priorsRevoke(args []string, open priorsOpener) error {
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
	db, guard, err := open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	store, err := pgstructurepriors.NewStore(db)
	if err != nil {
		return err
	}

	return guard.run(ctx, "revoke", *org, func(ctx context.Context) (bool, error) {
		if err := store.RevokeEntry(ctx, *org, *entry, *by); err != nil {
			return false, fmt.Errorf("revoke: %w", err)
		}
		_, err := fmt.Printf("org %s: entry %s revoked (by %s)\n", *org, *entry, *by)
		return true, err
	})
}
