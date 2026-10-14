// Package orgpurge removes every ACR-held trace of one organization that has
// left Dev Health: its FalkorDB graphs and its org-keyed Postgres rows.
package orgpurge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrRefused = errors.New("orgpurge: purge refused")
	ErrLocked  = errors.New("orgpurge: organization is locked by another projector")
	// ErrIncomplete means the run failed after it started removing data:
	// some graphs may be gone while database rows remain. Run it again.
	ErrIncomplete = errors.New("orgpurge: purge INCOMPLETE")
)

// OrgKey is the log-safe, non-reversible handle of an organization.
func OrgKey(orgID string) string {
	sum := sha256.Sum256([]byte("org-purge\x00" + orgID))
	return hex.EncodeToString(sum[:8])
}

// GraphDropper finds and removes the organization's graphs at every epoch,
// by scanning the graph keys that exist rather than counting epochs.
type GraphDropper interface {
	CountOrganizationGraphs(ctx context.Context, orgID string) (int, error)
	DeleteOrganizationGraphs(ctx context.Context, orgID string) (int, error)
}

// EffectiveSet is the projector's current organization set.
type EffectiveSet interface {
	RefreshOrgs(ctx context.Context) error
	EffectiveOrgs() []string
}

// Entitlements asks Dev Health whether the organization is gone. It returns
// true only on a positive unknown-organization answer; false with a nil error
// means Dev Health still answers for it, and an error means the answer is
// unavailable (outage, timeout, malformed id, an answer without the stable
// code). Only true lets a purge proceed.
type Entitlements interface {
	OrganizationUnknown(ctx context.Context, orgID string) (bool, error)
}

type Locker interface {
	Lock(ctx context.Context, orgID string) (func() error, error)
}

type Purger struct {
	DB           *sql.DB
	Graph        GraphDropper
	Set          EffectiveSet
	Entitlements Entitlements
	Locker       Locker
	Now          func() time.Time
	Logger       *slog.Logger
	// WriteFenceLockTimeout bounds the wait for the write fence. Zero means
	// DefaultWriteFenceLockTimeout.
	WriteFenceLockTimeout time.Duration
}

type Options struct {
	DryRun          bool
	ConfirmOrgKey   string
	DeleteBatchHint int
}

const (
	StatusDryRun     = "dry_run"
	StatusPurged     = "purged"
	StatusIncomplete = "incomplete"
)

// Report counts what a run removed (or, for a dry run, would remove).
type Report struct {
	OrgKey            string           `json:"org_key"`
	Status            string           `json:"status"`
	DryRun            bool             `json:"dry_run"`
	Graphs            int              `json:"graphs"`
	Rows              map[string]int64 `json:"rows"`
	TotalRows         int64            `json:"total_rows"`
	KeptAuditEvents   int64            `json:"kept_audit_events"`
	KeptLegalHold     int64            `json:"kept_legal_hold_episodes"`
	AuditRowWritten   bool             `json:"audit_row_written"`
	TableOrderApplied []string         `json:"-"`
}

type tableSpec struct {
	Name string
	// Where is the predicate over $1 (the organization).
	Where string
	// Cascaded child tables keyed by device_code_hash, counted before the
	// parent delete because the database removes them with it.
	CascadedByDeviceCode []string
}

