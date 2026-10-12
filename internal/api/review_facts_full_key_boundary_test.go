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

// reviewRow is a git_pull_request_reviews row: (repo_id, number, review_id)
// is its key within the organization.
type reviewRow struct {
	repoID   string
	number   uint32
	reviewID string
	state    string
}

// reviewKeyStore answers the reviews read the way ClickHouse evaluates its
// ids predicate: the statement's own key expression decides which rows
// match, so a key without the pull request number admits every pull
// request's review with that id.
type reviewKeyStore struct{ rows []reviewRow }

func (s reviewKeyStore) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if !strings.Contains(statement, "FROM git_pull_request_reviews") {
		return &reviewRowScanner{}, nil
	}
	withNumber := strings.Contains(statement, "concat(toString(r.repo_id), ':', toString(r.number), ':', r.review_id) IN")
	if !withNumber && !strings.Contains(statement, "concat(toString(r.repo_id), ':', r.review_id) IN") {
		return nil, errors.New("unrecognised reviews key")
	}
	ids := map[string]bool{}
	for _, b := range bindings {
		if values, ok := b.Value.([]string); ok && b.Name == "ids" {
			for _, id := range values {
				ids[id] = true
			}
		}
	}
	var matched []reviewRow
	for _, row := range s.rows {
		key := row.repoID + ":" + row.reviewID
		if withNumber {
			key = fmt.Sprintf("%s:%d:%s", row.repoID, row.number, row.reviewID)
		}
		if ids[key] {
			matched = append(matched, row)
		}
	}
	return &reviewRowScanner{rows: matched}, nil
}

type reviewRowScanner struct {
	rows []reviewRow
	row  int
}

func (s *reviewRowScanner) Next() bool { return s.row < len(s.rows) }
func (s *reviewRowScanner) Scan(dest ...any) error {
	row := s.rows[s.row]
	values := []any{row.reviewID, row.state, row.repoID, row.number}
	for i, target := range dest {
		switch p := target.(type) {
		case *string:
			*p = values[i].(string)
		case *uint32:
			*p = values[i].(uint32)
		default:
			return fmt.Errorf("unsupported scan destination %T", target)
		}
	}
	s.row++
	return nil
}
func (s *reviewRowScanner) Err() error   { return nil }
func (s *reviewRowScanner) Close() error { return nil }

func reviewSubjectID(t *testing.T, repoID, number, reviewID string) string {
	t.Helper()
	canonical, omitted, err := identity.Derive(identity.KindPullRequestReview, []string{repoID, number, reviewID}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive: omitted=%v err=%v", omitted, err)
	}
	return canonical
}

// One approver's review id on two pull requests of one repository: read_facts
// answers each review subject with its own pull request's state and cites
// its own row.
func TestReadFactsAnswersEachReviewFromItsOwnPullRequest(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	h.app.config.RequestTimeout = 30 * time.Second
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	store := reviewKeyStore{rows: []reviewRow{
		{repoID: "repo-1", number: 1042, reviewID: "approval-42", state: "APPROVED"},
		{repoID: "repo-1", number: 1043, reviewID: "approval-42", state: "CHANGES_REQUESTED"},
	}}
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(store), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h.app.runtime.DirectReadGate = directread.NewSubjectGate(admitAllGraph{}, nil)
	h.app.runtime.DirectFactReader = directread.NewFactReader(registry.WithoutScopeExpansion())

	want := map[string]struct{ state, ref string }{
		reviewSubjectID(t, "repo-1", "1042", "approval-42"): {"APPROVED", "repo-1:1042:approval-42"},
		reviewSubjectID(t, "repo-1", "1043", "approval-42"): {"CHANGES_REQUESTED", "repo-1:1043:approval-42"},
	}
	var subjects []map[string]string
	for id := range want {
		subjects = append(subjects, map[string]string{"kind": "pull_request_review", "canonical_id": id})
	}
	body, _ := json.Marshal(map[string]any{"kinds": []string{"reviews"}, "subjects": subjects})
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
		t.Fatalf("served %d facts, want one per review subject: %s", len(document.Facts), served)
	}
	for _, fact := range document.Facts {
		expect, ok := want[fact.Subject.CanonicalID]
		if !ok {
			t.Fatalf("fact for an unrequested subject %q: %s", fact.Subject.CanonicalID, served)
		}
		delete(want, fact.Subject.CanonicalID)
		if fact.Fields["state"] != expect.state {
			t.Fatalf("%s state = %v, want %s: %s", fact.Subject.CanonicalID, fact.Fields["state"], expect.state, served)
		}
		cited := false
		for _, key := range fact.Provenance.NaturalKeys {
			cited = cited || (key.Entity == "review.v2" && key.ID == expect.ref)
		}
		if !cited {
			t.Fatalf("%s does not cite review.v2 %s: %+v", fact.Subject.CanonicalID, expect.ref, fact.Provenance.NaturalKeys)
		}
	}
	if len(want) != 0 {
		t.Fatalf("subjects with no fact: %v: %s", want, served)
	}
}
