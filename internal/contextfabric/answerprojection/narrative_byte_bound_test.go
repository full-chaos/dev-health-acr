package answerprojection

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func narrativeBytes(values []string) int {
	total := 0
	for _, value := range values {
		total += len(value)
	}
	return total
}

func bigNarrative(prefix string, count int) []string {
	return filled(count, func(i int) string { return pad(prefix, i, contractsv1.ContextFabricProjectedNarrativeMaxLength) })
}

func TestNarrativeArraysYieldToTheByteCeiling(t *testing.T) {
	const ceiling = 262144
	allowance := ceiling / NarrativeByteShareDivisor
	for _, array := range []string{"limitations", "warnings"} {
		t.Run(array, func(t *testing.T) {
			result := richResult()
			values := bigNarrative(array, contractsv1.ContextFabricProjectedNarrativeMaxCount)
			result.Limitations, result.Warnings = nil, nil
			if array == "limitations" {
				result.Limitations = values
			} else {
				result.Warnings = values
			}
			p := Project(result, Budget{MaxSerializedBytes: ceiling})
			got, omitted := p.Limitations, p.ProjectionBudget.LimitationsOmitted
			if array == "warnings" {
				got, omitted = p.Warnings, p.ProjectionBudget.WarningsOmitted
			}
			if n := narrativeBytes(got); n > allowance {
				t.Errorf("%s carry %d bytes, want <= %d", array, n, allowance)
			}
			if len(got) == 0 {
				t.Fatalf("%s emptied", array)
			}
			if want := len(values) - len(got); omitted != want || omitted == 0 {
				t.Errorf("%s omitted = %d, want %d (nonzero)", array, omitted, want)
			}
		})
	}
}

func TestNarrativeByteCutKeepsTheDegradationDisclosureFirst(t *testing.T) {
	result := richResult()
	result.Limitations = append(bigNarrative("limitation", contractsv1.ContextFabricProjectedNarrativeMaxCount-1), contractsv1.ContextFabricRetrievalDegradedLimitation)
	// allowance 64000 = 32 whole 2000-byte entries: nothing is left for the
	// disclosure unless it is admitted first.
	p := Project(result, Budget{MaxSerializedBytes: 256000})
	found := false
	for _, limitation := range p.Limitations {
		if limitation == contractsv1.ContextFabricRetrievalDegradedLimitation {
			found = true
		}
	}
	if !found {
		t.Fatal("the retrieval-degraded disclosure was cut by the byte ceiling")
	}
	if n := narrativeBytes(p.Limitations); n > 256000/NarrativeByteShareDivisor {
		t.Errorf("limitations carry %d bytes over the allowance", n)
	}
}

func TestNarrativeByteCutNeverEmptiesANonEmptyList(t *testing.T) {
	huge := strings.Repeat("é", contractsv1.ContextFabricProjectedNarrativeMaxLength) // 4000 bytes
	result := richResult()
	result.Limitations = []string{huge + "a", huge + "b"}
	result.Warnings = []string{huge + "c"}
	p := Project(result, Budget{MaxSerializedBytes: 8192}) // allowance 2048 < one entry
	if len(p.Limitations) != 1 || len(p.Warnings) != 1 {
		t.Fatalf("limitations=%d warnings=%d, want 1 and 1", len(p.Limitations), len(p.Warnings))
	}
	if p.ProjectionBudget.LimitationsOmitted != 1 || p.ProjectionBudget.WarningsOmitted != 0 {
		t.Errorf("omitted = %d/%d, want 1/0", p.ProjectionBudget.LimitationsOmitted, p.ProjectionBudget.WarningsOmitted)
	}
}

func TestNarrativeUnderTheAllowanceIsUntouched(t *testing.T) {
	result := richResult()
	result.Limitations = []string{"one", "two", "three"}
	result.Warnings = []string{"w"}
	p := Project(result, Budget{MaxSerializedBytes: 8192})
	if len(p.Limitations) != 3 || p.ProjectionBudget.LimitationsOmitted != 0 || len(p.Warnings) != 1 || p.ProjectionBudget.WarningsOmitted != 0 {
		t.Fatalf("a list under its allowance was cut: %v %+v", p.Limitations, p.ProjectionBudget)
	}
}
