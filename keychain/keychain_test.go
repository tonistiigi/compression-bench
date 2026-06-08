package keychain_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/compression-bench/keychain"
)

// testService returns a service name unique to this test run so we cannot
// collide with the user's real keychain entries.
func testService(t *testing.T, suffix string) string {
	t.Helper()
	return fmt.Sprintf("compbench-test-%d-%s", time.Now().UnixNano(), suffix)
}

// skipIfUnsupported skips the test unless the platform supports keychain
// storage. The package compiles everywhere but only macOS has a real impl.
func skipIfUnsupported(t *testing.T) {
	t.Helper()
	if !keychain.Supported() {
		t.Skip("keychain not supported on this platform")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	skipIfUnsupported(t)
	service := testService(t, "roundtrip")
	account := "test-user"
	token := "ghp_abcdef1234567890"

	require.NoError(t, keychain.Save(service, account, token))
	t.Cleanup(func() {
		_ = keychain.Delete(service, account)
	})

	got, err := keychain.Load(service, account)
	require.NoError(t, err)
	assert.Equal(t, token, got)
}

func TestSaveOverwrites(t *testing.T) {
	skipIfUnsupported(t)
	service := testService(t, "overwrite")
	account := "test-user"

	require.NoError(t, keychain.Save(service, account, "first"))
	t.Cleanup(func() {
		_ = keychain.Delete(service, account)
	})

	require.NoError(t, keychain.Save(service, account, "second"))

	got, err := keychain.Load(service, account)
	require.NoError(t, err)
	assert.Equal(t, "second", got)
}

func TestLoadMissingReturnsNotFound(t *testing.T) {
	skipIfUnsupported(t)
	service := testService(t, "missing")
	_, err := keychain.Load(service, "nobody")
	require.Error(t, err)
	assert.True(t, errors.Is(err, keychain.ErrNotFound),
		"expected ErrNotFound, got %v", err)
}

func TestDeleteThenLoadReturnsNotFound(t *testing.T) {
	skipIfUnsupported(t)
	service := testService(t, "delete")
	account := "test-user"

	require.NoError(t, keychain.Save(service, account, "value"))
	require.NoError(t, keychain.Delete(service, account))

	_, err := keychain.Load(service, account)
	assert.True(t, errors.Is(err, keychain.ErrNotFound),
		"expected ErrNotFound after delete, got %v", err)
}

func TestDeleteMissingReturnsNotFound(t *testing.T) {
	skipIfUnsupported(t)
	service := testService(t, "delete-missing")
	err := keychain.Delete(service, "nobody")
	assert.True(t, errors.Is(err, keychain.ErrNotFound),
		"expected ErrNotFound on delete of missing entry, got %v", err)
}

func TestTokenWithSpecialChars(t *testing.T) {
	skipIfUnsupported(t)
	service := testService(t, "special")
	account := "test-user"
	// Shell metacharacters; using exec.Cmd with argv these must be passed
	// through verbatim without any interpretation.
	token := `$(echo pwned); rm -rf / "weird'token"`

	require.NoError(t, keychain.Save(service, account, token))
	t.Cleanup(func() {
		_ = keychain.Delete(service, account)
	})

	got, err := keychain.Load(service, account)
	require.NoError(t, err)
	assert.Equal(t, token, got)
}

func TestUnsupportedPlatform(t *testing.T) {
	if keychain.Supported() {
		t.Skip("supported platform; this test targets the !darwin build")
	}
	err := keychain.Save("svc", "acct", "tok")
	require.Error(t, err)
	_, err = keychain.Load("svc", "acct")
	require.Error(t, err)
	err = keychain.Delete("svc", "acct")
	require.Error(t, err)
}
