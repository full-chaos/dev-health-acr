package mcpclientfixtures

import (
	"fmt"
	"strings"
)

// ExtractMarkedBlock returns the literal text between
// "<!-- FIXTURE:name -->" and "<!-- /FIXTURE:name -->" HTML comment
// markers in a Markdown document. The returned text is trimmed of the
// blank lines immediately surrounding the markers but is otherwise
// byte-for-byte as written -- no de-indentation -- so callers compare it
// directly against a canonical Go string using the exact same Markdown
// list-item indentation the checked-in docs use.
func ExtractMarkedBlock(data []byte, name string) (string, error) {
	begin := "<!-- FIXTURE:" + name + " -->"
	end := "<!-- /FIXTURE:" + name + " -->"
	content := string(data)
	beginIdx := strings.Index(content, begin)
	if beginIdx < 0 {
		return "", fmt.Errorf("marker %q not found", begin)
	}
	afterBegin := beginIdx + len(begin)
	endIdx := strings.Index(content[afterBegin:], end)
	if endIdx < 0 {
		return "", fmt.Errorf("marker %q not found after %q", end, begin)
	}
	return strings.Trim(content[afterBegin:afterBegin+endIdx], "\n"), nil
}

// ExtractFencedBlocks returns the content of every ```<lang> ... ``` fenced
// code block in data, in document order. Fence delimiter lines are
// stripped, and any indentation the opening fence line itself carried
// (Markdown list-item nesting, as in a fenced block under a numbered step)
// is stripped from every content line too, so the returned text is always
// flush left regardless of where in the document the fence appears.
func ExtractFencedBlocks(data []byte, lang string) []string {
	fence := "```" + lang
	lines := strings.Split(string(data), "\n")
	var blocks []string
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != fence {
			continue
		}
		indent := leadingWhitespace(lines[i])
		var content []string
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != "```"; i++ {
			content = append(content, strings.TrimPrefix(lines[i], indent))
		}
		blocks = append(blocks, strings.Join(content, "\n"))
	}
	return blocks
}

// ExtractHeredocBlocks returns the content of every `<< 'delimiter'` ...
// `delimiter` heredoc in an already fence-stripped block of shell text
// (see ExtractFencedBlocks), with the heredoc's own leading indentation
// (again, Markdown list-item nesting) stripped from every content line.
// It supports exactly the quoted-delimiter heredoc form
// (`<< 'EOF' ... EOF`) this repository's docs use; an unquoted or
// here-string form is not recognized and simply yields no match.
func ExtractHeredocBlocks(shellBlock string, delimiter string) []string {
	openMarker := "<< '" + delimiter + "'"
	lines := strings.Split(shellBlock, "\n")
	var blocks []string
	for i := 0; i < len(lines); i++ {
		if !strings.Contains(lines[i], openMarker) {
			continue
		}
		var content []string
		var indent string
		if len(lines) > i+1 {
			indent = leadingWhitespace(lines[i+1])
		}
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != delimiter; i++ {
			content = append(content, strings.TrimPrefix(lines[i], indent))
		}
		blocks = append(blocks, strings.Join(content, "\n"))
	}
	return blocks
}

// leadingWhitespace returns the leading run of spaces and tabs in line.
func leadingWhitespace(line string) string {
	trimmedLen := len(line) - len(strings.TrimLeft(line, " \t"))
	return line[:trimmedLen]
}
