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

const maxGraphEpochs = 4096

var (
	ErrRefused = errors.New("orgpurge: purge refused")
	ErrLocked  = errors.New("orgpurge: organization is locked by another projector")
)

// OrgKey is the log-safe, non-reversible handle of an organization.
func OrgKey(orgID string) string {
	sum := sha256.Sum256([]byte("org-purge\x00" + orgID))
	return hex.EncodeToString(sum[:8])
}

type GraphDropper interface {
	DeleteEpochGraph(ctx context.Context, orgID string, epoch, activeEpoch int64) error
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

// Report counts what a run removed (or, for a dry run, would remove).
type Report struct {
	OrgKey            string           `json:"org_key"`
	DryRun            bool             `json:"dry_run"`
	GraphEpochs       int              `json:"graph_epochs_targeted"`
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
}

// purgeTables is the deletion order: dependents before the rows they point
// at. device_authorizations goes first (it references client_credentials and
// cascades to the OAuth rows); client_credentials goes before
// workload_bindings (its binding FK is ON DELETE RESTRICT).
var purgeTables = []tableSpec{
	{"device_authorizations", `authorized_org_id = $1::uuid
		OR redeemed_credential_id IN (SELECT credential_id FROM acr.client_credentials WHERE org_id = $1::uuid)
		OR device_code_hash IN (SELECT device_code_hash FROM acr.oauth_authorization_requests WHERE bound_org_id = $1::uuid::text)`},
	{"client_credentials", `org_id = $1::uuid`},
	{"workload_bindings", `org_id = $1::uuid`},
	{"context_packet_snapshots", `org_id = $1::uuid`},
	{"agent_episodes", `org_id = $1::uuid AND retention_class <> 'legal_hold'`},
	{"context_fabric_projection_checkpoints", `org_id = $1`},
	{"context_fabric_projection_rebuild_markers", `org_id = $1`},
	{"context_fabric_investigation_results", `org_id = $1`},
	{"context_fabric_model_execution_receipts", `org_id = $1`},
	{"context_fabric_org_model_config", `org_id = $1`},
	{"context_fabric_reuse_invalidations", `org_id = $1`},
	{"context_fabric_clarification_selections", `org_id = $1`},
	{"context_fabric_structure_selections", `org_id = $1`},
	{"context_fabric_structure_supersession_claims", `org_id = $1`},
	{"context_fabric_structure_prior_pointer_history", `org_id = $1`},
	{"context_fabric_structure_prior_pointer", `org_id = $1`},
	{"context_fabric_structure_prior_revocations", `org_id = $1`},
	{"context_fabric_structure_priors", `org_id = $1`},
	{"context_fabric_graph_build_source_progress", `org_id = $1`},
	{"context_fabric_graph_epoch_retirements", `org_id = $1`},
	{"context_fabric_graph_lifecycle", `org_id = $1`},
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
func CascadedTables() []string { return []string{"oauth_authorization_requests"} }

// Run guards, then removes the organization's graphs and rows.
func (p *Purger) Run(ctx context.Context, orgID string, opts Options) (Report, error) {
	report := Report{OrgKey: OrgKey(orgID), DryRun: opts.DryRun, Rows: map[string]int64{}}
	if p == nil || p.DB == nil || p.Graph == nil || p.Set == nil || p.Entitlements == nil || p.Locker == nil {
		return report, errors.New("orgpurge: purger is not fully configured")
	}
	orgID = strings.TrimSpace(orgID)
	if _, err := uuid.Parse(orgID); err != nil {
		return report, fmt.Errorf("%w: organization id is not a UUID", ErrRefused)
	}
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

	maxEpoch, err := p.maxEpoch(ctx, orgID)
	if err != nil {
		return report, err
	}
	report.GraphEpochs = int(maxEpoch) + 1

	if !opts.DryRun {
		for epoch := int64(0); epoch <= maxEpoch; epoch++ {
			if err := p.Graph.DeleteEpochGraph(ctx, orgID, epoch, -1); err != nil {
				return report, fmt.Errorf("drop graph epoch %d: %w", epoch, err)
			}
		}
	}

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("begin purge transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, spec := range purgeTables {
		var n int64
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
			return report, fmt.Errorf("purge %s: %w", spec.Name, err)
		}
		report.Rows[spec.Name] = n
		report.TotalRows += n
		report.TableOrderApplied = append(report.TableOrderApplied, spec.Name)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM acr.audit_events WHERE org_id = $1::uuid`, orgID).Scan(&report.KeptAuditEvents); err != nil {
		return report, fmt.Errorf("count audit events: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM acr.agent_episodes WHERE org_id = $1::uuid AND retention_class = 'legal_hold'`, orgID).Scan(&report.KeptLegalHold); err != nil {
		return report, fmt.Errorf("count legal hold episodes: %w", err)
	}
	if opts.DryRun {
		return report, nil
	}
	if report.TotalRows > 0 {
		if err := p.writeAudit(ctx, tx, orgID, report); err != nil {
			return report, err
		}
		report.AuditRowWritten = true
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit purge transaction: %w", err)
	}
	return report, nil
}

func (p *Purger) maxEpoch(ctx context.Context, orgID string) (int64, error) {
	var maxEpoch int64
	err := p.DB.QueryRowContext(ctx, `
SELECT GREATEST(
  COALESCE((SELECT last_allocated_epoch FROM acr.context_fabric_graph_lifecycle WHERE org_id = $1), 0),
  COALESCE((SELECT max(epoch) FROM acr.context_fabric_graph_epoch_retirements WHERE org_id = $1), 0),
  COALESCE((SELECT max(epoch) FROM acr.context_fabric_graph_build_source_progress WHERE org_id = $1), 0))`, orgID).Scan(&maxEpoch)
	if err != nil {
		return 0, fmt.Errorf("read graph epochs: %w", err)
	}
	if maxEpoch < 0 || maxEpoch >= maxGraphEpochs {
		return 0, fmt.Errorf("%w: graph epoch count out of range", ErrRefused)
	}
	return maxEpoch, nil
}

func (p *Purger) writeAudit(ctx context.Context, tx *sql.Tx, orgID string, report Report) error {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	metadata, err := json.Marshal(map[string]any{"rows": report.Rows, "graph_epochs": report.GraphEpochs, "kept_legal_hold_episodes": report.KeptLegalHold})
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
