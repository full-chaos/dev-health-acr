package contextfabric

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the probe's zone must load on a host with no zoneinfo

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The lead's ruling on CHAOS-12608: the forecast summary serves computed_at as
// an RFC 3339 UTC instant and every date as an RFC 3339 full-date. The fact
// carries capacity_forecasts.computed_at as ClickHouse prints a
// DateTime64(3, 'UTC'), with no zone.

func TestTheSummaryInstantIsRFC3339UTC(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		ok        bool
	}{
		{"2026-11-02 04:00:00.000", "2026-11-02T04:00:00Z", true},
		{"2026-11-02 04:00:00.123", "2026-11-02T04:00:00.123Z", true},
		{"2026-11-02 04:00:00", "2026-11-02T04:00:00Z", true},
		{"2026-11-02T04:00:00Z", "2026-11-02T04:00:00Z", true},
		{"2026-11-03T01:00:00+02:00", "2026-11-02T23:00:00Z", true},
		{"2026-11-02", "", false},
		{"", "", false},
		{"not a time", "", false},
	} {
		got, ok := forecastInstant(tc.raw)
		if got != tc.want || ok != tc.ok {
			t.Errorf("forecastInstant(%q) = %q, %v; want %q, %v", tc.raw, got, ok, tc.want, tc.ok)
		}
		if ok && !contractsv1.ValidContextFabricForecastComputedAt(got) {
			t.Errorf("forecastInstant(%q) = %q, which the contract refuses", tc.raw, got)
		}
	}
}

func TestTheSummaryDatesAreFullDatesOrAbsent(t *testing.T) {
	fact := summaryFact("scope-a", "available", days(12), nil)
	for value, want := range map[string]string{
		"2026-11-13":          "2026-11-13",
		"2026-11-13 00:00:00": "",
		"2026-02-30":          "",
	} {
		fact.Fields["forecast_p50_date"] = StringFactValue(value)
		if got := completionForecastOf(fact); got == nil || got.P50Date != want {
			t.Errorf("p50 date %q: summary = %+v, want %q", value, got, want)
		}
	}
}

func TestASummaryWhoseInstantCannotBeReadIsNotAttached(t *testing.T) {
	fact := summaryFact("scope-a", "available", days(12), nil)
	fact.Fields["computed_at"] = StringFactValue("not a time")
	if got := completionForecastOf(fact); got != nil {
		t.Fatalf("summary = %+v, want none: its instant cannot be stated", got)
	}
}

// forecastZoneProbeEnv runs TestTheSummaryInstantIsUTCInAnyZone's assertions
// in a child test process whose zone is +14:00.
const forecastZoneProbeEnv = "ACR_FORECAST_ZONE_PROBE"

// TestTheSummaryInstantIsUTCInAnyZone runs the disclosure in a process whose
// local zone is Pacific/Kiritimati (+14:00), where 23:30 UTC is already the
// next day. time.Local is fixed when a process starts, so only a child
// process measures this; the child must report that it ran in that zone, or
// the test fails.
func TestTheSummaryInstantIsUTCInAnyZone(t *testing.T) {
	if os.Getenv(forecastZoneProbeEnv) == "1" {
		forecastZoneProbe(t)
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestTheSummaryInstantIsUTCInAnyZone$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), forecastZoneProbeEnv+"=1", "TZ=Pacific/Kiritimati")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("the probe in a +14:00 zone failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "forecast zone probe: +14:00 ok") {
		t.Fatalf("the probe did not report a run in the +14:00 zone; a measurement that did not happen is no pass:\n%s", output)
	}
}

func forecastZoneProbe(t *testing.T) {
	late := time.Date(2026, 11, 2, 23, 30, 0, 0, time.UTC)
	if _, offset := late.In(time.Local).Zone(); offset != 14*60*60 {
		t.Fatalf("local zone offset = %d s (TZ=%q), want +14:00: this probe measured nothing", offset, os.Getenv("TZ"))
	}
	fact := summaryFact("scope-a", "available", days(12), nil)
	fact.Fields["computed_at"] = StringFactValue("2026-11-02 23:30:00.000")
	summary := completionForecastOf(fact)
	if summary == nil || summary.ComputedAt != "2026-11-02T23:30:00Z" {
		t.Fatalf("summary = %+v, want computed_at 2026-11-02T23:30:00Z: read in UTC, not in the host's zone", summary)
	}
	instant, err := time.Parse(time.RFC3339Nano, summary.ComputedAt)
	if err != nil || instant.UTC().Day() != 2 {
		t.Fatalf("computed_at %q reads as UTC day %d (%v), want 2", summary.ComputedAt, instant.UTC().Day(), err)
	}
	if summary.P50Date != "2026-11-13" {
		t.Fatalf("p50 date = %q, want the stored day", summary.P50Date)
	}
	t.Log("forecast zone probe: +14:00 ok")
}
