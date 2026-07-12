package mcpclientfixtures

import "strings"

// doctorGateNoteRaw is DoctorGateNote's source text; see
// installSidecarSnippetRaw's doc comment for why the "@BT@" placeholder
// stands in for a literal backtick here.
const doctorGateNoteRaw = `Local flags grant no server authorization; the hosted API is the authority. The connected MCP client's tools/list response is the authoritative runtime tool surface. acr-mcp metadata is a static, network-free description of the default surface and does not report live registration; @BT@doctor@BT@ diagnoses the hosted gates automatically once local configuration is valid (network-free otherwise), @BT@doctor --offline@BT@ forces a network-free check regardless of configuration validity, and @BT@doctor --live@BT@ is an explicit, equivalent alias for that automatic behavior.`

// DoctorGateNote is the single canonical source of the trailing clause
// every guide under docs/examples/mcp-clients/ embeds verbatim at the end
// of its ACR_ENABLE_WRITEBACK bullet (marked with
// "<!-- FIXTURE:doctor-gate-note -->" / "<!-- /FIXTURE:doctor-gate-note -->"
// HTML comments): it describes exactly cmd/acr-mcp/main.go's current
// CLI dispatch -- plain `doctor` attempts a live capabilities handshake
// only after static local configuration is already valid,
// `doctor --offline` is guaranteed network-free regardless, and
// `doctor --live` remains a valid, explicit alias that behaves
// identically to the default. A future change to that dispatch logic
// that isn't reflected here fails this package's marker-parity test
// instead of silently leaving every guide's prose stale.
var DoctorGateNote = strings.ReplaceAll(doctorGateNoteRaw, "@BT@", "`")
