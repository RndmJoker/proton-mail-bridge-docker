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

	// Starts on, which is the bridge's own default for a fresh vault
	// (internal/vault/types_settings.go). Anything else would let a test pass
	// that only ever looks at a value nobody changed.
	automaticUpdate bool

	// setWasCalled separates "never asked" from "asked and it had no effect".
	setWasCalled bool

	// swallowSet makes the setter report success without changing anything,
	// which is what a silently failed vault write looks like from here.
	swallowSet bool

	mailServerSettings *bridgepb.ImapSmtpSettings

	// The three plain switches. Each starts at the bridge's own default for a
	// fresh vault, so a test that never sets one is not looking at a value it
	// happens to agree with.
	//
	// telemetryDisabled is stored the way the interface names it, inverted
	// against the configuration. Keeping the stub honest about that is the
	// point: if the production code and the stub both flipped it, the test
	// would pass with reporting switched on for everyone.
	doh               bool
	allMailVisible    bool
	telemetryDisabled bool

	// swallowSwitch is swallowSet for the three above.
	swallowSwitch bool

	dohErr       error
	dohGetErr    error
	allMailErr   error
	telemetryErr error

	updateErr    error
	updateGetErr error
	settingsErr  error
	versionErr   error
}

func newStub() *stubClient {
	// allMailVisible true and telemetryDisabled false are the bridge's own
	// defaults. Telemetry therefore starts *enabled*, which is exactly the
	// state this container is supposed to change.
	return &stubClient{automaticUpdate: true, allMailVisible: true}
}

func (s *stubClient) SetIsDoHEnabled(_ context.Context, in *wrapperspb.BoolValue, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if s.dohErr != nil {
		return nil, s.dohErr
	}

	if !s.swallowSwitch {
		s.doh = in.GetValue()
	}

	return &emptypb.Empty{}, nil
}

func (s *stubClient) IsDoHEnabled(_ context.Context, _ *emptypb.Empty, _ ...grpc.CallOption) (*wrapperspb.BoolValue, error) {
	if s.dohGetErr != nil {
		return nil, s.dohGetErr
	}

	return wrapperspb.Bool(s.doh), nil
}

func (s *stubClient) SetIsAllMailVisible(_ context.Context, in *wrapperspb.BoolValue, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if s.allMailErr != nil {
		return nil, s.allMailErr
	}

	if !s.swallowSwitch {
		s.allMailVisible = in.GetValue()
	}

	return &emptypb.Empty{}, nil
}

func (s *stubClient) IsAllMailVisible(_ context.Context, _ *emptypb.Empty, _ ...grpc.CallOption) (*wrapperspb.BoolValue, error) {
	return wrapperspb.Bool(s.allMailVisible), nil
}

func (s *stubClient) SetIsTelemetryDisabled(_ context.Context, in *wrapperspb.BoolValue, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if s.telemetryErr != nil {
		return nil, s.telemetryErr
	}

	if !s.swallowSwitch {
		s.telemetryDisabled = in.GetValue()
	}

	return &emptypb.Empty{}, nil
}

func (s *stubClient) IsTelemetryDisabled(_ context.Context, _ *emptypb.Empty, _ ...grpc.CallOption) (*wrapperspb.BoolValue, error) {
	return wrapperspb.Bool(s.telemetryDisabled), nil
}

func (s *stubClient) SetIsAutomaticUpdateOn(_ context.Context, in *wrapperspb.BoolValue, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}

	s.setWasCalled = true

	if !s.swallowSet {
		s.automaticUpdate = in.GetValue()
	}

	return &emptypb.Empty{}, nil
}

