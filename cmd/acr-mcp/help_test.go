package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestHelpAliasesPrintUsage(t *testing.T) {
	for _, alias := range []string{"help", "-h", "--help"} {
		t.Run(alias, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=TestHelpProcess", "--", alias)
			command.Env = append(os.Environ(), "ACR_MCP_HELP_PROCESS=1")
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("help command failed: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), "Usage: acr-mcp") {
				t.Fatalf("help output missing usage: %s", output)
			}
		})
	}
}

func TestHelpProcess(t *testing.T) {
	if os.Getenv("ACR_MCP_HELP_PROCESS") != "1" {
		return
	}
	args := os.Args
	for index, arg := range args {
		if arg == "--" {
			os.Args = append([]string{"acr-mcp"}, args[index+1:]...)
			main()
			return
		}
	}
	t.Fatal("missing command separator")
}
