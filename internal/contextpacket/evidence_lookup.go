package contextpacket

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func evidenceLookupDigest(evidence contractsv1.EvidenceRef) []byte {
	confidence := make([]byte, 8)
	binary.LittleEndian.PutUint64(confidence, math.Float64bits(evidence.Confidence))
	values := []string{
		evidence.EvidenceRefID,
		evidence.Source.System,
		evidence.Source.EntityType,
		evidence.Source.EntityID,
		evidence.Source.DisplayLabel,
		evidence.Source.SafeURI,
		evidence.Provenance,
		hex.EncodeToString(confidence),
		evidence.Citation,
		strconv.FormatInt(evidence.ObservedAt.UTC().UnixMilli(), 10),
	}
	digest := sha256.New()
	for _, value := range values {
		_, _ = fmt.Fprintf(digest, "%d:%s", len(value), value)
	}
	return digest.Sum(nil)
}

func evidenceLookupHashSQL() string {
	fields := []string{
		"evidence_ref_id",
		"system",
		"entity_type",
		"entity_id",
		"display_label",
		"safe_uri",
		"provenance",
		"lower(hex(reinterpretAsString(toFloat64(confidence))))",
		"citation",
		"toString(toUnixTimestamp64Milli(toDateTime64(observed_at, 3, 'UTC')))",
	}
	framed := make([]string, 0, len(fields)*3)
	for _, field := range fields {
		framed = append(framed, "toString(length("+field+"))", "':'", field)
	}
	return "lower(hex(SHA256(concat(" + strings.Join(framed, ", ") + "))))"
}
