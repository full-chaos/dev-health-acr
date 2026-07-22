package sidecar

import (
	"os"
	"path/filepath"
)

// codeGraphDBGuard pins every path identity used to locate a CodeGraph database.
// It narrows replacement exposure around a fixed subprocess; it cannot eliminate
// every swap-and-restore race between filesystem operations.
type codeGraphDBGuard struct {
	file                 *os.File
	repository           os.FileInfo
	logicalIndex         os.FileInfo
	resolvedIndex        os.FileInfo
	logicalDatabase      os.FileInfo
	openedDatabase       os.FileInfo
	canonicalRoot        string
	resolvedIndexPath    string
	resolvedDatabasePath string
}

func openCodeGraphDB(root string) (codeGraphDBGuard, error) {
	if !trustedCodeGraphIndex(root) {
		return codeGraphDBGuard{}, errCodeGraphMissing
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return codeGraphDBGuard{}, errCodeGraphMissing
	}
	repository, err := os.Stat(canonicalRoot)
	if err != nil || !repository.IsDir() {
		return codeGraphDBGuard{}, errCodeGraphMissing
	}
	logicalIndexPath := filepath.Join(canonicalRoot, ".codegraph")
	logicalIndex, err := os.Lstat(logicalIndexPath)
	if err != nil {
		return codeGraphDBGuard{}, errCodeGraphMissing
	}
	resolvedIndexPath, err := filepath.EvalSymlinks(logicalIndexPath)
	if err != nil {
		return codeGraphDBGuard{}, errCodeGraphMissing
	}
	resolvedIndex, err := os.Stat(resolvedIndexPath)
	if err != nil || !resolvedIndex.IsDir() {
		return codeGraphDBGuard{}, errCodeGraphMissing
	}
	resolvedDatabasePath := filepath.Join(resolvedIndexPath, "codegraph.db")
	logicalDatabase, err := os.Lstat(resolvedDatabasePath)
	if err != nil || !trustedCodeGraphDatabase(logicalDatabase) {
		return codeGraphDBGuard{}, errCodeGraphMissing
	}
	file, openedDatabase, err := openTrustedCodeGraphDatabase(resolvedDatabasePath, logicalDatabase)
	if err != nil {
		return codeGraphDBGuard{}, errCodeGraphMissing
	}
	return codeGraphDBGuard{
		file:                 file,
		repository:           repository,
		logicalIndex:         logicalIndex,
		resolvedIndex:        resolvedIndex,
		logicalDatabase:      logicalDatabase,
		openedDatabase:       openedDatabase,
		canonicalRoot:        canonicalRoot,
		resolvedIndexPath:    resolvedIndexPath,
		resolvedDatabasePath: resolvedDatabasePath,
	}, nil
}

func openTrustedCodeGraphDatabase(path string, expected os.FileInfo) (*os.File, os.FileInfo, error) {
	file, err := openCodeGraphDatabase(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !trustedCodeGraphDatabase(info) || !os.SameFile(expected, info) {
		_ = file.Close()
		return nil, nil, errCodeGraphMissing
	}
	return file, info, nil
}

func (g codeGraphDBGuard) unchanged(root string) bool {
	defer g.close()
	current, err := openCodeGraphDB(root)
	if err != nil {
		return false
	}
	defer current.close()
	return g.canonicalRoot == current.canonicalRoot &&
		g.resolvedIndexPath == current.resolvedIndexPath &&
		g.resolvedDatabasePath == current.resolvedDatabasePath &&
		os.SameFile(g.repository, current.repository) &&
		os.SameFile(g.logicalIndex, current.logicalIndex) &&
		os.SameFile(g.resolvedIndex, current.resolvedIndex) &&
		os.SameFile(g.logicalDatabase, current.logicalDatabase) &&
		os.SameFile(g.openedDatabase, current.openedDatabase)
}

func (g codeGraphDBGuard) close() {
	if g.file != nil {
		_ = g.file.Close()
	}
}

func trustedCodeGraphDB(index string) bool {
	info, err := os.Lstat(filepath.Join(index, "codegraph.db"))
	return err == nil && trustedCodeGraphDatabase(info)
}

func trustedCodeGraphDatabase(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && info.Size() > 0 && verifyCurrentUserOwned(info) == nil
}
