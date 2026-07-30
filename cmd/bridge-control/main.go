// Command bridge-control starts the Proton Mail Bridge core, configures it
// through the same gRPC interface Proton's own window uses, and gets out of
// the way.
//
// It replaces the window, not the core. Everything it does could be done by a
// person clicking through the graphical application; none of it can be done
// from a command line, which is why this program exists.
//
// It never sees a Proton password. Signing in is a separate step.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgeclient"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/config"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/control"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/forward"
)

// How often to look for the config file and for a listening port. Both are
// waits of a few seconds in practice, so the interval only decides how much
// of that is spent sleeping.
const pollInterval = 250 * time.Millisecond

// How long to give the bridge to shut down after SIGTERM before killing it.
// It flushes its vault on the way out, and a half-written vault is the one
// thing in this container that cannot be recreated.
const shutdownGrace = 30 * time.Second

func logf(format string, args ...any) {
	fmt.Printf("%s  bridge-control  %s\n", time.Now().UTC().Format("2006-01-02T15:04:05Z"), fmt.Sprintf(format, args...))
}

func main() {
	if err := run(); err != nil {
		logf("ERROR: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	configPath, err := bridgeclient.ServerConfigPath()
	if err != nil {
		return err
	}

	// Signals arrive here rather than at the bridge: this process has to tear
	// down the forwards and give the bridge a chance to close its vault.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// The bridge overwrites this file when its gRPC service comes up, but
	// until then the previous run's file is still there, naming a socket that
	// no longer exists and a token that is no longer accepted. Removing it
	// first is what makes the wait below mean "the bridge is up" rather than
	// "a file exists".
	if err := bridgeclient.RemoveServerConfig(configPath); err != nil {
		return fmt.Errorf("could not remove the stale gRPC config at %s: %w", configPath, err)
	}

	bridge, err := startBridge(cfg)
	if err != nil {
		return err
	}

	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- bridge.Wait() }()

	forwarder, err := configure(ctx, cfg, configPath, bridgeDone)
	if err != nil {
		terminate(bridge)
		return err
	}

	defer forwarder.Stop()

	select {
	case err := <-bridgeDone:
		if err != nil {
			return fmt.Errorf("the bridge exited: %w", err)
		}

		logf("The bridge exited.")

		return nil

	case <-ctx.Done():
		logf("Shutting down.")
		forwarder.Stop()
		terminate(bridge)

		select {
		case <-bridgeDone:
		case <-time.After(shutdownGrace):
			logf("WARNING: the bridge did not stop within %v, killing it.", shutdownGrace)

			if bridge.Process != nil {
				_ = bridge.Process.Kill()
			}
		}

		return nil
	}
}

// startBridge launches the bridge core with its output going straight to ours,
// so `docker logs` shows the bridge's own log rather than a copy of it.
func startBridge(cfg config.Config) (*exec.Cmd, error) {
	args := control.BridgeArgs(cfg)

	logf("Starting the bridge: bridge %v", args)

	cmd := exec.Command("bridge", args...) //nolint:gosec // arguments are built from validated config
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("could not start the bridge: %w", err)
	}

	return cmd, nil
}

// configure waits for the bridge to answer, applies the settings and starts
// forwarding the mail ports.
//
// bridgeDone is watched throughout: if the bridge dies during startup, waiting
// for a config file it will never write would otherwise run into a timeout
// and report the wrong cause.
func configure(ctx context.Context, cfg config.Config, configPath string, bridgeDone <-chan error) (*forward.Forwarder, error) {
	waitCtx, cancel := context.WithTimeout(ctx, cfg.StartTimeout)
	defer cancel()

	configCh := make(chan bridgeclient.ServerConfig, 1)
	errCh := make(chan error, 1)

	go func() {
		serverConfig, err := bridgeclient.WaitForServerConfig(waitCtx, configPath, pollInterval)
		if err != nil {
			errCh <- err
			return
		}

		configCh <- serverConfig
	}()

	var serverConfig bridgeclient.ServerConfig

	select {
	case serverConfig = <-configCh:
	case err := <-errCh:
		return nil, err
	case err := <-bridgeDone:
		return nil, fmt.Errorf("the bridge stopped before its gRPC service was up: %w", err)
	}

	client, err := bridgeclient.Dial(serverConfig)
	if err != nil {
		return nil, err
	}

	// The connection is deliberately not closed on the way out. It stays open
	// for the life of the process, which ends when the bridge does.
	version, err := control.Version(ctx, client)
	if err != nil {
		return nil, err
	}

	logf("Connected to bridge %s over %s", version, serverConfig.Target())

	if err := control.Apply(ctx, client, cfg); err != nil {
		return nil, err
	}

	logf("Applied settings: IMAP %d (ssl=%v), SMTP %d (ssl=%v), automatic updates off",
		cfg.IMAPPort, cfg.IMAPSSL, cfg.SMTPPort, cfg.SMTPSSL)

	// Runs for the life of the process: the sign-in page comes and goes with
	// the account, not with the startup.
	go runSignIn(ctx, cfg, client)

	return startForwarding(ctx, cfg)
}

// startForwarding exposes the mail ports on the container's own address.
//
// A container with no non-loopback address is unusual but not broken: the
// bridge still runs and is still reachable from inside. Saying so and carrying
// on beats refusing to start.
func startForwarding(ctx context.Context, cfg config.Config) (*forward.Forwarder, error) {
	address, err := forward.ContainerAddress()
	if err != nil {
		logf("WARNING: %v, so the mail ports stay on loopback only.", err)
		return forward.New("", logf), nil
	}

	forwarder := forward.New(address, logf)

	for _, port := range []struct {
		number int
		label  string
	}{
		{cfg.IMAPPort, "IMAP"},
		{cfg.SMTPPort, "SMTP"},
	} {
		if err := forwarder.Start(ctx, port.number, port.label, cfg.ForwardTimeout, pollInterval); err != nil {
			forwarder.Stop()
			return nil, err
		}
	}

	return forwarder, nil
}

// terminate asks the bridge to stop.
func terminate(bridge *exec.Cmd) {
	if bridge.Process == nil {
		return
	}

	if err := bridge.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		logf("WARNING: could not signal the bridge: %v", err)
	}
}
