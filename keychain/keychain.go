// Package keychain provides token storage backed by the operating system's
// secret store. On macOS it shells out to the `security` CLI to manage
// generic-password entries. On other platforms all write/read operations
// return an error and Supported() reports false; callers are expected to
// fall back to an environment variable.
package keychain

import "github.com/pkg/errors"

// ErrNotFound is returned by Load and Delete when no entry exists for the
// given (service, account) key.
var ErrNotFound = errors.New("keychain: not found")

// Supported reports whether token storage is available on this platform.
func Supported() bool {
	return platformSupported()
}

// Save stores a token under the given service + account. It overwrites any
// existing entry for the same key. Returns an error if Supported() is false.
func Save(service, account, token string) error {
	return platformSave(service, account, token)
}

// Load returns the stored token, or ErrNotFound if no entry exists for the key.
func Load(service, account string) (string, error) {
	return platformLoad(service, account)
}

// Delete removes the entry. Returns ErrNotFound if it did not exist.
func Delete(service, account string) error {
	return platformDelete(service, account)
}
