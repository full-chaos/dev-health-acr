package releasebuild

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

type verifiedArchive struct {
	file *os.File
	size int64
	hash []byte
	zip  bool
}

func (archive *verifiedArchive) Close() error { return archive.file.Close() }

func openVerifiedArchive(name, expected string) (*verifiedArchive, error) {
	file, err := openNoFollow(name)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("stat archive: %w", err)
	}
	if !opened.Mode().IsRegular() || opened.Size() > maxArchiveBytes {
		file.Close()
		return nil, fmt.Errorf("archive is unsafe or exceeds the size limit")
	}
	hash := sha256.New()
	read, err := io.Copy(hash, io.LimitReader(file, maxArchiveBytes+1))
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("hash archive: %w", err)
	}
	if read > maxArchiveBytes {
		file.Close()
		return nil, fmt.Errorf("archive exceeds the size limit")
	}
	digest, err := hex.DecodeString(expected)
	if err != nil || subtle.ConstantTimeCompare(hash.Sum(nil), digest) != 1 {
		file.Close()
		return nil, fmt.Errorf("archive checksum mismatch")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, fmt.Errorf("rewind archive: %w", err)
	}
	return &verifiedArchive{file: file, size: opened.Size(), hash: hash.Sum(nil), zip: strings.HasSuffix(name, ".zip")}, nil
}

func extractArchive(archive *verifiedArchive, destination string, expectZIP bool) error {
	if archive.zip != expectZIP {
		return fmt.Errorf("archive format does not match host platform")
	}
	if archive.zip {
		return extractZIP(archive, destination)
	}
	reader, err := gzip.NewReader(archive.file)
	if err != nil {
		return fmt.Errorf("open gzip archive: %w", err)
	}
	defer reader.Close()
	return extractTAR(tar.NewReader(reader), destination, hostBinaryForPlatform(expectZIP))
}

func extractTAR(reader *tar.Reader, destination, binaryName string) error {
	state := newExtractionState(destination, binaryName)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return state.finish()
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if err := state.writeTar(header, reader); err != nil {
			return err
		}
	}
}

func extractZIP(archive *verifiedArchive, destination string) error {
	reader, err := zip.NewReader(archive.file, archive.size)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	state := newExtractionState(destination, hostBinaryForPlatform(true))
	for _, member := range reader.File {
		if member.FileInfo().Mode()&os.ModeType != 0 || member.UncompressedSize64 > uint64(maxMemberBytes) {
			return fmt.Errorf("archive member is unsafe or exceeds the size limit")
		}
		stream, err := member.Open()
		if err != nil {
			return fmt.Errorf("open zip member: %w", err)
		}
		err = state.write(member.Name, member.FileInfo().Mode(), int64(member.UncompressedSize64), stream)
		closeErr := stream.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return fmt.Errorf("close zip member: %w", closeErr)
		}
	}
	return state.finish()
}

type extractionState struct {
	destination string
	names       map[string]bool
	aliases     map[string]bool
	members     int
	total       int64
	binary      bool
	binaryName  string
}

func newExtractionState(destination, binaryName string) *extractionState {
	return &extractionState{destination: destination, names: map[string]bool{}, aliases: map[string]bool{}, binaryName: binaryName}
}

func (state *extractionState) writeTar(header *tar.Header, reader io.Reader) error {
	if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
		return fmt.Errorf("archive contains a non-regular entry")
	}
	return state.write(header.Name, os.FileMode(header.Mode), header.Size, reader)
}

func (state *extractionState) write(name string, mode os.FileMode, size int64, reader io.Reader) error {
	if state.members >= maxArchiveMembers || size < 0 || size > maxMemberBytes || state.total > maxExtractedBytes-size {
		return fmt.Errorf("archive exceeds extraction limits")
	}
	if !safeArchivePath(name) {
		return fmt.Errorf("archive contains an unsafe path")
	}
	alias := norm.NFC.String(strings.ToLower(name))
	if state.names[name] || state.aliases[alias] {
		return fmt.Errorf("archive contains duplicate or aliased paths")
	}
	for existing := range state.names {
		if strings.HasPrefix(existing, name+"/") || strings.HasPrefix(name, existing+"/") {
			return fmt.Errorf("archive contains a file-directory collision")
		}
	}
	state.names[name], state.aliases[alias], state.members, state.total = true, true, state.members+1, state.total+size
	output := filepath.Join(state.destination, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return fmt.Errorf("create extraction directory: %w", err)
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, safeMode(mode))
	if err != nil {
		return fmt.Errorf("create extracted file: %w", err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(reader, size+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("extract archive member: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close extracted file: %w", closeErr)
	}
	if written != size {
		return fmt.Errorf("archive member size mismatch")
	}
	if name == state.binaryName {
		state.binary = true
	}
	return nil
}

func hostBinaryForPlatform(windows bool) string {
	if windows {
		return "acr-mcp.exe"
	}
	return "acr-mcp"
}

func (state *extractionState) finish() error {
	if !state.binary {
		return fmt.Errorf("archive is missing host binary")
	}
	return nil
}

func safeArchivePath(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, ":") || path.Clean(name) != name {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || reservedWindowsName(part) {
			return false
		}
	}
	return true
}

func reservedWindowsName(part string) bool {
	base := strings.ToUpper(strings.Split(part, ".")[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		return base[3] >= '1' && base[3] <= '9'
	}
	return false
}

func safeMode(mode os.FileMode) os.FileMode {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}
