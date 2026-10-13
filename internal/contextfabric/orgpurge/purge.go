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
	"strings"
	"time"

	"github.com/google/uuid"
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
	AllowsOrg(orgID string) bool
}

// Entitlements is the Dev Health entitlement lookup. A nil error means Dev
// Health answered 200 for the organization, so it still exists there.
type Entitlements interface {
	HasEntitlement(ctx context.Context, orgID, entitlement string) (bool, error)
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
}

type Options struct {
	DryRun          bool
	AttestDeleted   bool
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
		OR lower(organization_id_hint) = $1::uuid::text
		OR device_code_hash IN (SELECT device_code_hash FROM acr.oauth_authorization_requests WHERE bound_org_id = $1::uuid::text)`,
		CascadedByDeviceCode: []string{"oauth_authorization_requests", "oauth_device_grants"}},
	{Name: "client_credentials", Where: `org_id = $1::uuid`},
	{Name: "workload_bindings", Where: `org_id = $1::uuid`},
	{Name: "context_packet_snapshots", Where: `org_id = $1::uuid`},
	{Name: "agent_episodes", Where: `org_id = $1::uuid AND retention_class <> 'legal_hold'`},
	{Name: "context_fabric_projection_checkpoints", Where: `org_id = $1`},
	{Name: "context_fabric_projection_rebuild_markers", Where: `org_id = $1`},
	{Name: "context_fabric_investigation_results", Where: `org_id = $1`},
	{Name: "context_fabric_model_execution_receipts", Where: `org_id = $1`},
	{Name: "context_fabric_org_model_config", Where: `org_id = $1`},
	{Name: "context_fabric_reuse_invalidations", Where: `org_id = $1`},
	{Name: "context_fabric_clarification_selections", Where: `org_id = $1`},
	{Name: "context_fabric_structure_selections", Where: `org_id = $1`},
	{Name: "context_fabric_structure_supersession_claims", Where: `org_id = $1`},
	{Name: "context_fabric_structure_prior_pointer_history", Where: `org_id = $1`},
	{Name: "context_fabric_structure_prior_pointer", Where: `org_id = $1`},
	{Name: "context_fabric_structure_prior_revocations", Where: `org_id = $1`},
	{Name: "context_fabric_structure_priors", Where: `org_id = $1`},
	{Name: "context_fabric_graph_build_source_progress", Where: `org_id = $1`},
	{Name: "context_fabric_graph_epoch_retirements", Where: `org_id = $1`},
	{Name: "context_fabric_graph_lifecycle", Where: `org_id = $1`},
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

func KeptTables() []string { return []string{"audit_events"} }

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
		if !opts.AttestDeleted {
			return report, fmt.Errorf("%w: --attest-deleted is required", ErrRefused)
		}
		if opts.ConfirmOrgKey != report.OrgKey {
			return report, fmt.Errorf("%w: --confirm-hash does not match the organization key (run --dry-run to read it)", ErrRefused)
		}
	}
	if err := p.Set.RefreshOrgs(ctx); err != nil {
		return report, fmt.Errorf("%w: cannot prove the organization is outside the projector set: discovery failed", ErrRefused)
	}
	if p.Set.AllowsOrg(orgID) {
		return report, fmt.Errorf("%w: organization is in the projector set (static list or discovery)", ErrRefused)
	}
	if _, err := p.Entitlements.HasEntitlement(ctx, orgID, "agent_context_runtime"); err == nil {
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
		return report, fmt.Errorf("%w: graphs may be gone and database rows may remain; run it again: %v", ErrIncomplete, err)
	}
	report.Status = StatusPurged
	if opts.DryRun {
		report.Status = StatusDryRun
	}
	return report, nil
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
	if report.TotalRows > 0 {
		if err := p.writeAudit(ctx, tx, orgID, *report); err != nil {
			return err
		}
		report.AuditRowWritten = true
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit purge transaction: %w", err)
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
