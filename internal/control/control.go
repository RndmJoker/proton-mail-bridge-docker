// Package control holds the steps bridge-control performs against a running
// bridge, separated from the process handling in main so they can be tested
// against a stub instead of a real bridge.
package control

import (
	"context"
	"errors"
	"fmt"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/config"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// BridgeArgs builds the command line for the bridge core.
//
// --grpc is what makes it start the interface this program talks to. Without
// it the bridge prints its help and exits.
//
// --parent-pid is deliberately absent. Passing it makes the bridge watch that
// process and stop as soon as it disappears, which is right for the desktop
// launcher and wrong here: the bridge has to outlive nothing in particular,
// and a mistaken value would shut it down at a moment nobody can explain.
func BridgeArgs(cfg config.Config) []string {
	return []string{"--grpc", "--log-level", cfg.LogLevel}
}

// Apply pushes the container's configuration into a running bridge.
//
// Every step is fatal on failure. A bridge that is running but listening
// somewhere other than it was told to is worse than one that did not start:
// the mail client connects to nothing, and there is no error anywhere to
// explain why.
func Apply(ctx context.Context, client bridgepb.BridgeClient, cfg config.Config) error {
	if err := disableAutomaticUpdates(ctx, client); err != nil {
		return err
	}

	return setMailServerSettings(ctx, client, cfg)
}

// disableAutomaticUpdates turns off the bridge's own updater and reads the
// setting back.
//
// A new image is the only intended way to update this container. A bridge that
// replaces its own binary makes the image worthless as a record of what is
// running, and the launcher that would carry out such an update is not even
// in the image. Leaving the setting on would mean it downloads updates it can
// never apply.
//
// The read-back is the point. Sending the call and assuming it worked would
// make a running container proof of nothing: the setting lives in the vault,
// and a write that failed silently would leave the updater on for the whole
// life of the container with the log claiming otherwise.
func disableAutomaticUpdates(ctx context.Context, client bridgepb.BridgeClient) error {
	if _, err := client.SetIsAutomaticUpdateOn(ctx, wrapperspb.Bool(false)); err != nil {
		return fmt.Errorf("could not turn off automatic updates: %w", err)
	}

	state, err := client.IsAutomaticUpdateOn(ctx, &emptypb.Empty{})
	if err != nil {
		return fmt.Errorf("could not read back the automatic update setting: %w", err)
	}

	if state.GetValue() {
		return errors.New("automatic updates are still on after being turned off; the bridge would replace its own binary")
	}

	return nil
}

// AutomaticUpdatesOn reports the current setting.
//
// Read from the bridge rather than remembered from the call above, so that it
// still says the truth after a restart, when nothing in this process set it.
func AutomaticUpdatesOn(ctx context.Context, client bridgepb.BridgeClient) (bool, error) {
	state, err := client.IsAutomaticUpdateOn(ctx, &emptypb.Empty{})
	if err != nil {
		return false, fmt.Errorf("could not read the automatic update setting: %w", err)
	}

	return state.GetValue(), nil
}

// setMailServerSettings applies the ports and the TLS mode.
//
// All four values go in one call because that is the shape of the interface:
// there is no way to set only the IMAP port. Sending the current value for
// something that has not changed is what the bridge's own window does too, and
// the bridge ignores a setting that already matches.
func setMailServerSettings(ctx context.Context, client bridgepb.BridgeClient, cfg config.Config) error {
	settings := &bridgepb.ImapSmtpSettings{
		ImapPort:      int32(cfg.IMAPPort), //nolint:gosec // validated to 1024-65535 in config
		SmtpPort:      int32(cfg.SMTPPort), //nolint:gosec // validated to 1024-65535 in config
		UseSSLForImap: cfg.IMAPSSL,
		UseSSLForSmtp: cfg.SMTPSSL,
	}

	if _, err := client.SetMailServerSettings(ctx, settings); err != nil {
		return fmt.Errorf("could not set the mail server settings: %w", err)
	}

	return nil
}

// Version asks the bridge what it is.
//
// Used as the first call after connecting, because grpc.NewClient connects
// lazily: until something is actually sent, a wrong token or an unreachable
// socket looks exactly like a working connection.
func Version(ctx context.Context, client bridgepb.BridgeClient) (string, error) {
	version, err := client.Version(ctx, &emptypb.Empty{})
	if err != nil {
		return "", fmt.Errorf("the bridge did not answer: %w", err)
	}

	return version.GetValue(), nil
}
