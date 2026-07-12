package diagnostics

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// MaxBundleBytes bounds the total size of a built archive. Real bundle
// content is a few kilobytes of JSON and Markdown; this ceiling is a
// generous defense-in-depth backstop against a future caller accidentally
// wiring in unbounded data (for example, an unbounded Checks list), not a
// limit legitimate content is expected to approach.
const MaxBundleBytes = 1 << 20 // 1 MiB

// entryMode is the mode recorded for every tar entry: owner read/write
// only, matching the 0600 file the archive itself is written with.
const entryMode = 0o600

// Build renders input into a deterministic, uncompressed tar archive: for
// a fixed input and generatedAt, Build always produces byte-identical
// output. generatedAt is taken as an explicit parameter (rather than
// calling time.Now internally) so callers -- and this package's own
// determinism tests -- can reproduce a given bundle exactly.
func Build(input Input, generatedAt time.Time) ([]byte, error) {
	generatedAt = generatedAt.UTC()
	m := buildManifest(input, generatedAt)

	manifestJSON, err := marshalIndent(m)
	if err != nil {
		return nil, fmt.Errorf("diagnostics: encode manifest: %w", err)
	}
	staticJSON, err := marshalIndent(input.Static)
	if err != nil {
		return nil, fmt.Errorf("diagnostics: encode static report: %w", err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	if err := writeEntry(tw, manifestFile, manifestJSON, generatedAt); err != nil {
		return nil, err
	}
	if err := writeEntry(tw, staticReportFile, staticJSON, generatedAt); err != nil {
		return nil, err
	}
	if input.Live != nil {
		liveJSON, err := marshalIndent(input.Live)
		if err != nil {
			return nil, fmt.Errorf("diagnostics: encode live report: %w", err)
		}
		if err := writeEntry(tw, liveReportFile, liveJSON, generatedAt); err != nil {
			return nil, err
		}
	}
	if err := writeEntry(tw, interpretationDoc, []byte(readmeText(input)), generatedAt); err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("diagnostics: finalize archive: %w", err)
	}

	if buf.Len() > MaxBundleBytes {
		return nil, fmt.Errorf("diagnostics: built archive of %d bytes exceeds the %d byte bound", buf.Len(), MaxBundleBytes)
	}
	return buf.Bytes(), nil
}

// marshalIndent renders v as indented JSON with a trailing newline, so
// every JSON entry in the archive is deterministic and diff-friendly.
func marshalIndent(v any) ([]byte, error) {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// writeEntry writes one fixed-mode, fixed-modtime tar entry. Using the
// same generatedAt for every entry's ModTime (rather than time.Now, which
// would vary entry to entry) is part of what keeps the whole archive
// byte-for-byte deterministic for a fixed input and generatedAt.
func writeEntry(tw *tar.Writer, name string, data []byte, modTime time.Time) error {
	header := &tar.Header{
		Name:    name,
		Mode:    entryMode,
		Size:    int64(len(data)),
		ModTime: modTime,
		Uid:     0,
		Gid:     0,
		Uname:   "",
		Gname:   "",
		Format:  tar.FormatUSTAR,
	}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("diagnostics: write %s header: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("diagnostics: write %s content: %w", name, err)
	}
	return nil
}
