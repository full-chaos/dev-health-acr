package factoracle

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// A recorded capture is only a measurement of what the policy lets a client
// send: a shape case that carries a measure the policy no longer serves was
// taken before that change and has to be captured again, not edited.
func TestRecordedShapeCasesSendOnlyMeasuresThePolicyServes(t *testing.T) {
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, caller := range []directread.CallerClass{directread.CallerUnrestricted, directread.CallerRestricted} {
		for _, op := range policy.Catalogue().Operations(caller) {
			for _, v := range op.Variables {
				if v.Allowed && (v.Path == "measure" || len(v.Path) > 8 && v.Path[len(v.Path)-8:] == ".measure") {
					for _, value := range sendableValues(v) {
						allowed[value] = true
					}
				}
			}
		}
	}
	if len(allowed) == 0 {
		t.Fatal("the policy serves no measure variable: the check is not a measurement")
	}
	if allowed["CHURN_LOC"] {
		t.Fatal("the policy still lets a client send CHURN_LOC")
	}
	manifest, _, _ := loadedCapture(t)
	checked := 0
	var walk func(shape string, value any)
	walk = func(shape string, value any) {
		switch v := value.(type) {
		case map[string]any:
			for name, item := range v {
				if text, ok := item.(string); ok && name == "measure" {
					checked++
					if !allowed[text] {
						t.Errorf("shape case %s sends measure %s, which the policy does not let a client send: capture the venue again", shape, text)
					}
				}
				walk(shape, item)
			}
		case []any:
			for _, item := range v {
				walk(shape, item)
			}
		}
	}
	for _, c := range manifest.ShapeCases {
		walk(c.ShapeID, c.Variables)
	}
	if checked == 0 {
		t.Fatal("no recorded shape case carries a measure: the check is not a measurement")
	}
	if !allowed["COUNT"] {
		t.Fatal("COUNT is not a measure the policy serves")
	}
}
