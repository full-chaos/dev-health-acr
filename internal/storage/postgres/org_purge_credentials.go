package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage/internal/credentiallifecycle"
)

// credentialColumnList is the column list scanCredential reads.
const credentialColumnList = "credential_id, name, token_prefix, org_id, repository_scopes, scopes, created_at, expires_at, revoked_at, last_used_at, workload_binding_id, resource, client_class, approving_user_id, client_id, session_id, family_id, family_expires_at"

// The statements the organization purge issues against the credential rows and
// the lifecycle outbox, exported so the purge's privilege set is derived from
// the text it executes.
const (
	// OrgPurgeCredentialsDeleteSQL takes the organization's credentials with
	// the DELETE that holds their row locks: it waits for a revoke in flight
	// and returns the row as that revoke committed it.
	OrgPurgeCredentialsDeleteSQL = "DELETE FROM acr.client_credentials WHERE org_id = $1::uuid RETURNING " + credentialColumnList
	// OrgPurgeCredentialsSelectSQL is the dry run's read of the same rows.
	OrgPurgeCredentialsSelectSQL = "SELECT " + credentialColumnList + " FROM acr.client_credentials WHERE org_id = $1::uuid"
	// LifecycleOutboxMaxRevisionSQL reads an aggregate's highest revision.
	LifecycleOutboxMaxRevisionSQL = "SELECT COALESCE(MAX(revision), 0) FROM acr.credential_lifecycle_outbox WHERE aggregate_id = $1"
	// LifecycleOutboxInsertSQL appends one outbox row.
	LifecycleOutboxInsertSQL = `INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at)
VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::jsonb, $7)`
)

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// CredentialLiveAt is the one live predicate of the organization purge: a
// credential not revoked and not past its expiry at now. A terminal credential
// gets no purge event, and an unrevoked expired one stays non-live and
// non-terminal at the consumer (the consumer's Live() refuses it).
func CredentialLiveAt(c contractsv1.ClientCredential, now time.Time) bool {
	return c.RevokedAt == nil && (c.ExpiresAt == nil || c.ExpiresAt.After(now))
}

// PurgeOrgCredentials deletes every credential of the organization and, in the
// same transaction, appends a revoked row (actor system, no reference, first
// touch included) for each deleted credential that was live at now. It returns
// the number of credentials deleted and of events written. A failed outbox
// write returns the error: the caller's transaction rolls the delete back.
func PurgeOrgCredentials(ctx context.Context, tx *sql.Tx, orgID string, now time.Time, newID func() (string, error)) (deleted int64, events int, err error) {
	rows, err := readCredentials(ctx, tx, OrgPurgeCredentialsDeleteSQL, orgID)
	if err != nil {
		return 0, 0, fmt.Errorf("purge credentials: %w", err)
	}
	events, err = appendLiveRevocations(ctx, lifecycleSink{tx: tx, newID: newID}, rows, now)
	if err != nil {
		return 0, 0, fmt.Errorf("record credential lifecycle events: %w", err)
	}
	return int64(len(rows)), events, nil
}

// CountOrgCredentials is the dry run: the organization's credentials and how
// many the purge would write an event for, by the same predicate, with no write.
func CountOrgCredentials(ctx context.Context, q queryer, orgID string, now time.Time) (credentials int64, events int, err error) {
	rows, err := readCredentials(ctx, q, OrgPurgeCredentialsSelectSQL, orgID)
	if err != nil {
		return 0, 0, fmt.Errorf("count credentials: %w", err)
	}
	for _, row := range rows {
		if CredentialLiveAt(row, now) {
			events++
		}
	}
	return int64(len(rows)), events, nil
}

func readCredentials(ctx context.Context, q queryer, statement, orgID string) ([]contractsv1.ClientCredential, error) {
	result, err := q.QueryContext(ctx, statement, orgID)
	if err != nil {
		return nil, sanitizeDatabaseError(err)
	}
	defer result.Close()
	var rows []contractsv1.ClientCredential
	for result.Next() {
		credential, err := scanCredential(result)
		if err != nil {
			return nil, sanitizeDatabaseError(err)
		}
		rows = append(rows, credential)
	}
	if err := result.Err(); err != nil {
		return nil, sanitizeDatabaseError(err)
	}
	return rows, nil
}

// appendLiveRevocations writes, in credential id order, one revoked event for
// each live row (the writer adds its revision 1 first when the stream has none).
func appendLiveRevocations(ctx context.Context, sink credentiallifecycle.Sink, rows []contractsv1.ClientCredential, now time.Time) (int, error) {
	ordered := slices.Clone(rows)
	slices.SortFunc(ordered, func(a, b contractsv1.ClientCredential) int { return strings.Compare(a.CredentialID, b.CredentialID) })
	written := 0
	for _, row := range ordered {
		if !CredentialLiveAt(row, now) {
			continue
		}
		facts := credentiallifecycle.Facts{Kind: credentiallifecycle.KindRevoked, Credential: row, At: now, Actor: credentiallifecycle.Actor{Type: credentiallifecycle.ActorSystem}}
		if err := credentiallifecycle.Append(ctx, sink, facts); err != nil {
			return 0, err
		}
		written++
	}
	return written, nil
}
