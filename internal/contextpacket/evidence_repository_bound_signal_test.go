package contextpacket_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type boundRepositoryClient struct{ repositories int }

func (c boundRepositoryClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return &boundRepositoryRows{total: c.repositories}, nil
}

type boundRepositoryRows struct{ total, next int }

func (r *boundRepositoryRows) Next() bool { return r.next < r.total }

func (r *boundRepositoryRows) Scan(destinations ...any) error {
	if len(destinations) != 2 {
		return errors.New("bound fixture scan mismatch")
	}
	*destinations[0].(*string) = boundRepoID(r.next)
	*destinations[1].(*string) = boundRepoSlug(r.next)
	r.next++
	return nil
}

func (r *boundRepositoryRows) Err() error   { return nil }
func (r *boundRepositoryRows) Close() error { return nil }

func boundRepoID(index int) string   { return fmt.Sprintf("bound-repo-id-%05d", index) }
func boundRepoSlug(index int) string { return fmt.Sprintf("bound-owner/bound-repo-%05d", index) }

// An organization at the evidence repository bound is answered generic not
// found and leaves a WARN with org and count; no repository id or slug.
func TestAuthorizedRepositoriesWarnsAtTheBoundWithoutRepositoryIDs(t *testing.T) {
	const orgID = "org-bound-signal"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	under := contextpacket.MaxEvidenceCandidateRepos
	if got, err := contextpacket.NewCatalogClickHouseRows(boundRepositoryClient{repositories: under}).AuthorizedRepositories(context.Background(), orgID, []string{"*"}); err != nil || len(got) != under {
		t.Fatalf("at the bound: repositories = %d, error = %v", len(got), err)
	}
	if logs.Len() != 0 {
		t.Fatalf("at the bound: logged %q, want nothing", logs.String())
	}

	over := contextpacket.MaxEvidenceCandidateRepos + 1
	_, err := contextpacket.NewCatalogClickHouseRows(boundRepositoryClient{repositories: over}).AuthorizedRepositories(context.Background(), orgID, []string{"*"})
	if !errors.Is(err, storage.ErrNotFound) || err.Error() != storage.ErrNotFound.Error() {
		t.Fatalf("above the bound: error = %v, want generic not found", err)
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("above the bound: %d log lines, want 1: %q", len(lines), logs.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decode log line: %v", err)
	}
	if record["level"] != "WARN" || record["org_id"] != orgID || record["repositories_read"] != float64(over) || record["repository_bound"] != float64(contextpacket.MaxEvidenceCandidateRepos) {
		t.Fatalf("bound signal = %v", record)
	}
	for _, marker := range []string{"bound-repo-id-", "bound-owner/", "bound-repo-"} {
		if strings.Contains(lines[0], marker) {
			t.Fatalf("bound signal carries repository identity %q: %s", marker, lines[0])
		}
	}
}
