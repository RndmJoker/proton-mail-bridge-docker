package login

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// stubClient records the login calls. It embeds the generated interface so
// that any other method panics rather than quietly doing nothing.
type stubClient struct {
	bridgepb.BridgeClient

	login          *bridgepb.LoginRequest
	twoFA          *bridgepb.LoginRequest
	twoPasswords   *bridgepb.LoginRequest
	aborted        *bridgepb.LoginAbortRequest
	loginCallCount int

	err error
}

func (s *stubClient) Login(_ context.Context, in *bridgepb.LoginRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if s.err != nil {
		return nil, s.err
	}

	s.login = in
	s.loginCallCount++

	return &emptypb.Empty{}, nil
}

func (s *stubClient) Login2FA(_ context.Context, in *bridgepb.LoginRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if s.err != nil {
		return nil, s.err
	}

	s.twoFA = in

	return &emptypb.Empty{}, nil
}

func (s *stubClient) Login2Passwords(_ context.Context, in *bridgepb.LoginRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if s.err != nil {
		return nil, s.err
	}

	s.twoPasswords = in

	return &emptypb.Empty{}, nil
}

func (s *stubClient) LoginAbort(_ context.Context, in *bridgepb.LoginAbortRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if s.err != nil {
		return nil, s.err
	}

	s.aborted = in

	return &emptypb.Empty{}, nil
}

// Events as the bridge builds them, so the tests exercise the same shapes the
// stream delivers.
func loginEvent(inner *bridgepb.LoginEvent) *bridgepb.StreamEvent {
	return &bridgepb.StreamEvent{Event: &bridgepb.StreamEvent_Login{Login: inner}}
}

func errorEvent(kind bridgepb.LoginErrorType, message string) *bridgepb.StreamEvent {
	return loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_Error{
			Error: &bridgepb.LoginErrorEvent{Type: kind, Message: message},
		},
	})
}

// The interface takes `bytes password` and says nothing more. The service
// base64-decodes it, so anything else fails authentication with no hint as to
// why. This is the check that keeps that knowledge from getting lost.
func TestPasswordIsBase64Encoded(t *testing.T) {
	client := &stubClient{}
	session := New(client)

	const password = "correct horse battery staple"

	if err := session.Start(context.Background(), "someone@example.invalid", []byte(password)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sent := client.login.GetPassword()

	if string(sent) == password {
		t.Fatal("the password went out unencoded, the bridge would reject it")
	}

	decoded, err := base64.StdEncoding.DecodeString(string(sent))
	if err != nil {
		t.Fatalf("what was sent is not base64: %v", err)
	}

	if string(decoded) != password {
		t.Fatalf("decoded to %q", decoded)
	}
}

func TestStartSetsUsernameAndState(t *testing.T) {
	client := &stubClient{}
	session := New(client)

	if err := session.Start(context.Background(), "someone@example.invalid", []byte("pw")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := session.Status().State; got != StateAwaitingCredentials {
		t.Fatalf("state is %q", got)
	}

	if client.login.GetUsername() != "someone@example.invalid" {
		t.Fatalf("username arrived as %q", client.login.GetUsername())
	}

	// Sending it unasked makes the bridge look for verification details it
	// does not have.
	if client.login.UseHvDetails != nil {
		t.Fatal("human verification details were claimed on a first attempt")
	}
}

func TestHappyPath(t *testing.T) {
	session := New(&stubClient{})

	if err := session.Start(context.Background(), "someone@example.invalid", []byte("pw")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_Finished{Finished: &bridgepb.LoginFinishedEvent{UserID: "u"}},
	}))

	if got := session.Status().State; got != StateSucceeded {
		t.Fatalf("state is %q", got)
	}
}

// An account that is already signed in is not a failure. It is the state the
// caller wanted.
func TestAlreadyLoggedInCountsAsSuccess(t *testing.T) {
	session := New(&stubClient{})
	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"))

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_AlreadyLoggedIn{AlreadyLoggedIn: &bridgepb.LoginFinishedEvent{UserID: "u"}},
	}))

	if got := session.Status().State; got != StateSucceeded {
		t.Fatalf("state is %q", got)
	}
}

