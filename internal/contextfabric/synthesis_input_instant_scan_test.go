package contextfabric

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// fullTextInputInstantDays is the reference: every pattern over the whole
// encoded input, with no split into string leaves and no prefilter.
func fullTextInputInstantDays(t *testing.T, input SynthesisInput) instantDays {
	t.Helper()
	days := newInstantDays()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	scanInstantDays(string(encoded), days.add)
	for _, m := range isoDatetimePattern.FindAllStringSubmatch(string(encoded), -1) {
		if at, err := time.Parse(time.RFC3339, m[1]+zoneOrUTC(m[2])); err == nil {
			days.addAdjacent(at)
		}
	}
	if window := input.EvidenceWindow; window != nil {
		if window.Start != nil {
			days.addTime(*window.Start)
		}
		if window.End != nil {
			days.addTime(*window.End)
			days.addTime(window.End.Add(-time.Nanosecond))
		}
	}
	if !input.ReadTimeClamp.At.IsZero() {
		days.addAdjacent(input.ReadTimeClamp.At)
	}
	return days
}

var instantScanTokens = []string{
	"2031-03-04", "2031-03-04T10:00:00Z", "2031-03-05T23:30:00-12:00", "2031-03-06T01:00:00+0530", "2031-03-07T09:15",
	"2031-02-29", "2031-13-40", "12031-03-04", "2031-03-044", "x2031-03-08", "_2031-03-09", "-2031-03-10",
	"March", "Mar", "Mar.", "Sept", "Sep", "June", "May", "Feb", "Dec", "january", "4", "4th", "29", "31", "1st", "of",
	",", ", ", ", 2031", ",123", "2031", " ", "  ", "\n", "\t", "\"", "\\", "-", "T", ":", "x", "_", "<", "&", "é",
}

func randomInstantLabel(r *rand.Rand) string {
	var b strings.Builder
	for n := 1 + r.Intn(8); n > 0; n-- {
		b.WriteString(instantScanTokens[r.Intn(len(instantScanTokens))])
	}
	return b.String()
}

func instantScanInputs() map[string]SynthesisInput {
	inputs := map[string]SynthesisInput{}
	inputs["valid_fixture"] = validSynthesisInputFixture()
	closure, _ := closureFixture()
	inputs["closure_fixture"] = closure

	forms, _ := closureFixture()
	held := time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC)
	forms.Facts.Facts[0].ObservedAt = &held
	end := time.Date(2031, 4, 2, 0, 0, 0, 0, time.UTC)
	start := time.Date(2031, 1, 10, 0, 0, 0, 0, time.UTC)
	forms.EvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{Start: &start, End: &end}
	forms.ReadTimeClamp.At = time.Date(2031, 6, 15, 9, 0, 0, 0, time.UTC)
	inputs["instant_forms_fixture"] = forms

	offset, _ := closureFixture()
	at := time.Date(2031, 3, 5, 23, 30, 0, 0, time.FixedZone("west", -12*3600))
	offset.Facts.Facts[0].ObservedAt = &at
	inputs["offset_fixture"] = offset

	dated, _ := closureFixture()
	base := dated.Facts.Facts[0]
	dated.Facts.Facts = nil
	for i := 0; i < 500; i++ {
		fact := base
		day := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i)
		fact.ObservedAt = &day
		dated.Facts.Facts = append(dated.Facts.Facts, fact)
	}
	inputs["dated_facts"] = dated

	for seed := int64(1); seed <= 8; seed++ {
		r := rand.New(rand.NewSource(seed))
		in, _ := closureFixture()
		base := in.Facts.Facts[0]
		in.Facts.Facts = nil
		for i := 0; i < 400; i++ {
			fact := base
			fact.Subject.Label = randomInstantLabel(r)
			text := randomInstantLabel(r)
			fact.Fields = map[string]FactValue{randomInstantLabel(r): {String: &text}}
			in.Facts.Facts = append(in.Facts.Facts, fact)
		}
		inputs[fmt.Sprintf("random_labels_seed_%d", seed)] = in
	}
	return inputs
}

func TestInputInstantDaysEqualTheFullTextScan(t *testing.T) {
	t.Parallel()
	for name, input := range instantScanInputs() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			want := fullTextInputInstantDays(t, input)
			got, err := synthesisInputInstantDays(input)
			if err != nil {
				t.Fatal(err)
			}
			if len(want.full)+len(want.yearless) == 0 && !strings.HasPrefix(name, "valid") && !strings.HasPrefix(name, "closure") {
				t.Fatalf("%s holds no day: the case proves nothing", name)
			}
			if !reflect.DeepEqual(got.full, want.full) || !reflect.DeepEqual(got.yearless, want.yearless) {
				t.Fatalf("held days differ from the full-text scan: got %d full / %d yearless, want %d / %d", len(got.full), len(got.yearless), len(want.full), len(want.yearless))
			}
			t.Logf("%s: %d full days, %d yearless days", name, len(want.full), len(want.yearless))
		})
	}
}

// Not parallel: the timings compare two reads of one input in this process.
func TestDatedDraftInputScanCostsAFewMarshals(t *testing.T) {
	input, draft := closureFixture()
	base := input.Facts.Facts[0]
	input.Facts.Facts = nil
	for i := 0; i < 2000; i++ {
		fact := base
		fact.Subject.Label = fmt.Sprintf("Title work-%04d %x", i, i*7919)
		fact.Subject.CanonicalID = fmt.Sprintf("work_item:%064x", i*104729)
		input.Facts.Facts = append(input.Facts.Facts, fact)
	}
	held := time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC)
	input.Facts.Facts[0].ObservedAt = &held
	draft.CurrentState = "As of 2031-03-04."
	fastest := func(fn func()) time.Duration {
		best := time.Duration(1<<63 - 1)
		for i := 0; i < 3; i++ {
			start := time.Now()
			fn()
			if took := time.Since(start); took < best {
				best = took
			}
		}
		return best
	}
	var size int
	marshal := fastest(func() {
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		size = len(encoded)
	})
	check := fastest(func() {
		if err := draft.requireGroundedInstants(input); err != nil {
			t.Fatalf("requireGroundedInstants() = %v", err)
		}
	})
	const bound = 8
	t.Logf("dated draft over %d input bytes: grounding check %s, marshal %s, ratio %.1f, bound %d", size, check, marshal, float64(check)/float64(marshal), bound)
	if check > bound*marshal {
		t.Fatalf("the grounding check took %s, more than %d marshals of its input (%s)", check, bound, marshal)
	}
}
