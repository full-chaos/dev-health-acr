package contextfabric

import "testing"

func TestInvestmentUnitsCursorRoundTripAndRefusal(t *testing.T) {
	t.Parallel()
	in := InvestmentUnitsCursor{Share: 0.1 + 0.2, WorkUnitID: "wu|1", RepoID: "0b8f2a4e-1c3d-4e5f-8a9b-0c1d2e3f4a5b"}
	out, err := DecodeInvestmentUnitsCursor(EncodeInvestmentUnitsCursor(in))
	if err != nil || out != in {
		t.Fatalf("round trip = %+v, %v; want %+v", out, err, in)
	}
	for _, bad := range []string{"", "!!", "e30", "eyJzIjoiTmFOIiwidSI6IngiLCJyIjoieSJ9"} {
		if _, err := DecodeInvestmentUnitsCursor(bad); err == nil {
			t.Errorf("cursor %q decoded", bad)
		}
	}
}
