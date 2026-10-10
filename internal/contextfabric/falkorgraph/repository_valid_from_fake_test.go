package falkorgraph

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

type entityWriteCapture struct {
	cypher string
	params map[string]interface{}
}

func captureOwnedEntityWrite(t *testing.T, subject contextfabric.SubjectRef, validFrom *time.Time) entityWriteCapture {
	t.Helper()
	var captured entityWriteCapture
	adapter := newFakeAdapter(t, &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		captured = entityWriteCapture{cypher: cypher, params: params}
		return nil, nil
	}})
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	err := adapter.projectEntity(context.Background(), "key", "org-1", contextfabric.EntityProjection{
		Subject: subject, ObservedAt: at, ValidFrom: validFrom, SourceVersion: "v1",
	})
	if err != nil {
		t.Fatalf("projectEntity() error = %v", err)
	}
	return captured
}

func TestRepositoryEntityWriteKeepsTheEarlierValidityStart(t *testing.T) {
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r", Label: "acme/r"}
	write := captureOwnedEntityWrite(t, repo, &start)
	const want = "MERGE (n:Subject {org_id:$org, subject_kind:$nKind, canonical_id:$nId}) SET n:Repository SET n += $attrs" +
		" SET n += {valid_from: CASE WHEN $repoValidFromNs IS NULL THEN n.valid_from WHEN n.valid_from_ns IS NULL OR $repoValidFromNs < n.valid_from_ns THEN $repoValidFrom ELSE n.valid_from END," +
		" valid_from_ns: CASE WHEN $repoValidFromNs IS NULL THEN n.valid_from_ns WHEN n.valid_from_ns IS NULL OR $repoValidFromNs < n.valid_from_ns THEN $repoValidFromNs ELSE n.valid_from_ns END}"
	if write.cypher != want {
		t.Fatalf("repository write cypher =\n%s\nwant\n%s", write.cypher, want)
	}
	attrs := write.params["attrs"].(map[string]interface{})
	for _, name := range []string{propValidFrom, propValidFromNs} {
		if _, present := attrs[name]; present {
			t.Fatalf("repository attrs carry %s, which would overwrite a held start before the compare", name)
		}
	}
	if write.params["repoValidFromNs"] != nsTimestamp(start) {
		t.Fatalf("incoming start ns = %v, want %v", write.params["repoValidFromNs"], nsTimestamp(start))
	}
	none := captureOwnedEntityWrite(t, repo, nil)
	if none.params["repoValidFromNs"] != nil || none.params["repoValidFrom"] != nil {
		t.Fatalf("a repository write with no start sent %v / %v, want null", none.params["repoValidFrom"], none.params["repoValidFromNs"])
	}
}

func TestOtherKindsKeepTheCanonicalValidityOverwrite(t *testing.T) {
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	item := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_i", Label: "i"}
	write := captureOwnedEntityWrite(t, item, &start)
	if strings.Contains(write.cypher, "CASE") {
		t.Fatalf("a work item write compares starts: %s", write.cypher)
	}
	attrs := write.params["attrs"].(map[string]interface{})
	if attrs[propValidFromNs] != nsTimestamp(start) {
		t.Fatalf("work item attrs start = %v, want %v", attrs[propValidFromNs], nsTimestamp(start))
	}
	if cleared := captureOwnedEntityWrite(t, item, nil).params["attrs"].(map[string]interface{}); cleared[propValidFromNs] != nil {
		t.Fatalf("a work item write with no start must carry a null start to clear it, got %v", cleared[propValidFromNs])
	} else if _, present := cleared[propValidFromNs]; !present {
		t.Fatal("a work item write with no start dropped the key, so it can no longer clear")
	}
}
