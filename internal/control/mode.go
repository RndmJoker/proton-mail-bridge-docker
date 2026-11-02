package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// ErrModeNotApplied is returned when the bridge accepted the request to switch
// an account's mode and then never reported the new one.
var ErrModeNotApplied = errors.New("the bridge did not report the new mode")

// ErrNotConnected is returned for an account the bridge has not loaded.
var ErrNotConnected = errors.New("the account is not connected")

// SetSplitMode puts an account into split or combined mode and reads the
// result back.
//
// Split mode gives every address of the account a mailbox of its own, with the
// address as the username. It does not give it a password of its own: the
// bridge keeps one bridge password per account and checks every address against
// it (CheckAuth in the upstream internal/services/useridentity/state.go). The
// mode decides what a mail client sees, not what the password opens.
//
// Switching is not free. The bridge drops the account's mailboxes, forgets how
// far it had synced and downloads everything again (setAddressMode in the
// upstream internal/services/imapservice/service.go). That is the reason the
// sign-in asks before the first sync rather than leaving it for later.
//
// Read back rather than trusted, for a reason specific to this call. The bridge
// answers it before doing anything, does the work in the background, and sends
// the event that says it finished whether or not it worked; a failure only
// reaches its own log (SetUserSplitMode in the upstream
// internal/frontend/grpc/service_user.go). That event could not be used anyway:
// the bridge allows one event stream and bridge-control holds it. So the only
// way to know is to ask for the account until it says what was asked for, or
// until ctx gives up.
//
// An account that already is in the requested mode is returned as it is,
// without a call. An account that is not connected is refused: the bridge
// switches only accounts it has loaded, and for any other it would accept the
// call and do nothing.
func SetSplitMode(ctx context.Context, client bridgepb.BridgeClient, userID string, split bool, interval time.Duration) (*bridgepb.User, error) {
	user, err := client.GetUser(ctx, wrapperspb.String(userID))
	if err != nil {
		return nil, fmt.Errorf("could not read the account from the bridge: %w", err)
	}

	if user.GetState() != bridgepb.UserState_CONNECTED {
		return nil, fmt.Errorf("%w: %s is %s", ErrNotConnected, user.GetUsername(), strings.ToLower(user.GetState().String()))
	}

	if user.GetSplitMode() == split {
		return user, nil
	}

	if _, err := client.SetUserSplitMode(ctx, &bridgepb.UserSplitModeRequest{UserID: userID, Active: split}); err != nil {
		return nil, fmt.Errorf("the bridge refused to switch the mode: %w", err)
	}

	for {
		timer := time.NewTimer(interval)

		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("%w: %s is still in %s", ErrModeNotApplied, user.GetUsername(), ModeName(user.GetSplitMode()))
		case <-timer.C:
		}

		// A failed read is retried rather than returned, for the same reason as
		// in WaitForAccounts: the bridge is busy rebuilding the account's
		// mailboxes right now, and a call that fails in the middle of that says
		// nothing about whether the switch worked.
		next, err := client.GetUser(ctx, wrapperspb.String(userID))
		if err != nil {
			continue
		}

		user = next

		if user.GetSplitMode() == split {
			return user, nil
		}
	}
}

// ModeName labels the account mode the way Proton's own settings do.
func ModeName(splitMode bool) string {
	if splitMode {
		return "split mode"
	}

	return "combined mode"
}
