package eventspec

import (
	"sort"
	"strings"
	"testing"
)

func TestEveryZeroOrManyEventHasANumericBoundOrAListedReason(t *testing.T) {
	t.Parallel()
	var withReason []string
	for _, event := range All {
		if event.Multiplicity != MultiplicityZeroOrManyPerRequest {
			continue
		}
		bounded, reasoned := event.MaxLinesPerScope > 0, event.UnboundedReason != ""
		switch {
		case bounded && reasoned:
			t.Errorf("%s declares both MaxLinesPerScope and UnboundedReason", event.ID)
		case !bounded && !reasoned:
			t.Errorf("%s declares neither MaxLinesPerScope nor UnboundedReason", event.ID)
		case reasoned:
			withReason = append(withReason, event.ID)
			if len(strings.Fields(event.UnboundedReason)) < 8 {
				t.Errorf("%s: UnboundedReason %q does not state why the count cannot be bounded", event.ID, event.UnboundedReason)
			}
		}
	}
	sort.Strings(withReason)
	want := append([]string(nil), unboundedEvents...)
	sort.Strings(want)
	if strings.Join(withReason, "|") != strings.Join(want, "|") {
		t.Errorf("events without a numeric bound = %v, the reviewed list is %v", withReason, want)
	}
}
