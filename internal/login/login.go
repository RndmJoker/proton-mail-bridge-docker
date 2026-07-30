// Package login drives a Proton sign-in through the bridge's gRPC interface.
//
// The calls themselves return immediately and do nothing visible. Everything
// that happens afterwards, whether a second factor is needed, whether the
// account has two passwords, whether Proton wants a human to solve a
// challenge, and whether any of it worked, arrives on the event stream. This
// package is the piece that turns that stream into a state a caller can act
// on.
//
// It holds no password longer than the call that sends it. The bridge keeps
// what it needs; there is no reason for a second copy to sit in memory here
// while a person types a six-digit code.
package login

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
)

// State is where a sign-in currently stands.
type State string

const (
	// StateIdle means no sign-in is in progress.
	StateIdle State = "idle"

	// StateAwaitingCredentials means a sign-in was started and the bridge has
	// not said anything about it yet.
	StateAwaitingCredentials State = "awaiting_credentials"

	// StateAwaitingTOTP means Proton wants a six-digit code.
	StateAwaitingTOTP State = "awaiting_totp"

	// StateAwaitingMailboxPassword means the account has a separate mailbox
	// password, which Proton calls two-password mode.
	StateAwaitingMailboxPassword State = "awaiting_mailbox_password"

	// StateAwaitingHumanVerification means Proton wants a challenge solved in
	// a browser. The URL is in Status.
	StateAwaitingHumanVerification State = "awaiting_human_verification"

	// StateSucceeded means the account is signed in.
	StateSucceeded State = "succeeded"

	// StateFailed means the attempt ended. Status carries the reason.
	StateFailed State = "failed"
)

// Status is a snapshot of a sign-in.
//
// It deliberately carries no password, no token and no user ID: it is built to
// be shown to a person and to travel through an HTTP response.
type Status struct {
	State    State  `json:"state"`
	Username string `json:"username,omitempty"`

	// HumanVerificationURL is set when State is
	// StateAwaitingHumanVerification. It has to be opened in a browser, by a
	// person, on any machine.
	HumanVerificationURL string `json:"humanVerificationUrl,omitempty"`

	// Message explains a failure in words meant for whoever is signing in.
	Message string `json:"message,omitempty"`

	// Retryable says whether starting over can help. A wrong password can be
	// retried; a free Proton plan cannot.
	Retryable bool `json:"retryable,omitempty"`
}

// ErrNotExpected is returned when a step arrives that does not fit the current
// state, for example a code submitted when nothing asked for one.
var ErrNotExpected = errors.New("that step is not what the bridge is waiting for")

// Session is one sign-in at a time.
//
// One at a time is not a simplification: the bridge keeps a single set of
// authentication state for a sign-in in progress, so a second concurrent
// attempt would overwrite the first one's.
type Session struct {
	client bridgepb.BridgeClient

	mu       sync.Mutex
	status   Status
	username string

	// humanVerified records that a challenge was solved, so the retry can ask
	// the bridge to use the details it stored.
	humanVerified bool
}

// New returns a Session that talks to client.
func New(client bridgepb.BridgeClient) *Session {
	return &Session{
		client: client,
		status: Status{State: StateIdle},
	}
}

// Status returns the current state.
func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.status
}

// encode is how the bridge wants secrets on this interface.
//
// Not documented in bridge.proto, where the field is a plain `bytes password`.
// The service base64-decodes it on the way in (service_methods.go), so a
// client that sends the raw bytes gets an authentication failure and no hint
// as to why.
func encode(secret []byte) []byte {
	out := make([]byte, base64.StdEncoding.EncodedLen(len(secret)))
	base64.StdEncoding.Encode(out, secret)

	return out
}

// Start begins a sign-in.
//
// The password is used for this call and not kept. Callers should clear their
// own copy afterwards.
func (s *Session) Start(ctx context.Context, username string, password []byte) error {
	s.mu.Lock()

	// A previous attempt that ended does not block a new one; one that is
	// still waiting for something does, because the bridge would lose track of
	// the first.
	switch s.status.State {
	case StateAwaitingCredentials, StateAwaitingTOTP, StateAwaitingMailboxPassword:
		s.mu.Unlock()
		return fmt.Errorf("%w: a sign-in for %q is still in progress", ErrNotExpected, s.status.Username)
	case StateIdle, StateAwaitingHumanVerification, StateSucceeded, StateFailed:
	}

	useHV := s.humanVerified
	s.username = username
	s.status = Status{State: StateAwaitingCredentials, Username: username}
	s.mu.Unlock()

	request := &bridgepb.LoginRequest{
		Username: username,
		Password: encode(password),
	}

	// Only set when a challenge was actually solved. Sending it otherwise
	// makes the bridge look for details it does not have.
	if useHV {
		request.UseHvDetails = &useHV
	}

	if _, err := s.client.Login(ctx, request); err != nil {
		s.fail("could not reach the bridge to start the sign-in", false)
		return err
	}

	return nil
}

// SubmitTOTP answers a request for a two-factor code.
func (s *Session) SubmitTOTP(ctx context.Context, code []byte) error {
	s.mu.Lock()

	if s.status.State != StateAwaitingTOTP {
		defer s.mu.Unlock()
		return fmt.Errorf("%w: no two-factor code was requested", ErrNotExpected)
	}

	username := s.username
	s.status.State = StateAwaitingCredentials
	s.mu.Unlock()

	if _, err := s.client.Login2FA(ctx, &bridgepb.LoginRequest{
		Username: username,
		Password: encode(code),
	}); err != nil {
		s.fail("could not reach the bridge to send the two-factor code", false)
		return err
	}

	return nil
}

