//go:build linux

package keyring

import (
	"errors"
	"fmt"
	"os"

	dbus "github.com/godbus/dbus/v5"
	gkr "github.com/zalando/go-keyring"
	"golang.org/x/sys/unix"
)

const (
	dbusServiceName        = "org.freedesktop.DBus"
	dbusObjectPath         = "/org/freedesktop/DBus"
	dbusNameHasOwnerMethod = "org.freedesktop.DBus.NameHasOwner"
	secretServiceName      = "org.freedesktop.secrets"

	// Linux kernel keyring parameters.
	keyringKeyType = "user"
)

// openBackend selects a Linux keyring backend. It prefers Secret Service
// (D-Bus), falling back to the kernel user keyring (KeyCtl) when no
// session bus / Secret Service is available. Returns ErrKeyringUnavailable
// when neither backend can be initialized.
func openBackend() (backend, error) {
	if isSecretServiceAvailable() {
		return secretServiceBackend{}, nil
	}
	if err := ensureKeyringLinked(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeyringUnavailable, err)
	}
	return keyctlBackend{}, nil
}

// OpenKeyctl returns a keyring pinned to the Linux kernel user keyring
// (KeyCtl), bypassing openBackend's Secret Service preference entirely.
// The two backends are separate stores: on a machine with a D-Bus Secret
// Service, Open() returns the D-Bus backend and cannot see keys written
// via keyctl — callers that provision credentials into the kernel keyring
// (e.g. iac-server's deployment contract) must read them through here.
// Returns ErrKeyringUnavailable when the kernel keyring is not usable
// (non-Linux, containers without keyutils).
func OpenKeyctl() (Keyring, error) {
	if err := ensureKeyringLinked(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeyringUnavailable, err)
	}
	return keyctlBackend{}, nil
}

// isSecretServiceAvailable checks whether the Secret Service D-Bus
// service is registered on the session bus. It guards against the two
// autolaunch triggers in godbus getSessionBusAddress — an empty or
// "autolaunch:" DBUS_SESSION_BUS_ADDRESS — before calling SessionBus,
// which would otherwise spawn dbus-launch + dbus-daemon. conn.Close()
// does not terminate the daemon, leaking an orphan dbus-daemon on
// every call.
func isSecretServiceAvailable() bool {
	addr := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if addr == "" || addr == "autolaunch:" {
		return false
	}
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	defer conn.Close()
	obj := conn.Object(dbusServiceName, dbusObjectPath)
	var hasOwner bool
	if err := obj.Call(dbusNameHasOwnerMethod, 0, secretServiceName).Store(&hasOwner); err != nil {
		return false
	}
	return hasOwner
}

// ensureKeyringLinked links the user keyring to the session keyring so
// that keys added by this process are visible to future sibling processes
// in the same session. Mirrors `ensureKeyringLinked()` in
// ~/projects/hdspace-models/credential/credential_linux.go. Returns an
// error when the kernel keyring is not usable (e.g. containers lacking
// the keyutils capability).
func ensureKeyringLinked() error {
	if _, err := unix.KeyctlInt(unix.KEYCTL_LINK,
		unix.KEY_SPEC_USER_KEYRING,
		unix.KEY_SPEC_SESSION_KEYRING, 0, 0); err != nil {
		return fmt.Errorf("keyctl link user keyring: %w", err)
	}
	// Probe the user keyring id (creates if missing). If this fails the
	// kernel doesn't support keyrings for this process and we should NOT
	// silently fall back to MemStore.
	if _, err := unix.KeyctlGetKeyringID(unix.KEY_SPEC_USER_KEYRING, true); err != nil {
		return fmt.Errorf("keyctl get user keyring id: %w", err)
	}
	return nil
}

// keyctlBackend stores base64-encoded secrets in the Linux kernel
// keyring under type "user" with descriptions "<service>:<key>".
//
// Set and Delete operate on the user keyring (@u) — the keyring shared
// by every session of the UID. Get searches the session keyring tree
// (@s) first and falls back to @u, so keys provisioned into either the
// session keyring (sandbox / container entrypoint provisioning, where
// the session keyring is the natural per-boot secret store) or the user
// keyring are readable. When both hold a key with the same description,
// the session-keyring copy wins.
type keyctlBackend struct{}

func keyctlDesc(service, key string) string {
	return service + ":" + key
}

// keyctlSearchID finds the id of a "user" key with the given description,
// searching the keyring tree rooted at root. Returns gkr.ErrNotFound when
// no such key exists in the tree.
func keyctlSearchID(root int, desc string) (int, error) {
	id, err := unix.KeyctlSearch(root, keyringKeyType, desc, 0)
	if err != nil {
		if errors.Is(err, unix.ENOKEY) || errors.Is(err, unix.ENOENT) {
			return 0, gkr.ErrNotFound
		}
		return 0, fmt.Errorf("keyctl search: %w", err)
	}
	return id, nil
}

// keyctlRead reads a key's payload by id and decodes the base64 wrapper.
func keyctlRead(id int) (string, error) {
	// First call with nil buffer returns the required size.
	n, err := unix.KeyctlBuffer(unix.KEYCTL_READ, id, nil, 0)
	if err != nil {
		return "", fmt.Errorf("keyctl read (probe): %w", err)
	}
	buf := make([]byte, n)
	if _, err := unix.KeyctlBuffer(unix.KEYCTL_READ, id, buf, 0); err != nil {
		return "", fmt.Errorf("keyctl read: %w", err)
	}
	return b64Decode(string(buf))
}

func (keyctlBackend) Get(service, key string) (string, error) {
	desc := keyctlDesc(service, key)
	// Search the session keyring tree first. KEYCTL_SEARCH walks the tree
	// depth-first, and ensureKeyringLinked() — run by openBackend /
	// OpenKeyctl before any Get — links @u into the caller's session
	// keyring, so an @s-rooted search normally covers @u-provisioned keys
	// as well. The explicit @u retry below covers setups where that link
	// does not reach the key's actual location.
	id, err := keyctlSearchID(unix.KEY_SPEC_SESSION_KEYRING, desc)
	if err == nil {
		return keyctlRead(id)
	}
	if !errors.Is(err, gkr.ErrNotFound) {
		return "", err
	}
	// Not in the session keyring tree — retry the user keyring directly.
	id, err = keyctlSearchID(unix.KEY_SPEC_USER_KEYRING, desc)
	if err != nil {
		return "", err
	}
	return keyctlRead(id)
}

func (keyctlBackend) Set(service, key, value string) error {
	desc := keyctlDesc(service, key)
	// Revoke an existing key with the same description to avoid duplicates.
	// AddKey would update an in-place key, but revoking first is simpler
	// and consistent across kernel versions.
	if id, err := keyctlSearchID(unix.KEY_SPEC_USER_KEYRING, desc); err == nil {
		_, _ = unix.KeyctlInt(unix.KEYCTL_REVOKE, id, 0, 0, 0)
	}
	if _, err := unix.AddKey(keyringKeyType, desc,
		[]byte(b64Encode(value)), unix.KEY_SPEC_USER_KEYRING); err != nil {
		return fmt.Errorf("keyctl add: %w", err)
	}
	return nil
}

func (keyctlBackend) Delete(service, key string) error {
	id, err := keyctlSearchID(unix.KEY_SPEC_USER_KEYRING, keyctlDesc(service, key))
	if err != nil {
		return err
	}
	if _, err := unix.KeyctlInt(unix.KEYCTL_UNLINK, id,
		unix.KEY_SPEC_USER_KEYRING, 0, 0); err != nil {
		return fmt.Errorf("keyctl unlink: %w", err)
	}
	return nil
}
