//go:build !darwin && !linux

package sidecar

func writeCredentialFile(string, string) error {
	return ErrCredentialPersistenceUnsupported
}

func removeCredentialFile(string) error {
	return ErrCredentialPersistenceUnsupported
}

// rejectSharedWritableCredentialParent has nothing to check on a platform with
// no credential writer: removeCredentialFile above already refuses every
// removal here, so there is no window for a shared-writable parent to be
// exploited through.
func rejectSharedWritableCredentialParent(string) error { return nil }
