package main

import (
	"fmt"
	"strings"
)

// splitShellWords splits s into words using POSIX-shell-like quoting rules, plus bash's
// $'...' ANSI-C quoting extension. The orchestrator builds --probe-command with bash's
// printf '%q ', which for the plain-ASCII command-prefix words this tool receives
// (compose exec -T clickhouse clickhouse-client --user default --password ch --database
// <db> --query) never needs anything fancier than plain words, but %q falls back to
// single-quoting or $'...' the moment a word contains a character a shell would treat
// specially, so a real splitter is required rather than strings.Fields.
//
// Supported forms:
//   - unquoted words, whitespace-separated; a backslash escapes the following character
//   - 'single quoted' — every character literal, no escapes recognized
//   - "double quoted" — backslash escapes \, $, `, ", and newline; other characters literal
//   - $'ansi-c quoted' — backslash escape sequences (\n \t \r \\ \' \" \a \b \f \v \0 \xHH)
//
// Quote forms may be concatenated without whitespace to form a single word, exactly as a
// POSIX shell does (e.g. foo'bar'"baz" -> one word "foobarbaz").
func splitShellWords(s string) ([]string, error) {
	var words []string
	var current strings.Builder
	haveCurrent := false
	runes := []rune(s)
	i := 0
	n := len(runes)

	flush := func() {
		if haveCurrent {
			words = append(words, current.String())
			current.Reset()
			haveCurrent = false
		}
	}

	for i < n {
		c := runes[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			flush()
			i++
		case c == '\'':
			haveCurrent = true
			j := i + 1
			for j < n && runes[j] != '\'' {
				current.WriteRune(runes[j])
				j++
			}
			if j >= n {
				return nil, fmt.Errorf("shellwords: unterminated single quote starting at byte %d", i)
			}
			i = j + 1
		case c == '"':
			haveCurrent = true
			j := i + 1
			for j < n && runes[j] != '"' {
				if runes[j] == '\\' && j+1 < n && strings.ContainsRune(`\$"`+"`\n", runes[j+1]) {
					current.WriteRune(runes[j+1])
					j += 2
					continue
				}
				current.WriteRune(runes[j])
				j++
			}
			if j >= n {
				return nil, fmt.Errorf("shellwords: unterminated double quote starting at byte %d", i)
			}
			i = j + 1
		case c == '$' && i+1 < n && runes[i+1] == '\'':
			haveCurrent = true
			j := i + 2
			for j < n && runes[j] != '\'' {
				if runes[j] == '\\' && j+1 < n {
					decoded, consumed := decodeANSICEscape(runes[j+1:])
					current.WriteString(decoded)
					j += 1 + consumed
					continue
				}
				current.WriteRune(runes[j])
				j++
			}
			if j >= n {
				return nil, fmt.Errorf("shellwords: unterminated $'' quote starting at byte %d", i)
			}
			i = j + 1
		case c == '\\':
			haveCurrent = true
			if i+1 >= n {
				return nil, fmt.Errorf("shellwords: dangling backslash at end of input")
			}
			current.WriteRune(runes[i+1])
			i += 2
		default:
			haveCurrent = true
			current.WriteRune(c)
			i++
		}
	}
	flush()
	return words, nil
}

// decodeANSICEscape decodes a single backslash escape sequence (the runes following the
// backslash) as bash's $'...' quoting would, returning the decoded text and the number of
// input runes consumed (not counting the backslash itself).
func decodeANSICEscape(rest []rune) (string, int) {
	if len(rest) == 0 {
		return "\\", 0
	}
	switch rest[0] {
	case 'n':
		return "\n", 1
	case 't':
		return "\t", 1
	case 'r':
		return "\r", 1
	case 'a':
		return "\a", 1
	case 'b':
		return "\b", 1
	case 'f':
		return "\f", 1
	case 'v':
		return "\v", 1
	case '\\':
		return "\\", 1
	case '\'':
		return "'", 1
	case '"':
		return "\"", 1
	case 'x':
		hex := ""
		j := 1
		for j < len(rest) && j <= 2 && isHexDigit(rest[j]) {
			hex += string(rest[j])
			j++
		}
		if hex == "" {
			return "x", 1
		}
		var v int
		fmt.Sscanf(hex, "%x", &v)
		return string(rune(v)), j
	default:
		return string(rest[0]), 1
	}
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}
