package mcpclientfixtures

// BundleShareCaution is the single canonical source of the short clause
// every guide under docs/examples/mcp-clients/ uses when pointing a
// reader at `acr-mcp diagnostics`/`doctor --bundle` in its "Next Steps"
// list (marked with "<!-- FIXTURE:bundle-share-caution -->" /
// "<!-- /FIXTURE:bundle-share-caution -->" HTML comments). Being
// secrets-free (see internal/diagnostics's canary tests) does not make a
// bundle safe for a public audience: it still identifies the requesting
// organization's sidecar deployment, so every mention of sharing a bundle
// must name an approved private support channel, never a generic public
// "issue" or "issue tracker" -- matching internal/diagnostics/readme.go's
// own "## Sharing this bundle" section embedded in the bundle itself.
const BundleShareCaution = "a bundle safe to share only through an approved private support channel (never a public issue tracker)"
