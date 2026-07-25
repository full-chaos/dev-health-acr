//go:build !darwin && !linux

package sidecar

func writeCredentialFile(string, string) error {
	return ErrCredentialPersistenceUnsupported
}

func removeCredentialFile(string) error {
	return ErrCredentialPersistenceUnsupported
}
