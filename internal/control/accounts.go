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

// EveryAccountLocked reports whether the bridge named accounts and every one of
// them is locked.
//
// Locked means one of two things and the state cannot tell them apart:
//
//   - The bridge is opening its vault. Every account is locked for a moment on
//     the way to connected, and it ends by itself.
//   - The account waits for its mailbox password. That ends only when somebody
//     types it, so the sign-in page has to come up.
//
// The enum has three values and none of them is "loading", so a single sample
// cannot answer which case this is. What distinguishes them is time: the first
// resolves on its own, the second does not.
//
// Accounts that are signed out are deliberately not covered. A signed-out
// account is a final answer rather than a stage, and waiting for one to become
// connected would delay exactly the case that needs the page most.
func EveryAccountLocked(users *bridgepb.UserListResponse) bool {
	list := users.GetUsers()
	if len(list) == 0 {
		return false
	}

	for _, user := range list {
		if user.GetState() != bridgepb.UserState_LOCKED {
			return false
		}
	}

	return true
}

// WaitForSettled blocks while every account is locked, and reports whether the
// accounts reached a state worth acting on before the timeout ran out.
//
// True means the answer a caller gets now is trustworthy: either something is
// connected, or something is signed out, or there are no accounts at all. False
// means the wait was given up on and every account is still locked, which is
// then taken at face value: a mailbox password is wanted and the page has to
// come up.
//
// This is what #35 was actually about. The first fix distinguished "the bridge
// has not answered yet" from "there are no accounts". What it did not
// distinguish is "an account that is still unlocking" from "an account that
// wants a password", so a restart with a connected account still ran the
// sign-in page for a few seconds and wrote a fresh access token to the log.
// Measured on 2026-08-24 against 0.5.15, four restarts of a container with one
// connected account: windows of 4 s, 5 s, 1 s and 4.167 s, the last at 7 ms
// resolution. Before the first fix it was 6.9 s, so that fix shortened the
// window without closing it.
//
// Errors are retried rather than returned, for the same reason as in
// WaitForAccounts: a call that fails while the bridge is coming up says nothing
// about the accounts.
func WaitForSettled(ctx context.Context, client bridgepb.BridgeClient, timeout, interval time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for {
		users, err := client.GetUserList(ctx, &emptypb.Empty{})
		if err == nil && !EveryAccountLocked(users) {
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
