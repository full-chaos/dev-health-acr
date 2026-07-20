//go:build !darwin && !linux

package sidecar

func codeGraphRootHasOnlyBaseACL(string) bool { return false }
