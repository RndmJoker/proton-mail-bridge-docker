package control

import (
	"context"
	"fmt"
	"time"

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
//
// The case this function cannot see is the one that caused #35: an empty list
// means "no accounts" here, and just after the bridge starts it also means "the
// vault is not loaded yet". Both look identical from the outside. Callers that
// run at startup have to ask AccountsReported first.
func SignInNeeded(users *bridgepb.UserListResponse) bool {
	for _, user := range users.GetUsers() {
		if user.GetState() == bridgepb.UserState_CONNECTED {
			return false
		}
	}

	return true
}

// AccountsReported reports whether the bridge has named any account at all.
//
// False is not an answer, it is the absence of one. The bridge answers gRPC
// calls before it has finished reading its vault, so for the first seconds of
// its life every account it has is invisible and GetUserList returns nothing.
//
// Nothing in the event stream announces that the vault is loaded. Its events
// cover the app, logins, updates, the disk cache, the mail server settings, the
// keychain, mail and users, and none of them fires for a vault that turns out
// to be empty. So a caller can wait for this to become true, but it has to give
// up eventually, and the giving up is what makes an empty vault usable.
func AccountsReported(users *bridgepb.UserListResponse) bool {
	return len(users.GetUsers()) > 0
}

// AccountsNeedSignIn asks the bridge and applies the rule above.
//
// The second return value is AccountsReported for the same answer, so that a
// caller can tell "no account is connected" from "the bridge has not said yet".
func AccountsNeedSignIn(ctx context.Context, client bridgepb.BridgeClient) (needed, reported bool, err error) {
	users, err := client.GetUserList(ctx, &emptypb.Empty{})
	if err != nil {
		return false, false, fmt.Errorf("could not read the account list: %w", err)
	}

	return SignInNeeded(users), AccountsReported(users), nil
}

// WaitForAccounts blocks until the bridge names an account, and reports
// whether it did before the timeout ran out.
//
// False means the wait was given up on, not that the vault is empty. Those
// cannot be told apart from here, which is the whole difficulty: a bridge that
// is still reading its vault and one with nothing in it answer identically.
// What the timeout buys is that the first case stops being mistaken for the
// second for the few seconds it lasts.
//
// Errors are retried rather than returned. A call that fails while the bridge
// is still coming up says nothing about the vault, and giving up on the first
// one would reintroduce exactly the race this exists to close.
func WaitForAccounts(ctx context.Context, client bridgepb.BridgeClient, timeout, interval time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for {
		if _, reported, err := AccountsNeedSignIn(ctx, client); err == nil && reported {
			return true
		}

		if !time.Now().Before(deadline) {
			return false
		}

		select {
		case <-ctx.Done():
			return false
		case <-time.After(interval):
		}
	}
}
