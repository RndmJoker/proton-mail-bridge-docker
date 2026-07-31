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
