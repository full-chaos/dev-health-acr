package contextpacket

import (
	"strconv"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
	client, clientOK := coreVersion(request.Client.SidecarVersion)
	minimum, minimumOK := coreVersion(packet.Compatibility.MinimumSidecarVersion)
	if !clientOK || !minimumOK {
		return CompatibilityUnknown, true
	}
	if versionLess(client, minimum) || schemaMismatch {
		return CompatibilityIncompatible, true
	}
	return CompatibilityCompatible, false
}

func versionLess(left, right [3]int) bool {
	for index := range left {
		if left[index] != right[index] {
			return left[index] < right[index]
		}
	}
	return false
}

func coreVersion(value string) ([3]int, bool) {
	var parsed [3]int
	core := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(value), "v"), "-", 2)[0]
	parts := strings.Split(core, ".")
	if len(parts) != len(parsed) {
		return parsed, false
	}
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return [3]int{}, false
		}
		parsed[index] = number
	}
	return parsed, true
}