// SubmitMailboxPassword answers a request for the second password.
//
// The bridge allows three attempts and aborts the sign-in after that, which
// arrives here as a failure like any other.
func (s *Session) SubmitMailboxPassword(ctx context.Context, password []byte) error {
	s.mu.Lock()

	if s.status.State != StateAwaitingMailboxPassword {
		defer s.mu.Unlock()
		return fmt.Errorf("%w: no mailbox password was requested", ErrNotExpected)
	}

	username := s.username
	s.status.State = StateAwaitingCredentials
	s.mu.Unlock()

	if _, err := s.client.Login2Passwords(ctx, &bridgepb.LoginRequest{
		Username: username,
		Password: encode(password),
	}); err != nil {
		s.fail("could not reach the bridge to send the mailbox password", false)
		return err
	}

	return nil
}

// Abort gives up on the current sign-in.
func (s *Session) Abort(ctx context.Context) error {
	s.mu.Lock()
	username := s.username
	s.status = Status{State: StateIdle}
	s.humanVerified = false
	s.mu.Unlock()

	if username == "" {
		return nil
	}

	_, err := s.client.LoginAbort(ctx, &bridgepb.LoginAbortRequest{Username: username})

	return err
}

// Reset returns the session to idle without telling the bridge anything.
// Used after a finished sign-in, where there is nothing left to abort.
func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.status = Status{State: StateIdle}
	s.username = ""
	s.humanVerified = false
}

func (s *Session) fail(message string, retryable bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.status = Status{
		State:     StateFailed,
		Username:  s.username,
		Message:   message,
		Retryable: retryable,
	}
}

// Handle feeds one event from the bridge's stream into the session.
//
// Events that have nothing to do with signing in are ignored, so a caller can
// pass the whole stream through without filtering it first.
func (s *Session) Handle(event *bridgepb.StreamEvent) {
	loginEvent := event.GetLogin()
	if loginEvent == nil {
		return
	}

	switch {
	case loginEvent.GetError() != nil:
		message, retryable := describeError(loginEvent.GetError())
		s.fail(message, retryable)

	case loginEvent.GetTfaRequested() != nil:
		s.await(StateAwaitingTOTP)

	// The bridge offers both when the account has a security key and TOTP.
	// A container has no security key, so this is the code path.
	case loginEvent.GetTfaOrFidoRequested() != nil:
		s.await(StateAwaitingTOTP)

	// A security key and nothing else. There is no way to satisfy this from a
	// container: it needs hardware attached to the machine running the bridge.
	// Saying so beats leaving the sign-in hanging on a prompt that will never
	// be answered.
	case loginEvent.GetFidoRequested() != nil:
		s.fail("this account requires a security key, which cannot be used from a container. Add TOTP as a second factor in your Proton account settings, or sign in with the desktop application.", false)

	case loginEvent.GetTwoPasswordRequested() != nil:
		s.await(StateAwaitingMailboxPassword)

	case loginEvent.GetHvRequested() != nil:
		s.mu.Lock()
		s.status = Status{
			State:                StateAwaitingHumanVerification,
			Username:             s.username,
			HumanVerificationURL: loginEvent.GetHvRequested().GetHvUrl(),
		}
		s.humanVerified = true
		s.mu.Unlock()

	case loginEvent.GetFinished() != nil, loginEvent.GetAlreadyLoggedIn() != nil:
		s.mu.Lock()
		s.status = Status{State: StateSucceeded, Username: s.username}
		s.humanVerified = false
		s.mu.Unlock()
	}
}

func (s *Session) await(state State) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.status = Status{State: state, Username: s.username}
}

// describeError turns a login error into something worth showing, and says
// whether trying again could help.
//
// The bridge's own message is passed through only where it adds something.
// For a wrong password it is empty, and for the rest it is written for a
// desktop window.
func describeError(event *bridgepb.LoginErrorEvent) (string, bool) {
	switch event.GetType() {
	case bridgepb.LoginErrorType_USERNAME_PASSWORD_ERROR:
		if message := event.GetMessage(); message != "" {
			return message, true
		}

		return "wrong username or password", true

	case bridgepb.LoginErrorType_FREE_USER:
		return "the bridge needs a paid Proton plan; it is not part of the free tier", false

	case bridgepb.LoginErrorType_CONNECTION_ERROR:
		return "could not reach Proton", true

	case bridgepb.LoginErrorType_TFA_ERROR:
		return "that two-factor code was not accepted", true

	case bridgepb.LoginErrorType_TFA_ABORT:
		return "the two-factor step was abandoned", true

	case bridgepb.LoginErrorType_TWO_PASSWORDS_ERROR:
		return "that mailbox password was not accepted", true

	case bridgepb.LoginErrorType_TWO_PASSWORDS_ABORT:
		return "too many attempts at the mailbox password, the sign-in was abandoned", true

	case bridgepb.LoginErrorType_HV_ERROR:
		return "the human verification did not go through, start again", true

	case bridgepb.LoginErrorType_FIDO_PIN_INVALID,
		bridgepb.LoginErrorType_FIDO_PIN_BLOCKED,
		bridgepb.LoginErrorType_FIDO_ERROR:
		return "this account requires a security key, which cannot be used from a container", false

	default:
		if message := event.GetMessage(); message != "" {
			return message, true
		}

		return "the sign-in failed", true
	}
}