func (s *stubClient) IsAutomaticUpdateOn(_ context.Context, _ *emptypb.Empty, _ ...grpc.CallOption) (*wrapperspb.BoolValue, error) {
	if s.updateGetErr != nil {
		return nil, s.updateGetErr
	}

	return wrapperspb.Bool(s.automaticUpdate), nil
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
	client := newStub()

	cfg := config.Config{IMAPPort: 2143, SMTPPort: 2025, IMAPSSL: true, SMTPSSL: false}

	if err := Apply(context.Background(), client, cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !client.setWasCalled {
		t.Fatal("automatic updates were never touched")
	}

	if client.automaticUpdate {
		t.Fatal("automatic updates are still on")
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
	client := newStub()
	client.settingsErr = errors.New("rejected")

	err := Apply(context.Background(), client, config.Config{IMAPPort: 1143, SMTPPort: 1025})
	if err == nil {
		t.Fatal("expected an error, got none")
	}
}

// The call reports success but the setting does not move. Without reading it
// back this passes, the container runs, and the log says automatic updates are
// off while the bridge is free to replace its own binary.
func TestApplyFailsWhenTheSettingDoesNotStick(t *testing.T) {
	client := newStub()
	client.swallowSet = true

	err := Apply(context.Background(), client, config.Config{IMAPPort: 1143, SMTPPort: 1025})
	if err == nil {
		t.Fatal("expected an error, got none")
	}

	if client.mailServerSettings != nil {
		t.Fatal("the settings were applied although the updater is still on")
	}
}

func TestApplyFailsWhenTheSettingCannotBeReadBack(t *testing.T) {
	client := newStub()
	client.updateGetErr = errors.New("unavailable")

	if err := Apply(context.Background(), client, config.Config{IMAPPort: 1143, SMTPPort: 1025}); err == nil {
		t.Fatal("expected an error, got none")
	}
}

func TestAutomaticUpdatesOn(t *testing.T) {
	client := newStub()

	on, err := AutomaticUpdatesOn(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The stub starts where a fresh vault does. If this ever reads false
	// without anything having turned it off, the check below stops measuring.
	if !on {
		t.Fatal("expected the bridge default to be on")
	}

	if err := Apply(context.Background(), client, config.Config{IMAPPort: 1143, SMTPPort: 1025}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if on, err = AutomaticUpdatesOn(context.Background(), client); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if on {
		t.Fatal("still on after Apply")
	}
}

func TestApplyFailsWhenUpdatesCannotBeDisabled(t *testing.T) {
	client := newStub()
	client.updateErr = errors.New("rejected")

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
	version, err := Version(context.Background(), newStub())
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

// TestTelemetryIsInverted is the one that matters most in this file.
//
// The interface offers SetIsTelemetryDisabled, the configuration offers
// BRIDGE_TELEMETRY, and the two mean opposite things. Getting it backwards
// would switch reporting *on* for everyone who left the variable alone, and
// nothing else here would notice: the container would start, the mail would
// flow, and the log would say nothing.
func TestTelemetryIsInverted(t *testing.T) {
	for _, tc := range []struct {
		name         string
		telemetry    bool
		wantDisabled bool
	}{
		{"default off means disabled true", false, true},
		{"explicitly on means disabled false", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newStub()

			if err := Apply(context.Background(), stub, config.Config{Telemetry: tc.telemetry}); err != nil {
				t.Fatalf("Apply: %v", err)
			}

			if stub.telemetryDisabled != tc.wantDisabled {
				t.Errorf("BRIDGE_TELEMETRY=%t left IsTelemetryDisabled=%t, want %t",
					tc.telemetry, stub.telemetryDisabled, tc.wantDisabled)
			}
		})
	}
}

// The default has to be reached without anyone setting anything, because that
// is the case it exists for. A zero Config is what a container with no
// BRIDGE_TELEMETRY in its environment produces.
func TestTelemetryIsOffWithoutBeingAsked(t *testing.T) {
	stub := newStub()

	// The bridge's own default: reporting enabled.
	if stub.telemetryDisabled {
		t.Fatal("the stub starts with telemetry already disabled, so this test cannot fail")
	}

	if err := Apply(context.Background(), stub, config.Config{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !stub.telemetryDisabled {
		t.Error("an empty configuration left telemetry enabled")
	}
}

func TestAlternativeRoutingIsApplied(t *testing.T) {
	for _, want := range []bool{true, false} {
		stub := newStub()
		stub.doh = !want // so that "unchanged" cannot pass

		if err := Apply(context.Background(), stub, config.Config{AlternativeRouting: want}); err != nil {
			t.Fatalf("Apply: %v", err)
		}

		if stub.doh != want {
			t.Errorf("alternative routing is %t, want %t", stub.doh, want)
		}
	}
}

// TestShowAllMailUnsetChangesNothing is the reason ShowAllMail is a pointer.
//
// Nil has to mean "not our business". A default here would overwrite whatever
// was chosen in Proton's own application, the first time this container starts
// against an existing vault, with nobody having asked for it.
func TestShowAllMailUnsetChangesNothing(t *testing.T) {
	for _, existing := range []bool{true, false} {
		stub := newStub()
		stub.allMailVisible = existing

		if err := Apply(context.Background(), stub, config.Config{}); err != nil {
			t.Fatalf("Apply: %v", err)
		}

		if stub.allMailVisible != existing {
			t.Errorf("an unset BRIDGE_SHOW_ALL_MAIL changed All Mail from %t to %t", existing, stub.allMailVisible)
		}
	}
}

func TestShowAllMailIsAppliedWhenSet(t *testing.T) {
	for _, want := range []bool{true, false} {
		stub := newStub()
		stub.allMailVisible = !want

		if err := Apply(context.Background(), stub, config.Config{ShowAllMail: &want}); err != nil {
			t.Fatalf("Apply: %v", err)
		}

		if stub.allMailVisible != want {
			t.Errorf("All Mail is %t, want %t", stub.allMailVisible, want)
		}
	}
}

// A write the bridge accepts and then ignores has to fail here, for the same
// reason the updater check does: the setting lives in the vault, and a silent
// failure would leave a container running for months with the log claiming the
// opposite.
func TestApplyFailsWhenASwitchDoesNotStick(t *testing.T) {
	stub := newStub()
	stub.swallowSwitch = true

	if err := Apply(context.Background(), stub, config.Config{AlternativeRouting: true}); err == nil {
		t.Error("Apply succeeded although the bridge kept none of the switches")
	}
}

func TestApplyFailsWhenASwitchCannotBeSet(t *testing.T) {
	stub := newStub()
	stub.dohErr = errors.New("nope")

	if err := Apply(context.Background(), stub, config.Config{}); err == nil {
		t.Error("Apply succeeded although setting alternative routing failed")
	}
}

func TestApplyFailsWhenASwitchCannotBeReadBack(t *testing.T) {
	stub := newStub()
	stub.dohGetErr = errors.New("nope")

	if err := Apply(context.Background(), stub, config.Config{}); err == nil {
		t.Error("Apply succeeded although the switch could not be read back")
	}
}

// ReadSwitches has to undo the inversion too, or proton-info would report the
// opposite of the truth to somebody checking whether telemetry is off.
func TestReadSwitchesUndoesTheInversion(t *testing.T) {
	stub := newStub()
	stub.telemetryDisabled = true
	stub.doh = true
	stub.allMailVisible = false

	got, err := ReadSwitches(context.Background(), stub)
	if err != nil {
		t.Fatalf("ReadSwitches: %v", err)
	}

	if got.Telemetry {
		t.Error("telemetry reported as on although the bridge has it disabled")
	}

	if !got.AlternativeRouting {
		t.Error("alternative routing reported as off although it is on")
	}

	if got.ShowAllMail {
		t.Error("All Mail reported as visible although it is not")
	}
}
