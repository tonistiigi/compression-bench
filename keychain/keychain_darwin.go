package keychain

import (
	"bytes"
	"os/exec"
	"strings"

	"github.com/pkg/errors"
)

// errSecItemNotFound is the exit status the `security` CLI returns when an
// entry cannot be found. It maps to the OSStatus value errSecItemNotFound
// (-25300), but the CLI exposes it as decimal 44.
const errSecItemNotFound = 44

func platformSupported() bool {
	return true
}

func platformSave(service, account, token string) error {
	if service == "" || account == "" {
		return errors.New("keychain: service and account are required")
	}
	// -U: update the entry if one with the same service+account already exists
	// -T '': do not allow any application to access the item without prompt
	//        (callers that need the value go through `security` again, which
	//        is allowed implicitly for the user's own command).
	// -w <token>: the password value; passed as an explicit argv element so
	//             no shell interpretation occurs.
	cmd := exec.Command("security",
		"add-generic-password",
		"-a", account,
		"-s", service,
		"-T", "",
		"-U",
		"-w", token,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return errors.Wrapf(err, "security add-generic-password: %s",
			strings.TrimSpace(stderr.String()))
	}
	return nil
}

func platformLoad(service, account string) (string, error) {
	if service == "" || account == "" {
		return "", errors.New("keychain: service and account are required")
	}
	cmd := exec.Command("security",
		"find-generic-password",
		"-a", account,
		"-s", service,
		"-w",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == errSecItemNotFound {
			return "", ErrNotFound
		}
		return "", errors.Wrapf(err, "security find-generic-password: %s",
			strings.TrimSpace(stderr.String()))
	}
	// `security ... -w` prints the password followed by a single newline.
	// Trim only the trailing newline so a token that legitimately contains
	// whitespace is preserved.
	out := stdout.Bytes()
	out = bytes.TrimSuffix(out, []byte("\n"))
	return string(out), nil
}

func platformDelete(service, account string) error {
	if service == "" || account == "" {
		return errors.New("keychain: service and account are required")
	}
	cmd := exec.Command("security",
		"delete-generic-password",
		"-a", account,
		"-s", service,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == errSecItemNotFound {
			return ErrNotFound
		}
		return errors.Wrapf(err, "security delete-generic-password: %s",
			strings.TrimSpace(stderr.String()))
	}
	return nil
}
