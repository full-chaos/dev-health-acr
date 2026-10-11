package answerprojection

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// narrativeBytes is what the served ceiling measures: the encoded array.
func narrativeBytes(values []string) int {
	encoded, err := json.Marshal(values)
	if err != nil {
		panic(err)
	}
	return len(encoded)
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
	huge := strings.Repeat("é", contractsv1.ContextFabricProjectedNarrativeMaxLength) // 4000 bytes, more encoded
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

// Escapes make the encoded length exceed the raw one: a quote, a newline and
// an angle bracket each cost 2 to 6 bytes on the wire.
func TestNarrativeByteCutMeasuresTheEncodedLength(t *testing.T) {
	const ceiling = 8192 // allowance 2048
	// 250 characters, 5 raw bytes each is not the point: 250 x '"' is 250
	// raw bytes and 500 encoded. 8 entries are 2000 raw (fits) and 4000
	// encoded (does not).
	entry := func(i int) string { return strconv.Itoa(1000+i) + strings.Repeat("\"", 246) }
	result := richResult()
	result.Limitations = filled(8, entry)
	result.Warnings = nil
	raw := 0
	for _, value := range result.Limitations {
		raw += len(value)
	}
	if raw > ceiling/NarrativeByteShareDivisor {
		t.Fatalf("fixture is over the allowance by the raw measure (%d), so it cannot tell the measures apart", raw)
	}
	p := Project(result, Budget{MaxSerializedBytes: ceiling})
	if n := narrativeBytes(p.Limitations); n > ceiling/NarrativeByteShareDivisor {
		t.Errorf("limitations encode to %d bytes, want <= %d", n, ceiling/NarrativeByteShareDivisor)
	}
	if p.ProjectionBudget.LimitationsOmitted == 0 {
		t.Error("nothing was cut although the encoded array overshoots the allowance")
	}
}

// An overlarge entry is the last resort: it is kept only when no entry fits,
// never ahead of one that does.
func TestNarrativeByteCutPrefersAFittingEntryToAnOverlargeFirst(t *testing.T) {
	huge := strings.Repeat("é", contractsv1.ContextFabricProjectedNarrativeMaxLength)
	result := richResult()
	result.Limitations = []string{huge, "ok"}
	result.Warnings = nil
	p := Project(result, Budget{MaxSerializedBytes: 8192}) // allowance 2048
	if len(p.Limitations) != 1 || p.Limitations[0] != "ok" {
		t.Fatalf("limitations = %d entries (first %.8q), want just the fitting entry", len(p.Limitations), strings.Join(p.Limitations, "|"))
	}
	if p.ProjectionBudget.LimitationsOmitted != 1 {
		t.Errorf("limitations_omitted = %d, want 1", p.ProjectionBudget.LimitationsOmitted)
	}
}

func logField(args []any, name string) (any, bool) {
	for i := 0; i+1 < len(args); i += 2 {
		if args[i] == name {
			return args[i+1], true
		}
	}
	return nil, false
}

// The answer-display event names the byte cut, its ceiling and its effect:
// the byte cut is the part the count cap did not remove.
func TestDisplayLogNamesTheNarrativeByteCut(t *testing.T) {
	const ceiling = 262144
	result := richResult()
	result.Limitations = bigNarrative("limitation", 100) // count cap does not bite
	result.Warnings = []string{"short"}
	budget := Budget{MaxSerializedBytes: ceiling}
	p := Project(result, budget)
	args := DisplayLogArgs(result, p, budget, false, false)
	want := map[string]any{
		"max_serialized_bytes":     ceiling,
		"narrative_byte_allowance": ceiling / NarrativeByteShareDivisor,
		"limitations_omitted":      p.ProjectionBudget.LimitationsOmitted,
		"warnings_omitted":         0,
		"warnings_byte_cut":        0,
	}
	for name, expected := range want {
		if got, ok := logField(args, name); !ok || got != expected {
			t.Errorf("%s = %v (present %v), want %v", name, got, ok, expected)
		}
	}
	got, ok := logField(args, "limitations_byte_cut")
	if !ok || got.(int) == 0 || got.(int) != p.ProjectionBudget.LimitationsOmitted {
		t.Errorf("limitations_byte_cut = %v (present %v), want the %d entries only the byte allowance removed", got, ok, p.ProjectionBudget.LimitationsOmitted)
	}
}
