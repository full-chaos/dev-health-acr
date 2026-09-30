package directread

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

// The find_subjects line ("context fabric direct read"), certified from the
// bytes the production SlogFindRecorder wrote for real Find calls: a served
// list, an invalid request and an unavailable graph. No id, label or query
// text may reach it.
func TestDirectReadLineCertifiesAgainstItsSpecification(t *testing.T) {
	graph := threeRepoGraph()
	var buffer bytes.Buffer
	lookup := newLookup(graph, NewSlogFindRecorder(slog.New(slog.NewJSONHandler(&buffer, nil))))
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
	principal := lookupPrincipal(orgA, "acme/a")

	if _, err := lookup.Find(ctx, principal, FindRequest{Kind: "repository"}); err != nil {
		t.Fatal(err)
	}
	if _, err := lookup.Find(ctx, principal, FindRequest{Query: "secret-query-text", Kinds: []string{"no_such_kind"}}); err == nil {
		t.Fatal("invalid kind accepted")
	}
	graph.listErr = context.DeadlineExceeded
	if _, err := lookup.Find(ctx, principal, FindRequest{Kind: "repository"}); err == nil {
		t.Fatal("graph failure served")
	}
	for _, leak := range []string{"repository:a", "acme/", "secret-query-text"} {
		if strings.Contains(buffer.String(), leak) {
			t.Fatalf("direct read line leaks %q:\n%s", leak, buffer.String())
		}
	}
	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d:\n%s", len(lines), buffer.String())
	}
	wants := []map[string]any{
		{"org_id": orgA, "tool": "find_subjects", "mode": "list", "kinds": []any{"repository"}, "subject_kinds": []any{"repository"}, "count": 1, "status": "complete", "request_id": "req_0123456789abcdef0123456789abcdef"},
		{"org_id": orgA, "tool": "find_subjects", "mode": "name", "count": 0, "status": "invalid_request", "error_class": "invalid_request"},
		{"org_id": orgA, "tool": "find_subjects", "mode": "list", "count": 0, "status": "unavailable", "error_class": "deadline_exceeded"},
	}
	for i, line := range lines {
		parsed, err := certify.Parse([]byte(line))
		if err != nil {
			t.Fatalf("certify.Parse: %v", err)
		}
		if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.DirectRead, Want: wants[i]}); err != nil {
			t.Fatalf("certify line %d: %v\n%s", i, err, line)
		}
	}
}

// The literal vocabularies of eventspec.DirectRead equal the producer's.
func TestDirectReadEventVocabulariesMatchProducer(t *testing.T) {
	statuses := []string{"invalid_request", "unavailable"}
	for _, s := range FindStatusVocabulary() {
		statuses = append(statuses, string(s))
	}
	want := map[string][]string{
		"tool":        {FindSubjectsTool},
		"mode":        func() []string { v := FindModeVocabulary(); return v[:] }(),
		"status":      statuses,
		"error_class": {"invalid_request", "scope_required", "deadline_exceeded", "canceled", "dependency_unavailable", "graph_error"},
		"anchor":      {"admitted", "refused"},
	}
	seen := 0
	for _, field := range eventspec.DirectRead.Fields {
		expected, ok := want[field.Key]
		if !ok {
			continue
		}
		seen++
		got := append([]string{}, field.ClosedVocabulary...)
		exp := append([]string{}, expected...)
		sort.Strings(got)
		sort.Strings(exp)
		if !slices.Equal(got, exp) {
			t.Errorf("eventspec %s vocabulary %v, producer %v", field.Key, got, exp)
		}
	}
	if seen != len(want) {
		t.Fatalf("checked %d of %d vocabularies", seen, len(want))
	}
	// Every error class the producer can write is in the declared set.
	for _, err := range []error{context.DeadlineExceeded, context.Canceled, errFake{}} {
		if class := findErrorClass(err); !slices.Contains(want["error_class"], class) {
			t.Errorf("producer error class %q outside the vocabulary", class)
		}
	}
	if DirectReadLogMessage != eventspec.DirectRead.Msg {
		t.Fatalf("message %q vs spec %q", DirectReadLogMessage, eventspec.DirectRead.Msg)
	}
}

type errFake struct{}

func (errFake) Error() string { return "fake" }