// purgeTables is the deletion order: dependents before the rows they point
// at. device_authorizations goes first (it references client_credentials and
// cascades to the OAuth rows); client_credentials goes before
// workload_bindings (its binding FK is ON DELETE RESTRICT).
var purgeTables = []tableSpec{
	{Name: "device_authorizations", Where: `authorized_org_id = $1::uuid
		OR redeemed_credential_id IN (SELECT credential_id FROM acr.client_credentials WHERE org_id = $1::uuid)
		OR ` + matchOrg("organization_id_hint") + `
		OR device_code_hash IN (SELECT device_code_hash FROM acr.oauth_authorization_requests WHERE ` + matchOrg("bound_org_id") + `)`,
		CascadedByDeviceCode: []string{"oauth_authorization_requests", "oauth_device_grants"}},
	{Name: "client_credentials", Where: `org_id = $1::uuid`},
	{Name: "workload_bindings", Where: `org_id = $1::uuid`},
	{Name: "context_packet_snapshots", Where: `org_id = $1::uuid`},
	{Name: "agent_episodes", Where: `org_id = $1::uuid AND retention_class <> 'legal_hold'`},
	{Name: "context_fabric_projection_checkpoints", Where: matchOrg("org_id")},
	{Name: "context_fabric_projection_rebuild_markers", Where: matchOrg("org_id")},
	{Name: "context_fabric_investigation_results", Where: matchOrg("org_id")},
	{Name: "context_fabric_model_execution_receipts", Where: matchOrg("org_id")},
	{Name: "context_fabric_org_model_config", Where: matchOrg("org_id")},
	{Name: "context_fabric_reuse_invalidations", Where: matchOrg("org_id")},
	{Name: "context_fabric_clarification_selections", Where: matchOrg("org_id")},
	{Name: "context_fabric_structure_selections", Where: matchOrg("org_id")},
	{Name: "context_fabric_structure_supersession_claims", Where: matchOrg("org_id")},
	{Name: "context_fabric_structure_prior_pointer_history", Where: matchOrg("org_id")},
	{Name: "context_fabric_structure_prior_pointer", Where: matchOrg("org_id")},
	{Name: "context_fabric_structure_prior_revocations", Where: matchOrg("org_id")},
	{Name: "context_fabric_structure_priors", Where: matchOrg("org_id")},
	{Name: "context_fabric_graph_build_source_progress", Where: matchOrg("org_id")},
	{Name: "context_fabric_graph_epoch_retirements", Where: matchOrg("org_id")},
	{Name: "context_fabric_graph_lifecycle", Where: matchOrg("org_id")},
}

// PurgedTables and KeptTables name every table the purge handles, for the
// schema guard test.
func PurgedTables() []string {
	names := make([]string, 0, len(purgeTables))
	for _, spec := range purgeTables {
		names = append(names, spec.Name)
	}
	return names
}

// KeptTables are org-keyed tables the purge never deletes: the audit trail,
// and the purge marker, which is the fence that outlives the purge.
func KeptTables() []string {
	return []string{"audit_events", "context_fabric_org_purge_markers"}
}

// CascadedTables are org-keyed tables whose rows go with their
// device_authorizations parent (ON DELETE CASCADE).
func CascadedTables() []string {
	return []string{"oauth_authorization_requests", "oauth_device_grants"}
}

// Run guards, then removes the organization's graphs and rows.
func (p *Purger) Run(ctx context.Context, orgID string, opts Options) (Report, error) {
	report := Report{DryRun: opts.DryRun, Rows: map[string]int64{}}
	if p == nil || p.DB == nil || p.Graph == nil || p.Set == nil || p.Entitlements == nil || p.Locker == nil {
		return report, errors.New("orgpurge: purger is not fully configured")
	}
	var err error
	var parsed uuid.UUID
	parsed, err = uuid.Parse(strings.TrimSpace(orgID))
	if err != nil {
		return report, fmt.Errorf("%w: organization id is not a UUID", ErrRefused)
	}
	// Every comparison below (projector set, lock, graph key, text-keyed
	// rows) is exact-string, so only the canonical lowercase spelling is used.
	orgID = parsed.String()
	report.OrgKey = OrgKey(orgID)
	if !opts.DryRun {
		if opts.ConfirmOrgKey != report.OrgKey {
			return report, fmt.Errorf("%w: --confirm-hash does not match the organization key (run --dry-run to read it)", ErrRefused)
		}
	}
	if err := p.Set.RefreshOrgs(ctx); err != nil {
		return report, fmt.Errorf("%w: cannot prove the organization is outside the projector set: discovery failed", ErrRefused)
	}
	for _, member := range p.Set.EffectiveOrgs() {
		if member == orgID || sameUUID(member, orgID) {
			return report, fmt.Errorf("%w: organization is in the projector set (static list or discovery)", ErrRefused)
		}
	}
	unknown, err := p.Entitlements.OrganizationUnknown(ctx, orgID)
	if err != nil {
		return report, fmt.Errorf("%w: cannot prove Dev Health no longer knows this organization: Dev Health is unavailable or gave no positive answer", ErrRefused)
	}
	if !unknown {
		return report, fmt.Errorf("%w: Dev Health still answers for this organization", ErrRefused)
	}

	unlock, err := p.Locker.Lock(ctx, orgID)
	if err != nil {
		return report, fmt.Errorf("%w: %v", ErrLocked, err)
	}
	defer func() { _ = unlock() }()

	if err := p.removeOrganization(ctx, orgID, opts, &report); err != nil {
		if opts.DryRun {
			return report, err
		}
		report.Status = StatusIncomplete
		report.Rows, report.TotalRows, report.TableOrderApplied = map[string]int64{}, 0, nil // the rolled-back transaction removed nothing
		report.AuditRowWritten = p.writeIncompleteAudit(ctx, orgID, report)
		return report, fmt.Errorf("%w: graphs may be gone and database rows may remain; run it again: %w", ErrIncomplete, err)
	}
	report.Status = StatusPurged
	if opts.DryRun {
		report.Status = StatusDryRun
	}
	return report, nil
}

