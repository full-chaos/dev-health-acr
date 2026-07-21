package releasebuild

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBuild_rejects_in_root_output_outside_tmp(t *testing.T) {
	// Given
	root := t.TempDir()
	writeClientSourceAt(t, root)
	output := filepath.Join(root, "dist", "release")
	builder := NewBuilder(CompilerFunc(writeTestBinary))

	// When
	_, err := builder.Build(context.Background(), Request{SourceDir: root, OutputDir: output, Identity: testIdentity()})

	// Then
	if err == nil {
		t.Fatal("Build() error = nil, want in-root output rejection")
	}
	if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output path was created: %v", statErr)
	}
}

func TestBuild_allows_in_root_tmp_output(t *testing.T) {
	// Given
	root := t.TempDir()
	writeClientSourceAt(t, root)
	output := filepath.Join(root, ".tmp", "release")
	builder := NewBuilder(CompilerFunc(writeTestBinary))

	// When
	_, err := builder.Build(context.Background(), Request{SourceDir: root, OutputDir: output, Identity: testIdentity()})

	// Then
	if err != nil {
		t.Fatalf("Build() error = %v, want nil", err)
	}
}
