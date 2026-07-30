package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// CertDirName is where the setup page keeps its certificate and token, inside
// the bridge's settings directory and therefore inside the volume.
const CertDirName = "setup"

// TokenFileName is the file the access token is written to, inside CertDirName.
//
// Exported so that the log can name the path without repeating it, and so
// that nothing has to print the token itself.
const TokenFileName = "token"

// SaveToken writes the access token where proton-login can read it.
//
// The token is also printed to the log, which is where a person reads it. This
// file exists for proton-login, which would otherwise have no way to reach an
// exposed page from inside the container.
//
// A file rather than an environment variable: the variable would be visible in
// `docker inspect` and in the process list of every command run in the
// container. This file is 0600 in a directory that is 0700, owned by the only
// user in the container, and it is removed when the page stops.
func SaveToken(dir, token string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, TokenFileName), []byte(token), 0o600)
}

// LoadToken reads the token, or returns an empty string if there is none.
//
// No token is the normal case: the page only has one when it is exposed beyond
// the container.
func LoadToken(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, TokenFileName)) //nolint:gosec // path is ours
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}

		return "", err
	}

	return strings.TrimSpace(string(raw)), nil
}

// RemoveToken deletes the stored token.
//
// Called when the page starts without one, so that a token left over from a
// previous run with BRIDGE_SETUP_EXPOSE set does not linger in the volume
// after it has been turned off again.
func RemoveToken(dir string) error {
	if err := os.Remove(filepath.Join(dir, TokenFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}
