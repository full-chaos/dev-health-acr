package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// The read_facts route over the production status provider, with a store
// client that refuses a whole batch when any id carries a backslash, the way
// the real ClickHouse client does.

type backslashRefusingStore struct{ rows [][]any }

func (s backslashRefusingStore) Query(_ context.Context, _ string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	for _, b := range bindings {
		if ids, ok := b.Value.([]string); ok {
			for _, id := range ids {
				if strings.Contains(id, `\`) {
					return nil, errors.New("binding value cannot be safely encoded")
				}
			}
		}
	}
	return &stringRowScanner{rows: s.rows}, nil
}

type stringRowScanner struct {
	rows [][]any
	row  int
}

func (s *stringRowScanner) Next() bool { return s.row < len(s.rows) }
func (s *stringRowScanner) Scan(dest ...any) error {
	for i, target := range dest {
		p, ok := target.(*string)
		if !ok {
			return errors.New(fmt.Sprintf("unsupported scan destination %T", target))
		}
		*p = s.rows[s.row][i].(string)
	}
	s.row++
	return nil
}
func (s *stringRowScanner) Err() error   { return nil }
func (s *stringRowScanner) Close() error { return nil }

func workItemSubjectID(t *testing.T, repoID, id string) string {
	t.Helper()
	canonical, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, id}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive: omitted=%v err=%v", omitted, err)
	}
	return canonical
}

func TestReadFactsServesTheSafeWorkItemsWhenOneIDIsUnsafe(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	h.app.config.RequestTimeout = 30 * time.Second
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	store := backslashRefusingStore{rows: [][]any{
		{"WIDGET-1", "in_progress", "repo-1", ""},
		{"WIDGET-3", "in_progress", "repo-1", ""},
	}}
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(store), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h.app.runtime.DirectReadGate = directread.NewSubjectGate(admitAllGraph{}, nil)
	h.app.runtime.DirectFactReader = directread.NewFactReader(registry.WithoutScopeExpansion())

	body, _ := json.Marshal(map[string]any{
		"kinds": []string{"status"},
		"subjects": []map[string]string{
			{"kind": "work_item", "canonical_id": workItemSubjectID(t, "repo-1", "WIDGET-1")},
			{"kind": "work_item", "canonical_id": workItemSubjectID(t, "repo-1", `back\slash-2`)},
			{"kind": "work_item", "canonical_id": workItemSubjectID(t, "repo-1", "WIDGET-3")},
		},
	})
	response := h.postFacts(token, string(body))
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	var document directread.FactsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	served := response.Body.String()
	if len(document.Facts) != 2 {
		t.Fatalf("served %d facts, want the 2 safe work items: %s", len(document.Facts), served)
	}
	if strings.Contains(served, `"outcome":"unavailable"`) || strings.Contains(served, "query work item status failed") {
		t.Fatalf("one unsafe id was served as a store outage: %s", served)
	}
	if !strings.Contains(served, "subject_id_shape_rejected") {
		t.Fatalf("the unsafe id is not disclosed with its own cause: %s", served)
	}
}
