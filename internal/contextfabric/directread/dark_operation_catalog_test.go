package directread

import (
	"slices"
	"testing"
)

const darkOperation = "securityAlerts"

func TestCatalogOffersNoDarkOperation(t *testing.T) {
	cat := loadDefault(t)
	var offered []string
	for _, op := range cat.Operations(CallerUnrestricted) {
		offered = append(offered, op.Name)
	}
	if slices.Contains(offered, darkOperation) {
		t.Fatalf("policy offers %s, whose root field is not enabled on the ops query service", darkOperation)
	}
	if len(offered) != 20 {
		t.Fatalf("unrestricted policy offers %d operations, want 20: %v", len(offered), offered)
	}
	if _, refusal := cat.Lookup(darkOperation); refusal == nil || refusal.Code != RefusalUnknownOperation || refusal.Reason == "" {
		t.Fatalf("lookup of %s: refusal %+v, want unknown_operation with a reason", darkOperation, refusal)
	}
	for _, class := range []PrincipalClass{ClassUnrestricted, ClassUniversal, ClassRestricted} {
		section := catalogFor(t, class, true, true).Operations
		for _, op := range section.Operations {
			if op.Name == darkOperation {
				t.Fatalf("class %v: data_catalog lists %s", class, darkOperation)
			}
		}
		listed := false
		for _, ns := range section.NotServed {
			if ns.Name == darkOperation {
				listed = true
				if ns.Code != RefusalUnknownOperation || ns.Reason == "" {
					t.Fatalf("not_served entry %+v", ns)
				}
			}
		}
		if !listed {
			t.Fatalf("class %v: %s is not in not_served", class, darkOperation)
		}
	}
	unrestricted := catalogFor(t, ClassUnrestricted, true, true).Operations
	if len(unrestricted.Operations) != 20 {
		t.Fatalf("data_catalog lists %d operations, want 20", len(unrestricted.Operations))
	}
	for _, op := range unrestricted.Operations {
		if !op.Available {
			t.Fatalf("%s is listed but unavailable", op.Name)
		}
	}
}
