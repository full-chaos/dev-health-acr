package outboundhttp

import (
	"encoding/json"
	"net/netip"
	"os"
	"testing"
)

type opsAddressOracle struct {
	OpsCommit string   `json:"ops_commit"`
	Rows      [][2]any `json:"rows"`
}

// acrStricterThanOps are the prefixes acr refuses and ops accepts: the only addresses on which the two may differ.
var acrStricterThanOps = []netip.Prefix{
	netip.MustParsePrefix("2001:20::/28"),
}

// TestPublicAddressNeverAcceptsWhatOpsRefuses runs against rows produced by EXECUTING ops' isSafePublicIP at the
// recorded commit (testdata/ops_public_address_oracle.json): an address ops refuses is refused here, and an address
// acr refuses that ops accepts lies in a pinned prefix.
func TestPublicAddressNeverAcceptsWhatOpsRefuses(t *testing.T) {
	raw, err := os.ReadFile("testdata/ops_public_address_oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle opsAddressOracle
	if err := json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	if oracle.OpsCommit != "69e7179d70573959df2586f1d8ba090e30fccb01" || len(oracle.Rows) < 1000 {
		t.Fatalf("oracle commit %q rows %d: want the recorded ops commit and the full row set", oracle.OpsCommit, len(oracle.Rows))
	}
	var opsRefuses, opsAccepts, stricter int
	stricterHit := map[netip.Prefix]bool{}
	for _, row := range oracle.Rows {
		text, _ := row[0].(string)
		opsSafe, ok := row[1].(bool)
		if !ok {
			t.Fatalf("row %v: want [address, bool]", row)
		}
		address, err := netip.ParseAddr(text)
		if err != nil {
			t.Fatalf("row %v: %v", row, err)
		}
		acrSafe := PublicAddress(address)
		switch {
		case !opsSafe:
			opsRefuses++
			if acrSafe {
				t.Errorf("%s: ops refuses it, PublicAddress accepts it", text)
			}
		case !acrSafe:
			opsAccepts++
			listed := false
			for _, prefix := range acrStricterThanOps {
				if prefix.Contains(address.Unmap()) {
					listed, stricterHit[prefix] = true, true
				}
			}
			if !listed {
				t.Errorf("%s: ops accepts it, PublicAddress refuses it, and no pinned stricter prefix holds it", text)
			}
			stricter++
		default:
			opsAccepts++
		}
	}
	for _, prefix := range acrStricterThanOps {
		if !stricterHit[prefix] {
			t.Errorf("pinned stricter prefix %s has no row where acr is stricter than ops: remove it or regenerate the oracle", prefix)
		}
	}
	if opsRefuses < 300 || opsAccepts < 300 || stricter == 0 {
		t.Fatalf("oracle rows: ops refuses %d, ops accepts %d, acr stricter %d: too few to mean anything", opsRefuses, opsAccepts, stricter)
	}
}
