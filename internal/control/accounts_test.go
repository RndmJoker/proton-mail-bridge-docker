package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

func users(states ...bridgepb.UserState) *bridgepb.UserListResponse {
	response := &bridgepb.UserListResponse{}

	for i, state := range states {
		response.Users = append(response.Users, &bridgepb.User{
			Id:       string(rune('a' + i)),
			Username: "someone@example.invalid",
			State:    state,
		})
	}

	return response
}

func TestSignInNeeded(t *testing.T) {
	tests := []struct {
		name string
		list *bridgepb.UserListResponse
		want bool
	}{
		{
			name: "a fresh volume",
			list: users(),
			want: true,
		},
		{
			name: "an account that is connected",
			list: users(bridgepb.UserState_CONNECTED),
			want: false,
		},
		{
			// The case that matters and is easy to get wrong. The bridge signs
			// an account out when the password or the two-factor setup
			// changes, when the session is revoked, after a long time offline
			// or after a failed sync. Counting it as "we have an account"
			// leaves a container that serves nothing with no way back in.
			name: "an account that was signed out again",
			list: users(bridgepb.UserState_SIGNED_OUT),
			want: true,
		},
		{
			// Waiting for the mailbox password, so it is not usable either.
			name: "an account that is locked",
			list: users(bridgepb.UserState_LOCKED),
			want: true,
		},
		{
			name: "one connected among several",
			list: users(bridgepb.UserState_SIGNED_OUT, bridgepb.UserState_CONNECTED),
			want: false,
		},
		{
			name: "several, none connected",
			list: users(bridgepb.UserState_SIGNED_OUT, bridgepb.UserState_LOCKED),
			want: true,
		},
		{
			// A failed call must not read as "everything is fine". nil here
			// means no accounts, which means the page runs.
			name: "no answer at all",
			list: nil,
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SignInNeeded(test.list); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestAccountsReported(t *testing.T) {
	tests := []struct {
		name string
		list *bridgepb.UserListResponse
		want bool
	}{
		{
			// The whole reason this function exists. A bridge that has not
			// finished reading its vault answers exactly like this, and so does
			// one whose vault is genuinely empty.
			name: "nothing said yet, or nothing to say",
			list: users(),
			want: false,
		},
		{
			name: "no answer at all",
			list: nil,
			want: false,
		},
		{
			name: "an account, connected",
			list: users(bridgepb.UserState_CONNECTED),
			want: true,
		},
		{
			// The state does not matter. A signed-out account still proves the
			// vault was read, which is the only question here.
			name: "an account, signed out",
			list: users(bridgepb.UserState_SIGNED_OUT),
			want: true,
		},
		{
			name: "an account, locked",
			list: users(bridgepb.UserState_LOCKED),
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AccountsReported(test.list); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

// TestEmptyListIsAmbiguous is the bug of 2026-07-31 written down as a test.
//
// SignInNeeded says yes for an empty list, correctly: no account is connected.
// What it cannot say is whether that is because there are none or because the
// bridge has not looked yet. Acting on the first answer alone opened the
// sign-in page for seven seconds on every restart of a container that was
// already signed in.
//
// The two functions together are what distinguishes the cases. If a change ever
// makes AccountsReported return true for an empty list, this fails, and it
// should: the distinction would be gone and nothing else would notice.
func TestEmptyListIsAmbiguous(t *testing.T) {
	empty := users()

	if !SignInNeeded(empty) {
		t.Fatal("an empty list should still read as needing a sign-in")
	}

	if AccountsReported(empty) {
		t.Fatal("an empty list must not count as the bridge having answered")
	}

	// And with an account present the pair is unambiguous in both directions.
	one := users(bridgepb.UserState_CONNECTED)

	if SignInNeeded(one) || !AccountsReported(one) {
		t.Fatal("a connected account should be reported and need no sign-in")
	}
}

// listClient answers GetUserList with nothing for the first `emptyFor` calls
// and with one connected account afterwards, which is what a bridge reading
// its vault looks like from the outside.
type listClient struct {
	bridgepb.BridgeClient

	emptyFor int
	calls    int
	err      error
}

func (c *listClient) GetUserList(_ context.Context, _ *emptypb.Empty, _ ...grpc.CallOption) (*bridgepb.UserListResponse, error) {
	c.calls++

	if c.err != nil {
		return nil, c.err
	}

	if c.calls <= c.emptyFor {
		return users(), nil
	}

	return users(bridgepb.UserState_CONNECTED), nil
}

func TestWaitForAccounts(t *testing.T) {
	t.Run("returns as soon as the bridge answers", func(t *testing.T) {
		client := &listClient{emptyFor: 2}

		start := time.Now()
		got := WaitForAccounts(context.Background(), client, time.Minute, time.Millisecond)
		elapsed := time.Since(start)

		if !got {
			t.Fatal("got false, want true")
		}

		// The point of the test: it must not sit out the timeout when the
		// answer arrives early. A minute was allowed and three calls were
		// enough.
		if elapsed > time.Second {
			t.Fatalf("waited %s for an answer that came after three calls", elapsed)
		}

		if client.calls != 3 {
			t.Fatalf("asked %d times, want 3", client.calls)
		}
	})

	t.Run("gives up so that an empty vault stays usable", func(t *testing.T) {
		// Never answers. Without a deadline this is the container that never
		// shows a sign-in page, which is worse than the bug being fixed.
		client := &listClient{emptyFor: 1 << 30}

		if WaitForAccounts(context.Background(), client, 20*time.Millisecond, time.Millisecond) {
			t.Fatal("got true, want false")
		}

		if client.calls < 2 {
			t.Fatalf("asked %d times, want more than one attempt before giving up", client.calls)
		}
	})

	t.Run("keeps trying after an error", func(t *testing.T) {
		// A call that fails while the bridge is coming up says nothing about
		// the vault. Giving up on the first one would reopen the race.
		client := &listClient{err: errors.New("not up yet")}

		if WaitForAccounts(context.Background(), client, 20*time.Millisecond, time.Millisecond) {
			t.Fatal("got true, want false")
		}

		if client.calls < 2 {
			t.Fatalf("asked %d times, want it to retry rather than return on the first error", client.calls)
		}
	})

	t.Run("a cancelled context ends the wait", func(t *testing.T) {
		client := &listClient{emptyFor: 1 << 30}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if WaitForAccounts(ctx, client, time.Hour, time.Millisecond) {
			t.Fatal("got true, want false")
		}
	})
}

func TestEveryAccountLocked(t *testing.T) {
	tests := []struct {
		name string
		list *bridgepb.UserListResponse
		want bool
	}{
		{
			// Nothing to wait for. An empty list is answered by
			// AccountsReported and its timeout, not here.
			name: "no accounts at all",
			list: users(),
			want: false,
		},
		{
			// The case the wait exists for. Either the vault is opening or a
			// mailbox password is wanted, and this cannot tell which.
			name: "one locked account",
			list: users(bridgepb.UserState_LOCKED),
			want: true,
		},
		{
			name: "two locked accounts",
			list: users(bridgepb.UserState_LOCKED, bridgepb.UserState_LOCKED),
			want: true,
		},
		{
			name: "locked next to connected",
			list: users(bridgepb.UserState_LOCKED, bridgepb.UserState_CONNECTED),
			want: false,
		},
		{
			// A signed-out account is a final answer, not a stage. Waiting for
			// it would delay the page for the case that needs it most, so this
			// must not report true.
			name: "locked next to signed out",
			list: users(bridgepb.UserState_LOCKED, bridgepb.UserState_SIGNED_OUT),
			want: false,
		},
		{
			name: "signed out on its own",
			list: users(bridgepb.UserState_SIGNED_OUT),
			want: false,
		},
		{
			name: "connected on its own",
			list: users(bridgepb.UserState_CONNECTED),
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := EveryAccountLocked(test.list); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

// stateClient answers GetUserList with lockedFor locked replies and then with
// whatever `then` holds. That is what a bridge opening its vault looks like
// from the outside: the account is named at once and is locked for a moment
// before it becomes connected.
type stateClient struct {
	bridgepb.BridgeClient

	lockedFor int
	then      bridgepb.UserState
	calls     int
	err       error
}

func (c *stateClient) GetUserList(_ context.Context, _ *emptypb.Empty, _ ...grpc.CallOption) (*bridgepb.UserListResponse, error) {
	c.calls++

	if c.err != nil {
		return nil, c.err
	}

	if c.calls <= c.lockedFor {
		return users(bridgepb.UserState_LOCKED), nil
	}

	return users(c.then), nil
}

// settledWithin runs WaitForSettled with a watchdog.
//
// Every case below that expects the wait to give up would otherwise hang the
// whole package until the test binary's own timeout if it did not, and a test
// that hangs instead of failing is one whose output nobody reads. The leaked
// goroutine is deliberate: the point is to fail here, not to tidy up after a
// function that is already broken.
func settledWithin(t *testing.T, client bridgepb.BridgeClient, timeout, interval, watchdog time.Duration) bool {
	t.Helper()

	done := make(chan bool, 1)

	go func() {
		done <- WaitForSettled(context.Background(), client, timeout, interval)
	}()

	select {
	case got := <-done:
		return got
	case <-time.After(watchdog):
		t.Fatalf("WaitForSettled did not return within %s, so it never gives up", watchdog)

		return false
	}
}

func TestWaitForSettled(t *testing.T) {
	t.Run("waits out a lock that resolves itself", func(t *testing.T) {
		client := &stateClient{lockedFor: 3, then: bridgepb.UserState_CONNECTED}

		start := time.Now()
		got := WaitForSettled(context.Background(), client, time.Minute, time.Millisecond)
		elapsed := time.Since(start)

		if !got {
			t.Fatal("got false, want true")
		}

		if elapsed > time.Second {
			t.Fatalf("waited %s for a lock that cleared after four calls", elapsed)
		}

		if client.calls != 4 {
			t.Fatalf("asked %d times, want 4", client.calls)
		}
	})

	t.Run("gives up so a mailbox password can still be typed", func(t *testing.T) {
		// Locked forever, which is what an account waiting for its mailbox
		// password looks like. Without the deadline this is a container that
		// never shows a sign-in page, and that is worse than the bug.
		client := &stateClient{lockedFor: 1 << 30}

		if settledWithin(t, client, 20*time.Millisecond, time.Millisecond, 2*time.Second) {
			t.Fatal("got true, want false")
		}

		if client.calls < 2 {
			t.Fatalf("asked %d times, want more than one attempt before giving up", client.calls)
		}
	})

	t.Run("does not wait for a signed-out account", func(t *testing.T) {
		client := &stateClient{then: bridgepb.UserState_SIGNED_OUT}

		// A short deadline on purpose. If the function does wait here after all,
		// this has to fail rather than hang.
		if !WaitForSettled(context.Background(), client, 20*time.Millisecond, time.Millisecond) {
			t.Fatal("got false, want true")
		}

		if client.calls != 1 {
			t.Fatalf("asked %d times, want 1: a signed-out account is an answer", client.calls)
		}
	})

	t.Run("does not wait when there are no accounts", func(t *testing.T) {
		client := &listClient{emptyFor: 1 << 30}

		if !WaitForSettled(context.Background(), client, 20*time.Millisecond, time.Millisecond) {
			t.Fatal("got false, want true")
		}

		if client.calls != 1 {
			t.Fatalf("asked %d times, want 1", client.calls)
		}
	})

	t.Run("keeps trying after an error", func(t *testing.T) {
		client := &stateClient{err: errors.New("not up yet")}

		if settledWithin(t, client, 20*time.Millisecond, time.Millisecond, 2*time.Second) {
			t.Fatal("got true, want false")
		}

		if client.calls < 2 {
			t.Fatalf("asked %d times, want it to retry rather than return on the first error", client.calls)
		}
	})

	t.Run("a cancelled context ends the wait", func(t *testing.T) {
		client := &stateClient{lockedFor: 1 << 30}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if WaitForSettled(ctx, client, time.Hour, time.Millisecond) {
			t.Fatal("got true, want false")
		}
	})
}

// TestLockedAtStartupIsAmbiguous is #35 written down, the second time.
//
// A single sample of a locked account says "a sign-in is needed", and that is
// correct for an account waiting for its mailbox password and wrong for one
// that is a moment away from being connected. Removing the wait puts the
// sign-in page back on every restart of a container that has nothing to sign
// in, which is what this must fail for.
func TestLockedAtStartupIsAmbiguous(t *testing.T) {
	locked := users(bridgepb.UserState_LOCKED)

	if !SignInNeeded(locked) {
		t.Fatal("a locked account should read as needing a sign-in from one sample")
	}

	if !AccountsReported(locked) {
		t.Fatal("a locked account has been named, so it counts as reported")
	}

	if !EveryAccountLocked(locked) {
		t.Fatal("the ambiguity has to be visible to the caller")
	}

	// And the way out is time, not a better look at the same sample.
	client := &stateClient{lockedFor: 2, then: bridgepb.UserState_CONNECTED}

	if !WaitForSettled(context.Background(), client, time.Minute, time.Millisecond) {
		t.Fatal("the wait should have seen the account settle")
	}

	needed, reported, err := AccountsNeedSignIn(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if needed || !reported {
		t.Fatal("after the wait the account is connected, so no sign-in is needed")
	}
}
