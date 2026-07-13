package entitlements

import (
	"errors"
	"os"
	"path/filepath"
)

func readRestrictedFile(path string, maxSize int64) ([]byte, error) {
	file, info, err := openVerifiedRegularFile(path)
	if err != nil || !isPrivateSecret(info) {
		return nil, errors.New("invalid file")
	}
	defer file.Close()
	return readVerifiedFile(file, info, path, maxSize)
}

func readVerifiedRegularFile(path string, maxSize int64) ([]byte, error) {
	file, info, err := openVerifiedRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readVerifiedFile(file, info, path, maxSize)
}

func openVerifiedRegularFile(path string) (*os.File, os.FileInfo, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, errors.New("invalid file")
	}
	return file, info, nil
}

func readVerifiedFile(file *os.File, opened os.FileInfo, path string, maxSize int64) ([]byte, error) {
	if opened.Size() > maxSize {
		return nil, errors.New("invalid file")
	}
	contents, err := readBounded(file, maxSize)
	if err != nil {
		return nil, err
	}
	closed, err := file.Stat()
	if err != nil || !os.SameFile(opened, closed) || closed.Size() != opened.Size() || closed.Size() != int64(len(contents)) {
		return nil, errors.New("invalid file")
	}
	linked, err := os.Lstat(path)
	if err != nil || !linked.Mode().IsRegular() || !os.SameFile(opened, linked) {
		return nil, errors.New("invalid file")
	}
	return contents, nil
}
