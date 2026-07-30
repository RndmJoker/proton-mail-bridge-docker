package control

import (
	"context"
	"fmt"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
	"google.golang.org/protobuf/types/known/emptypb"
)

// SignInNeeded reports whether the sign-in page has to be running.
//
// The rule the security notes state as "it runs only while it is needed". It
// is a function rather than a line inside a loop so that it can be tested
// against every account state the bridge has, including the two that are easy
// to get wrong.
//
// Needed whenever no account is connected:
//
//   - No accounts at all. The obvious case, a fresh volume.
//   - An account that is signed out. It exists in the vault, but the bridge
//     signs one out when the password or the two-factor setup changes, when
//     the session is revoked, after a long time offline, or after a failed
//     sync. Treating that as "we have an account" would leave a container that
//     serves nothing and offers no way back in.
//   - An account that is locked, which is waiting for the mailbox password.
func SignInNeeded(users *bridgepb.UserListResponse) bool {
	for _, user := range users.GetUsers() {
		if user.GetState() == bridgepb.UserState_CONNECTED {
			return false
		}
	}

	return true
}

// AccountsNeedSignIn asks the bridge and applies the rule above.
func AccountsNeedSignIn(ctx context.Context, client bridgepb.BridgeClient) (bool, error) {
	users, err := client.GetUserList(ctx, &emptypb.Empty{})
	if err != nil {
		return false, fmt.Errorf("could not read the account list: %w", err)
	}

	return SignInNeeded(users), nil
}
