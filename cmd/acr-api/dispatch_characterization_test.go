package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRun_dispatches_credentials_help_without_starting_server(t *testing.T) {
	// Given
	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	t.Cleanup(func() { os.Stderr = original })

	// When
	err = run([]string{"credentials", "--help"})
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	var output bytes.Buffer
	if _, copyErr := io.Copy(&output, reader); copyErr != nil {
		t.Fatal(copyErr)
	}
	if closeErr := reader.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "credentials <command>") {
		t.Fatalf("stderr = %q, want credentials help", output.String())
	}
}
