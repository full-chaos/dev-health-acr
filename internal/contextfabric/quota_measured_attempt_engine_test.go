package contextfabric

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The class-B test, driven through the REAL engine on EVERY terminal arm.
//
// The history this exists against is three review rounds of one shape. The
// quota was written at three sites and read at none. It was then read at the
// served-narrowing emitter and dropped on BOTH refusal arms. It was then
// present on the refusal arms and read from an EMPTY value on the arm where the
// retry succeeded -- a two-group answer served with `quota_groups=0`.
//
// Every one of those is the same defect: exposure lived on an OPTIONAL
// narrowing attempt, so an arm that did not narrow had nothing to carry, and
// the arm that FITS -- the majority path -- never computed one at all. A test
// that drove one arm and asserted three numbers would have passed each time.
//
// So this drives the same five arms the attribution sweep drives, from the same
// case table, and asserts what every arm must be able to say about the document
// it describes. An arm added without a measured attempt fails here, and an arm
// added without a case fails the population check the attribution sweep already
// makes against the stamping sites.

var quotaFieldPattern = regexp.MustCompile(`\b(quota_availability|ledger_status)=("?)([a-z_]+)("?)`)

var quotaCountPattern = regexp.MustCompile(`\bquota_(group_allowance|groups_granted|groups_measured|groups_over_allowance)=(-?\d+)`)

// quotaFieldsOf reads the ledger and quota dimensions off ONE emitted line.
//
// It reads the LINE TEXT, never the event struct: a field populated on the
// struct and never logged is not telemetry, and this seam has shipped exactly
// that -- three write sites and no reader.
func quotaFieldsOf(t *testing.T, line string) (map[string]string, map[string]int) {
	t.Helper()
	tokens := map[string]string{}
	for _, match := range quotaFieldPattern.FindAllStringSubmatch(line, -1) {
		tokens[match[1]] = match[3]
	}
	counts := map[string]int{}
	for _, match := range quotaCountPattern.FindAllStringSubmatch(line, -1) {
		value, err := strconv.Atoi(match[2])
		if err != nil {
			t.Fatalf("quota_%s is not an integer on the emitted line: %v", match[1], err)
		}
		counts[match[1]] = value
	}
	for _, name := range []string{"quota_availability", "ledger_status"} {
		if _, ok := tokens[name]; !ok {
			t.Fatalf("the emitted line carries no %s at all -- enforcement is told nothing about the "+
				"account or the quota of the document this line describes.\nline: %s", name, line)
		}
	}
	for _, name := range []string{"group_allowance", "groups_granted", "groups_measured", "groups_over_allowance"} {
		if _, ok := counts[name]; !ok {
			t.Fatalf("the emitted line carries no quota_%s.\nline: %s", name, line)
		}
	}
	return tokens, counts
}

