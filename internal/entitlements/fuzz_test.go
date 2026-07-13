package entitlements

import (
	"bytes"
	"strings"
	"testing"
)

func FuzzDecodeResponse_enforces_strict_bounded_contract(f *testing.F) {
	valid := `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true}`
	trailing := valid + ` {}`
	duplicate := `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"agent_context_runtime":false}`
	null := `{"schema_version":null,"org_id":"org-1","agent_context_runtime":true}`
	unknown := `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"extra":true}`
	oversized := `{"schema_version":"acr_entitlement.v1","org_id":"` + strings.Repeat("x", 1024) + `","agent_context_runtime":true}`

	for _, payload := range []string{valid, trailing, duplicate, null, unknown, oversized} {
		f.Add([]byte(payload))
	}

	f.Fuzz(func(t *testing.T, input []byte) {
		// When
		decoded, err := decodeResponse(bytes.NewReader(input), 1024)

		// Then
		switch string(input) {
		case valid:
			if err != nil || decoded.SchemaVersion != entitlementSchemaVersion || decoded.OrgID != "org-1" || !decoded.AgentContextRuntime {
				t.Fatalf("decodeResponse(valid) = %#v, %v; want valid known contract", decoded, err)
			}
		case trailing, duplicate, null, unknown, oversized:
			if err == nil {
				t.Fatalf("decodeResponse accepted invalid contract payload %q", input)
			}
		}
	})
}
