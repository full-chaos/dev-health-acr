package api

import (
	"strings"
	"testing"
)

// CHAOS-12427: every operation the OpenAPI publishes, secured or not, is
// registered in the mux. A published route with no handler answers 404 with
// no access read, so the contract would promise what the service does not do.
func TestEveryOpenAPIOperationIsRegisteredInTheMux(t *testing.T) {
	operations := loadOpenAPIOperations(t)
	patterns := registeredPatterns(t, true)
	if len(operations) == 0 || len(patterns) == 0 {
		t.Fatalf("matched nothing: %d OpenAPI operations, %d registered patterns", len(operations), len(patterns))
	}
	var secured, unsecured int
	for _, op := range operations {
		if op.secured {
			secured++
		} else {
			unsecured++
		}
		found := false
		for _, pattern := range patterns {
			method, template, ok := strings.Cut(pattern, " ")
			if !ok {
				t.Fatalf("registered pattern %q has no method", pattern)
			}
			// Parameter names may differ between the two; the shape is what counts.
			if method == op.method && sameShape(template, op.template) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the OpenAPI publishes %s %s (secured = %v) but the mux does not register it", op.method, op.template, op.secured)
		}
	}
	if secured == 0 || unsecured == 0 {
		t.Fatalf("checked %d secured and %d unsecured operations; want both kinds", secured, unsecured)
	}
}

// sameShape compares two path templates segment by segment: literals equal,
// a {name} segment matches any {other} segment.
func sameShape(a, b string) bool {
	left, right := strings.Split(a, "/"), strings.Split(b, "/")
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		leftParam := strings.HasPrefix(left[i], "{") && strings.HasSuffix(left[i], "}")
		rightParam := strings.HasPrefix(right[i], "{") && strings.HasSuffix(right[i], "}")
		if leftParam != rightParam || (!leftParam && left[i] != right[i]) {
			return false
		}
	}
	return true
}
