//go:build !darwin

package keychain

import (
	"runtime"

	"github.com/pkg/errors"
)

func platformSupported() bool {
	return false
}

func platformSave(service, account, token string) error {
	return errors.Errorf("token storage not supported on %s; use --token-env instead", runtime.GOOS)
}

func platformLoad(service, account string) (string, error) {
	return "", errors.Errorf("token storage not supported on %s; use --token-env instead", runtime.GOOS)
}

func platformDelete(service, account string) error {
	return errors.Errorf("token storage not supported on %s; use --token-env instead", runtime.GOOS)
}
