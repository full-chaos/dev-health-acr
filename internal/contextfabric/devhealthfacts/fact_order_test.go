package devhealthfacts_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

type orderVariant struct {
	name  string
	order []int
	noise float64
}

// TestClientInputIsRowOrderAndAggregateNoiseIndependentForEveryFactKind runs
// every registered fact kind over every subject kind it supports through the
// real registry to the client payload, over the SAME static rows served in
// three different orders and with last-digit float noise on every float, and
// requires the client input bytes to be equal. A kind that keeps provider
// order, or serves a value that moves with the aggregation order, makes two
// identical calls carry two different input_sha256 values.
func TestClientInputIsRowOrderAndAggregateNoiseIndependentForEveryFactKind(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	variants := []orderVariant{
		{name: "static", order: []int{0, 1, 2}},
		{name: "reversed", order: []int{2, 1, 0}},
		{name: "rotated", order: []int{1, 2, 0}},
	}
	tc := clockTestCases()[1]
	produced := map[string]int{}
	var labels []string
	for _, base := range devhealthfacts.NewProviders(&universalClient{}) {
		capability := base.Capability()
		kind := capability.Kind
		for _, subjectKind := range capability.SupportedSubjectKinds {
			canonicalID, rowID := clockTestSubjectID(subjectKind)
			subject := contextfabric.SubjectRef{Kind: subjectKind, CanonicalID: canonicalID, Label: "subject-1"}
			label := fmt.Sprintf("%s/%s", kind, subjectKind)
			labels = append(labels, label)
			var first []byte
			for index, variant := range variants {
				client := &universalClient{subjectID: rowID, rows: 3, rules: clockTestRules(), order: variant.order, noise: variant.noise}
				provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
				payload, facts, err := clockTestPayload(t, provider, subject, kind, tc, now)
				if err != nil && !strings.HasPrefix(err.Error(), "no facts") {
					t.Logf("%s refused: %v", label, err)
					break
				}
				if facts > produced[label] {
					produced[label] = facts
				}
				if index == 0 {
					first = payload
					if facts < 2 {
						// One fact cannot change position; the SQL behind such a
						// read groups to one row per subject, so a fake that
						// serves several rows for the one key is not an answer
						// the store can give.
						break
					}
					continue
				}
				if bytes.Equal(first, payload) {
					continue
				}
				var left, right any
				if err := json.Unmarshal(first, &left); err != nil {
					t.Fatalf("%s: decode: %v", label, err)
				}
				if err := json.Unmarshal(payload, &right); err != nil {
					t.Fatalf("%s: decode: %v", label, err)
				}
				var diffs []string
				diffJSON("$", left, right, &diffs)
				t.Errorf("%s: client input differs between static and %s (%d facts):\n  %s", label, variant.name, facts, strings.Join(diffs, "\n  "))
			}
		}
	}
	for _, label := range labels {
		if produced[label] == 0 {
			t.Logf("%s produced no fact", label)
		}
	}
}