// TestEveryAssembledResultArmReportsAMeasuredQuota is the behavioural sweep.
func TestEveryAssembledResultArmReportsAMeasuredQuota(t *testing.T) {
	t.Parallel()
	cases := assembledResultArmCases()
	if len(cases) == 0 {
		t.Fatal("no arms are driven, so this test asserts nothing")
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()
			var sink bytes.Buffer
			cohortSizes := []int{}
			result, servedDocument := one.drive(t, &sink, one.spec, &cohortSizes)

			line := ""
			for _, candidate := range strings.Split(sink.String(), "\n") {
				if strings.Contains(candidate, "context fabric plan narrowing") &&
					strings.Contains(candidate, "stage=assembled_result") &&
					strings.Contains(candidate, one.discriminator) {
					line = candidate
				}
			}
			if line == "" {
				t.Fatalf("no assembled_result line carrying %q was emitted: this fixture did not reach the "+
					"arm it claims to test.\nemitted:\n%s", one.discriminator, sink.String())
			}

			tokens, counts := quotaFieldsOf(t, line)

			// UNCLASSIFIED IS THE FAILURE, and it is the whole point of the
			// availability vocabulary: the sink fails closed on a value
			// outside it, and the UNSET zero value of an attempt nobody
			// measured lands there. An arm that forgot to build its attempt
			// says `unclassified` here rather than three innocent zeros.
			availability := tokens["quota_availability"]
			if availability == "unclassified" || availability == "" {
				t.Fatalf("quota_availability = %q: this arm emitted a quota nothing measured.\nline: %s",
					availability, line)
			}
			if !ValidItemQuotaAvailability(ItemQuotaAvailability(availability)) {
				t.Fatalf("quota_availability = %q is outside the closed vocabulary.\nline: %s", availability, line)
			}
			// Every one of these fixtures runs under a POSITIVE item ceiling,
			// so "no ceiling at all" is not an answer any of them may give.
			if availability == string(ItemQuotaUnbounded) {
				t.Errorf("quota_availability = unbounded on an arm driven with a positive ceiling.\nline: %s", line)
			}
			// And the account of the document this line describes must
			// reconcile: a disagreement takes the typed-error exit and never
			// reaches a narrowing line at all.
			if status := tokens["ledger_status"]; status != string(contractsv1.ContextFabricLedgerReconciled) {
				t.Errorf("ledger_status = %q, want %q.\nline: %s", status, contractsv1.ContextFabricLedgerReconciled, line)
			}

			// The two group counts are separate facts and must both be
			// present. A retry narrows the cohort, so an arm whose measured
			// count differs from its granted one is measuring a document the
			// grants were not written for -- publishing one number for both
			// would hide exactly that.
			granted, measured := counts["groups_granted"], counts["groups_measured"]
			if granted < 0 || measured < 0 {
				t.Errorf("negative group counts: granted %d measured %d.\nline: %s", granted, measured, line)
			}
			if counts["groups_over_allowance"] > measured {
				t.Errorf("quota_groups_over_allowance = %d exceeds the %d groups measured: incidence was "+
					"counted against something other than the measured groups.\nline: %s",
					counts["groups_over_allowance"], measured, line)
			}
			// An answer with a group axis must report one. `unavailable` is
			// for an answer that HAS no group axis, and reporting it for one
			// that does is the absence that used to be indistinguishable from
			// a measured zero.
			if servedDocument && result.Cohort != nil && len(result.Cohort.Groups) > 0 {
				if availability == string(ItemQuotaUnavailable) {
					t.Errorf("the served document carries %d groups and the line says the quota is "+
						"unavailable.\nline: %s", len(result.Cohort.Groups), line)
				}
				if measured != len(result.Cohort.Groups) {
					t.Errorf("quota_groups_measured = %d but the served document carries %d groups.\nline: %s",
						measured, len(result.Cohort.Groups), line)
				}
			}
			if availability == string(ItemQuotaBoundedZero) && counts["group_allowance"] != 0 {
				t.Errorf("availability says bounded_zero and the allowance is %d.\nline: %s",
					counts["group_allowance"], line)
			}
			if availability == string(ItemQuotaBounded) && counts["group_allowance"] <= 0 {
				t.Errorf("availability says bounded and the allowance is %d: a zero allowance is "+
					"bounded_zero, which is a different statement.\nline: %s", counts["group_allowance"], line)
			}
		})
	}
}

// TestTheFittingArmMeasuresItsQuotaToo is L1 stated as a single assertion.
//
// It is separated from the sweep above because it is the finding the sweep
// exists to make impossible, and because it is the MAJORITY path: exposure was
// reachable only from inside the narrowing function, whose call sites are both
// behind an overrun guard, so on every answer that FITS the quota fields were
// zero because nothing had produced them -- not because nothing was over.
func TestTheFittingArmMeasuresItsQuotaToo(t *testing.T) {
	t.Parallel()
	var fitting *assembledResultArmCase
	cases := assembledResultArmCases()
	for index := range cases {
		if strings.Contains(cases[index].discriminator, "overrun=fits") {
			fitting = &cases[index]
			break
		}
	}
	if fitting == nil {
		t.Fatal("no arm case drives a measured FIT, so the majority path is untested")
	}

	var sink bytes.Buffer
	cohortSizes := []int{}
	result, served := fitting.drive(t, &sink, fitting.spec, &cohortSizes)
	if !served {
		t.Fatal("the fitting arm served no document")
	}
	line := assembledResultLine(t, sink.String())

	tokens, counts := quotaFieldsOf(t, line)
	if tokens["quota_availability"] == "unclassified" {
		t.Fatalf("the fitting answer emitted an unmeasured quota.\nline: %s", line)
	}
	if result.Cohort != nil && len(result.Cohort.Groups) > 0 {
		if counts["groups_granted"] == 0 && counts["groups_measured"] == 0 {
			t.Fatalf("a grouped answer that FITS reported no groups at all: this is the zero that meant "+
				"'never computed' being read as 'measured none'.\nline: %s", line)
		}
	}
}
