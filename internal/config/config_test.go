package config

import (
	"testing"
	"time"
)

// clearEnv removes every variable FromEnv looks at, so a test starts from a
// known state whatever the machine running it has set.
func clearEnv(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		"BRIDGE_IMAP_PORT",
		"BRIDGE_SMTP_PORT",
		"BRIDGE_IMAP_SSL",
		"BRIDGE_SMTP_SSL",
		"BRIDGE_LOG_LEVEL",
		"BRIDGE_FORWARD_TIMEOUT",
		"BRIDGE_START_TIMEOUT",
		"BRIDGE_SETUP_PORT",
		"BRIDGE_SETUP_EXPOSE",
	} {
		t.Setenv(name, "")
	}
}

func TestFromEnvDefaults(t *testing.T) {
	clearEnv(t)

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if config.IMAPPort != DefaultIMAPPort {
		t.Errorf("IMAP port: got %d, want %d", config.IMAPPort, DefaultIMAPPort)
	}

	if config.SMTPPort != DefaultSMTPPort {
		t.Errorf("SMTP port: got %d, want %d", config.SMTPPort, DefaultSMTPPort)
	}

	if config.IMAPSSL || config.SMTPSSL {
		t.Error("SSL should default to off, matching the bridge's own default")
	}

	if config.LogLevel != DefaultLogLevel {
		t.Errorf("log level: got %q, want %q", config.LogLevel, DefaultLogLevel)
	}

	if config.ForwardTimeout != DefaultForwardTimeout {
		t.Errorf("forward timeout: got %v, want %v", config.ForwardTimeout, DefaultForwardTimeout)
	}

	if config.SetupPort != DefaultSetupPort {
		t.Errorf("setup port: got %d, want %d", config.SetupPort, DefaultSetupPort)
	}

	// The page that accepts a Proton password is not something to open by
	// accident. Off unless somebody says otherwise, every time.
	if config.SetupExpose {
		t.Error("the sign-in page defaults to being exposed beyond the container")
	}
}

func TestFromEnvReadsValues(t *testing.T) {
	clearEnv(t)

	t.Setenv("BRIDGE_IMAP_PORT", "2143")
	t.Setenv("BRIDGE_SMTP_PORT", "2025")
	t.Setenv("BRIDGE_IMAP_SSL", "true")
	t.Setenv("BRIDGE_SMTP_SSL", "1")
	t.Setenv("BRIDGE_LOG_LEVEL", "debug")
	t.Setenv("BRIDGE_FORWARD_TIMEOUT", "30")
	t.Setenv("BRIDGE_START_TIMEOUT", "300")

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if config.IMAPPort != 2143 || config.SMTPPort != 2025 {
		t.Errorf("ports: got %d and %d", config.IMAPPort, config.SMTPPort)
	}

	if !config.IMAPSSL || !config.SMTPSSL {
		t.Error("both SSL flags should be on")
	}

	if config.LogLevel != "debug" {
		t.Errorf("log level: got %q", config.LogLevel)
	}

	if config.ForwardTimeout != 30*time.Second {
		t.Errorf("forward timeout: got %v", config.ForwardTimeout)
	}

	if config.StartTimeout != 300*time.Second {
		t.Errorf("start timeout: got %v", config.StartTimeout)
	}
}

