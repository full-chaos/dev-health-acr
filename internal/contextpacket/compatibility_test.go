package contextpacket

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestPacketCompatibilityClassifiesSidecarVersions(t *testing.T) {
	packet := contractsv1.ContextPacket{SchemaVersion: contractsv1.ContextPacketSchema, Compatibility: contractsv1.Compatibility{MinimumSidecarVersion: "1.2.3"}}
	tests := []struct {
		name     string
		version  string
		want     CompatibilityOutcome
		mismatch bool
	}{
		{name: "compatible", version: "1.2.4", want: CompatibilityCompatible},
		{name: "incompatible", version: "1.2.2", want: CompatibilityIncompatible, mismatch: true},
		{name: "invalid", version: "secret-version", want: CompatibilityUnknown, mismatch: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome, mismatch := packetCompatibility(contractsv1.ContextPacketRequest{Client: contractsv1.ClientInfo{SidecarVersion: test.version}}, packet)
			if outcome != test.want || mismatch != test.mismatch {
				t.Fatalf("compatibility = %q mismatch=%v", outcome, mismatch)
			}
		})
	}
}
