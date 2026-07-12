package mcpclientfixtures

import "regexp"

// KnownACRMCPSubcommands is the exact set cmd/acr-mcp/main.go's switch
// dispatches on. A doc referencing anything outside this set is
// referencing a subcommand the real binary does not implement.
var KnownACRMCPSubcommands = map[string]bool{
	"version": true, "--version": true, "-version": true,
	"doctor": true, "diagnostics": true, "metadata": true, "serve": true,
}

// acrMCPInvocation matches a literal "acr-mcp" token followed by a
// same-line subcommand-shaped word, at the start of a line or preceded by
// whitespace, a quote, or a path separator -- so "/path/to/acr-mcp serve"
// and a standalone "acr-mcp doctor" line both match, while an unrelated
// occurrence such as "go build -o acr-mcp ./cmd/acr-mcp" (no
// subcommand-shaped word follows on that line) does not. The whitespace
// between "acr-mcp" and the captured word is restricted to spaces/tabs
// (never \n) so a match can never accidentally cross into the next line's
// first word.
var acrMCPInvocation = regexp.MustCompile(`(?m)(?:^|[ \t"'/])acr-mcp[ \t]+([a-zA-Z-][a-zA-Z0-9_-]*)`)

// ExtractACRMCPInvocations returns every subcommand-shaped word that
// immediately follows a literal "acr-mcp" invocation in text (typically
// the content of one or more fenced bash blocks), in document order.
func ExtractACRMCPInvocations(text string) []string {
	matches := acrMCPInvocation.FindAllStringSubmatch(text, -1)
	invocations := make([]string, 0, len(matches))
	for _, m := range matches {
		invocations = append(invocations, m[1])
	}
	return invocations
}
