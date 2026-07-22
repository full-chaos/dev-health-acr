//go:build !darwin && !linux

package sidecar

var codeGraphACLCheck = func(string) bool { return false }
