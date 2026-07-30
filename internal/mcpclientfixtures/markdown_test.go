package mcpclientfixtures

import (
	"reflect"
	"testing"
)

func TestExtractMarkedBlockReturnsExactContentTrimmedOfSurroundingBlankLines(t *testing.T) {
	doc := "prose before\n\n<!-- FIXTURE:x -->\n\nhello\nworld\n\n<!-- /FIXTURE:x -->\n\nprose after\n"
	got, err := ExtractMarkedBlock([]byte(doc), "x")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello\nworld" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractMarkedBlockFailsClosedWhenMarkerMissing(t *testing.T) {
	if _, err := ExtractMarkedBlock([]byte("no markers here"), "x"); err == nil {
		t.Fatal("expected an error when the begin marker is absent")
	}
	if _, err := ExtractMarkedBlock([]byte("<!-- FIXTURE:x -->\nunterminated"), "x"); err == nil {
		t.Fatal("expected an error when the end marker is absent")
	}
}

func TestExtractFencedBlocksStripsFenceAndListIndentation(t *testing.T) {
	doc := "1. Step:\n   ```bash\n   echo one\n   echo two\n   ```\n\nplain:\n```bash\necho three\n```\n"
	got := ExtractFencedBlocks([]byte(doc), "bash")
	want := []string{"echo one\necho two", "echo three"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExtractHeredocBlocksStripsDelimiterAndListIndentation(t *testing.T) {
	shellBlock := "mkdir -p .cursor\ncat > .cursor/mcp.json << 'EOF'\n{\n  \"a\": 1\n}\nEOF\n"
	got := ExtractHeredocBlocks(shellBlock, "EOF")
	want := []string{"{\n  \"a\": 1\n}"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExtractACRMCPInvocationsFindsRealInvocationsOnly(t *testing.T) {
	text := "cd /path/to/acr\n" +
		"go build -o acr-mcp ./cmd/acr-mcp\n" +
		"go build -o acr-mcp ./cmd/acr-mcp\n" +
		"/path/to/acr-mcp serve\n" +
		"acr-mcp doctor\n" +
		"acr-mcp diagnostics --output ./x.tar --live\n" +
		"acr-mcp login\n" +
		"acr-mcp logout\n"
	got := ExtractACRMCPInvocations(text)
	want := []string{"serve", "doctor", "diagnostics", "login", "logout"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v -- a false match here would mean the regex crossed a newline or matched a build-output path", got, want)
	}
	for _, invocation := range got {
		if !KnownACRMCPSubcommands[invocation] {
			t.Fatalf("extracted invocation %q is not in KnownACRMCPSubcommands", invocation)
		}
	}
}

func TestExtractACRMCPInvocationsFlagsAnUnknownSubcommand(t *testing.T) {
	got := ExtractACRMCPInvocations("acr-mcp frobnicate")
	if len(got) != 1 || got[0] != "frobnicate" {
		t.Fatalf("expected to extract the stale subcommand token itself, got %#v", got)
	}
	if KnownACRMCPSubcommands[got[0]] {
		t.Fatal("expected \"frobnicate\" to not be in KnownACRMCPSubcommands -- this is the staleness canary")
	}
}