func TestTOTPFlow(t *testing.T) {
	client := &stubClient{}
	session := New(client)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"))

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_TfaRequested{
			TfaRequested: &bridgepb.LoginTfaRequestedEvent{Username: "someone@example.invalid"},
		},
	}))

	if got := session.Status().State; got != StateAwaitingTOTP {
		t.Fatalf("state is %q", got)
	}

	if err := session.SubmitTOTP(context.Background(), []byte("123456")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(string(client.twoFA.GetPassword()))
	if err != nil {
		t.Fatalf("the code is not base64: %v", err)
	}

	if string(decoded) != "123456" {
		t.Fatalf("code arrived as %q", decoded)
	}

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_Finished{Finished: &bridgepb.LoginFinishedEvent{UserID: "u"}},
	}))

	if got := session.Status().State; got != StateSucceeded {
		t.Fatalf("state is %q", got)
	}
}

// The bridge sends this when the account has both a security key and TOTP.
// A container has no security key, so the code is the only way through and
// this has to land in the same state as a plain TOTP request.
func TestTfaOrFidoAsksForACode(t *testing.T) {
	session := New(&stubClient{})
	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"))

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_TfaOrFidoRequested{
			TfaOrFidoRequested: &bridgepb.LoginTfaOrFidoRequestedEvent{Username: "someone@example.invalid"},
		},
	}))

	if got := session.Status().State; got != StateAwaitingTOTP {
		t.Fatalf("state is %q", got)
	}
}

// A security key and nothing else cannot be satisfied from a container. It has
// to end the attempt with an explanation rather than wait for an answer nobody
// can give.
func TestFidoOnlyFailsWithAnExplanation(t *testing.T) {
	session := New(&stubClient{})
	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"))

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_FidoRequested{
			FidoRequested: &bridgepb.LoginFidoRequestedEvent{Username: "someone@example.invalid"},
		},
	}))

	status := session.Status()

	if status.State != StateFailed {
		t.Fatalf("state is %q", status.State)
	}

	if status.Retryable {
		t.Fatal("marked retryable, but trying again cannot help")
	}

	if status.Message == "" {
		t.Fatal("no explanation")
	}
}

func TestTwoPasswordFlow(t *testing.T) {
	client := &stubClient{}
	session := New(client)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"))

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_TwoPasswordRequested{
			TwoPasswordRequested: &bridgepb.LoginTwoPasswordsRequestedEvent{Username: "someone@example.invalid"},
		},
	}))

	if got := session.Status().State; got != StateAwaitingMailboxPassword {
		t.Fatalf("state is %q", got)
	}

	if err := session.SubmitMailboxPassword(context.Background(), []byte("mailbox")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.twoPasswords == nil {
		t.Fatal("the mailbox password was never sent")
	}
}

// Proton asks for a challenge to be solved in a browser. The retry has to tell
// the bridge to use the details it stored, or it starts from scratch and asks
// again.
func TestHumanVerification(t *testing.T) {
	client := &stubClient{}
	session := New(client)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"))

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_HvRequested{
			HvRequested: &bridgepb.LoginHvRequestedEvent{HvUrl: "https://verify.example.invalid/challenge"},
		},
	}))

	status := session.Status()

	if status.State != StateAwaitingHumanVerification {
		t.Fatalf("state is %q", status.State)
	}

	if status.HumanVerificationURL != "https://verify.example.invalid/challenge" {
		t.Fatalf("url is %q", status.HumanVerificationURL)
	}

	// Starting again is allowed from this state, and only from here does the
	// retry claim the stored details.
	if err := session.Start(context.Background(), "someone@example.invalid", []byte("pw")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.login.UseHvDetails == nil || !client.login.GetUseHvDetails() {
		t.Fatal("the retry did not ask the bridge to use the verification details")
	}
}

func TestErrorsAreExplained(t *testing.T) {
	tests := []struct {
		name          string
		kind          bridgepb.LoginErrorType
		message       string
		wantRetryable bool
	}{
		{name: "wrong password", kind: bridgepb.LoginErrorType_USERNAME_PASSWORD_ERROR, wantRetryable: true},
		{name: "free plan", kind: bridgepb.LoginErrorType_FREE_USER, wantRetryable: false},
		{name: "no connection", kind: bridgepb.LoginErrorType_CONNECTION_ERROR, wantRetryable: true},
		{name: "bad code", kind: bridgepb.LoginErrorType_TFA_ERROR, wantRetryable: true},
		{name: "bad mailbox password", kind: bridgepb.LoginErrorType_TWO_PASSWORDS_ERROR, wantRetryable: true},
		{name: "too many attempts", kind: bridgepb.LoginErrorType_TWO_PASSWORDS_ABORT, wantRetryable: true},
		{name: "verification failed", kind: bridgepb.LoginErrorType_HV_ERROR, wantRetryable: true},
		{name: "security key", kind: bridgepb.LoginErrorType_FIDO_ERROR, wantRetryable: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := New(&stubClient{})
			_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"))

			session.Handle(errorEvent(test.kind, test.message))

			status := session.Status()

			if status.State != StateFailed {
				t.Fatalf("state is %q", status.State)
			}

			if status.Message == "" {
				t.Fatal("no explanation")
			}

			if status.Retryable != test.wantRetryable {
				t.Fatalf("retryable is %v, want %v", status.Retryable, test.wantRetryable)
			}
		})
	}
}