// sameUUID reports whether two strings are the same UUID in any spelling
// uuid.Parse accepts.
func sameUUID(a, b string) bool {
	left, err := uuid.Parse(strings.TrimSpace(a))
	if err != nil {
		return false
	}
	right, err := uuid.Parse(strings.TrimSpace(b))
	return err == nil && left == right
}

// matchOrg is the predicate for a TEXT column that holds an organization id
// in any UUID spelling: case, hyphens, braces and a urn:uuid: prefix are
// ignored. $1 is the canonical UUID.
func matchOrg(column string) string {
	return "lower(regexp_replace(" + column + ", '^urn:uuid:|[{}-]', '', 'gi')) = replace($1::uuid::text, '-', '')"
}

func (p *Purger) removeOrganization(ctx context.Context, orgID string, opts Options, report *Report) error {
	var err error
	if opts.DryRun {
		report.Graphs, err = p.Graph.CountOrganizationGraphs(ctx, orgID)
	} else {
		report.Graphs, err = p.Graph.DeleteOrganizationGraphs(ctx, orgID)
	}
	if err != nil {
		return fmt.Errorf("organization graphs: %w", err)
	}

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin purge transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if !opts.DryRun {
		if err := p.lockWriteFence(ctx, tx, orgID); err != nil {
			return err
		}
	}
	for _, spec := range purgeTables {
		var n int64
		for _, child := range spec.CascadedByDeviceCode {
			var c int64
			err = tx.QueryRowContext(ctx, "SELECT count(*) FROM acr."+child+" WHERE device_code_hash IN (SELECT device_code_hash FROM acr."+spec.Name+" WHERE "+spec.Where+")", orgID).Scan(&c)
			if err != nil {
				return fmt.Errorf("count %s: %w", child, err)
			}
			report.Rows[child] = c
			report.TotalRows += c
		}
		if opts.DryRun {
			err = tx.QueryRowContext(ctx, "SELECT count(*) FROM acr."+spec.Name+" WHERE "+spec.Where, orgID).Scan(&n)
		} else {
			var res sql.Result
			res, err = tx.ExecContext(ctx, "DELETE FROM acr."+spec.Name+" WHERE "+spec.Where, orgID)
			if err == nil {
				n, err = res.RowsAffected()
			}
		}
		if err != nil {
			return fmt.Errorf("purge %s: %w", spec.Name, err)
		}
		report.Rows[spec.Name] = n
		report.TotalRows += n
		report.TableOrderApplied = append(report.TableOrderApplied, spec.Name)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM acr.audit_events WHERE org_id = $1::uuid`, orgID).Scan(&report.KeptAuditEvents); err != nil {
		return fmt.Errorf("count audit events: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM acr.agent_episodes WHERE org_id = $1::uuid AND retention_class = 'legal_hold'`, orgID).Scan(&report.KeptLegalHold); err != nil {
		return fmt.Errorf("count legal hold episodes: %w", err)
	}
	if opts.DryRun {
		return nil
	}
	if report.TotalRows > 0 || report.Graphs > 0 {
		if err := p.writeAudit(ctx, tx, orgID, *report); err != nil {
			return err
		}
		report.AuditRowWritten = true
	}
	if err := p.writePurgeMarker(ctx, tx, orgID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit purge transaction: %w", err)
	}
	p.logger().InfoContext(ctx, "projection organization purge marker set", "org_key", contextfabric.SanitizeLogAttr(report.OrgKey))
	return nil
}

// WriteFenceLockClass is the advisory lock class of the organization write
// fence. The trigger function of migration 0052 takes the same class in
// shared mode before it reads the purge marker.
const WriteFenceLockClass = 20261014

