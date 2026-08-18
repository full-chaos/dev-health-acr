package identity_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// TestDerivePinnedExamples pins the exact fixed-kinds table from design
// brief §1.2, byte for byte.
func TestDerivePinnedExamples(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		values []string
		want   string
	}{
		{"ci_pipeline_run", identity.KindCIPipelineRun, []string{"repo-1", "run-42"}, "ci_pipeline_run.v2:repo-1:run-42"},
		{"pull_request_review", identity.KindPullRequestReview, []string{"repo-1", "17", "review-9"}, "pull_request_review.v2:repo-1:17:review-9"},
		{"deployment", identity.KindDeployment, []string{"repo-1", "deploy-3"}, "deployment.v2:repo-1:deploy-3"},
		{"work_item", identity.KindWorkItem, []string{"repo-1", "WIDGET-101"}, "work_item.v2:repo-1:WIDGET-101"},
		{"project", identity.KindProject, []string{"github", "71133891"}, "project.v2:github:71133891"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, omitted, err := identity.Derive(c.kind, c.values, nil)
			if err != nil {
				t.Fatalf("Derive(%q, %v) error: %v", c.kind, c.values, err)
			}
			if omitted {
				t.Fatalf("Derive(%q, %v) unexpectedly omitted", c.kind, c.values)
			}
			if got != c.want {
				t.Errorf("Derive(%q, %v) = %q, want %q", c.kind, c.values, got, c.want)
			}
		})
	}
}

// TestDeriveNamespaceIsByteDisjointFromV1 pins the ".v2:" break design
// brief §1.1 relies on: a v2 id can never collide with any v1 id of the
// same kind, because no v1 id contains the literal substring ".v2:".
func TestDeriveNamespaceIsByteDisjointFromV1(t *testing.T) {
	id, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "WIDGET-101"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v1ID := "work_item:WIDGET-101"
	if id == v1ID {
		t.Fatalf("v2 id collided with v1 id: %q", id)
	}
	if !strings.HasPrefix(id, "work_item.v2:") {
		t.Fatalf("v2 id %q does not carry the .v2: namespace break", id)
	}
}

// TestDeriveEncodesEverySegmentUniformly proves segments are passed
// through the codec even when they don't "need" it in live data --
// uniformity is the property the injectivity proof depends on (§1.1).
func TestDeriveEncodesEverySegmentUniformly(t *testing.T) {
	got, omitted, err := identity.Derive(identity.KindDeployment, []string{"re:po", "dep%loy"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if omitted {
		t.Fatal("unexpectedly omitted")
	}
	want := "deployment.v2:re%3Apo:dep%25loy"
	if got != want {
		t.Errorf("Derive = %q, want %q", got, want)
	}
}

// TestDeriveClosesTheNamedAmbiguityAcrossSegments is JoinSegments' §1.1
// ambiguity closure, exercised through Derive itself (same kind, 2
// segments): (a, "b:c") and ("a:b", c) must not collide.
func TestDeriveClosesTheNamedAmbiguityAcrossSegments(t *testing.T) {
	idLeft, _, err := identity.Derive(identity.KindDeployment, []string{"a", "b:c"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	idRight, _, err := identity.Derive(identity.KindDeployment, []string{"a:b", "c"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idLeft == idRight {
		t.Fatalf("ambiguity not closed through Derive: %q == %q", idLeft, idRight)
	}
}

func TestDeriveRejectsUnregisteredKind(t *testing.T) {
	if _, _, err := identity.Derive("no_such_kind", []string{"x"}, nil); err == nil {
		t.Fatal("expected an error for an unregistered kind")
	}
}

func TestDeriveRejectsWrongSegmentCount(t *testing.T) {
	if _, _, err := identity.Derive(identity.KindDeployment, []string{"only-one"}, nil); err == nil {
		t.Fatal("expected an error for a segment-count mismatch")
	}
	if _, _, err := identity.Derive(identity.KindDeployment, []string{"a", "b", "c"}, nil); err == nil {
		t.Fatal("expected an error for a segment-count mismatch")
	}
}

func TestLookupUnknownKind(t *testing.T) {
	if _, ok := identity.Lookup("not_a_kind"); ok {
		t.Fatal("Lookup unexpectedly found an unregistered kind")
	}
}