// A free account is the one failure where trying again is pointless, and the
// message has to say what to do instead rather than repeat "failed".
func TestFreePlanIsExplainedNotJustRejected(t *testing.T) {
	session := New(&stubClient{})
	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"))

	session.Handle(errorEvent(bridgepb.LoginErrorType_FREE_USER, ""))

	status := session.Status()

	if status.Retryable {
		t.Fatal("marked retryable")
	}

	if status.Message == "the sign-in failed" {
		t.Fatal("fell through to the generic message")
	}
}

func TestStepsOutOfOrderAreRefused(t *testing.T) {
	session := New(&stubClient{})

	if err := session.SubmitTOTP(context.Background(), []byte("123456")); !errors.Is(err, ErrNotExpected) {
		t.Fatalf("expected ErrNotExpected, got %v", err)
	}

	if err := session.SubmitMailboxPassword(context.Background(), []byte("pw")); !errors.Is(err, ErrNotExpected) {
		t.Fatalf("expected ErrNotExpected, got %v", err)
	}
}

// The bridge keeps one set of authentication state. A second attempt started
// while the first is still waiting would overwrite it, and both would end in
// something inexplicable.
func TestASecondSignInIsRefusedWhileOneIsWaiting(t *testing.T) {
	client := &stubClient{}
	session := New(client)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"))

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_TfaRequested{
			TfaRequested: &bridgepb.LoginTfaRequestedEvent{Username: "someone@example.invalid"},
		},
	}))

	if err := session.Start(context.Background(), "other@example.invalid", []byte("pw")); !errors.Is(err, ErrNotExpected) {
		t.Fatalf("expected ErrNotExpected, got %v", err)
	}

	if client.loginCallCount != 1 {
		t.Fatalf("the bridge saw %d login calls", client.loginCallCount)
	}
}

// A failed attempt has to be retryable without anything being reset by hand,
// because that is what a wrong password looks like.
func TestStartAgainAfterAFailure(t *testing.T) {
	client := &stubClient{}
	session := New(client)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("wrong"))
	session.Handle(errorEvent(bridgepb.LoginErrorType_USERNAME_PASSWORD_ERROR, ""))

	if err := session.Start(context.Background(), "someone@example.invalid", []byte("right")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.loginCallCount != 2 {
		t.Fatalf("the bridge saw %d login calls", client.loginCallCount)
	}
}

func TestAbort(t *testing.T) {
	client := &stubClient{}
	session := New(client)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"))

	if err := session.Abort(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.aborted.GetUsername() != "someone@example.invalid" {
		t.Fatalf("aborted %q", client.aborted.GetUsername())
	}

	if got := session.Status().State; got != StateIdle {
		t.Fatalf("state is %q", got)
	}
}

// Nothing was started, so there is nothing for the bridge to abort. Sending an
// abort for an empty username would be a call about nobody.
func TestAbortWithoutASignIn(t *testing.T) {
	client := &stubClient{}
	session := New(client)

	if err := session.Abort(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.aborted != nil {
		t.Fatal("an abort was sent for nobody")
	}
}

// Events about anything else share the stream. Reacting to them would move the
// sign-in on something that has nothing to do with it.
func TestUnrelatedEventsAreIgnored(t *testing.T) {
	session := New(&stubClient{})
	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"))

	session.Handle(&bridgepb.StreamEvent{
		Event: &bridgepb.StreamEvent_Mail{
			Mail: &bridgepb.MailEvent{
				Event: &bridgepb.MailEvent_AddressChanged{
					AddressChanged: &bridgepb.AddressChangedEvent{Address: "someone@example.invalid"},
				},
			},
		},
	})

	if got := session.Status().State; got != StateAwaitingCredentials {
		t.Fatalf("an unrelated event moved the state to %q", got)
	}
}

func TestStartReportsATransportFailure(t *testing.T) {
	client := &stubClient{err: errors.New("unavailable")}
	session := New(client)

	if err := session.Start(context.Background(), "someone@example.invalid", []byte("pw")); err == nil {
		t.Fatal("expected an error, got none")
	}

	if got := session.Status().State; got != StateFailed {
		t.Fatalf("state is %q", got)
	}
}
