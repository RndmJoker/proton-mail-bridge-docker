// Package bridgeclient connects to the gRPC interface of a running Proton Mail
// Bridge, the same interface Proton's own window uses.
//
// The bridge writes a file on startup describing how to reach it: the address,
// a self-signed certificate and a token. Everything in this file is about
// finding that description, reading it, and turning it into a connection.
package bridgeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ServerConfigFileName is the file the bridge writes into its settings
// directory once the gRPC service is listening.
const ServerConfigFileName = "grpcServerConfig.json"

// ServerConfig mirrors the file the bridge writes. The field names are its
// own; do not rename the JSON tags.
//
// Exactly one of FileSocketPath and Port is meaningful. The bridge uses a Unix
// socket everywhere except Windows, so in this container it is always the
// socket, and Port stays zero. Both are handled anyway: the file is Proton's
// format, not ours, and a client that only understands the case it happens to
// meet is a client that breaks silently on the other one.
type ServerConfig struct {
	Port           int    `json:"port"`
	Cert           string `json:"cert"`
	Token          string `json:"token"`
	FileSocketPath string `json:"fileSocketPath"`
}

// ErrConfigIncomplete is returned when the file parses but does not describe a
// reachable server. Distinguished from a parse error because it is what a
// half-written file looks like, and the caller may want to wait rather than
// give up.
var ErrConfigIncomplete = errors.New("gRPC server config is incomplete")

// Validate reports whether the config describes something that can be dialled.
func (c ServerConfig) Validate() error {
	if c.Token == "" {
		return fmt.Errorf("%w: no token", ErrConfigIncomplete)
	}

	if c.Cert == "" {
		return fmt.Errorf("%w: no certificate", ErrConfigIncomplete)
	}

	if c.FileSocketPath == "" && c.Port == 0 {
		return fmt.Errorf("%w: neither a socket path nor a port", ErrConfigIncomplete)
	}

	return nil
}

// Target returns the gRPC target string for this config.
//
// The certificate the bridge generates carries 127.0.0.1 as its common name
// and as its only IP address, which is why the TLS server name is pinned to
// that even when the transport is a Unix socket. See dial.go.
func (c ServerConfig) Target() string {
	if c.FileSocketPath != "" {
		return "unix://" + c.FileSocketPath
	}

	return fmt.Sprintf("127.0.0.1:%d", c.Port)
}

// SettingsDir returns the directory the bridge keeps its settings in.
//
// This mirrors what the bridge itself computes from $XDG_CONFIG_HOME. It is
// duplicated rather than asked for, because the bridge offers no way to ask
// before it is running, and this is needed to find out whether it is.
func SettingsDir() (string, error) {
	config := os.Getenv("XDG_CONFIG_HOME")

	if config == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return "", errors.New("neither $XDG_CONFIG_HOME nor $HOME are set")
		}

		config = filepath.Join(home, ".config")
	}

	return filepath.Join(config, "protonmail", "bridge-v3"), nil
}

// ServerConfigPath returns the full path of the file the bridge writes.
func ServerConfigPath() (string, error) {
	dir, err := SettingsDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, ServerConfigFileName), nil
}

// LoadServerConfig reads and validates the file at path.
func LoadServerConfig(path string) (ServerConfig, error) {
	var config ServerConfig

	b, err := os.ReadFile(path) //nolint:gosec // the path is ours, not user input
	if err != nil {
		return config, err
	}

	if err := json.Unmarshal(b, &config); err != nil {
		return config, fmt.Errorf("could not parse %s: %w", path, err)
	}

	if err := config.Validate(); err != nil {
		return config, err
	}

	return config, nil
}

// WaitForServerConfig polls until the file at path can be read and validated,
// or ctx is done.
//
// Polling rather than watching on purpose: an inotify watch would have to cope
// with the directory not existing yet, and the file arriving through a rename
// from a temporary name, which is how the bridge writes it. A poll every
// interval is a few dozen stat calls over the life of a startup.
//
// The caller is expected to have removed any previous file first. This
// function cannot tell a fresh config from a stale one: the file has no
// timestamp of its own, and a stale one points at a socket that no longer
// exists and a token that is no longer accepted.
func WaitForServerConfig(ctx context.Context, path string, interval time.Duration) (ServerConfig, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastErr error

	for {
		config, err := LoadServerConfig(path)
		if err == nil {
			return config, nil
		}

		lastErr = err

		select {
		case <-ctx.Done():
			return ServerConfig{}, fmt.Errorf("gave up waiting for %s: %w (last attempt: %v)", path, ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

// RemoveServerConfig deletes the file at path if it exists.
//
// This has to happen before the bridge is started. The bridge overwrites the
// file when its gRPC service comes up, but until then the previous run's file
// is still lying there, with a token that is no longer valid and a socket path
// that no longer exists. A client that reads it connects to nothing and
// reports something misleading.
func RemoveServerConfig(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}
