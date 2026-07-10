package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestUnknownCommand(t *testing.T) {
	err := run([]string{"unknown"})
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVersionCommand(t *testing.T) {
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = original })
	if err := run([]string{"version"}); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	var output bytes.Buffer
	_, _ = io.Copy(&output, reader)
	_ = reader.Close()
	if strings.TrimSpace(output.String()) == "" {
		t.Fatal("version output was empty")
	}
}
