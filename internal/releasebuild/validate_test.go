package releasebuild

import (
	"context"
	"testing"
)

func TestIdentity_Validate_rejects_noncanonical_release_identity(t *testing.T) {
	tests := []struct {
		name     string
		identity Identity
	}{
		{name: "version has v prefix", identity: Identity{Version: "v1.2.3", Commit: testIdentity().Commit, Date: testIdentity().Date}},
		{name: "version is not semantic", identity: Identity{Version: "release", Commit: testIdentity().Commit, Date: testIdentity().Date}},
		{name: "commit is abbreviated", identity: Identity{Version: testIdentity().Version, Commit: "0123456", Date: testIdentity().Date}},
		{name: "commit is uppercase", identity: Identity{Version: testIdentity().Version, Commit: "0123456789ABCDEF0123456789ABCDEF01234567", Date: testIdentity().Date}},
		{name: "date has offset", identity: Identity{Version: testIdentity().Version, Commit: testIdentity().Commit, Date: "2026-07-12T15:14:15+02:00"}},
		{name: "date has fractions", identity: Identity{Version: testIdentity().Version, Commit: testIdentity().Commit, Date: "2026-07-12T13:14:15.000Z"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// When
			err := test.identity.Validate()

			// Then
			if err == nil {
				t.Fatal("Validate() error = nil, want rejection")
			}
		})
	}
}

func TestVerify_rejects_duplicate_manifest_artifacts(t *testing.T) {
	// Given
	dir := t.TempDir()
	source := writeClientSource(t)
	builder := NewBuilder(CompilerFunc(writeTestBinary))
	manifest, err := builder.Build(context.Background(), Request{SourceDir: source, OutputDir: dir, Identity: testIdentity()})
	if err != nil {
		t.Fatalf("build release: %v", err)
	}
	manifest.Artifacts[1].Name = manifest.Artifacts[0].Name
	if err := writeManifest(dir, manifest); err != nil {
		t.Fatalf("write malformed manifest: %v", err)
	}

	// When
	err = Verify(dir)

	// Then
	if err == nil {
		t.Fatal("Verify() error = nil, want duplicate rejection")
	}
}

func TestVerify_rejects_path_traversal_in_manifest(t *testing.T) {
	// Given
	dir := t.TempDir()
	source := writeClientSource(t)
	builder := NewBuilder(CompilerFunc(writeTestBinary))
	manifest, err := builder.Build(context.Background(), Request{SourceDir: source, OutputDir: dir, Identity: testIdentity()})
	if err != nil {
		t.Fatalf("build release: %v", err)
	}
	manifest.Artifacts[0].Name = "../outside.tar.gz"
	if err := writeManifest(dir, manifest); err != nil {
		t.Fatalf("write malformed manifest: %v", err)
	}

	// When
	err = Verify(dir)

	// Then
	if err == nil {
		t.Fatal("Verify() error = nil, want traversal rejection")
	}
}
