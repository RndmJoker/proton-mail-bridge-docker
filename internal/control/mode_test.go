package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// modeStub is a bridge with one account whose mode can be switched. It embeds
// the generated interface so that any other call panics.
type modeStub struct {
	bridgepb.BridgeClient

	user *bridgepb.User

	setCalls int

	// lag is how many reads after the call still show the old mode. The real
	// bridge answers the call first and switches afterwards, so a read straight
	// after it seeing the old value is the normal case, not an error.
	lag int

	// swallow makes the call succeed without the mode ever changing, which is
	// what a failure looks like from outside: the bridge only logs it.
	swallow bool

	// readErrs fail that many reads after the call, while the bridge is busy.
	readErrs int

	pending *bool
}

func (s *modeStub) GetUser(_ context.Context, in *wrapperspb.StringValue, _ ...grpc.CallOption) (*bridgepb.User, error) {
	if in.GetValue() != s.user.GetId() {
		return nil, errors.New("no such user")
	}

	if s.pending != nil {
		if s.readErrs > 0 {
			s.readErrs--
			return nil, errors.New("busy")
		}

		if s.lag > 0 {
			s.lag--
		} else {
			s.user.SplitMode = *s.pending
			s.pending = nil
		}
	}

	// A copy, so a caller holding the old answer does not see it change.
	return &bridgepb.User{
		Id:        s.user.GetId(),
		Username:  s.user.GetUsername(),
		State:     s.user.GetState(),
		SplitMode: s.user.GetSplitMode(),
	}, nil
}

func (s *modeStub) SetUserSplitMode(_ context.Context, in *bridgepb.UserSplitModeRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	s.setCalls++

	if !s.swallow {
		active := in.GetActive()
		s.pending = &active
	}

	return &emptypb.Empty{}, nil
}

func connectedUser() *bridgepb.User {
	return &bridgepb.User{Id: "u1", Username: "someone", State: bridgepb.UserState_CONNECTED}
}

func TestSetSplitModeReadsTheNewModeBack(t *testing.T) {
	stub := &modeStub{user: connectedUser(), lag: 2}

	user, err := SetSplitMode(context.Background(), stub, "u1", true, time.Millisecond)
	if err != nil {
		t.Fatalf("SetSplitMode: %v", err)
	}

	if !user.GetSplitMode() {
		t.Error("the account came back in combined mode")
	}

	if stub.setCalls != 1 {
		t.Errorf("the bridge was asked %d times, want once", stub.setCalls)
	}
}

func TestSetSplitModeSwitchesBack(t *testing.T) {
	user := connectedUser()
	user.SplitMode = true
	stub := &modeStub{user: user}

	got, err := SetSplitMode(context.Background(), stub, "u1", false, time.Millisecond)
	if err != nil {
		t.Fatalf("SetSplitMode: %v", err)
	}

	if got.GetSplitMode() {
		t.Error("the account is still in split mode")
	}
}

// Asking for the mode an account already has must not reach the bridge. The
// bridge would ignore it, but proton-mode prints what happened, and "switched"
// for something that did not move would be a lie.
func TestSetSplitModeLeavesAnAccountThatIsAlreadyThere(t *testing.T) {
	user := connectedUser()
	user.SplitMode = true
	stub := &modeStub{user: user}

	got, err := SetSplitMode(context.Background(), stub, "u1", true, time.Millisecond)
	if err != nil {
		t.Fatalf("SetSplitMode: %v", err)
	}

	if !got.GetSplitMode() {
		t.Error("the account lost its mode")
	}

	if stub.setCalls != 0 {
		t.Error("the bridge was asked to switch an account that already was in that mode")
	}
}

// The bridge switches only accounts it has loaded, and accepts the call for any
// other without doing anything. Refusing here is the only way that becomes a
// message instead of a wait that runs into its timeout.
func TestSetSplitModeRefusesAnAccountThatIsNotConnected(t *testing.T) {
	for _, state := range []bridgepb.UserState{bridgepb.UserState_SIGNED_OUT, bridgepb.UserState_LOCKED} {
		user := connectedUser()
		user.State = state
		stub := &modeStub{user: user}

		_, err := SetSplitMode(context.Background(), stub, "u1", true, time.Millisecond)
		if !errors.Is(err, ErrNotConnected) {
			t.Errorf("%s: got %v, want ErrNotConnected", state, err)
		}

		if stub.setCalls != 0 {
			t.Errorf("%s: the bridge was asked anyway", state)
		}
	}
}

// The failure this read-back exists for. The bridge answers the call, does the
// work afterwards and reports a failure only in its own log. Without reading
// back, this would print "now in split mode" for an account that is not.
func TestSetSplitModeFailsWhenTheModeNeverArrives(t *testing.T) {
	stub := &modeStub{user: connectedUser(), swallow: true}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := SetSplitMode(ctx, stub, "u1", true, time.Millisecond)
	if !errors.Is(err, ErrModeNotApplied) {
		t.Fatalf("got %v, want ErrModeNotApplied", err)
	}
}

// Reads that fail while the bridge rebuilds the mailboxes are part of the
// switch, not a verdict on it.
func TestSetSplitModeRetriesFailedReads(t *testing.T) {
	stub := &modeStub{user: connectedUser(), readErrs: 3}

	user, err := SetSplitMode(context.Background(), stub, "u1", true, time.Millisecond)
	if err != nil {
		t.Fatalf("SetSplitMode: %v", err)
	}

	if !user.GetSplitMode() {
		t.Error("the account came back in combined mode")
	}
}

func TestSetSplitModeFailsWhenTheAccountCannotBeRead(t *testing.T) {
	stub := &modeStub{user: connectedUser()}

	if _, err := SetSplitMode(context.Background(), stub, "nobody", true, time.Millisecond); err == nil {
		t.Fatal("expected an error for an account the bridge does not know")
	}
}
