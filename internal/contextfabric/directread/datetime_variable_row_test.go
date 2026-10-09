package directread_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

func TestDateOnlyForDateTimeVariableIsRefusedBeforeUpstream(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	hot, _ := cat.Lookup("hotspots")
	vars := opMinimalVariables(t, hot)
	calls := 0
	h := newOpHarness(t, func(opRecorded) (int, string) { calls++; return 200, `{"data":{"hotspots":{"rows":[]}}}` }, opHarnessOptions{})
	vars["input"].(map[string]any)["sinceUtc"] = "2026-09-08"
	raw, _ := json.Marshal(vars)
	resp, err := h.runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "hotspots", Variables: raw})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(resp)
	if calls != 0 || resp.Call != directread.CallRefused || !strings.Contains(string(out), "input.sinceUtc") || !strings.Contains(string(out), "RFC3339") {
		t.Fatalf("calls=%d %s", calls, out)
	}
}

func TestUpstreamGraphQLErrorMessageAndPathReachErrorsEntry(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	hot, _ := cat.Lookup("hotspots")
	vars := opMinimalVariables(t, hot)
	h := newOpHarness(t, func(opRecorded) (int, string) {
		return 200, `{"data":null,"errors":[{"message":"parsing time boom","path":["hotspots","input","sinceUtc"]}]}`
	}, opHarnessOptions{})
	raw, _ := json.Marshal(vars)
	resp, err := h.runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "hotspots", Variables: raw})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(resp)
	if !strings.Contains(string(out), "parsing time boom") || !strings.Contains(string(out), "sinceUtc") {
		t.Fatalf("errors[] lacks upstream message/path: %s", out)
	}
	if !strings.Contains(h.logs.String(), `"error_message":"hotspots.input.sinceUtc: parsing time boom"`) {
		t.Fatalf("log line lacks error_message:\n%s", h.logs.String())
	}
}

func TestTemporalScalarForms(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	hot, _ := cat.Lookup("hotspots")
	for _, tc := range []struct {
		value   any
		refused bool
		want    string
	}{
		{"2026-09-08T00:00:00Z", false, ""},
		{"2026-09-08T00:00:00.5+02:00", false, ""},
		{"2026-09-08", true, "got a date"},
		{"yesterday", true, "not a timestamp"},
		{float64(5), true, "RFC3339 timestamp, e.g. 2026-09-08T00:00:00Z; got a number"},
		{true, true, "got a boolean"},
		{"2026-09-08T00:00:00,5Z", true, "not a timestamp"},
	} {
		vars := opMinimalVariables(t, hot)
		in := vars["input"].(map[string]any)
		in["sinceUtc"] = "2026-09-07T00:00:00Z"
		in["untilUtc"] = tc.value
		h := newOpHarness(t, func(opRecorded) (int, string) { return 200, `{"data":{"hotspots":{"rows":[]}}}` }, opHarnessOptions{})
		raw, _ := json.Marshal(vars)
		resp, _ := h.runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "hotspots", Variables: raw})
		out, _ := json.Marshal(resp)
		if (resp.Call == directread.CallRefused) != tc.refused || !strings.Contains(string(out), tc.want) {
			t.Fatalf("%v: %s", tc.value, out)
		}
	}
}

func TestGraphQLErrorTextOnlyFromStatus200(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	hot, _ := cat.Lookup("hotspots")
	vars := opMinimalVariables(t, hot)
	for _, status := range []int{200, 201, 206} {
		h := newOpHarness(t, func(opRecorded) (int, string) {
			return status, `{"data":null,"errors":[{"message":"upstream words","path":["hotspots"]}]}`
		}, opHarnessOptions{})
		raw, _ := json.Marshal(vars)
		resp, _ := h.runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "hotspots", Variables: raw})
		out, _ := json.Marshal(resp)
		if resp.Call != directread.CallUpstreamError || len(resp.Errors) != 1 || resp.Errors[0].Class != directread.UpstreamGraphQLErrors {
			t.Fatalf("%d: %s", status, out)
		}
		if carried := strings.Contains(string(out), "upstream words"); carried != (status == 200) {
			t.Fatalf("status %d carried=%v: %s", status, carried, out)
		}
		if logged := strings.Contains(h.logs.String(), "upstream words"); logged != (status == 200) {
			t.Fatalf("status %d logged=%v", status, logged)
		}
	}
}