func TestFromEnvRejects(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{
			name: "a port that is not a number",
			env:  map[string]string{"BRIDGE_IMAP_PORT": "eleven forty three"},
		},
		{
			// The container never runs as root, so it could not bind this even
			// if it tried. Failing at startup beats the bridge quietly moving
			// to another port and nothing being reachable.
			name: "a privileged port",
			env:  map[string]string{"BRIDGE_IMAP_PORT": "143"},
		},
		{
			name: "a port above the range",
			env:  map[string]string{"BRIDGE_SMTP_PORT": "70000"},
		},
		{
			name: "a negative port",
			env:  map[string]string{"BRIDGE_SMTP_PORT": "-1"},
		},
		{
			// Both would end up forwarded to the same place and one of the two
			// services would be unreachable, without any error anywhere.
			name: "the same port twice",
			env:  map[string]string{"BRIDGE_IMAP_PORT": "1143", "BRIDGE_SMTP_PORT": "1143"},
		},
		{
			// Three listening sockets in one container. The sign-in page and a
			// mail port on the same number means one of them silently loses,
			// and which one depends on start order.
			name: "the sign-in page on the IMAP port",
			env:  map[string]string{"BRIDGE_SETUP_PORT": "1143"},
		},
		{
			name: "the sign-in page on the SMTP port",
			env:  map[string]string{"BRIDGE_SETUP_PORT": "1025"},
		},
		{
			name: "a privileged port for the sign-in page",
			env:  map[string]string{"BRIDGE_SETUP_PORT": "443"},
		},
		{
			name: "an exposure flag that is not a truth value",
			env:  map[string]string{"BRIDGE_SETUP_EXPOSE": "sure"},
		},
		{
			name: "a log level the bridge does not know",
			env:  map[string]string{"BRIDGE_LOG_LEVEL": "verbose"},
		},
		{
			name: "an SSL flag that is not a truth value",
			env:  map[string]string{"BRIDGE_IMAP_SSL": "yes please"},
		},
		{
			name: "a timeout of zero",
			env:  map[string]string{"BRIDGE_FORWARD_TIMEOUT": "0"},
		},
		{
			name: "a negative timeout",
			env:  map[string]string{"BRIDGE_START_TIMEOUT": "-5"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearEnv(t)

			for name, value := range test.env {
				t.Setenv(name, value)
			}

			if _, err := FromEnv(); err == nil {
				t.Fatal("expected an error, got none")
			}
		})
	}
}

// An empty variable is what `docker run -e BRIDGE_LOG_LEVEL` without a value
// produces. Treating it as "unset" rather than as an invalid value is what
// keeps that from being a startup failure.
func TestFromEnvTreatsEmptyAsUnset(t *testing.T) {
	clearEnv(t)

	t.Setenv("BRIDGE_LOG_LEVEL", "")
	t.Setenv("BRIDGE_IMAP_PORT", "")

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if config.LogLevel != DefaultLogLevel || config.IMAPPort != DefaultIMAPPort {
		t.Fatalf("empty values did not fall back to the defaults: %+v", config)
	}
}

// TestOptionalBooleanHasThreeStates is why ShowAllMail is a pointer.
//
// Unset and "set to something meaningless" are different: only the first means
// leave it alone. A version of this that treated a typo as unset would silently
// ignore BRIDGE_SHOW_ALL_MAIL=ture.
func TestOptionalBooleanHasThreeStates(t *testing.T) {
	t.Setenv("BRIDGE_SHOW_ALL_MAIL", "")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}

	if cfg.ShowAllMail != nil {
		t.Errorf("an unset variable produced %v, want nil", *cfg.ShowAllMail)
	}

	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"true", true},
		{"false", false},
	} {
		t.Setenv("BRIDGE_SHOW_ALL_MAIL", tc.raw)

		cfg, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv with %q: %v", tc.raw, err)
		}

		if cfg.ShowAllMail == nil {
			t.Fatalf("%q produced nil", tc.raw)
		}

		if *cfg.ShowAllMail != tc.want {
			t.Errorf("%q produced %v, want %v", tc.raw, *cfg.ShowAllMail, tc.want)
		}
	}

	t.Setenv("BRIDGE_SHOW_ALL_MAIL", "ture")

	if _, err := FromEnv(); err == nil {
		t.Error("a typo was accepted; it would have been silently ignored")
	}
}

// Telemetry defaults to off, and that is the whole point of the setting.
func TestTelemetryDefaultsOff(t *testing.T) {
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}

	if cfg.Telemetry {
		t.Error("telemetry defaults to on")
	}

	if cfg.AlternativeRouting {
		t.Error("alternative routing defaults to on")
	}

	t.Setenv("BRIDGE_TELEMETRY", "true")

	cfg, err = FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}

	if !cfg.Telemetry {
		t.Error("BRIDGE_TELEMETRY=true was not read, so the default above proves nothing")
	}
}