// DefaultWriteFenceLockTimeout bounds the wait for writers that are in flight
// when the purge starts.
const DefaultWriteFenceLockTimeout = 30 * time.Second

// ErrWriteFenceTimeout means writers of the organization held the write fence
// for longer than the timeout. Nothing was deleted from the database.
var ErrWriteFenceTimeout = errors.New("orgpurge: timed out waiting for in-flight writers of the organization")

// lockWriteFence takes the organization's write fence in exclusive mode for
// the rest of the delete transaction, before any delete. It waits for writers
// already in flight and stops new ones until the marker is committed. The
// purge never continues without the lock.
func (p *Purger) lockWriteFence(ctx context.Context, tx *sql.Tx, orgID string) error {
	timeout := p.WriteFenceLockTimeout
	if timeout <= 0 {
		timeout = DefaultWriteFenceLockTimeout
	}
	var previous string
	if err := tx.QueryRowContext(ctx, `SELECT current_setting('lock_timeout')`).Scan(&previous); err != nil {
		return fmt.Errorf("read lock timeout: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('lock_timeout', $1, true)`, fmt.Sprintf("%dms", timeout.Milliseconds())); err != nil {
		return fmt.Errorf("set write fence lock timeout: %w", err)
	}
	started := time.Now()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, WriteFenceLockClass, orgID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			p.logger().ErrorContext(ctx, "projection organization purge stopped: write fence lock timed out", "org_key", contextfabric.SanitizeLogAttr(OrgKey(orgID)), "waited_ms", time.Since(started).Milliseconds())
			return fmt.Errorf("%w (waited %s)", ErrWriteFenceTimeout, timeout)
		}
		return fmt.Errorf("take write fence lock: %w", err)
	}
	p.logger().DebugContext(ctx, "projection organization purge took the write fence", "org_key", contextfabric.SanitizeLogAttr(OrgKey(orgID)), "waited_ms", time.Since(started).Milliseconds())
	if _, err := tx.ExecContext(ctx, `SELECT set_config('lock_timeout', $1, true)`, previous); err != nil {
		return fmt.Errorf("restore lock timeout: %w", err)
	}
	return nil
}

func (p *Purger) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// writePurgeMarker records the fence in the purge's own transaction: either
// the rows are gone and the marker is set, or neither happened. The marker
// is the canonical lowercase UUID, which is what the coordinator checks, and
// its time is the database clock, the one a discovery compares it with.
func (p *Purger) writePurgeMarker(ctx context.Context, tx *sql.Tx, orgID string) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO acr.context_fabric_org_purge_markers (org_id, purged_at)
VALUES ($1, clock_timestamp())
ON CONFLICT (org_id) DO UPDATE SET purged_at = EXCLUDED.purged_at`, orgID)
	if err != nil {
		return fmt.Errorf("write purge marker: %w", err)
	}
	return nil
}

func (p *Purger) writeAudit(ctx context.Context, tx *sql.Tx, orgID string, report Report) error {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	metadata, err := json.Marshal(map[string]any{"rows": report.Rows, "graphs": report.Graphs, "kept_legal_hold_episodes": report.KeptLegalHold})
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO acr.audit_events (audit_event_id, org_id, actor_type, actor_id, action, resource_type, resource_id, status, metadata, created_at)
VALUES ($1::uuid, $2::uuid, 'operator', 'acr-projector', 'org_purged', 'organization', $3, 'success', $4::jsonb, $5)`,
		uuid.NewString(), orgID, report.OrgKey, string(metadata), now().UTC())
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

// writeIncompleteAudit records a failed run on its own statement, outside the
// rolled-back transaction, so the attempt is visible. Best effort.
func (p *Purger) writeIncompleteAudit(ctx context.Context, orgID string, report Report) bool {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	metadata, err := json.Marshal(map[string]any{"graphs": report.Graphs})
	if err != nil {
		return false
	}
	_, err = p.DB.ExecContext(context.WithoutCancel(ctx), `
INSERT INTO acr.audit_events (audit_event_id, org_id, actor_type, actor_id, action, resource_type, resource_id, status, metadata, created_at)
VALUES ($1::uuid, $2::uuid, 'operator', 'acr-projector', 'org_purge_incomplete', 'organization', $3, 'failure', $4::jsonb, $5)`,
		uuid.NewString(), orgID, report.OrgKey, string(metadata), now().UTC())
	return err == nil
}
