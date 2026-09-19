package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The keyed lookup's own input domain: no organization, a row carrying no
// node, a row for a subject not asked about, a batch boundary, and a failed
// read.
func TestAuthorizeStoredSubjectsInputDomain(t *testing.T) {
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/api"}}
	binding := contextfabric.ResolvedGraphBinding{GraphKey: "acr-cf-fake-key", Epoch: 1}

	if _, err := newFakeAdapter(t, &fakeConn{}).AuthorizeStoredSubjects(context.Background(), storage.Principal{OrgID: "  "}, binding, nil); !errors.Is(err, contextfabric.ErrUnavailable) {
		t.Fatalf("blank organization error = %v, want ErrUnavailable", err)
	}

	subjects := make([]contextfabric.SubjectRef, 0, storedSubjectBatch+2)
	for index := 0; index < storedSubjectBatch+2; index++ {
		subjects = append(subjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: fmt.Sprintf("pr:%d", index)})
	}
	var batches []int
	fake := &fakeConn{queryFunc: func(_ context.Context, graphKey, _ string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if graphKey != binding.GraphKey || !readOnly || params["org"] != "org-1" {
			t.Fatalf("query key=%s readOnly=%t org=%v", graphKey, readOnly, params["org"])
		}
		targets := params["targets"].([]interface{})
		batches = append(batches, len(targets))
		var rows []row
		for _, raw := range targets {
			target := raw.(map[string]interface{})
			switch target["id"] {
			case "pr:0":
				rows = append(rows, row{"n": &node{Properties: map[string]interface{}{propKind: "pull_request", propCanonicalID: "pr:0", "authorization_repositories": []string{"acme/api"}}}})
			case "pr:1":
				rows = append(rows, row{"n": &node{Properties: map[string]interface{}{propKind: "pull_request", propCanonicalID: "pr:1", "authorization_repositories": []string{"other/repo"}}}})
			case "pr:2":
				rows = append(rows, row{"n": nil}, row{"x": "no node"})
			case fmt.Sprintf("pr:%d", storedSubjectBatch+1):
				rows = append(rows, row{"n": &node{Properties: map[string]interface{}{propKind: "pull_request", propCanonicalID: target["id"], "authorization_repositories": "*"}}})
			}
		}
		return rows, nil
	}}
	outcomes, err := newFakeAdapter(t, fake).AuthorizeStoredSubjects(context.Background(), principal, binding, subjects)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 || batches[0] != storedSubjectBatch || batches[1] != 2 {
		t.Fatalf("batches = %v, want [%d 2]", batches, storedSubjectBatch)
	}
	want := map[int]contextfabric.StoredSubjectOutcome{0: contextfabric.StoredSubjectAdmitted, 1: contextfabric.StoredSubjectDenied, 2: contextfabric.StoredSubjectAbsent, 3: contextfabric.StoredSubjectAbsent, storedSubjectBatch + 1: contextfabric.StoredSubjectAdmitted}
	for index, outcome := range want {
		if outcomes[index] != outcome {
			t.Errorf("subject %d = %s, want %s", index, outcomes[index], outcome)
		}
	}

	// A subject named by canonical id alone is read by id; every node that
	// carries the id decides it.
	var unkindedQueries int
	byID := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		var rows []row
		for _, raw := range params["targets"].([]interface{}) {
			target := raw.(map[string]interface{})
			if _, hasKind := target["kind"]; hasKind {
				continue
			}
			unkindedQueries++
			switch target["id"] {
			case "shared":
				rows = append(rows,
					row{"n": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "shared", "authorization_repositories": []string{"acme/api"}}}},
					row{"n": &node{Properties: map[string]interface{}{propKind: "team", propCanonicalID: "shared", "authorization_repositories": []string{"other/repo"}}}})
			case "granted":
				rows = append(rows,
					row{"n": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "granted", "authorization_repositories": []string{"acme/api"}}}},
					row{"n": &node{Properties: map[string]interface{}{propKind: "team", propCanonicalID: "granted", "authorization_repositories": "*"}}})
			}
		}
		return rows, nil
	}}
	unkinded, err := newFakeAdapter(t, byID).AuthorizeStoredSubjects(context.Background(), principal, binding, []contextfabric.SubjectRef{{CanonicalID: "shared"}, {CanonicalID: "granted"}, {CanonicalID: "nowhere"}})
	if err != nil {
		t.Fatal(err)
	}
	if unkindedQueries != 3 || unkinded[0] != contextfabric.StoredSubjectDenied || unkinded[1] != contextfabric.StoredSubjectAdmitted || unkinded[2] != contextfabric.StoredSubjectAbsent {
		t.Fatalf("unkinded outcomes = %v (queries %d), want denied/admitted/absent", unkinded, unkindedQueries)
	}

	failing := &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
		return nil, fmt.Errorf("wrapped: %w", ErrNotFound)
	}}
	if _, err := newFakeAdapter(t, failing).AuthorizeStoredSubjects(context.Background(), principal, binding, subjects[:1]); !errors.Is(err, contextfabric.ErrGraphNotProjected) {
		t.Fatalf("missing graph error = %v, want ErrGraphNotProjected", err)
	}
}
