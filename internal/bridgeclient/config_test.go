package bridgeclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  ServerConfig
		wantErr bool
	}{
		{
			name:   "socket, as on linux",
			config: ServerConfig{Cert: "pem", Token: "t", FileSocketPath: "/tmp/bridge0042"},
		},
		{
			name:   "port, as on windows",
			config: ServerConfig{Cert: "pem", Token: "t", Port: 42000},
		},
		{
			name:    "no token means every call would be rejected",
			config:  ServerConfig{Cert: "pem", FileSocketPath: "/tmp/bridge0042"},
			wantErr: true,
		},
		{
			name:    "no certificate means nothing to pin the connection to",
			config:  ServerConfig{Token: "t", FileSocketPath: "/tmp/bridge0042"},
			wantErr: true,
		},
		{
			name:    "neither socket nor port",
			config:  ServerConfig{Cert: "pem", Token: "t"},
			wantErr: true,
		},
		{
			name:    "empty, which is what a truncated file parses to",
			config:  ServerConfig{},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()

			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error, got none")
				}

				if !errors.Is(err, ErrConfigIncomplete) {
					t.Fatalf("expected ErrConfigIncomplete, got %v", err)
				}

				return
			}

			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

func TestServerConfigTarget(t *testing.T) {
	tests := []struct {
		name   string
		config ServerConfig
		want   string
	}{
		{
			name:   "socket",
			config: ServerConfig{FileSocketPath: "/tmp/bridge0042"},
			want:   "unix:///tmp/bridge0042",
		},
		{
			name:   "port",
			config: ServerConfig{Port: 42000},
			want:   "127.0.0.1:42000",
		},
		{
			// The bridge never writes both. If it ever did, the socket is the
			// one that is actually listening on this platform.
			name:   "both, socket wins",
			config: ServerConfig{FileSocketPath: "/tmp/bridge0042", Port: 42000},
			want:   "unix:///tmp/bridge0042",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.config.Target(); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestSettingsDir(t *testing.T) {
	t.Run("XDG_CONFIG_HOME wins", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/data/config")
		t.Setenv("HOME", "/home/bridge")

		got, err := SettingsDir()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if want := "/data/config/protonmail/bridge-v3"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to HOME", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "/home/bridge")

		got, err := SettingsDir()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if want := "/home/bridge/.config/protonmail/bridge-v3"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("neither is an error, not a guess", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "")

		if _, err := SettingsDir(); err == nil {
			t.Fatal("expected an error, got none")
		}
	})
}

func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()

	path := filepath.Join(dir, ServerConfigFileName)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("could not write test config: %v", err)
	}

	return path
}

func TestLoadServerConfig(t *testing.T) {
	// Shaped like the real thing, values invented. The bridge writes port 0
	// and a socket path on every platform but Windows.
	const valid = `{"port":0,"cert":"-----BEGIN CERTIFICATE-----\nnot a real one\n-----END CERTIFICATE-----\n","token":"5f3e...","fileSocketPath":"/tmp/bridge0042"}`

	t.Run("valid", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), valid)

		config, err := LoadServerConfig(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if config.FileSocketPath != "/tmp/bridge0042" {
			t.Fatalf("socket path did not survive: %q", config.FileSocketPath)
		}

		if config.Token != "5f3e..." {
			t.Fatalf("token did not survive: %q", config.Token)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := LoadServerConfig(filepath.Join(t.TempDir(), "nothing.json"))

		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected a not-exist error, got %v", err)
		}
	})

	t.Run("truncated json", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), `{"port":0,"cert":"-----BEG`)

		if _, err := LoadServerConfig(path); err == nil {
			t.Fatal("expected an error, got none")
		}
	})

	t.Run("parses but describes nothing reachable", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), `{"port":0,"cert":"","token":"","fileSocketPath":""}`)

		_, err := LoadServerConfig(path)

		if !errors.Is(err, ErrConfigIncomplete) {
			t.Fatalf("expected ErrConfigIncomplete, got %v", err)
		}
	})
}

func TestWaitForServerConfig(t *testing.T) {
	const valid = `{"port":0,"cert":"pem","token":"t","fileSocketPath":"/tmp/bridge0042"}`

	t.Run("returns once the file appears", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ServerConfigFileName)

		go func() {
			time.Sleep(50 * time.Millisecond)
			writeConfig(t, dir, valid)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		config, err := WaitForServerConfig(ctx, path, 10*time.Millisecond)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if config.Token != "t" {
			t.Fatalf("got token %q", config.Token)
		}
	})

	t.Run("gives up when the context does", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ServerConfigFileName)

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		if _, err := WaitForServerConfig(ctx, path, 10*time.Millisecond); err == nil {
			t.Fatal("expected an error, got none")
		}
	})
}

func TestRemoveServerConfig(t *testing.T) {
	t.Run("removes an existing file", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), `{}`)

		if err := RemoveServerConfig(path); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("the file is still there")
		}
	})

	// The first start of a fresh container has no file to remove. That is the
	// normal case, not a failure.
	t.Run("a missing file is not an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ServerConfigFileName)

		if err := RemoveServerConfig(path); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestVaultExists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	settings := filepath.Join(dir, "protonmail", "bridge-v3")
	if err := os.MkdirAll(settings, 0o700); err != nil {
		t.Fatal(err)
	}

	// A container that has never run. Nothing to wait for, and saying "maybe"
	// here would make every first start pay the account timeout for an account
	// that cannot exist.
	got, err := VaultExists()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got {
		t.Fatal("got true for a directory with no vault, want false")
	}

	if err := os.WriteFile(filepath.Join(settings, VaultFileName), []byte("not really a vault"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err = VaultExists()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !got {
		t.Fatal("got false with a vault present, want true")
	}
}

// TestVaultExistsErrsTowardsWaiting pins the direction the doubtful case leans.
//
// Neither variable set, so the settings directory cannot be worked out. Being
// wrong by waiting costs a delay. Being wrong the other way opens an HTTPS
// server that accepts a Proton password on a container that is already signed
// in, which is #35.
func TestVaultExistsErrsTowardsWaiting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	got, err := VaultExists()
	if err == nil {
		t.Fatal("expected an error when neither variable is set")
	}

	if !got {
		t.Fatal("got false alongside an error, want true so that the caller waits")
	}
}
