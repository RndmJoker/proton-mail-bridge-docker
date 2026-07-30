package control

import (
	"testing"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
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
