// Package config turns the container's environment variables into settings.
//
// These variables are the contract with whoever runs the image, so the rules
// here are deliberately strict: an unreadable value is an error at startup,
// not a silent fallback to a default. A container that quietly listens on a
// different port than it was told to is worse than one that refuses to start.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Defaults. These match what the bridge itself uses, so a container started
// with no variables at all behaves like a bridge on a desktop.
const (
	DefaultIMAPPort       = 1143
	DefaultSMTPPort       = 1025
	DefaultLogLevel       = "info"
	DefaultForwardTimeout = 60 * time.Second
	DefaultStartTimeout   = 120 * time.Second
)

// Config is everything bridge-control reads from the environment.
//
// The Proton credentials are not in here and never will be. Signing in happens
// interactively; a password in an environment variable ends up in the process
// list, in `docker inspect` and in every log that dumps the environment.
type Config struct {
	IMAPPort int
	SMTPPort int

	// IMAPSSL and SMTPSSL choose between direct TLS and STARTTLS. The bridge
	// defaults to STARTTLS for IMAP and both are encrypted either way; this
	// only decides when the handshake happens.
	IMAPSSL bool
	SMTPSSL bool

	LogLevel string

	// ForwardTimeout is how long to wait for the bridge to open a mail port
	// before giving up on forwarding it.
	ForwardTimeout time.Duration

	// StartTimeout is how long to wait for the bridge's gRPC service to come
	// up. Generous on purpose: the first start of a fresh volume generates a
	// GPG key and unlocks a new vault.
	StartTimeout time.Duration
}

// validLogLevels are the ones the bridge accepts. Anything else makes it exit
// during argument parsing, which is a confusing way to find out about a typo.
var validLogLevels = map[string]bool{
	"panic": true,
	"fatal": true,
	"error": true,
	"warn":  true,
	"info":  true,
	"debug": true,
}

// FromEnv reads the configuration from the process environment.
func FromEnv() (Config, error) {
	config := Config{
		IMAPPort:       DefaultIMAPPort,
		SMTPPort:       DefaultSMTPPort,
		LogLevel:       DefaultLogLevel,
		ForwardTimeout: DefaultForwardTimeout,
		StartTimeout:   DefaultStartTimeout,
	}

	var err error

	if config.IMAPPort, err = port("BRIDGE_IMAP_PORT", DefaultIMAPPort); err != nil {
		return Config{}, err
	}

	if config.SMTPPort, err = port("BRIDGE_SMTP_PORT", DefaultSMTPPort); err != nil {
		return Config{}, err
	}

	if config.IMAPPort == config.SMTPPort {
		return Config{}, fmt.Errorf("BRIDGE_IMAP_PORT and BRIDGE_SMTP_PORT are both %d, they have to differ", config.IMAPPort)
	}

	if config.IMAPSSL, err = boolean("BRIDGE_IMAP_SSL", false); err != nil {
		return Config{}, err
	}

	if config.SMTPSSL, err = boolean("BRIDGE_SMTP_SSL", false); err != nil {
		return Config{}, err
	}

	if config.LogLevel, err = logLevel("BRIDGE_LOG_LEVEL", DefaultLogLevel); err != nil {
		return Config{}, err
	}

	if config.ForwardTimeout, err = seconds("BRIDGE_FORWARD_TIMEOUT", DefaultForwardTimeout); err != nil {
		return Config{}, err
	}

	if config.StartTimeout, err = seconds("BRIDGE_START_TIMEOUT", DefaultStartTimeout); err != nil {
		return Config{}, err
	}

	return config, nil
}

// port reads a TCP port number.
//
// Ports below 1024 are rejected rather than attempted: the container runs as
// an unprivileged user and cannot bind them, and the failure would otherwise
// appear much later as a port the bridge silently moved away from.
func port(name string, fallback int) (int, error) {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return fallback, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is %q, which is not a number", name, raw)
	}

	if value < 1024 || value > 65535 {
		return 0, fmt.Errorf("%s is %d, which is outside 1024-65535; the container does not run as root and cannot bind a privileged port", name, value)
	}

	return value, nil
}

func boolean(name string, fallback bool) (bool, error) {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return fallback, nil
	}

	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s is %q, which is not a truth value; use true or false", name, raw)
	}

	return value, nil
}

func logLevel(name, fallback string) (string, error) {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return fallback, nil
	}

	if !validLogLevels[raw] {
		return "", fmt.Errorf("%s is %q; use one of panic, fatal, error, warn, info, debug", name, raw)
	}

	return raw, nil
}

func seconds(name string, fallback time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return fallback, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is %q, which is not a number of seconds", name, raw)
	}

	if value <= 0 {
		return 0, fmt.Errorf("%s is %d; a timeout of zero or less would give up before trying", name, value)
	}

	return time.Duration(value) * time.Second, nil
}
