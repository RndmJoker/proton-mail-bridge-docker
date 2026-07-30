package control

import (
	"context"
	"errors"
	"testing"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// stubClient records what was asked of it. It embeds the generated interface
// so that any method not overridden here panics rather than silently doing
// nothing, which is what makes an unnoticed extra call show up as a failure.
type stubClient struct {
	bridgepb.BridgeClient

	automaticUpdate    *bool
	mailServerSettings *bridgepb.ImapSmtpSettings

	updateErr   error
	settingsErr error
	versionErr  error
}

func (s *stubClient) SetIsAutomaticUpdateOn(_ context.Context, in *wrapperspb.BoolValue, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}

	value := in.GetValue()
	s.automaticUpdate = &value

	return &emptypb.Empty{}, nil
}

func (s *stubClient) SetMailServerSettings(_ context.Context, in *bridgepb.ImapSmtpSettings, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if s.settingsErr != nil {
		return nil, s.settingsErr
	}

	s.mailServerSettings = in

	return &emptypb.Empty{}, nil
}

func (s *stubClient) Version(_ context.Context, _ *emptypb.Empty, _ ...grpc.CallOption) (*wrapperspb.StringValue, error) {
	if s.versionErr != nil {
		return nil, s.versionErr
	}

	return wrapperspb.String("3.25.0"), nil
}

func TestBridgeArgs(t *testing.T) {
	args := BridgeArgs(config.Config{LogLevel: "debug"})

	var hasGRPC bool

	for _, arg := range args {
		if arg == "--grpc" {
			hasGRPC = true
		}

		// The bridge stops as soon as the process named here disappears.
		// Nothing in this container should be able to cause that.
		if arg == "--parent-pid" {
			t.Fatal("--parent-pid must not be passed")
		}
	}

	if !hasGRPC {
		t.Fatal("--grpc is missing, the bridge would print its help and exit")
	}

	if args[len(args)-1] != "debug" {
		t.Fatalf("the log level did not reach the command line: %v", args)
	}
}

func TestApply(t *testing.T) {
	client := &stubClient{}

	cfg := config.Config{IMAPPort: 2143, SMTPPort: 2025, IMAPSSL: true, SMTPSSL: false}

	if err := Apply(context.Background(), client, cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.automaticUpdate == nil {
		t.Fatal("automatic updates were never touched")
	}

	if *client.automaticUpdate {
		t.Fatal("automatic updates were turned on, not off")
	}

	settings := client.mailServerSettings
	if settings == nil {
		t.Fatal("the mail server settings were never sent")
	}

	if settings.GetImapPort() != 2143 || settings.GetSmtpPort() != 2025 {
		t.Errorf("ports arrived as %d and %d", settings.GetImapPort(), settings.GetSmtpPort())
	}

	if !settings.GetUseSSLForImap() || settings.GetUseSSLForSmtp() {
		t.Errorf("TLS flags arrived as imap=%v smtp=%v", settings.GetUseSSLForImap(), settings.GetUseSSLForSmtp())
	}
}

// If the ports cannot be applied, the container is listening somewhere other
// than it was told to. That has to surface as a failure, not as a log line
// nobody reads.
func TestApplyFailsWhenSettingsAreRejected(t *testing.T) {
	client := &stubClient{settingsErr: errors.New("rejected")}

	err := Apply(context.Background(), client, config.Config{IMAPPort: 1143, SMTPPort: 1025})
	if err == nil {
		t.Fatal("expected an error, got none")
	}
}

func TestApplyFailsWhenUpdatesCannotBeDisabled(t *testing.T) {
	client := &stubClient{updateErr: errors.New("rejected")}

	err := Apply(context.Background(), client, config.Config{IMAPPort: 1143, SMTPPort: 1025})
	if err == nil {
		t.Fatal("expected an error, got none")
	}

	// The order matters: the settings must not have been applied to a bridge
	// whose updater is still on, because that would leave the container in a
	// state the log claims it is not in.
	if client.mailServerSettings != nil {
		t.Fatal("the settings were applied after the update call had already failed")
	}
}

func TestVersion(t *testing.T) {
	version, err := Version(context.Background(), &stubClient{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if version != "3.25.0" {
		t.Fatalf("got %q", version)
	}
}

// The first call after connecting is what turns a lazy connection into a real
// one. If it cannot fail, an unreachable bridge would look like a healthy one
// until the first mail client showed up.
func TestVersionReportsAFailure(t *testing.T) {
	if _, err := Version(context.Background(), &stubClient{versionErr: errors.New("unauthenticated")}); err == nil {
		t.Fatal("expected an error, got none")
	}
}
