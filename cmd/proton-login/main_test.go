package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/login"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/setup"
)

// unreachable builds the error the setup client produces when the server has
// gone away, wrapped the same way, so the test exercises errors.Is rather than
// a string.
func unreachable() error {
	return fmt.Errorf("%w at %s: %w", setup.ErrUnreachable, "https://127.0.0.1:8443", errors.New("connection refused"))
}

func TestResolveUnreachable(t *testing.T) {
	original := confirmSignedIn
	t.Cleanup(func() { confirmSignedIn = original })

	t.Run("any other error is passed through untouched", func(t *testing.T) {
		confirmSignedIn = func() (bool, error) {
			t.Fatal("the bridge must not be asked about an error that is not an unreachable server")

			return false, nil
		}

		want := errors.New("the setup server answered 401 Unauthorized")

		status, err := resolveUnreachable(want)
		if !errors.Is(err, want) {
			t.Fatalf("got %v, want the original error", err)
		}

		if status.State != "" {
			t.Fatalf("got state %q, want none", status.State)
		}
	})

	// The case #35 left behind: the sign-in worked and the page shut itself
	// down, which is what made the next call fail.
	t.Run("an unreachable server with an account signed in is success", func(t *testing.T) {
		confirmSignedIn = func() (bool, error) { return true, nil }

		status, err := resolveUnreachable(unreachable())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if status.State != login.StateSucceeded {
			t.Fatalf("got state %q, want %q", status.State, login.StateSucceeded)
		}
	})

	// And the other direction, which is why the bridge is asked at all rather
	// than the disappearance being taken as proof. Announcing a sign-in that
	// did not happen would be worse than the bug being fixed.
	t.Run("an unreachable server with nothing signed in stays an error", func(t *testing.T) {
		confirmSignedIn = func() (bool, error) { return false, nil }

		status, err := resolveUnreachable(unreachable())
		if !errors.Is(err, setup.ErrUnreachable) {
			t.Fatalf("got %v, want the unreachable error back", err)
		}

		if status.State == login.StateSucceeded {
			t.Fatal("a sign-in that did not happen must not be reported as one")
		}
	})

	t.Run("a bridge that cannot be asked says both things", func(t *testing.T) {
		askErr := errors.New("no server config in this container")
		confirmSignedIn = func() (bool, error) { return false, askErr }

		_, err := resolveUnreachable(unreachable())
		if !errors.Is(err, setup.ErrUnreachable) {
			t.Fatalf("got %v, want it to still carry the unreachable error", err)
		}

		if !errors.Is(err, askErr) {
			t.Fatalf("got %v, want it to also carry why the bridge could not answer", err)
		}
	})
}
