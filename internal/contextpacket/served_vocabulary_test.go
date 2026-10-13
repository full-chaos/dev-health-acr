package contextpacket

import (
	"encoding/json"
	"os"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

func TestValidatePacketRejectsAnItemOutsideTheServedVocabulary(t *testing.T) {
	raw, err := os.ReadFile(repopath.Path(t, "contracts", "examples", "v1", "context_packet.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var packet contractsv1.ContextPacket
	if err := json.Unmarshal(raw, &packet); err != nil {
		t.Fatal(err)
	}
	packet.Budget.ItemsUsed = len(packet.Items)
	if err := validatePacket(packet); err != nil {
		t.Fatalf("the golden packet must pass the builder check: %v", err)
	}
	packet.Items[0].Category = contractsv1.PacketCategory("value_added_after_the_client_cached_the_schema")
	if err := validatePacket(packet); err == nil {
		t.Fatal("the packet builder accepts an item category outside the served vocabulary")
	}
}
