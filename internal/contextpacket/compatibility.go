package contextpacket

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

type CompatibilityOutcome string

const (
	CompatibilityUnknown      CompatibilityOutcome = "unknown"
	CompatibilityCompatible   CompatibilityOutcome = "compatible"
	CompatibilityIncompatible CompatibilityOutcome = "incompatible"
)

func packetCompatibility(request contractsv1.ContextPacketRequest, packet contractsv1.ContextPacket) (CompatibilityOutcome, bool) {
	schemaMismatch := packet.SchemaVersion != contractsv1.ContextPacketSchema
	if request.Client.SidecarVersion == "" {
		return CompatibilityUnknown, schemaMismatch
	}
	if !version.IsValid(request.Client.SidecarVersion) || !version.IsValid(packet.Compatibility.MinimumSidecarVersion) {
		return CompatibilityUnknown, true
	}
	if !version.AtLeast(request.Client.SidecarVersion, packet.Compatibility.MinimumSidecarVersion) || schemaMismatch {
		return CompatibilityIncompatible, true
	}
	return CompatibilityCompatible, false
}
