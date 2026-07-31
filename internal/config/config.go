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
	DefaultSetupPort      = 8443
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

	// SetupPort is where the sign-in page listens.
	SetupPort int

	// SetupExpose opens the sign-in page beyond the container.
	//
	// Off by default, and the page that takes a Proton password is not
	// something to open by accident. With it on, the page demands an access
	// token that is generated at startup and printed to the log.
	SetupExpose bool

	// PublicIMAPPort and PublicSMTPPort are what the operator published the
	// mail ports as on the host. Zero when nobody said.
	//
	// Purely descriptive: nothing in the container binds these or checks them.
	// They exist so that proton-info can name the numbers a mail client
	// actually needs, which the container cannot find out for itself. Publishing
	// happens outside it, and reading the engine's socket to find out would mean
	// giving a container that holds a mailbox control of the engine, for a line
	// of output.
	//
	// Deliberately outside the clash check below. These are host ports; the
	// three that must not collide are sockets inside the container, and two of
	// these could legitimately be the same number as one of those.
	PublicIMAPPort int
	PublicSMTPPort int
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

	if config.SetupPort, err = port("BRIDGE_SETUP_PORT", DefaultSetupPort); err != nil {
		return Config{}, err
	}

	if config.SetupExpose, err = boolean("BRIDGE_SETUP_EXPOSE", false); err != nil {
		return Config{}, err
	}

	// Zero means "nobody said", which is different from a bad value: an
	// unparseable one still fails, because a wrong port in a report is worse
	// than no port at all.
	if config.PublicIMAPPort, err = hostPort("BRIDGE_PUBLIC_IMAP_PORT"); err != nil {
		return Config{}, err
	}

	if config.PublicSMTPPort, err = hostPort("BRIDGE_PUBLIC_SMTP_PORT"); err != nil {
		return Config{}, err
	}

	// All three end up as listening sockets in the same container. Two on one
	// number means one of them silently loses.
	for _, clash := range []struct {
		a, b         int
		nameA, nameB string
	}{
		{config.IMAPPort, config.SMTPPort, "BRIDGE_IMAP_PORT", "BRIDGE_SMTP_PORT"},
		{config.IMAPPort, config.SetupPort, "BRIDGE_IMAP_PORT", "BRIDGE_SETUP_PORT"},
		{config.SMTPPort, config.SetupPort, "BRIDGE_SMTP_PORT", "BRIDGE_SETUP_PORT"},
	} {
		if clash.a == clash.b {
			return Config{}, fmt.Errorf("%s and %s are both %d, they have to differ", clash.nameA, clash.nameB, clash.a)
		}
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

// hostPort reads a port on the host rather than one this container binds.
//
// Its own parser because port() would be wrong twice over. Its range starts at
// 1024 and its error explains that the container cannot bind a privileged port,
// and neither applies: nothing here binds these, and whoever published them may
// well have used 143 with the privileges to do so. An error that names the
// wrong reason sends the reader looking in the wrong place.
//
// Zero means nobody said, which is not an error. A value that cannot be a port
// is one: a wrong number in the report is worse than no number, because the
// missing one sends people to the readme and the wrong one sends them nowhere.
func hostPort(name string) (int, error) {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return 0, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is %q, which is not a number", name, raw)
	}

	if value < 1 || value > 65535 {
		return 0, fmt.Errorf("%s is %d, which is not a port number", name, value)
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
