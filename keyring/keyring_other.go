//go:build !linux

package keyring

import (
	"errors"

	"github.com/yusheng-g/openagent-go/version"
	gkr "github.com/zalando/go-keyring"
)

// openBackend selects a backend on non-Linux platforms (macOS / Windows).
// zalando/go-keyring routes to the native keychain (Keychain / WinCred)
// which does not depend on D-Bus.
func openBackend() (backend, error) {
	if _, err := gkr.Get(version.Name, "__probe__"); err != nil &&
		!errors.Is(err, gkr.ErrNotFound) {
		return nil, err
	}
	return secretServiceBackend{}, nil
}

// OpenKeyctl returns ErrKeyringUnavailable on non-Linux platforms: the
// kernel user keyring (KeyCtl) is a Linux-only facility. Callers using it
// for a cross-platform credential contract must degrade to their
// "credentials not configured" error path, not abort.
func OpenKeyctl() (Keyring, error) {
	return nil, ErrKeyringUnavailable
}
